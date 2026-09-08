-- Context handoff v2 keeps agent-authored state separate from the LLM-derived
-- digest. The old handoff_summary column is intentionally retained for wire
-- compatibility with older clients; new writers use the two typed columns.
--
-- Handoff versions are independent from issue.revision. A summary refresh
-- must not make the summary stale by changing the revision it is comparing
-- against, while manual checkpoint edits are still versioned for CAS writes.
ALTER TABLE issue
    ADD COLUMN manual_checkpoint JSONB,
    ADD COLUMN manual_checkpoint_version BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN manual_checkpoint_source TEXT NOT NULL DEFAULT 'agent',
    ADD COLUMN manual_checkpoint_updated_at TIMESTAMPTZ,
    ADD COLUMN derived_summary JSONB,
    ADD COLUMN derived_summary_version BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN derived_summary_source TEXT NOT NULL DEFAULT 'llm',
    ADD COLUMN derived_summary_updated_at TIMESTAMPTZ,
    ADD COLUMN derived_summary_source_revision BIGINT,
    ADD COLUMN derived_summary_source_comment_id UUID,
    ADD COLUMN derived_summary_source_task_id UUID,
    ADD COLUMN derived_summary_latency_ms INT,
    ADD COLUMN handoff_version BIGINT NOT NULL DEFAULT 0;

-- Existing handoff data was intentionally treated as an authored checkpoint:
-- preserving it is safer than guessing whether a previous deployment produced
-- it from an LLM. The legacy column remains populated until all installed
-- clients have moved to the v2 fields.
UPDATE issue
SET manual_checkpoint = handoff_summary,
    manual_checkpoint_version = 1,
    manual_checkpoint_updated_at = COALESCE(updated_at, now()),
    handoff_version = 1
WHERE handoff_summary IS NOT NULL
  AND manual_checkpoint IS NULL;

-- The manifest is a server-generated audit record attached to the exact task
-- that claimed the context. rerun_mode makes the user-facing actions explicit
-- without changing the legacy force_fresh_session compatibility flag.
ALTER TABLE agent_task_queue
    ADD COLUMN context_manifest JSONB NOT NULL DEFAULT '{}',
    ADD COLUMN rerun_mode TEXT NOT NULL DEFAULT '';
