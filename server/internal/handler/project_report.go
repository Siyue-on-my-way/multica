package handler

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/logger"
	"github.com/multica-ai/multica/server/internal/scheduler"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type CreateProjectReportRequest struct {
	PeriodType string    `json:"period_type"`
	RangeStart time.Time `json:"range_start"`
	RangeEnd   time.Time `json:"range_end"`
	Timezone   string    `json:"timezone"`
}

type ProjectReportResponse struct {
	ID              string          `json:"id"`
	WorkspaceID     string          `json:"workspace_id"`
	ProjectID       string          `json:"project_id"`
	PeriodType      string          `json:"period_type"`
	RangeStart      string          `json:"range_start"`
	RangeEnd        string          `json:"range_end"`
	Timezone        string          `json:"timezone"`
	GeneratedByType string          `json:"generated_by_type"`
	GeneratedByID   string          `json:"generated_by_id"`
	DataSnapshot    json.RawMessage `json:"data_snapshot"`
	Content         string          `json:"content"`
	CreatedAt       string          `json:"created_at"`
	SavedAt         *string         `json:"saved_at,omitempty"`
}

func reportHistoryToResponse(report db.ReportHistory) ProjectReportResponse {
	return ProjectReportResponse{
		ID:              uuidToString(report.ID),
		WorkspaceID:     uuidToString(report.WorkspaceID),
		ProjectID:       uuidToString(report.ProjectID),
		PeriodType:      report.PeriodType,
		RangeStart:      timestampToString(report.RangeStart),
		RangeEnd:        timestampToString(report.RangeEnd),
		Timezone:        report.Timezone,
		GeneratedByType: report.GeneratedByType,
		GeneratedByID:   uuidToString(report.GeneratedByID),
		DataSnapshot:    json.RawMessage(report.DataSnapshot),
		Content:         report.Content,
		CreatedAt:       timestampToString(report.CreatedAt),
		SavedAt:         timestampToPtr(report.SavedAt),
	}
}

type ProjectReportJobResponse struct {
	JobID     string                 `json:"job_id"`
	ReportID  string                 `json:"report_id"`
	Status    string                 `json:"status"`
	Attempt   int32                  `json:"attempt"`
	Max       int32                  `json:"max_attempts"`
	Error     *string                `json:"error_message,omitempty"`
	CreatedAt string                 `json:"created_at"`
	Report    *ProjectReportResponse `json:"report,omitempty"`
}

type ProjectReportSummaryResponse struct {
	ID              string `json:"id"`
	WorkspaceID     string `json:"workspace_id"`
	ProjectID       string `json:"project_id"`
	PeriodType      string `json:"period_type"`
	RangeStart      string `json:"range_start"`
	RangeEnd        string `json:"range_end"`
	Timezone        string `json:"timezone"`
	GeneratedByType string `json:"generated_by_type"`
	GeneratedByID   string `json:"generated_by_id"`
	CreatedAt       string `json:"created_at"`
	SavedAt         string `json:"saved_at"`
}

type ListProjectReportsResponse struct {
	Reports []ProjectReportSummaryResponse `json:"reports"`
	Total   int                            `json:"total"`
}

func reportHistoryToSummaryResponse(report db.ListProjectReportsRow) ProjectReportSummaryResponse {
	return ProjectReportSummaryResponse{
		ID:              uuidToString(report.ID),
		WorkspaceID:     uuidToString(report.WorkspaceID),
		ProjectID:       uuidToString(report.ProjectID),
		PeriodType:      report.PeriodType,
		RangeStart:      timestampToString(report.RangeStart),
		RangeEnd:        timestampToString(report.RangeEnd),
		Timezone:        report.Timezone,
		GeneratedByType: report.GeneratedByType,
		GeneratedByID:   uuidToString(report.GeneratedByID),
		CreatedAt:       timestampToString(report.CreatedAt),
		SavedAt:         timestampToString(report.SavedAt),
	}
}

