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
        rag: { status: "disabled" },
        request_id: "request-1",
      }),
    ).toEqual({
      status: "ready",
      version: "0.1.0",
      database: { status: "ready" },
      graph: { status: "ready" },
      rag: { status: "disabled" },
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
        rag: { status: "ready" },
        request_id: "request-1",
      }),
    ).toThrow(ApiBoundaryError);
    expect(() => decodeSystemStatus({
      status: "ready", version: "0.1.0", database: { status: "ready" }, graph: { status: "ready" },
      rag: { status: "ready" }, request_id: "request-1", ignored_field: true,
    })).toThrow(ApiBoundaryError);
  });

  it("解码 Graph 降级并拒绝未知状态或原因", () => {
    expect(decodeSystemStatus({
      status: "degraded",
      version: "0.1.0",
      database: { status: "ready" },
      graph: { status: "unavailable", reason: "graph_dependencies_unavailable" },
      rag: { status: "disabled" },
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
        rag: { status: "disabled" },
        request_id: "request-graph",
      })).toThrow(ApiBoundaryError);
    }
  });
});
