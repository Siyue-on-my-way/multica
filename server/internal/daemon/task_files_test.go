package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSanitizeTaskFileName(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want string
	}{
		// Ordinary Unicode names survive untouched — users rely on them.
		{"需求文档.docx", "需求文档.docx"},
		{"spec v2.pdf", "spec v2.pdf"},
		// Directory components are stripped, not honored.
		{"../../etc/passwd", "passwd"},
		{`C:\Users\eve\secret.txt`, "secret.txt"},
		// Windows-forbidden characters become underscores.
		{"a<b>c:d\"e|f?g*h.md", "a_b_c_d_e_f_g_h.md"},
		{"control\x01char.txt", "control_char.txt"},
		// Trailing dots/spaces are trimmed (Windows).
		{"name. ", "name"},
		{"...", "file"},
		{"", "file"},
		// Reserved device names are defused.
		{"CON.md", "_CON.md"},
		{"com1", "_com1"},
	}
	for _, tc := range cases {
		if got := sanitizeTaskFileName(tc.in); got != tc.want {
			t.Errorf("sanitizeTaskFileName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	// Long names keep their extension and stay within the cap.
	long := strings.Repeat("文", 200) + ".docx"
	got := sanitizeTaskFileName(long)
	if len(got) > maxTaskFileNameBytes+20 { // extension may push past the byte cap on the base side only
		t.Errorf("long name not truncated: %d bytes", len(got))
	}
	if !strings.HasSuffix(got, ".docx") {
		t.Errorf("long name lost its extension: %q", got)
	}
}

func newTestFileCache(t *testing.T) *TaskFileCache {
	t.Helper()
	return NewTaskFileCache(filepath.Join(t.TempDir(), ".files", "v1"))
}

func TestTaskFileCacheStoreLoadRoundtrip(t *testing.T) {
	t.Parallel()
	c := newTestFileCache(t)
	ref := taskFileRef{WorkspaceFileID: "wf-1", AttachmentID: "att-1", Filename: "需求文档.docx", Sha256: "", SizeBytes: 5}
	body := "hello"
	sum := sha256.Sum256([]byte(body))
	ref.Sha256 = hex.EncodeToString(sum[:])

	p, err := c.Store("ws-1", ref, strings.NewReader(body))
	if err != nil {
		t.Fatalf("Store: %v", err)
	}
	data, err := os.ReadFile(p)
	if err != nil || string(data) != body {
		t.Fatalf("stored bytes differ: %q err=%v", data, err)
	}
	got, ok := c.Load("ws-1", "wf-1", ref.Filename, 5)
	if !ok || got != p {
		t.Fatalf("Load miss after Store: %q ok=%v", got, ok)
	}

	// Size mismatch → miss.
	if _, ok := c.Load("ws-1", "wf-1", ref.Filename, 6); ok {
		t.Fatalf("Load hit despite size mismatch")
	}
	// Different workspace → isolated namespace.
	if _, ok := c.Load("ws-2", "wf-1", ref.Filename, 5); ok {
		t.Fatalf("Load hit across workspaces")
	}
}

func TestTaskFileCacheRejectsSha256Mismatch(t *testing.T) {
	t.Parallel()
	c := newTestFileCache(t)
	ref := taskFileRef{WorkspaceFileID: "wf-2", AttachmentID: "att-2", Filename: "spec.pdf", Sha256: strings.Repeat("0", 64), SizeBytes: 5}
	p, err := c.Store("ws-1", ref, strings.NewReader("hello"))
	if err == nil {
		t.Fatalf("Store accepted corrupt bytes at %s", p)
	}
	if _, ok := c.Load("ws-1", "wf-2", ref.Filename, 5); ok {
		t.Fatalf("corrupt download left a loadable cache entry")
	}
	// No temp litter left behind.
	entries, _ := os.ReadDir(filepath.Dir(filepath.Join(c.root, "ws-1", "wf-2", "x")))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".download-") {
			t.Errorf("temp file leaked: %s", e.Name())
		}
	}
}

func TestTaskFileCacheConcurrentStores(t *testing.T) {
	t.Parallel()
	c := newTestFileCache(t)
	body := "shared"
	sum := sha256.Sum256([]byte(body))
	ref := taskFileRef{WorkspaceFileID: "wf-3", AttachmentID: "att-3", Filename: "a.txt", Sha256: hex.EncodeToString(sum[:]), SizeBytes: 6}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = c.WithFileLock("ws-1", "wf-3", func() error {
				if _, ok := c.Load("ws-1", "wf-3", ref.Filename, 6); ok {
					return nil
				}
				_, err := c.Store("ws-1", ref, strings.NewReader(body))
				return err
			})
		}()
	}
	wg.Wait()
	if _, ok := c.Load("ws-1", "wf-3", ref.Filename, 6); !ok {
		t.Fatalf("concurrent stores did not converge on a cached copy")
	}
}

