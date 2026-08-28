package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
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
)

type ReportLLM interface {
	Enabled() bool
	GenerateJSON(ctx context.Context, model, systemPrompt, userPrompt string, temperature float64, maxCompletionTokens int64) (string, error)
}

type ReportIssue struct {
	Identifier string `json:"identifier"`
	Title      string `json:"title"`
}

type ReportSnapshot struct {
	PeriodType      string        `json:"period_type"`
	RangeStart      time.Time     `json:"range_start"`
	RangeEnd        time.Time     `json:"range_end"`
	Timezone        string        `json:"timezone"`
	GeneratedAt     time.Time     `json:"generated_at"`
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
	snapshot, err := aggregator.Aggregate(
		ctx, project, rangeStart, rangeEnd, now,
	)
	if err != nil {
		return nil, "", err
	}
	snapshot.PeriodType = periodType
	snapshot.RangeStart = rangeStart.In(location)
	snapshot.RangeEnd = rangeEnd.In(location)
	snapshot.Timezone = timezoneName

	snapshotJSON, err := json.Marshal(snapshot)
	if err != nil {
		return nil, "", fmt.Errorf("encode report snapshot: %w", err)
	}

	content, err := g.generateContent(ctx, snapshot)
	if err != nil {
		return nil, "", err
	}
	return snapshotJSON, content, nil
}

func (g *ReportGenerator) generateContent(ctx context.Context, snapshot ReportSnapshot) (string, error) {
	snapshotJSON, err := json.Marshal(snapshot)
	if err != nil {
		return "", fmt.Errorf("encode report prompt: %w", err)
	}
	// The database snapshot is the source of truth. A report must remain
	// useful even when the optional narrative model is disabled or unavailable.
	// The deterministic fallback below also prevents a provider formatting issue
	// from turning an otherwise valid report job into a failed cron execution.
	if g.LLM == nil || !g.LLM.Enabled() {
		return buildDeterministicReportContent(snapshot), nil
	}
	generateCtx, cancel := context.WithTimeout(ctx, reportLLMTimeout)
	defer cancel()
	raw, err := g.LLM.GenerateJSON(
		generateCtx,
		"",
		reportSystemPrompt,
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
	return fmt.Sprintf(`%s

## 数据指标

- 完成事项：%d
- 进行中事项：%d
- 阻塞/风险：%d
- 逾期事项：%d
- 取消事项：%d`, content, snapshot.CompletedCount, snapshot.InProgressCount, snapshot.BlockedCount, snapshot.OverdueCount, snapshot.CancelledCount)
}

const reportSystemPrompt = `You are a project reporting assistant. Return exactly one JSON object shaped {"content":"markdown"}.
Do not wrap the JSON in Markdown code fences. Do not add prose before or after the JSON object.
The markdown content must use these sections in order: 完成事项, 进行中事项, 阻塞/风险, 逾期事项, 下一步重点. Do not write 数据指标; the caller adds that section.
Only cite issues and facts present in the supplied JSON. Never introduce other projects, periods, calculations, numbers, or assumptions.
Preserve issue identifiers exactly. Do not change any counts; use only the supplied *_count fields.
The AI's role is wording only. Write concise, professional Chinese Markdown.`
