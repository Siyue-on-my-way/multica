//go:build windows

package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/cli"
)

// Windows-specific regressions for the container diagnostics capability.
// The capability's execution path is OS-neutral (direct argv, no shell), so
// what Windows actually exercises is path handling: pinned engine paths with
// drive letters and backslashes, and the rejection of relative paths that
// would silently resolve against whatever PATH the daemon's launch context
// happened to carry.

func TestContainerDiagnosticsWindowsPinnedEnginePath(t *testing.T) {
	dir := t.TempDir()
	engine := filepath.Join(dir, "podman.exe")
	if err := os.WriteFile(engine, []byte("stub"), 0o755); err != nil {
		t.Fatalf("write stub engine: %v", err)
	}
	cfg := &cli.ContainerDiagnosticsConfig{
		Enabled:    true,
		Engine:     "podman",
		EnginePath: engine,
		Containers: []string{"ai-cms-api"},
	}
	capability, err := newContainerDiagnostics(cfg, nil, nil)
	if err != nil {
		t.Fatalf("newContainerDiagnostics with absolute pinned path: %v", err)
	}
	if capability.enginePath != engine {
		t.Fatalf("engine path not pinned as given: %q", capability.enginePath)
	}

	// A relative engine_path must fail loudly, not silently fall back to a
	// PATH lookup against the daemon's launch-context environment — that
	// fallback is exactly the failure mode this capability exists to remove.
	rel := &cli.ContainerDiagnosticsConfig{
		Enabled:    true,
		Engine:     "podman",
		EnginePath: "podman.exe",
		Containers: []string{"ai-cms-api"},
	}
	if _, err := newContainerDiagnostics(rel, nil, nil); err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Fatalf("relative engine_path must be rejected, got %v", err)
	}
}

func TestContainerDiagnosticsWindowsContainerNames(t *testing.T) {
	// Windows container names may carry the same charset as Linux ones;
	// paths and UNC fragments are not container names and must never pass.
	for _, name := range []string{`C:\evil`, `\\server\share`, "ai-cms-api/../../secret", "con\tainer"} {
		cfg := &cli.ContainerDiagnosticsConfig{
			Enabled:    true,
			Engine:     "podman",
			Containers: []string{name},
		}
		if _, err := newContainerDiagnostics(cfg, nil, nil); err == nil {
			t.Fatalf("container name %q must be rejected", name)
		}
	}
}
