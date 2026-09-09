package handler

// issue_handoff_compress.go — LLM-powered context compression for Agent Handoff.
//
// When an issue is re-assigned to a different agent, the previous agent's
// comment history may be long. Rather than asking the new agent to re-read
// everything, this module calls the configured LLM to produce a structured
// HandoffSummary that is written to the issue before the new task is enqueued.
//
// SIY-167 reworked the mechanism around the split storage (migration 452):
//
//   - The result is a DERIVED summary. It never touches an agent's authored
//     manual checkpoint, so even a forced compression cannot clobber
//     hand-written resume state (the effective handoff_summary mirror prefers
//     the manual checkpoint).
//   - A freshness gate skips the LLM call when a derived summary was built
//     from the issue's current revision (non-force paths). A summary that is
//     already fresh gains nothing from being regenerated; one built from an
//     older revision is stale and worth refreshing.
//   - The write is a compare-and-swap on (revision, handoff_version). A slow
//     LLM response that lands after a user edit — or after another writer —
//     is refused instead of overwriting fresher state.
//   - The LLM input carries the full issue context, not just a comment
//     transcript: description, acceptance criteria, metadata, ancestors,
//     branch/progress state, the last execution outcome, attachments, and the
//     threads (parent links) of the included comments.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/llm"
)

// maxCommentsForCompression caps how many of the most-recent comments we feed
// to the LLM. Long issues can have hundreds of comments; we take the newest
// ones because they contain the most recent work state. The Context Manifest
// tells the agent which older comments were omitted so nothing is silently
// lost — it is reachable on demand.
const maxCommentsForCompression = 80

// maxHandoffSectionChars bounds each free-form section (description, task
// result, …) of the compression input so one huge description cannot crowd
// the comment transcript out of the prompt. The ancestor brief applies its
// own budget; it is not re-truncated here.
const maxHandoffSectionChars = 6000

const handoffSystemPrompt = `You are a technical project assistant. Your only job is to produce a concise handoff summary for an AI coding agent that is taking over an issue.

You receive the issue itself (description, acceptance criteria, metadata, ancestor context), the branch/progress state, the last execution outcome, attachments, and the recent comment history. Ground the summary in ALL of it: details that decide the direction or quality of the task must survive, especially decisions, constraints, and open questions stated in comments.

Output EXACTLY this JSON object and nothing else. The word "JSON" appears in this instruction to satisfy API requirements:
{
  "current_progress": "one sentence: what has been accomplished so far",
  "key_decisions": ["decision or constraint that decides the task's direction or quality — state it so the next agent does not relitigate it; empty array if none"],
  "next_steps": ["step 1", "step 2"],
  "unresolved_issues": "any blockers, open questions, or known problems — empty string if none",
  "risks": ["risk or pitfall the next agent would otherwise rediscover the hard way — empty array if none"]
}`

const handoffUserTemplate = `Issue title: {{issue_title}}

## Issue description
{{issue_description}}

## Acceptance criteria
{{acceptance_criteria}}

## Issue metadata
{{issue_metadata}}

## Ancestor context
{{ancestor_context}}

## Branch / progress state
{{branch_state}}

## Last execution outcome
{{last_execution}}

## Attachments
{{attachments}}

## Recent comment history (oldest first, newest last; "[reply to <id>]" marks a thread reply)
{{comments}}

Write the handoff summary JSON.`

// CompressHandoffResult reports what one compression attempt did. The rerun
// API's refresh-summary action returns it so the UI can show the new state
// without a second read; fire-and-forget callers ignore it.
type CompressHandoffResult struct {
	// Status is the issue's handoff classification AFTER the attempt
	// (manual | fresh | stale | none — service.HandoffStatus*).
	Status string `json:"compression_status"`
	// SourceRevision is the issue revision the derived summary was written
	// against. Zero when no derived summary exists.
	SourceRevision int64 `json:"source_revision,omitempty"`
	// Latency is the wall-clock duration in milliseconds of the last attempt,
	// persisted with the summary and surfaced for latency dashboards. The API
	// deliberately uses the short, handoff-specific name shared by issue and
	// rerun responses; the database column retains its _ms suffix.
	Latency int32 `json:"latency,omitempty"`
	// Written is true when a fresh derived summary landed.
	Written bool `json:"written,omitempty"`
	// SkippedReason explains a no-op (manual_present, fresh, no_comments,
	// llm_not_configured, conflict). Empty when written.
	SkippedReason string `json:"skipped_reason,omitempty"`
}

