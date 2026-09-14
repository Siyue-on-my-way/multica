package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
)

const subissuePlanJobFallbackTTL = 15 * time.Minute

// SubissuePlanJobResponse is the public, non-sensitive view of a planning
// job. The immutable source snapshot is deliberately represented by its
// digest only; the source comment/issue remains available through the normal
// permission-checked issue APIs.
type SubissuePlanJobResponse struct {
	ID                   string                 `json:"id"`
	IssueID              string                 `json:"issue_id"`
	SourceCommentID      string                 `json:"source_comment_id,omitempty"`
	SourceSnapshotDigest string                 `json:"source_snapshot_digest"`
	ContextRevision      int64                  `json:"context_revision"`
	Status               string                 `json:"status"`
	Source               string                 `json:"source"`
	FallbackReason       string                 `json:"fallback_reason,omitempty"`
	PrimaryErrorClass    string                 `json:"primary_error_class,omitempty"`
	AgentID              string                 `json:"agent_id,omitempty"`
	TaskID               string                 `json:"task_id,omitempty"`
	Plans                []service.SubissuePlan `json:"plans,omitempty"`
	ErrorSummary         string                 `json:"error_summary,omitempty"`
	ProposalID           string                 `json:"proposal_id,omitempty"`
	CreatedAt            string                 `json:"created_at"`
	UpdatedAt            string                 `json:"updated_at"`
	StartedAt            string                 `json:"started_at,omitempty"`
	CompletedAt          string                 `json:"completed_at,omitempty"`
	ExpiresAt            string                 `json:"expires_at"`
}

type subissuePlanJobAcceptedResponse struct {
	Job            SubissuePlanJobResponse               `json:"job"`
	JobID          string                                `json:"job_id"`
	Status         string                                `json:"status"`
	Source         string                                `json:"source"`
	FallbackReason string                                `json:"fallback_reason"`
	Diagnostics    []service.SubissuePlanDiagnostic      `json:"diagnostics,omitempty"`
	Summary        service.SubissuePlanGenerationSummary `json:"summary"`
}

type subissuePlanFallbackOutcome struct {
	StatusCode int
	Body       any
}

