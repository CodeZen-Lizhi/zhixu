import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  OrganizingApiError,
  addOrganizingMaterial,
  confirmOrganizingDraft,
  createOrganizingDraft,
  decodeOrganizingDraft,
  decodeOrganizingRun,
  decodeOrganizingSnapshot,
  decodeOrganizingTemplate,
  listOrganizingTemplates,
  removeOrganizingMaterial,
  searchOrganizingMaterials,
  setOrganizingMaterialSelection,
  suggestOrganizingMaterials,
  updateOrganizingDraft,
} from "./organizing";

const id = (suffix: string): string => `a1000000-0000-4000-8000-${suffix.padStart(12, "0")}`;
const workspaceId = id("1");
const otherWorkspaceId = id("2");
const draftId = id("3");
const templateId = id("4");
const templateRevisionId = id("5");
const snapshotId = id("6");
const materialId = id("7");
const hash = "a".repeat(64);

const evidenceWire = {
  index_version_id: id("30"),
  chunk_id: id("31"),
  source_version_id: id("32"),
  source_span_id: id("33"),
  content_hash: hash,
  excerpt_hash: "f".repeat(64),
};

const referenceWires = {
  SOURCE_VERSION: { kind: "SOURCE_VERSION", source_version_id: id("32"), profile_revision_id: id("35"), version: 0, content_hash: hash },
  DOCUMENT_REVISION: { kind: "DOCUMENT_REVISION", document_id: id("36"), article_revision_id: id("37"), version: 2, content_hash: "b".repeat(64) },
  CLAIM: { kind: "CLAIM", claim_id: id("38"), version: 3, content_hash: "c".repeat(64) },
  SMART_COLLECTION: { kind: "SMART_COLLECTION", collection_id: id("39"), version: 4, query_hash: "d".repeat(64), read_model_revision: "e".repeat(64) },
} as const;

const materialWire = (kind: keyof typeof referenceWires, position: number, changes: Record<string, unknown> = {}) => ({
  id: id(String(100 + position)),
  draft_id: draftId,
  workspace_id: workspaceId,
  kind,
  title: `${kind} material`,
  reasons: kind === "CLAIM" ? ["FORMAL_KNOWLEDGE"] : ["HYBRID_MATCH"],
  origin: "SUGGESTED",
  availability: "AVAILABLE",
  score: 0.86,
  selected: true,
  position,
  reference: referenceWires[kind],
  evidence: kind === "SOURCE_VERSION" || kind === "CLAIM" ? [evidenceWire] : [],
  created_at: "2026-08-03T08:00:00Z",
  ...changes,
});

const draftWire = (changes: Record<string, unknown> = {}) => ({
  id: draftId,
  workspace_id: workspaceId,
  intent: "整理幂等与恢复",
  status: "EDITING",
  template_revision_id: templateRevisionId,
  confirmed_snapshot_id: null,
  version: 5,
  materials: [
    materialWire("SOURCE_VERSION", 0),
    materialWire("DOCUMENT_REVISION", 1),
    materialWire("CLAIM", 2),
    materialWire("SMART_COLLECTION", 3),
  ],
  created_at: "2026-08-03T08:00:00Z",
  updated_at: "2026-08-03T08:03:00.123456789Z",
  ...changes,
});

const declarationWire = {
  schema_version: "organizing-template/v1",
  kind: "TOPIC_ARTICLE",
  name: "专题知识文章",
  description: "先审大纲，再分章生成。",
  materials: { allowed_kinds: ["SOURCE_VERSION", "DOCUMENT_REVISION", "CLAIM", "SMART_COLLECTION"], min_materials: 1, max_materials: 500 },
  sections: [
    { key: "overview", title: "概览", required: true },
    { key: "conflicts", title: "冲突", required: true },
    { key: "gaps", title: "知识缺口", required: true },
    { key: "sources", title: "来源", required: true },
  ],
  presentation: { audience: "工程师", language: "zh-CN", tone: "严谨", length: "MEDIUM", include_code: true, include_examples: true, include_faq: false },
  output: { directory: "articles", filename_pattern: "{slug}.md" },
  additional_instructions: "",
};

const templateRevisionWire = {
  id: templateRevisionId,
  template_id: templateId,
  workspace_id: null,
  revision_no: 1,
  kind: "TOPIC_ARTICLE",
  schema_version: "organizing-template/v1",
  canonical_hash: "d".repeat(64),
  declaration: declarationWire,
  created_at: "2026-08-03T08:00:00Z",
};

