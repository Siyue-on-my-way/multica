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

// SubissueSuggestSourceIssue is the minimal shape the prompt needs for the
// issue whose comment is being decomposed, its existing children (to avoid
// suggesting a duplicate), and the candidate parent list.
type SubissueSuggestSourceIssue struct {
	Identifier string
	Title      string
}

type SubissueCandidateParent struct {
	Identifier string
	Title      string
}

// SubissueSuggestion is one parsed (but not yet validated against the
// candidate parent list) entry from the model's output.
type SubissueSuggestion struct {
	Title                     string   `json:"title"`
	Description               string   `json:"description"`
	Stage                     int      `json:"stage"`
	DependsOnTitles           []string `json:"depends_on_titles"`
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
	return parseSubissueSuggestResponse(raw)
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
	var b strings.Builder

	b.WriteString("要拆解的评论原文：\n")
	b.WriteString(truncateSubissueSuggestContent(strings.TrimSpace(sourceContent)))
	b.WriteString("\n\n")

	fmt.Fprintf(&b, "当前 issue：%s %s\n\n", sourceIssue.Identifier, sourceIssue.Title)

	b.WriteString("当前 issue 下已有的兄弟子issue（避免拆出重复任务）：\n")
	if len(siblings) == 0 {
		b.WriteString("(无)\n")
	} else {
		for _, s := range siblings {
			fmt.Fprintf(&b, "- %s %s\n", s.Identifier, s.Title)
		}
	}
	b.WriteString("\n")

	b.WriteString("候选父issue列表（挑选时必须原样使用下面的 identifier）：\n")
	if len(candidateParents) == 0 {
		b.WriteString("(无)\n")
	} else {
		for _, p := range candidateParents {
			fmt.Fprintf(&b, "- %s %s\n", p.Identifier, p.Title)
		}
	}
	b.WriteString("\n请输出拆解结果。")
	return b.String()
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
