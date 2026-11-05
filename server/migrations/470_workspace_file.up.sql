-- Workspace file library: files are first-class workspace resources, not
-- message decorations. Upload via POST /api/workspace-files; chat/issue
-- composers reference a row through attachment.workspace_file_id, and task
-- claims deliver it to runtimes as a sha256-pinned manifest entry.
CREATE TABLE workspace_file (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id  UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    filename      TEXT NOT NULL,
    url           TEXT NOT NULL,
    content_type  TEXT NOT NULL,
    size_bytes    BIGINT NOT NULL,
    sha256        TEXT NOT NULL DEFAULT '',
    uploader_type TEXT NOT NULL CHECK (uploader_type IN ('member', 'agent')),
    uploader_id   UUID NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_workspace_file_workspace ON workspace_file(workspace_id, created_at DESC);
