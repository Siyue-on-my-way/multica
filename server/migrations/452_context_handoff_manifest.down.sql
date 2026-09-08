ALTER TABLE issue
    DROP COLUMN IF EXISTS handoff_version,
    DROP COLUMN IF EXISTS derived_summary_latency_ms,
    DROP COLUMN IF EXISTS derived_summary_source_task_id,
    DROP COLUMN IF EXISTS derived_summary_source_comment_id,
    DROP COLUMN IF EXISTS derived_summary_source_revision,
    DROP COLUMN IF EXISTS derived_summary_updated_at,
    DROP COLUMN IF EXISTS derived_summary_source,
    DROP COLUMN IF EXISTS derived_summary_version,
    DROP COLUMN IF EXISTS derived_summary,
    DROP COLUMN IF EXISTS manual_checkpoint_updated_at,
    DROP COLUMN IF EXISTS manual_checkpoint_source,
    DROP COLUMN IF EXISTS manual_checkpoint_version,
    DROP COLUMN IF EXISTS manual_checkpoint;

ALTER TABLE agent_task_queue
    DROP COLUMN IF EXISTS rerun_mode,
    DROP COLUMN IF EXISTS context_manifest;
