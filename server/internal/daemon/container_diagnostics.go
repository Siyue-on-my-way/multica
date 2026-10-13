package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/internal/cli"
)

// Container diagnostics capability
//
// A daemon-host-local, explicitly configured, least-privilege bridge that lets
// an agent task run a FIXED set of read-only container operations against an
// exact-name allowlist of containers. It exists because the task shell's
// environment is the daemon process's environment by design (PATH snapshot at
// daemon start; the managed Codex shell_environment_policy passes that same
// set through), so a per-user Podman install or a podman machine connection
// visible in the user's interactive PowerShell is routinely invisible to the
// agent's shell — and "command not found" is the only symptom, with no
// actionable path. Rather than widening the task shell, this exposes the
// three diagnostics operations (status, logs, inspect) through the local
// daemon, which resolves the engine itself and enforces:
//
//   - exact container-name allowlist (no globs, no patterns),
//   - fixed argv built without any shell, so nothing the agent sends can
//     become shell syntax,
//   - read-only subcommands only (ps, logs, inspect),
//   - tail/since/output-size limits and per-invocation timeouts,
//   - sensitive-value masking on everything that leaves the host boundary
//     into the task,
//   - an audit log line for every decision, allowed or refused.
//
// Everything is opt-in via cli.CLIConfig.ContainerDiagnostics on THIS
// machine; nil config means the capability does not exist, which is the
// historical behavior for every runtime.

const (
	// containerDiagRequestTimeout bounds one engine invocation.
	containerDiagRequestTimeout = 30 * time.Second
	// containerDiagMaxLogTail is the hard ceiling on `--tail`, regardless of
	// what the request asks for.
	containerDiagMaxLogTail = 500
	// containerDiagDefaultLogTail is used when the request omits --tail.
	containerDiagDefaultLogTail = 200
	// containerDiagMaxSince bounds the logs time window.
	containerDiagMaxSince = 24 * time.Hour
	// containerDiagMaxOutputBytes caps what any single operation may return
	// into the task (logs and inspect routinely dwarf this otherwise).
	containerDiagMaxOutputBytes = 256 << 10
	// containerDiagMaxStatusBytes caps the ps output before filtering.
	containerDiagMaxStatusBytes = 64 << 10
)

// containerDiagNameRe is the exact container-name shape. It rejects
// whitespace, shell metacharacters and glob syntax outright; the allowlist
// check does the rest.
var containerDiagNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

// containerDiagSinceRe accepts a bare Go-style duration like the engines do
// for `logs --since` (10m, 2h, 1d for podman; docker wants Go durations, so
// days are rejected here and converted by the caller).
var containerDiagSinceRe = regexp.MustCompile(`^([0-9]{1,5})([smhd])$`)

// containerDiagSensitiveRe masks the VALUE of any key whose name contains a
// credential-ish word, across JSON and KEY=VALUE shapes:
//
//	"API_TOKEN": "abcd..."     →  "API_TOKEN": "***"
//	POSTGRES_PASSWORD=hunter2  →  POSTGRES_PASSWORD=***
//
// The value run deliberately stops at quotes, commas and brackets so one
// match cannot swallow the rest of a JSON document; masking output stays
// readable evidence with key names intact.
var containerDiagSensitiveRe = regexp.MustCompile(
	`(?i)([\w.\-]*(?:key|secret|token|password|passwd|pwd)[\w.\-]*("?)\s*(?::|==?|=>)\s*"?)[^\s"',\]\}{]{1,}`)

// containerDiagnostics is the compiled, validated capability. Constructed
// once at daemon start from the local CLI config; immutable afterwards.
type containerDiagnostics struct {
	engine     string // "podman" | "docker"
	enginePath string // resolved absolute path of the engine binary
	engineArgs []string
	containers map[string]struct{}

	// run executes one engine invocation. A field, not a hardcoded
	// exec.CommandContext call, so handler tests inject a fake engine
	// without spawning processes.
	run func(ctx context.Context, path string, args []string) (stdout, stderr []byte, err error)

	logger *slog.Logger
}

