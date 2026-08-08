import { fireEvent, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, type Mock, vi } from "vitest";

import type { ActiveWorkspace } from "../../api/active-workspace";

interface ActiveWorkspaceMock {
  status: "loading" | "ready" | "unavailable" | "error";
  workspace: ActiveWorkspace | undefined;
  error: Error | undefined;
  refresh: Mock<() => Promise<void>>;
}

const fixtures = vi.hoisted((): { workspace: ActiveWorkspace; active: ActiveWorkspaceMock } => {
  const workspace: ActiveWorkspace = {
    id: "11111111-1111-4111-8111-111111111111",
    name: "知识库",
    rootPath: "/tmp/knowledge",
    status: "active",
    availability: "available",
    version: 3,
  };
  return {
    workspace,
    active: {
      status: "ready",
      workspace,
      error: undefined,
      refresh: vi.fn(() => Promise.resolve()),
    },
  };
});
const { active, workspace: workspaceFixture } = fixtures;

vi.mock("../../app/WorkspaceCacheBoundary", () => ({ useActiveWorkspace: () => active }));

import { renderWithAppProviders } from "../../test/render";
import { WorkspacePage } from "./WorkspacePage";

beforeEach(() => {
  active.status = "ready";
  active.workspace = { ...workspaceFixture, availability: "available" };
  active.error = undefined;
  active.refresh.mockClear();
});

describe("WorkspacePage", () => {
  it("只读展示服务端 Active Workspace 与本机切换命令", () => {
    renderWithAppProviders(<WorkspacePage />);

    expect(screen.getByRole("heading", { name: "当前 Workspace", level: 1 })).toBeInTheDocument();
    expect(screen.getByText("知识库")).toBeInTheDocument();
    expect(screen.getByText("/tmp/knowledge")).toBeInTheDocument();
    expect(screen.getByText(workspaceFixture.id)).toBeInTheDocument();
    expect(screen.getByText("./zhixu up --workspace <宿主机绝对目录>")).toBeInTheDocument();
    expect(screen.getByText("./zhixu workspace switch <宿主机绝对目录>")).toBeInTheDocument();
    expect(screen.queryByLabelText("宿主机目录")).not.toBeInTheDocument();
    expect(screen.queryByLabelText("Workspace ID")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /创建|打开工作区/ })).not.toBeInTheDocument();
  });

  it("不可用时保留只读身份并提供重新检查", () => {
    active.status = "unavailable";
    active.workspace = { ...workspaceFixture, availability: "unavailable" };
    renderWithAppProviders(<WorkspacePage />);

    expect(screen.getByRole("alert")).toHaveTextContent("当前目录暂不可用于业务请求");
    expect(screen.getByText(workspaceFixture.id)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "重新检查" }));
    expect(active.refresh).toHaveBeenCalledOnce();
  });

  it("Active API 失败时显示服务端错误且不恢复本地 Workspace", () => {
    active.status = "error";
    active.error = new Error("active unavailable");
    active.workspace = undefined;
    renderWithAppProviders(<WorkspacePage />);

    expect(screen.getByRole("alert")).toHaveTextContent("active unavailable");
    expect(screen.queryByText(workspaceFixture.id)).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "重新读取" }));
    expect(active.refresh).toHaveBeenCalledOnce();
  });
});
