-- Skill CRUD

-- name: ListSkillsByWorkspace :many
SELECT * FROM skill
WHERE workspace_id = $1
ORDER BY name ASC;

-- name: ListSkillSummariesByWorkspace :many
-- Same as ListSkillsByWorkspace but omits the SKILL.md `content` column. Used
-- by list endpoints (CLI table, web list page) where the body is never read;
-- shipping it everywhere blew up payload size on workspaces with many skills
-- and caused 15s CLI timeouts from high-latency regions (GH multica-ai/multica#2174).
SELECT id, workspace_id, name, description, config, created_by, created_at, updated_at
FROM skill
WHERE workspace_id = $1
ORDER BY name ASC;

-- name: ListVisibleSkillSummariesForWorkspace :many
-- The skills-page listing: the workspace's own skills plus every global skill,
-- which every workspace can read. is_global lets the UI badge the shared ones.
SELECT id, workspace_id, name, description, config, created_by, created_at, updated_at, is_global
FROM skill
WHERE workspace_id = $1 OR is_global
ORDER BY name ASC;

-- name: GetSkill :one
SELECT * FROM skill
WHERE id = $1;

-- name: GetSkillInWorkspace :one
SELECT * FROM skill
WHERE id = $1 AND workspace_id = $2;

-- name: GetSkillVisibleInWorkspace :one
-- Same as GetSkillInWorkspace but also resolves global skills: they are
-- readable from every workspace, so a member of any workspace may load one.
SELECT * FROM skill
WHERE id = $1 AND (workspace_id = $2 OR is_global);

-- name: GetGlobalSkillByName :one
-- Global skills share one cross-workspace namespace, so their name lookup
-- ignores workspace scoping entirely.
SELECT * FROM skill
WHERE name = $1 AND is_global;

-- name: ListGlobalSkills :many
SELECT * FROM skill
WHERE is_global
ORDER BY name ASC;

-- name: GetOldestWorkspaceID :one
-- Owner workspace for skills created by host-side automation (the skill
-- inbox) that has no requesting user to inherit a workspace from.
SELECT id FROM workspace
ORDER BY created_at ASC, id ASC
LIMIT 1;

-- name: GetSkillByWorkspaceAndName :one
-- Used by agent-template materialization to implement find-or-create: when a
-- template references a skill by name that already exists in the workspace,
-- reuse the existing skill_id rather than INSERT (which would fail the
-- UNIQUE(workspace_id, name) constraint from migration 008).
SELECT * FROM skill
WHERE workspace_id = $1 AND name = $2;

-- name: CreateSkill :one
INSERT INTO skill (workspace_id, name, description, content, config, created_by, is_global)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;

-- name: UpdateSkill :one
UPDATE skill SET
    name = COALESCE(sqlc.narg('name'), name),
    description = COALESCE(sqlc.narg('description'), description),
    content = COALESCE(sqlc.narg('content'), content),
    config = COALESCE(sqlc.narg('config'), config),
    updated_at = now()
WHERE id = $1
RETURNING *;

-- name: DeleteSkill :exec
-- Defense-in-depth: workspace_id is a SQL-layer tenant guard. See DeleteIssue.
DELETE FROM skill WHERE id = $1 AND workspace_id = $2;

-- Skill File CRUD

-- name: ListSkillFiles :many
SELECT * FROM skill_file
WHERE skill_id = $1
ORDER BY path ASC;

-- name: GetSkillFile :one
SELECT * FROM skill_file
WHERE id = $1;

-- name: UpsertSkillFile :one
INSERT INTO skill_file (skill_id, path, content, content_encoding, file_mode)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (skill_id, path) DO UPDATE SET
    content = EXCLUDED.content,
    content_encoding = EXCLUDED.content_encoding,
    file_mode = EXCLUDED.file_mode,
    updated_at = now()
RETURNING *;

-- name: DeleteSkillFile :exec
DELETE FROM skill_file WHERE id = $1;

-- name: DeleteSkillFilesBySkill :exec
DELETE FROM skill_file WHERE skill_id = $1;

-- Agent-Skill junction

-- name: ListAgentSkills :many
SELECT s.* FROM skill s
JOIN agent_skill ask ON ask.skill_id = s.id
WHERE ask.agent_id = $1 AND ask.enabled = TRUE
ORDER BY s.name ASC;

-- name: ListAgentSkillSummaries :many
-- Summary variant for the agent skills list endpoint — omits `content` for
-- the same reason as ListSkillSummariesByWorkspace.
SELECT s.id, s.workspace_id, s.name, s.description, s.config, s.created_by, s.created_at, s.updated_at, s.is_global, ask.enabled
FROM skill s
JOIN agent_skill ask ON ask.skill_id = s.id
WHERE ask.agent_id = $1
ORDER BY s.name ASC;

-- name: ListAgentSkillNamesByAgentIDs :many
SELECT ask.agent_id, s.name
FROM agent_skill ask
JOIN skill s ON s.id = ask.skill_id
WHERE ask.agent_id = ANY(sqlc.arg('agent_ids')::uuid[])
  AND ask.enabled = TRUE
ORDER BY ask.agent_id, s.name ASC;

-- name: AddAgentSkill :exec
INSERT INTO agent_skill (agent_id, skill_id)
VALUES ($1, $2)
ON CONFLICT DO NOTHING;

-- name: SetAgentSkillEnabled :execrows
UPDATE agent_skill
SET enabled = $3
WHERE agent_id = $1 AND skill_id = $2;

-- name: RemoveAgentSkill :exec
DELETE FROM agent_skill
WHERE agent_id = $1 AND skill_id = $2;

-- name: RemoveAllAgentSkills :exec
DELETE FROM agent_skill WHERE agent_id = $1;

-- name: ListAgentSkillsByWorkspace :many
SELECT ask.agent_id, s.id, s.name, s.description, ask.enabled
FROM agent_skill ask
JOIN skill s ON s.id = ask.skill_id
WHERE s.workspace_id = $1
ORDER BY s.name ASC;
