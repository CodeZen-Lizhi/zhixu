import { afterEach, describe, expect, it, vi } from "vitest";

import { CollectionApiError, createCollection, decodeCollection, decodePreviewResultPage, decodeResultPage, getCollectionResults, listCollections, previewCollection, type CollectionQuery } from "./collections";

const workspaceId = "11000000-0000-4000-8000-000000000001";
const collectionId = "11000000-0000-4000-8000-000000000002";
const at = "2026-07-22T00:00:00Z";
const hash = "a".repeat(64);
const query: CollectionQuery = { schema_version: "collection-query/v1", root: { kind: "group", operator: "AND", clauses: [{ kind: "predicate", field: "object_type", operator: "EQ", value: "TOPIC" }] }, sort: [{ field: "updated_at", direction: "DESC" }] };
const viewConfig = { columns: ["object_type", "title"], fixed_columns: ["title"], sort: [{ field: "updated_at", direction: "DESC" }], group_by: null, density: "COMFORTABLE" };
const collectionPayload = {
  id: collectionId,
  workspace_id: workspaceId,
  name: "Active knowledge",
  description: "",
  query_schema_version: "collection-query/v1",
  query_version: 1,
  query,
  query_hash: hash,
  view_type: "LIST",
  view_config: viewConfig,
  status: "ACTIVE",
  cached_result_version: null,
  last_executed_at: null,
  version: 3,
  created_at: at,
  updated_at: at,
};

const jsonResponse = (payload: unknown, status = 200): Response => new Response(JSON.stringify(payload), { status, headers: { "Content-Type": "application/json" } });

afterEach(() => vi.unstubAllGlobals());

