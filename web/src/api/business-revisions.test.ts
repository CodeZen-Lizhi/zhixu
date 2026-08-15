import { afterEach, describe, expect, it, vi } from "vitest";

import {
  appendProposalRevision,
  decodeProposalRevisionHistory,
  decodeProposalRevisionHistoryDetail,
  decodeProposalRevisionPreview,
  getProposalRevision,
  listProposalRevisions,
  previewProposalRevision,
  proposalRevisionMergeAlgorithm,
  proposalRevisionMergeAlgorithmVersion,
  type AppendProposalRevisionInput,
  type ProposalRevisionPreviewBinding,
} from "./business-revisions";

afterEach(() => vi.unstubAllGlobals());

const workspaceId = "10000000-0000-4000-8000-000000000001";
const proposalId = "10000000-0000-4000-8000-000000000002";
const sourceRevisionId = "10000000-0000-4000-8000-000000000003";
const nextRevisionId = "10000000-0000-4000-8000-000000000004";
const approvalId = "10000000-0000-4000-8000-000000000005";
const workflowId = "10000000-0000-4000-8000-000000000006";
const binding: ProposalRevisionPreviewBinding = {
  workspaceId,
  proposalId,
  expectedProposalVersion: 7,
  sourceRevisionId,
  sourceRevisionNo: 1,
  sourceChangeHash: "c".repeat(64),
  targetPath: "notes/example.md",
};

const sha256Hex = async (value: string): Promise<string> => {
  const digest = await crypto.subtle.digest("SHA-256", new TextEncoder().encode(value));
  return [...new Uint8Array(digest)].map((byte) => byte.toString(16).padStart(2, "0")).join("");
};

const textSnapshot = async (content: string) => ({
  content,
  hash: await sha256Hex(content),
  byte_size: new TextEncoder().encode(content).byteLength,
});

const previewResponse = async (overrides: Record<string, unknown> = {}) => ({
  schema_version: "proposal-text-merge-preview/v1",
  merge_algorithm: proposalRevisionMergeAlgorithm,
  merge_algorithm_version: proposalRevisionMergeAlgorithmVersion,
  merge_fingerprint: "f".repeat(64),
  proposal_id: proposalId,
  workspace_id: workspaceId,
  proposal_version: 7,
  source_revision_id: sourceRevisionId,
  source_revision_no: 1,
  source_change_hash: "c".repeat(64),
  target_path: "notes/example.md",
  target_mode: "REPLACE",
  base: await textSnapshot("# Base\n"),
  current: await textSnapshot("# Current\n"),
  proposed: await textSnapshot("# Proposed\n"),
  candidate: await textSnapshot("# Candidate\n"),
  conflict_count: 1,
  conflicts: [{ id: "d".repeat(64), ordinal: 1, base: "Base", current: "Current", proposed: "Proposed" }],
  ...overrides,
});

const appendInput = async (): Promise<AppendProposalRevisionInput> => ({
  expectedProposalVersion: 7,
  sourceRevisionId,
  sourceRevisionNo: 1,
  sourceChangeHash: "c".repeat(64),
  expectedCurrentHash: await sha256Hex("# Current\n"),
  mergeFingerprint: "f".repeat(64),
  mergeAlgorithm: proposalRevisionMergeAlgorithm,
  mergeAlgorithmVersion: proposalRevisionMergeAlgorithmVersion,
  content: "# Candidate\n",
  evidenceSummary: "更新后的证据",
  risk: "低风险",
  rollbackPlan: "恢复上一版本",
  resolvedConflictIds: ["d".repeat(64)],
});

