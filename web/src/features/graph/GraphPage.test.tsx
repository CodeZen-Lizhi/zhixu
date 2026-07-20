import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { MemoryRouter } from "react-router-dom";

import { setActiveWorkspaceId } from "../../app/active-workspace";
import { GraphPage } from "./GraphPage";

const workspaceId = "92000000-0000-4000-8000-000000000001";
const topicId = "92000000-0000-4000-8000-000000000002";
const claimId = "92000000-0000-4000-8000-000000000003";
const relationId = "92000000-0000-4000-8000-000000000004";
const evidenceId = "92000000-0000-4000-8000-000000000005";
const sourceVersionId = "92000000-0000-4000-8000-000000000006";
const sourceSpanId = "92000000-0000-4000-8000-000000000007";
const timestamp = "2026-07-20T09:30:00.123456789Z";
const fingerprint = "a".repeat(64);

const topicNode = {
  type: "TOPIC",
  id: topicId,
  workspace_id: workspaceId,
  name: "Graph Projection",
  description: "正式知识的有界只读图。",
  topic_status: "ACTIVE",
  version: 2,
  updated_at: timestamp,
};

const claimNode = {
  type: "CLAIM",
  id: claimId,
  workspace_id: workspaceId,
  statement: "Graph 只投影 Knowledge 中的正式事实。",
  claim_status: "CONFIRMED",
  confidence: 0.92,
  applicability: {
    schema_version: "knowledge-applicability/v1",
    value: { audience: "maintainers" },
    hash: "b".repeat(64),
  },
  version: 3,
  updated_at: timestamp,
};

const edge = {
  relation_id: relationId,
  workspace_id: workspaceId,
  source: { type: "CLAIM", id: claimId },
  target: { type: "TOPIC", id: topicId },
  type: "BELONGS_TO",
  status: "CONFIRMED",
  traversal: "REVERSE",
  confidence: 0.88,
  version: 4,
  evidence_count: 1,
  evidence_fingerprint: "c".repeat(64),
  evidence_href: `/api/v1/graph/relations/${relationId}/evidence?workspace_id=${workspaceId}`,
  updated_at: timestamp,
};

const completeMeta = { fingerprint, complete: true, truncated: false };

const globalResponse = {
  workspace_id: workspaceId,
  clusters: [{
    topic: topicNode,
    direct_claim_count: 1,
    incident_relation_count: 1,
    cluster_score: 2,
    updated_at: timestamp,
  }],
  meta: completeMeta,
};

const neighborhoodResponse = {
  workspace_id: workspaceId,
  center: { type: "TOPIC", id: topicId },
  nodes: [claimNode, topicNode],
  edges: [edge],
  boundary_nodes: [],
  layer_counts: [1],
  completed_depth: 1,
  meta: completeMeta,
};

const relationDetailResponse = {
  edge,
  confirmation: { method: "USER_APPROVAL", reference: "approval:graph" },
  fingerprint: "d".repeat(64),
  valid_from: null,
  valid_to: null,
  created_at: "2026-07-20T09:00:00Z",
  updated_at: timestamp,
};

const evidenceResponse = {
  workspace_id: workspaceId,
  relation_id: relationId,
  items: [{
    id: evidenceId,
    workspace_id: workspaceId,
    relation_id: relationId,
    provenance: {
      workspace_id: workspaceId,
      source_version_id: sourceVersionId,
      source_span_id: sourceSpanId,
    },
    reason: "该来源片段直接支持当前关系。",
    applicability: {
      schema_version: "knowledge-applicability/v1",
      value: {},
      hash: "e".repeat(64),
    },
    confirmation: { method: "SOURCE_DERIVED", reference: "source:graph" },
    source_href: `/api/v1/workspaces/${workspaceId}/source-versions/${sourceVersionId}`,
    span_href: `/api/v1/workspaces/${workspaceId}/source-versions/${sourceVersionId}/spans/${sourceSpanId}`,
    created_at: timestamp,
  }],
  meta: completeMeta,
};

const jsonResponse = (body: unknown, status = 200) => new Response(JSON.stringify(body), {
  status,
  headers: { "Content-Type": "application/json" },
});

const requestUrl = (input: RequestInfo | URL | undefined): URL => {
  if (input === undefined) throw new Error("Graph request URL is missing");
  const value = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
  return new URL(value, window.location.origin);
};

const renderPage = (entry = "/graph?mode=global") => {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={[entry]}>
        <GraphPage />
      </MemoryRouter>
    </QueryClientProvider>,
  );
};

