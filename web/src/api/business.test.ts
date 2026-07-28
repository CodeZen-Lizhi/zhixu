import { afterEach, describe, expect, it, vi } from "vitest";
import { controlWorkflow, decideProposal, getProposal, getProposalCurrentContent, getSourceVersion, getWorkflow, listProposals, listWorkflows, preflightProposal } from "./business";

afterEach(() => vi.unstubAllGlobals());

const workspaceId = "10000000-0000-4000-8000-000000000002";
const sourceVersionId = "10000000-0000-4000-8000-000000000004";
const sourceVersionResponse = (overrides: Record<string, unknown> = {}) => ({
  workspace_id: workspaceId,
  source_id: "10000000-0000-4000-8000-000000000003",
  source_version_id: sourceVersionId,
  source_type: "file",
  logical_name: "历史资料",
  relative_path: "docs/history.md",
  content_hash: "a".repeat(64),
  byte_size: 42,
  media_type: "text/markdown",
  security_status: "passed",
  captured_at: "2026-07-01T00:00:00Z",
  ...overrides,
});
const proposalId = "10000000-0000-4000-8000-000000000001";
const revisionId = "10000000-0000-4000-8000-000000000003";
const approvalId = "10000000-0000-4000-8000-000000000004";
const workflowRunId = "10000000-0000-4000-8000-000000000005";
const fileProposalResponse = (
  approval: Record<string, unknown>,
  proposalOverrides: Record<string, unknown> = {},
) => ({
  id: proposalId,
  workspace_id: workspaceId,
  proposal_type: "file_patch",
  target_path: "docs/a.md",
  status: "approved",
  risk_level: "LOW",
  revision: {
    id: revisionId,
    revision_no: 1,
    base_hash: "a".repeat(64),
    content: "next",
    evidence_summary: "evidence",
    risk: "low",
    rollback_plan: "revert",
    change_hash: "b".repeat(64),
    created_at: "2026-07-22T00:00:00Z",
  },
  approval: {
    id: approvalId,
    proposal_id: proposalId,
    revision_id: revisionId,
    change_hash: "b".repeat(64),
    decision: "approved",
    decided_at: "2026-07-22T00:02:00Z",
    ...approval,
  },
  created_at: "2026-07-22T00:00:00Z",
  updated_at: "2026-07-22T00:02:00Z",
  ...proposalOverrides,
});
const knowledgeProposalResponse = (
  revisionOverrides: Record<string, unknown> = {},
  proposalOverrides: Record<string, unknown> = {},
) => ({
  id: proposalId,
  workspace_id: workspaceId,
  proposal_type: "knowledge_change",
  status: "ready_for_review",
  risk_level: "HIGH",
  revision: {
    id: revisionId,
    revision_no: 1,
    schema_version: "knowledge-relation-change/v1",
    target_refs: [{ type: "RELATION_CANDIDATE", id: "10000000-0000-4000-8000-000000000006", fingerprint: "c".repeat(64) }],
    base_versions: [
      { node_type: "CLAIM", node_id: "10000000-0000-4000-8000-000000000007", version: 1 },
      { node_type: "TOPIC", node_id: "10000000-0000-4000-8000-000000000008", version: 2 },
    ],
    change_set: {
      operation: "CREATE_RELATION",
      source: { type: "CLAIM", id: "10000000-0000-4000-8000-000000000007", version: 1 },
      target: { type: "TOPIC", id: "10000000-0000-4000-8000-000000000008", version: 2 },
      relation_type: "BELONGS_TO",
    },
    evidence_refs: [{ candidate_evidence_id: "10000000-0000-4000-8000-000000000009", semantic_hash: "d".repeat(64) }],
    risk: "medium",
    rollback_plan: "remove relation",
    change_hash: "b".repeat(64),
    created_at: "2026-07-22T00:00:00Z",
    ...revisionOverrides,
  },
  created_at: "2026-07-22T00:00:00Z",
  updated_at: "2026-07-22T00:01:00Z",
  ...proposalOverrides,
});
const publishArtifactProposalResponse = (
  revisionOverrides: Record<string, unknown> = {},
  publicationOverrides: Record<string, unknown> = {},
  proposalOverrides: Record<string, unknown> = {},
) => ({
  id: proposalId,
  workspace_id: workspaceId,
  proposal_type: "publish_artifact",
  status: "ready_for_review",
  risk_level: "HIGH",
  revision: {
    id: revisionId,
    revision_no: 1,
    publication: {
      workspace_id: workspaceId,
      artifact_id: "10000000-0000-4000-8000-000000000006",
      revision_id: "10000000-0000-4000-8000-000000000007",
      revision_no: 3,
      artifact_version: 5,
      content_hash: "c".repeat(64),
      source_coverage: [
        { section_key: "intro", status: "COVERED", gaps: [] },
        { section_key: "limits", status: "GAP", gaps: [{ code: "NO_SOURCE", description: "没有可验证来源" }] },
      ],
      schema_version: "artifact-publication/v1",
      ...publicationOverrides,
    },
    risk: "publish approved artifact",
    rollback_plan: "keep artifact isolated",
    change_hash: "f".repeat(64),
    created_at: "2026-07-26T00:00:00Z",
    ...revisionOverrides,
  },
  approval: null,
  created_at: "2026-07-26T00:00:00Z",
  updated_at: "2026-07-26T00:01:00Z",
  ...proposalOverrides,
});
const downstreamArtifactProposalResponse = (
  updateOverrides: Record<string, unknown> = {},
  revisionOverrides: Record<string, unknown> = {},
  proposalOverrides: Record<string, unknown> = {},
) => ({
  id: proposalId,
  workspace_id: workspaceId,
  proposal_type: "downstream_update",
  status: "ready_for_review",
  risk_level: "HIGH",
  revision: {
    id: revisionId,
    revision_no: 1,
    update: {
      workspace_id: workspaceId,
      source_report: {
        id: "10000000-0000-4000-8000-000000000006",
        analysis_version: "impact-analysis/v2",
        fingerprint: "a".repeat(64),
      },
      source_event: { id: "10000000-0000-4000-8000-000000000007", event_version: 2 },
      target_type: "ARTIFACT",
      target_id: "10000000-0000-4000-8000-000000000008",
      base_version: 5,
      action: "REGENERATE_ARTIFACT",
      artifact_binding: {
        artifact_id: "10000000-0000-4000-8000-000000000008",
        artifact_version: 5,
        revision_id: "10000000-0000-4000-8000-000000000009",
        revision_no: 3,
        content_hash: "b".repeat(64),
      },
      reason: "引用来源发生变化",
      schema_version: "impact-downstream-update/v1",
      ...updateOverrides,
    },
    risk: "Impact report identified an owner-backed downstream dependency",
    rollback_plan: "No target write has executed",
    change_hash: "c".repeat(64),
    created_at: "2026-07-28T00:00:00Z",
    ...revisionOverrides,
  },
  approval: null,
  created_at: "2026-07-28T00:00:00Z",
  updated_at: "2026-07-28T00:01:00Z",
  ...proposalOverrides,
});
const downstreamReviewCardProposalResponse = () => downstreamArtifactProposalResponse({
  target_type: "REVIEW_CARD",
  target_id: "10000000-0000-4000-8000-000000000010",
  base_version: 4,
  action: "REVALIDATE_REVIEW_CARD",
  artifact_binding: undefined,
  review_card_binding: {
    card_id: "10000000-0000-4000-8000-000000000010",
    card_version: 4,
    status: "INVALIDATED",
    fingerprint: "d".repeat(64),
    claim_id: "10000000-0000-4000-8000-000000000011",
    evidence_binding_fingerprint: "e".repeat(64),
  },
});
const proposalSummaryResponse = (overrides: Record<string, unknown> = {}) => ({
  id: proposalId,
  workspace_id: workspaceId,
  proposal_type: "file_patch",
  status: "ready_for_review",
  target: "docs/a.md",
  risk_level: "LOW",
  risk: "风险说明",
  revision_id: revisionId,
  change_hash: "b".repeat(64),
  created_at: "2026-07-22T00:00:00Z",
  updated_at: "2026-07-22T00:01:00Z",
  ...overrides,
});
const knowledgeEndpointOverrides = (
  relationType: string,
  sourceType: "TOPIC" | "CLAIM",
  targetType: "TOPIC" | "CLAIM",
  sourceId = "10000000-0000-4000-8000-000000000007",
  targetId = "10000000-0000-4000-8000-000000000008",
) => ({
  base_versions: [
    { node_type: sourceType, node_id: sourceId, version: 1 },
    { node_type: targetType, node_id: targetId, version: 2 },
  ],
  change_set: {
    operation: "CREATE_RELATION",
    source: { type: sourceType, id: sourceId, version: 1 },
    target: { type: targetType, id: targetId, version: 2 },
    relation_type: relationType,
  },
});

