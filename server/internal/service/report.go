package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/llm"
)

const (
	reportLLMTimeout            = 45 * time.Second
	reportLLMTemperature        = 0.2
	reportLLMMaxCompletionToken = 3072
	reportPromptMaxEvents       = 80
	reportPromptMaxChars        = 24000
	reportSummaryVersion        = 1
)

type ReportLLM interface {
	Enabled() bool
	GenerateJSON(ctx context.Context, model, systemPrompt, userPrompt string, temperature float64, maxCompletionTokens int64) (string, error)
}

// ReportTimelineEvent is the normalized, issue-local representation of one
// source record. Context comments are retained with InRange=false so a reply
// can be understood without counting an older discussion in the period.
type ReportTimelineEvent struct {
	ID          string          `json:"id"`
	Type        string          `json:"type"`
	OccurredAt  time.Time       `json:"occurred_at"`
	InRange     bool            `json:"in_range"`
	AuthorType  string          `json:"author_type,omitempty"`
	AuthorID    string          `json:"author_id,omitempty"`
	Content     string          `json:"content,omitempty"`
	CommentType string          `json:"comment_type,omitempty"`
	ParentID    string          `json:"parent_id,omitempty"`
	Action      string          `json:"action,omitempty"`
	Details     json.RawMessage `json:"details,omitempty"`
}

type ReportIssueSummary struct {
	IssueID       string   `json:"issue_id"`
	Problem       string   `json:"problem"`
	Actions       []string `json:"actions"`
	Outcome       string   `json:"outcome"`
	OpenItems     []string `json:"open_items"`
	SummarySource string   `json:"summary_source,omitempty"`
}

type ReportIssue struct {
	IssueID           string                `json:"issue_id,omitempty"`
	Identifier        string                `json:"identifier"`
	Title             string                `json:"title"`
	Description       string                `json:"description,omitempty"`
	Status            string                `json:"status,omitempty"`
	DueDate           string                `json:"due_date,omitempty"`
	Summary           ReportIssueSummary    `json:"summary,omitempty"`
	Timeline          []ReportTimelineEvent `json:"timeline,omitempty"`
	TimelineTruncated bool                  `json:"timeline_truncated,omitempty"`
}

type ReportSnapshot struct {
	PeriodType       string        `json:"period_type"`
	RangeStart       time.Time     `json:"range_start"`
	RangeEnd         time.Time     `json:"range_end"`
	Timezone         string        `json:"timezone"`
	GeneratedAt      time.Time     `json:"generated_at"`
	SummaryVersion   int           `json:"summary_version"`
	Issues           []ReportIssue `json:"issues"`
	ActiveIssueCount int           `json:"active_issue_count"`

	// These fields are retained for existing consumers of report history. New
	// issue-centered clients should use Issues and its current status values.
	Completed       []ReportIssue `json:"completed"`
	InProgress      []ReportIssue `json:"in_progress"`
	Blocked         []ReportIssue `json:"blocked"`
	Overdue         []ReportIssue `json:"overdue"`
	Cancelled       []ReportIssue `json:"cancelled"`
	CompletedCount  int           `json:"completed_count"`
	InProgressCount int           `json:"in_progress_count"`
	BlockedCount    int           `json:"blocked_count"`
	OverdueCount    int           `json:"overdue_count"`
	CancelledCount  int           `json:"cancelled_count"`
}

type ReportGenerator struct {
	Queries *db.Queries
	LLM     ReportLLM
}

func (g *ReportGenerator) Generate(
	ctx context.Context,
	project db.Project,
	periodType string,
	rangeStart time.Time,
	rangeEnd time.Time,
	timezoneName string,
	generatedByType string,
	generatedByID pgtype.UUID,
) (db.ReportHistory, error) {
	snapshotJSON, content, err := g.build(ctx, project, periodType, rangeStart, rangeEnd, timezoneName)
	if err != nil {
		return db.ReportHistory{}, err
	}
	report, err := g.Queries.CreateReportHistory(ctx, db.CreateReportHistoryParams{
		WorkspaceID:     project.WorkspaceID,
		ProjectID:       project.ID,
		PeriodType:      periodType,
		RangeStart:      pgtype.Timestamptz{Time: rangeStart, Valid: true},
		RangeEnd:        pgtype.Timestamptz{Time: rangeEnd, Valid: true},
		Timezone:        timezoneName,
		GeneratedByType: generatedByType,
		GeneratedByID:   generatedByID,
		DataSnapshot:    snapshotJSON,
		Content:         content,
	})
	if err != nil {
		return db.ReportHistory{}, fmt.Errorf("save report history: %w", err)
	}
	return report, nil
}

