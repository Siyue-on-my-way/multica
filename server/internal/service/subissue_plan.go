package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/multica-ai/multica/server/pkg/llm"
	"golang.org/x/sync/errgroup"
)

const (
	SubissuePlanMaxCount       = 4
	SubissuePlanItemMaxCount   = 24
	SubissuePlanTitleMaxLength = 300
	SubissuePlanGoalMaxLength  = 1200
	SubissuePlanConstraintMax  = 2000
	SubissuePlanBusinessMaxLen = 80

	SubissuePlanItemImplementation = "implementation"
	SubissuePlanItemSummaryTest    = "summary_test"
	SubissuePlanOverallBusiness    = "整体流程"
	SubissuePlanSummaryThreshold   = 2

	// SubissuePlanCoverageFull marks the plan the backend has verified to
	// reference every identified source task. The frontend defaults to it.
	SubissuePlanCoverageFull = "full"

	// SubissuePlanFullName is the reserved name of the first, coverage-checked
	// plan. The model is told to emit it first; the parser enforces position.
	SubissuePlanFullName = "全量覆盖"

	// subissuePlanMaxAttempts bounds the generate→validate loop per stage. A
	// coverage miss or malformed JSON regenerates with corrective feedback
	// instead of ever reaching the preview panel incomplete (SIY-147).
	subissuePlanMaxAttempts = 3

	// subissuePlanOutlineMaxCompletionTokens is the outline-stage fallback
	// budget. It must cover identified_tasks plus three plans; the YAML stage
	// raises it the same way for registry deployments.
	subissuePlanOutlineMaxCompletionTokens = 8192

	// subissuePlanDetailMaxCompletionTokens is the detail-stage fallback
	// budget, raised from 4096: 24 items with full descriptions overflowed the
	// old budget and the truncated tail silently dropped sub-issues.
	subissuePlanDetailMaxCompletionTokens = 16384
)

// SubissueIdentifiedTask is one explicit task/direction recognized in the
// source discussion. IDs are draft-only and stable for the whole planning
// round: plan items reference them via SourceTaskIDs so the backend can prove
// the full-coverage plan lost nothing.
type SubissueIdentifiedTask struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

// SubissuePlanItem is the intentionally small outline shown before the
// detail-generation call. IDs are draft-only and never become issue IDs.
// SourceTaskIDs names which identified source task(s) the item covers — the
// coverage proof for the full-coverage plan.
type SubissuePlanItem struct {
	ID            string   `json:"id"`
	Title         string   `json:"title"`
	Goal          string   `json:"goal"`
	Kind          string   `json:"kind,omitempty"`
	Business      string   `json:"business,omitempty"`
	SourceTaskIDs []string `json:"source_task_ids,omitempty"`
}

// SubissuePlan is one alternative decomposition strategy. It contains no
// description, parent, stage, or dependency details until the user approves
// it and asks for expansion. The first plan carries Coverage "full" after the
// backend verified it references every identified task; the remaining plans
// are optional granularity references.
type SubissuePlan struct {
	ID       string             `json:"id"`
	Name     string             `json:"name"`
	Coverage string             `json:"coverage,omitempty"`
	Items    []SubissuePlanItem `json:"items"`
}

type subissueIdentifiedTaskRaw struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

type subissuePlanLLMResponse struct {
	IdentifiedTasks []subissueIdentifiedTaskRaw `json:"identified_tasks"`
	Plans []struct {
		Name  string `json:"name"`
		Items []struct {
			Title         string   `json:"title"`
			Goal          string   `json:"goal"`
			Business      string   `json:"business"`
			SourceTaskIDs []string `json:"source_task_ids"`
		} `json:"items"`
	} `json:"plans"`
}

const subissuePlanSystemPrompt = `你是一个任务拆分方案设计助手。请根据一段 Multica issue 讨论，先识别原文的全部任务，再生成三种不同粒度的候选拆分方案。

第一步，识别任务清单：找出原文中所有明确提到的可执行任务、编号条目和工作方向（包括藏在段落里的独立工作项），逐条输出到 identified_tasks，每项一个稳定 id（从 t1 开始连续编号）和原文表述 text。不要遗漏、不要合并、也不要凭空添加原文没有的工作。

第二步，生成三个方案，第一个方案必须叫“全量覆盖”：
1. 全量覆盖（第一个方案）：必须覆盖 identified_tasks 中的每一个 id。强相关的任务可以合并到一个条目，但任何任务都不能丢。每个条目用 source_task_ids 标注它对应哪些任务 id。
2. 上下文优先：尽量合并强相关内容，允许最终只有一个子issue，保证接手者拥有连贯上下文（可选参考）。
3. 并行优先：把确实可以独立并行处理的任务分开，但不要为了增加数量而拆散强相关工作（可选参考）。

每个方案只返回子issue的 title、goal、business 和 source_task_ids。不要生成 description、stage、依赖、父issue或其他字段。business 必须是从原始评论、当前 issue 标题/description 和上级上下文中识别出的具体业务概念或产品能力，例如“生成子issue”“日报/周报/年报生成”“订单支付”，不能写成“任务”“功能”这类空泛词。强相关条目必须使用同一个 business；只要上下文能识别出业务，就必须填写该业务，不能留空，也不能使用“整体流程”。只有确实没有任何可识别业务概念时，才允许使用“整体流程”。business 不要包含【】；title 只写任务名称，服务端会统一添加【business】前缀。方案必须来自原始讨论，不能凭空添加工作。系统会在每个包含至少两个条目的业务后自动补充汇总测试，因此不要自己生成汇总测试。严格只输出 JSON，不要输出 Markdown 或解释文字。

输出结构：
{"identified_tasks":[{"id":"t1","text":"..."}],"plans":[{"name":"全量覆盖","items":[{"title":"...","goal":"...","business":"...","source_task_ids":["t1"]}]}]}`

