-- Durable audit ledger for the local-disk -> object-storage migration.
--
-- The source URL/key is kept even after the application row is rewritten so
-- operators can prove which legacy object was copied and delay deleting the
-- old volume until the new path has been stable. The table intentionally does
-- not reference one owner table: attachments and the four avatar columns
-- share the same object namespace, while channel/source-context intent rows
-- may be migrated before an attachment row is committed.
CREATE TABLE file_storage_migration (
    id                    UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    object_type           TEXT NOT NULL CHECK (object_type IN (
                              'attachment',
                              'avatar',
                              'channel_media',
                              'source_context'
                          )),
    object_id             UUID,
    workspace_id          UUID,
    owner_table           TEXT,
    owner_id              UUID,
    source_url            TEXT NOT NULL,
    source_key            TEXT NOT NULL,
    target_key            TEXT NOT NULL,
    target_url            TEXT NOT NULL DEFAULT '',
    status                TEXT NOT NULL DEFAULT 'pending'
                              CHECK (status IN (
                                  'pending',
                                  'uploaded',
                                  'verified',
                                  'updated',
                                  'failed',
                                  'legacy_deleted',
                                  'skipped'
                              )),
    sha256                TEXT,
    size_bytes            BIGINT,
    legacy_delete_after   TIMESTAMPTZ,
    migrated_at           TIMESTAMPTZ,
    legacy_deleted_at     TIMESTAMPTZ,
    last_error            TEXT,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX idx_file_storage_migration_source
    ON file_storage_migration (object_type, source_url);
CREATE INDEX idx_file_storage_migration_status
    ON file_storage_migration (status, legacy_delete_after);
CREATE INDEX idx_file_storage_migration_key
    ON file_storage_migration (source_key, target_key);
