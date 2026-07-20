import { fireEvent, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

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
    const values = new Map<string, string>();
    Object.defineProperty(window, "localStorage", {
      configurable: true,
      value: {
        clear: () => values.clear(),
        getItem: (key: string) => values.get(key) ?? null,
        removeItem: (key: string) => values.delete(key),
        setItem: (key: string, value: string) => values.set(key, value),
      },
    });
    window.localStorage.clear();
    vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL) => {
      const url = requestURL(input);
      if (url.endsWith("/api/v1/system/status")) {
        return Promise.resolve(jsonResponse({ status: "ready", version: "dev", database: { status: "ready" }, graph: { status: "ready" }, rag: { status: "disabled" }, request_id: "req" }));
      }
      return Promise.reject(new Error(`unexpected request: ${url}`));
    }));
  });

  it("创建 Workspace 后显示真实 Git Dirty 警告", async () => {
    vi.mocked(fetch).mockImplementation((input: RequestInfo | URL) => {
      const url = requestURL(input);
      if (url.endsWith("/api/v1/system/status")) {
        return Promise.resolve(jsonResponse({ status: "ready", version: "dev", database: { status: "ready" }, graph: { status: "ready" }, rag: { status: "disabled" }, request_id: "req" }));
      }
      if (url.endsWith("/api/v1/workspaces")) return Promise.resolve(jsonResponse(workspace, 201));
      return Promise.reject(new Error(`unexpected request: ${url}`));
    });

    renderWithAppProviders(<WorkspacePage />);
    fireEvent.change(screen.getByLabelText("Workspace 名称"), { target: { value: "知识库" } });
    fireEvent.change(screen.getByLabelText("根目录绝对路径"), { target: { value: "/tmp/knowledge" } });
    fireEvent.click(screen.getByRole("button", { name: "创建 Workspace" }));

    expect(await screen.findByText("Git Dirty 警告")).toBeInTheDocument();
    expect(screen.getByText("/tmp/knowledge")).toBeInTheDocument();
  });

  it("扫描后以表格展示文件哈希并明确不是导入完成", async () => {
    window.localStorage.setItem("zhixu.active-workspace-id", workspace.id);
    vi.mocked(fetch).mockImplementation((input: RequestInfo | URL) => {
      const url = requestURL(input);
      if (url.endsWith("/api/v1/system/status")) {
        return Promise.resolve(jsonResponse({ status: "ready", version: "dev", database: { status: "ready" }, graph: { status: "ready" }, rag: { status: "disabled" }, request_id: "req" }));
      }
      if (url.endsWith(`/api/v1/workspaces/${workspace.id}/scan`)) {
        return Promise.resolve(jsonResponse({
          workspace_id: workspace.id,
          count: 1,
          files: [{ relative_path: "notes/a.md", byte_size: 12, content_hash: "a".repeat(64), media_type: "text/markdown", source_id: workspace.id, source_version_id: workspace.id, content_artifact_id: workspace.id, content_artifact_created: true }],
        }));
      }
      if (url.endsWith(`/api/v1/workspaces/${workspace.id}`)) return Promise.resolve(jsonResponse(workspace));
      return Promise.reject(new Error(`unexpected request: ${url}`));
    });

    renderWithAppProviders(<WorkspacePage />);
    fireEvent.click(await screen.findByRole("button", { name: "扫描受支持文件" }));

    expect(await screen.findByRole("cell", { name: "notes/a.md" })).toBeInTheDocument();
    expect(screen.getByText("扫描会把原始字节捕获到受管不可变存储，不修改源文件，也不代表已完成解析或索引。")).toBeInTheDocument();
  });
});
