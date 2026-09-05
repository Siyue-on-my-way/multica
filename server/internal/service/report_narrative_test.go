package service

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

func narrativeTestSnapshot() ReportSnapshot {
	return ReportSnapshot{
		PeriodType: "weekly",
		RangeStart: time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC),
		RangeEnd:   time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC),
		Timezone:   "UTC",
		Issues: []ReportIssue{
			{
				IssueID:        "issue-a",
				Identifier:     "RPT-1",
				Title:          "登录问题",
				BusinessDomain: "渠道治理",
				Status:         "done",
				Timeline: []ReportTimelineEvent{
					{ID: "c-old", Type: "comment", InRange: false, AuthorType: "member", OccurredAt: time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC), Content: "历史讨论背景"},
					{ID: "c-1", Type: "comment", InRange: true, AuthorType: "member", OccurredAt: time.Date(2026, 8, 25, 1, 0, 0, 0, time.UTC), Content: "复现了登录失败"},
					{ID: "s-1", Type: "issue_status_history", InRange: true, OccurredAt: time.Date(2026, 8, 25, 2, 0, 0, 0, time.UTC), Details: []byte(`{"from_status":"todo","to_status":"in_progress"}`)},
					{ID: "t-1", Type: "agent_task", InRange: true, OccurredAt: time.Date(2026, 8, 25, 3, 0, 0, 0, time.UTC), Details: []byte(`{"status":"completed"}`)},
					{ID: "c-2", Type: "comment", InRange: true, AuthorType: "agent", OccurredAt: time.Date(2026, 8, 26, 1, 0, 0, 0, time.UTC), Content: "已修复并通过测试"},
					{ID: "s-2", Type: "issue_status_history", InRange: true, OccurredAt: time.Date(2026, 8, 26, 2, 0, 0, 0, time.UTC), Details: []byte(`{"from_status":"in_progress","to_status":"done"}`)},
				},
			},
			{
				IssueID:    "issue-b",
				Identifier: "RPT-2",
				Title:      "支付问题",
				Status:     "blocked",
			},
		},
	}
}

func TestBuildIssueConversationExcludesStatusChurn(t *testing.T) {
	conversation := buildIssueConversation(narrativeTestSnapshot().Issues[0])
	joined := strings.Join(conversation, "\n")
	if !contains(joined, "[用户] 复现了登录失败") || !contains(joined, "[AI] 已修复并通过测试") {
		t.Fatalf("conversation lost discussion content: %s", joined)
	}
	if !contains(joined, "[AI任务] 执行完成") {
		t.Fatalf("conversation lost agent task outcome: %s", joined)
	}
	if contains(joined, "in_progress") || contains(joined, "status_changed") {
		t.Fatalf("status churn leaked into conversation: %s", joined)
	}
	if !contains(conversation[0], "窗口前上下文") {
		t.Fatalf("out-of-window context was not marked: %v", conversation)
	}
	if got := buildIssueConversation(narrativeTestSnapshot().Issues[1]); got != nil {
		t.Fatalf("issue without discussion should have no conversation, got %v", got)
	}
}

func TestBuildIssueConversationKeepsTaskResultButDropsActivityRows(t *testing.T) {
	issue := ReportIssue{
		IssueID:    "issue-task",
		Identifier: "RPT-3",
		Title:      "任务结果",
		Timeline: []ReportTimelineEvent{
			{ID: "status", Type: "issue_status_history", InRange: true, Details: []byte(`{"from_status":"todo","to_status":"in_progress"}`)},
			{ID: "activity", Type: "activity_log", InRange: true, Action: "issue_updated", Details: []byte(`{"field":"status"}`)},
			{ID: "task", Type: "agent_task_queue", InRange: true, Details: []byte(`{"status":"completed","result":{"summary":"测试通过"}}`)},
		},
	}

	conversation := strings.Join(buildIssueConversation(issue), "\n")
	if !contains(conversation, "任务结果") || !contains(conversation, "测试通过") {
		t.Fatalf("task result was not retained: %s", conversation)
	}
	if contains(conversation, "issue_updated") || contains(conversation, "in_progress") {
		t.Fatalf("bookkeeping leaked into Stage 0: %s", conversation)
	}
}

