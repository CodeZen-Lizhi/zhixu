import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes, useNavigate } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const workspaceId = "10000000-0000-4000-8000-000000000002";
const workflowId = "10000000-0000-4000-8000-000000000008";
const api = vi.hoisted(() => ({ controlWorkflow: vi.fn(), getWorkflow: vi.fn(), listWorkflows: vi.fn() }));
const workspaceState = vi.hoisted(() => ({ id: "10000000-0000-4000-8000-000000000002" }));

vi.mock("../../api/business", async (importOriginal) => ({
  // eslint-disable-next-line @typescript-eslint/consistent-type-imports
  ...(await importOriginal<typeof import("../../api/business")>()),
  controlWorkflow: api.controlWorkflow,
  getWorkflow: api.getWorkflow,
  listWorkflows: api.listWorkflows,
}));
vi.mock("../../app/active-workspace", async (importOriginal) => ({
  // eslint-disable-next-line @typescript-eslint/consistent-type-imports
  ...(await importOriginal<typeof import("../../app/active-workspace")>()),
  getActiveWorkspaceId: () => workspaceState.id,
  useActiveWorkspaceId: () => workspaceState.id,
}));

import { WorkflowDetailPage, WorkflowsPage } from "./WorkflowsPage";

const HistoryBackButton = () => {
  const navigate = useNavigate();
  return <button type="button" onClick={() => void navigate(-1)}>浏览器返回</button>;
};

const workflow = (status: "running" | "paused" | "succeeded") => ({
  id: workflowId,
  workspaceId,
  definitionId: "10000000-0000-4000-8000-000000000003",
  status,
  input: {},
  version: 2,
  createdAt: "2026-07-22T00:00:00Z",
  updatedAt: "2026-07-22T00:15:00Z",
  pauseRequested: false,
  cancelRequested: false,
  ...(status === "succeeded" ? { completedAt: "2026-07-22T00:15:00Z" } : {}),
});

const renderDetail = () => render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } })}><MemoryRouter initialEntries={[`/workflows/${workflowId}`]}><Routes><Route path="/workflows/:workflowId" element={<WorkflowDetailPage />} /></Routes></MemoryRouter></QueryClientProvider>);
const renderList = () => render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}><MemoryRouter initialEntries={["/workflows"]}><WorkflowsPage /><HistoryBackButton /></MemoryRouter></QueryClientProvider>);

beforeEach(() => {
  workspaceState.id = workspaceId;
  api.controlWorkflow.mockResolvedValue({ workflowRunId: workflowId, status: "running", version: 3, statusUrl: `/api/v1/workflows/${workflowId}`, pauseRequested: true, cancelRequested: false });
  api.getWorkflow.mockResolvedValue(workflow("running"));
  api.listWorkflows.mockResolvedValue({ items: [] });
});

afterEach(() => {
  cleanup();
  api.controlWorkflow.mockReset();
  api.getWorkflow.mockReset();
  api.listWorkflows.mockReset();
});

describe("WorkflowDetailPage controls", () => {
  it("only renders commands allowed by the current server state", async () => {
    renderDetail();

    expect(await screen.findByRole("button", { name: "暂停" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "取消" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "恢复" })).not.toBeInTheDocument();
  });

  it("renders no fake controls after the Run reaches a terminal state", async () => {
    api.getWorkflow.mockResolvedValue(workflow("succeeded"));
    renderDetail();

    expect(await screen.findByText("当前 Run 不允许控制")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /暂停|恢复|取消/ })).not.toBeInTheDocument();
    expect(screen.getByText("15 分钟")).toBeInTheDocument();
  });

  it("服务端 pending 控制请求在刷新后仍禁用所有命令", async () => {
    api.getWorkflow.mockResolvedValue({ ...workflow("running"), pauseRequested: true });
    renderDetail();

    expect(await screen.findByText("正在等待安全检查点")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "暂停" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "取消" })).toBeDisabled();
  });

  it("控制成功后等待权威详情回查完成才重新启用命令", async () => {
    let resolveRefetch: ((value: ReturnType<typeof workflow>) => void) | undefined;
    const refetchPending = new Promise<ReturnType<typeof workflow>>((resolve) => { resolveRefetch = resolve; });
    api.getWorkflow
      .mockReset()
      .mockResolvedValueOnce(workflow("running"))
      .mockImplementationOnce(() => refetchPending);
    renderDetail();

    const pause = await screen.findByRole("button", { name: "暂停" });
    fireEvent.click(pause);
    await waitFor(() => expect(api.controlWorkflow).toHaveBeenCalledWith(workflowId, "pause", 2));
    await waitFor(() => expect(api.getWorkflow).toHaveBeenCalledTimes(2));
    expect(screen.getByText("running")).toBeInTheDocument();
    expect(screen.queryByText("paused")).not.toBeInTheDocument();
    expect(pause).toBeDisabled();
    expect(screen.getByRole("button", { name: "取消" })).toBeDisabled();

    resolveRefetch?.(workflow("paused"));
    expect(await screen.findByRole("button", { name: "恢复" })).toBeEnabled();
  });

  it("没有活动 Workspace 时显示恢复入口且不读取详情", () => {
    workspaceState.id = "";
    renderDetail();

    expect(screen.getByText("先连接 Workspace")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "连接或切换 Workspace" })).toHaveAttribute("href", "/workspace");
    expect(api.getWorkflow).not.toHaveBeenCalled();
  });
});

describe("WorkflowsPage list", () => {
  it("同时展示创建时间、更新时间和等待人工状态", async () => {
    api.listWorkflows.mockResolvedValue({
      items: [{
        id: workflowId,
        workspaceId,
        definitionKey: "safe-writeback",
        definitionVersion: 1,
        status: "waiting_for_human",
        version: 2,
        createdAt: "2026-07-22T00:00:00Z",
        updatedAt: "2026-07-22T00:15:00Z",
        waitingForHuman: true,
        pauseRequested: false,
        cancelRequested: false,
      }],
    });
    renderList();

    expect(await screen.findByText(/创建/)).toBeInTheDocument();
    expect(screen.getByText(/更新/)).toBeInTheDocument();
    expect(screen.getByText("等待用户输入")).toBeInTheDocument();
  });

  it("浏览器历史恢复旧筛选时从第一页请求，不复活当前筛选的游标", async () => {
    api.listWorkflows.mockResolvedValue({ items: [], nextCursor: "workflow-page-2" });
    renderList();
    await waitFor(() => expect(api.listWorkflows).toHaveBeenLastCalledWith(
      workspaceId,
      { limit: 30 },
      expect.any(AbortSignal),
    ));

    fireEvent.change(screen.getByRole("combobox", { name: "运行状态" }), { target: { value: "running" } });
    await waitFor(() => expect(api.listWorkflows).toHaveBeenLastCalledWith(
      workspaceId,
      { limit: 30, status: "running" },
      expect.any(AbortSignal),
    ));
    fireEvent.click(screen.getByRole("button", { name: "下一页" }));
    await waitFor(() => expect(api.listWorkflows).toHaveBeenLastCalledWith(
      workspaceId,
      { limit: 30, status: "running", cursor: "workflow-page-2" },
      expect.any(AbortSignal),
    ));

    fireEvent.click(screen.getByRole("button", { name: "浏览器返回" }));

    await waitFor(() => expect(api.listWorkflows).toHaveBeenLastCalledWith(
      workspaceId,
      { limit: 30 },
      expect.any(AbortSignal),
    ));
  });
});
