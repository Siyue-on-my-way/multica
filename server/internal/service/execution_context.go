package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Structured execution context (SIY-177).
//
// Execution context is the machine-actionable subset of an issue that a task
// needs before it can run: repository URLs, per-OS code directory mappings,
// start/test/deploy commands and run constraints. It is stored as a first-class
// JSONB column on issue (migration 453) — never as prose inside description —
// so the runtime can merge it along the ancestor chain, de-duplicate by
// issue+version, and filter OS-specific entries without parsing free text.
//
// Two rules make the inheritance chain stay clean:
//
//  1. An issue's entries reach its descendants only when the author left the
//     "inherit" switch on (the create-form default). The issue's own entries
//     always apply to its own runs regardless of the switch.
//  2. Merging walks the chain root-first; a descendant that re-declares the
//     same kind+label overrides the inherited value, and a re-assertion of an
//     identical value collapses onto the original (one prompt line, the
//     root-most source). A resolved entry therefore appears exactly once in
//     the final prompt no matter how deep the chain is.
const (
	ExecutionContextKindRepo       = "repo"
	ExecutionContextKindCodeDir    = "code_dir"
	ExecutionContextKindCommand    = "command"
	ExecutionContextKindConstraint = "constraint"
	ExecutionContextKindResource   = "resource"
)

// Caps bound a hostile or accidental oversized payload. The resolved context
// rides on every claim of every descendant, so one ancestor must not be able
// to tax the whole subtree.
const (
	ExecutionContextMaxEntries    = 32
	ExecutionContextValueMaxRunes = 2000
	ExecutionContextLabelMaxRunes = 120
	// ExecutionContextContentMaxRunes bounds the free-form longtext the create
	// form carries (SIY-177 feedback: users write whatever they want to
	// inherit, not a rigid entry list). Comfortably larger than any real
	// setup text, small enough that a pathological ancestor cannot tax the
	// subtree's prompt budget.
	ExecutionContextContentMaxRunes = 20000
	ExecutionContextVersionMax      = 1 << 30
	ExecutionContextChainMaxDepth   = ancestorBriefMaxDepth
	executionContextJSONMaxBytes    = 256 << 10
)

var executionContextKinds = map[string]struct{}{
	ExecutionContextKindRepo:       {},
	ExecutionContextKindCodeDir:    {},
	ExecutionContextKindCommand:    {},
	ExecutionContextKindConstraint: {},
	ExecutionContextKindResource:   {},
}

// ExecutionContextEntry is one line of machine-usable context. OS ("", "*",
// linux, macos, windows) scopes a path or command to one platform: a code_dir
// entry tagged linux must never surface on a Windows runtime and vice versa.
type ExecutionContextEntry struct {
	Kind  string `json:"kind"`
	Label string `json:"label,omitempty"`
	Value string `json:"value"`
	OS    string `json:"os,omitempty"`
}

// ExecutionContext is the persisted document.
//
// Content is the free-form longtext users write in the create form — whatever
// they want every descendant to inherit (repo URLs, per-OS directories,
// commands, constraints, anything). Entries remain the structured, OS-scoped
// form used by programmatic writers and synthesized project resources; a
// document may carry either or both.
type ExecutionContext struct {
	Version int                     `json:"version"`
	Inherit bool                    `json:"inherit"`
	Content string                  `json:"content,omitempty"`
	Entries []ExecutionContextEntry `json:"entries,omitempty"`
}

// ExecutionContextSource records where a resolved entry came from. The
// issue_id + version pair is the dedup key the acceptance criteria ask for: a
// descendant run can tell whether it is seeing the same snapshot an earlier
// run already consumed. Sources carry the newest writer of each entry — an
// override moves the provenance to the overriding issue.
type ExecutionContextSource struct {
	IssueID    string `json:"issue_id,omitempty"`
	Identifier string `json:"identifier,omitempty"`
	Title      string `json:"title,omitempty"`
	Version    int    `json:"version"`
	UpdatedAt  string `json:"updated_at,omitempty"`
	// Project is true when the source is a project resource rather than an
	// issue along the chain.
	Project bool `json:"project,omitempty"`
}