const templateWire = (changes: Record<string, unknown> = {}) => ({
  id: templateId,
  workspace_id: null,
  key: "topic-article",
  name: "专题知识文章",
  description: "先审大纲，再分章生成。",
  built_in: true,
  kind: "TOPIC_ARTICLE",
  current_revision_id: templateRevisionId,
  version: 1,
  current_revision: templateRevisionWire,
  created_at: "2026-08-03T08:00:00Z",
  updated_at: "2026-08-03T08:00:00Z",
  ...changes,
});

const snapshotWire = (changes: Record<string, unknown> = {}) => ({
  id: snapshotId,
  workspace_id: workspaceId,
  draft_id: draftId,
  draft_version: 5,
  template_id: templateId,
  template_revision_id: templateRevisionId,
  template_hash: "d".repeat(64),
  intent: "整理幂等与恢复",
  canonical_hash: "e".repeat(64),
  materials: [{ position: 0, reference: referenceWires.SOURCE_VERSION, evidence: [evidenceWire] }],
  created_at: "2026-08-03T08:04:00Z",
  ...changes,
});

const runWire = (changes: Record<string, unknown> = {}) => ({
  snapshot_id: snapshotId,
  dispatch_status: "STARTED",
  workflow_status: "succeeded",
  retryable: false,
  attempt_count: 1,
  last_error_code: null,
  binding: {
    id: id("49"), workspace_id: workspaceId, snapshot_id: snapshotId, workflow_run_id: id("50"),
    definition_key: "organizing.topic-article", definition_version: 1, created_at: "2026-08-03T08:04:30Z",
  },
  result: {
    id: id("51"), workspace_id: workspaceId, run_binding_id: id("49"), snapshot_id: snapshotId,
    workflow_run_id: id("50"), node_run_id: id("52"), kind: "ARTIFACT", result_ref: id("53"),
    result_hash: "f".repeat(64), created_at: "2026-08-03T08:05:00Z",
  },
  updated_at: "2026-08-03T08:05:00Z",
  ...changes,
});

const jsonResponse = (body: unknown, status = 200): Response => new Response(JSON.stringify(body), {
  status,
  headers: { "Content-Type": "application/json" },
});

