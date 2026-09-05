-- SIY-83: physically separate the heavy raw-evidence snapshot from the
-- report summary row. report_history keeps the Markdown content (summary)
-- and a tiny v2 manifest pointer in data_snapshot permanently; the
-- gzip-compressed evidence lives in report_snapshot and is reclaimable via
-- a TTL without touching the summary. This is the storage-layer relief for
-- the "No space left on device" pressure that an ever-growing data_snapshot
-- JSONB column caused on report_history.
CREATE TABLE IF NOT EXISTS report_snapshot (
    report_id         UUID PRIMARY KEY NOT NULL,
    workspace_id      UUID NOT NULL,
    project_id        UUID NOT NULL,
    payload           BYTEA NOT NULL,                  -- gzip-compressed JSON evidence
    compression       TEXT NOT NULL DEFAULT 'gzip',
    raw_bytes         BIGINT NOT NULL DEFAULT 0,      -- uncompressed JSON size, for audit
    compressed_bytes  BIGINT NOT NULL DEFAULT 0,
    storage_truncated BOOLEAN NOT NULL DEFAULT FALSE,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at        TIMESTAMPTZ,                     -- NULL = within retention window
    CONSTRAINT report_snapshot_report_fk
        FOREIGN KEY (report_id) REFERENCES report_history(id) ON DELETE CASCADE
);

-- TTL lookups: only rows that actually carry an expiry are indexed, so the
-- retention sweep touches a small set even as saved reports accumulate.
-- indexes are created in migration 268_report_snapshot_indexes.up.sql
/* CREATE INDEX IF NOT EXISTS idx_report_snapshot_expires
    ON report_snapshot (expires_at) WHERE expires_at IS NOT NULL;

-- Project-scoped lifecycle and ad-hoc auditing.
CREATE INDEX IF NOT EXISTS idx_report_snapshot_project
    ON report_snapshot (project_id, created_at DESC); */
