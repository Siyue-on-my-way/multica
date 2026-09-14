package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/llm"
)

// SubissuePlanSourceRequest identifies the source discussion for both stages.
// The server resolves comment_id inside the requested issue; raw text remains
// available for trusted callers that already have the content.
type SubissuePlanSourceRequest struct {
	CommentID            *string `json:"comment_id,omitempty"`
	Text                 *string `json:"text,omitempty"`
	ProposalID           *string `json:"proposal_id,omitempty"`
	HumanConstraints     string  `json:"human_constraints,omitempty"`
	CarryAncestorContext *bool   `json:"carry_ancestor_context,omitempty"`
}

type SuggestSubissuePlansResponse struct {
	Plans             []service.SubissuePlan                `json:"plans"`
	Diagnostics       []service.SubissuePlanDiagnostic      `json:"diagnostics,omitempty"`
	Summary           service.SubissuePlanGenerationSummary `json:"summary"`
	AncestorBriefRefs []service.AncestorBriefRef            `json:"ancestor_brief_refs,omitempty"`
	// InheritedExecutionContext is the merged, de-duplicated execution context
	// the created children will receive at runtime (SIY-177): project
	// resources, ancestors that left inheritance on, and the source issue's
	// own entries. The preview panel renders it as the read-only
	// "已继承执行上下文" card — the descriptions below never embed it.
	InheritedExecutionContext *service.ResolvedExecutionContext `json:"inherited_execution_context,omitempty"`
	// ProposalID binds this preview to an immutable source snapshot. It is
	// created lazily for legacy comments so the old suggestion entry point gains
	// the same parent/child traceability as a structured AI proposal.
	ProposalID string `json:"proposal_id,omitempty"`
}

type suggestSubissuePlansErrorResponse struct {
	Error       string                                `json:"error"`
	Summary     service.SubissuePlanGenerationSummary `json:"summary"`
	Diagnostics []service.SubissuePlanDiagnostic      `json:"diagnostics,omitempty"`
}

type ExpandSubissuePlanRequest struct {
	SubissuePlanSourceRequest
	Plan service.SubissuePlan `json:"plan"`
}

