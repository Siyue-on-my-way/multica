package handler

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/storage"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Workspace file library: workspace-scoped, first-class files that outlive any
// single message. The composer references a library file through an attachment
// row (attachment.workspace_file_id) instead of re-uploading bytes, and task
// claims carry the sha256-pinned metadata so runtimes can materialize a
// verified local copy before the agent starts.

// maxLibraryFilenameBytes keeps the filename column (and the runtime-side
// materialized path) bounded.
const maxLibraryFilenameBytes = 1024

type WorkspaceFileResponse struct {
	ID           string `json:"id"`
	WorkspaceID  string `json:"workspace_id"`
	Filename     string `json:"filename"`
	ContentType  string `json:"content_type"`
	SizeBytes    int64  `json:"size_bytes"`
	Sha256       string `json:"sha256,omitempty"`
	UploaderType string `json:"uploader_type"`
	UploaderID   string `json:"uploader_id"`
	CreatedAt    string `json:"created_at"`
	DownloadURL  string `json:"download_url"`
}

func (h *Handler) workspaceFileToResponse(f db.WorkspaceFile) WorkspaceFileResponse {
	return WorkspaceFileResponse{
		ID:           uuidToString(f.ID),
		WorkspaceID:  uuidToString(f.WorkspaceID),
		Filename:     f.Filename,
		ContentType:  f.ContentType,
		SizeBytes:    f.SizeBytes,
		Sha256:       f.Sha256,
		UploaderType: f.UploaderType,
		UploaderID:   uuidToString(f.UploaderID),
		CreatedAt:    f.CreatedAt.Time.Format("2006-01-02T15:04:05Z07:00"),
		DownloadURL:  "/api/workspace-files/" + uuidToString(f.ID) + "/download",
	}
}

