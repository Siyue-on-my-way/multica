package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func execContextIssue(id, title string, parent pgtype.UUID, raw []byte) db.Issue {
	return db.Issue{
		ID:                idFromTest(id),
		WorkspaceID:       idFromTest("00000000-0000-0000-0000-000000000001"),
		Title:             title,
		ParentIssueID:     parent,
		ExecutionContext:  raw,
		UpdatedAt:         pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true},
	}
}

func mustMarshal(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return encoded
}

func TestParseExecutionContextDefaultsAndClamps(t *testing.T) {
	if got := ParseExecutionContext(nil); got.Inherit != true || len(got.Entries) != 0 {
		t.Fatalf("NULL parses to inherit-default, got %+v", got)
	}
	if got := ParseExecutionContext([]byte("null")); got.Inherit != true {
		t.Fatalf("null literal parses to inherit-default, got %+v", got)
	}
	if got := ParseExecutionContext([]byte("not json")); got.Inherit != true {
		t.Fatalf("garbage parses to inherit-default, got %+v", got)
	}

	// inherit explicitly recorded wins over the default.
	doc := ParseExecutionContext(mustMarshal(t, ExecutionContext{
		Version: 3, Inherit: false,
		Entries: []ExecutionContextEntry{{Kind: "repo", Value: "https://example.com/x"}, {Kind: "bogus", Value: "  "}},
	}))
	if doc.Version != 3 || doc.Inherit {
		t.Fatalf("version/inherit lost: %+v", doc)
	}
	// Entries without a value are dropped; unknown kinds collapse to constraint.
	if len(doc.Entries) != 1 || doc.Entries[0].Kind != ExecutionContextKindRepo {
		t.Fatalf("entries normalized wrong: %+v", doc.Entries)
	}
}

func TestNormalizeExecutionContextForWriteStampsVersion(t *testing.T) {
	encoded, doc, err := NormalizeExecutionContextForWrite([]byte(`{"version":99,"inherit":false,"entries":[{"kind":"command","value":"./restart.sh"}]}`), 1)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if doc.Version != 1 {
		t.Fatalf("client version must not win, got %d", doc.Version)
	}
	var roundTrip ExecutionContext
	if err := json.Unmarshal(encoded, &roundTrip); err != nil {
		t.Fatalf("round trip: %v", err)
	}
	if roundTrip.Version != 1 || roundTrip.Inherit || len(roundTrip.Entries) != 1 {
		t.Fatalf("round trip = %+v", roundTrip)
	}
	if _, _, err := NormalizeExecutionContextForWrite([]byte("[1,2]"), 1); err == nil {
		t.Fatalf("non-object document must be rejected")
	}
}

func TestExecutionContextEntryForOS(t *testing.T) {
	linuxDir := ExecutionContextEntry{Kind: ExecutionContextKindCodeDir, Value: "/mnt/a", OS: "linux"}
	if !ExecutionContextEntryForOS(linuxDir, "linux") {
		t.Fatalf("linux entry must apply on linux")
	}
	if ExecutionContextEntryForOS(linuxDir, "windows") {
		t.Fatalf("linux entry must never apply on windows")
	}
	if ExecutionContextEntryForOS(ExecutionContextEntry{Kind: "command", Value: "make", OS: "openvms"}, "linux") {
		t.Fatalf("unknown OS tag must fail closed")
	}
	// darwin folds into macos at parse time.
	parsed := ParseExecutionContext(mustMarshal(t, ExecutionContext{
		Entries: []ExecutionContextEntry{{Kind: ExecutionContextKindCodeDir, Value: "/Users/x", OS: "darwin"}},
	}))
	if parsed.Entries[0].OS != "macos" {
		t.Fatalf("darwin should normalize to macos, got %q", parsed.Entries[0].OS)
	}
}

