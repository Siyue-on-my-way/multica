-- Single statement: CREATE INDEX CONCURRENTLY cannot run inside a transaction.
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS subissue_plan_job_pkey_uidx
    ON subissue_plan_job (id);
