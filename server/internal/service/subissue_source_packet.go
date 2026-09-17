package service

import (
	"encoding/json"
	"fmt"
	"strings"
)

// SubissueSourceUnit is a small, immutable pointer into the source discussion.
// The model may use it to explain a split, but the source text itself is never
// replaced by a model-generated summary.
type SubissueSourceUnit struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	CommentID   string `json:"comment_id,omitempty"`
	AuthorType  string `json:"author_type,omitempty"`
	AuthorName  string `json:"author_name,omitempty"`
	Text        string `json:"text"`
	SourceIndex int    `json:"source_index,omitempty"`
}

// SubissueSourcePacket is the durable evidence contract shared by the
// preview, issue creation and the child-agent runtime. AgentAnswer is kept
// verbatim; proposal snapshots and execution context are derived metadata and
// must never be used as a replacement for it.
type SubissueSourcePacket struct {
	Version          int                            `json:"version"`
	SourceIssue      SourceContextIssueSnapshot     `json:"source_issue"`
	SourceCommentID  string                         `json:"source_comment_id,omitempty"`
	SourceIssueID    string                         `json:"source_issue_id,omitempty"`
	AgentAnswer      string                         `json:"agent_answer"`
	CommentThread    []SourceContextCommentSnapshot `json:"comment_thread,omitempty"`
	ProposalSnapshot json.RawMessage                `json:"proposal_snapshot,omitempty"`
	SourceUnits      []SubissueSourceUnit           `json:"source_units,omitempty"`
	CoverageNote     string                         `json:"coverage_note,omitempty"`
}

const subissueSourcePacketVersion = 1

// NewSubissueSourcePacket converts the immutable source-context snapshot into
// the compact contract used by subissue planning. sourceContent is retained as
// a fallback because legacy/manual proposals may not have a comment anchor.
func NewSubissueSourcePacket(
	snapshot SourceContextSnapshot,
	sourceContent, sourceCommentID string,
	proposalSnapshot []byte,
) *SubissueSourcePacket {
	sourceCommentID = strings.TrimSpace(sourceCommentID)
	answer := ""
	units := make([]SubissueSourceUnit, 0, len(snapshot.CommentThread))
	for index, comment := range snapshot.CommentThread {
		if comment.ID == sourceCommentID && comment.Author.Type == "agent" {
			answer = comment.Content
		}
		kind := "reply"
		if comment.ParentID == nil {
			kind = "root"
		}
		units = append(units, SubissueSourceUnit{
			ID: comment.ID, Kind: kind, CommentID: comment.ID,
			AuthorType: comment.Author.Type, AuthorName: comment.Author.Name,
			Text: comment.Content, SourceIndex: index,
		})
	}
	if strings.TrimSpace(answer) == "" {
		answer = strings.TrimSpace(sourceContent)
	}
	proposal := append(json.RawMessage(nil), proposalSnapshot...)
	if len(proposal) > 0 && !json.Valid(proposal) {
		proposal = nil
	}
	packet := &SubissueSourcePacket{
		Version:          subissueSourcePacketVersion,
		SourceIssue:      snapshot.SourceIssue,
		SourceCommentID:  sourceCommentID,
		SourceIssueID:    snapshot.SourceIssue.ID,
		AgentAnswer:      answer,
		CommentThread:    append([]SourceContextCommentSnapshot(nil), snapshot.CommentThread...),
		ProposalSnapshot: proposal,
		SourceUnits:      units,
	}
	if len(snapshot.CommentThread) == 0 {
		packet.CoverageNote = "完整评论线程未建立锚点；以上 agent 原文来自当前请求，创建后仍保留在 description 中。"
	}
	return packet
}

// NewSubissueSourcePacketFromContent is used by the rule/fallback planner,
// whose durable job snapshot has the raw discussion but not necessarily a
// source-context database row.
func NewSubissueSourcePacketFromContent(source SubissueSuggestSourceIssue, content, commentID string) *SubissueSourcePacket {
	return &SubissueSourcePacket{
		Version:         subissueSourcePacketVersion,
		SourceIssue:     SourceContextIssueSnapshot{ID: "", Identifier: source.Identifier, Title: source.Title},
		SourceCommentID: commentID,
		AgentAnswer:     strings.TrimSpace(content),
		SourceUnits: []SubissueSourceUnit{{
			ID: "source-comment", Kind: "agent_answer", CommentID: commentID,
			AuthorType: "agent", Text: strings.TrimSpace(content), SourceIndex: 0,
		}},
	}
}

// NewSubissueSourcePacketFromJSON decodes the persisted source-context
// snapshot used by the proposal batch. Invalid/legacy snapshots simply return
// nil so a batch can still create the reviewed description it already has.
func NewSubissueSourcePacketFromJSON(raw []byte) *SubissueSourcePacket {
	if len(raw) == 0 || !json.Valid(raw) {
		return nil
	}
	var snapshot SourceContextSnapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil || snapshot.SourceIssue.ID == "" && len(snapshot.CommentThread) == 0 {
		return nil
	}
	return NewSubissueSourcePacket(snapshot, "", snapshot.AnchorCommentID, nil)
}

