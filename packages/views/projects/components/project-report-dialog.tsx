"use client";

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { CalendarRange, ClipboardList, History, Loader2 } from "lucide-react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { Button } from "@multica/ui/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@multica/ui/components/ui/select";
import {
  Tabs,
  TabsContent,
  TabsList,
  TabsTrigger,
} from "@multica/ui/components/ui/tabs";
import {
  formatDateOnly,
} from "@multica/core/issues/date";
import { api } from "@multica/core/api";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import {
  projectReportKeys,
  projectReportDetailOptions,
  projectReportHistoryOptions,
  projectReportTemplateOptions,
  useCreateProjectReport,
  useSaveProjectReport,
} from "@multica/core/projects";
import type {
  ProjectReport,
  ProjectReportPeriod,
  ProjectReportSnapshot,
  ProjectReportTimelineEvent,
  ProjectReportWorkItem,
} from "@multica/core/types";
import { copyText } from "@multica/ui/lib/clipboard";
import { RichContent } from "../../rich-content";
import { AppLink } from "../../navigation";
import { DateOnlyPicker } from "../../common/date-only-picker";
import { TimezoneSelect } from "../../common/timezone-select";
import { resolveTimezone } from "../../common/timezone";
import { useViewingTimezone } from "../../common/use-viewing-timezone";
import { useT } from "../../i18n";
import {
  buildProjectReportDetailMarkdown,
  getProjectReportWorkItems,
  groupProjectReportNarratives,
  groupProjectReportDetails,
  projectReportEventContent,
} from "./project-report-detail";

type ReportPeriod = "day" | "week" | "month";
type DialogView = "generate" | "history";
type ReportView = "summary" | "details" | "raw";

function readProjectReportSnapshot(report: ProjectReport | undefined): ProjectReportSnapshot | null {
  const value = report?.data_snapshot;
  if (!value || typeof value !== "object") return null;
  const candidate = value as Partial<ProjectReportSnapshot>;
  if (!Array.isArray(candidate.issues)) return null;
  return candidate as ProjectReportSnapshot;
}

function reportAuthorLabel(event: ProjectReportTimelineEvent): string {
  if (event.author_type === "agent") return "agent";
  if (event.author_type === "member") return "member";
  return event.author_type || "system";
}

function reportCategoryLabel(
  t: ReturnType<typeof useT<"projects">>["t"],
  category: string,
): string {
  switch (category) {
    case "bug_fix":
      return t(($) => $.detail.report_dialog.category_bug_fix);
    case "feature":
      return t(($) => $.detail.report_dialog.category_feature);
    case "architecture":
      return t(($) => $.detail.report_dialog.category_architecture);
    case "design":
      return t(($) => $.detail.report_dialog.category_design);
    case "research":
      return t(($) => $.detail.report_dialog.category_research);
    case "operations":
      return t(($) => $.detail.report_dialog.category_operations);
    case "discussion":
      return t(($) => $.detail.report_dialog.category_discussion);
    case "risk":
      return t(($) => $.detail.report_dialog.category_risk);
    default:
      return t(($) => $.detail.report_dialog.category_misc);
  }
}

function ReportEvidenceList({ evidenceIds }: { evidenceIds?: string[] }) {
  const { t } = useT("projects");
  if (!evidenceIds?.length) {
    return <span className="text-muted-foreground">{t(($) => $.detail.report_dialog.evidence_pending)}</span>;
  }
  return (
    <span className="flex flex-wrap gap-1">
      {evidenceIds.map((evidenceId) => (
        <code key={evidenceId} className="rounded bg-muted px-1.5 py-0.5 text-micro">
          {evidenceId}
        </code>
      ))}
    </span>
  );
}

function ReportWarnings({ snapshot }: { snapshot: ProjectReportSnapshot }) {
  const { t } = useT("projects");
  if (!snapshot.analysis_warnings?.length) return null;
  return (
    <div className="rounded-md border border-amber-300/60 bg-amber-50/50 px-3 py-2 text-caption text-amber-900 dark:bg-amber-950/20 dark:text-amber-100">
      <p className="font-medium">{t(($) => $.detail.report_dialog.analysis_warning_label)}</p>
      <ul className="mt-1 list-disc space-y-0.5 pl-5">
        {snapshot.analysis_warnings.map((warning) => <li key={warning}>{warning}</li>)}
      </ul>
    </div>
  );
}

