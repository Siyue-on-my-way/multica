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
)

// SubissuePlanItem is the intentionally small outline shown before the
// detail-generation call. IDs are draft-only and never become issue IDs.
type SubissuePlanItem struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Goal  string `json:"goal"`
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
			Title string `json:"title"`
			Goal  string `json:"goal"`
		} `json:"items"`
	} `json:"plans"`
}

const subissuePlanSystemPrompt = `你是一个任务拆分方案设计助手。请根据一段 Multica issue 讨论，生成三种不同粒度的候选拆分方案：
1. 上下文优先：尽量合并强相关内容，允许最终只有一个子issue，保证接手者拥有连贯上下文。
2. 平衡拆分：只拆出边界清晰且有独立价值的任务。
3. 并行优先：把确实可以独立并行处理的任务分开，但不要为了增加数量而拆散强相关工作。

每个方案只返回子issue的 title 和 goal，不要生成 description、stage、依赖、父issue或其他字段。方案必须来自原始讨论，不能凭空添加工作。严格只输出 JSON，不要输出 Markdown 或解释文字。

输出结构：
{"plans":[{"name":"上下文优先","items":[{"title":"...","goal":"..."}]}]}`

const subissuePlanUserTemplate = `原始评论内容：
{{comment_text}}

当前 issue：{{issue_identifier}} {{issue_title}}

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
	return parseSubissuePlansResponse(raw)
}

const subissueDetailSystemPrompt = `你是一个 Multica 子issue详情生成助手。用户已经人工确认了拆分草稿，你只能为草稿中的每个条目补充可执行的详细 description、stage、依赖关系、建议父issue和 confidence。

必须严格保留 approved_outline 中每个条目的 id、title、goal、数量和顺序，不得增加、删除、合并、拆分或改写这些字段。description 必须让接手者不需要回看原始讨论就能开工，并包含背景、已经确定的约束、工作范围和验收标准。严格只输出 JSON，不要输出 Markdown 或解释文字。

输出结构：
{"subissues":[{"id":"plan-1-item-1","title":"...","goal":"...","description":"...","stage":1,"depends_on_ids":[],"suggested_parent_identifier":null,"confidence":0.86}]}`

const subissueDetailUserTemplate = `原始评论内容：
{{comment_text}}

当前 issue：{{issue_identifier}} {{issue_title}}

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
	return parseSubissueDetailsResponse(raw, plan)
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
			if len([]rune(title)) > SubissuePlanTitleMaxLength || len([]rune(goal)) > SubissuePlanGoalMaxLength {
				return nil, errors.New("parse subissue plans: title or goal is too long")
			}
			items = append(items, SubissuePlanItem{Title: title, Goal: goal})
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
		if strings.TrimSpace(item.Title) == "" || strings.TrimSpace(item.Goal) == "" {
			return errors.New("approved subissue plan items require title and goal")
		}
		if len([]rune(item.Title)) > SubissuePlanTitleMaxLength || len([]rune(item.Goal)) > SubissuePlanGoalMaxLength {
			return errors.New("approved subissue plan title or goal is too long")
		}
	}
	return nil
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
	return ordered, nil
}
