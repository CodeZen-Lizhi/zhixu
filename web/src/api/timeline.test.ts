import { afterEach, describe, expect, it, vi } from "vitest";

const authFetchMock = vi.hoisted(() => vi.fn<typeof fetch>());

vi.mock("./auth", () => ({ authFetch: authFetchMock }));

import {
  TimelineApiError,
  analyzeImpact,
  createDownstreamUpdateProposal,
  decodeDownstreamUpdateProposal,
  decodeImpactReport,
  decodeTimelineEvent,
  decodeTimelinePage,
  listTimeline,
} from "./timeline";

const workspaceId = "73000000-0000-4000-8000-000000000001";
const otherWorkspaceId = "73000000-0000-4000-8000-000000000002";
const eventId = "73000000-0000-4000-8000-000000000003";
const aggregateId = "73000000-0000-4000-8000-000000000004";
const artifactId = "73000000-0000-4000-8000-000000000005";
const revisionId = "73000000-0000-4000-8000-000000000006";
const relationId = "73000000-0000-4000-8000-000000000007";
const reportId = "73000000-0000-4000-8000-000000000008";
const previousReportId = "73000000-0000-4000-8000-000000000009";
const proposalId = "73000000-0000-4000-8000-000000000010";
const proposalRevisionId = "73000000-0000-4000-8000-000000000011";
const cardId = "73000000-0000-4000-8000-000000000012";
const claimId = "73000000-0000-4000-8000-000000000013";
const approvalId = "73000000-0000-4000-8000-000000000014";
const laterEventId = "73000000-0000-4000-8000-000000000015";
const occurredAt = "2026-07-29T01:02:03Z";
const createdAt = "2026-07-29T01:02:04Z";
const artifactHash = "a".repeat(64);
const reportHash = "b".repeat(64);
const changeHash = "c".repeat(64);

const v1Event = () => ({
  id: eventId,
  workspace_id: workspaceId,
  event_type: "CONFLICT_OPENED",
  aggregate_type: "CONFLICT",
  aggregate_id: aggregateId,
  source_event_ref: "conflict.opened:7300",
  source_ref: "conflict:7300",
  event_version: 1,
  schema_version: "knowledge-event/v1",
  summary: "Conflict opened",
  payload: {},
  correlation: {},
  occurred_at: occurredAt,
  created_at: createdAt,
});

const v2NonOwnerEvent = () => ({
  ...v1Event(),
  schema_version: "knowledge-event/v2",
  operator: { type: "SYSTEM" },
  owner_binding: null,
});

const artifactBinding = () => ({
  artifact_id: artifactId,
  artifact_version: 3,
  revision_id: revisionId,
  revision_no: 2,
  content_hash: artifactHash,
});

const v2ArtifactEvent = () => ({
  ...v2NonOwnerEvent(),
  event_type: "ARTIFACT_GENERATED",
  aggregate_type: "ARTIFACT",
  aggregate_id: artifactId,
  owner_binding: { artifact: artifactBinding() },
});

const reviewCardBinding = () => ({
  card_id: cardId,
  card_version: 4,
  status: "INVALIDATED",
  fingerprint: "d".repeat(64),
  claim_id: claimId,
  evidence_binding_fingerprint: "e".repeat(64),
});

const v2ReviewCardEvent = () => ({
  ...v2NonOwnerEvent(),
  event_type: "REVIEW_CARD_INVALIDATED",
  aggregate_type: "REVIEW_CARD",
  aggregate_id: cardId,
  owner_binding: { review_card: reviewCardBinding() },
});

const legacyObject = () => ({
  type: "RELATION",
  id: relationId,
  workspace_id: workspaceId,
  version: 1,
  action: "REVIEW",
  reason: "Inspect relation",
  requires_proposal: false,
});

