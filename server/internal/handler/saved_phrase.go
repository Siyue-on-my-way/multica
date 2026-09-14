package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
)

const (
	savedPhraseMaxNameChars     = 80
	savedPhraseMaxContentChars  = 8000
	savedPhraseMaxCategoryChars = 40
)

type SavedPhraseResponse struct {
	ID          string  `json:"id"`
	WorkspaceID string  `json:"workspace_id"`
	CreatorID   string  `json:"creator_id"`
	Name        string  `json:"name"`
	Content     string  `json:"content"`
	Category    string  `json:"category"`
	Scope       string  `json:"scope"`
	Pinned      bool    `json:"pinned"`
	UseCount    int64   `json:"use_count"`
	LastUsedAt  *string `json:"last_used_at,omitempty"`
	Status      string  `json:"status"`
	CreatedAt   string  `json:"created_at"`
	UpdatedAt   string  `json:"updated_at"`
}

type SavedPhraseListResponse struct {
	Phrases []SavedPhraseResponse `json:"phrases"`
}

type CreateSavedPhraseRequest struct {
	Name     string `json:"name"`
	Content  string `json:"content"`
	Category string `json:"category"`
	Scope    string `json:"scope"`
	Pinned   bool   `json:"pinned"`
}

type UpdateSavedPhraseRequest struct {
	Name     *string `json:"name"`
	Content  *string `json:"content"`
	Category *string `json:"category"`
	Scope    *string `json:"scope"`
	Pinned   *bool   `json:"pinned"`
}

func savedPhraseToResponse(row db.SavedPhrase) SavedPhraseResponse {
	return SavedPhraseResponse{
		ID:          uuidToString(row.ID),
		WorkspaceID: uuidToString(row.WorkspaceID),
		CreatorID:   uuidToString(row.CreatorID),
		Name:        row.Name,
		Content:     row.Content,
		Category:    row.Category,
		Scope:       row.Scope,
		Pinned:      row.Pinned,
		UseCount:    row.UseCount,
		LastUsedAt:  timestampToPtr(row.LastUsedAt),
		Status:      row.Status,
		CreatedAt:   timestampToString(row.CreatedAt),
		UpdatedAt:   timestampToString(row.UpdatedAt),
	}
}

// savedPhraseActor resolves membership for both middleware-backed requests and
// direct handler tests.
func (h *Handler) savedPhraseActor(w http.ResponseWriter, r *http.Request) (db.Member, pgtype.UUID, bool) {
	workspaceID := workspaceIDFromURL(r, "workspaceId")
	if workspaceID == "" {
		workspaceID = h.resolveWorkspaceID(r)
	}
	workspaceUUID, err := util.ParseUUID(strings.TrimSpace(workspaceID))
	if err != nil {
		writeError(w, http.StatusBadRequest, "workspace_id is required")
		return db.Member{}, pgtype.UUID{}, false
	}
	member, ok := h.workspaceMember(w, r, workspaceID)
	if !ok {
		return db.Member{}, pgtype.UUID{}, false
	}
	return member, workspaceUUID, true
}

func normalizeSavedPhraseText(raw, field string, max int) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", fmt.Errorf("%s is required", field)
	}
	if utf8.RuneCountInString(value) > max {
		return "", fmt.Errorf("%s must be at most %d characters", field, max)
	}
	return value, nil
}

func normalizeSavedPhraseCategory(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "general", nil
	}
	return normalizeSavedPhraseText(value, "category", savedPhraseMaxCategoryChars)
}

func normalizeSavedPhraseScope(raw string) (string, error) {
	scope := strings.TrimSpace(raw)
	if scope == "" {
		return "public", nil
	}
	if scope != "public" && scope != "workspace" {
		return "", errors.New("scope must be public or workspace")
	}
	return scope, nil
}

