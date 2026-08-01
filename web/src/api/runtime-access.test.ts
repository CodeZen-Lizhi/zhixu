import { afterEach, describe, expect, it, vi } from "vitest";

import { RuntimeAccessApiError, decodeRuntimeAccess, getRuntimeAccess } from "./runtime-access";

const workspaceId = "90000000-0000-4000-8000-000000000001";
const json = (body: unknown, status = 200, contentType = "application/json"): Response =>
  new Response(JSON.stringify(body), { status, headers: { "Content-Type": contentType } });

afterEach(() => vi.unstubAllGlobals());

describe("runtime access API boundary", () => {
  it("严格接受 ready 与非 ready 的互斥 Workspace 组合", () => {
    expect(decodeRuntimeAccess({ status: "ready", active_workspace_id: workspaceId, poll_after_ms: 500 })).toEqual({
      status: "ready", activeWorkspaceId: workspaceId, pollAfterMs: 500,
    });
    expect(decodeRuntimeAccess({ status: "waiting", active_workspace_id: null, poll_after_ms: 5000 })).toEqual({
      status: "waiting", activeWorkspaceId: null, pollAfterMs: 5000,
    });
    expect(() => decodeRuntimeAccess({ status: "ready", active_workspace_id: null, poll_after_ms: 500 })).toThrow(RuntimeAccessApiError);
    expect(() => decodeRuntimeAccess({ status: "unavailable", active_workspace_id: workspaceId, poll_after_ms: 500 })).toThrow(RuntimeAccessApiError);
    expect(() => decodeRuntimeAccess({ status: "ready", active_workspace_id: workspaceId, poll_after_ms: 499 })).toThrow(RuntimeAccessApiError);
    expect(() => decodeRuntimeAccess({ status: "ready", active_workspace_id: workspaceId, poll_after_ms: 500, root_path: "/secret" })).toThrow(RuntimeAccessApiError);
  });

  it("读取固定公开 endpoint，并严格解码 Problem", async () => {
    const fetchMock = vi.fn().mockResolvedValueOnce(json({ status: "ready", active_workspace_id: workspaceId, poll_after_ms: 1000 }))
      .mockResolvedValueOnce(json({ code: "RUNTIME_ACCESS_UNAVAILABLE", message: "业务运行时状态暂不可用", retryable: true }, 503));
    vi.stubGlobal("fetch", fetchMock);

    await expect(getRuntimeAccess()).resolves.toMatchObject({ status: "ready", activeWorkspaceId: workspaceId });
    const [path, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(path).toBe("/host/v1/runtime");
    expect(init.credentials).toBe("include");
    expect(init.headers).toEqual({ Accept: "application/json" });
    await expect(getRuntimeAccess()).rejects.toMatchObject({ code: "HTTP_ERROR", errorCode: "RUNTIME_ACCESS_UNAVAILABLE", status: 503, retryable: true });
  });

  it("拒绝控制接口或业务接口的其他 Problem 形状", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(json({ error_code: "RUNTIME_ACCESS_UNAVAILABLE", message: "wrong schema", retryable: true }, 503)));
    await expect(getRuntimeAccess()).rejects.toMatchObject({ code: "INVALID_RESPONSE", status: 503 });
  });

  it("保留 AbortError，将网络失败归类为可重试错误", async () => {
    vi.stubGlobal("fetch", vi.fn().mockRejectedValueOnce(new DOMException("aborted", "AbortError")).mockRejectedValueOnce(new Error("offline")));
    await expect(getRuntimeAccess()).rejects.toMatchObject({ name: "AbortError" });
    await expect(getRuntimeAccess()).rejects.toMatchObject({ code: "NETWORK_ERROR", retryable: true });
  });
});
