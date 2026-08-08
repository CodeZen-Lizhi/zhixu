import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { useLayoutEffect } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";

const activeApi = vi.hoisted(() => ({ getActiveWorkspace: vi.fn() }));
vi.mock("../api/active-workspace", () => ({ getActiveWorkspace: activeApi.getActiveWorkspace }));

let activeRefetchFailed = false;
const staleWorkspaceRequest = vi.fn();

import { getActiveWorkspaceId, setActiveWorkspaceId } from "./active-workspace";
import { useActiveWorkspace, WorkspaceCacheBoundary } from "./WorkspaceCacheBoundary";

const workspaceA = {
  id: "77000000-0000-4000-8000-000000000001",
  name: "Alpha",
  rootPath: "/tmp/alpha",
  status: "active" as const,
  availability: "available" as const,
  version: 1,
};
const workspaceB = {
  ...workspaceA,
  id: "77000000-0000-4000-8000-000000000002",
  name: "Beta",
  rootPath: "/tmp/beta",
};

const Probe = () => {
  const active = useActiveWorkspace();
  useLayoutEffect(() => {
    if (activeRefetchFailed) staleWorkspaceRequest(getActiveWorkspaceId());
  });
  return <div>
    <span data-testid="workspace-state">{active.status}:{active.workspace?.id ?? "none"}</span>
    <button type="button" onClick={() => void active.refresh()}>刷新 Active Workspace</button>
  </div>;
};

const renderBoundary = (queryClient: QueryClient) => render(
  <QueryClientProvider client={queryClient}>
    <WorkspaceCacheBoundary><Probe /></WorkspaceCacheBoundary>
  </QueryClientProvider>,
);

beforeEach(() => {
  activeApi.getActiveWorkspace.mockReset();
  activeRefetchFailed = false;
  staleWorkspaceRequest.mockReset();
  setActiveWorkspaceId("");
});