// TestResolveExecutionContextChainAncestorsToGrandchild is the ancestor-chain
// integration test (SIY-177 acceptance 3): root → child → grandchild, with
// the repo URL re-asserted verbatim by the child, a linux-only code directory,
// and the grandchild overriding the test command. The merged result must
// carry each distinct value exactly once with honest provenance.
func TestResolveExecutionContextChainAncestorsToGrandchild(t *testing.T) {
	repo := ExecutionContextEntry{Kind: ExecutionContextKindRepo, Value: "https://github.com/acme/widget"}
	linuxDir := ExecutionContextEntry{Kind: ExecutionContextKindCodeDir, Label: "checkout", Value: "/mnt/a/widget", OS: "linux"}
	restart := ExecutionContextEntry{Kind: ExecutionContextKindCommand, Label: "restart", Value: "./restart.sh"}

	root := ExecutionContext{Version: 1, Inherit: true, Entries: []ExecutionContextEntry{repo, linuxDir, restart}}
	child := ExecutionContext{Version: 1, Inherit: true, Entries: []ExecutionContextEntry{repo}}
	grandchild := ExecutionContext{Version: 2, Inherit: false, Entries: []ExecutionContextEntry{
		{Kind: ExecutionContextKindCommand, Label: "restart", Value: "npm run dev"},
	}}

	nodes := []ExecutionContextChainNode{
		{IssueID: "root", Identifier: "SIY-1", Context: root, Apply: true},
		{IssueID: "child", Identifier: "SIY-2", Context: child, Apply: true},
		{IssueID: "grandchild", Identifier: "SIY-3", Context: grandchild, Apply: true},
	}
	resolved := ResolveExecutionContextChain(nodes)

	if got := len(resolved.Entries); got != 3 {
		t.Fatalf("expected 3 distinct entries (repo deduped, override in place), got %d: %+v", got, resolved.Entries)
	}
	repoCount := 0
	for _, entry := range resolved.Entries {
		if entry.Value == repo.Value {
			repoCount++
		}
	}
	if repoCount != 1 {
		t.Fatalf("repo URL must appear exactly once, got %d", repoCount)
	}
	// The re-asserted repo keeps the root-most provenance.
	if resolved.Entries[0].SourceIssueID != "root" {
		t.Fatalf("repo provenance = %q, want root", resolved.Entries[0].SourceIssueID)
	}
	// The grandchild override moves the provenance and replaces the value.
	last := resolved.Entries[len(resolved.Entries)-1]
	if last.Value != "npm run dev" || last.SourceIssueID != "grandchild" {
		t.Fatalf("override lost: %+v (sources %+v)", last, resolved.Sources)
	}
	// Sources carry the version the content came from.
	sawGrandchildVersion := false
	for _, source := range resolved.Sources {
		if source.IssueID == "grandchild" {
			sawGrandchildVersion = source.Version == 2
		}
	}
	if !sawGrandchildVersion {
		t.Fatalf("grandchild source with version 2 missing: %+v", resolved.Sources)
	}

	// OS-tagged entries keep their scope on the wire; the runtime filter drops
	// the linux directory when resolving for a windows machine.
	kept := 0
	for _, entry := range resolved.Entries {
		if ExecutionContextEntryForOS(entry.ExecutionContextEntry, "windows") {
			kept++
		}
	}
	if kept != 2 {
		t.Fatalf("windows resolution must drop the linux code_dir, kept %d of %+v", kept, resolved.Entries)
	}
}

// TestResolveInheritedExecutionContextForIssueWalksChain drives the full
// resolver over a fake reader: inheritance switches, tenant checks and the
// current issue's unconditional self-contribution all in one pass.
func TestResolveInheritedExecutionContextForIssueWalksChain(t *testing.T) {
	workspace := idFromTest("00000000-0000-0000-0000-000000000001")
	rootParent := pgtype.UUID{}

	rootRaw := mustMarshal(t, ExecutionContext{
		Version: 1, Inherit: true,
		Entries: []ExecutionContextEntry{{Kind: ExecutionContextKindRepo, Value: "https://github.com/acme/widget"}},
	})
	// The middle issue opted OUT of inheritance: its entries must not reach
	// the grandchild's resolution.
	middleRaw := mustMarshal(t, ExecutionContext{
		Version: 1, Inherit: false,
		Entries: []ExecutionContextEntry{{Kind: ExecutionContextKindConstraint, Value: "deploy only on Fridays"}},
	})
	leafRaw := mustMarshal(t, ExecutionContext{
		Version: 4, Inherit: true,
		Entries: []ExecutionContextEntry{{Kind: ExecutionContextKindCommand, Value: "./restart.sh"}},
	})

	root := execContextIssue("00000000-0000-0000-0000-000000000002", "Root", rootParent, rootRaw)
	root.WorkspaceID = workspace
	middle := execContextIssue("00000000-0000-0000-0000-000000000003", "Middle", root.ID, middleRaw)
	middle.WorkspaceID = workspace
	leaf := execContextIssue("00000000-0000-0000-0000-000000000004", "Leaf", middle.ID, leafRaw)
	leaf.WorkspaceID = workspace

	reader := ancestorBriefReader{issues: map[string]db.Issue{
		util.UUIDToString(root.ID):   root,
		util.UUIDToString(middle.ID): middle,
		util.UUIDToString(leaf.ID):   leaf,
	}}

	resolved := ResolveInheritedExecutionContextForIssue(context.Background(), reader, leaf, "SIY-9", nil)
	values := make([]string, 0, len(resolved.Entries))
	for _, entry := range resolved.Entries {
		values = append(values, entry.Value)
	}
	joined := strings.Join(values, "|")
	if !strings.Contains(joined, "https://github.com/acme/widget") {
		t.Fatalf("root repo must be inherited: %q", joined)
	}
	if strings.Contains(joined, "deploy only on Fridays") {
		t.Fatalf("an ancestor that switched inheritance off must not contribute: %q", joined)
	}
	if !strings.Contains(joined, "./restart.sh") {
		t.Fatalf("the issue's own entries always apply: %q", joined)
	}
}

func TestProjectResourceExecutionEntries(t *testing.T) {
	entries := ProjectResourceExecutionEntries([]db.ProjectResource{
		{ResourceType: "github_repo", ResourceRef: mustMarshal(t, map[string]string{"url": "https://github.com/acme/widget"}), Label: pgtype.Text{String: "widget", Valid: true}},
		{ResourceType: "notion_page", ResourceRef: mustMarshal(t, map[string]string{"url": "https://notion.so/x"})},
		{ResourceType: "github_repo", ResourceRef: mustMarshal(t, map[string]string{})},
	})
	if len(entries) != 1 || entries[0].Value != "https://github.com/acme/widget" || entries[0].Label != "widget" {
		t.Fatalf("project entries = %+v", entries)
	}
}
