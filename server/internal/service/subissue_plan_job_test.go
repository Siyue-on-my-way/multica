package service

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
	openai "github.com/openai/openai-go/v3"
)

func validPlanOnlyContextForTest(t *testing.T) SubissuePlanOnlyContext {
	t.Helper()
	snapshot := SubissuePlanFallbackSnapshot{
		Version:     1,
		WorkspaceID: "00000000-0000-0000-0000-000000000001",
		IssueID:     "00000000-0000-0000-0000-000000000002",
		Discussion:  "split this work",
	}
	digest, _, err := DigestSubissuePlanSnapshot(snapshot)
	if err != nil {
		t.Fatalf("digest snapshot: %v", err)
	}
	return SubissuePlanOnlyContext{
		Type:                 SubissuePlanOnlyContextType,
		JobID:                "00000000-0000-0000-0000-000000000003",
		WorkspaceID:          snapshot.WorkspaceID,
		IssueID:              snapshot.IssueID,
		SourceSnapshotDigest: digest,
		SourceSnapshot:       snapshot,
		Readonly:             true,
		ExpiresAt:            time.Now().Add(time.Minute),
	}
}

func TestParseSubissuePlanOnlyContextRejectsSnapshotScopeMismatch(t *testing.T) {
	value := validPlanOnlyContextForTest(t)
	value.SourceSnapshot.IssueID = "00000000-0000-0000-0000-000000000099"

	if _, err := ParseSubissuePlanOnlyContext(mustMarshalPlanContext(value)); err == nil {
		t.Fatal("ParseSubissuePlanOnlyContext accepted a cross-issue snapshot")
	}
}

func TestParseSubissuePlanOnlyContextRejectsExpiredContext(t *testing.T) {
	value := validPlanOnlyContextForTest(t)
	value.ExpiresAt = time.Now().Add(-time.Second)

	if _, err := ParseSubissuePlanOnlyContext(mustMarshalPlanContext(value)); err == nil {
		t.Fatal("ParseSubissuePlanOnlyContext accepted an expired context")
	}
}

func TestHasSubissuePlanOnlyMarkerKeepsMalformedMarkerOutOfOrdinaryRouting(t *testing.T) {
	task := db.AgentTaskQueue{Context: []byte(`{"type":"plan_only","readonly":false}`)}
	if !HasSubissuePlanOnlyMarker(task) {
		t.Fatal("HasSubissuePlanOnlyMarker did not recognize the malformed plan-only marker")
	}
	if IsSubissuePlanOnlyTask(task) {
		t.Fatal("malformed plan-only marker was treated as a valid task")
	}
}

func TestClassifySubissuePlanLLMErrorRespectsFallbackBoundary(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		err        error
		want       SubissuePlanLLMErrorClass
		degradable bool
	}{
		{name: "not configured", err: ErrLLMNotConfigured, want: SubissuePlanLLMErrorNotConfigured, degradable: true},
		{name: "deadline", err: context.DeadlineExceeded, want: SubissuePlanLLMErrorTimeout, degradable: true},
		{name: "caller cancellation", err: context.Canceled, want: SubissuePlanLLMErrorUnknown},
		{name: "provider authentication", err: &openai.Error{StatusCode: 401}, want: SubissuePlanLLMErrorAuthentication, degradable: true},
		{name: "provider rate limit", err: &openai.Error{StatusCode: 429}, want: SubissuePlanLLMErrorQuota, degradable: true},
		{name: "provider outage", err: &openai.Error{StatusCode: 503}, want: SubissuePlanLLMErrorProvider5xx, degradable: true},
		{name: "bad request", err: &openai.Error{StatusCode: 400}, want: SubissuePlanLLMErrorInvalidRequest},
		{name: "permission", err: errors.New("provider forbidden"), want: SubissuePlanLLMErrorPermission},
		{name: "network", err: fmt.Errorf("transport: %w", errors.New("connection refused")), want: SubissuePlanLLMErrorNetwork, degradable: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ClassifySubissuePlanLLMError(tt.err)
			if got != tt.want {
				t.Fatalf("ClassifySubissuePlanLLMError() = %q, want %q", got, tt.want)
			}
			if got.Degradable() != tt.degradable {
				t.Fatalf("error class %q degradable = %v, want %v", got, got.Degradable(), tt.degradable)
			}
		})
	}
}