const installCompactViewport = (): void => {
  const listeners = new Set<EventListenerOrEventListenerObject>();
  vi.stubGlobal("matchMedia", vi.fn((query: string): MediaQueryList => ({
    matches: query === "(max-width: 1080px)",
    media: query,
    onchange: null,
    addEventListener: (_type: string, listener: EventListenerOrEventListenerObject | null) => {
      if (listener !== null) listeners.add(listener);
    },
    removeEventListener: (_type: string, listener: EventListenerOrEventListenerObject | null) => {
      if (listener !== null) listeners.delete(listener);
    },
    addListener: () => undefined,
    removeListener: () => undefined,
    dispatchEvent: () => true,
  })));
};

beforeEach(() => {
  const values = new Map<string, string>();
  Object.defineProperty(window, "localStorage", {
    configurable: true,
    value: {
      clear: () => values.clear(),
      getItem: (key: string) => values.get(key) ?? null,
      removeItem: (key: string) => values.delete(key),
      setItem: (key: string, value: string) => values.set(key, value),
    },
  });
  setActiveWorkspaceId(workspaceId);
  vi.unstubAllGlobals();
});

describe("GraphPage", () => {
  it("没有活动 Workspace 时提供可操作入口且不请求 Graph", () => {
    setActiveWorkspaceId("");
    const fetchMock = vi.fn<typeof fetch>();
    vi.stubGlobal("fetch", fetchMock);

    renderPage();

    expect(screen.getByRole("heading", { name: "先连接一个 Workspace" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "返回 Workspace 配置" })).toHaveAttribute("href", "/");
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("从全局聚类进入局部图并保留真实服务端查询", async () => {
    const fetchMock = vi.fn<typeof fetch>((input) => {
      const url = requestUrl(input);
      if (url.pathname.endsWith("/global")) return Promise.resolve(jsonResponse(globalResponse));
      if (url.pathname.endsWith("/neighborhood")) return Promise.resolve(jsonResponse(neighborhoodResponse));
      return Promise.reject(new Error(`Unexpected Graph request: ${url.pathname}`));
    });
    vi.stubGlobal("fetch", fetchMock);

    renderPage();

    expect(await screen.findByRole("button", { name: /Graph Projection/ })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: /Graph Projection/ }));

    await waitFor(() => expect(screen.getByRole("button", { name: "局部" })).toHaveAttribute("aria-pressed", "true"));
    expect(await screen.findByText("Graph 只投影 Knowledge 中的正式事实。")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "选择主题：Graph Projection" })).not.toHaveAttribute("data-locked", "true");
    expect(fetchMock.mock.calls.some(([input]) => requestUrl(input).pathname.endsWith("/neighborhood"))).toBe(true);
  });

  it("节点详情显式锁定位置，切换图查询时清除旧布局", async () => {
    const fetchMock = vi.fn<typeof fetch>((input) => {
      const url = requestUrl(input);
      if (url.pathname.endsWith("/neighborhood")) return Promise.resolve(jsonResponse(neighborhoodResponse));
      if (url.pathname === `/api/v1/graph/nodes/CLAIM/${claimId}`) return Promise.resolve(jsonResponse(claimNode));
      if (url.pathname.endsWith("/global")) return Promise.resolve(jsonResponse(globalResponse));
      return Promise.reject(new Error(`Unexpected Graph request: ${url.pathname}`));
    });
    vi.stubGlobal("fetch", fetchMock);

    renderPage(`/graph?mode=local&center_type=TOPIC&center_id=${topicId}&depth=1&direction=BOTH`);

    fireEvent.click(await screen.findByRole("button", { name: "选择主张：Graph 只投影 Knowledge 中的正式事实。" }));
    const pinButton = await screen.findByRole("button", { name: "锁定节点位置" });
    fireEvent.click(pinButton);
    expect(screen.getByRole("button", { name: "已锁定，选择主张：Graph 只投影 Knowledge 中的正式事实。" })).toHaveAttribute("data-locked", "true");

    fireEvent.click(screen.getByRole("button", { name: "全局" }));
    expect(await screen.findByRole("button", { name: "选择主题：Graph Projection" })).not.toHaveAttribute("data-locked", "true");
  });

  it("打开关系详情后才按需加载 Evidence，并管理详情焦点", async () => {
    const fetchMock = vi.fn<typeof fetch>((input) => {
      const url = requestUrl(input);
      if (url.pathname.endsWith("/neighborhood")) return Promise.resolve(jsonResponse(neighborhoodResponse));
      if (url.pathname === `/api/v1/graph/relations/${relationId}`) return Promise.resolve(jsonResponse(relationDetailResponse));
      if (url.pathname.endsWith("/evidence")) return Promise.resolve(jsonResponse(evidenceResponse));
      return Promise.reject(new Error(`Unexpected Graph request: ${url.pathname}`));
    });
    vi.stubGlobal("fetch", fetchMock);

    renderPage(`/graph?mode=local&center_type=TOPIC&center_id=${topicId}&depth=1&direction=BOTH`);

    fireEvent.click(await screen.findByRole("button", { name: "选择关系：归属于，已确认" }));
    const detail = await screen.findByRole("complementary", { name: "图谱详情" });
    await waitFor(() => expect(detail).toHaveFocus());
    expect(fetchMock.mock.calls.some(([input]) => requestUrl(input).pathname.endsWith("/evidence"))).toBe(false);

    fireEvent.click(await screen.findByRole("button", { name: "加载关系证据" }));

    expect(await screen.findByText("该来源片段直接支持当前关系。")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "打开来源段落" })).toHaveAttribute(
      "href",
      `/api/v1/workspaces/${workspaceId}/source-versions/${sourceVersionId}/spans/${sourceSpanId}`,
    );
  });

  it("紧凑视口将详情作为 modal drawer，支持焦点约束和 Escape 关闭", async () => {
    installCompactViewport();
    const fetchMock = vi.fn<typeof fetch>((input) => {
      const url = requestUrl(input);
      if (url.pathname.endsWith("/neighborhood")) return Promise.resolve(jsonResponse(neighborhoodResponse));
      if (url.pathname === `/api/v1/graph/relations/${relationId}`) return Promise.resolve(jsonResponse(relationDetailResponse));
      return Promise.reject(new Error(`Unexpected Graph request: ${url.pathname}`));
    });
    vi.stubGlobal("fetch", fetchMock);
    const view = renderPage(`/graph?mode=local&center_type=TOPIC&center_id=${topicId}&depth=1&direction=BOTH`);

    const relationButton = await screen.findByRole("button", { name: "选择关系：归属于，已确认" });
    fireEvent.click(relationButton);
    const dialog = await screen.findByRole("dialog", { name: "图谱详情" });
    await waitFor(() => expect(dialog).toHaveFocus());
    expect(view.container.querySelector(".graph-header")).toHaveAttribute("inert");
    expect(view.container.querySelector(".graph-mode-switch")).toHaveAttribute("inert");
    expect(view.container.querySelector(".graph-controls")).toHaveAttribute("inert");
    expect(view.container.querySelector(".graph-workspace")).toHaveAttribute("inert");

    fireEvent.keyDown(dialog, { key: "Tab" });
    expect(screen.getByRole("button", { name: "关闭详情" })).toHaveFocus();
    fireEvent.keyDown(dialog, { key: "Escape" });

    expect(screen.queryByRole("dialog", { name: "图谱详情" })).not.toBeInTheDocument();
    expect(relationButton).toHaveFocus();
  });

  it("明确展示无路径、截断与查询超时，不伪装为空图", async () => {
    const notFound = {
      workspace_id: workspaceId,
      from: { type: "TOPIC", id: topicId },
      to: { type: "CLAIM", id: claimId },
      status: "not_found",
      nodes: [],
      edges: [],
      hop_count: 0,
      explored_nodes: 2,
      common_topic_suggestions: [topicNode],
    };
    vi.stubGlobal("fetch", vi.fn<typeof fetch>().mockResolvedValue(jsonResponse(notFound)));
    const first = renderPage(`/graph?mode=path&from_type=TOPIC&from_id=${topicId}&to_type=CLAIM&to_id=${claimId}`);
    expect(await screen.findByRole("status", { name: "路径查询结果" })).toHaveTextContent("未找到正式关系路径");
    expect(screen.getByText("共同 Topic 建议：Graph Projection")).toBeInTheDocument();
    expect(screen.getByText("路径关系过滤 · 10")).toBeInTheDocument();
    expect(screen.queryByText("Claim 最低置信度")).not.toBeInTheDocument();
    first.unmount();

    vi.stubGlobal("fetch", vi.fn<typeof fetch>().mockResolvedValue(jsonResponse({
      ...globalResponse,
      meta: { fingerprint, complete: false, truncated: true, reason: "RESULT_WINDOW_LIMIT" },
    })));
    const second = renderPage();
    expect(await screen.findByRole("status", { name: "图谱结果状态" })).toHaveTextContent("结果已截断");
    expect(screen.getByText("RESULT_WINDOW_LIMIT")).toBeInTheDocument();
    second.unmount();

    vi.stubGlobal("fetch", vi.fn<typeof fetch>().mockResolvedValue(jsonResponse({
      error_code: "GRAPH_QUERY_TIMEOUT",
      message: "graph query timed out",
      retryable: true,
    }, 504)));
    renderPage();
    expect(await screen.findByRole("alert")).toHaveTextContent("查询超时");
    expect(screen.getByRole("alert")).toHaveTextContent("GRAPH_QUERY_TIMEOUT");
  });
});
