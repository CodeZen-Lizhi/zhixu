import { afterEach, describe, expect, it, vi } from "vitest";

import {
  SemanticLinkApiError,
  approveSemanticLinkProposal,
  decodeSemanticLinkCandidatePage,
  decodeSemanticLinkProposal,
  decodeSemanticLinkScan,
  decideSemanticLinkCandidate,
  getSemanticLinkScan,
  listSemanticLinkCandidates,
  startSemanticLinkScan,
} from "./semantic-links";

const workspaceId = "92000000-0000-4000-8000-000000000001";
const claimId = "92000000-0000-4000-8000-000000000002";
const topicId = "92000000-0000-4000-8000-000000000003";
const otherClaimId = "92000000-0000-4000-8000-000000000013";
const candidateId = "92000000-0000-4000-8000-000000000004";
const sourceVersionId = "92000000-0000-4000-8000-000000000005";
const sourceSpanId = "92000000-0000-4000-8000-000000000006";
const indexVersionId = "92000000-0000-4000-8000-000000000007";
const embeddingVersionId = "92000000-0000-4000-8000-000000000008";
const modelRunId = "92000000-0000-4000-8000-000000000009";
const rerankVersionId = "92000000-0000-4000-8000-000000000012";
const proposalId = "92000000-0000-4000-8000-00000000000a";
const revisionId = "92000000-0000-4000-8000-00000000000b";
const approvalId = "92000000-0000-4000-8000-00000000000c";
const candidateEvidenceId = "92000000-0000-4000-8000-00000000000d";
const targetCandidateId = "92000000-0000-4000-8000-00000000000e";
const scanId = "92000000-0000-4000-8000-00000000000f";
const workflowRunId = "92000000-0000-4000-8000-000000000010";
const decisionId = "92000000-0000-4000-8000-000000000011";
const collectionId = "92000000-0000-4000-8000-000000000012";
const scanStatusUrl = `/api/v1/graph/candidate-scans/${scanId}?workspace_id=${workspaceId}`;
const at = "2026-07-20T08:10:12.123456789Z";
const later = "2026-07-20T09:10:12.123456789Z";
const fingerprint = "a".repeat(64);
const semanticHash = "b".repeat(64);
const changeHash = "c".repeat(64);
const baseHash = "d".repeat(64);
const gitHead = "e".repeat(40);
const sourceVersionHref = `/api/v1/workspaces/${workspaceId}/source-versions/${sourceVersionId}`;
const sourceSpanHref = `${sourceVersionHref}/spans/${sourceSpanId}`;

const candidatePayload = {
  id: candidateId,
  workspace_id: workspaceId,
  fingerprint,
  status: "ACTIVE",
  version: 3,
  source: {
    type: "CLAIM",
    id: claimId,
    version: 5,
    summary: "Claim summary",
    excerpt: "Claim context excerpt",
  },
  target: {
    type: "CLAIM",
    id: otherClaimId,
    version: 2,
    summary: "Topic summary",
    excerpt: "Topic context excerpt",
  },
  proposed_relation_type: "COMPLEMENTS",
  confidence: 0.92,
  reason: "The claim and topic reinforce one another.",
  discovery_methods: ["TERM_MATCH", "COMMON_TOPIC"],
  evidence: [{
    id: candidateEvidenceId,
    semantic_hash: semanticHash,
    source_version_id: sourceVersionId,
    source_span_id: sourceSpanId,
    source_version_href: sourceVersionHref,
    source_span_href: sourceSpanHref,
    excerpt: "shared terminology",
    reason: "The source span uses the same bounded term.",
  }],
  generation: {
    index_version_id: indexVersionId,
    embedding_version_id: embeddingVersionId,
    rerank_version_id: rerankVersionId,
    model_version: "gpt-5-mini@2026-07-20",
    model_profile_version: "semantic-links-v1",
    prompt_version: "prompt-v1",
    schema_version: "relation-analyzer/v1",
    model_run_id: modelRunId,
  },
  reopened_reason: null,
  reopened_from_candidate_id: null,
  proposal_id: null,
  deferred_until: null,
  created_at: at,
  updated_at: later,
};