describe("Organizing API boundary", () => {
  beforeEach(() => {
    vi.stubGlobal("fetch", vi.fn<typeof fetch>());
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("decodes all four material variants and preserves explainability metadata", () => {
    const draft = decodeOrganizingDraft(draftWire());

    expect(draft).toMatchObject({ id: draftId, workspaceId, version: 5, materials: [
      { kind: "SOURCE_VERSION", reference: { sourceVersionId: id("32") }, evidence: [{ sourceSpanId: id("33") }] },
      { kind: "DOCUMENT_REVISION", reference: { revisionNo: 2 } },
      { kind: "CLAIM", reference: { claimVersion: 3 }, reasons: ["FORMAL_KNOWLEDGE"] },
      { kind: "SMART_COLLECTION", reference: { collectionVersion: 4 } },
    ] });
  });

  it.each([
    ["unknown draft field", draftWire({ unexpected: true })],
    ["workspace drift", draftWire({ materials: [materialWire("SOURCE_VERSION", 0, { workspace_id: otherWorkspaceId })] })],
    ["material union drift", draftWire({ materials: [materialWire("SOURCE_VERSION", 0, { reference: referenceWires.CLAIM })] })],
    ["duplicate reason", draftWire({ materials: [materialWire("SOURCE_VERSION", 0, { reasons: ["HYBRID_MATCH", "HYBRID_MATCH"] })] })],
    ["empty intent", draftWire({ intent: "" })],
    ["invalid time", draftWire({ updated_at: "2026-02-30T08:00:00Z" })],
  ])("fails closed for %s", (_name, payload) => {
    expect(() => decodeOrganizingDraft(payload)).toThrow(OrganizingApiError);
  });

  it("strictly binds template declaration, snapshot and run terminal state", () => {
    expect(decodeOrganizingTemplate(templateWire())).toMatchObject({ builtIn: true, currentRevision: { kind: "TOPIC_ARTICLE", declaration: { materials: { maxMaterials: 500 } } } });
    expect(decodeOrganizingSnapshot(snapshotWire())).toMatchObject({ id: snapshotId, materials: [{ position: 0, reference: { kind: "SOURCE_VERSION" } }] });
    expect(decodeOrganizingRun(runWire())).toMatchObject({ status: "SUCCEEDED", workflowStatus: "succeeded", resultKind: "ARTIFACT" });
    expect(() => decodeOrganizingTemplate(templateWire({ current_revision: { ...templateRevisionWire, declaration: { ...declarationWire, kind: "KNOWLEDGE_REPORT" } } }))).toThrow(OrganizingApiError);
    expect(decodeOrganizingRun(runWire({ result: null }))).toMatchObject({ status: "STARTED", workflowStatus: "succeeded", resultKind: null });
    expect(() => decodeOrganizingRun(runWire({ workflow_status: "running" }))).toThrow(OrganizingApiError);
  });

  it("accepts an empty audience and rejects output defaults outside the domain contract", () => {
    expect(decodeOrganizingTemplate(templateWire({
      current_revision: {
        ...templateRevisionWire,
        declaration: { ...declarationWire, presentation: { ...declarationWire.presentation, audience: "" } },
      },
    })).currentRevision.declaration.presentation.audience).toBe("");
    expect(() => decodeOrganizingTemplate(templateWire({
      current_revision: {
        ...templateRevisionWire,
        declaration: { ...declarationWire, output: { directory: "../outside", filename_pattern: "{slug}.md" } },
      },
    }))).toThrow(OrganizingApiError);
    expect(() => decodeOrganizingTemplate(templateWire({
      current_revision: {
        ...templateRevisionWire,
        declaration: { ...declarationWire, output: { directory: "articles", filename_pattern: `${"a".repeat(254)}.md` } },
      },
    }))).toThrow(OrganizingApiError);
  });

  it("creates and updates a server draft through exact CAS command bodies", async () => {
    vi.mocked(fetch)
      .mockResolvedValueOnce(jsonResponse({ draft: draftWire({ intent: "整理幂等与恢复", template_revision_id: null, materials: [], version: 1 }), replayed: false }, 201))
      .mockResolvedValueOnce(jsonResponse({ draft: draftWire({ version: 2 }), replayed: false }));

    await createOrganizingDraft({ workspaceId, intent: "整理幂等与恢复", idempotencyKey: "organizing-create-1" });
    await updateOrganizingDraft({ workspaceId, draftId, expectedVersion: 1, intent: "整理幂等与恢复", templateRevisionId, idempotencyKey: "organizing-update-1" });

    expect(fetch).toHaveBeenNthCalledWith(1, `/api/v1/workspaces/${workspaceId}/organizing/drafts`, expect.objectContaining({ method: "POST", body: JSON.stringify({ intent: "整理幂等与恢复" }) }));
    expect(new Headers(vi.mocked(fetch).mock.calls[0]?.[1]?.headers).get("Idempotency-Key")).toBe("organizing-create-1");
    expect(fetch).toHaveBeenNthCalledWith(2, `/api/v1/workspaces/${workspaceId}/organizing/drafts/${draftId}`, expect.objectContaining({ method: "PUT", body: JSON.stringify({ expected_version: 1, intent: "整理幂等与恢复", template_revision_id: templateRevisionId }) }));
  });

  it("uses explicit suggestion, add and remove commands without sending browser-authored versions", async () => {
    vi.mocked(fetch)
      .mockResolvedValueOnce(jsonResponse({ draft: draftWire({ version: 6 }), replayed: false }))
      .mockResolvedValueOnce(jsonResponse({ draft: draftWire({ version: 7 }), replayed: false }, 201))
      .mockResolvedValueOnce(jsonResponse({ draft: draftWire({ version: 8 }), replayed: false }));

    await suggestOrganizingMaterials({ workspaceId, draftId, expectedVersion: 5, idempotencyKey: "organizing-suggest-1" });
    await addOrganizingMaterial({ workspaceId, draftId, expectedVersion: 6, kind: "DOCUMENT_REVISION", documentId: id("36"), articleRevisionId: id("37"), idempotencyKey: "organizing-add-1" });
    await removeOrganizingMaterial({ workspaceId, draftId, materialId, expectedVersion: 7, idempotencyKey: "organizing-remove-1" });

    expect(vi.mocked(fetch).mock.calls[0]?.[1]?.body).toBe(JSON.stringify({ expected_version: 5 }));
    expect(vi.mocked(fetch).mock.calls[1]?.[1]?.body).toBe(JSON.stringify({ expected_version: 6, kind: "DOCUMENT_REVISION", document_id: id("36"), article_revision_id: id("37") }));
    expect(vi.mocked(fetch).mock.calls[2]?.[0]).toBe(`/api/v1/workspaces/${workspaceId}/organizing/drafts/${draftId}/materials/${materialId}`);
    expect(vi.mocked(fetch).mock.calls[2]?.[1]).toEqual(expect.objectContaining({ method: "DELETE", body: JSON.stringify({ expected_version: 7 }) }));
  });

  it("searches bounded Workspace materials with a strict typed result", async () => {
    vi.mocked(fetch).mockResolvedValueOnce(jsonResponse({
      workspace_id: workspaceId,
      items: [{
        workspace_id: workspaceId,
        kind: "CLAIM",
        title: "幂等恢复结论",
        availability: "AVAILABLE",
        reference: { kind: "CLAIM", claim_id: id("38") },
      }],
    }));

    await expect(searchOrganizingMaterials({ workspaceId, query: "幂等 恢复", kind: "CLAIM", limit: 12 })).resolves.toMatchObject([
      { workspaceId, kind: "CLAIM", reference: { claimId: id("38") } },
    ]);
    const [path, init] = vi.mocked(fetch).mock.calls[0] ?? [];
    expect(path).toBe(`/api/v1/workspaces/${workspaceId}/organizing/materials/search?q=${encodeURIComponent("幂等 恢复").replace("%20", "+")}&kind=CLAIM&limit=12`);
    expect(init?.credentials).toBe("include");
    expect(init).not.toHaveProperty("method");
  });

  it("rejects frozen fields in an identity-only material search result", async () => {
    vi.mocked(fetch).mockResolvedValueOnce(jsonResponse({
      workspace_id: workspaceId,
      items: [{
        workspace_id: workspaceId,
        kind: "CLAIM",
        title: "幂等恢复结论",
        availability: "AVAILABLE",
        reference: referenceWires.CLAIM,
        evidence: [evidenceWire],
      }],
    }));

    await expect(searchOrganizingMaterials({ workspaceId, query: "幂等", kind: "CLAIM", limit: 12 })).rejects.toMatchObject({ code: "INVALID_RESPONSE" });
  });

  it("sets one material selection through an explicit CAS command", async () => {
    vi.mocked(fetch).mockResolvedValueOnce(jsonResponse({
      draft: draftWire({
        version: 6,
        materials: [materialWire("SOURCE_VERSION", 0, { id: materialId, selected: false })],
      }),
      replayed: false,
    }));

    await setOrganizingMaterialSelection({ workspaceId, draftId, materialId, expectedVersion: 5, selected: false, idempotencyKey: "organizing-selection-1" });

    expect(fetch).toHaveBeenCalledWith(
      `/api/v1/workspaces/${workspaceId}/organizing/drafts/${draftId}/materials/${materialId}`,
      expect.objectContaining({ method: "PATCH", body: JSON.stringify({ expected_version: 5, selected: false }) }),
    );
  });

  it("confirms only by exact draft/template revision and validates template list Workspace", async () => {
    vi.mocked(fetch)
      .mockResolvedValueOnce(jsonResponse({ draft: draftWire({ status: "CONFIRMED", version: 6, confirmed_snapshot_id: snapshotId }), snapshot: snapshotWire(), dispatch_status: "PENDING", replayed: false }, 202))
      .mockResolvedValueOnce(jsonResponse({ workspace_id: workspaceId, items: [templateWire()] }));

    await expect(confirmOrganizingDraft({ workspaceId, draftId, expectedVersion: 5, templateRevisionId, idempotencyKey: "organizing-confirm-1" })).resolves.toMatchObject({ snapshot: { id: snapshotId }, replayed: false });
    await expect(listOrganizingTemplates(workspaceId)).resolves.toHaveLength(1);

    expect(vi.mocked(fetch).mock.calls[0]?.[1]?.body).toBe(JSON.stringify({ expected_version: 5, template_revision_id: templateRevisionId }));
  });

  it("rejects a replay flag that contradicts an accepted command status", async () => {
    vi.mocked(fetch).mockResolvedValueOnce(jsonResponse({ draft: draftWire({ status: "CONFIRMED", version: 6, confirmed_snapshot_id: snapshotId }), snapshot: snapshotWire(), dispatch_status: "PENDING", replayed: true }, 202));

    await expect(confirmOrganizingDraft({ workspaceId, draftId, expectedVersion: 5, templateRevisionId, idempotencyKey: "organizing-confirm-2" }))
      .rejects.toMatchObject({ code: "INVALID_RESPONSE", status: 202 });
  });
});
