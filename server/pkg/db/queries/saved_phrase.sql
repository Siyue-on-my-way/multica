-- name: ListSavedPhrases :many
SELECT * FROM saved_phrase
WHERE workspace_id = sqlc.arg(workspace_id)
  AND status = 'active'
  AND (scope = 'public' OR creator_id = sqlc.arg(creator_id))
ORDER BY pinned DESC, use_count DESC, updated_at DESC, id;

-- name: GetSavedPhrase :one
SELECT * FROM saved_phrase
WHERE workspace_id = sqlc.arg(workspace_id)
  AND id = sqlc.arg(id);

-- name: CreateSavedPhrase :one
INSERT INTO saved_phrase (
    id, workspace_id, creator_id, name, content, category, scope, pinned
) VALUES (
    sqlc.arg(id), sqlc.arg(workspace_id), sqlc.arg(creator_id), sqlc.arg(name),
    sqlc.arg(content), sqlc.arg(category), sqlc.arg(scope), sqlc.arg(pinned)
)
RETURNING *;

-- name: UpdateSavedPhrase :one
UPDATE saved_phrase
SET name = COALESCE(sqlc.narg(name), name),
    content = COALESCE(sqlc.narg(content), content),
    category = COALESCE(sqlc.narg(category), category),
    scope = COALESCE(sqlc.narg(scope), scope),
    pinned = COALESCE(sqlc.narg(pinned), pinned),
    updated_at = now()
WHERE workspace_id = sqlc.arg(workspace_id)
  AND id = sqlc.arg(id)
  AND status = 'active'
RETURNING *;

-- name: ArchiveSavedPhrase :one
UPDATE saved_phrase
SET status = 'archived', updated_at = now()
WHERE workspace_id = sqlc.arg(workspace_id)
  AND id = sqlc.arg(id)
  AND status = 'active'
RETURNING *;

-- name: UseSavedPhrase :one
UPDATE saved_phrase
SET use_count = use_count + 1, last_used_at = now(), updated_at = now()
WHERE workspace_id = sqlc.arg(workspace_id)
  AND id = sqlc.arg(id)
  AND status = 'active'
RETURNING *;