function ReportWorkItemCard({
  item,
}: {
  item: ProjectReportWorkItem;
}) {
  const { t } = useT("projects");
  const paths = useWorkspacePaths();
  return (
    <article className="space-y-2 rounded-lg border bg-background p-4 shadow-sm">
      <div className="flex items-start justify-between gap-3">
        <div className="min-w-0">
          <h3 className="text-body font-medium">{item.title}</h3>
          <div className="flex flex-wrap items-center gap-1.5 text-caption text-muted-foreground">
            <span className="rounded bg-muted px-1.5 py-0.5">{reportCategoryLabel(t, item.category)}</span>
            <AppLink href={paths.issueDetail(item.identifier)} className="text-primary hover:underline">
              {item.identifier}
            </AppLink>
            <span>· {item.status}</span>
          </div>
        </div>
        <span className="shrink-0 text-micro text-muted-foreground">
          {item.confidence || t(($) => $.detail.report_dialog.confidence_low)}
        </span>
      </div>
      <div className="space-y-1 text-caption">
        {item.business_domain && (
          <p><span className="font-medium text-muted-foreground">{t(($) => $.detail.report_dialog.business_domain)} · </span>{item.business_domain}</p>
        )}
        {item.milestones?.length ? (
          <p><span className="font-medium text-muted-foreground">{t(($) => $.detail.report_dialog.milestone)} · </span>{item.milestones.join(" / ")}</p>
        ) : null}
        <p><span className="font-medium text-muted-foreground">{t(($) => $.detail.report_dialog.work_done)} · </span>{item.description}</p>
        {item.decision && <p><span className="font-medium text-muted-foreground">{t(($) => $.detail.report_dialog.decision)} · </span>{item.decision}</p>}
        {item.deliverables?.length ? <p><span className="font-medium text-muted-foreground">{t(($) => $.detail.report_dialog.deliverables)} · </span>{item.deliverables.join("；")}</p> : null}
        {item.verification?.length ? <p><span className="font-medium text-muted-foreground">{t(($) => $.detail.report_dialog.verification)} · </span>{item.verification.join("；")}</p> : null}
        <p><span className="font-medium text-muted-foreground">{t(($) => $.detail.report_dialog.outcome)} · </span>{item.outcome}</p>
        <p><span className="font-medium text-muted-foreground">{t(($) => $.detail.report_dialog.current_state)} · </span>{item.current_state || item.status}</p>
        {item.dependencies?.length ? <p><span className="font-medium text-muted-foreground">{t(($) => $.detail.report_dialog.dependencies)} · </span>{item.dependencies.join("；")}</p> : null}
        {item.risks?.length ? <p><span className="font-medium text-muted-foreground">{t(($) => $.detail.report_dialog.item_risks)} · </span>{item.risks.join("；")}</p> : null}
        <p><span className="font-medium text-muted-foreground">{t(($) => $.detail.report_dialog.impact)} · </span>{item.impact || t(($) => $.detail.report_dialog.impact_pending)}</p>
        <div className="flex flex-wrap items-start gap-1">
          <span className="font-medium text-muted-foreground">{t(($) => $.detail.report_dialog.evidence)} · </span>
          <ReportEvidenceList evidenceIds={item.evidence_ids} />
        </div>
      </div>
    </article>
  );
}

function AudienceSummaryReport({ snapshot }: { snapshot: ProjectReportSnapshot }) {
  const { t } = useT("projects");
  const analysis = snapshot.project_analysis;
  const summary = snapshot.executive_summary?.trim()
    || analysis?.summary?.trim()
    || t(($) => $.detail.report_dialog.no_report_items);
  const activeIssueCount = snapshot.active_issue_count ?? snapshot.issues.length;
  return (
    <div className="w-full space-y-3 text-left">
      <div className="rounded-lg border bg-surface-hover/30 px-4 py-3">
        <p className="text-caption font-medium text-muted-foreground">
          {t(($) => $.detail.report_dialog.layer1_summary)}
        </p>
        <p className="mt-1 whitespace-pre-wrap text-body">{summary}</p>
        <p className="mt-1 text-caption text-muted-foreground">
          {t(($) => $.detail.report_dialog.active_issue_count, {
            count: activeIssueCount,
          })}
        </p>
      </div>
      <ReportWarnings snapshot={snapshot} />
      <div className="rounded-lg border bg-background p-4 shadow-sm">
        <h3 className="text-body font-medium">{t(($) => $.detail.report_dialog.risks_and_next_steps)}</h3>
        <div className="mt-2 space-y-2 text-caption">
          {analysis?.risks?.map((note) => (
            <div key={`risk-${note.title}`}>
              <p className="font-medium">{t(($) => $.detail.report_dialog.risk_label)} · {note.title}</p>
              <p className="text-muted-foreground">{note.description}</p>
            </div>
          ))}
          {analysis?.next_steps?.slice(0, 3).map((note) => (
            <div key={`next-${note.title}`}>
              <p className="font-medium">{t(($) => $.detail.report_dialog.next_step_label)} · {note.title}</p>
              <p className="text-muted-foreground">{note.description}</p>
            </div>
          ))}
          {!analysis?.risks?.length && !analysis?.next_steps?.length && (
            <p className="text-muted-foreground">{t(($) => $.detail.report_dialog.no_risks)}</p>
          )}
        </div>
      </div>
    </div>
  );
}

function ExecutionReport({ snapshot }: { snapshot: ProjectReportSnapshot }) {
  const { t } = useT("projects");
  const workItems = useMemo(() => getProjectReportWorkItems(snapshot), [snapshot]);
  return (
    <div className="w-full space-y-3 text-left">
      <div className="rounded-lg border bg-surface-hover/30 px-4 py-3">
        <p className="text-caption font-medium text-muted-foreground">{t(($) => $.detail.report_dialog.layer2_grouped_details)}</p>
        <p className="mt-1 text-body">{t(($) => $.detail.report_dialog.execution_count, { count: workItems.length })}</p>
      </div>
      {workItems.length ? workItems.map((item) => (
        <ReportWorkItemCard key={item.id} item={item} />
      )) : (
        <div className="rounded-md border border-dashed px-4 py-8 text-center text-caption text-muted-foreground">
          {t(($) => $.detail.report_dialog.no_report_items)}
        </div>
      )}
    </div>
  );
}

