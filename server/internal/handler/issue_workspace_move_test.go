package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func createIssueMoveWorkspace(t *testing.T, role string) string {
	t.Helper()
	ctx := context.Background()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	var workspaceID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO workspace (name, slug, issue_prefix)
		VALUES ($1, $2, 'IMW')
		RETURNING id`, "Issue move target "+suffix, "issue-move-target-"+suffix).Scan(&workspaceID); err != nil {
		t.Fatalf("create issue move workspace: %v", err)
	}
	if role != "" {
		if _, err := testPool.Exec(ctx, `
			INSERT INTO member (workspace_id, user_id, role)
			VALUES ($1, $2, $3)`, workspaceID, testUserID, role); err != nil {
			t.Fatalf("add issue move workspace member: %v", err)
		}
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM workspace WHERE id = $1`, workspaceID)
	})
	return workspaceID
}

func createIssueMoveFixture(t *testing.T, projectID *string) (string, string) {
	t.Helper()
	ctx := context.Background()
	var issueID string
	var number int
	if err := testPool.QueryRow(ctx, `
		SELECT GREATEST(issue_counter, COALESCE((SELECT MAX(number) FROM issue WHERE workspace_id = $1), 0)) + 1
		FROM workspace WHERE id = $1`, testWorkspaceID).Scan(&number); err != nil {
		t.Fatalf("load issue move number: %v", err)
	}
	query := `
		INSERT INTO issue (workspace_id, title, description, status, priority,
			assignee_type, assignee_id, creator_type, creator_id, number, project_id)
		VALUES ($1, 'Workspace move parent', 'preserve this description', 'todo',
			'none', 'member', $2, 'member', $2, $3, $4)
		RETURNING id`
	if err := testPool.QueryRow(ctx, query, testWorkspaceID, testUserID, number, projectID).Scan(&issueID); err != nil {
		t.Fatalf("create issue move parent: %v", err)
	}
	var childID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO issue (workspace_id, title, status, priority, creator_type,
			creator_id, parent_issue_id, number)
		VALUES ($1, 'Workspace move child', 'in_review', 'high', 'member', $2,
			$3, (SELECT COALESCE(MAX(number), 0) + 1 FROM issue WHERE workspace_id = $1))
		RETURNING id`, testWorkspaceID, testUserID, issueID).Scan(&childID); err != nil {
		t.Fatalf("create issue move child: %v", err)
	}
	return issueID, childID
}

func TestMoveIssueToWorkspacePreservesTreeAndScopedData(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	targetWorkspaceID := createIssueMoveWorkspace(t, "admin")
	var sourceProjectID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO project (workspace_id, title) VALUES ($1, 'Move source project') RETURNING id`, testWorkspaceID).Scan(&sourceProjectID); err != nil {
		t.Fatalf("create source project: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM project WHERE id = $1`, sourceProjectID)
	})
	parentID, childID := createIssueMoveFixture(t, &sourceProjectID)
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM issue WHERE id IN ($1, $2)`, parentID, childID)
	})

	var commentID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO comment (issue_id, workspace_id, author_type, author_id, content)
		VALUES ($1, $2, 'member', $3, 'move comment') RETURNING id`, parentID, testWorkspaceID, testUserID).Scan(&commentID); err != nil {
		t.Fatalf("create move comment: %v", err)
	}
	if _, err := testPool.Exec(ctx, `
		INSERT INTO attachment (workspace_id, issue_id, uploader_type, uploader_id,
			filename, url, content_type, size_bytes)
		VALUES ($1, $2, 'member', $3, 'move.txt', 'https://example.test/move.txt', 'text/plain', 4)`, testWorkspaceID, parentID, testUserID); err != nil {
		t.Fatalf("create move attachment: %v", err)
	}
	if _, err := testPool.Exec(ctx, `
		INSERT INTO activity_log (workspace_id, issue_id, actor_type, actor_id, action, details)
		VALUES ($1, $2, 'member', $3, 'move_test', '{}'::jsonb)`, testWorkspaceID, parentID, testUserID); err != nil {
		t.Fatalf("create move activity: %v", err)
	}

	w := httptest.NewRecorder()
	req := withURLParam(newRequest(http.MethodPost, "/api/issues/"+parentID+"/move-workspace", map[string]any{
		"target_workspace_id": targetWorkspaceID,
		"new_project":         map[string]any{"title": "Moved project"},
	}), "id", parentID)
	testHandler.MoveIssueToWorkspace(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("MoveIssueToWorkspace: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var response map[string]any
	if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
		t.Fatalf("decode move response: %v", err)
	}
	if response["id"] != parentID {
		t.Fatalf("response id = %v, want %s", response["id"], parentID)
	}

	var parentWorkspace, childWorkspace, childParent, assigneeID string
	if err := testPool.QueryRow(ctx, `
		SELECT p.workspace_id::text, c.workspace_id::text, c.parent_issue_id::text,
			p.assignee_id::text
		FROM issue p JOIN issue c ON c.id = $2 WHERE p.id = $1`, parentID, childID).
		Scan(&parentWorkspace, &childWorkspace, &childParent, &assigneeID); err != nil {
		t.Fatalf("reload moved tree: %v", err)
	}
	if parentWorkspace != targetWorkspaceID || childWorkspace != targetWorkspaceID || childParent != parentID || assigneeID != testUserID {
		t.Fatalf("moved tree = %s/%s/%s/%s, want %s/%s/%s/%s", parentWorkspace, childWorkspace, childParent, assigneeID, targetWorkspaceID, targetWorkspaceID, parentID, testUserID)
	}
	for name, check := range map[string]struct {
		query string
		args  []any
	}{
		"comment":    {`SELECT workspace_id::text FROM comment WHERE id = $1`, []any{commentID}},
		"attachment": {`SELECT workspace_id::text FROM attachment WHERE issue_id = $1`, []any{parentID}},
		"activity":   {`SELECT workspace_id::text FROM activity_log WHERE issue_id = $1`, []any{parentID}},
	} {
		var workspaceID string
		if err := testPool.QueryRow(ctx, check.query, check.args...).Scan(&workspaceID); err != nil {
			t.Fatalf("reload moved %s: %v", name, err)
		}
		if workspaceID != targetWorkspaceID {
			t.Errorf("moved %s workspace = %s, want %s", name, workspaceID, targetWorkspaceID)
		}
	}
}

