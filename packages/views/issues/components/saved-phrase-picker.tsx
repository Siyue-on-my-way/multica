"use client";

import { useMemo, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { BookOpen, Copy, Loader2, Plus, Search } from "lucide-react";
import { toast } from "sonner";
import { api } from "@multica/core/api";
import { useCurrentWorkspace } from "@multica/core/paths";
import type { SavedPhrase } from "@multica/core/types";
import { copyText } from "@multica/ui/lib/clipboard";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Popover, PopoverContent, PopoverTrigger } from "@multica/ui/components/ui/popover";

export interface SavedPhrasePickerProps {
  draft?: string;
  onInsert: (phrase: SavedPhrase) => void;
}

/**
 * Small, composer-friendly phrase browser. Selecting a phrase only calls
 * onInsert; the composer owns the editor and therefore remains responsible
 * for sending. Keeping that boundary explicit prevents a saved phrase from
 * accidentally becoming an execution shortcut.
 */
export function SavedPhrasePicker({ draft = "", onInsert }: SavedPhrasePickerProps) {
  const workspace = useCurrentWorkspace();
  const wsId = workspace?.id ?? "";
  const queryClient = useQueryClient();
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const [category, setCategory] = useState("");
  const [name, setName] = useState("");
  const [saving, setSaving] = useState(false);

  const { data: phrases = [], isLoading } = useQuery({
    queryKey: ["saved-phrases", wsId, query, category],
    queryFn: () => api.listSavedPhrases({ q: query, category }),
    enabled: open && wsId !== "",
  });
  const categories = useMemo(
    () => [...new Set(phrases.map((phrase) => phrase.category).filter(Boolean))].sort(),
    [phrases],
  );

  const handleInsert = (phrase: SavedPhrase) => {
    onInsert(phrase);
    void api.useSavedPhrase(phrase.id).catch(() => undefined);
    setOpen(false);
  };

  const handleCopy = async (phrase: SavedPhrase) => {
    if (await copyText(phrase.content)) toast.success("已复制常用语");
  };

  const handleSaveDraft = async () => {
    const content = draft.trim();
    const phraseName = name.trim();
    if (!content || !phraseName || saving) return;
    setSaving(true);
    try {
      await api.createSavedPhrase({
        name: phraseName,
        content,
        category: "general",
        scope: "public",
      });
      await queryClient.invalidateQueries({ queryKey: ["saved-phrases", wsId] });
      setName("");
      toast.success("已保存为公共常用语");
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "保存常用语失败");
    } finally {
      setSaving(false);
    }
  };

  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger
        render={
          <Button
            type="button"
            size="sm"
            variant="ghost"
            className="h-7 gap-1 px-2 text-caption text-muted-foreground hover:text-foreground"
            aria-label="打开常用语"
          >
            <BookOpen className="size-3.5" aria-hidden />
            常用语
          </Button>
        }
      />
      <PopoverContent align="end" className="w-80 max-w-[calc(100vw-1.5rem)] p-3">
        <div className="flex items-center gap-2">
          <div className="relative min-w-0 flex-1">
            <Search className="pointer-events-none absolute left-2 top-1/2 size-3.5 -translate-y-1/2 text-muted-foreground" aria-hidden />
            <Input
              value={query}
              onChange={(event) => setQuery(event.target.value)}
              placeholder="搜索常用语"
              className="h-8 pl-7 text-caption"
              autoFocus
            />
          </div>
          <select
            value={category}
            onChange={(event) => setCategory(event.target.value)}
            className="h-8 max-w-24 rounded-md border border-border bg-background px-1.5 text-caption"
            aria-label="常用语分类"
          >
            <option value="">全部</option>
            {categories.map((item) => <option key={item} value={item}>{item}</option>)}
          </select>
        </div>

        <div className="max-h-56 space-y-1 overflow-y-auto py-1">
          {isLoading ? (
            <div className="flex items-center justify-center gap-2 py-5 text-caption text-muted-foreground">
              <Loader2 className="size-3.5 animate-spin" /> 加载中…
            </div>
          ) : phrases.length === 0 ? (
            <p className="py-5 text-center text-caption text-muted-foreground">暂无可用常用语</p>
          ) : phrases.map((phrase) => (
            <div key={phrase.id} className="group flex items-start gap-1 rounded-md px-1.5 py-1.5 hover:bg-muted/60">
              <button
                type="button"
                className="min-w-0 flex-1 text-left"
                onClick={() => handleInsert(phrase)}
              >
                <div className="truncate text-caption font-medium">{phrase.name}</div>
                <div className="mt-0.5 line-clamp-2 whitespace-pre-wrap text-micro text-muted-foreground">{phrase.content}</div>
              </button>
              <Button
                type="button"
                size="icon-sm"
                variant="ghost"
                className="size-6 shrink-0 opacity-60 hover:opacity-100"
                aria-label={`复制${phrase.name}`}
                onClick={() => void handleCopy(phrase)}
              >
                <Copy className="size-3" aria-hidden />
              </Button>
            </div>
          ))}
        </div>

        <div className="border-t border-border/60 pt-2">
          <div className="mb-1 text-micro font-medium text-muted-foreground">保存当前草稿</div>
          <div className="flex gap-1.5">
            <Input
              value={name}
              onChange={(event) => setName(event.target.value)}
              placeholder="常用语名称"
              className="h-8 min-w-0 text-caption"
              disabled={!draft.trim() || saving}
            />
            <Button
              type="button"
              size="sm"
              className="h-8 shrink-0 gap-1"
              onClick={() => void handleSaveDraft()}
              disabled={!draft.trim() || !name.trim() || saving}
            >
              {saving ? <Loader2 className="size-3.5 animate-spin" /> : <Plus className="size-3.5" />}
              保存
            </Button>
          </div>
        </div>
      </PopoverContent>
    </Popover>
  );
}
