package handler

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Direct archive URL import: any http(s) link that points at a .zip/.skill
// bundle (GitHub release assets, repo zipballs, a vendor's download link…)
// downloads server-side and runs through the same archive parser as an
// uploaded file. Hosted fetchers (clawhub/skills.sh/github) stay on their
// allowlisted hosts; this path accepts arbitrary hosts, so it carries an SSRF
// guard — the target must resolve to a public IP before any request is made.

// archiveHostAllowed is a package var so tests can bypass the range check
// for a loopback httptest server.
var archiveHostAllowed = isPublicArchiveHost

// resolveArchiveHostIPs is a package var so tests can stub DNS.
var resolveArchiveHostIPs = func(ctx context.Context, host string) ([]net.IP, error) {
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	ips := make([]net.IP, 0, len(addrs))
	for _, a := range addrs {
		ips = append(ips, a.IP)
	}
	return ips, nil
}

// isPublicArchiveHost reports whether host resolves to at least one public IP.
// Loopback, private, link-local, and unspecified ranges are rejected: a URL
// from a user must never aim the server's outbound fetch at internal services.
func isPublicArchiveHost(ctx context.Context, host string) (bool, error) {
	if host == "" {
		return false, nil
	}
	// An IP literal parses without DNS; apply the same range rules.
	if ip := net.ParseIP(host); ip != nil {
		return ipIsPublic(ip), nil
	}
	ips, err := resolveArchiveHostIPs(ctx, host)
	if err != nil {
		return false, err
	}
	if len(ips) == 0 {
		return false, nil
	}
	for _, ip := range ips {
		if !ipIsPublic(ip) {
			return false, nil
		}
	}
	return true, nil
}

func ipIsPublic(ip net.IP) bool {
	return !ip.IsLoopback() && !ip.IsPrivate() && !ip.IsLinkLocalUnicast() &&
		!ip.IsLinkLocalMulticast() && !ip.IsUnspecified() && !ip.IsMulticast()
}

// fetchFromArchiveURL downloads a skill bundle from a direct archive URL and
// parses it with the same parser used for uploaded archives.
func fetchFromArchiveURL(ctx context.Context, httpClient *http.Client, raw string) (*importedSkill, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid archive URL: %w", err)
	}
	host := parsed.Hostname()
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("archive URL must use http or https: %s", raw)
	}
	public, err := archiveHostAllowed(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("resolve archive host %s: %w", host, err)
	}
	if !public {
		return nil, fmt.Errorf("archive URL host %s resolves to a private address and cannot be fetched", host)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, fmt.Errorf("build archive request: %w", err)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: fetching archive: %v", errImportSourceUnavailable, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: archive URL returned HTTP %d", errImportSourceUnavailable, resp.StatusCode)
	}

	// Cap the download at the same bound as an uploaded archive so a huge or
	// lying response cannot balloon memory before the parser sees it.
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxImportArchiveUploadSize+1))
	if err != nil {
		return nil, fmt.Errorf("%w: reading archive: %v", errImportSourceUnavailable, err)
	}
	if int64(len(data)) > maxImportArchiveUploadSize {
		return nil, fmt.Errorf("%w: archive exceeds %d bytes", errImportCapExceeded, int64(maxImportArchiveUploadSize))
	}

	filename := parsed.Path
	if idx := strings.LastIndexByte(filename, '/'); idx >= 0 {
		filename = filename[idx+1:]
	}
	imported, err := parseSkillArchive(data, filename)
	if err != nil {
		return nil, err
	}
	imported.origin = map[string]any{"type": "archive_url", "url": raw}
	return imported, nil
}

// archiveHTTPClient is the shared client for direct archive downloads. Kept
// separate from the hosted-source client so its timeout can be tuned without
// changing upstream API behavior.
var archiveHTTPClient = &http.Client{Timeout: 45 * time.Second}
