package handler

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// buildSubissueSourcePacket is intentionally best-effort at the preview
// boundary. The raw source comment is already available in content; when an
// anchor exists we additionally capture the whole permission-checked thread
// so creation and the child runtime retain the surrounding discussion.
func (h *Handler) buildSubissueSourcePacket(
	r *http.Request,
	issue db.Issue,
	req SuggestSubIssuesRequest,
	content, prefix string,
) *service.SubissueSourcePacket {
	anchorID := h.resolveSubissueSourceAnchor(r, issue, req)
	var proposalSnapshot []byte
	if req.ProposalID != nil && strings.TrimSpace(*req.ProposalID) != "" {
		if proposal, ok := h.loadProposalForIssueSilently(r, issue, *req.ProposalID); ok {
			proposalSnapshot = append([]byte(nil), proposal.Snapshot...)
			if !anchorID.Valid && proposal.AnswerCommentID.Valid {
				anchorID = proposal.AnswerCommentID
			}
			if !anchorID.Valid && proposal.QuestionCommentID.Valid {
				anchorID = proposal.QuestionCommentID
			}
		}
	}
	if anchorID.Valid {
		if build, err := service.BuildSourceContext(r.Context(), h.Queries, issue.WorkspaceID, anchorID); err == nil {
			packet := service.NewSubissueSourcePacket(build.Snapshot, content, uuidToString(anchorID), proposalSnapshot)
			if packet.SourceIssue.Identifier == "" {
				packet.SourceIssue.Identifier = prefix + "-" + strconv.Itoa(int(issue.Number))
			}
			return packet
		}
	}
	packet := service.NewSubissueSourcePacketFromContent(service.SubissueSuggestSourceIssue{
		Identifier:  prefix + "-" + strconv.Itoa(int(issue.Number)),
		Title:       issue.Title,
		Description: issue.Description.String,
	}, content, uuidToString(anchorID))
	packet.SourceIssue.ID = uuidToString(issue.ID)
	packet.SourceIssue.Identifier = prefix + "-" + strconv.Itoa(int(issue.Number))
	packet.ProposalSnapshot = proposalSnapshot
	if anchorID.Valid {
		packet.CoverageNote = "完整评论线程快照暂不可用；以下 agent 原文来自当前请求，创建后的 description 仍会保留它。"
	}
	return packet
}

func (h *Handler) resolveSubissueSourceAnchor(r *http.Request, issue db.Issue, req SuggestSubIssuesRequest) pgtype.UUID {
	if req.CommentID != nil && strings.TrimSpace(*req.CommentID) != "" {
		id, parseErr := util.ParseUUID(strings.TrimSpace(*req.CommentID))
		if parseErr == nil {
			if comment, commentErr := h.Queries.GetCommentInWorkspace(r.Context(), db.GetCommentInWorkspaceParams{
				ID: id, WorkspaceID: issue.WorkspaceID,
			}); commentErr == nil && comment.IssueID == issue.ID {
				return id
			}
		}
	}
	if req.ProposalID != nil && strings.TrimSpace(*req.ProposalID) != "" {
		if proposal, ok := h.loadProposalForIssueSilently(r, issue, *req.ProposalID); ok {
			if proposal.AnswerCommentID.Valid {
				return proposal.AnswerCommentID
			}
			if proposal.QuestionCommentID.Valid {
				return proposal.QuestionCommentID
			}
		}
	}
	return pgtype.UUID{}
}
