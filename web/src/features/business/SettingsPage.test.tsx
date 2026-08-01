import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, useLocation } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const workspaceId = "10000000-0000-4000-8000-000000000002";
const tokenId = "10000000-0000-4000-8000-000000000003";
const workspaceApi = vi.hoisted(() => ({ getWorkspace: vi.fn() }));
const auth = vi.hoisted(() => ({ createApiToken: vi.fn(), listApiTokens: vi.fn(), revokeApiToken: vi.fn() }));
const systemStatus = vi.hoisted(() => ({ render: vi.fn() }));

vi.mock("../../api/auth", async (importOriginal) => ({
  // eslint-disable-next-line @typescript-eslint/consistent-type-imports
  ...(await importOriginal<typeof import("../../api/auth")>()),
  createApiToken: auth.createApiToken,
  listApiTokens: auth.listApiTokens,
  revokeApiToken: auth.revokeApiToken,
}));
vi.mock("../../api/workspace", () => ({
  getWorkspace: workspaceApi.getWorkspace,
}));
vi.mock("../../app/active-workspace", () => ({ useActiveWorkspaceId: () => "10000000-0000-4000-8000-000000000002" }));
vi.mock("../../app/auth-context", () => ({ useAuth: () => ({ state: { status: "authenticated", mode: "required" } }) }));
vi.mock("../settings/ModelSettingsPanel", () => ({ ModelSettingsPanel: () => <section aria-label="模型设置面板">真实模型设置面板</section> }));
vi.mock("../system-status/SystemStatusPage", () => ({ SystemStatusPage: (props: { display?: string }) => { systemStatus.render(props); return <section aria-label="系统状态面板">{props.display}</section>; } }));

import { SettingsPage } from "../settings/SettingsPage";

const LocationProbe = () => {
  const location = useLocation();
  return <output data-testid="settings-location">{location.pathname}{location.search}</output>;
};

const renderSettings = (path = "/settings") => {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const result = render(
  <QueryClientProvider client={queryClient}>
    <MemoryRouter initialEntries={[path]}><SettingsPage /><LocationProbe /></MemoryRouter>
  </QueryClientProvider>,
  );
  return { ...result, queryClient };
};

beforeEach(() => {
  window.localStorage.clear();
  systemStatus.render.mockClear();
  workspaceApi.getWorkspace.mockResolvedValue({ id: workspaceId, name: "Docs", rootPath: "/workspace", status: "active", git: { present: true, dirty: false, branch: "dev", head: "abc" } });
});

afterEach(() => {
  auth.createApiToken.mockReset();
  auth.listApiTokens.mockReset();
  auth.revokeApiToken.mockReset();
});

