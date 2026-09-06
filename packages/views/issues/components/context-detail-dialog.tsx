import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { api } from "@multica/core/api";
import { issueKeys } from "@multica/core/issues/queries";
import type { TaskContext, TaskContextSection } from "@multica/core/types/task-context";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import {
  Tabs,
  TabsContent,
  TabsList,
  TabsTrigger,
} from "@multica/ui/components/ui/tabs";
import { useT } from "../../i18n";
import { formatTokens } from "../../runtimes/utils";

// SIY-125: three-layer context detail. Overview summarizes the run's resume
// and workdir state; Token breakdown shows each section's share of the input;
// Field details lists every delivered section with its delivery channel,
// injection flag, and a redacted preview, expanding the full (still-redacted)
// raw text per section on demand behind an owner/admin-gated fetch.

const DELIVERY_LABEL: Record<string, string> = {
  provider_prompt: "Provider prompt",
  workdir_file: "Workdir file",
  provider_mcp: "Provider MCP",
  platform_readable: "Platform-readable",
};

export function ContextDetailDialog({
  taskId,
  open,
  onOpenChange,
}: {
  taskId: string;
  open: boolean;
  onOpenChange: (open: boolean) => void;
}) {
  const { t } = useT("issues");
  const { data, isLoading, error } = useQuery({
    queryKey: issueKeys.taskContext(taskId),
    queryFn: () => api.getTaskContext(taskId),
    enabled: open,
    staleTime: 60_000,
  });

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="!max-w-3xl !w-[calc(100vw-4rem)]">
        <DialogHeader>
          <DialogTitle>{t(($) => $.context.dialog_title)}</DialogTitle>
          <DialogDescription>{t(($) => $.context.dialog_subtitle)}</DialogDescription>
        </DialogHeader>

        {isLoading ? (
          <p className="text-caption text-muted-foreground">{t(($) => $.context.loading)}</p>
        ) : error ? (
          <p className="text-caption text-destructive">{t(($) => $.context.error)}</p>
        ) : !data || data.resume_actual === "unknown" ? (
          <p className="text-caption text-muted-foreground">{t(($) => $.context.no_data)}</p>
        ) : (
          <Tabs defaultValue="overview" className="mt-2">
            <TabsList>
              <TabsTrigger value="overview">{t(($) => $.context.tab_overview)}</TabsTrigger>
              <TabsTrigger value="tokens">{t(($) => $.context.tab_tokens)}</TabsTrigger>
              <TabsTrigger value="sections">{t(($) => $.context.tab_sections)}</TabsTrigger>
            </TabsList>
            <TabsContent value="overview">
              <OverviewLayer ctx={data} />
            </TabsContent>
            <TabsContent value="tokens">
              <TokenBreakdownLayer ctx={data} />
            </TabsContent>
            <TabsContent value="sections">
              <SectionsLayer taskId={taskId} ctx={data} />
            </TabsContent>
          </Tabs>
        )}
      </DialogContent>
    </Dialog>
  );
}

function OverviewLayer({ ctx }: { ctx: TaskContext }) {
  const { t } = useT("issues");
  const rows: [string, string | undefined][] = [
    [t(($) => $.context.field_task_id), ctx.task_id],
    [t(($) => $.context.field_provider), ctx.provider],
    [t(($) => $.context.field_runtime), ctx.runtime_id],
    [
      t(($) => $.context.field_session_expected),
      ctx.resume_expected ? t(($) => $.context.yes) : t(($) => $.context.no),
    ],
    [t(($) => $.context.field_session_actual), ctx.resume_actual],
    [t(($) => $.context.field_workdir), ctx.workdir_reused ? t(($) => $.context.reused) : t(($) => $.context.fresh)],
    [t(($) => $.context.field_fallback_reason), ctx.fallback_reason],
    [t(($) => $.context.field_session_id), ctx.session_id || "—"],
    [t(($) => $.context.field_prompt_bytes), String(ctx.prompt_bytes)],
  ];
  return (
    <dl className="grid grid-cols-[max-content_1fr] gap-x-4 gap-y-1.5 text-caption">
      {rows
        .filter(([, v]) => v !== undefined && v !== "")
        .map(([k, v]) => (
          <div key={k} className="contents">
            <dt className="text-muted-foreground">{k}</dt>
            <dd className="min-w-0 truncate font-mono">{v}</dd>
          </div>
        ))}
    </dl>
  );
}

