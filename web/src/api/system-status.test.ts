import { describe, expect, it } from "vitest";

import { ApiBoundaryError, decodeSystemStatus } from "./system-status";

describe("decodeSystemStatus", () => {
  it("将冻结的 API 契约映射为前端领域模型", () => {
    expect(
      decodeSystemStatus({
        status: "ready",
        version: "0.1.0",
        database: { status: "ready" },
        graph: { status: "ready" },
        semantic_links: { status: "ready" },
        rag: { status: "disabled" },
        collections: { status: "ready" },
        knowledge_health: { status: "ready" },
        request_id: "request-1",
      }),
    ).toEqual({
      status: "ready",
      version: "0.1.0",
      database: { status: "ready" },
      graph: { status: "ready" },
      semanticLinks: { status: "ready" },
      rag: { status: "disabled" },
      collections: { status: "ready" },
      knowledgeHealth: { status: "ready" },
      requestId: "request-1",
    });
  });

  it("拒绝缺少必填字段或未知状态的响应", () => {
    expect(() =>
      decodeSystemStatus({
        status: "healthy",
        version: "0.1.0",
        database: { status: "ready" },
        graph: { status: "ready" },
        semantic_links: { status: "ready" },
        rag: { status: "ready" },
        collections: { status: "ready" }, knowledge_health: { status: "ready" },
        request_id: "request-1",
      }),
    ).toThrow(ApiBoundaryError);
    expect(() => decodeSystemStatus({
      status: "ready", version: "0.1.0", database: { status: "ready" }, graph: { status: "ready" },
      semantic_links: { status: "ready" }, rag: { status: "ready" }, request_id: "request-1", ignored_field: true,
      collections: { status: "ready" }, knowledge_health: { status: "ready" },
    })).toThrow(ApiBoundaryError);
  });

  it("解码 Graph 降级并拒绝未知状态或原因", () => {
    expect(decodeSystemStatus({
      status: "degraded",
      version: "0.1.0",
      database: { status: "ready" },
      graph: { status: "unavailable", reason: "graph_dependencies_unavailable" },
      semantic_links: { status: "ready" },
      rag: { status: "disabled" },
      collections: { status: "ready" }, knowledge_health: { status: "ready" },
      request_id: "request-graph",
    }).graph).toEqual({ status: "unavailable", reason: "graph_dependencies_unavailable" });

    for (const graph of [
      { status: "disabled" },
      { status: "unavailable", reason: "private_failure" },
      { status: "ready", ignored: true },
    ]) {
      expect(() => decodeSystemStatus({
        status: "degraded",
        version: "0.1.0",
        database: { status: "ready" },
        graph,
        semantic_links: { status: "ready" },
        rag: { status: "disabled" },
        collections: { status: "ready" }, knowledge_health: { status: "ready" },
        request_id: "request-graph",
      })).toThrow(ApiBoundaryError);
    }
  });

  it("解码语义候选降级并拒绝未知状态或原因", () => {
    expect(decodeSystemStatus({
      status: "degraded",
      version: "0.1.0",
      database: { status: "ready" },
      graph: { status: "ready" },
      semantic_links: { status: "unavailable", reason: "semantic_link_dependencies_unavailable" },
      rag: { status: "disabled" },
      collections: { status: "ready" }, knowledge_health: { status: "ready" },
      request_id: "request-semantic-links",
    }).semanticLinks).toEqual({ status: "unavailable", reason: "semantic_link_dependencies_unavailable" });

    for (const semanticLinks of [
      { status: "disabled" },
      { status: "unavailable", reason: "private_failure" },
      { status: "ready", ignored: true },
    ]) {
      expect(() => decodeSystemStatus({
        status: "degraded",
        version: "0.1.0",
        database: { status: "ready" },
        graph: { status: "ready" },
        semantic_links: semanticLinks,
        rag: { status: "disabled" },
        collections: { status: "ready" }, knowledge_health: { status: "ready" },
        request_id: "request-semantic-links",
      })).toThrow(ApiBoundaryError);
    }
  });
});