// ExecutionContextEntryResolved is the claim-wire and preview form of one
// resolved entry: the entry plus the provenance of the chain node whose
// value won the merge (root-most original for re-asserted content, the
// overriding node after a value change). On the wire the embedded struct's
// fields stay flat.
type ExecutionContextEntryResolved struct {
	ExecutionContextEntry
	SourceIssueID string `json:"source_issue_id,omitempty"`
}

// ResolvedContent is one free-form content block that won the merge: the
// text plus the provenance of the chain node that wrote it. Identical text
// re-asserted deeper in the chain collapses onto the root-most occurrence.
type ResolvedContent struct {
	Text          string `json:"text"`
	SourceIssueID string `json:"source_issue_id,omitempty"`
}

// ResolvedExecutionContext is the merged, de-duplicated view delivered to the
// preview panel and (as entries + sources) on the claim wire.
type ResolvedExecutionContext struct {
	Entries  []ExecutionContextEntryResolved `json:"entries"`
	Contents []ResolvedContent               `json:"contents,omitempty"`
	Sources  []ExecutionContextSource        `json:"sources"`
}

// Empty reports whether the resolution carries nothing at all — no entries,
// no contents and no sources — so the preview panel can skip the
// inherited-context card.
func (r ResolvedExecutionContext) Empty() bool {
	return len(r.Entries) == 0 && len(r.Contents) == 0 && len(r.Sources) == 0
}

// ParseExecutionContext tolerantly decodes the persisted JSONB. NULL/absent
// decodes to the zero document (no entries). Unknown JSON fields are ignored
// so the shape can evolve without breaking old readers. A document that never
// recorded the switch reads as inherit=true — the create form's default.
func ParseExecutionContext(raw []byte) ExecutionContext {
	return parseExecutionContextWithInherit(raw, true)
}

func parseExecutionContextWithInherit(raw []byte, defaultInherit bool) ExecutionContext {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return ExecutionContext{Inherit: defaultInherit}
	}
	if len(trimmed) > executionContextJSONMaxBytes {
		return ExecutionContext{Inherit: defaultInherit}
	}
	var doc struct {
		Version *int                    `json:"version"`
		Inherit *bool                   `json:"inherit"`
		Content *string                 `json:"content"`
		Entries []ExecutionContextEntry `json:"entries"`
	}
	if err := json.Unmarshal([]byte(trimmed), &doc); err != nil {
		return ExecutionContext{Inherit: defaultInherit}
	}
	out := ExecutionContext{Inherit: defaultInherit}
	if doc.Inherit != nil {
		out.Inherit = *doc.Inherit
	}
	if doc.Version != nil && *doc.Version > 0 {
		out.Version = minInt(*doc.Version, ExecutionContextVersionMax)
	}
	if doc.Content != nil {
		out.Content = clampRunes(strings.TrimSpace(*doc.Content), ExecutionContextContentMaxRunes)
	}
	out.Entries = normalizeExecutionContextEntries(doc.Entries)
	return out
}

// normalizeExecutionContextEntries clamps, whitelists and de-noises a raw
// entry list. Entries without a value are dropped; unknown kinds collapse to
// "constraint" (they were free-form guidance); unknown OS tags are kept
// verbatim — the OS filter fails closed on them (see
// ExecutionContextEntryForOS) so a mistyped tag can never leak a
// platform-specific path onto the wrong OS.
func normalizeExecutionContextEntries(entries []ExecutionContextEntry) []ExecutionContextEntry {
	if len(entries) > ExecutionContextMaxEntries {
		entries = entries[:ExecutionContextMaxEntries]
	}
	out := make([]ExecutionContextEntry, 0, len(entries))
	for _, entry := range entries {
		entry.Value = strings.TrimSpace(clampRunes(entry.Value, ExecutionContextValueMaxRunes))
		if entry.Value == "" {
			continue
		}
		entry.Kind = strings.TrimSpace(entry.Kind)
		if _, ok := executionContextKinds[entry.Kind]; !ok {
			entry.Kind = ExecutionContextKindConstraint
		}
		entry.Label = strings.TrimSpace(clampRunes(entry.Label, ExecutionContextLabelMaxRunes))
		entry.OS = normalizeExecutionContextOS(entry.OS)
		out = append(out, entry)
	}
	return out
}

