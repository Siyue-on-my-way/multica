DROP TABLE IF EXISTS issue_agent_result_read;
ALTER TABLE issue
    DROP COLUMN IF EXISTS agent_result_at,
    DROP COLUMN IF EXISTS manual_position_locked;
