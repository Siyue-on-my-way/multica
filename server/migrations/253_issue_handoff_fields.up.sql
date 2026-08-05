ALTER TABLE issue ADD COLUMN working_branch TEXT;
ALTER TABLE issue ADD COLUMN agent_status TEXT;
ALTER TABLE issue ADD COLUMN handoff_summary JSONB;