// loadProjectForReports applies the workspace-level project visibility model
// used by GetProject: any workspace member can view the project and generate a
// report, while callers outside the workspace see the same 404 as a missing ID.
func (h *Handler) loadProjectForReports(w http.ResponseWriter, r *http.Request, projectID pgtype.UUID) (db.Project, db.Member, bool) {
	workspaceID := h.resolveWorkspaceID(r)
	workspaceUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return db.Project{}, db.Member{}, false
	}
	member, ok := h.workspaceMember(w, r, workspaceID)
	if !ok {
		return db.Project{}, db.Member{}, false
	}
	project, err := h.Queries.GetProjectInWorkspace(r.Context(), db.GetProjectInWorkspaceParams{
		ID:          projectID,
		WorkspaceID: workspaceUUID,
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "project not found")
		return db.Project{}, db.Member{}, false
	}
	return project, member, true
}

func (h *Handler) CreateProjectReport(w http.ResponseWriter, r *http.Request) {
	projectID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "project id")
	if !ok {
		return
	}
	var req CreateProjectReportRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.PeriodType != "daily" && req.PeriodType != "weekly" && req.PeriodType != "monthly" {
		writeError(w, http.StatusBadRequest, "period_type must be daily, weekly, or monthly")
		return
	}
	if req.Timezone == "" {
		req.Timezone = "UTC"
	}
	if _, err := time.LoadLocation(req.Timezone); err != nil {
		writeError(w, http.StatusBadRequest, "invalid timezone")
		return
	}
	if req.RangeStart.IsZero() || req.RangeEnd.IsZero() || !req.RangeStart.Before(req.RangeEnd) {
		writeError(w, http.StatusBadRequest, "range_start and range_end are required, and range_start must be earlier than range_end")
		return
	}
	project, _, ok := h.loadProjectForReports(w, r, projectID)
	if !ok {
		return
	}

	actorType, actorID := h.resolveActor(r, requestUserID(r), uuidToString(project.WorkspaceID))
	actorUUID, err := util.ParseUUID(actorID)
	if err != nil {
		writeError(w, http.StatusForbidden, "invalid caller identity")
		return
	}

	report, err := h.Queries.CreateReportHistory(r.Context(), db.CreateReportHistoryParams{
		WorkspaceID:     project.WorkspaceID,
		ProjectID:       project.ID,
		PeriodType:      req.PeriodType,
		RangeStart:      pgtype.Timestamptz{Time: req.RangeStart, Valid: true},
		RangeEnd:        pgtype.Timestamptz{Time: req.RangeEnd, Valid: true},
		Timezone:        req.Timezone,
		GeneratedByType: actorType,
		GeneratedByID:   actorUUID,
		DataSnapshot:    []byte("{}"),
		Content:         "",
	})
	if err != nil {
		slog.Error("create project report job failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to create report job")
		return
	}

	writeJSON(w, http.StatusAccepted, ProjectReportJobResponse{
		JobID:     uuidToString(report.ID),
		ReportID:  uuidToString(report.ID),
		Status:    "pending",
		Attempt:   0,
		Max:       scheduler.ReportJobMaxAttempts,
		CreatedAt: timestampToString(report.CreatedAt),
	})
}

func (h *Handler) ListProjectReports(w http.ResponseWriter, r *http.Request) {
	projectID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "project id")
	if !ok {
		return
	}
	loadedProject, _, allowed := h.loadProjectForReports(w, r, projectID)
	if !allowed {
		return
	}

	reports, err := h.Queries.ListProjectReports(r.Context(), db.ListProjectReportsParams{
		WorkspaceID: parseUUID(uuidToString(loadedProject.WorkspaceID)),
		ProjectID:   projectID,
	})
	if err != nil {
		slog.Error("list project reports failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to list project reports")
		return
	}
	responses := make([]ProjectReportSummaryResponse, 0, len(reports))
	for _, report := range reports {
		responses = append(responses, reportHistoryToSummaryResponse(report))
	}
	writeJSON(w, http.StatusOK, ListProjectReportsResponse{
		Reports: responses,
		Total:   len(responses),
	})
}

