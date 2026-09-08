package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"log/slog"
)

func TestBusinessRegistryLoadsAndHotReloadsTextBusiness(t *testing.T) {
	var requests []map[string]any
	var authHeaders []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		requests = append(requests, request)
		authHeaders = append(authHeaders, r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"test","object":"chat.completion","created":1,"model":"configured-model","choices":[{"index":0,"message":{"role":"assistant","content":"Configured title"},"finish_reason":"stop"}]}`)
	}))
	defer server.Close()

	t.Setenv("MULTICA_TEST_TITLE_KEY", "test-secret")
	directory := t.TempDir()
	writeBusinessFile(t, directory, "chat-title.yaml", strings.ReplaceAll(`version: 1
business: chat-title
enabled: true
llm:
  provider: openai-compatible
  base_url: BASE_URL
  api_key_env: MULTICA_TEST_TITLE_KEY
  model: configured-model
  temperature: 0.7
  max_completion_tokens: 77
  timeout_ms: 5000
prompt:
  system: "System {{source_text}}"
  user_template: "First {{source_text}}"
output:
  format: text
`, "BASE_URL", server.URL+"/"))

	registry := NewBusinessRegistry(BusinessRegistryConfig{
		Directory:  directory,
		HTTPClient: server.Client(),
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	status := registry.Status(BusinessChatTitle)
	if status.State != BusinessLoadActive || status.Source != "file" {
		t.Fatalf("unexpected initial status: %+v", status)
	}

	client := registry.Client(BusinessChatTitle)
	got, err := client.GenerateTextTemplate(context.Background(), map[string]string{"source_text": "hello"}, "", "", 0, 0)
	if err != nil {
		t.Fatalf("generate configured title: %v", err)
	}
	if got != "Configured title" {
		t.Fatalf("generated title = %q", got)
	}
	if len(requests) != 1 {
		t.Fatalf("request count = %d, want 1", len(requests))
	}
	if authHeaders[0] != "Bearer test-secret" {
		t.Fatalf("authorization header = %q", authHeaders[0])
	}
	if requests[0]["model"] != "configured-model" {
		t.Fatalf("model = %v", requests[0]["model"])
	}
	messages, ok := requests[0]["messages"].([]any)
	if !ok || len(messages) != 2 || messages[0].(map[string]any)["content"] != "System hello" || messages[1].(map[string]any)["content"] != "First hello" {
		t.Fatalf("rendered messages = %#v", requests[0]["messages"])
	}
	if got := requests[0]["temperature"]; got != 0.7 {
		t.Fatalf("temperature = %v, want 0.7", got)
	}
	if err := os.Setenv("MULTICA_TEST_TITLE_KEY", "rotated-secret"); err != nil {
		t.Fatalf("rotate API key env: %v", err)
	}

	writeBusinessFile(t, directory, "chat-title.yaml", strings.ReplaceAll(`version: 1
business: chat-title
enabled: true
llm:
  provider: openai-compatible
  base_url: BASE_URL
  api_key_env: MULTICA_TEST_TITLE_KEY
  model: reloaded-model
  temperature: 0.2
prompt:
  system: "Reloaded"
  user_template: "Second {{source_text}}"
output:
  format: text
`, "BASE_URL", server.URL+"/"))
	registry.Reload()
	status = registry.Status(BusinessChatTitle)
	if status.State != BusinessLoadActive || status.Version != 1 || status.Fingerprint == "" {
		t.Fatalf("unexpected reloaded status: %+v", status)
	}
	if _, err := client.GenerateTextTemplate(context.Background(), map[string]string{"source_text": "world"}, "", "", 0, 0); err != nil {
		t.Fatalf("generate reloaded title: %v", err)
	}
	if requests[1]["model"] != "reloaded-model" {
		t.Fatalf("reloaded model = %v", requests[1]["model"])
	}
	if authHeaders[1] != "Bearer rotated-secret" {
		t.Fatalf("reloaded authorization header = %q", authHeaders[1])
	}
	reloadedMessages := requests[1]["messages"].([]any)
	if reloadedMessages[0].(map[string]any)["content"] != "Reloaded" || reloadedMessages[1].(map[string]any)["content"] != "Second world" {
		t.Fatalf("reloaded messages = %#v", requests[1]["messages"])
	}
}

func TestBusinessRegistryLoadsIndependentSubissueStages(t *testing.T) {
	var requests []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		requests = append(requests, request)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"test","object":"chat.completion","created":1,"model":"configured-model","choices":[{"index":0,"message":{"role":"assistant","content":"{\"plans\":[]}"},"finish_reason":"stop"}]}`)
	}))
	defer server.Close()

	t.Setenv("MULTICA_TEST_STAGE_KEY", "stage-secret")
	directory := t.TempDir()
	content := strings.ReplaceAll(`version: 1
business: subissue-suggest
enabled: true
stages:
  recognize:
    llm:
      provider: openai-compatible
      base_url: BASE_URL
      api_key_env: MULTICA_TEST_STAGE_KEY
      model: recognize-model
      timeout_ms: 5000
    prompt:
      system: "recognize system"
      user_template: "recognize {{comment_segment}} {{segment_index}} {{segment_total}} {{retry_feedback}}"
    output:
      format: json
      json_schema:
        type: object
  outline:
    llm:
      provider: openai-compatible
      base_url: BASE_URL
      api_key_env: MULTICA_TEST_STAGE_KEY
      model: outline-model
      timeout_ms: 5000
    prompt:
      system: "outline system"
      user_template: "outline {{comment_text}} {{human_constraints}} {{identified_tasks}} {{retry_feedback}}"
    output:
      format: json
      json_schema:
        type: object
  detail:
    llm:
      provider: openai-compatible
      base_url: BASE_URL
      api_key_env: MULTICA_TEST_STAGE_KEY
      model: detail-model
      timeout_ms: 5000
    prompt:
      system: "detail system"
      user_template: "detail {{approved_outline}} {{retry_feedback}}"
    output:
      format: json
      json_schema:
        type: object
`, "BASE_URL", server.URL+"/")
	writeBusinessFile(t, directory, "subissue-suggest.yaml", content)

	registry := NewBusinessRegistry(BusinessRegistryConfig{
		Directory:  directory,
		HTTPClient: server.Client(),
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if status := registry.Status(BusinessSubissueSuggest); status.State != BusinessLoadActive {
		t.Fatalf("staged subissue status = %+v", status)
	}

	outline := registry.Client(BusinessSubissueSuggest).Stage(BusinessStageOutline)
	if _, err := outline.GenerateJSONTemplate(context.Background(), map[string]string{
		"comment_text":      "comment",
		"human_constraints": "keep together",
		"identified_tasks":  "（无）",
		"retry_feedback":    "（无）",
	}, "", "", 0, 0); err != nil {
		t.Fatalf("outline generation: %v", err)
	}
	detail := registry.Client(BusinessSubissueSuggest).Stage(BusinessStageDetail)
	if _, err := detail.GenerateJSONTemplate(context.Background(), map[string]string{
		"approved_outline": "outline",
		"retry_feedback":   "（无）",
	}, "", "", 0, 0); err != nil {
		t.Fatalf("detail generation: %v", err)
	}
	if len(requests) != 2 {
		t.Fatalf("request count = %d, want 2", len(requests))
	}
	if requests[0]["model"] != "outline-model" || requests[1]["model"] != "detail-model" {
		t.Fatalf("stage models = %v, %v", requests[0]["model"], requests[1]["model"])
	}
	messages, ok := requests[0]["messages"].([]any)
	if !ok || len(messages) == 0 {
		t.Fatal("expected staged request messages")
	}
}

func TestCheckedInSubissueConfigUsesBothStages(t *testing.T) {
	path := filepath.Join("..", "..", "..", "docker", "config", "subissue-suggest.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read checked-in subissue config: %v", err)
	}
	parsed, err := parseBusinessFile(BusinessSubissueSuggest, businessDefinitions[BusinessSubissueSuggest], data)
	if err != nil {
		t.Fatalf("parse checked-in subissue config: %v", err)
	}
	if len(parsed.Stages) != 3 {
		t.Fatalf("checked-in config stages = %d, want 3", len(parsed.Stages))
	}
	for _, stage := range []string{BusinessStageRecognize, BusinessStageOutline, BusinessStageDetail} {
		if parsed.Stages[stage] == nil {
			t.Fatalf("checked-in config missing stage %q", stage)
		}
	}
	outline := parsed.Stages[BusinessStageOutline]
	if !strings.Contains(outline.Prompt.System, "business") || !strings.Contains(outline.Prompt.System, "整体流程") {
		t.Fatal("outline prompt must explicitly require a concrete business and reserve overall-flow fallback")
	}
	if !strings.Contains(outline.Prompt.System, "identified_tasks") || !strings.Contains(outline.Prompt.System, "全量覆盖") {
		t.Fatal("outline prompt must require the identified task list and the full-coverage first plan (SIY-147)")
	}
	for _, variable := range []string{"{{issue_description}}", "{{ancestor_brief}}", "{{business_context}}", "{{identified_tasks}}", "{{retry_feedback}}"} {
		if !strings.Contains(outline.Prompt.UserTemplate, variable) {
			t.Fatalf("outline prompt missing business context variable %q", variable)
		}
	}
	// identified_tasks became a required schema key with the coverage gate.
	if err := validateBusinessJSON(`{"plans":[{"name":"方案","items":[{"title":"任务","goal":"目标"}]}]}`, outline.Output.JSONSchema); err == nil {
		t.Fatal("outline schema must require identified_tasks")
	}
	if err := validateBusinessJSON(`{"identified_tasks":[{"id":"t1","text":"任务"}],"plans":[{"name":"全量覆盖","items":[{"title":"任务","goal":"目标","business":"业务","source_task_ids":["t1"]}]}]}`, outline.Output.JSONSchema); err != nil {
		t.Fatalf("outline schema must accept the coverage contract: %v", err)
	}
	recognize := parsed.Stages[BusinessStageRecognize]
	for _, variable := range []string{"{{comment_segment}}", "{{segment_index}}", "{{segment_total}}", "{{retry_feedback}}"} {
		if !strings.Contains(recognize.Prompt.UserTemplate, variable) {
			t.Fatalf("recognize prompt missing variable %q", variable)
		}
	}
	if err := validateBusinessJSON(`{"tasks":[{"id":"t1","text":"任务"}]}`, recognize.Output.JSONSchema); err != nil {
		t.Fatalf("recognize schema must accept task lists: %v", err)
	}
	detail := parsed.Stages[BusinessStageDetail]
	if !strings.Contains(detail.Prompt.UserTemplate, "{{retry_feedback}}") {
		t.Fatal("detail prompt must carry retry feedback for coverage retries")
	}
}