func (g *ReportGenerator) GenerateInto(
	ctx context.Context,
	project db.Project,
	reportID pgtype.UUID,
	periodType string,
	rangeStart time.Time,
	rangeEnd time.Time,
	timezoneName string,
) error {
	snapshotJSON, content, err := g.build(ctx, project, periodType, rangeStart, rangeEnd, timezoneName)
	if err != nil {
		return err
	}
	if _, err := g.Queries.UpdateReportHistoryGeneration(ctx, db.UpdateReportHistoryGenerationParams{
		ID:           reportID,
		DataSnapshot: snapshotJSON,
		Content:      content,
	}); err != nil {
		return fmt.Errorf("save generated report: %w", err)
	}
	return nil
}

func (g *ReportGenerator) build(
	ctx context.Context,
	project db.Project,
	periodType string,
	rangeStart time.Time,
	rangeEnd time.Time,
	timezoneName string,
) ([]byte, string, error) {
	location, err := time.LoadLocation(timezoneName)
	if err != nil {
		return nil, "", fmt.Errorf("invalid timezone %q: %w", timezoneName, err)
	}

	now := time.Now().In(location)
	aggregator := &ProjectIssueAggregator{Queries: g.Queries}
	snapshot, err := aggregator.Aggregate(ctx, project, rangeStart, rangeEnd, now)
	if err != nil {
		return nil, "", err
	}
	snapshot.PeriodType = periodType
	snapshot.RangeStart = rangeStart.In(location)
	snapshot.RangeEnd = rangeEnd.In(location)
	snapshot.Timezone = timezoneName

	var content string
	if len(snapshot.Issues) > 0 {
		snapshot = g.withIssueSummaries(ctx, snapshot)
		content = buildIssueReportContent(snapshot)
	} else {
		content, err = g.generateContent(ctx, snapshot)
		if err != nil {
			return nil, "", err
		}
	}

	snapshotJSON, err := json.Marshal(snapshot)
	if err != nil {
		return nil, "", fmt.Errorf("encode report snapshot: %w", err)
	}
	return snapshotJSON, content, nil
}

// generateContent keeps the legacy path available for old saved snapshots and
// tests. New snapshots use withIssueSummaries, which always returns one
// deterministic summary per issue even if the model fails for another issue.
func (g *ReportGenerator) generateContent(ctx context.Context, snapshot ReportSnapshot) (string, error) {
	if len(snapshot.Issues) > 0 {
		return buildIssueReportContent(g.withIssueSummaries(ctx, snapshot)), nil
	}

	snapshotJSON, err := json.Marshal(snapshot)
	if err != nil {
		return "", fmt.Errorf("encode report prompt: %w", err)
	}
	if g.LLM == nil || !g.LLM.Enabled() {
		return buildDeterministicReportContent(snapshot), nil
	}

	generateCtx, cancel := context.WithTimeout(ctx, reportLLMTimeout)
	defer cancel()
	raw, err := g.LLM.GenerateJSON(
		generateCtx,
		"",
		legacyReportSystemPrompt,
		string(snapshotJSON),
		reportLLMTemperature,
		reportLLMMaxCompletionToken,
	)
	if err != nil {
		return deterministicReportFallback(snapshot, "LLM request failed", err)
	}
	raw = llm.StripJSONFence(raw)
	var payload struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return deterministicReportFallback(snapshot, "LLM returned invalid JSON", err)
	}
	content := strings.TrimSpace(buildReportContent(payload.Content, snapshot))
	if content == "" {
		return deterministicReportFallback(snapshot, "LLM returned empty content", nil)
	}
	return content, nil
}