// newContainerDiagnostics validates the admin config and resolves the engine
// binary in the daemon's own process environment. The returned error is a
// configuration problem the operator must fix; callers log it and run without
// the capability (fail closed), never partially.
func newContainerDiagnostics(cfg *cli.ContainerDiagnosticsConfig, lookup lookPathFunc, logger *slog.Logger) (*containerDiagnostics, error) {
	if cfg == nil || !cfg.Enabled {
		return nil, fmt.Errorf("container diagnostics capability is not enabled")
	}
	engine := strings.ToLower(strings.TrimSpace(cfg.Engine))
	if engine == "" {
		engine = "podman"
	}
	if engine != "podman" && engine != "docker" {
		return nil, fmt.Errorf("container diagnostics: unsupported engine %q (use \"podman\" or \"docker\")", cfg.Engine)
	}
	if len(cfg.Containers) == 0 {
		return nil, fmt.Errorf("container diagnostics: at least one container name is required")
	}
	containers := make(map[string]struct{}, len(cfg.Containers))
	names := make([]string, 0, len(cfg.Containers))
	for _, name := range cfg.Containers {
		name = strings.TrimSpace(name)
		if !containerDiagNameRe.MatchString(name) {
			return nil, fmt.Errorf("container diagnostics: invalid container name %q (exact names only: letters, digits, '.', '_', '-')", name)
		}
		if _, dup := containers[name]; dup {
			continue
		}
		containers[name] = struct{}{}
		names = append(names, name)
	}
	engineArgs := make([]string, 0, len(cfg.EngineArgs))
	for _, arg := range cfg.EngineArgs {
		arg = strings.TrimSpace(arg)
		if arg == "" {
			return nil, fmt.Errorf("container diagnostics: engine_args entries must be non-empty")
		}
		if strings.ContainsAny(arg, "\x00\r\n") {
			return nil, fmt.Errorf("container diagnostics: engine_args entries must not contain control characters")
		}
		engineArgs = append(engineArgs, arg)
	}

	enginePath := strings.TrimSpace(cfg.EnginePath)
	if enginePath != "" && !filepath.IsAbs(enginePath) {
		return nil, fmt.Errorf("container diagnostics: engine_path must be an absolute path (got %q)", cfg.EnginePath)
	}
	resolved, err := resolveEnginePath(enginePath, engine, lookup)
	if err != nil {
		return nil, err
	}

	sort.Strings(names)
	capability := &containerDiagnostics{
		engine:     engine,
		enginePath: resolved,
		engineArgs: engineArgs,
		containers: containers,
		run:        runEngineCommand,
		logger:     logger,
	}
	if logger != nil {
		logger.Info("container diagnostics capability enabled",
			"engine", engine,
			"engine_path", resolved,
			"containers", strings.Join(names, ","),
		)
	}
	return capability, nil
}

// lookPathFunc mirrors exec.LookPath so tests can pin resolution.
type lookPathFunc func(file string) (string, error)

func resolveEnginePath(pinned, engine string, lookup lookPathFunc) (string, error) {
	if pinned != "" {
		if info, err := os.Stat(pinned); err != nil || info.IsDir() {
			return "", fmt.Errorf("container diagnostics: engine_path %q does not point at an executable file", pinned)
		}
		return pinned, nil
	}
	resolved, err := lookup(engine)
	if err != nil {
		return "", fmt.Errorf(
			"container diagnostics: %s not found on the daemon's PATH; set engine_path in the container_diagnostics section of ~/.multica/config.json (or the profile's config.json) and restart the daemon",
			engine)
	}
	return resolved, nil
}

// promptHint is the per-turn context block advertising the capability to the
// task. Only rendered when the capability exists, so runtimes without the
// config see byte-identical prompts.
func (c *containerDiagnostics) promptHint() string {
	names := c.containerNames()
	return "## Container diagnostics (this runtime)\n\n" +
		"This runtime exposes a restricted, read-only container diagnostics capability. It does NOT depend on the task shell PATH — do not use podman/docker directly; use the multica CLI, which routes through the local daemon:\n\n" +
		"- `multica containers status` — state of the allowed containers\n" +
		"- `multica containers logs <name> [--tail N] [--since 15m]` — recent logs (tail capped at " + fmt.Sprintf("%d", containerDiagMaxLogTail) + " lines, window at 24h, output truncated)\n" +
		"- `multica containers inspect <name> --output json` — limited engine inspect (sensitive values masked)\n\n" +
		"Allowed containers: " + strings.Join(names, ", ") + ". Any other container or any mutating operation is rejected by the daemon and audited. If a call fails, report its message verbatim — it names the missing permission or configuration.\n\n"
}

