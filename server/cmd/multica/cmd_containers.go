package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/spf13/cobra"
)

// Container diagnostics commands
//
// `multica containers status|logs|inspect` are the task-facing surface of the
// daemon's restricted container diagnostics capability. They run INSIDE a
// Multica task (the daemon injects MULTICA_DAEMON_PORT and the task-scoped
// MULTICA_TOKEN) and never touch a podman/docker binary themselves — the
// local daemon resolves and executes the engine, so the task shell's PATH is
// irrelevant. That is the point: a daemon launched by an autostart context
// without a per-user Podman install shows up as an explicit, actionable
// answer instead of "Get-Command podman" failing.
//
// `multica containers configure|disable` are the operator's side: human-local
// commands (refused inside daemon tasks) that edit this machine's
// ~/.multica/config.json. The allowlist is per-machine on purpose — another
// runtime or host never inherits it.

var containersCmd = &cobra.Command{
	Use:   "containers",
	Short: "Read-only container diagnostics via the local daemon (agent tasks)",
	Long: "Restricted, read-only container diagnostics served by the local Multica daemon.\n" +
		"status/logs/inspect run inside a daemon task and honor the runtime's exact-name container allowlist; " +
		"configure/disable are operator commands for the daemon host.",
	GroupID: groupRuntime,
}

var containersStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show the state of the runtime's allowed containers",
	Args:  cobra.NoArgs,
	RunE:  runContainersStatus,
}

var containersLogsCmd = &cobra.Command{
	Use:   "logs <container>",
	Short: "Fetch a bounded, masked log excerpt from an allowed container",
	Args:  cobra.ExactArgs(1),
	RunE:  runContainersLogs,
}

var containersInspectCmd = &cobra.Command{
	Use:   "inspect <container>",
	Short: "Run a limited, masked engine inspect on an allowed container",
	Args:  cobra.ExactArgs(1),
	RunE:  runContainersInspect,
}

var containersConfigureCmd = &cobra.Command{
	Use:   "configure",
	Short: "Configure this machine's container diagnostics allowlist (operator)",
	Args:  cobra.NoArgs,
	RunE:  runContainersConfigure,
}

var containersDisableCmd = &cobra.Command{
	Use:   "disable",
	Short: "Disable this machine's container diagnostics capability (operator)",
	Args:  cobra.NoArgs,
	RunE:  runContainersDisable,
}

var (
	containersOutput            string
	containersLogsTail          int
	containersLogsSince         string
	containersCfgEngine         string
	containersCfgEnginePath     string
	containersCfgEngineArgs     []string
	containersCfgContainerNames []string
)

func init() {
	containersStatusCmd.Flags().StringVar(&containersOutput, "output", "text", "output format: text | json")
	containersLogsCmd.Flags().StringVar(&containersOutput, "output", "text", "output format: text | json")
	containersLogsCmd.Flags().IntVar(&containersLogsTail, "tail", 0, "number of log lines (server caps the value)")
	containersLogsCmd.Flags().StringVar(&containersLogsSince, "since", "", "log window, e.g. 30m, 2h (server caps at 24h)")
	containersInspectCmd.Flags().StringVar(&containersOutput, "output", "text", "output format: text | json")

	containersConfigureCmd.Flags().StringVar(&containersCfgEngine, "engine", "podman", "container engine: podman | docker")
	containersConfigureCmd.Flags().StringVar(&containersCfgEnginePath, "engine-path", "", "absolute path to the engine binary (recommended when the daemon's PATH lacks it)")
	containersConfigureCmd.Flags().StringSliceVar(&containersCfgEngineArgs, "engine-arg", nil, "extra engine global argument, repeatable (e.g. --engine-arg=--connection=mymachine)")
	containersConfigureCmd.Flags().StringSliceVar(&containersCfgContainerNames, "container", nil, "exact container name allowed for read-only diagnostics, repeatable (required)")
	containersConfigureCmd.Flags().StringVar(&containersOutput, "output", "text", "output format: text | json")

	containersCmd.AddCommand(containersStatusCmd)
	containersCmd.AddCommand(containersLogsCmd)
	containersCmd.AddCommand(containersInspectCmd)
	containersCmd.AddCommand(containersConfigureCmd)
	containersCmd.AddCommand(containersDisableCmd)
}

// containersDaemonEndpoint resolves the local daemon endpoint and task
// credential from the task environment, mirroring `multica repo checkout`.
func containersDaemonEndpoint() (string, string, error) {
	daemonPort := strings.TrimSpace(os.Getenv("MULTICA_DAEMON_PORT"))
	if daemonPort == "" {
		return "", "", fmt.Errorf("MULTICA_DAEMON_PORT not set (this command is intended to be run by an agent inside a daemon task)")
	}
	taskToken := strings.TrimSpace(os.Getenv("MULTICA_TOKEN"))
	if taskToken == "" {
		return "", "", fmt.Errorf("MULTICA_TOKEN not set (container diagnostics requires the active task credential)")
	}
	return fmt.Sprintf("http://127.0.0.1:%s/containers/diagnostics", daemonPort), taskToken, nil
}

