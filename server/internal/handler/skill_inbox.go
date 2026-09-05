package handler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/pkg/skillbundle"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// The skill inbox: a host-side drop zone for skill bundles and skill links.
// An administrator copies a .zip / .skill bundle — or a .url / .txt / .link
// file listing skill source URLs (github.com, clawhub.ai, skills.sh, or a
// direct archive link) — into {LOCAL_UPLOAD_DIR}/skills/ (in the docker
// deployment that is docker/volumes/backend_uploads/skills on the host), the
// backend picks it up, registers the result as a GLOBAL skill (readable by
// every workspace, attached to a run only when the run's trigger text names
// the skill), leaves the extracted tree at skills/<name>/ as a human-readable
// record of what is deployed, and deletes the source file so the inbox drains
// to empty. Failed drops are quarantined under skills/failed/ with a .error
// note instead of being silently swallowed.
//
// There is no user identity behind an inbox drop — placing a file on the
// server's upload volume is itself an administrator action, so imports here
// bypass the per-user ownership checks (skillOverwriteInput.AllowOverwrite)
// and same-name conflicts overwrite in place, per the SIY-95 decision.

const (
	// SkillInboxSubdir is the inbox directory relative to the upload root.
	SkillInboxSubdir = "skills"
	// skillInboxFailedSubdir holds bundles that failed to import.
	skillInboxFailedSubdir = "failed"
	// skillInboxInFlightSuffix marks an archive that is being processed, so a
	// partially-processed file is never picked up concurrently.
	skillInboxInFlightSuffix = ".importing"
	// skillInboxQuietPeriod: a file must be untouched this long before it is
	// read, so a copy still in progress is not observed mid-write.
	skillInboxQuietPeriod = 3 * time.Second
	// skillInboxDefaultPollInterval balances pickup latency against stat load.
	skillInboxDefaultPollInterval = 15 * time.Second
)

// SkillInboxConfig is resolved from the environment once at startup.
type SkillInboxConfig struct {
	Dir           string
	PollInterval  time.Duration
	WorkspaceID   string // optional fixed owner workspace for created skills
}

// SkillInboxConfigFromEnv resolves the inbox configuration. Enabled-by-default:
// Dir falls back to the same upload root the local storage backend uses.
func SkillInboxConfigFromEnv() SkillInboxConfig {
	dir := os.Getenv("MULTICA_SKILL_INBOX_DIR")
	if dir == "" {
		uploadRoot := os.Getenv("LOCAL_UPLOAD_DIR")
		if uploadRoot == "" {
			uploadRoot = "./data/uploads"
		}
		dir = filepath.Join(uploadRoot, SkillInboxSubdir)
	}
	interval := skillInboxDefaultPollInterval
	if raw := os.Getenv("MULTICA_SKILL_INBOX_POLL_INTERVAL"); raw != "" {
		if parsed, err := time.ParseDuration(raw); err == nil && parsed >= 5*time.Second {
			interval = parsed
		} else if err != nil {
			slog.Warn("invalid MULTICA_SKILL_INBOX_POLL_INTERVAL, using default", "value", raw, "default", skillInboxDefaultPollInterval)
		}
	}
	return SkillInboxConfig{
		Dir:          dir,
		PollInterval: interval,
		WorkspaceID:  os.Getenv("MULTICA_SKILL_INBOX_WORKSPACE_ID"),
	}
}

// StartSkillInboxWatcher creates the inbox directories and launches the
// background scan loop. Startup failures are logged, not fatal: a broken
// inbox must not take the API down.
func StartSkillInboxWatcher(ctx context.Context, h *Handler, cfg SkillInboxConfig) {
	if err := os.MkdirAll(cfg.Dir, 0o755); err != nil {
		slog.Error("skill inbox disabled: cannot create inbox dir", "dir", cfg.Dir, "error", err)
		return
	}
	if err := os.MkdirAll(filepath.Join(cfg.Dir, skillInboxFailedSubdir), 0o755); err != nil {
		slog.Error("skill inbox disabled: cannot create failed dir", "dir", cfg.Dir, "error", err)
		return
	}
	reclaimInFlightArchives(cfg.Dir)
	go runSkillInboxLoop(ctx, h, cfg)
	slog.Info("skill inbox watcher started", "dir", cfg.Dir, "poll_interval", cfg.PollInterval)
}

