import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const workspaceId = "10000000-0000-4000-8000-000000000002";
const workspaceState = vi.hoisted(() => ({
  id: "10000000-0000-4000-8000-000000000002",
  active: {
    status: "ready",
    workspace: {
      id: "10000000-0000-4000-8000-000000000002",
      name: "知序产品知识库",
      rootPath: "/tmp/workspace",
      status: "active" as const,
      availability: "available" as const,
      version: 1,
    },
    error: undefined as Error | undefined,
    refresh: vi.fn(() => Promise.resolve()),
  },
}));
const api = vi.hoisted(() => ({
  getAuthoringOverview: vi.fn(),
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
vi.mock("../../app/WorkspaceCacheBoundary", () => ({ useActiveWorkspace: () => workspaceState.active }));
vi.mock("../../api/business", async (importOriginal) => ({
  // eslint-disable-next-line @typescript-eslint/consistent-type-imports
  ...(await importOriginal<typeof import("../../api/business")>()),
  listProposals: api.listProposals,
  listSourceVersions: api.listSourceVersions,
  listWorkflows: api.listWorkflows,
}));
vi.mock("../../api/authoring", async (importOriginal) => ({
  // eslint-disable-next-line @typescript-eslint/consistent-type-imports
  ...(await importOriginal<typeof import("../../api/authoring")>()),
  getAuthoringOverview: api.getAuthoringOverview,
}));
vi.mock("../system-status/use-system-status", () => ({ useSystemStatus: systemStatus.useSystemStatus }));

import { DashboardPage } from "./DashboardPage";

const renderPage = () => render(
  <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
    <MemoryRouter><DashboardPage /></MemoryRouter>
  </QueryClientProvider>,
);

const source = (id: string, path: string, capturedAt = new Date().toISOString()) => ({
  id,
  sourceId: "50000000-0000-4000-8000-000000000001",
  path,
  mimeType: "text/markdown",
  byteSize: 2048,
  capturedAt,
  contentHash: "a".repeat(64),
  securityStatus: "passed" as const,
  ingestionStatus: "parsed" as const,
  indexStatus: "included" as const,
});

beforeEach(() => {
  workspaceState.id = workspaceId;
  workspaceState.active.status = "ready";
  workspaceState.active.workspace.id = workspaceId;
  workspaceState.active.workspace.name = "知序产品知识库";
  workspaceState.active.workspace.rootPath = "/tmp/workspace";
  api.listProposals.mockResolvedValue({ items: [] });
  api.listSourceVersions.mockResolvedValue({ items: [] });
  api.listWorkflows.mockResolvedValue({ items: [] });
  api.getAuthoringOverview.mockResolvedValue({
    workspaceId,
    organizing: { available: false, reason: "整理模板尚未接入", href: null },
    recentDrafts: [],
    pendingPublications: [],
    completedDocuments: [],
  });
});

afterEach(() => {
  cleanup();
  api.listProposals.mockReset();
  api.listSourceVersions.mockReset();
  api.listWorkflows.mockReset();
  api.getAuthoringOverview.mockReset();
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

    const dashboardHeading = screen.getByRole("heading", { name: "今天的知识桌面" });
    expect(dashboardHeading).toBeInTheDocument();
    const workspaceSummary = dashboardHeading.parentElement?.querySelector("p");
    await waitFor(() => expect(workspaceSummary).toHaveTextContent("知序产品知识库 · 当前目录可用"));
    expect(workspaceSummary).not.toHaveAttribute("title");
    expect(screen.queryByText("/tmp/workspace")).not.toBeInTheDocument();
    expect(screen.queryByTitle("/tmp/workspace")).not.toBeInTheDocument();
    expect(await screen.findByRole("heading", { name: "manual_review" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /快速记录/ })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /新建文章/ })).toHaveAttribute("href", "/authoring/new");
    expect(screen.getByRole("button", { name: /整理成文/ })).toBeDisabled();
    expect(screen.getByRole("link", { name: /搜索知识/ })).toHaveAttribute("href", "/search");
    expect(screen.getByRole("link", { name: "工作区设置" })).toHaveAttribute("href", "/settings?section=workspace");
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
    expect(screen.getAllByRole("link", { name: /新建文章/ }).some((link) => link.getAttribute("href") === "/authoring/new")).toBe(true);
    expect(await screen.findByRole("heading", { name: "从一篇文章开始" })).toBeInTheDocument();
    expect(screen.getByText("今天还没有新的业务记录。")).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: /资料收件箱/ })).not.toBeInTheDocument();
    expect(screen.queryByText(/总计|最近进入|示例/)).not.toBeInTheDocument();
  });

  it("有创作草稿时优先作为继续事项，并把当天资料放入动态而非重复收件箱", async () => {
    api.getAuthoringOverview.mockResolvedValue({
      workspaceId,
      organizing: { available: true, reason: null, href: "/authoring/organize" },
      recentDrafts: [{
        id: "d0000000-0000-4000-8000-000000000001",
        workspaceId,
        documentId: null,
        title: "系统设计草稿",
        targetPath: "notes/system-design.md",
        status: "EDITING",
        version: 3,
        updatedAt: new Date().toISOString(),
      }],
      pendingPublications: [],
      completedDocuments: [],
    });
    api.listSourceVersions.mockResolvedValue({ items: [source("60000000-0000-4000-8000-000000000001", "research/homepage.md")] });

    renderPage();

    expect(await screen.findByRole("heading", { name: "系统设计草稿" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /打开草稿/ })).toHaveAttribute("href", "/authoring/new?draft=d0000000-0000-4000-8000-000000000001");
    expect(screen.getByRole("link", { name: /homepage\.md/ })).toHaveAttribute("href", "/documents/60000000-0000-4000-8000-000000000001");
    expect(screen.queryByRole("link", { name: /资料收件箱/ })).not.toBeInTheDocument();
  });

  it("overview 提供真实入口后才启用整理成文", async () => {
    api.getAuthoringOverview.mockResolvedValue({
      workspaceId,
      organizing: { available: true, reason: null, href: "/authoring/organize" },
      recentDrafts: [],
      pendingPublications: [],
      completedDocuments: [],
    });
    renderPage();

    expect(await screen.findByRole("link", { name: /整理成文/ })).toHaveAttribute("href", "/authoring/organize");
  });
});
