import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { MemoryRouter } from "react-router-dom";

import { setActiveWorkspaceId } from "../../app/active-workspace";
import { GraphPage } from "./GraphPage";
import { GraphWorkspaceCacheBoundary } from "./GraphWorkspaceCacheBoundary";
import { graphQueryKeys } from "./query-keys";

const workspaceId = "92000000-0000-4000-8000-000000000001";
const otherWorkspaceId = "93000000-0000-4000-8000-000000000001";
const topicId = "92000000-0000-4000-8000-000000000002";
const claimId = "92000000-0000-4000-8000-000000000003";
const relationId = "92000000-0000-4000-8000-000000000004";
const candidateScanId = "92000000-0000-4000-8000-000000000015";
const candidateScanWorkflowRunId = "92000000-0000-4000-8000-000000000016";
const candidateScanStatusUrl = `/api/v1/graph/candidate-scans/${candidateScanId}?workspace_id=${workspaceId}`;
const evidenceId = "92000000-0000-4000-8000-000000000005";
const sourceVersionId = "92000000-0000-4000-8000-000000000006";
const sourceSpanId = "92000000-0000-4000-8000-000000000007";
const secondClaimId = "92000000-0000-4000-8000-000000000008";
const secondRelationId = "92000000-0000-4000-8000-000000000009";
const secondEvidenceId = "92000000-0000-4000-8000-000000000010";
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