func TestBusinessRegistryIsolatesInvalidFilesAndKeepsStaleSnapshot(t *testing.T) {
	directory := t.TempDir()
	writeBusinessFile(t, directory, "chat-title.yaml", validBusinessYAML("chat-title", "text", "{{source_text}}", "{{source_text}}"))
	writeBusinessFile(t, directory, "subissue-suggest.yaml", "business: wrong-business\n")

	registry := NewBusinessRegistry(BusinessRegistryConfig{
		Directory: directory,
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if status := registry.Status(BusinessChatTitle); status.State != BusinessLoadActive {
		t.Fatalf("chat-title status = %+v", status)
	}
	if status := registry.Status(BusinessSubissueSuggest); status.State != BusinessLoadError {
		t.Fatalf("subissue status = %+v", status)
	}

	writeBusinessFile(t, directory, "chat-title.yaml", "version: 1\nbusiness: chat-title\nenabled: true\nunknown: true\n")
	registry.Reload()
	status := registry.Status(BusinessChatTitle)
	if status.State != BusinessLoadStale || status.Source != "file" || status.Version != 1 {
		t.Fatalf("chat-title stale status = %+v", status)
	}
	if !registry.Client(BusinessChatTitle).Enabled() {
		t.Fatal("stale valid snapshot should remain enabled")
	}
}

func TestBusinessRegistryValidatesConfiguredJSONSchema(t *testing.T) {
	directory := t.TempDir()
	content := `version: 1
business: subissue-suggest
enabled: true
llm:
  provider: openai-compatible
  base_url: https://example.test/v1
  api_key_env: MULTICA_TEST_SUBISSUE_KEY
  model: test-model
prompt:
  system: "Return JSON"
  user_template: "{{comment_text}}"
output:
  format: json
  json_schema:
    type: object
    required: [subissues]
    properties:
      subissues:
        type: array
        items:
          type: object
          required: [title]
          properties:
            title:
              type: string
`
	writeBusinessFile(t, directory, "subissue-suggest.yaml", content)
	data, err := os.ReadFile(filepath.Join(directory, "subissue-suggest.yaml"))
	if err != nil {
		t.Fatalf("read JSON schema config: %v", err)
	}
	parsed, err := parseBusinessFile(BusinessSubissueSuggest, businessDefinitions[BusinessSubissueSuggest], data)
	if err != nil {
		t.Fatalf("parse valid JSON schema config: %v", err)
	}
	if parsed.Output == nil || len(parsed.Output.JSONSchema) == 0 {
		t.Fatal("parsed JSON schema is empty")
	}

	valid := map[string]any{"subissues": []any{map[string]any{"title": "one"}}}
	if err := validateBusinessJSON(mustBusinessJSON(t, valid), map[string]any{
		"type": "object",
	}); err != nil {
		t.Fatalf("validate simple JSON schema: %v", err)
	}
	if err := validateBusinessJSON(`{"subissues":[{"title":1}]}`, map[string]any{
		"type":     "object",
		"required": []any{"subissues"},
		"properties": map[string]any{
			"subissues": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type":     "object",
					"required": []any{"title"},
					"properties": map[string]any{
						"title": map[string]any{"type": "string"},
					},
				},
			},
		},
	}); err == nil {
		t.Fatal("invalid nested JSON should fail schema validation")
	}
}

