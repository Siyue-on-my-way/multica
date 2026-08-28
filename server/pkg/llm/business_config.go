package llm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/openai/openai-go/v3/option"
	"gopkg.in/yaml.v3"
)

// Business identifies one server-side LLM use case. It is deliberately a
// closed set: callers select one of these values and never provide a config
// path from a request.
type Business string

const (
	BusinessChatTitle        Business = "chat-title"
	BusinessSubissueSuggest  Business = "subissue-suggest"
	BusinessHandoffCompress  Business = "handoff-compress"
	BusinessChatQuickActions Business = "chat-quick-actions"
	businessConfigVersion    int      = 1
)

const (
	BusinessLoadActive         BusinessLoadState = "active"
	BusinessLoadDisabled       BusinessLoadState = "disabled"
	BusinessLoadFallbackLegacy BusinessLoadState = "fallback_legacy"
	BusinessLoadStale          BusinessLoadState = "stale"
	BusinessLoadError          BusinessLoadState = "error"
)

// BusinessLoadState describes the effective source for one business. Status
// is operational metadata only; it never includes credentials or prompts.
type BusinessLoadState string

// BusinessLoadStatus is a safe diagnostic snapshot for one business config.
// Fingerprint is a content hash, not the file contents.
type BusinessLoadStatus struct {
	Business      Business
	FileName      string
	State         BusinessLoadState
	Source        string
	Version       int
	Fingerprint   string
	LoadedAt      time.Time
	LastAttemptAt time.Time
	LastError     string
}

type businessDefinition struct {
	fileName       string
	expectedOutput string
	variables      map[string]struct{}
}

var businessDefinitions = map[Business]businessDefinition{
	BusinessChatTitle: {
		fileName:       "chat-title.yaml",
		expectedOutput: "text",
		variables:      map[string]struct{}{"source_text": {}},
	},
	BusinessSubissueSuggest: {
		fileName:       "subissue-suggest.yaml",
		expectedOutput: "json",
		variables: map[string]struct{}{
			"comment_text":      {},
			"issue_identifier":  {},
			"issue_title":       {},
			"siblings":          {},
			"candidate_parents": {},
		},
	},
	BusinessHandoffCompress: {
		fileName:       "handoff-compress.yaml",
		expectedOutput: "json",
		variables: map[string]struct{}{
			"issue_title": {},
			"comments":    {},
		},
	},
	BusinessChatQuickActions: {
		fileName:       "chat-quick-actions.yaml",
		expectedOutput: "json",
		variables: map[string]struct{}{
			"conversation_context": {},
			"latest_user_message":  {},
			"latest_agent_message": {},
			"already_suggested":    {},
		},
	},
}