// RenderSubissueDescription is intentionally deterministic. The LLM is
// allowed to describe the work, but it cannot delete the evidence that made
// the work understandable. The marker makes this operation idempotent when a
// preview response is sent through the proposal batch endpoint again.
func RenderSubissueDescription(base, goal string, packet *SubissueSourcePacket, sourceTaskIDs []string) string {
	base = strings.TrimSpace(base)
	if base == "" {
		base = strings.TrimSpace(goal)
	}
	if packet == nil || strings.TrimSpace(packet.AgentAnswer) == "" {
		return base
	}
	const marker = "<!-- multica:subissue-source-evidence:v1 -->"
	if strings.Contains(base, marker) {
		return base
	}

	var b strings.Builder
	b.WriteString(base)
	b.WriteString("\n\n")
	b.WriteString(marker)
	b.WriteString("\n\n## 原始 agent 回复（保真证据）\n\n")
	b.WriteString("以下内容来自生成本子issue的 agent 回复，按原文保留；上面的执行说明是派生内容，不能替代这段证据。\n\n")
	b.WriteString(markdownFence(packet.AgentAnswer))

	if len(sourceTaskIDs) > 0 {
		quotes := sourceTaskQuotes(packet, sourceTaskIDs)
		if len(quotes) > 0 {
			b.WriteString("\n\n## 本条任务对应的原文定位\n\n")
			for _, quote := range quotes {
				fmt.Fprintf(&b, "- %s\n", quote)
			}
		}
	}

	b.WriteString("\n## 来源\n\n")
	if packet.SourceIssue.Identifier != "" || packet.SourceIssue.Title != "" {
		fmt.Fprintf(&b, "- source issue：%s %s\n", packet.SourceIssue.Identifier, packet.SourceIssue.Title)
	}
	if packet.SourceCommentID != "" {
		fmt.Fprintf(&b, "- source comment id：%s\n", packet.SourceCommentID)
	}
	if packet.CoverageNote != "" {
		fmt.Fprintf(&b, "- 证据说明：%s\n", packet.CoverageNote)
	}
	return strings.TrimSpace(b.String())
}

// RenderSubissueSuggestionDescriptions applies the deterministic renderer to
// every detail returned by either the primary or rule-based detail stage.
func RenderSubissueSuggestionDescriptions(details []SubissueSuggestion, plan SubissuePlan, packet *SubissueSourcePacket) []SubissueSuggestion {
	if packet == nil {
		return details
	}
	items := make(map[string]SubissuePlanItem, len(plan.Items))
	for _, item := range plan.Items {
		items[item.ID] = item
	}
	forTask := make(map[string]string, len(plan.SourceTasks))
	for _, task := range plan.SourceTasks {
		if strings.TrimSpace(task.ID) != "" && strings.TrimSpace(task.Quote) != "" {
			forTask[task.ID] = task.Quote
		}
	}
	out := make([]SubissueSuggestion, len(details))
	copy(out, details)
	for index := range out {
		item := items[out[index].ID]
		out[index].Description = RenderSubissueDescriptionWithTaskQuotes(out[index].Description, item.Goal, packet, item.SourceTaskIDs, forTask)
	}
	return out
}

func RenderSubissueDescriptionWithTaskQuotes(base, goal string, packet *SubissueSourcePacket, sourceTaskIDs []string, taskQuotes map[string]string) string {
	rendered := RenderSubissueDescription(base, goal, packet, nil)
	if packet == nil || len(sourceTaskIDs) == 0 || strings.Contains(rendered, "## 本条任务对应的原文定位") {
		return rendered
	}
	quotes := make([]string, 0, len(sourceTaskIDs))
	for _, id := range sourceTaskIDs {
		if quote := strings.TrimSpace(taskQuotes[strings.TrimSpace(id)]); quote != "" {
			quotes = append(quotes, quote)
		}
	}
	if len(quotes) == 0 {
		return rendered
	}
	return strings.TrimSpace(rendered + "\n\n## 本条任务对应的原文定位\n\n- " + strings.Join(quotes, "\n- "))
}

func sourceTaskQuotes(packet *SubissueSourcePacket, ids []string) []string {
	if packet == nil || len(ids) == 0 {
		return nil
	}
	wanted := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" {
			wanted[id] = struct{}{}
		}
	}
	quotes := make([]string, 0, len(wanted))
	for _, unit := range packet.SourceUnits {
		if _, ok := wanted[unit.ID]; ok && strings.TrimSpace(unit.Text) != "" {
			quotes = append(quotes, unit.Text)
		}
	}
	return quotes
}

func markdownFence(value string) string {
	maxRun := 0
	for _, line := range strings.Split(value, "\n") {
		run := 0
		for _, char := range line {
			if char != '`' {
				break
			}
			run++
		}
		if run > maxRun {
			maxRun = run
		}
	}
	fence := strings.Repeat("`", maxRun+3)
	return fence + "text\n" + value + "\n" + fence
}
