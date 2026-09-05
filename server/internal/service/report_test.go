package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type stubReportLLM struct {
	mu       sync.Mutex
	prompt   string
	response string
	err      error
}

func (s *stubReportLLM) Enabled() bool { return true }

func (s *stubReportLLM) GenerateJSON(_ context.Context, _, _, userPrompt string, _ float64, _ int64) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prompt = userPrompt
	if s.err != nil {
		return "", s.err
	}
	if s.response != "" {
		return s.response, nil
	}
	return `{"content":"## 完成事项\n\n- RPT-1 测试完成\n\n## 数据指标\n\n- 完成事项：999"}`, nil
}

func TestBuildReportContentUsesDatabaseMetrics(t *testing.T) {
	content := buildReportContent("## 完成事项\n\n- RPT-1 测试完成\n\n## 数据指标\n\n- 完成事项：999", ReportSnapshot{
		CompletedCount: 1,
	})
	if !contains(content, "完成事项：1") || contains(content, "完成事项：999") {
		t.Fatalf("metrics were not replaced from snapshot: %s", content)
	}
}

func TestMarshalReportSnapshotCompactsEvidenceAndBoundsStorage(t *testing.T) {
	events := make([]ReportTimelineEvent, 0, 260)
	for index := 0; index < 260; index++ {
		events = append(events, ReportTimelineEvent{
			ID:         fmt.Sprintf("comment-%d", index),
			Type:       "comment",
			OccurredAt: time.Date(2026, 8, 24, 0, index, 0, 0, time.UTC),
			InRange:    true,
			Content:    strings.Repeat("这是一段讨论内容。", 500),
		})
	}
	events = append(events,
		ReportTimelineEvent{
			ID:      "status",
			Type:    "issue_status_history",
			InRange: true,
			Details: json.RawMessage(`{"from_status":"todo","to_status":"done"}`),
		},
		ReportTimelineEvent{
			ID:      "activity",
			Type:    "activity_log",
			InRange: true,
			Action:  "issue_updated",
		},
		ReportTimelineEvent{
			ID:      "task",
			Type:    "agent_task_queue",
			InRange: true,
			Details: json.RawMessage(`{"status":"completed","result":{"summary":"任务已完成"}}`),
		},
	)

	data, truncated, err := marshalReportSnapshot(ReportSnapshot{
		PeriodType: "weekly",
		Issues: []ReportIssue{{
			IssueID:    "issue-1",
			Identifier: "RPT-1",
			Title:      "大讨论",
			Status:     "done",
			Timeline:   events,
		}},
		Completed:  []ReportIssue{},
		InProgress: []ReportIssue{},
		Blocked:    []ReportIssue{},
		Overdue:    []ReportIssue{},
		Cancelled:  []ReportIssue{},
	})
	if err != nil {
		t.Fatalf("marshal report snapshot: %v", err)
	}
	if !truncated {
		t.Fatal("expected oversized evidence to be marked truncated")
	}
	if len(data) > reportSnapshotMaxBytes {
		t.Fatalf("snapshot size = %d, want <= %d", len(data), reportSnapshotMaxBytes)
	}

	var stored ReportSnapshot
	if err := json.Unmarshal(data, &stored); err != nil {
		t.Fatalf("decode compact snapshot: %v", err)
	}
	if !stored.StorageTruncated {
		t.Fatal("stored snapshot did not expose truncation")
	}
	for _, event := range stored.Issues[0].Timeline {
		if event.Type == "issue_status_history" || event.Type == "activity_log" {
			t.Fatalf("bookkeeping event leaked into stored evidence: %+v", event)
		}
		if event.Type == "agent_task_queue" && len(event.Details) != 0 {
			t.Fatalf("raw task details leaked into stored evidence: %+v", event)
		}
		if len(event.Content) > reportSnapshotMaxEventContent {
			t.Fatalf("event content was not bounded: %d", len(event.Content))
		}
	}
}

