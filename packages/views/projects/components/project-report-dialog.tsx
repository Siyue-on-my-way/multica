"use client";

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { CalendarRange, History, Loader2 } from "lucide-react";
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
import {
  projectReportKeys,
  projectReportDetailOptions,
  projectReportHistoryOptions,
  useCreateProjectReport,
  useSaveProjectReport,
} from "@multica/core/projects";
import type { ProjectReportPeriod } from "@multica/core/types";
import { copyText } from "@multica/ui/lib/clipboard";
import { RichContent } from "../../rich-content";
import { DateOnlyPicker } from "../../common/date-only-picker";
import { TimezoneSelect } from "../../common/timezone-select";
import { useViewingTimezone } from "../../common/use-viewing-timezone";
import { useT } from "../../i18n";

type ReportPeriod = "day" | "week" | "month";
type DialogView = "generate" | "history";

function dateOnlyInTimezone(at: Date, timezone: string): string {
  const parts = new Intl.DateTimeFormat("en-US", {
    timeZone: timezone,
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
    timeZone: timezone,
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
    timeZone: timezone,
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
  const timezone = customTimezone ?? userTimezone;
  const initialRange = useMemo(() => periodRange("week", timezone), [timezone]);
  const [startDate, setStartDate] = useState<string | null>(initialRange[0]);
  const [endDate, setEndDate] = useState<string | null>(initialRange[1]);
  const [dialogView, setDialogView] = useState<DialogView>("generate");
  const [activeJobId, setActiveJobId] = useState<string | null>(null);
  const [selectedReportId, setSelectedReportId] = useState<string | null>(null);
  const [generationError, setGenerationError] = useState<string | null>(null);
  const pollingTimerRef = useRef<number | null>(null);
  const queryClient = useQueryClient();
  const createReport = useCreateProjectReport(workspaceId ?? "", projectId);
  const saveReport = useSaveProjectReport(workspaceId ?? "", projectId);
  const reportHistory = useQuery({
    ...projectReportHistoryOptions(workspaceId ?? "", projectId),
    enabled: Boolean(workspaceId) && open,
  });
  const selectedReport = useQuery({
    ...projectReportDetailOptions(workspaceId ?? "", projectId, selectedReportId ?? ""),
    enabled: Boolean(workspaceId) && open && Boolean(selectedReportId),
  });
  const report = selectedReport.data;
  const isGenerating = createReport.isPending || activeJobId !== null;

  const periodItems = useMemo(() => [
    { value: "day" as const, label: t(($) => $.detail.report_dialog.period_day) },
    { value: "week" as const, label: t(($) => $.detail.report_dialog.period_week) },
    { value: "month" as const, label: t(($) => $.detail.report_dialog.period_month) },
  ], [t]);

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
      setDialogView("generate");
      setActiveJobId(null);
      setSelectedReportId(null);
      setGenerationError(null);
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
  }, [timezone]);

  const invalidRange = !startDate || !endDate || startDate > endDate;

  const handleGenerate = useCallback(() => {
    if (!projectId || invalidRange) return;
    setActiveJobId(null);
    setSelectedReportId(null);
    setGenerationError(null);
    createReport.mutate(
      {
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
    timezone,
    workspaceId,
  ]);

  const handleCopy = useCallback(async () => {
    if (!report?.content) return;
    const copied = await copyText(report.content);
    if (copied) {
      toast.success(t(($) => $.detail.report_dialog.copy_success));
    } else {
      toast.error(t(($) => $.detail.report_dialog.copy_failed));
    }
  }, [report?.content, t]);

  const handleDownload = useCallback(() => {
    if (!report?.content) return;
    const blob = new Blob([report.content], { type: "text/markdown;charset=utf-8" });
    const url = URL.createObjectURL(blob);
    const anchor = document.createElement("a");
    anchor.href = url;
    anchor.download = `${projectName.replace(/[^\p{L}\p{N}]+/gu, "-")}-${startDate ?? "report"}.md`;
    anchor.click();
    URL.revokeObjectURL(url);
  }, [projectName, report?.content, startDate]);

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
    setSelectedReportId(reportId);
  }, []);

  return (
    <Dialog open={open} onOpenChange={handleDialogOpenChange}>
      <DialogContent className="max-h-[85svh] overflow-y-auto sm:max-w-lg">
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
            <div className="grid gap-4 sm:grid-cols-2">
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
                  <RichContent content={report.content} className="w-full text-left" />
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
                {reportHistory.data.map((item) => (
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
                        {formatDateInTimezone(item.range_start, item.timezone)} – {formatDateInTimezone(item.range_end, item.timezone)}
                      </span>
                    </span>
                    <span className="shrink-0 text-caption text-muted-foreground">
                      {formatDateInTimezone(item.created_at, item.timezone)}
                    </span>
                  </Button>
                ))}
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
