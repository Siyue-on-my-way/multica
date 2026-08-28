package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func countIssueStatusHistory(t *testing.T, issueID string) int {
	t.Helper()
	var count int
	if err := testPool.QueryRow(context.Background(),
		`SELECT count(*) FROM issue_status_history WHERE issue_id = $1`, issueID,
	).Scan(&count); err != nil {
		t.Fatalf("count issue status history: %v", err)
	}
	return count
}

func cleanupIssueWithStatusHistory(t *testing.T, issueID string) {
	t.Helper()
	t.Cleanup(func() {
		if _, err := testPool.Exec(context.Background(),
			`DELETE FROM issue_status_history WHERE issue_id = $1`, issueID,
		); err != nil {
			t.Errorf("delete issue status history: %v", err)
		}
		deleteTestIssue(t, issueID)
	})
}

func lastIssueStatusHistoryTransition(t *testing.T, issueID string) (string, string, string, string) {
	t.Helper()
	var fromStatus, toStatus, changedByType string
	var changedByID *string
	if err := testPool.QueryRow(context.Background(), `
		SELECT from_status, to_status, changed_by_type, changed_by_id::text
		FROM issue_status_history
		WHERE issue_id = $1
		ORDER BY changed_at DESC, id DESC
		LIMIT 1
	`, issueID).Scan(&fromStatus, &toStatus, &changedByType, &changedByID); err != nil {
		t.Fatalf("read latest issue status history: %v", err)
	}
	return fromStatus, toStatus, changedByType, pointerValue(changedByID)
}

func pointerValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func TestUpdateIssueRecordsOnlyRealStatusChanges(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	issueID := createTestIssue(t, "status history single", "todo", "none")
	cleanupIssueWithStatusHistory(t, issueID)
	if count := countIssueStatusHistory(t, issueID); count != 0 {
		t.Fatalf("new issue history count: expected 0, got %d", count)
	}

	w := httptest.NewRecorder()
	req := newRequest("PUT", "/api/issues/"+issueID, map[string]any{"title": "edited"})
	req = withURLParam(req, "id", issueID)
	testHandler.UpdateIssue(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("title update: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if count := countIssueStatusHistory(t, issueID); count != 0 {
		t.Fatalf("after title update: expected no history, got %d", count)
	}

	w = httptest.NewRecorder()
	req = newRequest("PUT", "/api/issues/"+issueID, map[string]any{"status": "in_progress"})
	req = withURLParam(req, "id", issueID)
	testHandler.UpdateIssue(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status update: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if count := countIssueStatusHistory(t, issueID); count != 1 {
		t.Fatalf("after first status change: expected 1, got %d", count)
	}

	w = httptest.NewRecorder()
	req = newRequest("PUT", "/api/issues/"+issueID, map[string]any{"status": "in_progress"})
	req = withURLParam(req, "id", issueID)
	testHandler.UpdateIssue(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("same-status update: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if count := countIssueStatusHistory(t, issueID); count != 1 {
		t.Fatalf("after same-status update: expected 1, got %d", count)
	}

	fromStatus, toStatus, changedByType, changedByID := lastIssueStatusHistoryTransition(t, issueID)
	if fromStatus != "todo" || toStatus != "in_progress" || changedByType != "member" || changedByID != testUserID {
		t.Fatalf("unexpected transition: %s -> %s by %s/%s", fromStatus, toStatus, changedByType, changedByID)
	}
}

func TestBatchUpdateRecordsStatusHistory(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	firstID := createTestIssue(t, "status history batch 1", "todo", "none")
	secondID := createTestIssue(t, "status history batch 2", "todo", "none")
	cleanupIssueWithStatusHistory(t, firstID)
	cleanupIssueWithStatusHistory(t, secondID)

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/issues/batch-update", map[string]any{
		"issue_ids": []string{firstID, secondID},
		"updates":   map[string]any{"status": "in_progress"},
	})
	testHandler.BatchUpdateIssues(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("batch update: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	for _, issueID := range []string{firstID, secondID} {
		if count := countIssueStatusHistory(t, issueID); count != 1 {
			t.Fatalf("issue %s history count: expected 1, got %d", issueID, count)
		}
		fromStatus, toStatus, changedByType, changedByID := lastIssueStatusHistoryTransition(t, issueID)
		if fromStatus != "todo" || toStatus != "in_progress" || changedByType != "member" || changedByID != testUserID {
			t.Fatalf("issue %s unexpected transition: %s -> %s by %s/%s", issueID, fromStatus, toStatus, changedByType, changedByID)
		}
	}
}

func TestUpdateIssueStatusRecordsSystemTransition(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	issueID := createTestIssue(t, "status history system", "todo", "none")
	cleanupIssueWithStatusHistory(t, issueID)
	issueUUID, err := util.ParseUUID(issueID)
	if err != nil {
		t.Fatalf("parse issue id: %v", err)
	}

	updated, err := testHandler.Queries.UpdateIssueStatus(context.Background(), db.UpdateIssueStatusParams{
		ID:            issueUUID,
		Status:        "done",
		WorkspaceID:   util.MustParseUUID(createTestIssueWorkspaceID(t, issueID)),
		ChangedByType: "system",
	})
	if err != nil {
		t.Fatalf("update issue status: %v", err)
	}
	if updated.Status != "done" {
		t.Fatalf("updated status: expected done, got %s", updated.Status)
	}
	if count := countIssueStatusHistory(t, issueID); count != 1 {
		t.Fatalf("system status change history count: expected 1, got %d", count)
	}

	if _, err := testHandler.Queries.UpdateIssueStatus(context.Background(), db.UpdateIssueStatusParams{
		ID:            issueUUID,
		Status:        "done",
		WorkspaceID:   util.MustParseUUID(createTestIssueWorkspaceID(t, issueID)),
		ChangedByType: "system",
	}); err != nil {
		t.Fatalf("same-status issue update: %v", err)
	}
	if count := countIssueStatusHistory(t, issueID); count != 1 {
		t.Fatalf("same-status history count: expected 1, got %d", count)
	}

	fromStatus, toStatus, changedByType, changedByID := lastIssueStatusHistoryTransition(t, issueID)
	if fromStatus != "todo" || toStatus != "done" || changedByType != "system" || changedByID != "" {
		t.Fatalf("unexpected transition: %s -> %s by %s/%s", fromStatus, toStatus, changedByType, changedByID)
	}
}

func createTestIssueWorkspaceID(t *testing.T, issueID string) string {
	t.Helper()
	var workspaceID string
	if err := testPool.QueryRow(context.Background(),
		`SELECT workspace_id::text FROM issue WHERE id = $1`, issueID,
	).Scan(&workspaceID); err != nil {
		t.Fatalf("read issue workspace: %v", err)
	}
	return workspaceID
}