func clampRunes(value string, max int) string {
	runes := []rune(value)
	if len(runes) <= max {
		return value
	}
	return string(runes[:max])
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// normalizeExecutionContextOS canonicalizes the platform tag. Empty and "*"
// mean "every OS". darwin/osx fold into macos; win into windows. Anything else
// is lowercased and kept — an unknown tag will not match a real runtime, which
// is the safe direction for a platform-specific value.
func normalizeExecutionContextOS(os string) string {
	os = strings.ToLower(strings.TrimSpace(os))
	switch os {
	case "", "*", "all", "any":
		return ""
	case "darwin", "osx", "mac":
		return "macos"
	case "win":
		return "windows"
	default:
		return os
	}
}

// ExecutionContextEntryForOS reports whether an entry applies to the named
// runtime OS (canonical form: linux / macos / windows). Untagged entries are
// universal. Unknown tags fail closed.
func ExecutionContextEntryForOS(entry ExecutionContextEntry, os string) bool {
	if entry.OS == "" {
		return true
	}
	if os == "" {
		return false
	}
	return entry.OS == os
}

// NormalizeExecutionContextForWrite validates a client-supplied document for
// persistence and stamps the authoritative version. The JSON must decode to an
// object; entries are clamped; the version is set server-side so a stale or
// forged client version cannot regress provenance.
func NormalizeExecutionContextForWrite(raw []byte, version int) ([]byte, ExecutionContext, error) {
	if version < 1 {
		version = 1
	}
	if version > ExecutionContextVersionMax {
		version = ExecutionContextVersionMax
	}
	if len(raw) > executionContextJSONMaxBytes {
		return nil, ExecutionContext{}, fmt.Errorf("execution context too large (max %d bytes)", executionContextJSONMaxBytes)
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil, ExecutionContext{}, fmt.Errorf("execution context must be a JSON object: %w", err)
	}
	doc := parseExecutionContextWithInherit(raw, true)
	doc.Version = version
	encoded, err := json.Marshal(doc)
	if err != nil {
		return nil, ExecutionContext{}, err
	}
	return encoded, doc, nil
}

// ExecutionContextChainNode is one issue along the resolved chain, ordered
// root-most first. Apply is false for ancestors whose author switched
// inheritance off; the current issue's node always applies (its own switch
// governs only its descendants).
type ExecutionContextChainNode struct {
	IssueID    string
	Identifier string
	Title      string
	UpdatedAt  string
	Context    ExecutionContext
	Apply      bool
}

// ResolveExecutionContextChain merges the nodes in the given order (most
// shared first). Entries are keyed by kind+label: a later node with the same
// key but different content overrides the earlier entry (and moves its
// provenance); an identical re-assertion collapses onto the earlier occurrence
// so the entry is injected exactly once. Content blocks follow the same
// collapse rule on exact text: a descendant re-asserting its ancestor's text
// verbatim adds nothing; different text survives as an additional block, each
// with its own provenance.
func ResolveExecutionContextChain(nodes []ExecutionContextChainNode) ResolvedExecutionContext {
	type keyed struct {
		entry  ExecutionContextEntryResolved
		source ExecutionContextSource
	}
	order := make([]string, 0, len(nodes))
	byKey := make(map[string]keyed)
	type contentBlock struct {
		resolved ResolvedContent
		source   ExecutionContextSource
	}
	contents := make([]contentBlock, 0, len(nodes))
	contentSeen := make(map[string]struct{}, len(nodes))

	for _, node := range nodes {
		if !node.Apply {
			continue
		}
		source := ExecutionContextSource{
			IssueID:    node.IssueID,
			Identifier: node.Identifier,
			Title:      node.Title,
			Version:    node.Context.Version,
			UpdatedAt:  node.UpdatedAt,
		}
		if text := strings.TrimSpace(node.Context.Content); text != "" {
			if _, ok := contentSeen[text]; !ok {
				contentSeen[text] = struct{}{}
				contents = append(contents, contentBlock{
					resolved: ResolvedContent{Text: text, SourceIssueID: node.IssueID},
					source:   source,
				})
			}
		}
		for _, entry := range node.Context.Entries {
			key := entry.Kind + "\x00" + strings.ToLower(entry.Label)
			existing, ok := byKey[key]
			if ok && existing.entry.Value == entry.Value {
				continue
			}
			if !ok {
				order = append(order, key)
			}
			byKey[key] = keyed{
				entry:  ExecutionContextEntryResolved{ExecutionContextEntry: entry, SourceIssueID: node.IssueID},
				source: source,
			}
		}
	}

	resolved := ResolvedExecutionContext{
		Entries:  make([]ExecutionContextEntryResolved, 0, len(order)),
		Contents: make([]ResolvedContent, 0, len(contents)),
		Sources:  make([]ExecutionContextSource, 0),
	}
	sourceSeen := make(map[string]struct{}, len(order)+len(contents))
	appendSource := func(source ExecutionContextSource) {
		if _, ok := sourceSeen[source.IssueID]; !ok {
			sourceSeen[source.IssueID] = struct{}{}
			resolved.Sources = append(resolved.Sources, source)
		}
	}
	for _, block := range contents {
		resolved.Contents = append(resolved.Contents, block.resolved)
		appendSource(block.source)
	}
	for _, key := range order {
		item := byKey[key]
		resolved.Entries = append(resolved.Entries, item.entry)
		appendSource(item.source)
	}
	return resolved
}

// ChainNodeFromIssue renders one db.Issue as a chain node. apply is true when
// the node is the current issue or its author left inheritance on.
func ChainNodeFromIssue(issue db.Issue, identifier string, apply bool) ExecutionContextChainNode {
	return ExecutionContextChainNode{
		IssueID:    util.UUIDToString(issue.ID),
		Identifier: identifier,
		Title:      issue.Title,
		UpdatedAt:  issue.UpdatedAt.Time.UTC().Format("2006-01-02T15:04:05Z"),
		Context:    ParseExecutionContext(issue.ExecutionContext),
		Apply:      apply,
	}
}

// ProjectResourceExecutionEntries synthesizes context entries from a project's
// repository resources so the preview panel can show what the project layer
// contributes. resource_ref for a github_repo is {"url": ...} — other types
// have no execution meaning yet and are skipped.
func ProjectResourceExecutionEntries(resources []db.ProjectResource) []ExecutionContextEntry {
	entries := make([]ExecutionContextEntry, 0)
	for _, resource := range resources {
		if resource.ResourceType != "github_repo" {
			continue
		}
		var ref struct {
			URL string `json:"url"`
		}
		if err := json.Unmarshal(resource.ResourceRef, &ref); err != nil || strings.TrimSpace(ref.URL) == "" {
			continue
		}
		entries = append(entries, ExecutionContextEntry{
			Kind:  ExecutionContextKindRepo,
			Label: resource.Label.String,
			Value: ref.URL,
		})
	}
	return entries
}

// ResolveInheritedExecutionContextForIssue resolves the merged execution
// context for one issue: the project's resources (least specific), then the
// ancestor chain root-first — keeping only ancestors whose author left
// inheritance on — and finally the issue's own entries. Sources carry each
// contributing issue's own identifier. The walk is bounded by the same depth
// cap as the ancestor brief and fails soft like it: a broken chain link
// yields a partial context rather than an error.
func ResolveInheritedExecutionContextForIssue(
	ctx context.Context,
	reader IssueReader,
	issue db.Issue,
	prefix string,
	projectEntries []ExecutionContextEntry,
) ResolvedExecutionContext {
	nodes := make([]ExecutionContextChainNode, 0)

	if len(projectEntries) > 0 {
		nodes = append(nodes, ExecutionContextChainNode{
			Context: ExecutionContext{Entries: projectEntries},
			Apply:   true,
		})
	}

	// Ancestors collected leaf→root, then reversed so the merge sees the
	// root-most (most shared) entries first.
	ancestors := make([]ExecutionContextChainNode, 0, 4)
	parentID := issue.ParentIssueID
	seen := make(map[string]struct{})
	for depth := 0; parentID.Valid && depth < ExecutionContextChainMaxDepth; depth++ {
		id := util.UUIDToString(parentID)
		if id == "" {
			break
		}
		if _, ok := seen[id]; ok {
			break
		}
		seen[id] = struct{}{}
		ancestor, err := reader.GetIssue(ctx, parentID)
		if err != nil || ancestor.WorkspaceID != issue.WorkspaceID {
			break
		}
		ancestorContext := ParseExecutionContext(ancestor.ExecutionContext)
		ancestors = append(ancestors, ExecutionContextChainNode{
			IssueID:    id,
			Identifier: IssueIdentifier(prefix, ancestor.Number),
			Title:      ancestor.Title,
			UpdatedAt:  ancestor.UpdatedAt.Time.UTC().Format("2006-01-02T15:04:05Z"),
			Context:    ancestorContext,
			Apply:      ancestorContext.Inherit,
		})
		parentID = ancestor.ParentIssueID
	}
	for i := len(ancestors) - 1; i >= 0; i-- {
		nodes = append(nodes, ancestors[i])
	}

	// The current issue contributes its own entries unconditionally.
	nodes = append(nodes, ChainNodeFromIssue(issue, IssueIdentifier(prefix, issue.Number), true))

	return ResolveExecutionContextChain(nodes)
}

// SubissueExecutionContextPromptBudget caps the execution-context text fed to
// the sub-issue generation pass (SIY-177 feedback: the generation prompt
// carries the merged execution context, never the ancestor descriptions).
const SubissueExecutionContextPromptBudget = 4000

// RenderExecutionContextForPrompt renders the resolved context as bounded
// prompt text: OS-filtered entries first, then the free-form content blocks,
// each tagged with its source. Nothing here duplicates ancestor descriptions —
// the caller feeds this INSTEAD of the ancestor brief.
func RenderExecutionContextForPrompt(resolved ResolvedExecutionContext, maxRunes int) string {
	if resolved.Empty() {
		return ""
	}
	var b strings.Builder
	b.WriteString("执行上下文（系统已按继承链合并去重，来源标注在括号内；子issue运行时会自动获得这些信息，不要复制进 description）：\n")
	for _, entry := range resolved.Entries {
		kind := entry.Kind
		if entry.OS != "" {
			kind += "(" + entry.OS + ")"
		}
		if entry.Label != "" {
			fmt.Fprintf(&b, "- [%s] %s: %s", kind, entry.Label, entry.Value)
		} else {
			fmt.Fprintf(&b, "- [%s] %s", kind, entry.Value)
		}
		if name := resolved.SourceName(entry.SourceIssueID); name != "" {
			fmt.Fprintf(&b, "（来自 %s）", name)
		}
		b.WriteString("\n")
	}
	for _, content := range resolved.Contents {
		name := resolved.SourceName(content.SourceIssueID)
		if name != "" {
			fmt.Fprintf(&b, "继承文本（来自 %s）：\n", name)
		} else {
			b.WriteString("继承文本：\n")
		}
		for _, line := range strings.Split(strings.TrimRight(content.Text, "\n"), "\n") {
			b.WriteString("> ")
			b.WriteString(line)
			b.WriteString("\n")
		}
	}
	text := strings.TrimRight(b.String(), "\n")
	if maxRunes > 0 {
		if runes := []rune(text); len(runes) > maxRunes {
			text = string(runes[:maxRunes]) + "\n…[截断]"
		}
	}
	return text
}

// SourceName returns the display identifier (or title fallback) for a
// contributing issue, used to tag prompt lines with provenance.
func (r ResolvedExecutionContext) SourceName(issueID string) string {
	for _, source := range r.Sources {
		if source.IssueID == issueID {
			if source.Identifier != "" {
				return source.Identifier
			}
			return source.Title
		}
	}
	return ""
}
