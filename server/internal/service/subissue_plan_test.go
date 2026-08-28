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
	if plans[0].Items[0].ID != "plan-1-item-1" || plans[0].Items[0].Title != "【整体流程】合并任务" {
		t.Fatalf("unexpected first item: %+v", plans[0].Items[0])
	}
	if len(plans[1].Items) != 3 || plans[1].Items[2].Kind != SubissuePlanItemSummaryTest {
		t.Fatalf("multi-item plan must receive a summary test: %+v", plans[1].Items)
	}
}

func TestParseSubissuePlansAddsSummaryTestPerBusiness(t *testing.T) {
	plans, err := parseSubissuePlansResponse(`{"plans":[{
		"name":"并行优先",
		"items":[
			{"title":"登录","goal":"实现登录","business":"认证"},
			{"title":"支付","goal":"实现支付","business":"支付"},
			{"title":"鉴权","goal":"实现鉴权","business":"认证"},
			{"title":"退款","goal":"实现退款","business":"支付"},
			{"title":"对账","goal":"实现对账","business":"支付"}
		]
	}]}`)
	if err != nil {
		t.Fatalf("parse plans: %v", err)
	}
	items := plans[0].Items
	type summaryAt struct {
		index    int
		business string
	}
	expected := []summaryAt{{3, "认证"}, {6, "支付"}}
	for _, want := range expected {
		item := items[want.index]
		if item.Kind != SubissuePlanItemSummaryTest || item.Business != want.business {
			t.Fatalf("item %d = %+v, want %s summary", want.index, item, want.business)
		}
	}
	if len(items) != 7 {
		t.Fatalf("item count = %d, want 5 development items plus 2 summaries", len(items))
	}
}

func TestParseSubissuePlansFormatsBusinessTitles(t *testing.T) {
	plans, err := parseSubissuePlansResponse(`{"plans":[{
		"name":"上下文优先",
		"items":[
			{"title":" 登录 ","goal":"实现登录","business":"认证"},
			{"title":"【支付】登录","goal":"实现登录","business":"认证"}
		]
	}]}`)
	if err != nil {
		t.Fatalf("parse plans: %v", err)
	}
	items := plans[0].Items
	if items[0].Title != "【认证】登录" || items[1].Title != "【认证】登录" {
		t.Fatalf("titles were not normalized by business: %+v", items)
	}
	if items[1].Business != "认证" {
		t.Fatalf("business = %q, want 认证", items[1].Business)
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

func TestNormalizeSubissuePlanTitlesAddsPrefixOnce(t *testing.T) {
	items := normalizeSubissuePlanTitles([]SubissuePlanItem{
		{ID: "item-1", Title: "任务", Goal: "目标", Business: "日报/周报/年报生成"},
		{ID: "item-2", Title: "【旧业务】任务", Goal: "目标", Business: "认证"},
		{ID: "item-3", Title: "任务", Goal: "目标"},
	})
	want := []string{
		"【日报/周报/年报生成】任务",
		"【认证】任务",
		"【整体流程】任务",
	}
	for index, expected := range want {
		if items[index].Title != expected {
			t.Fatalf("title %d = %q, want %q", index, items[index].Title, expected)
		}
	}
}

func TestParseSubissueDetailsKeepsNormalizedApprovedTitle(t *testing.T) {
	plan := SubissuePlan{Items: []SubissuePlanItem{
		{ID: "item-1", Title: "用户标题", Goal: "用户目标", Business: "日报/周报/年报生成"},
	}}
	plan.Items = normalizeSubissuePlanTitles(plan.Items)
	out, err := parseSubissueDetailsResponse(`{"subissues":[{
		"id":"item-1","title":"模型改名","description":"详细说明","stage":2,
		"depends_on_ids":[],"suggested_parent_identifier":null,"confidence":0.8
	}]}`, plan)
	if err != nil {
		t.Fatalf("parse details: %v", err)
	}
	if out[0].Title != "【日报/周报/年报生成】用户标题" {
		t.Fatalf("normalized approved title was not preserved: %+v", out[0])
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

func TestParseSubissueDetailsRejectsExtraDraftItemsAndRestoresOrder(t *testing.T) {
	plan := SubissuePlan{Items: []SubissuePlanItem{
		{ID: "item-1", Title: "First", Goal: "Do first"},
		{ID: "item-2", Title: "Second", Goal: "Do second"},
	}}
	if _, err := parseSubissueDetailsResponse(`{"subissues":[
		{"id":"item-1","description":"first"},
		{"id":"item-2","description":"second"},
		{"id":"item-3","description":"extra"}
	]}`, plan); err == nil {
		t.Fatal("extra draft item must fail")
	}

	out, err := parseSubissueDetailsResponse(`{"subissues":[
		{"id":"item-2","description":"second"},
		{"id":"item-1","description":"first"}
	]}`, plan)
	if err != nil {
		t.Fatalf("parse reordered details: %v", err)
	}
	if out[0].ID != "item-1" || out[1].ID != "item-2" {
		t.Fatalf("details were not restored to approved order: %+v", out)
	}
}

func TestParseSubissueDetailsGatesSummaryTestsAfterTheirBusiness(t *testing.T) {
	plan := SubissuePlan{Items: []SubissuePlanItem{
		{ID: "login", Title: "Login", Goal: "Build login", Kind: SubissuePlanItemImplementation, Business: "认证"},
		{ID: "payment", Title: "Payment", Goal: "Build payment", Kind: SubissuePlanItemImplementation, Business: "支付"},
		{ID: "auth-test", Title: "认证汇总测试", Goal: "Test auth", Kind: SubissuePlanItemSummaryTest, Business: "认证"},
		{ID: "audit", Title: "Audit", Goal: "Build audit", Kind: SubissuePlanItemImplementation, Business: "支付"},
		{ID: "payment-test", Title: "支付汇总测试", Goal: "Test payment", Kind: SubissuePlanItemSummaryTest, Business: "支付"},
	}}
	raw := `{"subissues":[
		{"id":"login","description":"login","stage":2},
		{"id":"payment","description":"payment","stage":5},
		{"id":"auth-test","description":"auth test","stage":9},
		{"id":"audit","description":"audit","stage":3},
		{"id":"payment-test","description":"payment test","stage":1}
	]}`
	out, err := parseSubissueDetailsResponse(raw, plan)
	if err != nil {
		t.Fatalf("parse details: %v", err)
	}
	if out[2].Stage != 3 {
		t.Fatalf("auth summary stage = %d, want 3", out[2].Stage)
	}
	if len(out[2].DependsOnIDs) != 1 || out[2].DependsOnIDs[0] != "login" {
		t.Fatalf("auth summary dependencies = %+v", out[2].DependsOnIDs)
	}
	if out[4].Stage != 6 {
		t.Fatalf("payment summary stage = %d, want 6", out[4].Stage)
	}
	if len(out[4].DependsOnIDs) != 2 || out[4].DependsOnIDs[0] != "payment" || out[4].DependsOnIDs[1] != "audit" {
		t.Fatalf("payment summary dependencies = %+v", out[4].DependsOnIDs)
	}
}