func TestMarshalReportSnapshotCapsHistoricalReferences(t *testing.T) {
	references := make([]ReportIssue, 0, reportSnapshotMaxReferences+25)
	for index := 0; index < reportSnapshotMaxReferences+25; index++ {
		references = append(references, ReportIssue{
			IssueID:     fmt.Sprintf("issue-%d", index),
			Identifier:  fmt.Sprintf("RPT-%d", index),
			Title:       strings.Repeat("历史事项", 200),
			Description: strings.Repeat("不应在状态索引中重复保存的描述。", 200),
		})
	}

	data, truncated, err := marshalReportSnapshot(ReportSnapshot{
		Completed:      references,
		CompletedCount: len(references),
	})
	if err != nil {
		t.Fatalf("marshal report snapshot: %v", err)
	}
	if !truncated {
		t.Fatal("expected reference cap to mark snapshot as truncated")
	}
	if len(data) > reportSnapshotMaxBytes {
		t.Fatalf("snapshot size = %d, want <= %d", len(data), reportSnapshotMaxBytes)
	}

	var stored ReportSnapshot
	if err := json.Unmarshal(data, &stored); err != nil {
		t.Fatalf("decode compact snapshot: %v", err)
	}
	if len(stored.Completed) != reportSnapshotMaxReferences {
		t.Fatalf("stored references = %d, want %d", len(stored.Completed), reportSnapshotMaxReferences)
	}
	if stored.CompletedCount != len(references) {
		t.Fatalf("stored count = %d, want %d", stored.CompletedCount, len(references))
	}
}

func TestGenerateContentAcceptsFencedJSON(t *testing.T) {
	const payload = `{"content":"## 完成事项\n\n- RPT-1 测试完成"}`
	for _, test := range []struct {
		name string
		raw  string
	}{
		{name: "json fence", raw: "```json\n" + payload + "\n```"},
		{name: "bare fence", raw: "```\n" + payload + "\n```"},
	} {
		t.Run(test.name, func(t *testing.T) {
			generator := ReportGenerator{LLM: &stubReportLLM{response: test.raw}}
			content, err := generator.generateContent(context.Background(), ReportSnapshot{CompletedCount: 1}, "")
			if err != nil {
				t.Fatalf("generateContent: %v", err)
			}
			if !contains(content, "RPT-1 测试完成") || !contains(content, "完成事项：1") {
				t.Fatalf("unexpected report content: %s", content)
			}
		})
	}
}

func TestGenerateContentFallsBackToDeterministicReport(t *testing.T) {
	snapshot := ReportSnapshot{
		Completed:      []ReportIssue{{Identifier: "RPT-1", Title: "测试完成"}},
		CompletedCount: 1,
		Blocked:        []ReportIssue{{Identifier: "RPT-2", Title: "等待依赖"}},
		BlockedCount:   1,
	}
	for _, test := range []struct {
		name string
		llm  ReportLLM
	}{
		{name: "malformed response", llm: &stubReportLLM{response: "not JSON"}},
		{name: "upstream error", llm: &stubReportLLM{err: errors.New("upstream unavailable")}},
		{name: "disabled LLM", llm: nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			generator := ReportGenerator{LLM: test.llm}
			content, err := generator.generateContent(context.Background(), snapshot, "")
			if err != nil {
				t.Fatalf("generateContent: %v", err)
			}
			for _, expected := range []string{
				"AI 总结暂时不可用",
				"RPT-1：测试完成",
				"RPT-2：等待依赖",
				"完成事项：1",
				"阻塞/风险：1",
			} {
				if !contains(content, expected) {
					t.Fatalf("fallback report missing %q: %s", expected, content)
				}
			}
		})
	}
}

