-- name: CreateReportTemplate :one
INSERT INTO report_templates (
    workspace_id, name, period_type, system_prompt
) VALUES (
    $1, $2, $3, $4
) RETURNING *;

-- name: ListReportTemplates :many
SELECT * FROM report_templates
WHERE (workspace_id = $1 OR workspace_id IS NULL)
  AND (period_type = $2 OR $2 = '')
ORDER BY workspace_id NULLS FIRST, name ASC;

-- name: GetReportTemplate :one
SELECT * FROM report_templates
WHERE id = $1 AND (workspace_id = $2 OR workspace_id IS NULL);
