import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import { workspaceFileKeys } from "./queries";

/** Upload adds to the library — await then invalidate so the list and the
 *  usage total refetch together (no optimistic entry: sizes are server-owned). */
export function useUploadWorkspaceFile(wsId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (file: File) => api.uploadWorkspaceFile(file),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: workspaceFileKeys.all(wsId) });
    },
  });
}

/** Delete is a confirmed flow (dialog) — no optimism. The server removes the
 *  storage object and cascades to referencing attachment rows, so every
 *  surface that renders the file must refetch. */
export function useDeleteWorkspaceFile(wsId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => api.deleteWorkspaceFile(id),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: workspaceFileKeys.all(wsId) });
      // Referencing chat messages / comments rendered the same bytes through
      // an attachment row; those surfaces prune on their own refetch cycles.
    },
  });
}
