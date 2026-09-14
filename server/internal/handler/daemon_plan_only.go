package handler

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/taskfailure"
)

// buildClaimedPlanOnlyTaskResponse is intentionally separate from the normal
// claim builder. A plan-only task receives an immutable issue snapshot and a
// provider configuration, but it must never inherit the ordinary task's
// comments, project resources, plugins, skills, MCP servers, checkout, or
// resume pointers. Keeping this branch before those reads makes the boundary
// structural rather than dependent on every future ordinary-task feature
// remembering to add another `!planOnly` guard.
func (h *Handler) buildClaimedPlanOnlyTaskResponse(
	r *http.Request,
	task *db.AgentTaskQueue,
	runtime db.AgentRuntime,
	runtimeID,
	runtimeWorkspaceID string,
) (resp AgentTaskResponse, deliveredCommentIDs []pgtype.UUID, agentSkillCount, builtinSkillCount int, failure *claimBuildFailure) {
	deliveredCommentIDs = []pgtype.UUID{}
	resp = planOnlyTaskResponse(*task, runtimeWorkspaceID)

	reject := func(message, outcome string, reason taskfailure.Reason, status int) (AgentTaskResponse, []pgtype.UUID, int, int, *claimBuildFailure) {
		return resp, deliveredCommentIDs, agentSkillCount, builtinSkillCount, h.failClaimedTaskBeforeLaunch(
			r.Context(), task, message, reason, outcome, status, message,
		)
	}

	planContext, err := service.ParseSubissuePlanOnlyContext(task.Context)
	if err != nil {
		return reject("plan-only task context is invalid or expired", "error_plan_only_context", taskfailure.ReasonInvalidTaskIdentity, http.StatusConflict)
	}

	if !task.IssueID.Valid || planContext.IssueID != uuidToString(task.IssueID) {
		return reject("plan-only task issue scope is invalid", "error_plan_only_issue_scope", taskfailure.ReasonInvalidTaskIdentity, http.StatusConflict)
	}
	workspaceUUID, workspaceErr := util.ParseUUID(planContext.WorkspaceID)
	if workspaceErr != nil || planContext.WorkspaceID != runtimeWorkspaceID || runtime.WorkspaceID != workspaceUUID {
		return reject("plan-only task workspace isolation check failed", "error_plan_only_workspace", taskfailure.ReasonInvalidTaskIdentity, http.StatusForbidden)
	}
	if runtimeID != uuidToString(task.RuntimeID) || runtime.ID != task.RuntimeID || runtime.Status != "online" {
		return reject("plan-only task runtime is no longer available", "error_plan_only_runtime", taskfailure.ReasonInvalidTaskIdentity, http.StatusConflict)
	}
	if task.TriggerCommentID.Valid || len(task.CoalescedCommentIds) > 0 || task.ChatSessionID.Valid || task.AutopilotRunID.Valid || task.ParentTaskID.Valid || task.RerunOfTaskID.Valid || task.RetryOfTaskID.Valid {
		return reject("plan-only task carries ordinary task routing state", "error_plan_only_routing", taskfailure.ReasonInvalidTaskIdentity, http.StatusConflict)
	}
	if len(task.RuntimeMcpOverlay) > 0 || len(task.RuntimeConnectedApps) > 0 || len(task.ContextManifest) > 0 {
		return reject("plan-only task carries writable or ordinary runtime context", "error_plan_only_capability", taskfailure.ReasonInvalidTaskIdentity, http.StatusConflict)
	}

	issue, err := h.Queries.GetIssue(r.Context(), task.IssueID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return reject("plan-only task issue no longer exists", "error_plan_only_issue_missing", taskfailure.ReasonInvalidTaskIdentity, http.StatusConflict)
		}
		return resp, deliveredCommentIDs, agentSkillCount, builtinSkillCount, h.rejectClaimSourceLoad(r.Context(), task, err, "plan-only issue", uuidToString(task.IssueID))
	}
	if issue.WorkspaceID != runtime.WorkspaceID || uuidToString(issue.ID) != planContext.IssueID {
		return reject("plan-only task issue workspace isolation check failed", "error_plan_only_issue_workspace", taskfailure.ReasonInvalidTaskIdentity, http.StatusForbidden)
	}

	jobID, err := parsePlanOnlyUUID(planContext.JobID)
	if err != nil {
		return reject("plan-only task job identity is invalid", "error_plan_only_job", taskfailure.ReasonInvalidTaskIdentity, http.StatusConflict)
	}
	job, err := h.Queries.GetSubissuePlanJob(r.Context(), db.GetSubissuePlanJobParams{ID: jobID, WorkspaceID: runtime.WorkspaceID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return reject("plan-only planning job no longer exists", "error_plan_only_job_missing", taskfailure.ReasonInvalidTaskIdentity, http.StatusConflict)
		}
		return resp, deliveredCommentIDs, agentSkillCount, builtinSkillCount, h.rejectClaimSourceLoad(r.Context(), task, err, "plan-only planning job", planContext.JobID)
	}
	if job.IssueID != issue.ID || !job.TaskID.Valid || job.TaskID != task.ID || !job.AgentID.Valid || job.AgentID != task.AgentID || (job.Status != "queued" && job.Status != "running") || job.SourceSnapshotDigest != planContext.SourceSnapshotDigest {
		return reject("plan-only planning job binding is invalid", "error_plan_only_job_binding", taskfailure.ReasonInvalidTaskIdentity, http.StatusConflict)
	}

	agent, err := h.Queries.GetAgent(r.Context(), task.AgentID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return reject("plan-only task agent no longer exists", "error_plan_only_agent_missing", taskfailure.ReasonInvalidTaskIdentity, http.StatusConflict)
		}
		return resp, deliveredCommentIDs, agentSkillCount, builtinSkillCount, h.rejectClaimSourceLoad(r.Context(), task, err, "plan-only task agent", uuidToString(task.AgentID))
	}
	if agent.WorkspaceID != runtime.WorkspaceID || agent.ArchivedAt.Valid || agent.Kind != "user" || !agent.RuntimeID.Valid || agent.RuntimeID != task.RuntimeID {
		return reject("plan-only task agent/runtime binding is invalid", "error_plan_only_agent_binding", taskfailure.ReasonInvalidTaskIdentity, http.StatusConflict)
	}
	if runtime.Visibility == "private" && runtime.OwnerID.Valid && (!agent.OwnerID.Valid || agent.OwnerID != runtime.OwnerID) {
		return reject("plan-only task agent is not allowed on this private runtime", "error_plan_only_runtime_access", taskfailure.ReasonInvalidTaskIdentity, http.StatusForbidden)
	}

	resp.Agent = planOnlyAgentData(agent)
	resp.LeaderRoleResolved = true
	return resp, deliveredCommentIDs, agentSkillCount, builtinSkillCount, nil
}