func (g *ReportGenerator) withIssueSummaries(ctx context.Context, snapshot ReportSnapshot) ReportSnapshot {
	for index := range snapshot.Issues {
		snapshot.Issues[index].TimelineTruncated = timelineNeedsTruncation(snapshot.Issues[index].Timeline)
		snapshot.Issues[index].Summary = deterministicIssueSummary(snapshot.Issues[index])
	}
	if g.LLM == nil || !g.LLM.Enabled() || len(snapshot.Issues) == 0 {
		return snapshot
	}

	prompt := prepareReportPrompt(snapshot)
	promptJSON, err := json.Marshal(prompt)
	if err != nil {
		slog.Warn("project report: unable to encode issue prompt", "error", err)
		return snapshot
	}

	generateCtx, cancel := context.WithTimeout(ctx, reportLLMTimeout)
	defer cancel()
	raw, err := g.LLM.GenerateJSON(
		generateCtx,
		"",
		reportSystemPrompt,
		string(promptJSON),
		reportLLMTemperature,
		reportLLMMaxCompletionToken,
	)
	if err != nil {
		slog.Warn("project report: using per-issue fallback", "reason", "LLM request failed", "error", err)
		return snapshot
	}

	summaries, err := parseIssueSummaries(raw, snapshot.Issues)
	if err != nil {
		slog.Warn("project report: using per-issue fallback", "reason", "LLM returned invalid summaries", "error", err)
		return snapshot
	}
	for index := range snapshot.Issues {
		issueID := snapshot.Issues[index].IssueID
		if summary, ok := summaries[issueID]; ok {
			summary.SummarySource = "ai"
			snapshot.Issues[index].Summary = summary
		}
	}
	return snapshot
}

type reportPromptSnapshot struct {
	PeriodType       string        `json:"period_type"`
	RangeStart       time.Time     `json:"range_start"`
	RangeEnd         time.Time     `json:"range_end"`
	Timezone         string        `json:"timezone"`
	ActiveIssueCount int           `json:"active_issue_count"`
	Issues           []ReportIssue `json:"issues"`
}

func prepareReportPrompt(snapshot ReportSnapshot) reportPromptSnapshot {
	issues := make([]ReportIssue, 0, len(snapshot.Issues))
	for _, issue := range snapshot.Issues {
		prepared := issue
		prepared.Timeline = cloneAndLimitTimeline(issue.Timeline, &prepared.TimelineTruncated)
		prepared.Title = sanitizeSensitiveText(prepared.Title)
		prepared.Description = sanitizeSensitiveText(prepared.Description)
		for index := range prepared.Timeline {
			event := &prepared.Timeline[index]
			event.Content = sanitizeSensitiveText(event.Content)
			event.Action = sanitizeSensitiveText(event.Action)
			event.Details = sanitizeJSONForPrompt(event.Details)
		}
		issues = append(issues, prepared)
	}
	activeCount := snapshot.ActiveIssueCount
	if activeCount == 0 {
		activeCount = len(snapshot.Issues)
	}
	return reportPromptSnapshot{
		PeriodType:       snapshot.PeriodType,
		RangeStart:       snapshot.RangeStart,
		RangeEnd:         snapshot.RangeEnd,
		Timezone:         snapshot.Timezone,
		ActiveIssueCount: activeCount,
		Issues:           issues,
	}
}

func cloneAndLimitTimeline(events []ReportTimelineEvent, truncated *bool) []ReportTimelineEvent {
	if len(events) == 0 {
		return nil
	}
	cloned := make([]ReportTimelineEvent, len(events))
	copy(cloned, events)
	for index := range cloned {
		if cloned[index].Details != nil {
			cloned[index].Details = append(json.RawMessage(nil), cloned[index].Details...)
		}
	}
	totalChars := 0
	for _, event := range cloned {
		totalChars += len(event.Content) + len(event.Action) + len(event.Details)
	}
	if len(cloned) <= reportPromptMaxEvents && totalChars <= reportPromptMaxChars {
		return cloned
	}

	selected := make([]ReportTimelineEvent, 0, reportPromptMaxEvents)
	selectedIDs := make(map[string]struct{}, reportPromptMaxEvents)
	selectedChars := 0
	for _, event := range cloned {
		if !event.InRange && len(selected) < 4 {
			selected = append(selected, event)
			selectedIDs[event.ID] = struct{}{}
			selectedChars += eventChars(event)
		}
	}
	for index := len(cloned) - 1; index >= 0 && len(selected) < reportPromptMaxEvents; index-- {
		event := cloned[index]
		if _, exists := selectedIDs[event.ID]; exists {
			continue
		}
		if selectedChars+eventChars(event) > reportPromptMaxChars && len(selected) > 0 {
			continue
		}
		selected = append(selected, event)
		selectedIDs[event.ID] = struct{}{}
		selectedChars += eventChars(event)
	}
	sort.SliceStable(selected, func(left, right int) bool {
		if selected[left].OccurredAt.Equal(selected[right].OccurredAt) {
			return selected[left].ID < selected[right].ID
		}
		return selected[left].OccurredAt.Before(selected[right].OccurredAt)
	})
	selected = fitTimelineToCharacterBudget(selected, reportPromptMaxChars)
	*truncated = timelineNeedsTruncation(events)
	return selected
}

