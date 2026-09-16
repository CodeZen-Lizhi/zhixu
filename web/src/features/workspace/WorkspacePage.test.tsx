import { fireEvent, screen, waitFor } from "@testing-library/react";
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

const discovery = vi.hoisted(() => ({ list: vi.fn(), scan: vi.fn() }));
vi.mock("../../api/workspace", () => ({ listDiscoveryFailures: discovery.list, scanWorkspace: discovery.scan }));
vi.mock("../../app/WorkspaceCacheBoundary", () => ({ useActiveWorkspace: () => active }));

import { renderWithAppProviders } from "../../test/render";
import { WorkspacePage } from "./WorkspacePage";

beforeEach(() => {
  discovery.list.mockReset(); discovery.scan.mockReset();
  discovery.list.mockResolvedValue({ workspace_id: workspaceFixture.id, binding_version: 1, items: [], next_cursor: "" });
  discovery.scan.mockResolvedValue({ workspaceId: workspaceFixture.id, files: [], count: 0 });
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
    expect(screen.getByText("已连接")).toBeInTheDocument();
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

it("刷新服务端失败记录并在重新扫描后显示恢复", async () => {
  const item = { workspace_id: workspaceFixture.id, binding_version: 1, path: "unreadable.md", stage: "OBSERVE", code: "FILE_OBSERVATION_FAILED", status: "FAILED", failure_count: 1, last_failed_at: "2026-09-15T08:00:00Z", recovered_at: null };
  discovery.list.mockResolvedValue({ workspace_id: workspaceFixture.id, binding_version: 1, items: [item], next_cursor: "" });
  renderWithAppProviders(<WorkspacePage />);
  expect(await screen.findByText("unreadable.md")).toBeInTheDocument();
  expect(screen.getByText("待处理")).toBeInTheDocument();
  fireEvent.click(screen.getByRole("button", { name: "刷新记录" }));
  await waitFor(() => expect(discovery.list).toHaveBeenCalledTimes(2));
  discovery.list.mockResolvedValue({ workspace_id: workspaceFixture.id, binding_version: 1, items: [{ ...item, status: "RECOVERED", recovered_at: "2026-09-15T08:01:00Z" }], next_cursor: "" });
  fireEvent.click(screen.getByRole("button", { name: "重新扫描" }));
  expect(await screen.findByText("已恢复")).toBeInTheDocument();
  expect(discovery.scan).toHaveBeenCalledWith(workspaceFixture.id);
});


it("丢弃不同目录绑定的分页并从首页刷新", async () => {
  const item = { workspace_id: workspaceFixture.id, binding_version: 1, path: "old-root.md", stage: "OBSERVE", code: "FILE_OBSERVATION_FAILED", status: "FAILED", failure_count: 1, last_failed_at: "2026-09-15T08:00:00Z", recovered_at: null };
  discovery.list.mockResolvedValueOnce({ workspace_id: workspaceFixture.id, binding_version: 1, items: [item], next_cursor: "old-root.md" });
  discovery.list.mockResolvedValueOnce({ workspace_id: workspaceFixture.id, binding_version: 2, items: [{ ...item, binding_version: 2, path: "new-root-page2.md" }], next_cursor: "new-root-page2.md" });
  renderWithAppProviders(<WorkspacePage />);
  expect(await screen.findByText("old-root.md")).toBeInTheDocument();
  fireEvent.click(screen.getByRole("button", { name: "更多记录" }));
  expect(await screen.findByRole("alert")).toHaveTextContent("目录绑定已变更");
  expect(screen.queryByText("old-root.md")).not.toBeInTheDocument();
  expect(screen.queryByText("new-root-page2.md")).not.toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "更多记录" })).not.toBeInTheDocument();
  expect(screen.queryByText("当前根目录暂无已记录的扫描失败。")).not.toBeInTheDocument();
  discovery.list.mockResolvedValue({ workspace_id: workspaceFixture.id, binding_version: 2, items: [{ ...item, binding_version: 2, path: "new-root-page1.md" }], next_cursor: "" });
  fireEvent.click(screen.getByRole("button", { name: "刷新记录" }));
  expect(await screen.findByText("new-root-page1.md")).toBeInTheDocument();
  expect(discovery.list).toHaveBeenLastCalledWith(workspaceFixture.id, "", expect.any(AbortSignal));
  expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  expect(screen.queryByText("old-root.md")).not.toBeInTheDocument();
});
