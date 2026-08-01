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
const runtimeMocks = vi.hoisted(() => ({ getRuntimeAccess: vi.fn() }));

vi.mock("../api/controller", async (importOriginal) => {
  // eslint-disable-next-line @typescript-eslint/consistent-type-imports
  const actual = await importOriginal<typeof import("../api/controller")>();
  return { ...actual, ...apiMocks };
});
vi.mock("../api/runtime-access", () => runtimeMocks);
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

import { ControllerApiError, type ControllerSession, type ControllerState } from "../api/controller";
import { ControllerApp } from "./App";

const workspaceId = "74000000-0000-4000-8000-000000000001";
const session: ControllerSession = {
  controllerInstanceId: "c".repeat(43),
  sessionId: "s".repeat(43),
  csrfToken: "t".repeat(43),
  expiresAt: "2026-08-01T00:00:00Z",
};
const activeWorkspace = { workspaceId, name: "Alpha", rootPath: "/Users/test/alpha", availability: "available" as const };
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

const renderControllerApp = (initialControllerToken?: string) => render(
  <QueryClientProvider client={new QueryClient()}>
    <ControllerApp initialControllerToken={initialControllerToken} />
  </QueryClientProvider>,
);

const navigate = (path: string): void => {
  act(() => {
    window.history.pushState(null, "", path);
    window.dispatchEvent(new PopStateEvent("popstate"));
  });
};

beforeEach(() => {
  vi.clearAllMocks();
  window.localStorage.clear();
  window.history.replaceState(null, "", "/");
  apiMocks.getControllerSession.mockResolvedValue(session);
  apiMocks.exchangeControllerSession.mockResolvedValue(session);
  apiMocks.getControllerState.mockResolvedValue({ ...readyState(), pollAfterMs: 60_000 });
  runtimeMocks.getRuntimeAccess.mockResolvedValue({ status: "ready", activeWorkspaceId: workspaceId, pollAfterMs: 5000 });
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

  it.each(["/workspace", "/workspace/", "/WORKSPACE", "/work%73pace"])("控制路径变体 %s 均位于业务 Auth 与 SSE 子树之外", async (path) => {
    apiMocks.getControllerState.mockResolvedValue({ ...readyState(), pollAfterMs: 60_000 });
    window.history.replaceState(null, "", path);
    renderControllerApp();

    expect(await screen.findByText("controller-workspace")).toBeInTheDocument();
    expect(screen.queryByTestId("business-auth")).not.toBeInTheDocument();
    expect(screen.queryByTestId("business-sse")).not.toBeInTheDocument();
  });

  it("普通业务深链只读取公开 runtime，不探测控制会话", async () => {
    window.history.replaceState(null, "", "/artifacts/old-artifact");
    renderControllerApp();
    expect(await screen.findByText("business-routes")).toBeInTheDocument();
    expect(window.location.pathname).toBe("/artifacts/old-artifact");
    expect(screen.getByTestId("business-auth")).toBeInTheDocument();
    expect(screen.getByTestId("business-sse")).toBeInTheDocument();
    expect(apiMocks.getControllerSession).not.toHaveBeenCalled();
    expect(apiMocks.getControllerState).not.toHaveBeenCalled();
  });

  it.each([
    { status: "waiting" as const, heading: "尚未连接工作区" },
    { status: "unavailable" as const, heading: "暂时无法进入" },
  ])("公开 runtime 为 $status 时保留业务深链并阻止 Auth 与 SSE", async ({ status, heading }) => {
    runtimeMocks.getRuntimeAccess.mockResolvedValue({ status, activeWorkspaceId: null, pollAfterMs: 5000 });
    window.history.replaceState(null, "", "/artifacts/old-artifact");
    renderControllerApp();

    expect(await screen.findByRole("heading", { name: heading })).toBeInTheDocument();
    expect(window.location.pathname).toBe("/artifacts/old-artifact");
    expect(screen.queryByTestId("business-auth")).not.toBeInTheDocument();
    expect(screen.queryByTestId("business-sse")).not.toBeInTheDocument();
    expect(apiMocks.getControllerSession).not.toHaveBeenCalled();
    expect(apiMocks.getControllerState).not.toHaveBeenCalled();
  });

  it("Controller Session 失效只关闭控制页，业务 runtime 仍可进入", async () => {
    apiMocks.getControllerSession.mockRejectedValue(new ControllerApiError(
      "HTTP_ERROR",
      "CONTROL_SESSION_REQUIRED",
      "需要有效的控制会话",
      false,
      401,
    ));
    window.history.replaceState(null, "", "/workspace");
    renderControllerApp();

    expect(await screen.findByRole("heading", { name: "控制链接已失效" })).toBeInTheDocument();
    navigate("/dashboard");
    expect(await screen.findByText("business-routes")).toBeInTheDocument();
    expect(screen.getByTestId("business-auth")).toBeInTheDocument();
  });

  it("离开控制页后丢弃完整控制投影，返回时先重新验证 Cookie Session", async () => {
    window.history.replaceState(null, "", "/workspace");
    renderControllerApp();
    expect(await screen.findByText("controller-workspace")).toBeInTheDocument();

    navigate("/dashboard");
    expect(await screen.findByText("business-routes")).toBeInTheDocument();
    await waitFor(() => expect(screen.queryByText("controller-workspace")).not.toBeInTheDocument());
    apiMocks.getControllerState.mockRejectedValueOnce(new ControllerApiError(
      "HTTP_ERROR",
      "CONTROL_SESSION_REQUIRED",
      "需要有效的控制会话",
      false,
      401,
    ));

    navigate("/workspace");

    expect(screen.queryByText("controller-workspace")).not.toBeInTheDocument();
    expect(await screen.findByRole("heading", { name: "控制链接已失效" })).toBeInTheDocument();
    expect(apiMocks.getControllerState).toHaveBeenCalledTimes(2);
  });

  it("路由往返不会重放一次性 Controller Token", async () => {
    const token = "b".repeat(43);
    renderControllerApp(token);
    expect(await screen.findByText("controller-workspace")).toBeInTheDocument();

    navigate("/dashboard");
    expect(await screen.findByText("business-routes")).toBeInTheDocument();
    navigate("/workspace");
    expect(await screen.findByText("controller-workspace")).toBeInTheDocument();

    expect(apiMocks.exchangeControllerSession).toHaveBeenCalledTimes(1);
    expect(apiMocks.exchangeControllerSession).toHaveBeenCalledWith(token);
    expect(apiMocks.getControllerSession).not.toHaveBeenCalled();
  });
});
