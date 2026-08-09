import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";

import { SearchApiError, type SearchResponse } from "../../api/search";

const workspaceId = "92000000-0000-4000-8000-000000000001";
const workspaceB = "92000000-0000-4000-8000-000000000009";
const sourceVersionId = "92000000-0000-4000-8000-000000000008";
const spanId = "92000000-0000-4000-8000-000000000006";
const api = vi.hoisted(() => ({ search: vi.fn() }));
const workspaceState = vi.hoisted(() => ({ id: "92000000-0000-4000-8000-000000000001" }));
const eventState = vi.hoisted(() => ({
  recover: undefined as ((workspaceId: string) => Promise<void>) | undefined,
}));

vi.mock("../../api/search", async (importOriginal) => ({
  // eslint-disable-next-line @typescript-eslint/consistent-type-imports
  ...(await importOriginal<typeof import("../../api/search")>()),
  search: api.search,
}));
vi.mock("../../app/active-workspace", async (importOriginal) => ({
  // eslint-disable-next-line @typescript-eslint/consistent-type-imports
  ...(await importOriginal<typeof import("../../app/active-workspace")>()),
  useActiveWorkspaceId: () => workspaceState.id,
}));
vi.mock("../../events/event-store", () => ({
  useRegisterWorkspaceRecovery: (recover: (workspaceId: string) => Promise<void>) => {
    eventState.recover = recover;
  },
}));

import { SearchPage } from "./SearchPage";

const response = (overrides: Partial<SearchResponse> = {}): SearchResponse => ({
  workspaceId,
  indexVersionId: "92000000-0000-4000-8000-000000000002",
  embeddingVersionId: "92000000-0000-4000-8000-000000000003",
  requestedMode: "semantic",
  effectiveMode: "semantic",
  indexDegradedCapabilities: [],
  degradations: [{ capability: "rerank", errorCode: "RETRIEVAL_RERANK_UNAVAILABLE", retryable: true }],
  items: [{
    chunkId: "92000000-0000-4000-8000-000000000004",
    parseProjectionId: "92000000-0000-4000-8000-000000000005",
    sequence: 0,
    contentHash: "a".repeat(64),
    headingPath: ["Semantic Link", "Approval"],
    span: { spanId, startLine: 2, endLine: 3, startByte: 5, endByte: 25 },
    snippet: "Approval 后由 Knowledge apply 正式写入 Relation。",
    provenances: [{
      sourceId: "92000000-0000-4000-8000-000000000007",
      sourceVersionId,
      relativePath: "docs/semantic-links.md",
      capturedAt: "2026-07-19T01:02:03Z",
      sourceVersionHref: `/api/v1/workspaces/${workspaceId}/source-versions/${sourceVersionId}`,
      sourceSpanHref: `/api/v1/workspaces/${workspaceId}/source-versions/${sourceVersionId}/spans/${spanId}`,
    }],
    provenanceTruncated: false,
    scores: {
      lexical: null,
      vector: { rank: 1, distance: 0.125 },
      fusion: { rank: 1, score: 0.0327 },
      rerank: null,
    },
  }],
  ...overrides,
});

const LocationProbe = () => {
  const location = useLocation();
  return <output data-testid="location">{location.pathname}{location.search}</output>;
};

const pageElement = (entry: string, client: QueryClient) => (
  <QueryClientProvider client={client}>
    <MemoryRouter initialEntries={[entry]}>
      <LocationProbe />
      <Routes><Route path="/search" element={<SearchPage />} /></Routes>
    </MemoryRouter>
  </QueryClientProvider>
);

const renderPage = (entry = "/search") => {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return { client, entry, ...render(pageElement(entry, client)) };
};

afterEach(() => {
  cleanup();
  api.search.mockReset();
  workspaceState.id = workspaceId;
  eventState.recover = undefined;
});

