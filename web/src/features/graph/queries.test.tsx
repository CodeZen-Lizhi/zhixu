import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import type { PropsWithChildren } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";

import {
  clearGraphRelationEvidence,
  clearGraphWorkspaceDetails,
  useGraphGlobal,
  useGraphNeighborhood,
  useGraphNodeDetail,
  useGraphNodeSearch,
  useGraphPath,
  useGraphRelationDetail,
  useGraphRelationEvidence,
} from "./queries";
import { graphQueryKeys } from "./query-keys";

const workspaceId = "92000000-0000-4000-8000-000000000001";
const topicId = "92000000-0000-4000-8000-000000000002";
const claimId = "92000000-0000-4000-8000-000000000003";
const relationId = "92000000-0000-4000-8000-000000000004";
const firstEvidenceId = "92000000-0000-4000-8000-000000000005";
const secondEvidenceId = "92000000-0000-4000-8000-000000000006";
const firstSourceVersionId = "92000000-0000-4000-8000-000000000007";
const secondSourceVersionId = "92000000-0000-4000-8000-000000000008";
const firstSourceSpanId = "92000000-0000-4000-8000-000000000009";
const secondSourceSpanId = "92000000-0000-4000-8000-00000000000a";
const updatedAt = "2026-07-20T09:30:00Z";

const topicNode = {
  type: "TOPIC",
  id: topicId,
  workspace_id: workspaceId,
  name: "Graph 查询",
  description: "Graph 查询主题",
  topic_status: "ACTIVE",
  version: 1,
  updated_at: updatedAt,
};

const claimNode = {
  type: "CLAIM",
  id: claimId,
  workspace_id: workspaceId,
  statement: "Graph 是 Knowledge 的只读查询投影。",
  claim_status: "CONFIRMED",
  confidence: 0.9,
  applicability: {
    schema_version: "knowledge-applicability/v1",
    value: {},
    hash: "a".repeat(64),
  },
  version: 2,
  updated_at: updatedAt,
};

const edge = {
  relation_id: relationId,
  workspace_id: workspaceId,
  source: { type: "CLAIM", id: claimId },
  target: { type: "TOPIC", id: topicId },
  type: "BELONGS_TO",
  status: "CONFIRMED",
  traversal: "REVERSE",
  confidence: 0.8,
  version: 3,
  evidence_count: 0,
  evidence_fingerprint: "",
  evidence_href: `/api/v1/graph/relations/${relationId}/evidence?workspace_id=${workspaceId}`,
  updated_at: updatedAt,
};

const completeMeta = (fingerprint: string) => ({
  fingerprint,
  complete: true,
  truncated: false,
});

const nextMeta = (fingerprint: string, cursor: string) => ({
  fingerprint,
  next_cursor: cursor,
  complete: false,
  truncated: false,
});

const globalPage = (meta: ReturnType<typeof completeMeta> | ReturnType<typeof nextMeta>) => ({
  workspace_id: workspaceId,
  clusters: [{
    topic: topicNode,
    direct_claim_count: 1,
    incident_relation_count: 1,
    cluster_score: 2,
    updated_at: updatedAt,
  }],
  meta,
});

const neighborhoodPage = (
  depth: 1 | 2 | 3,
  meta: ReturnType<typeof completeMeta> | ReturnType<typeof nextMeta>,
) => ({
  workspace_id: workspaceId,
  center: { type: "TOPIC", id: topicId },
  nodes: depth === 1 ? [claimNode, topicNode] : [topicNode],
  edges: depth === 1 ? [edge] : [],
  boundary_nodes: [],
  layer_counts: depth === 1 ? [1] : Array.from({ length: depth }, () => 0),
  completed_depth: depth,
  meta,
});

const pathResponse = {
  workspace_id: workspaceId,
  from: { type: "TOPIC", id: topicId },
  to: { type: "CLAIM", id: claimId },
  status: "found",
  nodes: [topicNode, claimNode],
  edges: [edge],
  hop_count: 1,
  explored_nodes: 2,
  common_topic_suggestions: [],
};

const relationDetailResponse = {
  edge,
  confirmation: null,
  fingerprint: "c".repeat(64),
  valid_from: null,
  valid_to: null,
  created_at: "2026-07-20T09:00:00Z",
  updated_at: updatedAt,
};

const evidenceItem = (
  id: string,
  sourceVersionId: string,
  sourceSpanId: string,
  createdAt: string,
) => ({
  id,
  workspace_id: workspaceId,
  relation_id: relationId,
  provenance: {
    workspace_id: workspaceId,
    source_version_id: sourceVersionId,
    source_span_id: sourceSpanId,
  },
  reason: "该来源片段支持当前关系。",
  applicability: {
    schema_version: "knowledge-applicability/v1",
    value: {},
    hash: "a".repeat(64),
  },
  confirmation: null,
  source_href: `/api/v1/workspaces/${workspaceId}/source-versions/${sourceVersionId}`,
  span_href: `/api/v1/workspaces/${workspaceId}/source-versions/${sourceVersionId}/spans/${sourceSpanId}`,
  created_at: createdAt,
});

