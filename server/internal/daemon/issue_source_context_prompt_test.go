package daemon

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/service"
)

func TestBuildIssueSourceContextBlockPutsAnchorFirstAndQuotesContent(t *testing.T) {
	snapshot := service.SourceContextSnapshot{
		SourceIssue:     service.SourceContextIssueSnapshot{ID: "source-issue", Identifier: "SIY-9", Title: "来源方案"},
		AnchorCommentID: "answer-1",
		CommentThread: []service.SourceContextCommentSnapshot{
			{ID: "question-1", Author: service.SourceContextAuthor{Type: "member", Name: "用户"}, Content: "问题背景"},
			{ID: "answer-1", Author: service.SourceContextAuthor{Type: "agent", Name: "Agent"}, Content: "原始答案\n保留这个约束。"},
		},
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}

	block := buildIssueSourceContextBlock(Task{IssueID: "child-1", IssueSourceContext: raw})
	if !strings.Contains(block, "## 原始讨论证据（只读，优先于摘要）") {
		t.Fatalf("evidence heading missing: %s", block)
	}
	if !strings.Contains(block, "来源 issue：SIY-9 来源方案") {
		t.Fatalf("source lineage missing: %s", block)
	}
	if !strings.Contains(block, "> 原始答案") || !strings.Contains(block, "> 保留这个约束。") {
		t.Fatalf("agent answer was not quoted: %s", block)
	}
	if strings.Index(block, "原始答案") > strings.Index(block, "问题背景") {
		t.Fatal("anchor answer must be rendered before surrounding discussion")
	}
	if !strings.Contains(block, "multica issue comment list source-issue --thread answer-1") {
		t.Fatalf("recovery command missing: %s", block)
	}
}

func TestBuildIssueSourceContextBlockMarksOmittedEvidence(t *testing.T) {
	snapshot := service.SourceContextSnapshot{
		SourceIssue:     service.SourceContextIssueSnapshot{ID: "source-issue", Identifier: "SIY-9"},
		AnchorCommentID: "answer-1",
		CommentThread: []service.SourceContextCommentSnapshot{
			{ID: "answer-1", Author: service.SourceContextAuthor{Type: "agent"}, Content: strings.Repeat("原始答案 ", issueSourceContextPromptBudget)},
			{ID: "question-1", Author: service.SourceContextAuthor{Type: "member"}, Content: "后续上下文不应静默丢失"},
		},
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}

	block := buildIssueSourceContextBlock(Task{IssueID: "child-1", IssueSourceContext: raw})
	if !strings.Contains(block, "原始讨论证据区块达到提示预算") {
		t.Fatalf("truncation note missing: %s", block[len(block)-minInt(len(block), 1000):])
	}
	if !strings.Contains(block, "--thread answer-1") {
		t.Fatalf("truncated block must include a recovery command: %s", block)
	}
	if len(block) > issueSourceContextPromptBudget+512 {
		t.Fatalf("evidence block exceeded its budget: %d", len(block))
	}
}

func TestBuildIssueSourceContextBlockSkipsWarmSession(t *testing.T) {
	block := buildIssueSourceContextBlock(Task{
		IssueID:            "child-1",
		PriorSessionID:     "session-1",
		IssueSourceContext: json.RawMessage(`{"source_issue":{"id":"source"},"anchor_comment_id":"answer"}`),
	})
	if block != "" {
		t.Fatalf("warm session must not repeat source evidence, got %q", block)
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