// SuggestSubissuePlans is the first, intentionally lightweight AI pass. It
// returns alternative title/goal outlines and never creates or mutates issues.
func (h *Handler) SuggestSubissuePlans(w http.ResponseWriter, r *http.Request) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	var req SubissuePlanSourceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	ctxData, ok := h.loadSubissuePlanContext(w, r, issue, req)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), service.SubissueSuggestTimeout)
	defer cancel()
	generation, err := service.SuggestSubissuePlansWithDiagnostics(
		ctx,
		service.SubissuePlanStages{
			SingleFullContext: h.subissuePlanLLM(llm.BusinessStageSingleFullContext),
			Parallel:          h.subissuePlanLLM(llm.BusinessStageParallel),
			FullCoverage:      h.subissuePlanLLM(llm.BusinessStageFullCoverage),
			Recognize:         h.subissuePlanLLM(llm.BusinessStageRecognize),
		},
		ctxData.sourceIssue,
		ctxData.content,
		ctxData.siblings,
		ctxData.candidateParents,
		req.HumanConstraints,
		subissuePlanCommentID(req.CommentID),
	)
	if err != nil {
		h.logSubissuePlanFailure("plan generation", uuidToString(issue.ID), err)
		if errors.Is(err, service.ErrLLMNotConfigured) {
			writeJSON(w, http.StatusServiceUnavailable, suggestSubissuePlansErrorResponse{
				Error:       "llm not configured",
				Summary:     generation.Summary,
				Diagnostics: generation.Diagnostics,
			})
			return
		}
		writeJSON(w, http.StatusBadGateway, suggestSubissuePlansErrorResponse{
			Error:       "failed to generate subissue plans",
			Summary:     generation.Summary,
			Diagnostics: generation.Diagnostics,
		})
		return
	}
	prefix := h.getIssuePrefix(r.Context(), issue.WorkspaceID)
	proposalID := ""
	if req.ProposalID != nil && strings.TrimSpace(*req.ProposalID) != "" {
		proposal, proposalOK := h.loadProposalForIssue(w, r, issue, *req.ProposalID)
		if !proposalOK {
			return
		}
		proposalID = uuidToString(proposal.ID)
	}
	var questionID pgtype.UUID
	if proposalID == "" && req.CommentID != nil && strings.TrimSpace(*req.CommentID) != "" {
		var commentOK bool
		questionID, commentOK = h.parseOptionalProposalComment(w, r, issue, req.CommentID)
		if !commentOK {
			return
		}
	}
	legacySnapshot, snapshotErr := json.Marshal(map[string]any{
		"title": issue.Title, "summary": ctxData.content, "scope": "legacy/manual",
		"risks": []string{}, "acceptanceCriteria": []string{}, "items": generation.Plans,
	})
	if snapshotErr == nil {
		if proposalID == "" {
			if proposal, _, proposalErr := h.ensureDiscussionProposal(r, issue, questionID, pgtype.UUID{}, legacySnapshot); proposalErr == nil {
				proposalID = uuidToString(proposal.ID)
			} else {
				slog.Warn("save legacy proposal snapshot failed", "issue_id", uuidToString(issue.ID), "error", proposalErr)
			}
		}
	}
	writeJSON(w, http.StatusOK, SuggestSubissuePlansResponse{Plans: generation.Plans, Diagnostics: generation.Diagnostics, Summary: generation.Summary, AncestorBriefRefs: func() []service.AncestorBriefRef {
		if req.CarryAncestorContext != nil && !*req.CarryAncestorContext {
			return nil
		}
		return service.BuildAncestorBrief(r.Context(), h.Queries, issue).Refs
	}(), InheritedExecutionContext: func() *service.ResolvedExecutionContext {
		resolved := h.resolveInheritedExecutionContext(r, issue, prefix)
		if resolved.Empty() {
			return nil
		}
		return &resolved
	}(), ProposalID: proposalID})
}

// ExpandSubissuePlan generates detail only for the user-approved outline.
// The returned structure is still a preview; issue creation remains a
// separate, explicit frontend action.
func (h *Handler) ExpandSubissuePlan(w http.ResponseWriter, r *http.Request) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	var req ExpandSubissuePlanRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	ctxData, ok := h.loadSubissuePlanContext(w, r, issue, req.SubissuePlanSourceRequest)
	if !ok {
		return
	}
	client := h.subissuePlanLLM(llm.BusinessStageDetail)
	if client == nil || !client.Enabled() {
		writeError(w, http.StatusServiceUnavailable, "llm not configured")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), service.SubissueSuggestTimeout)
	defer cancel()
	suggestions, err := service.ExpandSubissuePlan(
		ctx,
		client,
		ctxData.sourceIssue,
		ctxData.content,
		ctxData.siblings,
		ctxData.candidateParents,
		req.Plan,
		req.HumanConstraints,
		subissuePlanCommentID(req.CommentID),
	)
	if err != nil {
		h.logSubissuePlanFailure("detail generation", uuidToString(issue.ID), err)
		if errors.Is(err, service.ErrLLMNotConfigured) {
			writeError(w, http.StatusServiceUnavailable, "llm not configured")
			return
		}
		writeError(w, http.StatusBadGateway, "failed to generate subissue details")
		return
	}
	writeJSON(w, http.StatusOK, h.resolveSubissueSuggestionParents(suggestions, ctxData.parentByIdentifier))
}

type subissuePlanContext struct {
	content            string
	sourceIssue        service.SubissueSuggestSourceIssue
	siblings           []service.SubissueCandidateParent
	candidateParents   []service.SubissueCandidateParent
	parentByIdentifier map[string]subissueCandidateParentRef
}

