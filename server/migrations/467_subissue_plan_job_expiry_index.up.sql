-- Single statement: CREATE INDEX CONCURRENTLY cannot run inside a transaction.
CREATE INDEX CONCURRENTLY IF NOT EXISTS subissue_plan_job_expiry_idx
    ON subissue_plan_job (expires_at)
    WHERE status IN ('queued', 'running');
