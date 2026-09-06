-- name: UpsertTaskContextObservation :exec
-- SIY-125: store the daemon's redacted context-observation record for a task.
-- One row per task; a re-report (e.g. a retried run reusing the task id) overwrites
-- so the latest boundary observation wins. The caller has already verified the
-- task belongs to the daemon's workspace; no DB FK enforces it (house rule).
-- observed_at is the report time; the payload itself carries the precise
-- boundary ObservedAt for the UI.
INSERT INTO task_context_observation (task_id, payload, observed_at, updated_at)
VALUES ($1, $2, now(), now())
ON CONFLICT (task_id) DO UPDATE SET
    payload = EXCLUDED.payload,
    observed_at = now(),
    updated_at = now();

-- name: GetTaskContextObservation :one
SELECT payload FROM task_context_observation WHERE task_id = $1;