func TestIssueReportSummariesUseStructuredAIAndPerIssueFallback(t *testing.T) {
	llm := &stubReportLLM{response: `{"summaries":[{"issue_id":"issue-a","problem":"梳理登录问题","actions":["完成复现"],"outcome":"已定位","open_items":[]},{"issue_id":"issue-b","problem":"缺少字段"}]}`}
	snapshot := ReportSnapshot{
		PeriodType:       "weekly",
		RangeStart:       time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC),
		RangeEnd:         time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC),
		Timezone:         "UTC",
		ActiveIssueCount: 2,
		Issues: []ReportIssue{
			{
				IssueID:    "issue-a",
				Identifier: "RPT-1",
				Title:      "登录问题",
				Status:     "in_progress",
				Timeline: []ReportTimelineEvent{{
					ID: "comment-a", Type: "comment", InRange: true,
					Content: "Authorization: Bearer very-secret-token token=super-secret",
				}},
			},
			{IssueID: "issue-b", Identifier: "RPT-2", Title: "支付问题", Status: "blocked"},
		},
	}

	generator := ReportGenerator{LLM: llm}
	got := generator.withIssueSummaries(context.Background(), snapshot)
	if got.Issues[0].Summary.SummarySource != "ai" || got.Issues[0].Summary.Outcome != "已定位" {
		t.Fatalf("valid issue summary was not accepted: %+v", got.Issues[0].Summary)
	}
	if got.Issues[1].Summary.SummarySource != "deterministic" {
		t.Fatalf("invalid issue summary did not fall back: %+v", got.Issues[1].Summary)
	}
	if contains(llm.prompt, "very-secret-token") || contains(llm.prompt, "super-secret") {
		t.Fatalf("sensitive values leaked into AI prompt: %s", llm.prompt)
	}
	if !contains(llm.prompt, "issue-a") || !contains(llm.prompt, "issue-b") {
		t.Fatalf("AI prompt was not grouped by issue: %s", llm.prompt)
	}
}

func TestIssueReportContentUsesIssueCenteredMetrics(t *testing.T) {
	snapshot := ReportSnapshot{
		Issues: []ReportIssue{{
			IssueID:    "issue-a",
			Identifier: "RPT-1",
			Title:      "测试 issue",
			Status:     "done",
			Summary: ReportIssueSummary{
				IssueID: "issue-a",
				Problem: "需要完成测试",
				Actions: []string{"完成实现"},
				Outcome: "已完成",
			},
		}},
		CompletedCount: 1,
	}
	content := buildIssueReportContent(snapshot)
	for _, expected := range []string{"RPT-1：测试 issue", "问题：需要完成测试", "操作：", "结果：已完成", "活跃 issue：1"} {
		if !contains(content, expected) {
			t.Fatalf("issue-centered report missing %q: %s", expected, content)
		}
	}
}

func TestDeterministicIssueSummaryAddsWorkEvidence(t *testing.T) {
	issue := ReportIssue{
		IssueID:    "issue-a",
		Identifier: "RPT-1",
		Title:      "修复登录 Bug 并调整架构",
		Status:     "in_progress",
		Timeline: []ReportTimelineEvent{
			{ID: "event-old", Type: "comment", InRange: false, Content: "历史讨论"},
			{ID: "event-current", Type: "comment", InRange: true, Content: "完成复现并修复登录错误"},
		},
	}

	summary := deterministicIssueSummary(issue)
	if !contains(strings.Join(summary.WorkTypes, ","), "bug_fix") {
		t.Fatalf("expected bug category: %+v", summary.WorkTypes)
	}
	if !contains(strings.Join(summary.WorkTypes, ","), "architecture") {
		t.Fatalf("expected architecture category: %+v", summary.WorkTypes)
	}
	if len(summary.EvidenceIDs) != 1 || summary.EvidenceIDs[0] != "event-current" {
		t.Fatalf("historical evidence was included: %+v", summary.EvidenceIDs)
	}
	if !contains(strings.Join(summary.WorkDone, "\n"), "完成复现") {
		t.Fatalf("work details missing: %+v", summary.WorkDone)
	}
}

func TestParseProjectAnalysisRejectsUnknownEvidence(t *testing.T) {
	snapshot := ReportSnapshot{
		Issues: []ReportIssue{{
			IssueID:  "issue-a",
			Timeline: []ReportTimelineEvent{{ID: "event-current", InRange: true}},
		}},
	}
	_, err := parseProjectAnalysis(`{"summary":"项目有变化","changes":[{"category":"feature","title":"新增能力","description":"完成能力建设","impact":"待确认","status":"done","evidence_ids":["not-in-report"]}],"evidence_ids":["not-in-report"],"confidence":"high"}`, snapshot)
	if err == nil {
		t.Fatal("expected unknown evidence to be rejected")
	}
}

