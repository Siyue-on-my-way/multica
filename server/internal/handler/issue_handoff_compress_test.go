package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// The refresh_summary / new_session / retry split only decouples behaviour if
// the wire action always lands on the right task-row mode. The empty action
// must stay "" so pre-split rows and post-split legacy requests stay
// distinguishable in metrics and audit.
func TestRerunModeFor(t *testing.T) {
	tests := []struct {
		action string
		want   string
	}{
		{RerunActionRefreshSummary, "refresh_summary"},
		{RerunActionNewSession, "new_session"},
		{RerunActionRetry, "retry"},
		{"", ""},
		{"unknown_future_action", ""},
	}
	for _, tc := range tests {
		if got := rerunModeFor(tc.action); got != tc.want {
			t.Errorf("rerunModeFor(%q) = %q, want %q", tc.action, got, tc.want)
		}
	}
}

func TestRerunAllowsSourceSession(t *testing.T) {
	withSource := db.AgentTaskQueue{RerunOfTaskID: pgtype.UUID{Valid: true}}
	tests := []struct {
		name string
		task db.AgentTaskQueue
		want bool
	}{
		{"retry mode", db.AgentTaskQueue{RerunOfTaskID: withSource.RerunOfTaskID, RerunMode: RerunActionRetry, ForceFreshSession: true}, true},
		{"new session mode", db.AgentTaskQueue{RerunOfTaskID: withSource.RerunOfTaskID, RerunMode: RerunActionNewSession}, false},
		{"legacy fresh row", db.AgentTaskQueue{RerunOfTaskID: withSource.RerunOfTaskID, ForceFreshSession: true}, false},
		{"legacy resume row", db.AgentTaskQueue{RerunOfTaskID: withSource.RerunOfTaskID}, true},
		{"not a rerun", db.AgentTaskQueue{}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := rerunAllowsSourceSession(tc.task); got != tc.want {
				t.Errorf("rerunAllowsSourceSession() = %v, want %v", got, tc.want)
			}
		})
	}
}

// Both LLM paths (business client template substitution and the fallback's
// direct GenerateJSON) must render the same document. This pins the fallback
// renderer to the template's placeholder set: a placeholder the renderer
// misses ships raw {{braces}} to the model.
func TestRenderHandoffUserPromptSubstitutesEveryPlaceholder(t *testing.T) {
	vars := map[string]string{
		"issue_title":         "Fix login",
		"issue_description":   "The login form eats valid credentials.",
		"acceptance_criteria": "- rejects bad passwords",
		"issue_metadata":      `{"team":"identity"}`,
		"ancestor_context":    "ANCESTOR_BRIEF",
		"branch_state":        "Working branch: feat/login",
		"last_execution":      "Last task failed: timeout",
		"attachments":         "logs.txt",
		"comments":            "[2026-01-01 10:00] member: repro attached",
	}
	rendered := renderHandoffUserPrompt(vars)

	for key, value := range vars {
		if !strings.Contains(rendered, value) {
			t.Errorf("rendered prompt lost the %q value", key)
		}
	}
	if strings.Contains(rendered, "{{") {
		t.Errorf("rendered prompt still carries a raw placeholder: %.200s", rendered)
	}
}

// TestRerunIssue_NewSessionAttemptsSummaryRefresh pins the one-click reopen
// contract: new_session runs the pre-enqueue compression attempt (its result
// travels on the response even when the LLM is unconfigured and it writes
// nothing), while the other enqueue paths keep skipping compression — so a
// reopen can never start from a digest older than the issue without the user
// having to click refresh first.
func TestRerunIssue_NewSessionAttemptsSummaryRefresh(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	agentID := createHandlerTestAgent(t, "RerunCompressAgent", []byte("[]"))

	var issueID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO issue (workspace_id, title, creator_type, creator_id, assignee_type, assignee_id, priority)
		VALUES ($1, 'rerun compresses stale summary', 'member', $2, 'agent', $3, 'medium')
		RETURNING id`, testWorkspaceID, testUserID, agentID).Scan(&issueID); err != nil {
		t.Fatalf("seed issue: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE issue_id = $1`, issueID)
		testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, issueID)
	})

	rerun := func(body map[string]any) (int, map[string]any) {
		w := httptest.NewRecorder()
		testHandler.RerunIssue(w, withURLParam(newRequestAs(testUserID, "POST", "/api/issues/"+issueID+"/rerun", body), "id", issueID))
		var resp map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode rerun response (status %d): %v", w.Code, err)
		}
		return w.Code, resp
	}

	// new_session: compression IS attempted (freshness-gated server-side; the
	// test env has no handoff LLM, so the attempt reports why it wrote
	// nothing) — the field's presence is what proves the attempt happened.
	code, resp := rerun(map[string]any{"action": RerunActionNewSession})
	if code != http.StatusAccepted {
		t.Fatalf("new_session rerun: expected 202, got %d: %s", code, resp["error"])
	}
	if resp["compression_skipped_reason"] != "llm_not_configured" {
		t.Errorf("new_session compression_skipped_reason = %v, want llm_not_configured (attempt must run even without an LLM)", resp["compression_skipped_reason"])
	}
	if resp["compression_status"] == nil || resp["compression_status"] == "" {
		t.Errorf("new_session response missing compression_status: %v", resp)
	}

	// retry: no compression attempt — resuming a healthy session needs no
	// digest, and the retry must stay fast.
	origID := ""
	if err := testPool.QueryRow(ctx, `SELECT id FROM agent_task_queue WHERE issue_id = $1 ORDER BY created_at LIMIT 1`, issueID).Scan(&origID); err != nil {
		t.Fatalf("read enqueued task: %v", err)
	}
	_, resp = rerun(map[string]any{"action": RerunActionRetry, "task_id": origID})
	if _, ok := resp["compression_skipped_reason"]; ok {
		t.Errorf("retry response carries compression_skipped_reason %v, want none", resp["compression_skipped_reason"])
	}

	// Legacy bare rerun (the CLI shape): unchanged — no compression unless the
	// with_context_compress flag asks for it.
	_, resp = rerun(map[string]any{})
	if _, ok := resp["compression_skipped_reason"]; ok {
		t.Errorf("legacy rerun response carries compression_skipped_reason %v, want none", resp["compression_skipped_reason"])
	}
}