func TestBusinessRegistryUsesLiteralAPIKey(t *testing.T) {
	var authHeaders []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeaders = append(authHeaders, r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"test","object":"chat.completion","created":1,"model":"configured-model","choices":[{"index":0,"message":{"role":"assistant","content":"Configured title"},"finish_reason":"stop"}]}`)
	}))
	defer server.Close()

	directory := t.TempDir()
	writeBusinessFile(t, directory, "chat-title.yaml", strings.ReplaceAll(`version: 1
business: chat-title
enabled: true
llm:
  provider: openai-compatible
  base_url: BASE_URL
  api_key: yaml-literal-key
  model: configured-model
prompt:
  system: "System {{source_text}}"
  user_template: "{{source_text}}"
output:
  format: text
`, "BASE_URL", server.URL+"/"))

	registry := NewBusinessRegistry(BusinessRegistryConfig{
		Directory:  directory,
		HTTPClient: server.Client(),
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if _, err := registry.Client(BusinessChatTitle).GenerateTextTemplate(
		context.Background(), map[string]string{"source_text": "hello"}, "", "", 0, 0,
	); err != nil {
		t.Fatalf("generate configured title: %v", err)
	}
	if len(authHeaders) != 1 || authHeaders[0] != "Bearer yaml-literal-key" {
		t.Fatalf("authorization header = %#v", authHeaders)
	}
}

func TestBusinessConfigRequiresExactlyOneAPIKeySource(t *testing.T) {
	tests := []struct {
		name   string
		fields string
	}{
		{name: "missing", fields: "api_key: \"\""},
		{name: "literal", fields: "api_key: literal-key"},
		{name: "environment", fields: "api_key_env: MULTICA_TEST_KEY"},
		{name: "both", fields: "api_key: literal-key\n  api_key_env: MULTICA_TEST_KEY"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			yaml := strings.Replace(
				validBusinessYAML("chat-title", "text", "system", "user"),
				"api_key_env: MULTICA_TEST_KEY",
				test.fields,
				1,
			)
			_, err := parseBusinessFile(BusinessChatTitle, businessDefinitions[BusinessChatTitle], []byte(yaml))
			switch test.name {
			case "literal", "environment":
				if err != nil {
					t.Fatalf("parse valid config: %v", err)
				}
			default:
				if err == nil || !strings.Contains(err.Error(), "exactly one of llm.api_key or llm.api_key_env is required") {
					t.Fatalf("unexpected error: %v", err)
				}
			}
		})
	}
}

func TestBusinessConfigValidatesReasoningEffort(t *testing.T) {
	tests := []struct {
		name    string
		effort  string
		wantErr bool
	}{
		{name: "empty", effort: ""},
		{name: "high", effort: "high"},
		{name: "none", effort: "none"},
		{name: "invalid", effort: "maximum", wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			yaml := validBusinessYAML("chat-title", "text", "system", "user")
			if test.effort != "" {
				yaml = strings.Replace(yaml, "  model: test-model\n", "  model: test-model\n  reasoning_effort: "+test.effort+"\n", 1)
			}
			_, err := parseBusinessFile(BusinessChatTitle, businessDefinitions[BusinessChatTitle], []byte(yaml))
			if test.wantErr != (err != nil) {
				t.Fatalf("parseBusinessFile error = %v, wantErr %v", err, test.wantErr)
			}
			if test.wantErr && !strings.Contains(err.Error(), "llm.reasoning_effort must be one of") {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestBusinessRegistrySendsConfiguredReasoningEffort(t *testing.T) {
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"test","object":"chat.completion","created":1,"model":"configured-model","choices":[{"index":0,"message":{"role":"assistant","content":"Configured title"},"finish_reason":"stop"}]}`)
	}))
	defer server.Close()

	t.Setenv("MULTICA_TEST_TITLE_KEY", "test-secret")
	directory := t.TempDir()
	writeBusinessFile(t, directory, "chat-title.yaml", strings.ReplaceAll(`version: 1
business: chat-title
enabled: true
llm:
  provider: openai-compatible
  base_url: BASE_URL
  api_key_env: MULTICA_TEST_TITLE_KEY
  model: gemini-3.8-flash
  reasoning_effort: high
  temperature: 0.2
prompt:
  system: "System {{source_text}}"
  user_template: "{{source_text}}"
output:
  format: text
`, "BASE_URL", server.URL+"/"))

	registry := NewBusinessRegistry(BusinessRegistryConfig{
		Directory:  directory,
		HTTPClient: server.Client(),
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if _, err := registry.Client(BusinessChatTitle).GenerateTextTemplate(
		context.Background(), map[string]string{"source_text": "hello"}, "", "", 0, 0,
	); err != nil {
		t.Fatalf("generate configured title: %v", err)
	}
	if gotBody["reasoning_effort"] != "high" {
		t.Fatalf("reasoning_effort = %#v, want high", gotBody["reasoning_effort"])
	}
	if gotBody["temperature"] != 0.2 {
		t.Fatalf("temperature = %#v, want 0.2", gotBody["temperature"])
	}
}

func TestDockerBusinessConfigTemplatesMatchRegistry(t *testing.T) {
	for _, business := range SupportedBusinesses() {
		definition := businessDefinitions[business]
		path := filepath.Join("..", "..", "..", "docker", "config", definition.fileName)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read Docker config %s: %v", definition.fileName, err)
		}
		if _, err := parseBusinessFile(business, definition, data); err != nil {
			t.Fatalf("parse Docker config %s: %v", definition.fileName, err)
		}
	}
}

func TestStripBusinessJSONFence(t *testing.T) {
	const payload = `{"plans":[]}`
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "unfenced", raw: payload, want: payload},
		{name: "json fenced", raw: "```json\n" + payload + "\n```", want: payload},
		{name: "bare fenced", raw: "```\n" + payload + "\n```", want: payload},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := StripJSONFence(test.raw); got != test.want {
				t.Fatalf("StripJSONFence() = %q, want %q", got, test.want)
			}
		})
	}
}

func validBusinessYAML(business, format, system, user string) string {
	return "version: 1\n" +
		"business: " + business + "\n" +
		"enabled: true\n" +
		"llm:\n" +
		"  provider: openai-compatible\n" +
		"  base_url: https://example.test/v1\n" +
		"  api_key_env: MULTICA_TEST_KEY\n" +
		"  model: test-model\n" +
		"prompt:\n" +
		"  system: '" + system + "'\n" +
		"  user_template: '" + user + "'\n" +
		"output:\n" +
		"  format: " + format + "\n"
}

func writeBusinessFile(t *testing.T, directory, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(directory, name), []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func mustBusinessJSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal JSON: %v", err)
	}
	return string(encoded)
}
