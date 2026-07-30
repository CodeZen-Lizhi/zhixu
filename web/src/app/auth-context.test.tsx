import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { AuthBoundary, AuthProvider, useAuth } from "./auth-context";

const fetchSystemStatus = vi.hoisted(() => vi.fn());
vi.mock("../api/system-status", () => ({ fetchSystemStatus }));

const session = {
  id: "10000000-0000-4000-8000-000000000001",
  userLabel: "owner",
  scopes: ["READ_LOCAL" as const],
  createdAt: "2026-07-23T08:09:10Z",
  lastSeenAt: "2026-07-23T08:09:10Z",
  expiresAt: "2026-07-24T08:09:10Z",
};

const Probe = () => {
  const { state, refresh, signOut } = useAuth();
  return <><output data-testid="auth-state">{state.status}:{state.mode}</output><output data-testid="session-id">{state.session?.id ?? "none"}</output><button type="button" onClick={() => void refresh()}>刷新认证</button><button type="button" onClick={() => void signOut().catch(() => undefined)}>退出</button></>;
};

const renderAuth = (seedClient?: (client: QueryClient) => void) => {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  seedClient?.(client);
  return { client, ...render(<QueryClientProvider client={client}><AuthProvider><AuthBoundary><Probe /></AuthBoundary></AuthProvider></QueryClientProvider>) };
};

