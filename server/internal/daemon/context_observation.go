package daemon

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/multica-ai/multica/server/pkg/redact"
)

// This file implements context observation: a structured, read-only record of
// what context a task run actually assembled and delivered to its provider,
// captured at the assembly-complete / provider-startup boundary.
//
// It is strictly observation-only. Building a ContextObservation never mutates
// the Task, the assembled prompt, or the exec options, so the existing Prompt
// assembly and session-resume behavior is unchanged (constraint #1 of SIY-125).
// The recording hook in daemon.go only reads in-scope values and reports them;
// it never alters the run path.

// Delivery describes how a context section reached the agent. It is the axis the
// issue calls "投递方式": distinguish what was sent into the provider prompt,
// what was written to the workdir, what was handed to the provider as MCP, and
// what only the platform can read on demand.
const (
	deliveryProviderPrompt = "provider_prompt"  // placed into the user/system message sent to the provider
	deliveryWorkdirFile    = "workdir_file"     // written into the task workdir (AGENTS.md, sidecars, skills)
	deliveryProviderMCP    = "provider_mcp"      // handed to the provider via its MCP config, not the prompt text
	deliveryPlatformRead   = "platform_readable" // readable from the platform DB on demand (issue/comments/metadata)
)

// resumeActual values. "resumed" = a session id was handed to the provider and
// the run did not fall back; "fresh" = no resume was attempted; "fallback" = a
// resume was attempted, failed, and the run retried with a fresh session.
const (
	resumeActualResumed  = "resumed"
	resumeActualFresh    = "fresh"
	resumeActualFallback = "fallback"
	resumeActualUnknown  = "unknown"
)

// tokenMode values. "estimated" at the boundary (char-based heuristic); "exact"
// once the provider's reported usage is folded in after the run.
const (
	tokenModeEstimated = "estimated"
	tokenModeExact     = "exact"
)

// ContextSection is one delivered context fragment with its origin, delivery
// channel, injection status, size, integrity digest, and a redacted preview.
// Raw holds the full redacted text and is only returned by the API on explicit
// demand after a permission check; previews are always safe to surface.
type ContextSection struct {
	Key        string `json:"key"`
	Source     string `json:"source"`
	Delivery   string `json:"delivery"`
	Injected   bool   `json:"injected"` // actually placed into the prompt sent to the provider
	Bytes      int    `json:"bytes"`
	TokenCount int    `json:"token_count"`
	Truncated  bool   `json:"truncated"`
	Digest     string `json:"digest"`  // first 8 hex chars of sha256 over the redacted raw
	Preview    string `json:"preview"` // redacted, capped excerpt
	Raw        string `json:"raw,omitempty"` // redacted full text; only served on demand with permission
}

// ContextObservation is the full observation record for one task run. The
// summary fields are safe to surface in lists; SessionID and section Raw are
// gated behind a permission check on read.
type ContextObservation struct {
	TaskID          string           `json:"task_id"`
	Provider        string           `json:"provider"`
	RuntimeID       string           `json:"runtime_id,omitempty"`
	SessionReused   bool             `json:"session_reused"`   // a resume session id was handed to the provider
	ResumeExpected  bool             `json:"resume_expected"`   // the run intended to resume a prior session
	ResumeActual    string           `json:"resume_actual"`     // resumed | fresh | fallback | unknown
	FallbackReason  string           `json:"fallback_reason,omitempty"`
	WorkdirReused   bool             `json:"workdir_reused"`
	PromptBytes     int              `json:"prompt_bytes"`
	InputTokens     int              `json:"input_tokens"`
	TokenMode       string           `json:"token_mode"` // exact | estimated
	Sections        []ContextSection `json:"sections"`
	SessionID       string           `json:"session_id,omitempty"` // full value; API shortens unless permitted
	ObservedAt      time.Time        `json:"observed_at"`
	CompletedAt     *time.Time       `json:"completed_at,omitempty"` // set when post-run refinement lands
}

// observationParams carries the boundary values the builder needs, kept as
// primitives so the observation module stays decoupled from the agent package
// and is trivially testable. The daemon populates it from task/execOpts/env.
type observationParams struct {
	task             Task
	provider         string
	runtimeID        string
	runtimeBrief     string // bytes written as AGENTS.md / CLAUDE.md by InjectRuntimeConfig
	prompt           string // final assembled prompt from BuildPrompt
	mcpConfig        json.RawMessage
	resumeSessionID  string // execOpts.ResumeSessionID
	inlineSystemPrompt bool  // execOpts.SystemPrompt != ""
	resumeExpected   bool   // execOpts.ResumeExpected
	workdirReused    bool
}

