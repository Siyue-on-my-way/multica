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
	reportAnalysisVersion       = 1
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
	WorkTypes     []string `json:"work_types,omitempty"`
	WorkDone      []string `json:"work_done,omitempty"`
	Artifacts     []string `json:"artifacts,omitempty"`
	Impact        string   `json:"impact,omitempty"`
	EvidenceIDs   []string `json:"evidence_ids,omitempty"`
	Confidence    string   `json:"confidence,omitempty"`
	SummarySource string   `json:"summary_source,omitempty"`
}

type ReportWorkItem struct {
	ID          string   `json:"id"`
	IssueID     string   `json:"issue_id"`
	Identifier  string   `json:"identifier"`
	IssueTitle  string   `json:"issue_title"`
	Category    string   `json:"category"`
	Categories  []string `json:"categories,omitempty"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Outcome     string   `json:"outcome"`
	Impact      string   `json:"impact,omitempty"`
	Status      string   `json:"status"`
	EvidenceIDs []string `json:"evidence_ids,omitempty"`
	Confidence  string   `json:"confidence,omitempty"`
	Source      string   `json:"source,omitempty"`
}

type ReportAnalysisNote struct {
	Title       string   `json:"title"`
	Description string   `json:"description"`
	EvidenceIDs []string `json:"evidence_ids,omitempty"`
	Confidence  string   `json:"confidence,omitempty"`
	Source      string   `json:"source,omitempty"`
}

type ReportProjectChange struct {
	ID          string   `json:"id"`
	Category    string   `json:"category"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Impact      string   `json:"impact,omitempty"`
	Status      string   `json:"status"`
	EvidenceIDs []string `json:"evidence_ids,omitempty"`
	Confidence  string   `json:"confidence,omitempty"`
	Source      string   `json:"source,omitempty"`
}

type ReportProjectAnalysis struct {
	Summary     string                `json:"summary"`
	Changes     []ReportProjectChange `json:"changes,omitempty"`
	Risks       []ReportAnalysisNote  `json:"risks,omitempty"`
	NextSteps   []ReportAnalysisNote  `json:"next_steps,omitempty"`
	EvidenceIDs []string              `json:"evidence_ids,omitempty"`
	Confidence  string                `json:"confidence,omitempty"`
	Source      string                `json:"source,omitempty"`
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
	PeriodType       string                `json:"period_type"`
	RangeStart       time.Time             `json:"range_start"`
	RangeEnd         time.Time             `json:"range_end"`
	Timezone         string                `json:"timezone"`
	GeneratedAt      time.Time             `json:"generated_at"`
	SummaryVersion   int                   `json:"summary_version"`
	AnalysisVersion  int                   `json:"analysis_version,omitempty"`
	Issues           []ReportIssue         `json:"issues"`
	ActiveIssueCount int                   `json:"active_issue_count"`
	WorkItems        []ReportWorkItem      `json:"work_items,omitempty"`
	ProjectAnalysis  ReportProjectAnalysis `json:"project_analysis,omitempty"`
	AnalysisWarnings []string              `json:"analysis_warnings,omitempty"`

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
		snapshot = g.withProjectAnalysis(ctx, snapshot)
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
		snapshot = g.withIssueSummaries(ctx, snapshot)
		snapshot = g.withProjectAnalysis(ctx, snapshot)
		return buildIssueReportContent(snapshot), nil
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
			summary = mergeIssueSummary(summary, deterministicIssueSummary(snapshot.Issues[index]))
			summary.SummarySource = "ai"
			snapshot.Issues[index].Summary = summary
		}
	}
	return snapshot
}

func mergeIssueSummary(ai, fallback ReportIssueSummary) ReportIssueSummary {
	ai.Problem = strings.TrimSpace(sanitizeSensitiveText(ai.Problem))
	ai.Outcome = strings.TrimSpace(sanitizeSensitiveText(ai.Outcome))
	if ai.Problem == "" {
		ai.Problem = fallback.Problem
	}
	if ai.Outcome == "" {
		ai.Outcome = fallback.Outcome
	}
	if len(ai.Actions) == 0 {
		ai.Actions = fallback.Actions
	}
	if len(ai.OpenItems) == 0 {
		ai.OpenItems = fallback.OpenItems
	}
	if len(ai.WorkTypes) == 0 {
		ai.WorkTypes = fallback.WorkTypes
	}
	if len(ai.WorkDone) == 0 {
		ai.WorkDone = fallback.WorkDone
	}
	if len(ai.Artifacts) == 0 {
		ai.Artifacts = fallback.Artifacts
	}
	if strings.TrimSpace(ai.Impact) == "" {
		ai.Impact = fallback.Impact
	}
	if len(ai.EvidenceIDs) == 0 {
		ai.EvidenceIDs = fallback.EvidenceIDs
	}
	ai.Confidence = normalizeReportConfidence(ai.Confidence)
	return sanitizeReportIssueSummary(ai)
}

