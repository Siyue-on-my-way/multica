import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { api } from "@multica/core/api";
import { issueKeys } from "@multica/core/issues/queries";
import type { AgentTask } from "@multica/core/types";
import {
  contextBadgeState,
  type ContextBadgeState,
} from "@multica/core/types/task-context";
import { useT } from "../../i18n";
import { formatTokens } from "../../runtimes/utils";
import { ContextDetailDialog } from "./context-detail-dialog";

// SIY-125: a compact Context badge on each execution-log run row. It lazy-
// loads the per-task observation (only once a run is terminal — the daemon
// reports it at the completion boundary), renders the resume state, and opens
// the three-layer detail dialog on click.

const TERMINAL_STATUSES = new Set<AgentTask["status"]>([
  "completed",
  "failed",
  "cancelled",
]);

const STATE_CLASSES: Record<ContextBadgeState, string> = {
  resumed: "bg-success/15 text-success",
  fresh: "bg-info/15 text-info",
  fallback: "bg-warning/15 text-warning",
  unknown: "bg-muted text-muted-foreground",
};

export function ContextBadge({ task }: { task: AgentTask }) {
  const { t } = useT("issues");
  const [open, setOpen] = useState(false);

  const { data } = useQuery({
    queryKey: issueKeys.taskContext(task.id),
    queryFn: () => api.getTaskContext(task.id),
    // No observation exists mid-run; the daemon reports at the completion
    // boundary, so only fetch for terminal rows.
    enabled: TERMINAL_STATUSES.has(task.status),
    staleTime: 60_000,
  });

  const state = contextBadgeState(data);
  const label =
    state === "resumed"
      ? t(($) => $.context.badge_resumed)
      : state === "fresh"
        ? t(($) => $.context.badge_new_session)
        : state === "fallback"
          ? t(($) => $.context.badge_fallback)
          : t(($) => $.context.badge_unknown);

  // "Input 约 1.8k tokens · 6 sources · workdir reused" — estimated token
  // counts carry the "约" prefix; exact ones (provider-reported) do not.
  let summary: string | null = null;
  if (data && data.resume_actual !== "unknown") {
    const prefix =
      data.token_mode === "exact" ? "" : t(($) => $.context.summary_estimated_prefix);
    const tokens = formatTokens(data.input_tokens);
    const sources = `${data.sections.length} ${t(($) => $.context.sources)}`;
    const workdir = data.workdir_reused
      ? t(($) => $.context.workdir_reused)
      : t(($) => $.context.workdir_fresh);
    summary = `${prefix}${tokens} ${t(($) => $.context.tokens_unit)} · ${sources} · ${workdir}`;
  }

  return (
    <>
      <button
        type="button"
        onClick={() => setOpen(true)}
        title={t(($) => $.context.tooltip)}
        className={`shrink-0 whitespace-nowrap rounded-full px-1.5 py-0.5 text-micro font-medium transition-colors hover:opacity-80 ${STATE_CLASSES[state]}`}
      >
        {label}
        {summary ? <span className="opacity-70"> · {summary}</span> : null}
      </button>
      <ContextDetailDialog taskId={task.id} open={open} onOpenChange={setOpen} />
    </>
  );
}
