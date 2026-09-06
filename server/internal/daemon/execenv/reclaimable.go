package execenv

import "path/filepath"

const (
	codexHomeDirName       = "codex-home"
	codexSandboxBinDirName = ".sandbox-bin"
	// codexTmpDirName is the Codex CLI's per-session plugin staging directory:
	// a full working copy of the plugin cache (~98 MiB as of SIY-123) plus its
	// sync bookkeeping (plugins.sha, plugins.sync.lock). Codex re-creates it
	// from codex-home/plugins/cache on the next run, so like .sandbox-bin it
	// is regenerable and never needs to outlive the session that staged it.
	codexTmpDirName = ".tmp"
)

// ManagedReclaimableArtifactSubpaths returns daemon-owned, regenerable
// directories inside a task env root. Callers must match these as exact
// relative paths rather than basenames: a repository may legitimately contain
// a directory with the same leaf name.
func ManagedReclaimableArtifactSubpaths() []string {
	return []string{
		filepath.Join(codexHomeDirName, codexSandboxBinDirName),
		filepath.Join(codexHomeDirName, codexTmpDirName),
	}
}