func TestDeterministicNarrativeIgnoresStatusOnlyIssue(t *testing.T) {
	snapshot := ReportSnapshot{Issues: []ReportIssue{{
		IssueID:    "status-only",
		Identifier: "RPT-4",
		Title:      "仅状态变更",
		Status:     "done",
		Timeline: []ReportTimelineEvent{{
			ID: "status", Type: "issue_status_history", InRange: true,
			Details: []byte(`{"from_status":"in_progress","to_status":"done"}`),
		}},
	}}}

	narratives := deterministicNarratives(snapshot)
	if len(narratives) != 1 || narratives[0].Noteworthy {
		t.Fatalf("status-only issue should not be noteworthy: %+v", narratives)
	}
	generator := ReportGenerator{}
	withSummary := generator.withExecutiveSummary(context.Background(), ReportSnapshot{
		Issues:     snapshot.Issues,
		Narratives: narratives,
	}, "")
	if contains(withSummary.ExecutiveSummary, "RPT-4") {
		t.Fatalf("status-only issue leaked into L1: %q", withSummary.ExecutiveSummary)
	}
	if len(withSummary.ProjectAnalysis.NextSteps) != 0 || len(withSummary.ProjectAnalysis.Risks) != 0 {
		t.Fatalf("status-only issue leaked into L1 follow-up sections: %+v", withSummary.ProjectAnalysis)
	}
}

func TestIssueStatusSpanCompressesTransitions(t *testing.T) {
	from, to := issueStatusSpan(narrativeTestSnapshot().Issues[0])
	if from != "todo" || to != "done" {
		t.Fatalf("unexpected status span: %s -> %s", from, to)
	}
	from, to = issueStatusSpan(narrativeTestSnapshot().Issues[1])
	if from != "blocked" || to != "blocked" {
		t.Fatalf("issue without transitions should report its current status, got %s -> %s", from, to)
	}
}

func TestNarrativePipelineFallsBackWithoutLLM(t *testing.T) {
	generator := ReportGenerator{}
	snapshot := generator.withNarratives(context.Background(), narrativeTestSnapshot())
	snapshot = generator.withExecutiveSummary(context.Background(), snapshot, "")

	if len(snapshot.Narratives) != 2 {
		t.Fatalf("expected narratives for every issue, got %d", len(snapshot.Narratives))
	}
	first := snapshot.Narratives[0]
	if first.Source != "deterministic" || first.StatusFrom != "todo" || first.StatusTo != "done" {
		t.Fatalf("unexpected deterministic narrative: %+v", first)
	}
	if snapshot.ExecutiveSummary == "" || !contains(snapshot.ExecutiveSummary, "渠道治理") {
		t.Fatalf("executive summary missing deterministic content: %q", snapshot.ExecutiveSummary)
	}
	if snapshot.ProjectAnalysis.Summary != snapshot.ExecutiveSummary {
		t.Fatalf("project analysis summary should mirror the executive summary")
	}

	content := buildNarrativeReportContent(snapshot)
	for _, expected := range []string{"## 本周摘要", "## 分项进展", "渠道治理", "RPT-1"} {
		if !contains(content, expected) {
			t.Fatalf("narrative report missing %q: %s", expected, content)
		}
	}
	for _, noisyEvidence := range []string{"todo → 已完成", "`s-1`", "`s-2`", "issue_status_history", "已记录工作事件"} {
		if contains(content, noisyEvidence) {
			t.Fatalf("bookkeeping leaked into narrative evidence appendix (%q): %s", noisyEvidence, content)
		}
	}
}

