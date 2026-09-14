package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/issueguard"
	"github.com/multica-ai/multica/server/internal/issueposition"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
)

var ErrProposalBatchAlreadyExists = errors.New("proposal generation already exists")
var ErrProposalAssigneeNotFound = errors.New("proposal assignee is not in this workspace")

// ProposalBatchItem is the validated, transport-independent part of a
// proposal preview. Parent/project/assignee IDs are resolved by the handler
// before this service is called.
type ProposalBatchItem struct {
	ID               string
	Title            string
	Description      string
	ParentIssueID    pgtype.UUID
	ProjectID        pgtype.UUID
	AssigneeType     pgtype.Text
	AssigneeID       pgtype.UUID
	Stage            pgtype.Int4
	ExecutionContext []byte
}

type ProposalBatchParams struct {
	WorkspaceID           pgtype.UUID
	CreatorID             pgtype.UUID
	ProposalID            pgtype.UUID
	Strategy              string
	CreateMode            string
	IdempotencyKey        string
	ProposalSnapshot      []byte
	SourceContextSnapshot []byte
	SourceIssueID         pgtype.UUID
	AnchorCommentID       pgtype.UUID
	Items                 []ProposalBatchItem
}

type ProposalBatchResult struct {
	Generation db.ProposalGeneration
	Issues     []db.Issue
	Links      []db.ProposalGenerationIssue
	Reused     bool
}