func TestMoveIssueToWorkspaceRejectsMissingSourceAndUnauthorizedTarget(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	targetWorkspaceID := createIssueMoveWorkspace(t, "")
	for _, tc := range []struct {
		name       string
		issueID    string
		wantStatus int
	}{
		{name: "missing source", issueID: "00000000-0000-0000-0000-000000000000", wantStatus: http.StatusNotFound},
		{name: "unauthorized target", issueID: testWorkspaceID, wantStatus: http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			req := withURLParam(newRequest(http.MethodPost, "/api/issues/"+tc.issueID+"/move-workspace", map[string]any{
				"target_workspace_id": targetWorkspaceID,
				"target_project_id":   nil,
				"new_project":         map[string]any{"title": "Denied project"},
			}), "id", tc.issueID)
			testHandler.MoveIssueToWorkspace(w, req)
			if w.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d: %s", w.Code, tc.wantStatus, w.Body.String())
			}
		})
	}
}

func TestMoveIssueToWorkspaceRollsBackNewProjectOnMoveFailure(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	targetWorkspaceID := createIssueMoveWorkspace(t, "admin")
	var issueID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO issue (workspace_id, title, status, priority, creator_type, creator_id, number)
		VALUES ($1, 'Rollback workspace move', 'todo', 'none', 'member', $2,
			(SELECT COALESCE(MAX(number), 0) + 1 FROM issue WHERE workspace_id = $1))
		RETURNING id`, testWorkspaceID, testUserID).Scan(&issueID); err != nil {
		t.Fatalf("create rollback issue: %v", err)
	}
	t.Cleanup(func() { _, _ = testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, issueID) })
	if _, err := testPool.Exec(ctx, `UPDATE workspace SET issue_counter = 2147483647 WHERE id = $1`, targetWorkspaceID); err != nil {
		t.Fatalf("set target counter: %v", err)
	}

	w := httptest.NewRecorder()
	req := withURLParam(newRequest(http.MethodPost, "/api/issues/"+issueID+"/move-workspace", map[string]any{
		"target_workspace_id": targetWorkspaceID,
		"new_project":         map[string]any{"title": "Must roll back"},
	}), "id", issueID)
	testHandler.MoveIssueToWorkspace(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", w.Code, w.Body.String())
	}
	var workspaceID string
	if err := testPool.QueryRow(ctx, `SELECT workspace_id::text FROM issue WHERE id = $1`, issueID).Scan(&workspaceID); err != nil {
		t.Fatalf("reload rollback issue: %v", err)
	}
	if workspaceID != testWorkspaceID {
		t.Fatalf("rollback issue workspace = %s, want %s", workspaceID, testWorkspaceID)
	}
	var projectCount int
	if err := testPool.QueryRow(ctx, `SELECT COUNT(*) FROM project WHERE workspace_id = $1 AND title = 'Must roll back'`, targetWorkspaceID).Scan(&projectCount); err != nil {
		t.Fatalf("count rolled back projects: %v", err)
	}
	if projectCount != 0 {
		t.Fatalf("new project survived rollback: %d", projectCount)
	}
}
