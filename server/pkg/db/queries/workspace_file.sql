-- name: CreateWorkspaceFile :one
INSERT INTO workspace_file (
    id, workspace_id, filename, url, content_type, size_bytes, sha256,
    uploader_type, uploader_id
  )
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
RETURNING *;

-- name: GetWorkspaceFile :one
SELECT * FROM workspace_file
WHERE id = $1 AND workspace_id = $2;

-- name: ListWorkspaceFiles :many
SELECT * FROM workspace_file
WHERE workspace_id = $1
ORDER BY created_at DESC, id DESC
LIMIT $2 OFFSET $3;

-- name: CountWorkspaceFiles :one
SELECT COUNT(*) FROM workspace_file WHERE workspace_id = $1;

-- name: SumWorkspaceFileBytes :one
SELECT COALESCE(SUM(size_bytes), 0)::bigint AS total_bytes FROM workspace_file WHERE workspace_id = $1;

-- name: DeleteWorkspaceFile :one
DELETE FROM workspace_file
WHERE id = $1 AND workspace_id = $2
RETURNING *;

-- name: CreateAttachmentFromWorkspaceFile :one
-- "Choose from library" flow: create an attachment row that references an
-- existing library file instead of owning new bytes. Same revision-bump CTEs
-- as CreateAttachment so binding to a comment/issue keeps revision semantics.
WITH inserted AS (
  INSERT INTO attachment (
    id, workspace_id, issue_id, comment_id, chat_session_id, task_id, source_context_id,
    workspace_file_id, sha256,
    uploader_type, uploader_id, filename, url, content_type, size_bytes
  )
  VALUES (
    $1, $2, sqlc.narg(issue_id), sqlc.narg(comment_id), sqlc.narg(chat_session_id), sqlc.narg(task_id), sqlc.narg(source_context_id),
    sqlc.arg(workspace_file_id), sqlc.arg(sha256),
    $3, $4, $5, $6, $7, $8
  )
  RETURNING *
), bumped_issue AS (
  UPDATE issue
  SET revision = revision + 1
  WHERE id IN (SELECT issue_id FROM inserted WHERE issue_id IS NOT NULL)
  RETURNING revision
), bumped_comment AS (
  UPDATE comment
  SET revision = revision + 1
  WHERE id IN (SELECT comment_id FROM inserted WHERE comment_id IS NOT NULL)
  RETURNING revision
)
SELECT inserted.*,
       COALESCE((SELECT revision FROM bumped_issue), 0)::bigint AS issue_revision,
       COALESCE((SELECT revision FROM bumped_comment), 0)::bigint AS comment_revision
FROM inserted;
