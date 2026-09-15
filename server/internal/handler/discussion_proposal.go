package handler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
)

type DiscussionProposalResponse struct {
	ID                string                       `json:"id"`
	WorkspaceID       string                       `json:"workspace_id"`
	SourceIssueID     string                       `json:"source_issue_id"`
	QuestionCommentID *string                      `json:"question_comment_id,omitempty"`
	AnswerCommentID   *string                      `json:"answer_comment_id,omitempty"`
	Snapshot          json.RawMessage              `json:"snapshot"`
	Generations       []ProposalGenerationResponse `json:"generations"`
	CreatedAt         string                       `json:"created_at"`
	UpdatedAt         string                       `json:"updated_at"`
}

type ProposalGenerationResponse struct {
	ID             string                  `json:"id"`
	ProposalID     string                  `json:"proposal_id"`
	Strategy       string                  `json:"strategy"`
	CreateMode     string                  `json:"create_mode"`
	IdempotencyKey *string                 `json:"idempotency_key,omitempty"`
	Snapshot       json.RawMessage         `json:"snapshot"`
	Issues         []ProposalChildResponse `json:"issues"`
	CreatedAt      string                  `json:"created_at"`
}

type ProposalChildResponse struct {
	ID         string `json:"id"`
	Identifier string `json:"identifier"`
	Title      string `json:"title"`
	Status     string `json:"status"`
	Stage      *int32 `json:"stage,omitempty"`
	ItemID     string `json:"item_id,omitempty"`
}

type CreateDiscussionProposalRequest struct {
	QuestionCommentID *string         `json:"question_comment_id"`
	AnswerCommentID   *string         `json:"answer_comment_id"`
	Snapshot          json.RawMessage `json:"snapshot"`
	Title             string          `json:"title"`
	Summary           string          `json:"summary"`
	Content           string          `json:"content"`
}

type ProposalGenerationItemRequest struct {
	ID                string          `json:"id"`
	Title             string          `json:"title"`
	Goal              string          `json:"goal"`
	Description       string          `json:"description"`
	Stage             *int32          `json:"stage"`
	ParentIssueID     *string         `json:"parent_issue_id"`
	SuggestedParentID *string         `json:"suggested_parent_issue_id"`
	AssigneeType      string          `json:"assignee_type"`
	AssigneeID        *string         `json:"assignee_id"`
	ExecutionContext  json.RawMessage `json:"execution_context"`
}

type CreateProposalGenerationRequest struct {
	Strategy       string                          `json:"strategy"`
	CreateMode     string                          `json:"create_mode"`
	IdempotencyKey string                          `json:"idempotency_key"`
	Items          []ProposalGenerationItemRequest `json:"items"`
}

func discussionProposalToResponse(h *Handler, r *http.Request, row db.DiscussionProposal) (DiscussionProposalResponse, error) {
	response := DiscussionProposalResponse{
		ID:                uuidToString(row.ID),
		WorkspaceID:       uuidToString(row.WorkspaceID),
		SourceIssueID:     uuidToString(row.SourceIssueID),
		QuestionCommentID: uuidToPtr(row.QuestionCommentID),
		AnswerCommentID:   uuidToPtr(row.AnswerCommentID),
		Snapshot:          json.RawMessage(row.Snapshot),
		Generations:       []ProposalGenerationResponse{},
		CreatedAt:         timestampToString(row.CreatedAt),
		UpdatedAt:         timestampToString(row.UpdatedAt),
	}
	generations, err := h.Queries.ListProposalGenerations(r.Context(), db.ListProposalGenerationsParams{
		WorkspaceID: row.WorkspaceID, ProposalID: row.ID,
	})
	if err != nil {
		return DiscussionProposalResponse{}, err
	}
	prefix := h.getIssuePrefix(r.Context(), row.WorkspaceID)
	for _, generation := range generations {
		generationResponse := ProposalGenerationResponse{
			ID: uuidToString(generation.ID), ProposalID: uuidToString(generation.ProposalID),
			Strategy: generation.Strategy, CreateMode: generation.CreateMode,
			IdempotencyKey: textToPtr(generation.IdempotencyKey), Snapshot: json.RawMessage(generation.Snapshot),
			Issues: []ProposalChildResponse{}, CreatedAt: timestampToString(generation.CreatedAt),
		}
		links, linkErr := h.Queries.ListProposalGenerationIssues(r.Context(), db.ListProposalGenerationIssuesParams{
			WorkspaceID: row.WorkspaceID, GenerationID: generation.ID,
		})
		if linkErr != nil {
			return DiscussionProposalResponse{}, linkErr
		}
		for _, link := range links {
			issue, issueErr := h.Queries.GetIssueInWorkspace(r.Context(), db.GetIssueInWorkspaceParams{ID: link.IssueID, WorkspaceID: row.WorkspaceID})
			if errors.Is(issueErr, pgx.ErrNoRows) {
				continue
			}
			if issueErr != nil {
				return DiscussionProposalResponse{}, issueErr
			}
			child := ProposalChildResponse{ID: uuidToString(issue.ID), Identifier: prefix + "-" + fmt.Sprint(issue.Number), Title: issue.Title, Status: issue.Status, ItemID: link.ItemID}
			if issue.Stage.Valid {
				stage := issue.Stage.Int32
				child.Stage = &stage
			}
			generationResponse.Issues = append(generationResponse.Issues, child)
		}
		response.Generations = append(response.Generations, generationResponse)
	}
	return response, nil
}

