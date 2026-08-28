CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_report_history_project_range
    ON report_history(project_id, period_type, range_start DESC);