// SupportedBusinesses returns the fixed registry keys in stable order.
func SupportedBusinesses() []Business {
	out := make([]Business, 0, len(businessDefinitions))
	for business := range businessDefinitions {
		out = append(out, business)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// BusinessRegistryConfig controls the business config loader. Directory is
// expected to be a read-only mount. When it is empty, callers keep using the
// legacy Config and hard-coded fallback prompts during migration.
type BusinessRegistryConfig struct {
	Directory    string
	Legacy       Config
	PollInterval time.Duration
	HTTPClient   option.HTTPClient
	Logger       *slog.Logger
}

// BusinessRegistry owns independent immutable snapshots for the fixed set of
// server-side LLM businesses. A malformed file only affects its own business;
// an existing valid snapshot is never replaced by an invalid one.
type BusinessRegistry struct {
	directory    string
	legacy       Config
	pollInterval time.Duration
	httpClient   option.HTTPClient
	logger       *slog.Logger

	mu           sync.RWMutex
	snapshots    map[Business]*businessSnapshot
	statuses     map[Business]BusinessLoadStatus
	fingerprints map[Business]string
	started      bool
}

// BusinessClient is a stable handle for one registry key. It resolves the
// current snapshot for every call, so a successful hot reload applies to the
// next request without changing any caller wiring.
type BusinessClient struct {
	registry *BusinessRegistry
	business Business
}

type businessSnapshot struct {
	business            Business
	source              string
	version             int
	enabled             bool
	client              *Client
	clientConfig        Config
	apiKeyEnv           string
	systemPrompt        string
	userTemplate        string
	temperature         float64
	maxCompletionTokens int64
	timeout             time.Duration
	jsonSchema          map[string]any
	loadedAt            time.Time
	fingerprint         string
}

type businessFileConfig struct {
	Version  int                 `yaml:"version"`
	Business string              `yaml:"business"`
	Enabled  *bool               `yaml:"enabled"`
	LLM      *businessFileLLM    `yaml:"llm"`
	Prompt   *businessFilePrompt `yaml:"prompt"`
	Output   *businessFileOutput `yaml:"output"`
}

type businessFileLLM struct {
	Provider            string  `yaml:"provider"`
	BaseURL             string  `yaml:"base_url"`
	APIKeyEnv           *string `yaml:"api_key_env"`
	Model               string  `yaml:"model"`
	Temperature         float64 `yaml:"temperature"`
	MaxCompletionTokens int64   `yaml:"max_completion_tokens"`
	TimeoutMS           int64   `yaml:"timeout_ms"`
	MaxRetries          int     `yaml:"max_retries"`
}

type businessFilePrompt struct {
	System       string `yaml:"system"`
	UserTemplate string `yaml:"user_template"`
}

type businessFileOutput struct {
	Format     string         `yaml:"format"`
	JSONSchema map[string]any `yaml:"json_schema"`
}

// NewBusinessRegistry loads all known business files once. Start must be
// called by the server lifecycle to enable polling; tests can use Reload
// directly without starting a goroutine.
func NewBusinessRegistry(cfg BusinessRegistryConfig) *BusinessRegistry {
	interval := cfg.PollInterval
	if interval <= 0 {
		interval = 5 * time.Second
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}

	registry := &BusinessRegistry{
		directory:    strings.TrimSpace(cfg.Directory),
		legacy:       cfg.Legacy,
		pollInterval: interval,
		logger:       logger,
		snapshots:    make(map[Business]*businessSnapshot, len(businessDefinitions)),
		statuses:     make(map[Business]BusinessLoadStatus, len(businessDefinitions)),
		fingerprints: make(map[Business]string, len(businessDefinitions)),
	}
	registry.httpClient = cfg.HTTPClient
	registry.Reload()
	return registry
}

// Client returns a handle for a known business. Unknown keys return nil and
// cannot be turned into a path lookup.
func (r *BusinessRegistry) Client(business Business) *BusinessClient {
	if r == nil {
		return nil
	}
	if _, ok := businessDefinitions[business]; !ok {
		return nil
	}
	return &BusinessClient{registry: r, business: business}
}

// Start begins read-only polling. It is safe to call more than once; only one
// poller is started. The caller owns ctx and should cancel it during shutdown.
func (r *BusinessRegistry) Start(ctx context.Context) {
	if r == nil || strings.TrimSpace(r.directory) == "" {
		return
	}
	r.mu.Lock()
	if r.started {
		r.mu.Unlock()
		return
	}
	r.started = true
	r.mu.Unlock()

	go func() {
		ticker := time.NewTicker(r.pollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				r.Reload()
			}
		}
	}()
}

// Reload polls every fixed business file immediately. It never writes to the
// directory and performs each business independently.
func (r *BusinessRegistry) Reload() {
	if r == nil {
		return
	}
	if strings.TrimSpace(r.directory) == "" {
		for _, business := range SupportedBusinesses() {
			r.installMissingFile(business, "legacy-only", time.Now().UTC())
		}
		return
	}
	for _, business := range SupportedBusinesses() {
		r.reloadBusiness(business)
	}
}

// Status returns a copy of the safe load status for business. An unknown key
// returns an error status rather than exposing a dynamic file path.
func (r *BusinessRegistry) Status(business Business) BusinessLoadStatus {
	if r == nil {
		return BusinessLoadStatus{Business: business, State: BusinessLoadError, LastError: "registry is nil"}
	}
	r.mu.RLock()
	status, ok := r.statuses[business]
	r.mu.RUnlock()
	if ok {
		return status
	}
	return BusinessLoadStatus{Business: business, State: BusinessLoadError, LastError: "unknown business"}
}

// Statuses returns all known statuses in stable business order.
func (r *BusinessRegistry) Statuses() []BusinessLoadStatus {
	if r == nil {
		return nil
	}
	businesses := SupportedBusinesses()
	out := make([]BusinessLoadStatus, 0, len(businesses))
	for _, business := range businesses {
		out = append(out, r.Status(business))
	}
	return out
}

func (r *BusinessRegistry) reloadBusiness(business Business) {
	definition, ok := businessDefinitions[business]
	if !ok {
		return
	}

	path := filepath.Join(r.directory, definition.fileName)
	data, readErr := os.ReadFile(path)
	fingerprint := "missing"
	if readErr == nil {
		sum := sha256.Sum256(data)
		fingerprint = hex.EncodeToString(sum[:])
	}

	r.mu.RLock()
	previousFingerprint := r.fingerprints[business]
	previous := r.snapshots[business]
	r.mu.RUnlock()
	if previousFingerprint == fingerprint {
		return
	}

	now := time.Now().UTC()
	if readErr != nil {
		if errors.Is(readErr, os.ErrNotExist) {
			r.installMissingFile(business, fingerprint, now)
			return
		}
		r.installInvalid(business, fingerprint, now, previous, fmt.Errorf("read %s: %w", definition.fileName, readErr))
		return
	}

	parsed, err := parseBusinessFile(business, definition, data)
	if err != nil {
		r.installInvalid(business, fingerprint, now, previous, err)
		return
	}

	snapshot, err := r.snapshotFromFile(business, fingerprint, now, parsed)
	if err != nil {
		r.installInvalid(business, fingerprint, now, previous, err)
		return
	}
	state := BusinessLoadActive
	if !snapshot.enabled || !snapshot.client.Enabled() {
		state = BusinessLoadError
	}
	if !snapshot.enabled {
		state = BusinessLoadDisabled
	}
	r.publish(business, snapshot, BusinessLoadStatus{
		Business:      business,
		FileName:      definition.fileName,
		State:         state,
		Source:        "file",
		Version:       snapshot.version,
		Fingerprint:   fingerprint,
		LoadedAt:      now,
		LastAttemptAt: now,
		LastError:     statusErrorForSnapshot(snapshot),
	})
}

func (r *BusinessRegistry) installMissingFile(business Business, fingerprint string, now time.Time) {
	definition := businessDefinitions[business]
	legacy := r.legacySnapshot(business, now)
	if legacy.client.Enabled() {
		r.publish(business, legacy, BusinessLoadStatus{
			Business:      business,
			FileName:      definition.fileName,
			State:         BusinessLoadFallbackLegacy,
			Source:        "legacy",
			Fingerprint:   fingerprint,
			LoadedAt:      now,
			LastAttemptAt: now,
		})
		return
	}
	legacy.enabled = false
	r.publish(business, legacy, BusinessLoadStatus{
		Business:      business,
		FileName:      definition.fileName,
		State:         BusinessLoadDisabled,
		Source:        "none",
		Fingerprint:   fingerprint,
		LoadedAt:      now,
		LastAttemptAt: now,
	})
}

func (r *BusinessRegistry) installInvalid(business Business, fingerprint string, now time.Time, previous *businessSnapshot, err error) {
	definition := businessDefinitions[business]
	lastError := safeConfigError(err)
	if previous != nil && previous.source == "file" {
		r.publish(business, previous, BusinessLoadStatus{
			Business:      business,
			FileName:      definition.fileName,
			State:         BusinessLoadStale,
			Source:        "file",
			Version:       previous.version,
			Fingerprint:   fingerprint,
			LoadedAt:      previous.loadedAt,
			LastAttemptAt: now,
			LastError:     lastError,
		})
		return
	}

	legacy := r.legacySnapshot(business, now)
	if legacy.client.Enabled() {
		r.publish(business, legacy, BusinessLoadStatus{
			Business:      business,
			FileName:      definition.fileName,
			State:         BusinessLoadFallbackLegacy,
			Source:        "legacy",
			Fingerprint:   fingerprint,
			LoadedAt:      now,
			LastAttemptAt: now,
			LastError:     lastError,
		})
		return
	}

	legacy.enabled = false
	r.publish(business, legacy, BusinessLoadStatus{
		Business:      business,
		FileName:      definition.fileName,
		State:         BusinessLoadError,
		Source:        "none",
		Fingerprint:   fingerprint,
		LoadedAt:      now,
		LastAttemptAt: now,
		LastError:     lastError,
	})
}

func (r *BusinessRegistry) publish(business Business, snapshot *businessSnapshot, status BusinessLoadStatus) {
	r.mu.Lock()
	r.snapshots[business] = snapshot
	r.statuses[business] = status
	r.fingerprints[business] = status.Fingerprint
	r.mu.Unlock()

	if status.LastError != "" && status.State != BusinessLoadStale {
		r.logger.Warn("llm business config unavailable",
			"business", business,
			"state", status.State,
			"source", status.Source,
			"error", status.LastError,
		)
	}
}

func (r *BusinessRegistry) snapshotFor(business Business) *businessSnapshot {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	snapshot := r.snapshots[business]
	r.mu.RUnlock()
	return snapshot
}

func (r *BusinessRegistry) legacySnapshot(business Business, now time.Time) *businessSnapshot {
	legacy := r.legacy
	return &businessSnapshot{
		business:    business,
		source:      "legacy",
		version:     0,
		enabled:     true,
		client:      New(legacy),
		loadedAt:    now,
		fingerprint: "legacy",
	}
}

func (r *BusinessRegistry) snapshotFromFile(business Business, fingerprint string, now time.Time, file businessFileConfig) (*businessSnapshot, error) {
	apiKeyEnv := strings.TrimSpace(*file.LLM.APIKeyEnv)
	apiKey := ""
	if apiKeyEnv != "" {
		apiKey = strings.TrimSpace(os.Getenv(apiKeyEnv))
	}
	clientConfig := Config{
		APIKey:       apiKey,
		BaseURL:      strings.TrimSpace(file.LLM.BaseURL),
		DefaultModel: strings.TrimSpace(file.LLM.Model),
		MaxRetries:   file.LLM.MaxRetries,
	}
	clientConfig.HTTPClient = r.httpClient

	return &businessSnapshot{
		business:            business,
		source:              "file",
		version:             file.Version,
		enabled:             *file.Enabled,
		client:              New(clientConfig),
		clientConfig:        clientConfig,
		apiKeyEnv:           apiKeyEnv,
		systemPrompt:        file.Prompt.System,
		userTemplate:        file.Prompt.UserTemplate,
		temperature:         file.LLM.Temperature,
		maxCompletionTokens: file.LLM.MaxCompletionTokens,
		timeout:             time.Duration(file.LLM.TimeoutMS) * time.Millisecond,
		jsonSchema:          file.Output.JSONSchema,
		loadedAt:            now,
		fingerprint:         fingerprint,
	}, nil
}

func parseBusinessFile(business Business, definition businessDefinition, data []byte) (businessFileConfig, error) {
	var file businessFileConfig
	decoder := yaml.NewDecoder(strings.NewReader(string(data)))
	decoder.KnownFields(true)
	if err := decoder.Decode(&file); err != nil {
		return businessFileConfig{}, fmt.Errorf("parse %s: %w", definition.fileName, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return businessFileConfig{}, fmt.Errorf("parse %s: multiple YAML documents are not allowed", definition.fileName)
		}
		return businessFileConfig{}, fmt.Errorf("parse %s: %w", definition.fileName, err)
	}

	if file.Version != businessConfigVersion {
		return businessFileConfig{}, fmt.Errorf("validate %s: unsupported version %d", definition.fileName, file.Version)
	}
	if strings.TrimSpace(file.Business) != string(business) {
		return businessFileConfig{}, fmt.Errorf("validate %s: business must be %q", definition.fileName, business)
	}
	if file.Enabled == nil {
		return businessFileConfig{}, fmt.Errorf("validate %s: enabled is required", definition.fileName)
	}
	if file.LLM == nil || file.Prompt == nil || file.Output == nil {
		return businessFileConfig{}, fmt.Errorf("validate %s: llm, prompt, and output are required", definition.fileName)
	}
	// Keep deployment-specific endpoints out of the checked-in YAML examples.
	// Only scalar connection fields support ${ENV_NAME}; prompt values and the
	// api_key_env field remain literal so configuration cannot turn into a
	// general-purpose environment interpolation language.
	file.LLM.BaseURL = expandBusinessEnv(file.LLM.BaseURL)
	file.LLM.Model = expandBusinessEnv(file.LLM.Model)
	if strings.TrimSpace(file.LLM.Provider) == "" || strings.TrimSpace(file.LLM.Model) == "" {
		return businessFileConfig{}, fmt.Errorf("validate %s: llm.provider and llm.model are required", definition.fileName)
	}
	switch strings.ToLower(strings.TrimSpace(file.LLM.Provider)) {
	case "openai", "openai-compatible":
	default:
		return businessFileConfig{}, fmt.Errorf("validate %s: unsupported llm.provider", definition.fileName)
	}
	if file.LLM.APIKeyEnv == nil || strings.TrimSpace(*file.LLM.APIKeyEnv) == "" {
		return businessFileConfig{}, fmt.Errorf("validate %s: llm.api_key_env is required", definition.fileName)
	}
	if file.LLM.BaseURL != "" {
		parsed, err := url.Parse(strings.TrimSpace(file.LLM.BaseURL))
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return businessFileConfig{}, fmt.Errorf("validate %s: llm.base_url must be an absolute URL", definition.fileName)
		}
	}
	if file.LLM.Temperature < 0 || file.LLM.Temperature > 2 {
		return businessFileConfig{}, fmt.Errorf("validate %s: llm.temperature must be between 0 and 2", definition.fileName)
	}
	if file.LLM.MaxCompletionTokens < 0 || file.LLM.TimeoutMS < 0 || file.LLM.MaxRetries < 0 {
		return businessFileConfig{}, fmt.Errorf("validate %s: llm numeric limits cannot be negative", definition.fileName)
	}
	if strings.TrimSpace(file.Prompt.System) == "" || strings.TrimSpace(file.Prompt.UserTemplate) == "" {
		return businessFileConfig{}, fmt.Errorf("validate %s: prompt.system and prompt.user_template are required", definition.fileName)
	}
	if err := validateBusinessTemplate(file.Prompt.System, definition.variables); err != nil {
		return businessFileConfig{}, fmt.Errorf("validate %s: prompt.system: %w", definition.fileName, err)
	}
	if err := validateBusinessTemplate(file.Prompt.UserTemplate, definition.variables); err != nil {
		return businessFileConfig{}, fmt.Errorf("validate %s: prompt.user_template: %w", definition.fileName, err)
	}
	format := strings.ToLower(strings.TrimSpace(file.Output.Format))
	if format != definition.expectedOutput {
		return businessFileConfig{}, fmt.Errorf("validate %s: output.format must be %q", definition.fileName, definition.expectedOutput)
	}
	if format == "json" && len(file.Output.JSONSchema) == 0 {
		return businessFileConfig{}, fmt.Errorf("validate %s: output.json_schema is required for JSON output", definition.fileName)
	}
	if format == "json" {
		if err := validateJSONSchema(file.Output.JSONSchema); err != nil {
			return businessFileConfig{}, fmt.Errorf("validate %s: output.json_schema: %w", definition.fileName, err)
		}
	}
	return file, nil
}