func TestDeterministicProjectAnalysisContainsRisksAndNextSteps(t *testing.T) {
	snapshot := ReportSnapshot{
		WorkItems: []ReportWorkItem{
			{
				ID: "issue-a", IssueID: "issue-a", Identifier: "RPT-1", Title: "支付改造",
				Category: "feature", Description: "完成支付流程改造", Outcome: "进行中",
				Impact: "业务影响待确认。", Status: "in_progress", EvidenceIDs: []string{"event-a"}, Confidence: "low",
			},
			{
				ID: "issue-b", IssueID: "issue-b", Identifier: "RPT-2", Title: "等待依赖",
				Category: "risk", Description: "等待外部依赖", Outcome: "阻塞",
				Impact: "业务影响待确认。", Status: "blocked", EvidenceIDs: []string{"event-b"}, Confidence: "low",
			},
		},
	}

	analysis := deterministicProjectAnalysis(snapshot)
	if len(analysis.Changes) != 2 || len(analysis.Risks) != 1 || len(analysis.NextSteps) != 2 {
		t.Fatalf("unexpected project analysis: %+v", analysis)
	}
	if !contains(analysis.Risks[0].Description, "阻塞") {
		t.Fatalf("blocking risk missing: %+v", analysis.Risks)
	}
	if len(analysis.Changes[0].EvidenceIDs) != 1 {
		t.Fatalf("change evidence missing: %+v", analysis.Changes[0])
	}
}

