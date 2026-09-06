package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// SIY-125: handler tests for the context-observation GET (user-facing) and the
// daemon POST that stores it. They pin the acceptance points: default
// redaction (short session id, no raw), owner/admin raw reveal on demand, exact
// token merge from task_usage, the explicit "unknown" state when no
// observation exists, and workspace tenancy.

// insertCtxObsTask seeds an agent + issue + completed task in the test
// workspace and returns the task id. The agent/issue/task are the minimum
// requireDaemonTaskAccess / GetAgentTaskInWorkspace need to resolve tenancy.
func insertCtxObsTask(t *testing.T, name string) (taskID, agentID string) {
	t.Helper()
	if testHandler == nil {
		t.Skip("database not available")
	}
	agentID = createHandlerTestAgent(t, name, []byte("[]"))

	issueID := dbfx.Insert(t, "issue", testutil.Cols{
		"workspace_id": testWorkspaceID,
		"title":        name + " issue",
		"status":       "in_progress",
		"priority":     "medium",
		"creator_id":   testUserID,
		"creator_type": "member",
		"number":       93001,
		"position":     0,
	})
	t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, issueID) })

	dbfx.QueryRow(t,
		`INSERT INTO agent_task_queue (agent_id, runtime_id, issue_id, status, priority, completed_at)
		 VALUES ($1, (SELECT runtime_id FROM agent WHERE id = $1), $2, 'completed', 0, now())
		 RETURNING id`,
		agentID, issueID,
	).Scan(&taskID)
	t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE id = $1`, taskID) })
	return taskID, agentID
}

// ctxObsPayloadJSON builds a redacted observation payload carrying a full
// session id and a brief section with Raw text, so the redaction/reveal paths
// have something to withhold by default.
func ctxObsPayloadJSON(t *testing.T, taskID string) []byte {
	t.Helper()
	payload := taskContextResponse{
		TaskID:         taskID,
		Provider:       "claude",
		RuntimeID:      "rt-1",
		SessionReused:  true,
		ResumeExpected: true,
		ResumeActual:   "resumed",
		WorkdirReused:  true,
		PromptBytes:    1564,
		InputTokens:    400,
		TokenMode:      "estimated",
		SessionID:      "sess-abcdefghijklmnop",
		Sections: []taskContextSection{
			{
				Key:        "brief",
				Source:     "execenv.InjectRuntimeConfig",
				Delivery:   "workdir_file",
				Injected:   false,
				Bytes:      1200,
				TokenCount: 300,
				Preview:    "# Multica Agent Runtime …",
				Raw:        "# Multica Agent Runtime\n\nFull redacted brief text.",
			},
			{
				Key:      "mcp",
				Source:   "agent.mcp_config",
				Delivery: "provider_mcp",
				Injected: false,
				Preview:  "MCP config delivered to provider (withheld — contains secrets)",
			},
		},
	}
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return b
}

func insertCtxObservation(t *testing.T, taskID string, payload []byte) {
	t.Helper()
	dbfx.Exec(t,
		`INSERT INTO task_context_observation (task_id, payload) VALUES ($1, $2::jsonb)`,
		taskID, string(payload),
	)
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM task_context_observation WHERE task_id = $1`, taskID)
	})
}

// withMemberCtx loads the caller's member row and stamps it + the workspace id
// into the request context, mirroring the RequireWorkspaceMember middleware so
// ctxMember/roleAllowed work in a direct handler call.
func withMemberCtx(t *testing.T, req *http.Request, userID string) *http.Request {
	t.Helper()
	member, err := testHandler.Queries.GetMemberByUserAndWorkspace(context.Background(), db.GetMemberByUserAndWorkspaceParams{
		UserID:      util.MustParseUUID(userID),
		WorkspaceID: util.MustParseUUID(testWorkspaceID),
	})
	if err != nil {
		t.Fatalf("load member for %s: %v", userID, err)
	}
	return req.WithContext(middleware.SetMemberContext(req.Context(), testWorkspaceID, member))
}

