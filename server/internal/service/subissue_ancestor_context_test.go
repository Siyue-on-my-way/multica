package service

import (
	"context"
	"strings"
	"testing"
)

type subissuePlanAncestorContextTestLLM struct {
	response string
}

func (subissuePlanAncestorContextTestLLM) Enabled() bool { return true }

func (l subissuePlanAncestorContextTestLLM) GenerateJSONTemplate(
	_ context.Context,
	_ map[string]string,
	_ string,
	_ string,
	_ float64,
	_ int64,
) (string, error) {
	return l.response, nil
}

func TestExpandSubissuePlanRetainsOldestAncestorDescription(t *testing.T) {
	plan := SubissuePlan{
		ID:   "plan-1",
		Name: "上下文优先",
		Items: []SubissuePlanItem{
			{ID: "item-1", Title: "实现子任务", Goal: "完成子任务"},
		},
	}
	ancestorBrief := "ANCESTOR_BRIEF (background reference only; current task instructions take precedence)\n\n" +
		"[Background source: Issue SIY-59]\nTitle: multica业务优化\nDescription:\n" +
		"代码地址在：/mnt/a-opensource-tools/multica"
	llm := subissuePlanAncestorContextTestLLM{response: `{"subissues":[{"id":"item-1","description":"实现细节","stage":1,"depends_on_ids":[],"confidence":0.9}]}`}

	details, err := ExpandSubissuePlan(
		context.Background(),
		llm,
		SubissueSuggestSourceIssue{
			Identifier:    "SIY-116",
			Title:         "实现祖先上下文",
			AncestorBrief: ancestorBrief,
		},
		"请生成子任务",
		nil,
		nil,
		plan,
		"",
	)
	if err != nil {
		t.Fatalf("expand subissue plan: %v", err)
	}
	if len(details) != 1 || !strings.Contains(details[0].Description, "代码地址在：/mnt/a-opensource-tools/multica") {
		t.Fatalf("oldest ancestor description missing: %+v", details)
	}
}
