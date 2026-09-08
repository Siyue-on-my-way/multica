package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// RecoverOrphanedTasks is called by the daemon at startup for each runtime
// it owns. It atomically fails any dispatched/running tasks the server still
// believes belong to that runtime — those are the tasks the previous daemon
// process was running when it died — and triggers MaybeRetryFailedTask for
// each so the user sees a fresh attempt instead of a permanently stuck row.
//
// This is the targeted fix for "issue stuck at in_progress when daemon
// restarts mid-task": the runtime heartbeat sweeper takes up to 75s + the
// in-process task timeout (2.5h) to notice such tasks; the daemon itself
// knows the moment it comes back up, so we let it report orphan recovery.
func (h *Handler) RecoverOrphanedTasks(w http.ResponseWriter, r *http.Request) {
	runtimeID := chi.URLParam(r, "runtimeId")
	if _, ok := h.requireDaemonRuntimeAccess(w, r, runtimeID); !ok {
		return
	}

	rows, err := h.TaskService.RecoverOrphanedTasksForRuntime(r.Context(), parseUUID(runtimeID))
	if err != nil {
		slog.Warn("recover-orphans failed", "runtime_id", runtimeID, "error", err)
		writeError(w, http.StatusInternalServerError, "recover orphans failed")
		return
	}

	// Funnel through the shared post-failure pipeline so we get the same
	// task:failed events, agent reconcile, issue rollback, and auto-retry
	// behaviour as the runtime sweeper. This was previously a fast-path
	// that bypassed those side effects, leaving the UI stale when no retry
	// was created (max_attempts exhausted, autopilot, non-retryable reason).
	retried := h.TaskService.HandleFailedTasks(r.Context(), rows)

	if len(rows) > 0 {
		slog.Info("recover-orphans completed",
			"runtime_id", runtimeID,
			"orphaned", len(rows),
			"retried", retried,
		)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"orphaned": len(rows),
		"retried":  retried,
	})
}

// PinTaskSession lets the daemon persist the agent's session_id and
// work_dir as soon as they're known — typically right after the agent
// emits its first system message — so a crash mid-run doesn't lose the
// resume pointer needed to continue the conversation on the next attempt.
type PinTaskSessionRequest struct {
	SessionID string `json:"session_id,omitempty"`
	WorkDir   string `json:"work_dir,omitempty"`
}

