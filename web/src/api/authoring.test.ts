import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  AuthoringApiError,
  createWorkingDraft,
  decodeArticleRevisionDraft,
  decodeDocumentDraft,
  decodePublicationBinding,
  decodeWorkingDraft,
  freezeWorkingDraft,
  getAuthoringOverview,
  isPublishableDraft,
  publishArticleRevision,
  updateWorkingDraft,
} from "./authoring";

const workspaceId = "a1000000-0000-4000-8000-000000000001";
const otherWorkspaceId = "a1000000-0000-4000-8000-000000000002";
const draftId = "a1000000-0000-4000-8000-000000000003";
const documentId = "a1000000-0000-4000-8000-000000000004";
const revisionId = "a1000000-0000-4000-8000-000000000005";
const proposalId = "a1000000-0000-4000-8000-000000000006";
const proposalRevisionId = "a1000000-0000-4000-8000-000000000007";
const publicationId = "a1000000-0000-4000-8000-000000000008";

const workingDraftWire = (changes: Record<string, unknown> = {}) => ({
  id: draftId,
  workspace_id: workspaceId,
  document_id: null,
  title: "Java AI",
  target_path: "notes/java-ai.md",
  body: "# Java AI\n",
  status: "EDITING",
  version: 2,
  created_at: "2026-08-03T08:00:00Z",
  updated_at: "2026-08-03T08:01:00.123456789Z",
  ...changes,
});

const documentWire = (changes: Record<string, unknown> = {}) => ({
  id: documentId,
  workspace_id: workspaceId,
  canonical_path: "notes/java-ai.md",
  title: "Java AI",
  lifecycle_status: "DRAFT",
  current_published_revision_id: null,
  version: 1,
  created_at: "2026-08-03T08:02:00Z",
  updated_at: "2026-08-03T08:02:00Z",
  ...changes,
});

const revisionWire = (changes: Record<string, unknown> = {}) => ({
  id: revisionId,
  workspace_id: workspaceId,
  document_id: documentId,
  source_version_id: null,
  parent_revision_id: null,
  revision_no: 1,
  content: "# Java AI\n",
  content_hash: "a".repeat(64),
  status: "DRAFT",
  optimization_mode: "NONE",
  git_commit: null,
  created_by_type: "USER",
  created_at: "2026-08-03T08:02:00Z",
  ...changes,
});

const publicationWire = (changes: Record<string, unknown> = {}) => ({
  id: publicationId,
  workspace_id: workspaceId,
  document_id: documentId,
  article_revision_id: revisionId,
  proposal_id: proposalId,
  proposal_revision_id: proposalRevisionId,
  target_path: "notes/java-ai.md",
  content_hash: "a".repeat(64),
  status: "PENDING",
  git_commit: null,
  error_code: null,
  version: 1,
  created_at: "2026-08-03T08:03:00Z",
  updated_at: "2026-08-03T08:03:00Z",
  published_at: null,
  proposal_href: `/proposals/${proposalId}`,
  ...changes,
});

const jsonResponse = (body: unknown, status = 200): Response => new Response(JSON.stringify(body), {
  status,
  headers: { "Content-Type": "application/json" },
});