const subissuePlanUserTemplate = `原始评论内容：
{{comment_text}}

当前 issue：{{issue_identifier}} {{issue_title}}
当前 issue description：
{{issue_description}}

{{ancestor_brief}}

已识别的原文任务清单（若提供，这就是权威任务来源，source_task_ids 必须使用这里的 id）：
{{identified_tasks}}

当前业务概念（优先用于每个条目的 business 和标题前缀；只有原始讨论明确包含多个不同业务时才分别使用不同概念）：
{{business_context}}

已有兄弟子issue（避免重复）：
{{siblings}}

候选父issue（仅供理解上下文，不要在本阶段输出父issue）：
{{candidate_parents}}

用户额外拆分要求：
{{human_constraints}}

上一次输出的校验结果（若非“（无）”，必须先修正该问题再重新输出完整 JSON）：
{{retry_feedback}}

请输出 2～3 种拆分方案，第一个必须是“全量覆盖”。`

// SubissuePlanStages bundles the stage clients the outline pass may use. Plan
// is the outline stage; Recognize is the optional recognition stage that
// enables segmented identification of over-budget comments — when nil or
// disabled, long comments degrade to the truncated single-pass outline.
type SubissuePlanStages struct {
	Plan      SubissueSuggestConfiguredLLM
	Recognize SubissueSuggestConfiguredLLM
}

// subissueRecognizeSystemPrompt is the recognition pass for one segment of an
// over-budget comment. It only identifies tasks; planning happens later so
// every segment's tasks are visible to the coverage proof.
const subissueRecognizeSystemPrompt = `你是任务识别助手。给你一段较长的 Multica issue 讨论的一个分段，请识别这个分段中所有明确提到的可执行任务、编号条目和工作方向。只依据分段原文，不要凭空添加，也不要把同一个任务重复输出。每项输出一个稳定 id（从 t1 开始连续编号）和原文表述 text。分段中没有可执行任务时输出空数组。严格只输出 JSON，不要输出 Markdown 或解释文字。

输出结构：
{"tasks":[{"id":"t1","text":"..."}]}`

const subissueRecognizeUserTemplate = `分段原文（第 {{segment_index}}/{{segment_total}} 段）：
{{comment_segment}}

上一次输出的校验结果（若非“（无）”，必须先修正该问题再重新输出完整 JSON）：
{{retry_feedback}}

请输出该分段识别到的任务。`

// subissueSegmentBudget is the per-segment rune budget for recognition. A
// comment at or under subissueSuggestCommentBudget stays whole — the 3000-ish
// rune comments the feature mostly sees never segment.
const subissueSegmentBudget = 5000

// subissueSegmentMax caps how many segments one comment may expand into. The
// recognition calls run concurrently, so this keeps worst-case latency and
// token spend bounded; a comment beyond the cap loses its tail (logged).
const subissueSegmentMax = 8

// subissueSegmentConcurrency bounds parallel recognition calls.
const subissueSegmentConcurrency = 4

// SuggestSubissuePlans performs the lightweight structure-only pass. The
// caller remains responsible for the workspace-scoped context lookup; this
// service only knows how to build and validate the model contract.
//
// The outline contract is coverage-checked (SIY-147): identified_tasks names
// every explicit task in the source, the first plan must reference all of
// them, and a miss regenerates with corrective feedback instead of returning
// a partial plan. Over-budget comments are recognized segment by segment and
// merged before planning so truncation cannot silently drop tasks.
func SuggestSubissuePlans(
	ctx context.Context,
	stages SubissuePlanStages,
	sourceIssue SubissueSuggestSourceIssue,
	sourceContent string,
	siblings []SubissueCandidateParent,
	candidateParents []SubissueCandidateParent,
	humanConstraints string,
	commentID string,
) ([]SubissuePlan, error) {
	if stages.Plan == nil || !stages.Plan.Enabled() {
		return nil, ErrLLMNotConfigured
	}
	content := strings.TrimSpace(sourceContent)
	identified, segmented, err := identifySubissueSourceTasks(ctx, stages.Recognize, content, commentID)
	if err != nil {
		return nil, err
	}
	return generateSubissuePlansWithRetry(
		ctx, stages.Plan, sourceIssue, content, siblings, candidateParents,
		humanConstraints, identified, segmented, commentID,
	)
}

