import { QueryClient } from "@tanstack/react-query";
import { describe, expect, it, vi } from "vitest";

import { clearWorkspaceRuntimeState, workspaceQueryRoots } from "./workspace-runtime-state";

describe("clearWorkspaceRuntimeState", () => {
  it("先取消再移除完整的 Workspace 查询族，且保留其他 Workspace", async () => {
    const queryClient = new QueryClient();
    const workspaceId = "71000000-0000-4000-8000-000000000001";
    const otherWorkspaceId = "71000000-0000-4000-8000-000000000002";
    for (const root of workspaceQueryRoots) {
      queryClient.setQueryData([root, workspaceId, "detail"], root);
      queryClient.setQueryData([root, otherWorkspaceId, "detail"], root);
    }
    queryClient.setQueryData(["settings", "git-sync", workspaceId, "status"], "alpha-settings");
    queryClient.setQueryData(["settings", "git-sync", otherWorkspaceId, "status"], "beta-settings");
    queryClient.getMutationCache().build(queryClient, {
      mutationKey: ["business", workspaceId, "draft"],
      mutationFn: () => Promise.resolve(undefined),
    });
    const cancel = vi.spyOn(queryClient, "cancelQueries");
    const remove = vi.spyOn(queryClient, "removeQueries");

    expect(workspaceQueryRoots).toContain("authoring");
    expect(workspaceQueryRoots).toContain("document-history");
    expect(workspaceQueryRoots).toContain("organizing");

    await clearWorkspaceRuntimeState(queryClient, workspaceId);

    expect(cancel).toHaveBeenCalledOnce();
    expect(remove).toHaveBeenCalledOnce();
    const lastCancelOrder = Math.max(...cancel.mock.invocationCallOrder);
    const firstRemoveOrder = Math.min(...remove.mock.invocationCallOrder);
    expect(lastCancelOrder).toBeLessThan(firstRemoveOrder);
    expect(queryClient.getQueryData(["artifacts", workspaceId, "detail"])).toBeUndefined();
    expect(queryClient.getQueryData(["artifacts", otherWorkspaceId, "detail"])).toBe("artifacts");
    expect(queryClient.getQueryData(["settings", "git-sync", workspaceId, "status"])).toBeUndefined();
    expect(queryClient.getQueryData(["settings", "git-sync", otherWorkspaceId, "status"])).toBe("beta-settings");
    expect(queryClient.getMutationCache().getAll()).toHaveLength(0);
  });
});
