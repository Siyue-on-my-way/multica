-- name: SetIssueExecutionContext :one
-- Writes the issue's structured execution context (SIY-177). Passing NULL
-- clears it. The workspace_id filter is the authorization gate — the handler
-- resolves the issue first so this is also the tenant check. The version is
-- carried inside the JSON itself (service bumps it before the write) so the
-- claim path can report "issue_id + version" provenance without a second
-- column. A context write is an authored issue edit: revision and
-- last_activity_at move with it, exactly like a description edit does.
UPDATE issue SET
    execution_context = sqlc.narg('execution_context')::jsonb,
    revision = revision + 1,
    last_activity_at = GREATEST(COALESCE(last_activity_at, updated_at), now()),
    updated_at = now()
WHERE id = @id AND workspace_id = @workspace_id
RETURNING *;