function GroupedDetailsReport({ snapshot }: { snapshot: ProjectReportSnapshot }) {
  const { t } = useT("projects");
  const paths = useWorkspacePaths();
  const groups = useMemo(() => groupProjectReportNarratives(snapshot), [snapshot]);
  const detailCount = useMemo(
    () => groups.reduce((count, group) => count + group.narratives.length, 0),
    [groups],
  );
  if (!snapshot.narratives?.length) {
    return <ExecutionReport snapshot={snapshot} />;
  }
  return (
    <div className="w-full space-y-3 text-left">
      <div className="rounded-lg border bg-surface-hover/30 px-4 py-3">
        <p className="text-caption font-medium text-muted-foreground">{t(($) => $.detail.report_dialog.layer2_grouped_details)}</p>
        <p className="mt-1 text-body">{t(($) => $.detail.report_dialog.execution_count, { count: detailCount })}</p>
      </div>
      <ReportWarnings snapshot={snapshot} />
      {groups.length === 0 ? (
        <div className="rounded-md border border-dashed px-4 py-8 text-center text-caption text-muted-foreground">
          {t(($) => $.detail.report_dialog.no_report_items)}
        </div>
      ) : groups.map((group) => (
        <section key={group.businessDomain} className="space-y-3 rounded-lg border bg-background p-4 shadow-sm">
          <h3 className="text-body font-medium">{group.businessDomain}</h3>
          {group.narratives.map((narrative) => (
            <article key={narrative.issue_id} className="border-l-2 border-muted pl-3 text-caption">
              <p className="font-medium">
                <AppLink href={paths.issueDetail(narrative.identifier)} className="text-primary hover:underline">
                  {narrative.identifier}
                </AppLink>
                <span className="text-muted-foreground"> · {narrative.title}</span>
              </p>
              {narrative.noteworthy ? (
                <>
                  {narrative.done && <p className="mt-1 whitespace-pre-wrap">{t(($) => $.detail.report_dialog.work_done)} · {narrative.done}</p>}
                  {narrative.outcome && <p className="mt-1 whitespace-pre-wrap">{t(($) => $.detail.report_dialog.outcome)} · {narrative.outcome}</p>}
                  {narrative.risks?.length ? <p className="mt-1">{t(($) => $.detail.report_dialog.item_risks)} · {narrative.risks.join("；")}</p> : null}
                  <div className="mt-1 flex flex-wrap items-start gap-1 text-muted-foreground">
                    <span>{t(($) => $.detail.report_dialog.evidence)} · </span>
                    <ReportEvidenceList evidenceIds={narrative.evidence} />
                  </div>
                </>
              ) : (
                <p className="mt-1 text-muted-foreground">{t(($) => $.detail.report_dialog.no_report_items)}</p>
              )}
            </article>
          ))}
        </section>
      ))}
    </div>
  );
}

function reportInclusiveEnd(value: string): string {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return new Date(date.getTime() - 1).toISOString();
}

function DetailedReport({
  snapshot,
  fallbackTimezone,
}: {
  snapshot: ProjectReportSnapshot;
  fallbackTimezone: string;
}) {
  const { t } = useT("projects");
  const paths = useWorkspacePaths();
  const timezone = resolveTimezone(snapshot.timezone, fallbackTimezone);
  const groups = useMemo(
    () => groupProjectReportDetails(snapshot, timezone),
    [snapshot, timezone],
  );
  const detailCount = groups.reduce((total, group) => total + group.items.length, 0);
  const eventTypeLabel = useCallback((type: ProjectReportTimelineEvent["type"]) => {
    switch (type) {
      case "comment":
        return t(($) => $.detail.report_dialog.detail_event_comment);
      case "activity_log":
        return t(($) => $.detail.report_dialog.detail_event_activity);
      case "issue_status_history":
        return t(($) => $.detail.report_dialog.detail_event_status);
      case "agent_task_queue":
        return t(($) => $.detail.report_dialog.detail_event_task);
      default:
        return t(($) => $.detail.report_dialog.detail_event_record);
    }
  }, [t]);

  return (
    <div className="w-full space-y-3 text-left">
      <div className="rounded-lg border bg-surface-hover/30 px-4 py-3">
        <div className="flex flex-wrap items-baseline justify-between gap-2">
          <p className="text-caption font-medium text-muted-foreground">
            {t(($) => $.detail.report_dialog.detail_records_title)}
          </p>
          <span className="text-micro text-muted-foreground">
            {t(($) => $.detail.report_dialog.detail_records_count, { count: detailCount })}
          </span>
        </div>
        <p className="mt-1 text-caption text-muted-foreground">
          {formatDateInTimezone(snapshot.range_start, timezone)} – {formatDateInTimezone(reportInclusiveEnd(snapshot.range_end), timezone)}
        </p>
      </div>

      {groups.length === 0 ? (
        <div className="rounded-md border border-dashed px-4 py-8 text-center text-caption text-muted-foreground">
          {t(($) => $.detail.report_dialog.detail_records_empty)}
        </div>
      ) : (
        <div className="max-h-[46svh] space-y-3 overflow-y-auto pr-1">
          {groups.map((group) => (
            <section key={group.dateKey} className="rounded-lg border bg-background p-3 shadow-sm">
              <h3 className="text-caption font-semibold">
                {formatDateInTimezone(`${group.dateKey}T12:00:00.000Z`, timezone)}
              </h3>
              <div className="mt-3 space-y-3">
                {group.items.map(({ issue, event }) => (
                  <article key={`${issue.issue_id}-${event.id}`} className="border-l-2 border-muted pl-3">
                    <div className="flex flex-wrap items-baseline justify-between gap-x-2 gap-y-1">
                      <p className="min-w-0 text-caption">
                        <AppLink
                          href={paths.issueDetail(issue.identifier)}
                          className="font-medium text-primary hover:underline"
                        >
                          {issue.identifier}
                        </AppLink>
                        <span className="text-muted-foreground"> · {issue.title}</span>
                      </p>
                      <span className="text-micro text-muted-foreground">
                        {formatDateTimeInTimezone(event.occurred_at, timezone)}
                      </span>
                    </div>
                    <div className="mt-1 flex flex-wrap items-center gap-1.5 text-micro text-muted-foreground">
                      <span className="rounded bg-muted px-1.5 py-0.5">
                        {eventTypeLabel(event.type)}
                      </span>
                      {event.author_type && <span>{reportAuthorLabel(event)}</span>}
                    </div>
                    <RichContent
                      content={projectReportEventContent(event)}
                      className="mt-1 text-caption"
                    />
                  </article>
                ))}
              </div>
            </section>
          ))}
        </div>
      )}
    </div>
  );
}