func timelineNeedsTruncation(events []ReportTimelineEvent) bool {
	if len(events) > reportPromptMaxEvents {
		return true
	}
	totalChars := 0
	for _, event := range events {
		totalChars += eventChars(event)
	}
	return totalChars > reportPromptMaxChars
}

func fitTimelineToCharacterBudget(events []ReportTimelineEvent, budget int) []ReportTimelineEvent {
	if budget <= 0 {
		return nil
	}
	fit := make([]ReportTimelineEvent, 0, len(events))
	remaining := budget
	for _, event := range events {
		if remaining <= 0 {
			break
		}
		if eventChars(event) > remaining {
			event = trimTimelineEvent(event, remaining)
		}
		if eventChars(event) == 0 {
			continue
		}
		fit = append(fit, event)
		remaining -= eventChars(event)
	}
	return fit
}

func trimTimelineEvent(event ReportTimelineEvent, budget int) ReportTimelineEvent {
	// Keep the identifying metadata and spend the prompt budget on human text;
	// a task result is less useful than the comment/status context when a single
	// record is exceptionally large.
	event.Details = nil
	if eventChars(event) <= budget {
		return event
	}
	if len(event.Action) > 0 {
		event.Action = truncateReportText(event.Action, budget)
	}
	if eventChars(event) <= budget {
		return event
	}
	remaining := budget - len(event.Action)
	if remaining < 0 {
		remaining = 0
	}
	event.Content = truncateReportText(event.Content, remaining)
	return event
}

func truncateReportText(value string, max int) string {
	if max <= 0 {
		return ""
	}
	if len(value) <= max {
		return value
	}
	const suffix = "…"
	if max <= len(suffix) {
		return suffix[:max]
	}
	truncated := make([]rune, 0, len(value))
	for _, character := range value {
		candidate := string(append(truncated, character))
		if len(candidate)+len(suffix) > max {
			break
		}
		truncated = append(truncated, character)
	}
	return string(truncated) + suffix
}

func eventChars(event ReportTimelineEvent) int {
	return len(event.Content) + len(event.Action) + len(event.Details)
}

var (
	reportBearerPattern           = regexp.MustCompile(`(?i)\bBearer\s+[A-Za-z0-9._~+/=-]+`)
	reportTokenPattern            = regexp.MustCompile(`(?i)\b(?:ghp_[A-Za-z0-9_]+|github_pat_[A-Za-z0-9_]+|sk-[A-Za-z0-9_-]+|eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+)\b`)
	reportSecretAssignmentPattern = regexp.MustCompile(`(?i)(["']?(?:api[_-]?key|access[_-]?token|refresh[_-]?token|authorization|password|passwd|secret|token)["']?\s*[:=]\s*)(?:"[^"]*"|'[^']*'|[^,\s}]+)`)
	reportPrivateKeyPattern       = regexp.MustCompile(`(?s)-----BEGIN [^-]*PRIVATE KEY-----.*?-----END [^-]*PRIVATE KEY-----`)
)

func sanitizeSensitiveText(value string) string {
	if value == "" {
		return value
	}
	value = reportPrivateKeyPattern.ReplaceAllString(value, "[REDACTED_PRIVATE_KEY]")
	value = reportBearerPattern.ReplaceAllString(value, "Bearer [REDACTED_TOKEN]")
	value = reportTokenPattern.ReplaceAllString(value, "[REDACTED_TOKEN]")
	return reportSecretAssignmentPattern.ReplaceAllString(value, `${1}"[REDACTED_SECRET]"`)
}

func sanitizeJSONForPrompt(value json.RawMessage) json.RawMessage {
	if len(value) == 0 {
		return nil
	}
	redacted := sanitizeSensitiveText(string(value))
	if !json.Valid([]byte(redacted)) {
		return json.RawMessage(`{"redacted":true}`)
	}
	return json.RawMessage(redacted)
}

