-- Single statement: CREATE INDEX CONCURRENTLY cannot run inside a transaction.
CREATE INDEX CONCURRENTLY IF NOT EXISTS subissue_plan_job_active_idx
    ON subissue_plan_job (workspace_id, issue_id, status, updated_at DESC);