func TestNarrativePipelineUsesAIResponses(t *testing.T) {
	llm := &stubReportLLM{response: `{"done":"修复了登录超时","outcome":"测试通过并已合并","evidence":["PR #12"],"risks":["等待发布窗口"],"noteworthy":true,"executive_summary":"本周核心解决了登录超时问题。"}`}
	generator := ReportGenerator{LLM: llm}
	snapshot := generator.withNarratives(context.Background(), narrativeTestSnapshot())
	snapshot = generator.withExecutiveSummary(context.Background(), snapshot, "")

	first := snapshot.Narratives[0]
	if first.Source != "ai" || first.Done != "修复了登录超时" || first.Outcome != "测试通过并已合并" {
		t.Fatalf("AI narrative was not used: %+v", first)
	}
	if len(first.Evidence) != 1 || first.Evidence[0] != "PR #12" {
		t.Fatalf("narrative evidence missing: %+v", first)
	}
	if snapshot.Issues[0].Summary.Outcome != "测试通过并已合并" {
		t.Fatalf("issue summary did not inherit narrative outcome: %+v", snapshot.Issues[0].Summary)
	}
	if snapshot.ExecutiveSummary != "本周核心解决了登录超时问题。" {
		t.Fatalf("executive summary missing AI content: %q", snapshot.ExecutiveSummary)
	}
	llm.mu.Lock()
	projectPrompt := llm.prompt
	llm.mu.Unlock()
	if strings.Contains(projectPrompt, "status_from") || strings.Contains(projectPrompt, "status_to") || strings.Contains(projectPrompt, "status_counts") {
		t.Fatalf("status metadata leaked into Stage 2 prompt: %s", projectPrompt)
	}

	content := buildNarrativeReportContent(snapshot)
	if !contains(content, "做了什么：修复了登录超时") || !contains(content, "证据：PR #12") {
		t.Fatalf("narrative detail layer missing: %s", content)
	}
}

func TestNarrativePipelineCapsIssueCalls(t *testing.T) {
	snapshot := narrativeTestSnapshot()
	for index := 0; index < 40; index++ {
		snapshot.Issues = append(snapshot.Issues, ReportIssue{
			Identifier: fmt.Sprintf("RPT-X-%d", index),
			Title:      "填充",
			Timeline: []ReportTimelineEvent{{
				Type:    "comment",
				Content: "有意义的讨论",
			}},
		})
	}
	issues := reportNarrativeIssueOrder(snapshot)
	if len(issues) != reportNarrativeMaxIssues {
		t.Fatalf("expected issue calls capped at %d, got %d", reportNarrativeMaxIssues, len(issues))
	}
}

func TestReduceNarrativesCapsProjectPrompt(t *testing.T) {
	snapshot := ReportSnapshot{}
	for index := 0; index < reportNarrativeMaxIssues+5; index++ {
		issueID := fmt.Sprintf("issue-%02d", index)
		timeline := make([]ReportTimelineEvent, index+1)
		for eventIndex := range timeline {
			timeline[eventIndex] = ReportTimelineEvent{Type: "comment", Content: "讨论"}
		}
		snapshot.Issues = append(snapshot.Issues, ReportIssue{
			IssueID:  issueID,
			Timeline: timeline,
		})
		snapshot.Narratives = append(snapshot.Narratives, ReportIssueNarrative{
			IssueID:    issueID,
			Identifier: fmt.Sprintf("RPT-%02d", index),
			Noteworthy: true,
		})
	}

	reduced := reduceNarratives(snapshot)
	if len(reduced) != reportNarrativeMaxIssues {
		t.Fatalf("expected reduce prompt capped at %d, got %d", reportNarrativeMaxIssues, len(reduced))
	}
	if reduced[0].IssueID != "issue-34" {
		t.Fatalf("reduce prompt should prioritize the most discussed issue, got %s", reduced[0].IssueID)
	}
}
