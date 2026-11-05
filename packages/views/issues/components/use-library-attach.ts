"use client";

/**
 * "Choose from file library" for the issue comment composers.
 *
 * The picker resolves each selection to an attachment row that references the
 * library bytes (no re-upload). Registering that row as an already-settled
 * draft upload and drawing + settling its card in the editor leaves the exact
 * records a normal upload leaves behind, so the submit path's
 * referenced-in-body binding and the AttachmentDownloadProvider both see it
 * like any other upload.
 */

import { useCallback, useEffect, useState, type RefObject } from "react";
import { attachmentToDraftUpload } from "@multica/core/drafts";
import type { Attachment } from "@multica/core/types";
import type { UploadResult } from "@multica/core/hooks/use-file-upload";
import { useCommentDraftStore, type CommentDraftKey } from "@multica/core/issues/stores";
import type { ContentEditorRef } from "../../editor/content-editor";

interface LibraryAttachTarget {
  /** Persisted-draft key of the composer. Undefined disables the hook (the
   *  callers hide the picker): without a persisted upload record, submit
   *  could not bind the attachment. */
  draftKey: CommentDraftKey | undefined;
  editorRef: RefObject<ContentEditorRef | null>;
  /** Lazy-editor gate: the card can only be drawn once the real editor mount
   *  replaced the static shell. */
  ready: boolean;
  activate: () => void;
}

export function useLibraryAttach({ draftKey, editorRef, ready, activate }: LibraryAttachTarget) {
  // Attachments chosen before the editor mounted — drawn on first ready, same
  // retry shape as the saved-phrase queue above the composers.
  const [pending, setPending] = useState<Attachment[] | null>(null);

  useEffect(() => {
    if (!ready || !pending || pending.length === 0) return;
    const editor = editorRef.current;
    if (!editor) return;
    for (const attachment of pending) {
      const uploadId = attachment.id;
      editor.insertUploadPlaceholder({
        uploadId,
        filename: attachment.filename,
        size: attachment.size_bytes,
      });
      const result: UploadResult = {
        ...attachment,
        link: attachment.download_url || `/api/attachments/${attachment.id}/download`,
        markdownLink: attachment.markdown_url || attachment.download_url,
      };
      editor.settleUploadPlaceholder(uploadId, result);
    }
    setPending(null);
  }, [ready, pending, editorRef]);

  const handleSelectLibraryFiles = useCallback((attachments: Attachment[]) => {
    if (attachments.length === 0 || !draftKey) return;
    const store = useCommentDraftStore.getState();
    for (const attachment of attachments) {
      store.addUpload(draftKey, attachmentToDraftUpload(attachment));
    }
    setPending(attachments);
    activate();
  }, [draftKey, activate]);

  return { handleSelectLibraryFiles };
}
