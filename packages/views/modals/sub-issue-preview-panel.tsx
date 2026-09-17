"use client";

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { ChevronRight, Loader2 } from "lucide-react";
import type {
  Issue,
  SubIssuePlan,
  SubIssuePlanDiagnostic,
  SubIssuePlanGenerationSummary,
  SubIssuePlanItem,
  SubissuePlanJob,
  SubissuePlanJobActionResult,
  SubissuePlanJobAcceptedResponse,
  SuggestSubissuePlansResult,
  SubIssueSuggestion,
  ResolvedExecutionContext,
  SubissueSourcePacket,
} from "@multica/core/types";
import { api, ApiError } from "@multica/core/api";
import { useWorkspaceId } from "@multica/core/hooks";
import { issueDetailOptions, issueKeys } from "@multica/core/issues/queries";
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
import { InheritedExecutionContextCard } from "../issues/components";
import {
  ExecutionContextEditor,
  emptyExecutionContextEditorValue,
  toExecutionContext,
  type ExecutionContextEditorValue,
} from "../issues/components";
import { useT } from "../i18n";

type ModalPhase = "outline" | "details";

const OVERALL_BUSINESS = "整体流程";

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

function normalizeBusiness(value: string): string {
  return value.trim() || OVERALL_BUSINESS;
}

function stripBusinessPrefix(title: string): string {
  let result = title.trim();
  while (result.startsWith("【")) {
    const end = result.indexOf("】");
    if (end < 0) break;
    result = result.slice(end + 1).trim();
  }
  return result;
}

function formatBusinessTitle(title: string, business: string, isSummaryTest = false): string {
  const normalizedBusiness = normalizeBusiness(business);
  const cleanTitle = stripBusinessPrefix(title);
  if (!cleanTitle) return "";
  const suffix = isSummaryTest ? `${normalizedBusiness}汇总测试` : cleanTitle;
  return `【${normalizedBusiness}】${suffix}`;
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
    business: OVERALL_BUSINESS,
  };
}

function formatDiagnosticValue(value: string | number | undefined): string {
  if (value === undefined || value === "") return "—";
  return String(value);
}

function DiagnosticStat({ label, value }: { label: string; value: string | number | undefined }) {
  return (
    <div className="rounded-md bg-muted/40 px-2.5 py-2">
      <div className="text-caption text-muted-foreground">{label}</div>
      <div className="mt-0.5 break-words text-body font-medium">{formatDiagnosticValue(value)}</div>
    </div>
  );
}

function isPlanGenerationSummary(value: unknown): value is SubIssuePlanGenerationSummary {
  if (!value || typeof value !== "object") return false;
  const summary = value as Partial<SubIssuePlanGenerationSummary>;
  return (
    (summary.status === "running" || summary.status === "success" || summary.status === "failed") &&
    typeof summary.total_duration_ms === "number" &&
    typeof summary.upstream_duration_ms === "number" &&
    typeof summary.upstream_calls === "number" &&
    typeof summary.retry_count === "number"
  );
}

function planGenerationErrorDetails(error: unknown): {
  summary: SubIssuePlanGenerationSummary | null;
  diagnostics: SubIssuePlanDiagnostic[];
} {
  if (!(error instanceof ApiError) || !error.body || typeof error.body !== "object") {
    return { summary: null, diagnostics: [] };
  }
  const body = error.body as {
    summary?: unknown;
    diagnostics?: unknown;
  };
  return {
    summary: isPlanGenerationSummary(body.summary) ? body.summary : null,
    diagnostics: Array.isArray(body.diagnostics) ? (body.diagnostics as SubIssuePlanDiagnostic[]) : [],
  };
}

function isAcceptedPlanJobResponse(
  value: SuggestSubissuePlansResult | SubissuePlanJobActionResult,
): value is SubissuePlanJobAcceptedResponse {
  return "job" in value && !!value.job;
}

