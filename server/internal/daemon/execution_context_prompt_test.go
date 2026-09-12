package daemon

import (
	"strings"
	"testing"
)

// TestBuildExecutionContextBlockFiltersAndRenders verifies the SIY-177 prompt
// block: OS-scoped entries outside this runtime are dropped, entries render
// once each with provenance, and the no-copy instruction is present.
func TestBuildExecutionContextBlockFiltersAndRenders(t *testing.T) {
	thisOS := executionContextOS
	otherOS := "windows"
	if thisOS == "windows" {
		otherOS = "linux"
	}

	task := Task{
		ExecutionContextEntries: []ExecutionContextEntryResolved{
			{ExecutionContextEntry: ExecutionContextEntry{Kind: "repo", Value: "https://github.com/acme/widget"}, SourceIssueID: "root-id"},
			{ExecutionContextEntry: ExecutionContextEntry{Kind: "code_dir", Value: "/mnt/a/widget", OS: thisOS}, SourceIssueID: "root-id"},
			{ExecutionContextEntry: ExecutionContextEntry{Kind: "code_dir", Value: `C:\dev\widget`, OS: otherOS}, SourceIssueID: "root-id"},
			{ExecutionContextEntry: ExecutionContextEntry{Kind: "command", Label: "restart", Value: "./restart.sh"}, SourceIssueID: "leaf-id"},
		},
		ExecutionContextContents: []ExecutionContextContent{
			{Text: "镜像仓库：https://gitlab.com/acme/widget-mirror\n启动命令：make dev\n\n测试：pnpm test", SourceIssueID: "root-id"},
		},
		ExecutionContextSources: []ExecutionContextSource{
			{IssueID: "root-id", Identifier: "SIY-30", Version: 2},
			{IssueID: "leaf-id", Title: "Leaf issue", Version: 1},
		},
	}

	block := buildExecutionContextBlock(task)
	if block == "" {
		t.Fatalf("block must render")
	}
	if !strings.HasPrefix(block, "## Execution Context") {
		t.Fatalf("block heading missing: %q", block)
	}
	if strings.Contains(block, `C:\dev\widget`) {
		t.Fatalf("an entry scoped to another OS must be filtered out: %q", block)
	}
	if !strings.Contains(block, "/mnt/a/widget") {
		t.Fatalf("this runtime's scoped entry must render: %q", block)
	}
	// Each distinct value renders exactly once.
	if strings.Count(block, "https://github.com/acme/widget") != 1 {
		t.Fatalf("repo value must render once: %q", block)
	}
	// Provenance comes from the entry's own source.
	if !strings.Contains(block, "(from SIY-30 · v2)") {
		t.Fatalf("identifier+version provenance missing: %q", block)
	}
	if !strings.Contains(block, "(from Leaf issue · v1)") {
		t.Fatalf("title fallback provenance missing: %q", block)
	}
	// The free-form content renders as a quoted block with provenance.
	if !strings.Contains(block, "### Inherited context from SIY-30 (v2)") {
		t.Fatalf("content block header missing: %q", block)
	}
	if !strings.Contains(block, "> 测试：pnpm test") {
		t.Fatalf("content text missing: %q", block)
	}
	if !strings.Contains(block, ">\n") {
		t.Fatalf("blank line inside the content block must stay quoted: %q", block)
	}
	if !strings.Contains(block, "Never re-copy") {
		t.Fatalf("the no-copy instruction must be part of the block")
	}
}

func TestBuildExecutionContextBlockEmpty(t *testing.T) {
	if got := buildExecutionContextBlock(Task{}); got != "" {
		t.Fatalf("empty task must render nothing, got %q", got)
	}
	// Every entry scoped to a different OS → nothing renders.
	task := Task{ExecutionContextEntries: []ExecutionContextEntryResolved{
		{ExecutionContextEntry: ExecutionContextEntry{Kind: "code_dir", Value: "/x", OS: "openvms"}},
	}}
	if got := buildExecutionContextBlock(task); got != "" {
		t.Fatalf("unknown-OS entries fail closed and must render nothing, got %q", got)
	}
	// Content-only tasks still render.
	contentOnly := Task{ExecutionContextContents: []ExecutionContextContent{
		{Text: "启动：./restart.sh", SourceIssueID: "root-id"},
	}}
	if got := buildExecutionContextBlock(contentOnly); !strings.Contains(got, "./restart.sh") {
		t.Fatalf("content-only task must render, got %q", got)
	}
}

// TestBuildContextSectionsExecutionContextVerbatim is the SIY-177 feedback
// regression: the user's execution-context paths must reach the provider
// prompt VERBATIM, and the observation's execution_context audit section must
// store the same verbatim text (no [REDACTED:path] in the audit copy that
// would make it look like the agent cannot see the real paths).
func TestBuildContextSectionsExecutionContextVerbatim(t *testing.T) {
	realPath := "'/mnt/a-opensource-tools/Toonflow/Toonflow-web'"
	task := Task{
		IssueID:                 "issue-1",
		CurrentIssueTitle:       "T",
		CurrentIssueDescription: "D",
		ExecutionContextContents: []ExecutionContextContent{
			{Text: "前端根目录：\n" + realPath, SourceIssueID: "root-id"},
		},
		ExecutionContextSources: []ExecutionContextSource{
			{IssueID: "root-id", Identifier: "SIY-38", Version: 1},
		},
	}

	prompt := BuildPrompt(task, "claude")
	if !strings.Contains(prompt, realPath) {
		t.Fatalf("provider prompt must carry the execution-context path verbatim")
	}

	sections := buildContextSections(observationParams{
		task:         task,
		provider:     "claude",
		runtimeID:    "rt-1",
		runtimeBrief: "# brief\n",
		prompt:       prompt,
	})
	var execSection *ContextSection
	for i := range sections {
		if sections[i].Key == "execution_context" {
			execSection = &sections[i]
		}
	}
	if execSection == nil {
		t.Fatalf("execution_context section missing from observation")
	}
	if !strings.Contains(execSection.Raw, realPath) {
		t.Fatalf("execution_context audit copy must be verbatim, got %q", execSection.Raw)
	}
	if strings.Contains(execSection.Raw, "[REDACTED:path]") {
		t.Fatalf("execution_context audit copy must not redact paths")
	}
	// Every section is verbatim now (SIY-180 owner decision) — the ancestor
	// audit copy shows the real path too; no [REDACTED:path] anywhere.
	task2 := task
	task2.AncestorBrief = "代码地址：/mnt/some/repo\n"
	task2.ExecutionContextContents = nil
	sections2 := buildContextSections(observationParams{
		task:         task2,
		provider:     "claude",
		runtimeID:    "rt-1",
		runtimeBrief: "# brief\n",
		prompt:       BuildPrompt(task2, "claude"),
	})
	for i := range sections2 {
		if sections2[i].Key == "ancestor" && !strings.Contains(sections2[i].Raw, "/mnt/some/repo") {
			t.Fatalf("ancestor section must be verbatim, got %q", sections2[i].Raw)
		}
		if sections2[i].Key == "task_prompt" && strings.Contains(sections2[i].Raw, "[REDACTED:path]") {
			t.Fatalf("task_prompt audit copy must not redact paths, got %q", sections2[i].Raw)
		}
	}
}
