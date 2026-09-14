package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/pkg/llm"
	openai "github.com/openai/openai-go/v3"
)

const (
	SubissuePlanOnlyContextType = "plan_only"
	SubissuePlanOnlySourceAgent = "associated_agent"
	SubissuePlanSourcePrimary   = "primary_llm"
	SubissuePlanSourceRule      = "rule_draft"
	SubissuePlanSourceFallback  = "fallback_agent"
	SubissuePlanFallbackReason  = "primary_llm_unavailable"
	SubissuePlanAgentFailure    = "agent_unavailable"
	SubissuePlanNoCandidate     = "no_candidate_agent"
	SubissuePlanValidationError = "invalid_agent_result"
)

// SubissuePlanLLMErrorClass is deliberately a small public vocabulary. It is
// persisted in diagnostics and jobs, so it must not contain provider messages,
// URLs, API keys, or other unstable details.
type SubissuePlanLLMErrorClass string

const (
	SubissuePlanLLMErrorNotConfigured  SubissuePlanLLMErrorClass = "not_configured"
	SubissuePlanLLMErrorAuthentication SubissuePlanLLMErrorClass = "authentication"
	SubissuePlanLLMErrorQuota          SubissuePlanLLMErrorClass = "quota_or_rate_limit"
	SubissuePlanLLMErrorTimeout        SubissuePlanLLMErrorClass = "timeout"
	SubissuePlanLLMErrorNetwork        SubissuePlanLLMErrorClass = "network"
	SubissuePlanLLMErrorProvider5xx    SubissuePlanLLMErrorClass = "provider_5xx"
	SubissuePlanLLMErrorInvalidRequest SubissuePlanLLMErrorClass = "invalid_request"
	SubissuePlanLLMErrorPermission     SubissuePlanLLMErrorClass = "permission"
	SubissuePlanLLMErrorInvalidContext SubissuePlanLLMErrorClass = "invalid_context"
	SubissuePlanLLMErrorUnknown        SubissuePlanLLMErrorClass = "unknown"
)

func (c SubissuePlanLLMErrorClass) Degradable() bool {
	switch c {
	case SubissuePlanLLMErrorNotConfigured,
		SubissuePlanLLMErrorAuthentication,
		SubissuePlanLLMErrorQuota,
		SubissuePlanLLMErrorTimeout,
		SubissuePlanLLMErrorNetwork,
		SubissuePlanLLMErrorProvider5xx:
		return true
	default:
		return false
	}
}

// ClassifySubissuePlanLLMError maps SDK and transport failures into the
// fallback policy. Unknown errors fail closed: they are not retried against a
// different principal merely because a string happened to look transient.
func ClassifySubissuePlanLLMError(err error) SubissuePlanLLMErrorClass {
	if err == nil {
		return ""
	}
	if errors.Is(err, ErrLLMNotConfigured) || errors.Is(err, llm.ErrNotConfigured) {
		return SubissuePlanLLMErrorNotConfigured
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return SubissuePlanLLMErrorTimeout
	}
	var apiErr *openai.Error
	if errors.As(err, &apiErr) && apiErr != nil {
		switch {
		case apiErr.StatusCode == http.StatusRequestTimeout:
			return SubissuePlanLLMErrorTimeout
		case apiErr.StatusCode == http.StatusUnauthorized:
			return SubissuePlanLLMErrorAuthentication
		case apiErr.StatusCode == http.StatusForbidden:
			return SubissuePlanLLMErrorPermission
		case apiErr.StatusCode == http.StatusTooManyRequests,
			apiErr.StatusCode == http.StatusPaymentRequired:
			return SubissuePlanLLMErrorQuota
		case apiErr.StatusCode >= 500 && apiErr.StatusCode <= 599:
			return SubissuePlanLLMErrorProvider5xx
		case apiErr.StatusCode == http.StatusBadRequest:
			return SubissuePlanLLMErrorInvalidRequest
		}
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		if netErr.Timeout() {
			return SubissuePlanLLMErrorTimeout
		}
		return SubissuePlanLLMErrorNetwork
	}
	message := strings.ToLower(err.Error())
	for _, marker := range []string{"connection refused", "connection reset", "no such host", "network is unreachable", "broken pipe", "tls handshake"} {
		if strings.Contains(message, marker) {
			return SubissuePlanLLMErrorNetwork
		}
	}
	for _, marker := range []string{"invalid context", "context is invalid", "malformed context"} {
		if strings.Contains(message, marker) {
			return SubissuePlanLLMErrorInvalidContext
		}
	}
	if strings.Contains(message, "permission") || strings.Contains(message, "forbidden") {
		return SubissuePlanLLMErrorPermission
	}
	if strings.Contains(message, "invalid request") || strings.Contains(message, "bad request") {
		return SubissuePlanLLMErrorInvalidRequest
	}
	return SubissuePlanLLMErrorUnknown
}

