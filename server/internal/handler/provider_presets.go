package handler

import (
	"net/http"

	"github.com/multica-ai/multica/server/internal/agentconfig"
)

// HandleGetProviderPresets returns the list of built-in provider presets.
func HandleGetProviderPresets(w http.ResponseWriter, r *http.Request) {
	presets := agentconfig.GetProviderPresets()
	writeJSON(w, http.StatusOK, presets)
}
