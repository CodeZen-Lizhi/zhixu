import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const workspaceId = "10000000-0000-4000-8000-000000000002";
const workspaceState = vi.hoisted(() => ({ id: "10000000-0000-4000-8000-000000000002" }));
const api = vi.hoisted(() => ({
  getWorkspace: vi.fn(),
  listProposals: vi.fn(),
  listSourceVersions: vi.fn(),
  listWorkflows: vi.fn(),
}));
const systemStatus = vi.hoisted(() => ({ useSystemStatus: vi.fn() }));

vi.mock("../../app/active-workspace", async (importOriginal) => ({
  // eslint-disable-next-line @typescript-eslint/consistent-type-imports
  ...(await importOriginal<typeof import("../../app/active-workspace")>()),
  useActiveWorkspaceId: () => workspaceState.id,
}));
vi.mock("../../api/business", async (importOriginal) => ({
  // eslint-disable-next-line @typescript-eslint/consistent-type-imports
  ...(await importOriginal<typeof import("../../api/business")>()),
  listProposals: api.listProposals,
  listSourceVersions: api.listSourceVersions,
  listWorkflows: api.listWorkflows,
}));
vi.mock("../../api/workspace", async (importOriginal) => ({
  // eslint-disable-next-line @typescript-eslint/consistent-type-imports
  ...(await importOriginal<typeof import("../../api/workspace")>()),
  getWorkspace: api.getWorkspace,
}));
vi.mock("../system-status/use-system-status", () => ({ useSystemStatus: systemStatus.useSystemStatus }));

import { DashboardPage } from "./DashboardPage";

const renderPage = () => render(
  <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
    <MemoryRouter><DashboardPage /></MemoryRouter>
  </QueryClientProvider>,
);

const source = (id: string, path: string) => ({
  id,
  sourceId: "50000000-0000-4000-8000-000000000001",
  path,
  mimeType: "text/markdown",
  byteSize: 2048,
  capturedAt: "2026-07-31T09:36:00Z",
  contentHash: "a".repeat(64),
  securityStatus: "passed" as const,
  ingestionStatus: "parsed" as const,
  indexStatus: "included" as const,
});

beforeEach(() => {
  workspaceState.id = workspaceId;
  api.getWorkspace.mockResolvedValue({
    id: workspaceId,
    name: "知序产品知识库",
    rootPath: "/tmp/workspace",
    status: "active",
    version: 1,
    git: { present: true, repositoryPath: "/tmp/workspace", branch: "main", head: "abc", dirty: false, checkedAt: "2026-07-22T00:00:00Z" },
    warnings: [],
    createdAt: "2026-07-22T00:00:00Z",
    updatedAt: "2026-07-22T00:00:00Z",
  });
  api.listProposals.mockResolvedValue({ items: [] });
  api.listSourceVersions.mockResolvedValue({ items: [] });
  api.listWorkflows.mockResolvedValue({ items: [] });
});

afterEach(() => {
  cleanup();
  api.getWorkspace.mockReset();
  api.listProposals.mockReset();
  api.listSourceVersions.mockReset();
  api.listWorkflows.mockReset();
  systemStatus.useSystemStatus.mockReset();
});