func (h *Handler) loadSubissuePlanContext(w http.ResponseWriter, r *http.Request, issue db.Issue, req SubissuePlanSourceRequest) (subissuePlanContext, bool) {
	content, ok := h.resolveSubissueSuggestContent(w, r, issue, SuggestSubIssuesRequest{
		CommentID:  req.CommentID,
		Text:       req.Text,
		ProposalID: req.ProposalID,
	})
	if !ok {
		return subissuePlanContext{}, false
	}
	prefix := h.getIssuePrefix(r.Context(), issue.WorkspaceID)
	// SIY-177 feedback: the split/detail prompts carry the merged execution
	// context (repos / directories / commands with provenance), never the
	// ancestor issue descriptions. The carry switch gates whether that context
	// feeds the model at all.
	executionContext := ""
	if req.CarryAncestorContext == nil || *req.CarryAncestorContext {
		executionContext = service.RenderExecutionContextForPrompt(
			h.resolveInheritedExecutionContext(r, issue, prefix),
			service.SubissueExecutionContextPromptBudget,
		)
	}
	sourceIssue := service.SubissueSuggestSourceIssue{
		Identifier:       prefix + "-" + strconv.Itoa(int(issue.Number)),
		Title:            issue.Title,
		Description:      issue.Description.String,
		ExecutionContext: executionContext,
	}
	siblings, err := h.Queries.ListChildIssues(r.Context(), issue.ID)
	if err != nil {
		slog.Warn("suggest subissue plans: list siblings failed", "issue_id", uuidToString(issue.ID), "error", err)
		siblings = nil
	}
	siblingCandidates := make([]service.SubissueCandidateParent, 0, len(siblings))
	for _, sibling := range siblings {
		siblingCandidates = append(siblingCandidates, service.SubissueCandidateParent{
			Identifier: prefix + "-" + strconv.Itoa(int(sibling.Number)),
			Title:      sibling.Title,
		})
	}
	candidateParents, parentByIdentifier := h.buildSubissueSuggestCandidateParents(r.Context(), issue, prefix)
	return subissuePlanContext{
		content:            content,
		sourceIssue:        sourceIssue,
		siblings:           siblingCandidates,
		candidateParents:   candidateParents,
		parentByIdentifier: parentByIdentifier,
	}, true
}

func (h *Handler) subissuePlanLLM(stage string) service.SubissueSuggestConfiguredLLM {
	if business := h.businessLLM(llm.BusinessSubissueSuggest); business != nil {
		return business.Stage(stage)
	}
	if h == nil || h.LLM == nil {
		return nil
	}
	return service.LegacySubissueSuggestConfiguredLLM{Client: h.LLM}
}

func (h *Handler) resolveSubissueSuggestionParents(suggestions []service.SubissueSuggestion, parentByIdentifier map[string]subissueCandidateParentRef) SuggestSubIssuesResponse {
	resp := SuggestSubIssuesResponse{Subissues: make([]SubIssueSuggestionResponse, 0, len(suggestions))}
	for _, suggestion := range suggestions {
		out := SubIssueSuggestionResponse{
			ID:              suggestion.ID,
			Title:           suggestion.Title,
			Goal:            suggestion.Goal,
			Description:     suggestion.Description,
			Stage:           int32(suggestion.Stage),
			DependsOnTitles: suggestion.DependsOnTitles,
			DependsOnIDs:    suggestion.DependsOnIDs,
			Confidence:      suggestion.Confidence,
		}
		if suggestion.SuggestedParentIdentifier != nil {
			key := strings.ToUpper(strings.TrimSpace(*suggestion.SuggestedParentIdentifier))
			if parent, found := parentByIdentifier[key]; found {
				identifier := parent.identifier
				id := parent.id
				out.SuggestedParentIdentifier = &identifier
				out.SuggestedParentIssueID = &id
			}
		}
		resp.Subissues = append(resp.Subissues, out)
	}
	return resp
}

func (h *Handler) logSubissuePlanFailure(operation, issueID string, err error) {
	slog.Warn("suggest subissue plans: generation failed", "operation", operation, "issue_id", issueID, "error", err)
}

// subissuePlanCommentID surfaces the triggering comment in service-level logs
// so per-attempt token/coverage lines can be tied back to the source comment.
func subissuePlanCommentID(commentID *string) string {
	if commentID == nil {
		return ""
	}
	return strings.TrimSpace(*commentID)
}
