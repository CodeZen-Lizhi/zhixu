import { afterEach, describe, expect, it, vi } from "vitest";

import { decodeSearchResponse, search, SearchApiError } from "./search";

const workspaceId = "92000000-0000-4000-8000-000000000001";
const indexVersionId = "92000000-0000-4000-8000-000000000002";
const embeddingVersionId = "92000000-0000-4000-8000-000000000003";
const chunkId = "92000000-0000-4000-8000-000000000004";
const projectionId = "92000000-0000-4000-8000-000000000005";
const spanId = "92000000-0000-4000-8000-000000000006";
const sourceId = "92000000-0000-4000-8000-000000000007";
const sourceVersionId = "92000000-0000-4000-8000-000000000008";
const versionHref = `/api/v1/workspaces/${workspaceId}/source-versions/${sourceVersionId}`;

const evidence = {
  chunk_id: chunkId,
  parse_projection_id: projectionId,
  sequence: 0,
  content_hash: "a".repeat(64),
  heading_path: ["Search"],
  span: {
    span_id: spanId,
    start_line: 2,
    end_line: 3,
    start_byte: 5,
    end_byte: 25,
  },
  snippet: "explainable evidence",
  provenances: [{
    source_id: sourceId,
    source_version_id: sourceVersionId,
    relative_path: "docs/search.md",
    captured_at: "2026-07-19T01:02:03.123456789Z",
    source_version_href: versionHref,
    source_span_href: `${versionHref}/spans/${spanId}`,
  }],
  provenance_truncated: false,
  scores: {
    lexical: { rank: 1, score: 0.8, fts_score: 0.7, trigram_score: 0.1 },
    vector: null,
    fusion: { rank: 1, score: 0.0327 },
    rerank: null,
  },
};