describe("SettingsPage API Token management", () => {
  it("模型分类挂载真实设置面板，不再显示无契约占位", () => {
    auth.listApiTokens.mockResolvedValue({ items: [] });
    renderSettings("/settings?section=models");

    expect(screen.getByRole("region", { name: "模型设置面板" })).toHaveTextContent("真实模型设置面板");
    expect(screen.queryByText("无公开读取/写入契约")).not.toBeInTheDocument();
  });

  it("用 URL 分类状态组织五类设置", () => {
    auth.listApiTokens.mockResolvedValue({ items: [] });
    renderSettings();

    const navigation = screen.getByRole("navigation", { name: "设置分类" });
    expect(screen.getByRole("heading", { name: "设置", level: 1 })).toBeInTheDocument();
    expect(navigation).toHaveTextContent("工作区模型与检索数据导出访问权限系统状态");
    expect(screen.getByRole("link", { name: "工作区" })).toHaveAttribute("aria-current", "page");
  });

  it("未知分类回退到工作区，并在切换分类时保留其他查询参数", async () => {
    auth.listApiTokens.mockResolvedValue({ items: [] });
    renderSettings("/settings?section=unknown&from=shortcut");

    expect(screen.getByRole("link", { name: "工作区" })).toHaveAttribute("aria-current", "page");
    await waitFor(() => expect(screen.getByTestId("settings-location")).toHaveTextContent("/settings?section=workspace&from=shortcut"));

    fireEvent.click(screen.getByRole("link", { name: "模型与检索" }));
    await waitFor(() => expect(screen.getByTestId("settings-location")).toHaveTextContent("/settings?section=models&from=shortcut"));
  });

  it("系统状态分类展示完整依赖事实", () => {
    renderSettings("/settings?section=system");

    expect(screen.getByRole("region", { name: "系统状态面板" })).toHaveTextContent("full");
    expect(systemStatus.render).toHaveBeenCalledWith({ display: "full" });
  });

  it("一次性明文未确认复制前阻止连续创建和离页丢失", async () => {
    auth.listApiTokens
      .mockResolvedValueOnce({ items: [] })
      .mockResolvedValueOnce({ items: [{ id: tokenId, name: "CI read", scopes: ["READ_LOCAL"], createdAt: "2026-07-23T00:00:00Z", expiresAt: "2026-08-22T00:00:00Z" }] });
    auth.createApiToken.mockResolvedValue({ id: tokenId, name: "CI read", scopes: ["READ_LOCAL"], expiresAt: "2026-08-22T00:00:00Z", token: "t".repeat(43) });

    const { queryClient } = renderSettings("/settings?section=access");
    expect(await screen.findByText("尚无 API Token")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("名称"), { target: { value: "CI read" } });
    fireEvent.click(screen.getByRole("button", { name: "创建 API Token" }));

    expect(await screen.findByText("t".repeat(43))).toBeInTheDocument();
    expect(auth.createApiToken).toHaveBeenCalledWith({ name: "CI read", scopes: ["READ_LOCAL"], expiresInSeconds: 2_592_000 });
    await waitFor(() => expect(auth.listApiTokens).toHaveBeenCalledTimes(2));
    expect(screen.getByRole("button", { name: "创建 API Token" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "关闭一次性 Token" })).toBeDisabled();
    expect(JSON.stringify(window.localStorage)).not.toContain("t".repeat(43));
    const queryCache = queryClient.getQueryCache().getAll().map((query) => query.state.data);
    const mutationCache = queryClient.getMutationCache().getAll().map((mutation) => mutation.state);
    expect(JSON.stringify({ queryCache, mutationCache })).not.toContain("t".repeat(43));

    const confirmNavigation = vi.spyOn(window, "confirm").mockReturnValue(false);
    fireEvent.click(screen.getByRole("link", { name: "模型与检索" }));
    expect(confirmNavigation).toHaveBeenCalledOnce();
    expect(screen.getByTestId("settings-location")).toHaveTextContent("/settings?section=access");
    confirmNavigation.mockRestore();

    const beforeUnload = new Event("beforeunload", { cancelable: true });
    window.dispatchEvent(beforeUnload);
    expect(beforeUnload.defaultPrevented).toBe(true);

    fireEvent.click(screen.getByRole("checkbox", { name: "我已复制并安全保存该 Token" }));
    const confirmedBeforeUnload = new Event("beforeunload", { cancelable: true });
    window.dispatchEvent(confirmedBeforeUnload);
    expect(confirmedBeforeUnload.defaultPrevented).toBe(false);
    fireEvent.click(screen.getByRole("button", { name: "关闭一次性 Token" }));
    expect(screen.queryByText("t".repeat(43))).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "创建 API Token" })).toBeEnabled();
  });

  it("撤销失败显示可见错误并可使用同一 Token 重试", async () => {
    auth.listApiTokens.mockResolvedValue({ items: [{ id: tokenId, name: "CI read", scopes: ["READ_LOCAL"], createdAt: "2026-07-23T00:00:00Z", expiresAt: "2026-08-22T00:00:00Z" }] });
    auth.revokeApiToken
      .mockRejectedValueOnce(new Error("撤销服务不可用"))
      .mockResolvedValueOnce(undefined);

    renderSettings("/settings?section=access");
    expect(await screen.findByText("CI read")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "撤销" }));

    expect(await screen.findByRole("alert")).toHaveTextContent("撤销服务不可用");
    fireEvent.click(screen.getByRole("button", { name: "重试撤销 CI read" }));
    await waitFor(() => expect(auth.revokeApiToken).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(screen.queryByText("撤销服务不可用")).not.toBeInTheDocument());
    expect(auth.revokeApiToken).toHaveBeenNthCalledWith(1, tokenId);
    expect(auth.revokeApiToken).toHaveBeenNthCalledWith(2, tokenId);
  });

  it("下一页失败保留已加载 Token，并提供独立重试", async () => {
    const secondTokenId = "10000000-0000-4000-8000-000000000004";
    auth.listApiTokens
      .mockResolvedValueOnce({ items: [{ id: tokenId, name: "first", scopes: ["READ_LOCAL"], createdAt: "2026-07-23T00:00:00Z", expiresAt: "2026-08-22T00:00:00Z" }], nextCursor: "next-page" })
      .mockRejectedValueOnce(new Error("第二页暂时不可用"))
      .mockResolvedValueOnce({ items: [{ id: secondTokenId, name: "second", scopes: ["READ_LOCAL"], createdAt: "2026-07-22T00:00:00Z", expiresAt: "2026-08-21T00:00:00Z" }] });

    renderSettings("/settings?section=access");
    expect(await screen.findByText("first")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "加载更多 Token" }));

    expect(await screen.findByRole("alert")).toHaveTextContent("第二页暂时不可用");
    expect(screen.getByText("first")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "重试加载更多 Token" }));

    expect(await screen.findByText("second")).toBeInTheDocument();
    expect(screen.getByText("first")).toBeInTheDocument();
    expect(screen.queryByText("第二页暂时不可用")).not.toBeInTheDocument();
    expect(auth.listApiTokens).toHaveBeenNthCalledWith(2, "next-page", 30, expect.any(AbortSignal));
    expect(auth.listApiTokens).toHaveBeenNthCalledWith(3, "next-page", 30, expect.any(AbortSignal));
  });
});