func proposalSnapshotOrFallback(raw json.RawMessage, title, summary, content string) ([]byte, error) {
	if len(raw) > 0 && string(raw) != "null" {
		if !json.Valid(raw) {
			return nil, errors.New("snapshot must be valid JSON")
		}
		var object map[string]any
		if err := json.Unmarshal(raw, &object); err != nil || object == nil {
			return nil, errors.New("snapshot must be a JSON object")
		}
		if err := normalizeProposalSnapshotObject(object); err != nil {
			return nil, err
		}
		return json.Marshal(object)
	}
	text := strings.TrimSpace(content)
	if text == "" {
		text = strings.TrimSpace(summary)
	}
	if text == "" {
		text = strings.TrimSpace(title)
	}
	if text == "" {
		return nil, errors.New("proposal snapshot or content is required")
	}
	return json.Marshal(map[string]any{
		"title":              strings.TrimSpace(title),
		"summary":            text,
		"scope":              "manual",
		"risks":              []string{},
		"acceptanceCriteria": []string{},
		"items":              []any{},
	})
}

func normalizeProposalSnapshotObject(object map[string]any) error {
	if _, ok := object["title"].(string); !ok {
		object["title"] = "讨论方案"
	}
	if _, ok := object["summary"].(string); !ok {
		object["summary"] = ""
	}
	if _, ok := object["scope"].(string); !ok {
		object["scope"] = "manual"
	}
	if _, ok := object["risks"]; !ok {
		object["risks"] = []string{}
	}
	if _, ok := object["acceptanceCriteria"]; !ok {
		if value, exists := object["acceptance_criteria"]; exists {
			object["acceptanceCriteria"] = value
		} else {
			object["acceptanceCriteria"] = []string{}
		}
	}
	if items, ok := object["items"]; !ok {
		object["items"] = []any{}
	} else if _, ok := items.([]any); !ok {
		return errors.New("snapshot items must be an array")
	}
	return nil
}

const proposalConversionSystemPrompt = `你负责把一条已经写好的讨论回复整理成可审阅的方案卡片。
只输出一个 JSON 对象，不要 Markdown、解释文字或代码围栏。对象必须包含：
title（简短标题）、summary（方案摘要）、scope（范围）、risks（字符串数组）、
acceptanceCriteria（验收标准字符串数组）以及 items（方案条目数组）。
每个 items 条目包含 id、title、goal、description 和可选 stage；只根据原文归纳，
不要臆造原文没有的执行承诺。`

func (h *Handler) maybeGenerateProposalSnapshot(ctx context.Context, content string, fallback []byte) []byte {
	content = strings.TrimSpace(content)
	if content == "" || h == nil || h.LLM == nil || !h.LLM.Enabled() {
		return fallback
	}
	prompt := "请整理以下讨论回复：\n\n" + content
	raw, err := h.LLM.GenerateJSON(ctx, "", proposalConversionSystemPrompt, prompt, 0, 1536)
	if err != nil {
		slog.Warn("proposal conversion LLM failed; using content snapshot", "error", err)
		return fallback
	}
	snapshot, err := proposalSnapshotOrFallback(json.RawMessage(raw), "", "", content)
	if err != nil {
		slog.Warn("proposal conversion LLM returned invalid JSON; using content snapshot", "error", err)
		return fallback
	}
	return snapshot
}