describe("WorkspaceCacheBoundary", () => {
  it("认证后的服务端响应是 Active Workspace 唯一发布源", async () => {
    window.localStorage.setItem(["zhixu", "legacy", "workspace"].join("."), workspaceB.id);
    activeApi.getActiveWorkspace.mockResolvedValue(workspaceA);
    renderBoundary(new QueryClient({ defaultOptions: { queries: { retry: false } } }));

    expect(screen.getByRole("heading", { name: "正在读取当前 Workspace" })).toBeInTheDocument();
    await waitFor(() => expect(screen.getByTestId("workspace-state")).toHaveTextContent(`ready:${workspaceA.id}`));
    expect(getActiveWorkspaceId()).toBe(workspaceA.id);
  });

  it("ID 变化时先取消并移除 A 的缓存，再发布 B", async () => {
    activeApi.getActiveWorkspace.mockResolvedValueOnce(workspaceA).mockResolvedValueOnce(workspaceB);
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: Infinity } } });
    const oldKey = ["business", workspaceA.id, "draft"];
    const nextKey = ["business", workspaceB.id, "draft"];
    queryClient.setQueryData(oldKey, "alpha");
    queryClient.setQueryData(nextKey, "beta");
    const cancel = vi.spyOn(queryClient, "cancelQueries");
    const remove = vi.spyOn(queryClient, "removeQueries");
    renderBoundary(queryClient);
    await waitFor(() => expect(getActiveWorkspaceId()).toBe(workspaceA.id));

    fireEvent.click(screen.getByRole("button", { name: "刷新 Active Workspace" }));

    await waitFor(() => expect(screen.getByTestId("workspace-state")).toHaveTextContent(`ready:${workspaceB.id}`));
    expect(queryClient.getQueryData(oldKey)).toBeUndefined();
    expect(queryClient.getQueryData(nextKey)).toBe("beta");
    expect(cancel).toHaveBeenCalled();
    expect(remove).toHaveBeenCalled();
    expect(cancel.mock.invocationCallOrder.at(-1)).toBeLessThan(remove.mock.invocationCallOrder.at(-1) ?? Infinity);
  });

  it("Active API 断线时撤销旧 Workspace，且不回退浏览器缓存", async () => {
    activeApi.getActiveWorkspace
      .mockResolvedValueOnce(workspaceA)
      .mockRejectedValueOnce(new Error("offline"))
      .mockResolvedValueOnce(workspaceA);
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    queryClient.setQueryData(["search", workspaceA.id, "page"], { items: ["secret"] });
    renderBoundary(queryClient);
    await waitFor(() => expect(getActiveWorkspaceId()).toBe(workspaceA.id));

    fireEvent.click(screen.getByRole("button", { name: "刷新 Active Workspace" }));

    expect(await screen.findByRole("heading", { name: "无法确认当前 Workspace" })).toBeInTheDocument();
    expect(getActiveWorkspaceId()).toBe("");
    expect(queryClient.getQueryData(["search", workspaceA.id, "page"])).toBeUndefined();

    fireEvent.click(screen.getByRole("button", { name: "重新读取" }));
    await waitFor(() => expect(screen.getByTestId("workspace-state")).toHaveTextContent(`ready:${workspaceA.id}`));
    expect(getActiveWorkspaceId()).toBe(workspaceA.id);
  });

  it("refetch 失败且 Query 保留旧 data 时先阻断旧业务树", async () => {
    activeApi.getActiveWorkspace
      .mockResolvedValueOnce(workspaceA)
      .mockImplementationOnce(() => {
        activeRefetchFailed = true;
        return Promise.reject(new Error("offline"));
      });
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    renderBoundary(queryClient);
    await waitFor(() => expect(getActiveWorkspaceId()).toBe(workspaceA.id));

    fireEvent.click(screen.getByRole("button", { name: "刷新 Active Workspace" }));

    expect(await screen.findByRole("heading", { name: "无法确认当前 Workspace" })).toBeInTheDocument();
    expect(queryClient.getQueryData(["active-workspace"])).toEqual(workspaceA);
    expect(getActiveWorkspaceId()).toBe("");
    expect(screen.queryByTestId("workspace-state")).not.toBeInTheDocument();
    expect(staleWorkspaceRequest).not.toHaveBeenCalled();
  });

  it("当前 Workspace 变为不可用时卸载业务作用域并清除旧缓存", async () => {
    activeApi.getActiveWorkspace
      .mockResolvedValueOnce(workspaceA)
      .mockResolvedValueOnce({ ...workspaceA, availability: "unavailable", version: 2 });
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    queryClient.setQueryData(["business", workspaceA.id, "proposal"], { secret: "alpha" });
    renderBoundary(queryClient);
    await waitFor(() => expect(getActiveWorkspaceId()).toBe(workspaceA.id));

    fireEvent.click(screen.getByRole("button", { name: "刷新 Active Workspace" }));

    await waitFor(() => expect(screen.getByTestId("workspace-state")).toHaveTextContent(`unavailable:${workspaceA.id}`));
    expect(getActiveWorkspaceId()).toBe("");
    expect(queryClient.getQueryData(["business", workspaceA.id, "proposal"])).toBeUndefined();
  });

  it("清理旧 Workspace 查询失败时保持业务树卸载并显示错误", async () => {
    activeApi.getActiveWorkspace.mockResolvedValueOnce(workspaceA).mockResolvedValueOnce(workspaceB);
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    renderBoundary(queryClient);
    await waitFor(() => expect(getActiveWorkspaceId()).toBe(workspaceA.id));
    vi.spyOn(queryClient, "cancelQueries").mockRejectedValueOnce(new Error("cancel failed"));

    fireEvent.click(screen.getByRole("button", { name: "刷新 Active Workspace" }));

    expect(await screen.findByRole("heading", { name: "无法确认当前 Workspace" })).toBeInTheDocument();
    expect(screen.getByText("cancel failed")).toBeInTheDocument();
    expect(getActiveWorkspaceId()).toBe("");
    expect(screen.queryByTestId("workspace-state")).not.toBeInTheDocument();
  });
});