const artifactObject = () => ({
  type: "ARTIFACT",
  id: artifactId,
  workspace_id: workspaceId,
  version: 3,
  action: "REGENERATE_ARTIFACT",
  reason: "Regenerate from current evidence",
  requires_proposal: true,
  artifact_binding: artifactBinding(),
});

const v1Report = () => ({
  id: reportId,
  workspace_id: workspaceId,
  source_event_id: eventId,
  source_event_ref: "conflict.opened:7300",
  source_event_version: 1,
  status: "READY",
  objects: [legacyObject()],
  summary: { RELATION: 1, "action:REVIEW": 1 },
  fingerprint: reportHash,
  schema_version: "impact-report/v1",
  generated_at: occurredAt,
  created_at: createdAt,
  version: 1,
});

const v2Report = () => ({
  ...v1Report(),
  objects: [artifactObject()],
  summary: { ARTIFACT: 1, "action:REGENERATE_ARTIFACT": 1, requires_proposal: 1 },
  schema_version: "impact-report/v2",
  analysis_version: "impact-analysis/v2",
  supersedes_report_id: previousReportId,
  superseded_by_report_id: null,
  version: 2,
});

const proposalWire = (replayed: boolean) => ({
  proposal_type: "downstream_update",
  id: proposalId,
  workspace_id: workspaceId,
  status: "ready_for_review",
  risk_level: "HIGH",
  revision: {
    id: proposalRevisionId,
    revision_no: 1,
    update: {
      workspace_id: workspaceId,
      source_report: { id: reportId, analysis_version: "impact-analysis/v2", fingerprint: reportHash },
      source_event: { id: eventId, event_version: 1 },
      target_type: "ARTIFACT",
      target_id: artifactId,
      base_version: 3,
      action: "REGENERATE_ARTIFACT",
      artifact_binding: artifactBinding(),
      reason: "Regenerate from current evidence",
      schema_version: "impact-downstream-update/v1",
    },
    risk: "Owner update requires review",
    rollback_plan: "No target write has occurred",
    change_hash: changeHash,
    created_at: createdAt,
  },
  approval: null,
  replayed,
  created_at: createdAt,
  updated_at: createdAt,
});

const approvedProposalWire = () => ({
  ...proposalWire(true),
  status: "approved",
  approval: {
    id: approvalId,
    proposal_id: proposalId,
    revision_id: proposalRevisionId,
    change_hash: changeHash,
    decision: "approved",
    decided_at: "2026-07-29T01:02:05Z",
  },
  updated_at: "2026-07-29T01:02:05Z",
});

const jsonResponse = (value: unknown, status = 200): Response => new Response(JSON.stringify(value), {
  status,
  headers: { "Content-Type": "application/json" },
});

afterEach(() => {
  authFetchMock.mockReset();
});

