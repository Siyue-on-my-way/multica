package sandbox

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// Manifest represents the parsed manifest.yaml of a skill bundle.
type Manifest struct {
	Name            string         `yaml:"name"`
	Description     string         `yaml:"description"`
	Entrypoint      string         `yaml:"entrypoint"`
	Runtime         string         `yaml:"runtime"`
	Image           string         `yaml:"image"`
	RequiredSecrets []string       `yaml:"required_secrets"`
	Tools           []ManifestTool `yaml:"tools"`
}

// ManifestTool represents a tool exposed by the skill.
type ManifestTool struct {
	Name        string      `yaml:"name"`
	Description string      `yaml:"description"`
	Parameters  []ToolParam `yaml:"parameters,omitempty"`
}

// ToolParam describes a named parameter that the agent passes when invoking a tool.
type ToolParam struct {
	Name        string `yaml:"name"`
	Type        string `yaml:"type"`
	Description string `yaml:"description"`
	Required    bool   `yaml:"required"`
}

// ParseManifest parses a manifest.yaml file content.
func ParseManifest(content []byte) (*Manifest, error) {
	var m Manifest
	if err := yaml.Unmarshal(content, &m); err != nil {
		return nil, fmt.Errorf("failed to parse manifest: %w", err)
	}
	return &m, nil
}

// ToolUsageMarkdown builds a markdown section that describes each tool so the
// agent discovers them from SKILL.md and invokes them via `multica skill run`.
func ToolUsageMarkdown(m *Manifest, skillDir string) string {
	if len(m.Tools) == 0 {
		return ""
	}
	s := "\n## Available Tools\n\n"
	s += fmt.Sprintf(
		"Invoke a tool with: `multica skill run %s --skill-dir %s --tool <name> [--arg key=value ...]`\n\n",
		m.Name, skillDir,
	)
	for _, t := range m.Tools {
		s += fmt.Sprintf("### `%s`\n\n%s\n\n", t.Name, t.Description)
		if len(t.Parameters) > 0 {
			s += "**Parameters:**\n\n"
			for _, p := range t.Parameters {
				req := ""
				if p.Required {
					req = " (required)"
				}
				s += fmt.Sprintf("- `%s` (%s)%s — %s\n", p.Name, p.Type, req, p.Description)
			}
			s += "\n"
		}
	}
	s += "**Example:**\n```bash\nmultica skill run my-skill --tool my_tool --arg input=hello\n```\n"
	return s
}

// ExecArgs builds the argument list for running a tool inside the sandbox.
// toolArgs are key=value pairs provided by the agent at invocation time.
func (m *Manifest) ExecArgs(toolName string, toolArgs map[string]string) []string {
	for _, t := range m.Tools {
		if t.Name == toolName {
			args := []string{m.Entrypoint, "--tool", toolName}
			for _, p := range t.Parameters {
				if v, ok := toolArgs[p.Name]; ok {
					args = append(args, "--"+p.Name, v)
				} else if p.Required {
					args = append(args, "--"+p.Name, "")
				}
			}
			// Append any unrecognised positional kv pairs so ad-hoc tools work too.
			for k, v := range toolArgs {
				found := false
				for _, p := range t.Parameters {
					if p.Name == k {
						found = true
						break
					}
				}
				if !found {
					args = append(args, "--"+k, v)
				}
			}
			return args
		}
	}
	// Unknown tool: pass everything positionally.
	args := []string{m.Entrypoint, "--tool", toolName}
	for k, v := range toolArgs {
		args = append(args, "--"+k, v)
	}
	return args
}