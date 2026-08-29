package service

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func newProjectAggregatePool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		databaseURL = "postgres://multica:multica@localhost:5432/multica?sslmode=disable"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Skipf("database unavailable: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Skipf("database unreachable: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

type projectAggregateFixture struct {
	project     db.Project
	identifiers map[string]string
}

func seedProjectAggregateFixture(t *testing.T, pool *pgxpool.Pool) projectAggregateFixture {
	t.Helper()
	ctx := context.Background()
	unique := time.Now().UnixNano()

	var userID string
	err := pool.QueryRow(ctx, `
		INSERT INTO "user" (name, email)
		VALUES ($1, $2)
		RETURNING id
	`, "Project Aggregate Tests", fmt.Sprintf("project-aggregate-%d@multica.test", unique)).Scan(&userID)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	var workspaceID string
	err = pool.QueryRow(ctx, `
		INSERT INTO workspace (name, slug, issue_prefix)
		VALUES ($1, $2, 'AGG')
		RETURNING id
	`, "Project Aggregate Tests", fmt.Sprintf("project-aggregate-%d", unique)).Scan(&workspaceID)
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO member (workspace_id, user_id, role)
		VALUES ($1, $2, 'owner')
	`, workspaceID, userID); err != nil {
		t.Fatalf("create member: %v", err)
	}

	var projectID string
	err = pool.QueryRow(ctx, `
		INSERT INTO project (workspace_id, title)
		VALUES ($1, 'Aggregate fixture')
		RETURNING id
	`, workspaceID).Scan(&projectID)
	if err != nil {
		t.Fatalf("create project: %v", err)
	}

	fixture := projectAggregateFixture{identifiers: map[string]string{}}
	projectUUID, err := util.ParseUUID(projectID)
	if err != nil {
		t.Fatalf("parse project id: %v", err)
	}
	workspaceUUID, err := util.ParseUUID(workspaceID)
	if err != nil {
		t.Fatalf("parse workspace id: %v", err)
	}
	fixture.project = db.Project{ID: projectUUID, WorkspaceID: workspaceUUID}

	createdIDs := map[string]string{}
	createIssue := func(name, title, status string, dueDate *time.Time) string {
		t.Helper()
		var parentIssueID any
		if parentID, ok := createdIDs["in_progress_parent"]; ok {
			parentIssueID = parentID
		}
		var issueNumber int
		var issueID string
		err := pool.QueryRow(ctx, `
			UPDATE workspace
			SET issue_counter = issue_counter + 1
			WHERE id = $1
			RETURNING issue_counter
		`, workspaceID).Scan(&issueNumber)
		if err != nil {
			t.Fatalf("increment issue counter: %v", err)
		}
		err = pool.QueryRow(ctx, `
			INSERT INTO issue (
				workspace_id, title, status, priority, creator_type, creator_id,
				project_id, parent_issue_id, due_date, number
			)
			VALUES ($1, $2, $3, 'none', 'member', $4, $5, $6, $7, $8)
			RETURNING id
		`, workspaceID, title, status, userID, projectID, parentIssueID, dueDate, issueNumber).Scan(&issueID)
		if err != nil {
			t.Fatalf("create issue %s: %v", name, err)
		}
		createdIDs[name] = issueID
		fixture.identifiers[name] = fmt.Sprintf("AGG-%d", issueNumber)
		return issueID
	}
	addHistory := func(issueID, fromStatus, toStatus string, changedAt time.Time) {
		t.Helper()
		if _, err := pool.Exec(ctx, `
			INSERT INTO issue_status_history (
				issue_id, workspace_id, from_status, to_status, changed_at, changed_by_type
			)
			VALUES ($1, $2, $3, $4, $5, 'system')
		`, issueID, workspaceID, fromStatus, toStatus, changedAt); err != nil {
			t.Fatalf("create status history: %v", err)
		}
	}

	rangeStart := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	rangeEnd := time.Date(2026, 8, 8, 0, 0, 0, 0, time.UTC)
	asOf := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	yesterday := asOf.AddDate(0, 0, -1)
	today := asOf

	completedWindow := createIssue("completed_window", "Completed in window", "done", nil)
	addHistory(completedWindow, "in_progress", "done", rangeStart.Add(24*time.Hour))
	addHistory(completedWindow, "done", "in_progress", rangeStart.Add(48*time.Hour))
	addHistory(completedWindow, "in_progress", "done", rangeStart.Add(72*time.Hour))

	completedOld := createIssue("completed_old", "Completed before window", "done", nil)
	addHistory(completedOld, "in_progress", "done", rangeStart.Add(-24*time.Hour))

	reopened := createIssue("reopened", "Completed then reopened", "in_progress", nil)
	addHistory(reopened, "in_progress", "done", rangeStart.Add(24*time.Hour))
	addHistory(reopened, "done", "in_progress", rangeStart.Add(48*time.Hour))

	completedBoundary := createIssue("completed_boundary", "Completed at exclusive end", "done", nil)
	addHistory(completedBoundary, "in_progress", "done", rangeEnd)

	createIssue("in_progress_parent", "In-progress parent", "in_progress", nil)
	createIssue("in_progress_child", "In-progress child", "in_progress", nil)

	createIssue("blocked", "Blocked", "blocked", nil)

	cancelledWindow := createIssue("cancelled_window", "Cancelled in window", "cancelled", nil)
	addHistory(cancelledWindow, "in_progress", "cancelled", rangeStart.Add(24*time.Hour))

	cancelledOld := createIssue("cancelled_old", "Cancelled before window", "cancelled", nil)
	addHistory(cancelledOld, "in_progress", "cancelled", rangeStart.Add(-24*time.Hour))

	createIssue("overdue", "Overdue", "in_review", &yesterday)
	createIssue("due_today", "Due today", "todo", &today)
	createIssue("done_overdue", "Completed overdue", "done", &yesterday)

	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(), `
			DELETE FROM issue_status_history
			WHERE workspace_id = $1
		`, workspaceID); err != nil {
			t.Errorf("delete status history: %v", err)
		}
		if _, err := pool.Exec(context.Background(), `
			DELETE FROM workspace WHERE id = $1
		`, workspaceID); err != nil {
			t.Errorf("delete workspace: %v", err)
		}
		if _, err := pool.Exec(context.Background(), `
			DELETE FROM "user" WHERE id = $1
		`, userID); err != nil {
			t.Errorf("delete user: %v", err)
		}
	})

	return fixture
}

func identifiers(issues []ReportIssue) []string {
	values := make([]string, 0, len(issues))
	for _, issue := range issues {
		values = append(values, issue.Identifier)
	}
	return values
}

func sameIdentifiers(values []string, expected ...string) bool {
	if len(values) != len(expected) {
		return false
	}
	seen := make(map[string]int, len(values))
	for _, value := range values {
		seen[value]++
	}
	for _, value := range expected {
		seen[value]--
		if seen[value] < 0 {
			return false
		}
	}
	return true
}

func TestProjectIssueAggregator(t *testing.T) {
	pool := newProjectAggregatePool(t)
	fixture := seedProjectAggregateFixture(t, pool)
	aggregator := ProjectIssueAggregator{Queries: db.New(pool)}

	rangeStart := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	rangeEnd := time.Date(2026, 8, 8, 0, 0, 0, 0, time.UTC)
	asOf := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	result, err := aggregator.Aggregate(context.Background(), fixture.project, rangeStart, rangeEnd, asOf)
	if err != nil {
		t.Fatalf("aggregate: %v", err)
	}

	if !sameIdentifiers(identifiers(result.Completed), fixture.identifiers["completed_window"]) {
		t.Fatalf("completed = %v, want [%s]", result.Completed, fixture.identifiers["completed_window"])
	}
	if !sameIdentifiers(identifiers(result.InProgress),
		fixture.identifiers["reopened"],
		fixture.identifiers["in_progress_parent"],
		fixture.identifiers["in_progress_child"],
	) {
		t.Fatalf("in progress = %v, want reopened, parent, and child", result.InProgress)
	}
	if !sameIdentifiers(identifiers(result.Blocked), fixture.identifiers["blocked"]) {
		t.Fatalf("blocked = %v, want [%s]", result.Blocked, fixture.identifiers["blocked"])
	}
	if !sameIdentifiers(identifiers(result.Overdue), fixture.identifiers["overdue"]) {
		t.Fatalf("overdue = %v, want [%s]", result.Overdue, fixture.identifiers["overdue"])
	}
	if !sameIdentifiers(identifiers(result.Cancelled), fixture.identifiers["cancelled_window"]) {
		t.Fatalf("cancelled = %v, want [%s]", result.Cancelled, fixture.identifiers["cancelled_window"])
	}

	counts := map[string]int{
		"completed":   result.CompletedCount,
		"in_progress": result.InProgressCount,
		"blocked":     result.BlockedCount,
		"overdue":     result.OverdueCount,
		"cancelled":   result.CancelledCount,
	}
	expectedCounts := map[string]int{"completed": 1, "in_progress": 3, "blocked": 1, "overdue": 1, "cancelled": 1}
	for category, count := range expectedCounts {
		if counts[category] != count {
			t.Fatalf("%s count = %d, want %d", category, counts[category], count)
		}
	}

	nextStart := rangeEnd
	nextEnd := rangeEnd.AddDate(0, 0, 7)
	nextResult, err := aggregator.Aggregate(context.Background(), fixture.project, nextStart, nextEnd, asOf)
	if err != nil {
		t.Fatalf("aggregate next window: %v", err)
	}
	if !sameIdentifiers(identifiers(nextResult.Completed), fixture.identifiers["completed_boundary"]) {
		t.Fatalf("next completed = %v, want [%s]", nextResult.Completed, fixture.identifiers["completed_boundary"])
	}
	if nextResult.CompletedCount != 1 {
		t.Fatalf("next completed count = %d, want 1", nextResult.CompletedCount)
	}
}

func TestProjectIssueAggregatorCollectsIssueTimelineUnion(t *testing.T) {
	pool := newProjectAggregatePool(t)
	fixture := seedProjectAggregateFixture(t, pool)
	ctx := context.Background()
	rangeStart := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	rangeEnd := time.Date(2026, 8, 8, 0, 0, 0, 0, time.UTC)

	var actorID string
	if err := pool.QueryRow(ctx, `SELECT creator_id FROM issue WHERE project_id = $1 LIMIT 1`, fixture.project.ID).Scan(&actorID); err != nil {
		t.Fatalf("load fixture actor: %v", err)
	}
	var number int
	if err := pool.QueryRow(ctx, `SELECT COALESCE(MAX(number), 0) + 1 FROM issue WHERE project_id = $1`, fixture.project.ID).Scan(&number); err != nil {
		t.Fatalf("load next issue number: %v", err)
	}
	var issueID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO issue (
			workspace_id, project_id, title, status, priority, creator_type, creator_id,
			number, position
		) VALUES ($1, $2, 'Timeline union issue', 'in_progress', 'none', 'member', $3, $4, 0)
		RETURNING id`, fixture.project.WorkspaceID, fixture.project.ID, actorID, number).Scan(&issueID); err != nil {
		t.Fatalf("create timeline issue: %v", err)
	}

	rootAt := rangeStart.Add(-24 * time.Hour)
	replyAt := rangeStart.Add(time.Hour)
	var rootID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO comment (issue_id, workspace_id, author_type, author_id, content, created_at)
		VALUES ($1, $2, 'member', $3, 'Historical root', $4)
		RETURNING id`, issueID, fixture.project.WorkspaceID, actorID, rootAt).Scan(&rootID); err != nil {
		t.Fatalf("create historical comment: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO comment (issue_id, workspace_id, author_type, author_id, content, parent_id, created_at)
		VALUES ($1, $2, 'member', $3, 'Current reply', $4, $5)`, issueID, fixture.project.WorkspaceID, actorID, rootID, replyAt); err != nil {
		t.Fatalf("create current reply: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO activity_log (workspace_id, issue_id, actor_type, actor_id, action, details, created_at)
		VALUES ($1, $2, 'member', $3, 'description_changed', '{}', $4)`, fixture.project.WorkspaceID, issueID, actorID, replyAt.Add(time.Minute)); err != nil {
		t.Fatalf("create activity: %v", err)
	}

	var agentID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO agent (workspace_id, name, runtime_mode, owner_id)
		VALUES ($1, 'Report timeline agent', 'local', $2)
		RETURNING id`, fixture.project.WorkspaceID, actorID).Scan(&agentID); err != nil {
		t.Fatalf("create agent: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO agent_task_queue (agent_id, issue_id, status, created_at, started_at, completed_at, result)
		VALUES ($1, $2, 'completed', $3, $4, $5, '{"summary":"done"}')`,
		agentID, issueID, replyAt, replyAt.Add(10*time.Minute), replyAt.Add(20*time.Minute)); err != nil {
		t.Fatalf("create task: %v", err)
	}

	aggregator := ProjectIssueAggregator{Queries: db.New(pool)}
	result, err := aggregator.Aggregate(ctx, fixture.project, rangeStart, rangeEnd, rangeEnd)
	if err != nil {
		t.Fatalf("aggregate timeline union: %v", err)
	}
	var timelineIssue *ReportIssue
	for index := range result.Issues {
		if result.Issues[index].IssueID == issueID {
			timelineIssue = &result.Issues[index]
			break
		}
	}
	if timelineIssue == nil {
		t.Fatalf("timeline issue was not selected from activity union: %+v", result.Issues)
	}
	eventTypes := map[string]int{}
	historicalComments := 0
	currentComments := 0
	for _, event := range timelineIssue.Timeline {
		eventTypes[event.Type]++
		if event.Type == "comment" && event.InRange {
			currentComments++
		}
		if event.Type == "comment" && !event.InRange {
			historicalComments++
		}
	}
	for _, eventType := range []string{"comment", "activity_log", "agent_task_queue"} {
		if eventTypes[eventType] == 0 {
			t.Fatalf("timeline missing %s event: %+v", eventType, timelineIssue.Timeline)
		}
	}
	if currentComments != 1 || historicalComments != 1 {
		t.Fatalf("historical reply context = current %d, historical %d; timeline=%+v", currentComments, historicalComments, timelineIssue.Timeline)
	}
	for _, issue := range result.Issues {
		if issue.Title == "In-progress parent" {
			t.Fatalf("inactive issue unexpectedly included in active union: %+v", issue)
		}
	}
}
