package service

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type ProjectIssueAggregator struct {
	Queries *db.Queries
}

func (a *ProjectIssueAggregator) Aggregate(
	ctx context.Context,
	project db.Project,
	rangeStart time.Time,
	rangeEnd time.Time,
	asOf time.Time,
) (ReportSnapshot, error) {
	if !rangeStart.Before(rangeEnd) {
		return ReportSnapshot{}, fmt.Errorf("range start must be before range end")
	}

	workspace, err := a.Queries.GetWorkspace(ctx, project.WorkspaceID)
	if err != nil {
		return ReportSnapshot{}, fmt.Errorf("load issue identifier prefix: %w", err)
	}

	completedRows, err := a.Queries.ListIssuesCompletedForReport(ctx, db.ListIssuesCompletedForReportParams{
		ProjectID: project.ID,
		RangeStart: pgtype.Timestamptz{
			Time:  rangeStart,
			Valid: true,
		},
		RangeEnd: pgtype.Timestamptz{
			Time:  rangeEnd,
			Valid: true,
		},
	})
	if err != nil {
		return ReportSnapshot{}, fmt.Errorf("list completed issues: %w", err)
	}

	inProgressRows, err := a.listStatus(ctx, project.ID, "in_progress")
	if err != nil {
		return ReportSnapshot{}, err
	}
	blockedRows, err := a.listStatus(ctx, project.ID, "blocked")
	if err != nil {
		return ReportSnapshot{}, err
	}

	cancelledRows, err := a.Queries.ListIssuesCancelledForReport(ctx, db.ListIssuesCancelledForReportParams{
		ProjectID: project.ID,
		RangeStart: pgtype.Timestamptz{
			Time:  rangeStart,
			Valid: true,
		},
		RangeEnd: pgtype.Timestamptz{
			Time:  rangeEnd,
			Valid: true,
		},
	})
	if err != nil {
		return ReportSnapshot{}, fmt.Errorf("list cancelled issues: %w", err)
	}

	overdueRows, err := a.Queries.ListIssuesOverdueForReport(ctx, db.ListIssuesOverdueForReportParams{
		ProjectID: project.ID,
		DueDate: pgtype.Date{
			Time:  time.Date(asOf.Year(), asOf.Month(), asOf.Day(), 0, 0, 0, 0, asOf.Location()),
			Valid: true,
		},
	})
	if err != nil {
		return ReportSnapshot{}, fmt.Errorf("list overdue issues: %w", err)
	}

	completed := reportIssuesFromCompletedRows(completedRows, workspace.IssuePrefix)
	inProgress := reportIssuesFromStatusRows(inProgressRows, workspace.IssuePrefix)
	blocked := reportIssuesFromStatusRows(blockedRows, workspace.IssuePrefix)
	cancelled := reportIssuesFromCancelledRows(cancelledRows, workspace.IssuePrefix)
	overdue := reportIssuesFromOverdueRows(overdueRows, workspace.IssuePrefix)

	return ReportSnapshot{
		RangeStart:      rangeStart,
		RangeEnd:        rangeEnd,
		GeneratedAt:     asOf,
		Completed:       completed,
		InProgress:      inProgress,
		Blocked:         blocked,
		Overdue:         overdue,
		Cancelled:       cancelled,
		CompletedCount:  len(completed),
		InProgressCount: len(inProgress),
		BlockedCount:    len(blocked),
		OverdueCount:    len(overdue),
		CancelledCount:  len(cancelled),
	}, nil
}

func (a *ProjectIssueAggregator) listStatus(ctx context.Context, projectID pgtype.UUID, status string) ([]db.ListIssuesByStatusForReportRow, error) {
	rows, err := a.Queries.ListIssuesByStatusForReport(ctx, db.ListIssuesByStatusForReportParams{
		ProjectID: projectID,
		Status:    status,
	})
	if err != nil {
		return nil, fmt.Errorf("list %s issues: %w", status, err)
	}
	return rows, nil
}