// reclaimInFlightArchives renames leftover *.importing files back to their
// original names so a crash mid-import is retried on the next start instead of
// stranding the drop in a state the scanner skips.
func reclaimInFlightArchives(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, skillInboxInFlightSuffix) {
			continue
		}
		restored := strings.TrimSuffix(name, skillInboxInFlightSuffix)
		if err := os.Rename(filepath.Join(dir, name), filepath.Join(dir, restored)); err != nil {
			slog.Warn("skill inbox: failed to reclaim in-flight file", "file", name, "error", err)
		} else {
			slog.Info("skill inbox: reclaimed interrupted drop", "file", restored)
		}
	}
}

func runSkillInboxLoop(ctx context.Context, h *Handler, cfg SkillInboxConfig) {
	ticker := time.NewTicker(cfg.PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			scanSkillInbox(ctx, h, cfg)
		}
	}
}

func scanSkillInbox(ctx context.Context, h *Handler, cfg SkillInboxConfig) {
	entries, err := os.ReadDir(cfg.Dir)
	if err != nil {
		slog.Warn("skill inbox: scan failed", "dir", cfg.Dir, "error", err)
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		select {
		case <-ctx.Done():
			return
		default:
		}
		path := filepath.Join(cfg.Dir, e.Name())
		// Drops are claimed under their .importing name during processing, so
		// quarantine must look there (it falls back to the original name for
		// a failure that happened before the claim).
		inFlight := path + skillInboxInFlightSuffix
		switch inboxFileKind(e.Name()) {
		case inboxKindArchive:
			if err := processSkillInboxArchive(ctx, h, cfg, path); err != nil {
				slog.Warn("skill inbox: import failed", "file", e.Name(), "error", err)
				quarantineSkillInboxFile(cfg.Dir, inFlight, err)
			}
		case inboxKindURLFile:
			if err := processSkillInboxURLFile(ctx, h, cfg, path); err != nil {
				slog.Warn("skill inbox: URL import failed", "file", e.Name(), "error", err)
				quarantineSkillInboxFile(cfg.Dir, inFlight, err)
			}
		}
	}
}

// inboxFileKind classifies a dropped file by extension: archives are parsed
// as bundles, a .url / .txt / .link file is read as a list of skill source
// URLs to download and install, and everything else is left untouched.
func inboxFileKind(name string) inboxDropKind {
	lower := strings.ToLower(name)
	switch {
	case strings.HasSuffix(lower, ".zip"), strings.HasSuffix(lower, ".skill"):
		return inboxKindArchive
	case strings.HasSuffix(lower, ".url"), strings.HasSuffix(lower, ".link"), strings.HasSuffix(lower, ".txt"):
		return inboxKindURLFile
	default:
		return inboxKindNone
	}
}

type inboxDropKind int

const (
	inboxKindNone inboxDropKind = iota
	inboxKindArchive
	inboxKindURLFile
)

// claimInboxFile renames a dropped file to its .importing form so a later scan
// can never pick it up mid-processing. retryLater reports a file that is still
// settling: it was restored to its original name and the caller should skip it
// until the next scan instead of quarantining it.
func claimInboxFile(path string) (inFlight string, retryLater bool, err error) {
	inFlight = path + skillInboxInFlightSuffix
	if err := os.Rename(path, inFlight); err != nil {
		return "", false, fmt.Errorf("claim drop: %w", err)
	}

	// From here on the drop is claimed under its .importing name; any error
	// path leaves it in place for the caller to quarantine under the
	// original name.

	info, err := os.Stat(inFlight)
	if err != nil {
		return "", false, fmt.Errorf("stat claimed drop: %w", err)
	}
	if time.Since(info.ModTime()) < skillInboxQuietPeriod {
		// Still settling — put it back for a later scan.
		if renameErr := os.Rename(inFlight, path); renameErr != nil {
			return "", false, fmt.Errorf("restore unsettled drop: %w", renameErr)
		}
		return "", true, nil
	}
	return inFlight, false, nil
}

