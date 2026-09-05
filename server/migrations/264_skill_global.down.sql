-- Reversing is only clean when no global skill shares a name with a
-- same-workspace skill; such collisions must be resolved by hand first.
DROP INDEX IF EXISTS idx_skill_global;
DROP INDEX IF EXISTS skill_global_name_key;
DROP INDEX IF EXISTS skill_workspace_name_key;
ALTER TABLE skill ADD CONSTRAINT skill_workspace_id_name_key UNIQUE (workspace_id, name);
ALTER TABLE skill DROP COLUMN IF EXISTS is_global;