// identifySubissueSourceTasks recognizes the source task list. Only comments
// over the suggestion budget are segmented; shorter ones let the outline call
// identify tasks itself, which keeps the common path at one upstream call.
func identifySubissueSourceTasks(
	ctx context.Context,
	recognizer SubissueSuggestConfiguredLLM,
	content string,
	commentID string,
) ([]SubissueIdentifiedTask, bool, error) {
	segments, ok := splitSubissueContentSegments(content, commentID)
	if !ok {
		return nil, false, nil
	}
	if recognizer == nil || !recognizer.Enabled() {
		slog.Warn("subissue suggest: comment exceeds budget but recognition stage is not configured; falling back to truncated outline",
			"comment_id", commentID, "input_chars", len([]rune(content)))
		return nil, false, nil
	}

	perSegment := make([][]SubissueIdentifiedTask, len(segments))
	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(subissueSegmentConcurrency)
	for index, segment := range segments {
		index, segment := index, segment
		group.Go(func() error {
			tasks, err := recognizeSubissueSegmentWithRetry(groupCtx, recognizer, segment, index+1, len(segments), commentID)
			if err != nil {
				return err
			}
			perSegment[index] = tasks
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		return nil, true, fmt.Errorf("identify subissue source tasks: %w", err)
	}
	merged := mergeSubissueSegmentTasks(perSegment)
	slog.Info("subissue suggest: segmented recognition merged",
		"comment_id", commentID, "segment_count", len(segments),
		"task_count", len(merged), "input_chars", len([]rune(content)))
	return merged, true, nil
}

// splitSubissueContentSegments cuts an over-budget comment into line-aligned
// segments of at most subissueSegmentBudget runes. Short comments report
// false — segmentation is strictly for the over-budget tail case.
func splitSubissueContentSegments(content string, commentID string) ([]string, bool) {
	runes := []rune(content)
	if len(runes) <= subissueSuggestCommentBudget {
		return nil, false
	}
	var segments []string
	var current strings.Builder
	currentLen := 0
	flush := func() {
		if currentLen > 0 {
			segments = append(segments, strings.TrimSpace(current.String()))
			current.Reset()
			currentLen = 0
		}
	}
	for _, line := range strings.SplitAfter(content, "\n") {
		lineRunes := []rune(line)
		if len(lineRunes) > subissueSegmentBudget {
			// One pathological line longer than a whole segment: hard-split it.
			flush()
			for len(lineRunes) > subissueSegmentBudget {
				segments = append(segments, strings.TrimSpace(string(lineRunes[:subissueSegmentBudget])))
				lineRunes = lineRunes[subissueSegmentBudget:]
			}
			current.WriteString(string(lineRunes))
			currentLen = len(lineRunes)
			continue
		}
		if currentLen+len(lineRunes) > subissueSegmentBudget {
			flush()
		}
		current.WriteString(line)
		currentLen += len(lineRunes)
	}
	flush()
	if len(segments) > subissueSegmentMax {
		slog.Warn("subissue suggest: comment exceeds segment cap; tail dropped from recognition",
			"comment_id", commentID, "segment_count", len(segments), "cap", subissueSegmentMax)
		segments = segments[:subissueSegmentMax]
	}
	return segments, true
}

// mergeSubissueSegmentTasks concatenates per-segment task lists, drops exact
// duplicates (a task restated across a segment boundary), and renumbers the
// surviving tasks into one authoritative t1..tN list.
func mergeSubissueSegmentTasks(perSegment [][]SubissueIdentifiedTask) []SubissueIdentifiedTask {
	seen := make(map[string]struct{})
	merged := make([]SubissueIdentifiedTask, 0)
	for _, segment := range perSegment {
		for _, task := range segment {
			text := strings.TrimSpace(task.Text)
			if text == "" {
				continue
			}
			key := normalizeSubissueTaskKey(text)
			if _, duplicate := seen[key]; duplicate {
				continue
			}
			seen[key] = struct{}{}
			merged = append(merged, SubissueIdentifiedTask{Text: text})
		}
	}
	for index := range merged {
		merged[index].ID = fmt.Sprintf("t%d", index+1)
	}
	return merged
}

func normalizeSubissueTaskKey(text string) string {
	key := strings.ToLower(text)
	key = strings.Join(strings.Fields(key), "")
	return strings.Trim(key, "。；;，,、.!！？?")
}

func recognizeSubissueSegmentWithRetry(
	ctx context.Context,
	recognizer SubissueSuggestConfiguredLLM,
	segment string,
	index, total int,
	commentID string,
) ([]SubissueIdentifiedTask, error) {
	var lastErr error
	for attempt := 1; attempt <= subissuePlanMaxAttempts; attempt++ {
		variables := map[string]string{
			"comment_segment": segment,
			"segment_index":   fmt.Sprintf("%d", index),
			"segment_total":   fmt.Sprintf("%d", total),
			"retry_feedback":  subissueRetryFeedback(lastErr, attempt),
		}
		raw, stats, err := generateSubissueTemplateJSON(
			ctx, recognizer, variables,
			subissueRecognizeSystemPrompt, subissueRecognizeUserTemplate, 0.2, 4096,
		)
		tasks := []SubissueIdentifiedTask(nil)
		if err == nil {
			tasks, err = parseSubissueRecognizeResponse(raw)
		}
		logSubissueAttempt("subissue recognize", commentID, attempt, stats, "task_count", len(tasks), err)
		if err == nil {
			return tasks, nil
		}
		lastErr = err
	}
	return nil, fmt.Errorf("recognize segment %d/%d failed after %d attempts: %w", index, total, subissuePlanMaxAttempts, lastErr)
}

func parseSubissueRecognizeResponse(raw string) ([]SubissueIdentifiedTask, error) {
	var parsed struct {
		Tasks []struct {
			ID   string `json:"id"`
			Text string `json:"text"`
		} `json:"tasks"`
	}
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return nil, fmt.Errorf("parse subissue recognition: %w", err)
	}
	tasks := make([]SubissueIdentifiedTask, 0, len(parsed.Tasks))
	for _, task := range parsed.Tasks {
		if strings.TrimSpace(task.Text) == "" {
			continue
		}
		tasks = append(tasks, SubissueIdentifiedTask{
			ID:   strings.ToLower(strings.TrimSpace(task.ID)),
			Text: strings.TrimSpace(task.Text),
		})
	}
	return tasks, nil
}

// generateSubissuePlansWithRetry runs the outline call under the coverage
// gate: every attempt is parsed and checked, and a miss feeds the failure text
// back into the next attempt. An incomplete plan can therefore never leave
// this function — only a validated one or an error.
func generateSubissuePlansWithRetry(
	ctx context.Context,
	client SubissueSuggestConfiguredLLM,
	sourceIssue SubissueSuggestSourceIssue,
	sourceContent string,
	siblings []SubissueCandidateParent,
	candidateParents []SubissueCandidateParent,
	humanConstraints string,
	identified []SubissueIdentifiedTask,
	segmented bool,
	commentID string,
) ([]SubissuePlan, error) {
	identifiedText := "（未提供：请直接从上方原始评论中识别任务清单）"
	if segmented {
		encoded, err := json.Marshal(identified)
		if err != nil {
			return nil, fmt.Errorf("encode identified subissue tasks: %w", err)
		}
		identifiedText = string(encoded)
	}
	var lastErr error
	for attempt := 1; attempt <= subissuePlanMaxAttempts; attempt++ {
		variables := buildSubissuePlanVariables(sourceIssue, sourceContent, siblings, candidateParents, humanConstraints, "")
		variables["identified_tasks"] = identifiedText
		variables["retry_feedback"] = subissueRetryFeedback(lastErr, attempt)
		raw, stats, err := generateSubissueTemplateJSON(
			ctx, client, variables,
			subissuePlanSystemPrompt, subissuePlanUserTemplate, 0.4, subissuePlanOutlineMaxCompletionTokens,
		)
		var plans []SubissuePlan
		if err == nil {
			var parsedIdentified []SubissueIdentifiedTask
			plans, parsedIdentified, err = parseSubissuePlansResult(raw, inferSubissueBusiness(sourceIssue, sourceContent))
			if err == nil {
				// A segmented round already saw the whole comment; its task
				// list is authoritative over whatever the outline pass echoes.
				authoritative := identified
				if !segmented {
					authoritative = parsedIdentified
				}
				err = validateSubissuePlanCoverage(authoritative, plans)
			}
		}
		itemCount := 0
		for _, plan := range plans {
			itemCount += len(plan.Items)
		}
		logSubissueAttempt("subissue outline", commentID, attempt, stats, "plan_count", len(plans), err)
		if err == nil {
			slog.Info("subissue outline validated",
				"comment_id", commentID, "attempt", attempt, "segmented", segmented,
				"identified_task_count", len(identified), "plan_count", len(plans),
				"item_count", itemCount, "input_chars", stats.InputChars, "output_chars", stats.OutputChars)
			return plans, nil
		}
		lastErr = err
	}
	return nil, fmt.Errorf("generate subissue plans: validation failed after %d attempts: %w", subissuePlanMaxAttempts, lastErr)
}

// subissueRetryFeedback renders the corrective note fed into a retry attempt.
func subissueRetryFeedback(lastErr error, attempt int) string {
	if attempt == 1 || lastErr == nil {
		return "（无）"
	}
	return fmt.Sprintf("上一次输出未通过校验，原因：%v。请修正该问题后重新输出完整的 JSON。", lastErr)
}

// logSubissueAttempt emits one structured line per model call with the fields
// SIY-147 requires for triage: comment, attempt, char/token usage, finish
// reason, and the validated output count.
func logSubissueAttempt(operation, commentID string, attempt int, stats llm.GenerateStats, countName string, count int, err error) {
	attrs := []any{
		"comment_id", commentID,
		"attempt", attempt,
		"input_chars", stats.InputChars,
		"output_chars", stats.OutputChars,
		"prompt_tokens", stats.PromptTokens,
		"completion_tokens", stats.CompletionTokens,
		"finish_reason", stats.FinishReason,
		countName, count,
	}
	if err != nil {
		attrs = append(attrs, "error", err.Error())
		slog.Warn(operation+" attempt failed", attrs...)
		return
	}
	slog.Info(operation+" attempt", attrs...)
}

const subissueDetailSystemPrompt = `你是一个 Multica 子issue详情生成助手。用户已经人工确认了拆分草稿，你只能为草稿中的每个条目补充可执行的详细 description、stage、依赖关系、建议父issue和 confidence。

必须严格保留 approved_outline 中每个条目的 id、title、goal、数量和顺序，不得增加、删除、合并、拆分或改写这些字段。description 必须让接手者不需要回看原始讨论就能开工，并包含背景、已经确定的约束、工作范围和验收标准。summary_test 条目必须写集成测试、联调和整体验收步骤，服务端会把它安排到所属业务所有开发条目之后。严格只输出 JSON，不要输出 Markdown 或解释文字。

输出结构：
{"subissues":[{"id":"plan-1-item-1","title":"...","goal":"...","description":"...","stage":1,"depends_on_ids":[],"suggested_parent_identifier":null,"confidence":0.86}]}`

const subissueDetailUserTemplate = `原始评论内容：
{{comment_text}}

当前 issue：{{issue_identifier}} {{issue_title}}
当前 issue description：
{{issue_description}}

{{ancestor_brief}}

当前业务概念（已用于确认草稿的标题前缀）：
{{business_context}}

已有兄弟子issue：
{{siblings}}

候选父issue：
{{candidate_parents}}

用户额外拆分要求：
{{human_constraints}}

已确认的拆分草稿（这是不可变结构）：
{{approved_outline}}

请仅为已确认条目生成详细内容。`

type subissueDetailLLMResponse struct {
	Subissues []struct {
		ID                        string   `json:"id"`
		Title                     string   `json:"title"`
		Goal                      string   `json:"goal"`
		Description               string   `json:"description"`
		Stage                     int      `json:"stage"`
		DependsOnIDs              []string `json:"depends_on_ids"`
		SuggestedParentIdentifier *string  `json:"suggested_parent_identifier"`
		Confidence                float64  `json:"confidence"`
	} `json:"subissues"`
}

// ExpandSubissuePlan fills in details only after the outline has been
// confirmed. The approved title/goal are copied back from the draft even if a
// model returns a slightly different wording, so an LLM can never silently
// change the user's selected structure. The response is validated against the
// approved outline (count and ids) and retried with corrective feedback, so a
// truncated or incomplete detail pass cannot reach issue creation (SIY-147).
func ExpandSubissuePlan(
	ctx context.Context,
	llmClient SubissueSuggestConfiguredLLM,
	sourceIssue SubissueSuggestSourceIssue,
	sourceContent string,
	siblings []SubissueCandidateParent,
	candidateParents []SubissueCandidateParent,
	plan SubissuePlan,
	humanConstraints string,
	commentID string,
) ([]SubissueSuggestion, error) {
	if llmClient == nil || !llmClient.Enabled() {
		return nil, ErrLLMNotConfigured
	}
	plan.Items = normalizeSubissuePlanTitlesWithBusiness(plan.Items, inferSubissueBusiness(sourceIssue, sourceContent))
	if err := validateApprovedSubissuePlan(plan); err != nil {
		return nil, err
	}
	approved, err := json.Marshal(plan)
	if err != nil {
		return nil, fmt.Errorf("marshal approved subissue plan: %w", err)
	}
	var lastErr error
	for attempt := 1; attempt <= subissuePlanMaxAttempts; attempt++ {
		variables := buildSubissuePlanVariables(sourceIssue, sourceContent, siblings, candidateParents, humanConstraints, string(approved))
		variables["identified_tasks"] = "（详情阶段不需要）"
		variables["retry_feedback"] = subissueRetryFeedback(lastErr, attempt)
		raw, stats, err := generateSubissueTemplateJSON(
			ctx,
			llmClient,
			variables,
			subissueDetailSystemPrompt,
			subissueDetailUserTemplate,
			0.3,
			subissuePlanDetailMaxCompletionTokens,
		)
		var details []SubissueSuggestion
		if err == nil {
			details, err = parseSubissueDetailsResponse(raw, plan)
		}
		logSubissueAttempt("subissue detail", commentID, attempt, stats, "item_count", len(details), err)
		if err == nil {
			return includeAncestorBriefInSubissueDescriptions(details, sourceIssue.AncestorBrief), nil
		}
		lastErr = err
	}
	return nil, fmt.Errorf("generate subissue details: validation failed after %d attempts: %w", subissuePlanMaxAttempts, lastErr)
}

func buildSubissuePlanVariables(
	sourceIssue SubissueSuggestSourceIssue,
	sourceContent string,
	siblings []SubissueCandidateParent,
	candidateParents []SubissueCandidateParent,
	humanConstraints string,
	approvedOutline string,
) map[string]string {
	return map[string]string{
		"comment_text":      truncateSubissueSuggestContent(strings.TrimSpace(sourceContent)),
		"issue_identifier":  sourceIssue.Identifier,
		"issue_title":       sourceIssue.Title,
		"issue_description": truncateSubissueSuggestContent(strings.TrimSpace(sourceIssue.Description)),
		"ancestor_brief":    sourceIssue.AncestorBrief,
		"business_context":  businessContextForPrompt(inferSubissueBusiness(sourceIssue, sourceContent)),
		"siblings":          formatSubissueSuggestCandidates(siblings),
		"candidate_parents": formatSubissueSuggestCandidates(candidateParents),
		"human_constraints": truncateSubissuePlanConstraint(humanConstraints),
		"approved_outline":  approvedOutline,
		// Overwritten by the retry loops; defaulted so a template that uses
		// them always renders.
		"identified_tasks": "（无）",
		"retry_feedback":   "（无）",
	}
}

func truncateSubissuePlanConstraint(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "（无）"
	}
	runes := []rune(value)
	if len(runes) <= SubissuePlanConstraintMax {
		return value
	}
	return string(runes[:SubissuePlanConstraintMax]) + "…"
}

