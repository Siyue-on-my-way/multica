import type {
  ProjectReportBusinessDomain,
  ProjectReportIssue,
  ProjectReportNarrative,
  ProjectReportProjectAnalysis,
  ProjectReportSnapshot,
  ProjectReportTimelineEvent,
  ProjectReportTimelineEventType,
  ProjectReportWorkItem,
} from "@multica/core/types";
import { resolveTimezone } from "../../common/timezone";

export interface ProjectReportDetailItem {
  issue: ProjectReportIssue;
  event: ProjectReportTimelineEvent;
}

export interface ProjectReportDetailDay {
  dateKey: string;
  items: ProjectReportDetailItem[];
}

export interface ProjectReportNarrativeGroup {
  businessDomain: string;
  narratives: ProjectReportNarrative[];
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
    timeZone: resolveTimezone(timezone),
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
      .filter((event) => event.in_range && isProjectReportDiscussionEvent(event))
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

export function isProjectReportDiscussionEvent(event: ProjectReportTimelineEvent): boolean {
  return event.type === "comment" || event.type === "agent_task_queue";
}

function fallbackProjectReportNarrative(issue: ProjectReportIssue): ProjectReportNarrative {
  const done = issue.summary.work_done?.find(Boolean)
    || issue.summary.actions?.find(Boolean)
    || "";
  const evidence = issue.summary.evidence_ids?.filter(Boolean)
    || (issue.timeline ?? [])
      .filter((event) => event.in_range && isProjectReportDiscussionEvent(event))
      .map((event) => event.id);
  return {
    issue_id: issue.issue_id || issue.identifier,
    identifier: issue.identifier,
    title: issue.title,
    business_domain: issue.business_domain || "项目级能力建设",
    status_to: issue.status,
    done,
    outcome: issue.summary.outcome || "",
    evidence: [...new Set(evidence)],
    risks: issue.summary.risks?.filter(Boolean),
    noteworthy: Boolean(done || issue.summary.outcome),
    source: "deterministic",
  };
}

export function getProjectReportNarratives(snapshot: ProjectReportSnapshot): ProjectReportNarrative[] {
  if (snapshot.narratives?.length) return snapshot.narratives;
  return snapshot.issues.map(fallbackProjectReportNarrative);
}

export function groupProjectReportNarratives(
  snapshot: ProjectReportSnapshot,
): ProjectReportNarrativeGroup[] {
  const groups = new Map<string, ProjectReportNarrative[]>();
  for (const narrative of getProjectReportNarratives(snapshot).filter((item) => item.noteworthy)) {
    const businessDomain = narrative.business_domain || "项目级能力建设";
    const group = groups.get(businessDomain);
    if (group) group.push(narrative);
    else groups.set(businessDomain, [narrative]);
  }
  return [...groups].map(([businessDomain, narratives]) => ({ businessDomain, narratives }));
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

const reportCategories = [
  "bug_fix",
  "feature",
  "architecture",
  "design",
  "research",
  "operations",
  "discussion",
  "risk",
  "misc",
] as const;

function normalizeReportCategory(value: string | undefined): string {
  switch (value?.trim().toLowerCase()) {
    case "bug":
    case "bugfix":
    case "defect":
    case "fix":
      return "bug_fix";
    case "function":
    case "capability":
    case "enhancement":
      return "feature";
    case "architect":
    case "refactor":
      return "architecture";
    case "proposal":
    case "solution":
      return "design";
    case "investigation":
    case "analysis":
      return "research";
    case "operation":
    case "ops":
    case "deployment":
    case "release":
      return "operations";
    case "review":
    case "communication":
      return "discussion";
    case "blocker":
    case "blocked":
      return "risk";
    case "misc":
    case "other":
      return "misc";
    default:
      return reportCategories.includes(value?.trim().toLowerCase() as typeof reportCategories[number])
        ? value!.trim().toLowerCase()
        : "misc";
  }
}

function milestoneLabel(category: string): string {
  switch (normalizeReportCategory(category)) {
    case "bug_fix":
      return "问题定位与修复";
    case "feature":
      return "功能与能力交付";
    case "architecture":
      return "架构与配置改造";
    case "design":
      return "需求与方案决策";
    case "research":
      return "调研与问题分析";
    case "operations":
      return "验证与发布运维";
    case "discussion":
      return "讨论与协作决策";
    case "risk":
      return "风险与依赖处理";
    default:
      return "工作推进";
  }
}

function inferReportCategory(issue: ProjectReportIssue): string {
  const text = [
    issue.title,
    issue.description,
    issue.status,
    ...(issue.timeline ?? []).filter((event) => event.in_range).flatMap((event) => [
      event.content ?? "",
      event.action ?? "",
      JSON.stringify(event.details ?? {}),
    ]),
  ].join(" ").toLowerCase();
  const keywords: Array<[string, string[]]> = [
    ["bug_fix", ["bug", "bugfix", "fix", "defect", "error", "crash", "缺陷", "修复", "报错"]],
    ["feature", ["feature", "capability", "enhancement", "功能", "能力", "新增", "支持"]],
    ["architecture", ["architecture", "refactor", "schema", "performance", "架构", "重构", "性能"]],
    ["design", ["design", "proposal", "solution", "方案", "设计", "决策"]],
    ["research", ["research", "investigate", "spike", "调研", "研究", "分析", "排查"]],
    ["operations", ["deploy", "release", "build", "config", "ops", "部署", "发布", "构建", "配置"]],
    ["discussion", ["discussion", "comment", "review", "讨论", "评审", "审核"]],
    ["risk", ["blocked", "blocker", "risk", "dependency", "阻塞", "风险", "依赖"]],
  ];
  return keywords.find(([, values]) => values.some((keyword) => text.includes(keyword)))?.[0] ?? "misc";
}

function fallbackWorkDescription(issue: ProjectReportIssue): string[] {
  const descriptions = (issue.timeline ?? [])
    .filter((event) => event.in_range && isProjectReportDiscussionEvent(event))
    .map(projectReportEventContent)
    .filter(Boolean)
    .slice(0, 8);
  return descriptions.length > 0 ? descriptions : ["已记录本周期工作活动，具体内容待人工确认。"];
}

/** Normalize new snapshots and provide a useful view for older saved reports. */
export function getProjectReportWorkItems(snapshot: ProjectReportSnapshot): ProjectReportWorkItem[] {
  if (snapshot.work_items?.length) {
    return snapshot.work_items.map((item) => {
      const milestones = item.milestones?.filter(Boolean)
        ?? (item.milestone ? [item.milestone] : [milestoneLabel(item.category)]);
      const workDone = item.work_done?.filter(Boolean)
        ?? (item.description ? [item.description] : []);
      return {
        ...item,
        business_domain: item.business_domain || "项目级能力建设",
        milestone: item.milestone || milestones[0],
        milestones,
        work_done: workDone,
        current_state: item.current_state || item.status,
        dependencies: item.dependencies || [],
        risks: item.risks || [],
        deliverables: item.deliverables || [],
        verification: item.verification || [],
        business_impact: item.business_impact || item.impact || "业务影响待确认。",
      };
    });
  }
  return snapshot.issues.map((issue) => {
    const categories = issue.summary.work_types?.map(normalizeReportCategory).filter(Boolean) ?? [];
    const normalizedCategories = categories.length > 0 ? [...new Set(categories)] : [inferReportCategory(issue)];
    const category = normalizedCategories[0] ?? "misc";
    const workDone = issue.summary.work_done?.filter(Boolean) ?? fallbackWorkDescription(issue);
    const evidenceIds = issue.summary.evidence_ids?.filter(Boolean)
      ?? (issue.timeline ?? [])
        .filter((event) => event.in_range && isProjectReportDiscussionEvent(event))
        .map((event) => event.id);
    return {
      id: issue.issue_id || issue.identifier,
      issue_id: issue.issue_id || issue.identifier,
      identifier: issue.identifier,
      issue_title: issue.title,
      business_domain: issue.business_domain || "项目级能力建设",
      milestone: normalizedCategories.map((category) => milestoneLabel(category)).join(" / "),
      milestones: normalizedCategories.map((category) => milestoneLabel(category)),
      category,
      categories: normalizedCategories,
      title: issue.title,
      description: workDone.join("；"),
      work_done: workDone,
      decision: "",
      deliverables: [],
      verification: [],
      current_state: issue.summary.current_state || issue.status,
      dependencies: issue.summary.dependencies || [],
      risks: issue.summary.risks || [],
      outcome: issue.summary.outcome || "当前结果待确认。",
      impact: issue.summary.impact || "业务影响待确认。",
      business_impact: issue.summary.impact || "业务影响待确认。",
      status: issue.status,
      evidence_ids: [...new Set(evidenceIds)],
      confidence: issue.summary.confidence || "low",
      source: issue.summary.summary_source || "deterministic",
    } satisfies ProjectReportWorkItem;
  });
}

function fallbackAnalysisChanges(workItems: ProjectReportWorkItem[]) {
  return workItems.map((item) => ({
    id: `change-${item.id}`,
    category: item.category,
    title: item.title,
    description: item.description,
    impact: item.impact || "业务影响待确认。",
    status: item.status,
    evidence_ids: item.evidence_ids,
    confidence: item.confidence,
    source: "deterministic",
  }));
}

function fallbackBusinessDomains(workItems: ProjectReportWorkItem[]): ProjectReportBusinessDomain[] {
  const domains = new Map<string, ProjectReportBusinessDomain>();
  for (const item of workItems) {
    const name = item.business_domain || "项目级能力建设";
    const existing = domains.get(name);
    const milestones = item.milestones?.length
      ? item.milestones
      : [item.milestone || milestoneLabel(item.category)];
    const milestoneItems = milestones.map((title, index) => ({
      id: `${item.id}-milestone-${index + 1}`,
      business_domain: name,
      title,
      summary: item.description,
      work_item_ids: [item.id],
      status: item.status,
      evidence_ids: item.evidence_ids,
      confidence: item.confidence,
      source: "deterministic",
    }));
    if (existing) {
      existing.work_item_ids = [...new Set([...(existing.work_item_ids ?? []), item.id])];
      existing.evidence_ids = [...new Set([...(existing.evidence_ids ?? []), ...(item.evidence_ids ?? [])])];
      const known = new Set(existing.milestones?.map((milestone) => milestone.title) ?? []);
      existing.milestones = [
        ...(existing.milestones ?? []),
        ...milestoneItems.filter((milestone) => !known.has(milestone.title)),
      ];
    } else {
      domains.set(name, {
        id: `domain-${name}`,
        name,
        summary: item.description,
        work_item_ids: [item.id],
        milestones: milestoneItems,
        business_impact: item.business_impact || item.impact || "业务影响待确认。",
        evidence_ids: item.evidence_ids,
        confidence: item.confidence,
        source: "deterministic",
      });
    }
  }
  return [...domains.values()];
}

/** Return project-level analysis while keeping old report snapshots readable. */
export function getProjectReportAnalysis(
  snapshot: ProjectReportSnapshot,
  workItems = getProjectReportWorkItems(snapshot),
): ProjectReportProjectAnalysis {
  const existing = snapshot.project_analysis;
  const evidenceIds = [...new Set(
    existing?.evidence_ids?.length
      ? existing.evidence_ids
      : workItems.flatMap((item) => item.evidence_ids ?? []),
  )];
  const risks = existing?.risks?.length
    ? existing.risks
    : workItems
      .filter((item) => item.status === "blocked")
      .map((item) => ({
        title: `${item.identifier} 存在阻塞`,
        description: "该 issue 当前处于阻塞状态，解除依赖后才能继续推进。",
        evidence_ids: item.evidence_ids,
        confidence: item.confidence,
        source: "deterministic",
      }));
  const nextSteps = existing?.next_steps?.length
    ? existing.next_steps
    : workItems
      .filter((item) => item.status !== "done" && item.status !== "cancelled")
      .map((item) => ({
        title: `${item.identifier} 后续推进`,
        description: item.status === "blocked"
          ? "先确认依赖和解除阻塞条件，再继续推进。"
          : "继续推进并在完成后更新 issue 状态。",
        evidence_ids: item.evidence_ids,
        confidence: item.confidence,
        source: "deterministic",
      }));
  return {
    summary: existing?.summary || (workItems.length > 0
      ? "本周期的项目变化依据 issue 工作记录生成；业务收益和外部影响仍需结合实际验证确认。"
      : "本周期没有可用于项目变化分析的工作项。"),
    business_domains: existing?.business_domains?.length
      ? existing.business_domains
      : fallbackBusinessDomains(workItems),
    milestones: existing?.milestones?.length
      ? existing.milestones
      : fallbackBusinessDomains(workItems).flatMap((domain) => domain.milestones ?? []),
    changes: existing?.changes?.length ? existing.changes : fallbackAnalysisChanges(workItems),
    risks,
    next_steps: nextSteps,
    evidence_ids: evidenceIds,
    confidence: existing?.confidence || "medium",
    source: existing?.source || "deterministic",
  };
}

export type ProjectReportAudienceView = "summary" | "execution" | "business";

export interface ProjectReportAudienceMarkdownLabels {
  heading: string;
  range: (start: string, end: string) => string;
  issueCount: (count: number) => string;
  summary: string;
  execution: string;
  business: string;
  businessDomains: string;
  milestone: string;
  workItems: string;
  changes: string;
  risks: string;
  nextSteps: string;
  category: (category: string) => string;
  issue: (identifier: string, title: string) => string;
  description: string;
  decision: string;
  deliverables: string;
  verification: string;
  currentState: string;
  dependencies: string;
  itemRisks: string;
  outcome: string;
  status: string;
  impact: string;
  evidence: string;
  noItems: string;
  noRisks: string;
}

function reportMarkdownEvidence(lines: string[], evidenceIds: string[] | undefined, labels: ProjectReportAudienceMarkdownLabels): void {
  lines.push(evidenceIds?.length ? `- ${labels.evidence}：${evidenceIds.join(", ")}` : `- ${labels.evidence}：${labels.noItems}`);
}

function reportMarkdownList(lines: string[], label: string, values: string[] | undefined): void {
  if (!values?.length) return;
  lines.push(`  - ${label}：${values.join("；")}`);
}

function reportMarkdownDomains(
  lines: string[],
  domains: ProjectReportBusinessDomain[] | undefined,
  labels: ProjectReportAudienceMarkdownLabels,
): void {
  lines.push(`### ${labels.businessDomains}`, "");
  if (!domains?.length) {
    lines.push(`- ${labels.noItems}`, "");
    return;
  }
  for (const domain of domains) {
    lines.push(`#### ${domain.name}`, "", domain.summary);
    if (domain.business_impact) lines.push(`- ${labels.impact}：${domain.business_impact}`);
    reportMarkdownEvidence(lines, domain.evidence_ids, labels);
    for (const milestone of domain.milestones ?? []) {
      lines.push(`- **${labels.milestone}：${milestone.title}**（${milestone.status}）`);
      lines.push(`  - ${milestone.summary}`);
      if (milestone.work_item_ids?.length) lines.push(`  - ${labels.workItems}：${milestone.work_item_ids.join(", ")}`);
      reportMarkdownEvidence(lines, milestone.evidence_ids, labels);
    }
    lines.push("");
  }
}

/** Build one copyable audience section without requesting another AI analysis. */
export function buildProjectReportAudienceMarkdown(
  snapshot: ProjectReportSnapshot,
  view: ProjectReportAudienceView,
  labels: ProjectReportAudienceMarkdownLabels,
): string {
  const workItems = getProjectReportWorkItems(snapshot);
  const analysis = getProjectReportAnalysis(snapshot, workItems);
  const lines = [
    `## ${labels.heading}`,
    "",
    labels.range(snapshot.range_start, snapshot.range_end),
    "",
  ];
  if (snapshot.project_title) {
    lines.push(`**${labels.heading} · ${snapshot.project_title}**`, "");
  }

  if (view === "summary") {
    lines.push(`### ${labels.summary}`, "", analysis.summary, "", labels.issueCount(snapshot.active_issue_count ?? snapshot.issues.length), "");
    reportMarkdownDomains(lines, analysis.business_domains, labels);
    lines.push(`### ${labels.changes}`, "");
    const changes = analysis.changes?.slice(0, 5) ?? [];
    if (changes.length === 0) {
      lines.push(`- ${labels.noItems}`, "");
    } else {
      for (const change of changes) {
        lines.push(`- **${labels.category(change.category)}** ${change.title}：${change.description}`);
        lines.push(`  - ${labels.status}：${change.status}`);
        lines.push(`  - ${labels.impact}：${change.impact || labels.noItems}`);
        reportMarkdownEvidence(lines, change.evidence_ids, labels);
      }
      lines.push("");
    }
    lines.push(`### ${labels.risks}`, "");
    const risks = analysis.risks ?? [];
    if (risks.length === 0) {
      lines.push(`- ${labels.noRisks}`, "");
    } else {
      for (const note of risks) {
        lines.push(`- ${note.title}：${note.description}`);
        reportMarkdownEvidence(lines, note.evidence_ids, labels);
      }
      lines.push("");
    }
    return lines.join("\n").trim();
  }

  if (view === "execution") {
    lines.push(`### ${labels.execution}`, "");
    if (workItems.length === 0) {
      lines.push(`- ${labels.noItems}`);
    } else {
      for (const item of workItems) {
        lines.push(`- **${labels.category(item.category)}** ${labels.issue(item.identifier, item.title)}`);
        if (item.business_domain) lines.push(`  - ${labels.businessDomains}：${item.business_domain}`);
        if (item.milestones?.length) lines.push(`  - ${labels.milestone}：${item.milestones.join("、")}`);
        lines.push(`  - ${labels.description}：${item.description}`);
        if (item.decision) lines.push(`  - ${labels.decision}：${item.decision}`);
        reportMarkdownList(lines, labels.deliverables, item.deliverables);
        reportMarkdownList(lines, labels.verification, item.verification);
        lines.push(`  - ${labels.outcome}：${item.outcome}`);
        lines.push(`  - ${labels.currentState}：${item.current_state || item.status}`);
        reportMarkdownList(lines, labels.dependencies, item.dependencies);
        reportMarkdownList(lines, labels.itemRisks, item.risks);
        lines.push(`  - ${labels.status}：${item.status}`);
        reportMarkdownEvidence(lines, item.evidence_ids, labels);
      }
    }
    return lines.join("\n").trim();
  }

  lines.push(`### ${labels.business}`, "", analysis.summary, "");
  reportMarkdownDomains(lines, analysis.business_domains, labels);
  lines.push(`### ${labels.changes}`, "");
  const changes = analysis.changes ?? [];
  if (changes.length === 0) {
    lines.push(`- ${labels.noItems}`);
  } else {
    for (const change of changes) {
      lines.push(`- **${labels.category(change.category)}** ${change.title}：${change.description}`);
      lines.push(`  - ${labels.status}：${change.status}`);
      lines.push(`  - ${labels.impact}：${change.impact || labels.noItems}`);
      reportMarkdownEvidence(lines, change.evidence_ids, labels);
    }
  }
  lines.push("", `### ${labels.nextSteps}`, "");
  const nextSteps = analysis.next_steps ?? [];
  if (nextSteps.length === 0) {
    lines.push(`- ${labels.noItems}`);
  } else {
    for (const note of nextSteps) {
      lines.push(`- ${note.title}：${note.description}`);
      reportMarkdownEvidence(lines, note.evidence_ids, labels);
    }
  }
  return lines.join("\n").trim();
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
