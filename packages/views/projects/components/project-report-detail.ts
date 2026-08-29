import type {
  ProjectReportIssue,
  ProjectReportSnapshot,
  ProjectReportTimelineEvent,
  ProjectReportTimelineEventType,
} from "@multica/core/types";

export interface ProjectReportDetailItem {
  issue: ProjectReportIssue;
  event: ProjectReportTimelineEvent;
}

export interface ProjectReportDetailDay {
  dateKey: string;
  items: ProjectReportDetailItem[];
}

export interface ProjectReportDetailMarkdownLabels {
  heading: string;
  range: (start: string, end: string) => string;
  date: (dateKey: string) => string;
  issue: (identifier: string, title: string) => string;
  event: (type: ProjectReportTimelineEventType) => string;
  empty: string;
}

function dateKeyInTimezone(value: string, timezone: string): string {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value.slice(0, 10);

  const parts = new Intl.DateTimeFormat("en-US", {
    timeZone: timezone,
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
  }).formatToParts(date);
  const values = Object.fromEntries(parts.map((part) => [part.type, part.value]));
  return `${values.year}-${values.month}-${values.day}`;
}

/** Return only in-range records, ordered chronologically and grouped by local date. */
export function groupProjectReportDetails(
  snapshot: ProjectReportSnapshot,
  timezone: string,
): ProjectReportDetailDay[] {
  const items = snapshot.issues.flatMap((issue) =>
    (issue.timeline ?? [])
      .filter((event) => event.in_range)
      .map((event) => ({ issue, event })),
  );

  items.sort((left, right) => {
    const occurredAt = new Date(left.event.occurred_at).getTime() - new Date(right.event.occurred_at).getTime();
    if (occurredAt !== 0) return occurredAt;
    const issueOrder = left.issue.identifier.localeCompare(right.issue.identifier);
    if (issueOrder !== 0) return issueOrder;
    return left.event.id.localeCompare(right.event.id);
  });

  const groups = new Map<string, ProjectReportDetailItem[]>();
  for (const item of items) {
    const dateKey = dateKeyInTimezone(item.event.occurred_at, timezone);
    const group = groups.get(dateKey);
    if (group) {
      group.push(item);
    } else {
      groups.set(dateKey, [item]);
    }
  }

  return [...groups].map(([dateKey, groupedItems]) => ({
    dateKey,
    items: groupedItems,
  }));
}

function reportDetailsRecord(event: ProjectReportTimelineEvent): Record<string, unknown> {
  if (!event.details || typeof event.details !== "object" || Array.isArray(event.details)) {
    return {};
  }
  return event.details;
}

function reportValue(value: unknown): string {
  if (typeof value === "string") return value.trim();
  if (typeof value === "number" || typeof value === "boolean") return String(value);
  if (value === null || value === undefined) return "";
  try {
    return JSON.stringify(value);
  } catch {
    return "";
  }
}

function humanizeReportKey(value: string): string {
  return value.replace(/[_-]+/g, " ");
}

function humanizeReportAction(value: string | undefined): string {
  return value ? humanizeReportKey(value.trim()) : "";
}

function summarizeReportDetails(details: Record<string, unknown>, excluded: Set<string>): string {
  return Object.entries(details)
    .filter(([key, value]) => !excluded.has(key) && value !== null && value !== undefined && value !== "")
    .slice(0, 4)
    .map(([key, value]) => `${humanizeReportKey(key)}: ${reportValue(value)}`)
    .join(" · ");
}

/** Convert a raw timeline row into a readable detail entry without inventing facts. */
export function projectReportEventContent(event: ProjectReportTimelineEvent): string {
  const details = reportDetailsRecord(event);

  switch (event.type) {
    case "comment":
      return event.content?.trim() || humanizeReportAction(event.action) || "已记录讨论。";
    case "issue_status_history": {
      const from = reportValue(details.from_status);
      const to = reportValue(details.to_status);
      if (from && to) return `状态：${from} → ${to}`;
      return humanizeReportAction(event.action) || "已记录状态变化。";
    }
    case "agent_task_queue": {
      const error = reportValue(details.error);
      if (error) return `任务失败：${error}`;
      const result = reportValue(details.result);
      if (result && result !== "null") return `任务结果：${result}`;
      const status = reportValue(details.status);
      if (status) return `任务状态：${status}`;
      return humanizeReportAction(event.action) || "已记录 agent task 执行。";
    }
    case "activity_log": {
      const action = humanizeReportAction(event.action);
      const detailText = summarizeReportDetails(details, new Set(["id", "issue_id", "workspace_id"]));
      return [action, event.content?.trim(), detailText].filter(Boolean).join(" · ") || "已记录操作。";
    }
    default:
      return event.content?.trim() || humanizeReportAction(event.action) || "已记录工作活动。";
  }
}

export function buildProjectReportDetailMarkdown(
  snapshot: ProjectReportSnapshot,
  timezone: string,
  labels: ProjectReportDetailMarkdownLabels,
  formatDateTime: (value: string) => string,
): string {
  const groups = groupProjectReportDetails(snapshot, timezone);
  const lines = [`## ${labels.heading}`, "", labels.range(snapshot.range_start, snapshot.range_end), ""];

  if (groups.length === 0) {
    lines.push(labels.empty);
    return lines.join("\n");
  }

  for (const group of groups) {
    lines.push(`### ${labels.date(group.dateKey)}`, "");
    for (const { issue, event } of group.items) {
      const content = projectReportEventContent(event).replace(/\r?\n/g, "\n    ");
      lines.push(`- ${labels.issue(issue.identifier, issue.title)}`);
      lines.push(`  - ${labels.event(event.type)} · ${formatDateTime(event.occurred_at)}：${content}`);
    }
    lines.push("");
  }

  return lines.join("\n").trim();
}