func validateBusinessTemplate(value string, allowed map[string]struct{}) error {
	for _, variable := range templateVariables(value) {
		if _, ok := allowed[variable]; !ok {
			return fmt.Errorf("unknown template variable %q", variable)
		}
	}
	return nil
}

func expandBusinessEnv(value string) string {
	return os.Expand(value, func(key string) string {
		return os.Getenv(key)
	})
}

func templateVariables(value string) []string {
	variables := make([]string, 0)
	for remaining := value; ; {
		start := strings.Index(remaining, "{{")
		if start < 0 {
			break
		}
		end := strings.Index(remaining[start+2:], "}}")
		if end < 0 {
			variables = append(variables, "<malformed>")
			break
		}
		end += start + 2
		name := strings.TrimSpace(remaining[start+2 : end])
		if name == "" || strings.ContainsAny(name, "{} \t\r\n") {
			variables = append(variables, "<malformed>")
		} else {
			variables = append(variables, name)
		}
		remaining = remaining[end+2:]
	}
	return variables
}

func renderBusinessTemplate(template string, allowed map[string]struct{}, variables map[string]string) (string, error) {
	var b strings.Builder
	remaining := template
	for {
		start := strings.Index(remaining, "{{")
		if start < 0 {
			b.WriteString(remaining)
			break
		}
		b.WriteString(remaining[:start])
		end := strings.Index(remaining[start+2:], "}}")
		if end < 0 {
			return "", errors.New("malformed template variable")
		}
		end += start + 2
		name := strings.TrimSpace(remaining[start+2 : end])
		if name == "" || strings.ContainsAny(name, "{} \t\r\n") {
			return "", errors.New("malformed template variable")
		}
		if _, ok := allowed[name]; !ok {
			return "", fmt.Errorf("unknown template variable %q", name)
		}
		value, ok := variables[name]
		if !ok {
			return "", fmt.Errorf("missing template variable %q", name)
		}
		b.WriteString(value)
		remaining = remaining[end+2:]
	}
	return b.String(), nil
}

