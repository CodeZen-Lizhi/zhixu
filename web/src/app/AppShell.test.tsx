import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { lazy, useState, type ComponentType, type ReactNode } from "react";
import { Link, MemoryRouter, Route, Routes, useLocation } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";

const useActiveWorkspaceId = vi.hoisted(() => vi.fn());
const signOut = vi.hoisted(() => vi.fn());
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
    signOut,
  }),
}));

import { AppShell } from "./AppShell";
import { reportCaughtRouteError } from "../routes/RouteContentBoundary";

describe("AppShell", () => {
  beforeEach(() => {
    useActiveWorkspaceId.mockReturnValue("10000000-0000-4000-8000-000000000002");
    captureMutation.mutate.mockReset();
    captureMutation.reset.mockReset();
    signOut.mockReset().mockResolvedValue(undefined);
  });

  it("在懒加载期间保留一级入口，并以内联三级结构展示知识导航", async () => {
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
    expect(within(navigation).queryByRole("link", { name: "知识" })).not.toBeInTheDocument();
    const knowledgeDisclosure = within(navigation).getByRole("button", { name: "知识" });
    expect(knowledgeDisclosure).toHaveAttribute("aria-expanded", "true");
    const knowledgeGroups = document.getElementById(knowledgeDisclosure.getAttribute("aria-controls") ?? "");
    expect(knowledgeGroups).toBeInTheDocument();
    expect(knowledgeGroups).not.toHaveAttribute("hidden");
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

    const groups = [
      { name: "资料", iconClass: "lucide-folder-open" },
      { name: "探索", iconClass: "lucide-compass" },
      { name: "组织", iconClass: "lucide-network" },
      { name: "学习", iconClass: "lucide-graduation-cap" },
    ];
    groups.forEach(({ name, iconClass }) => {
      const disclosure = within(navigation).getByRole("button", { name });
      expect(disclosure).toHaveAttribute("aria-expanded", "false");
      expect(disclosure.querySelector(".rail-disclosure__group-icon")).toHaveClass(iconClass);
      const destinations = document.getElementById(disclosure.getAttribute("aria-controls") ?? "");
      expect(destinations).toHaveAttribute("hidden");
    });
    expect(within(navigation).queryByRole("link", { name: "资料收件箱" })).not.toBeInTheDocument();
    fireEvent.click(within(navigation).getByRole("button", { name: "资料" }));
    expect(within(navigation).getByRole("link", { name: "资料收件箱" })).toHaveAttribute("href", "/inbox");
    expect(document.querySelector('[role="menu"][aria-label="知识菜单"]')).not.toBeInTheDocument();

    fireEvent.click(knowledgeDisclosure);
    expect(knowledgeDisclosure).toHaveAttribute("aria-expanded", "false");
    expect(within(navigation).queryByRole("button", { name: "资料" })).not.toBeInTheDocument();
    knowledgeDisclosure.focus();
    expect(knowledgeDisclosure).toHaveFocus();
    fireEvent.click(knowledgeDisclosure);
    expect(knowledgeDisclosure).toHaveAttribute("aria-expanded", "true");

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

  it("移动导航复用知识层级，选择入口后关闭，并在 Escape 后恢复焦点", async () => {
    render(
      <MemoryRouter initialEntries={["/dashboard"]}>
        <Routes>
          <Route element={<AppShell />}>
            <Route path="*" element={<p>Dashboard</p>} />
          </Route>
        </Routes>
      </MemoryRouter>,
    );

    const trigger = screen.getByRole("button", { name: "打开主导航" });
    fireEvent.click(trigger);

    const sheet = await screen.findByRole("dialog", { name: "主导航" });
    const mobileNavigation = within(sheet).getByRole("navigation", { name: "主导航" });
    expect(within(mobileNavigation).getByRole("link", { name: "设置" })).toBeInTheDocument();
    const knowledgeDisclosure = within(mobileNavigation).getByRole("button", { name: "知识" });
    expect(knowledgeDisclosure).toHaveAttribute("aria-expanded", "true");
    const organizeDisclosure = within(mobileNavigation).getByRole("button", { name: "组织" });
    fireEvent.click(organizeDisclosure);
    expect(organizeDisclosure).toHaveAttribute("aria-expanded", "true");

    fireEvent.keyDown(sheet, { key: "Escape" });
    await waitFor(() => expect(screen.queryByRole("dialog", { name: "主导航" })).not.toBeInTheDocument());
    expect(trigger).toHaveFocus();

    fireEvent.click(trigger);
    const reopenedSheet = await screen.findByRole("dialog", { name: "主导航" });
    const reopenedNavigation = within(reopenedSheet).getByRole("navigation", { name: "主导航" });
    expect(within(reopenedNavigation).getByRole("button", { name: "组织" })).toHaveAttribute("aria-expanded", "true");
    expect(within(reopenedNavigation).getByRole("link", { name: "集合" })).toBeInTheDocument();
    fireEvent.click(within(reopenedNavigation).getByRole("button", { name: "资料" }));
    fireEvent.click(within(reopenedNavigation).getByRole("link", { name: "资料收件箱" }));
    await waitFor(() => expect(screen.queryByRole("dialog", { name: "主导航" })).not.toBeInTheDocument());
  });

  it("标识三级当前状态，并在路由变化时追加展开所属分组", async () => {
    render(
      <MemoryRouter initialEntries={["/search"]}>
        <Routes>
          <Route element={<AppShell />}>
            <Route path="*" element={<Link to="/review">切换到复习</Link>} />
          </Route>
        </Routes>
      </MemoryRouter>,
    );

    const navigation = screen.getByRole("navigation", { name: "主导航" });
    const knowledgeDisclosure = within(navigation).getByRole("button", { name: "知识" });
    const exploreDisclosure = within(navigation).getByRole("button", { name: "探索" });
    const organizeDisclosure = within(navigation).getByRole("button", { name: "组织" });
    const learningDisclosure = within(navigation).getByRole("button", { name: "学习" });
    expect(knowledgeDisclosure).toHaveClass("rail-disclosure--current-path");
    expect(knowledgeDisclosure).not.toHaveClass("rail-link--active");
    expect(knowledgeDisclosure).toHaveAttribute("aria-current", "true");
    expect(exploreDisclosure).toHaveClass("rail-disclosure--current-path");
    expect(exploreDisclosure).not.toHaveClass("rail-link--active");
    expect(exploreDisclosure).toHaveAttribute("aria-expanded", "true");
    expect(within(navigation).getByRole("link", { name: "检索" })).toHaveAttribute("aria-current", "page");

    fireEvent.click(organizeDisclosure);
    expect(organizeDisclosure).toHaveAttribute("aria-expanded", "true");
    expect(within(navigation).getByRole("link", { name: "集合" })).toBeInTheDocument();
    fireEvent.click(organizeDisclosure);
    expect(organizeDisclosure).toHaveAttribute("aria-expanded", "false");
    expect(exploreDisclosure).toHaveAttribute("aria-expanded", "true");
    fireEvent.click(organizeDisclosure);

    fireEvent.click(screen.getByRole("link", { name: "切换到复习" }));
    await waitFor(() => expect(learningDisclosure).toHaveAttribute("aria-expanded", "true"));
    expect(within(navigation).getByRole("link", { name: "复习" })).toHaveAttribute("aria-current", "page");
    expect(learningDisclosure).toHaveClass("rail-disclosure--current-path");
    expect(exploreDisclosure).toHaveAttribute("aria-expanded", "true");
    expect(organizeDisclosure).toHaveAttribute("aria-expanded", "true");
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

  it("页面崩溃后仍可退出登录和导航，切换路由清除错误", async () => {
    vi.spyOn(console, "error").mockImplementation(() => undefined);
    const BrokenPage = (): ReactNode => { throw new Error("render failed"); };
    render(
      <MemoryRouter initialEntries={["/search"]}>
        <Routes>
          <Route element={<AppShell />}>
            <Route path="/search" element={<BrokenPage />} />
            <Route path="/settings" element={<p>工作区设置已打开</p>} />
          </Route>
        </Routes>
      </MemoryRouter>,
      { onCaughtError: reportCaughtRouteError },
    );

    expect(screen.getByRole("alert", { name: "页面暂时无法显示" })).toBeInTheDocument();
    const navigation = screen.getByRole("navigation", { name: "主导航" });
    expect(screen.getByText("已认证 · owner")).toBeInTheDocument();
    expect(screen.getByRole("status")).toHaveTextContent("实时同步");
    expect(screen.getByRole("button", { name: "快速记录" })).toBeEnabled();
    fireEvent.click(screen.getByRole("button", { name: "退出登录" }));
    await waitFor(() => expect(signOut).toHaveBeenCalledTimes(1));

    fireEvent.click(within(navigation).getByRole("link", { name: "设置" }));

    expect(await screen.findByText("工作区设置已打开")).toBeInTheDocument();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    expect(navigation).toBeInTheDocument();
  });

  it("同一路由切换 Workspace 后不保留旧工作区的错误", () => {
    vi.spyOn(console, "error").mockImplementation(() => undefined);
    const nextWorkspace = "10000000-0000-4000-8000-000000000003";
    const WorkspacePage = () => {
      const workspaceId: unknown = useActiveWorkspaceId();
      if (workspaceId !== nextWorkspace) throw new Error("previous workspace failed");
      return <p>新工作区页面</p>;
    };
    const app = <MemoryRouter initialEntries={["/search"]}>
      <Routes><Route element={<AppShell />}><Route path="/search" element={<WorkspacePage />} /></Route></Routes>
    </MemoryRouter>;
    const { rerender } = render(app, { onCaughtError: reportCaughtRouteError });
    expect(screen.getByRole("alert", { name: "页面暂时无法显示" })).toBeInTheDocument();

    useActiveWorkspaceId.mockReturnValue(nextWorkspace);
    rerender(<MemoryRouter initialEntries={["/search"]}>
      <Routes><Route element={<AppShell />}><Route path="/search" element={<WorkspacePage />} /></Route></Routes>
    </MemoryRouter>);

    expect(screen.getByText("新工作区页面")).toBeInTheDocument();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    expect(screen.getByText("已认证 · owner")).toBeInTheDocument();
  });

  it("正常页面改变 URL 筛选时保留尚未提交的本地输入", async () => {
    const DraftPage = () => {
      const [draft, setDraft] = useState("");
      const location = useLocation();
      return <>
        <input aria-label="未提交的输入" value={draft} onChange={(event) => setDraft(event.target.value)} />
        <Link to="/search?mode=hybrid">切换检索模式</Link>
        <output>{location.search}</output>
      </>;
    };
    render(<MemoryRouter initialEntries={["/search"]}>
      <Routes><Route element={<AppShell />}><Route path="/search" element={<DraftPage />} /></Route></Routes>
    </MemoryRouter>);

    fireEvent.change(screen.getByRole("textbox", { name: "未提交的输入" }), { target: { value: "保留草稿" } });
    fireEvent.click(screen.getByRole("link", { name: "切换检索模式" }));

    expect(await screen.findByText("?mode=hybrid")).toBeInTheDocument();
    expect(screen.getByRole("textbox", { name: "未提交的输入" })).toHaveValue("保留草稿");
  });
});