// processSkillInboxArchive imports one archive. A nil return means the bundle
// was registered and the archive deleted; any error sends the archive to the
// failed/ quarantine by the caller.
func processSkillInboxArchive(ctx context.Context, h *Handler, cfg SkillInboxConfig, path string) error {
	inFlight, retryLater, err := claimInboxFile(path)
	if err != nil {
		return err
	}
	if retryLater {
		return errSkillInboxRetryLater
	}

	data, err := os.ReadFile(inFlight)
	if err != nil {
		return fmt.Errorf("read archive: %w", err)
	}
	if int64(len(data)) > maxImportArchiveUploadSize {
		return fmt.Errorf("archive exceeds the %d byte import cap", int64(maxImportArchiveUploadSize))
	}

	imported, err := parseSkillArchive(data, filepath.Base(path))
	if err != nil {
		return fmt.Errorf("parse archive: %w", err)
	}
	name := sanitizeNullBytes(imported.name)
	if name == "" {
		return errors.New("archive has no skill name (SKILL.md frontmatter is empty and the filename is unnamed)")
	}

	resp, created, err := upsertGlobalSkillFromImport(ctx, h, name, imported)
	if err != nil {
		return fmt.Errorf("register global skill: %w", err)
	}

	slug := skillInboxSlug(name)
	if err := writeSkillInboxExtractedCopy(cfg.Dir, slug, imported); err != nil {
		// The skill IS registered; a broken archive copy must not roll that
		// back or delete the source. Surface it loudly instead.
		slog.Error("skill inbox: extracted copy failed; skill registered, archive kept", "skill", name, "error", err)
		return fmt.Errorf("write extracted copy (skill registered): %w", err)
	}

	if err := os.Remove(inFlight); err != nil {
		return fmt.Errorf("delete archive: %w", err)
	}

	if created {
		h.publish(protocol.EventSkillCreated, resp.WorkspaceID, "system", "", map[string]any{"skill": resp})
	} else {
		h.publish(protocol.EventSkillUpdated, resp.WorkspaceID, "system", "", map[string]any{"skill": resp})
	}
	slog.Info("skill inbox: imported global skill", "skill", name, "created", created)
	return nil
}

// errSkillInboxRetryLater signals a not-yet-settled file: it was restored to
// its original name and should be retried on a later scan, not quarantined.
var errSkillInboxRetryLater = errors.New("skill inbox: archive not settled yet")

// upsertGlobalSkillFromImport registers the parsed bundle as a global skill.
// Same global name overwrites in place (SIY-95 decision 1: 覆盖); the row
// keeps its original id, creator and agent bindings.
func upsertGlobalSkillFromImport(ctx context.Context, h *Handler, name string, imported *importedSkill) (SkillWithFilesResponse, bool, error) {
	files := make([]CreateSkillFileRequest, 0, len(imported.files))
	for _, f := range imported.files {
		if !validateFilePath(f.path) {
			continue
		}
		files = append(files, CreateSkillFileRequest{
			Path:            f.path,
			Content:         f.content,
			ContentBase64:   f.contentBase64,
			ContentEncoding: f.contentEncoding,
			Mode:            int32PtrIfNonDefault(f.mode),
		})
	}

	existing, found, err := h.lookupGlobalSkillByName(ctx, name)
	if err != nil {
		return SkillWithFilesResponse{}, false, err
	}
	if found {
		overwrite, err := h.overwriteSkillWithFiles(ctx, skillOverwriteInput{
			// Key the overwrite on the row's own workspace: a global skill can
			// be owned by a workspace other than the inbox default.
			WorkspaceID:   existing.WorkspaceID,
			TargetSkillID: existing.ID,
			ExpectedName:  existing.Name,
			Description:   imported.description,
			Content:       imported.content,
			Config:        map[string]any{}, // no HTTP provenance on an inbox drop
			Files:         files,
			// No user is behind an inbox drop; the host filesystem is the
			// administrator credential here.
			AllowOverwrite: allowAllSkillOverwrite,
		})
		return overwrite, false, err
	}

	workspaceID, err := h.skillInboxWorkspaceID(ctx)
	if err != nil {
		return SkillWithFilesResponse{}, false, err
	}
	// created_by stays NULL: the introducer is the host administrator, and a
	// NULL creator restricts management to workspace admins by design.
	created, err := h.createImportedSkillWithName(ctx, workspaceID, pgtype.UUID{}, name, imported, map[string]any{}, files, true)
	return created, true, err
}

// skillInboxWorkspaceID resolves the workspace that owns inbox-created global
// skill rows: the configured workspace, else the oldest workspace in the
// deployment (self-hosted installs typically have exactly one).
func (h *Handler) skillInboxWorkspaceID(ctx context.Context) (pgtype.UUID, error) {
	if id := os.Getenv("MULTICA_SKILL_INBOX_WORKSPACE_ID"); id != "" {
		parsed := parseUUID(id)
		if parsed.Valid {
			return parsed, nil
		}
		slog.Warn("skill inbox: invalid MULTICA_SKILL_INBOX_WORKSPACE_ID, falling back to oldest workspace", "value", id)
	}
	wsID, err := h.Queries.GetOldestWorkspaceID(ctx)
	if err != nil {
		return pgtype.UUID{}, fmt.Errorf("resolve inbox workspace (set MULTICA_SKILL_INBOX_WORKSPACE_ID if this persists): %w", err)
	}
	return wsID, nil
}

