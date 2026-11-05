import { queryOptions } from "@tanstack/react-query";
import { api } from "../api";

export const workspaceFileKeys = {
  all: (wsId: string) => ["workspace-files", wsId] as const,
  list: (wsId: string) => [...workspaceFileKeys.all(wsId), "list"] as const,
};

export function workspaceFileListOptions(wsId: string) {
  return queryOptions({
    queryKey: workspaceFileKeys.list(wsId),
    queryFn: () => api.listWorkspaceFiles(),
  });
}