const appendResponse = (input: AppendProposalRevisionInput, overrides: Record<string, unknown> = {}) => ({
  proposal_type: "file_patch",
  id: proposalId,
  workspace_id: workspaceId,
  target_path: "notes/example.md",
  status: "ready_for_review",
  risk_level: "LOW",
  version: 8,
  revision_capability: { editable: true, reason: "AVAILABLE" },
  revision: {
    id: nextRevisionId,
    revision_no: 2,
    target_mode: "REPLACE",
    base_hash: input.expectedCurrentHash,
    content: input.content,
    evidence_summary: input.evidenceSummary,
    risk: input.risk,
    rollback_plan: input.rollbackPlan,
    change_hash: "b".repeat(64),
    created_at: "2026-08-14T08:00:00Z",
  },
  approval: null,
  created_at: "2026-08-13T08:00:00Z",
  updated_at: "2026-08-14T08:00:00Z",
  replayed: false,
  ...overrides,
});

const historyBinding = {
  workspaceId,
  proposalId,
  targetPath: "notes/example.md",
};

const historyItem = (revisionId: string, revisionNo: number, current: boolean) => ({
  proposal_id: proposalId,
  revision_id: revisionId,
  revision_no: revisionNo,
  target_path: "notes/example.md",
  target_mode: "REPLACE",
  base_hash: "a".repeat(64),
  change_hash: revisionNo === 2 ? "b".repeat(64) : "c".repeat(64),
  base_available: true,
  current,
  approval: null,
  workflow: null,
  created_at: `2026-08-${revisionNo === 2 ? "14" : "13"}T08:00:00Z`,
});

