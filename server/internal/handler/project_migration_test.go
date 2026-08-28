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

// createMigrationWorkspace creates a disposable target workspace. The caller
// can deliberately omit membership to exercise the target authorization gate.
func createMigrationWorkspace(t *testing.T, member bool) string {
	t.Helper()
	ctx := context.Background()
	slug := fmt.Sprintf("project-migration-%d", time.Now().UnixNano())
	var workspaceID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO workspace (name, slug, issue_prefix)
		VALUES ($1, $2, 'MIG')
		RETURNING id`, slug, slug).Scan(&workspaceID); err != nil {
		t.Fatalf("create migration workspace: %v", err)
	}
	if member {
		if _, err := testPool.Exec(ctx, `
			INSERT INTO member (workspace_id, user_id, role)
			VALUES ($1, $2, 'owner')`, workspaceID, testUserID); err != nil {
			t.Fatalf("add migration workspace member: %v", err)
		}
	}
	return workspaceID
}

func cleanupMigrationFixture(t *testing.T, workspaceID, projectID, labelID, propertyID string) {
	t.Helper()
	ctx := context.Background()
	if workspaceID != "" {
		if _, err := testPool.Exec(ctx, `DELETE FROM issue_property WHERE workspace_id = $1`, workspaceID); err != nil {
			t.Errorf("cleanup migrated workspace properties: %v", err)
		}
		if _, err := testPool.Exec(ctx, `DELETE FROM workspace WHERE id = $1`, workspaceID); err != nil {
			t.Errorf("cleanup migration workspace: %v", err)
		}
	}
	if projectID != "" {
		if _, err := testPool.Exec(ctx, `DELETE FROM project WHERE id = $1`, projectID); err != nil {
			t.Errorf("cleanup migration project: %v", err)
		}
	}
	if labelID != "" {
		if _, err := testPool.Exec(ctx, `DELETE FROM issue_label WHERE id = $1`, labelID); err != nil {
			t.Errorf("cleanup migration label: %v", err)
		}
	}
	if propertyID != "" {
		if _, err := testPool.Exec(ctx, `DELETE FROM issue_property WHERE id = $1`, propertyID); err != nil {
			t.Errorf("cleanup migration property: %v", err)
		}
	}
}

func TestMigrateProjectMovesScopedDataAndRemapsDefinitions(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	targetWorkspaceID := createMigrationWorkspace(t, true)
	var projectID, labelID, propertyID string
	var originalSourceIssueCounter int
	if err := testPool.QueryRow(ctx, `SELECT issue_counter FROM workspace WHERE id = $1`, testWorkspaceID).Scan(&originalSourceIssueCounter); err != nil {
		t.Fatalf("load original source issue counter: %v", err)
	}
	defer func() {
		cleanupMigrationFixture(t, targetWorkspaceID, projectID, labelID, propertyID)
		if _, err := testPool.Exec(context.Background(), `UPDATE workspace SET issue_counter = $1 WHERE id = $2`, originalSourceIssueCounter, testWorkspaceID); err != nil {
			t.Errorf("restore source issue counter: %v", err)
		}
	}()

	if err := testPool.QueryRow(ctx, `
		INSERT INTO project (workspace_id, title, status, priority, lead_type, lead_id)
		VALUES ($1, 'Migrated project', 'in_progress', 'high', 'member', $2)
		RETURNING id`, testWorkspaceID, testUserID).Scan(&projectID); err != nil {
		t.Fatalf("create migration project: %v", err)
	}

	if err := testPool.QueryRow(ctx, `
		INSERT INTO issue_label (workspace_id, resource_type, name, description, color)
		VALUES ($1, 'issue', 'Migration label', 'source label', '#123456')
		RETURNING id`, testWorkspaceID).Scan(&labelID); err != nil {
		t.Fatalf("create migration label: %v", err)
	}
	if err := testPool.QueryRow(ctx, `
		INSERT INTO issue_property (workspace_id, name, type, description, icon, config, position)
		VALUES ($1, 'Migration property', 'text', 'source property', 'tag', '{}'::jsonb, 3)
		RETURNING id`, testWorkspaceID).Scan(&propertyID); err != nil {
		t.Fatalf("create migration property: %v", err)
	}
	var sourceIssueCounter int
	if err := testPool.QueryRow(ctx, `
		SELECT GREATEST(issue_counter, COALESCE((SELECT MAX(number) FROM issue WHERE workspace_id = $1), 0))
		FROM workspace WHERE id = $1`, testWorkspaceID).Scan(&sourceIssueCounter); err != nil {
		t.Fatalf("load source issue counter: %v", err)
	}
	parentNumber := sourceIssueCounter + 1
	childNumber := sourceIssueCounter + 2

	var parentIssueID string
	parentProperties, err := json.Marshal(map[string]string{propertyID: "preserved value"})
	if err != nil {
		t.Fatalf("marshal migration properties: %v", err)
	}
	if err := testPool.QueryRow(ctx, `
		INSERT INTO issue (
			workspace_id, title, status, priority, assignee_type, assignee_id,
			creator_type, creator_id, number, project_id, properties
		)
		VALUES ($1, 'Parent issue', 'todo', 'medium', 'member', $2,
			'member', $2, $3, $4, $5::jsonb)
		RETURNING id`, testWorkspaceID, testUserID, parentNumber, projectID, parentProperties).Scan(&parentIssueID); err != nil {
		t.Fatalf("create parent issue: %v", err)
	}
	var childIssueID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO issue (
			workspace_id, title, status, priority, assignee_type, assignee_id,
			creator_type, creator_id, parent_issue_id, number, project_id
		)
		VALUES ($1, 'Child issue', 'in_review', 'low', 'member', $2,
			'member', $2, $3, $4, $5)
		RETURNING id`, testWorkspaceID, testUserID, parentIssueID, childNumber, projectID).Scan(&childIssueID); err != nil {
		t.Fatalf("create child issue: %v", err)
	}
	if _, err := testPool.Exec(ctx, `UPDATE workspace SET issue_counter = $1 WHERE id = $2`, childNumber, testWorkspaceID); err != nil {
		t.Fatalf("set source issue counter: %v", err)
	}

	if _, err := testPool.Exec(ctx, `INSERT INTO issue_to_label (issue_id, label_id) VALUES ($1, $2)`, parentIssueID, labelID); err != nil {
		t.Fatalf("attach migration label: %v", err)
	}
	var commentID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO comment (issue_id, workspace_id, author_type, author_id, content)
		VALUES ($1, $2, 'member', $3, 'historical comment')
		RETURNING id`, parentIssueID, testWorkspaceID, testUserID).Scan(&commentID); err != nil {
		t.Fatalf("create migration comment: %v", err)
	}
	if _, err := testPool.Exec(ctx, `
		INSERT INTO comment_reaction (comment_id, workspace_id, actor_type, actor_id, emoji)
		VALUES ($1, $2, 'member', $3, 'thumbsup')`, commentID, testWorkspaceID, testUserID); err != nil {
		t.Fatalf("create comment reaction: %v", err)
	}
	if _, err := testPool.Exec(ctx, `
		INSERT INTO issue_reaction (issue_id, workspace_id, actor_type, actor_id, emoji)
		VALUES ($1, $2, 'member', $3, 'heart')`, parentIssueID, testWorkspaceID, testUserID); err != nil {
		t.Fatalf("create issue reaction: %v", err)
	}
	if _, err := testPool.Exec(ctx, `
		INSERT INTO attachment (
			workspace_id, issue_id, uploader_type, uploader_id, filename,
			url, content_type, size_bytes
		) VALUES ($1, $2, 'member', $3, 'history.txt', 'https://example.test/history.txt', 'text/plain', 12)`,
		testWorkspaceID, parentIssueID, testUserID); err != nil {
		t.Fatalf("create migration attachment: %v", err)
	}
	if _, err := testPool.Exec(ctx, `
		INSERT INTO pinned_item (workspace_id, user_id, item_type, item_id)
		VALUES ($1, $2, 'project', $3), ($1, $2, 'issue', $4)`,
		testWorkspaceID, testUserID, projectID, parentIssueID); err != nil {
		t.Fatalf("create migration pins: %v", err)
	}
	if _, err := testPool.Exec(ctx, `
		INSERT INTO activity_log (workspace_id, issue_id, actor_type, actor_id, action, details)
		VALUES ($1, $2, 'member', $3, 'migration_test', '{}'::jsonb)`,
		testWorkspaceID, parentIssueID, testUserID); err != nil {
		t.Fatalf("create migration activity: %v", err)
	}
	if _, err := testPool.Exec(ctx, `
		INSERT INTO inbox_item (
			workspace_id, recipient_type, recipient_id, type, issue_id, title, body
		) VALUES ($1, 'member', $2, 'migration_test', $3, 'Migration notification', 'body')`,
		testWorkspaceID, testUserID, parentIssueID); err != nil {
		t.Fatalf("create migration notification: %v", err)
	}
	if _, err := testPool.Exec(ctx, `
		INSERT INTO project_resource (
			project_id, workspace_id, resource_type, resource_ref, created_by
		) VALUES ($1, $2, 'url', '{"url":"https://example.test/project"}'::jsonb, $3)`,
		projectID, testWorkspaceID, testUserID); err != nil {
		t.Fatalf("create migration project resource: %v", err)
	}

	// Seed a target issue so the migration must allocate a new number range.
	if _, err := testPool.Exec(ctx, `
		INSERT INTO issue (
			workspace_id, title, status, priority, creator_type, creator_id, number
		) VALUES ($1, 'Existing target issue', 'todo', 'none', 'member', $2, 7)`,
		targetWorkspaceID, testUserID); err != nil {
		t.Fatalf("create target issue: %v", err)
	}
	if _, err := testPool.Exec(ctx, `UPDATE workspace SET issue_counter = 7 WHERE id = $1`, targetWorkspaceID); err != nil {
		t.Fatalf("set target issue counter: %v", err)
	}

	w := httptest.NewRecorder()
	req := withURLParam(newRequest(http.MethodPut, "/api/projects/"+projectID+"/migrate", map[string]any{
		"target_workspace_id": targetWorkspaceID,
	}), "id", projectID)
	testHandler.MigrateProject(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("MigrateProject: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var response ProjectResponse
	if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
		t.Fatalf("decode migration response: %v", err)
	}
	if response.WorkspaceID != targetWorkspaceID || response.LeadID != nil || response.LeadType != nil {
		t.Fatalf("migration response = %+v, want target workspace with cleared lead", response)
	}

	var projectWorkspace, leadType, leadID string
	if err := testPool.QueryRow(ctx, `
		SELECT workspace_id::text, COALESCE(lead_type, ''), COALESCE(lead_id::text, '')
		FROM project WHERE id = $1`, projectID).Scan(&projectWorkspace, &leadType, &leadID); err != nil {
		t.Fatalf("load migrated project: %v", err)
	}
	if projectWorkspace != targetWorkspaceID || leadType != "" || leadID != "" {
		t.Fatalf("project workspace/lead = %s/%s/%s, want %s//", projectWorkspace, leadType, leadID, targetWorkspaceID)
	}

	var parentWorkspace, parentAssigneeType, parentAssigneeID string
	var migratedParentNumber int
	if err := testPool.QueryRow(ctx, `
		SELECT workspace_id::text, COALESCE(assignee_type, ''), COALESCE(assignee_id::text, ''), number
		FROM issue WHERE id = $1`, parentIssueID).
		Scan(&parentWorkspace, &parentAssigneeType, &parentAssigneeID, &migratedParentNumber); err != nil {
		t.Fatalf("load migrated parent issue: %v", err)
	}
	if parentWorkspace != targetWorkspaceID || parentAssigneeType != "" || parentAssigneeID != "" || migratedParentNumber != 8 {
		t.Fatalf("parent issue workspace/assignee/number = %s/%s/%s/%d, want %s///8", parentWorkspace, parentAssigneeType, parentAssigneeID, migratedParentNumber, targetWorkspaceID)
	}
	var childWorkspace, childAssigneeType, childAssigneeID string
	var migratedChildNumber int
	var childParentID string
	if err := testPool.QueryRow(ctx, `
		SELECT workspace_id::text, COALESCE(assignee_type, ''), COALESCE(assignee_id::text, ''), number, parent_issue_id::text
		FROM issue WHERE id = $1`, childIssueID).
		Scan(&childWorkspace, &childAssigneeType, &childAssigneeID, &migratedChildNumber, &childParentID); err != nil {
		t.Fatalf("load migrated child issue: %v", err)
	}
	if childWorkspace != targetWorkspaceID || childAssigneeType != "" || childAssigneeID != "" || migratedChildNumber != 9 || childParentID != parentIssueID {
		t.Fatalf("child issue workspace/assignee/number/parent = %s/%s/%s/%d/%s, want %s///9/%s", childWorkspace, childAssigneeType, childAssigneeID, migratedChildNumber, childParentID, targetWorkspaceID, parentIssueID)
	}

	var targetCounter int
	if err := testPool.QueryRow(ctx, `SELECT issue_counter FROM workspace WHERE id = $1`, targetWorkspaceID).Scan(&targetCounter); err != nil {
		t.Fatalf("load target issue counter: %v", err)
	}
	if targetCounter != 9 {
		t.Fatalf("target issue counter = %d, want 9", targetCounter)
	}

	var targetLabelID string
	if err := testPool.QueryRow(ctx, `
		SELECT l.id::text
		FROM issue_to_label il
		JOIN issue_label l ON l.id = il.label_id
		WHERE il.issue_id = $1`, parentIssueID).Scan(&targetLabelID); err != nil {
		t.Fatalf("load remapped issue label: %v", err)
	}
	if targetLabelID == labelID {
		t.Fatal("issue label reference still points at the source definition")
	}
	var targetLabelWorkspace string
	if err := testPool.QueryRow(ctx, `SELECT workspace_id::text FROM issue_label WHERE id = $1`, targetLabelID).Scan(&targetLabelWorkspace); err != nil {
		t.Fatalf("load target label workspace: %v", err)
	}
	if targetLabelWorkspace != targetWorkspaceID {
		t.Fatalf("target label workspace = %s, want %s", targetLabelWorkspace, targetWorkspaceID)
	}

	var targetPropertyID string
	if err := testPool.QueryRow(ctx, `
		SELECT id::text FROM issue_property
		WHERE workspace_id = $1 AND name = 'Migration property'`, targetWorkspaceID).Scan(&targetPropertyID); err != nil {
		t.Fatalf("load target property: %v", err)
	}
	if targetPropertyID == propertyID {
		t.Fatal("target property reused the source definition UUID")
	}
	var migratedProperties []byte
	if err := testPool.QueryRow(ctx, `SELECT properties FROM issue WHERE id = $1`, parentIssueID).Scan(&migratedProperties); err != nil {
		t.Fatalf("load migrated issue properties: %v", err)
	}
	var propertyValues map[string]string
	if err := json.Unmarshal(migratedProperties, &propertyValues); err != nil {
		t.Fatalf("decode migrated issue properties: %v", err)
	}
	if propertyValues[targetPropertyID] != "preserved value" {
		t.Fatalf("migrated properties = %#v, want only target property key", propertyValues)
	}
	if _, sourceKeyPresent := propertyValues[propertyID]; sourceKeyPresent {
		t.Fatalf("migrated properties still contain source property key: %#v", propertyValues)
	}

	for _, check := range []struct {
		name  string
		query string
	}{
		{"comments", `SELECT COUNT(*) FROM comment WHERE issue_id IN ($1, $2) AND workspace_id = $3`},
		{"comment reactions", `SELECT COUNT(*) FROM comment_reaction WHERE comment_id = $1 AND workspace_id = $2`},
		{"issue reactions", `SELECT COUNT(*) FROM issue_reaction WHERE issue_id = $1 AND workspace_id = $2`},
		{"attachments", `SELECT COUNT(*) FROM attachment WHERE issue_id = $1 AND workspace_id = $2`},
		{"pins", `SELECT COUNT(*) FROM pinned_item WHERE workspace_id = $1 AND item_id IN ($2, $3)`},
		{"activity", `SELECT COUNT(*) FROM activity_log WHERE issue_id = $1 AND workspace_id = $2`},
		{"notifications", `SELECT COUNT(*) FROM inbox_item WHERE issue_id = $1 AND workspace_id = $2`},
		{"resources", `SELECT COUNT(*) FROM project_resource WHERE project_id = $1 AND workspace_id = $2`},
	} {
		var got int
		var err error
		switch check.name {
		case "comments":
			err = testPool.QueryRow(ctx, check.query, parentIssueID, childIssueID, targetWorkspaceID).Scan(&got)
		case "pins":
			err = testPool.QueryRow(ctx, check.query, targetWorkspaceID, projectID, parentIssueID).Scan(&got)
		case "resources":
			err = testPool.QueryRow(ctx, check.query, projectID, targetWorkspaceID).Scan(&got)
		default:
			err = testPool.QueryRow(ctx, check.query, func() string {
				if check.name == "comment reactions" {
					return commentID
				}
				return parentIssueID
			}(), targetWorkspaceID).Scan(&got)
		}
		if err != nil {
			t.Fatalf("count migrated %s: %v", check.name, err)
		}
		want := 1
		if check.name == "pins" {
			want = 2
		} else if check.name == "comments" {
			want = 1
		}
		if got != want {
			t.Errorf("migrated %s count = %d, want %d", check.name, got, want)
		}
	}

	var sourceIssueCount, sourceProjectCount int
	if err := testPool.QueryRow(ctx, `SELECT COUNT(*) FROM issue WHERE project_id = $1 AND workspace_id = $2`, projectID, testWorkspaceID).Scan(&sourceIssueCount); err != nil {
		t.Fatalf("count source issues: %v", err)
	}
	if err := testPool.QueryRow(ctx, `SELECT COUNT(*) FROM project WHERE id = $1 AND workspace_id = $2`, projectID, testWorkspaceID).Scan(&sourceProjectCount); err != nil {
		t.Fatalf("count source project: %v", err)
	}
	if sourceIssueCount != 0 || sourceProjectCount != 0 {
		t.Fatalf("source still contains project/issues: project=%d issues=%d", sourceProjectCount, sourceIssueCount)
	}
}

func TestMigrateProjectRejectsInaccessibleTarget(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	targetWorkspaceID := createMigrationWorkspace(t, false)
	var projectID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO project (workspace_id, title)
		VALUES ($1, 'Authorization migration project')
		RETURNING id`, testWorkspaceID).Scan(&projectID); err != nil {
		t.Fatalf("create authorization project: %v", err)
	}
	defer cleanupMigrationFixture(t, targetWorkspaceID, projectID, "", "")

	w := httptest.NewRecorder()
	req := withURLParam(newRequest(http.MethodPut, "/api/projects/"+projectID+"/migrate", map[string]any{
		"target_workspace_id": targetWorkspaceID,
	}), "id", projectID)
	testHandler.MigrateProject(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("inaccessible target: expected 404, got %d: %s", w.Code, w.Body.String())
	}
	var workspaceID string
	if err := testPool.QueryRow(ctx, `SELECT workspace_id::text FROM project WHERE id = $1`, projectID).Scan(&workspaceID); err != nil {
		t.Fatalf("load project after denied migration: %v", err)
	}
	if workspaceID != testWorkspaceID {
		t.Fatalf("denied migration moved project to %s", workspaceID)
	}
}

