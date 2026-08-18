import { afterEach, describe, expect, it, vi } from "vitest";

import { createExport, decodeJob, downloadExport, listCollectionExports } from "./exports";

const workspaceId = "72000000-0000-4000-8000-000000000001";
const collectionId = "72000000-0000-4000-8000-000000000002";
const exportId = "72000000-0000-4000-8000-000000000003";
const hash = "a".repeat(64);
const jsonResponse = (value: unknown, init: ResponseInit = {}) => new Response(JSON.stringify(value), { status: 200, headers: { "Content-Type": "application/json" }, ...init });
const pendingJob = {
  id: exportId, version: 1, workspace_id: workspaceId, kind: "MARKDOWN", schema_version: "export/v1", collection_id: collectionId, collection_version: 2, query_hash: hash,
  fields: ["object_type", "id", "title"], redaction_policy: "MASKED", include_sensitive: false, status: "PENDING", file_size: 0, attempt_count: 0,
  expires_at: "2026-07-27T00:00:00Z", created_at: "2026-07-26T00:00:00Z", updated_at: "2026-07-26T00:00:00Z", download_count: 0,
};
const succeededJob = {
  ...pendingJob, status: "SUCCEEDED", file_hash: "b".repeat(64), file_size: 12, attempt_count: 1,
  started_at: "2026-07-26T00:00:01Z", completed_at: "2026-07-26T00:00:02Z", updated_at: "2026-07-26T00:00:04Z", read_model_revision: "c".repeat(64), exact_count: 1, download_url: `/api/v1/exports/${exportId}/download?workspace_id=${workspaceId}`,
};

afterEach(() => vi.unstubAllGlobals());