describe("business API boundary", () => {
  it("decodes the backend Proposal summary Approval and Workflow binding", async () => {
    const proposalId = "10000000-0000-4000-8000-000000000001";
    const revisionId = "10000000-0000-4000-8000-000000000003";
    const workflowRunId = "10000000-0000-4000-8000-000000000005";
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({ items: [{
      id: proposalId,
      workspace_id: workspaceId,
      proposal_type: "file_patch",
      status: "approved",
      target: "docs/a.md",
      risk_level: "LOW",
      risk: "低风险说明",
      revision_id: revisionId,
      change_hash: "a".repeat(64),
      approval: {
        id: "10000000-0000-4000-8000-000000000004",
        proposal_id: proposalId,
        revision_id: revisionId,
        change_hash: "a".repeat(64),
        decision: "approved",
        approved_git_head: "b".repeat(40),
        workflow_run_id: workflowRunId,
        workflow_status_url: `/api/v1/workflows/${workflowRunId}`,
        decided_at: "2026-07-22T00:01:00Z",
      },
      created_at: "2026-07-22T00:00:00Z",
      updated_at: "2026-07-22T00:01:00Z",
    }] }), { status: 200, headers: { "Content-Type": "application/json" } })));
    const page = await listProposals("10000000-0000-4000-8000-000000000002");
    expect(page.items[0]).toMatchObject({ target: "docs/a.md", riskLevel: "LOW", risk: "低风险说明", revisionId, approval: { proposalId, revisionId, workflowRunId, decision: "approved" } });
  });

  it("accepts a Proposal summary with an omitted Approval snapshot", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({
      items: [proposalSummaryResponse()],
    }), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    })));

    const page = await listProposals(workspaceId);
    expect(page.items).toHaveLength(1);
    expect(page.items[0]).not.toHaveProperty("approval");
  });

  it.each([
    ["missing", undefined],
    ["empty", ""],
    ["lowercase", "high"],
    ["unknown", "SEVERE"],
  ])("rejects a Proposal summary with %s risk_level", async (_label, riskLevel) => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({ items: [proposalSummaryResponse({ risk_level: riskLevel })] }), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    })));

    await expect(listProposals(workspaceId)).rejects.toMatchObject({ code: "INVALID_RESPONSE" });
  });

  it("rejects a knowledge_change Proposal summary without HIGH risk", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({
      items: [proposalSummaryResponse({ proposal_type: "knowledge_change", risk_level: "MEDIUM" })],
    }), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    })));

    await expect(listProposals(workspaceId)).rejects.toMatchObject({ code: "INVALID_RESPONSE" });
  });

  it("decodes and filters publish_artifact Proposal summaries", async () => {
    const fetcher = vi.fn().mockResolvedValue(new Response(JSON.stringify({
      items: [proposalSummaryResponse({ proposal_type: "publish_artifact", target: "Artifact 发布", risk_level: "HIGH" })],
    }), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    }));
    vi.stubGlobal("fetch", fetcher);

    await expect(listProposals(workspaceId, { type: "publish_artifact" })).resolves.toMatchObject({
      items: [{ type: "publish_artifact", target: "Artifact 发布", riskLevel: "HIGH" }],
    });
    expect(fetcher.mock.calls[0]?.[0]).toContain("proposal_type=publish_artifact");
  });

  it("rejects a publish_artifact Proposal summary without HIGH risk", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({
      items: [proposalSummaryResponse({ proposal_type: "publish_artifact", risk_level: "MEDIUM" })],
    }), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    })));

    await expect(listProposals(workspaceId)).rejects.toMatchObject({ code: "INVALID_RESPONSE" });
  });

  it("preserves idempotency and JSON headers for decisions", async () => {
    const proposalId = "10000000-0000-4000-8000-000000000001";
    const revisionId = "10000000-0000-4000-8000-000000000003";
    const workflowRunId = "10000000-0000-4000-8000-000000000004";
    const response = {
      id: "10000000-0000-4000-8000-000000000005",
      proposal_id: proposalId,
      revision_id: revisionId,
      change_hash: "a".repeat(64),
      decision: "approved",
      approved_git_head: "b".repeat(40),
      workflow_run_id: workflowRunId,
      workflow_status_url: `/api/v1/workflows/${workflowRunId}`,
      dispatch_status: "queued",
      decided_at: "2026-07-22T00:00:00Z",
    };
    const fetcher = vi.fn().mockImplementation(() => Promise.resolve(new Response(JSON.stringify(response), { status: 200, headers: { "Content-Type": "application/json" } })));
    vi.stubGlobal("fetch", fetcher);
    await expect(decideProposal(proposalId, { revisionId, changeHash: "a".repeat(64), decision: "approved", proposalType: "file_patch" })).resolves.toMatchObject({ proposalId, revisionId, workflowRunId });
    await decideProposal(proposalId, { revisionId, changeHash: "a".repeat(64), decision: "approved", proposalType: "file_patch" });
    const init = fetcher.mock.calls[0]?.[1] as RequestInit;
    const headers = new Headers(init.headers);
    expect(headers.get("Content-Type")).toBe("application/json");
    expect(headers.get("Idempotency-Key")).toContain("m9-approval-");
    expect(new Headers((fetcher.mock.calls[1]?.[1] as RequestInit).headers).get("Idempotency-Key")).toBe(headers.get("Idempotency-Key"));
  });

  it("accepts an approved Knowledge decision without Git or Workflow writeback fields", async () => {
    const proposalId = "10000000-0000-4000-8000-000000000001";
    const revisionId = "10000000-0000-4000-8000-000000000003";
    const response = {
      id: "10000000-0000-4000-8000-000000000005",
      proposal_id: proposalId,
      revision_id: revisionId,
      change_hash: "a".repeat(64),
      decision: "approved",
      decided_at: "2026-07-22T00:00:00Z",
    };
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify(response), { status: 201, headers: { "Content-Type": "application/json" } })));

    await expect(decideProposal(proposalId, {
      revisionId,
      changeHash: "a".repeat(64),
      decision: "approved",
      proposalType: "knowledge_change",
    })).resolves.toEqual({
      id: response.id,
      proposalId,
      revisionId,
      changeHash: response.change_hash,
      decision: "approved",
      decidedAt: response.decided_at,
    });
  });

  it("accepts a publish_artifact Approval without claiming a writeback Workflow", async () => {
    const response = {
      id: "10000000-0000-4000-8000-000000000005",
      proposal_id: proposalId,
      revision_id: revisionId,
      change_hash: "a".repeat(64),
      decision: "approved",
      decided_at: "2026-07-26T00:00:00Z",
    };
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify(response), { status: 201, headers: { "Content-Type": "application/json" } })));

    await expect(decideProposal(proposalId, {
      revisionId,
      changeHash: response.change_hash,
      decision: "approved",
      proposalType: "publish_artifact",
    })).resolves.toMatchObject({ proposalId, revisionId, decision: "approved" });
  });

  it("rejects a publish_artifact Approval decision that claims file writeback", async () => {
    const response = {
      id: "10000000-0000-4000-8000-000000000005",
      proposal_id: proposalId,
      revision_id: revisionId,
      change_hash: "a".repeat(64),
      decision: "approved",
      approved_git_head: "b".repeat(40),
      decided_at: "2026-07-26T00:00:00Z",
    };
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify(response), { status: 201, headers: { "Content-Type": "application/json" } })));

    await expect(decideProposal(proposalId, {
      revisionId,
      changeHash: response.change_hash,
      decision: "approved",
      proposalType: "publish_artifact",
    })).rejects.toMatchObject({ code: "INVALID_RESPONSE" });
  });

  it("accepts a downstream_update Approval decision only without Git or Workflow fields", async () => {
    const response = {
      id: "10000000-0000-4000-8000-000000000005",
      proposal_id: proposalId,
      revision_id: revisionId,
      change_hash: "c".repeat(64),
      decision: "approved",
      decided_at: "2026-07-28T00:02:00Z",
    };
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify(response), { status: 201, headers: { "Content-Type": "application/json" } })));

    await expect(decideProposal(proposalId, {
      revisionId,
      changeHash: response.change_hash,
      decision: "approved",
      proposalType: "downstream_update",
    })).resolves.toMatchObject({ proposalId, revisionId, decision: "approved" });
  });

  it("encodes list filters and rejects unknown response fields", async () => {
    const fetcher = vi.fn().mockResolvedValue(new Response(JSON.stringify({ items: [], next_cursor: "cursor" }), { status: 200, headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", fetcher);
    await listProposals("10000000-0000-4000-8000-000000000002", { status: "ready_for_review", type: "file_patch", risk: "HIGH", createdAfter: "2026-07-01T00:00:00Z" });
    expect(fetcher.mock.calls[0]?.[0]).toContain("status=ready_for_review&proposal_type=file_patch&risk=HIGH&created_after=2026-07-01T00%3A00%3A00Z");

    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({ items: [], future: true }), { status: 200, headers: { "Content-Type": "application/json" } })));
    await expect(listProposals("10000000-0000-4000-8000-000000000002")).rejects.toMatchObject({ code: "INVALID_RESPONSE" });
  });

  it("encodes and decodes the downstream_update Proposal list filter", async () => {
    const fetcher = vi.fn().mockResolvedValue(new Response(JSON.stringify({
      items: [proposalSummaryResponse({
        proposal_type: "downstream_update",
        target: "ARTIFACT:10000000-0000-4000-8000-000000000008",
        risk_level: "HIGH",
      })],
    }), { status: 200, headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", fetcher);

    await expect(listProposals(workspaceId, { type: "downstream_update" })).resolves.toMatchObject({
      items: [{ type: "downstream_update", riskLevel: "HIGH" }],
    });
    expect(fetcher.mock.calls[0]?.[0]).toContain("proposal_type=downstream_update");
  });

  it("preserves AbortError identity during fetch and response body reads", async () => {
    const fetchAbort = new DOMException("fetch aborted", "AbortError");
    vi.stubGlobal("fetch", vi.fn().mockRejectedValue(fetchAbort));
    await expect(listProposals("10000000-0000-4000-8000-000000000002")).rejects.toBe(fetchAbort);

    const bodyAbort = new DOMException("body aborted", "AbortError");
    const response = new Response(JSON.stringify({ items: [] }), { status: 200, headers: { "Content-Type": "application/json" } });
    vi.spyOn(response, "json").mockRejectedValue(bodyAbort);
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(response));
    await expect(listProposals("10000000-0000-4000-8000-000000000002")).rejects.toBe(bodyAbort);
  });

  it("strictly decodes current content and apply preflight", async () => {
    const responses = [
      { proposal_id: "10000000-0000-4000-8000-000000000001", workspace_id: "10000000-0000-4000-8000-000000000002", target_path: "docs/a.md", content: "current", current_hash: "a".repeat(64), base_hash: "a".repeat(64), base_hash_match: true },
      { proposal_id: "10000000-0000-4000-8000-000000000001", revision_id: "10000000-0000-4000-8000-000000000003", change_hash: "a".repeat(64), base_hash: "a".repeat(64), preflight_passed: true, mode: "preflight_only", write_performed: false },
    ];
    const fetcher = vi.fn().mockImplementation(() => Promise.resolve(new Response(JSON.stringify(responses.shift()), { status: 200, headers: { "Content-Type": "application/json" } })));
    vi.stubGlobal("fetch", fetcher);
    await expect(getProposalCurrentContent(
      "10000000-0000-4000-8000-000000000002",
      "10000000-0000-4000-8000-000000000001",
      { targetPath: "docs/a.md", baseHash: "a".repeat(64) },
    )).resolves.toMatchObject({ baseHashMatch: true, content: "current" });
    await expect(preflightProposal("10000000-0000-4000-8000-000000000001", { revisionId: "10000000-0000-4000-8000-000000000003", changeHash: "a".repeat(64) })).resolves.toMatchObject({ preflightPassed: true, writePerformed: false });
    expect((fetcher.mock.calls[1]?.[1] as RequestInit).body).toContain("approved_change_hash");
  });

  it.each([
    ["目标路径", { target_path: "docs/other.md" }],
    ["Proposal 基线", { base_hash: "b".repeat(64) }],
    ["Hash 比较结果", { current_hash: "b".repeat(64), base_hash_match: true }],
  ])("rejects current-content with inconsistent %s", async (_label, override) => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({
      proposal_id: "10000000-0000-4000-8000-000000000001",
      workspace_id: "10000000-0000-4000-8000-000000000002",
      target_path: "docs/a.md",
      content: "current",
      current_hash: "a".repeat(64),
      base_hash: "a".repeat(64),
      base_hash_match: true,
      ...override,
    }), { status: 200, headers: { "Content-Type": "application/json" } })));

    await expect(getProposalCurrentContent(
      "10000000-0000-4000-8000-000000000002",
      "10000000-0000-4000-8000-000000000001",
      { targetPath: "docs/a.md", baseHash: "a".repeat(64) },
    )).rejects.toMatchObject({ code: "INVALID_RESPONSE" });
  });

  it("rejects current-content above the 1 MiB UTF-8 boundary", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({
      proposal_id: proposalId,
      workspace_id: workspaceId,
      target_path: "docs/a.md",
      content: "\u{1F600}".repeat(262145),
      current_hash: "a".repeat(64),
      base_hash: "a".repeat(64),
      base_hash_match: true,
    }), { status: 200, headers: { "Content-Type": "application/json" } })));

    await expect(getProposalCurrentContent(workspaceId, proposalId, {
      targetPath: "docs/a.md",
      baseHash: "a".repeat(64),
    })).rejects.toMatchObject({ code: "INVALID_RESPONSE" });
  });

  it("rejects preflight responses that drift from the approved request", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({
      proposal_id: "10000000-0000-4000-8000-000000000009",
      revision_id: "10000000-0000-4000-8000-000000000003",
      change_hash: "a".repeat(64),
      base_hash: "a".repeat(64),
      preflight_passed: true,
      mode: "preflight_only",
      write_performed: false,
    }), { status: 200, headers: { "Content-Type": "application/json" } })));

    await expect(preflightProposal("10000000-0000-4000-8000-000000000001", {
      revisionId: "10000000-0000-4000-8000-000000000003",
      changeHash: "a".repeat(64),
    })).rejects.toMatchObject({ code: "INVALID_RESPONSE" });
  });

  it("strictly binds Workflow control responses to the requested Run", async () => {
    const workflowId = "10000000-0000-4000-8000-000000000008";
    const fetcher = vi.fn().mockResolvedValue(new Response(JSON.stringify({
      workflow_run_id: workflowId,
      status: "paused",
      version: 3,
      status_url: `/api/v1/workflows/${workflowId}`,
      pause_requested: true,
      cancel_requested: false,
    }), { status: 200, headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", fetcher);

    await expect(controlWorkflow(workflowId, "pause", 2)).resolves.toMatchObject({ workflowRunId: workflowId, status: "paused", version: 3 });
    expect(new Headers((fetcher.mock.calls[0]?.[1] as RequestInit).headers).get("Idempotency-Key")).toBe(`m9-workflow-${workflowId}-pause-2`);
  });

  it("rejects a Workflow control response that does not advance the expected version", async () => {
    const workflowId = "10000000-0000-4000-8000-000000000008";
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({
      workflow_run_id: workflowId,
      status: "running",
      version: 2,
      status_url: `/api/v1/workflows/${workflowId}`,
      pause_requested: true,
      cancel_requested: false,
    }), { status: 200, headers: { "Content-Type": "application/json" } })));

    await expect(controlWorkflow(workflowId, "pause", 2)).rejects.toMatchObject({ code: "INVALID_RESPONSE" });
  });

  it("strictly decodes workflow status and preserves waiting-for-human projection", async () => {
    const item = {
        id: "10000000-0000-4000-8000-000000000001",
        workspace_id: "10000000-0000-4000-8000-000000000002",
        definition_key: "ingestion",
        definition_version: 1,
        status: "running",
        version: 2,
        created_at: "2026-07-22T00:00:00Z",
        updated_at: "2026-07-22T00:01:00Z",
        waiting_for_human: true,
        pause_requested: false,
        cancel_requested: false,
    };
    vi.stubGlobal("fetch", vi.fn().mockImplementation(() => Promise.resolve(new Response(JSON.stringify({ items: [{ ...item }] }), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    }))));

    await expect(listWorkflows("10000000-0000-4000-8000-000000000002")).resolves.toMatchObject({
      items: [{ status: "running", waitingForHuman: true }],
    });

    item.status = "future_status";
    await expect(listWorkflows("10000000-0000-4000-8000-000000000002")).rejects.toMatchObject({ code: "INVALID_RESPONSE" });
  });

  it("reads a source version through its Workspace-scoped detail endpoint", async () => {
    const fetcher = vi.fn().mockResolvedValue(new Response(JSON.stringify(sourceVersionResponse({
      ingestion_status: "parsed",
      workflow_status: "running",
      index_status: "included",
    })), { status: 200, headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", fetcher);

    await expect(getSourceVersion(
      workspaceId,
      sourceVersionId,
    )).resolves.toMatchObject({ id: sourceVersionId, path: "docs/history.md", ingestionStatus: "parsed", workflowStatus: "running", indexStatus: "included" });
    expect(fetcher.mock.calls[0]?.[0]).toBe(`/api/v1/workspaces/${workspaceId}/source-versions/${sourceVersionId}`);
  });

  it("preserves excluded and absent Source Version detail projections", async () => {
    const responses = [
      sourceVersionResponse({ ingestion_status: "parse_failed", workflow_status: "failed", index_status: "excluded" }),
      sourceVersionResponse(),
    ];
    vi.stubGlobal("fetch", vi.fn().mockImplementation(() => Promise.resolve(new Response(JSON.stringify(responses.shift()), { status: 200, headers: { "Content-Type": "application/json" } }))));

    await expect(getSourceVersion(workspaceId, sourceVersionId)).resolves.toMatchObject({
      ingestionStatus: "parse_failed",
      workflowStatus: "failed",
      indexStatus: "excluded",
    });
    const withoutProjection = await getSourceVersion(workspaceId, sourceVersionId);
    expect(withoutProjection).not.toHaveProperty("ingestionStatus");
    expect(withoutProjection).not.toHaveProperty("workflowStatus");
    expect(withoutProjection).not.toHaveProperty("indexStatus");
  });

  it.each([
    ["ingestion_status", { ingestion_status: "future" }],
    ["workflow_status", { workflow_status: "future" }],
    ["index_status", { index_status: "future" }],
    ["unknown field", { future: true }],
  ])("rejects an invalid Source Version detail %s", async (_label, override) => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify(sourceVersionResponse(override)), { status: 200, headers: { "Content-Type": "application/json" } })));

    await expect(getSourceVersion(workspaceId, sourceVersionId)).rejects.toMatchObject({ code: "INVALID_RESPONSE" });
  });

  it.each([
    ["Workspace", { workspace_id: "10000000-0000-4000-8000-000000000009" }],
    ["Source Version", { source_version_id: "10000000-0000-4000-8000-000000000009" }],
  ])("rejects a Source Version detail bound to another %s", async (_label, override) => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify(sourceVersionResponse(override)), { status: 200, headers: { "Content-Type": "application/json" } })));

    await expect(getSourceVersion(workspaceId, sourceVersionId)).rejects.toMatchObject({ code: "INVALID_RESPONSE" });
  });

  it("decodes file patch and knowledge change details into typed UI models", async () => {
    const common = {
      id: "10000000-0000-4000-8000-000000000001",
      workspace_id: "10000000-0000-4000-8000-000000000002",
      status: "ready_for_review",
      risk_level: "LOW",
      created_at: "2026-07-22T00:00:00Z",
      updated_at: "2026-07-22T00:01:00Z",
    };
    const responses = [
      { ...common, proposal_type: "file_patch", target_path: "docs/a.md", revision: { id: "10000000-0000-4000-8000-000000000003", revision_no: 1, base_hash: "a".repeat(64), content: "next", evidence_summary: "evidence", risk: "low", rollback_plan: "revert", change_hash: "b".repeat(64), created_at: "2026-07-22T00:00:00Z" }, approval: null },
      { ...common, proposal_type: "knowledge_change", risk_level: "HIGH", revision: { id: "10000000-0000-4000-8000-000000000004", revision_no: 2, schema_version: "knowledge-relation-change/v1", target_refs: [{ type: "RELATION_CANDIDATE", id: "10000000-0000-4000-8000-000000000005", fingerprint: "e".repeat(64) }], base_versions: [{ node_type: "CLAIM", node_id: "10000000-0000-4000-8000-000000000005", version: 3 }, { node_type: "TOPIC", node_id: "10000000-0000-4000-8000-000000000006", version: 2 }], change_set: { operation: "CREATE_RELATION", source: { type: "CLAIM", id: "10000000-0000-4000-8000-000000000005", version: 3 }, target: { type: "TOPIC", id: "10000000-0000-4000-8000-000000000006", version: 2 }, relation_type: "BELONGS_TO" }, evidence_refs: [{ candidate_evidence_id: "10000000-0000-4000-8000-000000000007", semantic_hash: "c".repeat(64) }], risk: "medium", rollback_plan: "remove relation", change_hash: "d".repeat(64), created_at: "2026-07-22T00:00:00Z" }, approval: null },
    ];
    vi.stubGlobal("fetch", vi.fn().mockImplementation(() => Promise.resolve(new Response(JSON.stringify(responses.shift()), { status: 200, headers: { "Content-Type": "application/json" } }))));

    await expect(getProposal(common.workspace_id, common.id)).resolves.toMatchObject({ type: "file_patch", riskLevel: "LOW", targetPath: "docs/a.md", revision: { risk: "low", baseHash: "a".repeat(64), changeHash: "b".repeat(64) } });
    await expect(getProposal(common.workspace_id, common.id)).resolves.toMatchObject({ type: "knowledge_change", riskLevel: "HIGH", revision: { risk: "medium", changeSet: { relationType: "BELONGS_TO" }, evidenceRefs: [{ semanticHash: "c".repeat(64) }] } });
  });

  it("decodes a frozen publish_artifact detail without a formal-knowledge success projection", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify(publishArtifactProposalResponse()), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    })));

    await expect(getProposal(workspaceId, proposalId)).resolves.toMatchObject({
      type: "publish_artifact",
      riskLevel: "HIGH",
      revision: {
        id: revisionId,
        publication: {
          workspaceId,
          artifactId: "10000000-0000-4000-8000-000000000006",
          revisionId: "10000000-0000-4000-8000-000000000007",
          revisionNo: 3,
          artifactVersion: 5,
          contentHash: "c".repeat(64),
          sourceCoverage: [
            { sectionKey: "intro", status: "COVERED", gaps: [] },
            { sectionKey: "limits", status: "GAP", gaps: [{ code: "NO_SOURCE", description: "没有可验证来源" }] },
          ],
          schemaVersion: "artifact-publication/v1",
        },
      },
    });
  });

  it.each([
    ["非 HIGH 风险", {}, {}, { risk_level: "MEDIUM" }],
    ["跨 Workspace publication", {}, { workspace_id: "10000000-0000-4000-8000-000000000009" }, {}],
    ["空来源覆盖", {}, { source_coverage: [] }, {}],
    ["COVERED 携带 Gap", {}, { source_coverage: [{ section_key: "intro", status: "COVERED", gaps: [{ code: "UNEXPECTED", description: "不应存在" }] }] }, {}],
    ["GAP 缺少 Gap", {}, { source_coverage: [{ section_key: "intro", status: "GAP", gaps: [] }] }, {}],
    ["重复章节覆盖", {}, { source_coverage: [{ section_key: "intro", status: "COVERED", gaps: [] }, { section_key: "intro", status: "COVERED", gaps: [] }] }, {}],
    ["非法 schema", {}, { schema_version: "artifact-publication/v2" }, {}],
    ["非法 Artifact version", {}, { artifact_version: 0 }, {}],
    ["未知 publication 字段", {}, { future: true }, {}],
  ])("rejects publish_artifact detail with %s", async (_label, revisionOverrides, publicationOverrides, proposalOverrides) => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify(publishArtifactProposalResponse(
      revisionOverrides,
      publicationOverrides,
      proposalOverrides,
    )), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    })));

    await expect(getProposal(workspaceId, proposalId)).rejects.toMatchObject({ code: "INVALID_RESPONSE" });
  });

  it("rejects publish_artifact Approval snapshots with file writeback fields", async () => {
    const workflowRunId = "10000000-0000-4000-8000-000000000008";
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify(publishArtifactProposalResponse({}, {}, {
      status: "approved",
      approval: {
        id: approvalId,
        proposal_id: proposalId,
        revision_id: revisionId,
        change_hash: "f".repeat(64),
        decision: "approved",
        approved_git_head: "a".repeat(40),
        workflow_run_id: workflowRunId,
        workflow_status_url: `/api/v1/workflows/${workflowRunId}`,
        decided_at: "2026-07-26T00:02:00Z",
      },
    })), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    })));

    await expect(getProposal(workspaceId, proposalId)).rejects.toMatchObject({ code: "INVALID_RESPONSE" });
  });

  it("decodes Artifact and Review Card downstream_update details without a replay projection", async () => {
    const responses = [downstreamArtifactProposalResponse(), downstreamReviewCardProposalResponse()];
    vi.stubGlobal("fetch", vi.fn().mockImplementation(() => Promise.resolve(new Response(JSON.stringify(responses.shift()), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    }))));

    const artifact = await getProposal(workspaceId, proposalId);
    expect(artifact).toMatchObject({
      type: "downstream_update",
      riskLevel: "HIGH",
      revision: {
        update: {
          workspaceId,
          sourceReport: { id: "10000000-0000-4000-8000-000000000006", analysisVersion: "impact-analysis/v2", fingerprint: "a".repeat(64) },
          sourceEvent: { id: "10000000-0000-4000-8000-000000000007", eventVersion: 2 },
          targetType: "ARTIFACT",
          targetId: "10000000-0000-4000-8000-000000000008",
          baseVersion: 5,
          action: "REGENERATE_ARTIFACT",
          artifactBinding: { artifactVersion: 5, revisionNo: 3, contentHash: "b".repeat(64) },
          schemaVersion: "impact-downstream-update/v1",
        },
      },
    });
    expect(artifact).not.toHaveProperty("replayed");

    await expect(getProposal(workspaceId, proposalId)).resolves.toMatchObject({
      type: "downstream_update",
      revision: {
        update: {
          targetType: "REVIEW_CARD",
          action: "REVALIDATE_REVIEW_CARD",
          reviewCardBinding: {
            cardId: "10000000-0000-4000-8000-000000000010",
            cardVersion: 4,
            status: "INVALIDATED",
            claimId: "10000000-0000-4000-8000-000000000011",
            fingerprint: "d".repeat(64),
            evidenceBindingFingerprint: "e".repeat(64),
          },
        },
      },
    });
  });

  it.each([
    ["non-HIGH risk", {}, {}, { risk_level: "MEDIUM" }],
    ["v1 analysis", { source_report: { id: "10000000-0000-4000-8000-000000000006", analysis_version: "impact-analysis/v1", fingerprint: "a".repeat(64) } }, {}, {}],
    ["unknown schema", { schema_version: "impact-downstream-update/v2" }, {}, {}],
    ["mismatched action", { action: "REVALIDATE_REVIEW_CARD" }, {}, {}],
    ["missing owner", { artifact_binding: undefined }, {}, {}],
    ["both owners", { review_card_binding: { card_id: "10000000-0000-4000-8000-000000000010", card_version: 4, status: "INVALIDATED", fingerprint: "d".repeat(64), claim_id: "10000000-0000-4000-8000-000000000011", evidence_binding_fingerprint: "e".repeat(64) } }, {}, {}],
    ["target drift", { target_id: "10000000-0000-4000-8000-000000000012" }, {}, {}],
    ["base version drift", { base_version: 6 }, {}, {}],
    ["unknown update field", { future: true }, {}, {}],
    ["create-only replay marker", {}, {}, { replayed: false }],
  ])("rejects downstream_update detail with %s", async (_label, updateOverrides, revisionOverrides, proposalOverrides) => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify(downstreamArtifactProposalResponse(
      updateOverrides,
      revisionOverrides,
      proposalOverrides,
    )), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    })));

    await expect(getProposal(workspaceId, proposalId)).rejects.toMatchObject({ code: "INVALID_RESPONSE" });
  });

  it("rejects downstream_update Approval snapshots with file writeback fields", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify(downstreamArtifactProposalResponse({}, {}, {
      status: "approved",
      approval: {
        id: approvalId,
        proposal_id: proposalId,
        revision_id: revisionId,
        change_hash: "c".repeat(64),
        decision: "approved",
        approved_git_head: "a".repeat(40),
        decided_at: "2026-07-28T00:02:00Z",
      },
    })), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    })));

    await expect(getProposal(workspaceId, proposalId)).rejects.toMatchObject({ code: "INVALID_RESPONSE" });
  });

  it("rejects a Proposal detail without the discriminator", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify(fileProposalResponse({}, { proposal_type: undefined })), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    })));

    await expect(getProposal(workspaceId, proposalId)).rejects.toMatchObject({ code: "INVALID_RESPONSE" });
  });

  it.each([
    ["missing", undefined],
    ["empty", ""],
    ["lowercase", "critical"],
    ["unknown", "SEVERE"],
  ])("rejects a Proposal detail with %s risk_level", async (_label, riskLevel) => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify(knowledgeProposalResponse({}, { risk_level: riskLevel })), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    })));

    await expect(getProposal(workspaceId, proposalId)).rejects.toMatchObject({ code: "INVALID_RESPONSE" });
  });

  it.each(["CRITICAL", "MEDIUM", "LOW"] as const)("rejects knowledge_change with non-HIGH risk level %s", async (riskLevel) => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify(knowledgeProposalResponse({}, { risk_level: riskLevel })), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    })));

    await expect(getProposal(workspaceId, proposalId)).rejects.toMatchObject({ code: "INVALID_RESPONSE" });
  });

  it.each(["risk", "rollback_plan"] as const)("rejects an empty Knowledge Change %s", async (field) => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify(knowledgeProposalResponse({
      [field]: "",
    })), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    })));

    await expect(getProposal(workspaceId, proposalId)).rejects.toMatchObject({ code: "INVALID_RESPONSE" });
  });

  it.each(["risk", "rollback_plan"] as const)("rejects Knowledge Change %s above 4096 UTF-8 bytes", async (field) => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify(knowledgeProposalResponse({
      [field]: "界".repeat(1366),
    })), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    })));

    await expect(getProposal(workspaceId, proposalId)).rejects.toMatchObject({ code: "INVALID_RESPONSE" });
  });

  it("rejects unknown fields inside Proposal revisions", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({
      id: "10000000-0000-4000-8000-000000000001",
      workspace_id: "10000000-0000-4000-8000-000000000002",
      target_path: "docs/a.md",
      status: "ready_for_review",
      risk_level: "LOW",
      revision: { id: "10000000-0000-4000-8000-000000000003", revision_no: 1, base_hash: "a".repeat(64), content: "next", evidence_summary: "evidence", risk: "low", rollback_plan: "revert", change_hash: "b".repeat(64), created_at: "2026-07-22T00:00:00Z", future: true },
      created_at: "2026-07-22T00:00:00Z",
      updated_at: "2026-07-22T00:01:00Z",
    }), { status: 200, headers: { "Content-Type": "application/json" } })));

    await expect(getProposal("10000000-0000-4000-8000-000000000002", "10000000-0000-4000-8000-000000000001")).rejects.toMatchObject({ code: "INVALID_RESPONSE" });
  });

  it.each([
    ["target_path", "proposal"],
    ["content", "revision"],
    ["evidence_summary", "revision"],
    ["risk", "revision"],
    ["rollback_plan", "revision"],
  ] as const)("rejects an empty File Patch %s", async (field, owner) => {
    const response = fileProposalResponse({});
    const invalidResponse = owner === "proposal"
      ? { ...response, [field]: "" }
      : { ...response, revision: { ...response.revision, [field]: "" } };
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify(invalidResponse), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    })));

    await expect(getProposal(workspaceId, proposalId)).rejects.toMatchObject({ code: "INVALID_RESPONSE" });
  });

  it.each([
    ["Revision", { revision_id: "10000000-0000-4000-8000-000000000009" }],
    ["Change Hash", { change_hash: "c".repeat(64) }],
  ])("rejects a Proposal Approval snapshot bound to another %s", async (_label, approvalOverride) => {
    const proposalId = "10000000-0000-4000-8000-000000000001";
    const workspaceId = "10000000-0000-4000-8000-000000000002";
    const revisionId = "10000000-0000-4000-8000-000000000003";
    const response = {
      id: proposalId,
      workspace_id: workspaceId,
      target_path: "docs/a.md",
      status: "approved",
      risk_level: "LOW",
      revision: {
        id: revisionId,
        revision_no: 1,
        base_hash: "a".repeat(64),
        content: "next",
        evidence_summary: "evidence",
        risk: "low",
        rollback_plan: "revert",
        change_hash: "b".repeat(64),
        created_at: "2026-07-22T00:00:00Z",
      },
      approval: {
        id: "10000000-0000-4000-8000-000000000004",
        proposal_id: proposalId,
        revision_id: revisionId,
        change_hash: "b".repeat(64),
        decision: "approved",
        decided_at: "2026-07-22T00:02:00Z",
        ...approvalOverride,
      },
      created_at: "2026-07-22T00:00:00Z",
      updated_at: "2026-07-22T00:02:00Z",
    };
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify(response), { status: 200, headers: { "Content-Type": "application/json" } })));

    await expect(getProposal(workspaceId, proposalId)).rejects.toMatchObject({ code: "INVALID_RESPONSE" });
  });

  it("returns a Revision-bound Approval snapshot for the review UI", async () => {
    const proposalId = "10000000-0000-4000-8000-000000000001";
    const workspaceId = "10000000-0000-4000-8000-000000000002";
    const revisionId = "10000000-0000-4000-8000-000000000003";
    const workflowRunId = "10000000-0000-4000-8000-000000000005";
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({
      proposal_type: "file_patch",
      id: proposalId,
      workspace_id: workspaceId,
      target_path: "docs/a.md",
      status: "approved",
      risk_level: "LOW",
      revision: { id: revisionId, revision_no: 1, base_hash: "a".repeat(64), content: "next", evidence_summary: "evidence", risk: "low", rollback_plan: "revert", change_hash: "b".repeat(64), created_at: "2026-07-22T00:00:00Z" },
      approval: { id: "10000000-0000-4000-8000-000000000004", proposal_id: proposalId, revision_id: revisionId, change_hash: "b".repeat(64), decision: "approved", approved_git_head: "c".repeat(40), workflow_run_id: workflowRunId, workflow_status_url: `/api/v1/workflows/${workflowRunId}`, decided_at: "2026-07-22T00:02:00Z" },
      created_at: "2026-07-22T00:00:00Z",
      updated_at: "2026-07-22T00:02:00Z",
    }), { status: 200, headers: { "Content-Type": "application/json" } })));

    await expect(getProposal(workspaceId, proposalId)).resolves.toMatchObject({
      approval: { proposalId, revisionId, workflowRunId, decision: "approved", writebackState: "bound" },
    });
  });

  it("accepts an OpenAPI-null Approval on an undecided Proposal detail", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify(fileProposalResponse({}, {
      status: "ready_for_review",
      approval: null,
    })), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    })));

    await expect(getProposal(workspaceId, proposalId)).resolves.not.toHaveProperty("approval");
  });

  it("rejects a Proposal detail when the nullable Approval field is missing", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify(fileProposalResponse({}, {
      status: "ready_for_review",
      approval: undefined,
    })), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    })));

    await expect(getProposal(workspaceId, proposalId)).rejects.toMatchObject({ code: "INVALID_RESPONSE" });
  });

  it("rejects a null Approval in the non-nullable Proposal summary contract", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({
      items: [proposalSummaryResponse({ approval: null })],
    }), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    })));

    await expect(listProposals(workspaceId)).rejects.toMatchObject({ code: "INVALID_RESPONSE" });
  });

  it.each([
    [
      "完整 Workflow 绑定",
      {
        approved_git_head: "c".repeat(40),
        workflow_run_id: workflowRunId,
        workflow_status_url: `/api/v1/workflows/${workflowRunId}`,
      },
      "bound",
    ],
    ["历史记录待重派发", { approved_git_head: "c".repeat(40) }, "pending_dispatch"],
    ["历史记录缺少 Git 基线", {}, "legacy_unrecoverable"],
  ] as const)("maps approved file patch %s to the expected writeback state", async (_label, approval, expectedState) => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify(fileProposalResponse(approval)), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    })));

    await expect(getProposal(workspaceId, proposalId)).resolves.toMatchObject({
      approval: { writebackState: expectedState },
    });
  });

  it.each([
    ["只有 Workflow ID", { approved_git_head: "c".repeat(40), workflow_run_id: workflowRunId }],
    ["只有 Workflow URL", { approved_git_head: "c".repeat(40), workflow_status_url: `/api/v1/workflows/${workflowRunId}` }],
    ["Workflow 缺少 Git 基线", { workflow_run_id: workflowRunId, workflow_status_url: `/api/v1/workflows/${workflowRunId}` }],
  ])("rejects approved file patch with %s", async (_label, approval) => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify(fileProposalResponse(approval)), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    })));

    await expect(getProposal(workspaceId, proposalId)).rejects.toMatchObject({ code: "INVALID_RESPONSE" });
  });

  it("rejects Proposal, current content, and Workflow responses from another Workspace", async () => {
    const workspaceA = "10000000-0000-4000-8000-000000000002";
    const workspaceB = "10000000-0000-4000-8000-000000000009";
    const proposalId = "10000000-0000-4000-8000-000000000001";
    const workflowId = "10000000-0000-4000-8000-000000000008";
    const responses = [
      { id: proposalId, workspace_id: workspaceA, target_path: "docs/a.md", status: "ready_for_review", risk_level: "LOW", revision: { id: "10000000-0000-4000-8000-000000000003", revision_no: 1, base_hash: "a".repeat(64), content: "next", evidence_summary: "evidence", risk: "low", rollback_plan: "revert", change_hash: "b".repeat(64), created_at: "2026-07-22T00:00:00Z" }, created_at: "2026-07-22T00:00:00Z", updated_at: "2026-07-22T00:01:00Z" },
      { proposal_id: proposalId, workspace_id: workspaceA, target_path: "docs/a.md", content: "current", current_hash: "a".repeat(64), base_hash: "a".repeat(64), base_hash_match: true },
      { id: workflowId, workspace_id: workspaceA, definition_id: "10000000-0000-4000-8000-000000000003", status: "running", input: {}, version: 1, created_at: "2026-07-22T00:00:00Z", updated_at: "2026-07-22T00:01:00Z", pause_requested: false, cancel_requested: false },
    ];
    vi.stubGlobal("fetch", vi.fn().mockImplementation(() => Promise.resolve(new Response(JSON.stringify(responses.shift()), { status: 200, headers: { "Content-Type": "application/json" } }))));

    await expect(getProposal(workspaceB, proposalId)).rejects.toMatchObject({ code: "INVALID_RESPONSE" });
    await expect(getProposalCurrentContent(workspaceB, proposalId, {
      targetPath: "docs/a.md",
      baseHash: "a".repeat(64),
    })).rejects.toMatchObject({ code: "INVALID_RESPONSE" });
    await expect(getWorkflow(workspaceB, workflowId)).rejects.toMatchObject({ code: "INVALID_RESPONSE" });
  });

  it("rejects a Workflow response missing the required input field", async () => {
    const workflowId = "10000000-0000-4000-8000-000000000008";
    const workspaceId = "10000000-0000-4000-8000-000000000002";
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({
      id: workflowId,
      workspace_id: workspaceId,
      definition_id: "10000000-0000-4000-8000-000000000003",
      status: "running",
      version: 1,
      created_at: "2026-07-22T00:00:00Z",
      updated_at: "2026-07-22T00:01:00Z",
      pause_requested: false,
      cancel_requested: false,
    }), { status: 200, headers: { "Content-Type": "application/json" } })));

    await expect(getWorkflow(workspaceId, workflowId)).rejects.toMatchObject({ code: "INVALID_RESPONSE" });
  });

  it.each([
    ["zero year", "0000-01-01T00:00:00Z"],
    ["non-leap February 29", "2026-02-29T00:00:00Z"],
    ["normalized February 30", "2024-02-30T00:00:00Z"],
    ["normalized hour 24", "2026-07-22T24:00:00Z"],
    ["timezone hour 24", "2026-07-22T00:00:00+24:00"],
    ["timezone minute 60", "2026-07-22T00:00:00+08:60"],
  ])("rejects an invalid RFC3339 %s timestamp", async (_label, createdAt) => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify(knowledgeProposalResponse({}, {
      created_at: createdAt,
    })), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    })));

    await expect(getProposal(workspaceId, proposalId)).rejects.toMatchObject({ code: "INVALID_RESPONSE" });
  });

  it.each([
    ["Proposal 时间", {}, { created_at: "invalid-date" }],
    ["Revision 序号", { revision_no: 1.5 }, {}],
    ["schema version", { schema_version: "1" }, {}],
    ["风险等级", {}, { risk_level: "MEDIUM" }],
    ["文件目标路径", {}, { target_path: "docs/should-not-exist.md" }],
    ["Target Ref fingerprint", { target_refs: [{ type: "RELATION_CANDIDATE", id: "10000000-0000-4000-8000-000000000006", fingerprint: "fp" }] }, {}],
    ["重复 Target Ref", { target_refs: [{ type: "RELATION_CANDIDATE", id: "10000000-0000-4000-8000-000000000006", fingerprint: "c".repeat(64) }, { type: "RELATION_CANDIDATE", id: "10000000-0000-4000-8000-000000000006", fingerprint: "c".repeat(64) }] }, {}],
    ["Base Version 数量", { base_versions: [] }, {}],
    ["Base Version 与端点不匹配", { base_versions: [{ node_type: "CLAIM", node_id: "10000000-0000-4000-8000-000000000007", version: 9 }, { node_type: "TOPIC", node_id: "10000000-0000-4000-8000-000000000008", version: 2 }] }, {}],
    ["Change operation", { change_set: { operation: "create", source: { type: "CLAIM", id: "10000000-0000-4000-8000-000000000007", version: 1 }, target: { type: "TOPIC", id: "10000000-0000-4000-8000-000000000008", version: 2 }, relation_type: "SUPPORTS" } }, {}],
    ["Endpoint version", { change_set: { operation: "CREATE_RELATION", source: { type: "CLAIM", id: "10000000-0000-4000-8000-000000000007", version: -1 }, target: { type: "TOPIC", id: "10000000-0000-4000-8000-000000000008", version: 2 }, relation_type: "SUPPORTS" } }, {}],
    ["Evidence 数量", { evidence_refs: [] }, {}],
    ["Change Hash", { change_hash: "bad" }, {}],
    ["Workspace 绑定", {}, { workspace_id: "10000000-0000-4000-8000-000000000009" }],
  ] as const)("rejects semantically invalid Knowledge Change: %s", async (_label, revisionOverrides, proposalOverrides) => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify(knowledgeProposalResponse(revisionOverrides, proposalOverrides)), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    })));

    await expect(getProposal(workspaceId, proposalId)).rejects.toMatchObject({ code: "INVALID_RESPONSE" });
  });

  it.each([
    ["CITES", "TOPIC", "TOPIC"],
    ["DERIVED_FROM", "TOPIC", "TOPIC"],
    ["SUPPORTS", "CLAIM", "TOPIC"],
    ["CONFLICTS_WITH", "TOPIC", "TOPIC"],
    ["BELONGS_TO", "TOPIC", "CLAIM"],
    ["COMPLEMENTS", "CLAIM", "TOPIC"],
    ["DUPLICATES", "CLAIM", "TOPIC"],
    ["PREREQUISITE_OF", "CLAIM", "TOPIC"],
    ["VERSION_OF", "CLAIM", "TOPIC"],
  ] as const)("rejects %s for incompatible %s to %s endpoints", async (relationType, sourceType, targetType) => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify(knowledgeProposalResponse(
      knowledgeEndpointOverrides(relationType, sourceType, targetType),
    )), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    })));

    await expect(getProposal(workspaceId, proposalId)).rejects.toMatchObject({ code: "INVALID_RESPONSE" });
  });

  it("rejects a self-referential Knowledge Relation", async () => {
    const endpointId = "10000000-0000-4000-8000-000000000007";
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify(knowledgeProposalResponse(
      knowledgeEndpointOverrides("DUPLICATES", "CLAIM", "CLAIM", endpointId, endpointId),
    )), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    })));

    await expect(getProposal(workspaceId, proposalId)).rejects.toMatchObject({ code: "INVALID_RESPONSE" });
  });

  it.each(["DUPLICATES", "CONFLICTS_WITH"] as const)("rejects non-canonical %s endpoint order", async (relationType) => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify(knowledgeProposalResponse(
      knowledgeEndpointOverrides(
        relationType,
        "CLAIM",
        "CLAIM",
        "10000000-0000-4000-8000-000000000009",
        "10000000-0000-4000-8000-000000000007",
      ),
    )), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    })));

    await expect(getProposal(workspaceId, proposalId)).rejects.toMatchObject({ code: "INVALID_RESPONSE" });
  });
});
