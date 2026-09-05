package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/multica-ai/multica/server/pkg/llm"
)

// The narrative pipeline replaces the raw event-dump prompt with a map-reduce
// flow: code selects the conversation per issue (Stage 0), one LLM call per
// issue summarizes that conversation (Stage 1), and one project-level call
// writes the executive narrative (Stage 2). Status transitions and raw
// agent-task payloads stay out of the prompts as noise; task outcomes only
// feed the narrative as bounded one-line facts.
const (
	reportNarrativeVersion        = 1
	reportNarrativeMaxIssues      = 30
	reportNarrativeConcurrency    = 5
	reportNarrativeMaxChars       = 8000
	reportNarrativeMaxPerLine     = 600
	reportNarrativeMaxSummaryText = 300
)

// ReportIssueNarrative is the Stage-1 output for one issue: what the
// conversation actually produced, not what the status machine did.
type ReportIssueNarrative struct {
	IssueID    string   `json:"issue_id"`
	Identifier string   `json:"identifier"`
	Title      string   `json:"title"`
	Business   string   `json:"business_domain,omitempty"`
	StatusFrom string   `json:"status_from,omitempty"`
	StatusTo   string   `json:"status_to,omitempty"`
	Done       string   `json:"done"`
	Outcome    string   `json:"outcome,omitempty"`
	Evidence   []string `json:"evidence,omitempty"`
	Risks      []string `json:"risks,omitempty"`
	Noteworthy bool     `json:"noteworthy"`
	Source     string   `json:"source"` // "ai" | "deterministic"
}

// withNarratives runs Stage 0 + Stage 1 and folds the result into the
// snapshot: snapshot.Narratives carries the L2 detail layer, and each issue
// summary inherits the narrative outcome so the existing work-item views
// render the same story.
func (g *ReportGenerator) withNarratives(ctx context.Context, snapshot ReportSnapshot) ReportSnapshot {
	for index := range snapshot.Issues {
		snapshot.Issues[index].Summary = deterministicIssueSummary(snapshot.Issues[index])
	}

	narratives := deterministicNarratives(snapshot)
	if g.LLM != nil && g.LLM.Enabled() {
		narratives = g.narrateIssues(ctx, snapshot, narratives)
	}

	byIssue := make(map[string]ReportIssueNarrative, len(narratives))
	for _, narrative := range narratives {
		byIssue[narrative.IssueID] = narrative
	}
	for index := range snapshot.Issues {
		narrative, ok := byIssue[snapshot.Issues[index].IssueID]
		if !ok {
			continue
		}
		summary := snapshot.Issues[index].Summary
		if narrative.Done != "" {
			summary.Actions = appendUniqueReportText(summary.Actions, narrative.Done, 4)
			summary.WorkDone = appendUniqueReportText(summary.WorkDone, narrative.Done, 4)
		}
		if narrative.Outcome != "" {
			summary.Outcome = narrative.Outcome
		}
		if len(narrative.Risks) > 0 {
			summary.Risks = narrative.Risks
		}
		if narrative.Source == "ai" {
			summary.SummarySource = "ai"
		}
		snapshot.Issues[index].Summary = sanitizeReportIssueSummary(summary)
	}

	snapshot.Narratives = narratives
	snapshot.NarrativeVersion = reportNarrativeVersion
	return snapshot
}