describe("exports API", () => {
  it("creates a MASKED Collection export with its Idempotency-Key", async () => {
    const fetchMock = vi.fn<typeof fetch>().mockResolvedValue(jsonResponse({ job: pendingJob, replayed: false, dispatch_pending: true }, { status: 202 }));
    vi.stubGlobal("fetch", fetchMock);
    await expect(createExport({ workspaceId, collectionId, collectionVersion: 2, queryHash: hash, kind: "MARKDOWN", fields: ["object_type", "id", "title"], idempotencyKey: "export-create-1" })).resolves.toMatchObject({ job: { id: exportId }, dispatchPending: true });
    const firstCreateCall = fetchMock.mock.calls[0];
    if (firstCreateCall === undefined) throw new Error("missing Export create call");
    const init = firstCreateCall[1];
    if (init === undefined) throw new Error("missing Export create init");
    expect(new Headers(init.headers).get("Idempotency-Key")).toBe("export-create-1");
    if (typeof init.body !== "string") throw new Error("missing Export JSON body");
    expect(JSON.parse(init.body)).toMatchObject({ workspace_id: workspaceId, collection_id: collectionId, redaction_policy: "MASKED" });
  });

  it("uses collection_id in the list key and rejects a cross-bound collection job", async () => {
    const fetchMock = vi.fn<typeof fetch>().mockResolvedValue(jsonResponse({ workspace_id: workspaceId, items: [pendingJob], next_cursor: "opaque.cursor" }));
    vi.stubGlobal("fetch", fetchMock);
    await expect(listCollectionExports(workspaceId, collectionId)).resolves.toMatchObject({ nextCursor: "opaque.cursor" });
    const firstListCall = fetchMock.mock.calls[0];
    if (firstListCall === undefined) throw new Error("missing Export list call");
    const url = firstListCall[0];
    if (typeof url !== "string") throw new Error("missing Export list URL");
    expect(new URL(url, "http://localhost").searchParams.get("collection_id")).toBe(collectionId);
    vi.stubGlobal("fetch", vi.fn<typeof fetch>().mockResolvedValue(jsonResponse({ workspace_id: workspaceId, items: [{ ...pendingJob, collection_id: "72000000-0000-4000-8000-000000000099" }] })));
    await expect(listCollectionExports(workspaceId, collectionId)).rejects.toMatchObject({ code: "INVALID_RESPONSE" });
  });

  it("rejects duplicate JSON fields and impossible status/result combinations", async () => {
    expect(() => decodeJob({ ...pendingJob, status: "SUCCEEDED" })).toThrow(/job\.(terminal_state|result_state)/);
    expect(() => decodeJob({ ...pendingJob, fields: ["evaluation"] })).toThrow(/job.fields/);
    const fetchMock = vi.fn<typeof fetch>().mockResolvedValue(new Response(`{"job":${JSON.stringify(pendingJob)},"job":${JSON.stringify(pendingJob)},"replayed":false,"dispatch_pending":false}`, { status: 202, headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", fetchMock);
    await expect(createExport({ workspaceId, collectionId, collectionVersion: 2, queryHash: hash, kind: "MARKDOWN", fields: ["object_type"], idempotencyKey: "export-create-2" })).rejects.toMatchObject({ code: "INVALID_RESPONSE" });
  });

  it("accepts FULL and EXPIRED historical facts, but limits cursors to 4096 UTF-8 bytes", async () => {
    expect(decodeJob({ ...succeededJob, status: "EXPIRED", download_url: undefined, download_count: 2, last_downloaded_at: "2026-07-26T00:00:03Z", redaction_policy: "FULL", include_sensitive: true })).toMatchObject({ status: "EXPIRED", fileHash: "b".repeat(64), downloadCount: 2 });
    expect(() => decodeJob({ ...pendingJob, schema_version: "export/v2" })).toThrow(/job.invariants/);
    expect(() => decodeJob({ ...pendingJob, redaction_policy: "MASKED", include_sensitive: true })).toThrow(/job.invariants/);
    expect(() => decodeJob({ ...pendingJob, redaction_policy: "FULL", include_sensitive: false })).toThrow(/job.invariants/);
    expect(() => decodeJob({ ...pendingJob, read_model_revision: "c".repeat(64), exact_count: 1, file_hash: "b".repeat(64) })).toThrow(/job.pending_state/);
    expect(() => decodeJob({ ...succeededJob, status: "EXPIRED", download_url: undefined, completed_at: undefined })).toThrow(/job.expired_state/);
    expect(() => decodeJob({ ...succeededJob, exact_count: 10_001 })).toThrow(/job.exact_count/);
    expect(() => decodeJob({ ...succeededJob, status: "FAILED", error_code: "not-a-token", error_message: "render failed" })).toThrow(/job.error_code/);
    expect(() => decodeJob({ ...succeededJob, status: "FAILED", error_code: "EXPORT_RENDER_FAILED", error_message: "x".repeat(4097) })).toThrow(/job.error_message/);
    expect(() => decodeJob({ ...succeededJob, download_count: 0, last_downloaded_at: "2026-07-26T00:00:03Z" })).toThrow(/job.download_state/);
    expect(() => decodeJob({ ...succeededJob, download_url: `/api/v1/exports/${exportId}/download?workspace_id=72000000-0000-4000-8000-000000000099` })).toThrow(/job.download_url/);
    const acceptedCursorValue = `${"你".repeat(1365)}a`;
    const acceptedCursor = vi.fn<typeof fetch>().mockResolvedValue(jsonResponse({ workspace_id: workspaceId, items: [], next_cursor: acceptedCursorValue }));
    vi.stubGlobal("fetch", acceptedCursor);
    await expect(listCollectionExports(workspaceId, collectionId)).resolves.toMatchObject({ nextCursor: acceptedCursorValue });
    const fetchMock = vi.fn<typeof fetch>().mockResolvedValue(jsonResponse({ workspace_id: workspaceId, items: [], next_cursor: "你".repeat(1366) }));
    vi.stubGlobal("fetch", fetchMock);
    await expect(listCollectionExports(workspaceId, collectionId)).rejects.toMatchObject({ code: "INVALID_RESPONSE" });
    expect(() => listCollectionExports(workspaceId, collectionId, { cursor: "你".repeat(1366) })).toThrow(/Export 请求字段无效：list/);
  });

  it.each([
    ["started_at", "2026-07-25T23:59:59Z", {}],
    ["started_at", "2026-07-26T00:00:05Z", {}],
    ["completed_at", "2026-07-25T23:59:59Z", {}],
    ["completed_at", "2026-07-26T00:00:05Z", {}],
    ["last_downloaded_at", "2026-07-25T23:59:59Z", { download_count: 1 }],
    ["last_downloaded_at", "2026-07-26T00:00:05Z", { download_count: 1 }],
  ])("rejects %s outside the created/updated window", (field, value, extra) => {
    expect(() => decodeJob({ ...succeededJob, ...extra, [field]: value })).toThrow(/job.lifecycle_time/);
  });

  it("downloads through the generated client and rejects unsafe response headers", async () => {
    const fetchMock = vi.fn<typeof fetch>().mockResolvedValue(new Response("hello export", { status: 200, headers: { "Content-Type": "text/markdown; charset=utf-8", "Content-Length": "12", "Content-Disposition": `attachment; filename="collection-${collectionId}-${exportId}.md"`, "Cache-Control": "private, no-store", "X-Content-Type-Options": "nosniff" } }));
    vi.stubGlobal("fetch", fetchMock);
    await expect(downloadExport(workspaceId, decodeJob(succeededJob))).resolves.toMatchObject({ filename: `collection-${collectionId}-${exportId}.md`, contentType: "text/markdown" });
    expect(fetchMock.mock.calls[0]?.[0]).toBe(succeededJob.download_url);
    vi.stubGlobal("fetch", vi.fn<typeof fetch>().mockResolvedValue(new Response("hello export", { status: 200, headers: { "Content-Type": "text/markdown" } })));
    await expect(downloadExport(workspaceId, decodeJob(succeededJob))).rejects.toMatchObject({ code: "INVALID_RESPONSE" });
    vi.stubGlobal("fetch", vi.fn<typeof fetch>().mockResolvedValue(new Response("hello export", { status: 200, headers: { "Content-Type": "application/json", "X-Content-Type-Options": "nosniff" } })));
    await expect(downloadExport(workspaceId, decodeJob(succeededJob))).rejects.toMatchObject({ code: "INVALID_RESPONSE" });
  });

  it("rejects an unknown field in a Problem response", async () => {
    vi.stubGlobal("fetch", vi.fn<typeof fetch>().mockResolvedValue(jsonResponse({ error_code: "EXPORT_REQUEST_INVALID", message: "invalid export", retryable: false, extra: true }, { status: 400 })));
    await expect(createExport({ workspaceId, collectionId, collectionVersion: 2, queryHash: hash, kind: "MARKDOWN", fields: ["object_type"], idempotencyKey: "export-create-3" })).rejects.toMatchObject({ code: "INVALID_RESPONSE", errorCode: "INVALID_RESPONSE" });
  });
});