func statusErrorForSnapshot(snapshot *businessSnapshot) string {
	if snapshot.enabled && !snapshot.client.Enabled() {
		return "LLM credentials or base URL are not configured"
	}
	return ""
}

func safeConfigError(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func (c *BusinessClient) snapshot() (*businessSnapshot, error) {
	if c == nil || c.registry == nil {
		return nil, ErrNotConfigured
	}
	snapshot := c.registry.snapshotFor(c.business)
	client := c.clientFor(snapshot)
	if snapshot == nil || !snapshot.enabled || client == nil || !client.Enabled() {
		return nil, ErrNotConfigured
	}
	return snapshot, nil
}

func (c *BusinessClient) clientFor(snapshot *businessSnapshot) *Client {
	if snapshot == nil {
		return nil
	}
	if snapshot.source != "file" || snapshot.apiKeyEnv == "" {
		return snapshot.client
	}
	config := snapshot.clientConfig
	config.APIKey = strings.TrimSpace(os.Getenv(snapshot.apiKeyEnv))
	return New(config)
}

// Enabled reports the state of the current business snapshot. An explicit
// enabled:false file wins over the legacy global configuration.
func (c *BusinessClient) Enabled() bool {
	_, err := c.snapshot()
	return err == nil
}

// GenerateTextTemplate renders the configured business prompt and makes a
// text completion. Fallback prompts and limits are used only when the business
// is still served by the legacy global config during migration.
func (c *BusinessClient) GenerateTextTemplate(ctx context.Context, variables map[string]string, fallbackSystem, fallbackUserTemplate string, fallbackTemperature float64, fallbackMaxCompletionTokens int64) (string, error) {
	snapshot, err := c.snapshot()
	if err != nil {
		return "", err
	}
	system, user, err := c.render(snapshot, variables, fallbackSystem, fallbackUserTemplate)
	if err != nil {
		return "", err
	}
	temperature, maxTokens := snapshotLimits(snapshot, fallbackTemperature, fallbackMaxCompletionTokens)
	ctx, cancel := withBusinessTimeout(ctx, snapshot.timeout)
	defer cancel()
	return c.clientFor(snapshot).GenerateTextWithOptions(ctx, "", system, user, temperature, maxTokens)
}

// GenerateJSONTemplate is the structured sibling of GenerateTextTemplate.
func (c *BusinessClient) GenerateJSONTemplate(ctx context.Context, variables map[string]string, fallbackSystem, fallbackUserTemplate string, fallbackTemperature float64, fallbackMaxCompletionTokens int64) (string, error) {
	snapshot, err := c.snapshot()
	if err != nil {
		return "", err
	}
	system, user, err := c.render(snapshot, variables, fallbackSystem, fallbackUserTemplate)
	if err != nil {
		return "", err
	}
	temperature, maxTokens := snapshotLimits(snapshot, fallbackTemperature, fallbackMaxCompletionTokens)
	ctx, cancel := withBusinessTimeout(ctx, snapshot.timeout)
	defer cancel()
	raw, err := c.clientFor(snapshot).GenerateJSON(ctx, "", system, user, temperature, maxTokens)
	if err != nil {
		return "", err
	}
	if err := validateBusinessJSON(raw, snapshot.jsonSchema); err != nil {
		return "", fmt.Errorf("validate %s response: %w", c.business, err)
	}
	return raw, nil
}

func (c *BusinessClient) render(snapshot *businessSnapshot, variables map[string]string, fallbackSystem, fallbackUserTemplate string) (string, string, error) {
	definition := businessDefinitions[c.business]
	systemTemplate := fallbackSystem
	userTemplate := fallbackUserTemplate
	if snapshot.source == "file" {
		systemTemplate = snapshot.systemPrompt
		userTemplate = snapshot.userTemplate
	}
	system, err := renderBusinessTemplate(systemTemplate, definition.variables, variables)
	if err != nil {
		return "", "", fmt.Errorf("render %s system prompt: %w", c.business, err)
	}
	user, err := renderBusinessTemplate(userTemplate, definition.variables, variables)
	if err != nil {
		return "", "", fmt.Errorf("render %s user prompt: %w", c.business, err)
	}
	return system, user, nil
}

func snapshotLimits(snapshot *businessSnapshot, fallbackTemperature float64, fallbackMaxCompletionTokens int64) (float64, int64) {
	if snapshot.source == "file" {
		return snapshot.temperature, snapshot.maxCompletionTokens
	}
	return fallbackTemperature, fallbackMaxCompletionTokens
}

func withBusinessTimeout(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		return ctx, func() {}
	}
	if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) <= timeout {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, timeout)
}