const keywordPayload = {
  workspace_id: workspaceId,
  index_version_id: indexVersionId,
  embedding_version_id: null,
  requested_mode: "keyword",
  effective_mode: "keyword",
  index_degraded_capabilities: ["vector"],
  degradations: [],
  items: [evidence],
};

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("decodeSearchResponse", () => {
  it("解码正常 Keyword Evidence 与 nullable stages", () => {
    const result = decodeSearchResponse(keywordPayload);
    expect(result).toMatchObject({
      workspaceId,
      requestedMode: "keyword",
      effectiveMode: "keyword",
      embeddingVersionId: null,
      indexDegradedCapabilities: ["vector"],
    });
    expect(result.items[0]).toMatchObject({
      chunkId,
      contentHash: "a".repeat(64),
      scores: { vector: null, rerank: null, lexical: { ftsScore: 0.7 } },
    });
  });

  it("接受根级 Evidence 的空 heading_path 数组并拒绝 null", () => {
    const result = decodeSearchResponse({
      ...keywordPayload,
      items: [{ ...evidence, heading_path: [] }],
    });
    expect(result.items[0]?.headingPath).toEqual([]);
    expect(() => decodeSearchResponse({
      ...keywordPayload,
      items: [{ ...evidence, heading_path: null }],
    })).toThrow(SearchApiError);
  });

  it("保留 Hybrid vector 原始 distance、显式降级与 cursor", () => {
    const result = decodeSearchResponse({
      ...keywordPayload,
      embedding_version_id: embeddingVersionId,
      requested_mode: "hybrid",
      effective_mode: "hybrid",
      index_degraded_capabilities: [],
      degradations: [{ capability: "rerank", error_code: "RETRIEVAL_RERANK_UNAVAILABLE", retryable: true }],
      next_cursor: "cursor-v1.opaque",
      items: [{
        ...evidence,
        scores: {
          ...evidence.scores,
          vector: { rank: 2, distance: 0.125 },
        },
      }],
    });
    expect(result.nextCursor).toBe("cursor-v1.opaque");
    expect(result.degradations).toEqual([{ capability: "rerank", errorCode: "RETRIEVAL_RERANK_UNAVAILABLE", retryable: true }]);
    expect(result.items[0]?.scores.vector).toEqual({ rank: 2, distance: 0.125 });
  });

  it("接受 Semantic Evidence 缺少 lexical stage", () => {
    const result = decodeSearchResponse({
      ...keywordPayload,
      embedding_version_id: embeddingVersionId,
      requested_mode: "semantic",
      effective_mode: "semantic",
      index_degraded_capabilities: [],
      items: [{
        ...evidence,
        scores: {
          ...evidence.scores,
          lexical: null,
          vector: { rank: 1, distance: 0.25 },
        },
      }],
    });
    expect(result.items[0]?.scores).toMatchObject({ lexical: null, vector: { distance: 0.25 } });
  });

  it("解码 FTS-only Hybrid 为 keyword effective mode 和 vector/rerank degraded", () => {
    const result = decodeSearchResponse({
      ...keywordPayload,
      requested_mode: "hybrid",
      degradations: [
        { capability: "vector", error_code: "RETRIEVAL_VECTOR_UNAVAILABLE", retryable: false },
        { capability: "rerank", error_code: "RETRIEVAL_RERANK_UNAVAILABLE", retryable: false },
      ],
    });
    expect(result).toMatchObject({
      requestedMode: "hybrid",
      effectiveMode: "keyword",
      indexDegradedCapabilities: ["vector"],
    });
    expect(result.degradations.map((item) => item.capability)).toEqual(["vector", "rerank"]);
  });

  it.each([
    ["non-finite score", { ...evidence, scores: { ...evidence.scores, fusion: { rank: 1, score: Number.POSITIVE_INFINITY } } }],
    ["invalid UUID", { ...evidence, chunk_id: "not-a-uuid" }],
    ["invalid hash", { ...evidence, content_hash: "A".repeat(64) }],
    ["invalid time", { ...evidence, provenances: [{ ...evidence.provenances[0], captured_at: "2026-02-30T01:02:03Z" }] }],
    ["missing href", { ...evidence, provenances: [{ ...evidence.provenances[0], source_span_href: undefined }] }],
    ["mismatched href", { ...evidence, provenances: [{ ...evidence.provenances[0], source_version_href: `${versionHref}/wrong` }] }],
  ])("拒绝 %s", (_name, invalidEvidence) => {
    expect(() => decodeSearchResponse({ ...keywordPayload, items: [invalidEvidence] })).toThrow(SearchApiError);
  });

  it("拒绝未知 mode、capability 与缺失必填字段", () => {
    expect(() => decodeSearchResponse({ ...keywordPayload, requested_mode: "magic" })).toThrow(SearchApiError);
    expect(() => decodeSearchResponse({ ...keywordPayload, index_degraded_capabilities: ["rerank"] })).toThrow(SearchApiError);
    expect(() => decodeSearchResponse({ ...keywordPayload, degradations: [{ capability: "graph", error_code: "X", retryable: false }] })).toThrow(SearchApiError);
    const missingItems: Record<string, unknown> = { ...keywordPayload };
    delete missingItems.items;
    expect(() => decodeSearchResponse(missingItems)).toThrow(SearchApiError);
  });

  it.each([
    ["顶层未知字段", { ...keywordPayload, unexpected: true }],
    ["Evidence 未知字段", { ...keywordPayload, items: [{ ...evidence, unexpected: true }] }],
    ["Score 未知字段", { ...keywordPayload, items: [{ ...evidence, scores: { ...evidence.scores, unexpected: true } }] }],
    ["错误 cursor 类型", { ...keywordPayload, next_cursor: 42 }],
    ["过长 cursor", { ...keywordPayload, next_cursor: "x".repeat(2049) }],
    ["过长 snippet", { ...keywordPayload, items: [{ ...evidence, snippet: "x".repeat(4097) }] }],
    ["超界 rank", { ...keywordPayload, items: [{ ...evidence, scores: { ...evidence.scores, fusion: { rank: 501, score: 0.1 } } }] }],
    ["过长 heading", { ...keywordPayload, items: [{ ...evidence, heading_path: ["x".repeat(257)] }] }],
    ["过长 degradation code", { ...keywordPayload, degradations: [{ capability: "vector", error_code: "x".repeat(129), retryable: false }] }],
    ["过长 rerank model", { ...keywordPayload, items: [{ ...evidence, scores: { ...evidence.scores, rerank: { rank: 1, score: 0.9, model_version: "x".repeat(257) } } }] }],
  ])("拒绝 %s", (_name, payload) => {
    expect(() => decodeSearchResponse(payload)).toThrow(SearchApiError);
  });
});

