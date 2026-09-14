-- Single statement: CREATE INDEX CONCURRENTLY cannot run inside a transaction.
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS subissue_plan_job_idempotency_idx
    ON subissue_plan_job (workspace_id, issue_id, idempotency_key);
