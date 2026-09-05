-- Skill bundles may contain binary resources and executable scripts. Keep the
-- original TEXT column for backwards-compatible UTF-8 files, and add an
-- explicit transport marker plus a restricted POSIX mode for files that need
-- more than the original SKILL.md/text representation.
ALTER TABLE skill_file
    ADD COLUMN content_encoding TEXT NOT NULL DEFAULT 'utf8',
    ADD COLUMN file_mode INTEGER NOT NULL DEFAULT 420;
