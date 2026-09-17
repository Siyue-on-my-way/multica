package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/llm"
)

// SuggestSubIssuesRequest is the body for POST /api/issues/{id}/suggest-subissues.
// Exactly one of CommentID / Text must be set for the source. ProposalID is
// additive lineage when either source is present; when both are absent it
// selects the immutable proposal snapshot as the source. This lets a proposal
// created without a comment anchor still open the same review/decomposition
// flow without inventing a comment id.
type SuggestSubIssuesRequest struct {
	CommentID  *string `json:"comment_id,omitempty"`
	Text       *string `json:"text,omitempty"`
	ProposalID *string `json:"proposal_id,omitempty"`
}

// SubIssueSuggestionResponse is one candidate sub-issue returned to the
// preview panel. SuggestedParentIssueID is resolved server-side from
// SuggestedParentIdentifier against the real candidate list — the frontend
// never has to re-resolve an identifier the model produced.
type SubIssueSuggestionResponse struct {
	ID                        string   `json:"id,omitempty"`
	Title                     string   `json:"title"`
	Goal                      string   `json:"goal,omitempty"`
	Description               string   `json:"description"`
	Stage                     int32    `json:"stage"`
	DependsOnTitles           []string `json:"depends_on_titles"`
	DependsOnIDs              []string `json:"depends_on_ids,omitempty"`
	SuggestedParentIdentifier *string  `json:"suggested_parent_identifier"`
	SuggestedParentIssueID    *string  `json:"suggested_parent_issue_id"`
	Confidence                float64  `json:"confidence"`
}

type SuggestSubIssuesResponse struct {
	Subissues    []SubIssueSuggestionResponse  `json:"subissues"`
	SourcePacket *service.SubissueSourcePacket `json:"source_packet,omitempty"`
}

