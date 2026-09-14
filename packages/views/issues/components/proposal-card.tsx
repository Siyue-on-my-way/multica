"use client";

import { useMemo, useState } from "react";
import { Check, ChevronDown, Clipboard, ListTree, MessageSquareText, Plus, ShieldAlert } from "lucide-react";
import { toast } from "sonner";
import type { DiscussionProposal, ProposalSnapshot } from "@multica/core/types";
import { api } from "@multica/core/api";
import { copyText } from "@multica/ui/lib/clipboard";
import { Button } from "@multica/ui/components/ui/button";
import { Badge } from "@multica/ui/components/ui/badge";
import { cn } from "@multica/ui/lib/utils";
import { useWorkspacePaths } from "@multica/core/paths";
import { AppLink } from "../../navigation";
import { useModalStore } from "@multica/core/modals";

function normalizedSnapshot(snapshot: ProposalSnapshot): Required<Pick<ProposalSnapshot, "title" | "summary" | "scope" | "risks" | "acceptanceCriteria" | "items">> {
  return {
    title: snapshot.title?.trim() || "讨论方案",
    summary: snapshot.summary?.trim() || "",
    scope: snapshot.scope?.trim() || "",
    risks: Array.isArray(snapshot.risks) ? snapshot.risks.filter((item): item is string => typeof item === "string" && item.trim() !== "") : [],
    acceptanceCriteria: Array.isArray(snapshot.acceptanceCriteria)
      ? snapshot.acceptanceCriteria.filter((item): item is string => typeof item === "string" && item.trim() !== "")
      : [],
    items: Array.isArray(snapshot.items) ? snapshot.items : [],
  };
}

export function proposalToMarkdown(proposal: DiscussionProposal): string {
  const snapshot = normalizedSnapshot(proposal.snapshot);
  const lines = [`# ${snapshot.title}`];
  if (snapshot.summary) lines.push("", snapshot.summary);
  if (snapshot.scope) lines.push("", `范围：${snapshot.scope}`);
  if (snapshot.risks.length) lines.push("", "风险：", ...snapshot.risks.map((item) => `- ${item}`));
  if (snapshot.acceptanceCriteria.length) {
    lines.push("", "验收标准：", ...snapshot.acceptanceCriteria.map((item) => `- ${item}`));
  }
  if (snapshot.items.length) {
    lines.push("", "拆解项：", ...snapshot.items.map((item, index) => {
      const title = item.title?.trim() || `事项 ${index + 1}`;
      const goal = item.goal?.trim() || item.description?.trim();
      return goal ? `- ${title}：${goal}` : `- ${title}`;
    }));
  }
  return lines.join("\n");
}