function isTerminalPlanJob(job: SubissuePlanJob): boolean {
  return job.status === "succeeded" || job.status === "failed" || job.status === "cancelled" || job.status === "expired";
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
  const initialProposalId = (data?.proposalId as string) || (data?.proposal_id as string) || "";
  const wsId = useWorkspaceId();
  const createIssue = useCreateIssue();
  const queryClient = useQueryClient();

  const { data: sourceIssue = null } = useQuery({
    ...issueDetailOptions(wsId, issueId),
    enabled: !!issueId,
  });

  const [phase, setPhase] = useState<ModalPhase>("outline");
  const [proposalId, setProposalId] = useState(initialProposalId);
  const proposalIdRef = useRef(initialProposalId);
  const [plans, setPlans] = useState<SubIssuePlan[]>([]);
  const [diagnostics, setDiagnostics] = useState<SubIssuePlanDiagnostic[]>([]);
  const [generationSummary, setGenerationSummary] = useState<SubIssuePlanGenerationSummary | null>(null);
  const [planJob, setPlanJob] = useState<SubissuePlanJob | null>(null);
  const [planJobAction, setPlanJobAction] = useState<"cancel" | "retry" | null>(null);
  const [selectedPlanId, setSelectedPlanId] = useState("");
  const [diagnosticsOpen, setDiagnosticsOpen] = useState(false);
  const [mergeSelection, setMergeSelection] = useState<string[]>([]);
  const [humanConstraints, setHumanConstraints] = useState("");
  const [carryAncestorContext, setCarryAncestorContext] = useState(true);
  const [ancestorContextRefs, setAncestorContextRefs] = useState<{ id: string; updated_at: string }[]>([]);
  // SIY-177: what the created children will inherit at runtime, resolved by
  // the backend (chain + project resources), plus the issue-specific context
  // the user can attach on top of it.
  const [inheritedExecutionContext, setInheritedExecutionContext] = useState<ResolvedExecutionContext | null>(null);
  const [sourcePacket, setSourcePacket] = useState<SubissueSourcePacket | null>(null);
  const [ownExecutionContext, setOwnExecutionContext] = useState<ExecutionContextEditorValue>(
    emptyExecutionContextEditorValue,
  );
  const [ownContextOpen, setOwnContextOpen] = useState(false);
  const [loadingPlans, setLoadingPlans] = useState(true);
  const [planError, setPlanError] = useState(false);
  const [loadingDetails, setLoadingDetails] = useState(false);
  const [detailError, setDetailError] = useState(false);
  const [drafts, setDrafts] = useState<SubIssueDraft[]>([]);
  const [creating, setCreating] = useState(false);
  const [createMode, setCreateMode] = useState<"backlog" | "todo">("backlog");
  const [generationKey, setGenerationKey] = useState<string | null>(null);
  const [pickingParentFor, setPickingParentFor] = useState<string | null>(null);

  const applyPlanPayload = useCallback(
    (payload: {
      plans: SubIssuePlan[];
      diagnostics?: SubIssuePlanDiagnostic[];
      summary?: SubIssuePlanGenerationSummary;
      ancestor_brief_refs?: { id: string; updated_at: string }[];
      inherited_execution_context?: ResolvedExecutionContext;
      proposal_id?: string;
      source_packet?: SubissueSourcePacket;
    }) => {
      setPlans(payload.plans);
      setDiagnostics(payload.diagnostics ?? []);
      setGenerationSummary(payload.summary ?? null);
      setAncestorContextRefs(payload.ancestor_brief_refs ?? []);
      setInheritedExecutionContext(payload.inherited_execution_context ?? null);
      setSourcePacket(payload.source_packet ?? null);
      const nextProposalId = payload.proposal_id ?? initialProposalId;
      proposalIdRef.current = nextProposalId;
      setProposalId(nextProposalId);
      setSelectedPlanId((payload.plans.find((plan) => plan.coverage === "full") ?? payload.plans[0])?.id ?? "");
      setDiagnosticsOpen(false);
      setMergeSelection([]);
      setDetailError(false);
      setPhase("outline");
    },
    [initialProposalId],
  );

  const loadPlans = useCallback(
    async (constraints: string) => {
      if (!issueId || (!commentId && !initialProposalId)) return;
      setLoadingPlans(true);
      setPlanError(false);
      setPlanJob(null);
      setPlanJobAction(null);
      setDiagnostics([]);
      setGenerationSummary(null);
      let accepted = false;
      try {
        const response = await api.suggestSubissuePlans(issueId, {
          ...(commentId ? { comment_id: commentId } : {}),
          ...(proposalIdRef.current ? { proposal_id: proposalIdRef.current } : {}),
          human_constraints: constraints.trim() || undefined,
          carry_ancestor_context: carryAncestorContext,
        });
        if (isAcceptedPlanJobResponse(response)) {
          accepted = true;
          setPlanJob(response.job);
          setDiagnostics(response.diagnostics ?? []);
          setGenerationSummary(response.summary ?? null);
          setProposalId(response.job.proposal_id ?? initialProposalId);
          proposalIdRef.current = response.job.proposal_id ?? initialProposalId;
          setSourcePacket(response.job.source_packet ?? null);
          setPlans([]);
          setPhase("outline");
        } else {
          setPlanJob(null);
          applyPlanPayload(response);
        }
      } catch (error) {
        const failure = planGenerationErrorDetails(error);
        setGenerationSummary(failure.summary);
        setDiagnostics(failure.diagnostics);
        setPlanError(true);
      } finally {
        if (!accepted) setLoadingPlans(false);
      }
    },
    [applyPlanPayload, carryAncestorContext, commentId, initialProposalId, issueId],
  );

  useEffect(() => {
    void loadPlans("");
  }, [loadPlans]);

  useEffect(() => {
    if (!planJob || isTerminalPlanJob(planJob)) return;
    let disposed = false;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const poll = async () => {
      try {
        const next = await api.getSubissuePlanJob(planJob.id);
        if (disposed) return;
        setPlanJob(next);
        if (isTerminalPlanJob(next)) {
          setPlanJobAction(null);
          if (next.status === "succeeded") {
            applyPlanPayload({
              plans: next.plans,
              proposal_id: next.proposal_id,
              source_packet: next.source_packet,
              diagnostics,
              summary: { status: "success", total_duration_ms: 0, upstream_duration_ms: 0, upstream_calls: 0, retry_count: 0 },
            });
            setPlanError(next.plans.length === 0);
          } else {
            setPlans([]);
            setPlanError(true);
          }
          setLoadingPlans(false);
          return;
        }
        timer = setTimeout(poll, 1000);
      } catch {
        if (disposed) return;
        setPlanError(true);
        setLoadingPlans(false);
        setPlanJobAction(null);
      }
    };
    timer = setTimeout(poll, 700);
    return () => {
      disposed = true;
      if (timer) clearTimeout(timer);
    };
  }, [applyPlanPayload, diagnostics, planJob]);

  const selectedPlan = useMemo(
    () => plans.find((plan) => plan.id === selectedPlanId) ?? null,
    [plans, selectedPlanId],
  );
  const diagnosticByPlanId = useMemo(
    () => new Map(diagnostics.map((diagnostic) => [diagnostic.plan_id, diagnostic])),
    [diagnostics],
  );
  const selectedDiagnostic = selectedPlan ? diagnosticByPlanId.get(selectedPlan.id) : undefined;
  const outlineValid = !!selectedPlan && selectedPlan.items.length > 0 && selectedPlan.items.every((item) => item.title.trim() && item.goal.trim());
  const busy = loadingPlans || loadingDetails || creating;
  const checkedCount = drafts.filter((draft) => draft.checked).length;
  const single = drafts.length === 1;

  const handleCancelPlanJob = async () => {
    if (!planJob || planJobAction) return;
    setPlanJobAction("cancel");
    try {
      const next = await api.cancelSubissuePlanJob(planJob.id);
      setPlanJob(next);
      setLoadingPlans(false);
      setPlanError(true);
    } finally {
      setPlanJobAction(null);
    }
  };

  const handleRetryPlanJob = async () => {
    if (!planJob || planJobAction) return;
    setPlanJobAction("retry");
    setPlanError(false);
    setLoadingPlans(true);
    try {
      const response = await api.retrySubissuePlanJob(planJob.id);
      if (isAcceptedPlanJobResponse(response)) {
        setPlanJob(response.job);
        setSourcePacket(response.job.source_packet ?? null);
        setDiagnostics(response.diagnostics ?? []);
        setGenerationSummary(response.summary ?? null);
        setPlans([]);
        setLoadingPlans(true);
      } else {
        setPlanJob(response);
        if (response.status === "succeeded") {
          applyPlanPayload({ plans: response.plans, proposal_id: response.proposal_id, source_packet: response.source_packet, diagnostics });
          setPlanError(response.plans.length === 0);
          setLoadingPlans(false);
        } else {
          setPlans([]);
          setPlanError(true);
          setLoadingPlans(false);
        }
      }
    } catch {
      setPlanError(true);
      setLoadingPlans(false);
    } finally {
      setPlanJobAction(null);
    }
  };

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
    const businesses = new Set(selected.map((item) => item.business));
    const business = businesses.size === 1 ? selected[0]?.business : undefined;
    const isSummaryTest = selected.every((item) => item.kind === "summary_test") && businesses.size === 1;
    const merged: SubIssuePlanItem = {
      id: `${selectedPlan.id}-merged-${Date.now()}`,
      title: formatBusinessTitle(
        selected.map((item) => stripBusinessPrefix(item.title).trim()).filter(Boolean).join(" + "),
        business || OVERALL_BUSINESS,
        isSummaryTest,
      ),
      goal: selected.map((item) => item.goal.trim()).filter(Boolean).join("\n\n"),
    };
    if (isSummaryTest) {
      merged.kind = "summary_test";
      merged.business = business;
    }
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
        ...(commentId ? { comment_id: commentId } : {}),
        ...(proposalId ? { proposal_id: proposalId } : {}),
        human_constraints: humanConstraints.trim() || undefined,
        carry_ancestor_context: carryAncestorContext,
        plan: selectedPlan,
      });
      setDrafts(response.subissues.map(draftFromSuggestion));
      setGenerationKey((current) => current ?? `${Date.now()}-${Math.random().toString(36).slice(2)}`);
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
      // SIY-177: descriptions carry only the task's own content; the chain's
      // execution context is inherited structurally at runtime. Anything the
      // user typed into "本 issue 专属执行上下文" is stamped on every created
      // child as its own context document.
      const executionContextDocument = toExecutionContext(ownExecutionContext);
      if (proposalId) {
        // The create mode is part of the user's reviewed decision. Keep it in
        // the idempotency scope so switching from backlog to todo (or back)
        // creates the corresponding independent generation instead of
        // reusing the first mode's response.
        const baseGenerationKey = generationKey ?? `${Date.now()}-${Math.random().toString(36).slice(2)}`;
        const generation = await api.createProposalGeneration(proposalId, {
          strategy: selectedPlan?.strategy ?? selectedPlan?.id ?? "manual",
          create_mode: createMode,
          idempotency_key: `${baseGenerationKey}:${createMode}`,
          items: selected.map((draft) => ({
            id: draft.id,
            title: draft.title.trim(),
            goal: draft.goal.trim() || undefined,
            description: draft.description.trim() || undefined,
            stage: draft.stage,
            parent_issue_id: draft.parentIssueId ?? undefined,
            execution_context: executionContextDocument,
          })),
        });
        toast.success(`已创建 ${generation.issues.length} 个子 issue${createMode === "backlog" ? "（暂不执行）" : ""}`);
        await queryClient.invalidateQueries({ queryKey: issueKeys.timeline(issueId) });
        onClose();
        return;
      }
      const results = await Promise.allSettled(
        selected.map((draft) =>
          createIssue.mutateAsync({
            title: draft.title.trim(),
            description: draft.description.trim() || undefined,
            status: createMode,
            project_id: sourceIssue?.project_id ?? undefined,
            parent_issue_id: draft.parentIssueId ?? undefined,
            stage: draft.stage,
            ancestor_context_refs: carryAncestorContext ? ancestorContextRefs : undefined,
            execution_context: executionContextDocument,
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
        <DialogContent className="flex flex-col sm:max-w-2xl">
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
            <div className="flex min-h-0 flex-1 flex-col gap-4 overflow-y-auto">
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

              <div className="flex items-center justify-between rounded-md border border-border/60 p-2.5">
                <div>
                  <div className="text-body font-medium">{t(($) => $.suggest_subissues.ancestor_context_label)}</div>
                  <div className="text-caption text-muted-foreground">{t(($) => $.suggest_subissues.ancestor_context_hint)}</div>
                </div>
                <Checkbox checked={carryAncestorContext} onCheckedChange={(value) => setCarryAncestorContext(value === true)} />
              </div>

              {/* SIY-177: the execution context the children will inherit —
                  collapsed by default, read-only, with per-entry provenance. */}
              <InheritedExecutionContextCard resolved={inheritedExecutionContext} />

              {sourcePacket?.agent_answer && (
                <Collapsible className="rounded-lg border border-border/60" data-testid="subissue-source-evidence">
                  <CollapsibleTrigger className="flex w-full items-center justify-between gap-2 p-3 text-left hover:bg-muted/30">
                    <span className="flex min-w-0 items-center gap-1.5">
                      <ChevronRight className="h-3.5 w-3.5 shrink-0" />
                      <span className="text-body font-medium">原始 agent 回复（保真证据）</span>
                    </span>
                    <span className="shrink-0 text-caption text-muted-foreground">
                      {sourcePacket.source_issue.identifier || "来源讨论"}
                    </span>
                  </CollapsibleTrigger>
                  <CollapsibleContent className="border-t border-border/60 p-3">
                    <div className="mb-2 text-caption text-muted-foreground">
                      这是生成子 issue 的事实来源。方案摘要仅用于辅助拆分，创建后的 description 会保留下面的原文。
                    </div>
                    <pre className="max-h-80 overflow-auto whitespace-pre-wrap break-words rounded-md bg-muted/40 p-2 text-caption">
                      {sourcePacket.agent_answer}
                    </pre>
                    {sourcePacket.coverage_note && (
                      <div className="mt-2 rounded-md bg-amber-500/10 px-2.5 py-2 text-caption text-amber-700 dark:text-amber-300">
                        {sourcePacket.coverage_note}
                      </div>
                    )}
                  </CollapsibleContent>
                </Collapsible>
              )}

              {loadingPlans && (
                <div className="flex items-center justify-center gap-2 py-8 text-body text-muted-foreground">
                  <Loader2 className="h-4 w-4 animate-spin" />
                  {planJob && !isTerminalPlanJob(planJob)
                    ? t(($) => $.suggest_subissues.fallback_pending)
                    : t(($) => $.suggest_subissues.outline_loading)}
                </div>
              )}

              {planJob && (
                <div
                  data-testid="subissue-plan-fallback-status"
                  className={`rounded-lg border p-3 ${planJob.status === "failed" || planJob.status === "expired" ? "border-destructive/50" : "border-border/60"}`}
                >
                  <div className="flex flex-wrap items-center justify-between gap-2">
                    <div className="text-body font-medium">
                      {planJob.status === "queued" || planJob.status === "running"
                        ? t(($) => $.suggest_subissues.fallback_pending)
                        : planJob.status === "succeeded"
                          ? t(($) => $.suggest_subissues.fallback_succeeded)
                          : t(($) => $.suggest_subissues.fallback_failed)}
                    </div>
                    <Badge variant="secondary">{planJob.source || "—"}</Badge>
                  </div>
                  <div className="mt-2 grid gap-2 sm:grid-cols-4">
                    <DiagnosticStat label={t(($) => $.suggest_subissues.fallback_source)} value={planJob.source} />
                    <DiagnosticStat label={t(($) => $.suggest_subissues.fallback_reason)} value={planJob.fallback_reason} />
                    <DiagnosticStat label={t(($) => $.suggest_subissues.fallback_agent)} value={planJob.agent_id} />
                    <DiagnosticStat label={t(($) => $.suggest_subissues.fallback_task)} value={planJob.task_id} />
                  </div>
                  {planJob.error_summary && (
                    <div className="mt-2 rounded-md bg-destructive/10 px-2.5 py-2 text-caption text-destructive">
                      {planJob.error_summary}
                    </div>
                  )}
                  <div className="mt-2 flex flex-wrap justify-end gap-2">
                    {(planJob.status === "queued" || planJob.status === "running") && (
                      <Button type="button" variant="outline" size="sm" onClick={() => void handleCancelPlanJob()} disabled={!!planJobAction}>
                        {planJobAction === "cancel" && <Loader2 className="h-3.5 w-3.5 animate-spin" />}
                        {t(($) => $.suggest_subissues.fallback_cancel)}
                      </Button>
                    )}
                    {(planJob.status === "failed" || planJob.status === "cancelled" || planJob.status === "expired") && (
                      <Button type="button" variant="outline" size="sm" onClick={() => void handleRetryPlanJob()} disabled={!!planJobAction}>
                        {planJobAction === "retry" && <Loader2 className="h-3.5 w-3.5 animate-spin" />}
                        {t(($) => $.suggest_subissues.fallback_retry)}
                      </Button>
                    )}
                  </div>
                </div>
              )}

              {!loadingPlans && generationSummary && (
                <div className={`rounded-lg border p-3 ${generationSummary.status === "failed" ? "border-destructive/50" : "border-border/60"}`}>
                  <div className="mb-2 flex items-center justify-between gap-2">
                    <div className="text-body font-medium">
                      {generationSummary.status === "failed"
                        ? t(($) => $.suggest_subissues.generation_status_failed)
                        : t(($) => $.suggest_subissues.generation_status_success)}
                    </div>
                    <div className="text-caption text-muted-foreground">
                      {t(($) => $.suggest_subissues.generation_summary_hint)}
                    </div>
                  </div>
                  <div className="grid gap-2 sm:grid-cols-4">
                    <DiagnosticStat
                      label={t(($) => $.suggest_subissues.generation_total_duration)}
                      value={generationSummary.total_duration_ms}
                    />
                    <DiagnosticStat
                      label={t(($) => $.suggest_subissues.generation_upstream_duration)}
                      value={generationSummary.upstream_duration_ms}
                    />
                    <DiagnosticStat
                      label={t(($) => $.suggest_subissues.generation_calls)}
                      value={generationSummary.upstream_calls}
                    />
                    <DiagnosticStat
                      label={t(($) => $.suggest_subissues.generation_retries)}
                      value={generationSummary.retry_count}
                    />
                  </div>
                  {generationSummary.error && (
                    <div className="mt-2 rounded-md bg-destructive/10 px-2.5 py-2 text-caption text-destructive">
                      <span className="font-medium">{t(($) => $.suggest_subissues.generation_error)}：</span>
                      {generationSummary.error}
                    </div>
                  )}
                </div>
              )}

              {!loadingPlans && planError && diagnostics.length > 0 && (
                <div className="rounded-lg border border-destructive/50 p-3">
                  <div className="mb-2 text-body font-medium">
                    {t(($) => $.suggest_subissues.failed_stage_details)}
                  </div>
                  <div className="space-y-2">
                    {diagnostics.map((diagnostic) => (
                      <div key={`${diagnostic.plan_id || diagnostic.strategy}-${diagnostic.attempt}`} className="rounded-md bg-muted/40 p-2.5">
                        <div className="flex flex-wrap items-center justify-between gap-2">
                          <span className="text-body font-medium">{diagnostic.name || diagnostic.strategy}</span>
                          <span className="text-caption text-muted-foreground">
                            {t(($) => $.suggest_subissues.diagnostics_duration)}: {diagnostic.upstream_duration_ms ?? 0} ms
                          </span>
                        </div>
                        <div className="mt-1 flex flex-wrap gap-x-3 gap-y-1 text-caption text-muted-foreground">
                          <span>{t(($) => $.suggest_subissues.diagnostics_attempt_count)}: {diagnostic.attempts ?? 0}</span>
                          <span>{t(($) => $.suggest_subissues.diagnostics_retry_count)}: {diagnostic.retry_count ?? 0}</span>
                          {diagnostic.error && <span className="text-destructive">{diagnostic.error}</span>}
                        </div>
                      </div>
                    ))}
                  </div>
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
                <div className="flex flex-col gap-3">
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
                          setDiagnosticsOpen(false);
                          setMergeSelection([]);
                        }}
                      >
                        <div className="flex items-center justify-between gap-2">
                          <div className="font-medium text-body">{plan.name}</div>
                          {plan.coverage === "full" && (
                            <Badge variant="secondary">{t(($) => $.suggest_subissues.plan_full_badge)}</Badge>
                          )}
                        </div>
                        <div className="mt-1 text-caption text-muted-foreground">
                          {t(($) => $.suggest_subissues.plan_item_count, { count: plan.items.length })}
                        </div>
                        {diagnosticByPlanId.get(plan.id)?.model && (
                          <div className="mt-1 truncate text-caption text-muted-foreground">
                            {diagnosticByPlanId.get(plan.id)?.model}
                          </div>
                        )}
                      </button>
                    ))}
                  </div>

                  {selectedPlan && selectedPlan.coverage !== "full" && (
                    <div className="text-caption text-muted-foreground">
                      {t(($) => $.suggest_subissues.plan_reference_hint)}
                    </div>
                  )}

                  {selectedDiagnostic && (
                    <Collapsible
                      open={diagnosticsOpen}
                      onOpenChange={setDiagnosticsOpen}
                      className="rounded-lg border border-border/60"
                    >
                      <CollapsibleTrigger className="flex w-full items-center justify-between gap-2 p-3 text-left hover:bg-muted/30">
                        <span className="flex min-w-0 items-center gap-1.5">
                          <ChevronRight className={`h-3.5 w-3.5 shrink-0 transition-transform ${diagnosticsOpen ? "rotate-90" : ""}`} />
                          <span className="text-body font-medium">{t(($) => $.suggest_subissues.diagnostics_title)}</span>
                          <span className="truncate text-caption text-muted-foreground">
                            {selectedDiagnostic.model || t(($) => $.suggest_subissues.diagnostics_unavailable)}
                          </span>
                        </span>
                        <span className="shrink-0 text-caption text-muted-foreground">
                          {t(($) => $.suggest_subissues.diagnostics_hint)}
                        </span>
                      </CollapsibleTrigger>
                      <CollapsibleContent className="space-y-3 border-t border-border/60 p-3">
                        <div className="grid gap-2 sm:grid-cols-3">
                          <DiagnosticStat label={t(($) => $.suggest_subissues.diagnostics_model)} value={selectedDiagnostic.model} />
                          <DiagnosticStat label={t(($) => $.suggest_subissues.diagnostics_source)} value={selectedDiagnostic.source} />
                          <DiagnosticStat label={t(($) => $.suggest_subissues.diagnostics_version)} value={selectedDiagnostic.version} />
                          <DiagnosticStat label={t(($) => $.suggest_subissues.diagnostics_reasoning)} value={selectedDiagnostic.reasoning_effort} />
                          <DiagnosticStat label={t(($) => $.suggest_subissues.diagnostics_temperature)} value={selectedDiagnostic.temperature} />
                          <DiagnosticStat label={t(($) => $.suggest_subissues.diagnostics_max_tokens)} value={selectedDiagnostic.max_completion_tokens} />
                          <DiagnosticStat label={t(($) => $.suggest_subissues.diagnostics_timeout)} value={selectedDiagnostic.timeout_ms} />
                          <DiagnosticStat label={t(($) => $.suggest_subissues.diagnostics_attempt)} value={selectedDiagnostic.attempt} />
                          <DiagnosticStat label={t(($) => $.suggest_subissues.diagnostics_duration)} value={selectedDiagnostic.duration_ms} />
                          <DiagnosticStat label={t(($) => $.suggest_subissues.diagnostics_upstream_duration)} value={selectedDiagnostic.upstream_duration_ms} />
                          <DiagnosticStat label={t(($) => $.suggest_subissues.diagnostics_attempt_count)} value={selectedDiagnostic.attempts} />
                          <DiagnosticStat label={t(($) => $.suggest_subissues.diagnostics_retry_count)} value={selectedDiagnostic.retry_count} />
                          <DiagnosticStat label={t(($) => $.suggest_subissues.diagnostics_status)} value={selectedDiagnostic.status} />
                          <DiagnosticStat label={t(($) => $.suggest_subissues.diagnostics_finish_reason)} value={selectedDiagnostic.finish_reason} />
                          <DiagnosticStat label={t(($) => $.suggest_subissues.diagnostics_input_chars)} value={selectedDiagnostic.input_chars} />
                          <DiagnosticStat label={t(($) => $.suggest_subissues.diagnostics_output_chars)} value={selectedDiagnostic.output_chars} />
                          <DiagnosticStat label={t(($) => $.suggest_subissues.diagnostics_prompt_tokens)} value={selectedDiagnostic.prompt_tokens} />
                          <DiagnosticStat label={t(($) => $.suggest_subissues.diagnostics_completion_tokens)} value={selectedDiagnostic.completion_tokens} />
                          <DiagnosticStat label={t(($) => $.suggest_subissues.diagnostics_total_tokens)} value={selectedDiagnostic.total_tokens} />
                        </div>
                        {selectedDiagnostic.error && (
                          <div className="rounded-md bg-destructive/10 px-2.5 py-2 text-caption text-destructive">
                            <span className="font-medium">{t(($) => $.suggest_subissues.diagnostics_error)}：</span>
                            {selectedDiagnostic.error}
                          </div>
                        )}
                        <div className="space-y-1.5">
                          <div className="text-caption font-medium">{t(($) => $.suggest_subissues.diagnostics_system_prompt)}</div>
                          <pre className="max-h-48 overflow-auto whitespace-pre-wrap break-words rounded-md bg-muted/40 p-2 text-caption">
                            {selectedDiagnostic.system_prompt}
                          </pre>
                        </div>
                        <div className="space-y-1.5">
                          <div className="text-caption font-medium">{t(($) => $.suggest_subissues.diagnostics_user_prompt)}</div>
                          <pre className="max-h-64 overflow-auto whitespace-pre-wrap break-words rounded-md bg-muted/40 p-2 text-caption">
                            {selectedDiagnostic.user_prompt}
                          </pre>
                        </div>
                        <div className="space-y-1.5">
                          <div className="text-caption font-medium">{t(($) => $.suggest_subissues.diagnostics_raw_output)}</div>
                          <pre className="max-h-48 overflow-auto whitespace-pre-wrap break-words rounded-md bg-muted/40 p-2 text-caption">
                            {selectedDiagnostic.raw_output}
                          </pre>
                        </div>
                      </CollapsibleContent>
                    </Collapsible>
                  )}

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
                                {item.kind === "summary_test" && (
                                  <Badge variant="secondary">{t(($) => $.suggest_subissues.summary_test_badge)}</Badge>
                                )}
                                <Input
                                  value={item.business ?? ""}
                                  className="h-7"
                                  aria-label={t(($) => $.suggest_subissues.business_label)}
                                  placeholder={t(($) => $.suggest_subissues.business_placeholder)}
                                  onChange={(event) => {
                                    const business = event.target.value;
                                    updatePlanItem(item.id, {
                                      business,
                                      title: formatBusinessTitle(item.title, business, item.kind === "summary_test"),
                                    });
                                  }}
                                  onBlur={() => {
                                    const business = normalizeBusiness(item.business ?? "");
                                    updatePlanItem(item.id, {
                                      business,
                                      title: formatBusinessTitle(item.title, business, item.kind === "summary_test"),
                                    });
                                  }}
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
            </div>
          )}

          {phase === "details" && (
            <div className="flex min-h-0 flex-1 flex-col gap-3 overflow-y-auto">
              <fieldset className="rounded-lg border border-border/60 p-3">
                <legend className="px-1 text-caption font-medium">创建方式</legend>
                <div className="grid gap-2 sm:grid-cols-2">
                  <label className={`flex cursor-pointer gap-2 rounded-md border p-2.5 text-left ${createMode === "backlog" ? "border-primary bg-primary/5" : "border-border/60"}`}>
                    <input
                      type="radio"
                      name="proposal-create-mode"
                      value="backlog"
                      checked={createMode === "backlog"}
                      onChange={() => setCreateMode("backlog")}
                      className="mt-0.5"
                    />
                    <span>
                      <span className="block text-body font-medium">仅创建，暂不执行</span>
                      <span className="block text-caption text-muted-foreground">子 issue 进入 backlog，不启动 agent（默认）</span>
                    </span>
                  </label>
                  <label className={`flex cursor-pointer gap-2 rounded-md border p-2.5 text-left ${createMode === "todo" ? "border-primary bg-primary/5" : "border-border/60"}`}>
                    <input
                      type="radio"
                      name="proposal-create-mode"
                      value="todo"
                      checked={createMode === "todo"}
                      onChange={() => setCreateMode("todo")}
                      className="mt-0.5"
                    />
                    <span>
                      <span className="block text-body font-medium">创建并开始执行</span>
                      <span className="block text-caption text-muted-foreground">提交事务后将 todo 子 issue 交给 agent</span>
                    </span>
                  </label>
                </div>
              </fieldset>
              {/* SIY-177: issue-specific execution context — special directories
                  or commands that apply only to the issues being created here.
                  Whether they flow further down is decided by the inherit
                  switch inside the editor. */}
              <Collapsible open={ownContextOpen} onOpenChange={setOwnContextOpen}>
                <CollapsibleTrigger className="flex w-full items-center gap-1.5 rounded-md border border-border/60 px-2.5 py-2 text-left hover:bg-muted/30">
                  <ChevronRight className={`h-3.5 w-3.5 shrink-0 transition-transform ${ownContextOpen ? "rotate-90" : ""}`} />
                  <span className="text-body font-medium">
                    {t(($) => $.execution_context.own_title)}
                  </span>
                  {ownExecutionContext.content.trim().length > 0 && (
                    <span className="text-caption text-muted-foreground">
                      {t(($) => $.execution_context.content_filled)}
                    </span>
                  )}
                </CollapsibleTrigger>
                <CollapsibleContent className="pt-1.5">
                  <ExecutionContextEditor value={ownExecutionContext} onChange={setOwnExecutionContext} />
                </CollapsibleContent>
              </Collapsible>

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

          <DialogFooter className="shrink-0">
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