// SuggestSubIssues runs one AI decomposition pass over a comment's content
// (or arbitrary text) and returns candidate sub-issues for the "生成子issue"
// preview panel to render. It does not create anything — see CreateIssue for
// the batch-create step the frontend calls once the user confirms.
func (h *Handler) SuggestSubIssues(w http.ResponseWriter, r *http.Request) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}

	businessLLM := h.businessLLM(llm.BusinessSubissueSuggest)
	if businessLLM != nil {
		if !businessLLM.Enabled() {
			writeError(w, http.StatusServiceUnavailable, "llm not configured")
			return
		}
	} else if h.LLM == nil || !h.LLM.Enabled() {
		writeError(w, http.StatusServiceUnavailable, "llm not configured")
		return
	}

	var req SuggestSubIssuesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	content, ok := h.resolveSubissueSuggestContent(w, r, issue, req)
	if !ok {
		return
	}

	prefix := h.getIssuePrefix(r.Context(), issue.WorkspaceID)
	// SIY-177 feedback: the generation prompt carries the merged execution
	// context (repos / directories / commands with provenance), never the
	// ancestor issue descriptions.
	executionContext := service.RenderExecutionContextForPrompt(
		h.resolveInheritedExecutionContext(r, issue, prefix),
		service.SubissueExecutionContextPromptBudget,
	)
	sourceIssue := service.SubissueSuggestSourceIssue{
		Identifier:       prefix + "-" + strconv.Itoa(int(issue.Number)),
		Title:            issue.Title,
		Description:      issue.Description.String,
		ExecutionContext: executionContext,
	}

	siblings, err := h.Queries.ListChildIssues(r.Context(), issue.ID)
	if err != nil {
		slog.Warn("suggest subissues: list siblings failed", "issue_id", uuidToString(issue.ID), "error", err)
		siblings = nil
	}
	siblingCandidates := make([]service.SubissueCandidateParent, 0, len(siblings))
	for _, s := range siblings {
		siblingCandidates = append(siblingCandidates, service.SubissueCandidateParent{
			Identifier: prefix + "-" + strconv.Itoa(int(s.Number)),
			Title:      s.Title,
		})
	}

	candidateParents, parentByIdentifier := h.buildSubissueSuggestCandidateParents(r.Context(), issue, prefix)
	sourcePacket := h.buildSubissueSourcePacket(r, issue, req, content, prefix)

	ctx, cancel := context.WithTimeout(r.Context(), service.SubissueSuggestTimeout)
	defer cancel()

	var suggestions []service.SubissueSuggestion
	if businessLLM != nil {
		suggestions, err = service.SuggestSubissuesWithConfig(ctx, businessLLM, sourceIssue, content, siblingCandidates, candidateParents)
	} else {
		suggestions, err = service.SuggestSubissues(ctx, h.LLM, sourceIssue, content, siblingCandidates, candidateParents)
	}
	if err != nil {
		if errors.Is(err, service.ErrLLMNotConfigured) {
			writeError(w, http.StatusServiceUnavailable, "llm not configured")
			return
		}
		slog.Warn("suggest subissues: generation failed", "issue_id", uuidToString(issue.ID), "error", err)
		writeError(w, http.StatusBadGateway, "failed to generate subissue suggestions")
		return
	}

	resp := SuggestSubIssuesResponse{Subissues: make([]SubIssueSuggestionResponse, 0, len(suggestions)), SourcePacket: sourcePacket}
	for _, s := range suggestions {
		out := SubIssueSuggestionResponse{
			ID:              s.ID,
			Title:           s.Title,
			Goal:            s.Goal,
			Description:     service.RenderSubissueDescription(s.Description, s.Goal, sourcePacket, nil),
			Stage:           int32(s.Stage),
			DependsOnTitles: s.DependsOnTitles,
			DependsOnIDs:    s.DependsOnIDs,
			Confidence:      s.Confidence,
		}
		if s.SuggestedParentIdentifier != nil {
			key := strings.ToUpper(strings.TrimSpace(*s.SuggestedParentIdentifier))
			if parent, found := parentByIdentifier[key]; found {
				identifier := parent.identifier
				id := parent.id
				out.SuggestedParentIdentifier = &identifier
				out.SuggestedParentIssueID = &id
			}
			// A model-invented identifier that isn't in the candidate list is
			// dropped rather than surfaced — the panel must never pre-select a
			// parent the user did not actually see offered as an option.
		}
		resp.Subissues = append(resp.Subissues, out)
	}

	writeJSON(w, http.StatusOK, resp)
}

