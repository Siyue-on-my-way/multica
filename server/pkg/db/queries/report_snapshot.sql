-- SIY-83: report_snapshot query surface. The heavy raw-evidence payload is
-- persisted gzip-compressed in the report_snapshot table; report_history
-- keeps only a tiny manifest pointer in data_snapshot plus the Markdown
-- summary in content. See migrations/267_report_snapshot.up.sql.

-- name: UpsertReportSnapshot :exec
-- Persist the gzip-compressed raw-evidence snapshot for a report. The
-- report_history row holds only a manifest pointer in data_snapshot; the
-- heavy payload lives here. ON CONFLICT makes regenerate/retry idempotent.
INSERT INTO report_snapshot (
    report_id, workspace_id, project_id, payload, compression,
    raw_bytes, compressed_bytes, storage_truncated, created_at, expires_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, now(), $9
)
ON CONFLICT (report_id) DO UPDATE SET
    payload           = EXCLUDED.payload,
    compression       = EXCLUDED.compression,
    raw_bytes         = EXCLUDED.raw_bytes,
    compressed_bytes  = EXCLUDED.compressed_bytes,
    storage_truncated = EXCLUDED.storage_truncated,
    created_at        = EXCLUDED.created_at,
    expires_at        = EXCLUDED.expires_at;

-- name: GetReportSnapshot :one
-- Fetch the compressed evidence row for on-demand inflation. Returns no row
-- when the snapshot was reclaimed by the TTL or has not yet been written; the
-- caller falls back to the report_history.data_snapshot manifest.
SELECT report_id, payload, compression, raw_bytes, compressed_bytes,
       storage_truncated, created_at, expires_at
FROM report_snapshot
WHERE report_id = $1;

-- name: DeleteExpiredReportSnapshots :execrows
-- TTL reclamation. Deletes compressed evidence whose retention window has
-- elapsed while leaving report_history (the Markdown summary) untouched, so
-- historical report lists and basic display keep working. Rows without an
-- expires_at (still within retention or explicitly retained) are never
-- touched, which protects in-progress and recently generated reports.
DELETE FROM report_snapshot
WHERE expires_at IS NOT NULL
  AND expires_at < now();
