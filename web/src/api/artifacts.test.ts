import { afterEach, describe, expect, it, vi } from "vitest";
import {
  ArtifactApiError,
  approveArtifactDraft,
  approveArtifactOutline,
  createArtifactPublishProposal,
  decodeArtifact,
  decodeArtifactCommandResult,
  decodeArtifactPage,
  decodeArtifactSectionGenerationAcceptance,
  decodeArtifactSectionGenerationPage,
  exportArtifactMarkdown,
  generateArtifactSection,
  listArtifacts,
  getArtifactSectionGenerations,
  planArtifact,
  recordArtifactGapSection,
  startArtifactRevision,
  submitArtifactOutline,
} from "./artifacts";

const workspaceId = "12000000-0000-4000-8000-000000000001";
const artifactId = "12000000-0000-4000-8000-000000000002";
const revisionId = "12000000-0000-4000-8000-000000000003";
const sourceVersionId = "12000000-0000-4000-8000-000000000004";
const sourceSpanId = "12000000-0000-4000-8000-000000000005";
const proposalId = "12000000-0000-4000-8000-000000000006";
const documentId = "12000000-0000-4000-8000-00000000000a";
const articleRevisionId = "12000000-0000-4000-8000-00000000000b";
const generationId = "12000000-0000-4000-8000-000000000007";
const workflowRunId = "12000000-0000-4000-8000-000000000008";
const nodeRunId = "12000000-0000-4000-8000-000000000009";
const hash = "a".repeat(64);
const at = "2026-07-26T00:00:00Z";
const artifact = { id: artifactId, workspace_id: workspaceId, type: "knowledge-note", title: "Artifact", status: "DRAFT", scope_definition: "scope", source_coverage: [{ section_key: "gap", status: "GAP", gaps: [{ code: "KNOWLEDGE_GAP", description: "missing" }] }], current_revision_id: revisionId, version: 4, created_at: at, updated_at: at, revision: { id: revisionId, artifact_id: artifactId, revision_no: 4, outline: [{ key: "gap", title: "Gap" }], sections: [{ key: "gap", title: "Gap", content: "", citations: [], document_sources: [], coverage: { section_key: "gap", status: "GAP", gaps: [{ code: "KNOWLEDGE_GAP", description: "missing" }] } }], created_by: "HUMAN", content_hash: hash, created_at: at } };
const generationItem = {
  generation_id: generationId,
  workspace_id: workspaceId,
  artifact_id: artifactId,
  source_revision_id: revisionId,
  source_revision_no: 4,
  source_artifact_version: 4,
  section_key: "gap",
  workflow_run_id: workflowRunId,
  node_run_id: nodeRunId,
  status: "PENDING",
  version: 1,
  created_at: at,
  updated_at: at,
  status_url: `/api/v1/workflows/${workflowRunId}`,
};
const generation = { ...generationItem, replayed: false };
const jsonResponse = (payload: unknown, status = 200): Response => new Response(JSON.stringify(payload), { status, headers: { "Content-Type": "application/json" } });

afterEach(() => vi.unstubAllGlobals());

