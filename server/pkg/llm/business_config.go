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
	BusinessStageOutline = "outline"
	BusinessStageDetail  = "detail"
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
	stages         map[string]businessStageDefinition
}

type businessStageDefinition struct {
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
			"human_constraints": {},
			"approved_outline":  {},
		},
		stages: map[string]businessStageDefinition{
			BusinessStageOutline: {
				expectedOutput: "json",
				variables: map[string]struct{}{
					"comment_text":      {},
					"issue_identifier":  {},
					"issue_title":       {},
					"siblings":          {},
					"candidate_parents": {},
					"human_constraints": {},
				},
			},
			BusinessStageDetail: {
				expectedOutput: "json",
				variables: map[string]struct{}{
					"comment_text":      {},
					"issue_identifier":  {},
					"issue_title":       {},
					"siblings":          {},
					"candidate_parents": {},
					"human_constraints": {},
					"approved_outline":  {},
				},
			},
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

// BusinessStageClient is a stable handle for one phase of a business call.
// The parent registry still owns the immutable snapshot; resolving the stage
// for every request lets a hot reload take effect without rewiring callers.
type BusinessStageClient struct {
	parent *BusinessClient
	stage  string
}

type businessCallSnapshot struct {
	source              string
	version             int
	enabled             bool
	promptConfigured    bool
	client              *Client
	clientConfig        Config
	apiKeyEnv           string
	systemPrompt        string
	userTemplate        string
	temperature         float64
	maxCompletionTokens int64
	timeout             time.Duration
	jsonSchema          map[string]any
	variables           map[string]struct{}
}

type businessSnapshot struct {
	business    Business
	source      string
	version     int
	enabled     bool
	call        *businessCallSnapshot
	stages      map[string]*businessCallSnapshot
	loadedAt    time.Time
	fingerprint string
}

type businessFileConfig struct {
	Version  int                                 `yaml:"version"`
	Business string                              `yaml:"business"`
	Enabled  *bool                               `yaml:"enabled"`
	LLM      *businessFileLLM                    `yaml:"llm"`
	Prompt   *businessFilePrompt                 `yaml:"prompt"`
	Output   *businessFileOutput                 `yaml:"output"`
	Stages   map[string]*businessFileStageConfig `yaml:"stages"`
}

type businessFileStageConfig struct {
	LLM    *businessFileLLM    `yaml:"llm"`
	Prompt *businessFilePrompt `yaml:"prompt"`
	Output *businessFileOutput `yaml:"output"`
}

type businessFileLLM struct {
	Provider            string  `yaml:"provider"`
	BaseURL             string  `yaml:"base_url"`
	APIKey              *string `yaml:"api_key"`
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

// Stage returns a fixed phase handle for a business. Unknown phase names are
// rejected by the stage-aware loader and resolve to a disabled handle here;
// callers never turn request data into a filesystem path.
func (c *BusinessClient) Stage(stage string) *BusinessStageClient {
	if c == nil {
		return nil
	}
	return &BusinessStageClient{parent: c, stage: strings.TrimSpace(stage)}
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
	if !snapshot.enabled || !snapshot.hasEnabledCall() {
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
	if legacy.hasEnabledCall() {
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
	if legacy.hasEnabledCall() {
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
	definition := businessDefinitions[business]
	clientConfig := legacy
	call := &businessCallSnapshot{
		source:           "legacy",
		version:          0,
		enabled:          true,
		promptConfigured: false,
		clientConfig:     clientConfig,
		client:           New(clientConfig),
		variables:        definition.variables,
	}
	return &businessSnapshot{
		business:    business,
		source:      "legacy",
		version:     0,
		enabled:     true,
		call:        call,
		loadedAt:    now,
		fingerprint: "legacy",
	}
}

func (r *BusinessRegistry) snapshotFromFile(business Business, fingerprint string, now time.Time, file businessFileConfig) (*businessSnapshot, error) {
	definition := businessDefinitions[business]
	snapshot := &businessSnapshot{
		business:    business,
		source:      "file",
		version:     file.Version,
		enabled:     *file.Enabled,
		loadedAt:    now,
		fingerprint: fingerprint,
	}

	if len(file.Stages) > 0 {
		snapshot.stages = make(map[string]*businessCallSnapshot, len(file.Stages))
		for stageName, stageFile := range file.Stages {
			stageDefinition, ok := definition.stages[stageName]
			if !ok {
				return nil, fmt.Errorf("build %s: unknown stage %q", definition.fileName, stageName)
			}
			call, err := r.callSnapshotFromFile(stageDefinition.variables, stageFile.LLM, stageFile.Prompt, stageFile.Output)
			if err != nil {
				return nil, fmt.Errorf("build %s stage %s: %w", definition.fileName, stageName, err)
			}
			snapshot.stages[stageName] = call
		}
		return snapshot, nil
	}

	call, err := r.callSnapshotFromFile(definition.variables, file.LLM, file.Prompt, file.Output)
	if err != nil {
		return nil, fmt.Errorf("build %s: %w", definition.fileName, err)
	}
	snapshot.call = call
	return snapshot, nil
}

func (r *BusinessRegistry) callSnapshotFromFile(
	variables map[string]struct{},
	llmFile *businessFileLLM,
	promptFile *businessFilePrompt,
	outputFile *businessFileOutput,
) (*businessCallSnapshot, error) {
	if llmFile == nil || promptFile == nil || outputFile == nil {
		return nil, errors.New("llm, prompt, and output are required")
	}
	apiKey := strings.TrimSpace(pointerString(llmFile.APIKey))
	apiKeyEnv := strings.TrimSpace(pointerString(llmFile.APIKeyEnv))
	if apiKeyEnv != "" {
		apiKey = strings.TrimSpace(os.Getenv(apiKeyEnv))
	}
	clientConfig := Config{
		APIKey:       apiKey,
		BaseURL:      strings.TrimSpace(llmFile.BaseURL),
		DefaultModel: strings.TrimSpace(llmFile.Model),
		MaxRetries:   llmFile.MaxRetries,
		HTTPClient:   r.httpClient,
	}
	return &businessCallSnapshot{
		source:              "file",
		version:             businessConfigVersion,
		enabled:             true,
		promptConfigured:    true,
		client:              New(clientConfig),
		clientConfig:        clientConfig,
		apiKeyEnv:           apiKeyEnv,
		systemPrompt:        promptFile.System,
		userTemplate:        promptFile.UserTemplate,
		temperature:         llmFile.Temperature,
		maxCompletionTokens: llmFile.MaxCompletionTokens,
		timeout:             time.Duration(llmFile.TimeoutMS) * time.Millisecond,
		jsonSchema:          outputFile.JSONSchema,
		variables:           variables,
	}, nil
}

func pointerString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
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
	// Keep deployment-specific endpoints out of the checked-in YAML examples.
	// Only scalar connection fields support ${ENV_NAME}; prompt values and the
	// api_key_env field remain literal so configuration cannot turn into a
	// general-purpose environment interpolation language.
	if file.LLM != nil {
		file.LLM.BaseURL = expandBusinessEnv(file.LLM.BaseURL)
		file.LLM.Model = expandBusinessEnv(file.LLM.Model)
	}
	for _, stage := range file.Stages {
		if stage == nil || stage.LLM == nil {
			continue
		}
		stage.LLM.BaseURL = expandBusinessEnv(stage.LLM.BaseURL)
		stage.LLM.Model = expandBusinessEnv(stage.LLM.Model)
	}

	if len(file.Stages) > 0 {
		if len(definition.stages) == 0 {
			return businessFileConfig{}, fmt.Errorf("validate %s: stages are not supported for business %q", definition.fileName, business)
		}
		if file.LLM != nil || file.Prompt != nil || file.Output != nil {
			return businessFileConfig{}, fmt.Errorf("validate %s: use either top-level llm/prompt/output or stages, not both", definition.fileName)
		}
		for stageName := range file.Stages {
			if _, ok := definition.stages[stageName]; !ok {
				return businessFileConfig{}, fmt.Errorf("validate %s: unknown stage %q", definition.fileName, stageName)
			}
		}
		for stageName, stageDefinition := range definition.stages {
			stageFile, ok := file.Stages[stageName]
			if !ok {
				return businessFileConfig{}, fmt.Errorf("validate %s: stage %q is required", definition.fileName, stageName)
			}
			if err := validateBusinessCallConfig(definition.fileName, "stage "+stageName, stageDefinition, stageFile); err != nil {
				return businessFileConfig{}, err
			}
		}
		return file, nil
	}

	if err := validateBusinessCallConfig(definition.fileName, "", businessStageDefinition{
		expectedOutput: definition.expectedOutput,
		variables:      definition.variables,
	}, &businessFileStageConfig{LLM: file.LLM, Prompt: file.Prompt, Output: file.Output}); err != nil {
		return businessFileConfig{}, err
	}
	return file, nil
}

func validateBusinessCallConfig(fileName, label string, definition businessStageDefinition, config *businessFileStageConfig) error {
	prefix := "validate " + fileName
	if label != "" {
		prefix += " " + label
	}
	if config == nil || config.LLM == nil || config.Prompt == nil || config.Output == nil {
		return fmt.Errorf("%s: llm, prompt, and output are required", prefix)
	}
	if strings.TrimSpace(config.LLM.Provider) == "" || strings.TrimSpace(config.LLM.Model) == "" {
		return fmt.Errorf("%s: llm.provider and llm.model are required", prefix)
	}
	switch strings.ToLower(strings.TrimSpace(config.LLM.Provider)) {
	case "openai", "openai-compatible":
	default:
		return fmt.Errorf("%s: unsupported llm.provider", prefix)
	}
	hasAPIKey := config.LLM.APIKey != nil && strings.TrimSpace(*config.LLM.APIKey) != ""
	hasAPIKeyEnv := config.LLM.APIKeyEnv != nil && strings.TrimSpace(*config.LLM.APIKeyEnv) != ""
	if hasAPIKey == hasAPIKeyEnv {
		return fmt.Errorf("%s: exactly one of llm.api_key or llm.api_key_env is required", prefix)
	}
	if config.LLM.BaseURL != "" {
		parsed, err := url.Parse(strings.TrimSpace(config.LLM.BaseURL))
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return fmt.Errorf("%s: llm.base_url must be an absolute URL", prefix)
		}
	}
	if config.LLM.Temperature < 0 || config.LLM.Temperature > 2 {
		return fmt.Errorf("%s: llm.temperature must be between 0 and 2", prefix)
	}
	if config.LLM.MaxCompletionTokens < 0 || config.LLM.TimeoutMS < 0 || config.LLM.MaxRetries < 0 {
		return fmt.Errorf("%s: llm numeric limits cannot be negative", prefix)
	}
	if strings.TrimSpace(config.Prompt.System) == "" || strings.TrimSpace(config.Prompt.UserTemplate) == "" {
		return fmt.Errorf("%s: prompt.system and prompt.user_template are required", prefix)
	}
	if err := validateBusinessTemplate(config.Prompt.System, definition.variables); err != nil {
		return fmt.Errorf("%s: prompt.system: %w", prefix, err)
	}
	if err := validateBusinessTemplate(config.Prompt.UserTemplate, definition.variables); err != nil {
		return fmt.Errorf("%s: prompt.user_template: %w", prefix, err)
	}
	format := strings.ToLower(strings.TrimSpace(config.Output.Format))
	if format != definition.expectedOutput {
		return fmt.Errorf("%s: output.format must be %q", prefix, definition.expectedOutput)
	}
	if format == "json" && len(config.Output.JSONSchema) == 0 {
		return fmt.Errorf("%s: output.json_schema is required for JSON output", prefix)
	}
	if format == "json" {
		if err := validateJSONSchema(config.Output.JSONSchema); err != nil {
			return fmt.Errorf("%s: output.json_schema: %w", prefix, err)
		}
	}
	return nil
}

func expandBusinessEnv(value string) string {
	return os.Expand(value, func(key string) string {
		return os.Getenv(key)
	})
}

func validateBusinessTemplate(value string, allowed map[string]struct{}) error {
	for _, variable := range templateVariables(value) {
		if _, ok := allowed[variable]; !ok {
			return fmt.Errorf("unknown template variable %q", variable)
		}
	}
	return nil
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
	if snapshot.enabled && !snapshot.hasEnabledCall() {
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

func (snapshot *businessSnapshot) defaultCall() *businessCallSnapshot {
	if snapshot == nil {
		return nil
	}
	if snapshot.call != nil {
		return snapshot.call
	}
	if detail := snapshot.stages[BusinessStageDetail]; detail != nil {
		return detail
	}
	for _, stage := range []string{BusinessStageOutline} {
		if call := snapshot.stages[stage]; call != nil {
			return call
		}
	}
	return nil
}

func (snapshot *businessSnapshot) clientFor(call *businessCallSnapshot) *Client {
	if call == nil {
		return nil
	}
	if call.source != "file" || call.apiKeyEnv == "" {
		return call.client
	}
	config := call.clientConfig
	config.APIKey = strings.TrimSpace(os.Getenv(call.apiKeyEnv))
	return New(config)
}

func (snapshot *businessSnapshot) hasEnabledCall() bool {
	if snapshot == nil || !snapshot.enabled {
		return false
	}
	call := snapshot.defaultCall()
	client := snapshot.clientFor(call)
	return call != nil && call.enabled && client != nil && client.Enabled()
}

func (c *BusinessClient) resolveCall() (*businessSnapshot, *businessCallSnapshot, error) {
	if c == nil || c.registry == nil {
		return nil, nil, ErrNotConfigured
	}
	snapshot := c.registry.snapshotFor(c.business)
	if snapshot == nil {
		return nil, nil, ErrNotConfigured
	}
	call := snapshot.defaultCall()
	client := snapshot.clientFor(call)
	if !snapshot.enabled || call == nil || !call.enabled || client == nil || !client.Enabled() {
		return nil, nil, ErrNotConfigured
	}
	return snapshot, call, nil
}

func (c *BusinessClient) snapshot() (*businessSnapshot, error) {
	snapshot, _, err := c.resolveCall()
	return snapshot, err
}

func (c *BusinessStageClient) resolveCall() (*businessSnapshot, *businessCallSnapshot, error) {
	if c == nil || c.parent == nil || c.parent.registry == nil {
		return nil, nil, ErrNotConfigured
	}
	definition, ok := businessDefinitions[c.parent.business]
	if !ok {
		return nil, nil, ErrNotConfigured
	}
	if _, ok := definition.stages[c.stage]; !ok {
		return nil, nil, ErrNotConfigured
	}
	snapshot := c.parent.registry.snapshotFor(c.parent.business)
	var call *businessCallSnapshot
	if snapshot != nil {
		call = snapshot.stages[c.stage]
		// A legacy top-level file is still a valid migration source for either
		// phase. New stage-aware files always take the stage-specific path.
		if call == nil {
			if snapshot.call != nil {
				// Keep the legacy file's provider/model/budget, but use this
				// phase's built-in prompt and business-level parser. The old
				// top-level prompt/schema describe the full-detail response and
				// cannot validate an outline response.
				legacyPhase := *snapshot.call
				legacyPhase.promptConfigured = false
				legacyPhase.jsonSchema = nil
				call = &legacyPhase
			}
		}
	}
	if snapshot == nil {
		return nil, nil, ErrNotConfigured
	}
	client := snapshot.clientFor(call)
	if !snapshot.enabled || call == nil || !call.enabled || client == nil || !client.Enabled() {
		return nil, nil, ErrNotConfigured
	}
	return snapshot, call, nil
}

// Enabled reports the state of the current business snapshot. An explicit
// enabled:false file wins over the legacy global configuration.
func (c *BusinessClient) Enabled() bool {
	_, _, err := c.resolveCall()
	return err == nil
}

// Enabled reports whether this stage currently has a usable snapshot.
func (c *BusinessStageClient) Enabled() bool {
	_, _, err := c.resolveCall()
	return err == nil
}

// GenerateTextTemplate renders the configured business prompt and makes a
// text completion. Fallback prompts and limits are used only when the business
// is still served by the legacy global config during migration.
func (c *BusinessClient) GenerateTextTemplate(ctx context.Context, variables map[string]string, fallbackSystem, fallbackUserTemplate string, fallbackTemperature float64, fallbackMaxCompletionTokens int64) (string, error) {
	snapshot, call, err := c.resolveCall()
	if err != nil {
		return "", err
	}
	system, user, err := renderBusinessCall(c.business, call, variables, fallbackSystem, fallbackUserTemplate)
	if err != nil {
		return "", err
	}
	temperature, maxTokens := snapshotLimits(call, fallbackTemperature, fallbackMaxCompletionTokens)
	ctx, cancel := withBusinessTimeout(ctx, call.timeout)
	defer cancel()
	return snapshot.clientFor(call).GenerateTextWithOptions(ctx, "", system, user, temperature, maxTokens)
}

// GenerateJSONTemplate is the structured sibling of GenerateTextTemplate.
func (c *BusinessClient) GenerateJSONTemplate(ctx context.Context, variables map[string]string, fallbackSystem, fallbackUserTemplate string, fallbackTemperature float64, fallbackMaxCompletionTokens int64) (string, error) {
	snapshot, call, err := c.resolveCall()
	if err != nil {
		return "", err
	}
	return generateBusinessJSON(ctx, c.business, snapshot, call, variables, fallbackSystem, fallbackUserTemplate, fallbackTemperature, fallbackMaxCompletionTokens)
}

// GenerateJSONTemplate renders the phase-specific prompt and makes a
// structured completion. A legacy top-level YAML is used as a migration
// fallback when it predates the stages map.
func (c *BusinessStageClient) GenerateJSONTemplate(ctx context.Context, variables map[string]string, fallbackSystem, fallbackUserTemplate string, fallbackTemperature float64, fallbackMaxCompletionTokens int64) (string, error) {
	snapshot, call, err := c.resolveCall()
	if err != nil {
		return "", err
	}
	return generateBusinessJSON(ctx, c.parent.business+"/"+Business(c.stage), snapshot, call, variables, fallbackSystem, fallbackUserTemplate, fallbackTemperature, fallbackMaxCompletionTokens)
}

func generateBusinessJSON(ctx context.Context, name Business, snapshot *businessSnapshot, call *businessCallSnapshot, variables map[string]string, fallbackSystem, fallbackUserTemplate string, fallbackTemperature float64, fallbackMaxCompletionTokens int64) (string, error) {
	system, user, err := renderBusinessCall(name, call, variables, fallbackSystem, fallbackUserTemplate)
	if err != nil {
		return "", err
	}
	temperature, maxTokens := snapshotLimits(call, fallbackTemperature, fallbackMaxCompletionTokens)
	ctx, cancel := withBusinessTimeout(ctx, call.timeout)
	defer cancel()
	raw, err := snapshot.clientFor(call).GenerateJSON(ctx, "", system, user, temperature, maxTokens)
	if err != nil {
		return "", err
	}
	raw = StripJSONFence(raw)
	if err := validateBusinessJSON(raw, call.jsonSchema); err != nil {
		return "", fmt.Errorf("validate %s response: %w", name, err)
	}
	return raw, nil
}

func renderBusinessCall(name Business, call *businessCallSnapshot, variables map[string]string, fallbackSystem, fallbackUserTemplate string) (string, string, error) {
	systemTemplate := fallbackSystem
	userTemplate := fallbackUserTemplate
	allowed := map[string]struct{}{}
	if definition, ok := businessDefinitions[name]; ok {
		allowed = definition.variables
	}
	if call != nil {
		if call.source == "file" && call.promptConfigured {
			systemTemplate = call.systemPrompt
			userTemplate = call.userTemplate
		}
		if len(call.variables) > 0 {
			allowed = call.variables
		}
	}
	system, err := renderBusinessTemplate(systemTemplate, allowed, variables)
	if err != nil {
		return "", "", fmt.Errorf("render %s system prompt: %w", name, err)
	}
	user, err := renderBusinessTemplate(userTemplate, allowed, variables)
	if err != nil {
		return "", "", fmt.Errorf("render %s user prompt: %w", name, err)
	}
	return system, user, nil
}

func snapshotLimits(call *businessCallSnapshot, fallbackTemperature float64, fallbackMaxCompletionTokens int64) (float64, int64) {
	if call != nil && call.source == "file" {
		return call.temperature, call.maxCompletionTokens
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
