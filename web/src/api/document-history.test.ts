import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  DocumentHistoryApiError,
  compareDocumentHistory,
  createDocumentRestorePreview,
  createDocumentRestoreProposal,
  decodeDocumentHistoryPage,
  documentWorktreeRef,
  listDocumentHistory,
} from "./document-history";

const workspaceId = "d1000000-0000-4000-8000-000000000001";
const otherWorkspaceId = "d1000000-0000-4000-8000-000000000002";
const documentId = "d1000000-0000-4000-8000-000000000003";
const revisionId = "d1000000-0000-4000-8000-000000000004";
const proposalId = "d1000000-0000-4000-8000-000000000005";
const proposalRevisionId = "d1000000-0000-4000-8000-000000000006";
const approvalId = "d1000000-0000-4000-8000-000000000007";
const workflowId = "d1000000-0000-4000-8000-000000000008";
const writebackId = "d1000000-0000-4000-8000-000000000009";
const head = "a".repeat(40);
const managedCommit = "b".repeat(40);
const externalCommit = "c".repeat(40);
const hash = "d".repeat(64);
const otherHash = "e".repeat(64);

const managedEntry = (changes: Record<string, unknown> = {}) => ({
  kind: "MANAGED",
  commit: managedCommit,
  parent_commits: [externalCommit],
  author_name: "知序",
  author_email: "local@example.test",
  committed_at: "2026-08-03T08:00:00Z",
  summary: "publish article",
  article_revision_id: revisionId,
  article_revision_no: 3,
  proposal_id: proposalId,
  proposal_revision_id: proposalRevisionId,
  approval_id: approvalId,
  workflow_run_id: workflowId,
  writeback_id: writebackId,
  proposal_type: "file_patch",
  approval_decided_at: "2026-08-03T07:59:00Z",
  ...changes,
});

const externalEntry = (changes: Record<string, unknown> = {}) => ({
  kind: "EXTERNAL",
  commit: externalCommit,
  parent_commits: [],
  author_name: "Local user",
  author_email: "local@example.test",
  committed_at: "2026-08-02T08:00:00Z",
  summary: "external edit",
  ...changes,
});

const currentEntry = (changes: Record<string, unknown> = {}) => ({
  kind: "CURRENT_CHANGE",
  commit: null,
  parent_commits: [head],
  author_name: "",
  author_email: "",
  committed_at: null,
  summary: "当前未提交改动",
  ...changes,
});

const historyWire = (changes: Record<string, unknown> = {}) => ({
  workspace_id: workspaceId,
  document_id: documentId,
  document_version: 7,
  path: "notes/history.md",
  branch: "main",
  head,
  dirty: true,
  items: [currentEntry(), managedEntry(), externalEntry()],
  next_cursor: "signed-next-cursor",
  ...changes,
});

const compareWire = (changes: Record<string, unknown> = {}) => ({
  workspace_id: workspaceId,
  document_id: documentId,
  path: "notes/history.md",
  head,
  left: managedCommit,
  right: documentWorktreeRef,
  left_content: "# old\n",
  right_content: "# current\n",
  patch: "@@ -1 +1 @@\n-# old\n+# current\n",
  diff_hash: hash,
  ...changes,
});

const previewWire = (changes: Record<string, unknown> = {}) => ({
  workspace_id: workspaceId,
  document_id: documentId,
  path: "notes/history.md",
  target_commit: managedCommit,
  expected_head: head,
  expected_document_version: 7,
  current_content_hash: hash,
  target_content_hash: otherHash,
  current_content: "# current\n",
  target_content: "# old\n",
  patch: "@@ -1 +1 @@\n-# current\n+# old\n",
  diff_hash: hash,
  preview_hash: otherHash,
  blocked_by_dirty_worktree: false,
  ...changes,
});

const jsonResponse = (body: unknown, status = 200): Response => new Response(JSON.stringify(body), {
  status,
  headers: { "Content-Type": "application/json" },
});

