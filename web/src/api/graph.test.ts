import { afterEach, describe, expect, it, vi } from "vitest";

import {
  GraphApiError,
  decodeGraphGlobalPage,
  decodeGraphNeighborhood,
  decodeGraphNode,
  decodeGraphNodeSearchResult,
  decodeGraphPathResult,
  decodeGraphRelationDetail,
  decodeGraphRelationEvidencePage,
  findGraphPath,
  getGraphGlobalPage,
  getGraphNeighborhood,
  getGraphNodeDetail,
  getGraphRelationDetail,
  getGraphRelationEvidencePage,
  searchGraphNodes,
  type GraphGlobalInput,
  type GraphPathInput,
} from "./graph";

const workspaceId = "11111111-1111-1111-1111-111111111111";
const topicId = "22222222-2222-2222-2222-222222222222";
const claimId = "33333333-3333-3333-3333-333333333333";
const otherClaimId = "44444444-4444-4444-4444-444444444444";
const relationId = "55555555-5555-5555-5555-555555555555";
const evidenceId = "66666666-6666-6666-6666-666666666666";
const sourceVersionId = "77777777-7777-7777-7777-777777777777";
const sourceSpanId = "88888888-8888-8888-8888-888888888888";
const fingerprint = "a".repeat(64);
const evidenceFingerprint = "b".repeat(64);
const relationFingerprint = "c".repeat(64);
const applicabilityHash = "d".repeat(64);
const updatedAt = "2026-07-20T09:02:03.123456789Z";
const createdAt = "2026-07-19T09:02:03Z";

const applicabilityPayload = {
  schema_version: "knowledge-applicability/v1",
  value: { audience: ["engineers", { region: "cn" }], score: 1.25, enabled: true, optional: null },
  hash: applicabilityHash,
};

const topicPayload = {
  type: "TOPIC",
  id: topicId,
  workspace_id: workspaceId,
  name: "Graph projection",
  description: "A bounded topic cluster.",
  topic_status: "ACTIVE",
  version: 2,
  updated_at: updatedAt,
};

const claimPayload = {
  type: "CLAIM",
  id: claimId,
  workspace_id: workspaceId,
  statement: "Graph queries remain bounded.",
  claim_status: "CONFIRMED",
  confidence: 0.9,
  applicability: applicabilityPayload,
  version: 3,
  updated_at: updatedAt,
};

const otherClaimPayload = {
  ...claimPayload,
  id: otherClaimId,
  statement: "A second bounded claim.",
};

const evidenceHref = `/api/v1/graph/relations/${relationId}/evidence?workspace_id=${workspaceId}`;
const edgePayload = {
  relation_id: relationId,
  workspace_id: workspaceId,
  source: { type: "CLAIM", id: claimId },
  target: { type: "TOPIC", id: topicId },
  type: "BELONGS_TO",
  status: "CONFIRMED",
  traversal: "FORWARD",
  confidence: 0.8,
  version: 4,
  evidence_count: 1,
  evidence_fingerprint: evidenceFingerprint,
  evidence_href: evidenceHref,
  updated_at: updatedAt,
};

const supportRelationId = evidenceId;
const supportEdgePayload = {
  ...edgePayload,
  relation_id: supportRelationId,
  target: { type: "CLAIM", id: otherClaimId },
  type: "SUPPORTS",
  evidence_count: 0,
  evidence_fingerprint: "",
  evidence_href: `/api/v1/graph/relations/${supportRelationId}/evidence?workspace_id=${workspaceId}`,
};

const completeMeta = { fingerprint, complete: true, truncated: false };
const globalPayload = {
  workspace_id: workspaceId,
  clusters: [{
    topic: topicPayload,
    direct_claim_count: 2,
    incident_relation_count: 3,
    cluster_score: 5,
    updated_at: updatedAt,
  }],
  meta: completeMeta,
};