func (h *Handler) PinTaskSession(w http.ResponseWriter, r *http.Request) {
	taskID := chi.URLParam(r, "taskId")
	if _, ok := h.requireDaemonTaskAccess(w, r, taskID); !ok {
		return
	}

	var req PinTaskSessionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.SessionID == "" && req.WorkDir == "" {
		writeError(w, http.StatusBadRequest, "session_id or work_dir required")
		return
	}

	params := db.UpdateAgentTaskSessionParams{ID: parseUUID(taskID)}
	if req.SessionID != "" {
		params.SessionID = pgtype.Text{String: req.SessionID, Valid: true}
	}
	if req.WorkDir != "" {
		params.WorkDir = pgtype.Text{String: req.WorkDir, Valid: true}
	}
	// The pin can arrive after the user has already cancelled the run — it is
	// asynchronous, and for Codex it waits for the rollout to reach the store.
	// The cancel transaction then found no session to publish, so the chat's
	// resume pointer is still on the previous turn and would shadow the session
	// this pin is about to record (the claim handler reads the pointer before
	// the GetLastChatTaskSession fallback). Advancing it here closes that half
	// of GH #6340.
	//
	// Both writes commit together. Landing the session on the task row first and
	// the pointer second leaves the same window the cancel path had: the row
	// already names the new session while the pointer still names the previous
	// turn, and a follow-up claimed in between resumes the older one. The lock
	// comes first for the same reason it does in CancelTaskWithResult —
	// chat_session -> agent_task_queue is the global order, and ErrNoRows simply
	// means there is no session to lock or advance.
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		slog.Warn("pin-session failed to start tx", "task_id", taskID, "error", err)
		writeError(w, http.StatusInternalServerError, "pin session failed")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)

	if _, err := qtx.LockChatSessionForTask(r.Context(), params.ID); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		slog.Warn("pin-session failed to lock chat session", "task_id", taskID, "error", err)
		writeError(w, http.StatusInternalServerError, "pin session failed")
		return
	}
	if err := qtx.UpdateAgentTaskSession(r.Context(), params); err != nil {
		slog.Warn("pin-session failed", "task_id", taskID, "error", err)
		writeError(w, http.StatusInternalServerError, "pin session failed")
		return
	}
	// The statement re-reads the row, ignores anything that is not a cancelled
	// chat task, and refuses to move the pointer when a newer turn already owns
	// a session — so a straggler pin cannot drag the conversation backwards.
	if err := qtx.AdvanceCancelledChatSessionPointer(r.Context(), params.ID); err != nil {
		slog.Warn("advance cancelled chat session pointer failed", "task_id", taskID, "error", err)
		writeError(w, http.StatusInternalServerError, "pin session failed")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		slog.Warn("pin-session commit failed", "task_id", taskID, "error", err)
		writeError(w, http.StatusInternalServerError, "pin session failed")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Rerun action values for POST /api/issues/{id}/rerun. The legacy request
// shape (no action field) keeps working: with_context_compress=true with a
// task_id or a bare body still maps onto these semantics internally.
//
//   - refresh_summary: ONLY regenerate the LLM digest of the issue's state.
//     No task is enqueued and no run is cancelled — the next run (or the
//     agent itself, via the freshness gate) picks the summary up.
//   - new_session: enqueue a run that starts a clean provider session in a
//     fresh/reused workdir (the old force_fresh_session behaviour). Use when
//     the previous output was bad and replaying the conversation would replay
//     the problem.
//   - retry: re-run the source task's work and ALLOW resuming its provider
//     session when the failure did not poison it (infrastructure flake —
//     network cut, timeout — where the conversation is still good).
const (
	RerunActionRefreshSummary = "refresh_summary"
	RerunActionNewSession     = "new_session"
	RerunActionRetry          = "retry"
	// RerunActionLegacy marks requests that carried no action field; the
	// effective behaviour follows the legacy with_context_compress flag.
	RerunActionLegacy = "legacy"
)

// RerunIssueRequest is the optional body of POST /api/issues/{id}/rerun.
// All fields are optional; an empty body keeps the legacy "rerun the issue's
// current assignee" behaviour used by the CLI.
type RerunIssueRequest struct {
	// TaskID identifies the execution-log row the user clicked retry on.
	// When set, the rerun targets the agent that ran that specific task
	// (and reuses its leader/worker role) rather than the issue's current
	// assignee — so clicking retry on row that belonged to a now-displaced
	// agent re-fires that same agent, not the new assignee.
	TaskID string `json:"task_id,omitempty"`
	// Action picks one of the three decoupled rerun behaviours
	// (refresh_summary | new_session | retry). Empty means legacy: the
	// with_context_compress flag decides whether compression runs before a
	// force-fresh-session rerun, exactly as before this split existed.
	Action string `json:"action,omitempty"`
	// WithContextCompress, when true (legacy shape), runs the LLM-based
	// compression before enqueueing a force-fresh-session run — the old
	// "fresh session retry after switching the LLM gateway" behaviour, and
	// the standalone "compact context now" trigger when no task_id is set.
	WithContextCompress bool `json:"with_context_compress,omitempty"`
}

// RerunIssue manually re-enqueues an agent run for the issue. By default it
// targets the issue's current assignee (agent or squad leader); if the
// request body carries task_id, the rerun targets the agent that ran that
// specific past task instead. The new task is flagged force_fresh_session=true:
// the daemon claim handler skips the (agent_id, issue_id) session-resume
// lookup so the agent starts a clean session. A user clicking rerun has just
// judged the prior output bad — replaying the same conversation would replay
// the same poisoned state. (Automatic retry, by contrast, intentionally
// inherits the session — that path handles infrastructure failures, not bad
// output.)
func (h *Handler) RerunIssue(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	issue, ok := h.loadIssueForUser(w, r, id)
	if !ok {
		return
	}

	// Body is optional. A zero-length body or `{}` keeps the legacy
	// assignee-driven rerun behaviour the CLI relies on.
	var req RerunIssueRequest
	if r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
	}

	// Normalise the action. The legacy shape (no action) maps onto the split
	// semantics so old clients keep byte-for-byte behaviour:
	//   - with_context_compress=true → compress, then a force-fresh-session
	//     run (new_session), the old "compact and rerun" flow;
	//   - otherwise → plain force-fresh-session run (new_session without
	//     compression).
	action := req.Action
	if action == "" {
		action = RerunActionLegacy
	}
	h.Metrics.RecordIssueRerunAction(action)

	forceCompress := action == RerunActionRefreshSummary
	if action == RerunActionLegacy {
		forceCompress = req.WithContextCompress
	}

	var sourceTaskID pgtype.UUID
	if req.TaskID != "" {
		parsed, ok := parseUUIDOrBadRequest(w, req.TaskID, "task_id")
		if !ok {
			return
		}
		sourceTaskID = parsed
	}

	// refresh_summary deliberately does NOT enqueue: it only refreshes the
	// derived summary. Nothing is cancelled and no run starts — the explicit
	// cancel endpoint owns cancellation, and the caller decides when the next
	// run happens. The response carries the fresh compression state so the UI
	// can show it without a second read.
	if action == RerunActionRefreshSummary {
		result := h.compressHandoffContext(r.Context(), issue, true, "")
		if !result.Written {
			writeJSON(w, http.StatusOK, map[string]any{
				"compression_status": result.Status,
				"skipped_reason":     result.SkippedReason,
			})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"compression_status":          result.Status,
			"compression_source_revision": result.SourceRevision,
			"compression_latency_ms":      result.LatencyMs,
			"written":                     true,
		})
		return
	}

	// A manual rerun is a direct human action: attribute the new run to the
	// rerunning member (MUL-4302 §5). Resolve the actor the same way assign/promote
	// does; an agent A2A actor is not a human and threads an invalid actor.
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := uuidToString(issue.WorkspaceID)
	actorType, actorID := h.resolveActor(r, userID, workspaceID)
	actorUserID := memberActorUserID(actorType, actorID)

	// Re-validate the operator's invoke permission on the resolved target agent
	// before cancelling / creating anything (MUL-4525). Issue visibility does not
	// grant the right to trigger a private agent — a task_id rerun must gate the
	// historical agent, not the (possibly reassigned) current assignee.
	originatorUserID := h.invokeOriginatorFromRequest(r, actorType, actorID)
	canInvoke := func(agent db.Agent) bool {
		return h.canInvokeAgent(r.Context(), agent, actorType, actorID, originatorUserID, workspaceID)
	}

	// compression runs BEFORE the enqueue when requested (legacy flag or the
	// retry/new_session flows that ask for it): a slow LLM must never delay a
	// task that already claimed the pending slot, and the retry child of a
	// context-overflow failure depends on the summary existing at claim time.
	forceCompressNow := forceCompress || action == RerunActionNewSession && req.WithContextCompress
	var compressResult *CompressHandoffResult
	if forceCompressNow {
		compressResult = h.compressHandoffContext(r.Context(), issue, true, "")
	}

	allowResume := action == RerunActionRetry
	task, err := h.TaskService.RerunIssueWithMode(r.Context(), issue.ID, sourceTaskID, pgtype.UUID{}, actorUserID, canInvoke, rerunModeFor(action), allowResume)
	if errors.Is(err, service.ErrRerunInvokeNotAllowed) {
		h.writeDispatchBlocked(w, http.StatusForbidden, ReasonInvocationNotAllowed)
		return
	}
	if err != nil {
		slog.Warn("issue rerun failed", "issue_id", id, "error", err)
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	resp := taskToResponse(*task, uuidToString(issue.WorkspaceID))
	h.hydrateTaskAttributions(r.Context(), []*TaskAttribution{resp.Attribution})
	// Surface the compression outcome alongside the new task so a caller that
	// asked for a compressed rerun sees the state the run will start from.
	if compressResult != nil {
		resp.CompressionStatus = compressResult.Status
		resp.CompressionSourceRevision = compressResult.SourceRevision
		resp.CompressionLatencyMs = compressResult.LatencyMs
	}
	writeJSON(w, http.StatusAccepted, resp)
}

