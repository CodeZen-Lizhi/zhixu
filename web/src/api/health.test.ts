import { afterEach, describe, expect, it, vi } from "vitest";

import { HealthApiError, createHealthRepairProposal, decodeHealthDecisionPage, decodeHealthIssueDetail, decodeHealthObservationPage, decodeHealthScan, decodeHealthSummary, decideHealthIssue, getHealthIssue, getHealthSummary, listHealthIssueDecisions, listHealthIssueObservations, listHealthIssues, startHealthScan } from "./health";

const workspaceId = "12000000-0000-4000-8000-000000000001";
const issueId = "12000000-0000-4000-8000-000000000002";
const scanId = "12000000-0000-4000-8000-000000000003";
const workflowRunId = "12000000-0000-4000-8000-000000000004";
const at = "2026-07-22T00:00:00Z";
const hash = "b".repeat(64);
const readModelRevision = "c".repeat(64);
const ref = { type: "CLAIM", id: "12000000-0000-4000-8000-000000000006" };
const checkpoint = { cursor: "", page: 0, last_item: null };
const counters = { processed: 1, created: 1, reopened: 0, resolved: 0, unchanged: 0, failed: 0 };
const coverage = [{ detector_id: "missing-source", detector_version: "v1", status: "PARTIAL", checkpoint, counters, last_error: { stage: "evidence", code: "SOURCE_UNAVAILABLE", retryable: true }, unavailable_reason: null }];
const scan = { id: scanId, workspace_id: workspaceId, workflow_run_id: workflowRunId, scope: { type: "WORKSPACE", ref: workspaceId, version: 1, schema_version: "health-scope/workspace/v1", hash: null }, fingerprint: hash, request_hash: hash, max_items: 5000, status: "PARTIAL", checkpoint, counters, coverage, last_error: null, version: 2, created_at: at, updated_at: at, completed_at: at, status_url: `/api/v1/health/scans/${scanId}?workspace_id=${workspaceId}` };
const issue = { id: issueId, workspace_id: workspaceId, type: "MISSING_SOURCE", target: ref, detector_id: "missing-source", detector_version: "v1", identity_hash: hash, fingerprint: hash, severity: "HIGH", evidence_summary: "缺少来源", evidence: [{ ref, hash, summary: "Claim 没有来源" }], object_versions: [{ ref, version: 2 }], repair_options: [{ code: "attach_source", title: "补充来源", available: false, unavailable_reason: "Repair Proposal 尚未启用" }, { code: "review", title: "发起复核", available: false, unavailable_reason: "能力未启用" }], status: "OPEN", status_reason: "", deferred_until: null, proposal: null, first_detected_at: at, last_detected_at: at, last_verified_at: at, resolved_at: null, version: 4, created_at: at, updated_at: at };
const observationId = "12000000-0000-4000-8000-000000000007";
const decisionId = "12000000-0000-4000-8000-000000000008";
const observation = { id: observationId, issue_version: 4, scan_id: scanId, detector_version: "v1", fingerprint: hash, evidence_fingerprint: hash, target_versions: [{ ref, version: 2 }], severity: "HIGH", observed_at: at, evidence: [{ ref, hash, summary: "Claim 没有来源" }] };
const decision = { id: decisionId, issue_version: 4, proposal_id: null, idempotency_key: "decision-1", action: "ACKNOWLEDGE", reason: "已确认", deferred_until: null, created_at: at };
const detailPayload = { issue, latest_observation: observation, observations: [observation], observations_has_more: false, decisions: [], decisions_has_more: false };
const trend = Array.from({ length: 7 }, (_item, index) => ({ date: `2026-07-${String(16 + index).padStart(2, "0")}`, detected_count: index, resolved_count: 6 - index }));
const jsonResponse = (payload: unknown, status = 200): Response => new Response(JSON.stringify(payload), { status, headers: { "Content-Type": "application/json" } });
const fetchUrl = (input: string | URL | Request | undefined): string => {
  if (typeof input === "string") return input;
  if (input instanceof URL) return input.href;
  if (input instanceof Request) return input.url;
  throw new Error("missing fetch URL");
};