// SubissuePlanRelatedIssue is the bounded, read-only issue summary supplied to
// a fallback runtime. Descriptions and comments are data, not instructions.
type SubissuePlanRelatedIssue struct {
	ID          string `json:"id"`
	Identifier  string `json:"identifier,omitempty"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	Status      string `json:"status,omitempty"`
	ParentID    string `json:"parent_issue_id,omitempty"`
	ProjectID   string `json:"project_id,omitempty"`
	Revision    int64  `json:"revision,omitempty"`
}

type SubissuePlanFallbackSnapshot struct {
	Version          int                        `json:"version"`
	WorkspaceID      string                     `json:"workspace_id"`
	IssueID          string                     `json:"issue_id"`
	ProposalID       string                     `json:"proposal_id,omitempty"`
	SourceCommentID  string                     `json:"source_comment_id,omitempty"`
	SourceIssue      SubissueSuggestSourceIssue `json:"source_issue"`
	Discussion       string                     `json:"discussion"`
	HumanConstraints string                     `json:"human_constraints,omitempty"`
	RelatedIssues    []SubissuePlanRelatedIssue `json:"related_issues,omitempty"`
	Siblings         []SubissueCandidateParent  `json:"siblings,omitempty"`
	CandidateParents []SubissueCandidateParent  `json:"candidate_parents,omitempty"`
	ProjectContext   string                     `json:"project_context,omitempty"`
}

// SubissuePlanOnlyContext is embedded in agent_task_queue.context and is the
// sole source of extra input for the plan-only prompt. It intentionally has no
// writable checkout, actor, or issue-mutation capability fields.
type SubissuePlanOnlyContext struct {
	Type                 string                       `json:"type"`
	JobID                string                       `json:"job_id"`
	WorkspaceID          string                       `json:"workspace_id"`
	IssueID              string                       `json:"issue_id"`
	SourceCommentID      string                       `json:"source_comment_id,omitempty"`
	SourceSnapshotDigest string                       `json:"source_snapshot_digest"`
	ContextRevision      int64                        `json:"context_revision"`
	HumanConstraints     string                       `json:"human_constraints,omitempty"`
	SourceSnapshot       SubissuePlanFallbackSnapshot `json:"source_snapshot"`
	Readonly             bool                         `json:"readonly"`
	ExpiresAt            time.Time                    `json:"expires_at"`
}

func SubissuePlanIdempotencyKey(workspaceID, issueID, commentID, content, constraints string, revision int64) string {
	h := sha256.New()
	for _, part := range []string{workspaceID, issueID, commentID, content, constraints, fmt.Sprintf("%d", revision)} {
		h.Write([]byte(part))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func DigestSubissuePlanSnapshot(snapshot SubissuePlanFallbackSnapshot) (string, []byte, error) {
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return "", nil, err
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), raw, nil
}

// BuildRuleBasedSubissuePlan is a safe final fallback. It never claims that a
// model understood the request; each editable item links back to an explicit
// non-empty line from the captured discussion.
func BuildRuleBasedSubissuePlan(source SubissueSuggestSourceIssue, discussion, humanConstraints string) []SubissuePlan {
	business := inferSubissueBusiness(source, discussion)
	if business == "" {
		business = SubissuePlanOverallBusiness
	}
	lines := strings.Split(strings.TrimSpace(discussion), "\n")
	items := make([]SubissuePlanItem, 0, SubissuePlanMaxCount)
	seen := map[string]struct{}{}
	for _, line := range lines {
		line = strings.TrimSpace(strings.TrimLeft(line, "-+*• 	0123456789.、)）"))
		if line == "" || len([]rune(line)) < 4 {
			continue
		}
		key := strings.ToLower(strings.Join(strings.Fields(line), " "))
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		if len(items) == SubissuePlanMaxCount {
			break
		}
		if len([]rune(line)) > SubissuePlanTitleMaxLength {
			line = string([]rune(line)[:SubissuePlanTitleMaxLength]) + "…"
		}
		items = append(items, SubissuePlanItem{
			ID:            fmt.Sprintf("rule-item-%d", len(items)+1),
			Title:         line,
			Goal:          "根据来源讨论逐项核对并完成：" + line,
			Kind:          SubissuePlanItemImplementation,
			Business:      business,
			SourceTaskIDs: []string{fmt.Sprintf("t%d", len(items)+1)},
		})
	}
	if len(items) == 0 {
		items = append(items, SubissuePlanItem{
			ID:       "rule-item-1",
			Title:    source.Title,
			Goal:     "根据来源讨论拆解并完成该 issue；创建前请人工补充边界和验收标准。",
			Kind:     SubissuePlanItemImplementation,
			Business: business,
		})
	}
	items = normalizeSubissuePlanTitlesWithBusiness(items, business)
	return []SubissuePlan{{
		ID:       "plan-rule-draft",
		Name:     "规则化可编辑草稿",
		Strategy: "rule_draft",
		Coverage: SubissuePlanCoverageFull,
		Items:    items,
	}}
}

// BuildRuleBasedSubissueDetails keeps the explicit review/create path usable
// when the outline came from a fallback job but the primary model is still
// unavailable for the second, detail-generation call. The result is plainly
// marked as editable draft content by its description and never mutates an
// issue.
func BuildRuleBasedSubissueDetails(plan SubissuePlan) []SubissueSuggestion {
	details := make([]SubissueSuggestion, 0, len(plan.Items))
	for index, item := range plan.Items {
		title := strings.TrimSpace(item.Title)
		goal := strings.TrimSpace(item.Goal)
		description := "规则草稿：请在创建前补充背景、范围和验收标准。"
		if goal != "" {
			description = "目标：" + goal + "\n\n" + description
		}
		details = append(details, SubissueSuggestion{
			ID:                        item.ID,
			Title:                     title,
			Goal:                      goal,
			Description:               description,
			Stage:                     index + 1,
			DependsOnTitles:           []string{},
			DependsOnIDs:              []string{},
			SuggestedParentIdentifier: nil,
			Confidence:                0,
		})
	}
	return details
}

// ValidateSubissuePlanOnlyResult is the second server-side trust boundary.
// The agent is allowed to return only {plans:[...]}; prose, markdown fences,
// unknown item kinds, oversized fields, duplicate IDs, and empty plans fail.
func ValidateSubissuePlanOnlyResult(raw []byte) ([]SubissuePlan, error) {
	text := strings.TrimSpace(string(raw))
	if strings.HasPrefix(text, "```") {
		text = regexp.MustCompile("^```(?:json)?\\s*|\\s*```$").ReplaceAllString(text, "")
	}
	var envelope struct {
		Plans []SubissuePlan `json:"plans"`
	}
	if err := json.Unmarshal([]byte(text), &envelope); err != nil {
		return nil, fmt.Errorf("plan-only result is not JSON: %w", err)
	}
	if len(envelope.Plans) == 0 || len(envelope.Plans) > SubissuePlanMaxCount {
		return nil, fmt.Errorf("plan-only result must contain 1-%d plans", SubissuePlanMaxCount)
	}
	for index := range envelope.Plans {
		plan := &envelope.Plans[index]
		if strings.TrimSpace(plan.ID) == "" {
			plan.ID = fmt.Sprintf("plan-agent-%d", index+1)
		}
		if strings.TrimSpace(plan.Name) == "" {
			plan.Name = fmt.Sprintf("关联 Agent 方案 %d", index+1)
		}
		if plan.Strategy == "" {
			plan.Strategy = "agent_plan_only"
		}
		if plan.Coverage == "" {
			plan.Coverage = SubissuePlanCoverageFull
		}
		if err := validateApprovedSubissuePlan(*plan); err != nil {
			return nil, err
		}
		for itemIndex := range plan.Items {
			if strings.TrimSpace(plan.Items[itemIndex].Business) == "" {
				plan.Items[itemIndex].Business = SubissuePlanOverallBusiness
			}
		}
	}
	return envelope.Plans, nil
}

func ExtractSubissuePlanTaskOutput(result []byte) (string, error) {
	var envelope struct {
		Output string `json:"output"`
	}
	if err := json.Unmarshal(result, &envelope); err != nil {
		return "", fmt.Errorf("decode plan-only task result: %w", err)
	}
	if strings.TrimSpace(envelope.Output) == "" {
		return "", errors.New("plan-only task returned empty output")
	}
	return envelope.Output, nil
}