describe("search client", () => {
  it("发送规范 Search 请求并解码成功响应", async () => {
    const fetchMock = vi.fn<typeof fetch>().mockResolvedValue(new Response(JSON.stringify(keywordPayload), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    }));
    vi.stubGlobal("fetch", fetchMock);

    const result = await search({
      workspaceId,
      query: "stable query",
      retrievalMode: "keyword",
      filters: { sourceIds: [sourceId], capturedAtFrom: "2026-07-19T09:02:03+08:00" },
      cursor: "cursor-v1.opaque",
      limit: 20,
    });

    expect(result.requestedMode).toBe("keyword");
    const [, init] = fetchMock.mock.calls[0] ?? [];
    expect(init?.method).toBe("POST");
    const requestBody = init?.body;
    if (typeof requestBody !== "string") throw new Error("Search request body is not a string");
    expect(JSON.parse(requestBody)).toEqual({
      workspace_id: workspaceId,
      query: "stable query",
      retrieval_mode: "keyword",
      filters: { source_ids: [sourceId], captured_at_from: "2026-07-19T09:02:03+08:00" },
      cursor: "cursor-v1.opaque",
      limit: 20,
    });
  });

  it("将服务端 Problem 映射为稳定 SearchApiError", async () => {
    vi.stubGlobal("fetch", vi.fn<typeof fetch>().mockResolvedValue(new Response(JSON.stringify({
      error_code: "RETRIEVAL_SEMANTIC_UNAVAILABLE",
      message: "请求未完成",
      retryable: false,
      details: { restart_required: true },
    }), { status: 503, headers: { "Content-Type": "application/json" } })));

    await expect(search({ workspaceId, query: "q", retrievalMode: "semantic" })).rejects.toMatchObject({
      name: "SearchApiError",
      code: "RETRIEVAL_SEMANTIC_UNAVAILABLE",
      retryable: false,
      status: 503,
      details: { restart_required: true },
    });
  });

  it.each([
    ["未知字段", { error_code: "X", message: "failed", retryable: false, unexpected: true }],
    ["非法 workflow_run_id", { error_code: "X", message: "failed", retryable: false, workflow_run_id: "invalid" }],
  ])("拒绝含%s的 Problem", async (_name, problem) => {
    vi.stubGlobal("fetch", vi.fn<typeof fetch>().mockResolvedValue(new Response(JSON.stringify(problem), {
      status: 500,
      headers: { "Content-Type": "application/json" },
    })));
    await expect(search({ workspaceId, query: "q" })).rejects.toMatchObject({
      code: "INVALID_RESPONSE",
      retryable: false,
      status: 500,
    });
  });

  it("拒绝非 TypeScript 调用方注入的未知 retrieval mode", async () => {
    const input = { workspaceId, query: "q" };
    Reflect.set(input, "retrievalMode", "magic");
    await expect(search(input)).rejects.toMatchObject({ code: "INVALID_REQUEST", retryable: false });
  });

  it("拒绝超过公开契约上限的请求 cursor", async () => {
    await expect(search({ workspaceId, query: "q", cursor: "x".repeat(2049) })).rejects.toMatchObject({
      code: "INVALID_REQUEST",
      retryable: false,
    });
  });

  it.each([
    ["NUL query", "a\0b"],
    ["超过 8192 UTF-8 bytes 的 query", "汉".repeat(2731)],
  ])("拒绝%s", async (_name, query) => {
    await expect(search({ workspaceId, query })).rejects.toMatchObject({
      code: "INVALID_REQUEST",
      retryable: false,
    });
  });
});
