package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
)

type projectReportJobResponse struct {
	JobID    string                 `json:"job_id"`
	ReportID string                 `json:"report_id"`
	Status   string                 `json:"status"`
	Report   *ProjectReportResponse `json:"report,omitempty"`
}

type projectReportListResponse struct {
	Reports []ProjectReportSummaryResponse `json:"reports"`
	Total   int                            `json:"total"`
}

func createProjectReportForTest(t *testing.T, userID string, projectID string) string {
	t.Helper()

	w := httptest.NewRecorder()
	req := newRequestAsUser(userID, "POST", "/api/projects/"+projectID, map[string]any{
		"period_type": "daily",
		"range_start": time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC),
		"range_end":   time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC),
		"timezone":    "UTC",
	})
	req = withURLParam(req, "id", projectID)
	testHandler.CreateProjectReport(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("CreateProjectReport: expected 202, got %d: %s", w.Code, w.Body.String())
	}
	var response projectReportJobResponse
	if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
		t.Fatalf("decode CreateProjectReport: %v", err)
	}

	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM report_history WHERE id = $1`, response.ReportID)
	})
	return response.ReportID
}

func markProjectReportGenerated(t *testing.T, reportID string) {
	t.Helper()

	if _, err := testPool.Exec(context.Background(), `
		UPDATE report_history
		SET content = '## Report test', data_snapshot = '{}'::jsonb
		WHERE id = $1
	`, reportID); err != nil {
		t.Fatalf("mark report generated: %v", err)
	}
}

func TestProjectReportPermissionsForViewer(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("handler test fixture unavailable")
	}

	viewerID := createProjectPermissionTestMember(t, "member")
	project := createProjectPermissionTestProject(t, "report viewer permissions")
	reportID := createProjectReportForTest(t, viewerID, project.ID)

	w := httptest.NewRecorder()
	req := newRequestAsUser(viewerID, "GET", "/api/projects/"+project.ID+"/reports/jobs/"+reportID, nil)
	req = withURLParams(req, "id", project.ID, "jobId", reportID)
	testHandler.GetProjectReportJob(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GetProjectReportJob: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	req = newRequestAsUser(viewerID, "GET", "/api/projects/"+project.ID+"/reports", nil)
	req = withURLParam(req, "id", project.ID)
	testHandler.ListProjectReports(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("ListProjectReports: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var list projectReportListResponse
	if err := json.NewDecoder(w.Body).Decode(&list); err != nil {
		t.Fatalf("decode ListProjectReports: %v", err)
	}
	if list.Total != 0 {
		t.Fatalf("unsaved report history count = %d, want 0", list.Total)
	}

	markProjectReportGenerated(t, reportID)

	w = httptest.NewRecorder()
	req = newRequestAsUser(viewerID, "POST", "/api/projects/"+project.ID+"/reports/"+reportID+"/save", nil)
	req = withURLParams(req, "id", project.ID, "reportId", reportID)
	testHandler.SaveProjectReport(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("viewer SaveProjectReport: expected 403, got %d: %s", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	req = newRequestAsUser(testUserID, "POST", "/api/projects/"+project.ID+"/reports/"+reportID+"/save", nil)
	req = withURLParams(req, "id", project.ID, "reportId", reportID)
	testHandler.SaveProjectReport(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("admin SaveProjectReport: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	req = newRequestAsUser(viewerID, "GET", "/api/projects/"+project.ID+"/reports/"+reportID, nil)
	req = withURLParams(req, "id", project.ID, "reportId", reportID)
	testHandler.GetProjectReport(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("viewer GetProjectReport: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var report ProjectReportResponse
	if err := json.NewDecoder(w.Body).Decode(&report); err != nil {
		t.Fatalf("decode GetProjectReport: %v", err)
	}
	if report.Content != "## Report test" || report.SavedAt == nil {
		t.Fatalf("saved report = %+v, want generated content and saved_at", report)
	}

	w = httptest.NewRecorder()
	req = newRequestAsUser(viewerID, "GET", "/api/projects/"+project.ID+"/reports", nil)
	req = withURLParam(req, "id", project.ID)
	testHandler.ListProjectReports(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("saved ListProjectReports: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if err := json.NewDecoder(w.Body).Decode(&list); err != nil {
		t.Fatalf("decode saved ListProjectReports: %v", err)
	}
	if list.Total != 1 || len(list.Reports) != 1 {
		t.Fatalf("saved report history = %+v, want one row", list)
	}
}

func TestProjectReportRejectsCallerWithoutProjectAccess(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("handler test fixture unavailable")
	}

	viewerID := createProjectPermissionTestMember(t, "member")
	project := createProjectPermissionTestProject(t, "report tenant permissions")
	reportID := createProjectReportForTest(t, viewerID, project.ID)
	outsiderID := uuid.New().String()

	generate := func() *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := newRequestAsUser(outsiderID, "POST", "/api/projects/"+project.ID, map[string]any{
			"period_type": "daily",
			"range_start": time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC),
			"range_end":   time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC),
			"timezone":    "UTC",
		})
		req = withURLParam(req, "id", project.ID)
		testHandler.CreateProjectReport(w, req)
		return w
	}
	if w := generate(); w.Code != http.StatusNotFound {
		t.Fatalf("outsider CreateProjectReport: expected 404, got %d: %s", w.Code, w.Body.String())
	}

	requests := []struct {
		name    string
		method  string
		path    string
		handler func(w http.ResponseWriter, r *http.Request)
	}{
		{"job", "GET", "/reports/jobs/" + reportID, testHandler.GetProjectReportJob},
		{"list", "GET", "/reports", testHandler.ListProjectReports},
		{"detail", "GET", "/reports/" + reportID, testHandler.GetProjectReport},
		{"save", "POST", "/reports/" + reportID + "/save", testHandler.SaveProjectReport},
	}
	for _, request := range requests {
		w := httptest.NewRecorder()
		req := newRequestAsUser(outsiderID, request.method, "/api/projects/"+project.ID+request.path, nil)
		req = withURLParams(req, "id", project.ID, "jobId", reportID, "reportId", reportID)
		request.handler(w, req)
		if w.Code != http.StatusNotFound {
			t.Fatalf("outsider %s: expected 404, got %d: %s", request.name, w.Code, w.Body.String())
		}
	}
}

func TestCreateProjectReportRejectsCallerWithoutWorkspaceMembership(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("handler test fixture unavailable")
	}

	callerID := uuid.New().String()
	req := newRequestAsUser(callerID, "POST", "/api/projects/"+uuid.New().String(), map[string]any{
		"period_type": "daily",
		"range_start": time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC),
		"range_end":   time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC),
		"timezone":    "UTC",
	})
	req = withURLParam(req, "id", uuid.New().String())
	w := httptest.NewRecorder()

	testHandler.CreateProjectReport(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("CreateProjectReport status = %d, want %d", w.Code, http.StatusNotFound)
	}
}
