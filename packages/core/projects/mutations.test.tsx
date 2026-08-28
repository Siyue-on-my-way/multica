/**
 * @vitest-environment jsdom
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, renderHook } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { setApiInstance } from "../api";
import type { ApiClient } from "../api/client";
import { setCurrentWorkspace } from "../platform/workspace-storage";
import { issueKeys } from "../issues/queries";
import { workspaceKeys } from "../workspace/queries";
import { projectKeys } from "./queries";
import {
  getIssueSurfaceViewStore,
  pruneIssueSurfaceViewStates,
} from "../issues/stores/surface-view-store";
import { useDeleteProject, useMigrateProject } from "./mutations";

vi.mock("../hooks", () => ({
  useWorkspaceId: () => "ws-1",
}));

function createWrapper(qc: QueryClient) {
  return function Wrapper({ children }: { children: ReactNode }) {
    return <QueryClientProvider client={qc}>{children}</QueryClientProvider>;
  };
}

describe("useDeleteProject", () => {
  let qc: QueryClient;
  let deleteProject: ReturnType<typeof vi.fn<() => Promise<void>>>;

  beforeEach(() => {
    qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    deleteProject = vi.fn().mockResolvedValue(undefined);
    setApiInstance({ deleteProject } as unknown as ApiClient);
    setCurrentWorkspace("acme", "ws-1");
  });

  afterEach(() => {
    qc.clear();
    pruneIssueSurfaceViewStates([]);
    setCurrentWorkspace(null, null);
    vi.restoreAllMocks();
  });

  it("clears the deleted project's issue surface view state", async () => {
    const store = getIssueSurfaceViewStore("project:p1");
    store.getState().setViewMode("list");
    expect(store.getState().viewMode).toBe("list");

    const { result } = renderHook(() => useDeleteProject(), {
      wrapper: createWrapper(qc),
    });

    await act(async () => {
      await result.current.mutateAsync("p1");
    });

    expect(deleteProject).toHaveBeenCalledWith("p1");
    expect(store.getState().viewMode).toBe("board");
  });
});

describe("useMigrateProject", () => {
  it("refreshes source and target project, issue, and workspace caches", async () => {
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const migrateProject = vi.fn().mockResolvedValue({});
    setApiInstance({ migrateProject } as unknown as ApiClient);
    setCurrentWorkspace("acme", "ws-1");
    const invalidateQueries = vi.spyOn(qc, "invalidateQueries");

    const { result } = renderHook(() => useMigrateProject(), {
      wrapper: createWrapper(qc),
    });

    await act(async () => {
      await result.current.mutateAsync({ id: "p1", targetWorkspaceId: "ws-2" });
    });

    expect(migrateProject).toHaveBeenCalledWith("p1", "ws-2");
    const invalidatedKeys = invalidateQueries.mock.calls.map(([filters]) => filters?.queryKey);
    expect(invalidatedKeys).toEqual(expect.arrayContaining([
      projectKeys.all("ws-1"),
      projectKeys.all("ws-2"),
      issueKeys.all("ws-1"),
      issueKeys.all("ws-2"),
      workspaceKeys.list(),
    ]));

    qc.clear();
    setCurrentWorkspace(null, null);
  });
});
