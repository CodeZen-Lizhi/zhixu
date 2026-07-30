import { createHash } from "node:crypto";
import { afterEach, describe, expect, it, vi } from "vitest";

import { createAttachmentExport, decodeAttachmentExportJob, downloadAttachmentExport, listAttachmentExports } from "./attachment-exports";

const workspaceId = "72000000-0000-4000-8000-000000000001";
const exportId = "72000000-0000-4000-8000-000000000002";
const hash = "a".repeat(64);
const sha256 = (value: string): string => createHash("sha256").update(value).digest("hex");
const jsonResponse = (value: unknown, init: ResponseInit = {}) => new Response(JSON.stringify(value), { status: 200, headers: { "Content-Type": "application/json" }, ...init });
const pendingJob = {
  id: exportId, workspace_id: workspaceId, scope_kind: "WORKSPACE_ATTACHMENTS", kind: "ATTACHMENTS_ZIP", schema_version: "attachment-export/v1", attachment_root_contract_version: "workspace-attachments/v1", content_policy: "RAW_USER_OWNED",
  status: "PENDING", version: 1, attempt_count: 0, expires_at: "2026-07-31T00:00:00Z", created_at: "2026-07-30T00:00:00Z", updated_at: "2026-07-30T00:00:00Z", download_count: 0,
};
const succeededJob = {
  ...pendingJob, status: "SUCCEEDED", attempt_count: 1, manifest_sha256: hash, entry_count: 2, total_uncompressed_bytes: 13, archive_sha256: "b".repeat(64), archive_size: 29,
  started_at: "2026-07-30T00:00:01Z", completed_at: "2026-07-30T00:00:02Z", updated_at: "2026-07-30T00:00:03Z", download_url: `/api/v1/workspaces/${workspaceId}/attachment-exports/${exportId}/download`,
};

afterEach(() => vi.unstubAllGlobals());

describe("attachment exports API", () => {
  it("creates the fixed tagged command with its Idempotency-Key", async () => {
    const fetchMock = vi.fn<typeof fetch>().mockResolvedValue(jsonResponse({ job: pendingJob, replayed: false, dispatch_pending: true }, { status: 202 }));
    vi.stubGlobal("fetch", fetchMock);

    await expect(createAttachmentExport({ workspaceId, idempotencyKey: "attachment-create-1", expiresInSeconds: 3600 })).resolves.toMatchObject({ job: { id: exportId }, dispatchPending: true });
    expect(fetchMock.mock.calls[0]?.[0]).toBe(`/api/v1/workspaces/${workspaceId}/attachment-exports`);
    const init = fetchMock.mock.calls[0]?.[1];
    if (init === undefined || typeof init.body !== "string") throw new Error("missing attachment create body");
    expect(new Headers(init.headers).get("Idempotency-Key")).toBe("attachment-create-1");
    expect(JSON.parse(init.body)).toEqual({ kind: "ATTACHMENTS_ZIP", schema_version: "attachment-export/v1", attachment_root_contract_version: "workspace-attachments/v1", content_policy: "RAW_USER_OWNED", expires_in_seconds: 3600 });
  });

  it("keeps the list Workspace-bound and rejects a Collection-shaped or cross-Workspace job", async () => {
    const fetchMock = vi.fn<typeof fetch>().mockResolvedValue(jsonResponse({ workspace_id: workspaceId, scope_kind: "WORKSPACE_ATTACHMENTS", items: [pendingJob], next_cursor: "opaque.cursor" }));
    vi.stubGlobal("fetch", fetchMock);
    await expect(listAttachmentExports(workspaceId, { limit: 10 })).resolves.toMatchObject({ nextCursor: "opaque.cursor" });
    expect(fetchMock.mock.calls[0]?.[0]).toBe(`/api/v1/workspaces/${workspaceId}/attachment-exports?limit=10`);

    vi.stubGlobal("fetch", vi.fn<typeof fetch>().mockResolvedValue(jsonResponse({ workspace_id: workspaceId, scope_kind: "WORKSPACE_ATTACHMENTS", items: [{ ...pendingJob, workspace_id: "72000000-0000-4000-8000-000000000099" }] })));
    await expect(listAttachmentExports(workspaceId)).rejects.toMatchObject({ code: "INVALID_RESPONSE" });
    expect(() => decodeAttachmentExportJob({ ...pendingJob, collection_id: "72000000-0000-4000-8000-000000000003" })).toThrow(/job/);
  });

  it("rejects duplicate JSON fields, incomplete prepared bindings and impossible states", async () => {
    expect(() => decodeAttachmentExportJob({ ...succeededJob, archive_size: undefined })).toThrow(/job\.archive_binding/);
    expect(() => decodeAttachmentExportJob({ ...pendingJob, status: "SUCCEEDED" })).toThrow(/job\.result_state/);
    const fetchMock = vi.fn<typeof fetch>().mockResolvedValue(new Response(`{"job":${JSON.stringify(pendingJob)},"job":${JSON.stringify(pendingJob)},"replayed":false,"dispatch_pending":false}`, { status: 202, headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", fetchMock);
    await expect(createAttachmentExport({ workspaceId, idempotencyKey: "attachment-create-2" })).rejects.toMatchObject({ code: "INVALID_RESPONSE" });
  });

  it("downloads only a bound ZIP with the exact protected response headers and Blob length", async () => {
    const payload = "PK\u0003\u0004attachment-export-archive";
    const job = decodeAttachmentExportJob({ ...succeededJob, archive_sha256: sha256(payload), archive_size: payload.length });
    const fetchMock = vi.fn<typeof fetch>().mockResolvedValue(new Response(payload, {
      status: 200,
      headers: { "Content-Type": "application/zip", "Content-Length": String(payload.length), "Content-Disposition": `attachment; filename="workspace-attachments-${exportId}.zip"`, "Cache-Control": "private, no-store", "X-Content-Type-Options": "nosniff" },
    }));
    vi.stubGlobal("fetch", fetchMock);
    await expect(downloadAttachmentExport(workspaceId, job)).resolves.toMatchObject({ filename: `workspace-attachments-${exportId}.zip` });
    expect(fetchMock.mock.calls[0]?.[0]).toBe(job.downloadUrl);

    vi.stubGlobal("fetch", vi.fn<typeof fetch>().mockResolvedValue(new Response(payload, { status: 200, headers: { "Content-Type": "application/zip" } })));
    await expect(downloadAttachmentExport(workspaceId, job)).rejects.toMatchObject({ code: "INVALID_RESPONSE" });
  });

  it("rejects a same-length ZIP whose bytes do not match the frozen SHA-256", async () => {
    const expectedPayload = "PK\u0003\u0004attachment-export-archive";
    const tamperedPayload = "PK\u0003\u0004attachment-export-archivf";
    expect(tamperedPayload).toHaveLength(expectedPayload.length);
    const job = decodeAttachmentExportJob({ ...succeededJob, archive_sha256: sha256(expectedPayload), archive_size: expectedPayload.length });
    vi.stubGlobal("fetch", vi.fn<typeof fetch>().mockResolvedValue(new Response(tamperedPayload, {
      status: 200,
      headers: { "Content-Type": "application/zip", "Content-Length": String(tamperedPayload.length), "Content-Disposition": `attachment; filename="workspace-attachments-${exportId}.zip"`, "Cache-Control": "private, no-store", "X-Content-Type-Options": "nosniff" },
    })));

    await expect(downloadAttachmentExport(workspaceId, job)).rejects.toMatchObject({ code: "INVALID_RESPONSE" });
  });
});
