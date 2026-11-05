-- Attachments may reference a workspace file library entry instead of owning
-- their own bytes: the composer "choose from library" flow creates an
-- attachment row that points at the library file's storage object. sha256
-- carries the library file's content digest so the daemon can verify a
-- materialized download byte-for-byte; regular uploads keep the empty default.
-- CASCADE: deleting a library file reclaims its storage everywhere, so the
-- reference edges (chat messages / comments showing it) go with it.
ALTER TABLE attachment ADD COLUMN IF NOT EXISTS workspace_file_id UUID REFERENCES workspace_file(id) ON DELETE CASCADE;
ALTER TABLE attachment ADD COLUMN IF NOT EXISTS sha256 TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_attachment_workspace_file ON attachment(workspace_file_id) WHERE workspace_file_id IS NOT NULL;
