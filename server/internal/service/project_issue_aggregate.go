package service

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
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

	rows, err := a.Queries.ListProjectReportTimeline(ctx, db.ListProjectReportTimelineParams{
		WorkspaceID: project.WorkspaceID,
		ProjectID:   project.ID,
		RangeStart:  pgtype.Timestamptz{Time: rangeStart, Valid: true},
		RangeEnd:    pgtype.Timestamptz{Time: rangeEnd, Valid: true},
	})
	if err != nil {
		return ReportSnapshot{}, fmt.Errorf("list project report timeline: %w", err)
	}

	issues := make([]ReportIssue, 0)
	issueIndexes := make(map[string]int)
	for _, row := range rows {
		issueID := util.UUIDToString(row.IssueID)
		index, exists := issueIndexes[issueID]
		if !exists {
			index = len(issues)
			issueIndexes[issueID] = index
			issues = append(issues, ReportIssue{
				IssueID:        issueID,
				Identifier:     fmt.Sprintf("%s-%d", row.IssuePrefix, row.Number),
				Title:          row.Title,
				Description:    row.Description,
				BusinessDomain: inferReportBusinessDomain(row.Title, row.Description),
				Status:         row.Status,
				DueDate:        reportDateString(row.DueDate),
				Timeline:       make([]ReportTimelineEvent, 0, 8),
			})
		}
		issues[index].Timeline = append(issues[index].Timeline, reportTimelineEvent(row))
	}

	// The status buckets below are an intentionally separate, batched read for
	// compatibility with existing report-history consumers. They do not expand
	// the issue-centered Issues set: an issue is active only when the timeline
	// union above found a status change, comment, or task record in the window.
	currentRows, err := a.Queries.ListCurrentProjectIssueStatesForReport(ctx, db.ListCurrentProjectIssueStatesForReportParams{
		WorkspaceID: project.WorkspaceID,
		ProjectID:   project.ID,
	})
	if err != nil {
		return ReportSnapshot{}, fmt.Errorf("list current project issue states: %w", err)
	}

	completedIDs := make(map[string]struct{})
	cancelledIDs := make(map[string]struct{})
	for _, issue := range issues {
		for _, event := range issue.Timeline {
			if !event.InRange || event.Type != "issue_status_history" {
				continue
			}
			var details struct {
				To string `json:"to_status"`
			}
			if json.Unmarshal(event.Details, &details) != nil {
				continue
			}
			switch details.To {
			case "done":
				completedIDs[issue.IssueID] = struct{}{}
			case "cancelled":
				cancelledIDs[issue.IssueID] = struct{}{}
			}
		}
	}

	completed := make([]ReportIssue, 0)
	inProgress := make([]ReportIssue, 0)
	blocked := make([]ReportIssue, 0)
	overdue := make([]ReportIssue, 0)
	cancelled := make([]ReportIssue, 0)
	asOfDate := time.Date(asOf.In(asOf.Location()).Year(), asOf.In(asOf.Location()).Month(), asOf.In(asOf.Location()).Day(), 0, 0, 0, 0, asOf.Location())
	for _, row := range currentRows {
		issueID := util.UUIDToString(row.ID)
		ref := ReportIssue{
			IssueID:    issueID,
			Identifier: fmt.Sprintf("%s-%d", row.IssuePrefix, row.Number),
			Title:      row.Title,
			Status:     row.Status,
			DueDate:    reportDateString(row.DueDate),
		}
		switch row.Status {
		case "in_progress":
			inProgress = append(inProgress, ref)
		case "blocked":
			blocked = append(blocked, ref)
		case "cancelled":
			if _, ok := cancelledIDs[issueID]; ok {
				cancelled = append(cancelled, ref)
			}
		case "done":
			if _, ok := completedIDs[issueID]; ok {
				completed = append(completed, ref)
			}
		}
		if row.DueDate.Valid && row.Status != "done" && row.Status != "cancelled" {
			dueDate := row.DueDate.Time
			dueDate = time.Date(dueDate.Year(), dueDate.Month(), dueDate.Day(), 0, 0, 0, 0, asOf.Location())
			if dueDate.Before(asOfDate) {
				overdue = append(overdue, ref)
			}
		}
	}

	return ReportSnapshot{
		RangeStart:       rangeStart,
		RangeEnd:         rangeEnd,
		GeneratedAt:      asOf,
		SummaryVersion:   reportSummaryVersion,
		Issues:           issues,
		ActiveIssueCount: len(issues),
		Completed:        completed,
		InProgress:       inProgress,
		Blocked:          blocked,
		Overdue:          overdue,
		Cancelled:        cancelled,
		CompletedCount:   len(completed),
		InProgressCount:  len(inProgress),
		BlockedCount:     len(blocked),
		OverdueCount:     len(overdue),
		CancelledCount:   len(cancelled),
	}, nil
}

func reportTimelineEvent(row db.ListProjectReportTimelineRow) ReportTimelineEvent {
	details := json.RawMessage(row.Details)
	if len(details) == 0 {
		details = json.RawMessage(`{}`)
	}
	return ReportTimelineEvent{
		ID:          row.EventID,
		Type:        row.EventType,
		OccurredAt:  row.OccurredAt.Time,
		InRange:     row.InRange,
		AuthorType:  row.ActorType,
		AuthorID:    row.ActorID,
		Content:     row.Content,
		CommentType: row.CommentType,
		ParentID:    row.ParentID,
		Action:      row.Action,
		Details:     details,
	}
}

func reportDateString(value pgtype.Date) string {
	if !value.Valid {
		return ""
	}
	return value.Time.Format("2006-01-02")
}