func (h *Handler) handleSubissuePlanFallback(
	r *http.Request,
	issue db.Issue,
	ctxData subissuePlanContext,
	req SubissuePlanSourceRequest,
	primaryErr service.SubissuePlanLLMErrorClass,
) (subissuePlanFallbackOutcome, error) {
	if h == nil || h.Queries == nil || h.TaskService == nil {
		return subissuePlanFallbackOutcome{}, errors.New("planning fallback is not configured")
	}
	if !primaryErr.Degradable() {
		return subissuePlanFallbackOutcome{}, errors.New("primary planning error is not degradable")
	}

	proposalID := h.ensureFallbackProposal(r, issue, req, ctxData.content)
	snapshot := h.buildSubissuePlanFallbackSnapshot(r.Context(), issue, ctxData, req, proposalID)
	digest, rawSnapshot, err := service.DigestSubissuePlanSnapshot(snapshot)
	if err != nil {
		return subissuePlanFallbackOutcome{}, fmt.Errorf("digest fallback snapshot: %w", err)
	}
	commentID := subissuePlanCommentID(req.CommentID)
	idempotencyKey := service.SubissuePlanIdempotencyKey(
		util.UUIDToString(issue.WorkspaceID),
		util.UUIDToString(issue.ID),
		commentID,
		ctxData.content,
		req.HumanConstraints,
		issue.Revision,
	)

	job, err := h.Queries.CreateSubissuePlanJob(r.Context(), db.CreateSubissuePlanJobParams{
		ID:                   dbid.NewV7(),
		WorkspaceID:          issue.WorkspaceID,
		IssueID:              issue.ID,
		SourceCommentID:      optionalUUID(commentID),
		SourceSnapshot:       rawSnapshot,
		SourceSnapshotDigest: digest,
		ContextRevision:      issue.Revision,
		HumanConstraints:     strings.TrimSpace(req.HumanConstraints),
		PrimaryErrorClass:    string(primaryErr),
		FallbackReason:       service.SubissuePlanFallbackReason,
		IdempotencyKey:       idempotencyKey,
		ExpiresAt:            pgtype.Timestamptz{Time: time.Now().Add(subissuePlanJobFallbackTTL), Valid: true},
	})
	if errors.Is(err, pgx.ErrNoRows) {
		job, err = h.Queries.GetSubissuePlanJobByIdempotencyKey(r.Context(), db.GetSubissuePlanJobByIdempotencyKeyParams{
			WorkspaceID:    issue.WorkspaceID,
			IssueID:        issue.ID,
			IdempotencyKey: idempotencyKey,
		})
	}
	if err != nil {
		return subissuePlanFallbackOutcome{}, fmt.Errorf("create or load fallback planning job: %w", err)
	}

	if job.Status == "succeeded" {
		plans := plansFromSubissuePlanJob(job)
		return subissuePlanFallbackOutcome{
			StatusCode: http.StatusOK,
			Body: h.subissuePlanPreviewResponse(r, issue, req, plans, []service.SubissuePlanDiagnostic{
				jobDiagnostic(job, jobSource(job), ""),
			}, "success", proposalIDFromJob(job, proposalID)),
		}, nil
	}
	if job.Status == "queued" || job.Status == "running" {
		if job.TaskID.Valid {
			return subissuePlanFallbackOutcome{
				StatusCode: http.StatusAccepted,
				Body:       acceptedSubissuePlanJob(job),
			}, nil
		}
	}
	if job.Status == "queued" && !job.TaskID.Valid {
		claimed, claimErr := h.Queries.ClaimSubissuePlanJobDispatch(r.Context(), db.ClaimSubissuePlanJobDispatchParams{
			ID: job.ID, WorkspaceID: issue.WorkspaceID,
		})
		if errors.Is(claimErr, pgx.ErrNoRows) {
			current, readErr := h.Queries.GetSubissuePlanJob(r.Context(), db.GetSubissuePlanJobParams{
				ID: job.ID, WorkspaceID: issue.WorkspaceID,
			})
			if readErr != nil {
				return subissuePlanFallbackOutcome{}, fmt.Errorf("reload fallback planning job: %w", readErr)
			}
			if current.Status == "succeeded" {
				return subissuePlanFallbackOutcome{
					StatusCode: http.StatusOK,
					Body: h.subissuePlanPreviewResponse(r, issue, req, plansFromSubissuePlanJob(current), []service.SubissuePlanDiagnostic{
						jobDiagnostic(current, jobSource(current), ""),
					}, "success", proposalIDFromJob(current, proposalID)),
				}, nil
			}
			return subissuePlanFallbackOutcome{StatusCode: http.StatusAccepted, Body: acceptedSubissuePlanJob(current)}, nil
		}
		if claimErr != nil {
			return subissuePlanFallbackOutcome{}, fmt.Errorf("claim fallback planning job dispatch: %w", claimErr)
		}
		job = claimed
	}

	// A failed/cancelled/expired row is reusable only through the explicit
	// retry endpoint. A new click with the same idempotency key must not create
	// a second job or silently restart a failed one.
	if job.Status != "queued" {
		return subissuePlanFallbackOutcome{
			StatusCode: http.StatusAccepted,
			Body:       acceptedSubissuePlanJob(job),
		}, nil
	}

	candidates, err := h.Queries.ListSubissuePlanAgentCandidates(r.Context(), db.ListSubissuePlanAgentCandidatesParams{
		WorkspaceID: issue.WorkspaceID,
		IssueID:     issue.ID,
		ProjectID:   issue.ProjectID,
	})
	if err != nil {
		// Candidate lookup is an optional enhancement. The user can still edit
		// the deterministic draft when the hierarchy query is unavailable.
		candidates = nil
	}
	job, task, selectedAgent, dispatchErr := h.dispatchSubissuePlanJob(r.Context(), issue, job, snapshot, candidates)
	if dispatchErr != nil {
		return subissuePlanFallbackOutcome{}, dispatchErr
	}
	if task.ID.Valid {
		return subissuePlanFallbackOutcome{
			StatusCode: http.StatusAccepted,
			Body:       acceptedSubissuePlanJob(jobWithTask(job, selectedAgent, task)),
		}, nil
	}

	plans := service.BuildRuleBasedSubissuePlan(ctxData.sourceIssue, ctxData.content, req.HumanConstraints)
	result, marshalErr := json.Marshal(struct {
		Plans []service.SubissuePlan `json:"plans"`
	}{Plans: plans})
	if marshalErr != nil {
		return subissuePlanFallbackOutcome{}, fmt.Errorf("encode rule fallback result: %w", marshalErr)
	}
	job, err = h.Queries.CompleteSubissuePlanJob(r.Context(), db.CompleteSubissuePlanJobParams{
		ID:           job.ID,
		Status:       "succeeded",
		Result:       result,
		ErrorSummary: pgtype.Text{},
	})
	if err != nil {
		return subissuePlanFallbackOutcome{}, fmt.Errorf("complete rule fallback job: %w", err)
	}
	diagnostic := jobDiagnostic(job, service.SubissuePlanSourceRule, service.SubissuePlanNoCandidate)
	return subissuePlanFallbackOutcome{
		StatusCode: http.StatusOK,
		Body:       h.subissuePlanPreviewResponse(r, issue, req, plans, []service.SubissuePlanDiagnostic{diagnostic}, "success", proposalIDFromJob(job, proposalID)),
	}, nil
}