func (g *ReportGenerator) withProjectAnalysis(ctx context.Context, snapshot ReportSnapshot) ReportSnapshot {
	snapshot.AnalysisVersion = reportAnalysisVersion
	snapshot.WorkItems = buildReportWorkItems(snapshot)
	snapshot.ProjectAnalysis = deterministicProjectAnalysis(snapshot)
	if g.LLM == nil || !g.LLM.Enabled() || len(snapshot.WorkItems) == 0 {
		return snapshot
	}

	prompt, err := json.Marshal(prepareProjectPrompt(snapshot))
	if err != nil {
		return appendReportAnalysisWarning(snapshot, "项目业务分析输入编码失败，已使用确定性摘要。")
	}

	generateCtx, cancel := context.WithTimeout(ctx, reportLLMTimeout)
	defer cancel()
	raw, err := g.LLM.GenerateJSON(
		generateCtx,
		"",
		projectReportSystemPrompt,
		string(prompt),
		reportLLMTemperature,
		reportLLMMaxCompletionToken,
	)
	if err != nil {
		slog.Warn("project report: using deterministic project analysis", "reason", "LLM request failed", "error", err)
		return appendReportAnalysisWarning(snapshot, "项目业务分析暂不可用，已使用确定性摘要。")
	}

	analysis, err := parseProjectAnalysis(raw, snapshot)
	if err != nil {
		slog.Warn("project report: using deterministic project analysis", "reason", "LLM returned invalid project analysis", "error", err)
		return appendReportAnalysisWarning(snapshot, "项目业务分析结果未通过证据校验，已使用确定性摘要。")
	}
	snapshot.ProjectAnalysis = mergeProjectAnalysis(analysis, snapshot.ProjectAnalysis)
	return snapshot
}

type reportProjectPrompt struct {
	PeriodType       string               `json:"period_type"`
	RangeStart       time.Time            `json:"range_start"`
	RangeEnd         time.Time            `json:"range_end"`
	Timezone         string               `json:"timezone"`
	ActiveIssueCount int                  `json:"active_issue_count"`
	WorkItems        []ReportWorkItem     `json:"work_items"`
	Risks            []ReportAnalysisNote `json:"risks"`
}

func prepareProjectPrompt(snapshot ReportSnapshot) reportProjectPrompt {
	workItems := make([]ReportWorkItem, 0, len(snapshot.WorkItems))
	for _, item := range snapshot.WorkItems {
		prepared := item
		prepared.IssueTitle = sanitizeSensitiveText(prepared.IssueTitle)
		prepared.Title = sanitizeSensitiveText(prepared.Title)
		prepared.Description = sanitizeSensitiveText(prepared.Description)
		prepared.Outcome = sanitizeSensitiveText(prepared.Outcome)
		prepared.Impact = sanitizeSensitiveText(prepared.Impact)
		workItems = append(workItems, prepared)
	}
	deterministic := snapshot.ProjectAnalysis
	risks := append([]ReportAnalysisNote(nil), deterministic.Risks...)
	for index := range risks {
		risks[index].Title = sanitizeSensitiveText(risks[index].Title)
		risks[index].Description = sanitizeSensitiveText(risks[index].Description)
	}
	return reportProjectPrompt{
		PeriodType:       snapshot.PeriodType,
		RangeStart:       snapshot.RangeStart,
		RangeEnd:         snapshot.RangeEnd,
		Timezone:         snapshot.Timezone,
		ActiveIssueCount: snapshot.ActiveIssueCount,
		WorkItems:        workItems,
		Risks:            risks,
	}
}

func parseProjectAnalysis(raw string, snapshot ReportSnapshot) (ReportProjectAnalysis, error) {
	raw = llm.StripJSONFence(raw)
	var analysis ReportProjectAnalysis
	if err := json.Unmarshal([]byte(raw), &analysis); err != nil {
		return ReportProjectAnalysis{}, err
	}
	analysis.Summary = strings.TrimSpace(sanitizeSensitiveText(analysis.Summary))
	if analysis.Summary == "" || len(analysis.Summary) > 3000 {
		return ReportProjectAnalysis{}, fmt.Errorf("project analysis summary is missing or too long")
	}

	validEvidence := reportInRangeEvidenceSet(snapshot)
	analysis.EvidenceIDs = validateReportEvidenceIDs(analysis.EvidenceIDs, validEvidence)
	if len(validEvidence) > 0 && len(analysis.EvidenceIDs) == 0 {
		return ReportProjectAnalysis{}, fmt.Errorf("project analysis has no valid evidence")
	}
	var err error
	analysis.Changes, err = normalizeProjectChanges(analysis.Changes, validEvidence)
	if err != nil {
		return ReportProjectAnalysis{}, err
	}
	analysis.Risks, err = normalizeAnalysisNotes(analysis.Risks, validEvidence)
	if err != nil {
		return ReportProjectAnalysis{}, err
	}
	analysis.NextSteps, err = normalizeAnalysisNotes(analysis.NextSteps, validEvidence)
	if err != nil {
		return ReportProjectAnalysis{}, err
	}
	analysis.Confidence = normalizeReportConfidence(analysis.Confidence)
	analysis.Source = "ai"
	return analysis, nil
}

