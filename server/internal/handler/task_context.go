package handler

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/middleware"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// SIY-125: context observation. The daemon captures a redacted record of the
// context a run assembled and delivered, at the assembly-complete /
// provider-startup boundary, and reports it via the daemon-auth endpoint below.
// The user-facing GET endpoint serves it lazily, merging exact input tokens
// from task_usage (reported separately by ReportTaskUsage) and gating the full
// session id and per-section raw text behind a workspace owner/admin check.

// taskContextResponse mirrors the daemon's ContextObservation wire shape
// (internal/daemon/context_observation.go). The JSON tags must match exactly
// so the stored payload round-trips. It is duplicated here rather than shared
// across packages because the handler package cannot import the daemon package;
// this matches the existing TaskUsageEntry / TaskUsagePayload convention.
type taskContextResponse struct {
	TaskID          string                 `json:"task_id"`
	Provider        string                 `json:"provider"`
	RuntimeID       string                 `json:"runtime_id,omitempty"`
	SessionReused   bool                   `json:"session_reused"`
	ResumeExpected  bool                   `json:"resume_expected"`
	ResumeActual    string                 `json:"resume_actual"`
	FallbackReason  string                 `json:"fallback_reason,omitempty"`
	WorkdirReused   bool                   `json:"workdir_reused"`
	PromptBytes     int                    `json:"prompt_bytes"`
	InputTokens     int                    `json:"input_tokens"`
	TokenMode       string                 `json:"token_mode"`
	Sections        []taskContextSection   `json:"sections"`
	SessionID       string                 `json:"session_id,omitempty"`
	ObservedAt      time.Time              `json:"observed_at"`
	CompletedAt     *time.Time             `json:"completed_at,omitempty"`
}

type taskContextSection struct {
	Key        string `json:"key"`
	Source     string `json:"source"`
	Delivery   string `json:"delivery"`
	Injected   bool   `json:"injected"`
	Bytes      int    `json:"bytes"`
	TokenCount int    `json:"token_count"`
	Truncated  bool   `json:"truncated"`
	Digest     string `json:"digest"`
	Preview    string `json:"preview"`
	Raw        string `json:"raw,omitempty"`
}

// ReportTaskContextObservation receives the daemon's redacted context
// observation for a task and stores it. The body is the already-redacted
// ContextObservation JSON (secrets/emails/absolute paths scrubbed; MCP raw
// withheld), so it is stored verbatim without further parsing. A store
// failure never returns success to the daemon, but the daemon treats a report
// failure as non-fatal, so this only affects observability, not the run.
func (h *Handler) ReportTaskContextObservation(w http.ResponseWriter, r *http.Request) {
	taskID := chi.URLParam(r, "taskId")
	task, ok := h.requireDaemonTaskAccess(w, r, taskID)
	if !ok {
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	// Reject malformed JSON rather than persisting an unparseable blob; the
	// payload is opaque to this handler but must still be valid JSON so the
	// GET endpoint can unmarshal it later.
	if !json.Valid(body) {
		writeError(w, http.StatusBadRequest, "invalid context observation payload")
		return
	}

	if err := h.Queries.UpsertTaskContextObservation(r.Context(), db.UpsertTaskContextObservationParams{
		TaskID:  task.ID,
		Payload: body,
	}); err != nil {
		slog.Warn("upsert task context observation failed", "task_id", taskID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to store context observation")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// GetTaskContext serves a task's context observation to the UI. It is
// workspace-scoped: the task must belong to the caller's workspace. Exact input
// tokens are merged from task_usage when present (token_mode "exact"); otherwise
// the daemon's boundary estimate is returned (token_mode "estimated"). The full
// session id and per-section raw text are withheld unless the caller is a
// workspace owner/admin and requests them via ?raw=<key> (or ?raw=all); the
// default response carries only redacted previews and a shortened session id.
func (h *Handler) GetTaskContext(w http.ResponseWriter, r *http.Request) {
	taskID := chi.URLParam(r, "taskId")
	taskUUID, ok := parseUUIDOrBadRequest(w, taskID, "task_id")
	if !ok {
		return
	}

	task, err := h.Queries.GetAgentTask(r.Context(), taskUUID)
	if err != nil {
		writeError(w, http.StatusNotFound, "task not found")
		return
	}

	wsID := h.TaskService.ResolveTaskWorkspaceID(r.Context(), task)
	if wsID == "" || wsID != middleware.WorkspaceIDFromContext(r.Context()) {
		writeError(w, http.StatusNotFound, "task not found")
		return
	}

	// Exact input tokens live in task_usage (reported by ReportTaskUsage),
	// not in the observation payload, so a corrected provider figure always
	// overrides the daemon's char-based estimate.
	var exactInputTokens int64
	if usage, err := h.Queries.GetTaskUsage(r.Context(), taskUUID); err == nil {
		for _, u := range usage {
			exactInputTokens += u.InputTokens
		}
	}

	// The session id and raw section text are redacted (no secrets), but the
	// issue requires the full values to be permission-gated. Owner/admin may
	// request them; everyone else gets a shortened session id and previews only.
	member, hasMember := ctxMember(r.Context())
	canSeeFull := hasMember && roleAllowed(member.Role, "owner", "admin")
	rawKey := r.URL.Query().Get("raw")

	payload, err := h.Queries.GetTaskContextObservation(r.Context(), taskUUID)
	if err != nil {
		// No boundary observation for this task (e.g. it predates the feature,
		// or the report was lost). Surface an explicit "unknown" state rather
		// than 404 so the UI can render the Unknown badge without an error.
		resp := taskContextResponse{
			TaskID:       taskID,
			ResumeActual: "unknown",
			TokenMode:    "estimated",
			Sections:     []taskContextSection{},
		}
		if exactInputTokens > 0 {
			resp.InputTokens = int(exactInputTokens)
			resp.TokenMode = "exact"
		}
		writeJSON(w, http.StatusOK, resp)
		return
	}

	var obs taskContextResponse
	if err := json.Unmarshal(payload, &obs); err != nil {
		slog.Warn("unmarshal task context observation failed", "task_id", taskID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to read context observation")
		return
	}
	obs.TaskID = taskID

	if exactInputTokens > 0 {
		obs.InputTokens = int(exactInputTokens)
		obs.TokenMode = "exact"
	}

	// The full session id ships only when a permitted caller explicitly asks
	// for raw (?raw=...): owners/admins on demand. Everyone else, and every
	// default (no-?raw) request, gets a shortened id — the resumable pointer is
	// not needed to identify a run.
	if !(canSeeFull && rawKey != "") {
		obs.SessionID = shortSessionID(obs.SessionID)
	}
	for i := range obs.Sections {
		reveal := canSeeFull && rawKey != "" && (rawKey == "all" || rawKey == obs.Sections[i].Key)
		if !reveal {
			obs.Sections[i].Raw = ""
		} else {
			slog.Info("context observation raw revealed",
				"task_id", taskID, "section", obs.Sections[i].Key, "user_id", requestUserID(r))
		}
	}

	writeJSON(w, http.StatusOK, obs)
}

// shortSessionID returns a non-secret prefix of a provider session id so the UI
// can identify a run without exposing the full resumable pointer.
func shortSessionID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8] + "…"
}
