package service

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// ParseSubissuePlanOnlyContext is the single decoder used by task lifecycle
// code and the daemon-facing claim path. A malformed context is never treated
// as an ordinary issue task: the caller must fail closed.
func ParseSubissuePlanOnlyContext(raw []byte) (SubissuePlanOnlyContext, error) {
	var value SubissuePlanOnlyContext
	if len(raw) == 0 {
		return value, errors.New("plan-only context is empty")
	}
	if err := json.Unmarshal(raw, &value); err != nil {
		return value, fmt.Errorf("decode plan-only context: %w", err)
	}
	if value.Type != SubissuePlanOnlyContextType || !value.Readonly {
		return value, errors.New("task context is not a read-only plan-only context")
	}
	if strings.TrimSpace(value.JobID) == "" || strings.TrimSpace(value.WorkspaceID) == "" || strings.TrimSpace(value.IssueID) == "" {
		return value, errors.New("plan-only context is missing its owner ids")
	}
	if strings.TrimSpace(value.SourceSnapshotDigest) == "" {
		return value, errors.New("plan-only context is missing source digest")
	}
	digest, _, err := DigestSubissuePlanSnapshot(value.SourceSnapshot)
	if err != nil {
		return value, fmt.Errorf("digest plan-only snapshot: %w", err)
	}
	if subtle.ConstantTimeCompare([]byte(digest), []byte(value.SourceSnapshotDigest)) != 1 {
		return value, errors.New("plan-only source digest does not match snapshot")
	}
	if value.SourceSnapshot.WorkspaceID != value.WorkspaceID || value.SourceSnapshot.IssueID != value.IssueID {
		return value, errors.New("plan-only source snapshot scope does not match context")
	}
	if value.ExpiresAt.IsZero() || !value.ExpiresAt.After(time.Now()) {
		return value, errors.New("plan-only context has expired")
	}
	return value, nil
}

// IsSubissuePlanOnlyTask is intentionally conservative. Only a fully valid
// plan-only marker is special; malformed contexts are handled as a claim/task
// failure by their respective boundary rather than accidentally entering the
// ordinary issue-comment route.
func IsSubissuePlanOnlyTask(task db.AgentTaskQueue) bool {
	if len(task.Context) == 0 {
		return false
	}
	value, err := ParseSubissuePlanOnlyContext(task.Context)
	return err == nil && value.Type == SubissuePlanOnlyContextType
}

// HasSubissuePlanOnlyMarker distinguishes a malformed/expired plan-only row
// from an ordinary task. Lifecycle code uses this weaker predicate to avoid
// routing a damaged marker through comments, issue status, or retry logic.
// Claim code must still call ParseSubissuePlanOnlyContext and fail closed.
func HasSubissuePlanOnlyMarker(task db.AgentTaskQueue) bool {
	if len(task.Context) == 0 {
		return false
	}
	var marker struct {
		Type string `json:"type"`
	}
	return json.Unmarshal(task.Context, &marker) == nil && marker.Type == SubissuePlanOnlyContextType
}

// EnqueueSubissuePlanOnlyTask creates the only task kind that is allowed to
// carry a captured issue snapshot. It has no trigger comment, no handoff, no
// project resources, and no resume pointer; all of those omissions are part
// of the safety boundary rather than prompt conventions.
func (s *TaskService) EnqueueSubissuePlanOnlyTask(ctx context.Context, issue db.Issue, agentID pgtype.UUID, planContext SubissuePlanOnlyContext) (db.AgentTaskQueue, error) {
	if s == nil || s.Queries == nil {
		return db.AgentTaskQueue{}, errors.New("task service is not configured")
	}
	if planContext.Type != SubissuePlanOnlyContextType || !planContext.Readonly {
		return db.AgentTaskQueue{}, errors.New("invalid plan-only context")
	}
	if planContext.IssueID != util.UUIDToString(issue.ID) || planContext.WorkspaceID != util.UUIDToString(issue.WorkspaceID) {
		return db.AgentTaskQueue{}, errors.New("plan-only context scope does not match issue")
	}
	if _, err := ParseSubissuePlanOnlyContext(mustMarshalPlanContext(planContext)); err != nil {
		return db.AgentTaskQueue{}, err
	}
	agent, err := s.Queries.GetAgent(ctx, agentID)
	if err != nil {
		return db.AgentTaskQueue{}, fmt.Errorf("load fallback agent: %w", err)
	}
	if agent.WorkspaceID != issue.WorkspaceID || agent.ArchivedAt.Valid || agent.Kind != "user" ||
		!agent.RuntimeID.Valid || agent.Status == "offline" || agent.Status == "error" {
		return db.AgentTaskQueue{}, errors.New("fallback agent is unavailable")
	}
	runtime, err := s.Queries.GetAgentRuntime(ctx, agent.RuntimeID)
	if err != nil {
		return db.AgentTaskQueue{}, fmt.Errorf("load fallback runtime: %w", err)
	}
	if runtime.WorkspaceID != issue.WorkspaceID || runtime.Status != "online" {
		return db.AgentTaskQueue{}, errors.New("fallback runtime is unavailable")
	}
	rawContext := mustMarshalPlanContext(planContext)
	task, err := s.Queries.CreateAgentTask(ctx, db.CreateAgentTaskParams{
		ID:                dbid.NewV7(),
		AgentID:           agent.ID,
		RuntimeID:         agent.RuntimeID,
		IssueID:           issue.ID,
		Priority:          priorityToInt(issue.Priority),
		ForceFreshSession: pgtype.Bool{Bool: true, Valid: true},
		TaskContext:       rawContext,
		OriginatorUserID:  agent.OwnerID,
		AccountableUserID: agent.OwnerID,
		OriginatorSource:  pgtype.Text{String: "plan_only", Valid: true},
	})
	if err != nil {
		return db.AgentTaskQueue{}, fmt.Errorf("create plan-only task: %w", err)
	}
	slog.Info("plan-only task enqueued", "task_id", util.UUIDToString(task.ID), "job_id", planContext.JobID, "agent_id", util.UUIDToString(agent.ID))
	s.broadcastTaskEvent(ctx, protocol.EventTaskQueued, task)
	s.NotifyTaskEnqueued(ctx, task)
	return task, nil
}

