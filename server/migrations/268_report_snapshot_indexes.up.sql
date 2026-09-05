CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_report_snapshot_expires
    ON report_snapshot (expires_at) WHERE expires_at IS NOT NULL;
