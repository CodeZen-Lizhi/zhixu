import { beforeEach, describe, expect, it, vi } from "vitest";

import {
  AuthApiError,
  authFetch,
  bootstrapSession,
  clearCsrfToken,
  createApiToken,
  decodeApiTokenCredential,
  decodeSessionInfo,
  getCurrentSession,
  getCsrfToken,
  setCsrfToken,
  subscribeAuthInvalidation,
} from "./auth";

const sessionId = "10000000-0000-4000-8000-000000000001";
const tokenId = "10000000-0000-4000-8000-000000000002";
const csrfToken = "c".repeat(43);
const credentialToken = "t".repeat(43);
const timestamp = "2026-07-23T08:09:10.123Z";

const jsonResponse = (body: unknown, status = 200): Response => new Response(JSON.stringify(body), {
  status,
  headers: { "Content-Type": "application/json" },
});

describe("auth API boundary", () => {
  beforeEach(() => {
    window.localStorage.clear();
    window.sessionStorage.clear();
    vi.stubGlobal("fetch", vi.fn());
  });

  it("严格解码 Session，并保留可选撤销时间", () => {
    expect(decodeSessionInfo({
      id: sessionId,
      user_label: "owner",
      scopes: ["READ_LOCAL", "WRITE_PROPOSAL"],
      created_at: timestamp,
      last_seen_at: timestamp,
      expires_at: "2026-07-24T08:09:10Z",
    })).toEqual({
      id: sessionId,
      userLabel: "owner",
      scopes: ["READ_LOCAL", "WRITE_PROPOSAL"],
      createdAt: timestamp,
      lastSeenAt: timestamp,
      expiresAt: "2026-07-24T08:09:10Z",
    });

    expect(() => decodeSessionInfo({
      id: sessionId,
      user_label: "owner",
      scopes: ["READ_LOCAL"],
      created_at: timestamp,
      last_seen_at: timestamp,
      expires_at: "2026-07-24T08:09:10Z",
      unexpected: true,
    })).toThrow(AuthApiError);

    expect(() => decodeSessionInfo({
      id: sessionId,
      user_label: "owner",
      scopes: ["READ_LOCAL"],
      created_at: timestamp,
      last_seen_at: timestamp,
      expires_at: "2026-07-24T08:09:10Z",
      revoked_at: "not-a-date",
    })).toThrow(AuthApiError);
  });

  it.each([
    ["missing retryable", { error_code: "AUTH_UNAUTHORIZED", message: "unauthorized" }],
    ["non-boolean retryable", { error_code: "AUTH_UNAUTHORIZED", message: "unauthorized", retryable: "false" }],
    ["unknown field", { error_code: "AUTH_UNAUTHORIZED", message: "unauthorized", retryable: false, extra: true }],
    ["invalid details", { error_code: "AUTH_UNAUTHORIZED", message: "unauthorized", retryable: false, details: "invalid" }],
    ["invalid workflow run id", { error_code: "AUTH_UNAUTHORIZED", message: "unauthorized", retryable: false, workflow_run_id: "not-a-uuid" }],
  ])("拒绝不符合 Problem 契约的认证错误：%s", async (_name, payload) => {
    vi.mocked(fetch).mockResolvedValueOnce(jsonResponse(payload, 503));

    await expect(getCurrentSession()).rejects.toMatchObject({ code: "INVALID_RESPONSE", status: 503 });
  });

  it("为 Cookie 请求附加 credentials、JSON 和 CSRF，并保留 Bearer 例外", async () => {
    setCsrfToken(csrfToken);
    vi.mocked(fetch).mockResolvedValue(jsonResponse({ ok: true }));

    await authFetch("/api/v1/proposals", { method: "POST", body: "{}" });
    const firstCall = vi.mocked(fetch).mock.calls.at(-1);
    expect(firstCall?.[0]).toBe("/api/v1/proposals");
    expect(firstCall?.[1]?.credentials).toBe("include");
    const firstHeaders = new Headers(firstCall?.[1]?.headers);
    expect(firstHeaders.get("Accept")).toBe("application/json");
    expect(firstHeaders.get("Content-Type")).toBe("application/json");
    expect(firstHeaders.get("X-CSRF-Token")).toBe(csrfToken);

    await authFetch("/api/v1/auth/sessions", { method: "POST", headers: { Authorization: "Bearer bootstrap" } });
    const secondCall = vi.mocked(fetch).mock.calls.at(-1);
    expect(secondCall?.[0]).toBe("/api/v1/auth/sessions");
    expect(secondCall?.[1]?.credentials).toBe("include");
    expect(new Headers(secondCall?.[1]?.headers).get("X-CSRF-Token")).toBeNull();
  });

  it("Bootstrap 把 CSRF 放入与持久 Session 同生命周期的 Local Storage", async () => {
    vi.mocked(fetch).mockResolvedValueOnce(jsonResponse({
      session_id: sessionId,
      csrf_token: csrfToken,
      expires_at: "2026-07-24T08:09:10Z",
    }, 201));

    const credential = await bootstrapSession("bootstrap-token");

    expect(credential).toEqual({ sessionId, csrfToken, expiresAt: "2026-07-24T08:09:10Z" });
    expect(getCsrfToken()).toBe(csrfToken);
    expect(window.localStorage.getItem("zhixu.csrf-token")).toBe(csrfToken);
    expect(JSON.stringify(window.localStorage)).not.toContain("bootstrap-token");
    expect(window.sessionStorage.getItem("zhixu.csrf-token")).toBeNull();
  });

  it.each(["getItem", "setItem", "removeItem"] as const)("Storage.%s 被阻断时显式通知认证失效", (method) => {
    const listener = vi.fn();
    const unsubscribe = subscribeAuthInvalidation(listener);
    vi.spyOn(window.localStorage, method).mockImplementation(() => {
      throw new DOMException("blocked", "SecurityError");
    });

    const operation = method === "getItem"
      ? () => getCsrfToken()
      : method === "setItem"
        ? () => setCsrfToken(csrfToken)
        : () => clearCsrfToken();

    expect(operation).toThrow(expect.objectContaining({ code: "AUTH_STORAGE_UNAVAILABLE" }));
    expect(listener).toHaveBeenCalledOnce();
    expect(listener.mock.calls[0]?.[0]).toMatchObject({
      kind: "storage_unavailable",
      error: { code: "AUTH_STORAGE_UNAVAILABLE" },
    });
    unsubscribe();
  });

  it("认证请求不会把 Storage 故障伪装成网络错误", async () => {
    vi.spyOn(window.localStorage, "getItem").mockImplementation(() => {
      throw new DOMException("blocked", "SecurityError");
    });

    await expect(createApiToken({ name: "CI", scopes: ["READ_LOCAL"] })).rejects.toMatchObject({
      code: "AUTH_STORAGE_UNAVAILABLE",
      status: null,
      retryable: false,
    });
  });

  it("认证请求保留网络错误 cause，通用 authFetch 不污染领域错误类型", async () => {
    const cause = new Error("offline");
    vi.mocked(fetch).mockRejectedValue(cause);

    await expect(authFetch("/api/v1/workspaces")).rejects.toBe(cause);
    await expect(bootstrapSession("bootstrap-token")).rejects.toMatchObject({
      name: "AuthApiError",
      code: "NETWORK_ERROR",
      cause,
    });
  });

  it("业务 API 返回 401 时清理 CSRF 并通知认证边界", async () => {
    const listener = vi.fn();
    const unsubscribe = subscribeAuthInvalidation(listener);
    setCsrfToken(csrfToken);
    vi.mocked(fetch).mockResolvedValueOnce(jsonResponse({ error_code: "AUTH_UNAUTHORIZED", message: "unauthorized", retryable: false }, 401));

    await authFetch("/api/v1/workspaces");

    expect(getCsrfToken()).toBeUndefined();
    expect(listener).toHaveBeenCalledOnce();
    unsubscribe();
  });

  it("受保护认证端点返回 401 时失效 Session，但 Bootstrap 失败不清理既有状态", async () => {
    const listener = vi.fn();
    const unsubscribe = subscribeAuthInvalidation(listener);
    setCsrfToken(csrfToken);
    vi.mocked(fetch)
      .mockResolvedValueOnce(jsonResponse({ error_code: "AUTH_UNAUTHORIZED", message: "expired", retryable: false }, 401))
      .mockResolvedValueOnce(jsonResponse({ error_code: "AUTH_UNAUTHORIZED", message: "bad bootstrap", retryable: false }, 401));

    await authFetch("/api/v1/auth/api-tokens");
    expect(getCsrfToken()).toBeUndefined();
    expect(listener).toHaveBeenCalledOnce();

    setCsrfToken(csrfToken);
    await authFetch("/api/v1/auth/sessions", { method: "POST", headers: { Authorization: "Bearer bad" } });
    expect(getCsrfToken()).toBe(csrfToken);
    expect(listener).toHaveBeenCalledOnce();
    unsubscribe();
  });

  it("API Token 明文只接受严格的一次性凭据响应", () => {
    expect(decodeApiTokenCredential({
      id: tokenId,
      name: "automation",
      scopes: ["READ_LOCAL"],
      expires_at: "2026-08-23T08:09:10Z",
      token: credentialToken,
    })).toEqual({
      id: tokenId,
      name: "automation",
      scopes: ["READ_LOCAL"],
      expiresAt: "2026-08-23T08:09:10Z",
      token: credentialToken,
    });
    expect(() => decodeApiTokenCredential({
      id: tokenId,
      name: "automation",
      scopes: ["READ_LOCAL"],
      expires_at: "2026-08-23T08:09:10Z",
      token: "short",
    })).toThrow(AuthApiError);
  });

  it("清理 CSRF 是幂等的", () => {
    setCsrfToken(csrfToken);
    clearCsrfToken();
    clearCsrfToken();
    expect(getCsrfToken()).toBeUndefined();
  });
});
