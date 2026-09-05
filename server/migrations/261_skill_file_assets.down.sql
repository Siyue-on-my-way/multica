ALTER TABLE skill_file
    DROP COLUMN IF EXISTS file_mode,
    DROP COLUMN IF EXISTS content_encoding;