describe("DashboardPage", () => {
  it("没有活动工作区时展示极简知识脉络且不发业务请求", () => {
    workspaceState.id = "";
    renderPage();

    expect(screen.getByRole("heading", { name: "知序" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: /让每一条知识/ })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /连接知识目录/ })).toHaveAttribute("href", "/workspace");
    expect(screen.getAllByText("本地资料")).toHaveLength(2);
    expect(screen.getAllByText("知识主题")).toHaveLength(2);
    expect(screen.getAllByText("证据关系")).toHaveLength(2);
    expect(screen.getAllByText("待审提案")).toHaveLength(2);

    const boundary = screen.getByText("只连接你选择的本地目录；AI 建议经确认后才写回。");
    expect(boundary).not.toBeVisible();
    const boundaryTrigger = screen.getByRole("button", { name: /数据边界/ });
    expect(boundaryTrigger).toHaveAttribute("aria-expanded", "false");
    fireEvent.click(boundaryTrigger);
    expect(boundaryTrigger).toHaveAttribute("aria-expanded", "true");
    expect(boundary).toBeVisible();

    expect(api.getWorkspace).not.toHaveBeenCalled();
    expect(api.listProposals).not.toHaveBeenCalled();
    expect(api.listSourceVersions).not.toHaveBeenCalled();
    expect(api.listWorkflows).not.toHaveBeenCalled();
    expect(systemStatus.useSystemStatus).not.toHaveBeenCalled();
    expect(screen.queryByText(/请求 ID|能力矩阵|System status/i)).not.toBeInTheDocument();
  });

  it("已连接时按真实查询优先展示等待人工流程和最近捕获资料", async () => {
    api.listWorkflows.mockImplementation((_id: string, params: { status?: string }) => Promise.resolve({
      items: params.status === "waiting_for_human" ? [{
        id: "20000000-0000-4000-8000-000000000001",
        workspaceId,
        definitionKey: "manual_review",
        definitionVersion: 1,
        status: "waiting_for_human",
        version: 2,
        createdAt: "2026-07-30T08:00:00Z",
        updatedAt: "2026-07-31T08:00:00Z",
        waitingForHuman: true,
        pauseRequested: false,
        cancelRequested: false,
      }] : [{
        id: "20000000-0000-4000-8000-000000000002",
        workspaceId,
        definitionKey: "failed_index",
        definitionVersion: 1,
        status: "failed",
        version: 2,
        createdAt: "2026-07-30T08:00:00Z",
        updatedAt: "2026-08-01T08:00:00Z",
        waitingForHuman: false,
        pauseRequested: false,
        cancelRequested: false,
      }],
    }));
    api.listProposals.mockResolvedValue({ items: [{
      id: "30000000-0000-4000-8000-000000000001",
      workspaceId,
      type: "file_patch",
      status: "ready_for_review",
      target: "docs/high-risk.md",
      riskLevel: "CRITICAL",
      risk: "高风险变更",
      revisionId: "40000000-0000-4000-8000-000000000001",
      changeHash: "b".repeat(64),
      createdAt: "2026-07-31T08:00:00Z",
      updatedAt: "2026-08-01T09:00:00Z",
    }] });
    api.listSourceVersions.mockResolvedValue({ items: [
      source("60000000-0000-4000-8000-000000000001", "research/homepage.md"),
      source("60000000-0000-4000-8000-000000000002", "docs/data-boundary.md"),
    ] });

    renderPage();

    expect(screen.getByRole("heading", { name: "今天的知识桌面" })).toBeInTheDocument();
    expect(await screen.findByTitle("/tmp/workspace")).toHaveTextContent("知序产品知识库 · main · 工作树干净");
    expect(await screen.findByRole("heading", { name: "manual_review" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /继续流程/ })).toHaveAttribute("href", "/workflows/20000000-0000-4000-8000-000000000001");
    expect(screen.queryByText("failed_index")).not.toBeInTheDocument();
    expect(screen.queryByText("docs/high-risk.md")).not.toBeInTheDocument();

    expect(await screen.findByRole("heading", { name: "homepage.md" })).toBeInTheDocument();
    expect(screen.getByText("research/homepage.md")).toBeInTheDocument();
    expect(screen.getByText("已通过")).toBeInTheDocument();
    expect(screen.getByText("已解析")).toBeInTheDocument();
    expect(screen.getByText("已纳入")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /data-boundary.md/ })).toHaveAttribute("href", "/documents/60000000-0000-4000-8000-000000000002");

    expect(api.listWorkflows).toHaveBeenCalledWith(workspaceId, { limit: 1, status: "waiting_for_human" }, expect.anything());
    expect(api.listWorkflows).toHaveBeenCalledWith(workspaceId, { limit: 1, status: "failed" }, expect.anything());
    expect(api.listProposals).toHaveBeenCalledWith(workspaceId, { limit: 5, status: "ready_for_review" }, expect.anything());
    expect(api.listSourceVersions).toHaveBeenCalledWith(workspaceId, { limit: 4 }, expect.anything());
    expect(systemStatus.useSystemStatus).not.toHaveBeenCalled();
  });

  it("部分待办失败时保留成功结果并提供独立重试", async () => {
    api.listWorkflows.mockImplementation((_id: string, params: { status?: string }) => params.status === "waiting_for_human"
      ? Promise.reject(new Error("waiting workflow unavailable"))
      : Promise.resolve({ items: [] }));
    api.listProposals.mockResolvedValue({ items: [{
      id: "30000000-0000-4000-8000-000000000001",
      workspaceId,
      type: "file_patch",
      status: "ready_for_review",
      target: "docs/review.md",
      riskLevel: "HIGH",
      risk: "需要审阅",
      revisionId: "40000000-0000-4000-8000-000000000001",
      changeHash: "b".repeat(64),
      createdAt: "2026-07-31T08:00:00Z",
      updatedAt: "2026-08-01T09:00:00Z",
    }] });

    renderPage();

    expect(await screen.findByRole("heading", { name: "docs/review.md" })).toBeInTheDocument();
    expect(screen.getByText("部分待办读取失败")).toBeInTheDocument();
    expect(screen.getByText("当前焦点来自已成功返回的有界列表，结果可能不完整。")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: /重新读取/ }));
    await waitFor(() => expect(api.listWorkflows).toHaveBeenCalledTimes(3));
  });

  it("没有待办和资料时显示可执行空态，不填充示例数字", async () => {
    renderPage();

    expect(await screen.findByRole("heading", { name: "今天没有等待处理的事项" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /查看资料收件箱/ })).toHaveAttribute("href", "/inbox");
    expect(await screen.findByRole("heading", { name: "还没有捕获资料" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "前往资料收件箱" })).toHaveAttribute("href", "/inbox");
    expect(screen.queryByText(/总计|最近进入|示例/)).not.toBeInTheDocument();
  });
});
