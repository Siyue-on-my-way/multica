package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/service"
)

// TestExecutionContextAncestorChainIntegration is the SIY-177 ancestor-chain
// integration test: create root → child → grandchild through the real API,
// then verify the resolved execution context along the chain — repo URL
// inherited exactly once with the root's provenance, the grandchild's own
// override winning with its own provenance, a refreshed root document
// reaching descendants under a bumped version, and an ancestor that switched
// inheritance off dropping out of the resolution.
func TestExecutionContextAncestorChainIntegration(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	rootContext := map[string]any{
		"inherit": true,
		"entries": []map[string]any{
			{"kind": "repo", "label": "widget", "value": "https://github.com/acme/widget", "os": ""},
			{"kind": "code_dir", "label": "checkout", "value": "/mnt/a/widget", "os": "linux"},
		},
	}
	create := func(title string, parent string, context map[string]any) IssueResponse {
		body := map[string]any{"title": title}
		if parent != "" {
			body["parent_issue_id"] = parent
		}
		if context != nil {
			body["execution_context"] = context
		}
		w := httptest.NewRecorder()
		req := newRequest("POST", "/api/issues?workspace_id="+testWorkspaceID, body)
		testHandler.CreateIssue(w, req)
		if w.Code != http.StatusCreated {
			t.Fatalf("create %s: expected 201, got %d: %s", title, w.Code, w.Body.String())
		}
		var created IssueResponse
		if err := json.NewDecoder(w.Body).Decode(&created); err != nil {
			t.Fatalf("decode created issue: %v", err)
		}
		t.Cleanup(func() {
			cleanup := withURLParam(newRequest("DELETE", "/api/issues/"+created.ID, nil), "id", created.ID)
			testHandler.DeleteIssue(httptest.NewRecorder(), cleanup)
		})
		return created
	}
	getContext := func(id string) IssueExecutionContextResponse {
		w := httptest.NewRecorder()
		req := withURLParam(newRequest("GET", "/api/issues/"+id+"/execution-context", nil), "id", id)
		testHandler.GetIssueExecutionContext(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("get execution context: expected 200, got %d: %s", w.Code, w.Body.String())
		}
		var response IssueExecutionContextResponse
		if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
			t.Fatalf("decode execution context: %v", err)
		}
		return response
	}

	root := create("Exec ctx root", "", rootContext)
	child := create("Exec ctx child", root.ID, nil)
	grandchild := create("Exec ctx grandchild", child.ID, map[string]any{
		"inherit": false,
		"entries": []map[string]any{
			{"kind": "command", "label": "restart", "value": "npm run dev", "os": ""},
		},
	})

	// The create response echoes the stored document.
	if root.ExecutionContext == nil {
		t.Fatalf("create response must echo the execution context")
	}

	// Grandchild resolution: repo inherited from root (provenance root),
	// linux code_dir keeps its OS tag, restart command overridden by the
	// grandchild itself (provenance grandchild, inherit=false does not block
	// the issue's OWN entries).
	resolved := getContext(grandchild.ID)
	if len(resolved.Inherited.Entries) != 3 {
		t.Fatalf("expected 3 resolved entries, got %+v", resolved.Inherited.Entries)
	}
	repoCount, sawLinuxDir, commandValue := 0, false, ""
	var commandSource string
	for _, entry := range resolved.Inherited.Entries {
		switch {
		case entry.Value == "https://github.com/acme/widget":
			repoCount++
			if entry.SourceIssueID != root.ID {
				t.Fatalf("repo provenance = %q, want root", entry.SourceIssueID)
			}
		case entry.Value == "/mnt/a/widget":
			sawLinuxDir = true
			if entry.OS != "linux" {
				t.Fatalf("code_dir OS tag lost: %+v", entry)
			}
		case entry.Kind == "command":
			commandValue = entry.Value
			commandSource = entry.SourceIssueID
		}
	}
	if repoCount != 1 {
		t.Fatalf("repo must resolve exactly once, got %d", repoCount)
	}
	if !sawLinuxDir {
		t.Fatalf("linux code_dir missing from resolution: %+v", resolved.Inherited.Entries)
	}
	if commandValue != "npm run dev" || commandSource != grandchild.ID {
		t.Fatalf("grandchild override lost: value=%q source=%q", commandValue, commandSource)
	}

	// Refresh the root document: descendants must see the new content under a
	// bumped version (acceptance 4).
	refreshed := map[string]any{
		"inherit": true,
		"entries": []map[string]any{
			{"kind": "repo", "label": "widget", "value": "https://github.com/acme/widget-v2", "os": ""},
		},
	}
	w := httptest.NewRecorder()
	req := withURLParam(newRequest("PUT", "/api/issues/"+root.ID, map[string]any{
		"execution_context": refreshed,
	}), "id", root.ID)
	testHandler.UpdateIssue(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("update root context: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	resolved = getContext(grandchild.ID)
	sawV2, sawOld := false, false
	for _, entry := range resolved.Inherited.Entries {
		if entry.Value == "https://github.com/acme/widget-v2" {
			sawV2 = true
		}
		if entry.Value == "https://github.com/acme/widget" {
			sawOld = true
		}
	}
	if !sawV2 || sawOld {
		t.Fatalf("refreshed repo must replace the old URL (sawV2=%v sawOld=%v)", sawV2, sawOld)
	}
	for _, source := range resolved.Inherited.Sources {
		if source.IssueID == root.ID && source.Version < 2 {
			t.Fatalf("root source version must bump on refresh: %+v", resolved.Inherited.Sources)
		}
	}

	// Inheritance switch off at the root: its entries drop out of every
	// descendant's resolution while the grandchild keeps its own command.
	off := map[string]any{"inherit": false, "entries": refreshed["entries"]}
	w = httptest.NewRecorder()
	req = withURLParam(newRequest("PUT", "/api/issues/"+root.ID, map[string]any{
		"execution_context": off,
	}), "id", root.ID)
	testHandler.UpdateIssue(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("switch off inheritance: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	resolved = getContext(grandchild.ID)
	for _, entry := range resolved.Inherited.Entries {
		if entry.Value == "https://github.com/acme/widget-v2" {
			t.Fatalf("entries of an ancestor with inheritance off must not resolve: %+v", resolved.Inherited.Entries)
		}
	}
	sawOwn := false
	for _, entry := range resolved.Inherited.Entries {
		if entry.Value == "npm run dev" {
			sawOwn = true
		}
	}
	if !sawOwn {
		t.Fatalf("grandchild own entries must survive: %+v", resolved.Inherited.Entries)
	}

	// Clearing the document: null removes it.
	w = httptest.NewRecorder()
	req = withURLParam(newRequest("PUT", "/api/issues/"+root.ID, map[string]any{
		"execution_context": nil,
	}), "id", root.ID)
	testHandler.UpdateIssue(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("clear execution context: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	resolved = getContext(root.ID)
	if len(resolved.Own.Entries) != 0 {
		t.Fatalf("cleared context must come back empty, got %+v", resolved.Own)
	}
}

// TestExecutionContextContentLongtextIntegration covers the SIY-177 feedback
// flow: the execution context written as ONE free-form longtext block. A
// descendant re-asserting the root's text verbatim collapses onto it (still
// exactly one prompt block), different text arrives as an additional block
// with its own provenance, and the issue display payload echoes the content.
func TestExecutionContextContentLongtextIntegration(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	rootText := "仓库地址：https://github.com/acme/widget\n代码目录 Linux: /mnt/a/widget\n启动：./restart.sh"
	create := func(title string, parent string, context map[string]any) IssueResponse {
		body := map[string]any{"title": title}
		if parent != "" {
			body["parent_issue_id"] = parent
		}
		if context != nil {
			body["execution_context"] = context
		}
		w := httptest.NewRecorder()
		req := newRequest("POST", "/api/issues?workspace_id="+testWorkspaceID, body)
		testHandler.CreateIssue(w, req)
		if w.Code != http.StatusCreated {
			t.Fatalf("create %s: expected 201, got %d: %s", title, w.Code, w.Body.String())
		}
		var created IssueResponse
		if err := json.NewDecoder(w.Body).Decode(&created); err != nil {
			t.Fatalf("decode created issue: %v", err)
		}
		t.Cleanup(func() {
			cleanup := withURLParam(newRequest("DELETE", "/api/issues/"+created.ID, nil), "id", created.ID)
			testHandler.DeleteIssue(httptest.NewRecorder(), cleanup)
		})
		return created
	}
	getContext := func(id string) IssueExecutionContextResponse {
		w := httptest.NewRecorder()
		req := withURLParam(newRequest("GET", "/api/issues/"+id+"/execution-context", nil), "id", id)
		testHandler.GetIssueExecutionContext(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("get execution context: expected 200, got %d: %s", w.Code, w.Body.String())
		}
		var response IssueExecutionContextResponse
		if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
			t.Fatalf("decode execution context: %v", err)
		}
		return response
	}

	root := create("Exec ctx content root", "", map[string]any{
		"inherit": true,
		"content": rootText,
	})
	var echoedContext service.ExecutionContext
	if root.ExecutionContext == nil || json.Unmarshal(root.ExecutionContext, &echoedContext) != nil || echoedContext.Content != rootText {
		t.Fatalf("create response must echo the longtext content, got %s", string(root.ExecutionContext))
	}
	// The child re-asserts the root's text verbatim: collapse, not duplication.
	child := create("Exec ctx content child", root.ID, map[string]any{
		"inherit": true,
		"content": rootText,
	})
	grandchild := create("Exec ctx content grandchild", child.ID, map[string]any{
		"inherit": false,
		"content": "孙级专属命令：npm run dev",
	})

	resolved := getContext(grandchild.ID)
	if len(resolved.Inherited.Contents) != 2 {
		t.Fatalf("expected 2 content blocks (verbatim re-assertion collapsed), got %+v", resolved.Inherited.Contents)
	}
	if resolved.Inherited.Contents[0].Text != rootText || resolved.Inherited.Contents[0].SourceIssueID != root.ID {
		t.Fatalf("root content provenance wrong: %+v", resolved.Inherited.Contents[0])
	}
	if resolved.Inherited.Contents[1].SourceIssueID != grandchild.ID {
		t.Fatalf("grandchild content provenance wrong: %+v", resolved.Inherited.Contents[1])
	}
	if !strings.Contains(resolved.Own.Content, "npm run dev") {
		t.Fatalf("grandchild own document must carry its longtext, got %q", resolved.Own.Content)
	}
	if len(resolved.Inherited.Entries) != 0 {
		t.Fatalf("no structured entries in this scenario: %+v", resolved.Inherited.Entries)
	}

	// Refreshing the root's longtext reaches descendants under a bumped
	// version (acceptance 4), replacing the collapsed snapshot.
	w := httptest.NewRecorder()
	req := withURLParam(newRequest("PUT", "/api/issues/"+root.ID, map[string]any{
		"execution_context": map[string]any{"inherit": true, "content": rootText + "\n测试：pnpm test"},
	}), "id", root.ID)
	testHandler.UpdateIssue(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("update root content: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	resolved = getContext(grandchild.ID)
	// The refreshed ROOT text reaches descendants; the child still holds its
	// verbatim copy of the OLD text as its own block (different text survives
	// as an additional block), plus the grandchild's own — three blocks total.
	if len(resolved.Inherited.Contents) != 3 {
		t.Fatalf("refreshed root + child copy + grandchild own = 3 blocks, got %+v", resolved.Inherited.Contents)
	}
	if resolved.Inherited.Contents[0].SourceIssueID != root.ID || !strings.Contains(resolved.Inherited.Contents[0].Text, "pnpm test") {
		t.Fatalf("descendant must see the refreshed longtext from the root: %+v", resolved.Inherited.Contents[0])
	}
	for _, source := range resolved.Inherited.Sources {
		if source.IssueID == root.ID && source.Version < 2 {
			t.Fatalf("root version must bump on refresh: %+v", resolved.Inherited.Sources)
		}
	}
}
