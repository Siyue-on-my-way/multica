package service

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestNewSubissueSourcePacketUsesAgentAnchorInsteadOfSummary(t *testing.T) {
	snapshot := SourceContextSnapshot{
		SourceIssue:     SourceContextIssueSnapshot{ID: "issue-1", Identifier: "SIY-1", Title: "来源讨论"},
		AnchorCommentID: "answer-1",
		CommentThread: []SourceContextCommentSnapshot{
			{ID: "question-1", Author: SourceContextAuthor{Type: "member", Name: "用户"}, Content: "请修复问题"},
			{ID: "answer-1", Author: SourceContextAuthor{Type: "agent", Name: "Agent"}, Content: "原始 agent 回复：保留参数兼容性，并补充回归测试。"},
		},
	}

	packet := NewSubissueSourcePacket(snapshot, "LLM 总结：修复问题", snapshot.AnchorCommentID, nil)
	if packet.AgentAnswer != snapshot.CommentThread[1].Content {
		t.Fatalf("agent answer = %q, want anchored original answer", packet.AgentAnswer)
	}
	if packet.AgentAnswer == "LLM 总结：修复问题" {
		t.Fatal("summary must not replace the anchored agent answer")
	}
	if len(packet.SourceUnits) != len(snapshot.CommentThread) {
		t.Fatalf("source units = %d, want %d", len(packet.SourceUnits), len(snapshot.CommentThread))
	}
}

func TestNewSubissueSourcePacketFromJSONRestoresSourceContext(t *testing.T) {
	snapshot := SourceContextSnapshot{
		SourceIssue:     SourceContextIssueSnapshot{ID: "issue-1", Identifier: "SIY-1", Title: "来源讨论"},
		AnchorCommentID: "answer-1",
		CommentThread: []SourceContextCommentSnapshot{{
			ID: "answer-1", Author: SourceContextAuthor{Type: "agent"}, Content: "完整答案，不是摘要。",
		}},
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}

	packet := NewSubissueSourcePacketFromJSON(raw)
	if packet == nil || packet.AgentAnswer != "完整答案，不是摘要。" {
		t.Fatalf("restored packet = %+v", packet)
	}
	if packet.SourceIssue.Identifier != "SIY-1" || packet.SourceCommentID != "answer-1" {
		t.Fatalf("restored source lineage = %+v", packet)
	}
}

func TestRenderSubissueDescriptionPreservesEvidenceAndIsIdempotent(t *testing.T) {
	packet := &SubissueSourcePacket{
		SourceIssue:     SourceContextIssueSnapshot{ID: "issue-1", Identifier: "SIY-1", Title: "来源讨论"},
		SourceCommentID: "answer-1",
		AgentAnswer:     "第一行\n第二行：必须保留原始约束。",
	}

	first := RenderSubissueDescription("## 执行说明\n\n完成实现并验收。", "目标", packet, nil)
	if !strings.Contains(first, "第一行\n第二行：必须保留原始约束。") {
		t.Fatalf("rendered description lost agent answer: %s", first)
	}
	if !strings.Contains(first, "SIY-1") || !strings.Contains(first, "answer-1") {
		t.Fatalf("rendered description lost source lineage: %s", first)
	}
	second := RenderSubissueDescription(first, "目标", packet, nil)
	if second != first {
		t.Fatal("rendering the same description twice must not duplicate the evidence block")
	}
}

func TestRenderSubissueSuggestionDescriptionsAddsTaskQuote(t *testing.T) {
	packet := &SubissueSourcePacket{
		SourceIssue: SourceContextIssueSnapshot{Identifier: "SIY-1"},
		AgentAnswer: "完整 agent 回复",
	}
	plan := SubissuePlan{
		Items:       []SubissuePlanItem{{ID: "item-1", Goal: "修复上传", SourceTaskIDs: []string{"t1"}}},
		SourceTasks: []SubissueIdentifiedTask{{ID: "t1", Quote: "原文条目：修复上传失败并保留错误码。"}},
	}
	details := []SubissueSuggestion{{ID: "item-1", Description: "执行说明"}}
	out := RenderSubissueSuggestionDescriptions(details, plan, packet)
	if len(out) != 1 || !strings.Contains(out[0].Description, "原文条目：修复上传失败并保留错误码。") {
		t.Fatalf("task quote missing from description: %+v", out)
	}
}