const neighborhoodPayload = {
  workspace_id: workspaceId,
  center: { type: "CLAIM", id: claimId },
  nodes: [claimPayload, topicPayload],
  edges: [edgePayload],
  boundary_nodes: [],
  layer_counts: [1],
  completed_depth: 1,
  meta: completeMeta,
};

const foundPathPayload = {
  workspace_id: workspaceId,
  from: { type: "CLAIM", id: claimId },
  to: { type: "TOPIC", id: topicId },
  status: "found",
  nodes: [claimPayload, topicPayload],
  edges: [edgePayload],
  hop_count: 1,
  explored_nodes: 2,
  common_topic_suggestions: [],
};

const notFoundPathPayload = {
  workspace_id: workspaceId,
  from: { type: "CLAIM", id: claimId },
  to: { type: "TOPIC", id: topicId },
  status: "not_found",
  nodes: [],
  edges: [],
  hop_count: 0,
  explored_nodes: 4,
  common_topic_suggestions: [topicPayload],
};

const searchPayload = {
  workspace_id: workspaceId,
  matches: [
    { kind: "EXACT", node: topicPayload },
    { kind: "PREFIX", node: claimPayload },
  ],
};

const detailPayload = {
  edge: edgePayload,
  confirmation: { method: "USER_APPROVAL", reference: "approval:graph-1" },
  fingerprint: relationFingerprint,
  valid_from: createdAt,
  valid_to: null,
  created_at: createdAt,
  updated_at: updatedAt,
};

const sourceHref = `/api/v1/workspaces/${workspaceId}/source-versions/${sourceVersionId}`;
const spanHref = `${sourceHref}/spans/${sourceSpanId}`;
const evidenceItemPayload = {
  id: evidenceId,
  workspace_id: workspaceId,
  relation_id: relationId,
  provenance: {
    workspace_id: workspaceId,
    source_version_id: sourceVersionId,
    source_span_id: sourceSpanId,
  },
  reason: "The source span supports this relation.",
  applicability: applicabilityPayload,
  confirmation: { method: "SOURCE_DERIVED", reference: "source:graph-1" },
  source_href: sourceHref,
  span_href: spanHref,
  created_at: createdAt,
};

const evidencePagePayload = {
  workspace_id: workspaceId,
  relation_id: relationId,
  items: [evidenceItemPayload],
  meta: completeMeta,
};