// narrateIssues summarizes each issue's conversation with a bounded worker
// pool. Any per-issue failure keeps the deterministic narrative, so one bad
// response never blocks the report.
func (g *ReportGenerator) narrateIssues(ctx context.Context, snapshot ReportSnapshot, fallback []ReportIssueNarrative) []ReportIssueNarrative {
	issues := reportNarrativeIssueOrder(snapshot)
	fallbackByIssue := make(map[string]ReportIssueNarrative, len(fallback))
	for _, narrative := range fallback {
		fallbackByIssue[narrative.IssueID] = narrative
	}

	resultsByIssue := make(map[string]ReportIssueNarrative, len(fallback))
	for _, narrative := range fallback {
		resultsByIssue[narrative.IssueID] = narrative
	}
	var (
		mu      sync.Mutex
		wg      sync.WaitGroup
		sem     = make(chan struct{}, reportNarrativeConcurrency)
		failure bool
	)
	for _, issue := range issues {
		issue := issue
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			narrative, ok := g.narrateIssue(ctx, issue)
			if !ok {
				narrative = fallbackByIssue[issue.IssueID]
			}
			mu.Lock()
			resultsByIssue[issue.IssueID] = narrative
			failure = failure || !ok
			mu.Unlock()
		}()
	}
	wg.Wait()
	if failure {
		slog.Warn("project report: some issue narratives used deterministic fallback")
	}

	results := make([]ReportIssueNarrative, 0, len(snapshot.Issues))
	for _, issue := range snapshot.Issues {
		if narrative, ok := resultsByIssue[issue.IssueID]; ok {
			results = append(results, narrative)
		}
	}
	sort.SliceStable(results, func(left, right int) bool {
		return results[left].Identifier < results[right].Identifier
	})
	return results
}

// reportNarrativeIssueOrder picks which issues earn a Stage-1 call: the most
// discussed issues first, capped at reportNarrativeMaxIssues to bound cost.
func reportNarrativeIssueOrder(snapshot ReportSnapshot) []ReportIssue {
	issues := make([]ReportIssue, 0, len(snapshot.Issues))
	for _, issue := range snapshot.Issues {
		if reportNarrativeActivity(issue) > 0 {
			issues = append(issues, issue)
		}
	}
	sort.SliceStable(issues, func(left, right int) bool {
		leftActivity := reportNarrativeActivity(issues[left])
		rightActivity := reportNarrativeActivity(issues[right])
		if leftActivity != rightActivity {
			return leftActivity > rightActivity
		}
		return issues[left].Identifier < issues[right].Identifier
	})
	if len(issues) > reportNarrativeMaxIssues {
		issues = issues[:reportNarrativeMaxIssues]
	}
	return issues
}

func reportNarrativeActivity(issue ReportIssue) int {
	activity := 0
	for _, event := range issue.Timeline {
		if reportConversationEventContent(event) != "" {
			activity++
		}
	}
	return activity
}

func (g *ReportGenerator) narrateIssue(ctx context.Context, issue ReportIssue) (ReportIssueNarrative, bool) {
	conversation := buildIssueConversation(issue)
	if conversation == nil {
		return ReportIssueNarrative{}, false
	}
	payload := map[string]any{
		"identifier":   issue.Identifier,
		"title":        sanitizeSensitiveText(issue.Title),
		"description":  sanitizeSensitiveText(issue.Description),
		"conversation": conversation,
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return ReportIssueNarrative{}, false
	}

	generateCtx, cancel := context.WithTimeout(ctx, reportLLMTimeout)
	defer cancel()
	raw, err := g.LLM.GenerateJSON(
		generateCtx,
		"",
		narrativeIssueSystemPrompt,
		string(payloadJSON),
		reportLLMTemperature,
		1024,
	)
	if err != nil {
		return ReportIssueNarrative{}, false
	}
	return parseIssueNarrative(raw, issue)
}

func parseIssueNarrative(raw string, issue ReportIssue) (ReportIssueNarrative, bool) {
	raw = llm.StripJSONFence(raw)
	var parsed struct {
		Done       string   `json:"done"`
		Outcome    string   `json:"outcome"`
		Evidence   []string `json:"evidence"`
		Risks      []string `json:"risks"`
		Noteworthy *bool    `json:"noteworthy"`
	}
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return ReportIssueNarrative{}, false
	}
	parsed.Done = truncateReportText(strings.TrimSpace(parsed.Done), reportNarrativeMaxSummaryText)
	parsed.Outcome = truncateReportText(strings.TrimSpace(parsed.Outcome), reportNarrativeMaxSummaryText)
	if parsed.Done == "" && parsed.Outcome == "" {
		return ReportIssueNarrative{}, false
	}
	statusFrom, statusTo := issueStatusSpan(issue)
	narrative := ReportIssueNarrative{
		IssueID:    issue.IssueID,
		Identifier: issue.Identifier,
		Title:      issue.Title,
		Business:   issue.BusinessDomain,
		StatusFrom: statusFrom,
		StatusTo:   statusTo,
		Done:       parsed.Done,
		Outcome:    parsed.Outcome,
		Evidence:   normalizeReportStringList(parsed.Evidence, 4, 80),
		Risks:      normalizeReportStringList(parsed.Risks, 3, 120),
		Noteworthy: parsed.Noteworthy == nil || *parsed.Noteworthy,
		Source:     "ai",
	}
	if narrative.Noteworthy && narrative.Done == "" && narrative.Outcome == "" {
		narrative.Noteworthy = false
	}
	return narrative, true
}

