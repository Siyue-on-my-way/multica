"use client";

import { useEffect, useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { toast } from "sonner";
import { ChevronRight, Loader2 } from "lucide-react";
import type { Issue, SubIssueSuggestion } from "@multica/core/types";
import { api } from "@multica/core/api";
import { useWorkspaceId } from "@multica/core/hooks";
import { issueDetailOptions } from "@multica/core/issues/queries";
import { useCreateIssue } from "@multica/core/issues/mutations";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
  DialogFooter,
} from "@multica/ui/components/ui/dialog";
import { Button } from "@multica/ui/components/ui/button";
import { Checkbox } from "@multica/ui/components/ui/checkbox";
import { Input } from "@multica/ui/components/ui/input";
import { Textarea } from "@multica/ui/components/ui/textarea";
import { Badge } from "@multica/ui/components/ui/badge";
import { Collapsible, CollapsibleTrigger, CollapsibleContent } from "@multica/ui/components/ui/collapsible";
import { IssuePickerModal } from "./issue-picker-modal";
import { useT } from "../i18n";

/** Editable draft state for one AI-proposed sub-issue in the preview panel.
 *  `id` is a stable local key (index-based — rows are never reordered), not
 *  a server id: nothing here exists until the user confirms. */
interface SubIssueDraft {
  id: number;
  checked: boolean;
  title: string;
  description: string;
  stage: number;
  descriptionOpen: boolean;
  parentIssueId: string | null;
  parentIdentifier: string | null;
}

function draftFromSuggestion(id: number, s: SubIssueSuggestion): SubIssueDraft {
  return {
    id,
    checked: true,
    title: s.title,
    description: s.description,
    stage: s.stage,
    descriptionOpen: false,
    parentIssueId: s.suggested_parent_issue_id,
    parentIdentifier: s.suggested_parent_identifier,
  };
}