func TestReportGeneratorAggregatesProjectWindow(t *testing.T) {
	pool := newReportTestPool(t)
	ctx := context.Background()
	userID := uuid.New()
	workspaceID := uuid.New()
	projectID := uuid.New()
	rangeStart := time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC)
	rangeEnd := time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC)
	numberBase := int32(time.Now().UnixNano()%900000 + 100000)

	if _, err := pool.Exec(ctx, `INSERT INTO "user" (id, name, email) VALUES ($1, $2, $3)`, userID, "Report Generator User", "report-generator-"+userID.String()+"@test.local"); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO workspace (id, name, slug, issue_prefix) VALUES ($1, $2, $3, 'RPT')`, workspaceID, "Report Generator Workspace", "report-generator-"+workspaceID.String()); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO project (id, workspace_id, title) VALUES ($1, $2, $3)`, projectID, workspaceID, "Report Generator Project"); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	t.Cleanup(func() {
		cleanup := context.Background()
		pool.Exec(cleanup, `DELETE FROM report_history WHERE project_id = $1`, projectID)
		pool.Exec(cleanup, `DELETE FROM issue_status_history WHERE workspace_id = $1`, workspaceID)
		pool.Exec(cleanup, `DELETE FROM issue WHERE workspace_id = $1`, workspaceID)
		pool.Exec(cleanup, `DELETE FROM project WHERE id = $1`, projectID)
		pool.Exec(cleanup, `DELETE FROM workspace WHERE id = $1`, workspaceID)
		pool.Exec(cleanup, `DELETE FROM "user" WHERE id = $1`, userID)
	})

	createIssue := func(title, status string, offset int, dueDate *time.Time) pgtype.UUID {
		t.Helper()
		var due any
		if dueDate != nil {
			due = *dueDate
		}
		var id pgtype.UUID
		err := pool.QueryRow(ctx, `
			INSERT INTO issue (
				workspace_id, project_id, title, status, priority, creator_type, creator_id,
				number, position, due_date
			) VALUES ($1, $2, $3, $4, 'none', 'member', $5, $6, $7, $8)
			RETURNING id`,
			workspaceID, projectID, title, status, userID, numberBase+int32(offset), float64(offset), due,
		).Scan(&id)
		if err != nil {
			t.Fatalf("seed issue %s: %v", title, err)
		}
		return id
	}
	insertHistory := func(issueID pgtype.UUID, from, to string, changedAt time.Time) {
		t.Helper()
		if _, err := pool.Exec(ctx, `
			INSERT INTO issue_status_history (issue_id, workspace_id, from_status, to_status, changed_at, changed_by_type)
			VALUES ($1, $2, $3, $4, $5, 'member')`,
			issueID, workspaceID, from, to, changedAt,
		); err != nil {
			t.Fatalf("seed status history: %v", err)
		}
	}

	completedID := createIssue("Window completed", "done", 1, nil)
	insertHistory(completedID, "in_progress", "done", rangeStart)
	boundaryID := createIssue("Boundary completed", "done", 2, nil)
	insertHistory(boundaryID, "todo", "done", rangeEnd)
	cancelledID := createIssue("Window cancelled", "cancelled", 3, nil)
	insertHistory(cancelledID, "in_progress", "cancelled", rangeStart.Add(time.Hour))
	createIssue("In progress", "in_progress", 4, nil)
	createIssue("Blocked", "blocked", 5, nil)
	yesterday := time.Date(2026, 8, 23, 0, 0, 0, 0, time.UTC)
	createIssue("Overdue", "in_progress", 6, &yesterday)

	llm := &stubReportLLM{}
	generator := ReportGenerator{Queries: db.New(pool), LLM: llm}
	project := db.Project{ID: pgtype.UUID{Bytes: projectID, Valid: true}, WorkspaceID: pgtype.UUID{Bytes: workspaceID, Valid: true}}
	report, err := generator.Generate(ctx, project, pgtype.UUID{}, "daily", rangeStart, rangeEnd, "UTC", "member", pgtype.UUID{Bytes: userID, Valid: true})
	if err != nil {
		t.Fatalf("generate report: %v", err)
	}

	resolved, err := ResolveReportSnapshot(ctx, generator.Queries, report.ID, report.DataSnapshot)
	if err != nil {
		t.Fatalf("resolve snapshot: %v", err)
	}
	var snapshot ReportSnapshot
	if err := json.Unmarshal(resolved, &snapshot); err != nil {
		t.Fatalf("unmarshal snapshot: %v", err)
	}
	if snapshot.CompletedCount != 1 || len(snapshot.Completed) != 1 || snapshot.Completed[0].Identifier == "" {
		t.Fatalf("unexpected completed list: %+v", snapshot.Completed)
	}
	if snapshot.CancelledCount != 1 || snapshot.InProgressCount != 2 || snapshot.BlockedCount != 1 || snapshot.OverdueCount != 1 {
		t.Fatalf("unexpected counts: %+v", snapshot)
	}
	if !contains(report.Content, "完成事项：1") || contains(report.Content, "完成事项：999") {
		t.Fatalf("report metrics were not database-derived: %s", report.Content)
	}
	for _, issue := range append(snapshot.Completed, snapshot.Cancelled...) {
		if len(issue.Identifier) < 5 || issue.Identifier[:4] != "RPT-" {
			t.Fatalf("unexpected issue identifier %q", issue.Identifier)
		}
	}

	nextReport, err := generator.Generate(ctx, project, pgtype.UUID{}, "daily", rangeEnd, rangeEnd.Add(24*time.Hour), "UTC", "member", pgtype.UUID{Bytes: userID, Valid: true})
	if err != nil {
		t.Fatalf("generate next report: %v", err)
	}
	nextResolved, err := ResolveReportSnapshot(ctx, generator.Queries, nextReport.ID, nextReport.DataSnapshot)
	if err != nil {
		t.Fatalf("resolve next snapshot: %v", err)
	}
	var nextSnapshot ReportSnapshot
	if err := json.Unmarshal(nextResolved, &nextSnapshot); err != nil {
		t.Fatalf("unmarshal next snapshot: %v", err)
	}
	if nextSnapshot.CompletedCount != 1 || nextSnapshot.Completed[0].Title != "Boundary completed" {
		t.Fatalf("half-open boundary not respected: %+v", nextSnapshot.Completed)
	}
}

func newReportTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = "postgres://multica:multica@localhost:5432/multica?sslmode=disable"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dbURL)
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

func contains(haystack, needle string) bool { return strings.Contains(haystack, needle) }