describe("Proposal Revision API", () => {
  it("sends only the source binding and strictly decodes the four server-owned texts", async () => {
    const fetcher = vi.fn().mockResolvedValue(new Response(JSON.stringify(await previewResponse()), { status: 200 }));
    vi.stubGlobal("fetch", fetcher);

    await expect(previewProposalRevision(binding)).resolves.toMatchObject({
      proposalVersion: 7,
      sourceRevisionId,
      targetMode: "REPLACE",
      conflictCount: 1,
      candidate: { content: "# Candidate\n" },
    });
    const request = fetcher.mock.calls[0]?.[1] as RequestInit;
    expect(fetcher.mock.calls[0]?.[0]).toBe(`/api/v1/proposals/${proposalId}/revision-merge-previews`);
    expect(request.cache).toBe("no-store");
    if (typeof request.body !== "string") throw new Error("preview body was not serialized JSON");
    expect(JSON.parse(request.body)).toEqual({
      source_revision_id: sourceRevisionId,
      source_change_hash: "c".repeat(64),
      expected_proposal_version: 7,
    });
  });

  it.each([
    ["unknown field", async () => previewResponse({ internal_path: "/private/workspace" })],
    ["wrong binding", async () => previewResponse({ proposal_version: 8 })],
    ["count mismatch", async () => previewResponse({ conflict_count: 0 })],
    ["duplicate IDs", async () => previewResponse({ conflict_count: 2, conflicts: [
      { id: "d".repeat(64), ordinal: 1, base: "a", current: "b", proposed: "c" },
      { id: "d".repeat(64), ordinal: 2, base: "a", current: "b", proposed: "c" },
    ] })],
    ["bad text hash", async () => previewResponse({ candidate: { content: "changed", hash: "a".repeat(64), byte_size: 7 } })],
    ["NUL text", async () => previewResponse({ candidate: await textSnapshot("invalid\u0000text") })],
  ])("rejects a preview with %s", async (_label, payload) => {
    await expect(decodeProposalRevisionPreview(await payload(), binding)).rejects.toMatchObject({ code: "INVALID_RESPONSE" });
  });

  it("appends with one exact key/body and verifies the returned revision binding", async () => {
    const input = await appendInput();
    const fetcher = vi.fn().mockResolvedValue(new Response(JSON.stringify(appendResponse(input)), { status: 201 }));
    vi.stubGlobal("fetch", fetcher);

    await expect(appendProposalRevision(workspaceId, proposalId, "proposal-revision-command-1", input)).resolves.toMatchObject({
      replayed: false,
      proposal: { status: "ready_for_review", revision: { id: nextRevisionId, revisionNo: 2 } },
    });
    const request = fetcher.mock.calls[0]?.[1] as RequestInit;
    expect(new Headers(request.headers).get("Idempotency-Key")).toBe("proposal-revision-command-1");
    if (typeof request.body !== "string") throw new Error("append body was not serialized JSON");
    expect(JSON.parse(request.body)).toEqual({
      expected_proposal_version: 7,
      source_revision_id: sourceRevisionId,
      source_change_hash: "c".repeat(64),
      expected_current_hash: input.expectedCurrentHash,
      merge_fingerprint: "f".repeat(64),
      merge_algorithm: proposalRevisionMergeAlgorithm,
      merge_algorithm_version: proposalRevisionMergeAlgorithmVersion,
      content: "# Candidate\n",
      evidence_summary: "更新后的证据",
      risk: "低风险",
      rollback_plan: "恢复上一版本",
      resolved_conflict_ids: ["d".repeat(64)],
    });
  });

  it("accepts a metadata-only revision whose content change hash matches its source", async () => {
    const input = await appendInput();
    const response = appendResponse(input, {
      revision: {
        id: nextRevisionId,
        revision_no: 2,
        target_mode: "REPLACE",
        base_hash: input.expectedCurrentHash,
        content: input.content,
        evidence_summary: input.evidenceSummary,
        risk: input.risk,
        rollback_plan: input.rollbackPlan,
        change_hash: input.sourceChangeHash,
        created_at: "2026-08-14T08:00:00Z",
      },
    });
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify(response), { status: 201 })));

    await expect(appendProposalRevision(workspaceId, proposalId, "proposal-revision-metadata-only", input)).resolves.toMatchObject({
      proposal: { revision: { changeHash: input.sourceChangeHash } },
      replayed: false,
    });
  });

  it("retains a strictly decoded stable Problem without exposing invalid details", async () => {
    vi.stubGlobal("fetch", vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({
        error_code: "PROPOSAL_REVISION_STALE",
        message: "Proposal 已变化",
        retryable: false,
        details: { current_version: 8, conflict_type: "target_base_hash_changed" },
      }), { status: 409 }))
      .mockResolvedValueOnce(new Response(JSON.stringify({
        error_code: "PROPOSAL_REVISION_STALE",
        message: "private path /secret",
        retryable: false,
        details: { internal: { path: "/secret" } },
      }), { status: 409 })));

    await expect(previewProposalRevision(binding)).rejects.toMatchObject({
      errorCode: "PROPOSAL_REVISION_STALE",
      retryable: false,
      details: { current_version: 8 },
    });
    await expect(previewProposalRevision(binding)).rejects.toMatchObject({
      errorCode: undefined,
      message: "业务 API 请求失败（HTTP 409）。",
    });
  });

  it("rejects an append response that reuses approval or the source revision", async () => {
    const input = await appendInput();
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify(appendResponse(input, {
      revision: {
        id: sourceRevisionId,
        revision_no: 2,
        target_mode: "REPLACE",
        base_hash: input.expectedCurrentHash,
        content: input.content,
        evidence_summary: input.evidenceSummary,
        risk: input.risk,
        rollback_plan: input.rollbackPlan,
        change_hash: "b".repeat(64),
        created_at: "2026-08-14T08:00:00Z",
      },
    })), { status: 201 })));

    await expect(appendProposalRevision(workspaceId, proposalId, "proposal-revision-command-2", input)).rejects.toMatchObject({ code: "INVALID_RESPONSE" });
  });

  it("decodes newest-first history and sends the bounded revision cursor", async () => {
    const response = {
      items: [historyItem(nextRevisionId, 2, true), historyItem(sourceRevisionId, 1, false)],
      next_before_revision_no: 1,
    };
    expect(() => decodeProposalRevisionHistory(response, historyBinding, 30)).toThrow(expect.objectContaining({ code: "INVALID_RESPONSE" }));
    delete (response as { next_before_revision_no?: number }).next_before_revision_no;
    const fetcher = vi.fn().mockResolvedValue(new Response(JSON.stringify(response), { status: 200 }));
    vi.stubGlobal("fetch", fetcher);

    await expect(listProposalRevisions(historyBinding, { limit: 30, beforeRevisionNo: 3 })).resolves.toMatchObject({
      items: [
        { revisionId: nextRevisionId, revisionNo: 2, current: true },
        { revisionId: sourceRevisionId, revisionNo: 1, current: false },
      ],
    });
    expect(fetcher.mock.calls[0]?.[0]).toBe(`/api/v1/proposals/${proposalId}/revisions?limit=30&before_revision_no=3`);
    expect((fetcher.mock.calls[0]?.[1] as RequestInit).cache).toBe("no-store");
  });

  it("strictly binds historical base, lineage, approval, and Workflow facts", async () => {
    const baseContent = "# Current\n";
    const baseHash = await sha256Hex(baseContent);
    const detailBinding = {
      ...historyBinding,
      revisionId: nextRevisionId,
      revisionNo: 2,
      baseHash,
      changeHash: "b".repeat(64),
    };
    const response = {
      workspace_id: workspaceId,
      proposal_id: proposalId,
      current_revision_id: nextRevisionId,
      current: true,
      revision: {
        id: nextRevisionId,
        revision_no: 2,
        target_mode: "REPLACE",
        base_hash: baseHash,
        content: "# Candidate\n",
        evidence_summary: "更新后的证据",
        risk: "低风险",
        rollback_plan: "恢复上一版本",
        change_hash: "b".repeat(64),
        created_at: "2026-08-14T08:00:00Z",
      },
      base_available: true,
      base_snapshot: {
        hash: baseHash,
        content: baseContent,
        byte_size: new TextEncoder().encode(baseContent).byteLength,
        schema_version: "proposal-base-snapshot/v1",
        created_at: "2026-08-14T07:59:59Z",
      },
      lineage: {
        source_revision_id: sourceRevisionId,
        source_change_hash: "c".repeat(64),
        kind: "THREE_WAY_MERGE",
        merge_algorithm: proposalRevisionMergeAlgorithm,
        merge_algorithm_version: proposalRevisionMergeAlgorithmVersion,
        merge_fingerprint: "f".repeat(64),
        created_at: "2026-08-14T08:00:00Z",
      },
      approval: {
        id: approvalId,
        proposal_id: proposalId,
        revision_id: nextRevisionId,
        change_hash: "b".repeat(64),
        decision: "approved",
        approved_git_head: "d".repeat(40),
        decided_at: "2026-08-14T09:00:00Z",
      },
      workflow: { id: workflowId, status_url: `/api/v1/workflows/${workflowId}` },
    };
    const fetcher = vi.fn().mockResolvedValue(new Response(JSON.stringify(response), { status: 200 }));
    vi.stubGlobal("fetch", fetcher);

    await expect(getProposalRevision(detailBinding)).resolves.toMatchObject({
      current: true,
      revision: { id: nextRevisionId, revisionNo: 2 },
      baseSnapshot: { content: baseContent, hash: baseHash },
      lineage: { sourceRevisionId, kind: "THREE_WAY_MERGE" },
      approval: { id: approvalId, decision: "approved" },
      workflow: { id: workflowId },
    });
    expect(fetcher.mock.calls[0]?.[0]).toBe(`/api/v1/proposals/${proposalId}/revisions/${nextRevisionId}`);

    await expect(decodeProposalRevisionHistoryDetail({
      ...response,
      base_snapshot: { ...response.base_snapshot, hash: "e".repeat(64) },
    }, detailBinding)).rejects.toMatchObject({ code: "INVALID_RESPONSE" });
  });
});