func (h *Handler) GetProjectReport(w http.ResponseWriter, r *http.Request) {
	projectID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "project id")
	if !ok {
		return
	}
	reportID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "reportId"), "report id")
	if !ok {
		return
	}
	loadedProject, _, allowed := h.loadProjectForReports(w, r, projectID)
	if !allowed {
		return
	}

	report, err := h.Queries.GetProjectReport(r.Context(), db.GetProjectReportParams{
		WorkspaceID: loadedProject.WorkspaceID,
		ID:          reportID,
		ProjectID:   projectID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "report not found")
		return
	}
	if err != nil {
		slog.Error("get project report failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to get project report")
		return
	}
	writeJSON(w, http.StatusOK, reportHistoryToResponse(report))
}

func (h *Handler) SaveProjectReport(w http.ResponseWriter, r *http.Request) {
	projectID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "project id")
	if !ok {
		return
	}
	reportID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "reportId"), "report id")
	if !ok {
		return
	}

	_, member, ok := h.loadProjectForReports(w, r, projectID)
	if !ok {
		return
	}
	if !roleAllowed(member.Role, "owner", "admin") {
		writeError(w, http.StatusForbidden, "insufficient permissions")
		return
	}

	report, err := h.Queries.SaveProjectReport(r.Context(), db.SaveProjectReportParams{
		WorkspaceID: member.WorkspaceID,
		ProjectID:   projectID,
		ID:          reportID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "report not found")
		return
	}
	if err != nil {
		slog.Error("save project report failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to save project report")
		return
	}
	writeJSON(w, http.StatusOK, reportHistoryToResponse(report))
}

func (h *Handler) GetProjectReportJob(w http.ResponseWriter, r *http.Request) {
	projectID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "project id")
	if !ok {
		return
	}
	jobID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "jobId"), "job id")
	if !ok {
		return
	}
	_, _, allowed := h.loadProjectForReports(w, r, projectID)
	if !allowed {
		return
	}

	report, err := h.Queries.GetReportForJob(r.Context(), db.GetReportForJobParams{
		ID:        jobID,
		ProjectID: projectID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "report job not found")
		return
	}
	if err != nil {
		slog.Error("get project report job failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to get report job")
		return
	}

	execution, err := h.Queries.GetReportJobExecution(r.Context(), db.GetReportJobExecutionParams{
		JobName:   scheduler.JobNameProjectReportGenerate,
		ScopeKind: scheduler.ScopeKindProjectReport,
		ScopeID:   uuidToString(jobID),
		PlanTime:  report.CreatedAt,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		execution = db.GetReportJobExecutionRow{Status: "PENDING", MaxAttempts: scheduler.ReportJobMaxAttempts}
	} else if err != nil {
		slog.Error("get project report execution failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to get report job")
		return
	}

	status := reportJobStatus(report.Content, execution.Status)
	response := ProjectReportJobResponse{
		JobID:     uuidToString(jobID),
		ReportID:  uuidToString(jobID),
		Status:    status,
		Attempt:   execution.Attempt,
		Max:       execution.MaxAttempts,
		Error:     textToPtr(execution.ErrorMsg),
		CreatedAt: timestampToString(report.CreatedAt),
	}
	if status == "succeeded" {
		reportResponse := reportHistoryToResponse(report)
		response.Report = &reportResponse
	}
	writeJSON(w, http.StatusOK, response)
}

func reportJobStatus(content, executionStatus string) string {
	if content != "" {
		if executionStatus == "RUNNING" {
			return "running"
		}
		return "succeeded"
	}
	switch executionStatus {
	case "RUNNING":
		return "running"
	case "FAILED":
		return "failed"
	case "SUCCESS":
		return "failed"
	default:
		return "pending"
	}
}
