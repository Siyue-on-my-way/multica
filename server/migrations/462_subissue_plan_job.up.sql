-- Durable state for issue-split planning.  The source snapshot is immutable
-- input captured before any fallback task is dispatched; result is the only
-- structured output accepted from a plan-only runtime.
CREATE TABLE subissue_plan_job (
    id                    UUID NOT NULL DEFAULT gen_random_uuid(),
    workspace_id          UUID NOT NULL,
    issue_id              UUID NOT NULL,
    source_comment_id     UUID,
    source_snapshot       JSONB NOT NULL,
    source_snapshot_digest TEXT NOT NULL,
    context_revision      BIGINT NOT NULL DEFAULT 0,
    human_constraints     TEXT NOT NULL DEFAULT '',
    primary_error_class   TEXT NOT NULL DEFAULT '',
    fallback_reason       TEXT NOT NULL DEFAULT '',
    agent_id              UUID,
    task_id               UUID,
    status                TEXT NOT NULL DEFAULT 'queued'
        CHECK (status IN ('queued', 'running', 'succeeded', 'failed', 'cancelled', 'expired')),
    result                JSONB,
    error_summary         TEXT,
    idempotency_key       TEXT NOT NULL,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    started_at            TIMESTAMPTZ,
    completed_at          TIMESTAMPTZ,
    expires_at            TIMESTAMPTZ NOT NULL DEFAULT (now() + interval '15 minutes')
);
