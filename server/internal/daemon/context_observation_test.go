package daemon

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func baseTask() Task {
	return Task{
		ID:                      "task-fresh-1",
		RuntimeID:               "rt-1",
		IssueID:                 "issue-1",
		AgentID:                 "agent-1",
		CurrentIssueTitle:       "Implement feature X",
		CurrentIssueDescription: "Do the thing.",
		AncestorBrief:           "ANCESTOR_BRIEF (background reference only)\n[Background source: Issue parent]",
	}
}

func TestBuildContextObservation_FreshSession(t *testing.T) {
	task := baseTask()
	obs := buildContextObservation(observationParams{
		task:            task,
		provider:        "claude",
		runtimeID:       task.RuntimeID,
		runtimeBrief:    "# Multica Agent Runtime\n...",
		prompt:          "You are running as a local coding agent...\n\nYour assigned issue ID is: issue-1\n",
		resumeSessionID: "",
		resumeExpected:  false,
		workdirReused:   false,
	}, time.Time{})

	if obs.SessionReused {
		t.Fatalf("fresh run must not report a reused session")
	}
	if obs.ResumeExpected {
		t.Fatalf("fresh run must not expect a resume")
	}
	if obs.PromptBytes != len("You are running as a local coding agent...\n\nYour assigned issue ID is: issue-1\n") {
		t.Fatalf("prompt bytes mismatch: %d", obs.PromptBytes)
	}
	if obs.TokenMode != tokenModeEstimated {
		t.Fatalf("boundary record must be estimated, got %q", obs.TokenMode)
	}
	// Refine on the no-retry path → fresh.
	refineContextObservation(&obs, "sess-new", false, "", time.Now())
	if obs.ResumeActual != resumeActualFresh {
		t.Fatalf("fresh run refines to fresh, got %q", obs.ResumeActual)
	}
	if obs.SessionID != "sess-new" {
		t.Fatalf("session id not folded in: %q", obs.SessionID)
	}
	// Brief is file-delivered (not inline) → not injected into the prompt.
	brief := findSection(obs.Sections, "brief")
	if brief == nil || brief.Delivery != deliveryWorkdirFile || brief.Injected {
		t.Fatalf("file-only brief must be workdir_file, not injected: %+v", brief)
	}
}

func TestBuildContextObservation_WarmResume(t *testing.T) {
	task := baseTask()
	task.PriorSessionID = "prior-sess-123"
	obs := buildContextObservation(observationParams{
		task:               task,
		provider:           "codex",
		runtimeID:          task.RuntimeID,
		runtimeBrief:       "# brief",
		prompt:             "prompt body",
		resumeSessionID:    "prior-sess-123", // gates passed → session id handed to provider
		inlineSystemPrompt: true,             // provider that cannot read the file
		resumeExpected:     true,
		workdirReused:      true,
	}, time.Time{})

	if !obs.SessionReused || !obs.ResumeExpected || !obs.WorkdirReused {
		t.Fatalf("warm resume must set session_reused/resume_expected/workdir_reused: %+v", obs)
	}
	// Inline brief → delivered into the prompt and injected.
	brief := findSection(obs.Sections, "brief")
	if brief == nil || brief.Delivery != deliveryProviderPrompt || !brief.Injected {
		t.Fatalf("inline brief must be provider_prompt + injected: %+v", brief)
	}
	refineContextObservation(&obs, "prior-sess-123", false, "", time.Now())
	if obs.ResumeActual != resumeActualResumed {
		t.Fatalf("warm resume refines to resumed, got %q", obs.ResumeActual)
	}
}

func TestRefineContextObservation_FallbackRetry(t *testing.T) {
	task := baseTask()
	task.PriorSessionID = "prior-sess"
	obs := buildContextObservation(observationParams{
		task:            task,
		provider:        "codex",
		runtimeID:       task.RuntimeID,
		runtimeBrief:    "# brief",
		prompt:          "prompt",
		resumeSessionID: "prior-sess",
		resumeExpected:  true,
		workdirReused:   true,
	}, time.Time{})
	// The provider rejected the resume; the daemon retried fresh.
	refineContextObservation(&obs, "fresh-after-retry", true, "session history unresumable at /root/private with owner@example.com", time.Now())
	if obs.ResumeActual != resumeActualFallback {
		t.Fatalf("retry must refine to fallback, got %q", obs.ResumeActual)
	}
	if obs.FallbackReason == "" {
		t.Fatalf("fallback must carry the failure reason")
	}
	mustNotContain(t, obs.FallbackReason, "/root/private", "fallback reason leaked absolute path")
	mustNotContain(t, obs.FallbackReason, "owner@example.com", "fallback reason leaked email")
	if obs.SessionID != "fresh-after-retry" {
		t.Fatalf("fallback must record the fresh retry's session id")
	}
}

