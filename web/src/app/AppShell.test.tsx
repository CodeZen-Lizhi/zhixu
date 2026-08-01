import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { lazy, type ComponentType } from "react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";

const useActiveWorkspaceId = vi.hoisted(() => vi.fn());

vi.mock("../events/event-store", () => ({
  useEventStore: () => ({ state: "open", retryRecovery: vi.fn() }),
}));
vi.mock("./active-workspace", () => ({ useActiveWorkspaceId }));
vi.mock("./auth-context", () => ({
  useAuth: () => ({
    state: { status: "authenticated", mode: "required", session: { userLabel: "owner" } },
    signOut: vi.fn(),
  }),
}));

import { AppShell } from "./AppShell";

describe("AppShell", () => {
  beforeEach(() => {
    useActiveWorkspaceId.mockReturnValue("10000000-0000-4000-8000-000000000002");
  });

  it("在懒加载期间保留四个一级概念和分组菜单", async () => {
    let resolvePage: ((module: { default: ComponentType }) => void) | undefined;
    const DeferredPage = lazy(() => new Promise<{ default: ComponentType }>((resolve) => {
      resolvePage = resolve;
    }));

    render(
      <MemoryRouter initialEntries={["/dashboard"]}>
        <Routes>
          <Route element={<AppShell />}>
            <Route path="/dashboard" element={<DeferredPage />} />
          </Route>
        </Routes>
      </MemoryRouter>,
    );

    const navigation = screen.getByRole("navigation", { name: "主导航" });
    expect(within(navigation).getByRole("link", { name: "工作台" })).toHaveAttribute("href", "/dashboard");
    expect(within(navigation).getByRole("link", { name: "知识" })).toHaveAttribute("href", "/inbox");
    expect(within(navigation).getByRole("link", { name: "产出" })).toHaveAttribute("href", "/proposals");
    expect(within(navigation).getByRole("link", { name: "设置" })).toHaveAttribute("href", "/settings");
    expect(within(navigation).queryByRole("link", { name: "系统" })).not.toBeInTheDocument();
    expect(within(navigation).queryByRole("link", { name: "工作区" })).not.toBeInTheDocument();
    expect(screen.getByRole("status")).toHaveTextContent("实时同步");
    expect(screen.getByText("已认证 · owner")).toBeInTheDocument();
    expect(screen.getByText("正在加载…")).toBeInTheDocument();

    fireEvent.pointerDown(within(navigation).getByRole("button", { name: "打开知识菜单" }), { button: 0, ctrlKey: false });
    const knowledgeMenu = await screen.findByRole("menu");
    expect(knowledgeMenu).toHaveAttribute("aria-label", "知识菜单");
    expect(within(knowledgeMenu).getByText("资料")).toBeInTheDocument();
    expect(within(knowledgeMenu).getByText("探索")).toBeInTheDocument();
    expect(within(knowledgeMenu).getByText("组织")).toBeInTheDocument();
    expect(within(knowledgeMenu).getByText("学习")).toBeInTheDocument();
    expect(within(knowledgeMenu).getByRole("menuitem", { name: "资料收件箱" })).toBeInTheDocument();
    expect(within(knowledgeMenu).queryByRole("menuitem", { name: "资料版本" })).not.toBeInTheDocument();
    expect(within(knowledgeMenu).getByRole("menuitem", { name: "知识图谱" })).toBeInTheDocument();

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
    expect(within(navigation).queryByRole("link", { name: "产出" })).not.toBeInTheDocument();
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

  it("移动导航可展开知识分组，并在关闭后把焦点还给菜单按钮", async () => {
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

    fireEvent.click(within(mobileNavigation).getByRole("button", { name: "展开知识菜单" }));
    expect(within(mobileNavigation).getByRole("button", { name: "收起知识菜单" })).toHaveAttribute("aria-expanded", "true");
    expect(within(mobileNavigation).getByRole("link", { name: "检索" })).toHaveAttribute("href", "/search");

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
});