const secondClaimNode = {
  ...claimNode,
  id: secondClaimId,
  statement: "Evidence 必须由用户显式展开。",
  applicability: { ...claimNode.applicability, hash: "f".repeat(64) },
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

const secondEdge = {
  ...edge,
  relation_id: secondRelationId,
  target: { type: "CLAIM", id: secondClaimId },
  type: "SUPPORTS",
  traversal: "FORWARD",
  evidence_fingerprint: "1".repeat(64),
  evidence_href: `/api/v1/graph/relations/${secondRelationId}/evidence?workspace_id=${workspaceId}`,
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

const twoRelationNeighborhoodResponse = {
  ...neighborhoodResponse,
  nodes: [claimNode, secondClaimNode, topicNode],
  edges: [edge, secondEdge],
  layer_counts: [2],
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

const secondRelationDetailResponse = {
  ...relationDetailResponse,
  edge: secondEdge,
  fingerprint: "2".repeat(64),
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

const secondEvidenceResponse = {
  ...evidenceResponse,
  relation_id: secondRelationId,
  items: [{
    ...evidenceResponse.items[0],
    id: secondEvidenceId,
    relation_id: secondRelationId,
    reason: "第二条关系的证据只应在显式展开后加载。",
  }],
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
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: Infinity } } });
  const view = render(
    <QueryClientProvider client={queryClient}>
      <GraphWorkspaceCacheBoundary>
        <MemoryRouter initialEntries={[entry]}>
          <GraphPage />
        </MemoryRouter>
      </GraphWorkspaceCacheBoundary>
    </QueryClientProvider>,
  );
  return { ...view, queryClient };
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

  it("Global cursor stale 后从第一页恢复且不复用旧 cursor", async () => {
    const fetchMock = vi.fn<typeof fetch>()
      .mockResolvedValueOnce(jsonResponse({
        ...globalResponse,
        meta: { fingerprint, next_cursor: "global-next", complete: false, truncated: false },
      }))
      .mockResolvedValueOnce(jsonResponse({
        error_code: "GRAPH_CURSOR_STALE",
        message: "graph result changed",
        retryable: false,
      }, 409))
      .mockResolvedValueOnce(jsonResponse(globalResponse));
    vi.stubGlobal("fetch", fetchMock);

    renderPage();

    fireEvent.click(await screen.findByRole("button", { name: "加载下一页" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("图谱结果已变化");
    fireEvent.click(screen.getByRole("button", { name: "从第一页重新加载" }));

    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(3));
    const recoveryBody = fetchMock.mock.calls[2]?.[1]?.body;
    if (typeof recoveryBody !== "string") throw new Error("Graph recovery request body is missing");
    const recoveryRequest = JSON.parse(recoveryBody) as Record<string, unknown>;
    expect(recoveryRequest).toMatchObject({ workspace_id: workspaceId, limit: 25 });
    expect(recoveryRequest).not.toHaveProperty("cursor");
  });

  it("depth-1 Local cursor invalid 后从第一页恢复且不复用旧 cursor", async () => {
    let neighborhoodCallCount = 0;
    let nodeDetailCallCount = 0;
    const fetchMock = vi.fn<typeof fetch>((input) => {
      const url = requestUrl(input);
      if (url.pathname === `/api/v1/graph/nodes/CLAIM/${claimId}`) {
        nodeDetailCallCount += 1;
        return nodeDetailCallCount === 1
          ? Promise.resolve(jsonResponse(claimNode))
          : Promise.resolve(jsonResponse({ error_code: "GRAPH_NODE_NOT_FOUND", message: "graph node changed", retryable: false }, 404));
      }
      if (!url.pathname.endsWith("/neighborhood")) return Promise.reject(new Error(`Unexpected Graph request: ${url.pathname}`));
      neighborhoodCallCount += 1;
      if (neighborhoodCallCount === 1) return Promise.resolve(jsonResponse({
        ...neighborhoodResponse,
        meta: { fingerprint, next_cursor: "neighborhood-next", complete: false, truncated: false },
      }));
      if (neighborhoodCallCount === 2) return Promise.resolve(jsonResponse({
        error_code: "GRAPH_CURSOR_INVALID",
        message: "graph cursor expired",
        retryable: false,
      }, 422));
      return Promise.resolve(jsonResponse(neighborhoodResponse));
    });
    vi.stubGlobal("fetch", fetchMock);

    const { queryClient } = renderPage(`/graph?mode=local&center_type=TOPIC&center_id=${topicId}&depth=1&direction=BOTH`);

    fireEvent.click(await screen.findByRole("button", { name: "选择主张：Graph 只投影 Knowledge 中的正式事实。" }));
    fireEvent.click(await screen.findByRole("button", { name: "锁定节点位置" }));

    fireEvent.click(await screen.findByRole("button", { name: "加载下一页" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("分页状态已失效");
    fireEvent.click(screen.getByRole("button", { name: "从第一页重新加载" }));

    await waitFor(() => expect(neighborhoodCallCount).toBe(3));
    expect(screen.getByText("选择图中的节点或关系，查看服务端事实与证据。")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "已锁定，选择主张：Graph 只投影 Knowledge 中的正式事实。" })).not.toBeInTheDocument();
    await waitFor(() => expect(queryClient.getQueryState(graphQueryKeys.node({ workspaceId, nodeType: "CLAIM", nodeId: claimId }))).toBeUndefined());
    fireEvent.click(await screen.findByRole("button", { name: "选择主张：Graph 只投影 Knowledge 中的正式事实。" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("GRAPH_NODE_NOT_FOUND");
    expect(screen.queryByRole("heading", { name: "Graph 只投影 Knowledge 中的正式事实。" })).not.toBeInTheDocument();
    const neighborhoodCalls = fetchMock.mock.calls.filter(([input]) => requestUrl(input).pathname.endsWith("/neighborhood"));
    const recoveryBody = neighborhoodCalls[2]?.[1]?.body;
    if (typeof recoveryBody !== "string") throw new Error("Graph neighborhood recovery request body is missing");
    const recoveryRequest = JSON.parse(recoveryBody) as Record<string, unknown>;
    expect(recoveryRequest).toMatchObject({ workspace_id: workspaceId, depth: 1, limit: 25 });
    expect(recoveryRequest).not.toHaveProperty("cursor");
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

  it("候选 scan URL 更新不清除正式 Graph 详情、锁定位置或固定布局", async () => {
    const fetchMock = vi.fn<typeof fetch>((input) => {
      const url = requestUrl(input);
      if (url.pathname.endsWith("/neighborhood")) return Promise.resolve(jsonResponse(neighborhoodResponse));
      if (url.pathname === `/api/v1/graph/nodes/TOPIC/${topicId}`) return Promise.resolve(jsonResponse(topicNode));
      if (url.pathname.endsWith("/candidate-scans")) return Promise.resolve(jsonResponse({
        scan_id: candidateScanId,
        workflow_run_id: candidateScanWorkflowRunId,
        status: "PENDING",
        version: 1,
        status_url: candidateScanStatusUrl,
      }, 202));
      if (url.pathname.endsWith(`/candidate-scans/${candidateScanId}`)) return Promise.resolve(jsonResponse({
        id: candidateScanId,
        workspace_id: workspaceId,
        scope: { kind: "TOPIC", topic_id: topicId },
        status: "RUNNING",
        workflow_run_id: candidateScanWorkflowRunId,
        version: 2,
        status_url: candidateScanStatusUrl,
        total_count: 2,
        processed_count: 1,
        candidate_count: 0,
        ignored_count: 0,
        failed_count: 0,
        last_error: null,
        created_at: timestamp,
        updated_at: timestamp,
        completed_at: null,
      }));
      if (url.pathname.endsWith("/candidates")) return Promise.resolve(jsonResponse({ workspace_id: workspaceId, items: [] }));
      return Promise.reject(new Error(`Unexpected Graph request: ${url.pathname}`));
    });
    vi.stubGlobal("fetch", fetchMock);

    renderPage(`/graph?mode=local&center_type=TOPIC&center_id=${topicId}&depth=1&direction=BOTH`);

    fireEvent.click(await screen.findByRole("button", { name: "选择主题：Graph Projection" }));
    fireEvent.click(await screen.findByRole("button", { name: "锁定节点位置" }));
    fireEvent.click(screen.getByRole("button", { name: "固定当前布局" }));
    expect(screen.getByRole("button", { name: "释放固定布局" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "已锁定，选择主题：Graph Projection" })).toHaveAttribute("data-locked", "true");

    fireEvent.click(screen.getByRole("button", { name: "扫描当前 Topic" }));
    expect(await screen.findByText("Topic 扫描 · RUNNING")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "释放固定布局" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "已锁定，选择主题：Graph Projection" })).toHaveAttribute("data-locked", "true");
    expect(screen.getByRole("heading", { name: "Graph Projection" })).toBeInTheDocument();
  });

  it("结果进入完整列表 fallback 时清除锁定和固定布局", async () => {
    const clusters = Array.from({ length: 61 }, (_, offset) => {
      const index = offset + 1;
      const id = `94000000-0000-4000-8000-${String(index).padStart(12, "0")}`;
      const topic = { ...topicNode, id, name: `Paged Topic ${String(index)}` };
      return {
        topic,
        direct_claim_count: 0,
        incident_relation_count: 0,
        cluster_score: 0,
        updated_at: timestamp,
      };
    });
    const pages = [clusters.slice(0, 25), clusters.slice(25, 50), clusters.slice(50)];
    const fetchMock = vi.fn<typeof fetch>((input, init) => {
      const url = requestUrl(input);
      if (!url.pathname.endsWith("/global")) return Promise.reject(new Error(`Unexpected Graph request: ${url.pathname}`));
      const body = typeof init?.body === "string" ? JSON.parse(init.body) as { cursor?: string } : {};
      const pageIndex = body.cursor === "global-page-2" ? 1 : body.cursor === "global-page-3" ? 2 : 0;
      const nextCursor = pageIndex === 0 ? "global-page-2" : pageIndex === 1 ? "global-page-3" : undefined;
      return Promise.resolve(jsonResponse({
        workspace_id: workspaceId,
        clusters: pages[pageIndex],
        meta: {
          fingerprint: `${String(pageIndex)}${"a".repeat(63)}`,
          ...(nextCursor === undefined ? {} : { next_cursor: nextCursor }),
          complete: nextCursor === undefined,
          truncated: false,
        },
      }));
    });
    vi.stubGlobal("fetch", fetchMock);

    const view = renderPage();
    expect(await screen.findByRole("button", { name: "选择主题：Paged Topic 1" })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "固定当前布局" }));
    expect(screen.getByRole("button", { name: "释放固定布局" })).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "加载下一页" }));
    expect(await screen.findByText("50 nodes · 0 edges")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "加载下一页" }));
    expect(await screen.findByText("节点超过画布上限（60），已切换到完整列表。")).toBeInTheDocument();
    await waitFor(() => expect(view.container.querySelectorAll('[data-locked="true"]')).toHaveLength(0));
    expect(screen.queryByRole("button", { name: "释放固定布局" })).not.toBeInTheDocument();
  });

  it("切换 Workspace 清除详情和锁定布局且不以新 Workspace 查询旧 selection", async () => {
    const fetchMock = vi.fn<typeof fetch>((input, init) => {
      const url = requestUrl(input);
      if (url.pathname.endsWith("/neighborhood")) {
        const body = init?.body;
        if (typeof body !== "string") return Promise.reject(new Error("Graph neighborhood body is missing"));
        const request = JSON.parse(body) as { workspace_id?: string };
        if (request.workspace_id === workspaceId) return Promise.resolve(jsonResponse(neighborhoodResponse));
        return Promise.resolve(jsonResponse({
          error_code: "GRAPH_NODE_NOT_FOUND",
          message: "graph center is not visible",
          retryable: false,
        }, 404));
      }
      if (url.pathname === `/api/v1/graph/nodes/CLAIM/${claimId}`) {
        if (url.searchParams.get("workspace_id") === workspaceId) return Promise.resolve(jsonResponse(claimNode));
        return Promise.resolve(jsonResponse({
          error_code: "GRAPH_NODE_NOT_FOUND",
          message: "graph node is not visible",
          retryable: false,
        }, 404));
      }
      return Promise.reject(new Error(`Unexpected Graph request: ${url.pathname}`));
    });
    vi.stubGlobal("fetch", fetchMock);

    const { queryClient } = renderPage(`/graph?mode=local&center_type=TOPIC&center_id=${topicId}&depth=1&direction=BOTH`);

    fireEvent.click(await screen.findByRole("button", { name: "选择主张：Graph 只投影 Knowledge 中的正式事实。" }));
    fireEvent.click(await screen.findByRole("button", { name: "锁定节点位置" }));
    fireEvent.click(screen.getByRole("button", { name: "固定当前布局" }));
    expect(screen.getByRole("button", { name: "释放固定布局" })).toBeInTheDocument();
    expect(queryClient.getQueryCache().findAll({ queryKey: graphQueryKeys.all(workspaceId) }).length).toBeGreaterThan(0);

    act(() => setActiveWorkspaceId(otherWorkspaceId));

    await waitFor(() => expect(screen.getByText(otherWorkspaceId)).toBeInTheDocument());
    expect(screen.getByText("选择图中的节点或关系，查看服务端事实与证据。")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "释放固定布局" })).not.toBeInTheDocument();
    await waitFor(() => expect(fetchMock.mock.calls.some(([, init]) => {
      if (typeof init?.body !== "string") return false;
      return (JSON.parse(init.body) as { workspace_id?: string }).workspace_id === otherWorkspaceId;
    })).toBe(true));
    expect(fetchMock.mock.calls.some(([input]) => {
      const url = requestUrl(input);
      return url.pathname === `/api/v1/graph/nodes/CLAIM/${claimId}`
        && url.searchParams.get("workspace_id") === otherWorkspaceId;
    })).toBe(false);
    await waitFor(() => expect(queryClient.getQueryCache().findAll({ queryKey: graphQueryKeys.all(workspaceId) })).toHaveLength(0));
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

  it("收起后重开同一 Relation 时不回放旧 Evidence", async () => {
    let evidenceCallCount = 0;
    let resolveReopenedEvidence: ((response: Response) => void) | undefined;
    const refreshedEvidenceResponse = {
      ...evidenceResponse,
      items: [{ ...evidenceResponse.items[0], reason: "刷新后的关系证据。" }],
    };
    const fetchMock = vi.fn<typeof fetch>((input) => {
      const url = requestUrl(input);
      if (url.pathname.endsWith("/neighborhood")) return Promise.resolve(jsonResponse(neighborhoodResponse));
      if (url.pathname === `/api/v1/graph/relations/${relationId}`) return Promise.resolve(jsonResponse(relationDetailResponse));
      if (!url.pathname.endsWith("/evidence")) return Promise.reject(new Error(`Unexpected Graph request: ${url.pathname}`));
      evidenceCallCount += 1;
      if (evidenceCallCount === 1) return Promise.resolve(jsonResponse(evidenceResponse));
      return new Promise<Response>((resolve) => { resolveReopenedEvidence = resolve; });
    });
    vi.stubGlobal("fetch", fetchMock);

    renderPage(`/graph?mode=local&center_type=TOPIC&center_id=${topicId}&depth=1&direction=BOTH`);

    fireEvent.click(await screen.findByRole("button", { name: "选择关系：归属于，已确认" }));
    fireEvent.click(await screen.findByRole("button", { name: "加载关系证据" }));
    expect(await screen.findByText("该来源片段直接支持当前关系。")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "收起关系证据" }));
    fireEvent.click(screen.getByRole("button", { name: "加载关系证据" }));

    expect(screen.queryByText("该来源片段直接支持当前关系。")).not.toBeInTheDocument();
    expect(screen.getByRole("status", { name: "正在加载关系证据" })).toBeInTheDocument();
    await act(async () => {
      resolveReopenedEvidence?.(jsonResponse(refreshedEvidenceResponse));
      await Promise.resolve();
    });
    expect(await screen.findByText("刷新后的关系证据。")).toBeInTheDocument();
    expect(evidenceCallCount).toBe(2);
  });

  it("切换关系时不会绕过 Evidence 的显式展开", async () => {
    const fetchMock = vi.fn<typeof fetch>((input) => {
      const url = requestUrl(input);
      if (url.pathname.endsWith("/neighborhood")) return Promise.resolve(jsonResponse(twoRelationNeighborhoodResponse));
      if (url.pathname === `/api/v1/graph/relations/${relationId}`) return Promise.resolve(jsonResponse(relationDetailResponse));
      if (url.pathname === `/api/v1/graph/relations/${secondRelationId}`) return Promise.resolve(jsonResponse(secondRelationDetailResponse));
      if (url.pathname === `/api/v1/graph/relations/${relationId}/evidence`) return Promise.resolve(jsonResponse(evidenceResponse));
      if (url.pathname === `/api/v1/graph/relations/${secondRelationId}/evidence`) return Promise.resolve(jsonResponse(secondEvidenceResponse));
      return Promise.reject(new Error(`Unexpected Graph request: ${url.pathname}`));
    });
    vi.stubGlobal("fetch", fetchMock);

    renderPage(`/graph?mode=local&center_type=TOPIC&center_id=${topicId}&depth=1&direction=BOTH`);

    fireEvent.click(await screen.findByRole("button", { name: "选择关系：归属于，已确认" }));
    fireEvent.click(await screen.findByRole("button", { name: "加载关系证据" }));
    expect(await screen.findByText("该来源片段直接支持当前关系。")).toBeInTheDocument();

    fireEvent.click(await screen.findByRole("button", { name: "选择关系：支持，已确认" }));
    await screen.findByRole("heading", { name: "SUPPORTS" });
    expect(fetchMock.mock.calls.filter(([input]) => requestUrl(input).pathname === `/api/v1/graph/relations/${secondRelationId}/evidence`)).toHaveLength(0);

    fireEvent.click(await screen.findByRole("button", { name: "加载关系证据" }));
    expect(await screen.findByText("第二条关系的证据只应在显式展开后加载。")).toBeInTheDocument();
  });

  it("Evidence 截断时展示不完整状态和原因", async () => {
    const fetchMock = vi.fn<typeof fetch>((input) => {
      const url = requestUrl(input);
      if (url.pathname.endsWith("/neighborhood")) return Promise.resolve(jsonResponse(neighborhoodResponse));
      if (url.pathname === `/api/v1/graph/relations/${relationId}`) return Promise.resolve(jsonResponse(relationDetailResponse));
      if (url.pathname.endsWith("/evidence")) return Promise.resolve(jsonResponse({
        ...evidenceResponse,
        meta: { fingerprint, complete: false, truncated: true, reason: "RESULT_WINDOW_LIMIT" },
      }));
      return Promise.reject(new Error(`Unexpected Graph request: ${url.pathname}`));
    });
    vi.stubGlobal("fetch", fetchMock);

    renderPage(`/graph?mode=local&center_type=TOPIC&center_id=${topicId}&depth=1&direction=BOTH`);

    fireEvent.click(await screen.findByRole("button", { name: "选择关系：归属于，已确认" }));
    fireEvent.click(await screen.findByRole("button", { name: "加载关系证据" }));
    expect(await screen.findByRole("status", { name: "关系证据状态" })).toHaveTextContent("证据结果已截断");
    expect(screen.getByRole("status", { name: "关系证据状态" })).toHaveTextContent("RESULT_WINDOW_LIMIT");
  });

  it("Relation Evidence cursor stale 后从第一页恢复且不复用旧 cursor", async () => {
    let evidenceCallCount = 0;
    let relationDetailCallCount = 0;
    const fetchMock = vi.fn<typeof fetch>((input) => {
      const url = requestUrl(input);
      if (url.pathname.endsWith("/neighborhood")) return Promise.resolve(jsonResponse(neighborhoodResponse));
      if (url.pathname === `/api/v1/graph/relations/${relationId}`) {
        relationDetailCallCount += 1;
        return Promise.resolve(jsonResponse(relationDetailCallCount === 1 ? relationDetailResponse : {
          ...relationDetailResponse,
          edge: { ...edge, evidence_count: 2, evidence_fingerprint: "3".repeat(64) },
          fingerprint: "4".repeat(64),
        }));
      }
      if (!url.pathname.endsWith("/evidence")) return Promise.reject(new Error(`Unexpected Graph request: ${url.pathname}`));
      evidenceCallCount += 1;
      if (evidenceCallCount === 1) {
        return Promise.resolve(jsonResponse({
          ...evidenceResponse,
          meta: { fingerprint, next_cursor: "evidence-next", complete: false, truncated: false },
        }));
      }
      if (evidenceCallCount === 2) {
        return Promise.resolve(jsonResponse({
          error_code: "GRAPH_CURSOR_STALE",
          message: "relation evidence changed",
          retryable: false,
        }, 409));
      }
      return Promise.resolve(jsonResponse(evidenceResponse));
    });
    vi.stubGlobal("fetch", fetchMock);

    renderPage(`/graph?mode=local&center_type=TOPIC&center_id=${topicId}&depth=1&direction=BOTH`);

    fireEvent.click(await screen.findByRole("button", { name: "选择关系：归属于，已确认" }));
    fireEvent.click(await screen.findByRole("button", { name: "加载关系证据" }));
    expect(await screen.findByText("该来源片段直接支持当前关系。")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "加载更多证据" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("图谱结果已变化");
    fireEvent.click(screen.getByRole("button", { name: "从第一页重新加载" }));

    await waitFor(() => expect(evidenceCallCount).toBe(3));
    await waitFor(() => expect(relationDetailCallCount).toBe(2));
    expect(screen.getByText("2", { selector: "dd" })).toBeInTheDocument();
    const evidenceCalls = fetchMock.mock.calls.filter(([input]) => requestUrl(input).pathname.endsWith("/evidence"));
    expect(evidenceCalls).toHaveLength(3);
    expect(requestUrl(evidenceCalls[1]?.[0]).searchParams.get("cursor")).toBe("evidence-next");
    expect(requestUrl(evidenceCalls[2]?.[0]).searchParams.get("cursor")).toBeNull();
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

  it("列表 fallback 的移动 Claim drawer 允许键盘访问适用条件", async () => {
    installCompactViewport();
    const extraTopics = Array.from({ length: 59 }, (_, offset) => ({
      ...topicNode,
      id: `95000000-0000-4000-8000-${String(offset + 1).padStart(12, "0")}`,
      name: `Fallback Topic ${String(offset + 1)}`,
    }));
    const largeNeighborhood = {
      ...neighborhoodResponse,
      nodes: [claimNode, topicNode, ...extraTopics],
      edges: [],
      layer_counts: [60],
    };
    const fetchMock = vi.fn<typeof fetch>((input) => {
      const url = requestUrl(input);
      if (url.pathname.endsWith("/neighborhood")) return Promise.resolve(jsonResponse(largeNeighborhood));
      if (url.pathname === `/api/v1/graph/nodes/CLAIM/${claimId}`) return Promise.resolve(jsonResponse(claimNode));
      return Promise.reject(new Error(`Unexpected Graph request: ${url.pathname}`));
    });
    vi.stubGlobal("fetch", fetchMock);

    renderPage(`/graph?mode=local&center_type=TOPIC&center_id=${topicId}&depth=1&direction=BOTH`);

    fireEvent.click(await screen.findByRole("button", { name: "选择主张：Graph 只投影 Knowledge 中的正式事实。" }));
    const dialog = await screen.findByRole("dialog", { name: "图谱详情" });
    await waitFor(() => expect(dialog).toHaveFocus());
    const summary = await screen.findByText("适用条件");
    expect(screen.queryByRole("button", { name: "锁定节点位置" })).not.toBeInTheDocument();

    fireEvent.keyDown(dialog, { key: "Tab" });
    expect(screen.getByRole("button", { name: "关闭详情" })).toHaveFocus();
    fireEvent.keyDown(dialog, { key: "Tab", shiftKey: true });
    expect(summary).toHaveFocus();
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
