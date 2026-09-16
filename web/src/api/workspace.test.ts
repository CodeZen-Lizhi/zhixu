import { afterEach, describe, expect, it, vi } from "vitest";

import { decodeDiscoveryFailures, decodeWorkspaceScan, scanWorkspace, WorkspaceApiError } from "./workspace";

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

describe("discovery failures", () => {
  const workspaceId = "11111111-1111-4111-8111-111111111111";
  const item = { workspace_id: workspaceId, binding_version: 1, path: "notes/unreadable.md", stage: "OBSERVE", code: "FILE_OBSERVATION_FAILED", status: "FAILED", failure_count: 2, last_failed_at: "2026-09-15T08:00:00Z", recovered_at: null };
  const page = { workspace_id: workspaceId, binding_version: 1, items: [item], next_cursor: "" };
  it("decodes failed and recovered records with exact scope", () => {
    expect(decodeDiscoveryFailures(page, workspaceId).items[0]?.status).toBe("FAILED");
    expect(decodeDiscoveryFailures({ ...page, items: [{ ...item, status: "RECOVERED", recovered_at: "2026-09-15T08:01:00Z" }] }, workspaceId).items[0]?.status).toBe("RECOVERED");
  });
  it("rejects cross-workspace, unsafe paths, malformed state and extra fields", () => {
    for (const bad of [
      { ...page, workspace_id: "22222222-2222-4222-8222-222222222222" },
      { ...page, extra: true },
      ...[{ path: "/private/secret.md" }, { path: "../secret.md" }, { path: ".knowledge/private.md" }, { binding_version: 2 }, { status: "RECOVERED" }, { code: "RAW_ERROR" }, { recovered_at: "2026-09-15T08:01:00Z" }].map((patch) => ({ ...page, items: [{ ...item, ...patch }] })),
    ]) expect(() => decodeDiscoveryFailures(bad, workspaceId)).toThrow();
  });
});
