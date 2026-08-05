import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  CaptureApiError,
  createCapture,
  decodeCapture,
  decodeCaptureList,
  decodeKnowledgeProfile,
  getKnowledgeProfile,
  listCaptures,
  retryCapture,
  retryKnowledgeProfile,
  type CaptureKind,
} from "./captures";

const workspaceId = "95000000-0000-4000-8000-000000000001";
const otherWorkspaceId = "95000000-0000-4000-8000-000000000002";
const captureId = "95000000-0000-4000-8000-000000000003";
const sourceId = "95000000-0000-4000-8000-000000000004";
const sourceVersionId = "95000000-0000-4000-8000-000000000005";
const profileId = "95000000-0000-4000-8000-000000000006";
const profileRevisionId = "95000000-0000-4000-8000-000000000007";
const parseProjectionId = "95000000-0000-4000-8000-000000000008";
const indexVersionId = "95000000-0000-4000-8000-000000000009";
const modelRunId = "95000000-0000-4000-8000-00000000000a";
const sourceSpanId = "95000000-0000-4000-8000-00000000000b";

const captureWire = (kind: CaptureKind = "TEXT", changes: Record<string, unknown> = {}) => ({
  id: captureId,
  workspace_id: workspaceId,
  kind,
  display_name: kind === "URL" ? "example.com" : "Evidence first",
  source_id: sourceId,
  status: kind === "URL" ? "RECEIVED" : "SOURCE_SAVED",
  fetch_status: kind === "URL" ? "PENDING" : "NOT_APPLICABLE",
  ingestion_status: "PENDING",
  index_status: "PENDING",
  profile_status: "PENDING",
  retryable: false,
  version: 1,
  captured_at: "2026-08-02T10:00:00Z",
  updated_at: "2026-08-02T10:00:00.123456789Z",
  detail_href: `/api/v1/workspaces/${workspaceId}/captures/${captureId}`,
  ...(kind === "URL"
    ? { original_url: "https://example.com/article" }
    : {
        latest_source_version_id: sourceVersionId,
        profile_href: `/api/v1/workspaces/${workspaceId}/source-versions/${sourceVersionId}/knowledge-profile`,
      }),
  ...changes,
});

const profileWire = (changes: Record<string, unknown> = {}) => ({
  profile: {
    id: profileId,
    workspace_id: workspaceId,
    capture_id: captureId,
    source_version_id: sourceVersionId,
    current_revision_id: profileRevisionId,
    status: "READY",
    retryable: false,
    version: 3,
    created_at: "2026-08-02T10:00:00Z",
    updated_at: "2026-08-02T10:01:00Z",
  },
  revision: {
    id: profileRevisionId,
    profile_id: profileId,
    workspace_id: workspaceId,
    source_version_id: sourceVersionId,
    parse_projection_id: parseProjectionId,
    index_version_id: indexVersionId,
    model_run_id: modelRunId,
    model_settings_revision: null,
    prompt_version: "profile-v1",
    schema_version: "document-knowledge-profile/v1",
    content: {
      summary: "这是一份有证据约束的 Java AI 文档摘要。",
      topics: [{ label: "Java AI", source_span_ids: [sourceSpanId] }],
      terms: [],
      knowledge_points: [{ text: "模型调用必须保留来源证据。", source_span_ids: [sourceSpanId] }],
      examples: [],
    },
    content_digest: "a".repeat(64),
    created_at: "2026-08-02T10:01:00Z",
  },
  ...changes,
});

const jsonResponse = (body: unknown, status = 200): Response => new Response(JSON.stringify(body), {
  status,
  headers: { "Content-Type": "application/json; charset=utf-8" },
});

const rawJSONResponse = (body: string, status = 200): Response => new Response(body, {
  status,
  headers: { "Content-Type": "application/json" },
});