describe("Document History API boundary", () => {
  beforeEach(() => {
    vi.stubGlobal("fetch", vi.fn<typeof fetch>());
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("decodes the managed/external/current union and preserves the stable cursor", () => {
    expect(decodeDocumentHistoryPage(historyWire(), { workspaceId, documentId })).toMatchObject({
      workspaceId,
      documentId,
      documentVersion: 7,
      head,
      dirty: true,
      nextCursor: "signed-next-cursor",
      items: [
        { kind: "CURRENT_CHANGE", commit: null, parentCommits: [head] },
        { kind: "MANAGED", commit: managedCommit, proposalId, articleRevisionNo: 3 },
        { kind: "EXTERNAL", commit: externalCommit },
      ],
    });
  });

  it("preserves partial managed relations instead of inventing missing workflow facts", () => {
    expect(decodeDocumentHistoryPage(historyWire({ items: [managedEntry({
      approval_id: null,
      workflow_run_id: null,
      writeback_id: null,
      approval_decided_at: null,
    })] }), { workspaceId, documentId })).toMatchObject({
      items: [{ kind: "MANAGED", proposalId, approvalId: null, workflowRunId: null, writebackId: null }],
    });
  });

  it("rejects unknown fields, binding drift, duplicate commits and broken managed relations", () => {
    expect(() => decodeDocumentHistoryPage({ ...historyWire(), unknown: true }, { workspaceId, documentId })).toThrow(DocumentHistoryApiError);
    expect(() => decodeDocumentHistoryPage(historyWire({ workspace_id: otherWorkspaceId }), { workspaceId, documentId })).toThrow(DocumentHistoryApiError);
    expect(() => decodeDocumentHistoryPage(historyWire({ items: [externalEntry(), externalEntry()] }), { workspaceId, documentId })).toThrow(DocumentHistoryApiError);
    expect(() => decodeDocumentHistoryPage(historyWire({ items: [managedEntry({ proposal_id: null })] }), { workspaceId, documentId })).toThrow(DocumentHistoryApiError);
    expect(() => decodeDocumentHistoryPage(historyWire({ items: [managedEntry({ proposal_type: "unknown" })] }), { workspaceId, documentId })).toThrow(DocumentHistoryApiError);
    expect(() => decodeDocumentHistoryPage(historyWire({ items: [currentEntry({ parent_commits: [managedCommit] })] }), { workspaceId, documentId })).toThrow(DocumentHistoryApiError);
    expect(() => decodeDocumentHistoryPage(historyWire({ dirty: false }), { workspaceId, documentId })).toThrow(DocumentHistoryApiError);
    expect(() => decodeDocumentHistoryPage(historyWire({ path: "notes/a.txt" }), { workspaceId, documentId })).toThrow(DocumentHistoryApiError);
    expect(() => decodeDocumentHistoryPage(historyWire({ items: [managedEntry({
      article_revision_id: null,
      article_revision_no: null,
      proposal_id: null,
      proposal_revision_id: null,
      approval_id: null,
      workflow_run_id: null,
      writeback_id: null,
      proposal_type: null,
      approval_decided_at: null,
    })] }), { workspaceId, documentId })).toThrow(DocumentHistoryApiError);
  });

  it("lists with only bounded cursor pagination inputs", async () => {
    vi.mocked(fetch).mockResolvedValueOnce(jsonResponse(historyWire()));

    await expect(listDocumentHistory({ workspaceId, documentId, limit: 30, cursor: "signed-next-cursor" })).resolves.toMatchObject({ head, nextCursor: "signed-next-cursor" });

    const call = vi.mocked(fetch).mock.calls[0];
    expect(call?.[0]).toBe(`/api/v1/workspaces/${workspaceId}/documents/${documentId}/history?limit=30&cursor=signed-next-cursor`);
    expect(call?.[1]?.method).toBe("GET");
  });

  it("compares exact refs and binds the response to the requested HEAD and path", async () => {
    vi.mocked(fetch).mockResolvedValueOnce(jsonResponse(compareWire()));

    await expect(compareDocumentHistory({ workspaceId, documentId, expectedHead: head, expectedPath: "notes/history.md", left: managedCommit, right: documentWorktreeRef })).resolves.toMatchObject({
      left: managedCommit,
      right: documentWorktreeRef,
      leftContent: "# old\n",
      rightContent: "# current\n",
      diffHash: hash,
    });

    expect(vi.mocked(fetch).mock.calls[0]?.[0]).toBe(`/api/v1/workspaces/${workspaceId}/documents/${documentId}/history/compare?left=${managedCommit}&right=WORKTREE`);
    vi.mocked(fetch).mockResolvedValueOnce(jsonResponse(compareWire({ head: "f".repeat(40) })));
    await expect(compareDocumentHistory({ workspaceId, documentId, expectedHead: head, expectedPath: "notes/history.md", left: managedCommit, right: documentWorktreeRef })).rejects.toMatchObject({
      code: "HTTP_ERROR",
      errorCode: "DOCUMENT_HISTORY_CURSOR_STALE",
      status: 409,
    });
    vi.mocked(fetch).mockResolvedValueOnce(jsonResponse(compareWire({ path: "notes/renamed.md" })));
    await expect(compareDocumentHistory({ workspaceId, documentId, expectedHead: head, expectedPath: "notes/history.md", left: managedCommit, right: documentWorktreeRef })).rejects.toMatchObject({
      code: "HTTP_ERROR",
      errorCode: "DOCUMENT_HISTORY_CURSOR_STALE",
      status: 409,
    });
  });

  it("creates a HEAD/version-bound preview before a same-key restore Proposal", async () => {
    vi.mocked(fetch)
      .mockResolvedValueOnce(jsonResponse(previewWire()))
      .mockResolvedValueOnce(jsonResponse({
        proposal_id: proposalId,
        proposal_revision_id: proposalRevisionId,
        proposal_type: "restore_document",
        status: "ready_for_review",
        change_hash: hash,
        replayed: false,
      }, 201));

    const preview = await createDocumentRestorePreview({
      workspaceId,
      documentId,
      targetCommit: managedCommit,
      expectedHead: head,
      expectedDocumentVersion: 7,
    });
    await expect(createDocumentRestoreProposal({
      workspaceId,
      documentId,
      targetCommit: preview.targetCommit,
      expectedHead: preview.expectedHead,
      expectedDocumentVersion: preview.expectedDocumentVersion,
      previewHash: preview.previewHash,
      idempotencyKey: "restore-document-intent-1",
    })).resolves.toMatchObject({ proposalId, proposalType: "restore_document", replayed: false });

    const previewCall = vi.mocked(fetch).mock.calls[0];
    expect(previewCall?.[1]?.body).toBe(JSON.stringify({ target_commit: managedCommit, expected_document_version: 7 }));
    const proposalCall = vi.mocked(fetch).mock.calls[1];
    expect(new Headers(proposalCall?.[1]?.headers).get("Idempotency-Key")).toBe("restore-document-intent-1");
    expect(proposalCall?.[1]?.body).toBe(JSON.stringify({
      target_commit: managedCommit,
      expected_head: head,
      expected_document_version: 7,
      preview_hash: otherHash,
    }));
  });

  it("accepts an exact restore Proposal replay after its lifecycle advanced", async () => {
    vi.mocked(fetch).mockResolvedValueOnce(jsonResponse({
      proposal_id: proposalId,
      proposal_revision_id: proposalRevisionId,
      proposal_type: "restore_document",
      status: "approved",
      change_hash: hash,
      replayed: true,
    }));

    await expect(createDocumentRestoreProposal({
      workspaceId,
      documentId,
      targetCommit: managedCommit,
      expectedHead: head,
      expectedDocumentVersion: 7,
      previewHash: otherHash,
      idempotencyKey: "restore-document-intent-1",
    })).resolves.toMatchObject({ status: "approved", replayed: true });
  });

  it("fails a restore preview closed when the returned HEAD or Document version drifted", async () => {
    vi.mocked(fetch).mockResolvedValueOnce(jsonResponse(previewWire({ expected_head: "f".repeat(40) })));

    await expect(createDocumentRestorePreview({
      workspaceId,
      documentId,
      targetCommit: managedCommit,
      expectedHead: head,
      expectedDocumentVersion: 7,
    })).rejects.toMatchObject({
      code: "HTTP_ERROR",
      errorCode: "DOCUMENT_HISTORY_CURSOR_STALE",
      status: 409,
    });
  });

  it("keeps typed stale/dirty Problems and Abort identity", async () => {
    vi.mocked(fetch)
      .mockResolvedValueOnce(jsonResponse({ error_code: "DOCUMENT_HISTORY_CURSOR_STALE", message: "history baseline changed", retryable: false }, 409))
      .mockRejectedValueOnce(new DOMException("cancelled", "AbortError"));

    await expect(listDocumentHistory({ workspaceId, documentId, cursor: "stale", limit: 30 })).rejects.toMatchObject({
      code: "HTTP_ERROR",
      errorCode: "DOCUMENT_HISTORY_CURSOR_STALE",
      status: 409,
    });
    const controller = new AbortController();
    controller.abort();
    await expect(listDocumentHistory({ workspaceId, documentId }, controller.signal)).rejects.toMatchObject({ name: "AbortError" });
  });
});