func planOnlyTaskResponse(task db.AgentTaskQueue, workspaceID string) AgentTaskResponse {
	return AgentTaskResponse{
		ID:                  uuidToString(task.ID),
		AgentID:             uuidToString(task.AgentID),
		RuntimeID:           uuidToString(task.RuntimeID),
		IssueID:             uuidToString(task.IssueID),
		WorkspaceID:         workspaceID,
		Status:              task.Status,
		PlanOnly:            true,
		PlanOnlyContext:     append(json.RawMessage(nil), task.Context...),
		Priority:            task.Priority,
		DispatchedAt:        timestampToPtr(task.DispatchedAt),
		StartedAt:           timestampToPtr(task.StartedAt),
		CompletedAt:         timestampToPtr(task.CompletedAt),
		Attempt:             task.Attempt,
		MaxAttempts:         task.MaxAttempts,
		CreatedAt:           timestampToString(task.CreatedAt),
		DeliveredCommentIDs: []string{},
	}
}

func planOnlyAgentData(agent db.Agent) *TaskAgentData {
	data := &TaskAgentData{
		ID:            uuidToString(agent.ID),
		Name:          agent.Name,
		Model:         agent.Model.String,
		ThinkingLevel: agent.ThinkingLevel.String,
		ServiceTier:   agent.ServiceTier.String,
	}
	if agent.CustomEnv != nil {
		var customEnv map[string]string
		if err := json.Unmarshal(agent.CustomEnv, &customEnv); err == nil {
			data.CustomEnv = customEnv
		}
	}
	if raw := bytes.TrimSpace(agent.RuntimeConfig); len(raw) > 0 && !bytes.Equal(raw, []byte("{}")) && !bytes.Equal(raw, []byte("null")) {
		data.RuntimeConfig = json.RawMessage(agent.RuntimeConfig)
	}
	return data
}

func parsePlanOnlyUUID(value string) (pgtype.UUID, error) {
	id, err := util.ParseUUID(value)
	if err != nil {
		return pgtype.UUID{}, err
	}
	return id, nil
}