func parseIssueSummaries(raw string, issues []ReportIssue) (map[string]ReportIssueSummary, error) {
	raw = llm.StripJSONFence(raw)
	var envelope struct {
		Summaries json.RawMessage `json:"summaries"`
	}
	if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
		return nil, err
	}
	if len(envelope.Summaries) == 0 || string(envelope.Summaries) == "null" {
		return nil, fmt.Errorf("summaries array is missing")
	}
	var items []json.RawMessage
	if err := json.Unmarshal(envelope.Summaries, &items); err != nil {
		return nil, err
	}
	expected := make(map[string]struct{}, len(issues))
	for _, issue := range issues {
		expected[issue.IssueID] = struct{}{}
	}
	parsed := make(map[string]ReportIssueSummary, len(items))
	for _, item := range items {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(item, &fields); err != nil {
			continue
		}
		for _, required := range []string{"issue_id", "problem", "actions", "outcome", "open_items"} {
			value, ok := fields[required]
			if !ok || strings.TrimSpace(string(value)) == "null" {
				fields = nil
				break
			}
		}
		if fields == nil {
			continue
		}
		var summary ReportIssueSummary
		if err := json.Unmarshal(item, &summary); err != nil || summary.IssueID == "" {
			continue
		}
		if _, ok := expected[summary.IssueID]; !ok || summary.IssueID == "" {
			continue
		}
		if _, duplicate := parsed[summary.IssueID]; duplicate {
			continue
		}
		for index := range summary.Actions {
			summary.Actions[index] = strings.TrimSpace(summary.Actions[index])
		}
		for index := range summary.OpenItems {
			summary.OpenItems[index] = strings.TrimSpace(summary.OpenItems[index])
		}
		summary.Problem = strings.TrimSpace(summary.Problem)
		summary.Outcome = strings.TrimSpace(summary.Outcome)
		if len(summary.Actions) > 8 || len(summary.OpenItems) > 8 || len(summary.Problem) > 2000 || len(summary.Outcome) > 2000 {
			continue
		}
		tooLongItem := false
		for _, item := range append(append([]string{}, summary.Actions...), summary.OpenItems...) {
			if len(item) > 1000 {
				tooLongItem = true
				break
			}
		}
		if tooLongItem {
			continue
		}
		parsed[summary.IssueID] = summary
	}
	if len(parsed) == 0 && len(issues) > 0 {
		return nil, fmt.Errorf("no valid issue summaries")
	}
	return parsed, nil
}

func deterministicIssueSummary(issue ReportIssue) ReportIssueSummary {
	statusLabel := map[string]string{
		"backlog":     "待规划",
		"todo":        "待处理",
		"in_progress": "进行中",
		"in_review":   "待审核",
		"done":        "已完成",
		"blocked":     "阻塞",
		"cancelled":   "已取消",
	}[issue.Status]
	if statusLabel == "" {
		statusLabel = issue.Status
	}

	commentCount := 0
	activityCount := 0
	statusChanges := make([]string, 0, 4)
	taskCount := 0
	for _, event := range issue.Timeline {
		if !event.InRange {
			continue
		}
		switch event.Type {
		case "comment":
			commentCount++
		case "activity_log":
			activityCount++
		case "issue_status_history":
			var details struct {
				From string `json:"from_status"`
				To   string `json:"to_status"`
			}
			if json.Unmarshal(event.Details, &details) == nil && details.From != "" && details.To != "" {
				statusChanges = append(statusChanges, fmt.Sprintf("状态从 %s 变更为 %s", details.From, details.To))
			}
		case "agent_task_queue":
			taskCount++
		}
	}

	problem := fmt.Sprintf("本周期有 %d 条工作记录，当前状态为%s。", commentCount+activityCount+len(statusChanges)+taskCount, statusLabel)
	if len(issue.Timeline) == 0 {
		problem = fmt.Sprintf("当前状态为%s。", statusLabel)
	}
	actions := make([]string, 0, 4)
	actions = append(actions, statusChanges...)
	if commentCount > 0 {
		actions = append(actions, fmt.Sprintf("处理 %d 条新增讨论。", commentCount))
	}
	if taskCount > 0 {
		actions = append(actions, fmt.Sprintf("记录 %d 次 agent task 执行。", taskCount))
	}
	if activityCount > 0 {
		actions = append(actions, fmt.Sprintf("记录 %d 条操作日志。", activityCount))
	}
	if len(actions) == 0 {
		actions = append(actions, "本周期没有可进一步拆分的操作记录。")
	}

	openItems := make([]string, 0, 2)
	switch issue.Status {
	case "blocked":
		openItems = append(openItems, "解除阻塞并确认后续执行条件。")
	case "done", "cancelled":
		// Terminal states have no default follow-up.
	default:
		openItems = append(openItems, "继续推进该 issue 并在完成后更新状态。")
	}
	return ReportIssueSummary{
		IssueID:       issue.IssueID,
		Problem:       problem,
		Actions:       actions,
		Outcome:       fmt.Sprintf("当前状态：%s。", statusLabel),
		OpenItems:     openItems,
		SummarySource: "deterministic",
	}
}

