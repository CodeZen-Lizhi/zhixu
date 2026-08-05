import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { lazy, type ComponentType } from "react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";

const useActiveWorkspaceId = vi.hoisted(() => vi.fn());
const captureMutation = vi.hoisted(() => ({
  mutate: vi.fn(),
  reset: vi.fn(),
  isPending: false,
  isError: false,
  error: undefined,
  variables: undefined,
}));

vi.mock("../events/event-store", () => ({
  useEventStore: () => ({ state: "open", retryRecovery: vi.fn() }),
}));
vi.mock("./active-workspace", () => ({ useActiveWorkspaceId }));
vi.mock("../features/capture/queries", () => ({ useCreateCapture: () => captureMutation }));
vi.mock("./auth-context", () => ({
  useAuth: () => ({
    state: { status: "authenticated", mode: "required", session: { userLabel: "owner" } },
    signOut: vi.fn(),
  }),
}));

import { AppShell } from "./AppShell";

const findKnowledgeMenu = async (): Promise<HTMLElement> => {
  await waitFor(() => expect(document.querySelector('[role="menu"][aria-label="知识菜单"]')).toBeInTheDocument());
  const menu = document.querySelector<HTMLElement>('[role="menu"][aria-label="知识菜单"]');
  if (menu === null) throw new Error("知识菜单未挂载");
  return menu;
};

