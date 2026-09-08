package service

import (
	"encoding/json"
	"time"

	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// SIY-167: split handoff storage and the Context Manifest.
//
// An issue's handoff record used to be one opaque handoff_summary column
// mixing two very different things: an agent-authored checkpoint (precise,
// deliberately written) and an LLM-derived digest of the comment history
// (lossy, regenerated). Forced compression could clobber the authored
// checkpoint, and nothing could tell how old a digest was. Migration 452
// splits them into typed columns with independent versions:
//
//   - manual_checkpoint — authored state (issue update / CAS write). Never
//     touched by compression; it wins the effective handoff_summary mirror.
//   - derived_summary — the LLM digest, stamped with the issue revision and
//     newest comment id it was built from, plus a CAS-protected version.
//
// handoff_summary stays populated as the EFFECTIVE checkpoint (manual if
// present, else derived) so every existing reader — claim payload, prompt,
// API — keeps working unchanged; the split only changes who may write what.

// Handoff compression states surfaced by the API (compression_status) and the
// Context Manifest. "manual" means an authored checkpoint is the effective
// record; "fresh"/"stale" describe a derived-only summary against the issue's
// current revision; "none" means there is nothing to resume from.
const (
	HandoffStatusNone   = "none"
	HandoffStatusManual = "manual"
	HandoffStatusFresh  = "fresh"
	HandoffStatusStale  = "stale"
)

// DerivedSummaryFresh reports whether a derived summary was built from the
// issue's current revision. Compression writes do not bump issue.revision (a
// summary refresh is not an issue edit), so a summary is fresh exactly when
// the issue has not been edited — including any new comment — since it was
// written: source_revision >= revision can only hold when they are equal.
func DerivedSummaryFresh(issue db.Issue) bool {
	if len(issue.DerivedSummary) == 0 {
		return false
	}
	return issue.DerivedSummarySourceRevision.Valid &&
		issue.DerivedSummarySourceRevision.Int64 >= issue.Revision
}

// CompressionStatus classifies the issue's handoff record for the API and the
// Context Manifest. Authored state is reported as "manual" regardless of the
// derived digest: the manual checkpoint is what the next agent is told to
// resume from, so that is the state a user or agent needs to see.
func CompressionStatus(issue db.Issue) string {
	hasManual := len(issue.ManualCheckpoint) > 0
	hasDerived := len(issue.DerivedSummary) > 0
	switch {
	case hasManual:
		return HandoffStatusManual
	case hasDerived && DerivedSummaryFresh(issue):
		return HandoffStatusFresh
	case hasDerived:
		return HandoffStatusStale
	default:
		return HandoffStatusNone
	}
}

// ContextManifest is the claim-time audit record for the context a run was
// assembled from. The checkpoint digest answers "what happened"; the manifest
// answers "what am I holding and what did the server leave out" so an agent
// can reconcile on its first turn and pull raw comments on demand instead of
// trusting a lossy summary blindly.
type ContextManifest struct {
	SchemaVersion int    `json:"schema_version"`
	IssueID       string `json:"issue_id"`
	// IssueRevision is the issue's revision when this run claimed it. A run
	// whose manifest revision is behind the live issue's revision knows new
	// activity landed after its context was assembled.
	IssueRevision int64 `json:"issue_revision"`
	// Handoff describes the checkpoint the claim carries: its classification
	// (manual/fresh/stale/none), the CAS version it was read at, and — for a
	// derived summary — the revision, newest comment id, source task and
	// compression latency it was built from.
	Handoff ContextManifestHandoff `json:"handoff"`
	// Ancestors are the ancestor issues whose brief was delivered (same refs
	// as the ancestor brief itself).
	Ancestors []AncestorBriefRef `json:"ancestors,omitempty"`
	// IncludedCommentIDs are the comments delivered with the claim (trigger +
	// coalesced). OmittedCommentIDs are known comment ids NOT delivered —
	// older history beyond the payload budget. The pair lets an agent audit
	// coverage without the server copying every old comment body anywhere.
	IncludedCommentIDs  []string `json:"included_comment_ids,omitempty"`
	OmittedCommentIDs   []string `json:"omitted_comment_ids,omitempty"`
	OmittedCommentCount int      `json:"omitted_comment_count,omitempty"`
	// SourceTaskID is the rerun lineage (rerun_of_task_id) when this run is a
	// manual retry of an earlier execution.
	SourceTaskID string `json:"source_task_id,omitempty"`
	// WorkingBranch is the branch checkpoint the run should resume on.
	WorkingBranch string `json:"working_branch,omitempty"`
	GeneratedAt   string `json:"generated_at"`
}

// ContextManifestHandoff is the manifest's view of the handoff record.
type ContextManifestHandoff struct {
	Status           string `json:"status"`
	Version          int64  `json:"version"`
	SourceRevision   int64  `json:"source_revision,omitempty"`
	SourceCommentID  string `json:"source_comment_id,omitempty"`
	SourceTaskID     string `json:"source_task_id,omitempty"`
	LatencyMs        int32  `json:"latency_ms,omitempty"`
	ManualUpdatedAt  string `json:"manual_updated_at,omitempty"`
	DerivedUpdatedAt string `json:"derived_updated_at,omitempty"`
}

// ContextManifestSchemaVersion is bumped whenever the manifest shape changes
// in a way a consumer must notice.
const ContextManifestSchemaVersion = 1

// ManifestCommentScanLimit bounds how many comment ids the manifest (and the
// compression coverage ratio) scans. Long issues can exceed a thousand
// comments (prod max ~1.1k); the manifest needs coverage truth, not
// unbounded growth, so the omitted list reports the first window beyond the
// scan as a count.
const ManifestCommentScanLimit = 500

// BuildContextManifest assembles the manifest from values the claim path has
// already loaded. includedIDs are the comment ids delivered with this claim;
// omittedIDs are the recent-but-not-delivered ids the caller resolved (newest
// first, excluding included ones); omittedBeyondScan counts comments older
// than the scan window, reported as a count only.
func BuildContextManifest(
	issue db.Issue,
	ancestors []AncestorBriefRef,
	includedIDs []string,
	omittedIDs []string,
	omittedBeyondScan int,
	sourceTaskID string,
	now time.Time,
) ContextManifest {
	manifest := ContextManifest{
		SchemaVersion:       ContextManifestSchemaVersion,
		IssueID:             util.UUIDToString(issue.ID),
		IssueRevision:       issue.Revision,
		Ancestors:           ancestors,
		IncludedCommentIDs:  includedIDs,
		OmittedCommentIDs:   omittedIDs,
		OmittedCommentCount: omittedBeyondScan + len(omittedIDs),
		SourceTaskID:        sourceTaskID,
		GeneratedAt:         now.UTC().Format(time.RFC3339),
	}
	if issue.WorkingBranch.Valid {
		manifest.WorkingBranch = issue.WorkingBranch.String
	}
	manifest.Handoff = ContextManifestHandoff{
		Status:  CompressionStatus(issue),
		Version: issue.HandoffVersion,
	}
	if issue.DerivedSummarySourceRevision.Valid {
		manifest.Handoff.SourceRevision = issue.DerivedSummarySourceRevision.Int64
	}
	if issue.DerivedSummarySourceCommentID.Valid {
		manifest.Handoff.SourceCommentID = util.UUIDToString(issue.DerivedSummarySourceCommentID)
	}
	if issue.DerivedSummarySourceTaskID.Valid {
		manifest.Handoff.SourceTaskID = util.UUIDToString(issue.DerivedSummarySourceTaskID)
	}
	if issue.DerivedSummaryLatencyMs.Valid {
		manifest.Handoff.LatencyMs = issue.DerivedSummaryLatencyMs.Int32
	}
	if issue.ManualCheckpointUpdatedAt.Valid {
		manifest.Handoff.ManualUpdatedAt = issue.ManualCheckpointUpdatedAt.Time.UTC().Format(time.RFC3339)
	}
	if issue.DerivedSummaryUpdatedAt.Valid {
		manifest.Handoff.DerivedUpdatedAt = issue.DerivedSummaryUpdatedAt.Time.UTC().Format(time.RFC3339)
	}
	return manifest
}

// MarshalContextManifest renders the manifest for the claim payload and the
// task row audit column. A marshal failure is a programmer error (the struct
// is all scalar/slice fields), so the caller logs it and ships null.
func MarshalContextManifest(m ContextManifest) ([]byte, error) {
	return json.Marshal(m)
}