func deterministicReportFallback(snapshot ReportSnapshot, reason string, err error) (string, error) {
	if err != nil {
		slog.Warn("project report: using deterministic fallback", "reason", reason, "error", err)
	} else {
		slog.Warn("project report: using deterministic fallback", "reason", reason)
	}
	return buildDeterministicReportContent(snapshot), nil
}

func reportIssuesFromCompletedRows(rows []db.ListIssuesCompletedForReportRow, identifierPrefix string) []ReportIssue {
	issues := make([]ReportIssue, 0, len(rows))
	for _, row := range rows {
		issues = append(issues, reportIssue(identifierPrefix, row.Number, row.Title))
	}
	return issues
}

func reportIssuesFromCancelledRows(rows []db.ListIssuesCancelledForReportRow, identifierPrefix string) []ReportIssue {
	issues := make([]ReportIssue, 0, len(rows))
	for _, row := range rows {
		issues = append(issues, reportIssue(identifierPrefix, row.Number, row.Title))
	}
	return issues
}

func reportIssuesFromOverdueRows(rows []db.ListIssuesOverdueForReportRow, identifierPrefix string) []ReportIssue {
	issues := make([]ReportIssue, 0, len(rows))
	for _, row := range rows {
		issues = append(issues, reportIssue(identifierPrefix, row.Number, row.Title))
	}
	return issues
}

func reportIssuesFromStatusRows(rows []db.ListIssuesByStatusForReportRow, identifierPrefix string) []ReportIssue {
	issues := make([]ReportIssue, 0, len(rows))
	for _, row := range rows {
		issues = append(issues, reportIssue(identifierPrefix, row.Number, row.Title))
	}
	return issues
}

func reportIssue(identifierPrefix string, number int32, title string) ReportIssue {
	return ReportIssue{
		Identifier: fmt.Sprintf("%s-%d", identifierPrefix, number),
		Title:      title,
	}
}

func buildDeterministicReportContent(snapshot ReportSnapshot) string {
	if len(snapshot.Issues) > 0 {
		return buildIssueReportContent(snapshot)
	}
	var content strings.Builder
	content.WriteString("## 报告\n\n")
	content.WriteString("AI 总结暂时不可用，以下内容由系统根据项目数据生成。\n\n")
	appendReportIssueSection(&content, "完成事项", snapshot.Completed)
	appendReportIssueSection(&content, "进行中事项", snapshot.InProgress)
	appendReportIssueSection(&content, "阻塞/风险", snapshot.Blocked)
	appendReportIssueSection(&content, "逾期事项", snapshot.Overdue)
	content.WriteString("## 下一步重点\n\n")
	if len(snapshot.Blocked) > 0 {
		content.WriteString("- 优先处理阻塞/风险事项。\n")
	}
	if len(snapshot.Overdue) > 0 {
		content.WriteString("- 优先处理逾期事项。\n")
	}
	if len(snapshot.InProgress) > 0 {
		content.WriteString("- 持续推进进行中的事项。\n")
	}
	if len(snapshot.Blocked) == 0 && len(snapshot.Overdue) == 0 && len(snapshot.InProgress) == 0 {
		content.WriteString("- 暂无需要跟进的事项。\n")
	}
	return buildReportContent(content.String(), snapshot)
}

