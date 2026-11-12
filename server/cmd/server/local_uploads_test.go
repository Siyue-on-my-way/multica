package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/multica-ai/multica/server/internal/analytics"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/realtime"
	"github.com/multica-ai/multica/server/internal/testutil"
)

// TestLocalUploadLegacyRouteServesHistoricalObjects pins the fork's
// deliberate deviation from upstream: upstream removed the /uploads/* route
// in favor of signed downloads, but this fork keeps it so historical object
// URLs stay readable while the MinIO migration moves legacy objects
// (see router.go and LocalStorage.ServeFile).
func TestLocalUploadLegacyRouteServesHistoricalObjects(t *testing.T) {
	t.Setenv("S3_BUCKET", "")
	t.Setenv("LOCAL_UPLOAD_BASE_URL", "")
	dir := t.TempDir()
	t.Setenv("LOCAL_UPLOAD_DIR", dir)
	key := "workspaces/" + testWorkspaceID + "/" + uuid.NewString() + ".png"
	full := filepath.Join(dir, filepath.FromSlash(key))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{full, full + ".meta.json"} {
		if err := os.WriteFile(name, []byte("PRIVATE"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	router := NewRouter(testPool, realtime.NewHub(), events.New(), analytics.NoopClient{}, nil)
	req := httptest.NewRequest(http.MethodGet, "/uploads/"+key, nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("GET /uploads/%s = %d, want 200 (fork keeps the legacy route)", key, w.Code)
	}
	// Sidecar metadata and staging files stay non-servable.
	req = httptest.NewRequest(http.MethodGet, "/uploads/"+key+".meta.json", nil)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("GET /uploads/%s.meta.json = %d, want 404", key, w.Code)
	}
}

func TestLocalAttachmentAPIStillRequiresMembership(t *testing.T) {
	t.Setenv("CORS_ALLOWED_ORIGINS", "https://app.example.test")
	t.Setenv("S3_BUCKET", "")
	t.Setenv("LOCAL_UPLOAD_BASE_URL", "")
	dir := t.TempDir()
	t.Setenv("LOCAL_UPLOAD_DIR", dir)
	id := uuid.NewString()
	key := "workspaces/" + testWorkspaceID + "/" + id + ".png"
	full := filepath.Join(dir, filepath.FromSlash(key))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	const body = "PRIVATE IMAGE"
	if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	fixture := testutil.New(testPool, testWorkspaceID, testUserID)
	fixture.Insert(t, "attachment", testutil.Cols{
		"id": id, "workspace_id": testWorkspaceID,
		"uploader_type": "member", "uploader_id": testUserID,
		"filename": "private.png", "url": "/uploads/" + key,
		"content_type": "image/png", "size_bytes": len(body),
	})
	foreignToken, err := generateTestJWT(uuid.NewString(), "outsider@example.test", "Outsider")
	if err != nil {
		t.Fatal(err)
	}
	router := NewRouter(testPool, realtime.NewHub(), events.New(), analytics.NoopClient{}, nil)
	for _, tc := range []struct {
		name  string
		token string
		code  int
	}{
		{"anonymous", "", http.StatusUnauthorized},
		{"non-member", foreignToken, http.StatusNotFound},
		{"member", testToken, http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/attachments/"+id+"/download", nil)
			if tc.token != "" {
				req.Header.Set("Authorization", "Bearer "+tc.token)
			}
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
			if w.Code != tc.code {
				t.Fatalf("status = %d, want %d; body=%s", w.Code, tc.code, w.Body.String())
			}
			if tc.code == http.StatusOK {
				if got := w.Body.String(); got != body {
					t.Fatalf("body = %q, want %q", got, body)
				}
				if got := w.Header().Get("Content-Disposition"); got != `inline; filename="private.png"` {
					t.Errorf("Content-Disposition = %q, want original filename", got)
				}
				if got := w.Header().Get("Content-Security-Policy"); !strings.Contains(got, "frame-ancestors 'self' https://app.example.test") {
					t.Errorf("Content-Security-Policy = %q, want configured preview origin", got)
				}
			}
		})
	}
}
