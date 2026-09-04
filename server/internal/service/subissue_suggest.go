package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrLLMNotConfigured is returned by SuggestSubissues when the deployment has
// no LLM credentials configured (see llm.Client.Enabled). The handler turns
// this into a 503 so the frontend can hide/disable the entry point instead of
// showing a generic failure.
var ErrLLMNotConfigured = errors.New("subissue suggest: llm not configured")

// Budgets for one subissue-suggestion pass. Unlike the chat quick-actions
// pass (a nicety attached to a reply the user already has), this one is the
// user's explicit action — clicking "生成子issue" — so it can afford a
// slower, larger-output call: the client shows a loading state for the whole
// preview panel until this resolves.
const (
	SubissueSuggestTimeout             = 3 * time.Minute
	subissueSuggestTemperature         = 0.3
	subissueSuggestMaxCompletionTokens = 4096
)

// subissueSuggestCommentBudget caps how much of the source comment is fed to
// the model. A reply this long has already made its point several times
// over; the tail is kept because that is typically where a plan lands on its
// concrete next steps.
const subissueSuggestCommentBudget = 6000

// subissueSuggestCandidateParentMax bounds how many candidate parent issues
// are listed in the prompt. A large project would otherwise blow the token
// budget on a list the model mostly ignores in favor of the obvious pick
// (the issue whose comment is being decomposed).
const subissueSuggestCandidateParentMax = 30

// SubissueSuggestLLM is the seam the service uses to run the suggestion pass,
// satisfied by *llm.Client. Mirrors ChatQuickActionsLLM.
type SubissueSuggestLLM interface {
	Enabled() bool
	GenerateJSON(ctx context.Context, model, systemPrompt, userPrompt string, temperature float64, maxCompletionTokens int64) (string, error)
}

// SubissueSuggestConfiguredLLM is the prompt-template seam used when the
// per-business registry is enabled. It deliberately keeps the legacy seam
// above so callers and tests that inject the global client remain compatible.
type SubissueSuggestConfiguredLLM interface {
	Enabled() bool
	GenerateJSONTemplate(ctx context.Context, variables map[string]string, fallbackSystem, fallbackUserTemplate string, fallbackTemperature float64, fallbackMaxCompletionTokens int64) (string, error)
}

// LegacySubissueSuggestConfiguredLLM adapts the pre-registry global client to
// the staged prompt seam. It is used only during migration when no business
// YAML directory is enabled, and renders the supplied fallback template
// without allowing request data to select a file or a model.
type LegacySubissueSuggestConfiguredLLM struct {
	Client SubissueSuggestLLM
}

func (c LegacySubissueSuggestConfiguredLLM) Enabled() bool {
	return c.Client != nil && c.Client.Enabled()
}

func (c LegacySubissueSuggestConfiguredLLM) GenerateJSONTemplate(
	ctx context.Context,
	variables map[string]string,
	fallbackSystem string,
	fallbackUserTemplate string,
	temperature float64,
	maxCompletionTokens int64,
) (string, error) {
	if !c.Enabled() {
		return "", ErrLLMNotConfigured
	}
	render := func(template string) string {
		for name, value := range variables {
			template = strings.ReplaceAll(template, "{{"+name+"}}", value)
		}
		return template
	}
	return c.Client.GenerateJSON(ctx, "", render(fallbackSystem), render(fallbackUserTemplate), temperature, maxCompletionTokens)
}

// SubissueSuggestSourceIssue is the minimal shape the prompt needs for the
// issue whose comment is being decomposed, its existing children (to avoid
// suggesting a duplicate), and the candidate parent list.
type SubissueSuggestSourceIssue struct {
	Identifier    string
	Title         string
	Description   string
	AncestorBrief string
}

type SubissueCandidateParent struct {
	Identifier string
	Title      string
}

// SubissueSuggestion is one parsed (but not yet validated against the
// candidate parent list) entry from the model's output.
type SubissueSuggestion struct {
	ID                        string   `json:"id,omitempty"`
	Title                     string   `json:"title"`
	Goal                      string   `json:"goal,omitempty"`
	Description               string   `json:"description"`
	Stage                     int      `json:"stage"`
	DependsOnTitles           []string `json:"depends_on_titles"`
	DependsOnIDs              []string `json:"depends_on_ids,omitempty"`
	SuggestedParentIdentifier *string  `json:"suggested_parent_identifier"`
	Confidence                float64  `json:"confidence"`
}

