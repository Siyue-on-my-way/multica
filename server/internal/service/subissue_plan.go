package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
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
)

// SubissuePlanItem is the intentionally small outline shown before the
// detail-generation call. IDs are draft-only and never become issue IDs.
type SubissuePlanItem struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Goal     string `json:"goal"`
	Kind     string `json:"kind,omitempty"`
	Business string `json:"business,omitempty"`
}

// SubissuePlan is one alternative decomposition strategy. It contains no
// description, parent, stage, or dependency details until the user approves
// it and asks for expansion.
type SubissuePlan struct {
	ID    string             `json:"id"`
	Name  string             `json:"name"`
	Items []SubissuePlanItem `json:"items"`
}

type subissuePlanLLMResponse struct {
	Plans []struct {
		Name  string `json:"name"`
		Items []struct {
			Title    string `json:"title"`
			Goal     string `json:"goal"`
			Business string `json:"business"`
		} `json:"items"`
	} `json:"plans"`
}

const subissuePlanSystemPrompt = `你是一个任务拆分方案设计助手。请根据一段 Multica issue 讨论，生成三种不同粒度的候选拆分方案：
1. 上下文优先：尽量合并强相关内容，允许最终只有一个子issue，保证接手者拥有连贯上下文。
2. 平衡拆分：只拆出边界清晰且有独立价值的任务。
3. 并行优先：把确实可以独立并行处理的任务分开，但不要为了增加数量而拆散强相关工作。

每个方案只返回子issue的 title、goal 和 business。不要生成 description、stage、依赖、父issue或其他字段。business 必须是从原始评论、当前 issue 标题/description 和上级上下文中识别出的具体业务概念或产品能力，例如“生成子issue”“日报/周报/年报生成”“订单支付”，不能写成“任务”“功能”这类空泛词。强相关条目必须使用同一个 business；只要上下文能识别出业务，就必须填写该业务，不能留空，也不能使用“整体流程”。只有确实没有任何可识别业务概念时，才允许使用“整体流程”。business 不要包含【】；title 只写任务名称，服务端会统一添加【business】前缀。方案必须来自原始讨论，不能凭空添加工作。系统会在每个包含至少两个条目的业务后自动补充汇总测试，因此不要自己生成汇总测试。严格只输出 JSON，不要输出 Markdown 或解释文字。

输出结构：
{"plans":[{"name":"上下文优先","items":[{"title":"...","goal":"...","business":"..."}]}]}`

const subissuePlanUserTemplate = `原始评论内容：
{{comment_text}}

当前 issue：{{issue_identifier}} {{issue_title}}
当前 issue description：
{{issue_description}}

{{ancestor_brief}}

当前业务概念（优先用于每个条目的 business 和标题前缀；只有原始讨论明确包含多个不同业务时才分别使用不同概念）：
{{business_context}}

已有兄弟子issue（避免重复）：
{{siblings}}

候选父issue（仅供理解上下文，不要在本阶段输出父issue）：
{{candidate_parents}}

用户额外拆分要求：
{{human_constraints}}

请输出 2～3 种拆分方案。`