func validateJSONSchema(schema map[string]any) error {
	if len(schema) == 0 {
		return errors.New("schema must be a non-empty object")
	}
	if err := validateJSONSchemaNode(schema); err != nil {
		return err
	}
	return nil
}

func validateJSONSchemaNode(schema map[string]any) error {
	if rawType, ok := schema["type"]; ok {
		switch value := rawType.(type) {
		case string:
			if !validJSONSchemaType(value) {
				return fmt.Errorf("unsupported type %q", value)
			}
		case []any:
			if len(value) == 0 {
				return errors.New("type union must not be empty")
			}
			for _, item := range value {
				name, ok := item.(string)
				if !ok || !validJSONSchemaType(name) {
					return errors.New("type union must contain only valid type names")
				}
			}
		default:
			return errors.New("type must be a string or an array of strings")
		}
	}
	if required, ok := schema["required"]; ok {
		if err := validateStringList(required, "required"); err != nil {
			return err
		}
	}
	if properties, ok := schema["properties"]; ok {
		propertyMap, ok := properties.(map[string]any)
		if !ok {
			return errors.New("properties must be an object")
		}
		for name, raw := range propertyMap {
			nested, ok := raw.(map[string]any)
			if !ok {
				return fmt.Errorf("property %q must be an object schema", name)
			}
			if err := validateJSONSchemaNode(nested); err != nil {
				return fmt.Errorf("property %q: %w", name, err)
			}
		}
	}
	if items, ok := schema["items"]; ok {
		itemSchema, ok := items.(map[string]any)
		if !ok {
			return errors.New("items must be an object schema")
		}
		if err := validateJSONSchemaNode(itemSchema); err != nil {
			return fmt.Errorf("items: %w", err)
		}
	}
	for _, key := range []string{"minimum", "maximum"} {
		if value, ok := schema[key]; ok {
			if _, ok := schemaNumber(value); !ok {
				return fmt.Errorf("%s must be a number", key)
			}
		}
	}
	return nil
}