const jsonResponse = (payload: unknown, status = 200): Response => new Response(JSON.stringify(payload), {
  status,
  headers: { "Content-Type": "application/json" },
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("Semantic Link decoders", () => {
  it("严格解码 Candidate page、evidence href 和 generation 版本", () => {
    const result = decodeSemanticLinkCandidatePage({
      workspace_id: workspaceId,
      items: [candidatePayload],
      next_cursor: "cursor-v1.opaque",
    }, { workspaceId });
    expect(result.nextCursor).toBe("cursor-v1.opaque");
    expect(result.items[0]).toMatchObject({
      id: candidateId,
      proposedRelationType: "COMPLEMENTS",
      discoveryMethods: ["TERM_MATCH", "COMMON_TOPIC"],
      source: { type: "CLAIM", version: 5, excerpt: "Claim context excerpt" },
      target: { type: "CLAIM", version: 2, excerpt: "Topic context excerpt" },
      evidence: [{ sourceVersionHref, sourceSpanHref }],
      generation: {
        indexVersionId,
        embeddingVersionId,
        rerankVersionId,
        modelRunId,
      },
    });
  });

  it("Topic scope 接受由正式 membership 约束的 Claim pair", () => {
    const result = decodeSemanticLinkCandidatePage({
      workspace_id: workspaceId,
      items: [candidatePayload],
    }, { workspaceId, nodeType: "TOPIC", nodeId: topicId });

    expect(result.items).toHaveLength(1);
    expect(result.items[0]).toMatchObject({
      source: { type: "CLAIM", id: claimId },
      target: { type: "CLAIM", id: otherClaimId },
    });
  });

  it("解码 Go domain 的 rule generation 和 index-only generation", () => {
    const rule = decodeSemanticLinkCandidatePage({
      workspace_id: workspaceId,
      items: [{
        ...candidatePayload,
        generation: {
          index_version_id: indexVersionId,
          embedding_version_id: null,
          rerank_version_id: null,
          rule_id: candidateId,
          rule_version: "semantic-rule-v1",
        },
      }],
    }, { workspaceId });
    expect(rule.items[0]?.generation).toMatchObject({
      indexVersionId,
      ruleId: candidateId,
      ruleVersion: "semantic-rule-v1",
      modelVersion: null,
    });

    const indexOnly = decodeSemanticLinkCandidatePage({
      workspace_id: workspaceId,
      items: [{
        ...candidatePayload,
        generation: { index_version_id: indexVersionId, embedding_version_id: null, rerank_version_id: null },
      }],
    }, { workspaceId });
    expect(indexOnly.items[0]?.generation).toEqual({
      indexVersionId,
      embeddingVersionId: null,
      rerankVersionId: null,
      modelVersion: null,
      modelProfileVersion: null,
      promptVersion: null,
      schemaVersion: null,
      ruleId: null,
      ruleVersion: null,
      modelRunId: null,
    });
  });

  it.each([
    ["顶层未知字段", { workspace_id: workspaceId, items: [candidatePayload], unexpected: true }],
    ["未知 discovery method", { workspace_id: workspaceId, items: [{ ...candidatePayload, discovery_methods: ["TERM", "GRAPH_MAGIC"] }] }],
    ["非法 reopened reason", { workspace_id: workspaceId, items: [{ ...candidatePayload, reopened_reason: "MODEL_CHANGED" }] }],
    ["旧 generation 假字段", { workspace_id: workspaceId, items: [{ ...candidatePayload, generation: { ...candidatePayload.generation, kind: "model", model_adapter: "openai" } }] }],
    ["混合 rule/model generation", {
      workspace_id: workspaceId,
      items: [{ ...candidatePayload, generation: { ...candidatePayload.generation, rule_id: candidateId, rule_version: "rule-v1" } }],
    }],
    ["缺失 endpoint excerpt", {
      workspace_id: workspaceId,
      items: [{ ...candidatePayload, source: { type: "CLAIM", id: claimId, version: 5, summary: "Claim summary" } }],
    }],
    ["违反请求过滤器", { workspace_id: workspaceId, items: [candidatePayload] }],
    ["错误 source href", {
      workspace_id: workspaceId,
      items: [{
        ...candidatePayload,
        evidence: [{ ...candidatePayload.evidence[0], source_version_href: `${sourceVersionHref}/wrong` }],
      }],
    }],
  ])("拒绝%s", (_name, payload) => {
    const request = _name === "违反请求过滤器" ? { workspaceId, statuses: ["DEFERRED" as const] } : { workspaceId };
    expect(() => decodeSemanticLinkCandidatePage(payload, request)).toThrow(SemanticLinkApiError);
  });

  it.each([
    ["同类型但不同 ID 的请求范围", candidatePayload, { workspaceId, nodeType: "CLAIM" as const, nodeId: targetCandidateId }],
    ["同 ID 但不同类型的请求范围", {
      ...candidatePayload,
      target: { ...candidatePayload.target, type: "TOPIC", id: targetCandidateId },
      proposed_relation_type: "BELONGS_TO",
    }, { workspaceId, nodeType: "TOPIC" as const, nodeId: claimId }],
    ["自环端点", { ...candidatePayload, target: { ...candidatePayload.source } }, { workspaceId }],
    ["不兼容关系类型", { ...candidatePayload, proposed_relation_type: "BELONGS_TO" }, { workspaceId }],
    ["未规范化的对称端点", {
      ...candidatePayload,
      proposed_relation_type: "DUPLICATES",
      source: candidatePayload.target,
      target: candidatePayload.source,
    }, { workspaceId }],
  ])("拒绝%s", (_name, candidate, request) => {
    expect(() => decodeSemanticLinkCandidatePage({ workspace_id: workspaceId, items: [candidate] }, request)).toThrow(SemanticLinkApiError);
  });

  it("兼容 legacy file_patch Proposal 并解码 typed knowledge_change Proposal", () => {
    const legacy = decodeSemanticLinkProposal({
      id: proposalId,
      workspace_id: workspaceId,
      target_path: "notes/graph.md",
      status: "ready_for_review",
      revision: {
        id: revisionId,
        revision_no: 1,
        base_hash: baseHash,
        content: "patched",
        evidence_summary: "reason",
        risk: "low",
        rollback_plan: "git revert",
        change_hash: changeHash,
        created_at: at,
      },
      approval: null,
      created_at: at,
      updated_at: later,
    }, { proposalId });
    expect(legacy).toMatchObject({
      proposalType: "file_patch",
      targetPath: "notes/graph.md",
      revision: { baseHash, changeHash },
    });

    const typed = decodeSemanticLinkProposal({
      proposal_type: "knowledge_change",
      id: proposalId,
      workspace_id: workspaceId,
      status: "approved",
      revision: {
        id: revisionId,
        revision_no: 2,
        schema_version: "knowledge-relation-change/v1",
        target_refs: [{ type: "RELATION_CANDIDATE", id: targetCandidateId, fingerprint }],
        base_versions: [
          { node_type: "CLAIM", node_id: claimId, version: 5 },
          { node_type: "CLAIM", node_id: otherClaimId, version: 2 },
        ],
        change_set: {
          operation: "CREATE_RELATION",
          source: { type: "CLAIM", id: claimId, version: 5 },
          target: { type: "CLAIM", id: otherClaimId, version: 2 },
          relation_type: "COMPLEMENTS",
        },
        evidence_refs: [{ candidate_evidence_id: candidateEvidenceId, semantic_hash: semanticHash }],
        risk: "low",
        rollback_plan: "create compensating proposal",
        change_hash: changeHash,
        created_at: at,
      },
      approval: {
        id: approvalId,
        proposal_id: proposalId,
        revision_id: revisionId,
        change_hash: changeHash,
        decision: "approved",
        approved_git_head: gitHead,
        workflow_run_id: workflowRunId,
        workflow_status_url: `/api/v1/workflows/${workflowRunId}`,
        dispatch_status: "queued",
        decided_at: later,
      },
      created_at: at,
      updated_at: later,
    }, { proposalId });
    expect(typed).toMatchObject({
      proposalType: "knowledge_change",
      revision: {
        schemaVersion: "knowledge-relation-change/v1",
        changeSet: { relationType: "COMPLEMENTS" },
      },
      approval: {
        workflowRunId,
        dispatchStatus: "queued",
      },
    });
  });
});

describe("Semantic Link clients", () => {
  it("序列化 Candidate list 查询参数并解码结果", async () => {
    const fetchMock = vi.fn<typeof fetch>().mockResolvedValue(jsonResponse({
      workspace_id: workspaceId,
      items: [{ ...candidatePayload, reopened_reason: "CONTENT_CHANGED", reopened_from_candidate_id: targetCandidateId }],
      next_cursor: "cursor-v1.opaque",
    }));
    vi.stubGlobal("fetch", fetchMock);

    const result = await listSemanticLinkCandidates({
      workspaceId,
      nodeType: "CLAIM",
      nodeId: claimId,
      statuses: ["ACTIVE", "DEFERRED"],
      relationTypes: ["COMPLEMENTS"],
      reopenedReasons: ["CONTENT_CHANGED"],
      minConfidence: 0.65,
      cursor: "opaque+cursor",
      limit: 20,
    });

    expect(result.items).toHaveLength(1);
    const requestUrl = fetchMock.mock.calls[0]?.[0];
    if (typeof requestUrl !== "string") throw new Error("missing request url");
    expect(requestUrl).toContain(`/api/v1/graph/candidates?workspace_id=${workspaceId}`);
    expect(requestUrl).toContain("node_type=CLAIM");
    expect(requestUrl).toContain(`node_id=${claimId}`);
    expect(requestUrl).toContain("status=ACTIVE");
    expect(requestUrl).toContain("status=DEFERRED");
    expect(requestUrl).toContain("relation_type=COMPLEMENTS");
    expect(requestUrl).toContain("reopened_reason=CONTENT_CHANGED");
    expect(requestUrl).toContain("min_confidence=0.65");
    expect(requestUrl).toContain("cursor=opaque%2Bcursor");
    expect(requestUrl).toContain("limit=20");
  });

  it("拒绝越界的 minConfidence 请求参数", async () => {
    const fetchMock = vi.fn<typeof fetch>();
    vi.stubGlobal("fetch", fetchMock);
    await expect(listSemanticLinkCandidates({ workspaceId, minConfidence: 1.1 })).rejects.toMatchObject({ errorCode: "INVALID_REQUEST" });
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("启动 scan 时发送 Idempotency-Key 和严格 snake_case scope", async () => {
    const fetchMock = vi.fn<typeof fetch>().mockResolvedValue(jsonResponse({
      scan_id: scanId,
      workflow_run_id: workflowRunId,
      status: "PENDING",
      version: 1,
      status_url: scanStatusUrl,
    }, 202));
    vi.stubGlobal("fetch", fetchMock);

    const result = await startSemanticLinkScan({
      workspaceId,
      idempotencyKey: "scan-1",
      scope: { kind: "TOPIC", topicId },
    });

    expect(result.scanId).toBe(scanId);
    const [, init] = fetchMock.mock.calls[0] ?? [];
    expect(init?.headers).toMatchObject({ "Idempotency-Key": "scan-1" });
    if (typeof init?.body !== "string") throw new Error("missing body");
    expect(JSON.parse(init.body)).toEqual({
      workspace_id: workspaceId,
      scope: { kind: "TOPIC", topic_id: topicId },
    });
  });

  it("启动 SMART Collection scan 时只提交 collection_id", async () => {
    const fetchMock = vi.fn<typeof fetch>().mockResolvedValue(jsonResponse({
      scan_id: scanId,
      workflow_run_id: workflowRunId,
      status: "PENDING",
      version: 1,
      status_url: scanStatusUrl,
    }, 202));
    vi.stubGlobal("fetch", fetchMock);

    await expect(startSemanticLinkScan({
      workspaceId,
      idempotencyKey: "scan-smart",
      scope: { kind: "SMART_COLLECTION", collectionId },
    })).resolves.toMatchObject({ scanId });

    const [, init] = fetchMock.mock.calls[0] ?? [];
    if (typeof init?.body !== "string") throw new Error("missing body");
    expect(JSON.parse(init.body)).toEqual({
      workspace_id: workspaceId,
      scope: { kind: "SMART_COLLECTION", collection_id: collectionId },
    });
  });

  it("typed confirm 绑定 expected_version 和 relation_type 且不携带无关字段", async () => {
    const fetchMock = vi.fn<typeof fetch>().mockResolvedValue(jsonResponse({
      id: decisionId,
      candidate_id: candidateId,
      workspace_id: workspaceId,
      action: "CONFIRM_WITH_RELATION_TYPE",
      status: "PROPOSAL_CREATED",
      version: 4,
      proposal_id: proposalId,
      created_at: at,
      updated_at: later,
    }, 201));
    vi.stubGlobal("fetch", fetchMock);

    const result = await decideSemanticLinkCandidate({
      candidateId,
      workspaceId,
      action: "CONFIRM_WITH_RELATION_TYPE",
      expectedVersion: 3,
      idempotencyKey: "decision-1",
      relationType: "COMPLEMENTS",
    });

    expect(result.proposalId).toBe(proposalId);
    const [, init] = fetchMock.mock.calls[0] ?? [];
    expect(init?.headers).toMatchObject({ "Idempotency-Key": "decision-1" });
    if (typeof init?.body !== "string") throw new Error("missing body");
    expect(JSON.parse(init.body)).toEqual({
      workspace_id: workspaceId,
      action: "CONFIRM_WITH_RELATION_TYPE",
      expected_version: 3,
      relation_type: "COMPLEMENTS",
    });
  });

  it("拒绝与请求 Candidate/action/version 不绑定的 decision receipt", async () => {
    const fetchMock = vi.fn<typeof fetch>().mockResolvedValue(jsonResponse({
      id: decisionId,
      candidate_id: targetCandidateId,
      workspace_id: workspaceId,
      action: "CONFIRM_WITH_RELATION_TYPE",
      status: "PROPOSAL_CREATED",
      version: 4,
      proposal_id: proposalId,
      created_at: at,
      updated_at: later,
    }, 201));
    vi.stubGlobal("fetch", fetchMock);

    await expect(decideSemanticLinkCandidate({
      candidateId,
      workspaceId,
      action: "CONFIRM_WITH_RELATION_TYPE",
      expectedVersion: 3,
      idempotencyKey: "decision-binding",
      relationType: "COMPLEMENTS",
    })).rejects.toMatchObject({ errorCode: "INVALID_RESPONSE" });
  });

  it("在发出请求前拒绝超过领域 4 KiB 上限的 decision reason", async () => {
    const fetchMock = vi.fn<typeof fetch>();
    vi.stubGlobal("fetch", fetchMock);

    await expect(decideSemanticLinkCandidate({
      candidateId,
      workspaceId,
      action: "IGNORE",
      expectedVersion: 3,
      idempotencyKey: "decision-reason-limit",
      reason: "x".repeat(4097),
    })).rejects.toMatchObject({ errorCode: "INVALID_REQUEST" });
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("按 action 严格序列化 decision 可选字段", async () => {
    const fetchMock = vi.fn<typeof fetch>((_input, init) => {
      if (typeof init?.body !== "string") throw new Error("missing body");
      const requestBody = JSON.parse(init.body) as Record<string, unknown>;
      const action = requestBody.action;
      return Promise.resolve(jsonResponse({
        id: decisionId,
        candidate_id: candidateId,
        workspace_id: workspaceId,
        action,
        status: action === "CONFIRM" || action === "CONFIRM_WITH_RELATION_TYPE" ? "PROPOSAL_CREATED"
          : action === "IGNORE" ? "IGNORED" : action === "DEFER" ? "DEFERRED" : "ACTIVE",
        version: Number(requestBody.expected_version) + 1,
        proposal_id: action === "CONFIRM" || action === "CONFIRM_WITH_RELATION_TYPE" ? proposalId : null,
        created_at: at,
        updated_at: later,
      }, 201));
    });
    vi.stubGlobal("fetch", fetchMock);

    await decideSemanticLinkCandidate({ candidateId, workspaceId, action: "CONFIRM", expectedVersion: 1, idempotencyKey: "confirm" });
    await decideSemanticLinkCandidate({ candidateId, workspaceId, action: "IGNORE", expectedVersion: 1, idempotencyKey: "ignore", reason: "not related" });
    await decideSemanticLinkCandidate({ candidateId, workspaceId, action: "DEFER", expectedVersion: 1, idempotencyKey: "defer", reason: "review later", deferredUntil: null });
    await decideSemanticLinkCandidate({ candidateId, workspaceId, action: "RESUME", expectedVersion: 1, idempotencyKey: "resume", deferredUntil: null });

    const bodies = fetchMock.mock.calls.map(([, init]) => {
      if (typeof init?.body !== "string") throw new Error("missing body");
      return JSON.parse(init.body) as Record<string, unknown>;
    });
    expect(bodies[0]).not.toHaveProperty("reason");
    expect(bodies[0]).not.toHaveProperty("relation_type");
    expect(bodies[1]).toMatchObject({ action: "IGNORE", reason: "not related" });
    expect(bodies[1]).not.toHaveProperty("deferred_until");
    expect(bodies[2]).toMatchObject({ action: "DEFER", reason: "review later", deferred_until: null });
    expect(bodies[3]).toMatchObject({ action: "RESUME", deferred_until: null });
  });

  it("读取 scan 查询并解码计数/状态 URL", async () => {
    const fetchMock = vi.fn<typeof fetch>().mockResolvedValue(jsonResponse({
      id: scanId,
      workspace_id: workspaceId,
      scope: { kind: "TOPIC", topic_id: topicId },
      status: "RUNNING",
      workflow_run_id: workflowRunId,
      version: 2,
      status_url: scanStatusUrl,
      total_count: 10,
      processed_count: 4,
      candidate_count: 2,
      ignored_count: 1,
      failed_count: 0,
      last_error: null,
      created_at: at,
      updated_at: later,
      completed_at: null,
    }));
    vi.stubGlobal("fetch", fetchMock);

    const result = await getSemanticLinkScan({ scanId, workspaceId });
    expect(result.scope).toMatchObject({ kind: "TOPIC", topicId });
    expect(fetchMock.mock.calls[0]?.[0]).toContain(`/api/v1/graph/candidate-scans/${scanId}?workspace_id=${workspaceId}`);
  });

  it("解码 SMART Collection scan 的服务端冻结 binding", () => {
    const result = decodeSemanticLinkScan({
      id: scanId,
      workspace_id: workspaceId,
      scope: { kind: "SMART_COLLECTION", collection_id: collectionId, collection_version: 3, query_hash: fingerprint, read_model_revision: semanticHash },
      status: "RUNNING",
      workflow_run_id: workflowRunId,
      version: 2,
      status_url: scanStatusUrl,
      total_count: 10,
      processed_count: 4,
      candidate_count: 2,
      ignored_count: 1,
      failed_count: 0,
      last_error: null,
      created_at: at,
      updated_at: later,
      completed_at: null,
    }, { scanId, workspaceId });
    expect(result.scope).toMatchObject({ kind: "SMART_COLLECTION", collectionId, collectionVersion: 3, queryHash: fingerprint, readModelRevision: semanticHash });
    expect(() => decodeSemanticLinkScan({
      id: scanId,
      workspace_id: workspaceId,
      scope: { kind: "SMART_COLLECTION", collection_id: collectionId },
      status: "RUNNING",
      workflow_run_id: workflowRunId,
      version: 2,
      status_url: scanStatusUrl,
      total_count: 10,
      processed_count: 4,
      candidate_count: 2,
      ignored_count: 1,
      failed_count: 0,
      last_error: null,
      created_at: at,
      updated_at: later,
      completed_at: null,
    }, { scanId, workspaceId })).toThrow(SemanticLinkApiError);
  });

  it("严格解码 FAILED scan 的持久错误并拒绝非法状态组合", async () => {
    const failedPayload = {
      id: scanId,
      workspace_id: workspaceId,
      scope: { kind: "TOPIC", topic_id: topicId },
      status: "FAILED",
      workflow_run_id: workflowRunId,
      version: 4,
      status_url: scanStatusUrl,
      total_count: 10,
      processed_count: 4,
      candidate_count: 2,
      ignored_count: 1,
      failed_count: 1,
      last_error: { stage: "discover", code: "SEMANTIC_PROVIDER_TIMEOUT", retryable: true },
      created_at: at,
      updated_at: later,
      completed_at: later,
    };
    const fetchMock = vi.fn<typeof fetch>().mockResolvedValue(jsonResponse(failedPayload));
    vi.stubGlobal("fetch", fetchMock);

    await expect(getSemanticLinkScan({ scanId, workspaceId })).resolves.toMatchObject({
      status: "FAILED",
      lastError: { stage: "discover", code: "SEMANTIC_PROVIDER_TIMEOUT", retryable: true },
    });
    expect(() => decodeSemanticLinkScan({ ...failedPayload, status: "CANCELLED" }, { scanId, workspaceId })).toThrow(SemanticLinkApiError);
    expect(() => decodeSemanticLinkScan({ ...failedPayload, last_error: null }, { scanId, workspaceId })).toThrow(SemanticLinkApiError);
  });

  it("批准 Proposal 复用现有审批契约并映射服务端 Problem", async () => {
    const fetchMock = vi.fn<typeof fetch>()
      .mockResolvedValueOnce(jsonResponse({
        id: approvalId,
        proposal_id: proposalId,
        revision_id: revisionId,
        change_hash: changeHash,
        decision: "approved",
        approved_git_head: gitHead,
        workflow_run_id: workflowRunId,
        workflow_status_url: `/api/v1/workflows/${workflowRunId}`,
        dispatch_status: "replayed",
        decided_at: later,
      }, 200))
      .mockResolvedValueOnce(jsonResponse({
        error_code: "RELATION_PROPOSAL_BASE_STALE",
        message: "Candidate base changed",
        retryable: false,
        details: { resolution_actions: ["create_new_proposal_revision"] },
      }, 409));
    vi.stubGlobal("fetch", fetchMock);

    const approval = await approveSemanticLinkProposal({
      proposalId,
      revisionId,
      changeHash,
      decision: "approved",
    });
    expect(approval).toMatchObject({ workflowRunId, dispatchStatus: "replayed" });
    const [, init] = fetchMock.mock.calls[0] ?? [];
    if (typeof init?.body !== "string") throw new Error("missing body");
    expect(JSON.parse(init.body)).toEqual({
      revision_id: revisionId,
      change_hash: changeHash,
      decision: "approved",
    });

    await expect(approveSemanticLinkProposal({
      proposalId,
      revisionId,
      changeHash,
      decision: "approved",
    })).rejects.toMatchObject({
      name: "SemanticLinkApiError",
      errorCode: "RELATION_PROPOSAL_BASE_STALE",
      status: 409,
      details: { resolution_actions: ["create_new_proposal_revision"] },
    });
  });
});