func (h *Handler) ListSavedPhrases(w http.ResponseWriter, r *http.Request) {
	member, workspaceID, ok := h.savedPhraseActor(w, r)
	if !ok {
		return
	}
	rows, err := h.Queries.ListSavedPhrases(r.Context(), db.ListSavedPhrasesParams{
		WorkspaceID: workspaceID,
		CreatorID:   member.UserID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list saved phrases")
		return
	}
	query := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	category := strings.TrimSpace(r.URL.Query().Get("category"))
	phrases := make([]SavedPhraseResponse, 0, len(rows))
	for _, row := range rows {
		if category != "" && row.Category != category {
			continue
		}
		if query != "" && !strings.Contains(strings.ToLower(row.Name), query) && !strings.Contains(strings.ToLower(row.Content), query) {
			continue
		}
		phrases = append(phrases, savedPhraseToResponse(row))
	}
	writeJSON(w, http.StatusOK, SavedPhraseListResponse{Phrases: phrases})
}

func (h *Handler) CreateSavedPhrase(w http.ResponseWriter, r *http.Request) {
	member, workspaceID, ok := h.savedPhraseActor(w, r)
	if !ok {
		return
	}
	var req CreateSavedPhraseRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	name, err := normalizeSavedPhraseText(req.Name, "name", savedPhraseMaxNameChars)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	content, err := normalizeSavedPhraseText(req.Content, "content", savedPhraseMaxContentChars)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	category, err := normalizeSavedPhraseCategory(req.Category)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	scope, err := normalizeSavedPhraseScope(req.Scope)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	row, err := h.Queries.CreateSavedPhrase(r.Context(), db.CreateSavedPhraseParams{
		ID: dbid.NewV7(), WorkspaceID: workspaceID, CreatorID: member.UserID,
		Name: name, Content: content, Category: category, Scope: scope, Pinned: req.Pinned,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create saved phrase")
		return
	}
	writeJSON(w, http.StatusCreated, savedPhraseToResponse(row))
}

func (h *Handler) UpdateSavedPhrase(w http.ResponseWriter, r *http.Request) {
	member, workspaceID, ok := h.savedPhraseActor(w, r)
	if !ok {
		return
	}
	id, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "id")
	if !ok {
		return
	}
	existing, err := h.Queries.GetSavedPhrase(r.Context(), db.GetSavedPhraseParams{WorkspaceID: workspaceID, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "saved phrase not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load saved phrase")
		return
	}
	if existing.CreatorID != member.UserID && member.Role != "owner" && member.Role != "admin" {
		writeError(w, http.StatusForbidden, "only the phrase creator or a workspace admin may edit it")
		return
	}
	var req UpdateSavedPhraseRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	params := db.UpdateSavedPhraseParams{ID: id, WorkspaceID: workspaceID}
	if req.Name != nil {
		value, normalizeErr := normalizeSavedPhraseText(*req.Name, "name", savedPhraseMaxNameChars)
		if normalizeErr != nil {
			writeError(w, http.StatusBadRequest, normalizeErr.Error())
			return
		}
		params.Name = pgtype.Text{String: value, Valid: true}
	}
	if req.Content != nil {
		value, normalizeErr := normalizeSavedPhraseText(*req.Content, "content", savedPhraseMaxContentChars)
		if normalizeErr != nil {
			writeError(w, http.StatusBadRequest, normalizeErr.Error())
			return
		}
		params.Content = pgtype.Text{String: value, Valid: true}
	}
	if req.Category != nil {
		value, normalizeErr := normalizeSavedPhraseCategory(*req.Category)
		if normalizeErr != nil {
			writeError(w, http.StatusBadRequest, normalizeErr.Error())
			return
		}
		params.Category = pgtype.Text{String: value, Valid: true}
	}
	if req.Scope != nil {
		value, normalizeErr := normalizeSavedPhraseScope(*req.Scope)
		if normalizeErr != nil {
			writeError(w, http.StatusBadRequest, normalizeErr.Error())
			return
		}
		params.Scope = pgtype.Text{String: value, Valid: true}
	}
	if req.Pinned != nil {
		params.Pinned = pgtype.Bool{Bool: *req.Pinned, Valid: true}
	}
	row, err := h.Queries.UpdateSavedPhrase(r.Context(), params)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "saved phrase not found or archived")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update saved phrase")
		return
	}
	writeJSON(w, http.StatusOK, savedPhraseToResponse(row))
}

func (h *Handler) ArchiveSavedPhrase(w http.ResponseWriter, r *http.Request) {
	member, workspaceID, ok := h.savedPhraseActor(w, r)
	if !ok {
		return
	}
	id, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "id")
	if !ok {
		return
	}
	existing, err := h.Queries.GetSavedPhrase(r.Context(), db.GetSavedPhraseParams{WorkspaceID: workspaceID, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "saved phrase not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load saved phrase")
		return
	}
	if existing.CreatorID != member.UserID && member.Role != "owner" && member.Role != "admin" {
		writeError(w, http.StatusForbidden, "only the phrase creator or a workspace admin may archive it")
		return
	}
	_, err = h.Queries.ArchiveSavedPhrase(r.Context(), db.ArchiveSavedPhraseParams{WorkspaceID: workspaceID, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "saved phrase not found or already archived")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to archive saved phrase")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) UseSavedPhrase(w http.ResponseWriter, r *http.Request) {
	_, workspaceID, ok := h.savedPhraseActor(w, r)
	if !ok {
		return
	}
	id, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "id")
	if !ok {
		return
	}
	row, err := h.Queries.UseSavedPhrase(r.Context(), db.UseSavedPhraseParams{WorkspaceID: workspaceID, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "saved phrase not found or archived")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record saved phrase use")
		return
	}
	writeJSON(w, http.StatusOK, savedPhraseToResponse(row))
}