func normalizeProjectChanges(changes []ReportProjectChange, validEvidence map[string]struct{}) ([]ReportProjectChange, error) {
	if len(changes) > 20 {
		return nil, fmt.Errorf("too many project changes")
	}
	result := make([]ReportProjectChange, 0, len(changes))
	for index, change := range changes {
		change.ID = strings.TrimSpace(change.ID)
		if change.ID == "" {
			change.ID = fmt.Sprintf("change-%d", index+1)
		}
		change.Category = normalizeReportCategory(change.Category)
		change.Title = strings.TrimSpace(sanitizeSensitiveText(change.Title))
		change.Description = strings.TrimSpace(sanitizeSensitiveText(change.Description))
		change.Impact = strings.TrimSpace(sanitizeSensitiveText(change.Impact))
		change.Status = strings.TrimSpace(sanitizeSensitiveText(change.Status))
		if change.Status == "" {
			change.Status = "待确认"
		}
		if change.Title == "" || change.Description == "" || len(change.Title) > 500 || len(change.Description) > 2000 || len(change.Impact) > 1500 {
			return nil, fmt.Errorf("invalid project change at index %d", index)
		}
		change.EvidenceIDs = validateReportEvidenceIDs(change.EvidenceIDs, validEvidence)
		if len(change.EvidenceIDs) == 0 {
			return nil, fmt.Errorf("project change %q has no valid evidence", change.Title)
		}
		change.Confidence = normalizeReportConfidence(change.Confidence)
		change.Source = "ai"
		result = append(result, change)
	}
	return result, nil
}

func normalizeAnalysisNotes(notes []ReportAnalysisNote, validEvidence map[string]struct{}) ([]ReportAnalysisNote, error) {
	if len(notes) > 20 {
		return nil, fmt.Errorf("too many analysis notes")
	}
	result := make([]ReportAnalysisNote, 0, len(notes))
	for index, note := range notes {
		note.Title = strings.TrimSpace(sanitizeSensitiveText(note.Title))
		note.Description = strings.TrimSpace(sanitizeSensitiveText(note.Description))
		if note.Title == "" || note.Description == "" || len(note.Title) > 500 || len(note.Description) > 2000 {
			return nil, fmt.Errorf("invalid analysis note at index %d", index)
		}
		note.EvidenceIDs = validateReportEvidenceIDs(note.EvidenceIDs, validEvidence)
		if len(note.EvidenceIDs) == 0 {
			return nil, fmt.Errorf("analysis note %q has no valid evidence", note.Title)
		}
		note.Confidence = normalizeReportConfidence(note.Confidence)
		note.Source = "ai"
		result = append(result, note)
	}
	return result, nil
}

func mergeProjectAnalysis(ai, fallback ReportProjectAnalysis) ReportProjectAnalysis {
	if len(ai.Changes) == 0 {
		ai.Changes = fallback.Changes
	}
	if len(ai.Risks) == 0 {
		ai.Risks = fallback.Risks
	}
	if len(ai.NextSteps) == 0 {
		ai.NextSteps = fallback.NextSteps
	}
	if len(ai.EvidenceIDs) == 0 {
		ai.EvidenceIDs = fallback.EvidenceIDs
	}
	if ai.Confidence == "" {
		ai.Confidence = "medium"
	}
	ai.Source = "ai"
	return ai
}

func appendReportAnalysisWarning(snapshot ReportSnapshot, warning string) ReportSnapshot {
	if warning == "" {
		return snapshot
	}
	for _, existing := range snapshot.AnalysisWarnings {
		if existing == warning {
			return snapshot
		}
	}
	snapshot.AnalysisWarnings = append(snapshot.AnalysisWarnings, warning)
	return snapshot
}

var reportCategoryKeywords = map[string][]string{
	"bug_fix": {
		"bug", "bugfix", "fix", "defect", "regression", "error", "crash", "异常", "报错", "缺陷", "修复", "故障",
	},
	"feature": {
		"feature", "capability", "enhancement", "功能", "能力", "新增", "增加", "支持", "上线",
	},
	"architecture": {
		"architecture", "architect", "refactor", "schema", "performance", "scalability", "infra", "架构", "重构", "性能", "扩展性", "基础设施",
	},
	"design": {
		"design", "proposal", "solution", "方案", "设计", "决策", "技术选型", "规范",
	},
	"research": {
		"research", "investigate", "investigation", "spike", "调研", "研究", "分析", "排查", "验证",
	},
	"operations": {
		"deploy", "deployment", "release", "build", "config", "configuration", "ops", "运维", "部署", "发布", "构建", "配置", "重启",
	},
	"discussion": {
		"discussion", "comment", "review", "feedback", "discuss", "讨论", "评审", "审核", "沟通",
	},
	"risk": {
		"blocked", "blocker", "risk", "dependency", "阻塞", "风险", "依赖", "待确认",
	},
}

