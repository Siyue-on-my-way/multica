package handler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

// URL-file drops: the inbox's answer to "我直接提供一个 skill 链接，希望能自动
// 下载并安装". A .url / .txt / .link file placed in the inbox lists one or more
// skill source URLs — a github.com repo or tree link, a clawhub.ai (OpenClaw
// Hub) or skills.sh page, or a direct .zip/.skill download — and every listed
// URL is downloaded server-side through the same fetchers as the API import
// path, registered as a GLOBAL skill, and given the usual extracted copy under
// skills/<name>/. On success the URL file is deleted; any failure quarantines
// it under failed/ with a .error note naming the URLs that failed.

const (
	// skillInboxURLFileMaxSize bounds a dropped URL file, which is only ever a
	// list of links.
	skillInboxURLFileMaxSize = 64 << 10 // 64 KiB
	// skillInboxURLFileMaxURLs caps how many skills one dropped file may
	// import, so a pathological file cannot stall the scan loop for hours.
	skillInboxURLFileMaxURLs = 25
)

// importInboxSkillFromURL is a package var so the file-level flow can be
// tested without a database; production always uses the real implementation.
var importInboxSkillFromURL = defaultImportInboxSkillFromURL

// processSkillInboxURLFile reads a dropped URL file and imports one global
// skill per URL it lists. A nil return means every URL imported and the file
// was deleted; any error sends the file to the failed/ quarantine by the
// caller (skills already registered stay registered — imports overwrite in
// place, so re-dropping the file after a fix only retries the failures).
func processSkillInboxURLFile(ctx context.Context, h *Handler, cfg SkillInboxConfig, path string) error {
	inFlight, retryLater, err := claimInboxFile(path)
	if err != nil {
		return err
	}
	if retryLater {
		return errSkillInboxRetryLater
	}

	data, err := os.ReadFile(inFlight)
	if err != nil {
		return fmt.Errorf("read URL file: %w", err)
	}
	if len(data) > skillInboxURLFileMaxSize {
		return fmt.Errorf("URL file exceeds %d bytes", skillInboxURLFileMaxSize)
	}

	urls := extractInboxURLs(string(data))
	if len(urls) == 0 {
		return errors.New("no skill URL found in file (expected a github.com, clawhub.ai, skills.sh, or direct .zip/.skill link)")
	}
	if len(urls) > skillInboxURLFileMaxURLs {
		return fmt.Errorf("URL file lists %d URLs, above the %d-URL limit", len(urls), skillInboxURLFileMaxURLs)
	}

	var succeeded, failures []string
	for _, raw := range urls {
		if err := importInboxSkillFromURL(ctx, h, cfg, raw); err != nil {
			failures = append(failures, raw+" — "+err.Error())
			continue
		}
		succeeded = append(succeeded, raw)
	}
	if len(failures) > 0 {
		detail := strings.Join(failures, "; ")
		if len(succeeded) > 0 {
			return fmt.Errorf("%d of %d URL imports failed: %s (already installed: %s)",
				len(failures), len(urls), detail, strings.Join(succeeded, ", "))
		}
		return fmt.Errorf("all %d URL imports failed: %s", len(urls), detail)
	}

	if err := os.Remove(inFlight); err != nil {
		return fmt.Errorf("delete URL file: %w", err)
	}
	slog.Info("skill inbox: installed skills from URL file", "file", filepath.Base(path), "count", len(urls))
	return nil
}

