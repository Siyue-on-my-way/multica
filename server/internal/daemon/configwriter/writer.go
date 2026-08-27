// Package configwriter provides atomic CLI configuration file writers
// for agent provider switching. Each adapter reads the existing config,
// updates the provider fields (base_url, api_key, model), and overwrites
// the file atomically using a write-to-temp-then-rename pattern so
// a crash or signal cannot leave a half-written config behind.
package configwriter

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/pelletier/go-toml/v2"
)

// ProviderConfig is the provider configuration to apply.
type ProviderConfig struct {
	BaseURL string
	APIKey  string
	Model   string
}

// Apply atomically applies cfg to the target CLI's configuration file.
// targetCLI must be one of "codex", "grok", or "claude".
func Apply(targetCLI string, cfg ProviderConfig) error {
	switch targetCLI {
	case "codex":
		return applyCodex(cfg)
	case "grok":
		return applyGrok(cfg)
	case "claude":
		return applyClaudeCode(cfg)
	default:
		return fmt.Errorf("unsupported target CLI %q", targetCLI)
	}
}

// atomicWrite writes data to path by writing to a sibling temp file first
// then renaming into place, ensuring the destination is always intact.
func atomicWrite(path string, data []byte, perm fs.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}

	ts := time.Now().UnixNano()
	var counter atomic.Uint64
	name := filepath.Base(path)
	pid := os.Getpid()

	var tmp string
	var f *os.File
	for range 16 {
		cnt := counter.Add(1)
		candidate := filepath.Join(dir, fmt.Sprintf("%s.tmp.%d.%d.%d", name, pid, ts, cnt))
		var err error
		f, err = os.OpenFile(candidate, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
		if err == nil {
			tmp = candidate
			break
		}
		if !os.IsExist(err) {
			return fmt.Errorf("create temp file: %w", err)
		}
	}
	if tmp == "" {
		return fmt.Errorf("could not create unique temp file for %s", path)
	}

	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("sync temp file: %w", err)
	}
	f.Close()

	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("rename into place: %w", err)
	}
	return nil
}

// ---- Codex adapter ----

func applyCodex(cfg ProviderConfig) error {
	home, _ := os.UserHomeDir()
	path := filepath.Join(home, ".codex", "config.toml")

	// Read the existing config (raw map so we preserve unknown fields).
	raw := map[string]any{}
	if data, err := os.ReadFile(path); err == nil {
		if err := toml.Unmarshal(data, &raw); err != nil {
			// Unreadable config — start fresh.
			raw = map[string]any{}
		}
	}

	// Ensure model_providers map exists and set the "multica" entry.
	providers, _ := raw["model_providers"].(map[string]any)
	if providers == nil {
		providers = map[string]any{}
	}

	entry := map[string]any{
		"name":     "Multica Provider",
		"base_url": cfg.BaseURL,
	}
	if cfg.APIKey != "" {
		entry["api_key"] = cfg.APIKey
	}
	providers["multica"] = entry
	raw["model_providers"] = providers

	// Set the active model provider to "multica".
	raw["model_provider"] = "multica"

	if cfg.Model != "" {
		raw["model"] = cfg.Model
	}

	out, err := toml.Marshal(raw)
	if err != nil {
		return fmt.Errorf("marshal codex config: %w", err)
	}
	return atomicWrite(path, out, 0o600)
}

// ---- Grok adapter ----

// applyGrok writes provider settings into ~/.grok/config.toml.
// We create/update a "multica" model entry under [model."multica"] and set
// models.default = "multica".
func applyGrok(cfg ProviderConfig) error {
	home, _ := os.UserHomeDir()
	path := filepath.Join(home, ".grok", "config.toml")

	raw := map[string]any{}
	if data, err := os.ReadFile(path); err == nil {
		if err := toml.Unmarshal(data, &raw); err != nil {
			raw = map[string]any{}
		}
	}

	// Ensure models section.
	models, _ := raw["models"].(map[string]any)
	if models == nil {
		models = map[string]any{}
	}
	models["default"] = "multica"
	raw["models"] = models

	// Build model entry under [model."multica"].
	modelSection, _ := raw["model"].(map[string]any)
	if modelSection == nil {
		modelSection = map[string]any{}
	}

	entry := map[string]any{
		"base_url":    cfg.BaseURL,
		"api_backend": "responses",
	}
	if cfg.APIKey != "" {
		entry["api_key"] = cfg.APIKey
	}
	if cfg.Model != "" {
		entry["model"] = cfg.Model
	}
	modelSection["multica"] = entry
	raw["model"] = modelSection

	out, err := toml.Marshal(raw)
	if err != nil {
		return fmt.Errorf("marshal grok config: %w", err)
	}
	return atomicWrite(path, out, 0o600)
}

// ---- Claude Code adapter ----

func applyClaudeCode(cfg ProviderConfig) error {
	home, _ := os.UserHomeDir()
	path := filepath.Join(home, ".claude.json")

	raw := map[string]any{}
	if data, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(data, &raw); err != nil {
			raw = map[string]any{}
		}
	}

	raw["apiUrl"] = cfg.BaseURL
	if cfg.APIKey != "" {
		raw["authKey"] = cfg.APIKey
	}
	if cfg.Model != "" {
		raw["defaultModel"] = cfg.Model
	}

	out, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal claude config: %w", err)
	}
	return atomicWrite(path, append(out, '\n'), 0o600)
}