func parseSubissuePlansResponse(raw string) ([]SubissuePlan, error) {
	plans, _, err := parseSubissuePlansResult(raw, "")
	return plans, err
}

func parseSubissuePlansResponseWithBusiness(raw, fallbackBusiness string) ([]SubissuePlan, error) {
	plans, _, err := parseSubissuePlansResult(raw, fallbackBusiness)
	return plans, err
}

// parseSubissuePlansResult structurally parses the outline response: the
// identified task list, the plans with their business-normalized items, and
// each item's source task references. Coverage semantics are checked
// separately by validateSubissuePlanCoverage so malformed-but-parseable
// output can be retried with precise feedback.
func parseSubissuePlansResult(raw, fallbackBusiness string) ([]SubissuePlan, []SubissueIdentifiedTask, error) {
	var parsed subissuePlanLLMResponse
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return nil, nil, fmt.Errorf("parse subissue plans: %w", err)
	}
	identified := normalizeSubissueIdentifiedTasks(parsed.IdentifiedTasks)
	plans := make([]SubissuePlan, 0, len(parsed.Plans))
	seen := make(map[string]struct{})
	for _, candidate := range parsed.Plans {
		if len(plans) >= SubissuePlanMaxCount {
			return nil, identified, fmt.Errorf("parse subissue plans: at most %d plans are allowed", SubissuePlanMaxCount)
		}
		items := make([]SubissuePlanItem, 0, len(candidate.Items))
		for _, item := range candidate.Items {
			title := strings.TrimSpace(item.Title)
			goal := strings.TrimSpace(item.Goal)
			if title == "" && goal == "" {
				continue
			}
			if title == "" || goal == "" {
				return nil, identified, errors.New("parse subissue plans: every item needs title and goal")
			}
			if len([]rune(goal)) > SubissuePlanGoalMaxLength {
				return nil, identified, errors.New("parse subissue plans: goal is too long")
			}
			business := resolveSubissuePlanBusiness(item.Business, fallbackBusiness)
			title, err := formatSubissuePlanTitle(title, business)
			if err != nil {
				return nil, identified, err
			}
			items = append(items, SubissuePlanItem{
				Title:         title,
				Goal:          goal,
				Kind:          SubissuePlanItemImplementation,
				Business:      business,
				SourceTaskIDs: normalizeSubissueSourceTaskIDs(item.SourceTaskIDs),
			})
		}
		if len(items) == 0 {
			continue
		}
		if len(items) > SubissuePlanItemMaxCount {
			return nil, identified, fmt.Errorf("parse subissue plans: at most %d items are allowed per plan", SubissuePlanItemMaxCount)
		}
		name := strings.TrimSpace(candidate.Name)
		if name == "" {
			name = fmt.Sprintf("拆分方案 %d", len(plans)+1)
		}
		plan := SubissuePlan{ID: fmt.Sprintf("plan-%d", len(plans)+1), Name: name, Items: items}
		plan.Items = appendSubissueSummaryTests(plan.Items)
		if len(plan.Items) > SubissuePlanItemMaxCount {
			return nil, identified, fmt.Errorf("parse subissue plans: at most %d items are allowed per plan including summary tests", SubissuePlanItemMaxCount)
		}
		signatureBytes, _ := json.Marshal(items)
		signature := string(signatureBytes)
		if _, duplicate := seen[signature]; duplicate {
			continue
		}
		seen[signature] = struct{}{}
		for itemIndex := range plan.Items {
			plan.Items[itemIndex].ID = fmt.Sprintf("%s-item-%d", plan.ID, itemIndex+1)
		}
		plans = append(plans, plan)
	}
	if len(plans) == 0 {
		return nil, identified, errors.New("parse subissue plans: no actionable items returned")
	}
	// The first plan is the coverage-checked one by contract; mark it so the
	// frontend can default to it.
	plans[0].Name = SubissuePlanFullName
	plans[0].Coverage = SubissuePlanCoverageFull
	return plans, identified, nil
}

