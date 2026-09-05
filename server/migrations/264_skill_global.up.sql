-- Global skills: one skill row shared read-only by every workspace. Global
-- skills stay off an agent's task payload by default; a run picks one up only
-- when its trigger text (issue description / triggering comment) names the
-- skill. Management (edit/delete) stays with the admins of the workspace that
-- owns the row.

ALTER TABLE skill ADD COLUMN is_global BOOLEAN NOT NULL DEFAULT FALSE;

-- Global skill names form one shared namespace, independent of per-workspace
-- names, so a global skill and a same-name workspace skill can coexist. The
-- composite UNIQUE(workspace_id, name) from migration 008 becomes a partial
-- unique index that ignores global rows.
ALTER TABLE skill DROP CONSTRAINT IF EXISTS skill_workspace_id_name_key;
CREATE UNIQUE INDEX skill_workspace_name_key ON skill (workspace_id, name) WHERE NOT is_global;
CREATE UNIQUE INDEX skill_global_name_key ON skill (name) WHERE is_global;
CREATE INDEX idx_skill_global ON skill (is_global) WHERE is_global;
