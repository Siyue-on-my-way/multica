package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/util"
)

// TestCreateCommentProposalViaRoute drives CreateCommentProposal through the
// production route shape (`/api/comments/{commentId}/proposal`). The URL param
// is named commentId in the router; a handler reading a different param name
// fails with 400 before reaching any business logic, so mounting the real
// pattern is what pins this regression (SIY-182).
func TestCreateCommentProposalViaRoute(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()

	number := int(time.Now().UnixNano()%100000) + 9_200_000
	var sourceIssueID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO issue (workspace_id, title, description, creator_type, creator_id, number)
		VALUES ($1, 'proposal route issue', 'issue body', 'member', $2, $3)
		RETURNING id
	`, testWorkspaceID, testUserID, number).Scan(&sourceIssueID); err != nil {
		t.Fatalf("insert issue: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM discussion_proposal WHERE source_issue_id = $1`, sourceIssueID)
		testPool.Exec(context.Background(), `DELETE FROM comment WHERE issue_id = $1`, sourceIssueID)
		testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, sourceIssueID)
	})

	insertComment := func(authorType, content string, parent any) string {
		t.Helper()
		var id string
		if err := testPool.QueryRow(ctx, `
			INSERT INTO comment (issue_id, workspace_id, author_type, author_id, content, parent_id)
			VALUES ($1, $2, $3, $4, $5, $6)
			RETURNING id
		`, sourceIssueID, testWorkspaceID, authorType, testUserID, content, parent).Scan(&id); err != nil {
			t.Fatalf("insert comment: %v", err)
		}
		return id
	}
	questionID := insertComment("member", "question about the approach", nil)
	replyID := insertComment("agent", "draft answer that should become a proposal", questionID)

	router := chi.NewRouter()
	router.Route("/api/comments/{commentId}", func(r chi.Router) {
		r.Post("/proposal", testHandler.CreateCommentProposal)
	})

	post := func(commentID string) *httptest.ResponseRecorder {
		t.Helper()
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, newRequest(http.MethodPost, "/api/comments/"+commentID+"/proposal", map[string]any{}))
		return recorder
	}

	recorder := post(replyID)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("create proposal = %d: %s", recorder.Code, recorder.Body.String())
	}
	var proposal DiscussionProposalResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &proposal); err != nil {
		t.Fatalf("decode proposal response: %v", err)
	}
	if proposal.SourceIssueID != sourceIssueID {
		t.Fatalf("proposal source_issue_id = %s, want %s", proposal.SourceIssueID, sourceIssueID)
	}
	if proposal.AnswerCommentID == nil || *proposal.AnswerCommentID != replyID {
		t.Fatalf("proposal answer_comment_id = %v, want %s", proposal.AnswerCommentID, replyID)
	}
	if proposal.QuestionCommentID == nil || *proposal.QuestionCommentID != questionID {
		t.Fatalf("proposal question_comment_id = %v, want %s (parent of the reply)", proposal.QuestionCommentID, questionID)
	}
	var snapshot map[string]any
	if err := json.Unmarshal(proposal.Snapshot, &snapshot); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	if snapshot["summary"] != "draft answer that should become a proposal" {
		t.Fatalf("snapshot summary = %v, want the comment content fallback", snapshot["summary"])
	}

	// A second click on the same comment reuses the proposal (200) instead of
	// duplicating it.
	repeat := post(replyID)
	if repeat.Code != http.StatusOK {
		t.Fatalf("repeat create = %d: %s", repeat.Code, repeat.Body.String())
	}
	var reused DiscussionProposalResponse
	if err := json.Unmarshal(repeat.Body.Bytes(), &reused); err != nil {
		t.Fatalf("decode reused response: %v", err)
	}
	if reused.ID != proposal.ID {
		t.Fatalf("repeat created a second proposal %s, want %s", reused.ID, proposal.ID)
	}
	_ = util.MustParseUUID(proposal.ID) // response IDs must be well-formed UUIDs
}