// SuggestSubissuePlans performs the lightweight structure-only pass. The
// caller remains responsible for the workspace-scoped context lookup; this
// service only knows how to build and validate the model contract.
func SuggestSubissuePlans(
	ctx context.Context,
	llmClient SubissueSuggestConfiguredLLM,
	sourceIssue SubissueSuggestSourceIssue,
	sourceContent string,
	siblings []SubissueCandidateParent,
	candidateParents []SubissueCandidateParent,
	humanConstraints string,
) ([]SubissuePlan, error) {
	if llmClient == nil || !llmClient.Enabled() {
		return nil, ErrLLMNotConfigured
	}
	variables := buildSubissuePlanVariables(sourceIssue, sourceContent, siblings, candidateParents, humanConstraints, "")
	raw, err := llmClient.GenerateJSONTemplate(
		ctx,
		variables,
		subissuePlanSystemPrompt,
		subissuePlanUserTemplate,
		0.4,
		2048,
	)
	if err != nil {
		return nil, fmt.Errorf("generate subissue plans: %w", err)
	}
	return parseSubissuePlansResponseWithBusiness(raw, inferSubissueBusiness(sourceIssue, sourceContent))
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
// change the user's selected structure.
func ExpandSubissuePlan(
	ctx context.Context,
	llmClient SubissueSuggestConfiguredLLM,
	sourceIssue SubissueSuggestSourceIssue,
	sourceContent string,
	siblings []SubissueCandidateParent,
	candidateParents []SubissueCandidateParent,
	plan SubissuePlan,
	humanConstraints string,
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
	variables := buildSubissuePlanVariables(sourceIssue, sourceContent, siblings, candidateParents, humanConstraints, string(approved))
	raw, err := llmClient.GenerateJSONTemplate(
		ctx,
		variables,
		subissueDetailSystemPrompt,
		subissueDetailUserTemplate,
		0.3,
		4096,
	)
	if err != nil {
		return nil, fmt.Errorf("generate subissue details: %w", err)
	}
	details, err := parseSubissueDetailsResponse(raw, plan)
	if err != nil {
		return nil, err
	}
	return includeAncestorBriefInSubissueDescriptions(details, sourceIssue.AncestorBrief), nil
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
	return parseSubissuePlansResponseWithBusiness(raw, "")
}

func parseSubissuePlansResponseWithBusiness(raw, fallbackBusiness string) ([]SubissuePlan, error) {
	var parsed subissuePlanLLMResponse
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return nil, fmt.Errorf("parse subissue plans: %w", err)
	}
	if len(parsed.Plans) == 0 {
		return nil, errors.New("parse subissue plans: no plans returned")
	}
	plans := make([]SubissuePlan, 0, len(parsed.Plans))
	seen := make(map[string]struct{})
	for _, candidate := range parsed.Plans {
		if len(plans) >= SubissuePlanMaxCount {
			return nil, fmt.Errorf("parse subissue plans: at most %d plans are allowed", SubissuePlanMaxCount)
		}
		items := make([]SubissuePlanItem, 0, len(candidate.Items))
		for _, item := range candidate.Items {
			title := strings.TrimSpace(item.Title)
			goal := strings.TrimSpace(item.Goal)
			if title == "" && goal == "" {
				continue
			}
			if title == "" || goal == "" {
				return nil, errors.New("parse subissue plans: every item needs title and goal")
			}
			if len([]rune(goal)) > SubissuePlanGoalMaxLength {
				return nil, errors.New("parse subissue plans: goal is too long")
			}
			business := resolveSubissuePlanBusiness(item.Business, fallbackBusiness)
			title, err := formatSubissuePlanTitle(title, business)
			if err != nil {
				return nil, err
			}
			items = append(items, SubissuePlanItem{
				Title:    title,
				Goal:     goal,
				Kind:     SubissuePlanItemImplementation,
				Business: business,
			})
		}
		if len(items) == 0 {
			continue
		}
		if len(items) > SubissuePlanItemMaxCount {
			return nil, fmt.Errorf("parse subissue plans: at most %d items are allowed per plan", SubissuePlanItemMaxCount)
		}
		name := strings.TrimSpace(candidate.Name)
		if name == "" {
			name = fmt.Sprintf("拆分方案 %d", len(plans)+1)
		}
		plan := SubissuePlan{ID: fmt.Sprintf("plan-%d", len(plans)+1), Name: name, Items: items}
		plan.Items = appendSubissueSummaryTests(plan.Items)
		if len(plan.Items) > SubissuePlanItemMaxCount {
			return nil, fmt.Errorf("parse subissue plans: at most %d items are allowed per plan including summary tests", SubissuePlanItemMaxCount)
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
		return nil, errors.New("parse subissue plans: no actionable items returned")
	}
	return plans, nil
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