func TestCollectTaskFileRefs(t *testing.T) {
	t.Parallel()
	task := &Task{
		WorkspaceID: "ws-1",
		ChatMessageAttachments: []ChatAttachmentMeta{
			{ID: "a1", Filename: "regular.png"},                                       // no library backing → skipped
			{ID: "a2", Filename: "doc.docx", WorkspaceFileID: "wf-a", Sha256: "aa"},   // kept
			{ID: "a3", Filename: "doc.docx", WorkspaceFileID: "wf-a", Sha256: "aa"},   // duplicate file → deduped
			{ID: "a4", Filename: "lib.png", WorkspaceFileID: "wf-b"},                  // no sha256 → skipped (unverifiable)
		},
		IssueCommentAttachments: []ChatAttachmentMeta{
			{ID: "a5", Filename: "req.pdf", WorkspaceFileID: "wf-c", Sha256: "cc"},
		},
	}
	refs := collectTaskFileRefs(task)
	if len(refs) != 2 {
		t.Fatalf("expected 2 unique verifiable refs, got %d: %+v", len(refs), refs)
	}
	if refs[0].WorkspaceFileID != "wf-a" || refs[0].AttachmentID != "a2" {
		t.Errorf("first ref wrong: %+v", refs[0])
	}
	if refs[1].WorkspaceFileID != "wf-c" || refs[1].AttachmentID != "a5" {
		t.Errorf("second ref wrong: %+v", refs[1])
	}
}

func TestEnsureTaskFilesDeliversAndFallsBack(t *testing.T) {
	t.Parallel()
	d := &Daemon{taskFileCache: newTestFileCache(t)}
	// No server client configured: the library-backed ref cannot download and
	// must be left out of DeliveredFiles rather than failing prepare.
	body := "payload"
	sum := sha256.Sum256([]byte(body))
	d.taskFileCache = NewTaskFileCache(filepath.Join(t.TempDir(), ".files", "v1"))
	// Pre-seed the cache so no download is needed for the first ref.
	ref := taskFileRef{WorkspaceFileID: "wf-ok", AttachmentID: "att-ok", Filename: "ok.txt", Sha256: hex.EncodeToString(sum[:]), SizeBytes: int64(len(body))}
	if _, err := d.taskFileCache.Store("ws-1", ref, strings.NewReader(body)); err != nil {
		t.Fatalf("seed store: %v", err)
	}

	task := &Task{
		WorkspaceID: "ws-1",
		ChatMessageAttachments: []ChatAttachmentMeta{
			{ID: "att-ok", Filename: "ok.txt", WorkspaceFileID: "wf-ok", Sha256: ref.Sha256, SizeBytes: ref.SizeBytes},
			{ID: "att-missing", Filename: "missing.bin", WorkspaceFileID: "wf-missing", Sha256: "22", SizeBytes: 1},
		},
	}
	d.ensureTaskFiles(context.Background(), task)
	if len(task.DeliveredFiles) != 1 || task.DeliveredFiles[0].AttachmentID != "att-ok" {
		t.Fatalf("expected only the cached file delivered, got %+v", task.DeliveredFiles)
	}
	if _, err := os.Stat(task.DeliveredFiles[0].Path); err != nil {
		t.Fatalf("delivered path missing on disk: %v", err)
	}
}

func TestPruneTaskFileCacheTTL(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	c := NewTaskFileCache(filepath.Join(root, ".files", "v1"))
	d := &Daemon{taskFileCache: c}
	body := "stale"
	sum := sha256.Sum256([]byte(body))
	ref := taskFileRef{WorkspaceFileID: "wf-old", AttachmentID: "att-old", Filename: "old.txt", Sha256: hex.EncodeToString(sum[:]), SizeBytes: 5}
	p, err := c.Store("ws-1", ref, strings.NewReader(body))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	past := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(p, past, past); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	stats := &gcStats{byPattern: map[string]int{}}
	d.pruneTaskFileCache(root, time.Hour, stats)
	if stats.taskFilesReclaimed != 1 {
		t.Fatalf("expected 1 reclaimed file, got %d", stats.taskFilesReclaimed)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatalf("stale file survived prune: %v", err)
	}

	// A recently-touched file survives.
	freshRef := taskFileRef{WorkspaceFileID: "wf-new", AttachmentID: "att-new", Filename: "new.txt", Sha256: hex.EncodeToString(sum[:]), SizeBytes: 5}
	p2, err := c.Store("ws-1", freshRef, strings.NewReader(body))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	stats2 := &gcStats{byPattern: map[string]int{}}
	d.pruneTaskFileCache(root, time.Hour, stats2)
	if stats2.taskFilesReclaimed != 0 {
		t.Fatalf("fresh file was reclaimed")
	}
	if _, err := os.Stat(p2); err != nil {
		t.Fatalf("fresh file missing: %v", err)
	}
}

func TestDownloadTaskFileClientPath(t *testing.T) {
	// Purely a compile-level contract check of the streaming client call used
	// by ensureTaskFiles; the HTTP behavior is covered by e2e tests.
	var _ func(context.Context, string, string, string, io.Writer) (TransferStats, error) = (&Client{}).DownloadTaskFile
}
