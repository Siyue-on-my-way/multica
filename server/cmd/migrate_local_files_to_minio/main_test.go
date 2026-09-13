package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLegacyKeyFromURL(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "site relative", raw: "/uploads/workspaces/ws/file.png", want: "workspaces/ws/file.png"},
		{name: "absolute", raw: "http://localhost:8080/uploads/workspaces/ws/file.png", want: "workspaces/ws/file.png"},
		{name: "escaped path", raw: "/uploads/workspaces/ws/hello%20world.txt", want: "workspaces/ws/hello world.txt"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := legacyKeyFromURL(tt.raw)
			if err != nil {
				t.Fatalf("legacyKeyFromURL() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("legacyKeyFromURL() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestLegacyKeyFromURLRejectsUnsafeAndForeignURLs(t *testing.T) {
	for _, raw := range []string{
		"https://example.com/avatar.png",
		"/uploads/../secrets.txt",
		"/uploads/%2e%2e/secrets.txt",
	} {
		t.Run(raw, func(t *testing.T) {
			if _, err := legacyKeyFromURL(raw); err == nil {
				t.Fatalf("legacyKeyFromURL(%q) accepted an unsafe or foreign URL", raw)
			}
		})
	}
}

func TestScanLocalObjectsSkipsBackendSidecars(t *testing.T) {
	root := t.TempDir()
	for name, body := range map[string]string{
		"workspaces/ws/file.png":                "image",
		"workspaces/ws/file.png.meta.json":      "metadata",
		"workspaces/ws/.file.png.tmp":           "partial",
		"skills/installed/SKILL.md":             "skill",
		"users/user-1/avatar.png":               "avatar",
		"workspaces/ws/nested/another-file.txt": "text",
	} {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	objects, err := scanLocalObjects(root)
	if err != nil {
		t.Fatalf("scanLocalObjects() error = %v", err)
	}
	if _, ok := objects["workspaces/ws/file.png"]; !ok {
		t.Fatal("regular object was not scanned")
	}
	if _, ok := objects["skills/installed/SKILL.md"]; !ok {
		t.Fatal("backend-generated skill file was not scanned")
	}
	if _, ok := objects["workspaces/ws/file.png.meta.json"]; ok {
		t.Fatal("metadata sidecar was scanned as an object")
	}
	if _, ok := objects["workspaces/ws/.file.png.tmp"]; ok {
		t.Fatal("staging file was scanned as an object")
	}
}

func TestSafeLocalPathRejectsTraversal(t *testing.T) {
	root := t.TempDir()
	if _, err := safeLocalPath(root, "../outside"); err == nil {
		t.Fatal("safeLocalPath accepted traversal")
	}
	path, err := safeLocalPath(root, "workspaces/ws/file.png")
	if err != nil {
		t.Fatalf("safeLocalPath() error = %v", err)
	}
	if want := filepath.Join(root, "workspaces", "ws", "file.png"); path != want {
		t.Fatalf("safeLocalPath() = %q, want %q", path, want)
	}
}
