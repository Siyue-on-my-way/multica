package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Task input files: workspace-library attachments referenced on a claim carry
// workspace_file_id + sha256, and the daemon materializes a verified local
// copy BEFORE the agent starts — reliability must not depend on the agent
// remembering to run `multica attachment download`. The layout mirrors the
// skill bundle cache: a dot-prefixed directory under WorkspacesRoot, keyed by
// workspace + library file, so every claim that references the same file hits
// the same verified copy.
//
//	{WorkspacesRoot}/.files/v1/{workspaceID}/{workspaceFileID}/{safeFileName}

// taskFilesCacheDirName is dot-prefixed on purpose: runGC skips every
// dot-entry under WorkspacesRoot, and the cache has its own TTL pruner.
const taskFilesCacheDirName = ".files"

// taskFilesCacheVersion lets a future layout change orphan the old tree for
// the pruner to reclaim instead of mixing formats.
const taskFilesCacheVersion = "v1"

// maxTaskFileNameBytes caps the materialized leaf name so workspace + file id
// + name stays well under Windows MAX_PATH even for long upload names.
const maxTaskFileNameBytes = 120

// DeliveredFileMeta reports one input file the daemon materialized onto local
// disk. Runtime-local only — it never crosses the wire; the prompt renders it.
type DeliveredFileMeta struct {
	AttachmentID string `json:"attachment_id,omitempty"`
	Filename     string `json:"filename"`
	Path         string `json:"path"`
	SizeBytes    int64  `json:"size_bytes,omitempty"`
}

// taskFileRef is one unique library file a claim asked for.
type taskFileRef struct {
	WorkspaceFileID string
	AttachmentID    string
	Filename        string
	Sha256          string
	SizeBytes       int64
}

// collectTaskFileRefs extracts the library-backed attachments from a claim,
// de-duplicated by workspace file id (two messages referencing one library
// file share one materialized copy).
func collectTaskFileRefs(task *Task) []taskFileRef {
	if task == nil {
		return nil
	}
	seen := make(map[string]struct{})
	refs := make([]taskFileRef, 0, len(task.ChatMessageAttachments)+len(task.IssueCommentAttachments))
	appendRef := func(a ChatAttachmentMeta) {
		if a.WorkspaceFileID == "" || a.Sha256 == "" {
			return // regular upload: no verifiable digest, CLI-download fallback applies
		}
		if _, dup := seen[a.WorkspaceFileID]; dup {
			return
		}
		seen[a.WorkspaceFileID] = struct{}{}
		refs = append(refs, taskFileRef{
			WorkspaceFileID: a.WorkspaceFileID,
			AttachmentID:    a.ID,
			Filename:        a.Filename,
			Sha256:          a.Sha256,
			SizeBytes:       a.SizeBytes,
		})
	}
	for _, a := range task.ChatMessageAttachments {
		appendRef(a)
	}
	for _, a := range task.IssueCommentAttachments {
		appendRef(a)
	}
	return refs
}

// TaskFileCache stores materialized task input files under the daemon's
// workspaces root, with a per-file mutex so concurrent prepares of the same
// library file cannot race the atomic install.
type TaskFileCache struct {
	root      string
	mu        sync.Mutex
	locks     map[string]*sync.Mutex
	removeAll func(string) error
}

func NewTaskFileCache(root string) *TaskFileCache {
	return &TaskFileCache{root: root, locks: map[string]*sync.Mutex{}, removeAll: os.RemoveAll}
}

func (c *TaskFileCache) lockKey(workspaceID, workspaceFileID string) string {
	return workspaceID + "\x00" + workspaceFileID
}

// WithFileLock runs fn while holding the per-file mutex, serializing the
// download + rename of one library file across concurrent tasks.
func (c *TaskFileCache) WithFileLock(workspaceID, workspaceFileID string, fn func() error) error {
	c.mu.Lock()
	mu, ok := c.locks[c.lockKey(workspaceID, workspaceFileID)]
	if !ok {
		mu = &sync.Mutex{}
		c.locks[c.lockKey(workspaceID, workspaceFileID)] = mu
	}
	c.mu.Unlock()
	mu.Lock()
	defer mu.Unlock()
	return fn()
}

// pathFor is the final resting place of a materialized file.
func (c *TaskFileCache) pathFor(workspaceID, workspaceFileID, filename string) (string, error) {
	if workspaceID == "" || workspaceFileID == "" {
		return "", fmt.Errorf("task file cache path requires workspace and file ids")
	}
	wsSeg := safeCacheSegment(workspaceID)
	fileSeg := safeCacheSegment(workspaceFileID)
	if wsSeg == "" || fileSeg == "" || wsSeg == "." || wsSeg == ".." || fileSeg == "." || fileSeg == ".." {
		return "", fmt.Errorf("workspace/file id produced an unsafe cache path segment")
	}
	return filepath.Join(c.root, wsSeg, fileSeg, sanitizeTaskFileName(filename)), nil
}

