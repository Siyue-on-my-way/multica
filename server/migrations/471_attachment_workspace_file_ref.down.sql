DROP INDEX IF EXISTS idx_attachment_workspace_file;
ALTER TABLE attachment DROP COLUMN IF EXISTS sha256;
ALTER TABLE attachment DROP COLUMN IF EXISTS workspace_file_id;
