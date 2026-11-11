"use client";

import { useMemo, useRef, useState } from "react";
import { Download, File as FileIcon, Loader2, Plus, RefreshCw, Search, Trash2, Upload } from "@multica/ui/icons";
import { useQuery } from "@tanstack/react-query";
import { toast } from "sonner";

import type { WorkspaceFile } from "@multica/core/types/workspace-file";
import { useWorkspaceId } from "@multica/core/hooks";
import { MAX_FILE_SIZE } from "@multica/core/constants/upload";
import { workspaceFileListOptions } from "@multica/core/files/queries";
import { useDeleteWorkspaceFile, useUploadWorkspaceFile } from "@multica/core/files/mutations";
import { Button, buttonVariants } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@multica/ui/components/ui/alert-dialog";
import {
  CollectionPageHeader,
} from "../layout/collection-page";
import { useT } from "../i18n";

// ---------------------------------------------------------------------------
// Page: workspace file library. Files here outlive any single message — a
// composer references them through an attachment row without re-uploading
// bytes, and the daemon delivers a sha256-verified copy to the agent's
// machine before the run starts. Deleting a file removes its storage object
// and every referencing attachment row, which is the point: the library is
// also the storage management surface.
// ---------------------------------------------------------------------------

function formatBytes(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes <= 0) return "0 B";
  const units = ["B", "KB", "MB", "GB"];
  const exp = Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), units.length - 1);
  const value = bytes / 1024 ** exp;
  return `${value >= 100 || exp === 0 ? Math.round(value) : value.toFixed(1)} ${units[exp]}`;
}

function formatDate(iso: string): string {
  if (!iso) return "";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "";
  return d.toLocaleString(undefined, {
    year: "numeric",
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
  });
}