describe("Capture API boundary", () => {
  beforeEach(() => {
    vi.stubGlobal("fetch", vi.fn<typeof fetch>());
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("serializes TEXT create with the caller Idempotency-Key and decodes the 201 receipt", async () => {
    vi.mocked(fetch).mockResolvedValueOnce(jsonResponse({ capture: captureWire(), replayed: false }, 201));

    const result = await createCapture({
      workspaceId,
      idempotencyKey: "quick-capture-text-1",
      kind: "TEXT",
      displayName: " Evidence first ",
      text: "  Server state is authoritative.  ",
    });

    expect(result.capture).toMatchObject({
      id: captureId,
      workspaceId,
      kind: "TEXT",
      latestSourceVersionId: sourceVersionId,
    });
    expect(result.replayed).toBe(false);
    const call = vi.mocked(fetch).mock.calls[0];
    expect(call?.[0]).toBe(`/api/v1/workspaces/${workspaceId}/captures`);
    expect(call?.[1]?.credentials).toBe("include");
    expect(new Headers(call?.[1]?.headers).get("Idempotency-Key")).toBe("quick-capture-text-1");
    expect(call?.[1]?.body).toBe(JSON.stringify({
      kind: "TEXT",
      display_name: "Evidence first",
      text: "  Server state is authoritative.  ",
    }));
  });

  it("serializes one FILE through multipart without a JSON Content-Type override", async () => {
    vi.mocked(fetch).mockResolvedValueOnce(jsonResponse({ capture: captureWire("FILE"), replayed: true }, 200));
    const file = new File(["# Evidence\n"], "evidence.md", { type: "text/markdown", lastModified: 10 });

    await expect(createCapture({
      workspaceId,
      idempotencyKey: "quick-capture-file-1",
      kind: "FILE",
      displayName: "Evidence",
      file,
    })).resolves.toMatchObject({ replayed: true, capture: { kind: "FILE" } });

    const call = vi.mocked(fetch).mock.calls[0];
    expect(call?.[0]).toBe(`/api/v1/workspaces/${workspaceId}/capture-files`);
    expect(new Headers(call?.[1]?.headers).get("Content-Type")).toBeNull();
    const body = call?.[1]?.body;
    if (!(body instanceof FormData)) throw new Error("multipart request body is missing");
    expect([...body.keys()]).toEqual(["kind", "display_name", "file"]);
    expect(body.get("kind")).toBe("FILE");
    expect(body.get("display_name")).toBe("Evidence");
    const uploaded = body.get("file");
    expect(uploaded).toBeInstanceOf(File);
    if (!(uploaded instanceof File)) throw new Error("multipart file is missing");
    expect({ name: uploaded.name, type: uploaded.type, size: uploaded.size }).toEqual({
      name: "evidence.md",
      type: "text/markdown",
      size: file.size,
    });
  });

  it("rejects missing, unknown, null optional, invalid scalar, and mismatched href fields", () => {
    const missingVersion = captureWire();
    Reflect.deleteProperty(missingVersion, "version");
    const invalidValues = [
      missingVersion,
      captureWire("TEXT", { untrusted: true }),
      captureWire("TEXT", { original_url: null }),
      captureWire("TEXT", { id: "not-a-uuid" }),
      captureWire("TEXT", { status: "DONE" }),
      captureWire("URL", { status: "READY" }),
      captureWire("TEXT", { version: 0 }),
      captureWire("TEXT", { captured_at: "2026-02-31T10:00:00Z" }),
      captureWire("TEXT", { detail_href: `/api/v1/workspaces/${otherWorkspaceId}/captures/${captureId}` }),
      captureWire("TEXT", { profile_href: `/api/v1/workspaces/${workspaceId}/source-versions/${sourceId}/knowledge-profile` }),
    ];

    for (const value of invalidValues) {
      expect(() => decodeCapture(value, { workspaceId })).toThrow(CaptureApiError);
    }
  });

  it("fails closed when the list envelope or any item crosses its Workspace binding", () => {
    expect(() => decodeCaptureList({
      workspace_id: otherWorkspaceId,
      items: [],
    }, workspaceId)).toThrow(CaptureApiError);

    expect(() => decodeCaptureList({
      workspace_id: workspaceId,
      items: [captureWire("TEXT", {
        workspace_id: otherWorkspaceId,
        detail_href: `/api/v1/workspaces/${otherWorkspaceId}/captures/${captureId}`,
      })],
    }, workspaceId)).toThrow(CaptureApiError);
  });

  it("serializes list filters and rejects duplicate JSON keys", async () => {
    vi.mocked(fetch)
      .mockResolvedValueOnce(jsonResponse({ workspace_id: workspaceId, items: [captureWire("URL")] }))
      .mockResolvedValueOnce(rawJSONResponse(`{"workspace_id":"${workspaceId}","workspace_id":"${workspaceId}","items":[]}`));

    await expect(listCaptures(workspaceId, {
      kind: "URL",
      status: "RECEIVED",
      limit: 5,
      cursor: "opaque-cursor",
    })).resolves.toMatchObject({ workspaceId, items: [{ kind: "URL" }] });
    expect(vi.mocked(fetch).mock.calls[0]?.[0]).toBe(
      `/api/v1/workspaces/${workspaceId}/captures?kind=URL&status=RECEIVED&limit=5&cursor=opaque-cursor`,
    );

    await expect(listCaptures(workspaceId)).rejects.toMatchObject({
      code: "INVALID_RESPONSE",
      status: 200,
    });
  });

  it("serializes retry with expected_version and accepts a 202 receipt", async () => {
    const retryWire = captureWire("URL", {
      version: 4,
      status: "RECEIVED",
      fetch_status: "PENDING",
    });
    vi.mocked(fetch).mockResolvedValueOnce(jsonResponse({ capture: retryWire, replayed: false }, 202));

    await expect(retryCapture({
      workspaceId,
      captureId,
      expectedVersion: 3,
      idempotencyKey: "quick-capture-retry-1",
    })).resolves.toMatchObject({ capture: { id: captureId, version: 4 }, replayed: false });

    const call = vi.mocked(fetch).mock.calls[0];
    expect(call?.[0]).toBe(`/api/v1/workspaces/${workspaceId}/captures/${captureId}/retry`);
    expect(new Headers(call?.[1]?.headers).get("Idempotency-Key")).toBe("quick-capture-retry-1");
    expect(call?.[1]?.body).toBe(JSON.stringify({ expected_version: 3 }));
  });

  it("preserves a valid Problem and rejects an invalid Problem shape", async () => {
    vi.mocked(fetch)
      .mockResolvedValueOnce(jsonResponse({
        error_code: "CAPTURE_VERSION_CONFLICT",
        message: "Capture 已更新",
        retryable: false,
        details: { current_version: 4 },
      }, 409))
      .mockResolvedValueOnce(jsonResponse({
        error_code: "CAPTURE_UNAVAILABLE",
        message: "暂不可用",
        retryable: "yes",
      }, 503));

    await expect(retryCapture({
      workspaceId,
      captureId,
      expectedVersion: 3,
      idempotencyKey: "quick-capture-retry-2",
    })).rejects.toMatchObject({
      code: "HTTP_ERROR",
      errorCode: "CAPTURE_VERSION_CONFLICT",
      retryable: false,
      status: 409,
    });

    await expect(listCaptures(workspaceId)).rejects.toMatchObject({
      code: "INVALID_RESPONSE",
      status: 503,
    });
  });

  it("decodes an evidence-backed Profile and accepts omitted empty aliases", () => {
    const result = decodeKnowledgeProfile(profileWire(), { workspaceId, captureId, sourceVersionId });

    expect(result).toMatchObject({
      id: profileId,
      status: "READY",
      revision: {
        id: profileRevisionId,
        topics: [{ label: "Java AI", aliases: [], sourceSpanIds: [sourceSpanId] }],
        knowledgePoints: [{ sourceSpanIds: [sourceSpanId] }],
      },
    });

    const stale = decodeKnowledgeProfile({
      ...profileWire(),
      profile: { ...profileWire().profile, status: "STALE" },
    }, { workspaceId, sourceVersionId });
    expect(stale).toMatchObject({ status: "STALE", currentRevisionId: profileRevisionId });
    expect(stale.revision).toMatchObject({ id: profileRevisionId });
  });

  it("accepts stale Profile stages and requires a current revision for stale Profiles", () => {
    expect(decodeCapture(captureWire("TEXT", { profile_status: "STALE" }), { workspaceId })).toMatchObject({
      profileStatus: "STALE",
    });

    const staleWithoutRevision = profileWire({
      profile: {
        ...profileWire().profile,
        status: "STALE",
      },
      revision: null,
    });
    Reflect.deleteProperty(staleWithoutRevision.profile, "current_revision_id");
    expect(() => decodeKnowledgeProfile(staleWithoutRevision, { workspaceId, captureId, sourceVersionId })).toThrow(CaptureApiError);
  });

  it("rejects Profile contract drift, binding mismatch, missing required content, and invalid evidence", () => {
    const base = profileWire();
    const invalidValues = [
      { ...base, unexpected: true },
      {
        ...base,
        profile: { ...base.profile, workspace_id: otherWorkspaceId },
      },
      {
        ...base,
        revision: {
          ...base.revision,
          content: { ...base.revision.content, topics: [] },
        },
      },
      {
        ...base,
        revision: {
          ...base.revision,
          content: {
            ...base.revision.content,
            knowledge_points: [{ text: "无效证据", source_span_ids: ["not-a-uuid"] }],
          },
        },
      },
    ];

    for (const value of invalidValues) {
      expect(() => decodeKnowledgeProfile(value, { workspaceId, captureId, sourceVersionId })).toThrow(CaptureApiError);
    }
  });

  it("reads and retries a Profile through the Source Version-bound routes", async () => {
    vi.mocked(fetch)
      .mockResolvedValueOnce(jsonResponse(profileWire()))
      .mockResolvedValueOnce(jsonResponse({ ...profileWire(), replayed: true }));

    await expect(getKnowledgeProfile({ workspaceId, captureId, sourceVersionId })).resolves.toMatchObject({
      id: profileId,
      revision: { id: profileRevisionId },
    });
    expect(vi.mocked(fetch).mock.calls[0]?.[0]).toBe(
      `/api/v1/workspaces/${workspaceId}/source-versions/${sourceVersionId}/knowledge-profile`,
    );

    await expect(retryKnowledgeProfile({
      workspaceId,
      captureId,
      sourceVersionId,
      expectedVersion: 3,
      idempotencyKey: "profile-retry-original",
    })).resolves.toMatchObject({ replayed: true, profile: { id: profileId } });
    const retryCall = vi.mocked(fetch).mock.calls[1];
    expect(retryCall?.[0]).toBe(
      `/api/v1/workspaces/${workspaceId}/source-versions/${sourceVersionId}/knowledge-profile/retry`,
    );
    expect(new Headers(retryCall?.[1]?.headers).get("Idempotency-Key")).toBe("profile-retry-original");
    expect(retryCall?.[1]?.body).toBe(JSON.stringify({ expected_version: 3 }));
  });
});