func TestMigrateProjectRollsBackOnNumberOverflow(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	targetWorkspaceID := createMigrationWorkspace(t, true)
	var projectID, issueID string
	defer func() {
		cleanupMigrationFixture(t, targetWorkspaceID, projectID, "", "")
	}()
	if err := testPool.QueryRow(ctx, `
		INSERT INTO project (workspace_id, title)
		VALUES ($1, 'Rollback migration project')
		RETURNING id`, testWorkspaceID).Scan(&projectID); err != nil {
		t.Fatalf("create rollback project: %v", err)
	}
	var sourceIssueNumber int
	if err := testPool.QueryRow(ctx, `
		SELECT GREATEST(issue_counter, COALESCE((SELECT MAX(number) FROM issue WHERE workspace_id = $1), 0)) + 1
		FROM workspace WHERE id = $1`, testWorkspaceID).Scan(&sourceIssueNumber); err != nil {
		t.Fatalf("load rollback issue number: %v", err)
	}
	if err := testPool.QueryRow(ctx, `
		INSERT INTO issue (
			workspace_id, title, status, priority, assignee_type, assignee_id,
			creator_type, creator_id, number, project_id
		) VALUES ($1, 'Rollback issue', 'todo', 'none', 'member', $2,
			'member', $2, $3, $4)
		RETURNING id`, testWorkspaceID, testUserID, sourceIssueNumber, projectID).Scan(&issueID); err != nil {
		t.Fatalf("create rollback issue: %v", err)
	}
	if _, err := testPool.Exec(ctx, `UPDATE workspace SET issue_counter = 2147483647 WHERE id = $1`, targetWorkspaceID); err != nil {
		t.Fatalf("set overflow target counter: %v", err)
	}

	w := httptest.NewRecorder()
	req := withURLParam(newRequest(http.MethodPut, "/api/projects/"+projectID+"/migrate", map[string]any{
		"target_workspace_id": targetWorkspaceID,
	}), "id", projectID)
	testHandler.MigrateProject(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("overflow migration: expected 500, got %d: %s", w.Code, w.Body.String())
	}
	var projectWorkspace, issueWorkspace string
	if err := testPool.QueryRow(ctx, `SELECT workspace_id::text FROM project WHERE id = $1`, projectID).Scan(&projectWorkspace); err != nil {
		t.Fatalf("load rollback project: %v", err)
	}
	if err := testPool.QueryRow(ctx, `SELECT workspace_id::text FROM issue WHERE id = $1`, issueID).Scan(&issueWorkspace); err != nil {
		t.Fatalf("load rollback issue: %v", err)
	}
	if projectWorkspace != testWorkspaceID || issueWorkspace != testWorkspaceID {
		t.Fatalf("failed migration left partial state: project=%s issue=%s", projectWorkspace, issueWorkspace)
	}
}