export function FilesPage() {
  const { t } = useT("files");
  const wsId = useWorkspaceId() ?? "";
  const fileInputRef = useRef<HTMLInputElement>(null);
  const [filter, setFilter] = useState("");
  const [pendingDelete, setPendingDelete] = useState<WorkspaceFile | null>(null);

  const { data, isLoading, isFetching, refetch } = useQuery(workspaceFileListOptions(wsId));
  const uploadMutation = useUploadWorkspaceFile(wsId);
  const deleteMutation = useDeleteWorkspaceFile(wsId);

  const files = useMemo(() => data?.files ?? [], [data]);
  const filtered = useMemo(() => {
    const q = filter.trim().toLowerCase();
    if (!q) return files;
    return files.filter((f) => f.filename.toLowerCase().includes(q));
  }, [files, filter]);

  const handlePickFiles = (list: FileList | null) => {
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
    // Reset so picking the same file again re-fires onChange.
    if (fileInputRef.current) fileInputRef.current.value = "";
  };

  const handleConfirmDelete = () => {
    if (!pendingDelete) return;
    const target = pendingDelete;
    setPendingDelete(null);
    deleteMutation.mutate(target.id, {
      onError: (err) => toast.error(`${t(($) => $.page.delete_failed)}: ${err.message}`),
    });
  };

  return (
    <div className="flex h-full flex-col overflow-hidden">
      <CollectionPageHeader
        icon={FileIcon}
        title={t(($) => $.page.title)}
        tools={
          <>
            <Button
              variant="ghost"
              size="icon"
              onClick={() => void refetch()}
              disabled={isFetching}
              aria-label={t(($) => $.page.refresh)}
              title={t(($) => $.page.refresh)}
            >
              <RefreshCw aria-hidden="true" />
            </Button>
            <Button
              variant="ghost"
              size="icon"
              onClick={() => fileInputRef.current?.click()}
              disabled={uploadMutation.isPending}
              aria-label={t(($) => $.page.upload)}
              title={t(($) => $.page.upload)}
            >
              <Plus aria-hidden="true" />
            </Button>
          </>
        }
      />

      <input
        ref={fileInputRef}
        type="file"
        multiple
        className="hidden"
        onChange={(e) => handlePickFiles(e.target.files)}
      />

      <div className="flex min-h-0 flex-1 flex-col gap-3 overflow-y-auto px-4 pb-6 md:px-6">
        <div className="flex items-center gap-3">
          <div className="relative w-full max-w-xs">
            <Search aria-hidden="true" className="absolute top-1/2 left-2.5 size-3.5 -translate-y-1/2 text-muted-foreground" />
            <Input
              value={filter}
              onChange={(e) => setFilter(e.target.value)}
              placeholder={t(($) => $.page.search_placeholder)}
              className="h-8 pl-8"
            />
          </div>
          {data ? (
            <span className="shrink-0 text-caption text-muted-foreground">
              {t(($) => $.page.total_usage, {
                count: data.total,
                size: formatBytes(data.total_bytes),
              })}
            </span>
          ) : null}
        </div>

        {isLoading ? (
          <div className="flex flex-col gap-2">
            {Array.from({ length: 4 }).map((_, i) => (
              <Skeleton key={i} className="h-12 w-full" />
            ))}
          </div>
        ) : filtered.length === 0 ? (
          <EmptyState
            hasFiles={files.length > 0}
            onUpload={() => fileInputRef.current?.click()}
          />
        ) : (
          <div className="flex flex-col overflow-hidden rounded-lg border">
            <div className="flex items-center gap-3 border-b bg-muted/40 px-3 py-2 text-caption font-medium text-muted-foreground">
              <span className="min-w-0 flex-1">{t(($) => $.page.name)}</span>
              <span className="hidden w-20 shrink-0 sm:block">{t(($) => $.page.size)}</span>
              <span className="hidden w-44 shrink-0 md:block">{t(($) => $.page.created_at)}</span>
              <span className="w-16 shrink-0" />
            </div>
            {filtered.map((file) => (
              <FileRow
                key={file.id}
                file={file}
                onDelete={() => setPendingDelete(file)}
              />
            ))}
          </div>
        )}
      </div>

      <AlertDialog open={pendingDelete !== null} onOpenChange={(open) => !open && setPendingDelete(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t(($) => $.page.delete_confirm_title)}</AlertDialogTitle>
            <AlertDialogDescription>
              {t(($) => $.page.delete_confirm_description, { filename: pendingDelete?.filename ?? "" })}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t(($) => $.page.cancel)}</AlertDialogCancel>
            <AlertDialogAction
              onClick={(e) => {
                e.preventDefault();
                handleConfirmDelete();
              }}
              className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
            >
              {deleteMutation.isPending ? <Loader2 className="size-4 animate-spin" /> : t(($) => $.page.delete_confirm_action)}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}

function FileRow({ file, onDelete }: { file: WorkspaceFile; onDelete: () => void }) {
  const { t } = useT("files");
  return (
    <div className="group flex items-center gap-3 border-b px-3 py-2 text-body last:border-b-0 hover:bg-muted/30">
      <FileIcon aria-hidden="true" className="size-4 shrink-0 text-muted-foreground" />
      <span className="min-w-0 flex-1 truncate" title={file.filename}>
        {file.filename}
      </span>
      <span className="hidden w-20 shrink-0 text-caption text-muted-foreground sm:block">
        {formatBytes(file.size_bytes)}
      </span>
      <span className="hidden w-44 shrink-0 text-caption text-muted-foreground md:block">
        {formatDate(file.created_at)}
      </span>
      <span className="flex w-16 shrink-0 items-center justify-end gap-1">
        <a
          href={file.download_url}
          download={file.filename}
          aria-label={t(($) => $.page.download)}
          title={t(($) => $.page.download)}
          className={buttonVariants({
            variant: "ghost",
            size: "icon",
            class: "size-7 opacity-0 transition-opacity group-hover:opacity-100",
          })}
        >
          <Download className="size-3.5" />
        </a>
        <Button
          variant="ghost"
          size="icon"
          className="size-7 text-destructive opacity-0 transition-opacity group-hover:opacity-100 hover:text-destructive"
          aria-label={t(($) => $.page.delete)}
          onClick={onDelete}
        >
          <Trash2 className="size-3.5" />
        </Button>
      </span>
    </div>
  );
}

function EmptyState({ hasFiles, onUpload }: { hasFiles: boolean; onUpload: () => void }) {
  const { t } = useT("files");
  return (
    <div className="flex flex-col items-center justify-center gap-3 rounded-lg border border-dashed py-16 text-center">
      <Upload aria-hidden="true" className="size-6 text-muted-foreground" />
      <div className="text-body font-medium">
        {hasFiles ? t(($) => $.page.search_placeholder) : t(($) => $.page.empty_title)}
      </div>
      {!hasFiles ? (
        <>
          <p className="max-w-sm text-caption text-muted-foreground">
            {t(($) => $.page.empty_description)}
          </p>
          <Button size="sm" onClick={onUpload}>
            <Plus className="size-3.5" />
            {t(($) => $.page.upload)}
          </Button>
        </>
      ) : null}
    </div>
  );
}