// resolveSubissueSuggestContent picks the source text for the pass: the
// triggering comment, scoped to this issue so a stale comment_id from another
// issue can never leak its content; an explicit Text; or an immutable proposal
// snapshot when the proposal has no comment anchor.
func (h *Handler) resolveSubissueSuggestContent(w http.ResponseWriter, r *http.Request, issue db.Issue, req SuggestSubIssuesRequest) (string, bool) {
	hasComment := req.CommentID != nil && strings.TrimSpace(*req.CommentID) != ""
	hasText := req.Text != nil && strings.TrimSpace(*req.Text) != ""
	proposalID := ""
	if req.ProposalID != nil {
		proposalID = strings.TrimSpace(*req.ProposalID)
	}
	var proposal db.DiscussionProposal
	if proposalID != "" {
		var ok bool
		proposal, ok = h.loadProposalForIssue(w, r, issue, proposalID)
		if !ok {
			return "", false
		}
	}
	if hasComment && hasText {
		writeError(w, http.StatusBadRequest, "only one of comment_id or text may be provided")
		return "", false
	}

	if hasText {
		return strings.TrimSpace(*req.Text), true
	}
	if !hasComment {
		if proposalID == "" {
			writeError(w, http.StatusBadRequest, "one of comment_id, text, or proposal_id is required")
			return "", false
		}
		// A proposal snapshot is a derived view. When it has an answer anchor,
		// always recover the immutable agent reply as the decomposition source;
		// the summary snapshot is only a fallback for legacy/manual proposals.
		if proposal.AnswerCommentID.Valid {
			if answer, answerErr := h.Queries.GetCommentInWorkspace(r.Context(), db.GetCommentInWorkspaceParams{
				ID: proposal.AnswerCommentID, WorkspaceID: issue.WorkspaceID,
			}); answerErr == nil && answer.IssueID == issue.ID && strings.TrimSpace(answer.Content) != "" {
				return strings.TrimSpace(answer.Content), true
			}
		}
		if proposal.QuestionCommentID.Valid {
			if question, questionErr := h.Queries.GetCommentInWorkspace(r.Context(), db.GetCommentInWorkspaceParams{
				ID: proposal.QuestionCommentID, WorkspaceID: issue.WorkspaceID,
			}); questionErr == nil && question.IssueID == issue.ID && strings.TrimSpace(question.Content) != "" {
				return strings.TrimSpace(question.Content), true
			}
		}
		content := strings.TrimSpace(string(proposal.Snapshot))
		if content == "" || content == "null" {
			writeError(w, http.StatusBadRequest, "proposal has no content to decompose")
			return "", false
		}
		return content, true
	}

	commentUUID, ok := parseUUIDOrBadRequest(w, *req.CommentID, "comment_id")
	if !ok {
		return "", false
	}
	comment, err := h.Queries.GetCommentInWorkspace(r.Context(), db.GetCommentInWorkspaceParams{
		ID:          commentUUID,
		WorkspaceID: issue.WorkspaceID,
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "comment not found")
		return "", false
	}
	if comment.IssueID != issue.ID {
		writeError(w, http.StatusNotFound, "comment not found")
		return "", false
	}
	content := strings.TrimSpace(comment.Content)
	if content == "" {
		writeError(w, http.StatusBadRequest, "comment has no content to decompose")
		return "", false
	}
	return content, true
}

type subissueCandidateParentRef struct {
	identifier string
	id         string
}

// buildSubissueSuggestCandidateParents lists the parents offered to the
// model: the source issue itself first (the obvious default — these
// sub-issues are being extracted from ITS discussion), then other open
// issues in the same project, most recently updated first, capped so a large
// project never blows the prompt budget. Returns both the ordered list (for
// the prompt) and an identifier->ref lookup (for resolving the model's pick
// without a second DB round trip).
func (h *Handler) buildSubissueSuggestCandidateParents(ctx context.Context, issue db.Issue, prefix string) ([]service.SubissueCandidateParent, map[string]subissueCandidateParentRef) {
	selfIdentifier := prefix + "-" + strconv.Itoa(int(issue.Number))
	candidates := []service.SubissueCandidateParent{{Identifier: selfIdentifier, Title: issue.Title}}
	byIdentifier := map[string]subissueCandidateParentRef{
		strings.ToUpper(selfIdentifier): {identifier: selfIdentifier, id: uuidToString(issue.ID)},
	}

	if !issue.ProjectID.Valid {
		return candidates, byIdentifier
	}

	rows, err := h.Queries.ListOpenIssues(ctx, db.ListOpenIssuesParams{
		WorkspaceID: issue.WorkspaceID,
		ProjectID:   issue.ProjectID,
	})
	if err != nil {
		slog.Warn("suggest subissues: list candidate parents failed", "issue_id", uuidToString(issue.ID), "error", err)
		return candidates, byIdentifier
	}

	sort.Slice(rows, func(i, j int) bool {
		return rows[i].UpdatedAt.Time.After(rows[j].UpdatedAt.Time)
	})

	max := service.SubissueSuggestCandidateParentMax()
	for _, row := range rows {
		if row.ID == issue.ID {
			continue
		}
		if len(candidates) >= max {
			break
		}
		identifier := prefix + "-" + strconv.Itoa(int(row.Number))
		candidates = append(candidates, service.SubissueCandidateParent{Identifier: identifier, Title: row.Title})
		byIdentifier[strings.ToUpper(identifier)] = subissueCandidateParentRef{identifier: identifier, id: uuidToString(row.ID)}
	}

	return candidates, byIdentifier
}
