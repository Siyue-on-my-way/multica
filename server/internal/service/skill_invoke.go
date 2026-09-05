package service

import (
	"context"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/skillbundle"
)

// Global skills are OFF an agent's payload by default (SIY-95 decision 3). A
// run attaches one only when the text that triggered the run names the skill:
// the issue title/description for assignment runs, the triggering (and
// coalesced) comment bodies for comment runs, plus the assigner's handoff
// note. Matching is a case-insensitive, boundary-aware substring test, so
// typing a skill name — bare, `@name`, or inside a sentence — invokes it, in
// both the issue description and the discussion thread.

// skillMatchTextCap bounds how much trigger text is scanned per task. Comments
// are individually capped by the API, but a task can coalesce many of them.
const skillMatchTextCap = 128 << 10

// LoadTaskSkills returns the skill set for one task run: the agent's assigned
// skills, then any global skills the trigger text invokes.
func (s *TaskService) LoadTaskSkills(ctx context.Context, task *db.AgentTaskQueue, agentSystemKey string, legacyRedirects bool) ([]AgentSkillData, error) {
	skills, err := s.LoadAgentSkills(ctx, task.AgentID)
	if err != nil {
		return nil, err
	}
	globals := s.MatchedGlobalSkills(ctx, task, skills)
	return append(skills, globals...), nil
}

// LoadTaskSkillBundles is the bundle/ref variant of LoadTaskSkills, matching
// the shape LoadAgentSkillBundles returns for claims that carry skill refs.
func (s *TaskService) LoadTaskSkillBundles(ctx context.Context, task *db.AgentTaskQueue, agentSystemKey string, legacyRedirects bool) ([]AgentSkillData, []AgentSkillRefData, error) {
	skills, err := s.LoadTaskSkills(ctx, task, agentSystemKey, legacyRedirects)
	if err != nil {
		return nil, nil, err
	}
	skills = append(skills, s.BuiltinSkills(agentSystemKey, legacyRedirects)...)
	bundles, refs := BuildAgentSkillBundles(skills)
	return bundles, refs, nil
}

// MatchedGlobalSkills returns every global skill whose name appears in the
// task's trigger text, skipping skills the agent already has assigned.
func (s *TaskService) MatchedGlobalSkills(ctx context.Context, task *db.AgentTaskQueue, assigned []AgentSkillData) []AgentSkillData {
	globals, err := s.Queries.ListGlobalSkills(ctx)
	if err != nil || len(globals) == 0 {
		return nil
	}

	text := s.taskSkillMatchText(ctx, task)
	if strings.TrimSpace(text) == "" {
		return nil
	}
	// Cap before matching: the concatenation is bounded, not trusted.
	if len(text) > skillMatchTextCap {
		text = text[:skillMatchTextCap]
	}

	alreadyAssigned := make(map[string]bool, len(assigned))
	for _, a := range assigned {
		alreadyAssigned[a.Name] = true
	}

	matched := make([]AgentSkillData, 0, 2)
	for _, g := range globals {
		if alreadyAssigned[g.Name] {
			continue
		}
		if !SkillNameInText(text, g.Name) {
			continue
		}
		data := s.agentSkillDataFromDB(ctx, g)
		data.Source = skillbundle.SourceGlobal
		matched = append(matched, data)
	}
	if len(matched) > 0 {
		names := make([]string, 0, len(matched))
		for _, m := range matched {
			names = append(names, m.Name)
		}
		slog.Info("global skills attached by trigger text", "task_id", util.UUIDToString(task.ID), "skills", strings.Join(names, ","))
	}
	return matched
}

// taskSkillMatchText assembles the human-written text that triggered the run.
// Missing pieces are skipped silently: a chat or autopilot task has no issue
// text and simply matches nothing.
func (s *TaskService) taskSkillMatchText(ctx context.Context, task *db.AgentTaskQueue) string {
	var b strings.Builder
	if task.IssueID.Valid {
		issue, err := s.Queries.GetIssue(ctx, task.IssueID)
		if err == nil {
			b.WriteString(issue.Title)
			b.WriteByte('\n')
			b.WriteString(issue.Description.String)
			b.WriteByte('\n')
		}
	}
	for _, id := range commentIDsForSkillMatch(task) {
		comment, err := s.Queries.GetComment(ctx, id)
		if err != nil {
			continue
		}
		b.WriteString(comment.Content)
		b.WriteByte('\n')
	}
	if task.HandoffNote.Valid {
		b.WriteString(task.HandoffNote.String)
		b.WriteByte('\n')
	}
	return b.String()
}

func commentIDsForSkillMatch(task *db.AgentTaskQueue) []pgtype.UUID {
	ids := make([]pgtype.UUID, 0, 1+len(task.CoalescedCommentIds))
	if task.TriggerCommentID.Valid {
		ids = append(ids, task.TriggerCommentID)
	}
	ids = append(ids, task.CoalescedCommentIds...)
	return ids
}

// agentSkillDataFromDB converts a skill row to its wire shape, loading its
// supporting files. Shared by the agent-assignment and global-skill loaders.
func (s *TaskService) agentSkillDataFromDB(ctx context.Context, sk db.Skill) AgentSkillData {
	data := AgentSkillData{
		ID:          util.UUIDToString(sk.ID),
		Name:        sk.Name,
		Description: sk.Description,
		Content:     sk.Content,
	}
	files, _ := s.Queries.ListSkillFiles(ctx, sk.ID)
	for _, f := range files {
		file := AgentSkillFileData{
			Path:            f.Path,
			ContentEncoding: f.ContentEncoding,
			Mode:            skillbundle.NormalizeFileMode(f.FileMode),
		}
		if file.ContentEncoding == skillbundle.EncodingBase64 {
			file.ContentBase64 = f.Content
		} else {
			file.Content = f.Content
		}
		data.Files = append(data.Files, file)
	}
	return data
}

// SkillNameInText reports whether name appears in text as a standalone token:
// case-insensitive, and not glued to another name character on either side.
// Name characters are [a-z0-9_-], so `my-skill` matches "@my-skill please",
// "use the my-skill skill" and "My-Skill:", but not "my-skills" or "fix-my-skill".
func SkillNameInText(text, name string) bool {
	if text == "" || name == "" {
		return false
	}
	lt := strings.ToLower(text)
	ln := strings.ToLower(name)
	for i := 0; i <= len(lt)-len(ln); {
		at := strings.Index(lt[i:], ln)
		if at < 0 {
			return false
		}
		at += i
		end := at + len(ln)
		if skillNameBoundaryBefore(lt, at) && skillNameBoundaryAfter(lt, end) {
			return true
		}
		i = at + 1
	}
	return false
}

// skillNameBoundaryBefore reports whether the match starting at `at` is not
// preceded by a name character (or is at the text start).
func skillNameBoundaryBefore(s string, at int) bool {
	if at <= 0 || at > len(s) {
		return true
	}
	return !isSkillNameChar(s[at-1])
}

// skillNameBoundaryAfter reports whether the match ending at `at` is not
// followed by a name character (or runs to the text end).
func skillNameBoundaryAfter(s string, at int) bool {
	if at < 0 || at >= len(s) {
		return true
	}
	return !isSkillNameChar(s[at])
}

func isSkillNameChar(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-', c == '_':
		return true
	}
	return false
}
