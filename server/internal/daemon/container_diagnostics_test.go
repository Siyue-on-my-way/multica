package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/cli"
)

func testContainerDiagConfig() *cli.ContainerDiagnosticsConfig {
	return &cli.ContainerDiagnosticsConfig{
		Enabled:    true,
		Engine:     "podman",
		Containers: []string{"ai-cms-api", "ai-cms-worker", "ai-cms-runtime"},
	}
}

func newTestCapability(t *testing.T, cfg *cli.ContainerDiagnosticsConfig) *containerDiagnostics {
	t.Helper()
	capability, err := newContainerDiagnostics(cfg, func(string) (string, error) { return "/fake/podman", nil }, nil)
	if err != nil {
		t.Fatalf("newContainerDiagnostics: %v", err)
	}
	return capability
}

func TestNewContainerDiagnosticsValidation(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*cli.ContainerDiagnosticsConfig)
		wantErr string
	}{
		{name: "disabled", mutate: func(c *cli.ContainerDiagnosticsConfig) { c.Enabled = false }, wantErr: "not enabled"},
		{name: "no containers", mutate: func(c *cli.ContainerDiagnosticsConfig) { c.Containers = nil }, wantErr: "at least one container"},
		{name: "wildcard name", mutate: func(c *cli.ContainerDiagnosticsConfig) { c.Containers = []string{"ai-cms-*"} }, wantErr: "invalid container name"},
		{name: "shell metachar name", mutate: func(c *cli.ContainerDiagnosticsConfig) { c.Containers = []string{"ai-cms-api; rm -rf /"} }, wantErr: "invalid container name"},
		{name: "whitespace name", mutate: func(c *cli.ContainerDiagnosticsConfig) { c.Containers = []string{"ai cms"} }, wantErr: "invalid container name"},
		{name: "unsupported engine", mutate: func(c *cli.ContainerDiagnosticsConfig) { c.Engine = "kubectl" }, wantErr: "unsupported engine"},
		{name: "relative engine path", mutate: func(c *cli.ContainerDiagnosticsConfig) { c.EnginePath = "podman.exe" }, wantErr: "absolute path"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := testContainerDiagConfig()
			tc.mutate(cfg)
			_, err := newContainerDiagnostics(cfg, func(string) (string, error) { return "/fake/podman", nil }, nil)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestNewContainerDiagnosticsEngineLookupFailsClosed(t *testing.T) {
	_, err := newContainerDiagnostics(testContainerDiagConfig(), func(string) (string, error) {
		return "", errors.New("not found")
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "engine_path") {
		t.Fatalf("want actionable engine_path error, got %v", err)
	}
}

func TestContainerDiagnosticsStatusFiltersToAllowlist(t *testing.T) {
	capability := newTestCapability(t, testContainerDiagConfig())
	capability.run = func(ctx context.Context, path string, args []string) ([]byte, []byte, error) {
		if len(args) < 3 || args[0] != "ps" || args[1] != "-a" || args[2] != "--format" {
			t.Fatalf("unexpected ps argv: %v", args)
		}
		return []byte(`[
			{"Names":["ai-cms-api"],"State":"running","Status":"Up 3 hours","Image":"ai-cms-api:1"},
			{"Names":["postgres-secret-db"],"State":"running","Status":"Up 3 hours","Image":"postgres:16"}
		]`), nil, nil
	}
	result, err := capability.Status(context.Background(), containerDiagAudit{TaskID: "t1"})
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	raw, _ := json.Marshal(result)
	if strings.Contains(string(raw), "postgres-secret-db") {
		t.Fatalf("status leaked a non-allowlisted container: %s", raw)
	}
	if !strings.Contains(string(raw), "ai-cms-api") {
		t.Fatalf("status missing allowlisted container: %s", raw)
	}
}

func TestContainerDiagnosticsLogsRejectsForeignContainer(t *testing.T) {
	capability := newTestCapability(t, testContainerDiagConfig())
	_, err := capability.Logs(context.Background(), "postgres-secret-db", "", 10, containerDiagAudit{TaskID: "t1"})
	var notAllowed containerDiagNotAllowedError
	if !errorsAs(err, &notAllowed) {
		t.Fatalf("want notAllowed error, got %v", err)
	}
	if !strings.Contains(err.Error(), "ai-cms-api") || !strings.Contains(err.Error(), "allowlist") {
		t.Fatalf("refusal must name the allowed set, got: %v", err)
	}
}

func TestContainerDiagnosticsLogsClampsTailAndSince(t *testing.T) {
	capability := newTestCapability(t, testContainerDiagConfig())
	var gotArgs []string
	capability.run = func(ctx context.Context, path string, args []string) ([]byte, []byte, error) {
		gotArgs = args
		return []byte("log line\n"), nil, nil
	}
	if _, err := capability.Logs(context.Background(), "ai-cms-api", "999h", 100000, containerDiagAudit{}); err != nil {
		t.Fatalf("Logs: %v", err)
	}
	joined := strings.Join(gotArgs, " ")
	if !strings.Contains(joined, "--tail 500") {
		t.Fatalf("tail not clamped to %d: %v", containerDiagMaxLogTail, gotArgs)
	}
	if !strings.Contains(joined, "--since "+containerDiagMaxSince.Truncate(time.Second).String()) {
		t.Fatalf("since not clamped: %v", gotArgs)
	}
	// The container name must be the ONLY free-form argument, passed as its
	// own argv entry — never interpolated into a shell string.
	if gotArgs[len(gotArgs)-1] != "ai-cms-api" {
		t.Fatalf("container name not the final argv entry: %v", gotArgs)
	}
}

func TestContainerDiagnosticsInspectMasksSensitiveValues(t *testing.T) {
	capability := newTestCapability(t, testContainerDiagConfig())
	capability.run = func(ctx context.Context, path string, args []string) ([]byte, []byte, error) {
		return []byte(`[{"Name":"ai-cms-api","Config":{"Env":["POSTGRES_PASSWORD=hunter2","API_TOKEN=abcd1234","PATH=/usr/bin"]}}]`), nil, nil
	}
	raw, err := json.Marshal(mustInspect(t, capability))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	out := string(raw)
	if strings.Contains(out, "hunter2") || strings.Contains(out, "abcd1234") {
		t.Fatalf("sensitive values not masked: %s", out)
	}
	if !strings.Contains(out, "POSTGRES_PASSWORD") || !strings.Contains(out, "API_TOKEN") {
		t.Fatalf("masking must keep key names visible for diagnosis: %s", out)
	}
	if !strings.Contains(out, "/usr/bin") {
		t.Fatalf("non-sensitive values must survive: %s", out)
	}
}

func mustInspect(t *testing.T, capability *containerDiagnostics) any {
	t.Helper()
	result, err := capability.Inspect(context.Background(), "ai-cms-api", containerDiagAudit{})
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	return result
}

func TestMaskSensitiveValues(t *testing.T) {
	cases := []struct{ in, want, notWant string }{
		{in: `PASSWORD=opensesame`, want: `PASSWORD=***`},
		{in: `"client_secret": "topsecret"`, want: `"client_secret": "***`},
		{in: `MULTICA_TOKEN=abc`, want: `MULTICA_TOKEN=***`},
		{in: `api-key-value: sk-1234567890`, want: `api-key-value: ***`},
	}
	for _, tc := range cases {
		got := maskSensitiveValues(tc.in)
		if !strings.Contains(got, tc.want) {
			t.Errorf("maskSensitiveValues(%q) = %q, want it to contain %q", tc.in, got, tc.want)
		}
	}
	// Ordinary values are never touched.
	if got := maskSensitiveValues("image=ai-cms-api:1"); got != "image=ai-cms-api:1" {
		t.Errorf("non-sensitive value rewritten: %q", got)
	}
}

func TestParseDiagSince(t *testing.T) {
	cases := []struct {
		in   string
		want time.Duration
		err  bool
	}{
		{in: "30m", want: 30 * time.Minute},
		{in: "2h", want: 2 * time.Hour},
		{in: "45s", want: 45 * time.Second},
		{in: "7d", want: 7 * 24 * time.Hour},
		{in: "$(rm -rf /)", err: true},
		{in: "1h30m", err: true},
		{in: "", err: true},
	}
	for _, tc := range cases {
		got, err := parseDiagSince(tc.in)
		if tc.err {
			if err == nil {
				t.Errorf("parseDiagSince(%q): want error", tc.in)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("parseDiagSince(%q) = %v, %v; want %v", tc.in, got, err, tc.want)
		}
	}
}

func TestParseContainerPSListShapes(t *testing.T) {
	// Podman: Names is an array.
	list := parseContainerPSList([]byte(`[{"Names":["ai-cms-api"],"State":"running"}]`))
	if len(list) != 1 || firstContainerName(list[0]["Names"]) != "ai-cms-api" {
		t.Fatalf("podman shape not parsed: %v", list)
	}
	// Newer docker: Names is a plain string.
	list = parseContainerPSList([]byte(`[{"Names":"/ai-cms-worker","State":"running"}]`))
	if len(list) != 1 || firstContainerName(list[0]["Names"]) != "ai-cms-worker" {
		t.Fatalf("docker shape not parsed: %v", list)
	}
	// Garbage is skipped, not fatal.
	if got := parseContainerPSList([]byte(`not json`)); got != nil {
		t.Fatalf("garbage should parse to nil, got %v", got)
	}
}

// --- handler ---

func newContainerDiagTestDaemon(t *testing.T, capability *containerDiagnostics) *Daemon {
	t.Helper()
	return &Daemon{
		logger:        slog.New(slog.NewTextHandler(&strings.Builder{}, nil)),
		containerDiag: capability,
	}
}

func containerDiagRequest(d *Daemon, token string, payload string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/containers/diagnostics", strings.NewReader(payload))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	d.containerDiagnosticsHandler()(rec, req)
	return rec
}

func TestContainerDiagnosticsHandlerAuth(t *testing.T) {
	capability := newTestCapability(t, testContainerDiagConfig())
	capability.run = func(ctx context.Context, path string, args []string) ([]byte, []byte, error) {
		return []byte(`[]`), nil, nil
	}
	d := newContainerDiagTestDaemon(t, capability)
	d.registerActiveRepoCheckoutTask("live-token", activeRepoCheckoutTask{TaskID: "t1", AgentID: "a1", AgentName: "tester"})

	if rec := containerDiagRequest(d, "", `{"operation":"status"}`); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no credential: want 401, got %d", rec.Code)
	}
	if rec := containerDiagRequest(d, "stale-token", `{"operation":"status"}`); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unknown credential: want 401, got %d", rec.Code)
	}
	if rec := containerDiagRequest(d, "live-token", `{"operation":"status"}`); rec.Code != http.StatusOK {
		t.Fatalf("live credential: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestContainerDiagnosticsHandlerNotConfiguredIsActionable(t *testing.T) {
	d := newContainerDiagTestDaemon(t, nil)
	d.registerActiveRepoCheckoutTask("live-token", activeRepoCheckoutTask{TaskID: "t1"})
	rec := containerDiagRequest(d, "live-token", `{"operation":"status"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"not configured", "containers configure", "restart the daemon"} {
		if !strings.Contains(body, want) {
			t.Fatalf("404 body must be actionable (%q missing): %s", want, body)
		}
	}
}

func TestContainerDiagnosticsHandlerRejectsBadOperationAndForeignContainer(t *testing.T) {
	capability := newTestCapability(t, testContainerDiagConfig())
	capability.run = func(ctx context.Context, path string, args []string) ([]byte, []byte, error) {
		t.Fatalf("engine must not run for a refused request: %v", args)
		return nil, nil, nil
	}
	d := newContainerDiagTestDaemon(t, capability)
	d.registerActiveRepoCheckoutTask("live-token", activeRepoCheckoutTask{TaskID: "t1"})

	if rec := containerDiagRequest(d, "live-token", `{"operation":"stop","container":"ai-cms-api"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("mutating op: want 400, got %d: %s", rec.Code, rec.Body.String())
	}
	rec := containerDiagRequest(d, "live-token", `{"operation":"logs","container":"ai-cms-api; shutdown"}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("injected container name: want 403, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "allowed") {
		t.Fatalf("403 must list the allowed set: %s", rec.Body.String())
	}
}

func TestContainerDiagnosticsHandlerEngineFailureSurfacesStderr(t *testing.T) {
	capability := newTestCapability(t, testContainerDiagConfig())
	capability.run = func(ctx context.Context, path string, args []string) ([]byte, []byte, error) {
		return nil, []byte("cannot connect to podman socket"), errors.New("exit status 125")
	}
	d := newContainerDiagTestDaemon(t, capability)
	d.registerActiveRepoCheckoutTask("live-token", activeRepoCheckoutTask{TaskID: "t1"})

	rec := containerDiagRequest(d, "live-token", `{"operation":"logs","container":"ai-cms-api"}`)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("want 502, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "podman socket") {
		t.Fatalf("502 must carry the engine's stderr: %s", rec.Body.String())
	}
}

// --- prompt advertisement ---

func TestBuildPromptContainerDiagnosticsHint(t *testing.T) {
	task := Task{ID: "t1"}
	hint := (&containerDiagnostics{containers: map[string]struct{}{"ai-cms-api": {}}}).promptHint()

	with := BuildPrompt(task, "codex", WithContainerDiagnosticsHint(hint))
	if !strings.Contains(with, "## Container diagnostics (this runtime)") ||
		!strings.Contains(with, "multica containers status") {
		t.Fatalf("hint missing from prompt when capability configured")
	}
	without := BuildPrompt(task, "codex")
	if strings.Contains(without, "Container diagnostics") {
		t.Fatalf("hint must be absent when capability not configured")
	}
}

// --- config round-trip ---

func TestCLIConfigContainerDiagnosticsRoundTrip(t *testing.T) {
	cfg := cli.CLIConfig{
		ContainerDiagnostics: &cli.ContainerDiagnosticsConfig{
			Enabled:    true,
			Engine:     "podman",
			EnginePath: `C:\Program Files\Podman\podman.exe`,
			EngineArgs: []string{"--connection=podman-machine-default"},
			Containers: []string{"ai-cms-api", "ai-cms-worker", "ai-cms-runtime"},
		},
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(raw), `"container_diagnostics"`) {
		t.Fatalf("config key missing from JSON: %s", raw)
	}
	var back cli.CLIConfig
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.ContainerDiagnostics == nil ||
		back.ContainerDiagnostics.EnginePath != cfg.ContainerDiagnostics.EnginePath ||
		len(back.ContainerDiagnostics.Containers) != 3 ||
		back.ContainerDiagnostics.Containers[0] != "ai-cms-api" {
		t.Fatalf("round-trip lost data: %+v", back.ContainerDiagnostics)
	}
	// Absent stays absent so runtimes without the capability save unchanged
	// config shapes.
	var empty cli.CLIConfig
	rawEmpty, _ := json.Marshal(empty)
	if strings.Contains(string(rawEmpty), "container_diagnostics") {
		t.Fatalf("empty config must not gain the key: %s", rawEmpty)
	}
}
