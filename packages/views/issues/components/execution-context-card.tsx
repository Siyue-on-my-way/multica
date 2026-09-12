"use client";

import { ChevronRight } from "lucide-react";
import type { ResolvedContent, ResolvedExecutionContext } from "@multica/core/types";
import { Badge } from "@multica/ui/components/ui/badge";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@multica/ui/components/ui/collapsible";
import { useState } from "react";
import { useT } from "../../i18n";

// InheritedExecutionContextCard is the read-only "已继承执行上下文" view
// (SIY-177). It shows what a run of the surrounding issue will receive —
// merged along the chain (project resources → ancestors → the issue's own
// document), de-duplicated to one line/block per distinct value, with
// provenance. Collapsed by default: it is context, not an action surface.
// Longtext blocks clamp to two lines each and expand on click (SIY-177
// feedback: a very long inheritance text must not take over the page).
export function InheritedExecutionContextCard({
  resolved,
  defaultOpen = false,
}: {
  resolved: ResolvedExecutionContext | null | undefined;
  defaultOpen?: boolean;
}) {
  const { t } = useT("modals");
  const [open, setOpen] = useState(defaultOpen);

  const hasContent =
    !!resolved &&
    (resolved.entries.length > 0 || (resolved.contents?.length ?? 0) > 0 || resolved.sources.length > 0);
  if (!hasContent) return null;

  const sourceLabel = (sourceIssueId: string | undefined) => {
    if (!sourceIssueId) return t(($) => $.execution_context.source_project);
    const source = resolved?.sources.find((s) => s.issue_id === sourceIssueId);
    if (!source) return "";
    return source.identifier || source.title || "";
  };

  const isEmpty = resolved!.entries.length === 0 && (resolved!.contents?.length ?? 0) === 0;

  return (
    <Collapsible open={open} onOpenChange={setOpen} className="rounded-md border border-border/60">
      <CollapsibleTrigger className="flex w-full items-center justify-between gap-2 p-2.5 text-left hover:bg-muted/30">
        <span className="flex min-w-0 items-center gap-1.5">
          <ChevronRight className={`h-3.5 w-3.5 shrink-0 transition-transform ${open ? "rotate-90" : ""}`} />
          <span className="text-body font-medium">{t(($) => $.execution_context.inherited_title)}</span>
          <span className="text-caption text-muted-foreground">
            {t(($) => $.execution_context.entry_count, {
              count: resolved!.entries.length + (resolved!.contents?.length ?? 0),
            })}
          </span>
        </span>
        <span className="shrink-0 text-caption text-muted-foreground">
          {t(($) => $.execution_context.inherited_hint)}
        </span>
      </CollapsibleTrigger>
      <CollapsibleContent className="space-y-2 border-t border-border/60 p-2.5">
        {isEmpty ? (
          <div className="text-caption text-muted-foreground">
            {t(($) => $.execution_context.inherited_empty)}
          </div>
        ) : (
          <>
            {(resolved!.contents ?? []).map((content, index) => (
              <ClampedContentBlock
                key={index}
                content={content}
                source={sourceLabel(content.source_issue_id)}
              />
            ))}
            {resolved!.entries.length > 0 && (
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
            )}
          </>
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

// ClampedContentBlock shows one inherited longtext block, clamped to two
// lines until the user expands it.
function ClampedContentBlock({ content, source }: { content: ResolvedContent; source: string }) {
  const { t } = useT("modals");
  const [expanded, setExpanded] = useState(false);

  return (
    <div className="rounded-md border border-border/50 p-2">
      <div className="mb-1 flex items-center justify-between gap-2">
        <span className="shrink-0 text-caption text-muted-foreground">
          {t(($) => $.execution_context.content_block_label, {
            source: source || t(($) => $.execution_context.inherited_title),
          })}
        </span>
        <button
          type="button"
          className="shrink-0 text-caption text-primary hover:underline"
          onClick={() => setExpanded((value) => !value)}
        >
          {expanded
            ? t(($) => $.execution_context.content_collapse)
            : t(($) => $.execution_context.content_expand)}
        </button>
      </div>
      <pre
        className={`whitespace-pre-wrap break-words font-sans text-caption ${
          expanded ? "" : "line-clamp-2"
        }`}
      >
        {content.text}
      </pre>
    </div>
  );
}
