package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestExtractInboxURLs(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    []string
	}{
		{
			name:    "bare link",
			content: "https://github.com/anthropics/skills/tree/main/document-skills/pdf\n",
			want:    []string{"https://github.com/anthropics/skills/tree/main/document-skills/pdf"},
		},
		{
			name:    "windows url shortcut format",
			content: "[InternetShortcut]\nurl=https://clawhub.ai/owner/my-skill\n",
			want:    []string{"https://clawhub.ai/owner/my-skill"},
		},
		{
			name:    "markdown link punctuation excluded",
			content: "install it: [skill](https://skills.sh/owner/repo/pdf) today\n",
			want:    []string{"https://skills.sh/owner/repo/pdf"},
		},
		{
			name:    "sentence period trimmed",
			content: "see https://github.com/owner/repo.\n",
			want:    []string{"https://github.com/owner/repo"},
		},
		{
			name:    "scheme-less supported host",
			content: "github.com/owner/repo/tree/main/my-skill\nwww.clawhub.ai/owner/other\n",
			want:    []string{"https://github.com/owner/repo/tree/main/my-skill", "https://www.clawhub.ai/owner/other"},
		},
		{
			name:    "scheme-less unsupported host ignored",
			content: "example.com/skill.zip\nnot a link\n",
			want:    nil,
		},
		{
			name:    "duplicates dropped order kept",
			content: "https://github.com/owner/repo\nhttps://clawhub.ai/o/s\nhttps://github.com/owner/repo\n",
			want:    []string{"https://github.com/owner/repo", "https://clawhub.ai/o/s"},
		},
		{
			name:    "multiple links on one line",
			content: "first https://github.com/o/r then https://codeload.github.com/o/r/zip/refs/heads/main\n",
			want:    []string{"https://github.com/o/r", "https://codeload.github.com/o/r/zip/refs/heads/main"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractInboxURLs(tc.content)
			if len(got) != len(tc.want) {
				t.Fatalf("extractInboxURLs(%q) = %v, want %v", tc.content, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("extractInboxURLs(%q)[%d] = %q, want %q", tc.content, i, got[i], tc.want[i])
				}
			}
		})
	}
}

// stubInboxImport swaps the URL-import step for a recording stub.
func stubInboxImport(t *testing.T, calls *[]string, failFor map[string]error) {
	t.Helper()
	restore := importInboxSkillFromURL
	importInboxSkillFromURL = func(_ context.Context, _ *Handler, _ SkillInboxConfig, raw string) error {
		*calls = append(*calls, raw)
		if err := failFor[raw]; err != nil {
			return err
		}
		return nil
	}
	t.Cleanup(func() { importInboxSkillFromURL = restore })
}

// writeSettledDrop writes an inbox drop whose mtime is past the quiet period,
// so the scanner processes it immediately instead of deferring.
func writeSettledDrop(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write drop: %v", err)
	}
	past := time.Now().Add(-time.Minute)
	if err := os.Chtimes(path, past, past); err != nil {
		t.Fatalf("backdate drop: %v", err)
	}
	return path
}

func TestProcessSkillInboxURLFileImportsAndDeletes(t *testing.T) {
	dir := t.TempDir()
	var calls []string
	stubInboxImport(t, &calls, nil)

	path := writeSettledDrop(t, dir, "links.txt",
		"https://github.com/owner/repo\n# dup below\nhttps://github.com/owner/repo\nhttps://clawhub.ai/o/s\n")

	if err := processSkillInboxURLFile(context.Background(), &Handler{}, SkillInboxConfig{Dir: dir}, path); err != nil {
		t.Fatalf("processSkillInboxURLFile: %v", err)
	}
	if len(calls) != 2 || calls[0] != "https://github.com/owner/repo" || calls[1] != "https://clawhub.ai/o/s" {
		t.Fatalf("imports = %v, want deduped URLs in order", calls)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("URL file should be deleted after a fully successful import")
	}
}