// CreateProposalBatch writes the proposal generation, all children, source
// snapshots, and association rows in one transaction. Agent enqueueing is
// deliberately after Commit so a failed batch can never leave an orphan run.
func (s *IssueService) CreateProposalBatch(ctx context.Context, p ProposalBatchParams) (ProposalBatchResult, error) {
	if s == nil || s.Queries == nil || s.TxStarter == nil {
		return ProposalBatchResult{}, errors.New("proposal batch service is unavailable")
	}
	if len(p.Items) == 0 {
		return ProposalBatchResult{}, errors.New("proposal batch must contain at least one item")
	}
	if p.CreateMode == "" {
		p.CreateMode = "backlog"
	}
	if p.CreateMode != "backlog" && p.CreateMode != "todo" {
		return ProposalBatchResult{}, errors.New("create_mode must be backlog or todo")
	}
	if strings.TrimSpace(p.Strategy) == "" {
		p.Strategy = "manual"
	}
	if p.ProposalID.Valid == false {
		return ProposalBatchResult{}, errors.New("proposal_id is required")
	}

	if p.IdempotencyKey != "" {
		existing, err := s.Queries.GetProposalGenerationByIdempotency(ctx, db.GetProposalGenerationByIdempotencyParams{
			WorkspaceID: p.WorkspaceID, ProposalID: p.ProposalID, IdempotencyKey: pgtype.Text{String: p.IdempotencyKey, Valid: true},
		})
		if err == nil {
			links, linkErr := s.Queries.ListProposalGenerationIssues(ctx, db.ListProposalGenerationIssuesParams{
				WorkspaceID: p.WorkspaceID, GenerationID: existing.ID,
			})
			if linkErr != nil {
				return ProposalBatchResult{}, fmt.Errorf("load existing proposal generation issues: %w", linkErr)
			}
			issues := make([]db.Issue, 0, len(links))
			for _, link := range links {
				issue, issueErr := s.Queries.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{ID: link.IssueID, WorkspaceID: p.WorkspaceID})
				if issueErr != nil {
					return ProposalBatchResult{}, fmt.Errorf("load existing proposal child: %w", issueErr)
				}
				issues = append(issues, issue)
			}
			return ProposalBatchResult{Generation: existing, Issues: issues, Links: links, Reused: true}, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return ProposalBatchResult{}, fmt.Errorf("check proposal generation idempotency: %w", err)
		}
	}

	tx, err := s.TxStarter.Begin(ctx)
	if err != nil {
		return ProposalBatchResult{}, fmt.Errorf("begin proposal batch: %w", err)
	}
	defer tx.Rollback(ctx)
	qtx := s.Queries.WithTx(tx)

	proposal, err := qtx.GetDiscussionProposal(ctx, db.GetDiscussionProposalParams{WorkspaceID: p.WorkspaceID, ID: p.ProposalID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ProposalBatchResult{}, errors.New("proposal not found in this workspace")
		}
		return ProposalBatchResult{}, fmt.Errorf("load proposal: %w", err)
	}
	if proposal.SourceIssueID.Valid == false || proposal.SourceIssueID != p.SourceIssueID {
		return ProposalBatchResult{}, errors.New("proposal source issue does not match the request")
	}

	if p.IdempotencyKey != "" {
		existing, existingErr := qtx.GetProposalGenerationByIdempotency(ctx, db.GetProposalGenerationByIdempotencyParams{
			WorkspaceID: p.WorkspaceID, ProposalID: p.ProposalID, IdempotencyKey: pgtype.Text{String: p.IdempotencyKey, Valid: true},
		})
		if existingErr == nil {
			links, linkErr := qtx.ListProposalGenerationIssues(ctx, db.ListProposalGenerationIssuesParams{
				WorkspaceID: p.WorkspaceID, GenerationID: existing.ID,
			})
			if linkErr != nil {
				return ProposalBatchResult{}, fmt.Errorf("load raced proposal generation issues: %w", linkErr)
			}
			issues := make([]db.Issue, 0, len(links))
			for _, link := range links {
				issue, issueErr := qtx.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{ID: link.IssueID, WorkspaceID: p.WorkspaceID})
				if issueErr != nil {
					return ProposalBatchResult{}, fmt.Errorf("load raced proposal child: %w", issueErr)
				}
				issues = append(issues, issue)
			}
			return ProposalBatchResult{Generation: existing, Issues: issues, Links: links, Reused: true}, nil
		}
		if !errors.Is(existingErr, pgx.ErrNoRows) {
			return ProposalBatchResult{}, fmt.Errorf("check proposal generation in transaction: %w", existingErr)
		}
	}

	policy := ResolveIssueCountPolicy(ctx, s.Entitlements, p.WorkspaceID)
	generation, err := qtx.CreateProposalGeneration(ctx, db.CreateProposalGenerationParams{
		ID: dbid.NewV7(), WorkspaceID: p.WorkspaceID, ProposalID: p.ProposalID,
		Strategy: p.Strategy, CreateMode: p.CreateMode,
		IdempotencyKey: nullableText(p.IdempotencyKey), CreatedBy: p.CreatorID,
		Snapshot: json.RawMessage(p.ProposalSnapshot),
	})
	if err != nil {
		return ProposalBatchResult{}, fmt.Errorf("create proposal generation: %w", err)
	}
	proposalContextSnapshot := proposalSourceContextSnapshot(p.SourceContextSnapshot, p.ProposalID, generation.ID, p.Strategy)

	result := ProposalBatchResult{Generation: generation, Issues: make([]db.Issue, 0, len(p.Items)), Links: make([]db.ProposalGenerationIssue, 0, len(p.Items))}
	status := p.CreateMode
	for _, item := range p.Items {
		title := strings.TrimSpace(item.Title)
		if title == "" {
			return ProposalBatchResult{}, errors.New("proposal item title is required")
		}
		projectID := item.ProjectID
		if item.ParentIssueID.Valid {
			parent, parentErr := qtx.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{ID: item.ParentIssueID, WorkspaceID: p.WorkspaceID})
			if parentErr != nil {
				return ProposalBatchResult{}, ErrParentIssueNotFound
			}
			if !projectID.Valid {
				projectID = parent.ProjectID
			}
		}
		if projectID.Valid {
			if _, projectErr := qtx.GetProjectInWorkspace(ctx, db.GetProjectInWorkspaceParams{ID: projectID, WorkspaceID: p.WorkspaceID}); projectErr != nil {
				return ProposalBatchResult{}, ErrProjectNotFound
			}
		}
		if item.AssigneeType.Valid {
			if !item.AssigneeID.Valid {
				return ProposalBatchResult{}, errors.New("proposal assignee id is required")
			}
			switch item.AssigneeType.String {
			case "agent":
				if _, assigneeErr := qtx.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{ID: item.AssigneeID, WorkspaceID: p.WorkspaceID}); assigneeErr != nil {
					return ProposalBatchResult{}, fmt.Errorf("%w: agent", ErrProposalAssigneeNotFound)
				}
			case "squad":
				if _, assigneeErr := qtx.GetSquadInWorkspace(ctx, db.GetSquadInWorkspaceParams{ID: item.AssigneeID, WorkspaceID: p.WorkspaceID}); assigneeErr != nil {
					return ProposalBatchResult{}, fmt.Errorf("%w: squad", ErrProposalAssigneeNotFound)
				}
			default:
				return ProposalBatchResult{}, errors.New("proposal assignee type must be agent or squad")
			}
		}
		duplicate, found, duplicateErr := issueguard.LockAndFindActiveDuplicate(ctx, qtx, p.WorkspaceID, projectID, item.ParentIssueID, title, false)
		if duplicateErr != nil {
			return ProposalBatchResult{}, fmt.Errorf("check proposal child duplicate: %w", duplicateErr)
		}
		if found {
			_ = duplicate
			return ProposalBatchResult{}, errors.New("proposal item would duplicate an active issue")
		}
		number, numberErr := AllocateIssueNumber(ctx, qtx, p.WorkspaceID, policy)
		if numberErr != nil {
			return ProposalBatchResult{}, numberErr
		}
		position, positionErr := issueposition.NextTopPosition(ctx, tx, p.WorkspaceID, status)
		if positionErr != nil {
			return ProposalBatchResult{}, positionErr
		}
		issue, createErr := qtx.CreateIssue(ctx, db.CreateIssueParams{
			WorkspaceID: p.WorkspaceID, Title: title, Description: nullableText(strings.TrimSpace(item.Description)),
			Status: status, Priority: "none", AssigneeType: item.AssigneeType, AssigneeID: item.AssigneeID,
			CreatorType: "member", CreatorID: p.CreatorID, ParentIssueID: item.ParentIssueID,
			Position: position, Number: number, ProjectID: projectID, Stage: item.Stage,
			ID: dbid.NewV7(),
		})
		if createErr != nil {
			return ProposalBatchResult{}, fmt.Errorf("create proposal child: %w", createErr)
		}
		if len(item.ExecutionContext) > 0 {
			updated, contextErr := qtx.SetIssueExecutionContext(ctx, db.SetIssueExecutionContextParams{
				ExecutionContext: item.ExecutionContext, ID: issue.ID, WorkspaceID: p.WorkspaceID,
			})
			if contextErr != nil {
				return ProposalBatchResult{}, fmt.Errorf("persist proposal child execution context: %w", contextErr)
			}
			issue = updated
		}
		if len(proposalContextSnapshot) > 0 {
			_, contextErr := qtx.CreateIssueSourceContext(ctx, db.CreateIssueSourceContextParams{
				ID: dbid.NewV7(), WorkspaceID: p.WorkspaceID, IssueID: issue.ID,
				SourceIssueID: p.SourceIssueID, AnchorCommentID: p.AnchorCommentID,
				CapturedByUserID: p.CreatorID, SnapshotVersion: 1, Snapshot: proposalContextSnapshot,
				CaptureDigest: "proposal:" + uuidText(p.ProposalID), State: "attached",
				CapturedAt: pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true},
				AttachedAt: pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true},
			})
			if contextErr != nil {
				return ProposalBatchResult{}, fmt.Errorf("persist proposal source context: %w", contextErr)
			}
		}
		link, linkErr := qtx.CreateProposalGenerationIssue(ctx, db.CreateProposalGenerationIssueParams{
			ID: dbid.NewV7(), WorkspaceID: p.WorkspaceID, GenerationID: generation.ID,
			ProposalID: p.ProposalID, IssueID: issue.ID, ItemID: item.ID, Stage: item.Stage,
		})
		if linkErr != nil {
			return ProposalBatchResult{}, fmt.Errorf("link proposal child: %w", linkErr)
		}
		result.Issues = append(result.Issues, issue)
		result.Links = append(result.Links, link)
	}
	if err := tx.Commit(ctx); err != nil {
		return ProposalBatchResult{}, fmt.Errorf("commit proposal batch: %w", err)
	}

	for _, issue := range result.Issues {
		// A backlog batch has no side effects. An explicit todo batch may start
		// work only after the transaction has committed.
		if p.CreateMode == "todo" && issue.AssigneeType.Valid && issue.AssigneeID.Valid {
			_ = s.maybeEnqueueOnAssign(ctx, issue, "member", util.UUIDToString(p.CreatorID), time.Time{})
		}
		if s.Bus != nil {
			s.publishIssueCreated(issue, nil, nil, "member", util.UUIDToString(p.CreatorID), IssueCreateOpts{ActorID: util.UUIDToString(p.CreatorID)})
		}
	}
	return result, nil
}

func nullableText(value string) pgtype.Text {
	if value == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: value, Valid: true}
}

func uuidText(id pgtype.UUID) string {
	return util.UUIDToString(id)
}

func proposalSourceContextSnapshot(raw []byte, proposalID, generationID pgtype.UUID, strategy string) []byte {
	if len(raw) == 0 {
		return nil
	}
	var snapshot map[string]any
	if err := json.Unmarshal(raw, &snapshot); err != nil || snapshot == nil {
		return raw
	}
	snapshot["proposal_id"] = uuidText(proposalID)
	snapshot["generation_id"] = uuidText(generationID)
	snapshot["proposal_strategy"] = strategy
	decorated, err := json.Marshal(snapshot)
	if err != nil {
		return raw
	}
	return decorated
}
