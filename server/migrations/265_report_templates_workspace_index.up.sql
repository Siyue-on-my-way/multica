-- Single-statement migration: the runner sends each file as one Exec and
-- Postgres wraps multi-statement strings in an implicit transaction, which
-- rejects CREATE INDEX CONCURRENTLY (SQLSTATE 25001).
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_report_templates_workspace
    ON report_templates(workspace_id, period_type);