// defaultImportInboxSkillFromURL downloads one skill from raw, registers it as
// a global skill, and refreshes its extracted copy in the inbox.
func defaultImportInboxSkillFromURL(ctx context.Context, h *Handler, cfg SkillInboxConfig, raw string) error {
	// Same bound as the API import path: under the reverse-proxy gateway
	// timeout, so a slow source fails this drop instead of hanging the scan.
	fetchCtx, cancel := context.WithTimeout(ctx, importFetchTimeout)
	defer cancel()

	imported, err := fetchInboxSkillFromURL(fetchCtx, raw)
	if err != nil {
		return err
	}
	name := sanitizeNullBytes(imported.name)
	if name == "" {
		return errors.New("imported skill has no name (SKILL.md frontmatter is empty and no filename fallback)")
	}

	resp, created, err := upsertGlobalSkillFromImport(ctx, h, name, imported)
	if err != nil {
		return fmt.Errorf("register global skill: %w", err)
	}

	slug := skillInboxSlug(name)
	if err := writeSkillInboxExtractedCopy(cfg.Dir, slug, imported); err != nil {
		// The skill IS registered; a broken extracted copy must not roll that
		// back or delete the URL file. Surface it loudly instead.
		slog.Error("skill inbox: extracted copy failed; skill registered, URL file kept", "skill", name, "url", raw, "error", err)
		return fmt.Errorf("write extracted copy (skill registered): %w", err)
	}

	if created {
		h.publish(protocol.EventSkillCreated, resp.WorkspaceID, "system", "", map[string]any{"skill": resp})
	} else {
		h.publish(protocol.EventSkillUpdated, resp.WorkspaceID, "system", "", map[string]any{"skill": resp})
	}
	slog.Info("skill inbox: imported global skill from URL", "skill", name, "url", raw, "created", created)
	return nil
}

// fetchInboxSkillFromURL downloads a skill from a dropped URL using the same
// source dispatch as the API import path.
func fetchInboxSkillFromURL(ctx context.Context, raw string) (*importedSkill, error) {
	source, normalized, err := detectImportSource(raw)
	if err != nil {
		return nil, err
	}
	if source == sourceArchive {
		// archiveHTTPClient carries its own timeout and the fetcher its own
		// SSRF guard, since arbitrary hosts are allowed here.
		return fetchFromArchiveURL(ctx, archiveHTTPClient, normalized)
	}
	httpClient := &http.Client{Timeout: 30 * time.Second}
	switch source {
	case sourceClawHub:
		return fetchFromClawHub(ctx, httpClient, normalized)
	case sourceSkillsSh:
		return fetchFromSkillsSh(ctx, httpClient, normalized)
	case sourceGitHub:
		return fetchFromGitHub(ctx, httpClient, normalized)
	default:
		return nil, fmt.Errorf("unsupported import source: %s", normalized)
	}
}

// inboxURLPattern matches a scheme-bearing URL anywhere in a line: a bare
// link, a Windows-style `url=...` .url shortcut, or a link embedded in prose
// or markdown. Brackets and quotes end the match so markdown links and HTML
// attributes don't bleed punctuation into the URL.
var inboxURLPattern = regexp.MustCompile(`https?://[^\s"'<>()\[\]{}]+`)

// inboxBareHostPrefixes are the hosts accepted for scheme-less lines — the
// browser-address-bar form of a supported source, e.g. a pasted
// "github.com/owner/repo/tree/main/my-skill".
var inboxBareHostPrefixes = []string{
	"github.com/",
	"www.github.com/",
	"codeload.github.com/",
	"clawhub.ai/",
	"www.clawhub.ai/",
	"skills.sh/",
	"www.skills.sh/",
}

// extractInboxURLs pulls every candidate skill URL out of a dropped text file.
// Scheme-bearing links match anywhere in a line; a scheme-less line that starts
// with a supported host gets an https:// prefix. Duplicates are dropped, order
// preserved.
func extractInboxURLs(content string) []string {
	var urls []string
	seen := make(map[string]bool)
	add := func(raw string) {
		raw = strings.TrimRight(raw, ".,;:!?")
		if raw != "" && !seen[raw] {
			seen[raw] = true
			urls = append(urls, raw)
		}
	}
	for _, line := range strings.Split(content, "\n") {
		if m := inboxURLPattern.FindString(line); m != "" {
			add(m)
			continue
		}
		candidate := strings.TrimSpace(strings.TrimPrefix(line, "-"))
		if candidate == "" || strings.ContainsAny(candidate, " \t") {
			continue // prose or decoration, not a bare link
		}
		lower := strings.ToLower(candidate)
		for _, prefix := range inboxBareHostPrefixes {
			if strings.HasPrefix(lower, prefix) {
				add("https://" + candidate)
				break
			}
		}
	}
	return urls
}
