import { describe, expect, it } from "vitest";
import type { ProjectReportSnapshot, ProjectReportTimelineEvent } from "@multica/core/types";
import {
  buildProjectReportAudienceMarkdown,
  buildProjectReportDetailMarkdown,
  getProjectReportAnalysis,
  getProjectReportWorkItems,
  groupProjectReportNarratives,
  groupProjectReportDetails,
  projectReportEventContent,
} from "./project-report-detail";

function event(overrides: Partial<ProjectReportTimelineEvent>): ProjectReportTimelineEvent {
  return {
    id: "event-1",
    type: "comment",
    occurred_at: "2026-08-25T09:00:00.000Z",
    in_range: true,
    ...overrides,
  };
}

function snapshot(timeline: ProjectReportTimelineEvent[]): ProjectReportSnapshot {
  return {
    period_type: "weekly",
    range_start: "2026-08-24T00:00:00.000Z",
    range_end: "2026-08-31T00:00:00.000Z",
    timezone: "UTC",
    generated_at: "2026-08-29T00:00:00.000Z",
    summary_version: 1,
    active_issue_count: 1,
    issues: [{
      issue_id: "issue-1",
      identifier: "RPT-1",
      title: "报告窗口",
      status: "in_progress",
      summary: {
        issue_id: "issue-1",
        problem: "测试",
        actions: [],
        outcome: "进行中",
        open_items: [],
      },
      timeline,
    }],
  };
}

describe("project report detail helpers", () => {
  it("filters historical context and groups in-range records by local date", () => {
    const groups = groupProjectReportDetails(snapshot([
      event({ id: "later", occurred_at: "2026-08-25T09:00:00.000Z", content: "later" }),
      event({ id: "old", occurred_at: "2026-08-24T23:00:00.000Z", in_range: false, content: "context" }),
      event({ id: "earlier", occurred_at: "2026-08-25T08:00:00.000Z", content: "earlier" }),
      event({ id: "next-day", occurred_at: "2026-08-26T08:00:00.000Z", content: "next day" }),
    ]), "UTC");

    expect(groups.map((group) => group.dateKey)).toEqual(["2026-08-25", "2026-08-26"]);
    expect(groups[0]?.items.map(({ event: item }) => item.id)).toEqual(["earlier", "later"]);
    expect(groups.flatMap((group) => group.items).some(({ event: item }) => item.id === "old")).toBe(false);
  });

  it("keeps raw discussions and task results while hiding bookkeeping rows", () => {
    const groups = groupProjectReportDetails(snapshot([
      event({ id: "comment", type: "comment", content: "讨论内容" }),
      event({ id: "task", type: "agent_task_queue", details: { status: "completed", result: "测试通过" } }),
      event({ id: "status", type: "issue_status_history", details: { from_status: "todo", to_status: "done" } }),
      event({ id: "activity", type: "activity_log", action: "issue_updated" }),
    ]), "UTC");

    expect(groups.flatMap((group) => group.items).map(({ event: item }) => item.id)).toEqual(["comment", "task"]);
  });

  it("falls back when an old snapshot contains an invalid timezone", () => {
    expect(() => groupProjectReportDetails(snapshot([
      event({ content: "兼容旧报告" }),
    ]), "Local")).not.toThrow();
  });

  it("renders status, task, and activity records from their stored facts", () => {
    expect(projectReportEventContent(event({
      type: "issue_status_history",
      details: { from_status: "todo", to_status: "in_progress" },
    }))).toBe("状态：todo → in_progress");
    expect(projectReportEventContent(event({
      type: "agent_task_queue",
      details: { status: "completed", result: { summary: "已完成" } },
    }))).toContain("任务结果");
    expect(projectReportEventContent(event({
      type: "activity_log",
      action: "issue_updated",
      details: { field: "status", id: "hidden-id" },
    }))).toContain("issue updated");
  });

  it("builds an exportable detailed record document", () => {
    const content = buildProjectReportDetailMarkdown(
      snapshot([event({ content: "完成接口联调" })]),
      "UTC",
      {
        heading: "详细工作记录",
        range: (start, end) => `${start} - ${end}`,
        date: (date) => date,
        issue: (identifier, title) => `${identifier}: ${title}`,
        event: () => "讨论",
        empty: "暂无记录",
      },
      () => "09:00",
    );

    expect(content).toContain("## 详细工作记录");
    expect(content).toContain("RPT-1: 报告窗口");
    expect(content).toContain("完成接口联调");
  });

  it("derives audience views for snapshots saved before the analysis fields", () => {
    const oldSnapshot = snapshot([event({ id: "evidence-1", content: "完成接口联调" })]);
    const workItems = getProjectReportWorkItems(oldSnapshot);
    const analysis = getProjectReportAnalysis(oldSnapshot, workItems);

    expect(workItems).toHaveLength(1);
    expect(workItems[0]?.evidence_ids).toEqual(["evidence-1"]);
    expect(analysis.changes?.[0]?.title).toBe("报告窗口");

    const content = buildProjectReportAudienceMarkdown(oldSnapshot, "execution", {
      heading: "工作执行版",
      range: (start, end) => `${start} - ${end}`,
      issueCount: (count) => `${count} issues`,
      summary: "综合摘要",
      execution: "工作执行版",
      business: "项目业务版",
      businessDomains: "业务域",
      milestone: "里程碑",
      workItems: "工作项",
      changes: "项目变化",
      risks: "风险",
      nextSteps: "下一步",
      category: (category) => category,
      issue: (identifier, title) => `${identifier}: ${title}`,
      description: "实际工作",
      decision: "决策",
      deliverables: "产出",
      verification: "验证",
      currentState: "当前状态",
      dependencies: "依赖",
      itemRisks: "风险",
      outcome: "结果",
      status: "状态",
      impact: "影响",
      evidence: "证据",
      noItems: "暂无",
      noRisks: "暂无风险",
    });

    expect(content).toContain("RPT-1: 报告窗口");
    expect(content).toContain("完成接口联调");
    expect(content).toContain("evidence-1");
  });

  it("does not show status-only narratives in grouped details", () => {
    const current = snapshot([]);
    current.narratives = [
      {
        issue_id: "status-only",
        identifier: "RPT-2",
        title: "只发生状态变化",
        done: "",
        noteworthy: false,
        source: "deterministic",
      },
      {
        issue_id: "with-work",
        identifier: "RPT-3",
        title: "完成联调",
        done: "完成接口联调",
        noteworthy: true,
        source: "deterministic",
      },
    ];

    expect(groupProjectReportNarratives(current).flatMap((group) => group.narratives).map((item) => item.identifier))
      .toEqual(["RPT-3"]);
  });
});
