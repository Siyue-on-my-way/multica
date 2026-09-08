package service

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// The split storage is only useful while the classification is honest: a
// manual checkpoint must win, a stale digest must never read as fresh, and an
// empty issue must report none. These states are exactly what the API's
// compression_status and the Context Manifest handoff block expose.
func TestCompressionStatus(t *testing.T) {
	tests := []struct {
		name  string
		issue db.Issue
		want  string
	}{
		{
			name:  "no handoff state",
			issue: db.Issue{Revision: 7},
			want:  HandoffStatusNone,
		},
		{
			name: "manual checkpoint wins over everything",
			issue: db.Issue{
				Revision:         7,
				ManualCheckpoint: []byte(`{"current_progress":"x"}`),
				DerivedSummary:   []byte(`{"current_progress":"y"}`),
			},
			want: HandoffStatusManual,
		},
		{
			name: "derived summary fresh at source revision",
			issue: db.Issue{
				Revision:                     7,
				DerivedSummary:               []byte(`{"current_progress":"y"}`),
				DerivedSummarySourceRevision: pgtype.Int8{Int64: 7, Valid: true},
			},
			want: HandoffStatusFresh,
		},
		{
			name: "derived summary fresh when revision unchanged since write",
			issue: db.Issue{
				Revision:                     5,
				DerivedSummary:               []byte(`{"current_progress":"y"}`),
				DerivedSummarySourceRevision: pgtype.Int8{Int64: 7, Valid: true},
			},
			want: HandoffStatusFresh,
		},
		{
			name: "derived summary stale after new activity",
			issue: db.Issue{
				Revision:                     9,
				DerivedSummary:               []byte(`{"current_progress":"y"}`),
				DerivedSummarySourceRevision: pgtype.Int8{Int64: 7, Valid: true},
			},
			want: HandoffStatusStale,
		},
		{
			name: "derived summary without a source revision is stale",
			issue: db.Issue{
				Revision:       7,
				DerivedSummary: []byte(`{"current_progress":"y"}`),
			},
			want: HandoffStatusStale,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := CompressionStatus(tc.issue); got != tc.want {
				t.Errorf("CompressionStatus() = %q, want %q", got, tc.want)
			}
		})
	}
}

// DerivedSummaryFresh is the freshness gate's core: compression writes do not
// bump the revision, so a summary is fresh exactly when the issue has not
// moved since it was written.
func TestDerivedSummaryFresh(t *testing.T) {
	if DerivedSummaryFresh(db.Issue{Revision: 3}) {
		t.Error("no derived summary must never read as fresh")
	}
	issue := db.Issue{
		Revision:                     3,
		DerivedSummary:               []byte(`{}`),
		DerivedSummarySourceRevision: pgtype.Int8{Int64: 3, Valid: true},
	}
	if !DerivedSummaryFresh(issue) {
		t.Error("summary built from the current revision must be fresh")
	}
	issue.Revision = 4
	if DerivedSummaryFresh(issue) {
		t.Error("summary built from an older revision must not be fresh")
	}
}

// The manifest is what an agent reconciles against on its first turn, so its
// shape and values are contract, not detail: revision, handoff classification
// with provenance, included vs omitted comment ids, rerun lineage, and the
// branch checkpoint.
func TestBuildContextManifest(t *testing.T) {
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	issue := db.Issue{
		ID:                           pgtype.UUID{Bytes: [16]byte{1}, Valid: true},
		Revision:                     12,
		WorkingBranch:                pgtype.Text{String: "feat/x", Valid: true},
		DerivedSummary:               []byte(`{"current_progress":"p"}`),
		DerivedSummarySourceRevision: pgtype.Int8{Int64: 11, Valid: true},
		DerivedSummaryVersion:        3,
		HandoffVersion:               4,
		DerivedSummaryLatencyMs:      pgtype.Int4{Int32: 812, Valid: true},
		DerivedSummaryUpdatedAt:      pgtype.Timestamptz{Time: now, Valid: true},
	}

	manifest := BuildContextManifest(
		issue,
		[]AncestorBriefRef{{ID: "anc-1"}},
		[]string{"c-2", "c-1"},
		[]string{"c-0"},
		2,
		"task-9",
		now,
	)

	if manifest.SchemaVersion != ContextManifestSchemaVersion {
		t.Errorf("schema_version = %d, want %d", manifest.SchemaVersion, ContextManifestSchemaVersion)
	}
	if manifest.IssueRevision != 12 {
		t.Errorf("issue_revision = %d, want 12", manifest.IssueRevision)
	}
	if manifest.Handoff.Status != HandoffStatusStale {
		t.Errorf("handoff.status = %q, want stale (source revision 11 < issue revision 12)", manifest.Handoff.Status)
	}
	if manifest.Handoff.Version != 4 || manifest.Handoff.SourceRevision != 11 || manifest.Handoff.LatencyMs != 812 {
		t.Errorf("handoff block = %+v, want version 4 / source revision 11 / latency 812", manifest.Handoff)
	}
	if len(manifest.Ancestors) != 1 || manifest.Ancestors[0].ID != "anc-1" {
		t.Errorf("ancestors = %+v, want one ref anc-1", manifest.Ancestors)
	}
	if len(manifest.IncludedCommentIDs) != 2 || len(manifest.OmittedCommentIDs) != 1 {
		t.Errorf("included/omitted = %v / %v", manifest.IncludedCommentIDs, manifest.OmittedCommentIDs)
	}
	if manifest.OmittedCommentCount != 3 {
		t.Errorf("omitted_comment_count = %d, want 3 (1 listed + 2 beyond scan)", manifest.OmittedCommentCount)
	}
	if manifest.SourceTaskID != "task-9" || manifest.WorkingBranch != "feat/x" {
		t.Errorf("source_task_id/working_branch = %q / %q", manifest.SourceTaskID, manifest.WorkingBranch)
	}
	if manifest.GeneratedAt != "2026-09-09T12:00:00Z" {
		t.Errorf("generated_at = %q", manifest.GeneratedAt)
	}

	// The manifest ships in the claim payload as JSON; it must round-trip.
	raw, err := MarshalContextManifest(manifest)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	var back map[string]any
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("manifest is not valid JSON: %v", err)
	}
	if back["handoff"] == nil || back["issue_revision"] == nil {
		t.Errorf("manifest JSON missing contract keys: %s", raw)
	}
}