const jsonResponse = (payload: unknown, status = 200): Response => new Response(JSON.stringify(payload), {
  status,
  headers: { "Content-Type": "application/json" },
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("Graph response decoders", () => {
  it("decodes Topic and Claim as an exact discriminated union", () => {
    expect(decodeGraphNode(topicPayload, { workspaceId, nodeType: "TOPIC", nodeId: topicId })).toEqual({
      type: "TOPIC",
      id: topicId,
      workspaceId,
      name: "Graph projection",
      description: "A bounded topic cluster.",
      topicStatus: "ACTIVE",
      version: 2,
      updatedAt,
    });
    const claim = decodeGraphNode(claimPayload, { workspaceId, nodeType: "CLAIM", nodeId: claimId });
    expect(claim).toMatchObject({ type: "CLAIM", claimStatus: "CONFIRMED", confidence: 0.9 });
    if (claim.type !== "CLAIM") throw new Error("expected Claim");
    expect(claim.applicability).toEqual({
      schemaVersion: "knowledge-applicability/v1",
      value: applicabilityPayload.value,
      hash: applicabilityHash,
    });
  });

  it("decodes every list and detail response shape", () => {
    expect(decodeGraphGlobalPage(globalPayload, { workspaceId, limit: 25 })).toMatchObject({
      workspaceId,
      clusters: [{ directClaimCount: 2, incidentRelationCount: 3, clusterScore: 5 }],
    });
    expect(decodeGraphNodeSearchResult(searchPayload, { workspaceId, query: "Graph", limit: 20 }).matches).toHaveLength(2);
    expect(decodeGraphNeighborhood(neighborhoodPayload, {
      workspaceId,
      center: { type: "CLAIM", id: claimId },
      depth: 1,
    })).toMatchObject({ completedDepth: 1, layerCounts: [1], center: { type: "CLAIM", id: claimId } });
    expect(decodeGraphPathResult(foundPathPayload, {
      workspaceId,
      from: { type: "CLAIM", id: claimId },
      to: { type: "TOPIC", id: topicId },
    })).toMatchObject({ status: "found", hopCount: 1, exploredNodes: 2 });
    expect(decodeGraphPathResult(notFoundPathPayload, {
      workspaceId,
      from: { type: "CLAIM", id: claimId },
      to: { type: "TOPIC", id: topicId },
    })).toMatchObject({ status: "not_found", nodes: [], edges: [], hopCount: 0 });
    expect(decodeGraphRelationDetail(detailPayload, { workspaceId, relationId })).toMatchObject({
      edge: { relationId, evidenceHref },
      confirmation: { method: "USER_APPROVAL", reference: "approval:graph-1" },
      validTo: null,
    });
    expect(decodeGraphRelationEvidencePage(evidencePagePayload, { workspaceId, relationId, limit: 20 })).toMatchObject({
      workspaceId,
      relationId,
      items: [{ id: evidenceId, sourceHref, spanHref }],
    });
  });

  it("keeps empty arrays explicit and preserves paged/truncated metadata", () => {
    expect(decodeGraphGlobalPage({ ...globalPayload, clusters: [] }).clusters).toEqual([]);
    expect(decodeGraphNodeSearchResult({ ...searchPayload, matches: [] }).matches).toEqual([]);
    expect(decodeGraphNeighborhood({
      ...neighborhoodPayload,
      nodes: [claimPayload],
      edges: [],
      layer_counts: [0],
    }).edges).toEqual([]);
    expect(decodeGraphRelationEvidencePage({ ...evidencePagePayload, items: [] }).items).toEqual([]);
    expect(decodeGraphGlobalPage({
      ...globalPayload,
      meta: { fingerprint, next_cursor: "cursor-v1.opaque", complete: false, truncated: false },
    }).meta.nextCursor).toBe("cursor-v1.opaque");
    expect(decodeGraphGlobalPage({
      ...globalPayload,
      meta: { fingerprint, complete: false, truncated: true, reason: "RESULT_WINDOW_LIMIT" },
    }).meta.reason).toBe("RESULT_WINDOW_LIMIT");
  });

  it.each([
    ["top-level extra key", { ...globalPayload, extra: true }],
    ["nested extra key", { ...globalPayload, clusters: [{ ...globalPayload.clusters[0], extra: true }] }],
    ["invalid UUID", { ...globalPayload, workspace_id: "not-a-uuid" }],
    ["invalid hash", { ...globalPayload, meta: { ...completeMeta, fingerprint: "A".repeat(64) } }],
    ["invalid timestamp", { ...globalPayload, clusters: [{ ...globalPayload.clusters[0], updated_at: "2026-02-30T00:00:00Z" }] }],
    ["non-finite confidence", { ...globalPayload, clusters: [{ ...globalPayload.clusters[0], topic: { ...topicPayload, version: Number.POSITIVE_INFINITY } }] }],
    ["wrong cluster score", { ...globalPayload, clusters: [{ ...globalPayload.clusters[0], cluster_score: 4 }] }],
    ["duplicate cluster identity", { ...globalPayload, clusters: [globalPayload.clusters[0], globalPayload.clusters[0]] }],
    ["invalid meta state", { ...globalPayload, meta: { fingerprint, complete: true, truncated: false, next_cursor: "cursor" } }],
  ])("rejects %s", (_name, payload) => {
    expect(() => decodeGraphGlobalPage(payload)).toThrow(GraphApiError);
  });

  it("rejects oversized response arrays", () => {
    expect(() => decodeGraphGlobalPage({ ...globalPayload, clusters: Array.from({ length: 101 }, () => globalPayload.clusters[0]) })).toThrow(GraphApiError);
    expect(() => decodeGraphNodeSearchResult({ ...searchPayload, matches: Array.from({ length: 51 }, () => searchPayload.matches[0]) })).toThrow(GraphApiError);
    expect(() => decodeGraphRelationEvidencePage({ ...evidencePagePayload, items: Array.from({ length: 101 }, () => evidenceItemPayload) })).toThrow(GraphApiError);
  });

  it("rejects every unstable response order with nanosecond timestamp precision", () => {
    const laterTopic = { ...topicPayload, id: otherClaimId, name: "Later topic" };
    expect(() => decodeGraphGlobalPage({
      ...globalPayload,
      clusters: [
        { ...globalPayload.clusters[0], updated_at: "2026-07-20T09:02:03.000000001Z" },
        { ...globalPayload.clusters[0], topic: laterTopic, updated_at: "2026-07-20T09:02:03.000000002Z" },
      ],
    })).toThrow(GraphApiError);
    expect(() => decodeGraphNodeSearchResult({
      ...searchPayload,
      matches: [{ kind: "PREFIX", node: claimPayload }, { kind: "EXACT", node: topicPayload }],
    })).toThrow(GraphApiError);
    expect(() => decodeGraphNodeSearchResult({
      ...searchPayload,
      matches: [{ kind: "EXACT", node: topicPayload }, { kind: "EXACT", node: claimPayload }],
    })).toThrow(GraphApiError);
    expect(() => decodeGraphNeighborhood({
      ...neighborhoodPayload,
      nodes: [topicPayload, claimPayload],
    })).toThrow(GraphApiError);
    expect(() => decodeGraphNeighborhood({
      ...neighborhoodPayload,
      nodes: [claimPayload, otherClaimPayload, topicPayload],
      edges: [supportEdgePayload, edgePayload],
      layer_counts: [2],
    })).toThrow(GraphApiError);
    expect(() => decodeGraphPathResult({
      ...notFoundPathPayload,
      common_topic_suggestions: [laterTopic, topicPayload],
    })).toThrow(GraphApiError);
    const laterEvidence = { ...evidenceItemPayload, id: otherClaimId, created_at: "2026-07-19T09:02:03.000000002Z" };
    const earlierEvidence = { ...evidenceItemPayload, created_at: "2026-07-19T09:02:03.000000001Z" };
    expect(() => decodeGraphRelationEvidencePage({
      ...evidencePagePayload,
      items: [laterEvidence, earlierEvidence],
    })).toThrow(GraphApiError);
  });

  it("binds depth-one edges to request limit and uses path default max depth six", () => {
    const twoEdgeNeighborhood = {
      ...neighborhoodPayload,
      nodes: [claimPayload, otherClaimPayload, topicPayload],
      edges: [edgePayload, supportEdgePayload],
      layer_counts: [2],
    };
    expect(() => decodeGraphNeighborhood(twoEdgeNeighborhood, {
      workspaceId,
      center: { type: "CLAIM", id: claimId },
      depth: 1,
      limit: 1,
    })).toThrow(GraphApiError);
    expect(() => decodeGraphPathResult({
      ...foundPathPayload,
      nodes: Array.from({ length: 8 }, () => claimPayload),
      edges: Array.from({ length: 7 }, () => edgePayload),
      hop_count: 7,
    }, {
      workspaceId,
      from: { type: "CLAIM", id: claimId },
      to: { type: "TOPIC", id: topicId },
    })).toThrow(GraphApiError);
  });

  it("rejects pagination cursors for depth-two and depth-three snapshots", () => {
    expect(() => decodeGraphNeighborhood({
      ...neighborhoodPayload,
      nodes: [claimPayload],
      edges: [],
      layer_counts: [0, 0],
      completed_depth: 2,
      meta: { fingerprint, next_cursor: "unexpected-next", complete: false, truncated: false },
    }, {
      workspaceId,
      center: { type: "CLAIM", id: claimId },
      depth: 2,
    })).toThrow(GraphApiError);
  });

  it.each([
    ["cross-workspace node", { ...neighborhoodPayload, nodes: [{ ...claimPayload, workspace_id: otherClaimId }, topicPayload] }],
    ["duplicate node identity", { ...neighborhoodPayload, nodes: [claimPayload, claimPayload], layer_counts: [1] }],
    ["duplicate relation identity", { ...neighborhoodPayload, edges: [edgePayload, edgePayload] }],
    ["missing center", { ...neighborhoodPayload, nodes: [topicPayload] }],
    ["endpoint outside closure", { ...neighborhoodPayload, edges: [{ ...edgePayload, target: { type: "TOPIC", id: otherClaimId } }] }],
    ["unused boundary", { ...neighborhoodPayload, boundary_nodes: [{ type: "CLAIM", id: otherClaimId }] }],
    ["layer count mismatch", { ...neighborhoodPayload, layer_counts: [2] }],
    ["mismatched edge href", { ...neighborhoodPayload, edges: [{ ...edgePayload, evidence_href: `${evidenceHref}&extra=1` }] }],
    ["incompatible edge endpoints", { ...neighborhoodPayload, edges: [{ ...edgePayload, source: { type: "TOPIC", id: topicId }, target: { type: "CLAIM", id: claimId } }] }],
    ["missing evidence fingerprint", { ...neighborhoodPayload, edges: [{ ...edgePayload, evidence_fingerprint: "" }] }],
  ])("rejects neighborhood %s", (_name, payload) => {
    expect(() => decodeGraphNeighborhood(payload)).toThrow(GraphApiError);
  });

  it.each([
    ["discontinuous edge", { ...foundPathPayload, edges: [{ ...edgePayload, traversal: "REVERSE" }] }],
    ["partial not-found path", { ...notFoundPathPayload, nodes: [claimPayload], hop_count: 1 }],
    ["wrong hop count", { ...foundPathPayload, hop_count: 2 }],
    ["stale formal path edge", { ...foundPathPayload, edges: [{ ...edgePayload, status: "STALE" }] }],
    ["duplicate path node", { ...foundPathPayload, nodes: [claimPayload, claimPayload] }],
  ])("rejects path %s", (_name, payload) => {
    expect(() => decodeGraphPathResult(payload)).toThrow(GraphApiError);
  });

  it.each([
    ["relation owner", { ...detailPayload, edge: { ...edgePayload, workspace_id: otherClaimId } }],
    ["relation identity", { ...detailPayload, edge: { ...edgePayload, relation_id: otherClaimId } }],
    ["invalid validity interval", { ...detailPayload, valid_from: updatedAt, valid_to: createdAt }],
    ["invalid confirmation", { ...detailPayload, confirmation: { method: "AI", reference: "model" } }],
  ])("rejects detail %s", (_name, payload) => {
    expect(() => decodeGraphRelationDetail(payload, { workspaceId, relationId })).toThrow(GraphApiError);
  });

  it.each([
    ["evidence owner", { ...evidencePagePayload, items: [{ ...evidenceItemPayload, relation_id: otherClaimId }] }],
    ["provenance workspace", { ...evidencePagePayload, items: [{ ...evidenceItemPayload, provenance: { ...evidenceItemPayload.provenance, workspace_id: otherClaimId } }] }],
    ["source href", { ...evidencePagePayload, items: [{ ...evidenceItemPayload, source_href: `${sourceHref}/wrong` }] }],
    ["span href", { ...evidencePagePayload, items: [{ ...evidenceItemPayload, span_href: `${spanHref}/wrong` }] }],
    ["duplicate evidence", { ...evidencePagePayload, items: [evidenceItemPayload, evidenceItemPayload] }],
  ])("rejects %s mismatch", (_name, payload) => {
    expect(() => decodeGraphRelationEvidencePage(payload)).toThrow(GraphApiError);
  });

  it("rejects non-finite, oversized and over-depth canonical applicability JSON", () => {
    const nonFinite = structuredClone(claimPayload);
    Reflect.set(nonFinite.applicability.value, "score", Number.NaN);
    expect(() => decodeGraphNode(nonFinite)).toThrow(GraphApiError);

    const oversized = structuredClone(claimPayload);
    Reflect.set(oversized.applicability.value, "text", "x".repeat(16 * 1024));
    expect(() => decodeGraphNode(oversized)).toThrow(GraphApiError);

    let deep: unknown = {};
    for (let index = 0; index < 17; index += 1) deep = { child: deep };
    const tooDeep = structuredClone(claimPayload);
    Reflect.set(tooDeep.applicability, "value", deep);
    expect(() => decodeGraphNode(tooDeep)).toThrow(GraphApiError);
  });
});

describe("Graph clients and request serialization", () => {
  it("serializes all POST request fields to strict snake_case", async () => {
    const fetchMock = vi.fn<typeof fetch>()
      .mockResolvedValueOnce(jsonResponse(globalPayload))
      .mockResolvedValueOnce(jsonResponse(neighborhoodPayload))
      .mockResolvedValueOnce(jsonResponse(foundPathPayload));
    vi.stubGlobal("fetch", fetchMock);

    await getGraphGlobalPage({
      workspaceId,
      filter: {
        nodeTypes: ["TOPIC", "CLAIM"],
        relationTypes: ["BELONGS_TO"],
        topicIds: [topicId],
        relationStatuses: ["CONFIRMED", "STALE"],
        claimStatuses: ["CONFIRMED", "DISPUTED"],
        claimMinConfidence: 0,
        relationMinConfidence: 1,
        updatedAfter: createdAt,
      },
      cursor: "opaque-global",
      limit: 25,
    });
    await getGraphNeighborhood({
      workspaceId,
      center: { type: "CLAIM", id: claimId },
      depth: 1,
      limit: 25,
      direction: "BOTH",
      filter: { relationTypes: ["BELONGS_TO"] },
      maxNodes: 500,
      maxEdges: 1000,
      maxFrontier: 500,
      cursor: "opaque-neighborhood",
    });
    await findGraphPath({
      workspaceId,
      from: { type: "CLAIM", id: claimId },
      to: { type: "TOPIC", id: topicId },
      direction: "BOTH",
      relationTypes: ["BELONGS_TO"],
      maxDepth: 8,
      maxVisited: 500,
    });

    const bodies = fetchMock.mock.calls.map((call) => {
      const body = call[1]?.body;
      if (typeof body !== "string") throw new Error("request body missing");
      const parsed: unknown = JSON.parse(body);
      return parsed;
    });
    expect(bodies[0]).toEqual({
      workspace_id: workspaceId,
      filter: {
        node_types: ["TOPIC", "CLAIM"],
        relation_types: ["BELONGS_TO"],
        topic_ids: [topicId],
        relation_statuses: ["CONFIRMED", "STALE"],
        claim_statuses: ["CONFIRMED", "DISPUTED"],
        claim_min_confidence: 0,
        relation_min_confidence: 1,
        updated_after: createdAt,
      },
      cursor: "opaque-global",
      limit: 25,
    });
    expect(bodies[1]).toEqual({
      workspace_id: workspaceId,
      center: { type: "CLAIM", id: claimId },
      depth: 1,
      limit: 25,
      direction: "BOTH",
      filter: { relation_types: ["BELONGS_TO"] },
      max_nodes: 500,
      max_edges: 1000,
      max_frontier: 500,
      cursor: "opaque-neighborhood",
    });
    expect(bodies[2]).toEqual({
      workspace_id: workspaceId,
      from: { type: "CLAIM", id: claimId },
      to: { type: "TOPIC", id: topicId },
      direction: "BOTH",
      relation_types: ["BELONGS_TO"],
      max_depth: 8,
      max_visited: 500,
    });
  });

  it("calls and decodes all four GET endpoints with exact query serialization", async () => {
    const fetchMock = vi.fn<typeof fetch>()
      .mockResolvedValueOnce(jsonResponse(searchPayload))
      .mockResolvedValueOnce(jsonResponse(claimPayload))
      .mockResolvedValueOnce(jsonResponse(detailPayload))
      .mockResolvedValueOnce(jsonResponse(evidencePagePayload));
    vi.stubGlobal("fetch", fetchMock);

    await expect(searchGraphNodes({ workspaceId, query: "图谱 query", limit: 50 })).resolves.toMatchObject({ workspaceId });
    await expect(getGraphNodeDetail({ workspaceId, nodeType: "CLAIM", nodeId: claimId })).resolves.toMatchObject({ id: claimId });
    await expect(getGraphRelationDetail({ workspaceId, relationId })).resolves.toMatchObject({ edge: { relationId } });
    await expect(getGraphRelationEvidencePage({ workspaceId, relationId, cursor: "opaque/evidence+1", limit: 100 })).resolves.toMatchObject({ relationId });

    expect(fetchMock.mock.calls.map((call) => call[0])).toEqual([
      `/api/v1/graph/nodes?workspace_id=${workspaceId}&query=%E5%9B%BE%E8%B0%B1+query&limit=50`,
      `/api/v1/graph/nodes/CLAIM/${claimId}?workspace_id=${workspaceId}`,
      `/api/v1/graph/relations/${relationId}?workspace_id=${workspaceId}`,
      `/api/v1/graph/relations/${relationId}/evidence?workspace_id=${workspaceId}&cursor=opaque%2Fevidence%2B1&limit=100`,
    ]);
    for (const call of fetchMock.mock.calls) {
      expect(call[1]?.method).toBe("GET");
      expect(new Headers(call[1]?.headers).get("Accept")).toBe("application/json");
      expect(call[1]?.body).toBeUndefined();
    }
  });

  const invalidGlobalInputs: (readonly [string, GraphGlobalInput])[] = [
    ["duplicate filter values", { workspaceId, filter: { nodeTypes: ["TOPIC", "TOPIC"] } }],
    ["invalid confidence", { workspaceId, filter: { claimMinConfidence: Number.NaN } }],
    ["oversized cursor", { workspaceId, cursor: "x".repeat(2049) }],
    ["invalid limit", { workspaceId, limit: 101 }],
  ];

  it.each(invalidGlobalInputs)("rejects global request with %s before fetch", async (_name, input) => {
    const fetchMock = vi.fn<typeof fetch>();
    vi.stubGlobal("fetch", fetchMock);
    await expect(getGraphGlobalPage(input)).rejects.toMatchObject({ code: "INVALID_REQUEST", status: null });
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("rejects injected unknown request keys and enum values", async () => {
    const globalInput = { workspaceId };
    Reflect.set(globalInput, "unexpected", true);
    await expect(getGraphGlobalPage(globalInput)).rejects.toMatchObject({ code: "INVALID_REQUEST" });

    const pathInput: GraphPathInput = {
      workspaceId,
      from: { type: "CLAIM", id: claimId },
      to: { type: "TOPIC", id: topicId },
    };
    Reflect.set(pathInput, "direction", "SIDEWAYS");
    await expect(findGraphPath(pathInput)).rejects.toMatchObject({ code: "INVALID_REQUEST" });
  });

  it("rejects neighborhood and path semantic request violations", async () => {
    await expect(getGraphNeighborhood({
      workspaceId,
      center: { type: "TOPIC", id: topicId },
      depth: 2,
      cursor: "depth-one-only",
    })).rejects.toMatchObject({ code: "INVALID_REQUEST" });
    await expect(getGraphNeighborhood({
      workspaceId,
      center: { type: "TOPIC", id: topicId },
      filter: { nodeTypes: ["CLAIM"] },
    })).rejects.toMatchObject({ code: "INVALID_REQUEST" });
    await expect(findGraphPath({
      workspaceId,
      from: { type: "CLAIM", id: claimId },
      to: { type: "CLAIM", id: claimId },
    })).rejects.toMatchObject({ code: "INVALID_REQUEST" });
  });

  it.each([
    ["one UTF-8 byte", "a"],
    ["leading whitespace", " graph"],
    ["257 UTF-8 bytes", `a${"汉".repeat(86)}`],
    ["unpaired surrogate", "\ud800x"],
  ])("rejects node search query with %s", async (_name, query) => {
    await expect(searchGraphNodes({ workspaceId, query })).rejects.toMatchObject({ code: "INVALID_REQUEST" });
  });
});

describe("Graph failure semantics", () => {
  it("maps a strict server Problem to GraphApiError", async () => {
    vi.stubGlobal("fetch", vi.fn<typeof fetch>().mockResolvedValue(jsonResponse({
      error_code: "GRAPH_CURSOR_STALE",
      message: "cursor is stale",
      retryable: false,
      details: { restart_required: true },
    }, 409)));
    await expect(getGraphGlobalPage({ workspaceId })).rejects.toMatchObject({
      name: "GraphApiError",
      code: "GRAPH_CURSOR_STALE",
      errorCode: "GRAPH_CURSOR_STALE",
      status: 409,
      retryable: false,
      details: { restart_required: true },
    });
  });

  it.each([
    ["unknown field", { error_code: "X", message: "failed", retryable: false, extra: true }],
    ["invalid workflow", { error_code: "X", message: "failed", retryable: false, workflow_run_id: "bad" }],
    ["invalid details", { error_code: "X", message: "failed", retryable: false, details: [] }],
  ])("rejects Problem with %s", async (_name, problem) => {
    vi.stubGlobal("fetch", vi.fn<typeof fetch>().mockResolvedValue(jsonResponse(problem, 500)));
    await expect(getGraphGlobalPage({ workspaceId })).rejects.toMatchObject({
      code: "INVALID_RESPONSE",
      status: 500,
      retryable: false,
    });
  });

  it("reports malformed success JSON without hiding the HTTP status", async () => {
    vi.stubGlobal("fetch", vi.fn<typeof fetch>().mockResolvedValue(new Response("not-json", { status: 200 })));
    await expect(getGraphGlobalPage({ workspaceId })).rejects.toMatchObject({ code: "INVALID_RESPONSE", status: 200 });
  });

  it("wraps network failures as retryable and preserves their cause", async () => {
    const cause = new Error("offline");
    vi.stubGlobal("fetch", vi.fn<typeof fetch>().mockRejectedValue(cause));
    await expect(getGraphGlobalPage({ workspaceId })).rejects.toMatchObject({
      code: "NETWORK_ERROR",
      status: null,
      retryable: true,
      cause,
    });
  });

  it("preserves AbortError identity", async () => {
    const abort = new DOMException("aborted", "AbortError");
    vi.stubGlobal("fetch", vi.fn<typeof fetch>().mockRejectedValue(abort));
    await expect(getGraphGlobalPage({ workspaceId })).rejects.toBe(abort);
  });

  it("preserves AbortError identity while reading the response body", async () => {
    const abort = new DOMException("body aborted", "AbortError");
    const response = jsonResponse(globalPayload);
    vi.spyOn(response, "json").mockRejectedValue(abort);
    vi.stubGlobal("fetch", vi.fn<typeof fetch>().mockResolvedValue(response));
    await expect(getGraphGlobalPage({ workspaceId })).rejects.toBe(abort);
  });
});