// buildIssueConversation renders the Stage-0 view: discussion content in
// chronological order, with agent-task outcomes as one-liners. Status
// transitions are deliberately excluded — they carry no story.
func buildIssueConversation(issue ReportIssue) []string {
	type line struct {
		occurred time.Time
		text     string
	}
	lines := make([]line, 0, len(issue.Timeline))
	for _, event := range issue.Timeline {
		switch event.Type {
		case "comment":
			content := strings.TrimSpace(sanitizeSensitiveText(event.Content))
			if content == "" {
				continue
			}
			content = truncateReportText(content, reportNarrativeMaxPerLine)
			prefix := "用户"
			if event.AuthorType == "agent" {
				prefix = "AI"
			}
			if !event.InRange {
				prefix += "(窗口前上下文)"
			}
			lines = append(lines, line{event.OccurredAt, fmt.Sprintf("[%s] %s", prefix, content)})
		case "agent_task_queue", "agent_task":
			if outcome := reportConversationEventContent(event); outcome != "" {
				lines = append(lines, line{event.OccurredAt, "[AI任务] " + outcome})
			}
		}
	}
	sort.SliceStable(lines, func(left, right int) bool {
		return lines[left].occurred.Before(lines[right].occurred)
	})

	total := 0
	start := 0
	for index := range lines {
		total += len(lines[index].text)
	}
	for start < len(lines) && total > reportNarrativeMaxChars {
		total -= len(lines[start].text)
		start++
	}
	conversation := make([]string, 0, len(lines)-start)
	for index := start; index < len(lines); index++ {
		conversation = append(conversation, lines[index].text)
	}
	if len(conversation) == 0 {
		return nil
	}
	return conversation
}

// issueStatusSpan compresses the window's status churn into one transition.
func issueStatusSpan(issue ReportIssue) (string, string) {
	from := ""
	to := issue.Status
	for _, event := range issue.Timeline {
		if event.Type != "issue_status_history" || !event.InRange {
			continue
		}
		details := map[string]any{}
		if len(event.Details) > 0 {
			_ = json.Unmarshal(event.Details, &details)
		}
		if value, ok := details["from_status"].(string); ok && from == "" {
			from = value
		}
		if value, ok := details["to_status"].(string); ok {
			to = value
		}
	}
	if from == "" {
		from = to
	}
	return from, to
}

func deterministicNarratives(snapshot ReportSnapshot) []ReportIssueNarrative {
	narratives := make([]ReportIssueNarrative, 0, len(snapshot.Issues))
	for _, issue := range snapshot.Issues {
		statusFrom, statusTo := issueStatusSpan(issue)
		workDone := reportIssueWorkFacts(issue)
		done := ""
		outcome := ""
		if len(workDone) > 0 {
			done = workDone[0]
			outcome = workDone[len(workDone)-1]
		}
		narrative := ReportIssueNarrative{
			IssueID:    issue.IssueID,
			Identifier: issue.Identifier,
			Title:      issue.Title,
			Business:   issue.BusinessDomain,
			StatusFrom: statusFrom,
			StatusTo:   statusTo,
			Done:       done,
			Outcome:    outcome,
			Evidence:   reportIssueEvidence(issue),
			Noteworthy: done != "" || outcome != "",
			Source:     "deterministic",
		}
		narratives = append(narratives, narrative)
	}
	return narratives
}

