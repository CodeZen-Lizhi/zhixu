import { afterEach, describe, expect, it, vi } from "vitest";

import { ActiveWorkspaceApiError, decodeActiveWorkspace, getActiveWorkspace } from "./active-workspace";

const activeWorkspacePayload = {
  id: "11111111-1111-4111-8111-111111111111",
  name: "知识库",
  root_path: "/tmp/knowledge",
  status: "active",
  availability: "available",
  version: 4,
};

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("Active Workspace API", () => {
  it("严格解码服务端 Active Workspace 投影", () => {
    expect(decodeActiveWorkspace(activeWorkspacePayload)).toEqual({
      id: activeWorkspacePayload.id,
      name: "知识库",
      rootPath: "/tmp/knowledge",
      status: "active",
      availability: "available",
      version: 4,
    });
  });

  it.each([
    ["unknown field", { ...activeWorkspacePayload, extra: true }],
    ["invalid id", { ...activeWorkspacePayload, id: "not-a-uuid" }],
    ["relative root", { ...activeWorkspacePayload, root_path: "notes" }],
    ["unknown status", { ...activeWorkspacePayload, status: "inactive" }],
    ["unknown availability", { ...activeWorkspacePayload, availability: "ready" }],
    ["invalid version", { ...activeWorkspacePayload, version: 0 }],
  ])("拒绝不符合契约的响应：%s", (_name, payload) => {
    expect(() => decodeActiveWorkspace(payload)).toThrow(ActiveWorkspaceApiError);
  });

  it("只请求服务端 Active Workspace 端点", async () => {
    const fetcher = vi.fn<typeof fetch>().mockResolvedValue(new Response(JSON.stringify(activeWorkspacePayload), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    }));
    vi.stubGlobal("fetch", fetcher);

    await expect(getActiveWorkspace()).resolves.toMatchObject({ id: activeWorkspacePayload.id });
    expect(fetcher).toHaveBeenCalledOnce();
    expect(fetcher.mock.calls[0]?.[0]).toBe("/api/v1/workspaces/active");
    expect(fetcher.mock.calls[0]?.[1]?.credentials).toBe("include");
    expect(fetcher.mock.calls[0]?.[1]?.headers).toBeInstanceOf(Headers);
  });

  it("严格解码 Problem，并隐藏带未知字段的错误体", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValueOnce(new Response(JSON.stringify({
      error_code: "ACTIVE_WORKSPACE_UNAVAILABLE",
      message: "当前 Workspace 暂不可用。",
      retryable: true,
      details: {},
    }), { status: 503 })).mockResolvedValueOnce(new Response(JSON.stringify({
      error_code: "ACTIVE_WORKSPACE_UNAVAILABLE",
      message: "internal path: /private/secret",
      retryable: true,
      internal_path: "/private/secret",
    }), { status: 503 })));

    await expect(getActiveWorkspace()).rejects.toMatchObject({ code: "ACTIVE_WORKSPACE_UNAVAILABLE", retryable: true });
    await expect(getActiveWorkspace()).rejects.toMatchObject({ code: "HTTP_ERROR", message: "Active Workspace 请求失败（HTTP 503）。" });
  });

  it("保留 fetch 与响应体读取阶段的 AbortError identity", async () => {
    const fetchAbort = new DOMException("fetch aborted", "AbortError");
    vi.stubGlobal("fetch", vi.fn().mockRejectedValue(fetchAbort));
    await expect(getActiveWorkspace()).rejects.toBe(fetchAbort);

    const bodyAbort = new DOMException("body aborted", "AbortError");
    const response = new Response(JSON.stringify(activeWorkspacePayload));
    vi.spyOn(response, "json").mockRejectedValue(bodyAbort);
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(response));
    await expect(getActiveWorkspace()).rejects.toBe(bodyAbort);
  });
});
