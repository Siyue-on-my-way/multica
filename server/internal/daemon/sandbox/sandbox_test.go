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
}
