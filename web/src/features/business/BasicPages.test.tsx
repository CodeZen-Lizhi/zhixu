import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes, useNavigate } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";

const workspaceId = "10000000-0000-4000-8000-000000000002";
const sourceVersionId = "10000000-0000-4000-8000-000000000004";
const api = vi.hoisted(() => ({ getSourceVersion: vi.fn(), listSourceVersions: vi.fn() }));
const workspaceState = vi.hoisted(() => ({ id: "10000000-0000-4000-8000-000000000002" }));

vi.mock("../../api/business", async (importOriginal) => ({
  // eslint-disable-next-line @typescript-eslint/consistent-type-imports
  ...(await importOriginal<typeof import("../../api/business")>()),
  getSourceVersion: api.getSourceVersion,
  listSourceVersions: api.listSourceVersions,
}));
vi.mock("../../app/active-workspace", async (importOriginal) => ({
  // eslint-disable-next-line @typescript-eslint/consistent-type-imports
  ...(await importOriginal<typeof import("../../app/active-workspace")>()),
  getActiveWorkspaceId: () => workspaceState.id,
  useActiveWorkspaceId: () => workspaceState.id,
}));

import { DocumentsPage, InboxPage } from "./BasicPages";

const HistoryBackButton = () => {
  const navigate = useNavigate();
  return <button type="button" onClick={() => void navigate(-1)}>浏览器返回</button>;
};

const sourceVersion = (overrides: Record<string, unknown> = {}) => ({
  id: sourceVersionId,
  sourceId: "10000000-0000-4000-8000-000000000003",
  path: "docs/history.md",
  mimeType: "text/markdown",
  byteSize: 42,
  capturedAt: "2026-07-01T00:00:00Z",
  contentHash: "a".repeat(64),
  securityStatus: "passed",
  ingestionStatus: "parsed",
  workflowStatus: "running",
  indexStatus: "included",
  ...overrides,
});

const renderDetail = () => render(
  <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
    <MemoryRouter initialEntries={[`/documents/${sourceVersionId}`]}>
      <Routes><Route path="/documents/:sourceVersionId" element={<DocumentsPage />} /></Routes>
    </MemoryRouter>
  </QueryClientProvider>,
);
const renderInbox = () => render(
  <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
    <MemoryRouter initialEntries={["/inbox"]}><InboxPage /><HistoryBackButton /></MemoryRouter>
  </QueryClientProvider>,
);

afterEach(() => {
  cleanup();
  workspaceState.id = workspaceId;
  api.getSourceVersion.mockReset();
  api.listSourceVersions.mockReset();
});

describe("DocumentsPage Source Version projections", () => {
  it.each([
    ["included", "已纳入"],
    ["excluded", "已排除"],
  ] as const)("展示解析、Workflow 与 %s 索引状态", async (indexStatus, indexLabel) => {
    api.getSourceVersion.mockResolvedValue(sourceVersion({ indexStatus }));
    renderDetail();

    expect(await screen.findByText("已解析")).toBeInTheDocument();
    expect(screen.getByText("运行中")).toBeInTheDocument();
    expect(screen.getByText(indexLabel)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "在搜索中限定此版本" })).toHaveAttribute(
      "href",
      `/search?source_version_id=${sourceVersionId}&scope_workspace=${workspaceId}`,
    );
		expect(screen.queryByRole("link", { name: "打开来源事实" })).not.toBeInTheDocument();
    expect(api.getSourceVersion).toHaveBeenCalledWith(workspaceId, sourceVersionId, expect.any(AbortSignal));
  });

  it("distinguishes absent projections from completed states", async () => {
    api.getSourceVersion.mockResolvedValue(sourceVersion({
      ingestionStatus: undefined,
      workflowStatus: undefined,
      indexStatus: undefined,
    }));
    renderDetail();

    expect(await screen.findByText("未开始")).toBeInTheDocument();
    expect(screen.getByText("未绑定")).toBeInTheDocument();
    expect(screen.getByText("未选择")).toBeInTheDocument();
    expect(screen.queryByText("已解析")).not.toBeInTheDocument();
  });

  it("没有活动 Workspace 时不发详情请求且提供连接入口", () => {
    workspaceState.id = "";
    renderDetail();

    expect(screen.getByText("先连接 Workspace")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "连接或切换 Workspace" })).toHaveAttribute("href", "/workspace");
    expect(api.getSourceVersion).not.toHaveBeenCalled();
  });
});

describe("InboxPage Workspace gate", () => {
  it("没有活动 Workspace 时不显示伪加载且不发列表请求", () => {
    workspaceState.id = "";
    renderInbox();

    expect(screen.getByText("先连接 Workspace")).toBeInTheDocument();
    expect(screen.queryByText("读取中…")).not.toBeInTheDocument();
    expect(api.listSourceVersions).not.toHaveBeenCalled();
  });

  it("浏览器历史恢复旧筛选时从第一页请求，不复活当前筛选的游标", async () => {
    api.listSourceVersions.mockResolvedValue({ items: [], nextCursor: "source-page-2" });
    renderInbox();
    await waitFor(() => expect(api.listSourceVersions).toHaveBeenLastCalledWith(
      workspaceId,
      { limit: 30 },
      expect.any(AbortSignal),
    ));

    fireEvent.change(screen.getByRole("combobox", { name: "安全状态" }), { target: { value: "passed" } });
    await waitFor(() => expect(api.listSourceVersions).toHaveBeenLastCalledWith(
      workspaceId,
      { limit: 30, securityStatus: "passed" },
      expect.any(AbortSignal),
    ));
    fireEvent.click(screen.getByRole("button", { name: "下一页" }));
    await waitFor(() => expect(api.listSourceVersions).toHaveBeenLastCalledWith(
      workspaceId,
      { limit: 30, cursor: "source-page-2", securityStatus: "passed" },
      expect.any(AbortSignal),
    ));

    fireEvent.click(screen.getByRole("button", { name: "浏览器返回" }));

    await waitFor(() => expect(api.listSourceVersions).toHaveBeenLastCalledWith(
      workspaceId,
      { limit: 30 },
      expect.any(AbortSignal),
    ));
  });
});
