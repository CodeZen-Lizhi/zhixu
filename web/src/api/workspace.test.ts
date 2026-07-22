import { afterEach, describe, expect, it, vi } from "vitest";

import { decodeWorkspace, decodeWorkspaceScan, getWorkspace, WorkspaceApiError } from "./workspace";

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

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

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

  it("解码 Source Version 与 Content Artifact 身份", () => {
    const id = workspacePayload.id;
    expect(decodeWorkspaceScan({
      workspace_id: id,
      files: [{
        relative_path: "notes/a.md",
        byte_size: 3,
        content_hash: "a".repeat(64),
        media_type: "text/markdown",
        source_id: id,
        source_version_id: id,
        content_artifact_id: id,
        content_artifact_created: true,
      }],
      count: 1,
    }).files[0]).toMatchObject({ sourceVersionId: id, contentArtifactCreated: true });
  });

  it("拒绝与请求身份不一致的 Workspace 响应", async () => {
    const requestedId = workspacePayload.id;
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({
      ...workspacePayload,
      id: "22222222-2222-4222-8222-222222222222",
    }), { status: 200, headers: { "Content-Type": "application/json" } })));

    await expect(getWorkspace(requestedId)).rejects.toMatchObject({ code: "INVALID_RESPONSE" });
  });

  it("在 fetch 和响应体读取阶段保留 AbortError identity", async () => {
    const fetchAbort = new DOMException("fetch aborted", "AbortError");
    vi.stubGlobal("fetch", vi.fn().mockRejectedValue(fetchAbort));
    await expect(getWorkspace(workspacePayload.id)).rejects.toBe(fetchAbort);

    const bodyAbort = new DOMException("body aborted", "AbortError");
    const response = new Response(JSON.stringify(workspacePayload), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    });
    vi.spyOn(response, "json").mockRejectedValue(bodyAbort);
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(response));
    await expect(getWorkspace(workspacePayload.id)).rejects.toBe(bodyAbort);
  });
});