func getTaskContextReq(t *testing.T, userID, taskID, raw string) *http.Request {
	t.Helper()
	path := "/api/tasks/" + taskID + "/context"
	if raw != "" {
		path += "?raw=" + raw
	}
	req := newRequestAs(userID, http.MethodGet, path, nil)
	req = withURLParam(req, "taskId", taskID)
	return withChatTestWorkspaceCtx(t, req)
}

func decodeCtxResp(t *testing.T, w *httptest.ResponseRecorder) taskContextResponse {
	t.Helper()
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp taskContextResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode context response: %v", err)
	}
	return resp
}

func TestGetTaskContext_DefaultIsRedacted(t *testing.T) {
	taskID, _ := insertCtxObsTask(t, "CtxObsDefault")
	insertCtxObservation(t, taskID, ctxObsPayloadJSON(t, taskID))

	w := httptest.NewRecorder()
	testHandler.GetTaskContext(w, getTaskContextReq(t, testUserID, taskID, ""))
	resp := decodeCtxResp(t, w)

	// The full session id is withheld by default.
	if resp.SessionID == "sess-abcdefghijklmnop" {
		t.Fatalf("default response must shorten the session id, got full %q", resp.SessionID)
	}
	if resp.SessionID != "sess-abc…" {
		t.Fatalf("session id shortening mismatch: %q", resp.SessionID)
	}
	// Raw text is withheld without ?raw=.
	for _, s := range resp.Sections {
		if s.Key == "brief" && s.Raw != "" {
			t.Fatalf("default response must not expose section raw for %q: %q", s.Key, s.Raw)
		}
	}
}

func TestGetTaskContext_OwnerRawReveal(t *testing.T) {
	taskID, _ := insertCtxObsTask(t, "CtxObsOwnerReveal")
	insertCtxObservation(t, taskID, ctxObsPayloadJSON(t, taskID))

	w := httptest.NewRecorder()
	testHandler.GetTaskContext(w, getTaskContextReq(t, testUserID, taskID, "all"))
	resp := decodeCtxResp(t, w)

	// An owner requesting ?raw=all gets the full session id and section raw.
	if resp.SessionID != "sess-abcdefghijklmnop" {
		t.Fatalf("owner raw request must return the full session id, got %q", resp.SessionID)
	}
	var brief *taskContextSection
	for i := range resp.Sections {
		if resp.Sections[i].Key == "brief" {
			brief = &resp.Sections[i]
		}
	}
	if brief == nil || brief.Raw == "" {
		t.Fatalf("owner ?raw=all must reveal the brief section raw: %+v", brief)
	}
}

func TestGetTaskContext_PlainMemberNoRaw(t *testing.T) {
	taskID, _ := insertCtxObsTask(t, "CtxObsPlainMember")
	insertCtxObservation(t, taskID, ctxObsPayloadJSON(t, taskID))
	otherUserID := createWorkspaceMemberUser(t, "CtxObs Bystander", "ctxobs-bystander@multica.test")

	// Build the request as the plain member with that member's own context
	// (withChatTestWorkspaceCtx would stamp the owner's member row instead).
	req := withMemberCtx(t, withURLParam(
		newRequestAs(otherUserID, http.MethodGet, "/api/tasks/"+taskID+"/context?raw=all", nil),
		"taskId", taskID), otherUserID)

	w := httptest.NewRecorder()
	testHandler.GetTaskContext(w, req)
	resp := decodeCtxResp(t, w)

	// A plain member never gets the full session id or raw, even with ?raw=all.
	if resp.SessionID == "sess-abcdefghijklmnop" {
		t.Fatalf("plain member must not receive the full session id: %q", resp.SessionID)
	}
	for _, s := range resp.Sections {
		if s.Raw != "" {
			t.Fatalf("plain member must not receive section raw for %q", s.Key)
		}
	}
}

