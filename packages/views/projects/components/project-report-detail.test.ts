import { describe, expect, it } from "vitest";
import type { ProjectReportSnapshot, ProjectReportTimelineEvent } from "@multica/core/types";
import {
  buildProjectReportDetailMarkdown,
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
});