func (h *Handler) ensureDiscussionProposal(r *http.Request, issue db.Issue, questionID, answerID pgtype.UUID, snapshot []byte) (db.DiscussionProposal, bool, error) {
	if questionID.Valid {
		existing, err := h.Queries.GetDiscussionProposalByQuestion(r.Context(), db.GetDiscussionProposalByQuestionParams{
			WorkspaceID: issue.WorkspaceID, SourceIssueID: issue.ID, QuestionCommentID: questionID,
		})
		if err == nil {
			return existing, true, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return db.DiscussionProposal{}, false, err
		}
	}
	row, err := h.Queries.CreateDiscussionProposal(r.Context(), db.CreateDiscussionProposalParams{
		ID: dbid.NewV7(), WorkspaceID: issue.WorkspaceID, SourceIssueID: issue.ID,
		QuestionCommentID: questionID, AnswerCommentID: answerID, Snapshot: snapshot,
	})
	if err != nil && questionID.Valid {
		// A concurrent conversion may have won the partial unique index. Read it
		// back and make the action idempotent instead of exposing a 500.
		existing, readErr := h.Queries.GetDiscussionProposalByQuestion(r.Context(), db.GetDiscussionProposalByQuestionParams{
			WorkspaceID: issue.WorkspaceID, SourceIssueID: issue.ID, QuestionCommentID: questionID,
		})
		if readErr == nil {
			return existing, true, nil
		}
	}
	if err != nil {
		return db.DiscussionProposal{}, false, err
	}
	return row, false, nil
}

func (h *Handler) CreateDiscussionProposal(w http.ResponseWriter, r *http.Request) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	var req CreateDiscussionProposalRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	questionID, ok := h.parseOptionalProposalComment(w, r, issue, req.QuestionCommentID)
	if !ok {
		return
	}
	answerID, ok := h.parseOptionalProposalComment(w, r, issue, req.AnswerCommentID)
	if !ok {
		return
	}
	snapshot, err := proposalSnapshotOrFallback(req.Snapshot, req.Title, req.Summary, req.Content)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(req.Snapshot) == 0 || string(req.Snapshot) == "null" {
		snapshot = h.maybeGenerateProposalSnapshot(r.Context(), req.Content, snapshot)
	}
	row, reused, err := h.ensureDiscussionProposal(r, issue, questionID, answerID, snapshot)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save proposal")
		return
	}
	response, err := discussionProposalToResponse(h, r, row)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load proposal")
		return
	}
	if reused {
		writeJSON(w, http.StatusOK, response)
		return
	}
	writeJSON(w, http.StatusCreated, response)
}

func (h *Handler) parseOptionalProposalComment(w http.ResponseWriter, r *http.Request, issue db.Issue, raw *string) (pgtype.UUID, bool) {
	if raw == nil || strings.TrimSpace(*raw) == "" {
		return pgtype.UUID{}, true
	}
	id, ok := parseUUIDOrBadRequest(w, *raw, "comment_id")
	if !ok {
		return pgtype.UUID{}, false
	}
	comment, err := h.Queries.GetCommentInWorkspace(r.Context(), db.GetCommentInWorkspaceParams{ID: id, WorkspaceID: issue.WorkspaceID})
	if err != nil || comment.IssueID != issue.ID {
		writeError(w, http.StatusBadRequest, "comment does not belong to this issue")
		return pgtype.UUID{}, false
	}
	return id, true
}