describe("AppShell", () => {
  beforeEach(() => {
    useActiveWorkspaceId.mockReturnValue("10000000-0000-4000-8000-000000000002");
    captureMutation.mutate.mockReset();
    captureMutation.reset.mockReset();
  });

  it("在懒加载期间保留一级入口，并从知识菜单按分组打开工作区", async () => {
    let resolvePage: ((module: { default: ComponentType }) => void) | undefined;
    const DeferredPage = lazy(() => new Promise<{ default: ComponentType }>((resolve) => {
      resolvePage = resolve;
    }));

    const { container } = render(
      <MemoryRouter initialEntries={["/dashboard"]}>
        <Routes>
          <Route element={<AppShell />}>
            <Route path="/dashboard" element={<DeferredPage />} />
          </Route>
        </Routes>
      </MemoryRouter>,
    );

    expect(container.querySelector(".workbench")).toHaveClass("workbench--dashboard");
    const navigation = screen.getByRole("navigation", { name: "主导航" });
    expect(within(navigation).getByRole("link", { name: "工作台" })).toHaveAttribute("href", "/dashboard");
    expect(within(navigation).getByRole("link", { name: "知识" })).toHaveAttribute("href", "/inbox");
    const knowledgeMenuTrigger = within(navigation).getByRole("button", { name: "打开知识菜单" });
    expect(within(navigation).getByRole("link", { name: "创作" })).toHaveAttribute("href", "/authoring");
    expect(within(navigation).queryByRole("button", { name: "打开创作菜单" })).not.toBeInTheDocument();
    expect(within(navigation).getByRole("link", { name: "设置" })).toHaveAttribute("href", "/settings");
    expect(within(navigation).queryByRole("link", { name: "系统" })).not.toBeInTheDocument();
    expect(within(navigation).queryByRole("link", { name: "工作区" })).not.toBeInTheDocument();
    expect(screen.getByRole("status")).toHaveTextContent("实时同步");
    expect(screen.getByText("已认证 · owner")).toBeInTheDocument();
    expect(screen.getByText("正在加载…")).toBeInTheDocument();

    expect(screen.queryByRole("button", { name: "打开工作台快捷入口" })).not.toBeInTheDocument();
    const topbar = container.querySelector<HTMLElement>(".workbench__topbar");
    expect(topbar).not.toBeNull();
    if (topbar === null) throw new Error("Topbar 未挂载");
    expect(topbar.querySelector('button[aria-haspopup="menu"]')).toBeNull();
    expect(within(topbar).getByRole("button", { name: "快速记录" })).toBeInTheDocument();
    expect(within(topbar).getByRole("button", { name: "退出登录" })).toBeInTheDocument();
    expect(within(topbar).queryByRole("link", { name: "工作区设置" })).not.toBeInTheDocument();
    expect(within(topbar).queryByRole("link", { name: "系统状态" })).not.toBeInTheDocument();
    expect(within(topbar).queryByRole("link", { name: "资料收件箱" })).not.toBeInTheDocument();

    fireEvent.pointerDown(knowledgeMenuTrigger, { button: 0, ctrlKey: false });
    const knowledgeMenu = await findKnowledgeMenu();
    expect(within(knowledgeMenu).getByText("资料")).toBeInTheDocument();
    expect(within(knowledgeMenu).getByText("探索")).toBeInTheDocument();
    expect(within(knowledgeMenu).getByText("组织")).toBeInTheDocument();
    expect(within(knowledgeMenu).getByText("学习")).toBeInTheDocument();
    expect(within(knowledgeMenu).getByRole("menuitem", { name: "资料收件箱", hidden: true })).toHaveAttribute("href", "/inbox");
    expect(within(knowledgeMenu).getByRole("menuitem", { name: "检索", hidden: true })).toHaveAttribute("href", "/search");
    expect(within(knowledgeMenu).getByRole("menuitem", { name: "集合", hidden: true })).toHaveAttribute("href", "/collections");
    expect(within(knowledgeMenu).getByRole("menuitem", { name: "复习", hidden: true })).toHaveAttribute("href", "/review");
    fireEvent.keyDown(knowledgeMenu, { key: "Escape" });
    await waitFor(() => expect(knowledgeMenuTrigger).toHaveFocus());

    act(() => {
      resolvePage?.({ default: () => <p>Lazy route ready</p> });
    });

    expect(await screen.findByText("Lazy route ready")).toBeInTheDocument();
    expect(navigation).toBeInTheDocument();
  });

  it("未连接时只保留工作台和设置，并把根路由归入设置", () => {
    useActiveWorkspaceId.mockReturnValue("");

    render(
      <MemoryRouter initialEntries={["/"]}>
        <Routes>
          <Route element={<AppShell />}>
            <Route path="/" element={<p>Workspace root route</p>} />
          </Route>
        </Routes>
      </MemoryRouter>,
    );

    const navigation = screen.getByRole("navigation", { name: "主导航" });
    expect(within(navigation).getByRole("link", { name: "工作台" })).toHaveAttribute("href", "/dashboard");
    const settingsLink = within(navigation).getByRole("link", { name: "设置" });
    expect(settingsLink).toHaveAttribute("href", "/settings");
    expect(settingsLink).toHaveAttribute("aria-current", "page");
    expect(within(navigation).queryByRole("link", { name: "知识" })).not.toBeInTheDocument();
    expect(within(navigation).queryByRole("link", { name: "创作" })).not.toBeInTheDocument();
    expect(screen.getByRole("status")).toHaveTextContent("等待连接工作区");
    expect(screen.getByText("Workspace root route")).toBeInTheDocument();
  });

  it("未连接的 Dashboard 使用精简图标轨和入口 Topbar", () => {
    useActiveWorkspaceId.mockReturnValue("");

    const { container } = render(
      <MemoryRouter initialEntries={["/dashboard"]}>
        <Routes>
          <Route element={<AppShell />}>
            <Route path="/dashboard" element={<p>Entry dashboard</p>} />
          </Route>
        </Routes>
      </MemoryRouter>,
    );

    expect(container.querySelector(".workbench")).toHaveClass("workbench--dashboard-entry");
    expect(screen.getByRole("link", { name: "知序首页" })).toHaveAttribute("href", "/dashboard");
    const navigation = screen.getByRole("navigation", { name: "主导航" });
    expect(within(navigation).getByRole("link", { name: "工作台" })).toHaveAttribute("aria-current", "page");
    expect(within(navigation).getByRole("link", { name: "设置" })).toHaveAttribute("href", "/settings");
    expect(screen.getByText("知识脉络")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "本地模式" })).toHaveAttribute("href", "/settings?section=workspace");
    expect(screen.queryByRole("status")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "打开主导航" })).not.toBeInTheDocument();
  });

  it("移动导航复用知识菜单，并在关闭后把焦点还给菜单按钮", async () => {
    render(
      <MemoryRouter initialEntries={["/dashboard"]}>
        <Routes>
          <Route element={<AppShell />}>
            <Route path="/dashboard" element={<p>Dashboard</p>} />
          </Route>
        </Routes>
      </MemoryRouter>,
    );

    const trigger = screen.getByRole("button", { name: "打开主导航" });
    fireEvent.click(trigger);

    const sheet = await screen.findByRole("dialog", { name: "主导航" });
    const mobileNavigation = within(sheet).getByRole("navigation", { name: "主导航" });
    expect(within(mobileNavigation).getByRole("link", { name: "设置" })).toBeInTheDocument();
    expect(within(mobileNavigation).getByRole("link", { name: "知识" })).toHaveAttribute("href", "/inbox");
    const knowledgeMenuTrigger = within(mobileNavigation).getByRole("button", { name: "打开知识菜单" });
    fireEvent.pointerDown(knowledgeMenuTrigger, { button: 0, ctrlKey: false });
    fireEvent.keyDown(await findKnowledgeMenu(), { key: "Escape" });
    await waitFor(() => expect(knowledgeMenuTrigger).toHaveFocus());

    fireEvent.keyDown(sheet, { key: "Escape" });
    await waitFor(() => expect(screen.queryByRole("dialog", { name: "主导航" })).not.toBeInTheDocument());
    expect(trigger).toHaveFocus();
  });

  it("子页只高亮所属分组，不把分组入口误报为当前页面", () => {
    render(
      <MemoryRouter initialEntries={["/search"]}>
        <Routes>
          <Route element={<AppShell />}>
            <Route path="/search" element={<p>Search</p>} />
          </Route>
        </Routes>
      </MemoryRouter>,
    );

    const navigation = screen.getByRole("navigation", { name: "主导航" });
    const knowledgeLink = within(navigation).getByRole("link", { name: "知识" });
    expect(knowledgeLink).toHaveClass("rail-link--active");
    expect(knowledgeLink).not.toHaveAttribute("aria-current");
  });

  it("从业务页用快捷键打开快速记录，关闭后把焦点还给原控件", async () => {
    render(
      <MemoryRouter initialEntries={["/search"]}>
        <Routes>
          <Route element={<AppShell />}>
            <Route path="/search" element={<input aria-label="当前检索词" />} />
          </Route>
        </Routes>
      </MemoryRouter>,
    );

    const contextInput = screen.getByRole("textbox", { name: "当前检索词" });
    expect(screen.getByRole("button", { name: "快速记录" })).toBeInTheDocument();
    contextInput.focus();
    fireEvent.keyDown(window, { key: "k", ctrlKey: true, shiftKey: true });

    const dialog = await screen.findByRole("dialog", { name: "快速记录" });
    expect(dialog).toBeInTheDocument();
    await waitFor(() => expect(within(dialog).getByRole("textbox", { name: "记录内容" })).toHaveFocus());
    fireEvent.keyDown(window, { key: "k", ctrlKey: true, shiftKey: true });

    fireEvent.click(within(dialog).getByRole("button", { name: "关闭" }));
    await waitFor(() => expect(screen.queryByRole("dialog", { name: "快速记录" })).not.toBeInTheDocument());
    await waitFor(() => expect(contextInput).toHaveFocus());
  });
});
