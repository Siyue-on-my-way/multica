package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
)

// ExtractDiscussionProposalSnapshot recognizes the explicit structured sidecar
// form emitted by proposal-capable agents. Ordinary Markdown and arbitrary JSON
// replies remain ordinary comments; a marker is required so a user's pasted
// JSON cannot unexpectedly become an execution plan.
func ExtractDiscussionProposalSnapshot(content string) (json.RawMessage, bool) {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return nil, false
	}
	candidate := ""
	for _, marker := range []string{"proposal-json", "proposal_json", "proposal"} {
		open := "```" + marker
		if start := strings.Index(strings.ToLower(trimmed), open); start >= 0 {
			bodyStart := strings.IndexByte(trimmed[start:], '\n')
			if bodyStart < 0 {
				continue
			}
			bodyStart += start + 1
			if end := strings.Index(trimmed[bodyStart:], "```"); end >= 0 {
				candidate = strings.TrimSpace(trimmed[bodyStart : bodyStart+end])
				break
			}
		}
	}
	if candidate == "" {
		const open, close = "<proposal-json>", "</proposal-json>"
		lower := strings.ToLower(trimmed)
		if start := strings.Index(lower, open); start >= 0 {
			bodyStart := start + len(open)
			if end := strings.Index(lower[bodyStart:], close); end >= 0 {
				candidate = strings.TrimSpace(trimmed[bodyStart : bodyStart+end])
			}
		}
	}
	if candidate == "" {
		return nil, false
	}
	var object map[string]any
	if err := json.Unmarshal([]byte(candidate), &object); err != nil || object == nil {
		return nil, false
	}
	if _, ok := object["acceptanceCriteria"]; !ok {
		if value, exists := object["acceptance_criteria"]; exists {
			object["acceptanceCriteria"] = value
		}
	}
	items, ok := object["items"]
	if !ok {
		return nil, false
	}
	if _, ok := items.([]any); !ok {
		return nil, false
	}
	if _, ok := object["title"]; !ok {
		object["title"] = "讨论方案"
	}
	if _, ok := object["summary"]; !ok {
		object["summary"] = ""
	}
	snapshot, err := json.Marshal(object)
	if err != nil {
		return nil, false
	}
	return snapshot, true
}

// MaybeCreateDiscussionProposal attaches a structured agent answer to its
// question comment. It is deliberately best-effort: comment delivery has
// already committed, and an invalid/unsupported sidecar must degrade to the
// ordinary comment renderer rather than fail the agent run.
func MaybeCreateDiscussionProposal(ctx context.Context, q *db.Queries, comment db.Comment) error {
	if q == nil || comment.AuthorType != "agent" {
		return nil
	}
	snapshot, ok := ExtractDiscussionProposalSnapshot(comment.Content)
	if !ok {
		return nil
	}
	questionID := comment.ID
	answerID := pgtype.UUID{}
	if comment.ParentID.Valid {
		questionID = comment.ParentID
		answerID = comment.ID
	}
	if _, err := q.GetDiscussionProposalByQuestion(ctx, db.GetDiscussionProposalByQuestionParams{
		WorkspaceID: comment.WorkspaceID, SourceIssueID: comment.IssueID, QuestionCommentID: questionID,
	}); err == nil {
		return nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	_, err := q.CreateDiscussionProposal(ctx, db.CreateDiscussionProposalParams{
		ID: dbid.NewV7(), WorkspaceID: comment.WorkspaceID, SourceIssueID: comment.IssueID,
		QuestionCommentID: questionID, AnswerCommentID: answerID, Snapshot: snapshot,
	})
	if err != nil {
		// A concurrent delivery or manual conversion may have won the partial
		// unique index. The comment itself remains valid, so treat that race as
		// success after confirming the row exists.
		if _, readErr := q.GetDiscussionProposalByQuestion(ctx, db.GetDiscussionProposalByQuestionParams{
			WorkspaceID: comment.WorkspaceID, SourceIssueID: comment.IssueID, QuestionCommentID: questionID,
		}); readErr == nil {
			return nil
		}
	}
	return err
}

func discussionProposalEventPayload(ctx context.Context, q *db.Queries, comment db.Comment) map[string]any {
	questionID := comment.ID
	if comment.ParentID.Valid {
		questionID = comment.ParentID
	}
	proposal, err := q.GetDiscussionProposalByQuestion(ctx, db.GetDiscussionProposalByQuestionParams{
		WorkspaceID: comment.WorkspaceID, SourceIssueID: comment.IssueID, QuestionCommentID: questionID,
	})
	if err != nil {
		return nil
	}
	payload := map[string]any{
		"id":              util.UUIDToString(proposal.ID),
		"workspace_id":    util.UUIDToString(proposal.WorkspaceID),
		"source_issue_id": util.UUIDToString(proposal.SourceIssueID),
		"snapshot":        json.RawMessage(proposal.Snapshot),
		"generations":     []any{},
		"created_at":      util.TimestampToString(proposal.CreatedAt),
		"updated_at":      util.TimestampToString(proposal.UpdatedAt),
	}
	// Optional comment anchors must stay absent when the database value is
	// NULL. Emitting an empty string here makes the WS sidecar look like it has
	// a comment id, and the client then cannot distinguish a legacy/manual
	// proposal from a proposal that can jump back to a concrete comment.
	if proposal.QuestionCommentID.Valid {
		payload["question_comment_id"] = util.UUIDToString(proposal.QuestionCommentID)
	}
	if proposal.AnswerCommentID.Valid {
		payload["answer_comment_id"] = util.UUIDToString(proposal.AnswerCommentID)
	}
	return payload
}
