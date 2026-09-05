package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/multica-ai/multica/server/internal/daemon/sandbox"
	"github.com/spf13/cobra"
)

var skillRunArgs []string

var skillRunCmd = &cobra.Command{
	Use:   "run <skill-name> --skill-dir <directory> --tool <name> [--arg key=value ...]",
	Short: "Execute a tool from a skill bundle in a sandbox",
	Long: `Execute a tool from a skill bundle's manifest.yaml inside a Docker sandbox.

The skill directory must contain a manifest.yaml file that defines the tools
exposed by the skill. The --arg flag is repeatable and passes key=value pairs
to the tool entrypoint.

Environment: reads MULTICA_LLM_API_KEY and MULTICA_LLM_BASE_URL to bridge LLM
credentials into the sandbox for skills that need AI capabilities.`,
	Args: cobra.ExactArgs(1),
	RunE: runSkillRun,
}

var (
	skillRunDir  string
	skillRunTool string
)

func init() {
	skillRunCmd.Flags().StringVar(&skillRunDir, "skill-dir", "", "Path to the skill bundle directory (containing manifest.yaml)")
	skillRunCmd.Flags().StringVar(&skillRunTool, "tool", "", "Name of the tool to execute (from manifest.yaml)")
	skillRunCmd.Flags().StringArrayVar(&skillRunArgs, "arg", nil, "Tool arguments as key=value pairs (repeatable)")
	_ = skillRunCmd.MarkFlagRequired("skill-dir")
	_ = skillRunCmd.MarkFlagRequired("tool")
	skillCmd.AddCommand(skillRunCmd)
}

func runSkillRun(cmd *cobra.Command, args []string) error {
	skillName := args[0]
	dir := skillRunDir

	manifestPath := filepath.Join(dir, "manifest.yaml")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return fmt.Errorf("read manifest.yaml from %s: %w", dir, err)
	}

	m, err := sandbox.ParseManifest(data)
	if err != nil {
		return fmt.Errorf("parse manifest.yaml: %w", err)
	}

	if m.Name == "" {
		m.Name = skillName
	}

	// Validate the requested tool exists.
	toolFound := false
	for _, t := range m.Tools {
		if t.Name == skillRunTool {
			toolFound = true
			break
		}
	}
	// If the manifest has tools but this one isn't listed, warn but proceed.
	if len(m.Tools) > 0 && !toolFound {
		fmt.Fprintf(os.Stderr, "warning: tool %q is not listed in manifest.yaml tools; executing as ad-hoc tool\n", skillRunTool)
	}

	if m.Image == "" {
		m.Image = "python:3.10-slim"
	}
	if m.Entrypoint == "" {
		m.Entrypoint = "scripts/main.py"
	}

	// Parse --arg key=value pairs.
	toolArgs := make(map[string]string)
	for _, a := range skillRunArgs {
		parts := strings.SplitN(a, "=", 2)
		if len(parts) != 2 {
			return fmt.Errorf("invalid --arg %q: must be key=value", a)
		}
		toolArgs[parts[0]] = parts[1]
	}

	containerWorkdir := "/workspace"
	sandbox := sandbox.NewDockerSandbox(m.Image, containerWorkdir)

	// Mount the skill directory (scripts, data, etc.) as read-only.
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return fmt.Errorf("resolve skill directory: %w", err)
	}
	sandbox.AddMount(absDir, containerWorkdir+"/skill:ro")

	// Mount the current working directory for input/output files.
	wd, err := os.Getwd()
	if err != nil {
		wd = "/tmp"
	}
	sandbox.AddMount(wd, containerWorkdir+"/work")

	// Bridge LLM credentials so skill scripts can call Multica's LLM.
	if key := os.Getenv("MULTICA_LLM_API_KEY"); key != "" {
		sandbox.AddEnv("MULTICA_LLM_API_KEY", key)
	}
	if url := os.Getenv("MULTICA_LLM_BASE_URL"); url != "" {
		sandbox.AddEnv("MULTICA_LLM_BASE_URL", url)
	}
	// Expose the server URL so scripts can call Multica APIs.
	if serverURL := os.Getenv("MULTICA_SERVER_URL"); serverURL != "" {
		sandbox.AddEnv("MULTICA_SERVER_URL", serverURL)
	}
	if wsID := os.Getenv("MULTICA_WORKSPACE_ID"); wsID != "" {
		sandbox.AddEnv("MULTICA_WORKSPACE_ID", wsID)
	}

	// Bridge secrets declared by the manifest.
	for _, secret := range m.RequiredSecrets {
		envKey := "SKILL_SECRET_" + strings.ToUpper(strings.ReplaceAll(secret, "-", "_"))
		if val := os.Getenv(envKey); val != "" {
			sandbox.AddEnv(secret, val)
		}
	}

	// Build the command inside the container.
	// ExecArgs returns [entrypoint, --tool, <name>, --<param>, <val>, ...]
	execArgs := m.ExecArgs(skillRunTool, toolArgs)
	// The entrypoint is relative to the skill dir; run from the skill mount point.
	sandbox.WorkDir = containerWorkdir + "/skill"

	fmt.Fprintf(os.Stderr, "executing tool %q in sandbox (image=%s, workdir=%s)...\n", skillRunTool, m.Image, sandbox.WorkDir)
	stdout, stderr, err := sandbox.Run(cmd.Context(), execArgs)
	if stderr != "" {
		fmt.Fprint(os.Stderr, stderr)
	}
	if err != nil {
		return fmt.Errorf("sandbox execution failed: %w\nstdout: %s", err, stdout)
	}
	fmt.Print(stdout)
	return nil
}