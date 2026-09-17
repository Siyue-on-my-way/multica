"use client";

import { ListTree } from "lucide-react";
import type { DiscussionProposal } from "@multica/core/types";
import { Badge } from "@multica/ui/components/ui/badge";
import { cn } from "@multica/ui/lib/utils";
import { useWorkspacePaths } from "@multica/core/paths";
import { useT } from "../../i18n";
import { AppLink } from "../../navigation";

/**
 * Compact result row rendered directly below the source answer.
 *
 * The proposal snapshot remains available to the generation modal and is
 * persisted by the backend, but it is intentionally not repeated in the
 * timeline: the agent answer already contains that context. Only the durable
 * creation result belongs in the answer's follow-up row.
 */
export function CreatedSubissueSummary({
  proposal,
  className,
}: {
  proposal: DiscussionProposal;
  className?: string;
}) {
  const { t } = useT("modals");
  const paths = useWorkspacePaths();
  const generations = proposal.generations ?? [];

  if (generations.length === 0) return null;

  return (
    <section
      className={cn("mt-2 text-caption", className)}
      data-slot="created-subissue-summary"
    >
      <h4 className="flex items-center gap-1.5 font-semibold text-foreground">
        <ListTree className="size-3.5" aria-hidden />
        {t(($) => $.suggest_subissues.created_batches_title)}
      </h4>
      <div className="mt-1.5 space-y-1.5">
        {generations.map((generation) => {
          const issues = generation.issues ?? [];
          const modeLabel = generation.create_mode === "todo"
            ? t(($) => $.suggest_subissues.created_batch_mode_todo)
            : t(($) => $.suggest_subissues.created_batch_mode_backlog);

          return (
            <div
              key={generation.id}
              className="flex flex-wrap items-center gap-x-2 gap-y-1"
              data-slot="created-subissue-batch"
            >
              <span className="font-medium">{generation.strategy || "manual"}</span>
              <Badge variant="outline">
                {modeLabel}
              </Badge>
              <span className="text-muted-foreground">
                {t(($) => $.suggest_subissues.created_batch_count, { count: issues.length })}
              </span>
              {issues.length > 0 && (
                <div className="basis-full flex flex-wrap gap-x-3 gap-y-1 pl-0.5">
                  {issues.map((child) => (
                    <AppLink
                      key={child.id}
                      href={paths.issueDetail(child.id)}
                      className="text-primary hover:underline"
                    >
                      {child.identifier} · {child.title}
                    </AppLink>
                  ))}
                </div>
              )}
            </div>
          );
        })}
      </div>
    </section>
  );
}