var reportCategoryOrder = []string{
	"bug_fix",
	"feature",
	"architecture",
	"design",
	"research",
	"operations",
	"discussion",
	"risk",
	"misc",
}

func normalizeReportCategory(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case "bug", "bugfix", "bug_fix", "defect", "fix":
		return "bug_fix"
	case "feature", "function", "capability", "enhancement":
		return "feature"
	case "architecture", "architect", "refactor":
		return "architecture"
	case "design", "proposal", "solution":
		return "design"
	case "research", "investigation", "analysis":
		return "research"
	case "operations", "operation", "ops", "deployment", "release":
		return "operations"
	case "discussion", "review", "communication":
		return "discussion"
	case "risk", "blocker", "blocked":
		return "risk"
	case "misc", "other":
		return "misc"
	default:
		return "misc"
	}
}

func normalizeReportConfidence(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "high":
		return "high"
	case "medium", "中":
		return "medium"
	case "low", "低":
		return "low"
	default:
		return "low"
	}
}

func reportInRangeEvidenceSet(snapshot ReportSnapshot) map[string]struct{} {
	valid := make(map[string]struct{})
	for _, issue := range snapshot.Issues {
		for _, event := range issue.Timeline {
			if event.InRange && strings.TrimSpace(event.ID) != "" {
				valid[event.ID] = struct{}{}
			}
		}
	}
	return valid
}

func validateReportEvidenceIDs(ids []string, valid map[string]struct{}) []string {
	if len(ids) == 0 || len(valid) == 0 {
		return nil
	}
	result := make([]string, 0, minReportInt(len(ids), 20))
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := valid[id]; !ok {
			continue
		}
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		seen[id] = struct{}{}
		result = append(result, id)
		if len(result) == 20 {
			break
		}
	}
	return result
}

func minReportInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}

func reportIssueEvidence(issue ReportIssue) []string {
	result := make([]string, 0, len(issue.Timeline))
	seen := make(map[string]struct{}, len(issue.Timeline))
	for _, event := range issue.Timeline {
		if !event.InRange || strings.TrimSpace(event.ID) == "" {
			continue
		}
		if _, duplicate := seen[event.ID]; duplicate {
			continue
		}
		seen[event.ID] = struct{}{}
		result = append(result, event.ID)
	}
	return result
}

func reportTextForClassification(issue ReportIssue) string {
	parts := []string{issue.Title, issue.Description, issue.Status}
	for _, event := range issue.Timeline {
		if !event.InRange {
			continue
		}
		parts = append(parts, event.Content, event.Action, string(event.Details))
	}
	return strings.ToLower(strings.Join(parts, " "))
}

func inferReportCategories(issue ReportIssue) []string {
	text := reportTextForClassification(issue)
	categories := make([]string, 0, len(reportCategoryOrder))
	for _, category := range reportCategoryOrder[:len(reportCategoryOrder)-1] {
		for _, keyword := range reportCategoryKeywords[category] {
			if strings.Contains(text, strings.ToLower(keyword)) {
				categories = append(categories, category)
				break
			}
		}
	}
	if len(categories) == 0 {
		categories = append(categories, "misc")
	}
	return categories
}

func normalizeReportCategories(values []string, fallback []string) []string {
	result := make([]string, 0, minReportInt(len(values), 8))
	seen := make(map[string]struct{}, 8)
	for _, value := range values {
		category := normalizeReportCategory(value)
		if category == "misc" && strings.TrimSpace(value) == "" {
			continue
		}
		if _, duplicate := seen[category]; duplicate {
			continue
		}
		seen[category] = struct{}{}
		result = append(result, category)
		if len(result) == 8 {
			break
		}
	}
	if len(result) == 0 {
		for _, category := range fallback {
			if _, duplicate := seen[category]; duplicate {
				continue
			}
			seen[category] = struct{}{}
			result = append(result, category)
			if len(result) == 8 {
				break
			}
		}
	}
	if len(result) == 0 {
		return []string{"misc"}
	}
	return result
}

