package storage

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestHybridStorageWritesPrimaryAndReadsLegacy(t *testing.T) {
	primaryDir := t.TempDir()
	legacyDir := t.TempDir()
	primary := &LocalStorage{uploadDir: primaryDir}
	legacy := &LocalStorage{uploadDir: legacyDir}
	store := NewHybridStorage(primary, legacy)
	ctx := context.Background()

	const legacyKey = "workspaces/ws/legacy.png"
	if _, err := legacy.Upload(ctx, legacyKey, []byte("old"), "image/png", "legacy.png"); err != nil {
		t.Fatalf("seed legacy object: %v", err)
	}
	reader, err := store.GetReader(ctx, legacyKey)
	if err != nil {
		t.Fatalf("GetReader legacy: %v", err)
	}
	legacyBody, err := io.ReadAll(reader)
	_ = reader.Close()
	if err != nil || string(legacyBody) != "old" {
		t.Fatalf("legacy body = %q, err=%v", legacyBody, err)
	}

	const newKey = "workspaces/ws/new.png"
	if _, err := store.Upload(ctx, newKey, []byte("new"), "image/png", "new.png"); err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if _, err := os.Stat(filepath.Join(primaryDir, newKey)); err != nil {
		t.Fatalf("primary object missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(legacyDir, newKey)); !os.IsNotExist(err) {
		t.Fatalf("new object unexpectedly written to legacy storage, err=%v", err)
	}
}

func TestHybridStorageResolvesLegacyAndPrimaryURLs(t *testing.T) {
	primary := &LocalStorage{uploadDir: t.TempDir(), baseURL: "https://objects.example"}
	legacy := &LocalStorage{uploadDir: t.TempDir()}
	store := NewHybridStorage(primary, legacy)

	legacyBackend, legacyKey := store.ResolveURL("/uploads/workspaces/ws/old.txt")
	if legacyBackend != legacy || legacyKey != "workspaces/ws/old.txt" {
		t.Fatalf("legacy resolution = (%T, %q), want legacy backend and key", legacyBackend, legacyKey)
	}
	primaryURL := primary.ObjectURL("workspaces/ws/new.txt")
	primaryBackend, primaryKey := store.ResolveURL(primaryURL)
	if primaryBackend != primary || primaryKey != "workspaces/ws/new.txt" {
		t.Fatalf("primary resolution = (%T, %q), want primary backend and key", primaryBackend, primaryKey)
	}
}

func TestHybridStorageServesLegacyUploadRoute(t *testing.T) {
	legacyDir := t.TempDir()
	legacy := &LocalStorage{uploadDir: legacyDir}
	store := NewHybridStorage(&LocalStorage{uploadDir: t.TempDir()}, legacy)
	if _, err := legacy.Upload(context.Background(), "workspaces/ws/file.txt", []byte("body"), "text/plain", "file.txt"); err != nil {
		t.Fatalf("seed legacy object: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/uploads/workspaces/ws/file.txt", nil)
	rec := httptest.NewRecorder()
	store.ServeFile(rec, req, "workspaces/ws/file.txt")
	if rec.Code != http.StatusOK || rec.Body.String() != "body" {
		t.Fatalf("ServeFile = status %d body %q, want 200/body", rec.Code, rec.Body.String())
	}
}