// dispatchSubissuePlanJob makes at most two enqueue attempts. The second
// attempt is reserved for a failure before the first candidate can actually
// claim the task; it is never a fan-out.
func (h *Handler) dispatchSubissuePlanJob(
	ctx context.Context,
	issue db.Issue,
	job db.SubissuePlanJob,
	snapshot service.SubissuePlanFallbackSnapshot,
	candidates []db.ListSubissuePlanAgentCandidatesRow,
) (db.SubissuePlanJob, db.AgentTaskQueue, pgtype.UUID, error) {
	seen := make(map[string]struct{}, len(candidates))
	attempts := 0
	for _, candidate := range candidates {
		key := util.UUIDToString(candidate.AgentID)
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		if attempts >= 2 {
			break
		}
		attempts++
		expiresAt := job.ExpiresAt.Time
		if expiresAt.IsZero() {
			expiresAt = time.Now().Add(subissuePlanJobFallbackTTL)
		}
		planContext := service.SubissuePlanOnlyContext{
			Type:                 service.SubissuePlanOnlyContextType,
			JobID:                util.UUIDToString(job.ID),
			WorkspaceID:          util.UUIDToString(issue.WorkspaceID),
			IssueID:              util.UUIDToString(issue.ID),
			SourceCommentID:      snapshot.SourceCommentID,
			SourceSnapshotDigest: snapshotDigest(snapshot),
			ContextRevision:      snapshotRevision(snapshot, issue.Revision),
			HumanConstraints:     snapshot.HumanConstraints,
			SourceSnapshot:       snapshot,
			Readonly:             true,
			ExpiresAt:            expiresAt,
		}
		task, err := h.TaskService.EnqueueSubissuePlanOnlyTask(ctx, issue, candidate.AgentID, planContext)
		if err != nil {
			continue
		}
		updated, err := h.Queries.SetSubissuePlanJobTask(ctx, db.SetSubissuePlanJobTaskParams{
			ID:          job.ID,
			WorkspaceID: issue.WorkspaceID,
			AgentID:     candidate.AgentID,
			TaskID:      task.ID,
		})
		if err != nil {
			// The task row was created before the job binding. Cancel it through
			// TaskService so the task-scoped lifecycle cleanup also cancels the
			// job; then reopen the dispatch lease for the one permitted second
			// candidate. Without this compensation a failed binding leaves either
			// an orphan queued task or a permanently running job with no task.
			if _, cancelErr := h.TaskService.CancelTask(ctx, task.ID); cancelErr != nil {
				return job, db.AgentTaskQueue{}, pgtype.UUID{}, fmt.Errorf("cancel unbound plan-only task: %w", cancelErr)
			}
			reset, resetErr := h.Queries.RetrySubissuePlanJob(ctx, db.RetrySubissuePlanJobParams{
				ID: job.ID, WorkspaceID: issue.WorkspaceID,
			})
			if resetErr != nil {
				return job, db.AgentTaskQueue{}, pgtype.UUID{}, fmt.Errorf("reset unbound planning job: %w", resetErr)
			}
			job = reset
			continue
		}
		return updated, task, candidate.AgentID, nil
	}
	return job, db.AgentTaskQueue{}, pgtype.UUID{}, nil
}

