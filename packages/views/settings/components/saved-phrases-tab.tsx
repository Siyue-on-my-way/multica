"use client";

import { useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Archive, Loader2, Plus, Save, Star } from "lucide-react";
import { toast } from "sonner";
import { api } from "@multica/core/api";
import { useCurrentWorkspace } from "@multica/core/paths";
import type { SavedPhrase } from "@multica/core/types";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Textarea } from "@multica/ui/components/ui/textarea";
import { SettingsCard, SettingsSection, SettingsTab } from "./settings-layout";

const SAVED_PHRASES_KEY = "saved-phrases";

export function SavedPhrasesTab() {
  const workspace = useCurrentWorkspace();
  const wsId = workspace?.id ?? "";
  const queryClient = useQueryClient();
  const { data: phrases = [], isLoading } = useQuery({
    queryKey: [SAVED_PHRASES_KEY, wsId],
    queryFn: () => api.listSavedPhrases(),
    enabled: !!wsId,
  });
  const [name, setName] = useState("");
  const [content, setContent] = useState("");
  const [category, setCategory] = useState("general");
  const [scope, setScope] = useState<"public" | "workspace">("public");
  const [saving, setSaving] = useState(false);
  const [editingId, setEditingId] = useState<string | null>(null);

  const invalidate = () => queryClient.invalidateQueries({ queryKey: [SAVED_PHRASES_KEY, wsId] });

  const resetForm = () => {
    setEditingId(null);
    setName("");
    setContent("");
    setCategory("general");
    setScope("public");
  };

  const startEdit = (phrase: SavedPhrase) => {
    setEditingId(phrase.id);
    setName(phrase.name);
    setContent(phrase.content);
    setCategory(phrase.category);
    setScope(phrase.scope === "workspace" ? "workspace" : "public");
  };

  const save = async () => {
    if (!name.trim() || !content.trim() || saving) return;
    setSaving(true);
    try {
      if (editingId) {
        await api.updateSavedPhrase(editingId, { name, content, category, scope });
        toast.success("常用语已更新");
      } else {
        await api.createSavedPhrase({ name, content, category, scope });
        toast.success("常用语已创建");
      }
      await invalidate();
      resetForm();
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "保存常用语失败");
    } finally {
      setSaving(false);
    }
  };

  const archive = async (phrase: SavedPhrase) => {
    try {
      await api.archiveSavedPhrase(phrase.id);
      await invalidate();
      if (editingId === phrase.id) resetForm();
      toast.success("常用语已归档");
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "归档常用语失败");
    }
  };

  return (
    <SettingsTab title="公共常用语" description="管理评论和回复中可复用的公共常用语。归档只影响后续选择，不会改变历史评论。">
      <SettingsSection title={editingId ? "编辑常用语" : "新建常用语"}>
        <SettingsCard>
          <div className="space-y-3 p-4">
            <div className="grid gap-3 sm:grid-cols-2">
              <Input value={name} onChange={(event) => setName(event.target.value)} placeholder="名称，例如：代码审查" />
              <div className="flex gap-2">
                <Input value={category} onChange={(event) => setCategory(event.target.value)} placeholder="分类" />
                <select
                  value={scope}
                  onChange={(event) => setScope(event.target.value as "public" | "workspace")}
                  className="h-9 rounded-md border border-border bg-background px-2 text-body"
                  aria-label="常用语范围"
                >
                  <option value="public">公共</option>
                  <option value="workspace">仅我的工作区</option>
                </select>
              </div>
            </div>
            <Textarea value={content} onChange={(event) => setContent(event.target.value)} placeholder="输入插入评论前仍可编辑的内容" className="min-h-28" />
            <div className="flex justify-end gap-2">
              {editingId && <Button type="button" variant="ghost" onClick={resetForm}>取消</Button>}
              <Button type="button" onClick={() => void save()} disabled={saving || !name.trim() || !content.trim()}>
                {saving ? <Loader2 className="size-3.5 animate-spin" /> : editingId ? <Save className="size-3.5" /> : <Plus className="size-3.5" />}
                {editingId ? "保存修改" : "创建常用语"}
              </Button>
            </div>
          </div>
        </SettingsCard>
      </SettingsSection>

      <SettingsSection title={`已发布常用语（${phrases.length}）`}>
        <SettingsCard>
          {isLoading ? (
            <div className="flex items-center justify-center gap-2 p-8 text-muted-foreground"><Loader2 className="size-4 animate-spin" />加载中…</div>
          ) : phrases.length === 0 ? (
            <p className="p-8 text-center text-body text-muted-foreground">还没有公共常用语。</p>
          ) : phrases.map((phrase) => (
            <div key={phrase.id} className="flex gap-4 p-4">
              <div className="min-w-0 flex-1">
                <div className="flex items-center gap-2">
                  {phrase.pinned && <Star className="size-3.5 fill-current text-warning" aria-label="已置顶" />}
                  <span className="font-medium">{phrase.name}</span>
                  <span className="rounded bg-muted px-1.5 py-0.5 text-micro text-muted-foreground">{phrase.category}</span>
                  <span className="text-micro text-muted-foreground">使用 {phrase.use_count} 次</span>
                </div>
                <p className="mt-1 whitespace-pre-wrap break-words text-caption text-muted-foreground">{phrase.content}</p>
              </div>
              <div className="flex shrink-0 items-start gap-1">
                <Button type="button" size="sm" variant="ghost" onClick={() => startEdit(phrase)}>编辑</Button>
                <Button type="button" size="sm" variant="ghost" className="text-destructive" onClick={() => void archive(phrase)}>
                  <Archive className="size-3.5" />归档
                </Button>
              </div>
            </div>
          ))}
        </SettingsCard>
      </SettingsSection>
    </SettingsTab>
  );
}
