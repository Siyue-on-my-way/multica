package handler

import (
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// HandleGetDaemonBinary streams the multica CLI binary for the requesting
// platform. The server-side binary path is resolved in order:
//
//  1. MULTICA_DAEMON_BINARY_PATH env var (explicit absolute path to a single
//     binary, useful for single-platform deployments)
//  2. MULTICA_DAEMON_BINARY_DIR env var (directory containing per-platform
//     binaries named multica-<os>-<arch>[.exe])
//  3. The directory containing the running server binary (same as above)
//
// Authentication: this route sits inside the /api/daemon group, so DaemonAuth
// middleware is already enforced — a valid daemon token or PAT is required.
func HandleGetDaemonBinary(w http.ResponseWriter, r *http.Request) {
	goos := r.URL.Query().Get("os")
	goarch := r.URL.Query().Get("arch")

	// Sanitize: only accept known values to prevent path traversal
	if !isKnownOS(goos) || !isKnownArch(goarch) {
		// Fall back to current server platform when caller omits or sends bad values
		goos = runtime.GOOS
		goarch = runtime.GOARCH
	}

	path := resolveDaemonBinaryPath(goos, goarch)
	if path == "" {
		http.Error(w, "daemon binary not available on this server", http.StatusNotFound)
		return
	}

	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			http.Error(w, "daemon binary not found", http.StatusNotFound)
			return
		}
		http.Error(w, "failed to open daemon binary", http.StatusInternalServerError)
		return
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil {
		http.Error(w, "failed to stat daemon binary", http.StatusInternalServerError)
		return
	}

	filename := "multica"
	if goos == "windows" {
		filename = "multica.exe"
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	http.ServeContent(w, r, filename, stat.ModTime(), f)
}

func resolveDaemonBinaryPath(goos, goarch string) string {
	// 1. Explicit single-binary path (useful for single-platform)
	if p := os.Getenv("MULTICA_DAEMON_BINARY_PATH"); p != "" {
		return p
	}

	// 2. Directory with per-platform binaries
	dir := os.Getenv("MULTICA_DAEMON_BINARY_DIR")

	// 3. Fall back to the directory of the running server binary
	if dir == "" {
		if exe, err := os.Executable(); err == nil {
			dir = filepath.Dir(exe)
		}
	}

	if dir == "" {
		return ""
	}

	name := "multica-" + goos + "-" + goarch
	if goos == "windows" {
		name += ".exe"
	}
	return filepath.Join(dir, name)
}

var knownOSes = map[string]bool{
	"linux":   true,
	"darwin":  true,
	"windows": true,
}

var knownArches = map[string]bool{
	"amd64": true,
	"arm64": true,
	"arm":   true,
}

func isKnownOS(s string) bool   { return knownOSes[strings.ToLower(s)] }
func isKnownArch(s string) bool { return knownArches[strings.ToLower(s)] }