func (h *Handler) buildSubissuePlanFallbackSnapshot(
	ctx context.Context,
	issue db.Issue,
	ctxData subissuePlanContext,
	req SubissuePlanSourceRequest,
	proposalID string,
) service.SubissuePlanFallbackSnapshot {
	snapshot := service.SubissuePlanFallbackSnapshot{
		Version:          1,
		WorkspaceID:      util.UUIDToString(issue.WorkspaceID),
		IssueID:          util.UUIDToString(issue.ID),
		ProposalID:       proposalID,
		SourceCommentID:  subissuePlanCommentID(req.CommentID),
		SourceIssue:      ctxData.sourceIssue,
		Discussion:       ctxData.content,
		HumanConstraints: strings.TrimSpace(req.HumanConstraints),
		Siblings:         ctxData.siblings,
		CandidateParents: ctxData.candidateParents,
		ProjectContext:   ctxData.sourceIssue.ExecutionContext,
	}
	if related, err := h.Queries.ListSubissuePlanContextIssues(ctx, db.ListSubissuePlanContextIssuesParams{
		WorkspaceID: issue.WorkspaceID,
		IssueID:     issue.ID,
	}); err == nil {
		if len(related) > 128 {
			related = related[:128]
		}
		snapshot.RelatedIssues = make([]service.SubissuePlanRelatedIssue, 0, len(related))
		prefix := h.getIssuePrefix(ctx, issue.WorkspaceID)
		for _, relatedIssue := range related {
			identifier := ""
			if prefix != "" {
				identifier = fmt.Sprintf("%s-%d", prefix, relatedIssue.Number)
			}
			snapshot.RelatedIssues = append(snapshot.RelatedIssues, service.SubissuePlanRelatedIssue{
				ID:          util.UUIDToString(relatedIssue.ID),
				Identifier:  identifier,
				Title:       relatedIssue.Title,
				Description: relatedIssue.Description.String,
				Status:      relatedIssue.Status,
				ParentID:    util.UUIDToString(relatedIssue.ParentIssueID),
				ProjectID:   util.UUIDToString(relatedIssue.ProjectID),
				Revision:    relatedIssue.Revision,
			})
		}
	}
	return snapshot
}

func (h *Handler) ensureFallbackProposal(r *http.Request, issue db.Issue, req SubissuePlanSourceRequest, content string) string {
	if req.ProposalID != nil && strings.TrimSpace(*req.ProposalID) != "" {
		proposal, ok := h.loadProposalForIssueSilently(r, issue, *req.ProposalID)
		if ok {
			return util.UUIDToString(proposal.ID)
		}
	}
	var questionID pgtype.UUID
	if req.CommentID != nil && strings.TrimSpace(*req.CommentID) != "" {
		questionID = parseUUID(*req.CommentID)
	}
	snapshot, err := json.Marshal(map[string]any{
		"title": issue.Title, "summary": content, "scope": "fallback/source", "items": []any{},
	})
	if err != nil {
		return ""
	}
	proposal, _, err := h.ensureDiscussionProposal(r, issue, questionID, pgtype.UUID{}, snapshot)
	if err != nil {
		return ""
	}
	return util.UUIDToString(proposal.ID)
}

func (h *Handler) loadProposalForIssueSilently(r *http.Request, issue db.Issue, raw string) (db.DiscussionProposal, bool) {
	id, err := util.ParseUUID(strings.TrimSpace(raw))
	if err != nil {
		return db.DiscussionProposal{}, false
	}
	proposal, err := h.Queries.GetDiscussionProposal(r.Context(), db.GetDiscussionProposalParams{
		WorkspaceID: issue.WorkspaceID,
		ID:          id,
	})
	return proposal, err == nil && proposal.SourceIssueID == issue.ID
}

func (h *Handler) subissuePlanPreviewResponse(
	r *http.Request,
	issue db.Issue,
	req SubissuePlanSourceRequest,
	plans []service.SubissuePlan,
	diagnostics []service.SubissuePlanDiagnostic,
	status string,
	proposalID string,
) SuggestSubissuePlansResponse {
	prefix := h.getIssuePrefix(r.Context(), issue.WorkspaceID)
	var inherited *service.ResolvedExecutionContext
	resolved := h.resolveInheritedExecutionContext(r, issue, prefix)
	if !resolved.Empty() {
		inherited = &resolved
	}
	var refs []service.AncestorBriefRef
	if req.CarryAncestorContext == nil || *req.CarryAncestorContext {
		refs = service.BuildAncestorBrief(r.Context(), h.Queries, issue).Refs
	}
	return SuggestSubissuePlansResponse{
		Plans: plans, Diagnostics: diagnostics,
		Summary:           service.SubissuePlanGenerationSummary{Status: status},
		AncestorBriefRefs: refs, InheritedExecutionContext: inherited,
		ProposalID: proposalID,
	}
}

