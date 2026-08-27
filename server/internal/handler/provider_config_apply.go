package handler

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// ApplyProviderConfigRequest is the payload for POST /api/runtimes/{runtimeId}/apply-provider-config.
type ApplyProviderConfigRequest struct {
	Name         string `json:"name"`
	ProviderType string `json:"provider_type"`
	BaseURL      string `json:"base_url"`
	APIKey       string `json:"api_key"`
	Model        string `json:"model"`
}

const createProviderConfigSQL = `
INSERT INTO provider_configs (workspace_id, name, provider_type, base_url, api_key, model)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING id`

// HandleApplyProviderConfig persists a provider config and links it to the runtime,
// then notifies the daemon via a pending_work hint.
func (h *Handler) HandleApplyProviderConfig(w http.ResponseWriter, r *http.Request) {
	runtimeID := chi.URLParam(r, "runtimeId")
	if runtimeID == "" {
		writeError(w, http.StatusBadRequest, "missing runtime ID")
		return
	}

	var req ApplyProviderConfigRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.BaseURL == "" {
		writeError(w, http.StatusBadRequest, "base_url is required")
		return
	}
	if req.ProviderType == "" {
		req.ProviderType = "codex"
	}
	if req.Name == "" {
		req.Name = req.ProviderType
	}

	runtimeUUID, ok := parseUUIDOrBadRequest(w, runtimeID, "runtime_id")
	if !ok {
		return
	}

	rt, err := h.Queries.GetAgentRuntime(r.Context(), runtimeUUID)
	if err != nil {
		if err == pgx.ErrNoRows {
			writeError(w, http.StatusNotFound, "runtime not found")
			return
		}
		slog.Error("get_agent_runtime failed", "error", err, "runtime_id", runtimeID)
		writeError(w, http.StatusInternalServerError, "failed to load runtime")
		return
	}

	if _, ok := h.requireWorkspaceMember(w, r, uuidToString(rt.WorkspaceID), "runtime not found"); !ok {
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var configID pgtype.UUID
	row := h.DB.QueryRow(ctx, createProviderConfigSQL,
		rt.WorkspaceID, req.Name, req.ProviderType, req.BaseURL, req.APIKey, req.Model,
	)
	if err := row.Scan(&configID); err != nil {
		slog.Error("create_provider_config failed", "error", err, "runtime_id", runtimeID)
		writeError(w, http.StatusInternalServerError, "failed to save provider config")
		return
	}

	if err := h.setRuntimeActiveProviderConfig(ctx, rt.ID, &configID); err != nil {
		slog.Error("set_runtime_active_provider_config failed", "error", err, "runtime_id", runtimeID)
		writeError(w, http.StatusInternalServerError, "failed to link provider config")
		return
	}

	if h.DaemonHub != nil {
		h.DaemonHub.NotifyPendingWork(runtimeID, protocol.PendingWorkKindProviderConfig)
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"status":    "dispatched",
		"config_id": uuidToString(configID),
	})
}
