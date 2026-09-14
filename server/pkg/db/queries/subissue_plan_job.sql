-- name: CreateSubissuePlanJob :one
-- ON CONFLICT DO NOTHING is intentional: the idempotency key is the
-- serialization point for repeated clicks and concurrent browser tabs.
INSERT INTO subissue_plan_job (
    id, workspace_id, issue_id, source_comment_id, source_snapshot,
    source_snapshot_digest, context_revision, human_constraints,
    primary_error_class, fallback_reason, idempotency_key, expires_at
) VALUES (
    @id, @workspace_id, @issue_id, sqlc.narg('source_comment_id'),
    @source_snapshot::jsonb, @source_snapshot_digest, @context_revision,
    @human_constraints, @primary_error_class, @fallback_reason,
    @idempotency_key, COALESCE(sqlc.narg('expires_at'), now() + interval '15 minutes')
)
ON CONFLICT (workspace_id, issue_id, idempotency_key) DO NOTHING
RETURNING *;

-- name: GetSubissuePlanJob :one
SELECT * FROM subissue_plan_job
WHERE id = @id AND workspace_id = @workspace_id;

-- name: GetSubissuePlanJobByIdempotencyKey :one
SELECT * FROM subissue_plan_job
WHERE workspace_id = @workspace_id
  AND issue_id = @issue_id
  AND idempotency_key = @idempotency_key;

-- name: SetSubissuePlanJobTask :one
UPDATE subissue_plan_job
SET agent_id = @agent_id,
    task_id = @task_id,
    status = 'queued',
    updated_at = now()
WHERE id = @id AND workspace_id = @workspace_id
  AND status IN ('queued', 'running')
RETURNING *;

-- name: ClaimSubissuePlanJobDispatch :one
-- Serialize the enqueue side of idempotent browser retries. The caller holds
-- this short-lived dispatch lease while it selects at most two candidates and
-- either attaches one task or completes the deterministic draft.
UPDATE subissue_plan_job
SET status = 'running',
    started_at = COALESCE(started_at, now()),
    updated_at = now()
WHERE id = @id AND workspace_id = @workspace_id
  AND status = 'queued'
  AND task_id IS NULL
RETURNING *;

-- name: RetrySubissuePlanJob :one
-- An explicit user retry reuses the immutable source snapshot and
-- idempotency key, but clears the previous candidate task/result so the
-- dispatcher can select a fresh available runtime.
UPDATE subissue_plan_job
SET agent_id = NULL,
    task_id = NULL,
    status = 'queued',
    result = NULL,
    error_summary = NULL,
    started_at = NULL,
    completed_at = NULL,
    expires_at = now() + interval '15 minutes',
    updated_at = now()
WHERE id = @id AND workspace_id = @workspace_id
  AND status IN ('failed', 'cancelled', 'expired')
RETURNING *;

-- name: StartSubissuePlanJob :one
UPDATE subissue_plan_job
SET status = 'running',
    started_at = COALESCE(started_at, now()),
    updated_at = now()
WHERE id = @id
  AND status = 'queued'
RETURNING *;

-- name: CompleteSubissuePlanJob :one
UPDATE subissue_plan_job
SET status = @status,
    result = sqlc.narg('result')::jsonb,
    error_summary = sqlc.narg('error_summary'),
    completed_at = now(),
    updated_at = now()
WHERE id = @id
  AND status IN ('queued', 'running')
RETURNING *;

-- name: CancelSubissuePlanJob :one
UPDATE subissue_plan_job
SET status = 'cancelled', completed_at = now(), updated_at = now()
WHERE id = @id
  AND workspace_id = @workspace_id
  AND status IN ('queued', 'running')
RETURNING *;

-- name: ExpireSubissuePlanJobs :many
UPDATE subissue_plan_job
SET status = 'expired',
    error_summary = COALESCE(error_summary, 'planning job timed out'),
    completed_at = COALESCE(completed_at, now()),
    updated_at = now()
WHERE status IN ('queued', 'running')
  AND expires_at <= now()
RETURNING *;