// compressHandoffContext runs one best-effort compression attempt and records
// its outcome in metrics. Errors are logged and swallowed so the caller's
// enqueue path is never blocked; the returned result describes what happened.
//
// force=true is for a user-initiated "compact context now" request: it skips
// the freshness gate and regenerates the derived summary even when a current
// one exists. It still never overwrites a manual checkpoint — the CAS write
// keeps them separate, which is the whole point of the split.
func (h *Handler) compressHandoffContext(ctx context.Context, issue db.Issue, force bool, sourceTaskID string) *CompressHandoffResult {
	started := time.Now()
	outcome, result := h.compressHandoffAttempt(ctx, issue, force, sourceTaskID, started)

	h.Metrics.RecordHandoffCompression(outcome)
	switch outcome {
	case "written", "llm_error", "cas_conflict":
		h.Metrics.RecordHandoffCompressionLatency(time.Since(started).Seconds())
	}
	if result == nil {
		result = &CompressHandoffResult{Status: service.CompressionStatus(issue)}
	}
	return result
}

// compressHandoffAttempt is compressHandoffContext without the metric
// recording. It returns the metric outcome bucket alongside the result.
func (h *Handler) compressHandoffAttempt(ctx context.Context, issue db.Issue, force bool, sourceTaskID string, started time.Time) (string, *CompressHandoffResult) {
	baseline := func(reason string) *CompressHandoffResult {
		result := &CompressHandoffResult{
			Status:        service.CompressionStatus(issue),
			SkippedReason: reason,
		}
		if issue.DerivedSummarySourceRevision.Valid {
			result.SourceRevision = issue.DerivedSummarySourceRevision.Int64
		}
		if issue.DerivedSummaryLatencyMs.Valid {
			result.Latency = issue.DerivedSummaryLatencyMs.Int32
		}
		return result
	}

	if !h.handoffLLMEnabled() {
		return "not_configured", baseline("llm_not_configured")
	}

	// Freshness gate. A manual checkpoint is reported as-is: the authored
	// state is the effective record and a digest would not improve it. A
	// fresh derived summary (built from the current revision) is not worth
	// another LLM call. Only a stale or missing summary needs compression —
	// and any caller may bypass that with force.
	if !force {
		switch service.CompressionStatus(issue) {
		case service.HandoffStatusManual:
			return "skipped_manual", baseline("manual_present")
		case service.HandoffStatusFresh:
			return "skipped_fresh", baseline("fresh")
		}
	}

	comments, err := h.Queries.ListCommentsForIssue(ctx, db.ListCommentsForIssueParams{
		IssueID:     issue.ID,
		WorkspaceID: issue.WorkspaceID,
		Limit:       maxCommentsForCompression,
	})
	if err != nil {
		slog.Warn("handoff compress: failed to load comments",
			"issue_id", util.UUIDToString(issue.ID), "error", err)
		return "db_error", baseline("comments_read_failed")
	}
	if len(comments) == 0 {
		return "skipped_no_comments", baseline("no_comments")
	}

	// Coverage: how much of the issue's comment history the input carries.
	// The scan window caps the denominator (ManifestCommentScanLimit); the
	// ratio only ever understates coverage for very long issues, never
	// overstates it.
	var latestCommentID string
	if latest, err := h.Queries.GetLatestCommentForIssue(ctx, db.GetLatestCommentForIssueParams{
		IssueID:     issue.ID,
		WorkspaceID: issue.WorkspaceID,
	}); err == nil {
		latestCommentID = util.UUIDToString(latest.ID)
	}
	scannedIDs, err := h.Queries.ListCommentIDsForIssue(ctx, db.ListCommentIDsForIssueParams{
		IssueID:     issue.ID,
		WorkspaceID: issue.WorkspaceID,
		RowLimit:    service.ManifestCommentScanLimit,
	})
	if err != nil {
		slog.Debug("handoff compress: comment id scan failed; coverage not recorded",
			"issue_id", util.UUIDToString(issue.ID), "error", err)
	} else if total := len(scannedIDs); total > 0 {
		h.Metrics.RecordHandoffCoverage(float64(len(comments)) / float64(total))
	}

	input := h.buildHandoffCompressionInput(ctx, issue, comments)

	// Give the LLM call a tight deadline so a slow upstream never delays the
	// agent enqueue. We do NOT propagate the request context's cancellation:
	// the HTTP handler may return before the goroutine finishes, and that is
	// fine — the write is fire-and-forget from the request's perspective.
	llmCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	raw, err := h.callLLMForHandoffSummary(llmCtx, input)
	if err != nil {
		if errors.Is(err, errLLMNotConfigured) {
			slog.Debug("handoff compress: LLM not configured, skipping")
			return "not_configured", baseline("llm_not_configured")
		}
		slog.Warn("handoff compress: LLM call failed",
			"issue_id", util.UUIDToString(issue.ID), "error", err)
		return "llm_error", baseline("llm_error")
	}

	latencyMs := int32(time.Since(started).Milliseconds())
	updated, err := h.Queries.SetIssueDerivedSummaryCAS(ctx, db.SetIssueDerivedSummaryCASParams{
		Summary:                raw,
		Source:                 "llm",
		SourceRevision:         issue.Revision,
		SourceCommentID:        latestCommentID,
		SourceTaskID:           sourceTaskID,
		LatencyMs:              latencyMs,
		ID:                     issue.ID,
		WorkspaceID:            issue.WorkspaceID,
		ExpectedHandoffVersion: issue.HandoffVersion,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		// The issue moved while the LLM was thinking (a comment landed, an
		// edit bumped the revision, or another writer took the handoff
		// version). Refusing to land a summary built from a stale snapshot is
		// the CAS doing its job; the next non-force pass re-compresses.
		slog.Info("handoff compress: CAS conflict, summary not written",
			"issue_id", util.UUIDToString(issue.ID),
			"source_revision", issue.Revision)
		return "cas_conflict", baseline("conflict")
	}
	if err != nil {
		slog.Warn("handoff compress: failed to write derived summary",
			"issue_id", util.UUIDToString(issue.ID), "error", err)
		return "db_error", baseline("write_failed")
	}

	slog.Info("handoff compress: derived summary written",
		"issue_id", util.UUIDToString(issue.ID),
		"comment_count", len(comments),
		"source_revision", issue.Revision,
		"latency_ms", latencyMs)
	return "written", &CompressHandoffResult{
		Status:         service.CompressionStatus(updated),
		SourceRevision: issue.Revision,
		Latency:        latencyMs,
		Written:        true,
	}
}

// handoffLLMEnabled reports whether any LLM path can serve the compression.
func (h *Handler) handoffLLMEnabled() bool {
	businessLLM := h.businessLLM(llm.BusinessHandoffCompress)
	if businessLLM != nil {
		return businessLLM.Enabled()
	}
	return h.LLM != nil && h.LLM.Enabled()
}

// handoffCompressionInput is everything rendered into the LLM user prompt.
// It is a plain struct so the builder stays testable without an LLM client.
type handoffCompressionInput struct {
	IssueTitle        string
	IssueDescription  string
	Acceptance        string
	IssueMetadata     string
	AncestorContext   string
	BranchState       string
	LastExecution     string
	Attachments       string
	CommentTranscript string
}

// buildHandoffCompressionInput gathers the full issue context around the
// comment transcript. Every section degrades to "" when its source is empty
// or its read fails — compression is best-effort, and a failed optional read
// must not block the summary.
func (h *Handler) buildHandoffCompressionInput(ctx context.Context, issue db.Issue, comments []db.Comment) handoffCompressionInput {
	in := handoffCompressionInput{
		IssueTitle:       issue.Title,
		IssueDescription: truncateHandoffSection(issue.Description.String),
	}

	// Acceptance criteria are a JSON array on the issue row; render one
	// criterion per line so the LLM sees them as a checklist, not JSON syntax.
	var criteria []string
	if len(issue.AcceptanceCriteria) > 0 && json.Unmarshal(issue.AcceptanceCriteria, &criteria) == nil {
		in.Acceptance = truncateHandoffSection(strings.Join(nonEmpty(criteria), "\n- "))
		if in.Acceptance != "" {
			in.Acceptance = "- " + in.Acceptance
		}
	}

	if m := util.JSONObjectOrEmpty(issue.Metadata); len(m) > 0 {
		if b, err := json.Marshal(m); err == nil {
			in.IssueMetadata = truncateHandoffSection(string(b))
		}
	}

	// Ancestor background: the same bounded brief the claim payload delivers,
	// so the digest and the manifest describe the same material.
	ancestorBrief := service.BuildAncestorBrief(ctx, h.Queries, issue)
	in.AncestorContext = ancestorBrief.Text

	var branch []string
	if issue.WorkingBranch.Valid && issue.WorkingBranch.String != "" {
		branch = append(branch, "Working branch: "+issue.WorkingBranch.String)
	}
	if issue.AgentStatus.Valid && issue.AgentStatus.String != "" {
		branch = append(branch, "Progress stage: "+issue.AgentStatus.String)
	}
	in.BranchState = strings.Join(branch, "\n")

	if last, err := h.Queries.GetLastTerminalTaskForIssue(ctx, issue.ID); err == nil {
		var parts []string
		parts = append(parts, fmt.Sprintf("Last task %s (status %s)", util.UUIDToString(last.ID), last.Status))
		if last.FailureReason.Valid && last.FailureReason.String != "" {
			parts = append(parts, "failure_reason: "+last.FailureReason.String)
		}
		if last.Error.Valid && last.Error.String != "" {
			parts = append(parts, "error: "+truncateHandoffSection(last.Error.String))
		}
		if len(last.Result) > 0 && string(last.Result) != "null" {
			parts = append(parts, "result: "+truncateHandoffSection(string(last.Result)))
		}
		in.LastExecution = strings.Join(parts, "\n")
	}

	var atts []string
	if attachments, err := h.Queries.ListAttachmentsByIssue(ctx, db.ListAttachmentsByIssueParams{
		IssueID:     issue.ID,
		WorkspaceID: issue.WorkspaceID,
	}); err == nil {
		for _, a := range attachments {
			atts = append(atts, fmt.Sprintf("%s (%s, %d bytes, id %s)",
				a.Filename, a.ContentType, a.SizeBytes, util.UUIDToString(a.ID)))
		}
	}
	in.Attachments = strings.Join(atts, "\n")

	in.CommentTranscript = renderHandoffCommentTranscript(comments)
	return in
}

// renderHandoffCommentTranscript formats the comment window for the LLM.
// Thread replies are annotated with the parent id so reply relations survive
// the flattening — a "done, see above" reply means nothing without its root.
func renderHandoffCommentTranscript(comments []db.Comment) string {
	var sb strings.Builder
	for _, c := range comments {
		ts := c.CreatedAt.Time.UTC().Format("2006-01-02 15:04")
		sb.WriteString(fmt.Sprintf("[%s] %s (%s): %s\n", ts, c.AuthorType, util.UUIDToString(c.ID), c.Content))
		if c.ParentID.Valid {
			sb.WriteString(fmt.Sprintf("  [reply to %s]\n", util.UUIDToString(c.ParentID)))
		}
	}
	return sb.String()
}

func truncateHandoffSection(s string) string {
	if len(s) <= maxHandoffSectionChars {
		return s
	}
	return s[:maxHandoffSectionChars] + "\n…[truncated]"
}

func nonEmpty(values []string) []string {
	out := make([]string, 0, len(values))
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			out = append(out, v)
		}
	}
	return out
}

