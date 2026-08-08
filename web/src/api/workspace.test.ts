import { afterEach, describe, expect, it, vi } from "vitest";

import { decodeWorkspaceScan, scanWorkspace, WorkspaceApiError } from "./workspace";

const workspaceId = "11111111-1111-4111-8111-111111111111";

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("Workspace API decoders", () => {
  it("校验扫描数量与文件列表一致", () => {
    expect(() => decodeWorkspaceScan({
      workspace_id: workspaceId,
      files: [],
      count: 1,
    })).toThrow(WorkspaceApiError);
  });

  it("解码 Source Version 与 Content Artifact 身份", () => {
    const id = workspaceId;
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

  it("扫描请求只提交服务端 Active Workspace ID", async () => {
    const fetcher = vi.fn().mockResolvedValue(new Response(JSON.stringify({
      workspace_id: workspaceId,
      files: [],
      count: 0,
    }), { status: 200, headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", fetcher);

    await expect(scanWorkspace(workspaceId)).resolves.toMatchObject({ workspaceId, count: 0 });
    expect(fetcher).toHaveBeenCalledWith(
      expect.stringContaining(`/api/v1/workspaces/${workspaceId}/scan`),
      expect.objectContaining({ method: "POST" }),
    );
  });

  it("在 fetch 和响应体读取阶段保留 AbortError identity", async () => {
    const fetchAbort = new DOMException("fetch aborted", "AbortError");
    vi.stubGlobal("fetch", vi.fn().mockRejectedValue(fetchAbort));
    await expect(scanWorkspace(workspaceId)).rejects.toBe(fetchAbort);

    const bodyAbort = new DOMException("body aborted", "AbortError");
    const response = new Response(JSON.stringify({ workspace_id: workspaceId, files: [], count: 0 }), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    });
    vi.spyOn(response, "json").mockRejectedValue(bodyAbort);
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(response));
    await expect(scanWorkspace(workspaceId)).rejects.toBe(bodyAbort);
  });
});