// loadProposalForIssue resolves an optional proposal lineage token while
// keeping the proposal workspace- and issue-scoped. The issue permission check
// happens at the caller, so a proposal from another issue is intentionally
// reported as not found rather than leaking its existence.
func (h *Handler) loadProposalForIssue(w http.ResponseWriter, r *http.Request, issue db.Issue, raw string) (db.DiscussionProposal, bool) {
	id, ok := parseUUIDOrBadRequest(w, strings.TrimSpace(raw), "proposal_id")
	if !ok {
		return db.DiscussionProposal{}, false
	}
	proposal, err := h.Queries.GetDiscussionProposal(r.Context(), db.GetDiscussionProposalParams{
		WorkspaceID: issue.WorkspaceID,
		ID:          id,
	})
	if err != nil || proposal.SourceIssueID != issue.ID {
		writeError(w, http.StatusNotFound, "proposal not found")
		return db.DiscussionProposal{}, false
	}
	return proposal, true
}

func (h *Handler) ListDiscussionProposals(w http.ResponseWriter, r *http.Request) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	rows, err := h.Queries.ListDiscussionProposalsForIssue(r.Context(), db.ListDiscussionProposalsForIssueParams{
		WorkspaceID: issue.WorkspaceID, SourceIssueID: issue.ID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list proposals")
		return
	}
	responses := make([]DiscussionProposalResponse, 0, len(rows))
	for _, row := range rows {
		response, responseErr := discussionProposalToResponse(h, r, row)
		if responseErr != nil {
			writeError(w, http.StatusInternalServerError, "failed to load proposal")
			return
		}
		responses = append(responses, response)
	}
	writeJSON(w, http.StatusOK, responses)
}

func (h *Handler) GetDiscussionProposal(w http.ResponseWriter, r *http.Request) {
	_, workspaceID, ok := h.savedPhraseActor(w, r)
	if !ok {
		return
	}
	id, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "id")
	if !ok {
		return
	}
	row, err := h.Queries.GetDiscussionProposal(r.Context(), db.GetDiscussionProposalParams{WorkspaceID: workspaceID, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "proposal not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load proposal")
		return
	}
	response, err := discussionProposalToResponse(h, r, row)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load proposal")
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *Handler) CreateCommentProposal(w http.ResponseWriter, r *http.Request) {
	commentID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "commentId"), "comment_id")
	if !ok {
		return
	}
	workspaceID, parseErr := util.ParseUUID(strings.TrimSpace(h.resolveWorkspaceID(r)))
	if parseErr != nil {
		writeError(w, http.StatusBadRequest, "workspace id is required")
		return
	}
	comment, err := h.Queries.GetCommentInWorkspace(r.Context(), db.GetCommentInWorkspaceParams{ID: commentID, WorkspaceID: workspaceID})
	if err != nil {
		writeError(w, http.StatusNotFound, "comment not found")
		return
	}
	issue, ok := h.loadIssueForUser(w, r, uuidToString(comment.IssueID))
	if !ok {
		return
	}
	var req CreateDiscussionProposalRequest
	if decodeErr := json.NewDecoder(r.Body).Decode(&req); decodeErr != nil && !errors.Is(decodeErr, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	questionID := commentID
	if comment.ParentID.Valid {
		questionID = comment.ParentID
	}
	snapshot, err := proposalSnapshotOrFallback(req.Snapshot, req.Title, req.Summary, comment.Content)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(req.Snapshot) == 0 || string(req.Snapshot) == "null" {
		snapshot = h.maybeGenerateProposalSnapshot(r.Context(), comment.Content, snapshot)
	}
	row, reused, err := h.ensureDiscussionProposal(r, issue, questionID, commentID, snapshot)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save proposal")
		return
	}
	response, err := discussionProposalToResponse(h, r, row)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load proposal")
		return
	}
	if reused {
		writeJSON(w, http.StatusOK, response)
		return
	}
	writeJSON(w, http.StatusCreated, response)
}

