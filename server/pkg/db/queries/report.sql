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

-- name: ListProjectReportTimeline :many
-- Return one bounded, tenant-scoped event stream for every issue that was
-- active in the requested half-open window. The active issue set is the
-- union of status transitions, comments, and agent task execution records;
-- each source is read in one batch and grouped by issue in the service layer.
-- A reply whose root predates the window contributes that root as an
-- out-of-window context row so the model and UI can understand the thread.
WITH RECURSIVE scoped_issues AS (
    SELECT i.id,
           i.workspace_id,
           i.number,
           i.title,
           COALESCE(i.description, '') AS description,
           i.status,
           i.due_date,
           w.issue_prefix
    FROM issue i
    JOIN workspace w ON w.id = i.workspace_id
    WHERE i.workspace_id = sqlc.arg('workspace_id')
      AND i.project_id = sqlc.arg('project_id')
), active_issue_ids AS (
    SELECT h.issue_id
    FROM issue_status_history h
    JOIN scoped_issues i ON i.id = h.issue_id
    WHERE h.workspace_id = sqlc.arg('workspace_id')
      AND h.changed_at >= sqlc.arg('range_start')
      AND h.changed_at < sqlc.arg('range_end')
    UNION
    SELECT c.issue_id
    FROM comment c
    JOIN scoped_issues i ON i.id = c.issue_id
    WHERE c.workspace_id = sqlc.arg('workspace_id')
      AND c.created_at >= sqlc.arg('range_start')
      AND c.created_at < sqlc.arg('range_end')
    UNION
    SELECT t.issue_id
    FROM agent_task_queue t
    JOIN scoped_issues i ON i.id = t.issue_id
    WHERE (
        (t.created_at >= sqlc.arg('range_start') AND t.created_at < sqlc.arg('range_end'))
        OR (t.started_at >= sqlc.arg('range_start') AND t.started_at < sqlc.arg('range_end'))
        OR (t.completed_at >= sqlc.arg('range_start') AND t.completed_at < sqlc.arg('range_end'))
    )
), window_comments AS (
    SELECT c.*
    FROM comment c
    JOIN active_issue_ids a ON a.issue_id = c.issue_id
    WHERE c.workspace_id = sqlc.arg('workspace_id')
      AND c.created_at >= sqlc.arg('range_start')
      AND c.created_at < sqlc.arg('range_end')
), comment_ancestors(reply_id, issue_id, current_id, parent_id) AS (
    SELECT c.id, c.issue_id, c.id, c.parent_id
    FROM window_comments c
    UNION ALL
    SELECT a.reply_id, a.issue_id, parent.id, parent.parent_id
    FROM comment_ancestors a
    JOIN comment parent ON parent.id = a.parent_id
    WHERE parent.issue_id = a.issue_id
      AND parent.workspace_id = sqlc.arg('workspace_id')
), context_comment_ids AS (
    SELECT DISTINCT a.current_id AS comment_id
    FROM comment_ancestors a
    JOIN comment root ON root.id = a.current_id
    WHERE a.parent_id IS NULL
      AND root.created_at < sqlc.arg('range_start')
), timeline AS (
    SELECT c.issue_id,
           'comment'::text AS event_type,
           c.id::text AS event_id,
           c.created_at AS occurred_at,
           TRUE AS in_range,
           c.author_type::text AS actor_type,
           c.author_id::text AS actor_id,
           c.content::text AS content,
           c.type::text AS comment_type,
           COALESCE(c.parent_id::text, '') AS parent_id,
           ''::text AS action,
           '{}'::jsonb AS details
    FROM window_comments c
    UNION ALL
    SELECT c.issue_id,
           'comment'::text,
           c.id::text,
           c.created_at,
           FALSE,
           c.author_type::text,
           c.author_id::text,
           c.content::text,
           c.type::text,
           COALESCE(c.parent_id::text, ''),
           ''::text,
           '{}'::jsonb
    FROM comment c
    JOIN context_comment_ids context ON context.comment_id = c.id
    UNION ALL
    SELECT a.issue_id,
           'activity_log'::text,
           a.id::text,
           a.created_at,
           TRUE,
           COALESCE(a.actor_type::text, ''),
           COALESCE(a.actor_id::text, ''),
           ''::text,
           ''::text,
           ''::text,
           a.action::text,
           COALESCE(a.details, '{}'::jsonb)
    FROM activity_log a
    JOIN active_issue_ids active ON active.issue_id = a.issue_id
    WHERE a.workspace_id = sqlc.arg('workspace_id')
      AND a.created_at >= sqlc.arg('range_start')
      AND a.created_at < sqlc.arg('range_end')
    UNION ALL
    SELECT h.issue_id,
           'issue_status_history'::text,
           h.id::text,
           h.changed_at,
           TRUE,
           h.changed_by_type::text,
           COALESCE(h.changed_by_id::text, ''),
           ''::text,
           ''::text,
           ''::text,
           'status_changed'::text,
           jsonb_build_object(
               'from_status', h.from_status,
               'to_status', h.to_status,
               'changed_by_type', h.changed_by_type,
               'changed_by_id', h.changed_by_id
           )
    FROM issue_status_history h
    JOIN active_issue_ids active ON active.issue_id = h.issue_id
    WHERE h.workspace_id = sqlc.arg('workspace_id')
      AND h.changed_at >= sqlc.arg('range_start')
      AND h.changed_at < sqlc.arg('range_end')
    UNION ALL
    SELECT t.issue_id,
           'agent_task_queue'::text,
           t.id::text,
           CASE
               WHEN t.completed_at >= sqlc.arg('range_start') AND t.completed_at < sqlc.arg('range_end') THEN t.completed_at
               WHEN t.started_at >= sqlc.arg('range_start') AND t.started_at < sqlc.arg('range_end') THEN t.started_at
               WHEN t.created_at >= sqlc.arg('range_start') AND t.created_at < sqlc.arg('range_end') THEN t.created_at
               WHEN t.dispatched_at >= sqlc.arg('range_start') AND t.dispatched_at < sqlc.arg('range_end') THEN t.dispatched_at
               ELSE COALESCE(t.completed_at, t.started_at, t.dispatched_at, t.created_at)
           END,
           TRUE,
           'agent'::text,
           t.agent_id::text,
           ''::text,
           ''::text,
           ''::text,
           'agent_task'::text,
           jsonb_build_object(
               'task_id', t.id,
               'status', t.status,
               'created_at', t.created_at,
               'started_at', t.started_at,
               'completed_at', t.completed_at,
               'error', t.error,
               'result', COALESCE(t.result, 'null'::jsonb)
           )
    FROM agent_task_queue t
    JOIN active_issue_ids active ON active.issue_id = t.issue_id
    WHERE (
        (t.created_at >= sqlc.arg('range_start') AND t.created_at < sqlc.arg('range_end'))
        OR (t.started_at >= sqlc.arg('range_start') AND t.started_at < sqlc.arg('range_end'))
        OR (t.completed_at >= sqlc.arg('range_start') AND t.completed_at < sqlc.arg('range_end'))
    )
)
SELECT i.id AS issue_id,
       i.number,
       i.title,
       i.description,
       i.status,
       i.due_date,
       i.issue_prefix,
       timeline.event_type,
       timeline.event_id,
       timeline.occurred_at,
       timeline.in_range,
       timeline.actor_type,
       timeline.actor_id,
       timeline.content,
       timeline.comment_type,
       CAST(timeline.parent_id AS text) AS parent_id,
       timeline.action,
       timeline.details
FROM scoped_issues i
JOIN active_issue_ids active ON active.issue_id = i.id
JOIN timeline ON timeline.issue_id = i.id
ORDER BY i.number ASC, timeline.occurred_at ASC, timeline.event_type ASC, timeline.event_id ASC;

-- name: ListCurrentProjectIssueStatesForReport :many
-- Kept as a single batch for the legacy overview counters. The issue-centered
-- report itself only uses ListProjectReportTimeline's activity union.
SELECT i.id, i.number, i.title, i.status, i.due_date, w.issue_prefix
FROM issue i
JOIN workspace w ON w.id = i.workspace_id
WHERE i.workspace_id = sqlc.arg('workspace_id')
  AND i.project_id = sqlc.arg('project_id')
ORDER BY i.number ASC;
