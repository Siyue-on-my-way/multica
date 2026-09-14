-- Single statement: CREATE INDEX CONCURRENTLY cannot run inside a transaction.
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS subissue_plan_job_task_idx
    ON subissue_plan_job (task_id)
    WHERE task_id IS NOT NULL;