func (h *Handler) CreateProposalGeneration(w http.ResponseWriter, r *http.Request) {
	if h.IssueService == nil {
		writeError(w, http.StatusServiceUnavailable, "proposal generation is unavailable")
		return
	}
	_, workspaceID, ok := h.savedPhraseActor(w, r)
	if !ok {
		return
	}
	proposalID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "proposal_id")
	if !ok {
		return
	}
	proposal, err := h.Queries.GetDiscussionProposal(r.Context(), db.GetDiscussionProposalParams{WorkspaceID: workspaceID, ID: proposalID})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "proposal not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load proposal")
		return
	}
	creatorID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	creatorUUID, err := util.ParseUUID(creatorID)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid user identity")
		return
	}
	var req CreateProposalGenerationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.CreateMode == "" {
		req.CreateMode = "backlog"
	}
	if req.CreateMode != "backlog" && req.CreateMode != "todo" {
		writeError(w, http.StatusBadRequest, "create_mode must be backlog or todo")
		return
	}
	sourceIssue, err := h.Queries.GetIssueInWorkspace(r.Context(), db.GetIssueInWorkspaceParams{ID: proposal.SourceIssueID, WorkspaceID: workspaceID})
	if err != nil {
		writeError(w, http.StatusNotFound, "proposal source issue not found")
		return
	}
	items := req.Items
	if len(items) == 0 {
		var snapshot struct {
			Items []ProposalGenerationItemRequest `json:"items"`
		}
		if json.Unmarshal(proposal.Snapshot, &snapshot) == nil {
			items = snapshot.Items
		}
	}
	if len(items) == 0 {
		writeError(w, http.StatusBadRequest, "at least one proposal item is required")
		return
	}
	if len(items) > service.SubissuePlanItemMaxCount {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("proposal contains too many items; maximum is %d", service.SubissuePlanItemMaxCount))
		return
	}
	contextSnapshot := []byte(nil)
	anchorID := pgtype.UUID{}
	anchorCandidate := proposal.QuestionCommentID
	if !anchorCandidate.Valid {
		anchorCandidate = proposal.AnswerCommentID
	}
	if anchorCandidate.Valid {
		build, buildErr := service.BuildSourceContext(r.Context(), h.Queries, workspaceID, anchorCandidate)
		if buildErr == nil {
			contextSnapshot, _ = json.Marshal(build.Snapshot)
			anchorID = anchorCandidate
		}
	}
	serviceItems := make([]service.ProposalBatchItem, 0, len(items))
	for index, item := range items {
		title := strings.TrimSpace(item.Title)
		if title == "" {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("proposal item %d title is required", index+1))
			return
		}
		parentID := proposal.SourceIssueID
		if item.ParentIssueID != nil && strings.TrimSpace(*item.ParentIssueID) != "" {
			parentID, ok = parseUUIDOrBadRequest(w, *item.ParentIssueID, "parent_issue_id")
			if !ok {
				return
			}
		} else if item.SuggestedParentID != nil && strings.TrimSpace(*item.SuggestedParentID) != "" {
			parentID, ok = parseUUIDOrBadRequest(w, *item.SuggestedParentID, "suggested_parent_issue_id")
			if !ok {
				return
			}
		}
		stage := int32(1)
		if item.Stage != nil {
			stage = *item.Stage
		}
		if stage < 1 {
			writeError(w, http.StatusBadRequest, "stage must be at least 1")
			return
		}
		var assigneeID pgtype.UUID
		assigneeType := pgtype.Text{}
		if strings.TrimSpace(item.AssigneeType) != "" || item.AssigneeID != nil {
			if item.AssigneeType != "agent" && item.AssigneeType != "squad" {
				writeError(w, http.StatusBadRequest, "assignee_type must be agent or squad")
				return
			}
			if item.AssigneeID == nil {
				writeError(w, http.StatusBadRequest, "assignee_id is required")
				return
			}
			assigneeID, ok = parseUUIDOrBadRequest(w, *item.AssigneeID, "assignee_id")
			if !ok {
				return
			}
			assigneeType = pgtype.Text{String: item.AssigneeType, Valid: true}
		}
		contextDocument, contextErr := normalizeExecutionContextCreate(item.ExecutionContext)
		if contextErr != nil {
			writeError(w, http.StatusBadRequest, "invalid execution_context: "+contextErr.Error())
			return
		}
		itemID := strings.TrimSpace(item.ID)
		if itemID == "" {
			itemID = fmt.Sprintf("item-%d", index+1)
		}
		description := strings.TrimSpace(item.Description)
		if description == "" {
			description = strings.TrimSpace(item.Goal)
		}
		serviceItems = append(serviceItems, service.ProposalBatchItem{
			ID: itemID, Title: title, Description: description,
			ParentIssueID: parentID, ProjectID: sourceIssue.ProjectID,
			AssigneeType: assigneeType, AssigneeID: assigneeID,
			Stage: pgtype.Int4{Int32: stage, Valid: true}, ExecutionContext: contextDocument,
		})
	}
	if req.IdempotencyKey == "" {
		payload, _ := json.Marshal(struct {
			Strategy string                          `json:"strategy"`
			Mode     string                          `json:"mode"`
			Items    []ProposalGenerationItemRequest `json:"items"`
		}{req.Strategy, req.CreateMode, items})
		digest := sha256.Sum256(append(proposalID.Bytes[:], payload...))
		req.IdempotencyKey = hex.EncodeToString(digest[:])
	}
	snapshot, _ := json.Marshal(map[string]any{"strategy": req.Strategy, "create_mode": req.CreateMode, "items": items})
	result, err := h.IssueService.CreateProposalBatch(r.Context(), service.ProposalBatchParams{
		WorkspaceID: workspaceID, CreatorID: creatorUUID, ProposalID: proposalID,
		Strategy: req.Strategy, CreateMode: req.CreateMode, IdempotencyKey: req.IdempotencyKey,
		ProposalSnapshot: snapshot, SourceContextSnapshot: contextSnapshot,
		SourceIssueID: proposal.SourceIssueID, AnchorCommentID: anchorID, Items: serviceItems,
	})
	if errors.Is(err, service.ErrProposalBatchAlreadyExists) {
		writeError(w, http.StatusConflict, "proposal generation already exists")
		return
	}
	if err != nil {
		if errors.Is(err, service.ErrParentIssueNotFound) || errors.Is(err, service.ErrProjectNotFound) || errors.Is(err, service.ErrProposalAssigneeNotFound) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to create proposal generation: "+err.Error())
		return
	}
	response := proposalGenerationResponseFromResult(h, r, result, workspaceID)
	status := http.StatusCreated
	if result.Reused {
		status = http.StatusOK
	}
	writeJSON(w, status, response)
}

