-- SIY-125: context observation. One redacted observation record per task run,
-- captured at the daemon's assembly-complete / provider-startup boundary and
-- reported via POST /api/daemon/tasks/{id}/context-observation. Served to the
-- UI lazily by GET /api/tasks/{id}/context so the hot task-list paths never
-- carry the payload.
--
-- No database foreign key (house rule: relationships are enforced in the
-- application layer). The task_id 1:1 with agent_task_queue is verified
-- through GetAgentTaskInWorkspace (user-facing read) and the daemon
-- task-access guard (daemon write) before any row is touched, so a stray
-- observation row can never grant cross-workspace access.
--
-- The payload is already redacted by the daemon before it leaves the host
-- (secrets/emails/absolute paths scrubbed via redact.Text + observation rules;
-- MCP config raw is withheld entirely), so this column never stores a secret.
-- Exact input tokens are NOT stored here — they live in task_usage (reported
-- by ReportTaskUsage) and are merged into the GET response at read time.
CREATE TABLE task_context_observation (
    task_id     UUID        PRIMARY KEY,
    payload     JSONB       NOT NULL DEFAULT '{}',
    observed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