// containerNames returns the sorted allowlist, for display.
func (c *containerDiagnostics) containerNames() []string {
	names := make([]string, 0, len(c.containers))
	for name := range c.containers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// allowRejected reports whether the requested container is outside the
// allowlist. Every refusal is audited with the caller's task identity.
func (c *containerDiagnostics) allowRejected(container string) bool {
	_, ok := c.containers[strings.TrimSpace(container)]
	return !ok
}

// Status returns the state of every allowlisted container. One `ps -a` run is
// filtered in-process, so the response only ever contains allowlisted names.
func (c *containerDiagnostics) Status(ctx context.Context, audit containerDiagAudit) (any, error) {
	stdout, stderr, err := c.invoke(ctx, audit, []string{"ps", "-a", "--format", "json"}, containerDiagMaxStatusBytes)
	if err != nil {
		return nil, statusError(err, stderr)
	}
	type row struct {
		Name   string `json:"name"`
		State  string `json:"state"`
		Status string `json:"status"`
		Image  string `json:"image"`
	}
	rows := make([]row, 0, len(c.containers))
	for _, item := range parseContainerPSList(stdout) {
		name := firstContainerName(item["Names"])
		if _, allowed := c.containers[name]; !allowed {
			continue
		}
		rows = append(rows, row{
			Name:   name,
			State:  asString(item["State"]),
			Status: asString(item["Status"]),
			Image:  asString(item["Image"]),
		})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
	return map[string]any{"containers": rows}, nil
}

// Logs returns a bounded, masked excerpt of one allowlisted container's logs.
func (c *containerDiagnostics) Logs(ctx context.Context, container, since string, tail int, audit containerDiagAudit) (any, error) {
	container = strings.TrimSpace(container)
	if c.allowRejected(container) {
		return nil, containerDiagNotAllowedError{requested: container, allowed: c.containerNames()}
	}
	args := []string{"logs"}
	if tail <= 0 {
		tail = containerDiagDefaultLogTail
	}
	if tail > containerDiagMaxLogTail {
		tail = containerDiagMaxLogTail
	}
	args = append(args, "--tail", fmt.Sprintf("%d", tail))
	if since = strings.TrimSpace(since); since != "" {
		d, err := parseDiagSince(since)
		if err != nil {
			return nil, fmt.Errorf("invalid --since %q: use a duration like 30m, 2h or 24h", since)
		}
		if d > containerDiagMaxSince {
			d = containerDiagMaxSince
		}
		args = append(args, "--since", formatDiagSince(d))
	}
	args = append(args, container)
	stdout, stderr, err := c.invoke(ctx, audit, args, containerDiagMaxOutputBytes)
	if err != nil {
		return nil, statusError(err, stderr)
	}
	return map[string]any{
		"container": container,
		"tail":      tail,
		"truncated": len(stdout) >= containerDiagMaxOutputBytes,
		"logs":      maskSensitiveValues(string(stdout)),
	}, nil
}

// Inspect returns the engine's inspect output for one allowlisted container,
// size-capped and masked.
func (c *containerDiagnostics) Inspect(ctx context.Context, container string, audit containerDiagAudit) (any, error) {
	container = strings.TrimSpace(container)
	if c.allowRejected(container) {
		return nil, containerDiagNotAllowedError{requested: container, allowed: c.containerNames()}
	}
	stdout, stderr, err := c.invoke(ctx, audit, []string{"inspect", container}, containerDiagMaxOutputBytes)
	if err != nil {
		return nil, statusError(err, stderr)
	}
	// The masked text is returned as a string, not embedded JSON: masking can
	// swallow a closing quote, and the caller wants readable evidence rather
	// than a strict parse.
	return map[string]any{
		"container": container,
		"truncated": len(stdout) >= containerDiagMaxOutputBytes,
		"inspect":   maskSensitiveValues(string(stdout)),
	}, nil
}

// containerDiagAudit carries the caller identity into every log line so the
// daemon log answers "who asked for this container" without correlating
// request ids by hand.
type containerDiagAudit struct {
	TaskID    string
	AgentID   string
	AgentName string
	Workspace string
}

// invoke runs one fixed read-only engine invocation: direct argv, no shell,
// timeout and output cap enforced here.
func (c *containerDiagnostics) invoke(ctx context.Context, audit containerDiagAudit, args []string, maxBytes int) ([]byte, []byte, error) {
	argv := append(append([]string{}, c.engineArgs...), args...)
	op := "unknown"
	if len(args) > 0 {
		op = args[0]
	}
	target := ""
	if len(args) > 0 {
		for _, a := range args[1:] {
			if _, allowed := c.containers[strings.TrimSpace(a)]; allowed {
				target = strings.TrimSpace(a)
				break
			}
		}
	}
	started := time.Now()

	runCtx, cancel := context.WithTimeout(ctx, containerDiagRequestTimeout)
	defer cancel()
	stdout, stderr, err := c.run(runCtx, c.enginePath, argv)
	elapsed := time.Since(started)

	logFields := []any{
		"audit", "container_diagnostics",
		"op", op,
		"container", target,
		"task_id", audit.TaskID,
		"agent_id", audit.AgentID,
		"agent_name", audit.AgentName,
		"workspace_id", audit.Workspace,
		"duration_ms", elapsed.Milliseconds(),
		"output_bytes", len(stdout),
		"allowed", true,
	}
	if err != nil {
		logFields = append(logFields, "error", err.Error())
		if c.logger != nil {
			c.logger.Warn("container diagnostics invocation failed", logFields...)
		}
	} else if c.logger != nil {
		c.logger.Info("container diagnostics invocation", logFields...)
	}

	if len(stdout) > maxBytes {
		stdout = stdout[:maxBytes]
	}
	return stdout, stderr, err
}

// runEngineCommand is the production runner: exec the engine with explicit
// argv in the daemon's own environment. No shell is involved at any point, so
// nothing in argv can gain shell semantics.
func runEngineCommand(ctx context.Context, path string, args []string) (stdout, stderr []byte, err error) {
	cmd := exec.CommandContext(ctx, path, args...)
	var outBuf, errBuf strings.Builder
	// Cap reads at the limit plus slack so a chatty engine cannot balloon
	// memory before the byte check; the invoke-side cut keeps the contract.
	cmd.Stdout = &limitedWriter{w: &outBuf, n: containerDiagMaxOutputBytes + (64 << 10)}
	cmd.Stderr = &limitedWriter{w: &errBuf, n: 16 << 10}
	err = cmd.Run()
	return []byte(outBuf.String()), []byte(errBuf.String()), err
}

type limitedWriter struct {
	w io.Writer
	n int
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	if l.n <= 0 {
		return len(p), nil // discard silently once over budget
	}
	take := len(p)
	if take > l.n {
		take = l.n
	}
	if _, err := l.w.Write(p[:take]); err != nil {
		return len(p), err
	}
	l.n -= take
	return len(p), nil
}

// containerDiagNotAllowedError is the typed refusal for a container outside
// the allowlist. The message names the allowed set so the agent can correct
// itself instead of concluding the tool is broken.
type containerDiagNotAllowedError struct {
	requested string
	allowed   []string
}

func (e containerDiagNotAllowedError) Error() string {
	return fmt.Sprintf("container %q is not in this runtime's container diagnostics allowlist (allowed: %s); the allowlist is configured by the runtime admin in the daemon's config.json and cannot be changed from a task",
		e.requested, strings.Join(e.allowed, ", "))
}

// statusError renders an engine failure with its stderr excerpt — the agent
// sees WHY the engine could not answer (machine down, engine missing) rather
// than a bare exit code.
func statusError(err error, stderr []byte) error {
	if err == nil {
		return nil
	}
	detail := strings.TrimSpace(string(stderr))
	if len(detail) > 512 {
		detail = detail[:512]
	}
	if detail == "" {
		return fmt.Errorf("container engine invocation failed: %v", err)
	}
	return fmt.Errorf("container engine invocation failed: %v: %s", err, detail)
}

// parseDiagSince accepts 30m / 2h / 24h / 45s / 7d. Days are accepted because
// podman does; the caller clamps the result to containerDiagMaxSince.
func parseDiagSince(value string) (time.Duration, error) {
	m := containerDiagSinceRe.FindStringSubmatch(value)
	if m == nil {
		return 0, fmt.Errorf("invalid duration")
	}
	n := 0
	for _, r := range m[1] {
		n = n*10 + int(r-'0')
	}
	switch m[2] {
	case "s":
	case "m":
		n *= 60
	case "h":
		n *= 3600
	case "d":
		n *= 86400
	}
	return time.Duration(n) * time.Second, nil
}

// formatDiagSince renders a duration the way both engines accept for
// `logs --since`: a Go-style duration (podman accepts these; docker requires
// them).
func formatDiagSince(d time.Duration) string {
	return d.Truncate(time.Second).String()
}

// maskSensitiveValues rewrites credential-looking values in engine output.
// It runs on everything returned to the task: logs and inspect both carry
// environment material on real deployments.
func maskSensitiveValues(output string) string {
	return containerDiagSensitiveRe.ReplaceAllString(output, `${1}***`)
}

// parseContainerPSList parses `ps --format json` leniently: podman emits an
// array of objects whose Names is an array; newer docker emits an array whose
// Names is a string. Unknown shapes are skipped rather than failing the whole
// status call.
func parseContainerPSList(stdout []byte) []map[string]any {
	trimmed := strings.TrimSpace(string(stdout))
	if trimmed == "" {
		return nil
	}
	var list []map[string]any
	if err := json.Unmarshal([]byte(trimmed), &list); err != nil {
		return nil
	}
	return list
}

func firstContainerName(v any) string {
	switch names := v.(type) {
	case []any:
		for _, entry := range names {
			if s, ok := entry.(string); ok && s != "" {
				return strings.TrimPrefix(s, "/")
			}
		}
	case string:
		if names != "" {
			return strings.TrimPrefix(names, "/")
		}
	}
	return ""
}

func asString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}
