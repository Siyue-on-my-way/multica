-- Durable workspace-scoped phrases and proposal snapshots.
-- Relationships are intentionally enforced by the application layer. This
-- keeps workspace deletion and historical snapshots under explicit control.
CREATE TABLE saved_phrase (
    id             UUID NOT NULL DEFAULT gen_random_uuid(),
    workspace_id   UUID NOT NULL,
    creator_id     UUID NOT NULL,
    name           TEXT NOT NULL,
    content        TEXT NOT NULL,
    category       TEXT NOT NULL DEFAULT 'general',
    scope          TEXT NOT NULL DEFAULT 'public',
    pinned         BOOLEAN NOT NULL DEFAULT false,
    use_count      BIGINT NOT NULL DEFAULT 0,
    last_used_at   TIMESTAMPTZ,
    status         TEXT NOT NULL DEFAULT 'active',
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (scope IN ('public', 'workspace')),
    CHECK (status IN ('active', 'archived')),
    CHECK (length(trim(name)) > 0),
    CHECK (length(trim(content)) > 0)
);

CREATE TABLE discussion_proposal (
    id                 UUID NOT NULL DEFAULT gen_random_uuid(),
    workspace_id       UUID NOT NULL,
    source_issue_id    UUID NOT NULL,
    question_comment_id UUID,
    answer_comment_id  UUID,
    snapshot           JSONB NOT NULL,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE proposal_generation (
    id              UUID NOT NULL DEFAULT gen_random_uuid(),
    workspace_id    UUID NOT NULL,
    proposal_id     UUID NOT NULL,
    strategy        TEXT NOT NULL,
    create_mode     TEXT NOT NULL DEFAULT 'backlog',
    idempotency_key TEXT,
    created_by      UUID NOT NULL,
    snapshot        JSONB NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (create_mode IN ('backlog', 'todo'))
);

CREATE TABLE proposal_generation_issue (
    id              UUID NOT NULL DEFAULT gen_random_uuid(),
    workspace_id    UUID NOT NULL,
    generation_id   UUID NOT NULL,
    proposal_id     UUID NOT NULL,
    issue_id        UUID NOT NULL,
    item_id         TEXT NOT NULL DEFAULT '',
    stage           INTEGER,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
