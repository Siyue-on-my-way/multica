"use client";

import { useState } from "react";
import { Loader2, MessageSquarePlus, RotateCcw } from "@multica/ui/icons";
import { toast } from "sonner";
import { api, dispatchReasonCode } from "@multica/core/api";
import type { AgentTask } from "@multica/core/types";
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@multica/ui/components/ui/tooltip";
import { useT } from "../../i18n";

/** Retry only makes sense for terminal-but-not-success runs. */
export function canRetryRun(task: AgentTask): boolean {
  return task.status === "failed" || task.status === "cancelled";
}

// Re-runs one specific past run. Shared by the execution log's rows and the
// Runs dialog so a failed run retries the same way wherever it is listed.
export function RetryRunButton({ task, issueId }: { task: AgentTask; issueId: string }) {
  const { t } = useT("issues");
  const [retrying, setRetrying] = useState(false);
  // SIY: a second affordance next to retry — start a FRESH provider session
  // for the same agent instead of resuming the failed run's session. The two
  // buttons disable each other while either request is in flight.
  const [freshRetrying, setFreshRetrying] = useState(false);
  const busy = retrying || freshRetrying;

  const handleRetry = async () => {
    if (retrying) return;
    setRetrying(true);
    try {
      // Passing task.id targets this row's agent — without it, the rerun
      // endpoint would fall back to the issue's current assignee and the wrong
      // agent would fire on rows whose agent has since been displaced
      // (reassignment, squad worker, or a one-off @-mention agent).
      // SIY-167: explicit "retry" action so the server resumes the source
      // session instead of applying its legacy fallback.
      await api.rerunIssue(issueId, task.id, false, "retry");
    } catch (e) {
      // A rerun is re-gated on the operator's invoke permission (MUL-4525): a
      // structured 403 means the agent can't be triggered, not a transient
      // failure — localize it instead of echoing the server's generic message.
      toast.error(
        dispatchReasonCode(e) === "invocation_not_allowed"
          ? t(($) => $.execution_log.retry_blocked)
          : e instanceof Error
            ? e.message
            : t(($) => $.execution_log.retry_failed),
      );
    } finally {
      // Reset on both success and failure: the row stays mounted (its task.id
      // is unchanged), so leaving `retrying` true on success would pin the
      // button as a permanent spinner.
      setRetrying(false);
    }
  };

  // SIY: fresh-session rerun of this row's agent — same targeting as retry,
  // but the server enqueues a new provider session (after refreshing a stale
  // summary) rather than resuming the old one.
  const handleFreshSessionRetry = async () => {
    if (freshRetrying) return;
    setFreshRetrying(true);
    try {
      await api.rerunIssue(issueId, task.id, true, "new_session");
    } catch (e) {
      toast.error(
        dispatchReasonCode(e) === "invocation_not_allowed"
          ? t(($) => $.execution_log.retry_blocked)
          : e instanceof Error
            ? e.message
            : t(($) => $.execution_log.fresh_session_retry_failed),
      );
    } finally {
      setFreshRetrying(false);
    }
  };

  return (
    <>
      <Tooltip>
        <TooltipTrigger
          render={
            <button
              type="button"
              onClick={handleRetry}
              disabled={busy}
              aria-label={t(($) => $.execution_log.retry_task_aria)}
            />
          }
          className="flex items-center justify-center rounded-xs p-1 text-muted-foreground transition-colors hover:bg-accent/50 hover:text-foreground disabled:cursor-not-allowed disabled:opacity-50"
        >
          {retrying ? (
            <Loader2 className="h-3.5 w-3.5 animate-spin" />
          ) : (
            <RotateCcw className="h-3.5 w-3.5" />
          )}
        </TooltipTrigger>
        <TooltipContent>{t(($) => $.execution_log.retry_task_tooltip)}</TooltipContent>
      </Tooltip>
      <Tooltip>
        <TooltipTrigger
          render={
            <button
              type="button"
              onClick={handleFreshSessionRetry}
              disabled={busy}
              aria-label={t(($) => $.execution_log.fresh_session_retry_aria)}
            />
          }
          className="flex items-center justify-center rounded-xs p-1 text-muted-foreground transition-colors hover:bg-accent/50 hover:text-foreground disabled:cursor-not-allowed disabled:opacity-50"
        >
          {freshRetrying ? (
            <Loader2 className="h-3.5 w-3.5 animate-spin" />
          ) : (
            <MessageSquarePlus className="h-3.5 w-3.5" />
          )}
        </TooltipTrigger>
        <TooltipContent>{t(($) => $.execution_log.fresh_session_retry_tooltip)}</TooltipContent>
      </Tooltip>
    </>
  );
}
