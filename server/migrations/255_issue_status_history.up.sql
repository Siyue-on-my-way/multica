-- Durable history for issue status transitions. One row is written for each
-- transition when the issue status changes; other issue edits never append.
--
-- Actor identity follows the issue/comment convention: member and agent IDs
-- are UUIDs, while system transitions have a NULL actor UUID. No foreign keys
-- are used per project migration policy.
CREATE TABLE issue_status_history (
    id              UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    issue_id        UUID        NOT NULL,
    workspace_id    UUID        NOT NULL,
    from_status     TEXT        NOT NULL,
    to_status       TEXT        NOT NULL,
    changed_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    changed_by_type TEXT        NOT NULL,
    changed_by_id   UUID,

    CONSTRAINT chk_issue_status_history_statuses
        CHECK (from_status <> to_status),
    CONSTRAINT chk_issue_status_history_changed_by_type
        CHECK (changed_by_type IN ('member', 'agent', 'system'))
);
