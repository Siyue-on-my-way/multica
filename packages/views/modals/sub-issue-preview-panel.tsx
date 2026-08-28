"use client";

import { useCallback, useEffect, useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { toast } from "sonner";
import { ChevronRight, Loader2 } from "lucide-react";
import type { Issue, SubIssuePlan, SubIssuePlanItem, SubIssueSuggestion } from "@multica/core/types";
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

type ModalPhase = "outline" | "details";

interface SubIssueDraft {
  id: string;
  checked: boolean;
  title: string;
  goal: string;
  description: string;
  stage: number;
  descriptionOpen: boolean;
  parentIssueId: string | null;
  parentIdentifier: string | null;
}

function draftFromSuggestion(s: SubIssueSuggestion): SubIssueDraft {
  return {
    id: s.id ?? `detail-${Math.random().toString(36).slice(2)}`,
    checked: true,
    title: s.title,
    goal: s.goal ?? "",
    description: s.description,
    stage: s.stage,
    descriptionOpen: false,
    parentIssueId: s.suggested_parent_issue_id,
    parentIdentifier: s.suggested_parent_identifier,
  };
}

function newManualItem(planId: string): SubIssuePlanItem {
  return {
    id: `${planId}-manual-${Date.now()}`,
    title: "",
    goal: "",
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

  const [phase, setPhase] = useState<ModalPhase>("outline");
  const [plans, setPlans] = useState<SubIssuePlan[]>([]);
  const [selectedPlanId, setSelectedPlanId] = useState("");
  const [mergeSelection, setMergeSelection] = useState<string[]>([]);
  const [humanConstraints, setHumanConstraints] = useState("");
  const [loadingPlans, setLoadingPlans] = useState(true);
  const [planError, setPlanError] = useState(false);
  const [loadingDetails, setLoadingDetails] = useState(false);
  const [detailError, setDetailError] = useState(false);
  const [drafts, setDrafts] = useState<SubIssueDraft[]>([]);
  const [creating, setCreating] = useState(false);
  const [pickingParentFor, setPickingParentFor] = useState<string | null>(null);

  const loadPlans = useCallback(
    async (constraints: string) => {
      if (!issueId || !commentId) return;
      setLoadingPlans(true);
      setPlanError(false);
      try {
        const response = await api.suggestSubissuePlans(issueId, {
          comment_id: commentId,
          human_constraints: constraints.trim() || undefined,
        });
      setPlans(response.plans);
      setSelectedPlanId(response.plans[0]?.id ?? "");
      setMergeSelection([]);
      setDetailError(false);
      setPhase("outline");
      } catch {
        setPlanError(true);
      } finally {
        setLoadingPlans(false);
      }
    },
    [commentId, issueId],
  );

  useEffect(() => {
    void loadPlans("");
  }, [loadPlans]);

  const selectedPlan = useMemo(
    () => plans.find((plan) => plan.id === selectedPlanId) ?? null,
    [plans, selectedPlanId],
  );
  const outlineValid = !!selectedPlan && selectedPlan.items.length > 0 && selectedPlan.items.every((item) => item.title.trim() && item.goal.trim());
  const busy = loadingPlans || loadingDetails || creating;
  const checkedCount = drafts.filter((draft) => draft.checked).length;
  const single = drafts.length === 1;

  const updateSelectedPlan = (update: (plan: SubIssuePlan) => SubIssuePlan) => {
    if (!selectedPlan) return;
    setPlans((previous) => previous.map((plan) => (plan.id === selectedPlan.id ? update(plan) : plan)));
  };

  const updatePlanItem = (itemId: string, patch: Partial<SubIssuePlanItem>) => {
    updateSelectedPlan((plan) => ({
      ...plan,
      items: plan.items.map((item) => (item.id === itemId ? { ...item, ...patch } : item)),
    }));
  };

  const movePlanItem = (itemId: string, direction: -1 | 1) => {
    updateSelectedPlan((plan) => {
      const index = plan.items.findIndex((item) => item.id === itemId);
      const nextIndex = index + direction;
      if (index < 0 || nextIndex < 0 || nextIndex >= plan.items.length) return plan;
      const items = [...plan.items];
      const current = items[index]!;
      const next = items[nextIndex]!;
      items[index] = next;
      items[nextIndex] = current;
      return { ...plan, items };
    });
  };

  const toggleMergeSelection = (itemId: string) => {
    setMergeSelection((previous) =>
      previous.includes(itemId) ? previous.filter((id) => id !== itemId) : [...previous, itemId],
    );
  };

  const mergeSelectedItems = () => {
    if (!selectedPlan || mergeSelection.length < 2) return;
    const selected = selectedPlan.items.filter((item) => mergeSelection.includes(item.id));
    const firstIndex = selectedPlan.items.findIndex((item) => item.id === selected[0]?.id);
    const merged: SubIssuePlanItem = {
      id: `${selectedPlan.id}-merged-${Date.now()}`,
      title: selected.map((item) => item.title.trim()).filter(Boolean).join(" + "),
      goal: selected.map((item) => item.goal.trim()).filter(Boolean).join("\n\n"),
    };
    updateSelectedPlan((plan) => {
      const remaining = plan.items.filter((item) => !mergeSelection.includes(item.id));
      remaining.splice(Math.max(firstIndex, 0), 0, merged);
      return { ...plan, items: remaining };
    });
    setMergeSelection([]);
  };

  const deleteSelectedItems = () => {
    if (!selectedPlan || mergeSelection.length === 0 || mergeSelection.length >= selectedPlan.items.length) return;
    updateSelectedPlan((plan) => ({
      ...plan,
      items: plan.items.filter((item) => !mergeSelection.includes(item.id)),
    }));
    setMergeSelection([]);
  };

  const addPlanItem = () => {
    if (!selectedPlan) return;
    updateSelectedPlan((plan) => ({ ...plan, items: [...plan.items, newManualItem(plan.id)] }));
  };

  const handleExpandDetails = async () => {
    if (!selectedPlan || !outlineValid) return;
    setLoadingDetails(true);
    setDetailError(false);
    try {
      const response = await api.expandSubissuePlan(issueId, {
        comment_id: commentId,
        human_constraints: humanConstraints.trim() || undefined,
        plan: selectedPlan,
      });
      setDrafts(response.subissues.map(draftFromSuggestion));
      setPhase("details");
    } catch {
      setDetailError(true);
    } finally {
      setLoadingDetails(false);
    }
  };

  const updateDraft = (id: string, patch: Partial<SubIssueDraft>) => {
    setDrafts((previous) => previous.map((draft) => (draft.id === id ? { ...draft, ...patch } : draft)));
  };

  const handleCreate = async () => {
    const selected = drafts.filter((draft) => draft.checked && draft.title.trim());
    if (selected.length === 0) return;
    setCreating(true);
    try {
      const results = await Promise.allSettled(
        selected.map((draft) =>
          createIssue.mutateAsync({
            title: draft.title.trim(),
            description: draft.description.trim() || undefined,
            status: "todo",
            project_id: sourceIssue?.project_id ?? undefined,
            parent_issue_id: draft.parentIssueId ?? undefined,
            stage: draft.stage,
          }),
        ),
      );
      const failedCount = results.filter((result) => result.status === "rejected").length;
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
        onOpenChange={(open) => {
          if (!open && !busy) onClose();
        }}
      >
        <DialogContent className="sm:max-w-2xl">
          <DialogHeader>
            <DialogTitle>
              {phase === "outline" ? t(($) => $.suggest_subissues.outline_title) : t(($) => $.suggest_subissues.details_title)}
            </DialogTitle>
            <DialogDescription>
              {phase === "outline"
                ? t(($) => $.suggest_subissues.outline_description)
                : t(($) => $.suggest_subissues.details_description)}
            </DialogDescription>
          </DialogHeader>

          {phase === "outline" && (
            <>
              <div className="flex flex-col gap-1.5">
                <label className="text-caption font-medium">{t(($) => $.suggest_subissues.constraints_label)}</label>
                <Textarea
                  value={humanConstraints}
                  onChange={(event) => setHumanConstraints(event.target.value)}
                  placeholder={t(($) => $.suggest_subissues.constraints_placeholder)}
                  className="min-h-16"
                  disabled={loadingPlans}
                />
              </div>

              {loadingPlans && (
                <div className="flex items-center justify-center gap-2 py-8 text-body text-muted-foreground">
                  <Loader2 className="h-4 w-4 animate-spin" />
                  {t(($) => $.suggest_subissues.outline_loading)}
                </div>
              )}

              {!loadingPlans && planError && (
                <div className="py-6 text-center text-body text-muted-foreground">
                  {t(($) => $.suggest_subissues.outline_load_failed)}
                </div>
              )}

              {!loadingPlans && !planError && plans.length === 0 && (
                <div className="py-6 text-center text-body text-muted-foreground">
                  {t(($) => $.suggest_subissues.empty)}
                </div>
              )}

              {!loadingDetails && detailError && (
                <div className="py-2 text-center text-body text-destructive">
                  {t(($) => $.suggest_subissues.details_load_failed)}
                </div>
              )}

              {!loadingPlans && !planError && plans.length > 0 && (
                <div className="flex max-h-[58vh] flex-col gap-3 overflow-y-auto">
                  <div className="grid gap-2 sm:grid-cols-3">
                    {plans.map((plan) => (
                      <button
                        key={plan.id}
                        type="button"
                        className={`rounded-lg border p-3 text-left transition-colors ${
                          plan.id === selectedPlanId ? "border-primary bg-primary/5" : "border-border/60 hover:bg-muted/50"
                        }`}
                        onClick={() => {
                          setSelectedPlanId(plan.id);
                          setMergeSelection([]);
                        }}
                      >
                        <div className="font-medium text-body">{plan.name}</div>
                        <div className="mt-1 text-caption text-muted-foreground">
                          {t(($) => $.suggest_subissues.plan_item_count, { count: plan.items.length })}
                        </div>
                      </button>
                    ))}
                  </div>

                  {selectedPlan && (
                    <div className="rounded-lg border border-border/60 p-3">
                      <div className="mb-2 flex flex-wrap items-center justify-between gap-2">
                        <div className="text-caption text-muted-foreground">
                          {t(($) => $.suggest_subissues.outline_edit_hint)}
                        </div>
                        <div className="flex flex-wrap gap-1.5">
                          <Button type="button" variant="outline" size="sm" onClick={mergeSelectedItems} disabled={mergeSelection.length < 2}>
                            {t(($) => $.suggest_subissues.merge_selected)}
                          </Button>
                          <Button type="button" variant="outline" size="sm" onClick={deleteSelectedItems} disabled={mergeSelection.length === 0}>
                            {t(($) => $.suggest_subissues.delete_selected)}
                          </Button>
                          <Button type="button" variant="outline" size="sm" onClick={addPlanItem}>
                            {t(($) => $.suggest_subissues.add_item)}
                          </Button>
                        </div>
                      </div>
                      <div className="flex flex-col gap-3">
                        {selectedPlan.items.map((item, index) => (
                          <div key={item.id} className="rounded-md border border-border/50 p-2.5">
                            <div className="flex items-start gap-2">
                              <Checkbox
                                className="mt-2"
                                checked={mergeSelection.includes(item.id)}
                                onCheckedChange={() => toggleMergeSelection(item.id)}
                                aria-label={t(($) => $.suggest_subissues.select_for_merge)}
                              />
                              <div className="min-w-0 flex-1 space-y-2">
                                <Input
                                  value={item.title}
                                  onChange={(event) => updatePlanItem(item.id, { title: event.target.value })}
                                  placeholder={t(($) => $.suggest_subissues.title_placeholder)}
                                />
                                <Textarea
                                  value={item.goal}
                                  onChange={(event) => updatePlanItem(item.id, { goal: event.target.value })}
                                  placeholder={t(($) => $.suggest_subissues.goal_placeholder)}
                                  className="min-h-16"
                                />
                              </div>
                              <div className="flex shrink-0 flex-col gap-1">
                                <Button type="button" variant="ghost" size="sm" className="h-7 px-2" onClick={() => movePlanItem(item.id, -1)} disabled={index === 0}>
                                  ↑
                                </Button>
                                <Button type="button" variant="ghost" size="sm" className="h-7 px-2" onClick={() => movePlanItem(item.id, 1)} disabled={index === selectedPlan.items.length - 1}>
                                  ↓
                                </Button>
                              </div>
                            </div>
                          </div>
                        ))}
                      </div>
                    </div>
                  )}
                </div>
              )}
            </>
          )}

          {phase === "details" && (
            <div className="flex max-h-[60vh] flex-col gap-3 overflow-y-auto">
              {loadingDetails && (
                <div className="flex items-center justify-center gap-2 py-8 text-body text-muted-foreground">
                  <Loader2 className="h-4 w-4 animate-spin" />
                  {t(($) => $.suggest_subissues.details_loading)}
                </div>
              )}
              {!loadingDetails && detailError && (
                <div className="py-6 text-center text-body text-muted-foreground">
                  {t(($) => $.suggest_subissues.details_load_failed)}
                </div>
              )}
              {!loadingDetails && !detailError && drafts.map((draft) => (
                <div key={draft.id} className="rounded-lg border border-border/60 p-3">
                  <div className="flex items-start gap-2">
                    {!single && (
                      <Checkbox
                        className="mt-1"
                        checked={draft.checked}
                        onCheckedChange={(value) => updateDraft(draft.id, { checked: value === true })}
                      />
                    )}
                    <div className="flex min-w-0 flex-1 flex-col gap-1.5">
                      <Input
                        value={draft.title}
                        onChange={(event) => updateDraft(draft.id, { title: event.target.value })}
                        placeholder={t(($) => $.suggest_subissues.title_placeholder)}
                      />
                      {draft.goal && <div className="text-caption text-muted-foreground">{draft.goal}</div>}
                      <div className="flex flex-wrap items-center gap-1.5">
                        <Badge variant="secondary">{t(($) => $.suggest_subissues.stage_label, { stage: draft.stage })}</Badge>
                        <Input
                          type="number"
                          min={1}
                          step={1}
                          value={draft.stage}
                          className="h-7 w-20"
                          aria-label={t(($) => $.suggest_subissues.stage_label, { stage: draft.stage })}
                          onChange={(event) => {
                            const stage = Number.parseInt(event.target.value, 10);
                            if (Number.isFinite(stage) && stage >= 1) updateDraft(draft.id, { stage });
                          }}
                        />
                        <Button
                          type="button"
                          variant="ghost"
                          size="sm"
                          className="h-6 px-1.5 text-caption text-muted-foreground"
                          onClick={() => setPickingParentFor(draft.id)}
                        >
                          {draft.parentIdentifier
                            ? t(($) => $.suggest_subissues.parent_label, { identifier: draft.parentIdentifier })
                            : t(($) => $.suggest_subissues.parent_unset)}
                        </Button>
                      </div>
                      <Collapsible
                        open={draft.descriptionOpen}
                        onOpenChange={(open) => updateDraft(draft.id, { descriptionOpen: open })}
                      >
                        <CollapsibleTrigger className="flex items-center gap-1 text-caption text-muted-foreground hover:text-foreground">
                          <ChevronRight className={`h-3 w-3 transition-transform ${draft.descriptionOpen ? "rotate-90" : ""}`} />
                          {t(($) => $.suggest_subissues.description_toggle)}
                        </CollapsibleTrigger>
                        <CollapsibleContent>
                          <Textarea
                            className="mt-1.5 min-h-24"
                            value={draft.description}
                            onChange={(event) => updateDraft(draft.id, { description: event.target.value })}
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
            {phase === "outline" ? (
              <>
                <Button variant="outline" onClick={onClose} disabled={busy}>
                  {t(($) => $.suggest_subissues.cancel)}
                </Button>
                <Button variant="outline" onClick={() => void loadPlans(humanConstraints)} disabled={busy}>
                  {t(($) => $.suggest_subissues.regenerate_plans)}
                </Button>
                <Button onClick={() => void handleExpandDetails()} disabled={busy || !outlineValid}>
                  {loadingDetails && <Loader2 className="h-3.5 w-3.5 animate-spin" />}
                  {t(($) => $.suggest_subissues.generate_details)}
                </Button>
              </>
            ) : (
              <>
                <Button variant="outline" onClick={() => setPhase("outline")} disabled={creating}>
                  {t(($) => $.suggest_subissues.back_to_outline)}
                </Button>
                <Button variant="outline" onClick={onClose} disabled={creating}>
                  {t(($) => $.suggest_subissues.cancel)}
                </Button>
                <Button onClick={handleCreate} disabled={detailError || drafts.length === 0 || checkedCount === 0 || creating}>
                  {creating && <Loader2 className="h-3.5 w-3.5 animate-spin" />}
                  {single
                    ? t(($) => $.suggest_subissues.create_single)
                    : t(($) => $.suggest_subissues.create_batch, { count: checkedCount })}
                </Button>
              </>
            )}
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {pickingParentFor != null && (
        <IssuePickerModal
          open
          onOpenChange={(open) => {
            if (!open) setPickingParentFor(null);
          }}
          title={t(($) => $.set_parent.title)}
          description={t(($) => $.set_parent.description)}
          excludeIds={[issueId]}
          onSelect={(selected: Issue) => {
            updateDraft(pickingParentFor, { parentIssueId: selected.id, parentIdentifier: selected.identifier });
            setPickingParentFor(null);
          }}
        />
      )}
    </>
  );
}