func reportEventWorkDescription(event ReportTimelineEvent) string {
	var value string
	switch event.Type {
	case "comment":
		value = event.Content
	case "issue_status_history":
		var details struct {
			From string `json:"from_status"`
			To   string `json:"to_status"`
		}
		if json.Unmarshal(event.Details, &details) == nil && details.From != "" && details.To != "" {
			value = fmt.Sprintf("状态从 %s 变更为 %s", details.From, details.To)
		}
	case "agent_task_queue":
		var details struct {
			Status string          `json:"status"`
			Error  string          `json:"error"`
			Result json.RawMessage `json:"result"`
		}
		if json.Unmarshal(event.Details, &details) == nil {
			switch {
			case details.Error != "":
				value = "任务失败：" + details.Error
			case details.Status != "":
				value = "任务状态：" + details.Status
			case len(details.Result) > 0 && string(details.Result) != "null":
				value = "任务结果：" + string(details.Result)
			}
		}
	case "activity_log":
		value = strings.TrimSpace(strings.Join([]string{event.Action, event.Content}, " · "))
	default:
		value = strings.TrimSpace(strings.Join([]string{event.Action, event.Content}, " · "))
	}
	value = strings.Join(strings.Fields(sanitizeSensitiveText(value)), " ")
	return truncateReportText(value, 500)
}

func reportStatusLabel(status string) string {
	return map[string]string{
		"backlog":     "待规划",
		"todo":        "待处理",
		"in_progress": "进行中",
		"in_review":   "待审核",
		"done":        "已完成",
		"blocked":     "阻塞",
		"cancelled":   "已取消",
	}[status]
}

func buildReportWorkItems(snapshot ReportSnapshot) []ReportWorkItem {
	items := make([]ReportWorkItem, 0, len(snapshot.Issues))
	for _, issue := range snapshot.Issues {
		fallbackCategories := inferReportCategories(issue)
		categories := normalizeReportCategories(issue.Summary.WorkTypes, fallbackCategories)
		category := categories[0]
		workDone := make([]string, 0, 6)
		for _, value := range issue.Summary.WorkDone {
			value = strings.TrimSpace(sanitizeSensitiveText(value))
			if value != "" && len(workDone) < 8 {
				workDone = append(workDone, truncateReportText(value, 500))
			}
		}
		if len(workDone) == 0 {
			for _, event := range issue.Timeline {
				if !event.InRange {
					continue
				}
				if value := reportEventWorkDescription(event); value != "" && len(workDone) < 8 {
					workDone = append(workDone, value)
				}
			}
		}
		if len(workDone) == 0 {
			workDone = append(workDone, "已记录本周期工作活动，具体内容待人工确认。")
		}
		description := strings.Join(workDone, "；")
		outcome := strings.TrimSpace(sanitizeSensitiveText(issue.Summary.Outcome))
		if outcome == "" {
			label := reportStatusLabel(issue.Status)
			if label == "" {
				label = issue.Status
			}
			outcome = "当前状态：" + label + "。"
		}
		confidence := normalizeReportConfidence(issue.Summary.Confidence)
		impact := strings.TrimSpace(sanitizeSensitiveText(issue.Summary.Impact))
		if impact == "" {
			impact = "业务影响待确认。"
		}
		evidenceIDs := validateReportEvidenceIDs(issue.Summary.EvidenceIDs, reportEvidenceSetForIssue(issue))
		if len(evidenceIDs) == 0 {
			evidenceIDs = reportIssueEvidence(issue)
		}
		title := strings.TrimSpace(issue.Title)
		if title == "" {
			title = issue.Identifier
		}
		items = append(items, ReportWorkItem{
			ID:          issue.IssueID,
			IssueID:     issue.IssueID,
			Identifier:  issue.Identifier,
			IssueTitle:  title,
			Category:    category,
			Categories:  categories,
			Title:       title,
			Description: description,
			Outcome:     outcome,
			Impact:      impact,
			Status:      issue.Status,
			EvidenceIDs: evidenceIDs,
			Confidence:  confidence,
			Source:      issue.Summary.SummarySource,
		})
	}
	return items
}

func reportEvidenceSetForIssue(issue ReportIssue) map[string]struct{} {
	valid := make(map[string]struct{})
	for _, event := range issue.Timeline {
		if event.InRange && strings.TrimSpace(event.ID) != "" {
			valid[event.ID] = struct{}{}
		}
	}
	return valid
}

func reportUniqueEvidenceIDs(items []ReportWorkItem) []string {
	result := make([]string, 0)
	seen := make(map[string]struct{})
	for _, item := range items {
		for _, evidenceID := range item.EvidenceIDs {
			if _, duplicate := seen[evidenceID]; duplicate {
				continue
			}
			seen[evidenceID] = struct{}{}
			result = append(result, evidenceID)
		}
	}
	return result
}

