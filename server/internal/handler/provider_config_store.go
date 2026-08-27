package handler

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// providerConfigRow holds the active provider configuration for a runtime.
type providerConfigRow struct {
	ID           pgtype.UUID
	Name         string
	ProviderType string
	BaseURL      string
	APIKey       string
	Model        string
}

const getRuntimeActiveProviderConfigSQL = `
SELECT pc.id, pc.name, pc.provider_type, pc.base_url, pc.api_key, pc.model
FROM agent_runtime ar
JOIN provider_configs pc ON ar.active_provider_config_id = pc.id
WHERE ar.id = $1`

// getRuntimeActiveProviderConfig fetches the active provider config for a runtime.
// Returns nil, nil when no active config is set.
func (h *Handler) getRuntimeActiveProviderConfig(ctx context.Context, runtimeID pgtype.UUID) (*providerConfigRow, error) {
	row := h.DB.QueryRow(ctx, getRuntimeActiveProviderConfigSQL, runtimeID)
	var r providerConfigRow
	err := row.Scan(&r.ID, &r.Name, &r.ProviderType, &r.BaseURL, &r.APIKey, &r.Model)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		slog.Warn("get_runtime_active_provider_config failed",
			"runtime_id", uuidToString(runtimeID), "error", err)
		return nil, nil
	}
	return &r, nil
}

const setRuntimeActiveProviderConfigSQL = `
UPDATE agent_runtime
SET active_provider_config_id = $2
WHERE id = $1`

// setRuntimeActiveProviderConfig sets (or clears when configID is nil) the active
// provider config for a runtime.
func (h *Handler) setRuntimeActiveProviderConfig(ctx context.Context, runtimeID pgtype.UUID, configID *pgtype.UUID) error {
	var arg any
	if configID != nil {
		arg = *configID
	}
	_, err := h.DB.Exec(ctx, setRuntimeActiveProviderConfigSQL, runtimeID, arg)
	return err
}