func normalizeSubissueIdentifiedTasks(raw []subissueIdentifiedTaskRaw) []SubissueIdentifiedTask {
	tasks := make([]SubissueIdentifiedTask, 0, len(raw))
	seen := make(map[string]struct{}, len(raw))
	for _, candidate := range raw {
		text := strings.TrimSpace(candidate.Text)
		if text == "" {
			continue
		}
		id := strings.ToLower(strings.TrimSpace(candidate.ID))
		if id == "" {
			id = fmt.Sprintf("t%d", len(tasks)+1)
		}
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		seen[id] = struct{}{}
		tasks = append(tasks, SubissueIdentifiedTask{ID: id, Text: text})
	}
	return tasks
}

func normalizeSubissueSourceTaskIDs(raw []string) []string {
	if len(raw) == 0 {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, id := range raw {
		id = strings.ToLower(strings.TrimSpace(id))
		if id == "" {
			continue
		}
		out = append(out, id)
	}
	return out
}

// validateSubissuePlanCoverage is the SIY-147 gate: the full-coverage plan
// (first) must reference every identified task, and no item may reference an
// id the outline never identified. Summary-test rows are server-generated and
// exempt from the reference requirement.
func validateSubissuePlanCoverage(identified []SubissueIdentifiedTask, plans []SubissuePlan) error {
	if len(identified) == 0 {
		return errors.New("coverage check failed: outline identified no source tasks")
	}
	if len(plans) == 0 {
		return errors.New("coverage check failed: no plans returned")
	}
	known := make(map[string]struct{}, len(identified))
	for _, task := range identified {
		known[task.ID] = struct{}{}
	}
	for _, plan := range plans {
		for _, item := range plan.Items {
			for _, ref := range item.SourceTaskIDs {
				if _, ok := known[ref]; !ok {
					return fmt.Errorf("coverage check failed: item %q references unknown source task %q", item.Title, ref)
				}
			}
		}
	}
	full := plans[0]
	covered := make(map[string]struct{}, len(identified))
	for _, item := range full.Items {
		if item.Kind == SubissuePlanItemSummaryTest {
			continue
		}
		if len(item.SourceTaskIDs) == 0 {
			return fmt.Errorf("coverage check failed: full plan item %q covers no identified task", item.Title)
		}
		for _, ref := range item.SourceTaskIDs {
			covered[ref] = struct{}{}
		}
	}
	var missing []string
	for _, task := range identified {
		if _, ok := covered[task.ID]; !ok {
			missing = append(missing, fmt.Sprintf("%s(%s)", task.ID, task.Text))
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("coverage check failed: full plan misses %d identified task(s): %s", len(missing), strings.Join(missing, "、"))
	}
	return nil
}

func validateApprovedSubissuePlan(plan SubissuePlan) error {
	if len(plan.Items) == 0 || len(plan.Items) > SubissuePlanItemMaxCount {
		return fmt.Errorf("approved subissue plan must contain 1-%d items", SubissuePlanItemMaxCount)
	}
	seen := make(map[string]struct{}, len(plan.Items))
	for _, item := range plan.Items {
		if strings.TrimSpace(item.ID) == "" {
			return errors.New("approved subissue plan item id is required")
		}
		if _, exists := seen[item.ID]; exists {
			return fmt.Errorf("approved subissue plan has duplicate item id %q", item.ID)
		}
		seen[item.ID] = struct{}{}
		switch item.Kind {
		case "", SubissuePlanItemImplementation, SubissuePlanItemSummaryTest:
		default:
			return fmt.Errorf("approved subissue plan has unknown item kind %q", item.Kind)
		}
		if strings.TrimSpace(item.Title) == "" || strings.TrimSpace(item.Goal) == "" {
			return errors.New("approved subissue plan items require title and goal")
		}
		if len([]rune(item.Title)) > SubissuePlanTitleMaxLength || len([]rune(item.Goal)) > SubissuePlanGoalMaxLength {
			return errors.New("approved subissue plan title or goal is too long")
		}
	}
	return nil
}

func normalizeSubissuePlanTitles(items []SubissuePlanItem) []SubissuePlanItem {
	return normalizeSubissuePlanTitlesWithBusiness(items, "")
}

func normalizeSubissuePlanTitlesWithBusiness(items []SubissuePlanItem, fallbackBusiness string) []SubissuePlanItem {
	normalized := make([]SubissuePlanItem, len(items))
	copy(normalized, items)
	for index, item := range normalized {
		business := resolveSubissuePlanBusiness(item.Business, fallbackBusiness)
		normalized[index].Business = business
		title, err := formatSubissuePlanTitle(strings.TrimSpace(item.Title), business)
		if err != nil {
			continue
		}
		normalized[index].Title = title
	}
	return normalized
}

func formatSubissuePlanTitle(title, business string) (string, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return "", errors.New("subissue plan title is required")
	}
	business = cleanSubissueBusinessCandidate(business)
	if business == "" {
		business = SubissuePlanOverallBusiness
	}
	for strings.HasPrefix(title, "【") {
		end := strings.Index(title, "】")
		if end < 0 {
			break
		}
		title = strings.TrimSpace(title[end+len("】"):])
	}
	if title == "" {
		return "", errors.New("subissue plan title has no text outside the business prefix")
	}
	formatted := "【" + business + "】" + title
	if len([]rune(formatted)) > SubissuePlanTitleMaxLength {
		return "", fmt.Errorf("subissue plan title exceeds %d runes including the business prefix", SubissuePlanTitleMaxLength)
	}
	return formatted, nil
}

func resolveSubissuePlanBusiness(modelBusiness, fallbackBusiness string) string {
	business := cleanSubissueBusinessCandidate(modelBusiness)
	fallback := cleanSubissueBusinessCandidate(fallbackBusiness)
	if (business == "" || isGenericSubissueBusiness(business)) && fallback != "" {
		return fallback
	}
	if business == "" {
		return SubissuePlanOverallBusiness
	}
	return business
}

func businessContextForPrompt(business string) string {
	if business == "" {
		return "（未识别；请从原始评论和上下文提取具体业务概念，确实无法识别时才使用“整体流程”）"
	}
	return business
}

func inferSubissueBusiness(sourceIssue SubissueSuggestSourceIssue, sourceContent string) string {
	// The triggering comment is the closest business signal: it often contains
	// an explicit label such as “订单支付” or 【日报/周报/年报生成】. The issue
	// title and description then provide stable fallbacks for short comments.
	texts := []string{sourceContent, sourceIssue.Title, sourceIssue.Description, sourceIssue.AncestorBrief}
	for _, text := range texts {
		if business := extractDelimitedSubissueBusiness(text); business != "" {
			return business
		}
	}
	for _, text := range texts {
		if business := extractVerbSubissueBusiness(text); business != "" {
			return business
		}
	}
	if business := cleanSubissueBusinessCandidate(sourceIssue.Title); len([]rune(business)) <= 40 && business != "" {
		return business
	}
	return ""
}

func extractDelimitedSubissueBusiness(text string) string {
	for _, pair := range []struct{ open, close string }{
		{open: "【", close: "】"},
		{open: "「", close: "」"},
		{open: "『", close: "』"},
		{open: "“", close: "”"},
		{open: "\"", close: "\""},
	} {
		remaining := text
		for {
			start := strings.Index(remaining, pair.open)
			if start < 0 {
				break
			}
			valueStart := start + len(pair.open)
			end := strings.Index(remaining[valueStart:], pair.close)
			if end < 0 {
				break
			}
			if business := cleanSubissueBusinessCandidate(remaining[valueStart : valueStart+end]); business != "" {
				return business
			}
			remaining = remaining[valueStart+end+len(pair.close):]
		}
	}
	return ""
}

func extractVerbSubissueBusiness(text string) string {
	for _, verb := range []string{"实现", "开发", "新增", "支持", "优化", "改造", "构建", "设计"} {
		remaining := text
		for {
			start := strings.Index(remaining, verb)
			if start < 0 {
				break
			}
			candidate := remaining[start+len(verb):]
			end := len(candidate)
			for _, delimiter := range []string{"功能", "模块", "能力", "：", ":", "，", ",", "。", "；", ";", "\n"} {
				if index := strings.Index(candidate, delimiter); index >= 0 && index < end {
					end = index
				}
			}
			if business := cleanSubissueBusinessCandidate(candidate[:end]); business != "" {
				return business
			}
			remaining = remaining[start+len(verb):]
		}
	}
	return ""
}

func cleanSubissueBusinessCandidate(value string) string {
	value = strings.TrimSpace(value)
	for strings.HasPrefix(value, "【") && strings.Contains(value, "】") {
		value = strings.TrimSpace(value[strings.Index(value, "】")+len("】"):])
	}
	value = strings.Trim(value, " \t\r\n\"“”‘’「」『』【】[]()（）:：,，。；;|-")
	for _, prefix := range []string{"一键式", "一键", "实现", "开发", "新增", "支持", "优化", "改造", "构建", "设计"} {
		value = strings.TrimSpace(strings.TrimPrefix(value, prefix))
	}
	for _, suffix := range []string{"产品能力", "功能", "业务", "模块", "能力"} {
		value = strings.TrimSpace(strings.TrimSuffix(value, suffix))
	}
	value = strings.Trim(value, " \t\r\n\"“”‘’「」『』【】[]()（）:：,，。；;|-")
	if value == "" || len([]rune(value)) > SubissuePlanBusinessMaxLen || isGenericSubissueBusiness(value) {
		return ""
	}
	return value
}

func isGenericSubissueBusiness(value string) bool {
	switch strings.TrimSpace(value) {
	case "整体流程", "整个流程", "任务", "功能", "业务", "需求", "项目", "工作", "通用功能":
		return true
	default:
		return false
	}
}

func appendSubissueSummaryTests(items []SubissuePlanItem) []SubissuePlanItem {
	businessOrder := make([]string, 0)
	businessCounts := make(map[string]int)
	lastIndexes := make(map[string]int)
	for index, item := range items {
		business := item.Business
		if business == "" {
			business = SubissuePlanOverallBusiness
		}
		if businessCounts[business] == 0 {
			businessOrder = append(businessOrder, business)
		}
		businessCounts[business]++
		lastIndexes[business] = index
	}

	hasGroupedSummary := false
	for _, business := range businessOrder {
		if businessCounts[business] < SubissuePlanSummaryThreshold {
			continue
		}
		hasGroupedSummary = true
		summary := SubissuePlanItem{
			Title: "【" + business + "】" + business + "汇总测试",
			Goal: fmt.Sprintf(
				"在该业务的 %d 个开发条目全部完成后，执行端到端联调和整体验收，确认完整流程可用。",
				businessCounts[business],
			),
			Kind:     SubissuePlanItemSummaryTest,
			Business: business,
		}
		insertAt := lastIndexes[business] + 1
		items = append(items, SubissuePlanItem{})
		copy(items[insertAt+1:], items[insertAt:])
		items[insertAt] = summary
		for laterBusiness, lastIndex := range lastIndexes {
			if lastIndex >= insertAt {
				lastIndexes[laterBusiness] = lastIndex + 1
			}
		}
		lastIndexes[business] = insertAt
	}

	if !hasGroupedSummary && len(items) >= SubissuePlanSummaryThreshold {
		items = append(items, SubissuePlanItem{
			Title:    "【" + SubissuePlanOverallBusiness + "】" + SubissuePlanOverallBusiness + "汇总测试",
			Goal:     "在所有开发条目全部完成后，执行端到端联调和整体验收，确认完整流程可用。",
			Kind:     SubissuePlanItemSummaryTest,
			Business: SubissuePlanOverallBusiness,
		})
	}
	return items
}

func parseSubissueDetailsResponse(raw string, plan SubissuePlan) ([]SubissueSuggestion, error) {
	var parsed subissueDetailLLMResponse
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return nil, fmt.Errorf("parse subissue details: %w", err)
	}
	if len(parsed.Subissues) != len(plan.Items) {
		return nil, fmt.Errorf("parse subissue details: returned %d items, expected %d", len(parsed.Subissues), len(plan.Items))
	}
	expected := make(map[string]SubissuePlanItem, len(plan.Items))
	for _, item := range plan.Items {
		expected[item.ID] = item
	}
	result := make(map[string]SubissueSuggestion, len(plan.Items))
	for _, detail := range parsed.Subissues {
		id := strings.TrimSpace(detail.ID)
		item, ok := expected[id]
		if !ok {
			return nil, fmt.Errorf("parse subissue details: unknown item id %q", id)
		}
		if _, duplicate := result[id]; duplicate {
			return nil, fmt.Errorf("parse subissue details: duplicate item id %q", id)
		}
		description := strings.TrimSpace(detail.Description)
		if description == "" {
			return nil, fmt.Errorf("parse subissue details: item %q has empty description", id)
		}
		if detail.Stage < 1 {
			detail.Stage = 1
		}
		if detail.Confidence < 0 || detail.Confidence > 1 {
			detail.Confidence = 0
		}
		depends := make([]string, 0, len(detail.DependsOnIDs))
		for _, dependency := range detail.DependsOnIDs {
			dependency = strings.TrimSpace(dependency)
			if dependency == "" {
				continue
			}
			if dependency == id {
				return nil, fmt.Errorf("parse subissue details: item %q depends on itself", id)
			}
			if _, exists := expected[dependency]; !exists {
				return nil, fmt.Errorf("parse subissue details: item %q depends on unknown item %q", id, dependency)
			}
			depends = append(depends, dependency)
		}
		var parent *string
		if detail.SuggestedParentIdentifier != nil {
			value := strings.TrimSpace(*detail.SuggestedParentIdentifier)
			if value != "" {
				parent = &value
			}
		}
		result[id] = SubissueSuggestion{
			ID:                        id,
			Title:                     item.Title,
			Goal:                      item.Goal,
			Description:               description,
			Stage:                     detail.Stage,
			DependsOnIDs:              depends,
			SuggestedParentIdentifier: parent,
			Confidence:                detail.Confidence,
		}
	}
	ordered := make([]SubissueSuggestion, 0, len(plan.Items))
	for _, item := range plan.Items {
		detail, ok := result[item.ID]
		if !ok {
			return nil, fmt.Errorf("parse subissue details: missing item id %q", item.ID)
		}
		ordered = append(ordered, detail)
	}
	return applySubissueSummaryTestGates(ordered, plan), nil
}