type subissueSuggestLLMResponse struct {
	Subissues []SubissueSuggestion `json:"subissues"`
}

// subissueSuggestSystemPrompt is the stable instruction set for the pass —
// the exact rules the requirement (SIY-63) settled on, so upstream prompt
// caching applies and every call reasons about the task identically.
const subissueSuggestSystemPrompt = `你是一个任务拆解助手，帮用户把一段较长的讨论/方案内容拆分成可以独立执行的 Multica 子issue。要求：
1. 识别里面包含的一个或多个可执行任务；没有依赖关系、能同时开工的任务归入同一个 stage，有先后依赖的任务放到更晚的 stage。stage 从 1 开始。
2. 每个子issue的 description 必须让接手的人或agent不需要回看原始对话就能独立开工——所以必须写清楚：背景/为什么要做这件事、讨论中已经拍板的技术方案或约束（不要让新agent重新纠结这些已经决定的事）、具体要做的事情和范围、验收标准、依赖哪些其他子issue。
3. 给出建议挂载的父issue（从候选列表里选，用候选列表给出的 identifier 原样填写；选不出来就填 null，不要编造一个不在候选列表里的 identifier）。
4. 严格输出JSON，不要输出多余文字，不要使用markdown代码块包裹。

输出结构：
{"subissues":[{"title":"...","description":"...(按上面4点组织：背景/已拍板约束/范围/验收标准/依赖)","stage":1,"depends_on_titles":[],"suggested_parent_identifier":"SIY-30","confidence":0.86}]}`

// subissueSuggestUserTemplate keeps the variable contract explicit for the
// independently configurable business file. The rendered text intentionally
// matches buildSubissueSuggestPrompt so switching sources does not change the
// model-visible legacy behavior.
const subissueSuggestUserTemplate = `要拆解的评论原文：
{{comment_text}}

当前 issue：{{issue_identifier}} {{issue_title}}
当前 issue description：
{{issue_description}}

{{ancestor_brief}}

当前 issue 下已有的兄弟子issue（避免拆出重复任务）：
{{siblings}}

候选父issue列表（挑选时必须原样使用下面的 identifier）：
{{candidate_parents}}

请输出拆解结果。`

// SuggestSubissues runs one decomposition pass over sourceContent (the
// triggering comment's body) and returns the parsed, unvalidated
// suggestions. Resolving suggested_parent_identifier against the real
// candidate list and turning titles/depends_on_titles into stage-ordered
// creation batches is the caller's job (the handler), since it also needs
// workspace-scoped identifier lookups the service has no access to.
func SuggestSubissues(
	ctx context.Context,
	llmClient SubissueSuggestLLM,
	sourceIssue SubissueSuggestSourceIssue,
	sourceContent string,
	siblings []SubissueCandidateParent,
	candidateParents []SubissueCandidateParent,
) ([]SubissueSuggestion, error) {
	if llmClient == nil || !llmClient.Enabled() {
		return nil, ErrLLMNotConfigured
	}
	prompt := buildSubissueSuggestPrompt(sourceIssue, sourceContent, siblings, candidateParents)
	raw, err := llmClient.GenerateJSON(ctx,
		"", // deployment default: MULTICA_LLM_DEFAULT_MODEL, else llm.FallbackModel
		subissueSuggestSystemPrompt,
		prompt,
		subissueSuggestTemperature,
		subissueSuggestMaxCompletionTokens,
	)
	if err != nil {
		return nil, fmt.Errorf("generate subissue suggestions: %w", err)
	}
	suggestions, err := parseSubissueSuggestResponse(raw)
	if err != nil {
		return nil, err
	}
	return includeAncestorBriefInSubissueDescriptions(suggestions, sourceIssue.AncestorBrief), nil
}

