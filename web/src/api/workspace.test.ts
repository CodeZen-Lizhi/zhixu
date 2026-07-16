import { describe, expect, it } from "vitest";

import { decodeWorkspace, decodeWorkspaceScan, WorkspaceApiError } from "./workspace";

const workspacePayload = {
  id: "11111111-1111-4111-8111-111111111111",
  name: "知识库",
  root_path: "/tmp/knowledge",
  status: "active",
  version: 1,
  git: {
    present: true,
    repository_path: "/tmp/knowledge",
    branch: "main",
    head: "abc",
    dirty: false,
    checked_at: "2026-07-16T10:00:00Z",
  },
  warnings: [],
  created_at: "2026-07-16T10:00:00Z",
  updated_at: "2026-07-16T10:00:00Z",
};

describe("Workspace API decoders", () => {
  it("将 Workspace API 契约映射为前端模型", () => {
    expect(decodeWorkspace(workspacePayload)).toMatchObject({
      name: "知识库",
      rootPath: "/tmp/knowledge",
      git: { branch: "main", dirty: false },
    });
  });

  it("拒绝未知 Workspace 状态", () => {
    expect(() => decodeWorkspace({ ...workspacePayload, status: "disabled" })).toThrow(WorkspaceApiError);
  });

  it("校验扫描数量与文件列表一致", () => {
    expect(() => decodeWorkspaceScan({
      workspace_id: workspacePayload.id,
      files: [],
      count: 1,
    })).toThrow(WorkspaceApiError);
  });
});