// Load returns the cached copy when one exists with the expected size, bumping
// its mtime so the TTL pruner reads it as recently used. Existence + size is
// the integrity check: copies only ever enter the cache through Store, which
// verifies sha256 before the atomic rename.
func (c *TaskFileCache) Load(workspaceID, workspaceFileID, filename string, sizeBytes int64) (string, bool) {
	if c == nil || c.root == "" || workspaceFileID == "" {
		return "", false
	}
	p, err := c.pathFor(workspaceID, workspaceFileID, filename)
	if err != nil {
		return "", false
	}
	info, err := os.Stat(p)
	if err != nil || info.IsDir() {
		return "", false
	}
	if sizeBytes > 0 && info.Size() != sizeBytes {
		return "", false
	}
	// LRU touch; failure is harmless — the pruner just sees an older file.
	now := time.Now()
	_ = os.Chtimes(p, now, now)
	return p, true
}

// pathForOrEmpty is pathFor for callers that only want the canonical path and
// can treat an unusable id pair as an empty result (Store already validated
// the same pair when the file was written).
func (c *TaskFileCache) pathForOrEmpty(workspaceID, workspaceFileID, filename string) string {
	p, err := c.pathFor(workspaceID, workspaceFileID, filename)
	if err != nil {
		return ""
	}
	return p
}

// Store streams src into the cache atomically: write to a temp sibling while
// hashing, verify the digest, then rename into place. A sha256 mismatch
// removes the temp file and fails the store — the caller falls back to the
// CLI download path rather than handing the agent corrupt bytes.
func (c *TaskFileCache) Store(workspaceID string, ref taskFileRef, src io.Reader) (string, error) {
	if c == nil || c.root == "" {
		return "", fmt.Errorf("task file cache disabled")
	}
	dir := filepath.Join(c.root, safeCacheSegment(workspaceID), safeCacheSegment(ref.WorkspaceFileID))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create task file cache dir: %w", err)
	}
	finalPath, err := c.pathFor(workspaceID, ref.WorkspaceFileID, ref.Filename)
	if err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(dir, ".download-*")
	if err != nil {
		return "", fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		if tmpName != "" {
			os.Remove(tmpName)
		}
	}()

	hasher := sha256.New()
	size, err := io.Copy(io.MultiWriter(tmp, hasher), src)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", fmt.Errorf("download task file: %w", err)
	}
	if hex.EncodeToString(hasher.Sum(nil)) != strings.ToLower(ref.Sha256) {
		return "", fmt.Errorf("task file sha256 mismatch: claimed %s, received %s", ref.Sha256, hex.EncodeToString(hasher.Sum(nil)))
	}
	if ref.SizeBytes > 0 && size != ref.SizeBytes {
		return "", fmt.Errorf("task file size mismatch: claimed %d, received %d", ref.SizeBytes, size)
	}
	// Windows rename-over-existing needs the target gone first; a leftover
	// target at this point can only be a stale/partial copy from a crashed
	// store, so removing it is safe.
	if err := os.Remove(finalPath); err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("clear stale task file copy: %w", err)
	}
	if err := os.Rename(tmpName, finalPath); err != nil {
		return "", fmt.Errorf("install task file: %w", err)
	}
	tmpName = "" // renamed into place; the deferred cleanup must not touch it
	return finalPath, nil
}

// sanitizeTaskFileName makes a client-supplied filename safe as the leaf of a
// materialized path on every runtime OS, while preserving readable names —
// including non-ASCII ones, which users actually rely on. It strips directory
// components, replaces Windows-forbidden characters, trims trailing
// dots/spaces, and defuses reserved device names.
func sanitizeTaskFileName(name string) string {
	// Leaf name only: never honor a directory component from the claim.
	if idx := strings.LastIndexAny(name, `/\`); idx >= 0 {
		name = name[idx+1:]
	}
	var b strings.Builder
	for _, r := range name {
		switch {
		case r < 0x20 || r == 0x7f:
			b.WriteRune('_')
		case strings.ContainsRune(`<>:"|?*`, r):
			b.WriteRune('_')
		default:
			b.WriteRune(r)
		}
	}
	name = b.String()
	name = strings.TrimRight(name, " .")
	if name == "" || name == "." || name == ".." {
		return "file"
	}
	if isWindowsReservedDeviceName(name) {
		name = "_" + name
	}
	// Cap the length while keeping the extension readable.
	if len(name) > maxTaskFileNameBytes {
		ext := filepath.Ext(name)
		base := strings.TrimSuffix(name, ext)
		keep := maxTaskFileNameBytes - len(ext)
		if keep < 1 {
			keep = maxTaskFileNameBytes
			ext = ""
		}
		for keep > 0 && (base[keep-1]&0xC0) == 0x80 {
			keep-- // back off to a UTF-8 boundary
		}
		name = base[:keep] + ext
	}
	return name
}