// errLLMNotConfigured is a sentinel returned by callLLMForHandoffSummary when
// the LLM client is not enabled, distinguishing "misconfigured" from real
// call errors so the caller can log at the appropriate level.
var errLLMNotConfigured = errors.New("llm not configured")

// callLLMForHandoffSummary renders the enriched input into the prompt, calls
// GenerateJSON, and returns validated JSON bytes for the derived summary.
func (h *Handler) callLLMForHandoffSummary(ctx context.Context, in handoffCompressionInput) ([]byte, error) {
	businessLLM := h.businessLLM(llm.BusinessHandoffCompress)
	if businessLLM != nil {
		if !businessLLM.Enabled() {
			return nil, errLLMNotConfigured
		}
	} else if h.LLM == nil || !h.LLM.Enabled() {
		return nil, errLLMNotConfigured
	}

	vars := map[string]string{
		"issue_title":         in.IssueTitle,
		"issue_description":   in.IssueDescription,
		"acceptance_criteria": in.Acceptance,
		"issue_metadata":      in.IssueMetadata,
		"ancestor_context":    in.AncestorContext,
		"branch_state":        in.BranchState,
		"last_execution":      in.LastExecution,
		"attachments":         in.Attachments,
		"comments":            in.CommentTranscript,
	}

	var raw string
	var err error
	// 768: the schema grew two arrays (key_decisions, risks) beyond the three
	// original keys; 512 truncated rich summaries in practice.
	if businessLLM != nil {
		raw, err = businessLLM.GenerateJSONTemplate(
			ctx,
			vars,
			handoffSystemPrompt,
			handoffUserTemplate,
			0,
			768,
		)
	} else {
		raw, err = h.LLM.GenerateJSON(ctx, "", handoffSystemPrompt, renderHandoffUserPrompt(vars), 0, 768)
	}
	if err != nil {
		if errors.Is(err, llm.ErrNotConfigured) {
			return nil, errLLMNotConfigured
		}
		return nil, err
	}

	// Validate: must be a JSON object with the five expected keys. The two
	// arrays (key_decisions, risks) are optional on OLD summaries — they are
	// missing there, which unmarshals to nil — but the prompt requires the
	// model to emit them (empty when nothing qualifies).
	var check struct {
		CurrentProgress  string   `json:"current_progress"`
		KeyDecisions     []string `json:"key_decisions"`
		NextSteps        []string `json:"next_steps"`
		UnresolvedIssues string   `json:"unresolved_issues"`
		Risks            []string `json:"risks"`
	}
	if err := json.Unmarshal([]byte(raw), &check); err != nil {
		return nil, fmt.Errorf("LLM returned malformed JSON: %w (raw: %.200s)", err, raw)
	}

	return []byte(raw), nil
}

// renderHandoffUserPrompt substitutes handoffUserTemplate's placeholders for
// the non-business-LLM fallback path, so both paths render the same document.
func renderHandoffUserPrompt(vars map[string]string) string {
	out := handoffUserTemplate
	for key, value := range vars {
		out = strings.ReplaceAll(out, "{{"+key+"}}", value)
	}
	return out
}
