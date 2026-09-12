package service

import (
	"context"
	"strings"
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

func TestStripRedundantAncestorBrief(t *testing.T) {
	rootID := "00000000-0000-0000-0000-000000000002"
	embedded := AncestorBriefMarker + "\n\n[Background source: Issue " + rootID + "]\nTitle: A\nDescription:\nA markdown"
	description := "child task body\n\n" + embedded

	// Covered: the live walk reached every referenced issue — drop the copy.
	covered := AncestorBriefCoveredIDs([]AncestorBriefRef{{ID: rootID}}, "00000000-0000-0000-0000-000000000004")
	if got := StripRedundantAncestorBrief(description, covered); got != "child task body" {
		t.Fatalf("covered strip = %q", got)
	}
	// Uncovered: a broken chain makes the embedded copy the only source — keep it.
	if got := StripRedundantAncestorBrief(description, map[string]struct{}{}); !strings.Contains(got, "Title: A") {
		t.Fatalf("uncovered block must be kept, got %q", got)
	}
	// No marker: untouched.
	if got := StripRedundantAncestorBrief("plain description", covered); got != "plain description" {
		t.Fatalf("plain description mutated: %q", got)
	}
}

// TestBuildAncestorBriefDedupesEmbeddedCopies covers the SIY-177 regression:
// a child created by the old flow carries its parent's full text inside an
// embedded ANCESTOR_BRIEF block, and the walk must not deliver that text
// twice.
func TestBuildAncestorBriefDedupesEmbeddedCopies(t *testing.T) {
	root := idFromTest("00000000-0000-0000-0000-000000000002")
	parent := idFromTest("00000000-0000-0000-0000-000000000003")
	workspace := idFromTest("00000000-0000-0000-0000-000000000001")

	embedded := AncestorBriefMarker + "\n\n[Background source: Issue " + util.UUIDToString(root) + "]\nTitle: A\nDescription:\nA markdown"
	parentDescription := "child task body\n\n" + embedded

	reader := ancestorBriefReader{issues: map[string]db.Issue{
		util.UUIDToString(parent): {ID: parent, WorkspaceID: workspace, Title: "B", Description: pgtype.Text{String: parentDescription, Valid: true}, ParentIssueID: root},
		util.UUIDToString(root):   {ID: root, WorkspaceID: workspace, Title: "A", Description: pgtype.Text{String: "A markdown", Valid: true}},
	}}
	brief := BuildAncestorBrief(context.Background(), reader, db.Issue{WorkspaceID: workspace, ParentIssueID: parent})

	if strings.Count(brief.Text, "A markdown") != 1 {
		t.Fatalf("root description must appear exactly once: %q", brief.Text)
	}
	if strings.Count(brief.Text, AncestorBriefMarker) != 1 {
		t.Fatalf("exactly the brief's own header may carry the marker: %q", brief.Text)
	}
	if !containsInOrder(brief.Text, "Title: B", "Title: A") {
		t.Fatalf("brief order = %q", brief.Text)
	}
}