func applySubissueSummaryTestGates(details []SubissueSuggestion, plan SubissuePlan) []SubissueSuggestion {
	implementationByBusiness := make(map[string][]string)
	var overallImplementation []string
	maxStageByBusiness := make(map[string]int)
	overallMaxStage := 0
	for index, item := range plan.Items {
		if item.Kind == SubissuePlanItemSummaryTest {
			continue
		}
		business := item.Business
		if business == "" {
			business = SubissuePlanOverallBusiness
		}
		implementationByBusiness[business] = append(implementationByBusiness[business], item.ID)
		overallImplementation = append(overallImplementation, item.ID)
		stage := details[index].Stage
		if stage > maxStageByBusiness[business] {
			maxStageByBusiness[business] = stage
		}
		if stage > overallMaxStage {
			overallMaxStage = stage
		}
	}

	for index, item := range plan.Items {
		if item.Kind != SubissuePlanItemSummaryTest {
			continue
		}
		dependencies := overallImplementation
		nextStage := overallMaxStage + 1
		if business := item.Business; business != "" && len(implementationByBusiness[business]) > 0 {
			dependencies = implementationByBusiness[business]
			nextStage = maxStageByBusiness[business] + 1
		}
		dependenciesCopy := make([]string, len(dependencies))
		copy(dependenciesCopy, dependencies)
		details[index].Stage = nextStage
		details[index].DependsOnIDs = dependenciesCopy
	}
	return details
}
