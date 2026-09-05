package sandbox

import (
	"testing"
)

func TestParseManifest(t *testing.T) {
	content := []byte(`
name: my-skill
description: A test skill
entrypoint: scripts/main.py
runtime: docker
image: python:3.10-slim
required_secrets:
  - API_KEY
tools:
  - name: my_tool
    description: A test tool
    parameters:
      - name: input
        type: string
        description: The input value
        required: true
      - name: verbose
        type: boolean
        description: Enable verbose output
        required: false
`)

	m, err := ParseManifest(content)
	if err != nil {
		t.Fatalf("ParseManifest failed: %v", err)
	}

	if m.Name != "my-skill" {
		t.Errorf("expected name 'my-skill', got %q", m.Name)
	}
	if m.Runtime != "docker" {
		t.Errorf("expected runtime 'docker', got %q", m.Runtime)
	}
	if len(m.RequiredSecrets) != 1 || m.RequiredSecrets[0] != "API_KEY" {
		t.Errorf("expected required_secrets ['API_KEY'], got %v", m.RequiredSecrets)
	}
	if len(m.Tools) != 1 || m.Tools[0].Name != "my_tool" {
		t.Errorf("expected tools ['my_tool'], got %v", m.Tools)
	}
	if len(m.Tools[0].Parameters) != 2 {
		t.Errorf("expected 2 parameters, got %d", len(m.Tools[0].Parameters))
	}
	if m.Tools[0].Parameters[0].Name != "input" || !m.Tools[0].Parameters[0].Required {
		t.Errorf("expected required param 'input', got %+v", m.Tools[0].Parameters[0])
	}
}

func TestToolUsageMarkdown(t *testing.T) {
	m := &Manifest{
		Name: "my-skill",
		Tools: []ManifestTool{
			{
				Name:        "my_tool",
				Description: "A test tool",
				Parameters: []ToolParam{
					{Name: "input", Type: "string", Description: "The input value", Required: true},
				},
			},
		},
	}
	md := ToolUsageMarkdown(m, "/home/user/.multica/skills/my-skill")
	if md == "" {
		t.Fatal("ToolUsageMarkdown returned empty for a manifest with tools")
	}
	if !contains(md, "### `my_tool`") {
		t.Errorf("markdown missing tool heading, got:\n%s", md)
	}
	if !contains(md, "multica skill run") {
		t.Errorf("markdown missing invocation command, got:\n%s", md)
	}
	if !contains(md, "`input`") {
		t.Errorf("markdown missing parameter, got:\n%s", md)
	}
}

func TestToolUsageMarkdownEmpty(t *testing.T) {
	m := &Manifest{Name: "no-tools"}
	md := ToolUsageMarkdown(m, "/tmp/no-tools")
	if md != "" {
		t.Errorf("expected empty string for no tools, got: %s", md)
	}
}

func TestExecArgs(t *testing.T) {
	m := &Manifest{
		Entrypoint: "scripts/main.py",
		Tools: []ManifestTool{
			{
				Name: "my_tool",
				Parameters: []ToolParam{
					{Name: "input", Type: "string", Required: true},
					{Name: "verbose", Type: "boolean", Required: false},
				},
			},
		},
	}

	toolArgs := map[string]string{"input": "hello", "verbose": "true"}
	args := m.ExecArgs("my_tool", toolArgs)
	// args layout: entrypoint, --tool, tool_name, --param1, val1, --param2, val2, ...
	if len(args) < 6 {
		t.Fatalf("expected at least 6 args (entrypoint + --tool name + 2 params), got %d: %v", len(args), args)
	}
	if args[0] != "scripts/main.py" {
		t.Errorf("expected entrypoint first, got %q", args[0])
	}
	if !containsSlice(args, "--input") {
		t.Errorf("expected --input in args: %v", args)
	}
	if !containsSlice(args, "hello") {
		t.Errorf("expected 'hello' in args: %v", args)
	}
	if !containsSlice(args, "--verbose") {
		t.Errorf("expected --verbose in args: %v", args)
	}
	if !containsSlice(args, "true") {
		t.Errorf("expected 'true' in args: %v", args)
	}
}

func TestExecArgsUnknownTool(t *testing.T) {
	m := &Manifest{
		Entrypoint: "scripts/main.py",
	}
	toolArgs := map[string]string{"x": "y"}
	args := m.ExecArgs("unknown_tool", toolArgs)
	if args[0] != "scripts/main.py" || args[1] != "--tool" || args[2] != "unknown_tool" {
		t.Errorf("unexpected args for unknown tool: %v", args)
	}
	if !containsSlice(args, "--x") || !containsSlice(args, "y") {
		t.Errorf("expected --x y in args: %v", args)
	}
}

func contains(s, substr string) bool {
	return len(s) > 0 && len(substr) > 0 && len(s) >= len(substr) &&
		(len(s) == len(substr) || s[0:len(substr)] == substr ||
			containsInMiddle(s, substr))
}

func containsInMiddle(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func containsSlice(s []string, v string) bool {
	for _, e := range s {
		if e == v {
			return true
		}
	}
	return false
}