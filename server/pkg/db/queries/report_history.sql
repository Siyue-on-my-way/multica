-- name: CreateReportHistory :one
INSERT INTO report_history (
    workspace_id, project_id, period_type, range_start, range_end, timezone,
    generated_by_type, generated_by_id, data_snapshot, content
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10
) RETURNING *;

-- name: GetReportHistoryInWorkspace :one
SELECT * FROM report_history
WHERE id = $1 AND workspace_id = $2;

-- name: ListReportHistoryByProject :many
SELECT * FROM report_history
WHERE workspace_id = $1 AND project_id = $2
ORDER BY range_start DESC, created_at DESC, id DESC
LIMIT $3 OFFSET $4;

-- name: DeleteReportHistoryByProject :exec
DELETE FROM report_history
WHERE workspace_id = $1 AND project_id = $2;

-- name: UpdateReportHistoryWorkspaceByProject :exec
UPDATE report_history SET workspace_id = $1
WHERE workspace_id = $2 AND project_id = $3;
