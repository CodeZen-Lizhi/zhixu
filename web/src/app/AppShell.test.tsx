import { act, fireEvent, render, screen, within } from "@testing-library/react";
import { lazy, type ComponentType } from "react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";

const useActiveWorkspaceId = vi.hoisted(() => vi.fn());

vi.mock("../events/event-store", () => ({
  useEventStore: () => ({ state: "open", retryRecovery: vi.fn() }),
}));
vi.mock("./active-workspace", () => ({
  useActiveWorkspaceId,
}));
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

  it("keeps grouped navigation and context mounted while a lazy route loads", async () => {
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
    expect(within(navigation).getByRole("link", { name: "检索" })).toHaveAttribute("href", "/search");
    expect(within(navigation).getByRole("link", { name: "时间线" })).toHaveAttribute("href", "/timeline");
    expect(within(navigation).getByRole("link", { name: "提案" })).toHaveAttribute("href", "/proposals");
    expect(screen.getByText("资料与知识")).toBeInTheDocument();
    expect(screen.getByText("审阅与产出")).toBeInTheDocument();
    const learningGroup = screen.getByText("学习与研究").closest("details");
    expect(learningGroup).not.toHaveAttribute("open");
    fireEvent.click(screen.getByText("学习与研究"));
    expect(learningGroup).toHaveAttribute("open");
    expect(within(navigation).getByRole("link", { name: "复习" })).toHaveAttribute("href", "/review");
    expect(within(navigation).getByRole("link", { name: "记忆" })).toHaveAttribute("href", "/memories");
    expect(within(navigation).getByRole("link", { name: "访谈" })).toHaveAttribute("href", "/interviews");
    expect(within(navigation).getByRole("link", { name: "对话" })).toHaveAttribute("href", "/chat");
    expect(screen.getByRole("status")).toHaveTextContent("实时同步");
    expect(screen.getByText("已认证 · owner")).toBeInTheDocument();
    expect(screen.getByText("正在加载工作台…")).toBeInTheDocument();

    act(() => {
      resolvePage?.({ default: () => <p>Lazy route ready</p> });
    });

    expect(await screen.findByText("Lazy route ready")).toBeInTheDocument();
    expect(screen.getByRole("navigation", { name: "主导航" })).toBeInTheDocument();
  });

  it("limits unconnected navigation and treats the root route as the Workspace page", () => {
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
    const workspaceLink = within(navigation).getByRole("link", { name: "工作区" });
    expect(workspaceLink).toHaveAttribute("href", "/workspace");
    expect(workspaceLink).toHaveAttribute("aria-current", "page");
    expect(workspaceLink).toHaveClass("rail-link--active");
    expect(within(navigation).getByRole("link", { name: "设置" })).toHaveAttribute("href", "/settings");
    expect(within(navigation).queryByRole("link", { name: "资料收件箱" })).not.toBeInTheDocument();
    expect(within(navigation).queryByRole("link", { name: "知识图谱" })).not.toBeInTheDocument();
    expect(screen.getByRole("status")).toHaveTextContent("等待连接 Workspace");
    expect(within(screen.getByRole("navigation", { name: "面包屑" })).queryByText("详情")).not.toBeInTheDocument();
    expect(screen.getByText("Workspace root route")).toBeInTheDocument();
  });
});