func proposalGenerationResponseFromResult(h *Handler, r *http.Request, result service.ProposalBatchResult, workspaceID pgtype.UUID) ProposalGenerationResponse {
	response := ProposalGenerationResponse{
		ID: uuidToString(result.Generation.ID), ProposalID: uuidToString(result.Generation.ProposalID),
		Strategy: result.Generation.Strategy, CreateMode: result.Generation.CreateMode,
		IdempotencyKey: textToPtr(result.Generation.IdempotencyKey), Snapshot: json.RawMessage(result.Generation.Snapshot),
		Issues: []ProposalChildResponse{}, CreatedAt: timestampToString(result.Generation.CreatedAt),
	}
	prefix := h.getIssuePrefix(r.Context(), workspaceID)
	for index, issue := range result.Issues {
		child := ProposalChildResponse{ID: uuidToString(issue.ID), Identifier: prefix + "-" + fmt.Sprint(issue.Number), Title: issue.Title, Status: issue.Status}
		if issue.Stage.Valid {
			stage := issue.Stage.Int32
			child.Stage = &stage
		}
		if index < len(result.Links) {
			child.ItemID = result.Links[index].ItemID
		}
		response.Issues = append(response.Issues, child)
	}
	return response
}

func proposalForCommentMap(h *Handler, r *http.Request, comments []pgtype.UUID, workspaceID pgtype.UUID) (map[string]DiscussionProposalResponse, error) {
	if len(comments) == 0 {
		return map[string]DiscussionProposalResponse{}, nil
	}
	rows, err := h.Queries.ListDiscussionProposalsForComments(r.Context(), db.ListDiscussionProposalsForCommentsParams{WorkspaceID: workspaceID, CommentIds: comments})
	if err != nil {
		return nil, err
	}
	result := make(map[string]DiscussionProposalResponse, len(rows)*2)
	for _, row := range rows {
		response, responseErr := discussionProposalToResponse(h, r, row)
		if responseErr != nil {
			return nil, responseErr
		}
		if row.QuestionCommentID.Valid {
			result[uuidToString(row.QuestionCommentID)] = response
		}
		if row.AnswerCommentID.Valid {
			result[uuidToString(row.AnswerCommentID)] = response
		}
	}
	return result, nil
}
