package handler

import (
	"errors"
	"os"
	"strings"
	"testing"
)

func TestQuarantineFallsBackToUnclaimedName(t *testing.T) {
	// A failure reported before the claim left the drop at its original name —
	// quarantine must still catch it instead of stranding it in the inbox.
	dir := t.TempDir()
	src := dir + "/broken.zip"
	if err := os.WriteFile(src, []byte("junk"), 0o644); err != nil {
		t.Fatalf("write src: %v", err)
	}
	quarantineSkillInboxFile(dir, src+skillInboxInFlightSuffix, errors.New("claim-time failure"))

	entries, err := os.ReadDir(dir + "/" + skillInboxFailedSubdir)
	if err != nil {
		t.Fatalf("read failed dir: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("expected the unclaimed drop to be quarantined")
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatal("unclaimed drop should have been moved into failed/")
	}
}

func TestSkillInboxSlug(t *testing.T) {
	cases := []struct{ in, want string }{
		{"My Skill", "my-skill"},
		{"code-review", "code-review"},
		{"  Report Gen  ", "report-gen"},
		{"数据skill", "skill"}, // non-ASCII collapses, then trims
		// Traversal never survives the slug: every separator/dot becomes '-'.
		{"../../etc/passwd", "etc-passwd"},
		{"!!!", "unnamed-skill"},
		{"", "unnamed-skill"},
	}
	for _, tc := range cases {
		if got := skillInboxSlug(tc.in); got != tc.want {
			t.Errorf("skillInboxSlug(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestIsTruthyFormValue(t *testing.T) {
	for _, v := range []string{"true", "TRUE", "1", " yes ", "on"} {
		if !isTruthyFormValue(v) {
			t.Errorf("isTruthyFormValue(%q) = false, want true", v)
		}
	}
	for _, v := range []string{"", "false", "0", "no", "enabled", "truefalse"} {
		if isTruthyFormValue(v) {
			t.Errorf("isTruthyFormValue(%q) = true, want false", v)
		}
	}
}

func TestQuarantineSkipsRetryLater(t *testing.T) {
	// errSkillInboxRetryLater means the file was restored to its original
	// name for a later scan — it must not be quarantined. The in-flight
	// source does not exist here, so a wrong move would error loudly rather
	// than silently no-op.
	quarantineSkillInboxFile(t.TempDir(), t.TempDir()+"/x.zip.importing", errSkillInboxRetryLater)
}

func TestQuarantineMovesArchiveAndWritesNote(t *testing.T) {
	dir := t.TempDir()
	src := dir + "/broken.zip.importing"
	if err := os.WriteFile(src, []byte("junk"), 0o644); err != nil {
		t.Fatalf("write src: %v", err)
	}
	cause := errors.New("parse archive: not a zip")
	quarantineSkillInboxFile(dir, src, cause)

	failed := dir + "/" + skillInboxFailedSubdir
	entries, err := os.ReadDir(failed)
	if err != nil {
		t.Fatalf("read failed dir: %v", err)
	}
	var zipName, noteName string
	for _, e := range entries {
		switch {
		case strings.HasSuffix(e.Name(), ".zip"):
			zipName = e.Name()
		case strings.HasSuffix(e.Name(), ".error"):
			noteName = e.Name()
		}
	}
	if zipName == "" || noteName == "" {
		t.Fatalf("expected quarantined zip + .error note, got %d entries", len(entries))
	}
	note, err := os.ReadFile(failed + "/" + noteName)
	if err != nil {
		t.Fatalf("read note: %v", err)
	}
	if !strings.Contains(string(note), "error: "+cause.Error()) {
		t.Fatalf("note %q missing reason %q", note, cause)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatal("in-flight archive should have been moved into failed/")
	}
}
