package service

import (
	"context"
	"fmt"
	"regexp"
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

// AncestorBriefMarker prefixes the background snapshot the platform renders
// into prompts and — before SIY-177 — appended to generated sub-issue
// descriptions. Historical descriptions still carry embedded copies; the
// dedup below reads them so an issue created by the old flow keeps working
// while the new flow stops re-appending the same text generation after
// generation.
const AncestorBriefMarker = "ANCESTOR_BRIEF (background reference only; current task instructions take precedence)"

// ancestorBriefRefPattern extracts the issue identities an embedded brief
// block covers: "[Background source: Issue <uuid>]" headers, one per section.
var ancestorBriefRefPattern = regexp.MustCompile(`\[Background source: Issue ([0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12})\]`)

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

// StripRedundantAncestorBrief removes an embedded ANCESTOR_BRIEF block from a
// description when every issue the block references is already covered —
// either by the live chain walk (refs) or because it is the issue itself.
// This is the SIY-177 dedup for the historical flow: a child created by the
// old code carries its ancestors' full text inside description, and claiming
// it used to inject that text AGAIN on top of the freshly regenerated brief.
//
// If any referenced issue is NOT covered (typically: an ancestor was deleted
// and broke the parent chain, so the live walk can no longer reach it), the
// block is kept — the embedded copy is then the only remaining source for
// that background and the issue must stay usable.
func StripRedundantAncestorBrief(description string, coveredIDs map[string]struct{}) string {
	idx := strings.Index(description, AncestorBriefMarker)
	if idx < 0 {
		return description
	}
	refs := ancestorBriefRefPattern.FindAllStringSubmatch(description[idx:], -1)
	if len(refs) == 0 {
		// Not a block we can attribute to a known snapshot; keep it rather
		// than risk deleting authored content.
		return description
	}
	for _, match := range refs {
		if _, ok := coveredIDs[match[1]]; !ok {
			return description
		}
	}
	return strings.TrimRight(description[:idx], " \t\n")
}

// AncestorBriefCoveredIDs builds the coverage set for StripRedundantAncestorBrief
// from a regenerated brief's refs plus the current issue's own id.
func AncestorBriefCoveredIDs(refs []AncestorBriefRef, currentIssueID string) map[string]struct{} {
	covered := make(map[string]struct{}, len(refs)+1)
	if currentIssueID != "" {
		covered[currentIssueID] = struct{}{}
	}
	for _, ref := range refs {
		covered[ref.ID] = struct{}{}
	}
	return covered
}

// ancestorRow is one ancestor in the reachable chain, ordered leaf→root.
type ancestorRow struct {
	id          string
	title       string
	description string
	updatedAt   string
}

// BuildAncestorBrief walks from the current issue's parent to the root and
// includes only each ancestor's ID, title, and Markdown description.
//
// SIY-177 dedup: descriptions along the chain may themselves contain embedded
// ANCESTOR_BRIEF blocks (the pre-SIY-177 flow appended them to every
// generated child). Before rendering a section, any embedded block whose
// referenced issues are all reachable in this walk is stripped, so the same
// ancestor text can never be delivered twice inside one brief. Sections that
// end up byte-identical are dropped for the same reason.
func BuildAncestorBrief(ctx context.Context, reader IssueReader, issue db.Issue) AncestorBrief {
	brief := AncestorBrief{}

	// Pass 1: collect the reachable chain, leaf→root.
	ancestors := make([]ancestorRow, 0, 4)
	parentID := issue.ParentIssueID
	seen := make(map[string]struct{})
	reachable := map[string]struct{}{util.UUIDToString(issue.ID): {}}
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
		ancestors = append(ancestors, ancestorRow{
			id:          id,
			title:       ancestor.Title,
			description: ancestor.Description.String,
			updatedAt:   ancestor.UpdatedAt.Time.UTC().Format("2006-01-02T15:04:05Z"),
		})
		reachable[id] = struct{}{}
		parentID = ancestor.ParentIssueID
	}

	// Pass 2: render the sections, closest ancestor first, stripping embedded
	// brief copies whose references this walk already covers.
	sections := make([]string, 0, len(ancestors))
	seenSection := make(map[string]struct{}, len(ancestors))
	for _, ancestor := range ancestors {
		description := StripRedundantAncestorBrief(ancestor.description, reachable)
		if description == "" {
			continue
		}
		section := fmt.Sprintf("[Background source: Issue %s]\nTitle: %s\nDescription:\n%s", ancestor.id, ancestor.title, description)
		if _, ok := seenSection[section]; ok {
			continue
		}
		seenSection[section] = struct{}{}
		candidate := strings.Join(append(append([]string{}, sections...), section), "\n\n")
		if len([]rune(candidate)) > ancestorBriefMaxChars {
			remaining := ancestorBriefMaxChars - len([]rune(strings.Join(sections, "\n\n"))) - 2
			if remaining > 0 {
				sections = append(sections, truncateAncestorBriefText(section, remaining))
			}
			brief.Truncated = true
			brief.TruncatedIDs = append(brief.TruncatedIDs, ancestor.id)
			break
		}
		sections = append(sections, section)
	}
	if len(sections) == 0 {
		return brief
	}
	brief.Text = AncestorBriefMarker + "\n\n" + strings.Join(sections, "\n\n")
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
