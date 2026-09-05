package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDetectImportSourceArchiveURLs(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want importSource
	}{
		{"github release asset", "https://github.com/owner/repo/releases/download/v1/my-skill.zip", sourceArchive},
		{"github branch zipball", "https://github.com/owner/repo/archive/refs/heads/main.zip", sourceArchive},
		{"codeload", "https://codeload.github.com/owner/repo/zip/refs/heads/main", sourceArchive},
		{"vendor zip with query token", "https://example.com/downloads/skill.zip?token=abc", sourceArchive},
		{"dot-skill extension", "https://cdn.example.com/bundles/cool.skill", sourceArchive},
		{"uppercase extension", "https://example.com/SKILL.ZIP", sourceArchive},
		// Hosted sources keep their own routes.
		{"clawhub", "https://clawhub.ai/owner/skill", sourceClawHub},
		{"skills.sh", "https://skills.sh/owner/repo/skill", sourceSkillsSh},
		{"github repo", "https://github.com/owner/repo", sourceGitHub},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, _, err := detectImportSource(tc.raw)
			if err != nil {
				t.Fatalf("detectImportSource(%q): %v", tc.raw, err)
			}
			if got != tc.want {
				t.Fatalf("detectImportSource(%q) = %v, want %v", tc.raw, got, tc.want)
			}
		})
	}
}

func TestDetectImportSourceArchivePathNotFooledByQuery(t *testing.T) {
	// ".zip" only in the query string is not an archive path; github host
	// routes to the repo fetcher.
	got, _, err := detectImportSource("https://github.com/owner/repo?file=x.zip")
	if err != nil {
		t.Fatalf("detectImportSource: %v", err)
	}
	if got != sourceGitHub {
		t.Fatalf("source = %v, want %v", got, sourceGitHub)
	}
}

func TestFetchFromArchiveURLDownloadsAndParses(t *testing.T) {
	// The SSRF guard is bypassed for this loopback httptest server; the guard
	// itself is exercised by the rejection tests with its real logic.
	restore := archiveHostAllowed
	archiveHostAllowed = func(ctx context.Context, host string) (bool, error) { return true, nil }
	defer func() { archiveHostAllowed = restore }()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/zip")
		_, _ = w.Write(buildTestZip(t, map[string]string{
			"SKILL.md":       "---\nname: url-skill\ndescription: from a direct link\n---\n\n# url-skill\n",
			"scripts/run.sh": "#!/bin/sh\necho hi\n",
		}))
	}))
	defer srv.Close()

	imported, err := fetchFromArchiveURL(context.Background(), srv.Client(), srv.URL+"/bundles/url-skill.zip")
	if err != nil {
		t.Fatalf("fetchFromArchiveURL: %v", err)
	}
	if imported.name != "url-skill" {
		t.Fatalf("name = %q, want url-skill", imported.name)
	}
	if len(imported.files) != 1 {
		t.Fatalf("files = %d, want 1 (SKILL.md is primary content)", len(imported.files))
	}
	if imported.origin == nil || imported.origin["type"] != "archive_url" {
		t.Fatalf("origin = %v, want archive_url provenance", imported.origin)
	}
}

func TestFetchFromArchiveURLRejectsPrivateHost(t *testing.T) {
	// httptest serves on 127.0.0.1; with real DNS the guard must refuse to
	// even request it — a user-supplied URL must not probe internal services.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("request must not reach a private host")
	}))
	defer srv.Close()

	_, err := fetchFromArchiveURL(context.Background(), srv.Client(), srv.URL+"/x.zip")
	if err == nil {
		t.Fatal("expected private-host rejection")
	}
	if !strings.Contains(err.Error(), "private address") {
		t.Fatalf("error = %v, want private-address rejection", err)
	}
}

func TestFetchFromArchiveURLRejectsOversize(t *testing.T) {
	restore := archiveHostAllowed
	archiveHostAllowed = func(ctx context.Context, host string) (bool, error) { return true, nil }
	defer func() { archiveHostAllowed = restore }()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		// Write past the cap; the reader must stop at the bound.
		_, _ = w.Write(make([]byte, maxImportArchiveUploadSize+1024))
	}))
	defer srv.Close()

	_, err := fetchFromArchiveURL(context.Background(), srv.Client(), srv.URL+"/big.zip")
	if err == nil {
		t.Fatal("expected oversize rejection")
	}
	if !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("error = %v, want cap error", err)
	}
}