func isWindowsReservedDeviceName(name string) bool {
	stem := strings.ToUpper(name)
	if idx := strings.IndexByte(stem, '.'); idx >= 0 {
		stem = stem[:idx]
	}
	switch stem {
	case "CON", "PRN", "AUX", "NUL",
		"COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9",
		"LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9":
		return true
	}
	return false
}

// ensureTaskFiles materializes every library-backed attachment on the claim
// before the agent starts. Best-effort per file: a failed download is logged
// and left out of DeliveredFiles, and the prompt's fallback steers the agent
// to `multica attachment download` for anything missing — one bad file must
// not kill a run that may not even need it.
func (d *Daemon) ensureTaskFiles(ctx context.Context, task *Task) {
	refs := collectTaskFileRefs(task)
	if len(refs) == 0 {
		return
	}
	delivered := make([]DeliveredFileMeta, 0, len(refs))
	for _, ref := range refs {
		ref := ref
		var result DeliveredFileMeta
		err := d.taskFileCache.WithFileLock(task.WorkspaceID, ref.WorkspaceFileID, func() error {
			if p, ok := d.taskFileCache.Load(task.WorkspaceID, ref.WorkspaceFileID, ref.Filename, ref.SizeBytes); ok {
				result = DeliveredFileMeta{AttachmentID: ref.AttachmentID, Filename: ref.Filename, Path: p, SizeBytes: ref.SizeBytes}
				return nil
			}
			if d.client == nil {
				return fmt.Errorf("daemon client unavailable")
			}
			dlCtx, cancel := context.WithTimeout(ctx, skillBundleResolveTimeout(ref.SizeBytes))
			defer cancel()
			pr, pw := io.Pipe()
			go func() {
				_, writeErr := d.client.DownloadTaskFile(dlCtx, task.RuntimeID, task.ID, ref.AttachmentID, pw)
				pw.CloseWithError(writeErr)
			}()
			if _, err := d.taskFileCache.Store(task.WorkspaceID, ref, pr); err != nil {
				return err
			}
			result = DeliveredFileMeta{AttachmentID: ref.AttachmentID, Filename: ref.Filename, Path: d.taskFileCache.pathForOrEmpty(task.WorkspaceID, ref.WorkspaceFileID, ref.Filename), SizeBytes: ref.SizeBytes}
			return nil
		})
		if err != nil {
			if ctx.Err() != nil {
				return // prepare is being torn down; stop trying
			}
			slog.Warn("task file materialization failed; agent will fall back to CLI download",
				"task_id", task.ID,
				"workspace_file_id", ref.WorkspaceFileID,
				"attachment_id", ref.AttachmentID,
				"filename", ref.Filename,
				"error", err)
			continue
		}
		delivered = append(delivered, result)
	}
	task.DeliveredFiles = delivered
}

// pruneTaskFileCache evicts materialized files untouched for GCTaskFileTTL.
// Cache hits bump mtime (Load), so anything a recent run used survives; the
// TTL only claims files no task has needed since. Runs from the GC loop.
func (d *Daemon) pruneTaskFileCache(workspacesRoot string, ttl time.Duration, stats *gcStats) {
	if ttl <= 0 {
		return
	}
	root := filepath.Join(workspacesRoot, taskFilesCacheDirName, taskFilesCacheVersion)
	cutoff := time.Now().Add(-ttl)
	wsEntries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, wsEntry := range wsEntries {
		if !wsEntry.IsDir() {
			continue
		}
		wsDir := filepath.Join(root, wsEntry.Name())
		fileEntries, err := os.ReadDir(wsDir)
		if err != nil {
			continue
		}
		for _, fileEntry := range fileEntries {
			fileDir := filepath.Join(wsDir, fileEntry.Name())
			if !fileEntry.IsDir() {
				continue
			}
			entries, err := os.ReadDir(fileDir)
			if err != nil {
				continue
			}
			for _, entry := range entries {
				p := filepath.Join(fileDir, entry.Name())
				info, err := os.Stat(p)
				if err != nil {
					continue
				}
				if info.ModTime().After(cutoff) {
					continue
				}
				// Serialize against an in-flight download/install of the same
				// file so the pruner never deletes a copy being installed.
				_ = d.taskFileCache.WithFileLock(wsEntry.Name(), fileEntry.Name(), func() error {
					if info2, err := os.Stat(p); err == nil && !info2.ModTime().After(cutoff) {
						if err := d.taskFileCache.removeAll(p); err == nil {
							stats.bytesReclaimed += info2.Size()
							stats.taskFilesReclaimed++
						}
					}
					return nil
				})
			}
			// Drop the per-file directory once its last copy is gone.
			if remaining, err := os.ReadDir(fileDir); err == nil && len(remaining) == 0 {
				os.Remove(fileDir)
			}
		}
		if remaining, err := os.ReadDir(wsDir); err == nil && len(remaining) == 0 {
			os.Remove(wsDir)
		}
	}
}
