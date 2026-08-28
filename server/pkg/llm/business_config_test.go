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
  outline:
    llm:
      provider: openai-compatible
      base_url: BASE_URL
      api_key_env: MULTICA_TEST_STAGE_KEY
      model: outline-model
      timeout_ms: 5000
    prompt:
      system: "outline system"
      user_template: "outline {{comment_text}} {{human_constraints}}"
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
      user_template: "detail {{approved_outline}}"
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
	}, "", "", 0, 0); err != nil {
		t.Fatalf("outline generation: %v", err)
	}
	detail := registry.Client(BusinessSubissueSuggest).Stage(BusinessStageDetail)
	if _, err := detail.GenerateJSONTemplate(context.Background(), map[string]string{
		"approved_outline": "outline",
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
	if len(parsed.Stages) != 2 {
		t.Fatalf("checked-in config stages = %d, want 2", len(parsed.Stages))
	}
	for _, stage := range []string{BusinessStageOutline, BusinessStageDetail} {
		if parsed.Stages[stage] == nil {
			t.Fatalf("checked-in config missing stage %q", stage)
		}
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
