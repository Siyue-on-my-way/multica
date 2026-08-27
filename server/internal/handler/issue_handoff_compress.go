package handler

// issue_handoff_compress.go — LLM-powered context compression for Agent Handoff.
//
// When an issue is re-assigned to a different agent, the previous agent's
// comment history may be long. Rather than asking the new agent to re-read
// everything, this module calls the configured LLM to produce a structured
// HandoffSummary that is written to the issue before the new task is enqueued.
//
// This is triggered synchronously inside dispatchIssueRun so the summary is
// already present on the issue when the new task is claimed and injected into
// issue_context.md.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// maxCommentsForCompression caps how many of the most-recent comments we feed
// to the LLM. Long issues can have hundreds of comments; we take the newest
// ones because they contain the most recent work state.
const maxCommentsForCompression = 80

// compressHandoffContext calls the LLM to summarise the issue's recent comment
// history and writes the result into issue.handoff_summary. It is a best-effort
// operation: any error is logged and silently swallowed so the caller's main
// enqueue path is never blocked.
//
// Returns early (no-op) when:
//   - The LLM client is not configured.
//   - The issue already has a handoff_summary AND force is false (a previous
//     agent wrote one explicitly, or an earlier compression already ran;
//     respect it instead of overwriting).
//   - There are no comments to summarise.
//
// force=true is for a user-initiated "compact context now" request (the manual
// standalone rerun path, distinct from a task_id-targeted retry): the whole
// point of that action is to refresh the checkpoint from the latest comments,
// so any existing summary — however it got there — must not block it.
func (h *Handler) compressHandoffContext(ctx context.Context, issue db.Issue, force bool) {
	if !h.LLM.Enabled() {
		return
	}
	// Respect an explicitly written checkpoint: the previous agent's structured
	// summary is more precise than anything the LLM can infer from comments.
	if len(issue.HandoffSummary) > 0 && !force {
		return
	}

	// Give the LLM call a tight deadline so a slow upstream never delays the
	// agent enqueue. We do NOT propagate the request context's cancellation:
	// the HTTP handler may return before the goroutine finishes, and that is
	// fine — the write is fire-and-forget from the request's perspective.
	llmCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	comments, err := h.Queries.ListCommentsForIssue(llmCtx, db.ListCommentsForIssueParams{
		IssueID:     issue.ID,
		WorkspaceID: issue.WorkspaceID,
		Limit:       maxCommentsForCompression,
	})
	if err != nil {
		slog.Warn("handoff compress: failed to load comments",
			"issue_id", uuidToString(issue.ID), "error", err)
		return
	}
	if len(comments) == 0 {
		return
	}

	raw, err := h.callLLMForHandoffSummary(llmCtx, issue, comments)
	if err != nil {
		// ErrNotConfigured just means the deployment has no LLM key; log at
		// debug so routine self-hosted deployments are not noisy.
		if errors.Is(err, errLLMNotConfigured) {
			slog.Debug("handoff compress: LLM not configured, skipping")
		} else {
			slog.Warn("handoff compress: LLM call failed",
				"issue_id", uuidToString(issue.ID), "error", err)
		}
		return
	}

	// Write the summary back to the issue without touching any other field.
	// We re-read the current issue state as UpdateIssue is a full-row COALESCE
	// update that needs every field pre-populated to avoid clearing them.
	current, err := h.Queries.GetIssue(llmCtx, issue.ID)
	if err != nil {
		slog.Warn("handoff compress: failed to re-read issue before update",
			"issue_id", uuidToString(issue.ID), "error", err)
		return
	}
	params := db.UpdateIssueParams{
		ID:             current.ID,
		Title:          pgtype.Text{String: current.Title, Valid: true},
		Description:    current.Description,
		Status:         pgtype.Text{String: current.Status, Valid: true},
		Priority:       pgtype.Text{String: current.Priority, Valid: true},
		AssigneeType:   current.AssigneeType,
		AssigneeID:     current.AssigneeID,
		Position:       pgtype.Float8{Float64: current.Position, Valid: true},
		StartDate:      current.StartDate,
		DueDate:        current.DueDate,
		ParentIssueID:  current.ParentIssueID,
		ProjectID:      current.ProjectID,
		Stage:          current.Stage,
		WorkingBranch:  current.WorkingBranch,
		AgentStatus:    current.AgentStatus,
		HandoffSummary: raw,
	}
	if _, err := h.Queries.UpdateIssue(llmCtx, params); err != nil {
		slog.Warn("handoff compress: failed to write handoff_summary",
			"issue_id", uuidToString(issue.ID), "error", err)
		return
	}
	slog.Info("handoff compress: summary written",
		"issue_id", uuidToString(issue.ID),
		"comment_count", len(comments))
}

// errLLMNotConfigured is a sentinel returned by callLLMForHandoffSummary when
// the LLM client is not enabled, distinguishing "misconfigured" from real
// call errors so the caller can log at the appropriate level.
var errLLMNotConfigured = errors.New("llm not configured")

// callLLMForHandoffSummary builds the prompt, calls GenerateJSON, and returns
// validated JSON bytes for the handoff_summary column.
func (h *Handler) callLLMForHandoffSummary(ctx context.Context, issue db.Issue, comments []db.Comment) ([]byte, error) {
	if !h.LLM.Enabled() {
		return nil, errLLMNotConfigured
	}

	// Build a compact comment transcript to stay within a reasonable token
	// budget. Each comment is rendered as "Author (time): content".
	var sb strings.Builder
	for _, c := range comments {
		ts := c.CreatedAt.Time.UTC().Format("2006-01-02 15:04")
		author := c.AuthorType
		sb.WriteString(fmt.Sprintf("[%s] %s: %s\n", ts, author, c.Content))
	}
	transcript := sb.String()

	systemPrompt := `You are a technical project assistant. Your only job is to produce a concise handoff summary for an AI coding agent that is taking over an issue.

Output EXACTLY this JSON object and nothing else. The word "JSON" appears in this instruction to satisfy API requirements:
{
  "current_progress": "one sentence: what has been accomplished so far",
  "next_steps": ["step 1", "step 2"],
  "unresolved_issues": "any blockers, open questions, or known problems — empty string if none"
}`

	userPrompt := fmt.Sprintf(
		"Issue title: %s\n\nRecent comment history (oldest first, newest last):\n\n%s\n\nWrite the handoff summary JSON.",
		issue.Title,
		transcript,
	)

	raw, err := h.LLM.GenerateJSON(ctx, "", systemPrompt, userPrompt, 0, 512)
	if err != nil {
		return nil, err
	}

	// Validate: must be a JSON object with the three expected keys.
	var check struct {
		CurrentProgress  string   `json:"current_progress"`
		NextSteps        []string `json:"next_steps"`
		UnresolvedIssues string   `json:"unresolved_issues"`
	}
	if err := json.Unmarshal([]byte(raw), &check); err != nil {
		return nil, fmt.Errorf("LLM returned malformed JSON: %w (raw: %.200s)", err, raw)
	}

	return []byte(raw), nil
}