export function SubIssuePreviewModal({
  onClose,
  data,
}: {
  onClose: () => void;
  data: Record<string, unknown> | null;
}) {
  const { t } = useT("modals");
  const issueId = (data?.issueId as string) || "";
  const commentId = (data?.commentId as string) || "";
  const wsId = useWorkspaceId();
  const createIssue = useCreateIssue();

  const { data: sourceIssue = null } = useQuery({
    ...issueDetailOptions(wsId, issueId),
    enabled: !!issueId,
  });

  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState(false);
  const [drafts, setDrafts] = useState<SubIssueDraft[]>([]);
  const [creating, setCreating] = useState(false);
  const [pickingParentFor, setPickingParentFor] = useState<number | null>(null);

  useEffect(() => {
    if (!issueId || !commentId) return;
    let cancelled = false;
    setLoading(true);
    setLoadError(false);
    api
      .suggestSubIssues(issueId, { comment_id: commentId })
      .then((res) => {
        if (cancelled) return;
        setDrafts(res.subissues.map((s, i) => draftFromSuggestion(i, s)));
        setLoading(false);
      })
      .catch(() => {
        if (cancelled) return;
        setLoadError(true);
        setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [issueId, commentId]);

  const minStage = useMemo(
    () => (drafts.length === 0 ? 1 : Math.min(...drafts.map((d) => d.stage))),
    [drafts],
  );

  const checkedCount = drafts.filter((d) => d.checked).length;
  const single = drafts.length === 1;

  const updateDraft = (id: number, patch: Partial<SubIssueDraft>) => {
    setDrafts((prev) => prev.map((d) => (d.id === id ? { ...d, ...patch } : d)));
  };

  const handleCreate = async () => {
    const selected = drafts.filter((d) => d.checked && d.title.trim());
    if (selected.length === 0) return;
    setCreating(true);
    try {
      const results = await Promise.allSettled(
        selected.map((d) =>
          createIssue.mutateAsync({
            title: d.title.trim(),
            description: d.description.trim() || undefined,
            status: "todo",
            project_id: sourceIssue?.project_id ?? undefined,
            parent_issue_id: d.parentIssueId ?? undefined,
            stage: d.stage,
          }),
        ),
      );
      const failedCount = results.filter((r) => r.status === "rejected").length;
      const succeededCount = results.length - failedCount;
      if (succeededCount > 0) {
        toast.success(t(($) => $.suggest_subissues.toast_created, { count: succeededCount }));
      }
      if (failedCount > 0) {
        toast.error(t(($) => $.suggest_subissues.toast_partial_failed, { count: failedCount }));
      }
      if (failedCount === 0) onClose();
    } finally {
      setCreating(false);
    }
  };

  return (
    <>
      <Dialog
        open
        onOpenChange={(v) => {
          if (!v && !creating) onClose();
        }}
      >
        <DialogContent className="sm:max-w-lg">
          <DialogHeader>
            <DialogTitle>{t(($) => $.suggest_subissues.title)}</DialogTitle>
            <DialogDescription>{t(($) => $.suggest_subissues.description)}</DialogDescription>
          </DialogHeader>

          {loading && (
            <div className="flex items-center justify-center gap-2 py-8 text-body text-muted-foreground">
              <Loader2 className="h-4 w-4 animate-spin" />
              {t(($) => $.suggest_subissues.loading)}
            </div>
          )}

          {!loading && loadError && (
            <div className="py-6 text-center text-body text-muted-foreground">
              {t(($) => $.suggest_subissues.load_failed)}
            </div>
          )}

          {!loading && !loadError && drafts.length === 0 && (
            <div className="py-6 text-center text-body text-muted-foreground">
              {t(($) => $.suggest_subissues.empty)}
            </div>
          )}

          {!loading && !loadError && drafts.length > 0 && (
            <div className="flex max-h-[60vh] flex-col gap-3 overflow-y-auto">
              {drafts.map((d) => (
                <div key={d.id} className="rounded-lg border border-border/60 p-3">
                  <div className="flex items-start gap-2">
                    {!single && (
                      <Checkbox
                        className="mt-1"
                        checked={d.checked}
                        onCheckedChange={(value) => updateDraft(d.id, { checked: value === true })}
                      />
                    )}
                    <div className="flex min-w-0 flex-1 flex-col gap-1.5">
                      <Input
                        value={d.title}
                        onChange={(e) => updateDraft(d.id, { title: e.target.value })}
                        placeholder={t(($) => $.suggest_subissues.title_placeholder)}
                      />
                      <div className="flex flex-wrap items-center gap-1.5">
                        <Badge variant="secondary">
                          {d.stage === minStage
                            ? t(($) => $.suggest_subissues.stage_parallel, { stage: d.stage })
                            : t(($) => $.suggest_subissues.stage_depends, {
                                stage: d.stage,
                                prevStage: d.stage - 1,
                              })}
                        </Badge>
                        <Button
                          type="button"
                          variant="ghost"
                          size="sm"
                          className="h-6 px-1.5 text-caption text-muted-foreground"
                          onClick={() => setPickingParentFor(d.id)}
                        >
                          {d.parentIdentifier
                            ? t(($) => $.suggest_subissues.parent_label, { identifier: d.parentIdentifier })
                            : t(($) => $.suggest_subissues.parent_unset)}
                        </Button>
                      </div>
                      <Collapsible
                        open={d.descriptionOpen}
                        onOpenChange={(open) => updateDraft(d.id, { descriptionOpen: open })}
                      >
                        <CollapsibleTrigger className="flex items-center gap-1 text-caption text-muted-foreground hover:text-foreground">
                          <ChevronRight className={`h-3 w-3 transition-transform ${d.descriptionOpen ? "rotate-90" : ""}`} />
                          {t(($) => $.suggest_subissues.description_toggle)}
                        </CollapsibleTrigger>
                        <CollapsibleContent>
                          <Textarea
                            className="mt-1.5 min-h-24"
                            value={d.description}
                            onChange={(e) => updateDraft(d.id, { description: e.target.value })}
                          />
                        </CollapsibleContent>
                      </Collapsible>
                    </div>
                  </div>
                </div>
              ))}
            </div>
          )}

          <DialogFooter>
            <Button variant="outline" onClick={onClose} disabled={creating}>
              {t(($) => $.suggest_subissues.cancel)}
            </Button>
            <Button
              onClick={handleCreate}
              disabled={loading || loadError || drafts.length === 0 || checkedCount === 0 || creating}
            >
              {creating && <Loader2 className="h-3.5 w-3.5 animate-spin" />}
              {single
                ? t(($) => $.suggest_subissues.create_single)
                : t(($) => $.suggest_subissues.create_batch, { count: checkedCount })}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {pickingParentFor != null && (
        <IssuePickerModal
          open
          onOpenChange={(v) => {
            if (!v) setPickingParentFor(null);
          }}
          title={t(($) => $.set_parent.title)}
          description={t(($) => $.set_parent.description)}
          excludeIds={[]}
          onSelect={(selected: Issue) => {
            updateDraft(pickingParentFor, { parentIssueId: selected.id, parentIdentifier: selected.identifier });
            setPickingParentFor(null);
          }}
        />
      )}
    </>
  );
}
