import { describe, expect, it } from "vitest";

import { ApiBoundaryError, decodeSystemStatus } from "./system-status";

describe("decodeSystemStatus", () => {
  it("将冻结的 API 契约映射为前端领域模型", () => {
    expect(
      decodeSystemStatus({
        status: "ready",
        version: "0.1.0",
        database: { status: "ready" },
        request_id: "request-1",
        ignored_field: true,
      }),
    ).toEqual({
      status: "ready",
      version: "0.1.0",
      database: { status: "ready" },
      requestId: "request-1",
    });
  });

  it("拒绝缺少必填字段或未知状态的响应", () => {
    expect(() =>
      decodeSystemStatus({
        status: "healthy",
        version: "0.1.0",
        database: { status: "ready" },
        request_id: "request-1",
      }),
    ).toThrow(ApiBoundaryError);
  });
});