describe("Authoring API boundary", () => {
  beforeEach(() => {
    vi.stubGlobal("fetch", vi.fn<typeof fetch>());
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("creates one server-owned blank Working Draft with an exact Idempotency-Key", async () => {
    vi.mocked(fetch).mockResolvedValueOnce(jsonResponse({ working_draft: workingDraftWire({ title: "", target_path: "", body: "", version: 1 }), replayed: false }, 201));

    await expect(createWorkingDraft({ workspaceId, idempotencyKey: "working-draft-create-1" })).resolves.toMatchObject({
      workingDraft: { id: draftId, workspaceId, title: "", version: 1 },
      replayed: false,
    });

    const call = vi.mocked(fetch).mock.calls[0];
    expect(call?.[0]).toBe(`/api/v1/workspaces/${workspaceId}/authoring/working-drafts`);
    expect(call?.[1]?.method).toBe("POST");
    expect(call?.[1]?.body).toBe("{}");
    expect(new Headers(call?.[1]?.headers).get("Idempotency-Key")).toBe("working-draft-create-1");
    expect(new Headers(call?.[1]?.headers).get("Content-Type")).toBe("application/json");
  });

  it("updates exact Markdown bytes through expected_version CAS", async () => {
    vi.mocked(fetch).mockResolvedValueOnce(jsonResponse({ working_draft: workingDraftWire({ version: 3, body: "# Java AI\n\nCAS\n" }), replayed: false }));

    await updateWorkingDraft({
      workspaceId,
      draftId,
      expectedVersion: 2,
      title: "Java AI",
      targetPath: "notes/java-ai.md",
      body: "# Java AI\n\nCAS\n",
      idempotencyKey: "working-draft-update-1",
    });

    const call = vi.mocked(fetch).mock.calls[0];
    expect(call?.[0]).toBe(`/api/v1/workspaces/${workspaceId}/authoring/working-drafts/${draftId}`);
    expect(call?.[1]?.method).toBe("PUT");
    expect(call?.[1]?.body).toBe(JSON.stringify({
      expected_version: 2,
      title: "Java AI",
      target_path: "notes/java-ai.md",
      body: "# Java AI\n\nCAS\n",
    }));
    expect(new Headers(call?.[1]?.headers).get("Idempotency-Key")).toBe("working-draft-update-1");
  });

  it("decodes the complete owner wire when freezing a Revision", async () => {
    vi.mocked(fetch).mockResolvedValueOnce(jsonResponse({
      working_draft: workingDraftWire({ document_id: documentId, version: 3, updated_at: "2026-08-03T08:02:00Z" }),
      document: documentWire(),
      article_revision: revisionWire(),
      replayed: false,
    }, 201));

    await expect(freezeWorkingDraft({ workspaceId, draftId, expectedVersion: 2, idempotencyKey: "working-draft-freeze-1" })).resolves.toMatchObject({
      workingDraft: { documentId, version: 3 },
      document: { id: documentId, canonicalPath: "notes/java-ai.md" },
      articleRevision: {
        id: revisionId,
        sourceVersionId: null,
        optimizationMode: "NONE",
        createdByType: "USER",
      },
    });

    const call = vi.mocked(fetch).mock.calls[0];
    expect(call?.[0]).toBe(`/api/v1/workspaces/${workspaceId}/authoring/working-drafts/${draftId}/freeze`);
    expect(call?.[1]?.body).toBe(JSON.stringify({ expected_version: 2 }));
  });

  it("publishes only by frozen Document/Revision identity and accepts a real Proposal href", async () => {
    vi.mocked(fetch).mockResolvedValueOnce(jsonResponse({ publication: publicationWire(), replayed: false }, 201));

    await expect(publishArticleRevision({
      workspaceId,
      documentId,
      revisionId,
      idempotencyKey: "document-publish-1",
    })).resolves.toMatchObject({ publication: { proposalId, proposalHref: `/proposals/${proposalId}`, status: "PENDING" } });

    const call = vi.mocked(fetch).mock.calls[0];
    expect(call?.[0]).toBe(`/api/v1/workspaces/${workspaceId}/documents/${documentId}/revisions/${revisionId}/publish-proposals`);
    expect(call?.[1]?.body).toBe("{}");
  });

  it("fails closed on unknown keys, impossible dates, multiline fields and binding drift", () => {
    expect(() => decodeWorkingDraft(workingDraftWire({ unknown: true }))).toThrow(AuthoringApiError);
    expect(() => decodeWorkingDraft(workingDraftWire({ created_at: "2026-02-30T08:00:00Z" }))).toThrow(AuthoringApiError);
    expect(() => decodeWorkingDraft(workingDraftWire({ title: "Java\nAI" }))).toThrow(AuthoringApiError);
    expect(() => decodeDocumentDraft(documentWire({ workspace_id: otherWorkspaceId }))).not.toThrow();
    expect(() => decodeDocumentDraft({ ...documentWire(), target_path: "notes/other.md" })).toThrow(AuthoringApiError);
    expect(() => decodeArticleRevisionDraft(revisionWire({ status: "PUBLISHED", git_commit: null }))).toThrow(AuthoringApiError);
    expect(() => decodeArticleRevisionDraft(revisionWire({ status: "DRAFT", git_commit: "b".repeat(40) }))).toThrow(AuthoringApiError);
    expect(() => decodePublicationBinding(publicationWire({ proposal_href: `/proposals/${revisionId}` }))).toThrow(AuthoringApiError);
    expect(() => decodePublicationBinding(publicationWire({ workspace_id: otherWorkspaceId }))).not.toThrow();
  });

  it("rejects duplicate JSON keys and cross-Workspace overview items", async () => {
    vi.mocked(fetch)
      .mockResolvedValueOnce(new Response(`{"working_draft":${JSON.stringify(workingDraftWire())},"replayed":false,"replayed":false}`, {
        status: 200,
        headers: { "Content-Type": "application/json" },
      }))
      .mockResolvedValueOnce(jsonResponse({
        workspace_id: workspaceId,
        organizing: { available: false, reason: "能力尚未接入", href: null },
        recent_drafts: [{
          id: draftId,
          workspace_id: otherWorkspaceId,
          document_id: null,
          title: "Java AI",
          target_path: "notes/java-ai.md",
          status: "EDITING",
          version: 2,
          updated_at: "2026-08-03T08:01:00Z",
        }],
        pending_publications: [],
        completed_documents: [],
      }));

    await expect(createWorkingDraft({ workspaceId, idempotencyKey: "working-draft-create-duplicate" })).rejects.toMatchObject({ code: "INVALID_RESPONSE" });
    await expect(getAuthoringOverview(workspaceId)).rejects.toMatchObject({ code: "INVALID_RESPONSE" });
  });

  it("accepts CLOSED in document facts but rejects it from the pending overview projection", async () => {
    const closed = publicationWire({ status: "CLOSED", error_code: "AUTHORING_PROPOSAL_CLOSED" });
    expect(decodePublicationBinding(closed)).toMatchObject({ status: "CLOSED", errorCode: "AUTHORING_PROPOSAL_CLOSED" });
    vi.mocked(fetch).mockResolvedValueOnce(jsonResponse({
      workspace_id: workspaceId,
      organizing: { available: false, reason: "能力尚未接入", href: null },
      recent_drafts: [],
      pending_publications: [closed],
      completed_documents: [],
    }));

    await expect(getAuthoringOverview(workspaceId)).rejects.toMatchObject({ code: "INVALID_RESPONSE" });
  });

  it("rejects wrong media types, undocumented success status and replay/status drift", async () => {
    const created = { working_draft: workingDraftWire({ title: "", target_path: "", body: "", version: 1 }), replayed: false };
    vi.mocked(fetch)
      .mockResolvedValueOnce(new Response(JSON.stringify(created), { status: 201, headers: { "Content-Type": "text/plain" } }))
      .mockResolvedValueOnce(jsonResponse(created, 202))
      .mockResolvedValueOnce(jsonResponse({ ...created, replayed: true }, 201));

    await expect(createWorkingDraft({ workspaceId, idempotencyKey: "create-media" })).rejects.toMatchObject({ code: "INVALID_RESPONSE" });
    await expect(createWorkingDraft({ workspaceId, idempotencyKey: "create-status" })).rejects.toMatchObject({ code: "INVALID_RESPONSE", status: 202 });
    await expect(createWorkingDraft({ workspaceId, idempotencyKey: "create-replay-drift" })).rejects.toMatchObject({ code: "INVALID_RESPONSE", status: 201 });
  });

  it("preserves a strict Problem for CAS conflicts", async () => {
    vi.mocked(fetch).mockResolvedValueOnce(jsonResponse({
      error_code: "AUTHORING_VERSION_CONFLICT",
      message: "Working Draft 已变化",
      retryable: false,
      details: { current_version: 3 },
    }, 409));

    await expect(updateWorkingDraft({
      workspaceId,
      draftId,
      expectedVersion: 2,
      title: "Java AI",
      targetPath: "notes/java-ai.md",
      body: "# Java AI\n",
      idempotencyKey: "working-draft-update-conflict",
    })).rejects.toMatchObject({
      code: "HTTP_ERROR",
      errorCode: "AUTHORING_VERSION_CONFLICT",
      status: 409,
      retryable: false,
      details: { current_version: 3 },
    });
  });

  it("validates publishable title, relative Markdown path and body before Freeze", () => {
    expect(isPublishableDraft({ title: "Java AI", targetPath: "notes/java-ai.MD", body: "# Java AI" })).toBe(true);
    expect(isPublishableDraft({ title: " Java AI ", targetPath: "notes/java-ai.md", body: "# Java AI" })).toBe(false);
    expect(isPublishableDraft({ title: "Java AI", targetPath: ".GIT/java-ai.md", body: "# Java AI" })).toBe(false);
    expect(isPublishableDraft({ title: "Java AI", targetPath: "../java-ai.md", body: "# Java AI" })).toBe(false);
    expect(isPublishableDraft({ title: "Java AI", targetPath: "notes/java-ai.md", body: "  " })).toBe(false);
  });
});
