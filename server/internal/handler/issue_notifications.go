package handler

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func (h *Handler) MarkIssueAgentResultRead(w http.ResponseWriter, r *http.Request) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	userUUID, err := util.ParseUUID(userID)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid user identity")
		return
	}
	if err := h.Queries.MarkIssueAgentResultRead(r.Context(), db.MarkIssueAgentResultReadParams{
		IssueID: issue.ID,
		UserID:  userUUID,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to mark agent result read")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) ClearIssueManualPositionLock(w http.ResponseWriter, r *http.Request) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	updated, err := h.Queries.ClearIssueManualPositionLock(r.Context(), db.ClearIssueManualPositionLockParams{
		ID:          issue.ID,
		WorkspaceID: issue.WorkspaceID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to restore automatic ordering")
		return
	}
	writeJSON(w, http.StatusOK, issueToResponse(updated, h.getIssuePrefix(r.Context(), updated.WorkspaceID)))
}
