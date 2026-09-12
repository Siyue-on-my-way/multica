package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// GetIssueExecutionContext serves GET /api/issues/{id}/execution-context.
// It returns the issue's own structured execution context plus the merged,
// de-duplicated view a run of this issue would receive: project resources,
// every ancestor that left inheritance on (root-most first), and the issue's
// own entries. The preview panel and the issue detail view both read from
// here so UI and runtime see the same resolution.
func (h *Handler) GetIssueExecutionContext(w http.ResponseWriter, r *http.Request) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	prefix := h.getIssuePrefix(r.Context(), issue.WorkspaceID)
	inherited := h.resolveInheritedExecutionContext(r, issue, prefix)
	writeJSON(w, http.StatusOK, IssueExecutionContextResponse{
		Own:       service.ParseExecutionContext(issue.ExecutionContext),
		Inherited: inherited,
	})
}

// IssueExecutionContextResponse is the body of the execution-context read.
type IssueExecutionContextResponse struct {
	Own       service.ExecutionContext           `json:"own"`
	Inherited service.ResolvedExecutionContext   `json:"inherited"`
}

// resolveInheritedExecutionContext merges the issue chain and the project's
// repository resources into the resolved view. Fail-soft throughout: a
// project-resource read error degrades to the chain-only view, never a 500 —
// this resolver also sits on the sub-issue preview path.
func (h *Handler) resolveInheritedExecutionContext(r *http.Request, issue db.Issue, prefix string) service.ResolvedExecutionContext {
	var projectEntries []service.ExecutionContextEntry
	if issue.ProjectID.Valid {
		resources, err := h.Queries.ListProjectResourcesInWorkspace(r.Context(), db.ListProjectResourcesInWorkspaceParams{
			ProjectID:   issue.ProjectID,
			WorkspaceID: issue.WorkspaceID,
		})
		if err != nil {
			slog.Warn("resolve execution context: list project resources failed",
				"issue_id", uuidToString(issue.ID), "error", err)
		} else {
			projectEntries = service.ProjectResourceExecutionEntries(resources)
		}
	}
	return service.ResolveInheritedExecutionContextForIssue(r.Context(), h.Queries, issue, prefix, projectEntries)
}

// normalizeExecutionContextCreate validates a create-request execution context
// for persistence. Version 1 — a brand-new document. The error is surfaced to
// the client so an unusable context fails the create instead of leaving an
// issue whose descendants would silently miss the context the author typed.
func normalizeExecutionContextCreate(raw json.RawMessage) ([]byte, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	encoded, _, err := service.NormalizeExecutionContextForWrite(raw, 1)
	return encoded, err
}

// normalizeExecutionContextUpdate validates an update-request execution
// context. A context write bumps the document's version so descendants (and
// the claim manifest) can tell the refreshed snapshot from the one earlier
// runs already consumed. Clearing (explicit null) needs no version.
func normalizeExecutionContextUpdate(raw json.RawMessage, prev []byte) ([]byte, error) {
	// raw was explicitly present in the JSON body (checked by the caller).
	trimmed := trimJSONValue(raw)
	if string(trimmed) == "null" {
		return nil, nil
	}
	version := service.ParseExecutionContext(prev).Version + 1
	encoded, _, err := service.NormalizeExecutionContextForWrite(trimmed, version)
	return encoded, err
}

// trimJSONValue strips surrounding whitespace from a raw JSON value.
func trimJSONValue(raw json.RawMessage) json.RawMessage {
	start, end := 0, len(raw)
	for start < end && (raw[start] == ' ' || raw[start] == '\n' || raw[start] == '\r' || raw[start] == '\t') {
		start++
	}
	for end > start && (raw[end-1] == ' ' || raw[end-1] == '\n' || raw[end-1] == '\r' || raw[end-1] == '\t') {
		end--
	}
	return raw[start:end]
}

// issueExecutionContextRaw normalizes the stored JSONB to a JSON-safe raw
// message: absent for NULL, whitespace-trimmed passthrough otherwise.
func issueExecutionContextRaw(raw []byte) json.RawMessage {
	trimmed := trimJSONValue(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return nil
	}
	return json.RawMessage(trimmed)
}

// persistIssueExecutionContext writes the normalized document after the issue
// row exists. A failed write is logged, not fatal: the issue itself is valid,
// and the create/update already told the user the context was accepted — a 500
// here would strand a committed issue behind a context hiccup, the same trade
// the ancestor-context-refs metadata write makes. Returns the refreshed row
// (the write bumps revision/updated_at) and whether it succeeded.
func persistIssueExecutionContext(h *Handler, r *http.Request, issue db.Issue, encoded []byte) (db.Issue, bool) {
	updated, err := h.Queries.SetIssueExecutionContext(r.Context(), db.SetIssueExecutionContextParams{
		ID:               issue.ID,
		WorkspaceID:      issue.WorkspaceID,
		ExecutionContext: encoded,
	})
	if err != nil {
		slog.Warn("persist execution context failed", "issue_id", uuidToString(issue.ID), "error", err)
		return issue, false
	}
	return updated, true
}