// rerunModeFor maps the API action onto the task row's audit/behaviour tag.
// The empty string stays the legacy marker for pre-split rows.
func rerunModeFor(action string) string {
	switch action {
	case RerunActionRefreshSummary:
		return RerunActionRefreshSummary
	case RerunActionNewSession:
		return RerunActionNewSession
	case RerunActionRetry:
		return RerunActionRetry
	default:
		return ""
	}
}

// errNoActiveTask marks an issue with nothing in flight to cancel.
var errNoActiveTask = errors.New("no active task for issue")

// activeTaskForIssue resolves the one task an issue-scoped cancel should stop.
// The list is ordered running-first; prefer a RUNNING row (an interruptible
// execution) over queued siblings, and cancel the oldest in its class so a
// multi-run issue stops deterministically.
func (h *Handler) activeTaskForIssue(ctx context.Context, issueID pgtype.UUID) (db.AgentTaskQueue, error) {
	tasks, err := h.Queries.ListActiveTasksByIssue(ctx, issueID)
	if err != nil {
		return db.AgentTaskQueue{}, err
	}
	if len(tasks) == 0 {
		return db.AgentTaskQueue{}, errNoActiveTask
	}
	for _, status := range []string{"running", "dispatched", "queued", "waiting_local_directory"} {
		oldest := -1
		for i, t := range tasks {
			if t.Status != status {
				continue
			}
			if oldest == -1 || taskCreatedAtBefore(t, tasks[oldest]) {
				oldest = i
			}
		}
		if oldest != -1 {
			return tasks[oldest], nil
		}
	}
	// Unknown active status — fall back to the first row rather than refusing.
	return tasks[0], nil
}

