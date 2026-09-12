"use client";

import { ChevronRight } from "lucide-react";
import type { ResolvedExecutionContext } from "@multica/core/types";
import { Badge } from "@multica/ui/components/ui/badge";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@multica/ui/components/ui/collapsible";
import { useState } from "react";
import { useT } from "../../i18n";

// InheritedExecutionContextCard is the read-only "已继承执行上下文" view
// (SIY-177). It shows what a run of the surrounding issue will receive —
// merged along the chain (project resources → ancestors → the issue's own
// entries), de-duplicated to one line per distinct value, with per-entry
// provenance. Collapsed by default: it is context, not an action surface.
export function InheritedExecutionContextCard({
  resolved,
  defaultOpen = false,
}: {
  resolved: ResolvedExecutionContext | null | undefined;
  defaultOpen?: boolean;
}) {
  const { t } = useT("modals");
  const [open, setOpen] = useState(defaultOpen);

  const hasContent = !!resolved && (resolved.entries.length > 0 || resolved.sources.length > 0);
  if (!hasContent) return null;

  const sourceLabel = (sourceIssueId: string | undefined) => {
    if (!sourceIssueId) return t(($) => $.execution_context.source_project);
    const source = resolved?.sources.find((s) => s.issue_id === sourceIssueId);
    if (!source) return "";
    return source.identifier || source.title || "";
  };

  return (
    <Collapsible open={open} onOpenChange={setOpen} className="rounded-md border border-border/60">
      <CollapsibleTrigger className="flex w-full items-center justify-between gap-2 p-2.5 text-left hover:bg-muted/30">
        <span className="flex min-w-0 items-center gap-1.5">
          <ChevronRight className={`h-3.5 w-3.5 shrink-0 transition-transform ${open ? "rotate-90" : ""}`} />
          <span className="text-body font-medium">{t(($) => $.execution_context.inherited_title)}</span>
          <span className="text-caption text-muted-foreground">
            {t(($) => $.execution_context.entry_count, { count: resolved!.entries.length })}
          </span>
        </span>
        <span className="shrink-0 text-caption text-muted-foreground">
          {t(($) => $.execution_context.inherited_hint)}
        </span>
      </CollapsibleTrigger>
      <CollapsibleContent className="space-y-2 border-t border-border/60 p-2.5">
        {resolved!.entries.length > 0 ? (
          <ul className="flex flex-col gap-1">
            {resolved!.entries.map((entry, index) => (
              <li key={index} className="flex min-w-0 items-center gap-1.5 text-caption">
                <Badge variant="secondary" className="shrink-0">
                  {t(($) => $.execution_context[`kind_${entry.kind}` as const])}
                  {entry.os ? ` · ${entry.os}` : ""}
                </Badge>
                <span className="min-w-0 flex-1 break-all">
                  {entry.label ? `${entry.label}: ` : ""}
                  {entry.value}
                </span>
                {sourceLabel(entry.source_issue_id) && (
                  <span className="shrink-0 text-muted-foreground">
                    {t(($) => $.execution_context.source_label, {
                      source: sourceLabel(entry.source_issue_id),
                    })}
                  </span>
                )}
              </li>
            ))}
          </ul>
        ) : (
          <div className="text-caption text-muted-foreground">
            {t(($) => $.execution_context.inherited_empty)}
          </div>
        )}
        {resolved!.sources.length > 0 && (
          <div className="flex flex-wrap gap-1">
            {resolved!.sources.map((source, index) => (
              <Badge key={source.issue_id || `project-${index}`} variant="outline" className="text-caption font-normal">
                {source.project
                  ? t(($) => $.execution_context.source_project)
                  : source.identifier || source.title || source.issue_id}
                {source.version > 0 ? ` · v${source.version}` : ""}
              </Badge>
            ))}
          </div>
        )}
      </CollapsibleContent>
    </Collapsible>
  );
}
