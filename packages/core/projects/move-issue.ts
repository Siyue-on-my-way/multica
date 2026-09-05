import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import { issueKeys } from "../issues/queries";
import { projectKeys } from "./queries";
import { workspaceKeys } from "../workspace/queries";
import type { MoveIssueWorkspaceRequest } from "../types";

export function useMoveIssueToWorkspace(issueId: string, sourceWorkspaceId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (request: MoveIssueWorkspaceRequest) => api.moveIssueToWorkspace(issueId, request),
    onSuccess: async (_issue, request) => {
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: issueKeys.all(sourceWorkspaceId) }),
        queryClient.invalidateQueries({ queryKey: issueKeys.all(request.target_workspace_id) }),
        queryClient.invalidateQueries({ queryKey: projectKeys.all(sourceWorkspaceId) }),
        queryClient.invalidateQueries({ queryKey: projectKeys.all(request.target_workspace_id) }),
        queryClient.invalidateQueries({ queryKey: workspaceKeys.list() }),
      ]);
    },
  });
}
