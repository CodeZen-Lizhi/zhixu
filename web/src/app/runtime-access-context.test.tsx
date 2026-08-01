import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const runtimeMocks = vi.hoisted(() => ({ getRuntimeAccess: vi.fn() }));
vi.mock("../api/runtime-access", () => runtimeMocks);

import { setActiveWorkspaceId, useActiveWorkspaceId } from "./active-workspace";
import { notifyRuntimeAccessInvalidation, suspendRuntimeAccess } from "../shared/runtime-access-invalidation";
import { RuntimeAccessBoundary, RuntimeAccessProvider } from "./runtime-access-context";

const workspaceA = "91000000-0000-4000-8000-000000000001";
const workspaceB = "91000000-0000-4000-8000-000000000002";
const deferred = <T,>() => {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => { resolve = done; });
  return { promise, resolve };
};
const Probe = () => <output data-testid="workspace">{useActiveWorkspaceId() || "none"}</output>;
const RuntimeProbe = () => <RuntimeAccessBoundary><output data-testid="business">business</output></RuntimeAccessBoundary>;

beforeEach(() => {
  window.localStorage.clear();
  setActiveWorkspaceId("");
  vi.clearAllMocks();
});
afterEach(() => window.localStorage.clear());

describe("RuntimeAccessProvider", () => {
  it("控制命令可等待旧 Workspace 缓存清理完成，并保持暂停直到显式重查", async () => {
    runtimeMocks.getRuntimeAccess.mockResolvedValue({ status: "ready", activeWorkspaceId: workspaceA, pollAfterMs: 5000 });
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    client.setQueryData(["business", workspaceA, "detail"], { workspaceId: workspaceA });
    render(<QueryClientProvider client={client}><RuntimeAccessProvider><Probe /><RuntimeProbe /></RuntimeAccessProvider></QueryClientProvider>);

    await waitFor(() => expect(screen.getByTestId("workspace")).toHaveTextContent(workspaceA));
    await suspendRuntimeAccess();

    expect(screen.getByTestId("workspace")).toHaveTextContent("none");
    expect(screen.queryByTestId("business")).not.toBeInTheDocument();
    expect(client.getQueryData(["business", workspaceA, "detail"])).toBeUndefined();
    expect(runtimeMocks.getRuntimeAccess).toHaveBeenCalledTimes(1);
  });

  it("runtime 撤销会先清理旧 Workspace 并 fail closed，再发布新的权威 ID", async () => {
    const next = deferred<{ status: "ready"; activeWorkspaceId: string; pollAfterMs: number }>();
    runtimeMocks.getRuntimeAccess.mockResolvedValueOnce({ status: "ready", activeWorkspaceId: workspaceA, pollAfterMs: 5000 })
      .mockReturnValueOnce(next.promise);
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    client.setQueryData(["business", workspaceA, "detail"], { workspaceId: workspaceA });
    render(<QueryClientProvider client={client}><RuntimeAccessProvider><Probe /><RuntimeProbe /></RuntimeAccessProvider></QueryClientProvider>);

    await waitFor(() => expect(screen.getByTestId("workspace")).toHaveTextContent(workspaceA));
    expect(screen.getByTestId("business")).toBeInTheDocument();
    notifyRuntimeAccessInvalidation();
    await waitFor(() => expect(screen.getByTestId("workspace")).toHaveTextContent("none"));
    expect(screen.queryByTestId("business")).not.toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "暂时无法进入" })).toBeInTheDocument();
    await waitFor(() => expect(client.getQueryData(["business", workspaceA, "detail"])).toBeUndefined());

    next.resolve({ status: "ready", activeWorkspaceId: workspaceB, pollAfterMs: 5000 });
    await waitFor(() => expect(screen.getByTestId("workspace")).toHaveTextContent(workspaceB));
    expect(screen.getByTestId("business")).toBeInTheDocument();
  });

  it("Abort 后的旧响应不能覆盖更新的 runtime epoch", async () => {
    const stale = deferred<{ status: "ready"; activeWorkspaceId: string; pollAfterMs: number }>();
    const current = deferred<{ status: "ready"; activeWorkspaceId: string; pollAfterMs: number }>();
    runtimeMocks.getRuntimeAccess.mockReturnValueOnce(stale.promise).mockReturnValueOnce(current.promise);
    render(<QueryClientProvider client={new QueryClient()}><RuntimeAccessProvider><Probe /></RuntimeAccessProvider></QueryClientProvider>);

    await waitFor(() => expect(runtimeMocks.getRuntimeAccess).toHaveBeenCalledTimes(1));
    const staleSignal = runtimeMocks.getRuntimeAccess.mock.calls[0]?.[0] as AbortSignal | undefined;
    notifyRuntimeAccessInvalidation();
    await waitFor(() => expect(runtimeMocks.getRuntimeAccess).toHaveBeenCalledTimes(2));
    expect(staleSignal?.aborted).toBe(true);

    stale.resolve({ status: "ready", activeWorkspaceId: workspaceA, pollAfterMs: 5000 });
    await new Promise((resolve) => window.setTimeout(resolve, 0));
    expect(screen.getByTestId("workspace")).toHaveTextContent("none");

    current.resolve({ status: "ready", activeWorkspaceId: workspaceB, pollAfterMs: 5000 });
    await waitFor(() => expect(screen.getByTestId("workspace")).toHaveTextContent(workspaceB));
  });
});
