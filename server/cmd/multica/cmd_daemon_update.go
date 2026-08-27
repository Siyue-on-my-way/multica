package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/multica-ai/multica/server/internal/daemon"
	"github.com/multica-ai/multica/server/internal/selfexec"
)

var daemonUpdateCmd = &cobra.Command{
	Use:   "update",
	Short: "Download the latest daemon binary from the Multica server and replace this executable",
	Long: "Downloads the multica binary for this platform from the connected Multica server and\n" +
		"atomically replaces the current executable. Use --restart to also restart the daemon\n" +
		"after a successful update.\n\n" +
		"The server must be configured with the MULTICA_DAEMON_BINARY_PATH or\n" +
		"MULTICA_DAEMON_BINARY_DIR environment variable pointing to the binaries to serve.\n" +
		"Or place per-platform binaries named multica-<os>-<arch>[.exe] alongside the server binary.",
	RunE: runDaemonUpdate,
}

func init() {
	uf := daemonUpdateCmd.Flags()
	uf.Bool("restart", false, "Restart the daemon after a successful update")
	daemonCmd.AddCommand(daemonUpdateCmd)
}

func runDaemonUpdate(cmd *cobra.Command, _ []string) error {
	profile := resolveProfile(cmd)
	if err := requireDaemonAuth(profile); err != nil {
		return err
	}

	cfg, err := cli.LoadCLIConfigForProfile(profile)
	if err != nil {
		return fmt.Errorf("load CLI config: %w", err)
	}

	rawURL := resolveDaemonServerURL(cmd, profile)
	if rawURL == "" {
		rawURL = daemon.DefaultServerURL
	}
	baseURL, err := daemon.NormalizeServerBaseURL(rawURL)
	if err != nil {
		return fmt.Errorf("invalid server URL %q: %w", rawURL, err)
	}

	exePath, err := selfexec.Resolve()
	if err != nil {
		return fmt.Errorf("resolve executable path: %w", err)
	}
	exePath, err = filepath.EvalSymlinks(exePath)
	if err != nil {
		return fmt.Errorf("resolve symlink: %w", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Downloading update from %s ...\n", baseURL)

	downloadURL := baseURL + "/api/daemon/binary?os=" + runtime.GOOS + "&arch=" + runtime.GOARCH

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	// Use the stored auth token (same as daemon heartbeats use)
	if cfg.Token != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.Token)
	}

	resp, err := (&http.Client{Timeout: 5 * time.Minute}).Do(req)
	if err != nil {
		return fmt.Errorf("download failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("the server does not have a binary for %s/%s (configure MULTICA_DAEMON_BINARY_PATH or MULTICA_DAEMON_BINARY_DIR on the server)", runtime.GOOS, runtime.GOARCH)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("server returned HTTP %d: %s", resp.StatusCode, string(body))
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	if len(data) == 0 {
		return fmt.Errorf("server returned empty binary")
	}

	// Atomic replace: write to a temp file in the same directory, then rename
	dir := filepath.Dir(exePath)
	tmp, err := os.CreateTemp(dir, "multica-update-*")
	if err != nil {
		return fmt.Errorf("create temp file (do you have write permission to %s?): %w", dir, err)
	}
	tmpPath := tmp.Name()

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("sync temp file: %w", err)
	}
	tmp.Close()

	// Preserve original executable permission bits
	info, err := os.Stat(exePath)
	if err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("stat original binary: %w", err)
	}
	if err := os.Chmod(tmpPath, info.Mode()|0o111); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("chmod temp file: %w", err)
	}

	if err := cli.ReplaceBinaryForUpdate(tmpPath, exePath); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("replace binary: %w", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Updated %s (%d bytes)\n", exePath, len(data))

	doRestart, _ := cmd.Flags().GetBool("restart")
	if doRestart {
		fmt.Fprintln(cmd.OutOrStdout(), "Restarting daemon...")
		return runDaemonRestart(cmd, nil)
	}

	fmt.Fprintln(cmd.OutOrStdout(), "Done. Run 'multica daemon restart --profile local' to activate the new binary.")
	return nil
}