describe("AuthProvider/AuthBoundary", () => {
  beforeEach(() => {
    fetchSystemStatus.mockReset();
    window.localStorage.clear();
    window.sessionStorage.clear();
  });

  it("disabled 模式直接进入应用并明确保留开发边界", async () => {
    fetchSystemStatus.mockResolvedValue({ auth: { status: "disabled" } });

    renderAuth();

    expect(await screen.findByTestId("auth-state")).toHaveTextContent("authenticated:disabled");
    expect(screen.getByRole("button", { name: "退出" })).toBeInTheDocument();
  });

  it("required 模式没有当前 CSRF 时显示 Bootstrap 登录门禁", async () => {
    window.localStorage.clear();
    fetchSystemStatus.mockResolvedValue({ auth: { status: "ready" } });

    renderAuth();

    expect(await screen.findByRole("heading", { name: "输入 Bootstrap Token" })).toBeInTheDocument();
    expect(screen.getByLabelText("Bootstrap Token")).toHaveAttribute("type", "password");
  });

  it("认证依赖不可用时保持 fail closed 并提供重试", async () => {
    fetchSystemStatus.mockResolvedValue({ auth: { status: "unavailable" } });

    const { client } = renderAuth((queryClient) => queryClient.setQueryData(["protected-resource"], { value: "stale" }));

    expect(await screen.findByRole("heading", { name: "正在确认访问边界" })).toBeInTheDocument();
    expect(screen.getByText("认证依赖暂不可用。")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "重新检查" })).toBeEnabled();
    expect(screen.queryByTestId("auth-state")).not.toBeInTheDocument();
    expect(client.getQueryData(["protected-resource"])).toBeUndefined();
  });

  it("Bootstrap 成功后恢复当前 Session", async () => {
    const csrfToken = "c".repeat(43);
    window.localStorage.clear();
    fetchSystemStatus.mockResolvedValue({ auth: { status: "ready" } });
    vi.stubGlobal("fetch", vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({ session_id: session.id, csrf_token: csrfToken, expires_at: session.expiresAt }), { status: 201, headers: { "Content-Type": "application/json" } }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ id: session.id, user_label: session.userLabel, scopes: session.scopes, created_at: session.createdAt, last_seen_at: session.lastSeenAt, expires_at: session.expiresAt }), { status: 200, headers: { "Content-Type": "application/json" } })));

    renderAuth();
    const input = await screen.findByLabelText("Bootstrap Token");
    fireEvent.change(input, { target: { value: "bootstrap-token" } });
    fireEvent.click(screen.getByRole("button", { name: "建立 Session" }));

    await waitFor(() => expect(screen.getByTestId("auth-state")).toHaveTextContent("authenticated:required"));
    expect(window.localStorage.getItem("zhixu.csrf-token")).toBe(csrfToken);
    expect(window.sessionStorage.getItem("zhixu.csrf-token")).toBeNull();
  });

  it("登出网络失败时不显示匿名假成功", async () => {
    window.localStorage.setItem("zhixu.csrf-token", "c".repeat(43));
    fetchSystemStatus.mockResolvedValue({ auth: { status: "ready" } });
    vi.stubGlobal("fetch", vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({ id: session.id, user_label: session.userLabel, scopes: session.scopes, created_at: session.createdAt, last_seen_at: session.lastSeenAt, expires_at: session.expiresAt }), { status: 200, headers: { "Content-Type": "application/json" } }))
      .mockRejectedValueOnce(new TypeError("network down")));

    renderAuth();
    expect(await screen.findByTestId("auth-state")).toHaveTextContent("authenticated:required");
    fireEvent.click(screen.getByRole("button", { name: "退出" }));

    await waitFor(() => expect(vi.mocked(fetch)).toHaveBeenCalledTimes(2));
    expect(screen.getByTestId("auth-state")).toHaveTextContent("authenticated:required");
    expect(window.localStorage.getItem("zhixu.csrf-token")).toBe("c".repeat(43));
  });

  it("跨 Tab 登录与登出通过 Local Storage 事件同步", async () => {
    const csrfToken = "c".repeat(43);
    fetchSystemStatus.mockResolvedValue({ auth: { status: "ready" } });
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({
      id: session.id,
      user_label: session.userLabel,
      scopes: session.scopes,
      created_at: session.createdAt,
      last_seen_at: session.lastSeenAt,
      expires_at: session.expiresAt,
    }), { status: 200, headers: { "Content-Type": "application/json" } })));

    renderAuth();
    expect(await screen.findByRole("heading", { name: "输入 Bootstrap Token" })).toBeInTheDocument();

    window.localStorage.setItem("zhixu.csrf-token", csrfToken);
    window.dispatchEvent(new StorageEvent("storage", {
      key: "zhixu.csrf-token",
      oldValue: null,
      newValue: csrfToken,
    }));
    expect(await screen.findByTestId("auth-state")).toHaveTextContent("authenticated:required");

    window.localStorage.removeItem("zhixu.csrf-token");
    window.dispatchEvent(new StorageEvent("storage", {
      key: "zhixu.csrf-token",
      oldValue: csrfToken,
      newValue: null,
    }));
    expect(await screen.findByRole("heading", { name: "输入 Bootstrap Token" })).toBeInTheDocument();
  });

  it("跨 Tab 替换 Session 时先清除旧 Model Settings query", async () => {
    const originalCsrf = "a".repeat(43);
    const replacementCsrf = "b".repeat(43);
    const replacementSession = { ...session, id: "20000000-0000-4000-8000-000000000002" };
    window.localStorage.setItem("zhixu.csrf-token", originalCsrf);
    fetchSystemStatus.mockResolvedValue({ auth: { status: "ready" } });
    vi.stubGlobal("fetch", vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({ id: session.id, user_label: session.userLabel, scopes: session.scopes, created_at: session.createdAt, last_seen_at: session.lastSeenAt, expires_at: session.expiresAt }), { status: 200, headers: { "Content-Type": "application/json" } }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ id: replacementSession.id, user_label: replacementSession.userLabel, scopes: replacementSession.scopes, created_at: replacementSession.createdAt, last_seen_at: replacementSession.lastSeenAt, expires_at: replacementSession.expiresAt }), { status: 200, headers: { "Content-Type": "application/json" } })));

    const { client } = renderAuth();
    expect(await screen.findByTestId("session-id")).toHaveTextContent(session.id);
    client.setQueryData(["settings", "models"], { desiredRevision: 7 });
    let protectedSignal: AbortSignal | undefined;
    const pendingProtectedQuery = client.fetchQuery({
      queryKey: ["settings", "models"],
      queryFn: ({ signal }) => {
        protectedSignal = signal;
        return new Promise<never>((_resolve, reject) => {
          signal.addEventListener("abort", () => reject(new DOMException("aborted", "AbortError")), { once: true });
        });
      },
    }).catch(() => undefined);
    await waitFor(() => expect(protectedSignal).toBeDefined());

    window.localStorage.setItem("zhixu.csrf-token", replacementCsrf);
    window.dispatchEvent(new StorageEvent("storage", {
      key: "zhixu.csrf-token",
      oldValue: originalCsrf,
      newValue: replacementCsrf,
    }));

    await waitFor(() => expect(screen.getByTestId("session-id")).toHaveTextContent(replacementSession.id));
    expect(protectedSignal?.aborted).toBe(true);
    expect(client.getQueryData(["settings", "models"])).toBeUndefined();
    await pendingProtectedQuery;
  });

  it("disabled 到 required 的显式 refresh 在接受新模式前清除旧 query", async () => {
    const csrfToken = "c".repeat(43);
    fetchSystemStatus
      .mockResolvedValueOnce({ auth: { status: "disabled" } })
      .mockResolvedValueOnce({ auth: { status: "ready" } });
    vi.stubGlobal("fetch", vi.fn().mockResolvedValueOnce(new Response(JSON.stringify({
      id: session.id,
      user_label: session.userLabel,
      scopes: session.scopes,
      created_at: session.createdAt,
      last_seen_at: session.lastSeenAt,
      expires_at: session.expiresAt,
    }), { status: 200, headers: { "Content-Type": "application/json" } })));

    const { client } = renderAuth();
    expect(await screen.findByTestId("auth-state")).toHaveTextContent("authenticated:disabled");
    client.setQueryData(["settings", "models"], { desiredRevision: 3 });
    window.localStorage.setItem("zhixu.csrf-token", csrfToken);

    fireEvent.click(screen.getByRole("button", { name: "刷新认证" }));

    expect(await screen.findByTestId("auth-state")).toHaveTextContent("authenticated:required");
    expect(client.getQueryData(["settings", "models"])).toBeUndefined();
  });

  it("Local Storage 被阻断时 fail closed，不声称已认证", async () => {
    fetchSystemStatus.mockResolvedValue({ auth: { status: "ready" } });
    vi.spyOn(window.localStorage, "getItem").mockImplementation(() => {
      throw new DOMException("blocked", "SecurityError");
    });

    renderAuth();

    expect(await screen.findByText("浏览器无法读取 Session 恢复状态，请允许本站存储后重新登录。")).toBeInTheDocument();
    expect(screen.queryByTestId("auth-state")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "重新检查" })).toBeEnabled();
  });
});