// postContainerDiagnostics sends one request and decodes the daemon's JSON
// envelope. Server messages are returned verbatim: they are written to be the
// whole actionable answer (not configured, container not allowed, engine
// failure), unlike a bare exit code.
func postContainerDiagnostics(ctx context.Context, payload map[string]any) (map[string]any, error) {
	endpoint, token, err := containersDaemonEndpoint()
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode request: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("create daemon request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("connect to daemon: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read daemon response: %w", err)
	}
	var decoded map[string]any
	_ = json.Unmarshal(body, &decoded)
	if resp.StatusCode != http.StatusOK {
		if msg, ok := decoded["error"].(string); ok && msg != "" {
			return decoded, fmt.Errorf("%s", msg)
		}
		return decoded, fmt.Errorf("container diagnostics failed (HTTP %d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return decoded, nil
}

func runContainersStatus(cmd *cobra.Command, args []string) error {
	result, err := postContainerDiagnostics(cmd.Context(), map[string]any{"operation": "status"})
	if err != nil {
		return err
	}
	return printContainersResult(result, containersOutput)
}

func runContainersLogs(cmd *cobra.Command, args []string) error {
	result, err := postContainerDiagnostics(cmd.Context(), map[string]any{
		"operation": "logs",
		"container": args[0],
		"tail":      containersLogsTail,
		"since":     containersLogsSince,
	})
	if err != nil {
		return err
	}
	return printContainersResult(result, containersOutput)
}

func runContainersInspect(cmd *cobra.Command, args []string) error {
	result, err := postContainerDiagnostics(cmd.Context(), map[string]any{
		"operation": "inspect",
		"container": args[0],
	})
	if err != nil {
		return err
	}
	return printContainersResult(result, containersOutput)
}

func printContainersResult(result map[string]any, output string) error {
	if output == "json" {
		return cli.PrintJSON(os.Stdout, result)
	}
	// Text mode renders the same payload a human can scan: one status table,
	// or the logs/inspect body with a truncation note.
	if rows, ok := result["containers"].([]any); ok {
		for _, rowRaw := range rows {
			row, ok := rowRaw.(map[string]any)
			if !ok {
				continue
			}
			fmt.Printf("%-32s %-10s %-28s %s\n",
				asContainersString(row["name"]),
				asContainersString(row["state"]),
				asContainersString(row["status"]),
				asContainersString(row["image"]))
		}
		return nil
	}
	if logs, ok := result["logs"].(string); ok && logs != "" {
		fmt.Print(logs)
		if asContainersBool(result["truncated"]) {
			fmt.Fprintln(os.Stderr, "[output truncated by the daemon's size limit]")
		}
		return nil
	}
	if inspect, ok := result["inspect"].(string); ok && inspect != "" {
		fmt.Println(inspect)
		if asContainersBool(result["truncated"]) {
			fmt.Fprintln(os.Stderr, "[output truncated by the daemon's size limit]")
		}
		return nil
	}
	return cli.PrintJSON(os.Stdout, result)
}

func asContainersString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func asContainersBool(v any) bool {
	b, ok := v.(bool)
	return ok && b
}

func runContainersConfigure(cmd *cobra.Command, args []string) error {
	if err := requireHumanLocalCommand("containers configure"); err != nil {
		return err
	}
	if len(containersCfgContainerNames) == 0 {
		return fmt.Errorf("at least one --container name is required (exact names; wildcards are rejected)")
	}
	cfg, err := cli.LoadCLIConfigForProfile(resolveProfile(cmd))
	if err != nil {
		return fmt.Errorf("load CLI config: %w", err)
	}
	cfg.ContainerDiagnostics = &cli.ContainerDiagnosticsConfig{
		Enabled:    true,
		Engine:     containersCfgEngine,
		EnginePath: strings.TrimSpace(containersCfgEnginePath),
		EngineArgs: containersCfgEngineArgs,
		Containers: containersCfgContainerNames,
	}
	if err := cli.SaveCLIConfigForProfile(cfg, resolveProfile(cmd)); err != nil {
		return fmt.Errorf("save CLI config: %w", err)
	}
	if containersOutput == "json" {
		return cli.PrintJSON(os.Stdout, map[string]any{"container_diagnostics": cfg.ContainerDiagnostics})
	}
	fmt.Printf("Container diagnostics enabled on this machine (engine: %s).\n", cfg.ContainerDiagnostics.Engine)
	fmt.Printf("Allowed containers: %s\n", strings.Join(cfg.ContainerDiagnostics.Containers, ", "))
	if cfg.ContainerDiagnostics.EnginePath == "" {
		fmt.Println("No engine-path pinned; the daemon will look up the engine on its own PATH. If the daemon was started before the engine was installed (or by an autostart context without it), set --engine-path to the engine's absolute path.")
	}
	fmt.Println("Restart the daemon for the change to take effect. The allowlist is per-machine and is never sent to the server.")
	return nil
}

func runContainersDisable(cmd *cobra.Command, args []string) error {
	if err := requireHumanLocalCommand("containers disable"); err != nil {
		return err
	}
	profile := resolveProfile(cmd)
	cfg, err := cli.LoadCLIConfigForProfile(profile)
	if err != nil {
		return fmt.Errorf("load CLI config: %w", err)
	}
	if cfg.ContainerDiagnostics == nil {
		fmt.Println("Container diagnostics is not configured on this machine.")
		return nil
	}
	cfg.ContainerDiagnostics.Enabled = false
	if err := cli.SaveCLIConfigForProfile(cfg, profile); err != nil {
		return fmt.Errorf("save CLI config: %w", err)
	}
	fmt.Println("Container diagnostics disabled on this machine. Restart the daemon for the change to take effect.")
	return nil
}