const jsonResponse = (body: unknown, status = 200) => new Response(JSON.stringify(body), {
  status,
  headers: { "Content-Type": "application/json" },
});

const requestUrl = (input: RequestInfo | URL | undefined): URL => {
  if (input === undefined) throw new Error("Graph fetch input is missing");
  const address = typeof input === "string"
    ? input
    : input instanceof URL
      ? input.href
      : input.url;
  return new URL(address, window.location.origin);
};

const createWrapper = () => {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { gcTime: 0 } },
  });
  return ({ children }: PropsWithChildren) => (
    <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
  );
};

afterEach(() => vi.unstubAllGlobals());

describe("Graph query hooks", () => {
  it("按 Relation 清理全部 Evidence 分页缓存且保留 Relation detail", () => {
    const queryClient = new QueryClient();
    queryClient.setQueryData(graphQueryKeys.relation({ workspaceId, relationId }), { detail: true });
    queryClient.setQueryData(graphQueryKeys.evidence({ workspaceId, relationId, limit: 20 }), { pageSize: 20 });
    queryClient.setQueryData(graphQueryKeys.evidence({ workspaceId, relationId, limit: 50 }), { pageSize: 50 });

    clearGraphRelationEvidence(queryClient, workspaceId, relationId);

    expect(queryClient.getQueryData(graphQueryKeys.relation({ workspaceId, relationId }))).toEqual({ detail: true });
    expect(queryClient.getQueryData(graphQueryKeys.evidence({ workspaceId, relationId, limit: 20 }))).toBeUndefined();
    expect(queryClient.getQueryData(graphQueryKeys.evidence({ workspaceId, relationId, limit: 50 }))).toBeUndefined();
  });

  it("恢复列表时只清理详情与 Evidence，不删除 Node Search 缓存", () => {
    const queryClient = new QueryClient();
    queryClient.setQueryData(graphQueryKeys.node({ workspaceId, nodeType: "CLAIM", nodeId: claimId }), { detail: true });
    queryClient.setQueryData(graphQueryKeys.evidence({ workspaceId, relationId, limit: 20 }), { evidence: true });
    queryClient.setQueryData(graphQueryKeys.nodeSearch({ workspaceId, query: "Graph", limit: 20 }), { matches: true });

    clearGraphWorkspaceDetails(queryClient, workspaceId);

    expect(queryClient.getQueryData(graphQueryKeys.node({ workspaceId, nodeType: "CLAIM", nodeId: claimId }))).toBeUndefined();
    expect(queryClient.getQueryData(graphQueryKeys.evidence({ workspaceId, relationId, limit: 20 }))).toBeUndefined();
    expect(queryClient.getQueryData(graphQueryKeys.nodeSearch({ workspaceId, query: "Graph", limit: 20 }))).toEqual({ matches: true });
  });

  it("Global 使用服务端 cursor 加载下一页", async () => {
    const fingerprint = "b".repeat(64);
    const fetchMock = vi.fn<typeof fetch>()
      .mockResolvedValueOnce(jsonResponse(globalPage(nextMeta(fingerprint, "global-next"))))
      .mockResolvedValueOnce(jsonResponse(globalPage(completeMeta(fingerprint))));
    vi.stubGlobal("fetch", fetchMock);

    const { result } = renderHook(() => useGraphGlobal({ workspaceId, limit: 1 }), {
      wrapper: createWrapper(),
    });

    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.hasNextPage).toBe(true);
    await act(async () => {
      await result.current.fetchNextPage();
    });
    await waitFor(() => expect(result.current.data?.pages).toHaveLength(2));

    expect(fetchMock).toHaveBeenCalledTimes(2);
    const firstBody = fetchMock.mock.calls[0]?.[1]?.body;
    const secondBody = fetchMock.mock.calls[1]?.[1]?.body;
    if (typeof firstBody !== "string" || typeof secondBody !== "string") {
      throw new Error("Graph global request body is missing");
    }
    expect(JSON.parse(firstBody)).toEqual({ workspace_id: workspaceId, limit: 1 });
    expect(JSON.parse(secondBody)).toEqual({ workspace_id: workspaceId, cursor: "global-next", limit: 1 });
  });

  it("depth=1 Neighborhood 使用服务端 cursor 加载下一页", async () => {
    const fingerprint = "d".repeat(64);
    const fetchMock = vi.fn<typeof fetch>()
      .mockResolvedValueOnce(jsonResponse(neighborhoodPage(1, nextMeta(fingerprint, "neighbor-next"))))
      .mockResolvedValueOnce(jsonResponse(neighborhoodPage(1, completeMeta(fingerprint))));
    vi.stubGlobal("fetch", fetchMock);

    const { result } = renderHook(() => useGraphNeighborhood({
      workspaceId,
      center: { type: "TOPIC", id: topicId },
      depth: 1,
      limit: 1,
    }), { wrapper: createWrapper() });

    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.hasNextPage).toBe(true);
    await act(async () => {
      await result.current.fetchNextPage();
    });
    expect(result.current.error).toBeNull();
    await waitFor(() => expect(result.current.data?.pages).toHaveLength(2));

    const secondBody = fetchMock.mock.calls[1]?.[1]?.body;
    if (typeof secondBody !== "string") throw new Error("Graph neighborhood request body is missing");
    expect(JSON.parse(secondBody)).toMatchObject({
      workspace_id: workspaceId,
      center: { type: "TOPIC", id: topicId },
      depth: 1,
      cursor: "neighbor-next",
      limit: 1,
    });
  });

  it.each([2, 3] as const)("depth=%i Neighborhood 不产生下一页请求", async (depth) => {
    const fetchMock = vi.fn<typeof fetch>().mockResolvedValue(jsonResponse(
      neighborhoodPage(depth, completeMeta("e".repeat(64))),
    ));
    vi.stubGlobal("fetch", fetchMock);

    const { result } = renderHook(() => useGraphNeighborhood({
      workspaceId,
      center: { type: "TOPIC", id: topicId },
      depth,
    }), { wrapper: createWrapper() });

    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.hasNextPage).toBe(false);
    await act(async () => {
      await result.current.fetchNextPage();
    });
    expect(fetchMock).toHaveBeenCalledOnce();
  });

  it("Relation 详情不预取 Evidence，打开后才按 cursor 分页", async () => {
    const fingerprint = "f".repeat(64);
    let evidencePageNumber = 0;
    const fetchMock = vi.fn<typeof fetch>((input) => {
      const url = requestUrl(input);
      if (!url.pathname.endsWith("/evidence")) {
        return Promise.resolve(jsonResponse(relationDetailResponse));
      }
      evidencePageNumber += 1;
      if (evidencePageNumber === 1) {
        return Promise.resolve(jsonResponse({
          workspace_id: workspaceId,
          relation_id: relationId,
          items: [evidenceItem(firstEvidenceId, firstSourceVersionId, firstSourceSpanId, "2026-07-20T09:10:00Z")],
          meta: nextMeta(fingerprint, "evidence-next"),
        }));
      }
      return Promise.resolve(jsonResponse({
        workspace_id: workspaceId,
        relation_id: relationId,
        items: [evidenceItem(secondEvidenceId, secondSourceVersionId, secondSourceSpanId, "2026-07-20T09:20:00Z")],
        meta: completeMeta(fingerprint),
      }));
    });
    vi.stubGlobal("fetch", fetchMock);
    const wrapper = createWrapper();

    const detail = renderHook(() => useGraphRelationDetail({ workspaceId, relationId }), { wrapper });
    await waitFor(() => expect(detail.result.current.isSuccess).toBe(true));
    expect(fetchMock).toHaveBeenCalledOnce();
    expect(requestUrl(fetchMock.mock.calls[0]?.[0]).pathname).not.toContain("/evidence");

    const evidence = renderHook(() => useGraphRelationEvidence({ workspaceId, relationId, limit: 1 }), { wrapper });
    await waitFor(() => expect(evidence.result.current.isSuccess).toBe(true));
    expect(evidence.result.current.hasNextPage).toBe(true);
    await act(async () => {
      await evidence.result.current.fetchNextPage();
    });
    await waitFor(() => expect(evidence.result.current.data?.pages).toHaveLength(2));

    const evidenceCalls = fetchMock.mock.calls.filter(([input]) => requestUrl(input).pathname.endsWith("/evidence"));
    expect(evidenceCalls).toHaveLength(2);
    const firstUrl = requestUrl(evidenceCalls[0]?.[0]);
    const secondUrl = requestUrl(evidenceCalls[1]?.[0]);
    expect(firstUrl.searchParams.get("cursor")).toBeNull();
    expect(secondUrl.searchParams.get("cursor")).toBe("evidence-next");
  });

  it("Node Search 对无效输入保持 idle，对有效输入发起真实 client 请求", async () => {
    const fetchMock = vi.fn<typeof fetch>().mockResolvedValue(jsonResponse({
      workspace_id: workspaceId,
      matches: [{ kind: "EXACT", node: topicNode }],
    }));
    vi.stubGlobal("fetch", fetchMock);

    const { result, rerender } = renderHook(
      ({ query }: { query: string }) => useGraphNodeSearch({ workspaceId, query, limit: 10 }),
      { initialProps: { query: "x" }, wrapper: createWrapper() },
    );

    expect(result.current.fetchStatus).toBe("idle");
    expect(fetchMock).not.toHaveBeenCalled();

    rerender({ query: "图" });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(fetchMock).toHaveBeenCalledOnce();
    const url = requestUrl(fetchMock.mock.calls[0]?.[0]);
    expect(url.pathname).toBe("/api/v1/graph/nodes");
    expect(Object.fromEntries(url.searchParams)).toEqual({
      workspace_id: workspaceId,
      query: "图",
      limit: "10",
    });
  });

  it("Path、Node detail 和 Relation detail 通过各自公共查询执行", async () => {
    const fetchMock = vi.fn<typeof fetch>((input) => {
      const url = requestUrl(input);
      if (url.pathname === "/api/v1/graph/path") return Promise.resolve(jsonResponse(pathResponse));
      if (url.pathname.includes("/graph/nodes/")) return Promise.resolve(jsonResponse(topicNode));
      if (url.pathname === `/api/v1/graph/relations/${relationId}`) {
        return Promise.resolve(jsonResponse(relationDetailResponse));
      }
      return Promise.reject(new Error(`Unexpected Graph query: ${url.pathname}`));
    });
    vi.stubGlobal("fetch", fetchMock);
    const wrapper = createWrapper();

    const path = renderHook(() => useGraphPath({
      workspaceId,
      from: { type: "TOPIC", id: topicId },
      to: { type: "CLAIM", id: claimId },
      direction: "BOTH",
      relationTypes: ["BELONGS_TO"],
      maxDepth: 3,
      maxVisited: 30,
    }), { wrapper });
    const node = renderHook(() => useGraphNodeDetail({
      workspaceId,
      nodeType: "TOPIC",
      nodeId: topicId,
    }), { wrapper });
    const relation = renderHook(() => useGraphRelationDetail({ workspaceId, relationId }), { wrapper });

    await waitFor(() => expect(
      path.result.current.isSuccess
      && node.result.current.isSuccess
      && relation.result.current.isSuccess,
    ).toBe(true));
    expect(fetchMock).toHaveBeenCalledTimes(3);

    const urls = fetchMock.mock.calls.map(([input]) => requestUrl(input));
    expect(urls.map((url) => url.pathname)).toEqual(expect.arrayContaining([
      "/api/v1/graph/path",
      `/api/v1/graph/nodes/TOPIC/${topicId}`,
      `/api/v1/graph/relations/${relationId}`,
    ]));
    const pathCall = fetchMock.mock.calls.find(([input]) => requestUrl(input).pathname === "/api/v1/graph/path");
    const pathBody = pathCall?.[1]?.body;
    if (typeof pathBody !== "string") throw new Error("Graph path request body is missing");
    expect(JSON.parse(pathBody)).toEqual({
      workspace_id: workspaceId,
      from: { type: "TOPIC", id: topicId },
      to: { type: "CLAIM", id: claimId },
      direction: "BOTH",
      relation_types: ["BELONGS_TO"],
      max_depth: 3,
      max_visited: 30,
    });
  });

  it("卸载查询会把 Abort signal 传到 fetch 边界", async () => {
    let receivedSignal: AbortSignal | null | undefined;
    const fetchMock = vi.fn<typeof fetch>((_input, init) => {
      receivedSignal = init?.signal;
      return new Promise<Response>((_resolve, reject) => {
        receivedSignal?.addEventListener("abort", () => {
          reject(new DOMException("Graph query aborted", "AbortError"));
        }, { once: true });
      });
    });
    vi.stubGlobal("fetch", fetchMock);

    const { unmount } = renderHook(() => useGraphGlobal({ workspaceId }), {
      wrapper: createWrapper(),
    });
    await waitFor(() => expect(fetchMock).toHaveBeenCalledOnce());
    expect(receivedSignal).toBeInstanceOf(AbortSignal);

    unmount();
    await waitFor(() => expect(receivedSignal?.aborted).toBe(true));
  });

  it("Graph 查询失败时 retry=false 不重复请求", async () => {
    const fetchMock = vi.fn<typeof fetch>().mockRejectedValue(new Error("network unavailable"));
    vi.stubGlobal("fetch", fetchMock);

    const { result } = renderHook(() => useGraphNodeDetail({
      workspaceId,
      nodeType: "TOPIC",
      nodeId: topicId,
    }), { wrapper: createWrapper() });

    await waitFor(() => expect(result.current.isError).toBe(true));
    expect(fetchMock).toHaveBeenCalledOnce();
  });
});