// buildContextObservation assembles the observation record from boundary values.
// It performs no I/O and mutates none of its inputs. now lets tests pin time.
func buildContextObservation(p observationParams, now time.Time) ContextObservation {
	sections := buildContextSections(p)

	// Estimated input tokens = the prompt the provider receives plus the
	// runtime brief when it is also delivered inline as the system prompt.
	// File-only briefs are reported as their own section (delivery=workdir_file)
	// and left out of this prompt-anchored total to honor "未实际注入 Prompt 的
	// 不应被统计为已注入" — the breakdown surfaces their size separately.
	inputTokens := estimateTokens(p.prompt)
	if p.inlineSystemPrompt && p.runtimeBrief != "" {
		inputTokens += estimateTokens(p.runtimeBrief)
	}

	resumeActual := resumeActualFresh
	switch {
	case p.resumeSessionID != "":
		resumeActual = resumeActualResumed
	case p.resumeExpected:
		// Expected to resume but no session id survived the gates: a gate dropped
		// it. The run starts fresh from the prompt's perspective.
		resumeActual = resumeActualFresh
	}

	return ContextObservation{
		TaskID:         p.task.ID,
		Provider:       p.provider,
		RuntimeID:      p.runtimeID,
		SessionReused:  p.resumeSessionID != "",
		ResumeExpected: p.resumeExpected,
		ResumeActual:   resumeActual,
		WorkdirReused:  p.workdirReused,
		PromptBytes:    len(p.prompt),
		InputTokens:    inputTokens,
		TokenMode:      tokenModeEstimated,
		Sections:       sections,
		ObservedAt:     now,
	}
}

// buildContextSections derives each delivered section from its source data. Only
// sections with measurable source text get a token estimate; metadata-only
// sections (skills, sidecar) carry their count in the preview and a zero count.
// refineContextObservation folds the post-run outcome into the boundary record.
// The boundary capture (buildContextObservation) recorded what was assembled;
// this folds what actually happened at the provider: whether the resume the
// boundary handed over survived (resumed), the run started fresh with no resume
// attempt (fresh), or the resume failed and the daemon retried with a fresh
// session (fallback, carrying the failure reason). It also lands the final
// session id and completion time. Separating it from the recording defer lets
// the four badge scenarios be unit-tested without spinning up a real run.
func refineContextObservation(obs *ContextObservation, sessionID string, freshRetryFired bool, fallbackReason string, completedAt time.Time) {
	obs.SessionID = sessionID
	switch {
	case freshRetryFired:
		obs.ResumeActual = resumeActualFallback
		obs.FallbackReason = fallbackReason
	case obs.SessionReused:
		obs.ResumeActual = resumeActualResumed
	default:
		obs.ResumeActual = resumeActualFresh
	}
	obs.CompletedAt = &completedAt
}