// withExecutiveSummary runs Stage 2: one call turns the per-issue narratives
// into the L1 narrative. A custom template prompt takes over here, matching
// the template contract.
func (g *ReportGenerator) withExecutiveSummary(ctx context.Context, snapshot ReportSnapshot, templatePrompt string) ReportSnapshot {
	snapshot.ProjectAnalysis = deterministicProjectAnalysis(narrativeAnalysisSnapshot(snapshot))
	snapshot.ProjectAnalysis.Summary = deterministicExecutiveSummary(snapshot)
	snapshot.ExecutiveSummary = snapshot.ProjectAnalysis.Summary
	noteworthy := reduceNarratives(snapshot)
	if g.LLM == nil || !g.LLM.Enabled() || len(noteworthy) == 0 {
		return snapshot
	}

	grouped := groupNarrativePromptGroups(noteworthy)
	payload := map[string]any{
		"period_type": snapshot.PeriodType,
		"range": map[string]string{
			"start": snapshot.RangeStart.Format("2006-01-02"),
			"end":   snapshot.RangeEnd.Format("2006-01-02"),
		},
		"groups": grouped,
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		snapshot.AnalysisWarnings = append(snapshot.AnalysisWarnings, "项目叙事汇总输入编码失败，已使用确定性摘要。")
		return snapshot
	}

	sysPrompt := narrativeProjectSystemPrompt
	if templatePrompt != "" {
		sysPrompt = templatePrompt
	}
	generateCtx, cancel := context.WithTimeout(ctx, reportLLMTimeout)
	defer cancel()
	raw, err := g.LLM.GenerateJSON(
		generateCtx,
		"",
		sysPrompt,
		string(payloadJSON),
		reportLLMTemperature,
		reportLLMMaxCompletionToken,
	)
	if err != nil {
		slog.Warn("project report: executive summary used deterministic fallback", "error", err)
		snapshot.AnalysisWarnings = append(snapshot.AnalysisWarnings, "AI 叙事汇总不可用，已使用确定性摘要。")
		return snapshot
	}
	summary, err := parseExecutiveSummary(raw)
	if err != nil {
		snapshot.AnalysisWarnings = append(snapshot.AnalysisWarnings, "AI 叙事汇总返回格式无效，已使用确定性摘要。")
		return snapshot
	}
	snapshot.ProjectAnalysis.Summary = summary
	snapshot.ProjectAnalysis.Source = "ai"
	snapshot.ExecutiveSummary = summary
	return snapshot
}

func narrativeAnalysisSnapshot(snapshot ReportSnapshot) ReportSnapshot {
	noteworthyIDs := make(map[string]struct{})
	for _, narrative := range snapshot.Narratives {
		if narrative.Noteworthy {
			noteworthyIDs[narrative.IssueID] = struct{}{}
		}
	}
	issues := make([]ReportIssue, 0, len(noteworthyIDs))
	for _, issue := range snapshot.Issues {
		if _, ok := noteworthyIDs[issue.IssueID]; ok {
			issues = append(issues, issue)
		}
	}
	snapshot.Issues = issues
	snapshot.ActiveIssueCount = len(issues)
	return snapshot
}

// reduceNarratives bounds the project-level prompt independently from the
// per-issue fallback list. The most discussed issues get the scarce Reduce
// context budget; the full list remains in the snapshot for L2/L3 review.
func reduceNarratives(snapshot ReportSnapshot) []ReportIssueNarrative {
	issueActivity := make(map[string]int, len(snapshot.Issues))
	for _, issue := range snapshot.Issues {
		issueActivity[issue.IssueID] = reportNarrativeActivity(issue)
	}
	noteworthy := make([]ReportIssueNarrative, 0, len(snapshot.Narratives))
	for _, narrative := range snapshot.Narratives {
		if narrative.Noteworthy {
			noteworthy = append(noteworthy, narrative)
		}
	}
	sort.SliceStable(noteworthy, func(left, right int) bool {
		leftActivity := issueActivity[noteworthy[left].IssueID]
		rightActivity := issueActivity[noteworthy[right].IssueID]
		if leftActivity != rightActivity {
			return leftActivity > rightActivity
		}
		return noteworthy[left].Identifier < noteworthy[right].Identifier
	})
	if len(noteworthy) > reportNarrativeMaxIssues {
		noteworthy = noteworthy[:reportNarrativeMaxIssues]
	}
	return noteworthy
}

