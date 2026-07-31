import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { MemoryRouter } from "react-router-dom";

const apiMocks = vi.hoisted(() => ({
  exchangeControllerSession: vi.fn(),
  getControllerSession: vi.fn(),
  getControllerState: vi.fn(),
  startControllerWorkspaceSwitch: vi.fn(),
  checkControllerWorkspaceAvailability: vi.fn(),
  removeControllerWorkspace: vi.fn(),
  clearControllerCsrfToken: vi.fn(),
}));

vi.mock("../../api/controller", async (importOriginal) => {
  // eslint-disable-next-line @typescript-eslint/consistent-type-imports
  const actual = await importOriginal<typeof import("../../api/controller")>();
  return { ...actual, ...apiMocks };
});

import type { ControllerOperation, ControllerSession, ControllerState } from "../../api/controller";
import { HostControlProvider } from "../../app/host-control-context";
import { ControllerWorkspacePage } from "./ControllerWorkspacePage";

const activeId = "73000000-0000-4000-8000-000000000001";
const availableId = "73000000-0000-4000-8000-000000000002";
const unavailableId = "73000000-0000-4000-8000-000000000003";
const migrationId = "73000000-0000-4000-8000-000000000004";
const longHostPath = `/Users/test/${"deep-workspace-segment/".repeat(12)}knowledge-base`;
const session: ControllerSession = {
  controllerInstanceId: "c".repeat(43),
  sessionId: "s".repeat(43),
  csrfToken: "t".repeat(43),
  expiresAt: "2026-08-01T00:00:00Z",
};
const operation: ControllerOperation = {
  operationId: "operation-controller-page",
  phase: "validating",
  retryable: true,
  startedAt: "2026-07-31T10:00:00Z",
  updatedAt: "2026-07-31T10:00:01Z",
};
const state: ControllerState = {
  controllerInstanceId: session.controllerInstanceId,
  stateVersion: 11,
  runtime: { status: "ready", api: { status: "ready" }, worker: { status: "ready" } },
  activeWorkspace: { workspaceId: activeId, name: "Active", rootPath: longHostPath, availability: "available" },
  recentWorkspaces: [
    { workspaceId: activeId, name: "Active", rootPath: longHostPath, availability: "available" },
    { workspaceId: availableId, name: "Available", rootPath: "/Users/test/available", availability: "available" },
    { workspaceId: unavailableId, name: "Missing", rootPath: "/Users/test/missing", availability: "unavailable", availabilityReason: "目录不存在" },
    { workspaceId: migrationId, name: "Legacy", rootPath: "/Users/test/legacy", availability: "migration_required", availabilityReason: "需要显式迁移" },
  ],
  operation: null,
  pollAfterMs: 60_000,
};

const renderPage = () => render(
  <QueryClientProvider client={new QueryClient()}>
    <MemoryRouter><HostControlProvider><ControllerWorkspacePage /></HostControlProvider></MemoryRouter>
  </QueryClientProvider>,
);

beforeEach(() => {
  vi.clearAllMocks();
  window.localStorage.clear();
  apiMocks.getControllerSession.mockResolvedValue(session);
  apiMocks.getControllerState.mockResolvedValue(state);
  apiMocks.startControllerWorkspaceSwitch.mockResolvedValue(operation);
  apiMocks.checkControllerWorkspaceAvailability.mockResolvedValue(state.recentWorkspaces[2]);
  apiMocks.removeControllerWorkspace.mockResolvedValue(undefined);
});

afterEach(() => window.localStorage.clear());