export function ProposalCard({ issueId, proposal }: { issueId: string; proposal: DiscussionProposal }) {
  const snapshot = useMemo(() => normalizedSnapshot(proposal.snapshot), [proposal.snapshot]);
  const paths = useWorkspacePaths();
  const openModal = useModalStore((state) => state.open);
  const [expanded, setExpanded] = useState(true);
  const [saving, setSaving] = useState(false);
  const markdown = useMemo(() => proposalToMarkdown(proposal), [proposal]);

  const handleCopy = async () => {
    if (await copyText(markdown)) toast.success("方案已复制");
  };

  const handleSavePhrase = async () => {
    if (saving) return;
    setSaving(true);
    try {
      await api.createSavedPhrase({
        name: `${snapshot.title}方案`,
        content: markdown,
        category: "proposal",
        scope: "public",
      });
      toast.success("方案已保存为常用语");
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "保存方案失败");
    } finally {
      setSaving(false);
    }
  };

  const openGeneration = () => {
    openModal("suggest-subissues", {
      issueId,
      proposalId: proposal.id,
      // Decompose the reviewed answer when one exists. The question remains
      // the source-context anchor, but using it as the LLM input would ask the
      // planner to split the user's question instead of the approved plan.
      commentId: proposal.answer_comment_id ?? proposal.question_comment_id ?? "",
    });
  };

  return (
    <div className="mt-3 rounded-lg border border-primary/30 bg-primary/[0.035] p-3" data-slot="proposal-card">
      <div className="flex items-start gap-2">
        <div className="mt-0.5 rounded-md bg-primary/10 p-1.5 text-primary">
          <MessageSquareText className="size-4" aria-hidden />
        </div>
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-2">
            <span className="text-body font-semibold">{snapshot.title}</span>
            <Badge variant="secondary">方案</Badge>
            {snapshot.scope && <span className="text-micro text-muted-foreground">{snapshot.scope}</span>}
          </div>
          {snapshot.summary && <p className="mt-1 whitespace-pre-wrap text-caption leading-5 text-muted-foreground">{snapshot.summary}</p>}
        </div>
        <Button
          type="button"
          size="icon-sm"
          variant="ghost"
          aria-label={expanded ? "收起方案" : "展开方案"}
          onClick={() => setExpanded((value) => !value)}
        >
          <ChevronDown className={cn("size-4 transition-transform", !expanded && "-rotate-90")} aria-hidden />
        </Button>
      </div>

      {expanded && (
        <div className="mt-3 space-y-3 pl-9">
          {snapshot.risks.length > 0 && (
            <section>
              <h4 className="flex items-center gap-1.5 text-caption font-semibold"><ShieldAlert className="size-3.5 text-warning" />风险</h4>
              <ul className="mt-1 list-disc space-y-0.5 pl-4 text-caption text-muted-foreground">{snapshot.risks.map((item, index) => <li key={`${item}-${index}`}>{item}</li>)}</ul>
            </section>
          )}
          {snapshot.acceptanceCriteria.length > 0 && (
            <section>
              <h4 className="flex items-center gap-1.5 text-caption font-semibold"><Check className="size-3.5 text-success" />验收标准</h4>
              <ul className="mt-1 space-y-0.5 text-caption text-muted-foreground">{snapshot.acceptanceCriteria.map((item, index) => <li key={`${item}-${index}`} className="flex gap-1.5"><Check className="mt-0.5 size-3.5 shrink-0 text-success" />{item}</li>)}</ul>
            </section>
          )}
          {snapshot.items.length > 0 && (
            <section>
              <h4 className="text-caption font-semibold">拆解项（{snapshot.items.length}）</h4>
              <ol className="mt-1 space-y-1.5 text-caption">
                {snapshot.items.map((item, index) => (
                  <li key={item.id ?? `${item.title}-${index}`} className="rounded-md bg-background/70 px-2.5 py-2">
                    <div className="font-medium">{item.title || `事项 ${index + 1}`}</div>
                    {(item.goal || item.description) && <div className="mt-0.5 whitespace-pre-wrap text-muted-foreground">{item.goal || item.description}</div>}
                  </li>
                ))}
              </ol>
            </section>
          )}

          {proposal.generations.length > 0 && (
            <section className="space-y-2">
              <h4 className="flex items-center gap-1.5 text-caption font-semibold"><ListTree className="size-3.5" />已创建批次</h4>
              {proposal.generations.map((generation) => (
                <div key={generation.id} className="rounded-md border border-border/60 bg-background/60 px-2.5 py-2">
                  <div className="flex flex-wrap items-center gap-2 text-caption">
                    <span className="font-medium">{generation.strategy || "manual"}</span>
                    <Badge variant="outline">{generation.create_mode === "todo" ? "已开始执行" : "暂不执行"}</Badge>
                    <span className="text-muted-foreground">{generation.issues.length} 个子 issue</span>
                  </div>
                  {generation.issues.length > 0 && (
                    <div className="mt-1.5 flex flex-wrap gap-x-3 gap-y-1">
                      {generation.issues.map((child) => (
                        <AppLink key={child.id} href={paths.issueDetail(child.id)} className="text-caption text-primary hover:underline">
                          {child.identifier} · {child.title}
                        </AppLink>
                      ))}
                    </div>
                  )}
                </div>
              ))}
            </section>
          )}
        </div>
      )}

      <div className="mt-3 flex flex-wrap items-center gap-1.5 border-t border-primary/15 pt-2 pl-9">
        <Button type="button" size="sm" variant="ghost" className="h-7 gap-1 px-2" onClick={() => void handleCopy()}>
          <Clipboard className="size-3.5" />复制方案
        </Button>
        <Button type="button" size="sm" variant="ghost" className="h-7 gap-1 px-2" onClick={() => void handleSavePhrase()} disabled={saving}>
          <Plus className="size-3.5" />保存为常用语
        </Button>
        <Button type="button" size="sm" className="h-7 gap-1 px-2" onClick={openGeneration}>
          <ListTree className="size-3.5" />生成子 issue
        </Button>
      </div>
    </div>
  );
}
