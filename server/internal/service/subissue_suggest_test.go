package service

import (
	"strings"
	"testing"
)

func TestParseSubissueSuggestResponseParsesAndNormalizes(t *testing.T) {
	raw := `{"subissues":[
		{"title":" Do the thing ","description":" desc ","stage":0,"depends_on_titles":[],"suggested_parent_identifier":" SIY-30 ","confidence":1.5},
		{"title":"Second","description":"d2","stage":2,"suggested_parent_identifier":null,"confidence":-1}
	]}`

	out, err := parseSubissueSuggestResponse(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("expected 2 suggestions, got %d", len(out))
	}

	first := out[0]
	if first.Title != "Do the thing" || first.Description != "desc" {
		t.Fatalf("title/description must be trimmed: %+v", first)
	}
	if first.Stage != 1 {
		t.Fatalf("stage < 1 must clamp to 1, got %d", first.Stage)
	}
	if first.SuggestedParentIdentifier == nil || *first.SuggestedParentIdentifier != " SIY-30 " {
		t.Fatalf("identifier must survive verbatim for the caller to resolve, got %v", first.SuggestedParentIdentifier)
	}
	if first.Confidence != 0 {
		t.Fatalf("out-of-range confidence must clamp to 0, got %v", first.Confidence)
	}

	second := out[1]
	if second.Stage != 2 {
		t.Fatalf("valid stage must survive unchanged, got %d", second.Stage)
	}
	if second.SuggestedParentIdentifier != nil {
		t.Fatalf("nil identifier must stay nil, got %v", second.SuggestedParentIdentifier)
	}
	if second.Confidence != 0 {
		t.Fatalf("negative confidence must clamp to 0, got %v", second.Confidence)
	}
}

func TestParseSubissueSuggestResponseDropsBlankTitles(t *testing.T) {
	out, err := parseSubissueSuggestResponse(`{"subissues":[{"title":"   ","description":"d"},{"title":"Keep me","description":"d2"}]}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 1 || out[0].Title != "Keep me" {
		t.Fatalf("blank-title entries must be dropped, got %+v", out)
	}
}

func TestParseSubissueSuggestResponseRejectsInvalidJSON(t *testing.T) {
	if _, err := parseSubissueSuggestResponse("not json"); err == nil {
		t.Fatal("expected an error for malformed JSON")
	}
}

func TestParseSubissueSuggestResponseEmptyIdentifierBecomesNil(t *testing.T) {
	out, err := parseSubissueSuggestResponse(`{"subissues":[{"title":"T","description":"d","suggested_parent_identifier":"   "}]}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out[0].SuggestedParentIdentifier != nil {
		t.Fatalf("whitespace-only identifier must become nil, got %v", out[0].SuggestedParentIdentifier)
	}
}

func TestBuildSubissueSuggestPromptIncludesAllSections(t *testing.T) {
	prompt := buildSubissueSuggestPrompt(
		SubissueSuggestSourceIssue{Identifier: "SIY-63", Title: "生成子issue"},
		"拆解这段内容",
		[]SubissueCandidateParent{{Identifier: "SIY-64", Title: "sibling"}},
		[]SubissueCandidateParent{{Identifier: "SIY-63", Title: "生成子issue"}, {Identifier: "SIY-30", Title: "parent candidate"}},
	)

	for _, want := range []string{
		"拆解这段内容",
		"SIY-63 生成子issue",
		"SIY-64 sibling",
		"SIY-30 parent candidate",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
}

func TestBuildSubissueSuggestPromptHandlesEmptyLists(t *testing.T) {
	prompt := buildSubissueSuggestPrompt(
		SubissueSuggestSourceIssue{Identifier: "SIY-1", Title: "t"},
		"content",
		nil,
		nil,
	)
	if strings.Count(prompt, "(无)") != 2 {
		t.Fatalf("empty siblings and candidate parents must both render (无):\n%s", prompt)
	}
}

func TestTruncateSubissueSuggestContentKeepsHeadAndTail(t *testing.T) {
	long := strings.Repeat("字", subissueSuggestCommentBudget+2000)
	out := truncateSubissueSuggestContent(long)
	if !strings.Contains(out, "[truncated]") {
		t.Fatalf("over-budget content must be marked truncated")
	}
	if len([]rune(out)) >= len([]rune(long)) {
		t.Fatalf("truncated content must be shorter than the original")
	}
}

func TestTruncateSubissueSuggestContentLeavesShortContentAlone(t *testing.T) {
	short := "short comment"
	if out := truncateSubissueSuggestContent(short); out != short {
		t.Fatalf("short content must pass through unchanged, got %q", out)
	}
}
