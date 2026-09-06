package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/multica-ai/multica/server/pkg/llm"
)

// scriptedSubissueLLM replays canned responses in order, keeping the last one
// when the script runs dry, and records every variables map it was given.
type scriptedSubissueLLM struct {
	mu      sync.Mutex
	script  []string
	calls   []map[string]string
	details []llm.GenerateStats
}

func (l *scriptedSubissueLLM) Enabled() bool { return true }

func (l *scriptedSubissueLLM) GenerateJSONTemplate(
	_ context.Context,
	variables map[string]string,
	_, _ string,
	_ float64,
	_ int64,
) (string, error) {
	raw, _, err := l.GenerateJSONTemplateDetailed(context.Background(), variables, "", "", 0, 0)
	return raw, err
}

func (l *scriptedSubissueLLM) GenerateJSONTemplateDetailed(
	_ context.Context,
	variables map[string]string,
	_, _ string,
	_ float64,
	_ int64,
) (string, llm.GenerateStats, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls = append(l.calls, variables)
	if len(l.script) == 0 {
		return "", llm.GenerateStats{}, errors.New("script exhausted")
	}
	raw := l.script[0]
	l.script = l.script[1:]
	stats := llm.GenerateStats{
		PromptTokens:     120,
		CompletionTokens: 340,
		FinishReason:     "stop",
		InputChars:       1000,
		OutputChars:      500,
	}
	l.details = append(l.details, stats)
	return raw, stats, nil
}

func (l *scriptedSubissueLLM) callCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.calls)
}

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

func TestBuildSubissuePlanVariablesIncludesBusinessContext(t *testing.T) {
	variables := buildSubissuePlanVariables(
		SubissueSuggestSourceIssue{
			Identifier:    "SIY-63",
			Title:         "在评论区实现「一键生成子issue」功能",
			Description:   "把评论中的任务拆成可执行的子issue。",
			AncestorBrief: "上级业务：生成子issue",
		},
		"我现在做的是“生成子issue”的业务，请拆分这个功能",
		nil,
		nil,
		"",
		"",
	)
	if variables["issue_description"] != "把评论中的任务拆成可执行的子issue。" {
		t.Fatalf("issue description = %q", variables["issue_description"])
	}
	if variables["ancestor_brief"] != "上级业务：生成子issue" {
		t.Fatalf("ancestor brief = %q", variables["ancestor_brief"])
	}
	if variables["business_context"] != "生成子issue" {
		t.Fatalf("business context = %q, want 生成子issue", variables["business_context"])
	}
}

func TestInferSubissueBusinessUsesExplicitCommentConcept(t *testing.T) {
	issue := SubissueSuggestSourceIssue{
		Title: "在评论区实现「一键生成子issue」功能：AI 批量拆解",
	}
	got := inferSubissueBusiness(issue, "我现在做的是“生成子issue”的业务")
	if got != "生成子issue" {
		t.Fatalf("business = %q, want 生成子issue", got)
	}
}

func TestInferSubissueBusinessUsesBusinessPrefix(t *testing.T) {
	issue := SubissueSuggestSourceIssue{}
	got := inferSubissueBusiness(issue, "请拆分【日报/周报/年报生成】的查询和存储任务")
	if got != "日报/周报/年报生成" {
		t.Fatalf("business = %q, want 日报/周报/年报生成", got)
	}
}

func TestParseSubissuePlansUsesInferredBusinessForGenericModelValues(t *testing.T) {
	plans, err := parseSubissuePlansResponseWithBusiness(`{"plans":[{
		"name":"上下文优先",
		"items":[
			{"title":"接口实现","goal":"实现接口" ,"business":""},
			{"title":"页面接入","goal":"接入页面" ,"business":"整体流程"},
			{"title":"支付对账","goal":"完成对账" ,"business":"支付"}
		]
	}]}`, "生成子issue")
	if err != nil {
		t.Fatalf("parse plans: %v", err)
	}
	items := plans[0].Items
	if items[0].Title != "【生成子issue】接口实现" || items[0].Business != "生成子issue" {
		t.Fatalf("empty business was not inferred: %+v", items[0])
	}
	if items[1].Title != "【生成子issue】页面接入" || items[1].Business != "生成子issue" {
		t.Fatalf("generic business was not inferred: %+v", items[1])
	}
	var paymentItem *SubissuePlanItem
	for index := range items {
		if items[index].Title == "【支付】支付对账" {
			paymentItem = &items[index]
			break
		}
	}
	if paymentItem == nil || paymentItem.Business != "支付" {
		t.Fatalf("specific model business should be preserved: %+v", items)
	}
}

