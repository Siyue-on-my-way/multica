package service

import (
	"strings"
	"testing"
)

// The SIY-180 fidelity contract: whatever the author typed into an ancestor's
// execution context must reach every descendant's resolved context and the
// rendered prompt text byte-for-byte. Paths, backticks, box-drawing tree
// characters, ${...} placeholders and Windows separators are load-bearing
// machine info — no intermediate step may redact, substitute or normalize
// them. (The only sanctioned transformation is the documented trim of the
// content's leading/trailing whitespace at write time; the fixtures below
// carry none, so every assertion below is an exact-match check.)
const execContextFidelityFixture = `## 前端代码

前端根目录：
` + "`/mnt/a-opensource-tools/Toonflow/Toonflow-web`" + `

主要路径：
ToonFlow-web/
  src/
    ├── main.ts    # 前入口
    └── router/index.ts    # 路由

关键文件：
- 【前端入口 (line 01|` + "`/mnt/a-opensource-tools/Toonflow/Toonflow-web/src/main.ts:1`" + `】
Windows 检出：C:\Users\dev\ToonFlow-web
Env 占位符：${MULTICA_LLM_BASE_URL} 与 ${MULTICA_LLM_API_KEY}
启动命令：./restart.sh --build`

func fixtureFidelityLines() []string {
	lines := strings.Split(execContextFidelityFixture, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(line) != "" {
			out = append(out, line)
		}
	}
	return out
}

// TestExecutionContextPipelinePreservesUserContentVerbatim walks the exact
// pipeline a user's execution context travels — write normalization, JSONB
// round trip, three-generation chain resolution, prompt rendering — and pins
// byte-identical survival at every hop (SIY-180 acceptance 1/3: no
// tampering, no loss, no over-redaction).
func TestExecutionContextPipelinePreservesUserContentVerbatim(t *testing.T) {
	// Hop 1: write normalization → JSONB → parse. JSON escaping (backslashes,
	// quotes, CJK, box-drawing characters) must round-trip exactly.
	encoded, _, err := NormalizeExecutionContextForWrite(
		mustMarshal(t, ExecutionContext{Version: 1, Inherit: true, Content: execContextFidelityFixture}), 1)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	stored := ParseExecutionContext(encoded)
	if stored.Content != execContextFidelityFixture {
		t.Fatalf("JSONB round trip altered the author's content:\n got %q\nwant %q", stored.Content, execContextFidelityFixture)
	}

	// Hop 2: three-generation chain. The child re-asserts the root's text
	// verbatim (the pre-SIY-177 copy habit) — it must collapse onto the root's
	// block, and the grandchild's resolution must carry the text exactly once,
	// byte-identical, with root provenance.
	root := ExecutionContextChainNode{
		IssueID: "root", Identifier: "SIY-1",
		Context: ExecutionContext{Version: 1, Inherit: true, Content: execContextFidelityFixture}, Apply: true,
	}
	child := ExecutionContextChainNode{
		IssueID: "child", Identifier: "SIY-2",
		Context: ExecutionContext{Version: 1, Inherit: true, Content: execContextFidelityFixture}, Apply: true,
	}
	grandchild := ExecutionContextChainNode{
		IssueID: "grandchild", Identifier: "SIY-3",
		Context: ExecutionContext{Version: 1, Inherit: true}, Apply: true,
	}
	resolved := ResolveExecutionContextChain([]ExecutionContextChainNode{root, child, grandchild})
	if len(resolved.Contents) != 1 {
		t.Fatalf("identical re-assertion must collapse to one block, got %d", len(resolved.Contents))
	}
	if resolved.Contents[0].Text != execContextFidelityFixture {
		t.Fatalf("chain resolution altered the author's content:\n got %q\nwant %q", resolved.Contents[0].Text, execContextFidelityFixture)
	}
	if resolved.Contents[0].SourceIssueID != "root" {
		t.Fatalf("collapsed block must keep root provenance, got %q", resolved.Contents[0].SourceIssueID)
	}

	// Hop 3: prompt rendering (the generation pass). Every authored line must
	// appear verbatim behind the blockquote marker — no redaction, no
	// placeholder substitution, no line rewriting.
	rendered := RenderExecutionContextForPrompt(resolved, 0)
	for _, line := range fixtureFidelityLines() {
		if !strings.Contains(rendered, "> "+line) {
			t.Fatalf("prompt render lost or altered line %q", line)
		}
	}
	if strings.Contains(rendered, "REDACTED") {
		t.Fatalf("prompt render must not redact execution context: %q", rendered)
	}
}

// TestExecutionContextEntriesSurviveSpecialCharacters pins the structured
// entries' side of the same contract: values carrying backslashes, quotes,
// backticks, ${...} and CJK must survive write normalization + JSONB round
// trip byte-for-byte.
func TestExecutionContextEntriesSurviveSpecialCharacters(t *testing.T) {
	values := []string{
		"/mnt/a-opensource-tools/Toonflow/Toonflow-web",
		`C:\Users\dev\ToonFlow-web\src`,
		"`/mnt/repo/src/main.ts:1`",
		"【前端入口 (line 01|`/mnt/repo/src/main.ts:1`】",
		"${MULTICA_LLM_BASE_URL}",
		"├── main.ts    # 前入口 └── router",
		`quote " and backslash \ mixed`,
	}
	entries := make([]ExecutionContextEntry, 0, len(values))
	for i, value := range values {
		entries = append(entries, ExecutionContextEntry{Kind: ExecutionContextKindCodeDir, Label: "dir", Value: value})
		_ = i
	}
	encoded, _, err := NormalizeExecutionContextForWrite(
		mustMarshal(t, ExecutionContext{Version: 1, Inherit: true, Entries: entries}), 1)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	parsed := ParseExecutionContext(encoded)
	if len(parsed.Entries) != len(values) {
		t.Fatalf("entry count changed: %d → %d", len(values), len(parsed.Entries))
	}
	for i, want := range values {
		if got := parsed.Entries[i].Value; got != want {
			t.Fatalf("entry %d altered:\n got %q\nwant %q", i, got, want)
		}
	}
}
