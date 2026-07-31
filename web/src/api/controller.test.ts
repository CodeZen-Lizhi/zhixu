import { afterEach, describe, expect, it, vi } from "vitest";

import {
  checkControllerWorkspaceAvailability,
  clearControllerCsrfToken,
  consumeControllerBootstrapTokenFromFragment,
  decodeControllerOperation,
  decodeControllerProblem,
  decodeControllerState,
  exchangeControllerSession,
  getControllerCsrfToken,
  getControllerState,
  removeControllerWorkspace,
  startControllerWorkspaceSwitch,
} from "./controller";
import { authFetch, getCsrfToken, setCsrfToken } from "./auth";

const token = "a".repeat(43);
const controllerId = "b".repeat(43);
const now = "2026-07-31T10:00:00Z";
const later = "2026-07-31T10:00:01Z";
const workspace = { workspace_id: "workspace-a", name: "知识库", root_path: "/Users/test/知识库", availability: "available", last_opened_at: now };
const operation = { operation_id: "operation-a", phase: "validating", retryable: true, started_at: now, updated_at: later };
const state = {
  controller_instance_id: controllerId,
  state_version: 7,
  runtime: { status: "ready", api: { status: "ready" }, worker: { status: "ready" } },
  active_workspace: workspace,
  recent_workspaces: [workspace],
  operation: null,
  poll_after_ms: 2000,
};
const json = (body: unknown, status = 200, headers: HeadersInit = { "Content-Type": "application/json" }): Response => new Response(JSON.stringify(body), { status, headers });
const problem = (body: unknown, status: number): Response => json(body, status, { "Content-Type": "application/problem+json" });

afterEach(() => {
  clearControllerCsrfToken();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  window.history.replaceState(null, "", "/");
  window.localStorage.clear();
});

describe("Controller API decoders", () => {
  it("严格解码控制状态与全部枚举", () => {
    expect(decodeControllerState(state)).toMatchObject({
      controllerInstanceId: controllerId,
      stateVersion: 7,
      runtime: { status: "ready", api: { status: "ready" } },
      activeWorkspace: { rootPath: "/Users/test/知识库" },
    });
    expect(() => decodeControllerState({ ...state, unexpected: true })).toThrow(/控制响应/);
    expect(() => decodeControllerState({ ...state, runtime: { ...state.runtime, status: "unknown" } })).toThrow(/控制响应/);
    for (const status of ["waiting_for_workspace", "switching", "ready", "recovery_failed"] as const) {
      expect(decodeControllerState({ ...state, runtime: { ...state.runtime, status } }).runtime.status).toBe(status);
    }
    for (const availability of ["available", "unavailable", "migration_required"] as const) {
      const decoded = decodeControllerState({
        ...state,
        active_workspace: { ...workspace, availability },
        recent_workspaces: [{ ...workspace, availability }],
      });
      expect(decoded.activeWorkspace?.availability).toBe(availability);
    }
    expect(decodeControllerState({ ...state, active_workspace: null, recent_workspaces: [] }).recentWorkspaces).toEqual([]);
  });

  it("严格解码 operation 和 Problem", () => {
    expect(decodeControllerOperation({ ...operation, result: "succeeded", target_name: "知识库" })).toMatchObject({ result: "succeeded", targetName: "知识库" });
    expect(() => decodeControllerOperation({ ...operation, phase: "done" })).toThrow(/控制响应/);
    expect(decodeControllerProblem({ code: "PATH_NOT_FOUND", message: "目录不存在", retryable: true, field_errors: { root_path: "目录不存在" } }, 400)).toMatchObject({
      code: "PATH_NOT_FOUND", fieldErrors: { root_path: "目录不存在" },
    });
    expect(() => decodeControllerProblem({ code: "BAD", message: "bad", retryable: true, details: {} }, 400)).toThrow(/控制响应/);
    for (const phase of ["validating", "quiescing", "revoking", "applying_grant", "preparing", "verifying", "committing", "activating", "rolling_back", "recovering"] as const) {
      expect(decodeControllerOperation({ ...operation, phase }).phase).toBe(phase);
    }
    for (const result of ["succeeded", "rejected", "cancelled", "rolled_back", "failed"] as const) {
      expect(decodeControllerOperation({ ...operation, result }).result).toBe(result);
    }
    expect(() => decodeControllerOperation({ ...operation, started_at: "2026-02-31T10:00:00Z" })).toThrow(/控制响应/);
  });
});

