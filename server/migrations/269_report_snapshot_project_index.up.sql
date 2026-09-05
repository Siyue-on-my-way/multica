CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_report_snapshot_project
    ON report_snapshot (project_id, created_at DESC);
