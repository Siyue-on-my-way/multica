"use client";

import { useMemo, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { File as FileIcon, Loader2, Plus, Search } from "@multica/ui/icons";
import { toast } from "sonner";

import type { Attachment } from "@multica/core/types";
import type { WorkspaceFile } from "@multica/core/types/workspace-file";
import { api } from "@multica/core/api";
import { useWorkspaceId } from "@multica/core/hooks";
import { MAX_FILE_SIZE } from "@multica/core/constants/upload";
import { workspaceFileListOptions } from "@multica/core/files/queries";
import { useUploadWorkspaceFile } from "@multica/core/files/mutations";
import { Button } from "@multica/ui/components/ui/button";
import { Checkbox } from "@multica/ui/components/ui/checkbox";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import { Input } from "@multica/ui/components/ui/input";
import { useT } from "../i18n";

// "Choose from file library" — lets a composer attach workspace library
// files without re-uploading bytes. Confirm resolves each selection to an
// attachment row (attachment.workspace_file_id references the library file)
// and hands the rows to the host composer, which inserts them exactly like
// freshly uploaded attachments — the normal send path binds and delivers
// them, and the daemon materializes a verified local copy before the agent
// starts.

interface LibraryPickerDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  /** Receives one resolved attachment row per selected library file. */
  onAttach: (attachments: Attachment[]) => void;
}

function formatBytes(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes <= 0) return "0 B";
  const units = ["B", "KB", "MB", "GB"];
  const exp = Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), units.length - 1);
  const value = bytes / 1024 ** exp;
  return `${value >= 100 || exp === 0 ? Math.round(value) : value.toFixed(1)} ${units[exp]}`;
}

export function LibraryPickerDialog({ open, onOpenChange, onAttach }: LibraryPickerDialogProps) {
  const { t } = useT("files");
  const wsId = useWorkspaceId() ?? "";
  const fileInputRef = useRef<HTMLInputElement>(null);
  const [filter, setFilter] = useState("");
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [attaching, setAttaching] = useState(false);

  const { data, isLoading } = useQuery({
    ...workspaceFileListOptions(wsId),
    enabled: open,
  });
  const uploadMutation = useUploadWorkspaceFile(wsId);

  const files = useMemo(() => data?.files ?? [], [data]);
  const filtered = useMemo(() => {
    const q = filter.trim().toLowerCase();
    if (!q) return files;
    return files.filter((f) => f.filename.toLowerCase().includes(q));
  }, [files, filter]);

  const toggle = (id: string) => {
    setSelected((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  };

  const handleUploadPicked = (list: FileList | null) => {
    if (!list || list.length === 0) return;
    for (const file of Array.from(list)) {
      if (file.size > MAX_FILE_SIZE) {
        toast.error(t(($) => $.page.fileTooLarge));
        continue;
      }
      uploadMutation.mutate(file, {
        onError: (err) => toast.error(`${t(($) => $.page.upload_failed)}: ${err.message}`),
      });
    }
    if (fileInputRef.current) fileInputRef.current.value = "";
  };

  const handleAttach = async () => {
    const chosen = files.filter((f) => selected.has(f.id));
    if (chosen.length === 0) return;
    setAttaching(true);
    const attachments: Attachment[] = [];
    try {
      for (const file of chosen) {
        attachments.push(await api.attachWorkspaceFile(file.id));
      }
      onAttach(attachments);
      setSelected(new Set());
      setFilter("");
      onOpenChange(false);
    } catch (err) {
      toast.error(`${t(($) => $.picker.attach_failed)}: ${err instanceof Error ? err.message : String(err)}`);
    } finally {
      setAttaching(false);
    }
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="flex max-h-[70vh] flex-col sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>{t(($) => $.picker.title)}</DialogTitle>
          <DialogDescription>{t(($) => $.picker.description)}</DialogDescription>
        </DialogHeader>

        <input
          ref={fileInputRef}
          type="file"
          multiple
          className="hidden"
          onChange={(e) => handleUploadPicked(e.target.files)}
        />

        <div className="flex items-center gap-2">
          <div className="relative min-w-0 flex-1">
            <Search aria-hidden="true" className="absolute top-1/2 left-2.5 size-3.5 -translate-y-1/2 text-muted-foreground" />
            <Input
              value={filter}
              onChange={(e) => setFilter(e.target.value)}
              placeholder={t(($) => $.picker.search_placeholder)}
              className="h-8 pl-8"
            />
          </div>
          <Button variant="outline" size="sm" onClick={() => fileInputRef.current?.click()}>
            <Plus className="size-3.5" />
            {t(($) => $.picker.upload_new)}
          </Button>
        </div>

        <div className="min-h-0 flex-1 overflow-y-auto rounded-md border">
          {isLoading ? (
            <div className="flex items-center justify-center py-10">
              <Loader2 className="size-4 animate-spin text-muted-foreground" />
            </div>
          ) : filtered.length === 0 ? (
            <p className="py-10 text-center text-caption text-muted-foreground">
              {files.length === 0 ? t(($) => $.picker.empty) : t(($) => $.picker.search_placeholder)}
            </p>
          ) : (
            filtered.map((file) => (
              <PickerRow
                key={file.id}
                file={file}
                checked={selected.has(file.id)}
                onToggle={() => toggle(file.id)}
              />
            ))
          )}
        </div>

        <DialogFooter className="items-center gap-2 sm:justify-between">
          <span className="text-caption text-muted-foreground">
            {t(($) => $.picker.selected, { count: selected.size })}
          </span>
          <Button size="sm" disabled={selected.size === 0 || attaching} onClick={() => void handleAttach()}>
            {attaching ? <Loader2 className="size-3.5 animate-spin" /> : t(($) => $.picker.attach_selected)}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function PickerRow({
  file,
  checked,
  onToggle,
}: {
  file: WorkspaceFile;
  checked: boolean;
  onToggle: () => void;
}) {
  return (
    <button
      type="button"
      aria-pressed={checked}
      onClick={onToggle}
      className="flex w-full items-center gap-3 border-b px-3 py-2 text-left text-body last:border-b-0 hover:bg-muted/30"
    >
      <span className="pointer-events-none">
        <Checkbox checked={checked} />
      </span>
      <FileIcon aria-hidden="true" className="size-4 shrink-0 text-muted-foreground" />
      <span className="min-w-0 flex-1 truncate" title={file.filename}>
        {file.filename}
      </span>
      <span className="shrink-0 text-caption text-muted-foreground">{formatBytes(file.size_bytes)}</span>
    </button>
  );
}