func mustMarshalPlanContext(value SubissuePlanOnlyContext) []byte {
	raw, _ := json.Marshal(value)
	return raw
}

func planOnlyContextForTask(task db.AgentTaskQueue) (SubissuePlanOnlyContext, bool) {
	if len(task.Context) == 0 {
		return SubissuePlanOnlyContext{}, false
	}
	var value SubissuePlanOnlyContext
	if err := json.Unmarshal(task.Context, &value); err != nil || value.Type != SubissuePlanOnlyContextType {
		return SubissuePlanOnlyContext{}, false
	}
	return value, true
}

func (s *TaskService) startSubissuePlanJobForTask(ctx context.Context, task db.AgentTaskQueue) {
	value, ok := planOnlyContextForTask(task)
	if !ok {
		return
	}
	if _, err := s.Queries.StartSubissuePlanJob(ctx, parseUUIDString(value.JobID)); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		slog.Warn("start subissue plan job failed", "job_id", value.JobID, "task_id", util.UUIDToString(task.ID), "error", err)
	}
}

func (s *TaskService) completeSubissuePlanJobForTask(ctx context.Context, task db.AgentTaskQueue, result []byte) {
	value, ok := planOnlyContextForTask(task)
	if !ok {
		return
	}
	status := "succeeded"
	var stored []byte
	errorSummary := pgtype.Text{}
	output, err := ExtractSubissuePlanTaskOutput(result)
	if err == nil {
		var plans []SubissuePlan
		plans, err = ValidateSubissuePlanOnlyResult([]byte(output))
		if err == nil {
			stored, err = json.Marshal(struct {
				Plans []SubissuePlan `json:"plans"`
			}{Plans: plans})
		}
	}
	if err != nil {
		status = "failed"
		errorSummary = pgtype.Text{String: "plan-only agent returned an invalid planning result", Valid: true}
		slog.Warn("plan-only result rejected", "job_id", value.JobID, "task_id", util.UUIDToString(task.ID), "error", err)
	}
	if _, updateErr := s.Queries.CompleteSubissuePlanJob(ctx, db.CompleteSubissuePlanJobParams{
		ID: parseUUIDString(value.JobID), Status: status, Result: stored, ErrorSummary: errorSummary,
	}); updateErr != nil && !errors.Is(updateErr, pgx.ErrNoRows) {
		slog.Warn("complete subissue plan job failed", "job_id", value.JobID, "task_id", util.UUIDToString(task.ID), "error", updateErr)
	}
}

func (s *TaskService) failSubissuePlanJobForTask(ctx context.Context, task db.AgentTaskQueue, reason string) {
	value, ok := planOnlyContextForTask(task)
	if !ok {
		return
	}
	message := "plan-only associated agent task failed"
	if strings.TrimSpace(reason) != "" {
		message += ": " + strings.TrimSpace(reason)
	}
	if len([]rune(message)) > 240 {
		message = string([]rune(message)[:240])
	}
	if _, err := s.Queries.CompleteSubissuePlanJob(ctx, db.CompleteSubissuePlanJobParams{
		ID: parseUUIDString(value.JobID), Status: "failed", ErrorSummary: pgtype.Text{String: message, Valid: true},
	}); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		slog.Warn("fail subissue plan job failed", "job_id", value.JobID, "task_id", util.UUIDToString(task.ID), "error", err)
	}
}

func (s *TaskService) cancelSubissuePlanJobForTask(ctx context.Context, task db.AgentTaskQueue) {
	value, ok := planOnlyContextForTask(task)
	if !ok {
		return
	}
	if _, err := s.Queries.CancelSubissuePlanJob(ctx, db.CancelSubissuePlanJobParams{
		ID: parseUUIDString(value.JobID), WorkspaceID: parseUUIDString(value.WorkspaceID),
	}); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		slog.Warn("cancel subissue plan job failed", "job_id", value.JobID, "task_id", util.UUIDToString(task.ID), "error", err)
	}
}

func parseUUIDString(value string) pgtype.UUID {
	parsed, err := util.ParseUUID(strings.TrimSpace(value))
	if err != nil {
		return pgtype.UUID{}
	}
	return parsed
}