func taskCreatedAtBefore(a, b db.AgentTaskQueue) bool {
	if !a.CreatedAt.Valid {
		return false
	}
	if !b.CreatedAt.Valid {
		return true
	}
	return a.CreatedAt.Time.Before(b.CreatedAt.Time)
}

// CancelActiveTaskForIssue cancels the ONE active run on an issue — the
// explicit stop the rerun API no longer pretends to be. Before the split, the
// UI's "compact context" copy claimed it would cancel the active run while the
// backend deliberately left running tasks alone (only pending rows are
// cleared); users who wanted a run stopped had no first-class way to say so.
// This endpoint is that way: POST /api/issues/{id}/cancel.
func (h *Handler) CancelActiveTaskForIssue(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	issue, ok := h.loadIssueForUser(w, r, id)
	if !ok {
		return
	}

	active, err := h.activeTaskForIssue(r.Context(), issue.ID)
	if errors.Is(err, errNoActiveTask) {
		writeJSON(w, http.StatusConflict, map[string]any{
			"code":    "no_active_task",
			"message": "No active run on this issue.",
		})
		return
	}
	if err != nil {
		slog.Warn("issue cancel: active task lookup failed", "issue_id", id, "error", err)
		writeError(w, http.StatusInternalServerError, "active task lookup failed")
		return
	}

	task, err := h.TaskService.CancelTaskByUser(r.Context(), active.ID)
	if err != nil {
		slog.Warn("issue cancel failed", "issue_id", id, "task_id", uuidToString(active.ID), "error", err)
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	slog.Info("issue active task cancelled by user",
		"issue_id", id, "task_id", uuidToString(task.ID))
	resp := taskToResponse(*task, uuidToString(issue.WorkspaceID))
	h.hydrateTaskAttributions(r.Context(), []*TaskAttribution{resp.Attribution})
	writeJSON(w, http.StatusOK, resp)
}

// RetrySourceContextQuickCreate manually re-enqueues a failed issue-less
// quick-create while atomically moving its pending immutable source context to
// the new task. The workspace middleware supplies tenancy; the service also
// requires the original requester and the normal private-agent invoke gate.
func (h *Handler) RetrySourceContextQuickCreate(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID, ok := parseUUIDOrBadRequest(w, ctxWorkspaceID(r.Context()), "workspace id")
	if !ok {
		return
	}
	requesterID, ok := parseUUIDOrBadRequest(w, userID, "user id")
	if !ok {
		return
	}
	taskID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "taskId"), "task id")
	if !ok {
		return
	}
	canInvoke := func(agent db.Agent) bool {
		return h.canInvokeAgent(r.Context(), agent, "member", userID, userID, uuidToString(workspaceID))
	}
	task, err := h.TaskService.RetrySourceContextQuickCreate(r.Context(), workspaceID, requesterID, taskID, canInvoke)
	if writeIssueLimitReached(w, err) {
		return
	}
	if errors.Is(err, service.ErrRerunInvokeNotAllowed) {
		h.writeDispatchBlocked(w, http.StatusForbidden, ReasonInvocationNotAllowed)
		return
	}
	if errors.Is(err, service.ErrSourceContextRetryUnavailable) {
		writeJSON(w, http.StatusConflict, map[string]any{
			"code":  "source_context_retry_unavailable",
			"error": "This context can no longer be retried. Start again from the branch point.",
		})
		return
	}
	if err != nil {
		slog.Warn("source context quick-create retry failed", "task_id", uuidToString(taskID), "error", err)
		writeError(w, http.StatusInternalServerError, "retry source context quick create")
		return
	}
	resp := taskToResponse(*task, uuidToString(workspaceID))
	h.hydrateTaskAttributions(r.Context(), []*TaskAttribution{resp.Attribution})
	writeJSON(w, http.StatusAccepted, resp)
}