function dateOnlyInTimezone(at: Date, timezone: string): string {
  const parts = new Intl.DateTimeFormat("en-US", {
    timeZone: resolveTimezone(timezone),
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
  }).formatToParts(at);
  const values = Object.fromEntries(parts.map((part) => [part.type, part.value]));
  return `${values.year}-${values.month}-${values.day}`;
}

function utcDateOnly(date: Date): string {
  return date.toISOString().slice(0, 10);
}

function periodRange(period: ReportPeriod, timezone: string): [string, string] {
  const today = dateOnlyInTimezone(new Date(), timezone);
  const now = new Date(`${today}T00:00:00Z`);
  if (period === "day") {
    return [today, today];
  }

  if (period === "week") {
    const start = new Date(now);
    start.setUTCDate(now.getUTCDate() - (now.getUTCDay() + 6) % 7);
    const end = new Date(start);
    end.setUTCDate(start.getUTCDate() + 6);
    return [utcDateOnly(start), utcDateOnly(end)];
  }

  const start = new Date(Date.UTC(now.getUTCFullYear(), now.getUTCMonth(), 1));
  const end = new Date(Date.UTC(now.getUTCFullYear(), now.getUTCMonth() + 1, 0));
  return [utcDateOnly(start), utcDateOnly(end)];
}

function periodToApiPeriod(period: ReportPeriod): ProjectReportPeriod {
  return period === "day" ? "daily" : period === "week" ? "weekly" : "monthly";
}

function timezoneOffsetMinutes(timezone: string, at: Date): number {
  const label = new Intl.DateTimeFormat("en-US", {
    timeZone: resolveTimezone(timezone),
    timeZoneName: "longOffset",
  }).formatToParts(at)
    .find((part) => part.type === "timeZoneName")?.value ?? "GMT";
  const match = /^GMT(?:(?<sign>[+-])(?<hours>\d{1,2})(?::?(?<minutes>\d{2}))?)?$/.exec(label);
  if (!match?.groups?.sign) return 0;
  const minutes = Number(match.groups.hours) * 60 + Number(match.groups.minutes ?? 0);
  return match.groups.sign === "-" ? -minutes : minutes;
}

function dateAtStartOfDay(date: string, timezone: string): Date {
  const [year = 0, month = 1, day = 1] = date.split("-").map(Number);
  const localMidnight = Date.UTC(year, month - 1, day);
  let instant = new Date(localMidnight);
  for (let attempt = 0; attempt < 4; attempt += 1) {
    const next = new Date(
      localMidnight - timezoneOffsetMinutes(timezone, instant) * 60_000,
    );
    if (next.getTime() === instant.getTime()) return next;
    instant = next;
  }
  return instant;
}

function nextDateAtStartOfDay(date: string, timezone: string): Date {
  const next = new Date(`${date}T00:00:00Z`);
  next.setUTCDate(next.getUTCDate() + 1);
  return dateAtStartOfDay(next.toISOString().slice(0, 10), timezone);
}

function formatDateInTimezone(value: string, timezone: string): string {
  return new Intl.DateTimeFormat(undefined, {
    year: "numeric",
    month: "short",
    day: "numeric",
    timeZone: resolveTimezone(timezone),
  }).format(new Date(value));
}

function formatDateTimeInTimezone(value: string, timezone: string): string {
  return new Intl.DateTimeFormat(undefined, {
    year: "numeric",
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
    timeZone: resolveTimezone(timezone),
  }).format(new Date(value));
}

