package service

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type ancestorBriefReader struct {
	issues map[string]db.Issue
}

func (r ancestorBriefReader) GetIssue(_ context.Context, id pgtype.UUID) (db.Issue, error) {
	return r.issues[util.UUIDToString(id)], nil
}

func briefIssue(id, title, description string, parent pgtype.UUID) db.Issue {
	return db.Issue{
		ID:            idFromTest(id),
		WorkspaceID:   idFromTest("00000000-0000-0000-0000-000000000001"),
		Title:         title,
		Description:   pgtype.Text{String: description, Valid: true},
		ParentIssueID: parent,
	}
}

func idFromTest(value string) pgtype.UUID {
	id, err := util.ParseUUID(value)
	if err != nil {
		panic(err)
	}
	return id
}

func TestBuildAncestorBriefWalksNearestToRoot(t *testing.T) {
	root := idFromTest("00000000-0000-0000-0000-000000000002")
	parent := idFromTest("00000000-0000-0000-0000-000000000003")
	workspace := idFromTest("00000000-0000-0000-0000-000000000001")
	reader := ancestorBriefReader{issues: map[string]db.Issue{
		util.UUIDToString(parent): {ID: parent, WorkspaceID: workspace, Title: "B", Description: pgtype.Text{String: "B markdown", Valid: true}, ParentIssueID: root},
		util.UUIDToString(root):   {ID: root, WorkspaceID: workspace, Title: "A", Description: pgtype.Text{String: "A markdown", Valid: true}},
	}}
	brief := BuildAncestorBrief(context.Background(), reader, db.Issue{WorkspaceID: workspace, ParentIssueID: parent})
	if !containsInOrder(brief.Text, "Title: B", "Title: A") {
		t.Fatalf("brief order = %q", brief.Text)
	}
	if len(brief.Refs) != 2 || brief.TokenCount == 0 {
		t.Fatalf("brief refs/tokens = %+v", brief)
	}
}

func containsInOrder(value string, parts ...string) bool {
	last := -1
	for _, part := range parts {
		index := indexAfter(value, part, last+1)
		if index < 0 {
			return false
		}
		last = index
	}
	return true
}

func indexAfter(value, part string, start int) int {
	for index := start; index+len(part) <= len(value); index++ {
		if value[index:index+len(part)] == part {
			return index
		}
	}
	return -1
}
