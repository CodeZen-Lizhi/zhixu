import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { StrictMode, type ReactNode } from "react";
import { MemoryRouter, useNavigate } from "react-router-dom";
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
const runtimeBoundaryMocks = vi.hoisted(() => ({
  notifyRuntimeAccessInvalidation: vi.fn(),
  suspendRuntimeAccess: vi.fn(),
}));

vi.mock("../api/controller", async (importOriginal) => {
  // eslint-disable-next-line @typescript-eslint/consistent-type-imports
  const actual = await importOriginal<typeof import("../api/controller")>();
  return { ...actual, ...apiMocks };
});
vi.mock("../shared/runtime-access-invalidation", () => runtimeBoundaryMocks);

import { ControllerApiError, type ControllerOperation, type ControllerSession, type ControllerState } from "../api/controller";
import { getCsrfToken, setCsrfToken } from "../api/auth";
import { HostControlProvider, useHostControl } from "./host-control-context";

const workspaceA = "72000000-0000-4000-8000-000000000001";
const workspaceB = "72000000-0000-4000-8000-000000000002";
const session: ControllerSession = {
  controllerInstanceId: "c".repeat(43),
  sessionId: "s".repeat(43),
  csrfToken: "t".repeat(43),
  expiresAt: "2026-08-01T00:00:00Z",
};
const workspace = (workspaceId: string, name: string) => ({
  workspaceId,
  name,
  rootPath: `/Users/test/${name}`,
  availability: "available" as const,
  lastOpenedAt: "2026-07-31T10:00:00Z",
});
const readyState = (activeWorkspaceId = workspaceA): ControllerState => ({
  controllerInstanceId: session.controllerInstanceId,
  stateVersion: 7,
  runtime: { status: "ready", api: { status: "ready" }, worker: { status: "ready" } },
  activeWorkspace: workspace(activeWorkspaceId, activeWorkspaceId === workspaceA ? "Alpha" : "Beta"),
  recentWorkspaces: [workspace(workspaceA, "Alpha"), workspace(workspaceB, "Beta")],
  operation: null,
  pollAfterMs: 60_000,
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
const switchingOperation: ControllerOperation = {
  operationId: "operation-switch",
  phase: "quiescing",
  targetWorkspaceId: workspaceB,
  retryable: true,
  startedAt: "2026-07-31T10:00:00Z",
  updatedAt: "2026-07-31T10:00:01Z",
};

const Probe = () => {
  const control = useHostControl();
  const navigate = useNavigate();
  return <>
    <output data-testid="status">{control.state.status}</output>
    <output data-testid="runtime-status">{control.state.status === "ready" ? control.state.controlState.runtime.status : "none"}</output>
    <output data-testid="pending">{control.actionPending ? "pending" : "idle"}</output>
    <output data-testid="action-error">{control.actionError?.message ?? "none"}</output>
    <button type="button" onClick={() => void control.switchToRegisteredWorkspace(workspaceB)}>switch</button>
    <button type="button" onClick={() => void control.refresh()}>refresh</button>
    <button type="button" onClick={() => void navigate("/dashboard")}>leave</button>
    <button type="button" onClick={() => void navigate("/workspace")}>enter</button>
  </>;
};

const deferred = <T,>() => {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => { resolve = done; });
  return { promise, resolve };
};

const Providers = ({ children, token, client = new QueryClient() }: { children: ReactNode; token?: string; client?: QueryClient }) => (
  <QueryClientProvider client={client}>
    <MemoryRouter><HostControlProvider initialControllerToken={token}>{children}</HostControlProvider></MemoryRouter>
  </QueryClientProvider>
);

beforeEach(() => {
  vi.clearAllMocks();
  window.localStorage.clear();
  apiMocks.getControllerSession.mockResolvedValue(session);
  apiMocks.exchangeControllerSession.mockResolvedValue(session);
  apiMocks.getControllerState.mockResolvedValue(waitingState());
  runtimeBoundaryMocks.suspendRuntimeAccess.mockResolvedValue(undefined);
});

afterEach(() => {
  window.localStorage.clear();
});

describe("HostControlProvider", () => {
  it("StrictMode 下只交换一次 fragment Token", async () => {
    render(<StrictMode><Providers token={"b".repeat(43)}><Probe /></Providers></StrictMode>);

    await waitFor(() => expect(screen.getByTestId("status")).toHaveTextContent("ready"));
    expect(apiMocks.exchangeControllerSession).toHaveBeenCalledTimes(1);
    expect(apiMocks.getControllerSession).not.toHaveBeenCalled();
    expect(screen.getByTestId("status")).toHaveTextContent("ready");
  });

  it("用户发起切换时等待独立 runtime Owner 清理完旧作用域再提交命令", async () => {
    const client = new QueryClient();
    const suspended = deferred<undefined>();
    runtimeBoundaryMocks.suspendRuntimeAccess.mockReturnValueOnce(suspended.promise);
    apiMocks.getControllerState
      .mockResolvedValueOnce(readyState())
      .mockResolvedValueOnce({
        ...readyState(),
        stateVersion: 8,
        runtime: { status: "switching", api: { status: "stopped" }, worker: { status: "stopped" } },
        operation: switchingOperation,
      });
    apiMocks.startControllerWorkspaceSwitch.mockResolvedValue(switchingOperation);
    render(<Providers client={client}><Probe /></Providers>);
    await waitFor(() => expect(screen.getByTestId("status")).toHaveTextContent("ready"));

    fireEvent.click(screen.getByRole("button", { name: "switch" }));

    await waitFor(() => expect(runtimeBoundaryMocks.suspendRuntimeAccess).toHaveBeenCalledOnce());
    expect(apiMocks.startControllerWorkspaceSwitch).not.toHaveBeenCalled();
    suspended.resolve(undefined);
    await waitFor(() => expect(apiMocks.startControllerWorkspaceSwitch).toHaveBeenCalledOnce());

    const switchCall = apiMocks.startControllerWorkspaceSwitch.mock.calls[0] as unknown as [
      { targetKind: "registered"; workspaceId: string },
      { stateVersion: number; idempotencyKey: string },
    ];
    expect(switchCall[0]).toEqual({ targetKind: "registered", workspaceId: workspaceB });
    expect(switchCall[1].stateVersion).toBe(7);
    expect(switchCall[1].idempotencyKey).not.toBe("");
    expect(runtimeBoundaryMocks.notifyRuntimeAccessInvalidation).toHaveBeenCalledOnce();
  });

  it("控制命令在离页后返回时不重新缓存完整控制投影", async () => {
    const command = deferred<ControllerOperation>();
    apiMocks.getControllerState.mockResolvedValueOnce(readyState());
    apiMocks.startControllerWorkspaceSwitch.mockReturnValueOnce(command.promise);
    render(<Providers><Probe /></Providers>);
    await waitFor(() => expect(screen.getByTestId("status")).toHaveTextContent("ready"));

    fireEvent.click(screen.getByRole("button", { name: "switch" }));
    await waitFor(() => expect(apiMocks.startControllerWorkspaceSwitch).toHaveBeenCalledOnce());
    fireEvent.click(screen.getByRole("button", { name: "leave" }));
    await waitFor(() => expect(screen.getByTestId("status")).toHaveTextContent("loading"));
    command.resolve(switchingOperation);

    await waitFor(() => expect(screen.getByTestId("pending")).toHaveTextContent("idle"));
    expect(apiMocks.getControllerState).toHaveBeenCalledOnce();

    fireEvent.click(screen.getByRole("button", { name: "enter" }));
    await waitFor(() => expect(apiMocks.getControllerState).toHaveBeenCalledTimes(2));
  });

  it("Controller 401 只关闭控制会话且不清理业务 CSRF", async () => {
    setCsrfToken("business-csrf");
    apiMocks.getControllerState.mockRejectedValue(new ControllerApiError(
      "HTTP_ERROR",
      "CONTROL_SESSION_REQUIRED",
      "需要有效的控制会话",
      false,
      401,
    ));

    render(<Providers><Probe /></Providers>);

    await waitFor(() => expect(screen.getByTestId("status")).toHaveTextContent("session_required"));
    expect(apiMocks.clearControllerCsrfToken).toHaveBeenCalledTimes(1);
    expect(getCsrfToken()).toBe("business-csrf");
  });

  it("响应丢失后重试同一意图时复用 Idempotency-Key", async () => {
    apiMocks.getControllerState
      .mockResolvedValueOnce(readyState())
      .mockResolvedValueOnce(readyState())
      .mockResolvedValueOnce({
        ...readyState(),
        stateVersion: 8,
        runtime: { status: "switching", api: { status: "stopped" }, worker: { status: "stopped" } },
        operation: switchingOperation,
      });
    apiMocks.startControllerWorkspaceSwitch
      .mockRejectedValueOnce(new ControllerApiError("NETWORK_ERROR", "NETWORK_ERROR", "响应丢失", true))
      .mockResolvedValueOnce(switchingOperation);
    render(<Providers><Probe /></Providers>);
    await waitFor(() => expect(screen.getByTestId("status")).toHaveTextContent("ready"));

    fireEvent.click(screen.getByRole("button", { name: "switch" }));
    await waitFor(() => expect(screen.getByTestId("action-error")).toHaveTextContent("响应丢失"));
    await waitFor(() => expect(screen.getByTestId("status")).toHaveTextContent("ready"));
    expect(screen.getByTestId("pending")).toHaveTextContent("idle");
    fireEvent.click(screen.getByRole("button", { name: "switch" }));
    await waitFor(() => expect(apiMocks.startControllerWorkspaceSwitch).toHaveBeenCalledTimes(2));

    const first = apiMocks.startControllerWorkspaceSwitch.mock.calls[0] as unknown as [unknown, { idempotencyKey: string }];
    const second = apiMocks.startControllerWorkspaceSwitch.mock.calls[1] as unknown as [unknown, { idempotencyKey: string }];
    expect(second[1].idempotencyKey).toBe(first[1].idempotencyKey);
  });

  it("会话失效后忽略更早发出的 state 成功响应", async () => {
    const staleState = deferred<ControllerState>();
    apiMocks.getControllerState
      .mockResolvedValueOnce({ ...readyState(), pollAfterMs: 1 })
      .mockReturnValueOnce(staleState.promise)
      .mockRejectedValueOnce(new ControllerApiError(
        "HTTP_ERROR",
        "CONTROL_SESSION_REQUIRED",
        "需要有效的控制会话",
        false,
        401,
      ));
    render(<Providers><Probe /></Providers>);
    await waitFor(() => expect(screen.getByTestId("status")).toHaveTextContent("ready"));
    await waitFor(() => expect(apiMocks.getControllerState).toHaveBeenCalledTimes(2));
    const staleSignal = apiMocks.getControllerState.mock.calls[1]?.[0] as AbortSignal | undefined;
    expect(staleSignal).toBeDefined();
    expect(staleSignal?.aborted).toBe(false);

    fireEvent.click(screen.getByRole("button", { name: "refresh" }));
    await waitFor(() => expect(screen.getByTestId("status")).toHaveTextContent("session_required"));
    expect(staleSignal?.aborted).toBe(true);
    staleState.resolve({ ...readyState(), stateVersion: 9 });

    await new Promise((resolve) => window.setTimeout(resolve, 0));
    expect(screen.getByTestId("status")).toHaveTextContent("session_required");
  });

  it("同一版本的较早 state 响应晚到时不恢复旧运行时", async () => {
    const staleReadyState = deferred<ControllerState>();
    apiMocks.getControllerState
      .mockResolvedValueOnce({ ...readyState(), pollAfterMs: 1 })
      .mockReturnValueOnce(staleReadyState.promise)
      .mockResolvedValueOnce({
        ...readyState(),
        runtime: { status: "switching", api: { status: "stopped" }, worker: { status: "stopped" } },
        operation: switchingOperation,
      });
    render(<Providers><Probe /></Providers>);
    await waitFor(() => expect(screen.getByTestId("status")).toHaveTextContent("ready"));
    await waitFor(() => expect(apiMocks.getControllerState).toHaveBeenCalledTimes(2));

    fireEvent.click(screen.getByRole("button", { name: "refresh" }));
    await waitFor(() => expect(screen.getByTestId("runtime-status")).toHaveTextContent("switching"));
    staleReadyState.resolve(readyState());

    await new Promise((resolve) => window.setTimeout(resolve, 0));
    expect(screen.getByTestId("runtime-status")).toHaveTextContent("switching");
  });

  it("卸载 Provider 时中止仍在等待的 state 请求", async () => {
    const pendingState = deferred<ControllerState>();
    apiMocks.getControllerState.mockReturnValueOnce(pendingState.promise);
    const view = render(<Providers><Probe /></Providers>);
    await waitFor(() => expect(apiMocks.getControllerState).toHaveBeenCalledTimes(1));
    const signal = apiMocks.getControllerState.mock.calls[0]?.[0] as AbortSignal | undefined;
    expect(signal).toBeDefined();
    expect(signal?.aborted).toBe(false);

    view.unmount();

    expect(signal?.aborted).toBe(true);
    pendingState.resolve(waitingState());
  });
});