// SuggestSubissuesWithConfig is the per-business counterpart of
// SuggestSubissues. It shares parsing and fallback prompts while allowing the
// registry to render an independently hot-reloaded prompt and model config.
func SuggestSubissuesWithConfig(
	ctx context.Context,
	llmClient SubissueSuggestConfiguredLLM,
	sourceIssue SubissueSuggestSourceIssue,
	sourceContent string,
	siblings []SubissueCandidateParent,
	candidateParents []SubissueCandidateParent,
) ([]SubissueSuggestion, error) {
	if llmClient == nil || !llmClient.Enabled() {
		return nil, ErrLLMNotConfigured
	}
	variables := buildSubissueSuggestVariables(sourceIssue, sourceContent, siblings, candidateParents)
	raw, err := llmClient.GenerateJSONTemplate(
		ctx,
		variables,
		subissueSuggestSystemPrompt,
		subissueSuggestUserTemplate,
		subissueSuggestTemperature,
		subissueSuggestMaxCompletionTokens,
	)
	if err != nil {
		return nil, fmt.Errorf("generate subissue suggestions: %w", err)
	}
	suggestions, err := parseSubissueSuggestResponse(raw)
	if err != nil {
		return nil, err
	}
	return includeAncestorBriefInSubissueDescriptions(suggestions, sourceIssue.AncestorBrief), nil
}

func parseSubissueSuggestResponse(raw string) ([]SubissueSuggestion, error) {
	var parsed subissueSuggestLLMResponse
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return nil, fmt.Errorf("parse subissue suggestions: %w", err)
	}
	out := make([]SubissueSuggestion, 0, len(parsed.Subissues))
	for _, s := range parsed.Subissues {
		s.Title = strings.TrimSpace(s.Title)
		s.Description = strings.TrimSpace(s.Description)
		if s.Title == "" {
			continue
		}
		if s.Stage < 1 {
			s.Stage = 1
		}
		if s.Confidence < 0 || s.Confidence > 1 {
			s.Confidence = 0
		}
		if s.SuggestedParentIdentifier != nil && strings.TrimSpace(*s.SuggestedParentIdentifier) == "" {
			s.SuggestedParentIdentifier = nil
		}
		out = append(out, s)
	}
	return out, nil
}

func buildSubissueSuggestPrompt(
	sourceIssue SubissueSuggestSourceIssue,
	sourceContent string,
	siblings []SubissueCandidateParent,
	candidateParents []SubissueCandidateParent,
) string {
	variables := buildSubissueSuggestVariables(sourceIssue, sourceContent, siblings, candidateParents)
	prompt := subissueSuggestUserTemplate
	for name, value := range variables {
		prompt = strings.ReplaceAll(prompt, "{{"+name+"}}", value)
	}
	return prompt
}

func buildSubissueSuggestVariables(
	sourceIssue SubissueSuggestSourceIssue,
	sourceContent string,
	siblings []SubissueCandidateParent,
	candidateParents []SubissueCandidateParent,
) map[string]string {
	return map[string]string{
		"comment_text":      truncateSubissueSuggestContent(strings.TrimSpace(sourceContent)),
		"issue_identifier":  sourceIssue.Identifier,
		"issue_title":       sourceIssue.Title,
		"issue_description": truncateSubissueSuggestContent(strings.TrimSpace(sourceIssue.Description)),
		"ancestor_brief":    sourceIssue.AncestorBrief,
		"siblings":          formatSubissueSuggestCandidates(siblings),
		"candidate_parents": formatSubissueSuggestCandidates(candidateParents),
	}
}

func formatSubissueSuggestCandidates(candidates []SubissueCandidateParent) string {
	var b strings.Builder
	if len(candidates) == 0 {
		b.WriteString("(无)")
	} else {
		for _, candidate := range candidates {
			fmt.Fprintf(&b, "- %s %s\n", candidate.Identifier, candidate.Title)
		}
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// truncateSubissueSuggestContent shortens the comment while keeping both
// ends. The head sets up the discussion; the tail typically lands on the
// concrete plan or next steps a decomposition is built from.
func truncateSubissueSuggestContent(content string) string {
	runes := []rune(content)
	if len(runes) <= subissueSuggestCommentBudget {
		return content
	}
	headBudget := subissueSuggestCommentBudget * 2 / 3
	tailBudget := subissueSuggestCommentBudget - headBudget
	head := string(runes[:headBudget])
	tail := string(runes[len(runes)-tailBudget:])
	return head + "\n…[truncated]…\n" + tail
}

// SubissueSuggestCandidateParentMax exposes the cap so the handler can apply
// the same limit when it selects which project issues to list.
func SubissueSuggestCandidateParentMax() int { return subissueSuggestCandidateParentMax }