func TestProcessSkillInboxURLFilePartialFailureKeepsFile(t *testing.T) {
	dir := t.TempDir()
	var calls []string
	stubInboxImport(t, &calls, map[string]error{
		"https://clawhub.ai/o/broken": errors.New("clawhub returned status 404"),
	})

	path := writeSettledDrop(t, dir, "links.txt",
		"https://github.com/owner/repo\nhttps://clawhub.ai/o/broken\n")

	err := processSkillInboxURLFile(context.Background(), &Handler{}, SkillInboxConfig{Dir: dir}, path)
	if err == nil {
		t.Fatal("expected partial-failure error")
	}
	if !strings.Contains(err.Error(), "https://clawhub.ai/o/broken") ||
		!strings.Contains(err.Error(), "https://github.com/owner/repo") {
		t.Fatalf("error %v should name both the failed and the succeeded URL", err)
	}
	if _, statErr := os.Stat(path); statErr != nil {
		t.Fatal("URL file must be kept for quarantine when any import fails")
	}
}

func TestProcessSkillInboxURLFileNoURLs(t *testing.T) {
	dir := t.TempDir()
	var calls []string
	stubInboxImport(t, &calls, nil)

	path := writeSettledDrop(t, dir, "notes.txt", "just some notes, no links\n")
	err := processSkillInboxURLFile(context.Background(), &Handler{}, SkillInboxConfig{Dir: dir}, path)
	if err == nil || !strings.Contains(err.Error(), "no skill URL found") {
		t.Fatalf("err = %v, want no-skill-URL rejection", err)
	}
	if len(calls) != 0 {
		t.Fatalf("no import should run, got %v", calls)
	}
}

func TestProcessSkillInboxURLFileDefersUnsettled(t *testing.T) {
	dir := t.TempDir()
	var calls []string
	stubInboxImport(t, &calls, nil)

	path := filepath.Join(dir, "links.txt")
	if err := os.WriteFile(path, []byte("https://github.com/owner/repo\n"), 0o644); err != nil {
		t.Fatalf("write drop: %v", err)
	}
	err := processSkillInboxURLFile(context.Background(), &Handler{}, SkillInboxConfig{Dir: dir}, path)
	if !errors.Is(err, errSkillInboxRetryLater) {
		t.Fatalf("err = %v, want errSkillInboxRetryLater", err)
	}
	if _, statErr := os.Stat(path); statErr != nil {
		t.Fatal("unsettled file must be restored to its original name")
	}
	if len(calls) != 0 {
		t.Fatalf("no import should run for an unsettled file, got %v", calls)
	}
}

func TestReclaimRestoresOriginalName(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"links.txt.importing", "skill.zip.importing"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	reclaimInFlightArchives(dir)
	for _, want := range []string{"links.txt", "skill.zip"} {
		if _, err := os.Stat(filepath.Join(dir, want)); err != nil {
			t.Fatalf("expected %s to be restored: %v", want, err)
		}
	}
}

func TestFetchInboxSkillFromURLArchiveDispatch(t *testing.T) {
	restore := archiveHostAllowed
	archiveHostAllowed = func(ctx context.Context, host string) (bool, error) { return true, nil }
	t.Cleanup(func() { archiveHostAllowed = restore })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(buildTestZip(t, map[string]string{
			"SKILL.md": "---\nname: inbox-url-skill\ndescription: from an inbox link\n---\n\n# inbox-url-skill\n",
		}))
	}))
	defer srv.Close()

	imported, err := fetchInboxSkillFromURL(context.Background(), srv.URL+"/bundles/inbox-url-skill.zip")
	if err != nil {
		t.Fatalf("fetchInboxSkillFromURL: %v", err)
	}
	if imported.name != "inbox-url-skill" {
		t.Fatalf("name = %q, want inbox-url-skill", imported.name)
	}
}
