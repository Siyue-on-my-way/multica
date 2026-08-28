-- name: CreateReportHistory :one
INSERT INTO report_history (
    workspace_id, project_id, period_type, range_start, range_end, timezone,
    generated_by_type, generated_by_id, data_snapshot, content
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10
) RETURNING *;

-- name: GetReportForJob :one
SELECT *
FROM report_history
WHERE id = $1 AND project_id = $2;

-- name: ListProjectReports :many
SELECT id, workspace_id, project_id, period_type, range_start, range_end,
       timezone, generated_by_type, generated_by_id, created_at, saved_at
FROM report_history
WHERE workspace_id = $1 AND project_id = $2 AND content <> '' AND saved_at IS NOT NULL
ORDER BY created_at DESC, id DESC
LIMIT 100;

-- name: GetProjectReport :one
SELECT *
FROM report_history
WHERE workspace_id = $1 AND project_id = $2 AND id = $3
  AND content <> '' AND saved_at IS NOT NULL;

-- name: SaveProjectReport :one
UPDATE report_history
SET saved_at = now()
WHERE workspace_id = $1
  AND project_id = $2
  AND id = $3
  AND content <> ''
  AND saved_at IS NULL
RETURNING *;

-- name: GetReport :one
SELECT *
FROM report_history
WHERE id = $1;

-- name: UpdateReportHistoryGeneration :one
UPDATE report_history
SET data_snapshot = $2,
    content = $3
WHERE id = $1 AND content = ''
RETURNING *;

-- name: GetReportJobExecution :one
SELECT status, attempt, max_attempts, error_msg
FROM sys_cron_executions
WHERE job_name = $1
  AND scope_kind = $2
  AND scope_id = $3
  AND plan_time = $4;

-- name: ListIssuesCompletedForReport :many
SELECT i.id, i.number, i.title
FROM issue i
WHERE i.project_id = $1
  AND i.status = 'done'
  AND EXISTS (
      SELECT 1
      FROM issue_status_history h
      WHERE h.issue_id = i.id
        AND h.to_status = 'done'
        AND h.changed_at >= sqlc.arg('range_start')
        AND h.changed_at < sqlc.arg('range_end')
  )
ORDER BY i.number ASC;

-- name: ListIssuesByStatusForReport :many
SELECT id, number, title
FROM issue
WHERE project_id = $1 AND status = $2
ORDER BY number ASC;

-- name: ListIssuesCancelledForReport :many
SELECT i.id, i.number, i.title
FROM issue i
WHERE i.project_id = $1
  AND i.status = 'cancelled'
  AND EXISTS (
      SELECT 1
      FROM issue_status_history h
      WHERE h.issue_id = i.id
        AND h.to_status = 'cancelled'
        AND h.changed_at >= sqlc.arg('range_start')
        AND h.changed_at < sqlc.arg('range_end')
  )
ORDER BY i.number ASC;

-- name: ListIssuesOverdueForReport :many
SELECT id, number, title
FROM issue
WHERE project_id = $1
  AND due_date IS NOT NULL
  AND due_date < $2
  AND status NOT IN ('done', 'cancelled')
ORDER BY due_date ASC, number ASC;