func TestNormalizeSubissuePlanTitlesUsesFallbackBusiness(t *testing.T) {
	items := normalizeSubissuePlanTitlesWithBusiness([]SubissuePlanItem{
		{ID: "item-1", Title: "任务一", Goal: "目标", Business: ""},
		{ID: "item-2", Title: "任务二", Goal: "目标", Business: "整体流程"},
	}, "生成子issue")
	if items[0].Title != "【生成子issue】任务一" || items[1].Title != "【生成子issue】任务二" {
		t.Fatalf("titles did not use fallback business: %+v", items)
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

func TestValidateSubissuePlanCoverage(t *testing.T) {
	identified := []SubissueIdentifiedTask{
		{ID: "t1", Text: "任务一"},
		{ID: "t2", Text: "任务二"},
	}
	full := SubissuePlan{ID: "plan-1", Name: SubissuePlanFullName, Coverage: SubissuePlanCoverageFull, Items: []SubissuePlanItem{
		{ID: "plan-1-item-1", Title: "A", Goal: "a", Kind: SubissuePlanItemImplementation, SourceTaskIDs: []string{"t1", "t2"}},
	}}
	if err := validateSubissuePlanCoverage(identified, []SubissuePlan{full}); err != nil {
		t.Fatalf("merged full coverage must pass: %v", err)
	}

	split := full
	split.Items = []SubissuePlanItem{
		{ID: "plan-1-item-1", Title: "A", Goal: "a", SourceTaskIDs: []string{"t1"}},
		{ID: "plan-1-item-2", Title: "B", Goal: "b", SourceTaskIDs: []string{"t2"}},
	}
	if err := validateSubissuePlanCoverage(identified, []SubissuePlan{split}); err != nil {
		t.Fatalf("split full coverage must pass: %v", err)
	}

	missing := split
	missing.Items = missing.Items[:1]
	err := validateSubissuePlanCoverage(identified, []SubissuePlan{missing})
	if err == nil || !strings.Contains(err.Error(), "misses 1") {
		t.Fatalf("dropped task must fail with the missing id, got %v", err)
	}

	unknown := split
	unknown.Items[0].SourceTaskIDs = []string{"t9"}
	if err := validateSubissuePlanCoverage(identified, []SubissuePlan{unknown}); err == nil {
		t.Fatal("unknown source task reference must fail")
	}

	noRefs := split
	noRefs.Items[0].SourceTaskIDs = nil
	if err := validateSubissuePlanCoverage(identified, []SubissuePlan{noRefs}); err == nil {
		t.Fatal("full plan item without refs must fail")
	}

	if err := validateSubissuePlanCoverage(nil, []SubissuePlan{full}); err == nil {
		t.Fatal("empty identified list must fail")
	}
}

func TestParseSubissuePlansResultMarksFullCoverage(t *testing.T) {
	plans, identified, err := parseSubissuePlansResult(`{
		"identified_tasks":[{"id":"T1","text":" 任务一 "},{"id":"t2","text":"任务二"},{"id":"t2","text":"重复"}],
		"plans":[
			{"name":"上下文优先","items":[{"title":"合并","goal":"合并目标","source_task_ids":["T1","t2"]}]},
			{"name":"并行优先","items":[{"title":"A","goal":"a","source_task_ids":["t1"]},{"title":"B","goal":"b","source_task_ids":["t2"]}]}
		]}`, "")
	if err != nil {
		t.Fatalf("parse plans: %v", err)
	}
	if len(identified) != 2 {
		t.Fatalf("identified = %+v, want 2 tasks (duplicate id dropped, ids normalized)", identified)
	}
	if plans[0].Name != SubissuePlanFullName || plans[0].Coverage != SubissuePlanCoverageFull {
		t.Fatalf("first plan must be marked full: %+v", plans[0])
	}
	if plans[0].Items[0].SourceTaskIDs[0] != "t1" {
		t.Fatalf("source ids must be normalized: %+v", plans[0].Items[0].SourceTaskIDs)
	}
	if err := validateSubissuePlanCoverage(identified, plans); err != nil {
		t.Fatalf("parsed plans must satisfy their own task list: %v", err)
	}
}

func TestSplitSubissueContentSegments(t *testing.T) {
	short := strings.Repeat("一行内容。", 772) // 3088 runes — explicitly must NOT segment
	if segments, ok := splitSubissueContentSegments(short, "c-1"); ok {
		t.Fatalf("short comment must not segment, got %d segments", len(segments))
	}

	var lines []string
	for i := 0; i < 20; i++ {
		lines = append(lines, fmt.Sprintf("任务%d：%s\n", i, strings.Repeat("细", 400)))
	}
	long := strings.Join(lines, "")
	segments, ok := splitSubissueContentSegments(long, "c-1")
	if !ok {
		t.Fatal("over-budget comment must segment")
	}
	if len(segments) < 2 {
		t.Fatalf("expected multiple segments, got %d", len(segments))
	}
	reassembled := 0
	for _, segment := range segments {
		if len([]rune(segment)) > subissueSegmentBudget {
			t.Fatalf("segment exceeds budget: %d runes", len([]rune(segment)))
		}
		reassembled += len([]rune(segment))
	}
	if reassembled < len([]rune(long))-100 {
		t.Fatalf("segments lost content: %d of %d runes", reassembled, len([]rune(long)))
	}
}

func TestMergeSubissueSegmentTasks(t *testing.T) {
	merged := mergeSubissueSegmentTasks([][]SubissueIdentifiedTask{
		{{ID: "t1", Text: "实现上传"}, {ID: "t2", Text: "实现下载"}},
		{{ID: "t1", Text: "实现上传 "}, {ID: "t2", Text: "配置存储"}},
	})
	if len(merged) != 3 {
		t.Fatalf("merged = %+v, want 3 tasks after dedupe", merged)
	}
	for index, want := range []string{"t1", "t2", "t3"} {
		if merged[index].ID != want {
			t.Fatalf("task %d id = %q, want %q", index, merged[index].ID, want)
		}
	}
}

func TestSuggestSubissuePlansIdentifiesTasksInOutlinePass(t *testing.T) {
	planLLM := &scriptedSubissueLLM{script: []string{`{
		"identified_tasks":[{"id":"t1","text":"任务A"},{"id":"t2","text":"任务B"}],
		"plans":[
			{"name":"全量覆盖","items":[
				{"title":"A","goal":"做A","source_task_ids":["t1"]},
				{"title":"B","goal":"做B","source_task_ids":["t2"]}]},
			{"name":"上下文优先","items":[{"title":"合并","goal":"合并目标","source_task_ids":["t1","t2"]}]}
		]}`}}
	recognizeLLM := &scriptedSubissueLLM{}

	plans, err := SuggestSubissuePlans(
		context.Background(),
		SubissuePlanStages{Plan: planLLM, Recognize: recognizeLLM},
		SubissueSuggestSourceIssue{Identifier: "SIY-147", Title: "覆盖校验"},
		"请拆分：任务A、任务B",
		nil, nil, "", "comment-1",
	)
	if err != nil {
		t.Fatalf("suggest plans: %v", err)
	}
	if recognizeLLM.callCount() != 0 {
		t.Fatalf("short comment must not call recognition, got %d calls", recognizeLLM.callCount())
	}
	if planLLM.callCount() != 1 {
		t.Fatalf("outline calls = %d, want 1", planLLM.callCount())
	}
	if plans[0].Coverage != SubissuePlanCoverageFull {
		t.Fatalf("plans[0] = %+v", plans[0])
	}
	implementationCount := 0
	for _, item := range plans[0].Items {
		if item.Kind == SubissuePlanItemImplementation {
			implementationCount++
		}
	}
	if implementationCount != 2 {
		t.Fatalf("plans[0] = %+v", plans[0])
	}
}

func TestSuggestSubissuePlansRetriesUntilCoverage(t *testing.T) {
	incomplete := `{
		"identified_tasks":[{"id":"t1","text":"任务A"},{"id":"t2","text":"任务B"}],
		"plans":[{"name":"全量覆盖","items":[{"title":"A","goal":"做A","source_task_ids":["t1"]}]}]
	}`
	complete := `{
		"identified_tasks":[{"id":"t1","text":"任务A"},{"id":"t2","text":"任务B"}],
		"plans":[{"name":"全量覆盖","items":[
			{"title":"A","goal":"做A","source_task_ids":["t1"]},
			{"title":"B","goal":"做B","source_task_ids":["t2"]}]}]
	}`
	planLLM := &scriptedSubissueLLM{script: []string{incomplete, complete}}

	plans, err := SuggestSubissuePlans(
		context.Background(),
		SubissuePlanStages{Plan: planLLM},
		SubissueSuggestSourceIssue{Identifier: "SIY-147", Title: "覆盖校验"},
		"请拆分：任务A、任务B",
		nil, nil, "", "comment-1",
	)
	if err != nil {
		t.Fatalf("suggest plans: %v", err)
	}
	if planLLM.callCount() != 2 {
		t.Fatalf("outline calls = %d, want 2 (one retry)", planLLM.callCount())
	}
	feedback := planLLM.calls[1]["retry_feedback"]
	if !strings.Contains(feedback, "coverage check failed") || !strings.Contains(feedback, "t2") {
		t.Fatalf("retry feedback must name the missed task, got %q", feedback)
	}
	implementationCount := 0
	for _, item := range plans[0].Items {
		if item.Kind == SubissuePlanItemImplementation {
			implementationCount++
		}
	}
	if implementationCount != 2 {
		t.Fatalf("retried plan items = %+v", plans[0].Items)
	}
}

func TestSuggestSubissuePlansFailsAfterMaxAttempts(t *testing.T) {
	planLLM := &scriptedSubissueLLM{script: []string{`{
		"identified_tasks":[{"id":"t1","text":"任务A"}],
		"plans":[{"name":"全量覆盖","items":[{"title":"A","goal":"做A"}]}]
	}`}}

	_, err := SuggestSubissuePlans(
		context.Background(),
		SubissuePlanStages{Plan: planLLM},
		SubissueSuggestSourceIssue{Identifier: "SIY-147", Title: "覆盖校验"},
		"请拆分：任务A",
		nil, nil, "", "comment-1",
	)
	if err == nil || !strings.Contains(err.Error(), "after 3 attempts") {
		t.Fatalf("exhausted retries must fail with attempt count, got %v", err)
	}
	if planLLM.callCount() != subissuePlanMaxAttempts {
		t.Fatalf("outline calls = %d, want %d", planLLM.callCount(), subissuePlanMaxAttempts)
	}
}

func TestSuggestSubissuePlansSegmentsOverBudgetComments(t *testing.T) {
	recognizeLLM := &scriptedSubissueLLM{script: []string{
		`{"tasks":[{"id":"t1","text":"任务A"}]}`,
		`{"tasks":[{"id":"t1","text":"任务B"}]}`,
	}}
	planLLM := &scriptedSubissueLLM{script: []string{`{
		"plans":[{"name":"全量覆盖","items":[
			{"title":"A","goal":"做A","source_task_ids":["t1"]},
			{"title":"B","goal":"做B","source_task_ids":["t2"]}]}]
	}`}}

	var lines []string
	for i := 0; i < 20; i++ {
		lines = append(lines, fmt.Sprintf("任务%d：%s", i, strings.Repeat("细", 400)))
	}
	longComment := strings.Join(lines, "\n")

	plans, err := SuggestSubissuePlans(
		context.Background(),
		SubissuePlanStages{Plan: planLLM, Recognize: recognizeLLM},
		SubissueSuggestSourceIssue{Identifier: "SIY-147", Title: "分段识别"},
		longComment,
		nil, nil, "", "comment-1",
	)
	if err != nil {
		t.Fatalf("suggest plans: %v", err)
	}
	if recognizeLLM.callCount() < 2 {
		t.Fatalf("recognition calls = %d, want at least one per segment", recognizeLLM.callCount())
	}
	identified := planLLM.calls[0]["identified_tasks"]
	if !strings.Contains(identified, `"t1"`) || !strings.Contains(identified, `"t2"`) || !strings.Contains(identified, "任务A") || !strings.Contains(identified, "任务B") {
		t.Fatalf("outline must receive the merged authoritative task list, got %q", identified)
	}
	if plans[0].Coverage != SubissuePlanCoverageFull {
		t.Fatalf("plans[0] = %+v", plans[0])
	}
}

func TestExpandSubissuePlanRetriesOnIncompleteDetails(t *testing.T) {
	plan := SubissuePlan{ID: "plan-1", Name: SubissuePlanFullName, Items: []SubissuePlanItem{
		{ID: "plan-1-item-1", Title: "A", Goal: "做A"},
		{ID: "plan-1-item-2", Title: "B", Goal: "做B"},
	}}
	incomplete := `{"subissues":[{"id":"plan-1-item-1","description":"A详情","stage":1,"depends_on_ids":[],"confidence":0.9}]}`
	complete := `{"subissues":[
		{"id":"plan-1-item-1","description":"A详情","stage":1,"depends_on_ids":[],"confidence":0.9},
		{"id":"plan-1-item-2","description":"B详情","stage":2,"depends_on_ids":["plan-1-item-1"],"confidence":0.9}
	]}`
	detailLLM := &scriptedSubissueLLM{script: []string{incomplete, complete}}

	details, err := ExpandSubissuePlan(
		context.Background(),
		detailLLM,
		SubissueSuggestSourceIssue{Identifier: "SIY-147", Title: "详情重试"},
		"原文",
		nil, nil, plan, "", "comment-1",
	)
	if err != nil {
		t.Fatalf("expand plan: %v", err)
	}
	if detailLLM.callCount() != 2 {
		t.Fatalf("detail calls = %d, want 2 (one retry)", detailLLM.callCount())
	}
	feedback := detailLLM.calls[1]["retry_feedback"]
	if !strings.Contains(feedback, "expected 2") {
		t.Fatalf("retry feedback must name the mismatch, got %q", feedback)
	}
	if len(details) != 2 || details[1].Stage != 2 {
		t.Fatalf("details = %+v", details)
	}
}