func acceptedSubissuePlanJob(job db.SubissuePlanJob) subissuePlanJobAcceptedResponse {
	view := subissuePlanJobToResponse(job)
	diagnostic := jobDiagnostic(job, view.Source, "")
	return subissuePlanJobAcceptedResponse{
		Job: view, JobID: view.ID, Status: view.Status, Source: view.Source,
		FallbackReason: view.FallbackReason,
		Diagnostics:    []service.SubissuePlanDiagnostic{diagnostic},
		Summary:        service.SubissuePlanGenerationSummary{Status: "running"},
	}
}

func subissuePlanJobToResponse(job db.SubissuePlanJob) SubissuePlanJobResponse {
	proposalID := ""
	var snapshot service.SubissuePlanFallbackSnapshot
	if json.Unmarshal(job.SourceSnapshot, &snapshot) == nil {
		proposalID = snapshot.ProposalID
	}
	return SubissuePlanJobResponse{
		ID: util.UUIDToString(job.ID), IssueID: util.UUIDToString(job.IssueID),
		SourceCommentID:      util.UUIDToString(job.SourceCommentID),
		SourceSnapshotDigest: job.SourceSnapshotDigest, ContextRevision: job.ContextRevision,
		Status: job.Status, Source: jobSource(job), FallbackReason: job.FallbackReason,
		PrimaryErrorClass: job.PrimaryErrorClass, AgentID: util.UUIDToString(job.AgentID),
		TaskID: util.UUIDToString(job.TaskID), Plans: plansFromSubissuePlanJob(job),
		ErrorSummary: job.ErrorSummary.String, ProposalID: proposalID,
		CreatedAt: timestampToString(job.CreatedAt), UpdatedAt: timestampToString(job.UpdatedAt),
		StartedAt: timestampToString(job.StartedAt), CompletedAt: timestampToString(job.CompletedAt),
		ExpiresAt: timestampToString(job.ExpiresAt),
	}
}

func plansFromSubissuePlanJob(job db.SubissuePlanJob) []service.SubissuePlan {
	if len(job.Result) == 0 {
		return nil
	}
	var result struct {
		Plans []service.SubissuePlan `json:"plans"`
	}
	if json.Unmarshal(job.Result, &result) != nil {
		return nil
	}
	return result.Plans
}

func jobSource(job db.SubissuePlanJob) string {
	if job.AgentID.Valid || job.TaskID.Valid {
		return service.SubissuePlanSourceFallback
	}
	return service.SubissuePlanSourceRule
}

func jobDiagnostic(job db.SubissuePlanJob, source, reason string) service.SubissuePlanDiagnostic {
	if reason == "" && source == service.SubissuePlanSourceRule {
		reason = service.SubissuePlanNoCandidate
	}
	return service.SubissuePlanDiagnostic{
		PlanID: "plan-fallback", Strategy: "fallback", Name: "降级规划方案",
		Source: source, FallbackReason: reason, AgentID: util.UUIDToString(job.AgentID),
		TaskID: util.UUIDToString(job.TaskID), Status: job.Status,
	}
}

func proposalIDFromJob(job db.SubissuePlanJob, fallback string) string {
	var snapshot service.SubissuePlanFallbackSnapshot
	if json.Unmarshal(job.SourceSnapshot, &snapshot) == nil && snapshot.ProposalID != "" {
		return snapshot.ProposalID
	}
	return fallback
}

func jobWithTask(job db.SubissuePlanJob, agentID pgtype.UUID, task db.AgentTaskQueue) db.SubissuePlanJob {
	job.AgentID = agentID
	job.TaskID = task.ID
	job.Status = "queued"
	return job
}

func snapshotDigest(snapshot service.SubissuePlanFallbackSnapshot) string {
	digest, _, err := service.DigestSubissuePlanSnapshot(snapshot)
	if err != nil {
		return ""
	}
	return digest
}

func snapshotRevision(snapshot service.SubissuePlanFallbackSnapshot, fallback int64) int64 {
	if snapshot.Version > 0 {
		return fallback
	}
	return fallback
}
