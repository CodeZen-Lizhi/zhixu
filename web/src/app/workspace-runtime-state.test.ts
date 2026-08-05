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
    const cancel = vi.spyOn(queryClient, "cancelQueries");
    const remove = vi.spyOn(queryClient, "removeQueries");

    expect(workspaceQueryRoots).toContain("authoring");
    expect(workspaceQueryRoots).toContain("document-history");
    expect(workspaceQueryRoots).toContain("organizing");

    await clearWorkspaceRuntimeState(queryClient, workspaceId);

    expect(cancel.mock.calls.map(([filters]) => filters?.queryKey)).toEqual(
      workspaceQueryRoots.map((root) => [root, workspaceId]),
    );
    expect(remove.mock.calls.map(([filters]) => filters?.queryKey)).toEqual(
      workspaceQueryRoots.map((root) => [root, workspaceId]),
    );
    const lastCancelOrder = Math.max(...cancel.mock.invocationCallOrder);
    const firstRemoveOrder = Math.min(...remove.mock.invocationCallOrder);
    expect(lastCancelOrder).toBeLessThan(firstRemoveOrder);
    expect(queryClient.getQueryData(["artifacts", workspaceId, "detail"])).toBeUndefined();
    expect(queryClient.getQueryData(["artifacts", otherWorkspaceId, "detail"])).toBe("artifacts");
  });
});
