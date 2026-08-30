package sandbox

import (
	"fmt"
	"gopkg.in/yaml.v3"
)

// Manifest represents the parsed manifest.yaml of a skill bundle.
type Manifest struct {
	Name            string            `yaml:"name"`
	Description     string            `yaml:"description"`
	Entrypoint      string            `yaml:"entrypoint"`
	Runtime         string            `yaml:"runtime"`
	Image           string            `yaml:"image"`
	RequiredSecrets []string          `yaml:"required_secrets"`
	Tools           []ManifestTool    `yaml:"tools"`
}

// ManifestTool represents a tool exposed by the skill.
type ManifestTool struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
}

// ParseManifest parses a manifest.yaml file content.
func ParseManifest(content []byte) (*Manifest, error) {
	var m Manifest
	if err := yaml.Unmarshal(content, &m); err != nil {
		return nil, fmt.Errorf("failed to parse manifest: %w", err)
	}
	return &m, nil
}
