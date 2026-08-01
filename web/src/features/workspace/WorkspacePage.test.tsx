import { fireEvent, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { activeWorkspaceStorageKey } from "../../app/active-workspace";
import { renderWithAppProviders } from "../../test/render";
import { WorkspacePage } from "./WorkspacePage";

const jsonResponse = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });

const requestURL = (input: RequestInfo | URL) => {
  if (typeof input === "string") return input;
  if (input instanceof URL) return input.href;
  return input.url;
};

const workspace = {
  id: "11111111-1111-4111-8111-111111111111",
  name: "知识库",
  root_path: "/tmp/knowledge",
  status: "active",
  version: 1,
  git: {
    present: true,
    repository_path: "/tmp/knowledge",
    branch: "main",
    head: "abcdef",
    dirty: true,
    checked_at: "2026-07-16T10:00:00Z",
  },
  warnings: ["GIT_WORKTREE_DIRTY"],
  created_at: "2026-07-16T10:00:00Z",
  updated_at: "2026-07-16T10:00:00Z",
};

describe("WorkspacePage", () => {
  beforeEach(() => {
    window.localStorage.clear();
    vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL) => Promise.reject(new Error(`unexpected request: ${requestURL(input)}`))));
  });

  it("每次只展示一种连接方式", () => {
    renderWithAppProviders(<WorkspacePage />);

    expect(screen.getByRole("heading", { name: "连接工作区", level: 1 })).toBeInTheDocument();
    expect(screen.getByLabelText("名称")).toBeInTheDocument();
    expect(screen.getByLabelText("宿主机目录")).toBeInTheDocument();
    expect(screen.queryByLabelText("Workspace ID")).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "使用 Workspace ID" }));

    expect(screen.getByLabelText("Workspace ID")).toBeInTheDocument();
    expect(screen.queryByLabelText("名称")).not.toBeInTheDocument();
    expect(screen.queryByLabelText("宿主机目录")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /扫描/ })).not.toBeInTheDocument();
  });

  it("创建工作区后保存活动 ID", async () => {
    vi.mocked(fetch).mockImplementation((input: RequestInfo | URL) => {
      const url = requestURL(input);
      if (url.endsWith("/api/v1/workspaces")) return Promise.resolve(jsonResponse(workspace, 201));
      return Promise.reject(new Error(`unexpected request: ${url}`));
    });

    renderWithAppProviders(<WorkspacePage />);
    fireEvent.change(screen.getByLabelText("名称"), { target: { value: "知识库" } });
    fireEvent.change(screen.getByLabelText("宿主机目录"), { target: { value: "/tmp/knowledge" } });
    fireEvent.click(screen.getByRole("button", { name: "创建工作区" }));

    await waitFor(() => expect(window.localStorage.getItem(activeWorkspaceStorageKey)).toBe(workspace.id));
    expect(fetch).toHaveBeenCalledTimes(1);
    expect(screen.queryByRole("button", { name: /扫描/ })).not.toBeInTheDocument();
  });

  it("使用 Workspace ID 连接后保存活动 ID", async () => {
    vi.mocked(fetch).mockImplementation((input: RequestInfo | URL) => {
      const url = requestURL(input);
      if (url.endsWith(`/api/v1/workspaces/${workspace.id}`)) return Promise.resolve(jsonResponse(workspace));
      return Promise.reject(new Error(`unexpected request: ${url}`));
    });

    renderWithAppProviders(<WorkspacePage />);
    fireEvent.click(screen.getByRole("button", { name: "使用 Workspace ID" }));
    fireEvent.change(screen.getByLabelText("Workspace ID"), { target: { value: workspace.id } });
    fireEvent.click(screen.getByRole("button", { name: "打开工作区" }));

    await waitFor(() => expect(window.localStorage.getItem(activeWorkspaceStorageKey)).toBe(workspace.id));
    expect(fetch).toHaveBeenCalledTimes(1);
  });
});