func TestRefineContextObservation_AgentSwitchIsFresh(t *testing.T) {
	// A switched-to agent has no prior session of its own; from the new
	// agent's perspective this is a fresh start, even though the issue and
	// ancestor context carry over. The observation must not claim a resume.
	task := baseTask()
	obs := buildContextObservation(observationParams{
		task:            task,
		provider:        "claude",
		runtimeID:       "rt-2", // different runtime
		runtimeBrief:    "# brief",
		prompt:          "prompt",
		resumeSessionID: "", // new agent: no session handed over
		resumeExpected:  false,
		workdirReused:   false,
	}, time.Time{})
	refineContextObservation(&obs, "new-agent-sess", false, "", time.Now())
	if obs.ResumeActual != resumeActualFresh || obs.SessionReused {
		t.Fatalf("agent switch must be fresh, not resumed: %+v", obs)
	}
	// Ancestor context still migrates (it is the L3 layer).
	if findSection(obs.Sections, "ancestor") == nil {
		t.Fatalf("agent switch must still carry the ancestor section")
	}
}

func TestFinalizeContextObservation_ProviderStartFailureIsUnknown(t *testing.T) {
	prompt := "final assembled prompt"
	obs := buildContextObservation(observationParams{
		task:            baseTask(),
		provider:        "claude",
		prompt:          prompt,
		resumeSessionID: "prior-session",
		resumeExpected:  true,
	}, time.Time{})

	finalizeContextObservation(
		&obs,
		false,
		"failed to start /root/provider owner@example.com",
		"",
		false,
		"",
		time.Now(),
	)

	if obs.ResumeActual != resumeActualUnknown {
		t.Fatalf("provider startup failure must be unknown, got %q", obs.ResumeActual)
	}
	mustNotContain(t, obs.FallbackReason, "/root/provider", "startup error leaked absolute path")
	mustNotContain(t, obs.FallbackReason, "owner@example.com", "startup error leaked email")
	promptSection := findSection(obs.Sections, "task_prompt")
	if promptSection == nil || promptSection.Raw != prompt {
		t.Fatal("startup failure must retain the assembled prompt for on-demand inspection")
	}
}

func TestRedaction_StripsSecretsEmailsAndPaths(t *testing.T) {
	task := baseTask()
	task.TriggerCommentContent = "Reach me at owner@example.com and check /root/secret/config.yaml token=sk-abcd1234efgh5678ijklmnopqrstuv0123"
	obs := buildContextObservation(observationParams{
		task:            task,
		provider:        "claude",
		runtimeID:       task.RuntimeID,
		runtimeBrief:    "MULTICA_TOKEN=supersecretvalue123456 path /etc/ssl/private.key",
		prompt:          "issue body owner@example.com /abs/path/to/repo",
		mcpConfig:       json.RawMessage(`{"mcpServers":{"x":{"command":"x","env":{"API_KEY":"sk-live-token-1234567890abcdef"}}}}`),
		resumeSessionID: "",
		resumeExpected:  false,
		workdirReused:   false,
	}, time.Time{})

	for _, s := range obs.Sections {
		if s.Preview == "" {
			continue
		}
		mustNotContain(t, s.Preview, "owner@example.com", "preview leaked email")
		mustNotContain(t, s.Preview, "/root/secret", "preview leaked abs path")
		mustNotContain(t, s.Preview, "/etc/ssl/private.key", "preview leaked abs path")
		mustNotContain(t, s.Preview, "sk-abcd1234", "preview leaked secret")
		if s.Raw != "" {
			mustNotContain(t, s.Raw, "owner@example.com", "raw leaked email")
			mustNotContain(t, s.Raw, "supersecretvalue", "raw leaked token value")
			mustNotContain(t, s.Raw, "/etc/ssl/private.key", "raw leaked abs path")
		}
	}
	// MCP raw is withheld entirely — it carries live server credentials.
	mcp := findSection(obs.Sections, "mcp")
	if mcp == nil {
		t.Fatalf("mcp section must be present when config is non-empty")
	}
	if mcp.Raw != "" {
		t.Fatalf("mcp raw must be withheld, got %q", mcp.Raw)
	}
	if mcp.Injected {
		t.Fatalf("mcp is not injected into the prompt text")
	}
	if mcp.Delivery != deliveryProviderMCP {
		t.Fatalf("mcp delivery must be provider_mcp, got %q", mcp.Delivery)
	}
}

func TestSections_AncestorTruncationFlag(t *testing.T) {
	task := baseTask()
	task.AncestorBrief = "ANCESTOR_BRIEF ...\nsome content [truncated]"
	obs := buildContextObservation(observationParams{
		task: task, provider: "claude", runtimeID: task.RuntimeID,
		prompt: "p", runtimeBrief: "b",
	}, time.Time{})
	anc := findSection(obs.Sections, "ancestor")
	if anc == nil || !anc.Truncated {
		t.Fatalf("ancestor with [truncated] marker must set truncated=true: %+v", anc)
	}
}

func findSection(sections []ContextSection, key string) *ContextSection {
	for i := range sections {
		if sections[i].Key == key {
			return &sections[i]
		}
	}
	return nil
}

func mustNotContain(t *testing.T, s, substr, msg string) {
	t.Helper()
	if strings.Contains(s, substr) {
		t.Fatalf("%s: %q contains %q", msg, s, substr)
	}
}
