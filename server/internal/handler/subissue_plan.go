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
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/llm"
)

// SubissuePlanSourceRequest identifies the source discussion for both stages.
// The server resolves comment_id inside the requested issue; raw text remains
// available for trusted callers that already have the content.
type SubissuePlanSourceRequest struct {
	CommentID        *string `json:"comment_id,omitempty"`
	Text             *string `json:"text,omitempty"`
	HumanConstraints string  `json:"human_constraints,omitempty"`
}

type SuggestSubissuePlansResponse struct {
	Plans []service.SubissuePlan `json:"plans"`
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
	ctxData, ok := h.loadSubissuePlanContext(w, r, issue, req.CommentID, req.Text)
	if !ok {
		return
	}
	client := h.subissuePlanLLM(llm.BusinessStageOutline)
	if client == nil || !client.Enabled() {
		writeError(w, http.StatusServiceUnavailable, "llm not configured")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), service.SubissueSuggestTimeout)
	defer cancel()
	plans, err := service.SuggestSubissuePlans(
		ctx,
		client,
		ctxData.sourceIssue,
		ctxData.content,
		ctxData.siblings,
		ctxData.candidateParents,
		req.HumanConstraints,
	)
	if err != nil {
		h.logSubissuePlanFailure("plan generation", uuidToString(issue.ID), err)
		if errors.Is(err, service.ErrLLMNotConfigured) {
			writeError(w, http.StatusServiceUnavailable, "llm not configured")
			return
		}
		writeError(w, http.StatusBadGateway, "failed to generate subissue plans")
		return
	}
	writeJSON(w, http.StatusOK, SuggestSubissuePlansResponse{Plans: plans})
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
	ctxData, ok := h.loadSubissuePlanContext(w, r, issue, req.CommentID, req.Text)
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

func (h *Handler) loadSubissuePlanContext(w http.ResponseWriter, r *http.Request, issue db.Issue, commentID, text *string) (subissuePlanContext, bool) {
	content, ok := h.resolveSubissueSuggestContent(w, r, issue, SuggestSubIssuesRequest{
		CommentID: commentID,
		Text:      text,
	})
	if !ok {
		return subissuePlanContext{}, false
	}
	prefix := h.getIssuePrefix(r.Context(), issue.WorkspaceID)
	sourceIssue := service.SubissueSuggestSourceIssue{
		Identifier: prefix + "-" + strconv.Itoa(int(issue.Number)),
		Title:      issue.Title,
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