func buildContextSections(p observationParams) []ContextSection {
	var out []ContextSection

	// L1 brief: written to the workdir as AGENTS.md/CLAUDE.md; also delivered
	// inline as the system prompt for providers that cannot load the file.
	if p.runtimeBrief != "" {
		delivery := deliveryWorkdirFile
		injected := false
		if p.inlineSystemPrompt {
			delivery = deliveryProviderPrompt
			injected = true
		}
		out = append(out, sectionFromText("brief", "execenv.InjectRuntimeConfig", delivery, injected, p.runtimeBrief))
	}

	// Current issue title + description — embedded in the prompt and also a
	// platform-readable record.
	issueText := issueSourceText(p.task)
	if issueText != "" {
		out = append(out, sectionFromText("issue", "task.current_issue", deliveryProviderPrompt, true, issueText))
	}

	// Ancestor background snapshot; the server may have truncated it.
	if p.task.AncestorBrief != "" {
		s := sectionFromText("ancestor", "ancestor_brief", deliveryProviderPrompt, true, p.task.AncestorBrief)
		s.Truncated = strings.Contains(p.task.AncestorBrief, "[truncated]")
		out = append(out, s)
	}

	// Handoff: structured checkpoint (in prompt + sidecar) plus the legacy note.
	handoffText := handoffSourceText(p.task)
	if handoffText != "" {
		out = append(out, sectionFromText("handoff", "task.handoff", deliveryProviderPrompt, true, handoffText))
	}

	// Triggering comment (embedded verbatim) and any coalesced earlier comments.
	if p.task.TriggerCommentContent != "" {
		out = append(out, sectionFromText("trigger_comment", "task.trigger_comment", deliveryProviderPrompt, true, p.task.TriggerCommentContent))
	}
	if coalesced := coalescedCommentsText(p.task); coalesced != "" {
		out = append(out, sectionFromText("coalesced_comments", "task.coalesced_comments", deliveryProviderPrompt, true, coalesced))
	}

	// MCP config delivered to the provider outside the prompt text. MCP config
	// carries live server credentials (tokens, connection strings), so — matching
	// the platform's own redactMcpConfig pattern — its raw text is never stored
	// or surfaced, even on demand. The section reports only its size and that it
	// was delivered.
	if len(p.mcpConfig) > 0 && string(p.mcpConfig) != "null" {
		out = append(out, ContextSection{
			Key:        "mcp",
			Source:     "agent.mcp_config",
			Delivery:   deliveryProviderMCP,
			Injected:   false,
			Bytes:      len(p.mcpConfig),
			TokenCount: estimateTokensLength(len(p.mcpConfig)),
			Digest:     digestString(string(p.mcpConfig)),
			Preview:    "MCP config delivered to provider (withheld — contains secrets)",
			Raw:        "",
		})
	}

	// Per-turn residual: the prompt minus the source strings embedded in it
	// (issue, ancestor, handoff, comments). This captures the wrapper, the
	// turn-mode marker, and perTurnContextBlocks (initiator, connected apps,
	// continuity notice, shared-dir, worktree conflicts). It is an estimate by
	// subtraction, so it never goes negative and is marked estimated downstream.
	embedded := len(issueText) + len(p.task.AncestorBrief) + len(handoffText) + len(p.task.TriggerCommentContent) + len(coalescedCommentsText(p.task))
	residual := len(p.prompt) - embedded
	if residual < 0 {
		residual = 0
	}
	if p.prompt != "" {
		out = append(out, ContextSection{
			Key:        "task_prompt",
			Source:     "daemon.BuildPrompt",
			Delivery:   deliveryProviderPrompt,
			Injected:   true,
			Bytes:      residual,
			TokenCount: estimateTokensLength(residual),
			Digest:     digestString(p.prompt),
			Preview:    redactedPreview(p.prompt),
			Raw:        redactForObservation(p.prompt),
		})
	}

	// Metadata-only delivered sections: skills and sidecar files land in the
	// workdir. Their text is not in the prompt, so injected=false and the token
	// count stays zero; the preview carries the count.
	if n := skillCount(p.task); n > 0 {
		out = append(out, ContextSection{
			Key:      "skills",
			Source:   "execenv.skills",
			Delivery: deliveryWorkdirFile,
			Injected: false,
			Preview:  pluralCount(n, "skill", "skills"),
			Digest:   "",
		})
	}
	// Sidecar files written by writeContextFiles (issue_context.md,
	// daemon_task_context.json, project resources). Counted, not tokenized.
	if sidecarCount := countSidecars(p.task); sidecarCount > 0 {
		out = append(out, ContextSection{
			Key:      "sidecar",
			Source:   "execenv.writeContextFiles",
			Delivery: deliveryWorkdirFile,
			Injected: false,
			Preview:  pluralCount(sidecarCount, "file", "files"),
		})
	}

	return out
}

// sectionFromText builds a section from a source string, redacting both the
// preview and the stored raw so no secret ever lands in the DB payload.
func sectionFromText(key, source, delivery string, injected bool, text string) ContextSection {
	return ContextSection{
		Key:        key,
		Source:     source,
		Delivery:   delivery,
		Injected:   injected,
		Bytes:      len(text),
		TokenCount: estimateTokens(text),
		Digest:     digestString(text),
		Preview:    redactedPreview(text),
		Raw:        redactForObservation(text),
	}
}

func issueSourceText(t Task) string {
	if t.CurrentIssueTitle == "" && t.CurrentIssueDescription == "" {
		return ""
	}
	return t.CurrentIssueTitle + "\n" + t.CurrentIssueDescription
}

func handoffSourceText(t Task) string {
	var b strings.Builder
	if t.HandoffNote != "" {
		b.WriteString(t.HandoffNote)
	}
	if len(t.HandoffSummary) > 0 && string(t.HandoffSummary) != "null" {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		// Compact JSON; if marshalling fails, fall back to the raw bytes.
		if compact, err := compactJSON(t.HandoffSummary); err == nil {
			b.Write(compact)
		} else {
			b.Write(t.HandoffSummary)
		}
	}
	return b.String()
}