func parseExecutiveSummary(raw string) (string, error) {
	raw = llm.StripJSONFence(raw)
	var parsed struct {
		ExecutiveSummary string `json:"executive_summary"`
		Content          string `json:"content"`
	}
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return "", err
	}
	summary := strings.TrimSpace(parsed.ExecutiveSummary)
	if summary == "" {
		summary = strings.TrimSpace(parsed.Content)
	}
	if summary == "" {
		return "", fmt.Errorf("executive summary is empty")
	}
	return truncateReportText(strings.TrimSpace(sanitizeSensitiveText(summary)), 4000), nil
}

type reportNarrativeGroup struct {
	Business   string                 `json:"business_domain"`
	IssueCount int                    `json:"issue_count"`
	Issues     []ReportIssueNarrative `json:"issues"`
}

func groupNarrativesByBusiness(narratives []ReportIssueNarrative) []reportNarrativeGroup {
	order := []string{}
	groups := map[string]*reportNarrativeGroup{}
	for _, narrative := range narratives {
		business := narrative.Business
		if business == "" {
			business = "其他"
		}
		group, ok := groups[business]
		if !ok {
			group = &reportNarrativeGroup{Business: business}
			groups[business] = group
			order = append(order, business)
		}
		group.IssueCount++
		group.Issues = append(group.Issues, narrative)
	}
	result := make([]reportNarrativeGroup, 0, len(order))
	for _, business := range order {
		result = append(result, *groups[business])
	}
	return result
}

type reportNarrativePromptItem struct {
	IssueID    string   `json:"issue_id"`
	Identifier string   `json:"identifier"`
	Title      string   `json:"title"`
	Done       string   `json:"done"`
	Outcome    string   `json:"outcome,omitempty"`
	Evidence   []string `json:"evidence,omitempty"`
	Risks      []string `json:"risks,omitempty"`
}

type reportNarrativePromptGroup struct {
	Business   string                      `json:"business_domain"`
	IssueCount int                         `json:"issue_count"`
	Issues     []reportNarrativePromptItem `json:"issues"`
}

func groupNarrativePromptGroups(narratives []ReportIssueNarrative) []reportNarrativePromptGroup {
	groups := make([]reportNarrativePromptGroup, 0)
	indexes := make(map[string]int)
	for _, narrative := range narratives {
		business := narrative.Business
		if business == "" {
			business = "其他"
		}
		index, ok := indexes[business]
		if !ok {
			index = len(groups)
			indexes[business] = index
			groups = append(groups, reportNarrativePromptGroup{
				Business: business,
				Issues:   make([]reportNarrativePromptItem, 0),
			})
		}
		group := &groups[index]
		group.IssueCount++
		group.Issues = append(group.Issues, reportNarrativePromptItem{
			IssueID:    narrative.IssueID,
			Identifier: narrative.Identifier,
			Title:      sanitizeSensitiveText(narrative.Title),
			Done:       sanitizeSensitiveText(narrative.Done),
			Outcome:    sanitizeSensitiveText(narrative.Outcome),
			Evidence:   normalizeReportStringList(narrative.Evidence, 4, 80),
			Risks:      normalizeReportStringList(narrative.Risks, 3, 120),
		})
	}
	return groups
}

// deterministicExecutiveSummary is the no-LLM L1: short domain lines with
// only noteworthy outcomes, no status noise.
func deterministicExecutiveSummary(snapshot ReportSnapshot) string {
	var builder strings.Builder
	for _, group := range groupNarrativesByBusiness(reduceNarratives(snapshot)) {
		items := make([]string, 0, len(group.Issues))
		for _, narrative := range group.Issues {
			if !narrative.Noteworthy {
				continue
			}
			text := narrative.Done
			if text == "" {
				text = narrative.Outcome
			}
			if text == "" {
				continue
			}
			items = append(items, fmt.Sprintf("%s %s", narrative.Identifier, text))
		}
		if len(items) == 0 {
			continue
		}
		fmt.Fprintf(&builder, "- **%s**：%s。\n", group.Business, strings.Join(items, "；"))
	}
	if builder.Len() == 0 {
		builder.WriteString("- 本周期内没有可写入报告的实质性进展。\n")
	}
	return strings.TrimRight(builder.String(), "\n")
}

