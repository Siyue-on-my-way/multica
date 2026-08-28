package service

import "testing"

func TestParseSubissuePlansResponseAssignsStableDraftIDs(t *testing.T) {
	plans, err := parseSubissuePlansResponse(`{"plans":[
		{"name":"上下文优先","items":[{"title":" 合并任务 ","goal":" 保持上下文 "}]},
		{"name":"并行优先","items":[{"title":"A","goal":"a"},{"title":"B","goal":"b"}]}
	]}`)
	if err != nil {
		t.Fatalf("parse plans: %v", err)
	}
	if len(plans) != 2 || plans[0].ID != "plan-1" || plans[1].ID != "plan-2" {
		t.Fatalf("unexpected plans: %+v", plans)
	}
	if plans[0].Items[0].ID != "plan-1-item-1" || plans[0].Items[0].Title != "合并任务" {
		t.Fatalf("unexpected first item: %+v", plans[0].Items[0])
	}
}

func TestParseSubissueDetailsPreservesApprovedStructure(t *testing.T) {
	plan := SubissuePlan{
		ID:   "plan-1",
		Name: "人工确认",
		Items: []SubissuePlanItem{
			{ID: "plan-1-item-1", Title: "用户标题", Goal: "用户目标"},
		},
	}
	out, err := parseSubissueDetailsResponse(`{"subissues":[{
		"id":"plan-1-item-1","title":"模型改名","goal":"模型改目标",
		"description":"详细说明","stage":2,"depends_on_ids":[],
		"suggested_parent_identifier":null,"confidence":0.8
	}]}`, plan)
	if err != nil {
		t.Fatalf("parse details: %v", err)
	}
	if len(out) != 1 || out[0].Title != "用户标题" || out[0].Goal != "用户目标" || out[0].Stage != 2 {
		t.Fatalf("approved structure was not preserved: %+v", out)
	}
}

func TestParseSubissueDetailsRejectsUnknownOrMissingDraftIDs(t *testing.T) {
	plan := SubissuePlan{Items: []SubissuePlanItem{{ID: "item-1", Title: "T", Goal: "G"}}}
	if _, err := parseSubissueDetailsResponse(`{"subissues":[{"id":"other","description":"d"}]}`, plan); err == nil {
		t.Fatal("unknown draft id must fail")
	}
	if _, err := parseSubissueDetailsResponse(`{"subissues":[]}`, plan); err == nil {
		t.Fatal("missing draft item must fail")
	}
}