function TokenBreakdownLayer({ ctx }: { ctx: TaskContext }) {
  const { t } = useT("issues");
  const total = ctx.input_tokens;
  const injectedSections = ctx.sections.filter((s) => s.token_count > 0);
  return (
    <div className="space-y-2">
      <div className="text-caption text-muted-foreground">
        {ctx.token_mode === "exact"
          ? t(($) => $.context.tokens_exact)
          : t(($) => $.context.tokens_estimated)}{" "}
        <span className="font-mono">{formatTokens(total)}</span>
      </div>
      {total <= 0 ? null : (
        <div className="flex h-2 w-full overflow-hidden rounded-full bg-muted">
          {injectedSections.map((s) => {
            const pct = Math.max((s.token_count / total) * 100, 2);
            return (
              <div
                key={s.key}
                title={`${s.key}: ${formatTokens(s.token_count)}`}
                className="h-full bg-primary/60"
                style={{ width: `${pct}%` }}
              />
            );
          })}
        </div>
      )}
      <table className="w-full text-caption">
        <thead>
          <tr className="text-left text-muted-foreground">
            <th className="py-1 font-medium">{t(($) => $.context.col_section)}</th>
            <th className="py-1 font-medium">{t(($) => $.context.col_delivery)}</th>
            <th className="py-1 text-right font-medium">{t(($) => $.context.col_tokens)}</th>
          </tr>
        </thead>
        <tbody>
          {ctx.sections.map((s) => (
            <tr key={s.key} className="border-t border-border/60">
              <td className="py-1 font-mono">{s.key}</td>
              <td className="py-1 text-muted-foreground">{DELIVERY_LABEL[s.delivery] ?? s.delivery}</td>
              <td className="py-1 text-right tabular-nums">{formatTokens(s.token_count)}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function SectionsLayer({ taskId, ctx }: { taskId: string; ctx: TaskContext }) {
  const { t } = useT("issues");
  const [expanded, setExpanded] = useState<string | null>(null);
  // On-demand raw fetch, gated server-side by owner/admin. A non-owner caller
  // receives no raw (the server strips it), and the toggle reads "unavailable".
  const { data: rawData, isFetching } = useQuery({
    queryKey: issueKeys.taskContext(`${taskId}:raw:${expanded}`),
    queryFn: () => api.getTaskContext(taskId, expanded ?? undefined),
    enabled: expanded !== null,
    staleTime: 60_000,
  });
  const rawSection: TaskContextSection | undefined = rawData?.sections.find(
    (s) => s.key === expanded,
  );

  return (
    <div className="space-y-1.5">
      {ctx.sections.map((s) => (
        <div key={s.key} className="rounded border border-border/60 p-2">
          <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-caption">
            <span className="font-mono font-medium">{s.key}</span>
            <span className="text-muted-foreground">{DELIVERY_LABEL[s.delivery] ?? s.delivery}</span>
            {s.injected ? (
              <span className="text-success">{t(($) => $.context.injected)}</span>
            ) : (
              <span className="text-muted-foreground">{t(($) => $.context.not_injected)}</span>
            )}
            {s.truncated ? (
              <span className="text-warning">{t(($) => $.context.truncated)}</span>
            ) : null}
            <span className="text-muted-foreground tabular-nums">
              {formatTokens(s.token_count)} · {s.bytes}B
            </span>
            {s.preview && (
              <span className="min-w-0 flex-1 truncate text-muted-foreground">{s.preview}</span>
            )}
          </div>
          <button
            type="button"
            onClick={() => setExpanded(expanded === s.key ? null : s.key)}
            className="mt-1 text-micro text-primary hover:underline"
          >
            {expanded === s.key ? t(($) => $.context.hide_raw) : t(($) => $.context.view_raw)}
          </button>
          {expanded === s.key && (
            <pre className="mt-1 max-h-60 overflow-auto whitespace-pre-wrap break-words rounded bg-muted/60 p-2 text-micro">
              {isFetching
                ? t(($) => $.context.loading)
                : rawSection?.raw
                  ? rawSection.raw
                  : t(($) => $.context.raw_unavailable)}
            </pre>
          )}
        </div>
      ))}
    </div>
  );
}