func coalescedCommentsText(t Task) string {
	if len(t.CoalescedComments) == 0 {
		return ""
	}
	var b strings.Builder
	for i, cc := range t.CoalescedComments {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString(cc.Content)
	}
	return b.String()
}

func skillCount(t Task) int {
	if t.Agent == nil {
		return 0
	}
	return len(t.Agent.Skills)
}

// countSidecars reports how many sidecar files this task's kind writes. It
// mirrors execenv.writeContextFiles' branch shape without reproducing its I/O.
func countSidecars(t Task) int {
	n := 0
	// issue_context.md carries assignment + checkpoint for issue-bound runs.
	if t.IssueID != "" {
		n++ // .agent_context/issue_context.md
	}
	// daemon_task_context.json is the minimal identity marker.
	n++ // .multica/daemon_task_context.json
	// project resources sidecar when the task carries a project with resources.
	if t.ProjectID != "" && len(t.ProjectResources) > 0 {
		n++ // .multica/project/resources.json
	}
	return n
}

// estimateTokens approximates token count from rune count. The codebase's own
// ancestor budget uses ~4 runes per token for mixed Markdown/CJK (a 32768-rune
// budget maps to the 8192-token ancestor allowance), so this matches that
// convention. Always an estimate; the provider's reported usage overrides it.
func estimateTokens(s string) int {
	if s == "" {
		return 0
	}
	return (utf8.RuneCountInString(s) + 3) / 4
}

// estimateTokensLength converts a byte length straight to the same estimate.
func estimateTokensLength(byteLen int) int {
	if byteLen <= 0 {
		return 0
	}
	// Bytes overcount CJK (3 bytes/rune) and undercount nothing meaningful for
	// the residual, which is mostly ASCII wrapper text; 4 bytes/token holds.
	return (byteLen + 3) / 4
}

func digestString(s string) string {
	if s == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:8]
}

// ---- redaction ----------------------------------------------------------
//
// Previews and stored raw are both redacted: tokens, secrets, emails, and
// absolute paths are removed before anything is persisted or surfaced. IDs
// (issue/task/agent/runtime) are structural and kept. This mirrors the
// courseware dump's redaction rules so the observation never carries a secret.
//
// Secret patterns (API keys, bearer tokens, JWTs, connection strings) are
// scrubbed by the shared redact.Text, which is the house scrubber for
// transcripts. The observation surface additionally redacts emails and absolute
// paths — redact.Text deliberately leaves those to the auth layer, but the
// issue requires the observation to withhold them explicitly.

var (
	reEmail   = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)
	// reAbsPath matches any absolute-path token (a leading slash followed by a
	// name char) regardless of what precedes it, so redaction survives the
	// placeholder reshuffling redact.Text performs on the surrounding text.
	reAbsPath = regexp.MustCompile(`(/[A-Za-z0-9][A-Za-z0-9_\-./]*)`)
	// reSecret is a narrow net for provider/API key prefixes redact.Text may
	// not classify; it deliberately does not match generic long alphanumeric
	// runs (those would erase legitimate content like UUIDs).
	reSecret = regexp.MustCompile(`(?i)(sk-[A-Za-z0-9_-]{16,}|Bearer\s+[A-Za-z0-9._\-]{16,})`)
)

// redactForObservation scrubs secrets, emails, and absolute paths. Structural
// content and markers ([NEW COMMENT], [Background source: Issue X], [truncated])
// survive. The name avoids clashing with the imported redact package.
func redactForObservation(s string) string {
	s = redact.Text(s)
	s = reSecret.ReplaceAllString(s, "[REDACTED]")
	s = reEmail.ReplaceAllString(s, "[REDACTED:email]")
	s = reAbsPath.ReplaceAllString(s, "[REDACTED:path]")
	return s
}

// redactedPreview returns a short redacted excerpt for list/drawer summaries.
const maxPreviewRunes = 200

func redactedPreview(s string) string {
	if s == "" {
		return ""
	}
	r := []rune(s)
	if len(r) > maxPreviewRunes {
		r = r[:maxPreviewRunes]
	}
	preview := string(r)
	if utf8.RuneCountInString(s) > maxPreviewRunes {
		preview += "…"
	}
	return redactForObservation(preview)
}

func pluralCount(n int, singular, plural string) string {
	if n == 1 {
		return "1 " + singular
	}
	return strconv.Itoa(n) + " " + plural
}

// compactJSON returns compact JSON for storage without changing semantics.
func compactJSON(raw json.RawMessage) ([]byte, error) {
	var v interface{}
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, err
	}
	return json.Marshal(v)
}