describe("ControllerWorkspacePage", () => {
  it("展示真实宿主机路径与显式 Git 选择，不暴露手工 UUID/容器映射", async () => {
    renderPage();

    expect(await screen.findByText("管理本机 Workspace。")).toBeInTheDocument();
    expect(screen.getByLabelText("Workspace 名称")).toBeInTheDocument();
    expect(screen.getByLabelText("宿主机目录")).toHaveAttribute("placeholder", "/Users/me/knowledge");
    expect(screen.getByLabelText(/明确初始化 Git 仓库/)).not.toBeChecked();
    const pathCopies = screen.getAllByText(longHostPath);
    expect(pathCopies.length).toBe(2);
    for (const path of pathCopies) {
      expect(path.tagName).toBe("CODE");
      expect(path).toHaveClass("control-host-path");
      expect(path).toHaveAttribute("title", longHostPath);
      expect(path).toHaveTextContent(longHostPath);
    }
    const gitOption = screen.getByLabelText(/明确初始化 Git 仓库/).closest("label");
    expect(gitOption).not.toBeNull();
    expect(gitOption?.querySelector(".control-git-option__label > svg")).not.toBeNull();
    expect(gitOption?.querySelector(".control-git-option__copy")).toHaveTextContent("如果目录尚未使用 Git，明确初始化 Git 仓库");
    expect(screen.queryByText(/已有 Workspace ID|UUID|容器映射/)).not.toBeInTheDocument();
  });

  it("新目录命令只发送名称、真实路径和显式 Git 选择", async () => {
    renderPage();
    await screen.findByText("管理本机 Workspace。");
    fireEvent.change(screen.getByLabelText("Workspace 名称"), { target: { value: "Research" } });
    fireEvent.change(screen.getByLabelText("宿主机目录"), { target: { value: "/Users/test/research" } });
    fireEvent.click(screen.getByLabelText(/明确初始化 Git 仓库/));
    fireEvent.click(screen.getByRole("button", { name: /授权并打开/ }));

    await waitFor(() => expect(apiMocks.startControllerWorkspaceSwitch).toHaveBeenCalled());
    const switchCall = apiMocks.startControllerWorkspaceSwitch.mock.calls[0] as unknown as [
      { targetKind: "new"; name: string; rootPath: string; initializeGit: boolean },
      { stateVersion: number; idempotencyKey: string },
    ];
    expect(switchCall[0]).toEqual({ targetKind: "new", name: "Research", rootPath: "/Users/test/research", initializeGit: true });
    expect(switchCall[1].stateVersion).toBe(11);
    expect(switchCall[1].idempotencyKey).not.toBe("");
  });

  it("Unavailable 记录支持重检与仅移除元数据的二次确认", async () => {
    renderPage();
    const row = (await screen.findByText("Missing")).closest("li");
    if (row === null) throw new Error("缺少 Missing registry row");

    fireEvent.click(within(row).getByRole("button", { name: /重新检查/ }));
    await waitFor(() => expect(apiMocks.checkControllerWorkspaceAvailability).toHaveBeenCalledWith(
      unavailableId,
      expect.objectContaining({ stateVersion: 11 }),
    ));
    await waitFor(() => expect(within(row).getByRole("button", { name: /移除记录/ })).toBeEnabled());
    fireEvent.click(within(row).getByRole("button", { name: /移除记录/ }));
    expect(within(row).getByText(/不会删除宿主机文件/)).toBeInTheDocument();
    fireEvent.click(within(row).getByRole("button", { name: /确认移除/ }));
    await waitFor(() => expect(apiMocks.removeControllerWorkspace).toHaveBeenCalledWith(
      unavailableId,
      expect.objectContaining({ stateVersion: 11 }),
    ));
  });

  it("把异步候选错误码解释为可执行的中文恢复提示", async () => {
    apiMocks.getControllerState.mockResolvedValue({
      ...state,
      runtime: { status: "recovery_failed", api: { status: "unavailable" }, worker: { status: "unavailable" } },
      activeWorkspace: null,
      operation: {
        ...operation,
        phase: "recovering",
        result: "failed",
        errorCode: "WORKSPACE_RUNTIME_ROOT_PERMISSION_DENIED",
      },
    });

    renderPage();

    expect(await screen.findByText(/请检查 Docker 文件共享设置和该目录的读写权限/)).toBeInTheDocument();
    expect(screen.getByText("WORKSPACE_RUNTIME_ROOT_PERMISSION_DENIED")).toBeInTheDocument();
  });
});
