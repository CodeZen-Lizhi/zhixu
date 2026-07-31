import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, render, screen, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const apiMocks = vi.hoisted(() => ({
  exchangeControllerSession: vi.fn(),
  getControllerSession: vi.fn(),
  getControllerState: vi.fn(),
  startControllerWorkspaceSwitch: vi.fn(),
  checkControllerWorkspaceAvailability: vi.fn(),
  removeControllerWorkspace: vi.fn(),
  clearControllerCsrfToken: vi.fn(),
}));

vi.mock("../api/controller", async (importOriginal) => {
  // eslint-disable-next-line @typescript-eslint/consistent-type-imports
  const actual = await importOriginal<typeof import("../api/controller")>();
  return { ...actual, ...apiMocks };
});
vi.mock("./auth-context", () => ({
  AuthProvider: ({ children }: { children: ReactNode }) => <div data-testid="business-auth">{children}</div>,
  AuthBoundary: ({ children }: { children: ReactNode }) => <>{children}</>,
}));
vi.mock("../events/event-store", () => ({
  EventStoreProvider: ({ children }: { children: ReactNode }) => <div data-testid="business-sse">{children}</div>,
}));
vi.mock("./WorkspaceCacheBoundary", () => ({ WorkspaceCacheBoundary: ({ children }: { children: ReactNode }) => <>{children}</> }));
vi.mock("../routes/AppRoutes", () => ({ AppRoutes: () => <div>business-routes</div> }));
vi.mock("../features/workspace/ControllerWorkspacePage", () => ({ ControllerWorkspacePage: () => <div>controller-workspace</div> }));

import type { ControllerOperation, ControllerSession, ControllerState } from "../api/controller";
import { ControllerApp } from "./App";

const workspaceId = "74000000-0000-4000-8000-000000000001";
const session: ControllerSession = {
  controllerInstanceId: "c".repeat(43),
  sessionId: "s".repeat(43),
  csrfToken: "t".repeat(43),
  expiresAt: "2026-08-01T00:00:00Z",
};
const activeWorkspace = { workspaceId, name: "Alpha", rootPath: "/Users/test/alpha", availability: "available" as const };
const operation = (result?: ControllerOperation["result"]): ControllerOperation => ({
  operationId: "operation-runtime-boundary",
  phase: result === "rolled_back" ? "rolling_back" : result === "failed" ? "recovering" : "quiescing",
  targetWorkspaceId: "74000000-0000-4000-8000-000000000002",
  retryable: true,
  startedAt: "2026-07-31T10:00:00Z",
  updatedAt: "2026-07-31T10:00:01Z",
  ...(result === undefined ? {} : { result }),
});
const readyState = (): ControllerState => ({
  controllerInstanceId: session.controllerInstanceId,
  stateVersion: 1,
  runtime: { status: "ready", api: { status: "ready" }, worker: { status: "ready" } },
  activeWorkspace,
  recentWorkspaces: [activeWorkspace],
  operation: null,
  pollAfterMs: 1,
});
const waitingState = (): ControllerState => ({
  controllerInstanceId: session.controllerInstanceId,
  stateVersion: 1,
  runtime: { status: "waiting_for_workspace", api: { status: "stopped" }, worker: { status: "stopped" } },
  activeWorkspace: null,
  recentWorkspaces: [],
  operation: null,
  pollAfterMs: 60_000,
});
const deferred = <T,>() => {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => { resolve = done; });
  return { promise, resolve };
};

const renderControllerApp = () => render(
  <QueryClientProvider client={new QueryClient()}>
    <ControllerApp />
  </QueryClientProvider>,
);

beforeEach(() => {
  vi.clearAllMocks();
  window.localStorage.clear();
  window.history.replaceState(null, "", "/");
  apiMocks.getControllerSession.mockResolvedValue(session);
});

afterEach(() => window.localStorage.clear());

describe("ControllerApp runtime boundary", () => {
  it("waiting 状态只挂载控制页面，不创建业务 Auth 或 SSE 子树", async () => {
    apiMocks.getControllerState.mockResolvedValue(waitingState());
    renderControllerApp();

    expect(await screen.findByText("controller-workspace")).toBeInTheDocument();
    expect(screen.queryByTestId("business-auth")).not.toBeInTheDocument();
    expect(screen.queryByTestId("business-sse")).not.toBeInTheDocument();
  });

  it("active ready 时控制路径仍位于业务 Auth 与 SSE 子树之外", async () => {
    apiMocks.getControllerState.mockResolvedValue({ ...readyState(), pollAfterMs: 60_000 });
    window.history.replaceState(null, "", "/workspace");
    renderControllerApp();

    expect(await screen.findByText("controller-workspace")).toBeInTheDocument();
    expect(screen.queryByTestId("business-auth")).not.toBeInTheDocument();
    expect(screen.queryByTestId("business-sse")).not.toBeInTheDocument();
  });

  it("切换时卸载业务树，回滚 ready 后重挂旧树，恢复失败后保持关闭", async () => {
    const switching = deferred<ControllerState>();
    const rolledBack = deferred<ControllerState>();
    const recoveryFailed = deferred<ControllerState>();
    apiMocks.getControllerState
      .mockResolvedValueOnce(readyState())
      .mockReturnValueOnce(switching.promise)
      .mockReturnValueOnce(rolledBack.promise)
      .mockReturnValueOnce(recoveryFailed.promise);
    window.history.replaceState(null, "", "/artifacts/old-artifact");
    renderControllerApp();
    expect(await screen.findByText("business-routes")).toBeInTheDocument();
    expect(window.location.pathname).toBe("/artifacts/old-artifact");
    expect(screen.getByTestId("business-auth")).toBeInTheDocument();
    expect(screen.getByTestId("business-sse")).toBeInTheDocument();

    await waitFor(() => expect(apiMocks.getControllerState).toHaveBeenCalledTimes(2));
    switching.resolve({
      ...readyState(),
      stateVersion: 2,
      runtime: { status: "switching", api: { status: "stopped" }, worker: { status: "stopped" } },
      operation: operation(),
    });
    expect(await screen.findByText("controller-workspace")).toBeInTheDocument();
    await waitFor(() => expect(window.location.pathname).toBe("/workspace"));
    expect(screen.queryByTestId("business-auth")).not.toBeInTheDocument();
    expect(screen.queryByTestId("business-sse")).not.toBeInTheDocument();

    await waitFor(() => expect(apiMocks.getControllerState).toHaveBeenCalledTimes(3));
    rolledBack.resolve({ ...readyState(), stateVersion: 3, operation: operation("rolled_back") });
    expect(await screen.findByText("controller-workspace")).toBeInTheDocument();
    expect(screen.queryByText("business-routes")).not.toBeInTheDocument();
    act(() => {
      window.history.pushState(null, "", "/dashboard");
      window.dispatchEvent(new PopStateEvent("popstate"));
    });
    expect(await screen.findByText("business-routes")).toBeInTheDocument();

    await waitFor(() => expect(apiMocks.getControllerState).toHaveBeenCalledTimes(4));
    recoveryFailed.resolve({
      ...waitingState(),
      stateVersion: 4,
      runtime: { status: "recovery_failed", api: { status: "unavailable" }, worker: { status: "unavailable" } },
      operation: operation("failed"),
    });
    expect(await screen.findByText("controller-workspace")).toBeInTheDocument();
    expect(screen.queryByText("business-routes")).not.toBeInTheDocument();
  });
});