describe("Collection API boundary", () => {
  it("strictly decodes Collection definitions and rejects unknown fields", () => {
    expect(decodeCollection(collectionPayload)).toMatchObject({ id: collectionId, workspaceId, querySchemaVersion: "collection-query/v1", version: 3 });
    expect(() => decodeCollection({ ...collectionPayload, future: true })).toThrow(CollectionApiError);
    expect(() => decodeCollection({ ...collectionPayload, query_schema_version: "collection-query/v2" })).toThrow(CollectionApiError);
  });

  it("decodes preview/result pages without parsing opaque cursors", () => {
    const page = decodeResultPage({
      workspace_id: workspaceId,
      collection_id: collectionId,
      items: [{
        object_type: "CLAIM",
        id: "11000000-0000-4000-8000-000000000003",
        topic_id: null,
        title: "Topic",
        summary: "",
        status: "ACTIVE",
        confidence: 0.8,
        aliases: [],
        source_summaries: [{ source_type: "FILE", file_path: "docs/topic.md", support_type: "SUPPORTS", created_at: at }],
        relation_types: ["BELONGS_TO"],
        health_summary: { count: 1, max_severity: "HIGH", issue_types: ["STALE"], summary: "存在过期引用" },
        relation_type: null,
        health_issue_type: "STALE",
        source_type: null,
        file_path: null,
        created_at: at,
        updated_at: at,
      }],
      query_hash: hash,
      exact_count: 1,
      next_cursor: "opaque.cursor",
      revision_hash: hash,
    });
    expect(page.items[0]).toMatchObject({ objectType: "CLAIM", aliases: [], sourceSummaries: [{ filePath: "docs/topic.md" }], relationTypes: ["BELONGS_TO"], healthSummary: { count: 1, maxSeverity: "HIGH" }, healthIssueType: "STALE" });
    expect(page.nextCursor).toBe("opaque.cursor");
  });

  it("preserves idempotency key and canonical request body for create", async () => {
    const fetchMock = vi.fn<typeof fetch>().mockResolvedValue(jsonResponse(collectionPayload, 201));
    vi.stubGlobal("fetch", fetchMock);

    await expect(createCollection({
      workspaceId,
      name: " Active knowledge ",
      query,
      viewType: "LIST",
      viewConfig: { columns: ["object_type", "title"], fixedColumns: ["title"], sort: [{ field: "updated_at", direction: "DESC" }], groupBy: null, density: "COMFORTABLE" },
    }, "create-1")).resolves.toMatchObject({ id: collectionId });
    const [, init] = fetchMock.mock.calls[0] ?? [];
    const bodyText = init?.body;
    if (typeof bodyText !== "string") throw new Error("missing JSON body");
    const body = JSON.parse(bodyText) as Record<string, unknown>;
    expect(new Headers(init?.headers).get("Idempotency-Key")).toBe("create-1");
    expect(body.name).toBe("Active knowledge");
    expect(body.view_config).toMatchObject({ fixed_columns: ["title"], density: "COMFORTABLE" });
  });

  it("fails closed on Workspace/Collection response drift and invalid client input", async () => {
    vi.stubGlobal("fetch", vi.fn<typeof fetch>().mockResolvedValue(jsonResponse({ ...collectionPayload, workspace_id: "11000000-0000-4000-8000-000000000009" })));
    await expect(createCollection({
      workspaceId,
      name: "Active knowledge",
      query,
      viewType: "LIST",
      viewConfig: { columns: [], fixedColumns: [], sort: [], groupBy: null, density: "COMFORTABLE" },
    }, "create-1")).rejects.toMatchObject({ code: "INVALID_RESPONSE" });

    expect(() => getCollectionResults(workspaceId, collectionId, hash, { cursor: "", limit: 25 })).toThrow(CollectionApiError);
    expect(() => previewCollection({
      workspaceId,
      name: "Preview",
      query: { schema_version: "collection-query/v2", root: query.root } as never,
      viewType: "LIST",
      viewConfig: { columns: [], fixedColumns: [], sort: [], groupBy: null, density: "COMFORTABLE" },
    })).toThrow(CollectionApiError);
  });

  it("enforces query node, byte, field/operator and scalar bounds", () => {
    const predicate = { kind: "predicate", field: "object_type", operator: "EQ", value: "TOPIC" } as const;
    const queryWith = (clauses: unknown[]) => ({ schema_version: "collection-query/v1", root: { kind: "group", operator: "AND", clauses } });
    expect(() => decodeCollection({ ...collectionPayload, query: queryWith(Array.from({ length: 63 }, () => predicate)) })).not.toThrow();
    expect(() => decodeCollection({ ...collectionPayload, query: queryWith(Array.from({ length: 64 }, () => predicate)) })).toThrow(CollectionApiError);
    expect(() => decodeCollection({ ...collectionPayload, query: queryWith([{ kind: "predicate", field: "text", operator: "CONTAINS", value: "x".repeat(33 * 1024) }]) })).toThrow(CollectionApiError);
    expect(() => decodeCollection({ ...collectionPayload, query: queryWith([{ kind: "predicate", field: "relation_type", operator: "IS_NULL" }]) })).toThrow(CollectionApiError);
    expect(() => decodeCollection({ ...collectionPayload, query: queryWith([{ kind: "predicate", field: "created_at", operator: "GTE", value: "2026-02-30T00:00:00Z" }]) })).toThrow(CollectionApiError);
    expect(() => decodeCollection({ ...collectionPayload, query: queryWith([{ kind: "predicate", field: "confidence", operator: "GTE", value: 1.2 }]) })).toThrow(CollectionApiError);
  });

  it("rejects cross-kind CollectionItem fields", () => {
    const topic = { object_type: "TOPIC", id: "11000000-0000-4000-8000-000000000003", topic_id: null, title: "Topic", summary: "", status: "ACTIVE", confidence: null, aliases: ["Topic alias"], source_summaries: [], relation_types: [], health_summary: null, relation_type: null, health_issue_type: null, source_type: null, file_path: null, created_at: at, updated_at: at };
    expect(() => decodeResultPage({ workspace_id: workspaceId, collection_id: collectionId, query_hash: hash, items: [{ ...topic, confidence: 0.5 }], exact_count: 1, next_cursor: null, revision_hash: hash })).toThrow(CollectionApiError);
    expect(() => decodeResultPage({ workspace_id: workspaceId, collection_id: collectionId, query_hash: hash, items: [{ ...topic, source_summaries: [{ source_type: "FILE", file_path: "a.md", support_type: "SUPPORTS", created_at: at }] }], exact_count: 1, next_cursor: null, revision_hash: hash })).toThrow(CollectionApiError);
  });

  it("separates saved result and preview bindings and enforces bounded unique items", () => {
    const topic = { object_type: "TOPIC", id: "11000000-0000-4000-8000-000000000003", topic_id: null, title: "Topic", summary: "", status: "ACTIVE", confidence: null, aliases: [], source_summaries: [], relation_types: [], health_summary: null, relation_type: null, health_issue_type: null, source_type: null, file_path: null, created_at: at, updated_at: at };
    const saved = { workspace_id: workspaceId, collection_id: collectionId, query_hash: hash, items: [topic], exact_count: 1, next_cursor: null, revision_hash: hash };
    expect(decodeResultPage(saved)).toMatchObject({ collectionId, queryHash: hash, exactCount: 1 });
    expect(() => decodeResultPage({ ...saved, collection_id: null })).toThrow(CollectionApiError);
    expect(() => decodeResultPage({ ...saved, query_hash: null })).toThrow(CollectionApiError);
    expect(decodePreviewResultPage({ ...saved, collection_id: null })).toMatchObject({ collectionId: null, queryHash: hash });
    const previewWithoutCollection = { workspace_id: workspaceId, query_hash: hash, items: [topic], exact_count: 1, next_cursor: null, revision_hash: hash };
    expect(decodePreviewResultPage(previewWithoutCollection)).toMatchObject({ collectionId: null, queryHash: hash });
    expect(() => decodePreviewResultPage(saved)).toThrow(CollectionApiError);
    expect(() => decodePreviewResultPage({ ...previewWithoutCollection, query_hash: undefined })).toThrow(CollectionApiError);
    expect(() => decodeResultPage({ ...saved, items: [topic, topic], exact_count: 2 })).toThrow(CollectionApiError);
    expect(() => decodeResultPage({ ...saved, exact_count: 0 })).toThrow(CollectionApiError);
    expect(() => decodeResultPage({ ...saved, items: Array.from({ length: 101 }, (_, index) => ({ ...topic, id: `11000000-0000-4000-8000-${String(index + 1).padStart(12, "0")}` })), exact_count: 101 })).toThrow(CollectionApiError);
  });

  it("rejects duplicate JSON keys before Collection decoding", async () => {
    const duplicateWorkspace = `{"workspace_id":"${workspaceId}","work\\u0073pace_id":"${workspaceId}","items":[]}`;
    vi.stubGlobal("fetch", vi.fn<typeof fetch>().mockResolvedValue(new Response(duplicateWorkspace, { status: 200, headers: { "Content-Type": "application/json" } })));
    await expect(listCollections(workspaceId)).rejects.toMatchObject({ code: "INVALID_RESPONSE" });
  });

  it("binds saved results to query hash and uses the 25 item default", async () => {
    const response = { workspace_id: workspaceId, collection_id: collectionId, query_hash: "b".repeat(64), items: [], exact_count: 0, revision_hash: hash };
    const fetchMock = vi.fn<typeof fetch>().mockResolvedValue(jsonResponse(response));
    vi.stubGlobal("fetch", fetchMock);
    await expect(getCollectionResults(workspaceId, collectionId, hash)).rejects.toMatchObject({ code: "INVALID_RESPONSE" });
    const requestUrl = fetchMock.mock.calls[0]?.[0];
    expect(typeof requestUrl === "string" ? new URL(requestUrl, "http://localhost").searchParams.get("limit") : null).toBe("25");
  });

  it("passes list cursor and preserves AbortError identity", async () => {
    const fetchMock = vi.fn<typeof fetch>().mockResolvedValue(jsonResponse({ workspace_id: workspaceId, items: [collectionPayload], next_cursor: "next.opaque" }));
    vi.stubGlobal("fetch", fetchMock);
    await expect(listCollections(workspaceId, { status: ["ACTIVE"], cursor: "cursor.opaque", limit: 25 })).resolves.toMatchObject({ nextCursor: "next.opaque" });
    const requestUrl = fetchMock.mock.calls[0]?.[0];
    if (typeof requestUrl !== "string") throw new Error("missing list request URL");
    expect(requestUrl).toContain("cursor=cursor.opaque");

    const abort = new DOMException("cancelled", "AbortError");
    vi.stubGlobal("fetch", vi.fn<typeof fetch>().mockRejectedValue(abort));
    await expect(listCollections(workspaceId)).rejects.toBe(abort);
  });
});