func deterministicProjectAnalysis(snapshot ReportSnapshot) ReportProjectAnalysis {
	items := snapshot.WorkItems
	analysis := ReportProjectAnalysis{
		Summary:     "本周期的项目变化依据 issue 工作记录生成；业务收益和外部影响仍需结合实际验证确认。",
		Changes:     make([]ReportProjectChange, 0, minReportInt(len(items), 20)),
		Risks:       make([]ReportAnalysisNote, 0),
		NextSteps:   make([]ReportAnalysisNote, 0),
		EvidenceIDs: reportUniqueEvidenceIDs(items),
		Confidence:  "medium",
		Source:      "deterministic",
	}
	seenChange := make(map[string]struct{})
	seenRisk := make(map[string]struct{})
	seenNext := make(map[string]struct{})
	for _, item := range items {
		changeKey := item.IssueID + ":" + item.Category
		if _, exists := seenChange[changeKey]; !exists && len(analysis.Changes) < 20 {
			seenChange[changeKey] = struct{}{}
			impact := strings.TrimSpace(item.Impact)
			if impact == "" {
				impact = "业务影响待确认。"
			}
			analysis.Changes = append(analysis.Changes, ReportProjectChange{
				ID:          "change-" + item.ID,
				Category:    item.Category,
				Title:       item.Title,
				Description: item.Description,
				Impact:      impact,
				Status:      reportStatusLabel(item.Status),
				EvidenceIDs: item.EvidenceIDs,
				Confidence:  item.Confidence,
				Source:      "deterministic",
			})
		}
		if item.Status == "blocked" {
			key := item.IssueID + ":blocked"
			if _, exists := seenRisk[key]; !exists && len(analysis.Risks) < 20 {
				seenRisk[key] = struct{}{}
				analysis.Risks = append(analysis.Risks, ReportAnalysisNote{
					Title:       item.Identifier + " 存在阻塞",
					Description: "该 issue 当前处于阻塞状态，解除依赖后才能继续推进。",
					EvidenceIDs: item.EvidenceIDs,
					Confidence:  item.Confidence,
					Source:      "deterministic",
				})
			}
		}
		if item.Status != "done" && item.Status != "cancelled" {
			key := item.IssueID + ":next"
			if _, exists := seenNext[key]; !exists && len(analysis.NextSteps) < 20 {
				seenNext[key] = struct{}{}
				description := "继续推进并在完成后更新 issue 状态。"
				if item.Status == "blocked" {
					description = "先确认依赖和解除阻塞条件，再继续推进。"
				}
				analysis.NextSteps = append(analysis.NextSteps, ReportAnalysisNote{
					Title:       item.Identifier + " 后续推进",
					Description: description,
					EvidenceIDs: item.EvidenceIDs,
					Confidence:  item.Confidence,
					Source:      "deterministic",
				})
			}
		}
	}
	if len(items) == 0 {
		analysis.Summary = "本周期没有可用于项目变化分析的工作项。"
	}
	return analysis
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
		prepared.Summary = sanitizeReportIssueSummary(prepared.Summary)
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

func sanitizeReportIssueSummary(summary ReportIssueSummary) ReportIssueSummary {
	summary.Problem = sanitizeSensitiveText(summary.Problem)
	summary.Outcome = sanitizeSensitiveText(summary.Outcome)
	summary.Impact = sanitizeSensitiveText(summary.Impact)
	for index := range summary.Actions {
		summary.Actions[index] = sanitizeSensitiveText(summary.Actions[index])
	}
	for index := range summary.OpenItems {
		summary.OpenItems[index] = sanitizeSensitiveText(summary.OpenItems[index])
	}
	for index := range summary.WorkTypes {
		summary.WorkTypes[index] = sanitizeSensitiveText(summary.WorkTypes[index])
	}
	for index := range summary.WorkDone {
		summary.WorkDone[index] = sanitizeSensitiveText(summary.WorkDone[index])
	}
	for index := range summary.Artifacts {
		summary.Artifacts[index] = sanitizeSensitiveText(summary.Artifacts[index])
	}
	return summary
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
	issuesByID := make(map[string]ReportIssue, len(issues))
	for _, issue := range issues {
		expected[issue.IssueID] = struct{}{}
		issuesByID[issue.IssueID] = issue
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
		summary.IssueID = strings.TrimSpace(summary.IssueID)
		if _, ok := expected[summary.IssueID]; !ok || summary.IssueID == "" {
			continue
		}
		if _, duplicate := parsed[summary.IssueID]; duplicate {
			continue
		}
		summary.Actions = normalizeReportStringList(summary.Actions, 8, 1000)
		summary.OpenItems = normalizeReportStringList(summary.OpenItems, 8, 1000)
		summary.WorkTypes = normalizeReportStringList(summary.WorkTypes, 8, 100)
		summary.WorkDone = normalizeReportStringList(summary.WorkDone, 8, 1000)
		summary.Artifacts = normalizeReportStringList(summary.Artifacts, 8, 1000)
		summary.Problem = strings.TrimSpace(sanitizeSensitiveText(summary.Problem))
		summary.Outcome = strings.TrimSpace(sanitizeSensitiveText(summary.Outcome))
		summary.Impact = strings.TrimSpace(sanitizeSensitiveText(summary.Impact))
		if len(summary.Problem) > 2000 || len(summary.Outcome) > 2000 || len(summary.Impact) > 1500 {
			continue
		}
		issue := issuesByID[summary.IssueID]
		validEvidence := reportEvidenceSetForIssue(issue)
		providedEvidence := len(summary.EvidenceIDs) > 0
		summary.EvidenceIDs = validateReportEvidenceIDs(summary.EvidenceIDs, validEvidence)
		if providedEvidence && len(summary.EvidenceIDs) == 0 && len(validEvidence) > 0 {
			continue
		}
		summary.Confidence = normalizeReportConfidence(summary.Confidence)
		parsed[summary.IssueID] = summary
	}
	if len(parsed) == 0 && len(issues) > 0 {
		return nil, fmt.Errorf("no valid issue summaries")
	}
	return parsed, nil
}

func normalizeReportStringList(values []string, maxItems, maxLength int) []string {
	if len(values) > maxItems {
		return nil
	}
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(sanitizeSensitiveText(value))
		if value == "" {
			continue
		}
		if len(value) > maxLength {
			return nil
		}
		result = append(result, value)
	}
	return result
}