func buildIssueReportContent(snapshot ReportSnapshot) string {
	var content strings.Builder
	content.WriteString("## 报告\n\n")
	content.WriteString(fmt.Sprintf("本周期共记录 %d 个活跃 issue 的工作过程。\n\n", len(snapshot.Issues)))
	content.WriteString("## Issue 工作摘要\n\n")
	for _, issue := range snapshot.Issues {
		fmt.Fprintf(&content, "### %s：%s\n\n", issue.Identifier, issue.Title)
		fmt.Fprintf(&content, "- 问题：%s\n", issue.Summary.Problem)
		content.WriteString("- 操作：\n")
		for _, action := range issue.Summary.Actions {
			fmt.Fprintf(&content, "  - %s\n", action)
		}
		fmt.Fprintf(&content, "- 结果：%s\n", issue.Summary.Outcome)
		content.WriteString("- 待跟进：\n")
		if len(issue.Summary.OpenItems) == 0 {
			content.WriteString("  - 暂无\n\n")
		} else {
			for _, item := range issue.Summary.OpenItems {
				fmt.Fprintf(&content, "  - %s\n", item)
			}
			content.WriteString("\n")
		}
	}
	if len(snapshot.Issues) == 0 {
		content.WriteString("暂无活跃 issue。\n")
	}
	return buildReportContent(content.String(), snapshot)
}

func appendReportIssueSection(content *strings.Builder, title string, issues []ReportIssue) {
	fmt.Fprintf(content, "## %s\n\n", title)
	if len(issues) == 0 {
		content.WriteString("- 暂无\n\n")
		return
	}
	for _, issue := range issues {
		fmt.Fprintf(content, "- %s：%s\n", issue.Identifier, issue.Title)
	}
	content.WriteString("\n")
}

func buildReportContent(modelContent string, snapshot ReportSnapshot) string {
	content := strings.TrimSpace(modelContent)
	if index := strings.LastIndex(content, "## 数据指标"); index >= 0 {
		content = strings.TrimSpace(content[:index])
	}
	if content == "" {
		content = "## 报告\n\n暂无可总结的内容。"
	}
	if len(snapshot.Issues) > 0 {
		counts := map[string]int{}
		for _, issue := range snapshot.Issues {
			counts[issue.Status]++
		}
		// Keep the established metric labels in the markdown export. The
		// issue-centered counters above describe this report window; these
		// labels remain for clients that already parse the saved markdown.
		return fmt.Sprintf(`%s

## 数据指标

- 活跃 issue：%d
- 已完成：%d
- 进行中：%d
- 阻塞：%d
- 取消：%d
- 完成事项：%d
- 进行中事项：%d
- 阻塞/风险：%d
- 逾期事项：%d
- 取消事项：%d`, content, len(snapshot.Issues), counts["done"], counts["in_progress"]+counts["in_review"]+counts["todo"]+counts["backlog"], counts["blocked"], counts["cancelled"], snapshot.CompletedCount, snapshot.InProgressCount, snapshot.BlockedCount, snapshot.OverdueCount, snapshot.CancelledCount)
	}
	return fmt.Sprintf(`%s

## 数据指标

- 完成事项：%d
- 进行中事项：%d
- 阻塞/风险：%d
- 逾期事项：%d
- 取消事项：%d`, content, snapshot.CompletedCount, snapshot.InProgressCount, snapshot.BlockedCount, snapshot.OverdueCount, snapshot.CancelledCount)
}

const reportSystemPrompt = `You are an issue-centered project reporting assistant. Return exactly one JSON object shaped {"summaries":[{"issue_id":"...","problem":"...","actions":["..."],"outcome":"...","open_items":["..."]}]}.
Do not wrap the JSON in Markdown code fences. Do not add prose before or after the JSON object.
Return at most one summary for each supplied issue_id. Use the exact supplied issue_id; never invent, merge, or omit an issue silently. Every field is required. actions and open_items must be arrays of concise strings.
Summarize only the supplied issue timeline. A timeline entry with in_range=false is historical context and must not be counted as work in the requested period.
Do not reveal or repeat tokens, passwords, API keys, authorization headers, private keys, or other secrets even if they appear in the input.
Do not introduce other projects, periods, numbers, status changes, or assumptions. The AI's role is wording only. Write concise, professional Chinese.`

const legacyReportSystemPrompt = `You are a project reporting assistant. Return exactly one JSON object shaped {"content":"markdown"}.
Do not wrap the JSON in Markdown code fences. Do not add prose before or after the JSON object.
The markdown content must use these sections in order: 完成事项, 进行中事项, 阻塞/风险, 逾期事项, 下一步重点. Do not write 数据指标; the caller adds that section.
Only cite issues and facts present in the supplied JSON. Never introduce other projects, periods, calculations, numbers, or assumptions.
Preserve issue identifiers exactly. Do not change any counts; use only the supplied *_count fields.
The AI's role is wording only. Write concise, professional Chinese Markdown.`