describe("Timeline API strict boundary", () => {
  it("strictly separates v1 from v2 and requires nullable owner_binding on v2", () => {
    expect(decodeTimelineEvent(v1Event())).toMatchObject({ schemaVersion: "knowledge-event/v1" });
    expect(decodeTimelineEvent(v2NonOwnerEvent())).toMatchObject({ schemaVersion: "knowledge-event/v2", ownerBinding: null });
    expect(decodeTimelineEvent(v2ArtifactEvent())).toMatchObject({
      eventType: "ARTIFACT_GENERATED",
      ownerBinding: { artifact: { artifactId, revisionId } },
    });
    expect(decodeTimelineEvent(v2ReviewCardEvent())).toMatchObject({
      eventType: "REVIEW_CARD_INVALIDATED",
      ownerBinding: { reviewCard: { cardId, claimId, status: "INVALIDATED" } },
    });

    const missingOwnerBinding: Record<string, unknown> = { ...v2NonOwnerEvent() };
    delete missingOwnerBinding.owner_binding;
    expect(() => decodeTimelineEvent(missingOwnerBinding)).toThrow(TimelineApiError);
    expect(() => decodeTimelineEvent({ ...v2ArtifactEvent(), owner_binding: null })).toThrow(TimelineApiError);
    expect(() => decodeTimelineEvent({ ...v2ArtifactEvent(), owner_binding: { review_card: reviewCardBinding() } })).toThrow(TimelineApiError);
    expect(() => decodeTimelineEvent({ ...v2ReviewCardEvent(), owner_binding: { review_card: { ...reviewCardBinding(), status: "APPROVED" } } })).toThrow(TimelineApiError);
    expect(() => decodeTimelineEvent({ ...v2NonOwnerEvent(), owner_binding: { artifact: artifactBinding() } })).toThrow(TimelineApiError);
    expect(() => decodeTimelineEvent({ ...v1Event(), operator: { type: "SYSTEM" } })).toThrow(TimelineApiError);
  });

  it("strictly decodes immutable impact-report v1/v2 bindings", () => {
    expect(decodeImpactReport(v1Report())).toMatchObject({ schemaVersion: "impact-report/v1", analysisVersion: "impact-analysis/v1" });
    expect(decodeImpactReport(v2Report())).toMatchObject({
      schemaVersion: "impact-report/v2",
      analysisVersion: "impact-analysis/v2",
      supersedesReportId: previousReportId,
      supersededByReportId: null,
      objects: [{ type: "ARTIFACT", artifactBinding: { artifactId, revisionId } }],
    });

    expect(() => decodeImpactReport({ ...v1Report(), analysis_version: "impact-analysis/v1" })).toThrow(TimelineApiError);
    expect(() => decodeImpactReport({ ...v2Report(), supersedes_report_id: reportId })).toThrow(TimelineApiError);
    expect(() => decodeImpactReport({ ...v2Report(), objects: [{ ...artifactObject(), workspace_id: otherWorkspaceId }] })).toThrow(TimelineApiError);
    expect(() => decodeImpactReport({ ...v2Report(), unexpected: true })).toThrow(TimelineApiError);
    expect(() => decodeImpactReport({ ...v2Report(), status: "FAILED", error_code: "" })).toThrow(TimelineApiError);
    expect(decodeImpactReport({
      ...v2Report(),
      objects: [artifactObject(), legacyObject()],
      summary: { ARTIFACT: 1, RELATION: 1, "action:REGENERATE_ARTIFACT": 1, "action:REVIEW": 1, requires_proposal: 1 },
    })).toMatchObject({ objects: [{ type: "ARTIFACT" }, { type: "RELATION" }] });
    expect(() => decodeImpactReport({
      ...v2Report(),
      objects: [legacyObject(), artifactObject()],
      summary: { ARTIFACT: 1, RELATION: 1, "action:REGENERATE_ARTIFACT": 1, "action:REVIEW": 1, requires_proposal: 1 },
    })).toThrow(TimelineApiError);
  });

  it("requires canonical Timeline ordering by occurred_at and id", () => {
    const later = { ...v2NonOwnerEvent(), id: laterEventId };
    expect(decodeTimelinePage({ workspace_id: workspaceId, items: [later, v2NonOwnerEvent()] })).toMatchObject({
      items: [{ id: laterEventId }, { id: eventId }],
    });
    expect(() => decodeTimelinePage({ workspace_id: workspaceId, items: [v2NonOwnerEvent(), later] })).toThrow(TimelineApiError);
    expect(() => decodeTimelinePage({
      workspace_id: workspaceId,
      items: [v2NonOwnerEvent(), { ...later, occurred_at: "2026-07-29T01:02:04Z", created_at: "2026-07-29T01:02:05Z" }],
    })).toThrow(TimelineApiError);
  });

  it("binds list responses to the requested Workspace and rejects duplicate JSON keys", async () => {
    authFetchMock
      .mockResolvedValueOnce(jsonResponse({ workspace_id: workspaceId, items: [v2NonOwnerEvent()], next_cursor: "opaque-next" }))
      .mockResolvedValueOnce(jsonResponse({ workspace_id: otherWorkspaceId, items: [] }))
      .mockResolvedValueOnce(new Response(`{"workspace_id":"${workspaceId}","items":[],"it\\u0065ms":[]}`, { status: 200 }))
      .mockResolvedValueOnce(new Response(`{"workspace_id":"${workspaceId}","items":[],"__proto__":{"polluted":true}}`, { status: 200 }));

    await expect(listTimeline({ workspaceId, eventTypes: ["CONFLICT_OPENED"], limit: 25 })).resolves.toMatchObject({
      workspaceId,
      items: [{ id: eventId, ownerBinding: null }],
      nextCursor: "opaque-next",
    });
    expect(authFetchMock.mock.calls[0]?.[0]).toBe(`/api/v1/workspaces/${workspaceId}/timeline?event_type=CONFLICT_OPENED&limit=25`);
    await expect(listTimeline({ workspaceId })).rejects.toMatchObject({ code: "INVALID_RESPONSE" });
    await expect(listTimeline({ workspaceId })).rejects.toMatchObject({ code: "INVALID_RESPONSE" });
    await expect(listTimeline({ workspaceId })).rejects.toMatchObject({ code: "INVALID_RESPONSE" });
  });

  it("maps Impact 201/200 to replayed=false/true and sends the same explicit key", async () => {
    authFetchMock
      .mockResolvedValueOnce(jsonResponse({ report: v2Report(), proposal_drafts: [], replayed: false }, 201))
      .mockResolvedValueOnce(jsonResponse({ report: v2Report(), proposal_drafts: [], replayed: true }, 200));
    const input = { workspaceId, eventId, idempotencyKey: "impact-intent-1" };

    await expect(analyzeImpact(input)).resolves.toMatchObject({ replayed: false, report: { id: reportId } });
    await expect(analyzeImpact(input)).resolves.toMatchObject({ replayed: true, report: { id: reportId } });
    for (const call of authFetchMock.mock.calls) {
      expect(call[0]).toBe(`/api/v1/workspaces/${workspaceId}/timeline/${eventId}/impact-analysis`);
      expect(new Headers(call[1]?.headers).get("Idempotency-Key")).toBe("impact-intent-1");
      expect(call[1]?.body).toBe("{}");
    }
  });

  it("creates and exactly replays a downstream Proposal with the endpoint contract", async () => {
    authFetchMock
      .mockResolvedValueOnce(jsonResponse(proposalWire(false), 201))
      .mockResolvedValueOnce(jsonResponse(proposalWire(true), 200));
    const input = {
      workspaceId,
      reportId,
      targetType: "ARTIFACT" as const,
      targetId: artifactId,
      action: "REGENERATE_ARTIFACT" as const,
      idempotencyKey: "downstream-intent-1",
    };

    await expect(createDownstreamUpdateProposal(input)).resolves.toMatchObject({ id: proposalId, replayed: false });
    await expect(createDownstreamUpdateProposal(input)).resolves.toMatchObject({ id: proposalId, replayed: true });
    const init = authFetchMock.mock.calls[0]?.[1];
    expect(new Headers(init?.headers).get("Idempotency-Key")).toBe("downstream-intent-1");
    if (typeof init?.body !== "string") throw new Error("missing Proposal request body");
    expect(JSON.parse(init.body)).toEqual({ target_type: "ARTIFACT", target_id: artifactId, action: "REGENERATE_ARTIFACT" });
    expect(() => decodeDownstreamUpdateProposal({ ...proposalWire(false), status: "applying" })).toThrow(TimelineApiError);
    expect(() => decodeDownstreamUpdateProposal({
      ...proposalWire(false),
      revision: {
        ...proposalWire(false).revision,
        update: { ...proposalWire(false).revision.update, reason: "" },
      },
    })).toThrow(TimelineApiError);
  });

  it("accepts an approved replay with a strictly bound non-file Approval", async () => {
    authFetchMock.mockResolvedValueOnce(jsonResponse(approvedProposalWire(), 200));
    const input = {
      workspaceId,
      reportId,
      targetType: "ARTIFACT" as const,
      targetId: artifactId,
      action: "REGENERATE_ARTIFACT" as const,
      idempotencyKey: "downstream-approved-replay",
    };

    await expect(createDownstreamUpdateProposal(input)).resolves.toMatchObject({
      status: "approved",
      replayed: true,
      approval: { id: approvalId, proposalId, revisionId: proposalRevisionId, changeHash, decision: "approved" },
    });
    expect(() => decodeDownstreamUpdateProposal({ ...approvedProposalWire(), approval: null })).toThrow(TimelineApiError);
    expect(() => decodeDownstreamUpdateProposal({
      ...approvedProposalWire(),
      approval: { ...approvedProposalWire().approval, proposal_id: otherWorkspaceId },
    })).toThrow(TimelineApiError);
    expect(() => decodeDownstreamUpdateProposal({
      ...approvedProposalWire(),
      approval: { ...approvedProposalWire().approval, workflow_run_id: eventId },
    })).toThrow(TimelineApiError);
    expect(decodeDownstreamUpdateProposal({
      ...approvedProposalWire(),
      status: "rejected",
      approval: { ...approvedProposalWire().approval, decision: "rejected" },
    })).toMatchObject({ status: "rejected", approval: { decision: "rejected" } });
  });

  it("rejects unknown downstream target/action values before the network boundary", () => {
    const valid = {
      workspaceId,
      reportId,
      targetType: "ARTIFACT" as const,
      targetId: artifactId,
      action: "REGENERATE_ARTIFACT" as const,
      idempotencyKey: "downstream-runtime-union",
    };
    const unknownTarget = { ...valid, targetType: "RELATION" } as unknown as Parameters<typeof createDownstreamUpdateProposal>[0];
    const unknownAction = { ...valid, action: "REVIEW" } as unknown as Parameters<typeof createDownstreamUpdateProposal>[0];

    expect(() => createDownstreamUpdateProposal(unknownTarget)).toThrow(TimelineApiError);
    expect(() => createDownstreamUpdateProposal(unknownAction)).toThrow(TimelineApiError);
    expect(authFetchMock).not.toHaveBeenCalled();
  });

  it("rejects list pages that exceed the requested limit", async () => {
    authFetchMock.mockResolvedValueOnce(jsonResponse({
      workspace_id: workspaceId,
      items: [{ ...v2NonOwnerEvent(), id: laterEventId }, v2NonOwnerEvent()],
    }));

    await expect(listTimeline({ workspaceId, limit: 1 })).rejects.toMatchObject({ code: "INVALID_RESPONSE" });
  });

  it("preserves stable 409/503 Problems and classifies an unknown network result", async () => {
    authFetchMock
      .mockResolvedValueOnce(jsonResponse({ error_code: "KNOWLEDGE_IMPACT_CONFLICT", message: "report changed", retryable: false }, 409))
      .mockResolvedValueOnce(jsonResponse({ error_code: "KNOWLEDGE_IMPACT_UNAVAILABLE", message: "owner unavailable", retryable: true }, 503))
      .mockRejectedValueOnce(new TypeError("connection lost"));
    const input = { workspaceId, eventId, idempotencyKey: "impact-error-1" };

    await expect(analyzeImpact(input)).rejects.toMatchObject({ status: 409, errorCode: "KNOWLEDGE_IMPACT_CONFLICT", retryable: false });
    await expect(analyzeImpact(input)).rejects.toMatchObject({ status: 503, errorCode: "KNOWLEDGE_IMPACT_UNAVAILABLE", retryable: true });
    await expect(analyzeImpact(input)).rejects.toMatchObject({ code: "NETWORK_ERROR", status: null, retryable: true });
  });
});