func TestGetTaskContext_MergesExactTokensFromUsage(t *testing.T) {
	taskID, _ := insertCtxObsTask(t, "CtxObsExactTokens")
	insertCtxObservation(t, taskID, ctxObsPayloadJSON(t, taskID))

	// task_usage is reported separately by ReportTaskUsage; the GET endpoint
	// merges its exact input total over the daemon's estimate.
	dbfx.Exec(t,
		`INSERT INTO task_usage (task_id, provider, model, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens)
		 VALUES ($1, 'claude', 'claude-sonnet-5', 1800, 200, 0, 0)`,
		taskID,
	)
	t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM task_usage WHERE task_id = $1`, taskID) })

	w := httptest.NewRecorder()
	testHandler.GetTaskContext(w, getTaskContextReq(t, testUserID, taskID, ""))
	resp := decodeCtxResp(t, w)

	if resp.TokenMode != "exact" {
		t.Fatalf("usage rows must upgrade token_mode to exact, got %q", resp.TokenMode)
	}
	if resp.InputTokens != 1800 {
		t.Fatalf("exact input tokens must be the usage sum, got %d", resp.InputTokens)
	}
}

func TestGetTaskContext_NoObservationReturnsUnknown(t *testing.T) {
	taskID, _ := insertCtxObsTask(t, "CtxObsUnknown")
	// No observation row inserted — e.g. a task that predates the feature.

	w := httptest.NewRecorder()
	testHandler.GetTaskContext(w, getTaskContextReq(t, testUserID, taskID, ""))
	resp := decodeCtxResp(t, w)

	if resp.ResumeActual != "unknown" {
		t.Fatalf("missing observation must surface as unknown, got %q", resp.ResumeActual)
	}
	if resp.TokenMode != "estimated" {
		t.Fatalf("unknown record with no usage must be estimated, got %q", resp.TokenMode)
	}
}

func TestGetTaskContext_CrossWorkspaceReturns404(t *testing.T) {
	foreignAgentID := createForeignWorkspaceAgent(t) // in a different workspace
	taskID := createAutopilotRunOnlyTask(t, foreignAgentID)

	w := httptest.NewRecorder()
	testHandler.GetTaskContext(w, getTaskContextReq(t, testUserID, taskID, ""))
	if w.Code != http.StatusNotFound {
		t.Fatalf("cross-workspace task must 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestReportTaskContextObservation_StoresAndServeed(t *testing.T) {
	taskID, _ := insertCtxObsTask(t, "CtxObsDaemonPost")
	payload := ctxObsPayloadJSON(t, taskID)

	// Daemon reports the redacted record.
	req := newDaemonTokenRequest("POST", "/api/daemon/tasks/"+taskID+"/context-observation",
		json.RawMessage(payload), testWorkspaceID, "legit-daemon")
	req = withURLParam(req, "taskId", taskID)
	w := httptest.NewRecorder()
	testHandler.ReportTaskContextObservation(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("POST context observation: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// The stored record is then served (redacted by default) by the GET endpoint.
	getW := httptest.NewRecorder()
	testHandler.GetTaskContext(getW, getTaskContextReq(t, testUserID, taskID, ""))
	resp := decodeCtxResp(t, getW)
	if resp.Provider != "claude" || resp.ResumeActual != "resumed" {
		t.Fatalf("stored observation did not round-trip: %+v", resp)
	}
	if resp.SessionID == "sess-abcdefghijklmnop" {
		t.Fatalf("GET must shorten the session id even after a daemon POST, got %q", resp.SessionID)
	}
}

func TestReportTaskContextObservation_RejectsInvalidJSON(t *testing.T) {
	taskID, _ := insertCtxObsTask(t, "CtxObsDaemonBadJSON")
	req := newDaemonTokenRequest("POST", "/api/daemon/tasks/"+taskID+"/context-observation",
		json.RawMessage(`{not valid json`), testWorkspaceID, "legit-daemon")
	req = withURLParam(req, "taskId", taskID)
	w := httptest.NewRecorder()
	testHandler.ReportTaskContextObservation(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("malformed JSON must be rejected: expected 400, got %d: %s", w.Code, w.Body.String())
	}
}
