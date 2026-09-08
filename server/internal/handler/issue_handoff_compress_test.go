package handler

import (
	"strings"
	"testing"
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