// buildNarrativeReportContent assembles the final markdown: L1 executive
// narrative, L2 per-issue details backing it, then the deterministic stats
// and evidence appendices for human review.
func buildNarrativeReportContent(snapshot ReportSnapshot) string {
	var content strings.Builder
	periodLabel := "本周"
	switch snapshot.PeriodType {
	case "daily":
		periodLabel = "今日"
	case "monthly":
		periodLabel = "本月"
	}
	fmt.Fprintf(&content, "## %s摘要\n\n", periodLabel)
	if snapshot.ExecutiveSummary != "" {
		content.WriteString(snapshot.ExecutiveSummary)
		content.WriteString("\n\n")
	} else if snapshot.ProjectAnalysis.Summary != "" {
		content.WriteString(snapshot.ProjectAnalysis.Summary)
		content.WriteString("\n\n")
	}

	content.WriteString("## 分项进展\n\n")
	noteworthyCount := 0
	for _, group := range groupNarrativesByBusiness(snapshot.Narratives) {
		wrote := false
		for _, narrative := range group.Issues {
			if !narrative.Noteworthy {
				continue
			}
			noteworthyCount++
			if !wrote {
				fmt.Fprintf(&content, "### %s\n\n", group.Business)
				wrote = true
			}
			fmt.Fprintf(&content, "- **%s %s**", narrative.Identifier, narrative.Title)
			content.WriteString("\n")
			if narrative.Done != "" {
				fmt.Fprintf(&content, "  - 做了什么：%s\n", narrative.Done)
			}
			if narrative.Outcome != "" {
				fmt.Fprintf(&content, "  - 结果：%s\n", narrative.Outcome)
			}
			if len(narrative.Risks) > 0 {
				fmt.Fprintf(&content, "  - 风险：%s\n", strings.Join(narrative.Risks, "；"))
			}
			if len(narrative.Evidence) > 0 {
				fmt.Fprintf(&content, "  - 证据：%s\n", strings.Join(narrative.Evidence, ", "))
			}
		}
	}
	if noteworthyCount == 0 {
		content.WriteString("- 本周期内没有可写入报告的实质性进展，详情见讨论记录附录。\n")
	}
	fmt.Fprintf(&content, "\n其余非重点事项与完整讨论记录见\"原始讨论\"。\n\n")

	return buildReportContent(content.String(), snapshot)
}

const narrativeIssueSystemPrompt = `你是项目周报助理。阅读一个 issue 的标题、描述与窗口期内的讨论记录（[用户] 为人写的发言，[AI] 为智能体的回复，[AI任务] 为后台任务结果），输出一个 JSON 对象：{"done":"本周期实际做了什么","outcome":"当前结论或结果","evidence":["支撑结论的证据，如测试结果、部署地址、合并的PR"],"risks":["遗留风险或阻碍"],"noteworthy":true或false}。
要求：
- 只依据讨论内容总结，不要编造；讨论里没有的结论不要写。
- 把讨论中的技术过程翻译成业务语言，而不是复述聊天记录或状态变化。
- 没有部署或验收证据时，不要写"已上线"。
- noteworthy 表示这个 issue 本周期是否有值得写进周报的实质进展。
- 不要输出 JSON 以外的任何内容，不要使用 Markdown 代码围栏。`

const narrativeProjectSystemPrompt = `你是资深项目经理，正在撰写项目周报的核心摘要。输入是本周期每个 issue 的真实总结，已按业务域分组（含每个 issue 的做了什么、结果、证据与风险）。
输出一个 JSON 对象：{"executive_summary":"Markdown 文本"}。
要求：
- 用叙事式中文 Markdown 总结本周核心成果与整体进展，按业务域组织，不要逐条罗列 issue。
- 不要出现状态流转流水、聊天记录、置信度或"业务域/工作类型"等元数据字样。
- 每个结论都必须来自输入的 issue 总结，不要引入输入之外的事实；没有验收证据不要写"已上线"。
- 结尾用一行点出主要风险或下一步重点。
- 摘要控制在 500 字以内。不要输出 JSON 以外的任何内容，不要使用 Markdown 代码围栏。`
