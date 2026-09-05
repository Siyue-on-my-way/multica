ALTER TABLE issue
    ADD COLUMN manual_position_locked BOOLEAN NOT NULL DEFAULT false,
    ADD COLUMN agent_result_at TIMESTAMPTZ;

CREATE TABLE issue_agent_result_read (
    issue_id UUID NOT NULL REFERENCES issue(id) ON DELETE CASCADE,
    user_id UUID NOT NULL REFERENCES "user"(id) ON DELETE CASCADE,
    last_seen_agent_result_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (issue_id, user_id)
);

CREATE INDEX issue_agent_result_read_user_issue_idx
    ON issue_agent_result_read (user_id, issue_id);
