package service

import (
	"encoding/json"
	"testing"

	"github.com/multica-ai/multica/server/internal/util"
)

func TestExtractDiscussionProposalSnapshotRequiresExplicitStructuredMarker(t *testing.T) {
	tests := []struct {
		name       string
		content    string
		wantOK     bool
		wantTitle  string
		wantAccept string
	}{
		{
			name:    "ordinary markdown stays ordinary",
			content: "{" + `"title":"pasted JSON","items":[]` + "}",
		},
		{
			name:       "fenced sidecar",
			content:    "答案\n```proposal-json\n{\"title\":\"发布方案\",\"items\":[{\"title\":\"上线\"}]}\n```",
			wantOK:     true,
			wantTitle:  "发布方案",
			wantAccept: "",
		},
		{
			name:       "html sidecar normalizes snake case",
			content:    "<proposal-json>{\"items\":[],\"acceptance_criteria\":[\"通过回归\"]}</proposal-json>",
			wantOK:     true,
			wantTitle:  "讨论方案",
			wantAccept: "通过回归",
		},
		{
			name:    "invalid sidecar degrades to ordinary reply",
			content: "```proposal-json\nnot json\n```",
		},
		{
			name:    "invalid items shape degrades to ordinary reply",
			content: "<proposal-json>{\"items\":{\"title\":\"not a list\"}}</proposal-json>",
		},
		{
			name:    "null items shape degrades to ordinary reply",
			content: "<proposal-json>{\"items\":null}</proposal-json>",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			snapshot, ok := ExtractDiscussionProposalSnapshot(test.content)
			if ok != test.wantOK {
				t.Fatalf("recognized = %v, want %v; snapshot = %s", ok, test.wantOK, snapshot)
			}
			if !ok {
				return
			}
			var decoded map[string]any
			if err := json.Unmarshal(snapshot, &decoded); err != nil {
				t.Fatalf("decode snapshot: %v", err)
			}
			if decoded["title"] != test.wantTitle {
				t.Fatalf("title = %v, want %q", decoded["title"], test.wantTitle)
			}
			acceptance, _ := decoded["acceptanceCriteria"].([]any)
			if test.wantAccept == "" && len(acceptance) != 0 {
				t.Fatalf("acceptance criteria = %v, want empty", acceptance)
			}
			if test.wantAccept != "" && (len(acceptance) != 1 || acceptance[0] != test.wantAccept) {
				t.Fatalf("acceptance criteria = %v, want %q", acceptance, test.wantAccept)
			}
		})
	}
}

func TestProposalSourceContextSnapshotAddsLineageWithoutChangingContent(t *testing.T) {
	proposalID := util.MustParseUUID("550e8400-e29b-41d4-a716-446655440000")
	generationID := util.MustParseUUID("550e8400-e29b-41d4-a716-446655440001")
	raw := []byte(`{"source_issue":{"id":"issue-1"},"comment_thread":[],"anchor_comment_id":"comment-1"}`)

	decorated := proposalSourceContextSnapshot(raw, proposalID, generationID, "parallel")
	var snapshot map[string]any
	if err := json.Unmarshal(decorated, &snapshot); err != nil {
		t.Fatalf("decode decorated snapshot: %v", err)
	}
	if snapshot["anchor_comment_id"] != "comment-1" {
		t.Fatalf("anchor comment changed: %v", snapshot["anchor_comment_id"])
	}
	if snapshot["proposal_id"] != util.UUIDToString(proposalID) {
		t.Fatalf("proposal id = %v", snapshot["proposal_id"])
	}
	if snapshot["generation_id"] != util.UUIDToString(generationID) {
		t.Fatalf("generation id = %v", snapshot["generation_id"])
	}
	if snapshot["proposal_strategy"] != "parallel" {
		t.Fatalf("strategy = %v", snapshot["proposal_strategy"])
	}
}