func deterministicIssueSummary(issue ReportIssue) ReportIssueSummary {
	statusLabel := reportStatusLabel(issue.Status)
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

	workDone := make([]string, 0, 8)
	for _, event := range issue.Timeline {
		if !event.InRange || len(workDone) >= 8 {
			continue
		}
		if value := reportEventWorkDescription(event); value != "" {
			workDone = append(workDone, value)
		}
	}
	if len(workDone) == 0 {
		workDone = append(workDone, "已记录本周期工作活动，具体内容待人工确认。")
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
		WorkTypes:     inferReportCategories(issue),
		WorkDone:      workDone,
		Impact:        "业务影响待确认。",
		EvidenceIDs:   reportIssueEvidence(issue),
		Confidence:    "low",
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
	for index := range snapshot.Issues {
		if snapshot.Issues[index].Summary.Problem == "" {
			snapshot.Issues[index].Summary = deterministicIssueSummary(snapshot.Issues[index])
		}
	}
	if len(snapshot.WorkItems) == 0 {
		snapshot.WorkItems = buildReportWorkItems(snapshot)
	}
	if snapshot.ProjectAnalysis.Summary == "" {
		snapshot.ProjectAnalysis = deterministicProjectAnalysis(snapshot)
	}

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

	content.WriteString("## 工作执行版\n\n")
	if len(snapshot.WorkItems) == 0 {
		content.WriteString("- 本周期没有可列出的工作项。\n\n")
	} else {
		for _, item := range snapshot.WorkItems {
			fmt.Fprintf(&content, "- **%s** %s：%s\n", reportCategoryLabel(item.Category), item.Identifier, item.Title)
			fmt.Fprintf(&content, "  - 实际工作：%s\n", item.Description)
			fmt.Fprintf(&content, "  - 结果：%s\n", item.Outcome)
			fmt.Fprintf(&content, "  - 状态：%s\n", reportStatusOrUnknown(item.Status))
			if len(item.EvidenceIDs) == 0 {
				content.WriteString("  - 证据：待人工确认\n")
			} else {
				fmt.Fprintf(&content, "  - 证据：%s\n", strings.Join(item.EvidenceIDs, ", "))
			}
		}
		content.WriteString("\n")
	}

	content.WriteString("## 项目业务推进版\n\n")
	content.WriteString(snapshot.ProjectAnalysis.Summary + "\n\n")
	if len(snapshot.ProjectAnalysis.Changes) == 0 {
		content.WriteString("- 暂无可确认的项目能力变化。\n\n")
	} else {
		for _, change := range snapshot.ProjectAnalysis.Changes {
			fmt.Fprintf(&content, "- **%s** %s：%s\n", reportCategoryLabel(change.Category), change.Title, change.Description)
			fmt.Fprintf(&content, "  - 状态：%s\n", reportStatusOrUnknown(change.Status))
			fmt.Fprintf(&content, "  - 影响：%s\n", nonEmptyReportText(change.Impact, "业务影响待确认。"))
			if len(change.EvidenceIDs) > 0 {
				fmt.Fprintf(&content, "  - 证据：%s\n", strings.Join(change.EvidenceIDs, ", "))
			} else {
				content.WriteString("  - 证据：待人工确认\n")
			}
		}
		content.WriteString("\n")
	}

	content.WriteString("## 风险与待确认\n\n")
	if len(snapshot.ProjectAnalysis.Risks) == 0 && len(snapshot.ProjectAnalysis.NextSteps) == 0 && len(snapshot.AnalysisWarnings) == 0 {
		content.WriteString("- 暂无额外风险；业务影响仍需结合实际结果确认。\n\n")
	} else {
		for _, note := range snapshot.ProjectAnalysis.Risks {
			fmt.Fprintf(&content, "- 风险：%s——%s\n", note.Title, note.Description)
			appendReportEvidenceLine(&content, note.EvidenceIDs)
		}
		for _, note := range snapshot.ProjectAnalysis.NextSteps {
			fmt.Fprintf(&content, "- 下一步：%s——%s\n", note.Title, note.Description)
			appendReportEvidenceLine(&content, note.EvidenceIDs)
		}
		for _, warning := range snapshot.AnalysisWarnings {
			fmt.Fprintf(&content, "- 待确认：%s\n", warning)
		}
		content.WriteString("\n")
	}

	content.WriteString("## 证据明细\n\n")
	appendReportEvidenceDetails(&content, snapshot)
	if len(snapshot.Issues) == 0 {
		content.WriteString("暂无活跃 issue。\n")
	}
	return buildReportContent(content.String(), snapshot)
}

func reportCategoryLabel(category string) string {
	return map[string]string{
		"bug_fix":      "Bug 修复",
		"feature":      "功能/能力",
		"architecture": "架构",
		"design":       "方案设计",
		"research":     "调研分析",
		"operations":   "运维发布",
		"discussion":   "讨论决策",
		"risk":         "风险处理",
		"misc":         "其他",
	}[normalizeReportCategory(category)]
}

func reportStatusOrUnknown(status string) string {
	if label := reportStatusLabel(status); label != "" {
		return label
	}
	if strings.TrimSpace(status) == "" {
		return "待确认"
	}
	return status
}

func nonEmptyReportText(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func appendReportEvidenceLine(content *strings.Builder, evidenceIDs []string) {
	if len(evidenceIDs) == 0 {
		content.WriteString("  - 证据：待人工确认\n")
		return
	}
	fmt.Fprintf(content, "  - 证据：%s\n", strings.Join(evidenceIDs, ", "))
}

func appendReportEvidenceDetails(content *strings.Builder, snapshot ReportSnapshot) {
	hasEvidence := false
	for _, issue := range snapshot.Issues {
		for _, event := range issue.Timeline {
			if !event.InRange || strings.TrimSpace(event.ID) == "" {
				continue
			}
			hasEvidence = true
			description := reportEventWorkDescription(event)
			if description == "" {
				description = "已记录工作事件。"
			}
			fmt.Fprintf(content, "- `%s` · %s · %s · %s：%s\n", event.ID, issue.Identifier, event.OccurredAt.Format(time.RFC3339), event.Type, description)
		}
	}
	if !hasEvidence {
		content.WriteString("- 暂无可展示的周期内证据。\n")
	}
	content.WriteString("\n")
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

const reportSystemPrompt = `You are an issue-centered project reporting assistant. Return exactly one JSON object shaped {"summaries":[{"issue_id":"...","problem":"...","actions":["..."],"outcome":"...","open_items":["..."],"work_types":["bug_fix|feature|architecture|design|research|operations|discussion|risk|misc"],"work_done":["..."],"artifacts":["..."],"impact":"...","evidence_ids":["..."],"confidence":"high|medium|low"}]}.
Do not wrap the JSON in Markdown code fences. Do not add prose before or after the JSON object.
Return at most one summary for each supplied issue_id. Use the exact supplied issue_id; never invent, merge, or omit an issue silently. Every field is required. actions and open_items must be arrays of concise strings.
Summarize only the supplied issue timeline. A timeline entry with in_range=false is historical context and must not be counted as work in the requested period. The optional work fields must describe actual work evidenced by in-range events. evidence_ids must contain only IDs from that issue's in-range timeline. When optional work fields cannot be established, return empty arrays or a low confidence instead of guessing.
Do not reveal or repeat tokens, passwords, API keys, authorization headers, private keys, or other secrets even if they appear in the input.
Do not introduce other projects, periods, numbers, status changes, or assumptions. The AI's role is wording only. Write concise, professional Chinese.`

const projectReportSystemPrompt = `You are a project reporting analyst. Return exactly one JSON object shaped {"summary":"...","changes":[],"risks":[],"next_steps":[],"evidence_ids":[],"confidence":"high|medium|low"}.
Each changes item must have category, title, description, impact, status, and evidence_ids. Each risks and next_steps item must have title, description, and evidence_ids. Use only the supplied work_items and their evidence IDs. Every change, risk, and next step must cite at least one supplied evidence ID; if the business impact is not explicit in the evidence, say "业务影响待确认" instead of guessing.
Do not introduce other projects, periods, numbers, statuses, milestones, users, or facts. Do not treat comment or task counts as business value. Do not reveal secrets. Do not wrap the JSON in Markdown fences or add prose. Write concise, professional Chinese.`

const legacyReportSystemPrompt = `You are a project reporting assistant. Return exactly one JSON object shaped {"content":"markdown"}.
Do not wrap the JSON in Markdown code fences. Do not add prose before or after the JSON object.
The markdown content must use these sections in order: 完成事项, 进行中事项, 阻塞/风险, 逾期事项, 下一步重点. Do not write 数据指标; the caller adds that section.
Only cite issues and facts present in the supplied JSON. Never introduce other projects, periods, calculations, numbers, or assumptions.
Preserve issue identifiers exactly. Do not change any counts; use only the supplied *_count fields.
The AI's role is wording only. Write concise, professional Chinese Markdown.`
