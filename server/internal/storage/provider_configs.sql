-- name: CreateProviderConfig :one
INSERT INTO provider_configs (
    workspace_id,
    name,
    provider_type,
    base_url,
    api_key,
    model
) VALUES (
    $1, $2, $3, $4, $5, $6
)
RETURNING *;

-- name: GetProviderConfig :one
SELECT * FROM provider_configs
WHERE id = $1 AND workspace_id = $2;

-- name: ListProviderConfigs :many
SELECT * FROM provider_configs
WHERE workspace_id = $1
ORDER BY created_at DESC;

-- name: UpdateProviderConfig :one
UPDATE provider_configs
SET
    name = COALESCE(sqlc.narg('name'), name),
    provider_type = COALESCE(sqlc.narg('provider_type'), provider_type),
    base_url = COALESCE(sqlc.narg('base_url'), base_url),
    api_key = COALESCE(sqlc.narg('api_key'), api_key),
    model = COALESCE(sqlc.narg('model'), model),
    updated_at = NOW()
WHERE id = sqlc.arg('id') AND workspace_id = sqlc.arg('workspace_id')
RETURNING *;

-- name: DeleteProviderConfig :exec
DELETE FROM provider_configs
WHERE id = $1 AND workspace_id = $2;

-- name: SetRuntimeActiveProviderConfig :exec
UPDATE agent_runtime
SET active_provider_config_id = $2
WHERE id = $1 AND workspace_id = $3;