-- name: ListSubissuePlanAgentCandidates :many
-- Walk the current issue, its ancestors, then descendants.  Each issue is
-- reduced to its latest successful terminal run before the caller deduplicates
-- agents, so a stale run can never outrank a newer successful run elsewhere.
WITH RECURSIVE
ancestors AS (
    SELECT i.id AS issue_id, i.parent_issue_id, i.project_id,
           1::int AS layer, 1::int AS distance,
           ARRAY[i.id]::uuid[] AS path
    FROM issue i
    WHERE i.id = @issue_id::uuid AND i.workspace_id = @workspace_id::uuid
    UNION ALL
    SELECT p.id, p.parent_issue_id, p.project_id,
           1::int, a.distance + 1,
           a.path || p.id
    FROM issue p
    JOIN ancestors a ON p.id = a.parent_issue_id
    WHERE p.workspace_id = @workspace_id::uuid
      AND a.distance < 32
      AND NOT p.id = ANY(a.path)
),
descendants AS (
    SELECT i.id AS issue_id, i.parent_issue_id, i.project_id,
           2::int AS layer, 1::int AS distance,
           ARRAY[i.id]::uuid[] AS path
    FROM issue i
    WHERE i.parent_issue_id = @issue_id::uuid
      AND i.workspace_id = @workspace_id::uuid
    UNION ALL
    SELECT c.id, c.parent_issue_id, c.project_id,
           2::int, d.distance + 1,
           d.path || c.id
    FROM issue c
    JOIN descendants d ON c.parent_issue_id = d.issue_id
    WHERE c.workspace_id = @workspace_id::uuid
      AND d.distance < 32
      AND NOT c.id = ANY(d.path)
),
nodes AS (
    SELECT i.id AS issue_id, i.project_id, 0::int AS layer,
           0::int AS distance
    FROM issue i
    WHERE i.id = @issue_id::uuid AND i.workspace_id = @workspace_id::uuid
    UNION ALL
    SELECT issue_id, project_id, layer, distance FROM ancestors
    WHERE issue_id <> @issue_id::uuid
    UNION ALL
    SELECT issue_id, project_id, layer, distance FROM descendants
),
ranked AS (
    SELECT n.issue_id, n.project_id, n.layer, n.distance,
           t.id AS task_id, t.agent_id, t.runtime_id, t.completed_at,
           ROW_NUMBER() OVER (
               PARTITION BY n.issue_id
               ORDER BY t.completed_at DESC, t.id DESC
           ) AS issue_run_rank
    FROM nodes n
    JOIN agent_task_queue t ON t.issue_id = n.issue_id
       AND t.status = 'completed'
       AND t.completed_at IS NOT NULL
    JOIN agent a ON a.id = t.agent_id
       AND a.workspace_id = @workspace_id::uuid
       AND a.archived_at IS NULL
       AND a.kind = 'user'
       AND a.runtime_id = t.runtime_id
       AND a.status NOT IN ('offline', 'error')
    JOIN agent_runtime r ON r.id = t.runtime_id
       AND r.workspace_id = @workspace_id::uuid
       AND r.status = 'online'
    WHERE NOT EXISTS (
        SELECT 1 FROM agent_task_queue auth_failure
        WHERE auth_failure.agent_id = t.agent_id
          AND auth_failure.status = 'failed'
          AND auth_failure.completed_at >= now() - interval '15 minutes'
          AND (
              lower(COALESCE(auth_failure.failure_reason, '')) LIKE '%auth%'
              OR lower(COALESCE(auth_failure.failure_reason, '')) LIKE '%credential%'
              OR lower(COALESCE(auth_failure.error, '')) LIKE '%unauthorized%'
              OR lower(COALESCE(auth_failure.error, '')) LIKE '%invalid api key%'
          )
    )
)
SELECT issue_id, project_id, layer, distance, task_id, agent_id, runtime_id,
       completed_at,
       (project_id IS NOT DISTINCT FROM sqlc.narg('project_id')::uuid) AS project_match
FROM ranked
WHERE issue_run_rank = 1
ORDER BY project_match DESC, layer ASC, distance ASC, completed_at DESC, task_id DESC;

-- name: ListSubissuePlanContextIssues :many
-- Same bounded hierarchy walk as the candidate query, but returns full issue
-- rows for the immutable read-only snapshot sent to a plan-only task. The
-- previous version joined on parent_issue_id = related.issue_id, which only
-- walked descendants and silently omitted the ancestor context promised by
-- the fallback contract.
WITH RECURSIVE ancestors AS (
    SELECT i.id AS issue_id, i.parent_issue_id, ARRAY[i.id]::uuid[] AS path, 0::int AS distance
    FROM issue i
    WHERE i.id = @issue_id::uuid AND i.workspace_id = @workspace_id::uuid
    UNION ALL
    SELECT p.id, p.parent_issue_id, a.path || p.id, a.distance + 1
    FROM issue p JOIN ancestors a ON p.id = a.parent_issue_id
    WHERE p.workspace_id = @workspace_id::uuid
      AND a.distance < 32
      AND NOT p.id = ANY(a.path)
), descendants AS (
    SELECT i.id AS issue_id, ARRAY[i.id]::uuid[] AS path, 0::int AS distance
    FROM issue i
    WHERE i.id = @issue_id::uuid AND i.workspace_id = @workspace_id::uuid
    UNION ALL
    SELECT c.id, d.path || c.id, d.distance + 1
    FROM issue c JOIN descendants d ON c.parent_issue_id = d.issue_id
    WHERE c.workspace_id = @workspace_id::uuid
      AND d.distance < 32
      AND NOT c.id = ANY(d.path)
), related AS (
    SELECT issue_id, path, distance, 0::int AS family FROM ancestors
    UNION ALL
    SELECT issue_id, path, distance, 1::int AS family FROM descendants
    WHERE issue_id <> @issue_id::uuid
)
SELECT i.*
FROM issue i
JOIN related r ON r.issue_id = i.id
WHERE i.workspace_id = @workspace_id::uuid
ORDER BY CASE WHEN r.family = 0 THEN 0 ELSE 1 END, r.distance ASC, i.number ASC;