afterEach(() => vi.unstubAllGlobals());

describe("Health API boundary", () => {
  it("strictly decodes summary, partial coverage and unavailable detectors", () => {
    const summary = decodeHealthSummary({
      workspace_id: workspaceId,
      open_count: 2,
      open_by_severity: { CRITICAL: 0, HIGH: 1, MEDIUM: 1, LOW: 0 },
      open_by_type: { MISSING_SOURCE: 1, REVIEW_INVALIDATED: 1 },
      trend,
      last_scan: { id: scanId, status: "PARTIAL", scope: scan.scope, completed_at: at, counters, coverage, updated_at: at },
      unavailable: [{ code: "REVIEW_INVALIDATED", reason: "Review 未启用" }],
    });
    expect(summary.lastScan?.coverage[0]).toMatchObject({ status: "PARTIAL", lastError: { code: "SOURCE_UNAVAILABLE" } });
    expect(summary.trend[0]).toEqual({ date: "2026-07-16", detectedCount: 0, resolvedCount: 6 });
    expect(summary.unavailable[0]?.code).toBe("REVIEW_INVALIDATED");
    expect(() => decodeHealthSummary({ workspace_id: workspaceId, open_count: 0, open_by_severity: { HIGH: 1, FUTURE: 1 }, open_by_type: {}, trend })).toThrow(HealthApiError);
    expect(() => decodeHealthSummary({ workspace_id: workspaceId, open_count: 0, open_by_severity: { CRITICAL: 0, HIGH: 0, MEDIUM: 0, LOW: 0 }, open_by_type: {}, trend: trend.slice(0, 6), unavailable: [] })).toThrow(HealthApiError);
    expect(() => decodeHealthSummary({ workspace_id: workspaceId, open_count: 0, open_by_severity: { CRITICAL: 0, HIGH: 0, MEDIUM: 0, LOW: 0 }, open_by_type: {}, trend: trend.map((point, index) => index === 2 ? { ...point, date: "2026-02-30" } : point), unavailable: [] })).toThrow(HealthApiError);
  });

  it("strictly decodes SMART Collection scan binding with exactCount zero", () => {
    const smartScope = { type: "SMART_COLLECTION", ref: issueId, version: 2, schema_version: "health-scope/smart-collection/v1", hash, read_model_revision: readModelRevision, exact_count: 0 };
    expect(decodeHealthScan({ ...scan, scope: smartScope })).toMatchObject({
      scope: { type: "SMART_COLLECTION", hash, readModelRevision, exactCount: 0 },
    });
    expect(decodeHealthSummary({
      workspace_id: workspaceId,
      open_count: 0,
      open_by_severity: { CRITICAL: 0, HIGH: 0, MEDIUM: 0, LOW: 0 },
      open_by_type: {},
      trend,
      last_scan: { id: scanId, status: "PARTIAL", scope: smartScope, completed_at: at, counters, coverage, updated_at: at },
      unavailable: [],
    }).lastScan?.scope).toMatchObject({ type: "SMART_COLLECTION", exactCount: 0 });
    expect(() => decodeHealthScan({ ...scan, scope: { ...smartScope, exact_count: undefined } })).toThrow(HealthApiError);
    expect(() => decodeHealthScan({ ...scan, scope: { ...smartScope, schema_version: "health-scope/smart-collection/v2" } })).toThrow(HealthApiError);
    expect(() => decodeHealthScan({ ...scan, scope: { ...scan.scope, read_model_revision: readModelRevision, exact_count: 1 } })).toThrow(HealthApiError);
  });

  it("rejects inconsistent scan and detector lifecycle facts", () => {
    expect(() => decodeHealthScan({ ...scan, status: "SUCCEEDED" })).toThrow(HealthApiError);
    expect(() => decodeHealthScan({ ...scan, status: "PARTIAL", completed_at: null })).toThrow(HealthApiError);
    expect(() => decodeHealthScan({ ...scan, coverage: [{ ...coverage[0], last_error: null }] })).toThrow(HealthApiError);
    expect(() => decodeHealthScan({ ...scan, coverage: [{ ...coverage[0], status: "UNAVAILABLE", last_error: null, unavailable_reason: null }] })).toThrow(HealthApiError);
    expect(() => decodeHealthScan({ ...scan, scope: { ...scan.scope, ref: issueId } })).toThrow(HealthApiError);
  });

  it("lazy-decodes evidence detail and repair options", () => {
    const detail = decodeHealthIssueDetail(detailPayload);
    expect(detail.issue.evidence[0]).toMatchObject({ summary: "Claim 没有来源" });
    expect(detail.issue.repairOptions.map((option) => option.available)).toEqual([false, false]);
    expect(detail).toMatchObject({ observationsNextCursor: null, observationsHasMore: false, decisionsNextCursor: null, decisionsHasMore: false });
    expect(() => decodeHealthIssueDetail({ ...detailPayload, issue: { ...issue, future: true } })).toThrow(HealthApiError);
    expect(() => decodeHealthIssueDetail({ issue, observations: [], observations_has_more: false, decisions: [], decisions_has_more: false })).toThrow(HealthApiError);
    expect(() => decodeHealthIssueDetail({ ...detailPayload, latest_observation: undefined })).toThrow(HealthApiError);
    expect(() => decodeHealthIssueDetail({ ...detailPayload, latest_observation: null })).toThrow(HealthApiError);
    expect(() => decodeHealthIssueDetail({ ...detailPayload, observations: [] })).toThrow(HealthApiError);
    expect(() => decodeHealthIssueDetail({ ...detailPayload, observations_has_more: true })).toThrow(HealthApiError);
    expect(() => decodeHealthIssueDetail({ ...detailPayload, latest_observation: { ...observation, fingerprint: "c".repeat(64) } })).toThrow(HealthApiError);
    expect(() => decodeHealthIssueDetail({ ...detailPayload, latest_observation: { ...observation, evidence: [{ ...observation.evidence[0], summary: "漂移" }] } })).toThrow(HealthApiError);
    const historicalObservation = { ...observation, id: "12000000-0000-4000-8000-000000000009", fingerprint: "c".repeat(64) };
    expect(decodeHealthIssueDetail({ ...detailPayload, observations: [historicalObservation], observations_next_cursor: "observation.next", observations_has_more: true, decisions: [decision] })).toMatchObject({ latestObservation: { id: observationId }, observations: [{ id: historicalObservation.id }], observationsNextCursor: "observation.next", observationsHasMore: true, decisions: [{ id: decisionId }], decisionsHasMore: false });
  });

  it("strictly decodes bounded observation and decision pages", () => {
    expect(decodeHealthObservationPage({ workspace_id: workspaceId, issue_id: issueId, items: [observation], next_cursor: "observation.next", has_more: true })).toMatchObject({ workspaceId, issueId, items: [{ id: observationId }], nextCursor: "observation.next", hasMore: true });
    expect(decodeHealthDecisionPage({ workspace_id: workspaceId, issue_id: issueId, items: [decision], has_more: false })).toMatchObject({ workspaceId, issueId, items: [{ id: decisionId }], nextCursor: null, hasMore: false });
    expect(() => decodeHealthObservationPage({ workspace_id: workspaceId, issue_id: issueId, items: [observation, observation], has_more: false })).toThrow(HealthApiError);
    expect(() => decodeHealthDecisionPage({ workspace_id: workspaceId, issue_id: issueId, items: [], has_more: false, future: true })).toThrow(HealthApiError);
    expect(() => decodeHealthDecisionPage({ workspace_id: workspaceId, issue_id: issueId, items: [], next_cursor: "unexpected", has_more: false })).toThrow(HealthApiError);
  });

  it("binds issue list/detail to Workspace and requested issue", async () => {
    vi.stubGlobal("fetch", vi.fn<typeof fetch>().mockResolvedValue(jsonResponse({ workspace_id: workspaceId, items: [{ ...issue, evidence: undefined, object_versions: undefined, repair_options: undefined, identity_hash: undefined, fingerprint: undefined, status_reason: undefined, deferred_until: undefined, proposal: undefined, resolved_at: undefined, created_at: undefined }], has_more: false })));
    await expect(listHealthIssues({ workspaceId, statuses: ["OPEN"], limit: 25 })).resolves.toMatchObject({ items: [{ id: issueId }] });

    vi.stubGlobal("fetch", vi.fn<typeof fetch>().mockResolvedValue(jsonResponse({ ...detailPayload, issue: { ...issue, id: "12000000-0000-4000-8000-000000000009" } })));
    await expect(getHealthIssue(workspaceId, issueId)).rejects.toMatchObject({ code: "INVALID_RESPONSE" });

    vi.stubGlobal("fetch", vi.fn<typeof fetch>().mockResolvedValue(jsonResponse({ workspace_id: workspaceId, items: [], next_cursor: null, has_more: true })));
    await expect(listHealthIssues({ workspaceId, limit: 25 })).rejects.toMatchObject({ code: "INVALID_RESPONSE" });
  });

  it("requests both history endpoints with bounded cursor pagination and response binding", async () => {
    const controller = new AbortController();
    const fetchMock = vi.fn<typeof fetch>()
      .mockResolvedValueOnce(jsonResponse({ workspace_id: workspaceId, issue_id: issueId, items: [observation], next_cursor: "opaque+observation", has_more: true }))
      .mockResolvedValueOnce(jsonResponse({ workspace_id: workspaceId, issue_id: issueId, items: [decision], has_more: false }));
    vi.stubGlobal("fetch", fetchMock);

    await expect(listHealthIssueObservations({ workspaceId, issueId, limit: 25 }, controller.signal)).resolves.toMatchObject({ items: [{ id: observationId }], hasMore: true });
    await expect(listHealthIssueDecisions({ workspaceId, issueId, limit: 100, cursor: "opaque+decision" })).resolves.toMatchObject({ items: [{ id: decisionId }], hasMore: false });
    expect(fetchUrl(fetchMock.mock.calls[0]?.[0])).toContain(`/api/v1/health/issues/${issueId}/observations?workspace_id=${workspaceId}&limit=25`);
    expect(fetchMock.mock.calls[0]?.[1]?.signal).toBe(controller.signal);
    expect(fetchUrl(fetchMock.mock.calls[1]?.[0])).toContain(`/api/v1/health/issues/${issueId}/decisions?workspace_id=${workspaceId}&limit=100&cursor=opaque%2Bdecision`);

    vi.stubGlobal("fetch", vi.fn<typeof fetch>().mockResolvedValue(jsonResponse({ workspace_id: workspaceId, issue_id: scanId, items: [], has_more: false })));
    await expect(listHealthIssueObservations({ workspaceId, issueId })).rejects.toMatchObject({ code: "INVALID_RESPONSE" });
    expect(() => listHealthIssueDecisions({ workspaceId, issueId, limit: 101 })).toThrow(HealthApiError);
  });

  it("validates decisions and preserves Idempotency-Key", async () => {
    expect(() => decideHealthIssue({ workspaceId, issueId, expectedVersion: 4, action: "IGNORE", idempotencyKey: "decision-1" })).toThrow(HealthApiError);

    const fetchMock = vi.fn<typeof fetch>().mockResolvedValue(jsonResponse({ ...issue, status: "ACKNOWLEDGED", version: 5 }));
    vi.stubGlobal("fetch", fetchMock);
    await expect(decideHealthIssue({ workspaceId, issueId, expectedVersion: 4, action: "ACKNOWLEDGE", idempotencyKey: "decision-1" })).resolves.toMatchObject({ status: "ACKNOWLEDGED", version: 5 });
    expect(new Headers(fetchMock.mock.calls[0]?.[1]?.headers).get("Idempotency-Key")).toBe("decision-1");
  });

  it("starts scans and sends repair option requests without inventing success", async () => {
    expect(() => startHealthScan({ workspaceId, scope: { type: "WORKSPACE", ref: workspaceId, version: 1, schemaVersion: "health-scope/workspace/v2", hash: null }, idempotencyKey: "scan-v2" })).toThrow(HealthApiError);
    const fetchMock = vi.fn<typeof fetch>()
      .mockResolvedValueOnce(jsonResponse({ scan, health_scan_id: scanId, workflow_run_id: workflowRunId, status_url: scan.status_url, replayed: false }, 202))
      .mockResolvedValueOnce(jsonResponse({ error_code: "HEALTH_REPAIR_PROPOSAL_UNAVAILABLE", message: "repair proposal unavailable", retryable: false }, 503));
    vi.stubGlobal("fetch", fetchMock);

    await expect(startHealthScan({ workspaceId, scope: { type: "WORKSPACE", ref: workspaceId, version: 1, schemaVersion: "health-scope/workspace/v1", hash: null }, idempotencyKey: "scan-1" })).resolves.toMatchObject({ healthScanId: scanId, scan: { status: "PARTIAL" } });
    await expect(createHealthRepairProposal({ workspaceId, issueId, expectedVersion: 4, repairOptionCode: "attach_source", idempotencyKey: "repair-1" })).rejects.toMatchObject({ code: "HTTP_ERROR", errorCode: "HEALTH_REPAIR_PROPOSAL_UNAVAILABLE" });
    const bodyText = fetchMock.mock.calls[1]?.[1]?.body;
    if (typeof bodyText !== "string") throw new Error("missing repair JSON body");
    expect(JSON.parse(bodyText)).toMatchObject({ repair_option_code: "attach_source", expected_version: 4 });
  });

  it("encodes SMART Collection scan scope with read model revision and exact count", async () => {
    const smartScan = { ...scan, scope: { type: "SMART_COLLECTION", ref: issueId, version: 2, schema_version: "health-scope/smart-collection/v1", hash, read_model_revision: readModelRevision, exact_count: 0 } };
    const fetchMock = vi.fn<typeof fetch>().mockResolvedValue(jsonResponse({ scan: smartScan, health_scan_id: scanId, workflow_run_id: workflowRunId, status_url: scan.status_url, replayed: false }, 202));
    vi.stubGlobal("fetch", fetchMock);
    await expect(startHealthScan({ workspaceId, scope: { type: "SMART_COLLECTION", ref: issueId, version: 2, schemaVersion: "health-scope/smart-collection/v1", hash, readModelRevision, exactCount: 0 }, idempotencyKey: "scan-smart" })).resolves.toMatchObject({ healthScanId: scanId });
    const bodyText = fetchMock.mock.calls[0]?.[1]?.body;
    if (typeof bodyText !== "string") throw new Error("missing scan JSON body");
    expect(JSON.parse(bodyText)).toMatchObject({ scope: { type: "SMART_COLLECTION", hash, read_model_revision: readModelRevision, exact_count: 0 } });
  });

  it("rejects scan acceptance binding drift and preserves AbortError identity", async () => {
    vi.stubGlobal("fetch", vi.fn<typeof fetch>().mockResolvedValue(jsonResponse({ scan, health_scan_id: issueId, workflow_run_id: workflowRunId, status_url: scan.status_url, replayed: false }, 202)));
    await expect(startHealthScan({ workspaceId, scope: { type: "WORKSPACE", ref: workspaceId, version: 1, schemaVersion: "health-scope/workspace/v1", hash: null }, idempotencyKey: "scan-drift" })).rejects.toMatchObject({ code: "INVALID_RESPONSE" });

    const abort = new DOMException("cancelled", "AbortError");
    vi.stubGlobal("fetch", vi.fn<typeof fetch>().mockRejectedValue(abort));
    await expect(getHealthSummary(workspaceId)).rejects.toBe(abort);
  });
});
