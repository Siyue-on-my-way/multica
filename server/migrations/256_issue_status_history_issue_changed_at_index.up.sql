-- Single-statement migration: CREATE INDEX CONCURRENTLY cannot run inside a
-- transaction.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_issue_status_history_issue_changed_at
    ON issue_status_history(issue_id, changed_at DESC);