// writeSkillInboxExtractedCopy refreshes skills/<slug>/ with the imported
// bundle so the directory shows what is actually deployed. The copy is a
// human-readable record only — runtimes receive skills through the task
// payload, never from this directory.
func writeSkillInboxExtractedCopy(inboxDir, slug string, imported *importedSkill) error {
	target := filepath.Join(inboxDir, slug)
	if slug == "" || slug == "." || slug == ".." || target == inboxDir {
		return fmt.Errorf("unsafe skill slug %q", slug)
	}
	if err := os.RemoveAll(target); err != nil {
		return fmt.Errorf("clear previous copy: %w", err)
	}
	if err := os.MkdirAll(target, 0o755); err != nil {
		return fmt.Errorf("create skill dir: %w", err)
	}
	if err := os.WriteFile(filepath.Join(target, "SKILL.md"), []byte(imported.content), 0o644); err != nil {
		return fmt.Errorf("write SKILL.md: %w", err)
	}
	for _, f := range imported.files {
		if !validateFilePath(f.path) {
			continue
		}
		content, err := skillbundle.FileBytes(skillbundle.File{
			Path:            f.path,
			Content:         f.content,
			ContentBase64:   f.contentBase64,
			ContentEncoding: f.contentEncoding,
			Mode:            f.mode,
		})
		if err != nil {
			return fmt.Errorf("decode %q: %w", f.path, err)
		}
		fpath := filepath.Join(target, filepath.FromSlash(f.path))
		if err := os.MkdirAll(filepath.Dir(fpath), 0o755); err != nil {
			return fmt.Errorf("create dir for %q: %w", f.path, err)
		}
		if err := os.WriteFile(fpath, content, os.FileMode(skillbundle.NormalizeFileMode(f.mode))); err != nil {
			return fmt.Errorf("write %q: %w", f.path, err)
		}
	}
	return nil
}

// quarantineSkillInboxFile moves a failed drop (still at inFlight) into
// failed/ with a sibling .error note describing the reason. It serves both
// archive bundles and URL files.
func quarantineSkillInboxFile(inboxDir, inFlightPath string, cause error) {
	if errors.Is(cause, errSkillInboxRetryLater) {
		return // restored in place; not a failure
	}
	failedDir := filepath.Join(inboxDir, skillInboxFailedSubdir)
	if err := os.MkdirAll(failedDir, 0o755); err != nil {
		slog.Error("skill inbox: cannot create failed dir", "dir", failedDir, "error", err)
		return
	}
	base := filepath.Base(strings.TrimSuffix(inFlightPath, skillInboxInFlightSuffix))
	stamp := time.Now().UTC().Format("20060102-150405")
	target := filepath.Join(failedDir, stamp+"-"+base)
	// The drop normally sits at its .importing name when a failure is
	// reported; a failure before the claim left it at the original path.
	src := inFlightPath
	if _, err := os.Stat(src); err != nil {
		src = strings.TrimSuffix(inFlightPath, skillInboxInFlightSuffix)
	}
	if err := os.Rename(src, target); err != nil {
		slog.Error("skill inbox: cannot quarantine failed drop", "file", inFlightPath, "error", err)
		return
	}
	note := target + ".error"
	noteContent := fmt.Sprintf("imported at: %s\nerror: %s\n", time.Now().UTC().Format(time.RFC3339), cause)
	if err := os.WriteFile(note, []byte(noteContent), 0o644); err != nil {
		slog.Warn("skill inbox: cannot write failure note", "file", note, "error", err)
	}
	slog.Warn("skill inbox: archive quarantined", "file", target, "error", cause)
}

// skillInboxSlug maps a skill name to one safe directory name under the
// inbox. Names come from untrusted archive frontmatter, so the slug can never
// contain a separator or dot segment.
func skillInboxSlug(name string) string {
	s := strings.ToLower(strings.TrimSpace(name))
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	slug := strings.Trim(b.String(), "-")
	if slug == "" {
		slug = "unnamed-skill"
	}
	return slug
}