// UploadWorkspaceFile handles POST /api/workspace-files: store bytes in object
// storage under the workspace's library prefix and index them in
// workspace_file with a content digest so runtime delivery can be verified.
func (h *Handler) UploadWorkspaceFile(w http.ResponseWriter, r *http.Request) {
	if h.Storage == nil {
		writeError(w, http.StatusServiceUnavailable, "file upload not configured")
		return
	}

	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	workspaceID := h.resolveWorkspaceID(r)
	if workspaceID == "" {
		writeError(w, http.StatusBadRequest, "workspace context required")
		return
	}
	if _, ok := h.requireWorkspaceMember(w, r, workspaceID, "workspace not found"); !ok {
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxUploadSize)
	if err := r.ParseMultipartForm(maxUploadSize); err != nil {
		writeError(w, http.StatusBadRequest, "file too large or invalid multipart form")
		return
	}
	defer r.MultipartForm.RemoveAll()

	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("missing file field: %v", err))
		return
	}
	defer file.Close()

	if header.Filename == "" || len(header.Filename) > maxLibraryFilenameBytes {
		writeError(w, http.StatusBadRequest, "invalid filename")
		return
	}

	// Sniff actual content type from file bytes instead of trusting the client header.
	buf := make([]byte, 512)
	n, err := file.Read(buf)
	if err != nil && err != io.EOF {
		writeError(w, http.StatusBadRequest, "failed to read file")
		return
	}
	contentType := http.DetectContentType(buf[:n])
	if ct, ok := extContentTypes[strings.ToLower(path.Ext(header.Filename))]; ok {
		contentType = ct
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to read file")
		return
	}

	data, err := io.ReadAll(file)
	if err != nil {
		writeError(w, http.StatusBadRequest, "failed to read file")
		return
	}

	id, err := uuid.NewV7()
	if err != nil {
		slog.Error("failed to generate uuid", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	objectName := id.String() + path.Ext(header.Filename)
	key := "workspaces/" + workspaceID + "/files/" + objectName

	link, err := h.Storage.Upload(r.Context(), key, data, contentType, header.Filename)
	if err != nil {
		slog.Error("workspace file upload failed", "workspace_id", workspaceID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to store file")
		return
	}

	digest := sha256.Sum256(data)
	uploaderType, uploaderID := h.resolveActor(r, userID, workspaceID)
	uploaderUUID := parseUUID(uploaderID)
	if !uploaderUUID.Valid {
		// resolveActor always returns a UUID for member and agent actors; guard
		// anyway so a future actor kind cannot insert a NULL uploader_id.
		uploaderType = "member"
		uploaderUUID = parseUUID(userID)
	}

	fileRow, err := h.Queries.CreateWorkspaceFile(r.Context(), db.CreateWorkspaceFileParams{
		ID:           pgtype.UUID{Bytes: id, Valid: true},
		WorkspaceID:  parseUUID(workspaceID),
		Filename:     header.Filename,
		Url:          link,
		ContentType:  contentType,
		SizeBytes:    int64(len(data)),
		Sha256:       hex.EncodeToString(digest[:]),
		UploaderType: uploaderType,
		UploaderID:   uploaderUUID,
	})
	if err != nil {
		// Bytes are already stored; keep the object (harmless orphan) and
		// report the failure — the client can retry, matching UploadFile.
		slog.Error("workspace file index failed", "workspace_id", workspaceID, "key", key, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to index file")
		return
	}

	writeJSON(w, http.StatusOK, h.workspaceFileToResponse(fileRow))
}

// ListWorkspaceFiles handles GET /api/workspace-files?limit=&offset=.
func (h *Handler) ListWorkspaceFiles(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	if workspaceID == "" {
		writeError(w, http.StatusBadRequest, "workspace context required")
		return
	}
	if _, ok := h.requireWorkspaceMember(w, r, workspaceID, "workspace not found"); !ok {
		return
	}

	limit := 200
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v > 0 {
			limit = v
		}
	}
	if limit > 500 {
		limit = 500
	}
	offset := 0
	if raw := r.URL.Query().Get("offset"); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v > 0 {
			offset = v
		}
	}

	wsUUID := parseUUID(workspaceID)
	files, err := h.Queries.ListWorkspaceFiles(r.Context(), db.ListWorkspaceFilesParams{
		WorkspaceID: wsUUID,
		Limit:       int32(limit),
		Offset:      int32(offset),
	})
	if err != nil {
		slog.Error("workspace file list failed", "workspace_id", workspaceID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list files")
		return
	}

	total, err := h.Queries.CountWorkspaceFiles(r.Context(), wsUUID)
	if err != nil {
		total = 0
	}
	totalBytes, err := h.Queries.SumWorkspaceFileBytes(r.Context(), wsUUID)
	if err != nil {
		totalBytes = 0
	}

	resp := make([]WorkspaceFileResponse, 0, len(files))
	for _, f := range files {
		resp = append(resp, h.workspaceFileToResponse(f))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"files":       resp,
		"total":       total,
		"total_bytes": totalBytes,
		"limit":       limit,
		"offset":      offset,
	})
}

// DeleteWorkspaceFile handles DELETE /api/workspace-files/{id}. Uploader or
// admin/owner only. The DB delete cascades to referencing attachment rows and
// the storage object is removed, so deletion genuinely reclaims space.
func (h *Handler) DeleteWorkspaceFile(w http.ResponseWriter, r *http.Request) {
	fileID := chi.URLParam(r, "id")
	workspaceID := h.resolveWorkspaceID(r)
	if workspaceID == "" || fileID == "" {
		writeError(w, http.StatusBadRequest, "workspace context and file id required")
		return
	}
	member, ok := h.requireWorkspaceMember(w, r, workspaceID, "workspace not found")
	if !ok {
		return
	}

	file, err := h.Queries.GetWorkspaceFile(r.Context(), db.GetWorkspaceFileParams{
		ID:          parseUUID(fileID),
		WorkspaceID: parseUUID(workspaceID),
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "file not found")
		return
	}

	// DeleteAttachment's rule: the uploader themselves or a workspace admin.
	isPrivileged := member.Role == "admin" || member.Role == "owner"
	userID, userIDOK := requireUserID(w, r)
	if !userIDOK {
		return
	}
	actorType, actorID := h.resolveActor(r, userID, workspaceID)
	if !isPrivileged && !(file.UploaderType == actorType && uuidToString(file.UploaderID) == actorID) {
		writeError(w, http.StatusForbidden, "only the uploader or a workspace admin can delete this file")
		return
	}

	if _, err := h.Queries.DeleteWorkspaceFile(r.Context(), db.DeleteWorkspaceFileParams{
		ID:          file.ID,
		WorkspaceID: file.WorkspaceID,
	}); err != nil {
		slog.Error("workspace file delete failed", "id", fileID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to delete file")
		return
	}

	h.deleteS3Object(r.Context(), file.Url)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// DownloadWorkspaceFile handles GET /api/workspace-files/{id}/download with
// the same CloudFront/presign/proxy mode resolution as attachment downloads.
func (h *Handler) DownloadWorkspaceFile(w http.ResponseWriter, r *http.Request) {
	fileID := chi.URLParam(r, "id")
	workspaceID := h.resolveWorkspaceID(r)
	if workspaceID == "" || fileID == "" {
		writeError(w, http.StatusBadRequest, "workspace context and file id required")
		return
	}
	if _, ok := h.requireWorkspaceMember(w, r, workspaceID, "workspace not found"); !ok {
		return
	}
	if h.Storage == nil {
		writeError(w, http.StatusServiceUnavailable, "storage not configured")
		return
	}

	file, err := h.Queries.GetWorkspaceFile(r.Context(), db.GetWorkspaceFileParams{
		ID:          parseUUID(fileID),
		WorkspaceID: parseUUID(workspaceID),
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "file not found")
		return
	}

	// Reuse the attachment download machinery by viewing the library file as
	// an attachment row; only id/filename/url/content_type matter for the
	// streaming modes below.
	att := db.Attachment{
		ID:          file.ID,
		WorkspaceID: file.WorkspaceID,
		Filename:    file.Filename,
		Url:         file.Url,
		ContentType: file.ContentType,
	}
	key := h.Storage.KeyFromURL(file.Url)
	switch h.resolveAttachmentDownloadMode(file.Url) {
	case attachmentDownloadModeCloudFront:
		if h.CFSigner == nil {
			writeError(w, http.StatusInternalServerError, "cloudfront attachment downloads are not configured")
			return
		}
		h.setAttachmentPreviewSecurityHeaders(w)
		http.Redirect(
			w,
			r,
			h.CFSigner.SignedURLWithContentDisposition(
				file.Url,
				storage.AttachmentContentDisposition(file.Filename),
				time.Now().Add(h.attachmentDownloadURLTTL()),
			),
			http.StatusFound,
		)
	case attachmentDownloadModePresign:
		presigner, key, ok := h.downloadPresignerForURL(file.Url)
		if !ok {
			writeError(w, http.StatusInternalServerError, "attachment storage does not support presigned downloads")
			return
		}
		signedURL, err := presigner.PresignGetWithContentDisposition(
			r.Context(),
			key,
			h.attachmentDownloadURLTTL(),
			storage.AttachmentContentDisposition(file.Filename),
		)
		if err != nil {
			slog.Error("failed to presign workspace file download", "id", fileID, "key", key, "error", err)
			writeError(w, http.StatusBadGateway, "failed to create download URL")
			return
		}
		h.setAttachmentPreviewSecurityHeaders(w)
		http.Redirect(w, r, signedURL, http.StatusFound)
	default:
		h.proxyAttachmentDownload(w, r, att, key, true)
	}
}

// AttachWorkspaceFile handles POST /api/workspace-files/{id}/attach: create an
// unbound attachment row that references the library file. The composer treats
// the returned attachment like a fresh upload and binds it through the normal
// attachment_ids path on send — no duplicate bytes are stored.
func (h *Handler) AttachWorkspaceFile(w http.ResponseWriter, r *http.Request) {
	fileID := chi.URLParam(r, "id")
	workspaceID := h.resolveWorkspaceID(r)
	if workspaceID == "" || fileID == "" {
		writeError(w, http.StatusBadRequest, "workspace context and file id required")
		return
	}

	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	if _, ok := h.requireWorkspaceMember(w, r, workspaceID, "workspace not found"); !ok {
		return
	}

	file, err := h.Queries.GetWorkspaceFile(r.Context(), db.GetWorkspaceFileParams{
		ID:          parseUUID(fileID),
		WorkspaceID: parseUUID(workspaceID),
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "file not found")
		return
	}

	uploaderType, uploaderID := h.resolveActor(r, userID, workspaceID)
	id, err := uuid.NewV7()
	if err != nil {
		slog.Error("failed to generate uuid", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	att, err := h.Queries.CreateAttachmentFromWorkspaceFile(r.Context(), db.CreateAttachmentFromWorkspaceFileParams{
		ID:              pgtype.UUID{Bytes: id, Valid: true},
		WorkspaceID:     parseUUID(workspaceID),
		WorkspaceFileID: file.ID,
		Sha256:          file.Sha256,
		UploaderType:    uploaderType,
		UploaderID:      parseUUID(uploaderID),
		Filename:        file.Filename,
		Url:             file.Url,
		ContentType:     file.ContentType,
		SizeBytes:       file.SizeBytes,
	})
	if err != nil {
		slog.Error("workspace file attach failed", "file_id", fileID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to attach file")
		return
	}

	writeJSON(w, http.StatusOK, h.attachmentToResponse(att.Attachment(), attachmentURLModeFromRequest(r)))
}

// ServeTaskWorkspaceFile handles the daemon-only
// GET /api/daemon/runtimes/{runtimeId}/tasks/{taskId}/files/{attachmentId}/content:
// stream the library bytes behind an attachment delivered on a claim so the
// runtime can materialize and sha256-verify them before the agent starts.
func (h *Handler) ServeTaskWorkspaceFile(w http.ResponseWriter, r *http.Request) {
	if h.Storage == nil {
		writeError(w, http.StatusServiceUnavailable, "storage not configured")
		return
	}
	_, workspaceID, ok := h.requireDaemonTaskAccessWithWorkspace(w, r, chi.URLParam(r, "taskId"))
	if !ok {
		return
	}
	attachmentID := chi.URLParam(r, "attachmentId")
	if attachmentID == "" {
		writeError(w, http.StatusBadRequest, "attachment id required")
		return
	}

	att, err := h.Queries.GetAttachmentByIDOnly(r.Context(), parseUUID(attachmentID))
	if err != nil {
		writeError(w, http.StatusNotFound, "attachment not found")
		return
	}
	// The claim is scoped to the task's workspace; a foreign attachment id
	// must be indistinguishable from a missing one (no IDOR oracle).
	if uuidToString(att.WorkspaceID) != workspaceID {
		writeError(w, http.StatusNotFound, "attachment not found")
		return
	}
	if !att.WorkspaceFileID.Valid {
		// Only library-backed attachments are served here; regular uploads go
		// through the member-facing download path via the CLI fallback.
		writeError(w, http.StatusNotFound, "attachment is not backed by the workspace file library")
		return
	}

	key := h.Storage.KeyFromURL(att.Url)
	h.proxyAttachmentDownload(w, r, att, key, false)
}