describe("Artifact API boundary", () => {
  it("strictly decodes GAP and verified citations without treating them as client claims", () => {
    const decoded = decodeArtifact({ ...artifact, revision: { ...artifact.revision, outline: [{ key: "covered", title: "Covered" }], sections: [{ key: "covered", title: "Covered", content: "verified body", citations: [{ source_version_id: sourceVersionId, source_span_id: sourceSpanId, verified_content_hash: hash, excerpt: "server excerpt", verified: true }], document_sources: [], coverage: { section_key: "covered", status: "COVERED", gaps: [] } }] }, source_coverage: [{ section_key: "covered", status: "COVERED", gaps: [] }] });
    expect(decoded.revision.sections[0]?.citations[0]?.verified).toBe(true);
    expect(decodeArtifact(artifact).revision.sections[0]?.coverage.status).toBe("GAP");
    const documentBacked = decodeArtifact({ ...artifact, revision: { ...artifact.revision, outline: [{ key: "partial", title: "Partial" }], sections: [{ key: "partial", title: "Partial", content: "bounded body", citations: [], document_sources: [{ document_id: documentId, article_revision_id: articleRevisionId, revision_no: 2, verified_content_hash: hash, verified: true }], coverage: { section_key: "partial", status: "PARTIAL", gaps: [{ code: "MISSING_DETAIL", description: "partial evidence" }] } }] }, source_coverage: [{ section_key: "partial", status: "PARTIAL", gaps: [{ code: "MISSING_DETAIL", description: "partial evidence" }] }] });
    expect(documentBacked.revision.sections[0]?.documentSources[0]).toMatchObject({ documentId, articleRevisionId, revisionNo: 2, verified: true });
    expect(() => decodeArtifact({ ...artifact, revision: { ...artifact.revision, sections: [{ ...artifact.revision.sections[0], content: "invented body" }] } })).toThrow(ArtifactApiError);
    expect(() => decodeArtifact({ ...artifact, future: true })).toThrow(ArtifactApiError);
  });

  it("rejects source coverage that drifts from the current Revision sections", () => {
    const covered = { section_key: "covered", status: "COVERED", gaps: [] };
    const partial = { section_key: "partial", status: "PARTIAL", gaps: [{ code: "MISSING_DETAIL", description: "partial evidence" }] };
    const revision = {
      ...artifact.revision,
      outline: [{ key: "covered", title: "Covered" }, { key: "partial", title: "Partial" }],
      sections: [
        { key: "covered", title: "Covered", content: "verified body", citations: [{ source_version_id: sourceVersionId, source_span_id: sourceSpanId, verified_content_hash: hash, excerpt: "covered citation", verified: true }], document_sources: [], coverage: covered },
        { key: "partial", title: "Partial", content: "bounded body", citations: [{ source_version_id: sourceVersionId, source_span_id: proposalId, verified_content_hash: hash, excerpt: "partial citation", verified: true }], document_sources: [], coverage: partial },
      ],
    };
    expect(decodeArtifact({ ...artifact, revision, source_coverage: [covered, partial] }).sourceCoverage).toEqual([
      { sectionKey: "covered", status: "COVERED", gaps: [] },
      { sectionKey: "partial", status: "PARTIAL", gaps: [{ code: "MISSING_DETAIL", description: "partial evidence" }] },
    ]);
    expect(() => decodeArtifact({ ...artifact, revision, source_coverage: [covered] })).toThrow(ArtifactApiError);
    expect(() => decodeArtifact({ ...artifact, revision, source_coverage: [covered, { ...partial, gaps: [{ code: "OTHER", description: "drift" }] }] })).toThrow(ArtifactApiError);
  });

  it("keeps list and command responses bound to the requested Workspace and revision", () => {
    expect(decodeArtifactPage({ workspace_id: workspaceId, items: [artifact] })).toMatchObject({ workspaceId, items: [{ id: artifactId }] });
    expect(() => decodeArtifactPage({ workspace_id: workspaceId, items: [{ ...artifact, workspace_id: proposalId }] })).toThrow(ArtifactApiError);
    expect(() => decodeArtifactCommandResult({ artifact, publication: { workspace_id: workspaceId, artifact_id: artifactId, revision_id: revisionId, artifact_version: 4, revision_no: 4, content_hash: hash, proposal_id: proposalId, created_at: at }, replayed: false })).not.toThrow();
    expect(() => decodeArtifactCommandResult({ artifact, publication: { workspace_id: workspaceId, artifact_id: artifactId, revision_id: revisionId, artifact_version: 4, revision_no: 4, content_hash: "b".repeat(64), proposal_id: proposalId, created_at: at }, replayed: false })).toThrow(ArtifactApiError);
    expect(() => decodeArtifactCommandResult({ artifact, publication: { workspace_id: workspaceId, artifact_id: artifactId, revision_id: revisionId, artifact_version: 5, revision_no: 4, content_hash: hash, proposal_id: proposalId, created_at: at }, replayed: false })).toThrow(ArtifactApiError);
    expect(() => decodeArtifactCommandResult({ artifact, export: { id: proposalId, workspace_id: workspaceId, artifact_id: artifactId, revision_id: revisionId, artifact_version: 4, revision_no: 3, revision_hash: hash, output_hash: hash, output_size: 1, exported_at: at }, replayed: false })).toThrow(ArtifactApiError);
  });

  it("strictly decodes the complete five-state generation acceptance", () => {
    for (const status of ["PENDING", "COMPLETED", "FAILED", "CANCELLED", "RECOVERY_REQUIRED"] as const) {
      expect(decodeArtifactSectionGenerationAcceptance({ ...generation, status })).toMatchObject({
        generationId,
        workflowRunId,
        status,
      });
    }

    const missingUpdatedAt: Record<string, unknown> = { ...generation };
    delete missingUpdatedAt.updated_at;
    for (const invalid of [
      { ...generation, future: true },
      missingUpdatedAt,
      { ...generation, generation_id: "not-a-uuid" },
      { ...generation, source_revision_no: 0 },
      { ...generation, updated_at: "not-a-time" },
      { ...generation, status: "RUNNING" },
      { ...generation, status_url: `/api/v1/workflows/${nodeRunId}` },
    ]) {
      expect(() => decodeArtifactSectionGenerationAcceptance(invalid)).toThrow(ArtifactApiError);
    }
  });

  it("strictly decodes refresh generations without accepting command-only fields or completed items", () => {
    for (const status of ["PENDING", "FAILED", "CANCELLED", "RECOVERY_REQUIRED"] as const) {
      expect(decodeArtifactSectionGenerationPage({ workspace_id: workspaceId, artifact_id: artifactId, items: [{ ...generationItem, status }] })).toMatchObject({
        workspaceId,
        artifactId,
        items: [{ generationId, sectionKey: "gap", status }],
      });
    }

    for (const invalid of [
      { workspace_id: workspaceId, artifact_id: artifactId, items: [{ ...generationItem, replayed: false }] },
      { workspace_id: workspaceId, artifact_id: artifactId, items: [{ ...generationItem, status: "COMPLETED" }] },
      { workspace_id: workspaceId, artifact_id: artifactId, items: [generationItem, { ...generationItem, generation_id: proposalId }] },
      { workspace_id: workspaceId, artifact_id: artifactId, items: [{ ...generationItem, workspace_id: proposalId }] },
      { workspace_id: workspaceId, artifact_id: proposalId, items: [generationItem] },
      { workspace_id: workspaceId, artifact_id: artifactId, items: [{ ...generationItem, section_key: "Bad key" }] },
      { workspace_id: workspaceId, artifact_id: artifactId, items: [{ ...generationItem, version: 0 }] },
      { workspace_id: workspaceId, artifact_id: artifactId, items: [{ ...generationItem, created_at: "not-a-time" }] },
      { workspace_id: workspaceId, artifact_id: artifactId, items: [{ ...generationItem, status_url: `/api/v1/workflows/${nodeRunId}` }] },
      { workspace_id: workspaceId, artifact_id: artifactId, items: [], future: true },
    ]) {
      expect(() => decodeArtifactSectionGenerationPage(invalid)).toThrow(ArtifactApiError);
    }
  });

  it("loads refresh generations from the frozen Workspace-bound GET route", async () => {
    const fetchMock = vi.fn<typeof fetch>().mockResolvedValue(jsonResponse({ workspace_id: workspaceId, artifact_id: artifactId, items: [generationItem] }));
    vi.stubGlobal("fetch", fetchMock);

    await expect(getArtifactSectionGenerations(workspaceId, artifactId)).resolves.toMatchObject({ items: [{ generationId, status: "PENDING" }] });
    expect(fetchMock.mock.calls[0]?.[0]).toBe(`/api/v1/artifacts/${artifactId}/section-generations?workspace_id=${workspaceId}`);
    expect(fetchMock.mock.calls[0]?.[1]?.method).toBeUndefined();
  });

  it("rejects refresh generation response binding drift", async () => {
    vi.stubGlobal("fetch", vi.fn<typeof fetch>()
      .mockResolvedValueOnce(jsonResponse({ workspace_id: proposalId, artifact_id: artifactId, items: [] }))
      .mockResolvedValueOnce(jsonResponse({ workspace_id: workspaceId, artifact_id: proposalId, items: [] })));

    await expect(getArtifactSectionGenerations(workspaceId, artifactId)).rejects.toMatchObject({ code: "INVALID_RESPONSE" });
    await expect(getArtifactSectionGenerations(workspaceId, artifactId)).rejects.toMatchObject({ code: "INVALID_RESPONSE" });
  });

  it("sends Workspace and Idempotency-Key for real API commands", async () => {
    const fetchMock = vi.fn<typeof fetch>().mockResolvedValue(jsonResponse({ artifact, replayed: false }, 201));
    vi.stubGlobal("fetch", fetchMock);
    await expect(planArtifact({ workspaceId, type: "knowledge-note", title: "Artifact", scopeDefinition: "scope", idempotencyKey: "artifact-plan-1" })).resolves.toMatchObject({ artifact: { id: artifactId } });
    expect(new Headers(fetchMock.mock.calls[0]?.[1]?.headers).get("Idempotency-Key")).toBe("artifact-plan-1");
    const body = fetchMock.mock.calls[0]?.[1]?.body;
    if (typeof body !== "string") throw new Error("missing request JSON");
    expect(JSON.parse(body)).toMatchObject({ workspace_id: workspaceId, scope_definition: "scope" });
  });

  it("sends only the frozen generation fields and omits creator from manual GAP commands", async () => {
    const fetchMock = vi.fn<typeof fetch>()
      .mockResolvedValueOnce(jsonResponse(generation, 202))
      .mockResolvedValueOnce(jsonResponse({ artifact, replayed: false }));
    vi.stubGlobal("fetch", fetchMock);

    await expect(generateArtifactSection({
      workspaceId,
      artifactId,
      expectedVersion: 4,
      sectionKey: "gap",
      idempotencyKey: "artifact-generate-1",
    })).resolves.toMatchObject({ generationId, status: "PENDING" });
    await expect(recordArtifactGapSection({
      workspaceId,
      artifactId,
      expectedVersion: 4,
      sectionKey: "gap",
      title: "Gap",
      gapCode: "KNOWLEDGE_GAP",
      gapDescription: "missing",
      idempotencyKey: "artifact-gap-1",
    })).resolves.toMatchObject({ artifact: { id: artifactId } });

    const generateRequest = fetchMock.mock.calls[0];
    expect(generateRequest?.[0]).toBe(`/api/v1/artifacts/${artifactId}/sections/generate`);
    expect(new Headers(generateRequest?.[1]?.headers).get("Idempotency-Key")).toBe("artifact-generate-1");
    if (typeof generateRequest?.[1]?.body !== "string") throw new Error("missing generation request JSON");
    expect(JSON.parse(generateRequest[1].body)).toEqual({
      workspace_id: workspaceId,
      expected_version: 4,
      section_key: "gap",
    });

    const gapRequest = fetchMock.mock.calls[1];
    if (typeof gapRequest?.[1]?.body !== "string") throw new Error("missing GAP request JSON");
    expect(JSON.parse(gapRequest[1].body)).toEqual({
      workspace_id: workspaceId,
      expected_version: 4,
      section: {
        key: "gap",
        title: "Gap",
        content: "",
        citations: [],
        coverage: {
          section_key: "gap",
          status: "GAP",
          gaps: [{ code: "KNOWLEDGE_GAP", description: "missing" }],
        },
      },
    });
  });

  it("rejects non-202 success, request binding drift, and stable unavailable responses", async () => {
    vi.stubGlobal("fetch", vi.fn<typeof fetch>()
      .mockResolvedValueOnce(jsonResponse(generation, 200))
      .mockResolvedValueOnce(jsonResponse({ ...generation, section_key: "other" }, 202))
      .mockResolvedValueOnce(jsonResponse({ error_code: "ARTIFACT_GENERATION_CAPABILITY_UNAVAILABLE", message: "generation unavailable", retryable: false }, 503)));
    const input = { workspaceId, artifactId, expectedVersion: 4, sectionKey: "gap", idempotencyKey: "artifact-generate-2" };

    await expect(generateArtifactSection(input)).rejects.toMatchObject({ code: "INVALID_RESPONSE", status: 200 });
    await expect(generateArtifactSection(input)).rejects.toMatchObject({ code: "INVALID_RESPONSE" });
    await expect(generateArtifactSection(input)).rejects.toMatchObject({
      code: "HTTP_ERROR",
      status: 503,
      errorCode: "ARTIFACT_GENERATION_CAPABILITY_UNAVAILABLE",
      retryable: false,
    });
  });

  it("rejects response Workspace drift and keeps conflicts visible", async () => {
    vi.stubGlobal("fetch", vi.fn<typeof fetch>()
      .mockResolvedValueOnce(jsonResponse({ workspace_id: proposalId, items: [] }))
      .mockResolvedValueOnce(jsonResponse({ code: "ARTIFACT_VERSION_CONFLICT", detail: "stale", retryable: false }, 409)));
    await expect(listArtifacts(workspaceId)).rejects.toMatchObject({ code: "INVALID_RESPONSE" });
    await expect(planArtifact({ workspaceId, type: "knowledge-note", title: "Artifact", scopeDefinition: "scope", idempotencyKey: "artifact-plan-2" })).rejects.toMatchObject({ code: "HTTP_ERROR", status: 409, errorCode: "ARTIFACT_VERSION_CONFLICT" });
  });

  it("binds every command response to its requested Workspace and Artifact", async () => {
    const fetchMock = vi.fn<typeof fetch>();
    vi.stubGlobal("fetch", fetchMock);
    const revisionInput = { workspaceId, artifactId, expectedVersion: 4, idempotencyKey: "artifact-command-binding" };
    const nonPlanCommands = [
      () => submitArtifactOutline({ ...revisionInput, outline: [{ key: "gap", title: "Gap" }] }),
      () => approveArtifactOutline(revisionInput),
      () => startArtifactRevision(revisionInput),
      () => recordArtifactGapSection({ ...revisionInput, sectionKey: "gap", title: "Gap", gapCode: "KNOWLEDGE_GAP", gapDescription: "missing" }),
      () => approveArtifactDraft(revisionInput),
      () => exportArtifactMarkdown(revisionInput),
      () => createArtifactPublishProposal(revisionInput),
    ];

    fetchMock.mockResolvedValueOnce(jsonResponse({ artifact: { ...artifact, workspace_id: proposalId }, replayed: false }));
    await expect(planArtifact({ workspaceId, type: "knowledge-note", title: "Artifact", scopeDefinition: "scope", idempotencyKey: "artifact-plan-binding" })).rejects.toMatchObject({ code: "INVALID_RESPONSE" });
    for (const execute of nonPlanCommands) {
      fetchMock.mockResolvedValueOnce(jsonResponse({ artifact: { ...artifact, workspace_id: proposalId }, replayed: false }));
      await expect(execute()).rejects.toMatchObject({ code: "INVALID_RESPONSE" });
      fetchMock.mockResolvedValueOnce(jsonResponse({ artifact: { ...artifact, id: proposalId, revision: { ...artifact.revision, artifact_id: proposalId } }, replayed: false }));
      await expect(execute()).rejects.toMatchObject({ code: "INVALID_RESPONSE" });
    }
  });

  it("accepts only OpenAPI success statuses for Artifact commands", async () => {
    const fetchMock = vi.fn<typeof fetch>()
      .mockResolvedValueOnce(jsonResponse({ artifact, replayed: false }, 200))
      .mockResolvedValueOnce(jsonResponse({ artifact, replayed: false }, 201))
      .mockResolvedValueOnce(jsonResponse({ artifact, replayed: false }, 202));
    vi.stubGlobal("fetch", fetchMock);
    const planInput = { workspaceId, type: "knowledge-note", title: "Artifact", scopeDefinition: "scope", idempotencyKey: "artifact-plan-status" };
    await expect(planArtifact(planInput)).resolves.toMatchObject({ artifact: { id: artifactId } });
    await expect(planArtifact({ ...planInput, idempotencyKey: "artifact-plan-created" })).resolves.toMatchObject({ artifact: { id: artifactId } });
    await expect(planArtifact({ ...planInput, idempotencyKey: "artifact-plan-invalid-2xx" })).rejects.toMatchObject({ code: "INVALID_RESPONSE", status: 202 });

    const revisionInput = { workspaceId, artifactId, expectedVersion: 4, idempotencyKey: "artifact-command-status" };
    const nonPlanCommands = [
      () => submitArtifactOutline({ ...revisionInput, outline: [{ key: "gap", title: "Gap" }] }),
      () => approveArtifactOutline(revisionInput),
      () => startArtifactRevision(revisionInput),
      () => recordArtifactGapSection({ ...revisionInput, sectionKey: "gap", title: "Gap", gapCode: "KNOWLEDGE_GAP", gapDescription: "missing" }),
      () => approveArtifactDraft(revisionInput),
      () => exportArtifactMarkdown(revisionInput),
      () => createArtifactPublishProposal(revisionInput),
    ];
    for (const execute of nonPlanCommands) {
      fetchMock.mockResolvedValueOnce(jsonResponse({ artifact, replayed: false }, 201));
      await expect(execute()).rejects.toMatchObject({ code: "INVALID_RESPONSE", status: 201 });
    }
  });
});
