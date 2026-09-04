package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const (
	// AncestorBriefMaxTokens bounds the background section independently from
	// the provider's completion budget. Four characters is a conservative
	// approximation for mixed Markdown and CJK text.
	AncestorBriefMaxTokens = 8192
	ancestorBriefMaxChars  = AncestorBriefMaxTokens * 4
	ancestorBriefMaxDepth  = 32
)

// AncestorBriefRef is the persisted identity snapshot for one ancestor. The
// brief text is intentionally not persisted; it is regenerated for each task.
type AncestorBriefRef struct {
	ID        string `json:"id"`
	UpdatedAt string `json:"updated_at"`
}

// AncestorBrief is bounded, source-labelled background for an issue.
type AncestorBrief struct {
	Text         string             `json:"text,omitempty"`
	Refs         []AncestorBriefRef `json:"refs,omitempty"`
	TokenCount   int                `json:"token_count,omitempty"`
	Truncated    bool               `json:"truncated,omitempty"`
	TruncatedIDs []string           `json:"truncated_ids,omitempty"`
}

// IssueReader is the query surface required to walk the parent chain.
type IssueReader interface {
	GetIssue(context.Context, pgtype.UUID) (db.Issue, error)
}

// BuildAncestorBrief walks from the current issue's parent to the root and
// includes only each ancestor's ID, title, and Markdown description.
func BuildAncestorBrief(ctx context.Context, reader IssueReader, issue db.Issue) AncestorBrief {
	brief := AncestorBrief{}
	parentID := issue.ParentIssueID
	seen := make(map[string]struct{})
	sections := make([]string, 0)

	for depth := 0; parentID.Valid && depth < ancestorBriefMaxDepth; depth++ {
		id := util.UUIDToString(parentID)
		if id == "" {
			break
		}
		if _, ok := seen[id]; ok {
			brief.Truncated = true
			brief.TruncatedIDs = append(brief.TruncatedIDs, id)
			break
		}
		seen[id] = struct{}{}

		ancestor, err := reader.GetIssue(ctx, parentID)
		if err != nil || ancestor.WorkspaceID != issue.WorkspaceID {
			break
		}
		brief.Refs = append(brief.Refs, AncestorBriefRef{
			ID:        id,
			UpdatedAt: ancestor.UpdatedAt.Time.UTC().Format("2006-01-02T15:04:05Z"),
		})

		section := fmt.Sprintf("[Background source: Issue %s]\nTitle: %s\nDescription:\n%s", id, ancestor.Title, ancestor.Description.String)
		candidate := strings.Join(append(append([]string{}, sections...), section), "\n\n")
		if len([]rune(candidate)) > ancestorBriefMaxChars {
			remaining := ancestorBriefMaxChars - len([]rune(strings.Join(sections, "\n\n"))) - 2
			if remaining > 0 {
				sections = append(sections, truncateAncestorBriefText(section, remaining))
			}
			brief.Truncated = true
			brief.TruncatedIDs = append(brief.TruncatedIDs, id)
			break
		}
		sections = append(sections, section)
		parentID = ancestor.ParentIssueID
	}
	if len(sections) == 0 {
		return brief
	}
	brief.Text = "ANCESTOR_BRIEF (background reference only; current task instructions take precedence)\n\n" + strings.Join(sections, "\n\n")
	brief.TokenCount = (len([]rune(brief.Text)) + 3) / 4
	return brief
}

func truncateAncestorBriefText(value string, max int) string {
	if max <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= max {
		return value
	}
	return string(runes[:max]) + "\n[truncated]"
}
