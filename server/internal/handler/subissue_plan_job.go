package handler

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func (h *Handler) loadSubissuePlanJobForRequest(w http.ResponseWriter, r *http.Request) (db.SubissuePlanJob, pgtype.UUID, bool) {
	workspaceID, ok := parseUUIDOrBadRequest(w, h.resolveWorkspaceID(r), "workspace_id")
	if !ok {
		return db.SubissuePlanJob{}, pgtype.UUID{}, false
	}
	jobID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "job_id")
	if !ok {
		return db.SubissuePlanJob{}, pgtype.UUID{}, false
	}
	job, err := h.Queries.GetSubissuePlanJob(r.Context(), db.GetSubissuePlanJobParams{ID: jobID, WorkspaceID: workspaceID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "planning job not found")
		} else {
			writeError(w, http.StatusInternalServerError, "failed to load planning job")
		}
		return db.SubissuePlanJob{}, pgtype.UUID{}, false
	}
	return job, workspaceID, true
}

func (h *Handler) GetSubissuePlanJob(w http.ResponseWriter, r *http.Request) {
	job, _, ok := h.loadSubissuePlanJobForRequest(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, subissuePlanJobToResponse(job))
}

func (h *Handler) CancelSubissuePlanJob(w http.ResponseWriter, r *http.Request) {
	job, workspaceID, ok := h.loadSubissuePlanJobForRequest(w, r)
	if !ok {
		return
	}
	if job.Status == "queued" || job.Status == "running" {
		if job.TaskID.Valid && h.TaskService != nil {
			if _, err := h.TaskService.CancelTaskByUser(r.Context(), job.TaskID); err != nil && !errors.Is(err, pgx.ErrNoRows) {
				writeError(w, http.StatusInternalServerError, "failed to cancel planning task")
				return
			}
		}
		if updated, err := h.Queries.CancelSubissuePlanJob(r.Context(), db.CancelSubissuePlanJobParams{ID: job.ID, WorkspaceID: workspaceID}); err == nil {
			job = updated
		} else if errors.Is(err, pgx.ErrNoRows) {
			// CancelTaskByUser also settles the linked job. Reload that terminal
			// row so a successful cancel cannot be reported as still running when
			// the task and job updates race each other.
			if refreshed, readErr := h.Queries.GetSubissuePlanJob(r.Context(), db.GetSubissuePlanJobParams{ID: job.ID, WorkspaceID: workspaceID}); readErr == nil {
				job = refreshed
			} else if !errors.Is(readErr, pgx.ErrNoRows) {
				writeError(w, http.StatusInternalServerError, "failed to reload planning job")
				return
			}
		} else {
			writeError(w, http.StatusInternalServerError, "failed to cancel planning job")
			return
		}
	}
	writeJSON(w, http.StatusOK, subissuePlanJobToResponse(job))
}

func (h *Handler) RetrySubissuePlanJob(w http.ResponseWriter, r *http.Request) {
	job, workspaceID, ok := h.loadSubissuePlanJobForRequest(w, r)
	if !ok {
		return
	}
	if job.Status == "succeeded" || job.Status == "queued" || job.Status == "running" {
		writeJSON(w, http.StatusOK, subissuePlanJobToResponse(job))
		return
	}
	job, err := h.Queries.RetrySubissuePlanJob(r.Context(), db.RetrySubissuePlanJobParams{ID: job.ID, WorkspaceID: workspaceID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusConflict, "planning job is no longer retryable")
		} else {
			writeError(w, http.StatusInternalServerError, "failed to retry planning job")
		}
		return
	}
	job, err = h.Queries.ClaimSubissuePlanJobDispatch(r.Context(), db.ClaimSubissuePlanJobDispatchParams{
		ID: job.ID, WorkspaceID: workspaceID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			current, readErr := h.Queries.GetSubissuePlanJob(r.Context(), db.GetSubissuePlanJobParams{ID: job.ID, WorkspaceID: workspaceID})
			if readErr == nil {
				writeJSON(w, http.StatusAccepted, acceptedSubissuePlanJob(current))
				return
			}
		}
		writeError(w, http.StatusInternalServerError, "failed to reserve planning retry")
		return
	}
	var snapshot service.SubissuePlanFallbackSnapshot
	if err := json.Unmarshal(job.SourceSnapshot, &snapshot); err != nil {
		writeError(w, http.StatusInternalServerError, "planning job snapshot is invalid")
		return
	}
	issue, err := h.Queries.GetIssue(r.Context(), job.IssueID)
	if err != nil || issue.WorkspaceID != workspaceID {
		writeError(w, http.StatusNotFound, "planning issue not found")
		return
	}
	candidates, err := h.Queries.ListSubissuePlanAgentCandidates(r.Context(), db.ListSubissuePlanAgentCandidatesParams{
		WorkspaceID: workspaceID,
		IssueID:     issue.ID,
		ProjectID:   issue.ProjectID,
	})
	if err != nil {
		candidates = nil
	}
	job, task, selectedAgent, err := h.dispatchSubissuePlanJob(r.Context(), issue, job, snapshot, candidates)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to dispatch planning retry")
		return
	}
	if task.ID.Valid {
		writeJSON(w, http.StatusAccepted, acceptedSubissuePlanJob(jobWithTask(job, selectedAgent, task)))
		return
	}
	plans := service.BuildRuleBasedSubissuePlan(snapshot.SourceIssue, snapshot.Discussion, snapshot.HumanConstraints)
	result, _ := json.Marshal(struct {
		Plans []service.SubissuePlan `json:"plans"`
	}{Plans: plans})
	job, err = h.Queries.CompleteSubissuePlanJob(r.Context(), db.CompleteSubissuePlanJobParams{
		ID: job.ID, Status: "succeeded", Result: result, ErrorSummary: pgtype.Text{},
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save rule planning retry")
		return
	}
	writeJSON(w, http.StatusOK, subissuePlanJobToResponse(job))
}