describe("SearchPage", () => {
  it("没有活动 Workspace 时不发送 Search 请求", () => {
    workspaceState.id = "";
    renderPage();

    expect(screen.getByText("先连接 Workspace")).toBeInTheDocument();
    expect(api.search).not.toHaveBeenCalled();
  });

  it("从 URL 恢复模式、Source Version 与 cursor，并展示可打开 Evidence 和原始 distance", async () => {
    api.search.mockResolvedValue(response({ nextCursor: "page-3" }));
    renderPage(`/search?query=relation&mode=semantic&source_version_id=${sourceVersionId}&cursor=page-2&scope_workspace=${workspaceId}`);

    expect(await screen.findByText("Approval 后由 Knowledge apply 正式写入 Relation。")).toBeInTheDocument();
    expect(api.search).toHaveBeenCalledWith({
      workspaceId,
      query: "relation",
      retrievalMode: "semantic",
      filters: { sourceVersionIds: [sourceVersionId] },
      cursor: "page-2",
      limit: 20,
    }, expect.any(AbortSignal));
    expect(screen.getByText("向量 #1 · 距离 0.1250")).toBeInTheDocument();
    expect(screen.getByText("rerank：RETRIEVAL_RERANK_UNAVAILABLE（可重试）")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "资料版本" })).toHaveAttribute("href", `/documents/${sourceVersionId}`);
    expect(screen.getByRole("button", { name: "打开证据片段" })).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "打开证据片段" })).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "下一页" }));
    await vi.waitFor(() => expect(api.search).toHaveBeenLastCalledWith(expect.objectContaining({ cursor: "page-3" }), expect.any(AbortSignal)));
  });

  it("提交查询和 Source Version 后写入可恢复 URL，并区分空结果", async () => {
    api.search.mockResolvedValue(response({
      requestedMode: "keyword",
      effectiveMode: "keyword",
      embeddingVersionId: null,
      degradations: [],
      items: [],
    }));
    renderPage();

    expect(screen.getByRole("option", { name: "混合" })).toBeInTheDocument();
    expect(screen.getByRole("option", { name: "关键词" })).toBeInTheDocument();
    expect(screen.getByRole("option", { name: "语义" })).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("检索内容"), { target: { value: "  durable relation  " } });
    fireEvent.change(screen.getByLabelText("检索模式"), { target: { value: "keyword" } });
    fireEvent.change(screen.getByLabelText("资料版本（可选）"), { target: { value: sourceVersionId } });
    fireEvent.click(screen.getByRole("button", { name: "检索" }));

    expect(await screen.findByText("没有命中证据")).toBeInTheDocument();
    expect(api.search).toHaveBeenCalledWith({
      workspaceId,
      query: "durable relation",
      retrievalMode: "keyword",
      filters: { sourceVersionIds: [sourceVersionId] },
      limit: 20,
    }, expect.any(AbortSignal));
    expect(screen.getByTestId("location")).toHaveTextContent(`query=durable+relation`);
    expect(screen.getByTestId("location")).toHaveTextContent("mode=keyword");
    expect(screen.getByTestId("location")).toHaveTextContent(`source_version_id=${sourceVersionId}`);
    expect(screen.getByTestId("location")).toHaveTextContent(`scope_workspace=${workspaceId}`);
  });

  it("Workspace 切换会清除旧 query、Source Version 与 cursor，且不在新 Workspace 自动检索", async () => {
    api.search.mockResolvedValue(response());
    const page = renderPage(`/search?query=relation&mode=semantic&source_version_id=${sourceVersionId}&cursor=page-2&scope_workspace=${workspaceId}`);

    expect(await screen.findByText("Approval 后由 Knowledge apply 正式写入 Relation。")).toBeInTheDocument();
    api.search.mockClear();
    workspaceState.id = workspaceB;
    page.rerender(pageElement(page.entry, page.client));

    await vi.waitFor(() => {
      expect(screen.getByTestId("location")).not.toHaveTextContent("query=");
      expect(screen.getByTestId("location")).not.toHaveTextContent("source_version_id=");
      expect(screen.getByTestId("location")).not.toHaveTextContent("cursor=");
      expect(screen.getByTestId("location")).not.toHaveTextContent("scope_workspace=");
    });
    expect(api.search).not.toHaveBeenCalled();
  });

  it("显式展示 Hybrid 降级为 Keyword 的能力差异", async () => {
    api.search.mockResolvedValue(response({
      requestedMode: "hybrid",
      effectiveMode: "keyword",
      embeddingVersionId: null,
      indexDegradedCapabilities: ["vector"],
      degradations: [{ capability: "vector", errorCode: "RETRIEVAL_VECTOR_UNAVAILABLE", retryable: true }],
    }));
    renderPage(`/search?query=relation&scope_workspace=${workspaceId}`);

    expect(await screen.findByText("检索已降级，但没有隐藏能力差异")).toBeInTheDocument();
    expect(screen.getByText("索引缺失能力：vector")).toBeInTheDocument();
    expect(screen.getByText("vector：RETRIEVAL_VECTOR_UNAVAILABLE（可重试）")).toBeInTheDocument();
  });

  it("Semantic 不可用时展示 503，而不是空结果或 Keyword 假成功", async () => {
    api.search.mockRejectedValue(new SearchApiError(
      "RETRIEVAL_SEMANTIC_UNAVAILABLE",
      "当前 Active Index 不支持 Semantic Search",
      false,
      503,
    ));
    renderPage(`/search?query=relation&mode=semantic&scope_workspace=${workspaceId}`);

    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("检索请求失败");
    expect(alert).toHaveTextContent("RETRIEVAL_SEMANTIC_UNAVAILABLE");
    expect(screen.queryByText("没有命中证据")).not.toBeInTheDocument();
  });

  it("展示全部返回的 provenance，并明确标记服务端截断", async () => {
    const first = response().items[0];
    if (!first) throw new Error("Search fixture must contain one Evidence item");
    const secondSourceVersionId = "92000000-0000-4000-8000-000000000010";
    const secondSpanId = "92000000-0000-4000-8000-000000000011";
    api.search.mockResolvedValue(response({
      items: [{
        ...first,
        provenances: [
          ...first.provenances,
          {
            sourceId: "92000000-0000-4000-8000-000000000012",
            sourceVersionId: secondSourceVersionId,
            relativePath: "docs/approval-river.md",
            capturedAt: "2026-07-20T01:02:03Z",
            sourceVersionHref: `/api/v1/workspaces/${workspaceId}/source-versions/${secondSourceVersionId}`,
            sourceSpanHref: `/api/v1/workspaces/${workspaceId}/source-versions/${secondSourceVersionId}/spans/${secondSpanId}`,
          },
        ],
        provenanceTruncated: true,
      }],
    }));
    renderPage(`/search?query=relation&scope_workspace=${workspaceId}`);

    expect(await screen.findByText("docs/semantic-links.md")).toBeInTheDocument();
    expect(screen.getByText("docs/approval-river.md")).toBeInTheDocument();
    expect(screen.getAllByRole("button", { name: "打开证据片段" })).toHaveLength(2);
    expect(screen.getByText("该证据还有更多来源，当前响应已按服务端上限截断。")).toBeInTheDocument();
  });

  it("输入无效时显示表单错误且不访问 API", () => {
    renderPage();

    fireEvent.change(screen.getByLabelText("检索内容"), { target: { value: "valid query" } });
    fireEvent.change(screen.getByLabelText("资料版本（可选）"), { target: { value: "not-a-uuid" } });
    fireEvent.click(screen.getByRole("button", { name: "检索" }));

    expect(screen.getByRole("alert")).toHaveTextContent("资料版本必须为空或使用规范 UUID");
    expect(api.search).not.toHaveBeenCalled();
  });

  it("明确展示加载状态", () => {
    api.search.mockImplementation(() => new Promise<SearchResponse>(() => undefined));
    renderPage("/search?query=pending");

    expect(screen.getByLabelText("正在加载检索证据")).toBeInTheDocument();
  });

  it("cursor stale 时允许从第一页重新检索", async () => {
    api.search
      .mockRejectedValueOnce(new SearchApiError("RETRIEVAL_SEARCH_CURSOR_STALE", "结果已变化", false, 409))
      .mockResolvedValueOnce(response());
    renderPage(`/search?query=relation&mode=semantic&cursor=page-2&scope_workspace=${workspaceId}`);

    const restart = await screen.findByRole("button", { name: "从第一页重新检索" });
    expect(screen.getByRole("alert")).toHaveTextContent("检索结果窗口已失效");
    fireEvent.click(restart);

    expect(await screen.findByText("Approval 后由 Knowledge apply 正式写入 Relation。")).toBeInTheDocument();
    expect(api.search).toHaveBeenLastCalledWith({
      workspaceId,
      query: "relation",
      retrievalMode: "semantic",
      limit: 20,
    }, expect.any(AbortSignal));
    expect(api.search).toHaveBeenCalledTimes(2);
    expect(screen.getByTestId("location")).not.toHaveTextContent("cursor=");
  });

  it("SSE 恢复回调清除 URL cursor 并权威回查首屏", async () => {
    const initial = response();
    const firstItem = initial.items[0];
    if (!firstItem) throw new Error("Search fixture must contain one Evidence item");
    api.search
      .mockResolvedValueOnce(initial)
      .mockResolvedValueOnce(response({ items: [{ ...firstItem, snippet: "SSE 恢复后的首屏 Evidence。" }] }));
    renderPage(`/search?query=relation&mode=semantic&cursor=page-2&scope_workspace=${workspaceId}`);

    expect(await screen.findByText("Approval 后由 Knowledge apply 正式写入 Relation。")).toBeInTheDocument();
    expect(eventState.recover).toBeTypeOf("function");

    await act(async () => {
      await eventState.recover?.(workspaceId);
    });

    expect(await screen.findByText("SSE 恢复后的首屏 Evidence。")).toBeInTheDocument();
    expect(api.search).toHaveBeenLastCalledWith({
      workspaceId,
      query: "relation",
      retrievalMode: "semantic",
      limit: 20,
    }, expect.any(AbortSignal));
    expect(api.search).toHaveBeenCalledTimes(2);
    expect(screen.getByTestId("location")).not.toHaveTextContent("cursor=");
  });

  it("SSE 首屏恢复失败会拒绝 recovery，且不会启动第二个恢复请求", async () => {
    const recoveryError = new SearchApiError("NETWORK_ERROR", "Search recovery unavailable", true);
    api.search
      .mockResolvedValueOnce(response())
      .mockRejectedValueOnce(recoveryError);
    renderPage(`/search?query=relation&mode=semantic&cursor=page-2&scope_workspace=${workspaceId}`);

    expect(await screen.findByText("Approval 后由 Knowledge apply 正式写入 Relation。")).toBeInTheDocument();
    expect(eventState.recover).toBeTypeOf("function");

    await act(async () => {
      await expect(eventState.recover?.(workspaceId)).rejects.toBe(recoveryError);
    });

    expect(api.search).toHaveBeenCalledTimes(2);
    expect(screen.getByTestId("location")).not.toHaveTextContent("cursor=");
  });
});