func validJSONSchemaType(value string) bool {
	switch value {
	case "object", "array", "string", "number", "integer", "boolean", "null":
		return true
	default:
		return false
	}
}

func validateStringList(value any, field string) error {
	items, ok := value.([]any)
	if !ok {
		return fmt.Errorf("%s must be an array of strings", field)
	}
	for _, item := range items {
		if _, ok := item.(string); !ok {
			return fmt.Errorf("%s must be an array of strings", field)
		}
	}
	return nil
}

func validateBusinessJSON(raw string, schema map[string]any) error {
	if len(schema) == 0 {
		return nil
	}
	var value any
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		return fmt.Errorf("response is not valid JSON: %w", err)
	}
	if err := validateJSONValue(value, schema, "response"); err != nil {
		return err
	}
	return nil
}

func validateJSONValue(value any, schema map[string]any, path string) error {
	if rawType, ok := schema["type"]; ok && !jsonSchemaTypeMatches(value, rawType) {
		return fmt.Errorf("%s has the wrong type", path)
	}
	if required, ok := schema["required"].([]any); ok {
		object, isObject := value.(map[string]any)
		if !isObject {
			return fmt.Errorf("%s must be an object for required fields", path)
		}
		for _, rawName := range required {
			name := rawName.(string)
			if _, exists := object[name]; !exists {
				return fmt.Errorf("%s.%s is required", path, name)
			}
		}
	}
	if properties, ok := schema["properties"].(map[string]any); ok {
		object, isObject := value.(map[string]any)
		if !isObject {
			return fmt.Errorf("%s must be an object for properties", path)
		}
		for name, rawSchema := range properties {
			nested, ok := rawSchema.(map[string]any)
			if !ok {
				continue
			}
			if child, exists := object[name]; exists {
				if err := validateJSONValue(child, nested, path+"."+name); err != nil {
					return err
				}
			}
		}
	}
	if itemSchema, ok := schema["items"].(map[string]any); ok {
		array, isArray := value.([]any)
		if !isArray {
			return fmt.Errorf("%s must be an array for items", path)
		}
		for index, item := range array {
			if err := validateJSONValue(item, itemSchema, fmt.Sprintf("%s[%d]", path, index)); err != nil {
				return err
			}
		}
	}
	if minimum, ok := schemaNumber(schema["minimum"]); ok {
		if number, ok := jsonNumber(value); !ok || number < minimum {
			return fmt.Errorf("%s is below minimum", path)
		}
	}
	if maximum, ok := schemaNumber(schema["maximum"]); ok {
		if number, ok := jsonNumber(value); !ok || number > maximum {
			return fmt.Errorf("%s is above maximum", path)
		}
	}
	return nil
}

func jsonSchemaTypeMatches(value any, rawType any) bool {
	if names, ok := rawType.([]any); ok {
		for _, name := range names {
			if jsonSchemaTypeMatches(value, name) {
				return true
			}
		}
		return false
	}
	name, ok := rawType.(string)
	if !ok {
		return false
	}
	switch name {
	case "object":
		_, ok = value.(map[string]any)
	case "array":
		_, ok = value.([]any)
	case "string":
		_, ok = value.(string)
	case "number":
		_, ok = value.(float64)
	case "integer":
		number, numberOK := value.(float64)
		ok = numberOK && number == float64(int64(number))
	case "boolean":
		_, ok = value.(bool)
	case "null":
		ok = value == nil
	default:
		ok = false
	}
	return ok
}

func schemaNumber(value any) (float64, bool) {
	switch number := value.(type) {
	case int:
		return float64(number), true
	case int64:
		return float64(number), true
	case uint64:
		return float64(number), true
	case float64:
		return number, true
	case float32:
		return float64(number), true
	default:
		return 0, false
	}
}

func jsonNumber(value any) (float64, bool) {
	number, ok := value.(float64)
	return number, ok
}