describe("Controller API transport boundary", () => {
  it("交换控制会话只发送 Bearer，不写入业务 CSRF 或 localStorage", async () => {
    setCsrfToken("business-csrf");
    const fetchMock = vi.fn().mockResolvedValue(json({ controller_instance_id: controllerId, session_id: token, csrf_token: token, expires_at: now }, 201));
    vi.stubGlobal("fetch", fetchMock);

    await expect(exchangeControllerSession(token)).resolves.toMatchObject({ csrfToken: token });
    const [path, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(path).toBe("/control/v1/sessions");
    expect(init.credentials).toBe("include");
    expect(new Headers(init.headers).get("Authorization")).toBe(`Bearer ${token}`);
    expect(new Headers(init.headers).get("X-CSRF-Token")).toBeNull();
    expect(new Headers(init.headers).get("X-Zhixu-Control-CSRF-Token")).toBeNull();
    expect(init.body).toBeUndefined();
    expect(getControllerCsrfToken()).toBe(token);
    expect(getCsrfToken()).toBe("business-csrf");
    expect(window.localStorage.getItem("zhixu.csrf-token")).toBe("business-csrf");
  });

  it("控制面 401 只报告控制错误，不改变业务 CSRF", async () => {
    setCsrfToken("business-csrf");
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(problem({ code: "CONTROL_SESSION_REQUIRED", message: "需要有效会话", retryable: false }, 401)));

    await expect(getControllerState()).rejects.toMatchObject({ status: 401, errorCode: "CONTROL_SESSION_REQUIRED" });
    expect(getCsrfToken()).toBe("business-csrf");
    expect(window.localStorage.getItem("zhixu.csrf-token")).toBe("business-csrf");
  });

  it("业务 401 不清理内存中的 Controller CSRF", async () => {
    setCsrfToken("business-csrf");
    vi.stubGlobal("fetch", vi.fn()
      .mockResolvedValueOnce(json({ controller_instance_id: controllerId, session_id: token, csrf_token: token, expires_at: now }, 201))
      .mockResolvedValueOnce(new Response(null, { status: 401 })));
    await exchangeControllerSession(token);

    await expect(authFetch("/api/v1/system/status")).resolves.toMatchObject({ status: 401 });
    expect(getCsrfToken()).toBeUndefined();
    expect(getControllerCsrfToken()).toBe(token);
  });

  it("验证 state ETag 必须精确等于带引号的 state_version", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(json(state, 200, { "Content-Type": "application/json", ETag: '"7"' })));
    await expect(getControllerState()).resolves.toMatchObject({ stateVersion: 7 });

    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(json(state, 200, { "Content-Type": "application/json", ETag: "7" })));
    await expect(getControllerState()).rejects.toMatchObject({ code: "INVALID_RESPONSE" });
  });

  it("所有不安全命令携带控制 CSRF、幂等键和精确 If-Match", async () => {
    vi.stubGlobal("fetch", vi.fn()
      .mockResolvedValueOnce(json({ controller_instance_id: controllerId, session_id: token, csrf_token: token, expires_at: now }, 201))
      .mockResolvedValueOnce(json({ operation }, 202))
      .mockResolvedValueOnce(json({ workspace }))
      .mockResolvedValueOnce(new Response(null, { status: 204 })));
    await exchangeControllerSession(token);
    await startControllerWorkspaceSwitch({ targetKind: "new", name: "知识库", rootPath: "/Users/test/知识库", initializeGit: true }, { stateVersion: 7, idempotencyKey: "switch-7" });
    await checkControllerWorkspaceAvailability("workspace-a", { stateVersion: 8, idempotencyKey: "check-8" });
    await removeControllerWorkspace("workspace-a", { stateVersion: 9, idempotencyKey: "remove-9" });

    const fetchMock = vi.mocked(fetch);
    const requestInitAt = (index: number): RequestInit => {
      const init = fetchMock.mock.calls[index]?.[1];
      if (init === undefined) throw new Error(`缺少第 ${String(index)} 个 fetch 请求参数`);
      return init;
    };
    const switchInit = requestInitAt(1);
    const checkInit = requestInitAt(2);
    const removeInit = requestInitAt(3);
    for (const [init, key, version] of [[switchInit, "switch-7", '"7"'], [checkInit, "check-8", '"8"'], [removeInit, "remove-9", '"9"']] as const) {
      const headers = new Headers(init.headers);
      expect(headers.get("X-Zhixu-Control-CSRF-Token")).toBe(token);
      expect(headers.get("Idempotency-Key")).toBe(key);
      expect(headers.get("If-Match")).toBe(version);
      expect(init.credentials).toBe("include");
    }
    expect(checkInit.body).toBe("{}");
    expect(removeInit.body).toBeUndefined();
    expect(new Headers(removeInit.headers).get("Content-Type")).toBe("application/json");
  });

  it("保留 AbortError，并将其他网络失败收敛为控制网络错误", async () => {
    const aborted = new DOMException("aborted", "AbortError");
    vi.stubGlobal("fetch", vi.fn().mockRejectedValue(aborted));
    await expect(getControllerState()).rejects.toBe(aborted);

    vi.stubGlobal("fetch", vi.fn().mockRejectedValue(new Error("offline")));
    await expect(getControllerState()).rejects.toMatchObject({ code: "NETWORK_ERROR", retryable: true });
  });
});

describe("Controller one-time fragment", () => {
  it("同步消费精确 fragment，并在任何网络请求前清理 URL", () => {
    window.history.replaceState(null, "", `/?view=control#control=${token}`);
    const replace = vi.spyOn(window.history, "replaceState");
    expect(consumeControllerBootstrapTokenFromFragment()).toBe(token);
    expect(window.location.href).not.toContain(token);
    expect(window.location.pathname + window.location.search).toBe("/?view=control");
    expect(replace).toHaveBeenCalledWith(window.history.state, "", "/?view=control");
  });

  it("清理 malformed control fragment，且不保存凭据", () => {
    window.history.replaceState(null, "", "/#control=bad&next=1");
    expect(consumeControllerBootstrapTokenFromFragment()).toBeUndefined();
    expect(window.location.hash).toBe("");
    expect(window.localStorage.length).toBe(0);
    expect(window.sessionStorage.length).toBe(0);
  });
});
