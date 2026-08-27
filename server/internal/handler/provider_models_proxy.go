package handler

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// HandleProxyProviderModels proxies GET {baseUrl}/models to avoid browser mixed-content
// and CORS restrictions. The caller passes base_url and api_key as query parameters.
func HandleProxyProviderModels(w http.ResponseWriter, r *http.Request) {
	baseURL := strings.TrimRight(r.URL.Query().Get("base_url"), "/")
	apiKey := r.URL.Query().Get("api_key")

	if baseURL == "" {
		writeError(w, http.StatusBadRequest, "base_url is required")
		return
	}

	target := baseURL + "/models"

	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, target, nil)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid base_url: %v", err))
		return
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		writeError(w, http.StatusBadGateway, fmt.Sprintf("failed to reach provider: %v", err))
		return
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1 MiB cap
	if err != nil {
		writeError(w, http.StatusBadGateway, "failed to read provider response")
		return
	}

	if resp.StatusCode != http.StatusOK {
		// Forward the provider's error as-is
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(body)
		return
	}

	// Validate it's parseable JSON before forwarding
	var parsed json.RawMessage
	if err := json.Unmarshal(body, &parsed); err != nil {
		writeError(w, http.StatusBadGateway, "provider returned non-JSON response")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}