export function ProjectReportDialog({
  open,
  onOpenChange,
  projectName,
  projectId,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  projectName: string;
  projectId: string;
}) {
  const { t } = useT("projects");
  const workspaceId = useWorkspaceId();
  const userTimezone = useViewingTimezone();
  const [period, setPeriod] = useState<ReportPeriod>("week");
  const [customTimezone, setCustomTimezone] = useState<string | null>(null);
  const timezone = resolveTimezone(customTimezone ?? userTimezone);
  const initialRange = useMemo(() => periodRange("week", timezone), [timezone]);
  const [startDate, setStartDate] = useState<string | null>(initialRange[0]);
  const [endDate, setEndDate] = useState<string | null>(initialRange[1]);
  const [dialogView, setDialogView] = useState<DialogView>("generate");
  const [activeJobId, setActiveJobId] = useState<string | null>(null);
  const [selectedReportId, setSelectedReportId] = useState<string | null>(null);
  const [generationError, setGenerationError] = useState<string | null>(null);
  const [reportView, setReportView] = useState<ReportView>("summary");
  const [templateId, setTemplateId] = useState<string>("");
  const pollingTimerRef = useRef<number | null>(null);
  const queryClient = useQueryClient();
  const createReport = useCreateProjectReport(workspaceId ?? "", projectId);
  const saveReport = useSaveProjectReport(workspaceId ?? "", projectId);
  const reportTemplates = useQuery({
    ...projectReportTemplateOptions(workspaceId ?? "", periodToApiPeriod(period)),
    enabled: Boolean(workspaceId) && open,
  });
  const reportHistory = useQuery({
    ...projectReportHistoryOptions(workspaceId ?? "", projectId),
    enabled: Boolean(workspaceId) && open,
  });
  const selectedReport = useQuery({
    ...projectReportDetailOptions(workspaceId ?? "", projectId, selectedReportId ?? ""),
    enabled: Boolean(workspaceId) && open && Boolean(selectedReportId),
  });
  const report = selectedReport.data;
  const reportSnapshot = useMemo(() => readProjectReportSnapshot(report), [report]);
  const isGenerating = createReport.isPending || activeJobId !== null;

  const periodItems = useMemo(() => [
    { value: "day" as const, label: t(($) => $.detail.report_dialog.period_day) },
    { value: "week" as const, label: t(($) => $.detail.report_dialog.period_week) },
    { value: "month" as const, label: t(($) => $.detail.report_dialog.period_month) },
  ], [t]);

  const templateItems = useMemo(() => [
    { value: "", label: t(($) => $.detail.report_dialog.template_default) },
    ...(reportTemplates.data ?? []).map((template) => ({
      value: template.id,
      label: template.name,
    })),
  ], [reportTemplates.data, t]);

  // Keep the selection valid when the period (and therefore the fetched
  // template list) changes; the server filters templates by period_type.
  useEffect(() => {
    if (templateId && !templateItems.some((item) => item.value === templateId)) {
      setTemplateId("");
    }
  }, [templateId, templateItems]);

  const handleDialogOpenChange = useCallback((nextOpen: boolean) => {
    if (!nextOpen) {
      if (pollingTimerRef.current !== null) {
        window.clearTimeout(pollingTimerRef.current);
        pollingTimerRef.current = null;
      }
      const [start, end] = periodRange("week", userTimezone);
      setPeriod("week");
      setStartDate(start);
      setEndDate(end);
      setCustomTimezone(null);
      setTemplateId("");
      setDialogView("generate");
      setActiveJobId(null);
      setSelectedReportId(null);
      setGenerationError(null);
      setReportView("summary");
    }
    onOpenChange(nextOpen);
  }, [onOpenChange, userTimezone]);

  useEffect(() => {
    if (!open || !activeJobId || !projectId) return;
    let cancelled = false;

    const poll = async () => {
      try {
        const job = await api.getProjectReportJob(projectId, activeJobId);
        if (cancelled) return;
        if (job.status === "succeeded" && job.report) {
          queryClient.setQueryData(
            projectReportKeys.detail(workspaceId ?? "", projectId, job.report.id),
            job.report,
          );
          setActiveJobId(null);
          setSelectedReportId(job.report.id);
          setGenerationError(null);
          return;
        }
        if (job.status === "failed") {
          // The server retries a failed generation up to max_attempts (a
          // timeout on one attempt is usually recovered by the next). Keep
          // polling while attempts remain instead of showing a dead error;
          // only give up when the retries are exhausted.
          if (job.attempt < job.max_attempts) {
            setGenerationError(
              t(($) => $.detail.report_dialog.generation_retrying, {
                attempt: job.attempt,
                max: job.max_attempts,
              }),
            );
            pollingTimerRef.current = window.setTimeout(poll, 3000);
            return;
          }
          setActiveJobId(null);
          setGenerationError(
            job.error_message || t(($) => $.detail.report_dialog.generation_failed),
          );
          return;
        }
        pollingTimerRef.current = window.setTimeout(poll, 1500);
      } catch (error) {
        if (cancelled) return;
        setActiveJobId(null);
        setGenerationError(
          error instanceof Error
            ? error.message
            : t(($) => $.detail.report_dialog.generation_failed),
        );
      }
    };

    void poll();

    return () => {
      cancelled = true;
      if (pollingTimerRef.current !== null) {
        window.clearTimeout(pollingTimerRef.current);
        pollingTimerRef.current = null;
      }
    };
  }, [activeJobId, open, projectId, queryClient, t, workspaceId]);

  useEffect(() => () => {
    if (pollingTimerRef.current !== null) {
      window.clearTimeout(pollingTimerRef.current);
    }
  }, []);

  const handlePeriodChange = useCallback((next: ReportPeriod) => {
    setPeriod(next);
    const [start, end] = periodRange(next, timezone);
    setStartDate(start);
    setEndDate(end);
    setActiveJobId(null);
    setSelectedReportId(null);
    setGenerationError(null);
    setReportView("summary");
  }, [timezone]);

  const invalidRange = !startDate || !endDate || startDate > endDate;

  const handleGenerate = useCallback(() => {
    if (!projectId || invalidRange) return;
    setActiveJobId(null);
    setSelectedReportId(null);
    setGenerationError(null);
    setReportView("summary");
    createReport.mutate(
      {
        ...(templateId ? { template_id: templateId } : {}),
        period_type: periodToApiPeriod(period),
        range_start: dateAtStartOfDay(
          startDate ?? dateOnlyInTimezone(new Date(), timezone),
          timezone,
        ).toISOString(),
        range_end: nextDateAtStartOfDay(
          endDate ?? dateOnlyInTimezone(new Date(), timezone),
          timezone,
        ).toISOString(),
        timezone,
      },
      {
        onSuccess: (job) => {
          if (job.status === "succeeded" && job.report) {
            queryClient.setQueryData(
              projectReportKeys.detail(workspaceId ?? "", projectId, job.report.id),
              job.report,
            );
            setSelectedReportId(job.report.id);
            setActiveJobId(null);
          } else {
            setActiveJobId(job.job_id);
          }
        },
        onError: (error) => {
          setGenerationError(
            error instanceof Error
              ? error.message
              : t(($) => $.detail.report_dialog.generation_failed),
          );
        },
      },
    );
  }, [
    createReport,
    endDate,
    invalidRange,
    period,
    projectId,
    queryClient,
    startDate,
    t,
    templateId,
    timezone,
    workspaceId,
  ]);

  const reportExportContent = useMemo(() => {
    if (!report?.content) return null;
    if (!reportSnapshot) return report.content;

    const detailTimezone = resolveTimezone(reportSnapshot.timezone, timezone);
    if (reportView !== "raw") return report.content;
    return buildProjectReportDetailMarkdown(
      reportSnapshot,
      detailTimezone,
      {
        heading: t(($) => $.detail.report_dialog.layer3_raw_discussions),
        range: (start, end) => t(($) => $.detail.report_dialog.detail_records_range, {
          start: formatDateInTimezone(start, detailTimezone),
          end: formatDateInTimezone(reportInclusiveEnd(end), detailTimezone),
        }),
        date: (dateKey) => formatDateInTimezone(`${dateKey}T12:00:00.000Z`, detailTimezone),
        issue: (identifier, title) => `${identifier}：${title}`,
        event: (type) => type === "comment"
          ? t(($) => $.detail.report_dialog.detail_event_comment)
          : t(($) => $.detail.report_dialog.detail_event_task),
        empty: t(($) => $.detail.report_dialog.detail_records_empty),
      },
      (value) => formatDateTimeInTimezone(value, detailTimezone),
    );
  }, [report?.content, reportSnapshot, reportView, t, timezone]);

  const fullReportContent = report?.content || null;

  const handleCopy = useCallback(async () => {
    if (!reportExportContent) return;
    const copied = await copyText(reportExportContent);
    if (copied) {
      toast.success(t(($) => $.detail.report_dialog.copy_success));
    } else {
      toast.error(t(($) => $.detail.report_dialog.copy_failed));
    }
  }, [reportExportContent, t]);

  const handleCopyFull = useCallback(async () => {
    if (!fullReportContent) return;
    const copied = await copyText(fullReportContent);
    if (copied) {
      toast.success(t(($) => $.detail.report_dialog.copy_success));
    } else {
      toast.error(t(($) => $.detail.report_dialog.copy_failed));
    }
  }, [fullReportContent, t]);

  const handleDownload = useCallback(() => {
    if (!reportExportContent) return;
    const blob = new Blob([reportExportContent], { type: "text/markdown;charset=utf-8" });
    const url = URL.createObjectURL(blob);
    const anchor = document.createElement("a");
    anchor.href = url;
    anchor.download = `${projectName.replace(/[^\p{L}\p{N}]+/gu, "-")}-${startDate ?? "report"}.md`;
    anchor.click();
    URL.revokeObjectURL(url);
  }, [projectName, reportExportContent, startDate]);

  const handleSave = useCallback(() => {
    if (!report?.content || report.saved_at) return;
    saveReport.mutate(report.id, {
      onSuccess: () => {
        toast.success(t(($) => $.detail.report_dialog.save_success));
      },
      onError: () => {
        toast.error(t(($) => $.detail.report_dialog.save_failed));
      },
    });
  }, [report?.content, report?.id, report?.saved_at, saveReport, t]);

  const handleSelectHistoryReport = useCallback((reportId: string) => {
    setDialogView("generate");
    setActiveJobId(null);
    setGenerationError(null);
    setReportView("summary");
    setSelectedReportId(reportId);
  }, []);

  return (
    <Dialog open={open} onOpenChange={handleDialogOpenChange}>
      <DialogContent className="max-h-[85svh] overflow-y-auto sm:max-w-3xl">
        <DialogHeader>
          <DialogTitle>{t(($) => $.detail.report_dialog.title)}</DialogTitle>
          <DialogDescription>
            {t(($) => $.detail.report_dialog.description, { name: projectName })}
          </DialogDescription>
        </DialogHeader>

        <Tabs value={dialogView} onValueChange={(value) => setDialogView(value as DialogView)}>
          <TabsList className="grid w-full grid-cols-2">
            <TabsTrigger value="generate">
              {t(($) => $.detail.report_dialog.generate_tab)}
            </TabsTrigger>
            <TabsTrigger value="history">
              {t(($) => $.detail.report_dialog.history_tab)}
            </TabsTrigger>
          </TabsList>

          <TabsContent value="generate" className="space-y-4 pt-4">
            <div className="grid gap-4 sm:grid-cols-3">
              <label className="space-y-1.5">
                <span className="text-caption text-muted-foreground">
                  {t(($) => $.detail.report_dialog.period_label)}
                </span>
                <Select
                  items={periodItems}
                  value={period}
                  onValueChange={(next) => {
                    if (next) handlePeriodChange(next);
                  }}
                >
                  <SelectTrigger
                    className="w-full"
                    aria-label={t(($) => $.detail.report_dialog.period_label)}
                  >
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent align="start">
                    {periodItems.map((item) => (
                      <SelectItem key={item.value} value={item.value}>
                        {item.label}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </label>

              <label className="space-y-1.5">
                <span className="text-caption text-muted-foreground">
                  {t(($) => $.detail.report_dialog.template_label)}
                </span>
                <Select
                  items={templateItems}
                  value={templateId}
                  onValueChange={(next) => {
                    if (next !== null) setTemplateId(next);
                  }}
                  disabled={templateItems.length <= 1}
                >
                  <SelectTrigger
                    className="w-full"
                    aria-label={t(($) => $.detail.report_dialog.template_label)}
                  >
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent align="start">
                    {templateItems.map((item) => (
                      <SelectItem key={item.value || "default"} value={item.value}>
                        {item.label}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </label>

              <label className="space-y-1.5">
                <span className="text-caption text-muted-foreground">
                  {t(($) => $.detail.report_dialog.timezone_label)}
                </span>
                <TimezoneSelect
                  value={timezone}
                  onValueChange={setCustomTimezone}
                  browserSuffix={t(($) => $.detail.report_dialog.browser_timezone_suffix)}
                />
              </label>
            </div>

            <div className="grid gap-4 sm:grid-cols-2">
              <div className="space-y-1.5">
                <p className="text-caption text-muted-foreground">
                  {t(($) => $.detail.report_dialog.start_date_label)}
                </p>
                <DateOnlyPicker
                  value={startDate}
                  onChange={setStartDate}
                  icon={<CalendarRange className="h-3.5 w-3.5 text-muted-foreground" />}
                  placeholder={t(($) => $.detail.report_dialog.select_date)}
                  clearLabel={t(($) => $.detail.report_dialog.clear_date)}
                  triggerRender={
                    <Button
                      type="button"
                      variant="outline"
                      className="h-8 w-full justify-start font-normal"
                    >
                      {startDate ? formatDateOnly(startDate) : t(($) => $.detail.report_dialog.select_date)}
                    </Button>
                  }
                  align="start"
                />
              </div>
              <div className="space-y-1.5">
                <p className="text-caption text-muted-foreground">
                  {t(($) => $.detail.report_dialog.end_date_label)}
                </p>
                <DateOnlyPicker
                  value={endDate}
                  onChange={setEndDate}
                  icon={<CalendarRange className="h-3.5 w-3.5 text-muted-foreground" />}
                  placeholder={t(($) => $.detail.report_dialog.select_date)}
                  clearLabel={t(($) => $.detail.report_dialog.clear_date)}
                  triggerRender={
                    <Button
                      type="button"
                      variant="outline"
                      className="h-8 w-full justify-start font-normal"
                    >
                      {endDate ? formatDateOnly(endDate) : t(($) => $.detail.report_dialog.select_date)}
                    </Button>
                  }
                  align="end"
                />
              </div>
            </div>

            <p className="rounded-md bg-surface-hover/60 px-3 py-2 text-caption text-muted-foreground">
              {t(($) => $.detail.report_dialog.scope_hint)}
            </p>

            {reportSnapshot && report?.content && (
              <div className="flex flex-wrap gap-1 rounded-md border bg-surface-hover/30 p-1">
                {([
                  ["summary", t(($) => $.detail.report_dialog.layer1_summary)],
                  ["details", t(($) => $.detail.report_dialog.layer2_grouped_details)],
                  ["raw", t(($) => $.detail.report_dialog.layer3_raw_discussions)],
                ] as const).map(([value, label]) => (
                  <Button
                    key={value}
                    type="button"
                    size="sm"
                    variant={reportView === value ? "default" : "ghost"}
                    className="h-7 flex-1 text-micro sm:flex-none"
                    disabled={isGenerating}
                    aria-pressed={reportView === value}
                    onClick={() => setReportView(value)}
                  >
                    {value === "raw" && <ClipboardList className="h-3.5 w-3.5" />}
                    {label}
                  </Button>
                ))}
              </div>
            )}

            <div className="min-h-40 rounded-md border border-dashed p-4">
              <div className="flex h-full min-h-32 items-center justify-center text-center text-caption text-muted-foreground">
                {isGenerating ? (
                  <span className="flex items-center gap-2">
                    <Loader2 className="h-4 w-4 animate-spin" />
                    {t(($) => $.detail.report_dialog.generating)}
                  </span>
                ) : generationError ? (
                  <span className="text-destructive">{generationError}</span>
                ) : report?.content ? (
                  reportSnapshot ? (
                    reportView === "raw" ? <DetailedReport snapshot={reportSnapshot} fallbackTimezone={timezone} />
                      : reportView === "details" ? <GroupedDetailsReport snapshot={reportSnapshot} />
                        : <AudienceSummaryReport snapshot={reportSnapshot} />
                  ) : (
                    <RichContent content={report.content} className="w-full text-left" />
                  )
                ) : (
                  t(($) => $.detail.report_dialog.preview_placeholder)
                )}
              </div>
            </div>

            <div className="flex flex-wrap gap-2">
              <Button
                type="button"
                variant="outline"
                size="sm"
                disabled={isGenerating || !report?.content}
                onClick={handleGenerate}
              >
                {t(($) => $.detail.report_dialog.regenerate)}
              </Button>
              <Button
                type="button"
                variant="outline"
                size="sm"
                disabled={!report?.content}
                onClick={handleCopy}
              >
                {t(($) => $.detail.report_dialog.copy_markdown)}
              </Button>
              <Button
                type="button"
                variant="outline"
                size="sm"
                disabled={!fullReportContent}
                onClick={handleCopyFull}
              >
                {t(($) => $.detail.report_dialog.copy_full_markdown)}
              </Button>
              <Button
                type="button"
                variant="outline"
                size="sm"
                disabled={!report?.content}
                onClick={handleDownload}
              >
                {t(($) => $.detail.report_dialog.download)}
              </Button>
              <Button
                type="button"
                variant="outline"
                size="sm"
                disabled={!report?.content || Boolean(report?.saved_at) || saveReport.isPending}
                onClick={handleSave}
              >
                {report?.saved_at
                  ? t(($) => $.detail.report_dialog.saved_history)
                  : t(($) => $.detail.report_dialog.save_history)}
              </Button>
            </div>
          </TabsContent>

          <TabsContent value="history" className="space-y-3 pt-4">
            {reportHistory.isLoading ? (
              <div className="flex min-h-32 items-center justify-center">
                <Loader2 className="h-4 w-4 animate-spin" />
              </div>
            ) : !reportHistory.data?.length ? (
              <div className="flex min-h-32 items-center justify-center text-center text-caption text-muted-foreground">
                {t(($) => $.detail.report_dialog.history_empty)}
              </div>
            ) : (
              <div className="max-h-72 space-y-2 overflow-y-auto">
                {reportHistory.data.map((item) => {
                  const historyTimezone = resolveTimezone(item.timezone, timezone);
                  return (
                    <Button
                      key={item.id}
                      type="button"
                      variant="ghost"
                      className="h-auto w-full justify-start gap-3 px-2 py-2 text-left"
                      onClick={() => handleSelectHistoryReport(item.id)}
                    >
                      <History className="h-4 w-4 shrink-0 text-muted-foreground" />
                      <span className="min-w-0 flex-1">
                        <span className="block truncate text-body">
                          {item.period_type === "daily"
                            ? t(($) => $.detail.report_dialog.period_day)
                            : item.period_type === "weekly"
                              ? t(($) => $.detail.report_dialog.period_week)
                              : t(($) => $.detail.report_dialog.period_month)}
                        </span>
                        <span className="block truncate text-caption text-muted-foreground">
                          {formatDateInTimezone(item.range_start, historyTimezone)} – {formatDateInTimezone(item.range_end, historyTimezone)}
                        </span>
                      </span>
                      <span className="shrink-0 text-caption text-muted-foreground">
                        {formatDateInTimezone(item.created_at, historyTimezone)}
                      </span>
                    </Button>
                  );
                })}
              </div>
            )}
          </TabsContent>
        </Tabs>

        <DialogFooter>
          <Button type="button" variant="outline" size="sm" onClick={() => handleDialogOpenChange(false)}>
            {t(($) => $.detail.report_dialog.cancel)}
          </Button>
          <Button
            type="button"
            size="sm"
            disabled={invalidRange || isGenerating}
            onClick={handleGenerate}
            aria-busy={isGenerating}
          >
            {isGenerating
              ? t(($) => $.detail.report_dialog.generating)
              : t(($) => $.detail.report_dialog.generate)}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
