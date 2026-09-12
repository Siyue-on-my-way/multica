"use client";

import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { toast } from "sonner";
import { Pencil } from "lucide-react";
import type { ExecutionContext, Issue, ResolvedExecutionContext } from "@multica/core/types";
import { api } from "@multica/core/api";
import { useUpdateIssue } from "@multica/core/issues/mutations";
import { Button } from "@multica/ui/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import { Badge } from "@multica/ui/components/ui/badge";
import { InheritedExecutionContextCard } from "./execution-context-card";
import {
  ExecutionContextEditor,
  emptyExecutionContextEditorValue,
  toExecutionContext,
  type ExecutionContextEditorValue,
} from "./execution-context-editor";
import { useT } from "../../i18n";

// IssueExecutionContextSection renders the structured execution context on the
// issue detail page (SIY-177): the issue's own document (with its inherit
// switch state, editable via a focused dialog) plus the merged chain view a
// run of this issue receives. The inherited half is fetched only when the
// user expands it — the merged view costs a chain walk the detail page does
// not otherwise need.
export function IssueExecutionContextSection({ issue }: { issue: Issue }) {
  const { t } = useT("issues");
  const [expanded, setExpanded] = useState(false);
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState<ExecutionContextEditorValue | null>(null);
  const updateIssue = useUpdateIssue();

  const own = issue.execution_context ?? null;
  const { data: resolved } = useQuery({
    queryKey: ["issue-execution-context", issue.id],
    queryFn: () => api.getIssueExecutionContext(issue.id),
    enabled: expanded,
  });

  const openEditor = () => {
    setDraft(own ? { entries: [...own.entries], inherit: own.inherit } : emptyExecutionContextEditorValue());
    setEditing(true);
  };

  const save = async () => {
    if (!draft) return;
    try {
      await updateIssue.mutateAsync({
        id: issue.id,
        execution_context: toExecutionContext(draft) ?? null,
      });
      setEditing(false);
      setDraft(null);
    } catch {
      toast.error(t(($) => $.execution_context_detail.toast_save_failed));
    }
  };

  const ownEntries = own?.entries ?? [];

  return (
    <div className="mt-3 flex flex-col gap-2">
      <button
        type="button"
        className="flex w-full items-center justify-between gap-2 rounded-md border border-border/60 px-2.5 py-2 text-left hover:bg-muted/30"
        onClick={() => setExpanded((value) => !value)}
      >
        <span className="flex min-w-0 items-center gap-1.5">
          <span className="text-body font-medium">{t(($) => $.execution_context_detail.own_title)}</span>
          {own ? (
            <Badge variant="secondary" className="shrink-0">
              {own.inherit
                ? t(($) => $.execution_context_detail.inherit_on)
                : t(($) => $.execution_context_detail.inherit_off)}
            </Badge>
          ) : (
            <span className="shrink-0 text-caption text-muted-foreground">
              {t(($) => $.execution_context_detail.empty_hint)}
            </span>
          )}
        </span>
        <Button
          type="button"
          variant="ghost"
          size="sm"
          className="h-6 shrink-0 px-1.5 text-caption text-muted-foreground"
          onClick={(event) => {
            event.stopPropagation();
            openEditor();
          }}
        >
          <Pencil className="h-3 w-3" />
          {t(($) => $.execution_context_detail.edit)}
        </Button>
      </button>

      {expanded && (
        <>
          {ownEntries.length > 0 && (
            <ul className="flex flex-col gap-1">
              {ownEntries.map((entry, index) => (
                <li key={index} className="flex min-w-0 items-center gap-1.5 text-caption">
                  <Badge variant="secondary" className="shrink-0">
                    {entry.kind}
                    {entry.os ? ` · ${entry.os}` : ""}
                  </Badge>
                  <span className="min-w-0 flex-1 break-all">
                    {entry.label ? `${entry.label}: ` : ""}
                    {entry.value}
                  </span>
                </li>
              ))}
            </ul>
          )}
          <div>
            <div className="mb-1 text-caption text-muted-foreground">
              {t(($) => $.execution_context_detail.resolved_title)}
            </div>
            <InheritedExecutionContextCard resolved={resolvedToCard(resolved)} defaultOpen />
          </div>
        </>
      )}

      {editing && draft && (
        <Dialog open onOpenChange={(open) => { if (!open) { setEditing(false); setDraft(null); } }}>
          <DialogContent className="sm:max-w-xl">
            <DialogHeader>
              <DialogTitle>{t(($) => $.execution_context_detail.edit_title)}</DialogTitle>
            </DialogHeader>
            <ExecutionContextEditor value={draft} onChange={setDraft} disabled={updateIssue.isPending} />
            <DialogFooter>
              <Button variant="outline" onClick={() => { setEditing(false); setDraft(null); }}>
                {t(($) => $.execution_context_detail.cancel)}
              </Button>
              <Button onClick={() => void save()} disabled={updateIssue.isPending}>
                {t(($) => $.execution_context_detail.save)}
              </Button>
            </DialogFooter>
          </DialogContent>
        </Dialog>
      )}
    </div>
  );
}

function resolvedToCard(
  data: { own: ExecutionContext; inherited: ResolvedExecutionContext } | undefined,
): ResolvedExecutionContext | null {
  return data?.inherited ?? null;
}
