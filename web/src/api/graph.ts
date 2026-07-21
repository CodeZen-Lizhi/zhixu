export type NodeType = "TOPIC" | "CLAIM";
export type RelationType =
  | "CITES"
  | "DERIVED_FROM"
  | "BELONGS_TO"
  | "SUPPORTS"
  | "COMPLEMENTS"
  | "DUPLICATES"
  | "CONFLICTS_WITH"
  | "PREREQUISITE_OF"
  | "VERSION_OF"
  | "IMPACTS";
export type RelationStatus = "CONFIRMED" | "STALE";
export type ClaimStatus = "SUGGESTED" | "CONFIRMED" | "DISPUTED" | "SUPERSEDED" | "DEPRECATED" | "INVALID";
export type TopicStatus = "ACTIVE" | "MERGED" | "DEPRECATED";
export type TraversalDirection = "BOTH" | "OUTBOUND" | "INBOUND";
export type EdgeTraversal = "FORWARD" | "REVERSE";
export type GraphConfirmationMethod = "USER_APPROVAL" | "SOURCE_DERIVED";

export interface GraphNodeRef {
  type: NodeType;
  id: string;
}

export interface GraphFilterInput {
  nodeTypes?: NodeType[];
  relationTypes?: RelationType[];
  topicIds?: string[];
  relationStatuses?: RelationStatus[];
  claimStatuses?: ClaimStatus[];
  claimMinConfidence?: number;
  relationMinConfidence?: number;
  updatedAfter?: string;
}

export interface GraphGlobalInput {
  workspaceId: string;
  filter?: GraphFilterInput;
  cursor?: string;
  limit?: number;
}

export interface GraphNeighborhoodInput {
  workspaceId: string;
  center: GraphNodeRef;
  depth?: number;
  limit?: number;
  direction?: TraversalDirection;
  filter?: GraphFilterInput;
  maxNodes?: number;
  maxEdges?: number;
  maxFrontier?: number;
  cursor?: string;
}

export interface GraphPathInput {
  workspaceId: string;
  from: GraphNodeRef;
  to: GraphNodeRef;
  direction?: TraversalDirection;
  relationTypes?: RelationType[];
  maxDepth?: number;
  maxVisited?: number;
}

export interface GraphNodeSearchInput {
  workspaceId: string;
  query: string;
  limit?: number;
}

export interface GraphNodeDetailInput {
  workspaceId: string;
  nodeType: NodeType;
  nodeId: string;
}

export interface GraphRelationDetailInput {
  workspaceId: string;
  relationId: string;
}

export interface GraphRelationEvidenceInput {
  workspaceId: string;
  relationId: string;
  cursor?: string;
  limit?: number;
}

export type GraphCanonicalJsonValue =
  | null
  | boolean
  | number
  | string
  | GraphCanonicalJsonValue[]
  | GraphCanonicalJsonObject;

export interface GraphCanonicalJsonObject {
  readonly [key: string]: GraphCanonicalJsonValue;
}

export interface GraphApplicability {
  schemaVersion: "knowledge-applicability/v1";
  value: GraphCanonicalJsonObject;
  hash: string;
}

interface GraphNodeBase {
  id: string;
  workspaceId: string;
  version: number;
  updatedAt: string;
}

export interface GraphTopicNode extends GraphNodeBase {
  type: "TOPIC";
  name: string;
  description: string;
  topicStatus: TopicStatus;
}

export interface GraphClaimNode extends GraphNodeBase {
  type: "CLAIM";
  statement: string;
  claimStatus: ClaimStatus;
  confidence: number | null;
  applicability: GraphApplicability;
}

export type GraphNode = GraphTopicNode | GraphClaimNode;

export interface GraphEdge {
  relationId: string;
  workspaceId: string;
  source: GraphNodeRef;
  target: GraphNodeRef;
  type: RelationType;
  status: RelationStatus;
  traversal: EdgeTraversal;
  confidence: number | null;
  version: number;
  evidenceCount: number;
  evidenceFingerprint: string;
  evidenceHref: string;
  updatedAt: string;
}

export interface GraphPageMeta {
  fingerprint: string;
  nextCursor?: string;
  complete: boolean;
  truncated: boolean;
  reason?: string;
}

export interface GraphGlobalCluster {
  topic: GraphTopicNode;
  directClaimCount: number;
  incidentRelationCount: number;
  clusterScore: number;
  updatedAt: string;
}

export interface GraphGlobalPage {
  workspaceId: string;
  clusters: GraphGlobalCluster[];
  meta: GraphPageMeta;
}

export interface GraphNeighborhood {
  workspaceId: string;
  center: GraphNodeRef;
  nodes: GraphNode[];
  edges: GraphEdge[];
  boundaryNodes: GraphNodeRef[];
  layerCounts: number[];
  completedDepth: number;
  meta: GraphPageMeta;
}

interface GraphPathBase {
  workspaceId: string;
  from: GraphNodeRef;
  to: GraphNodeRef;
  exploredNodes: number;
}

export interface GraphFoundPath extends GraphPathBase {
  status: "found";
  nodes: GraphNode[];
  edges: GraphEdge[];
  hopCount: number;
  commonTopicSuggestions: [];
}

export interface GraphNotFoundPath extends GraphPathBase {
  status: "not_found";
  nodes: [];
  edges: [];
  hopCount: 0;
  commonTopicSuggestions: GraphTopicNode[];
}

export type GraphPathResult = GraphFoundPath | GraphNotFoundPath;

export interface GraphNodeSearchMatch {
  kind: "EXACT" | "PREFIX";
  node: GraphNode;
}

export interface GraphNodeSearchResult {
  workspaceId: string;
  matches: GraphNodeSearchMatch[];
}

export interface GraphConfirmation {
  method: GraphConfirmationMethod;
  reference: string;
}

export interface GraphRelationDetail {
  edge: GraphEdge;
  confirmation: GraphConfirmation | null;
  fingerprint: string;
  validFrom: string | null;
  validTo: string | null;
  createdAt: string;
  updatedAt: string;
}

export interface GraphProvenance {
  workspaceId: string;
  sourceVersionId: string;
  sourceSpanId: string;
}

export interface GraphRelationEvidenceItem {
  id: string;
  workspaceId: string;
  relationId: string;
  provenance: GraphProvenance;
  reason: string;
  applicability: GraphApplicability;
  confirmation: GraphConfirmation | null;
  sourceHref: string;
  spanHref: string;
  createdAt: string;
}

export interface GraphRelationEvidencePage {
  workspaceId: string;
  relationId: string;
  items: GraphRelationEvidenceItem[];
  meta: GraphPageMeta;
}

export interface GraphProblem {
  errorCode: string;
  message: string;
  retryable: boolean;
  workflowRunId?: string;
  details?: Readonly<Record<string, unknown>>;
}

export class GraphApiError extends Error implements GraphProblem {
  readonly code: string;
  readonly errorCode: string;
  readonly retryable: boolean;
  readonly status: number | null;
  readonly workflowRunId?: string;
  readonly details?: Readonly<Record<string, unknown>>;

  constructor(problem: GraphProblem, status: number | null = null, options?: ErrorOptions) {
    super(problem.message, options);
    this.name = "GraphApiError";
    this.code = problem.errorCode;
    this.errorCode = problem.errorCode;
    this.retryable = problem.retryable;
    this.status = status;
    if (problem.workflowRunId !== undefined) this.workflowRunId = problem.workflowRunId;
    if (problem.details !== undefined) this.details = problem.details;
  }
}

const apiBaseUrl = (import.meta.env.VITE_API_BASE_URL ?? "").replace(/\/$/, "");
const uuidPattern = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;
const hashPattern = /^[0-9a-f]{64}$/;
const rfc3339Pattern = /^\d{4}-(?:0[1-9]|1[0-2])-(?:0[1-9]|[12]\d|3[01])T(?:[01]\d|2[0-3]):[0-5]\d:[0-5]\d(?:\.\d{1,9})?(?:Z|[+-](?:[01]\d|2[0-3]):[0-5]\d)$/;
const controlPattern = /[\u0000-\u001f\u007f-\u009f]/;
const textEncoder = new TextEncoder();
const maxCursorBytes = 2048;
const maxApplicabilityBytes = 16 * 1024;
const maxApplicabilityDepth = 16;

type InvalidFactory = (field: string) => GraphApiError;

const invalidResponse = (field: string): GraphApiError => new GraphApiError({
  errorCode: "INVALID_RESPONSE",
  message: `Graph API 响应字段无效：${field}`,
  retryable: false,
});

const invalidRequest = (field: string): GraphApiError => new GraphApiError({
  errorCode: "INVALID_REQUEST",
  message: `Graph API 请求字段无效：${field}`,
  retryable: false,
});

const isRecord = (value: unknown): value is Record<string, unknown> =>
  typeof value === "object" && value !== null && !Array.isArray(value);

const hasOwn = (value: Record<string, unknown>, key: string): boolean =>
  Object.prototype.hasOwnProperty.call(value, key);

const assertExactKeys = (
  value: Record<string, unknown>,
  allowed: readonly string[],
  field: string,
  fail: InvalidFactory = invalidResponse,
): void => {
  const allowedKeys = new Set(allowed);
  if (Object.keys(value).some((key) => !allowedKeys.has(key))) throw fail(field);
};

const readString = (value: unknown, field: string, fail: InvalidFactory = invalidResponse): string => {
  if (typeof value !== "string" || !validUnicode(value)) throw fail(field);
  return value;
};

const validUnicode = (value: string): boolean => {
  for (let index = 0; index < value.length; index += 1) {
    const code = value.charCodeAt(index);
    if (code >= 0xd800 && code <= 0xdbff) {
      const next = value.charCodeAt(index + 1);
      if (next < 0xdc00 || next > 0xdfff) return false;
      index += 1;
    } else if (code >= 0xdc00 && code <= 0xdfff) {
      return false;
    }
  }
  return true;
};

export const isValidGraphNodeSearchQuery = (query: string): boolean => {
  const bytes = textEncoder.encode(query).length;
  return validUnicode(query) && query.trim() === query && !controlPattern.test(query) && bytes >= 2 && bytes <= 256;
};

const readText = (
  value: unknown,
  field: string,
  maxBytes: number,
  allowEmpty = false,
  fail: InvalidFactory = invalidResponse,
): string => {
  const result = readString(value, field, fail);
  if ((!allowEmpty && result === "") || (result !== "" && result.trim() !== result) || controlPattern.test(result) ||
      textEncoder.encode(result).length > maxBytes) {
    throw fail(field);
  }
  return result;
};

const readBoolean = (value: unknown, field: string, fail: InvalidFactory = invalidResponse): boolean => {
  if (typeof value !== "boolean") throw fail(field);
  return value;
};

const readFiniteNumber = (value: unknown, field: string, fail: InvalidFactory = invalidResponse): number => {
  if (typeof value !== "number" || !Number.isFinite(value)) throw fail(field);
  return Object.is(value, -0) ? 0 : value;
};

const readInteger = (
  value: unknown,
  field: string,
  minimum: number,
  maximum = Number.MAX_SAFE_INTEGER,
  fail: InvalidFactory = invalidResponse,
): number => {
  const result = readFiniteNumber(value, field, fail);
  if (!Number.isSafeInteger(result) || result < minimum || result > maximum) throw fail(field);
  return result;
};

const readConfidence = (
  value: unknown,
  field: string,
  nullable: boolean,
  fail: InvalidFactory = invalidResponse,
): number | null => {
  if (nullable && value === null) return null;
  const result = readFiniteNumber(value, field, fail);
  if (result < 0 || result > 1) throw fail(field);
  return result;
};

const readUuid = (value: unknown, field: string, fail: InvalidFactory = invalidResponse): string => {
  const result = readString(value, field, fail);
  if (!uuidPattern.test(result)) throw fail(field);
  return result;
};

const readHash = (
  value: unknown,
  field: string,
  allowEmpty = false,
  fail: InvalidFactory = invalidResponse,
): string => {
  const result = readString(value, field, fail);
  if ((!allowEmpty || result !== "") && !hashPattern.test(result)) throw fail(field);
  return result;
};

const daysInMonth = (year: number, month: number): number => {
  if (month === 2) {
    const leap = year % 4 === 0 && (year % 100 !== 0 || year % 400 === 0);
    return leap ? 29 : 28;
  }
  return month === 4 || month === 6 || month === 9 || month === 11 ? 30 : 31;
};

const readTimestamp = (value: unknown, field: string, fail: InvalidFactory = invalidResponse): string => {
  const result = readString(value, field, fail);
  if (!rfc3339Pattern.test(result)) throw fail(field);
  const year = Number(result.slice(0, 4));
  const month = Number(result.slice(5, 7));
  const day = Number(result.slice(8, 10));
  if (year < 1 || day > daysInMonth(year, month) || !Number.isFinite(Date.parse(result))) throw fail(field);
  return result;
};

const timestampNanoseconds = (value: string): bigint => {
  const year = Number(value.slice(0, 4));
  const month = Number(value.slice(5, 7));
  const day = Number(value.slice(8, 10));
  const hour = Number(value.slice(11, 13));
  const minute = Number(value.slice(14, 16));
  const second = Number(value.slice(17, 19));
  const zoneStart = value.endsWith("Z") ? value.length - 1 : value.length - 6;
  const fractional = value.slice(19, zoneStart);
  const nanoseconds = fractional === "" ? 0 : Number(fractional.slice(1).padEnd(9, "0"));
  let offsetSeconds = 0;
  if (!value.endsWith("Z")) {
    const sign = value[zoneStart] === "+" ? 1 : -1;
    const offsetHours = Number(value.slice(zoneStart + 1, zoneStart + 3));
    const offsetMinutes = Number(value.slice(zoneStart + 4, zoneStart + 6));
    offsetSeconds = sign * (offsetHours * 60 + offsetMinutes) * 60;
  }
  const utc = new Date(0);
  utc.setUTCFullYear(year, month - 1, day);
  utc.setUTCHours(hour, minute, second, 0);
  const seconds = BigInt(utc.getTime() / 1000 - offsetSeconds);
  return seconds * 1_000_000_000n + BigInt(nanoseconds);
};

const readCursor = (value: unknown, field: string, fail: InvalidFactory = invalidResponse): string => {
  const result = readString(value, field, fail);
  if (result === "" || result.trim() !== result || controlPattern.test(result) ||
      textEncoder.encode(result).length > maxCursorBytes) {
    throw fail(field);
  }
  return result;
};

const readArray = <T>(
  value: unknown,
  field: string,
  maximum: number,
  reader: (item: unknown, itemField: string) => T,
  minimum = 0,
  fail: InvalidFactory = invalidResponse,
): T[] => {
  if (!Array.isArray(value) || value.length < minimum || value.length > maximum) throw fail(field);
  return value.map((item, index) => reader(item, `${field}[${String(index)}]`));
};

const requireUnique = <T>(values: T[], field: string, identity: (value: T) => string, fail: InvalidFactory = invalidResponse): T[] => {
  if (new Set(values.map(identity)).size !== values.length) throw fail(field);
  return values;
};

const readNodeType = (value: unknown, field: string, fail: InvalidFactory = invalidResponse): NodeType => {
  if (value === "TOPIC" || value === "CLAIM") return value;
  throw fail(field);
};

const readRelationType = (value: unknown, field: string, fail: InvalidFactory = invalidResponse): RelationType => {
  switch (value) {
    case "CITES": case "DERIVED_FROM": case "BELONGS_TO": case "SUPPORTS": case "COMPLEMENTS":
    case "DUPLICATES": case "CONFLICTS_WITH": case "PREREQUISITE_OF": case "VERSION_OF": case "IMPACTS":
      return value;
    default:
      throw fail(field);
  }
};

const readRelationStatus = (value: unknown, field: string, fail: InvalidFactory = invalidResponse): RelationStatus => {
  if (value === "CONFIRMED" || value === "STALE") return value;
  throw fail(field);
};

const readClaimStatus = (value: unknown, field: string, fail: InvalidFactory = invalidResponse): ClaimStatus => {
  switch (value) {
    case "SUGGESTED": case "CONFIRMED": case "DISPUTED": case "SUPERSEDED": case "DEPRECATED": case "INVALID":
      return value;
    default:
      throw fail(field);
  }
};

const readFilterClaimStatus = (value: unknown, field: string, fail: InvalidFactory): ClaimStatus => {
  if (value === "CONFIRMED" || value === "DISPUTED") return value;
  throw fail(field);
};

const readTopicStatus = (value: unknown, field: string): TopicStatus => {
  if (value === "ACTIVE" || value === "MERGED" || value === "DEPRECATED") return value;
  throw invalidResponse(field);
};

const readDirection = (value: unknown, field: string, fail: InvalidFactory = invalidResponse): TraversalDirection => {
  if (value === "BOTH" || value === "OUTBOUND" || value === "INBOUND") return value;
  throw fail(field);
};

const readTraversal = (value: unknown, field: string): EdgeTraversal => {
  if (value === "FORWARD" || value === "REVERSE") return value;
  throw invalidResponse(field);
};

export const graphNodeRefIdentity = (value: GraphNodeRef): string => `${value.type}\u0000${value.id}`;
const sameRef = (left: GraphNodeRef, right: GraphNodeRef): boolean => left.type === right.type && left.id === right.id;

const compareStrings = (left: string, right: string): number => left < right ? -1 : left > right ? 1 : 0;

const assertStableOrder = <T>(
  values: T[],
  field: string,
  compare: (left: T, right: T) => number,
): void => {
  for (let index = 1; index < values.length; index += 1) {
    const previous = values[index - 1];
    const current = values[index];
    if (previous === undefined || current === undefined || compare(previous, current) >= 0) throw invalidResponse(field);
  }
};

const readNodeRef = (value: unknown, field: string, fail: InvalidFactory = invalidResponse): GraphNodeRef => {
  if (!isRecord(value)) throw fail(field);
  assertExactKeys(value, ["type", "id"], field, fail);
  return {
    type: readNodeType(value.type, `${field}.type`, fail),
    id: readUuid(value.id, `${field}.id`, fail),
  };
};

interface JsonBudget {
  nodes: number;
  ancestors: Set<object>;
}

const readCanonicalJsonValue = (
  value: unknown,
  field: string,
  depth: number,
  budget: JsonBudget,
): GraphCanonicalJsonValue => {
  budget.nodes += 1;
  if (budget.nodes > maxApplicabilityBytes) throw invalidResponse(field);
  if (value === null || typeof value === "boolean") return value;
  if (typeof value === "string") {
    if (!validUnicode(value)) throw invalidResponse(field);
    return value;
  }
  if (typeof value === "number") return readFiniteNumber(value, field);
  if (Array.isArray(value)) {
    if (depth > maxApplicabilityDepth || budget.ancestors.has(value)) throw invalidResponse(field);
    budget.ancestors.add(value);
    const result = value.map((item, index) => readCanonicalJsonValue(item, `${field}[${String(index)}]`, depth + 1, budget));
    budget.ancestors.delete(value);
    return result;
  }
  if (isRecord(value)) {
    if (depth > maxApplicabilityDepth || budget.ancestors.has(value)) throw invalidResponse(field);
    budget.ancestors.add(value);
    const result: GraphCanonicalJsonObject = {};
    for (const [key, item] of Object.entries(value)) {
      if (!validUnicode(key)) throw invalidResponse(field);
      Object.defineProperty(result, key, {
        value: readCanonicalJsonValue(item, `${field}.${key}`, depth + 1, budget),
        enumerable: true,
        configurable: true,
        writable: true,
      });
    }
    budget.ancestors.delete(value);
    return result;
  }
  throw invalidResponse(field);
};

const readApplicability = (value: unknown, field: string): GraphApplicability => {
  if (!isRecord(value) || !isRecord(value.value)) throw invalidResponse(field);
  assertExactKeys(value, ["schema_version", "value", "hash"], field);
  if (value.schema_version !== "knowledge-applicability/v1") throw invalidResponse(`${field}.schema_version`);
  const decoded = readCanonicalJsonValue(value.value, `${field}.value`, 1, { nodes: 0, ancestors: new Set<object>() });
  if (!isRecord(decoded)) throw invalidResponse(`${field}.value`);
  const encoded = JSON.stringify(decoded);
  if (textEncoder.encode(encoded).length > maxApplicabilityBytes) {
    throw invalidResponse(`${field}.value`);
  }
  return {
    schemaVersion: "knowledge-applicability/v1",
    value: decoded,
    hash: readHash(value.hash, `${field}.hash`),
  };
};

const decodeNode = (value: unknown, field: string, expectedWorkspaceId?: string): GraphNode => {
  if (!isRecord(value)) throw invalidResponse(field);
  const type = readNodeType(value.type, `${field}.type`);
  if (type === "TOPIC") {
    assertExactKeys(value, ["type", "id", "workspace_id", "name", "description", "topic_status", "version", "updated_at"], field);
    const node: GraphTopicNode = {
      type,
      id: readUuid(value.id, `${field}.id`),
      workspaceId: readUuid(value.workspace_id, `${field}.workspace_id`),
      name: readText(value.name, `${field}.name`, 256),
      description: readText(value.description, `${field}.description`, 4096, true),
      topicStatus: readTopicStatus(value.topic_status, `${field}.topic_status`),
      version: readInteger(value.version, `${field}.version`, 1),
      updatedAt: readTimestamp(value.updated_at, `${field}.updated_at`),
    };
    if (expectedWorkspaceId !== undefined && node.workspaceId !== expectedWorkspaceId) throw invalidResponse(`${field}.workspace_id`);
    return node;
  }
  assertExactKeys(value, ["type", "id", "workspace_id", "statement", "claim_status", "confidence", "applicability", "version", "updated_at"], field);
  const node: GraphClaimNode = {
    type,
    id: readUuid(value.id, `${field}.id`),
    workspaceId: readUuid(value.workspace_id, `${field}.workspace_id`),
    statement: readText(value.statement, `${field}.statement`, 512),
    claimStatus: readClaimStatus(value.claim_status, `${field}.claim_status`),
    confidence: readConfidence(value.confidence, `${field}.confidence`, true),
    applicability: readApplicability(value.applicability, `${field}.applicability`),
    version: readInteger(value.version, `${field}.version`, 1),
    updatedAt: readTimestamp(value.updated_at, `${field}.updated_at`),
  };
  if (expectedWorkspaceId !== undefined && node.workspaceId !== expectedWorkspaceId) throw invalidResponse(`${field}.workspace_id`);
  return node;
};

export const decodeGraphNode = (value: unknown, expected?: GraphNodeDetailInput): GraphNode => {
  const node = decodeNode(value, "node", expected?.workspaceId);
  if (expected !== undefined && (node.type !== expected.nodeType || node.id !== expected.nodeId)) throw invalidResponse("node.identity");
  return node;
};

export const graphRelationTypeCompatible = (type: RelationType, source: NodeType, target: NodeType): boolean => {
  switch (type) {
    case "CITES": case "DERIVED_FROM": case "SUPPORTS": case "CONFLICTS_WITH":
      return source === "CLAIM" && target === "CLAIM";
    case "BELONGS_TO":
      return source === "CLAIM" && target === "TOPIC";
    case "COMPLEMENTS": case "DUPLICATES": case "PREREQUISITE_OF": case "VERSION_OF":
      return source === target;
    case "IMPACTS":
      return true;
  }
};

export const isSymmetricGraphRelationType = (type: RelationType): boolean => type === "DUPLICATES" || type === "CONFLICTS_WITH";

const decodeEdge = (value: unknown, field: string, expectedWorkspaceId?: string): GraphEdge => {
  if (!isRecord(value)) throw invalidResponse(field);
  assertExactKeys(value, [
    "relation_id", "workspace_id", "source", "target", "type", "status", "traversal", "confidence", "version",
    "evidence_count", "evidence_fingerprint", "evidence_href", "updated_at",
  ], field);
  const workspaceId = readUuid(value.workspace_id, `${field}.workspace_id`);
  const relationId = readUuid(value.relation_id, `${field}.relation_id`);
  const source = readNodeRef(value.source, `${field}.source`);
  const target = readNodeRef(value.target, `${field}.target`);
  const type = readRelationType(value.type, `${field}.type`);
  const evidenceCount = readInteger(value.evidence_count, `${field}.evidence_count`, 0);
  const evidenceFingerprint = readHash(value.evidence_fingerprint, `${field}.evidence_fingerprint`, true);
  const evidenceHref = readString(value.evidence_href, `${field}.evidence_href`);
  const expectedHref = `/api/v1/graph/relations/${relationId}/evidence?workspace_id=${workspaceId}`;
  if (expectedWorkspaceId !== undefined && workspaceId !== expectedWorkspaceId ||
      sameRef(source, target) || !graphRelationTypeCompatible(type, source.type, target.type) ||
      isSymmetricGraphRelationType(type) && graphNodeRefIdentity(source) > graphNodeRefIdentity(target) ||
      evidenceCount === 0 && evidenceFingerprint !== "" || evidenceCount > 0 && !hashPattern.test(evidenceFingerprint) ||
      evidenceHref !== expectedHref) {
    throw invalidResponse(field);
  }
  return {
    relationId,
    workspaceId,
    source,
    target,
    type,
    status: readRelationStatus(value.status, `${field}.status`),
    traversal: readTraversal(value.traversal, `${field}.traversal`),
    confidence: readConfidence(value.confidence, `${field}.confidence`, true),
    version: readInteger(value.version, `${field}.version`, 1),
    evidenceCount,
    evidenceFingerprint,
    evidenceHref,
    updatedAt: readTimestamp(value.updated_at, `${field}.updated_at`),
  };
};

const decodePageMeta = (value: unknown, field: string): GraphPageMeta => {
  if (!isRecord(value)) throw invalidResponse(field);
  assertExactKeys(value, ["fingerprint", "next_cursor", "complete", "truncated", "reason"], field);
  const complete = readBoolean(value.complete, `${field}.complete`);
  const truncated = readBoolean(value.truncated, `${field}.truncated`);
  const nextCursor = value.next_cursor === undefined && !hasOwn(value, "next_cursor")
    ? undefined
    : readCursor(value.next_cursor, `${field}.next_cursor`);
  const reason = value.reason === undefined && !hasOwn(value, "reason")
    ? undefined
    : readText(value.reason, `${field}.reason`, 128);
  if (truncated !== (reason !== undefined) || complete && (truncated || nextCursor !== undefined) ||
      !complete && !truncated && nextCursor === undefined) {
    throw invalidResponse(field);
  }
  return {
    fingerprint: readHash(value.fingerprint, `${field}.fingerprint`),
    ...(nextCursor === undefined ? {} : { nextCursor }),
    complete,
    truncated,
    ...(reason === undefined ? {} : { reason }),
  };
};

const assertUniqueNodes = (nodes: GraphNode[], field: string): void => {
  requireUnique(nodes, field, (node) => graphNodeRefIdentity(node));
};

const assertUniqueEdges = (edges: GraphEdge[], field: string): void => {
  requireUnique(edges, field, (edge) => edge.relationId);
};

const edgeOrderKey = (edge: GraphEdge): string =>
  `${edge.type}\u0000${graphNodeRefIdentity(edge.source)}\u0000${graphNodeRefIdentity(edge.target)}\u0000${edge.relationId}`;

const isFormalNode = (node: GraphNode): boolean =>
  node.type === "TOPIC" ? node.topicStatus === "ACTIVE" : node.claimStatus === "CONFIRMED" || node.claimStatus === "DISPUTED";

const validateNodeFilter = (node: GraphNode, filter: GraphFilterInput | undefined, field: string): void => {
  if (!isFormalNode(node)) throw invalidResponse(field);
  if (filter === undefined) return;
  if (filter.nodeTypes !== undefined && filter.nodeTypes.length > 0 && !filter.nodeTypes.includes(node.type)) throw invalidResponse(field);
  if (node.type === "TOPIC" && filter.topicIds !== undefined && filter.topicIds.length > 0 && !filter.topicIds.includes(node.id)) {
    throw invalidResponse(field);
  }
  if (node.type === "CLAIM") {
    if (filter.claimStatuses !== undefined && filter.claimStatuses.length > 0 && !filter.claimStatuses.includes(node.claimStatus)) {
      throw invalidResponse(field);
    }
    if (filter.claimMinConfidence !== undefined && (node.confidence === null || node.confidence < filter.claimMinConfidence)) {
      throw invalidResponse(field);
    }
  }
  if (filter.updatedAfter !== undefined && timestampNanoseconds(node.updatedAt) <= timestampNanoseconds(filter.updatedAfter)) throw invalidResponse(field);
};

const validateEdgeFilter = (
  edge: GraphEdge,
  filter: GraphFilterInput | undefined,
  direction: TraversalDirection,
  field: string,
): void => {
  const statuses: RelationStatus[] = filter?.relationStatuses === undefined || filter.relationStatuses.length === 0
    ? ["CONFIRMED"]
    : filter.relationStatuses;
  if (!statuses.includes(edge.status) ||
      filter?.relationTypes !== undefined && filter.relationTypes.length > 0 && !filter.relationTypes.includes(edge.type) ||
      filter?.relationMinConfidence !== undefined && (edge.confidence === null || edge.confidence < filter.relationMinConfidence) ||
      filter?.updatedAfter !== undefined && timestampNanoseconds(edge.updatedAt) <= timestampNanoseconds(filter.updatedAfter) ||
      !isSymmetricGraphRelationType(edge.type) && direction === "OUTBOUND" && edge.traversal === "REVERSE" ||
      !isSymmetricGraphRelationType(edge.type) && direction === "INBOUND" && edge.traversal === "FORWARD") {
    throw invalidResponse(field);
  }
};

export const decodeGraphGlobalPage = (value: unknown, expected?: GraphGlobalInput): GraphGlobalPage => {
  if (!isRecord(value)) throw invalidResponse("response");
  assertExactKeys(value, ["workspace_id", "clusters", "meta"], "response");
  const workspaceId = readUuid(value.workspace_id, "workspace_id");
  const clusters = readArray(value.clusters, "clusters", 100, (item, field): GraphGlobalCluster => {
    if (!isRecord(item)) throw invalidResponse(field);
    assertExactKeys(item, ["topic", "direct_claim_count", "incident_relation_count", "cluster_score", "updated_at"], field);
    const topic = decodeNode(item.topic, `${field}.topic`, workspaceId);
    if (topic.type !== "TOPIC" || topic.topicStatus !== "ACTIVE") throw invalidResponse(`${field}.topic`);
    const directClaimCount = readInteger(item.direct_claim_count, `${field}.direct_claim_count`, 0);
    const incidentRelationCount = readInteger(item.incident_relation_count, `${field}.incident_relation_count`, 0);
    const clusterScore = readInteger(item.cluster_score, `${field}.cluster_score`, 0);
    if (clusterScore !== directClaimCount + incidentRelationCount) throw invalidResponse(`${field}.cluster_score`);
    return { topic, directClaimCount, incidentRelationCount, clusterScore, updatedAt: readTimestamp(item.updated_at, `${field}.updated_at`) };
  });
  requireUnique(clusters, "clusters", (cluster) => cluster.topic.id);
  assertStableOrder(clusters, "clusters.order", (left, right) => {
    if (left.clusterScore !== right.clusterScore) return right.clusterScore - left.clusterScore;
    const leftUpdated = timestampNanoseconds(left.updatedAt);
    const rightUpdated = timestampNanoseconds(right.updatedAt);
    if (leftUpdated !== rightUpdated) return leftUpdated > rightUpdated ? -1 : 1;
    return compareStrings(left.topic.id, right.topic.id);
  });
  if (expected !== undefined) {
    const limit = expected.limit ?? 25;
    if (workspaceId !== expected.workspaceId || clusters.length > limit ||
        expected.filter?.nodeTypes !== undefined && expected.filter.nodeTypes.length > 0 && !expected.filter.nodeTypes.includes("TOPIC")) {
      throw invalidResponse("response.binding");
    }
    for (const cluster of clusters) {
      if (expected.filter?.topicIds !== undefined && expected.filter.topicIds.length > 0 && !expected.filter.topicIds.includes(cluster.topic.id) ||
          expected.filter?.updatedAfter !== undefined && timestampNanoseconds(cluster.updatedAt) <= timestampNanoseconds(expected.filter.updatedAfter)) {
        throw invalidResponse("clusters.filter");
      }
    }
  }
  return { workspaceId, clusters, meta: decodePageMeta(value.meta, "meta") };
};

export const decodeGraphNodeSearchResult = (value: unknown, expected?: GraphNodeSearchInput): GraphNodeSearchResult => {
  if (!isRecord(value)) throw invalidResponse("response");
  assertExactKeys(value, ["workspace_id", "matches"], "response");
  const workspaceId = readUuid(value.workspace_id, "workspace_id");
  const matches = readArray(value.matches, "matches", 50, (item, field): GraphNodeSearchMatch => {
    if (!isRecord(item)) throw invalidResponse(field);
    assertExactKeys(item, ["kind", "node"], field);
    if (item.kind !== "EXACT" && item.kind !== "PREFIX") throw invalidResponse(`${field}.kind`);
    const node = decodeNode(item.node, `${field}.node`, workspaceId);
    if (!isFormalNode(node)) throw invalidResponse(`${field}.node`);
    return { kind: item.kind, node };
  });
  requireUnique(matches, "matches", (match) => graphNodeRefIdentity(match.node));
  assertStableOrder(matches, "matches.order", (left, right) => {
    const leftRank = left.kind === "EXACT" ? 0 : 1;
    const rightRank = right.kind === "EXACT" ? 0 : 1;
    return leftRank === rightRank ? compareStrings(graphNodeRefIdentity(left.node), graphNodeRefIdentity(right.node)) : leftRank - rightRank;
  });
  if (expected !== undefined && (workspaceId !== expected.workspaceId || matches.length > (expected.limit ?? 20))) {
    throw invalidResponse("response.binding");
  }
  return { workspaceId, matches };
};

export const decodeGraphNeighborhood = (value: unknown, expected?: GraphNeighborhoodInput): GraphNeighborhood => {
  if (!isRecord(value)) throw invalidResponse("response");
  assertExactKeys(value, ["workspace_id", "center", "nodes", "edges", "boundary_nodes", "layer_counts", "completed_depth", "meta"], "response");
  const workspaceId = readUuid(value.workspace_id, "workspace_id");
  const center = readNodeRef(value.center, "center");
  const nodes = readArray(value.nodes, "nodes", 500, (item, field) => decodeNode(item, field, workspaceId));
  const edges = readArray(value.edges, "edges", 1000, (item, field) => decodeEdge(item, field, workspaceId));
  const boundaryNodes = readArray(value.boundary_nodes, "boundary_nodes", 2000, (item, field) => readNodeRef(item, field));
  const layerCounts = readArray(value.layer_counts, "layer_counts", 3, (item, field) => readInteger(item, field, 0));
  const completedDepth = readInteger(value.completed_depth, "completed_depth", 0, 3);
  const meta = decodePageMeta(value.meta, "meta");
  assertUniqueNodes(nodes, "nodes");
  assertUniqueEdges(edges, "edges");
  assertStableOrder(nodes, "nodes.order", (left, right) => compareStrings(graphNodeRefIdentity(left), graphNodeRefIdentity(right)));
  assertStableOrder(edges, "edges.order", (left, right) => compareStrings(edgeOrderKey(left), edgeOrderKey(right)));
  requireUnique(boundaryNodes, "boundary_nodes", graphNodeRefIdentity);
  const hydrated = new Set(nodes.map((node) => graphNodeRefIdentity(node)));
  const boundary = new Set(boundaryNodes.map(graphNodeRefIdentity));
  if (!hydrated.has(graphNodeRefIdentity(center)) || layerCounts.length !== completedDepth ||
      layerCounts.reduce((sum, count) => sum + count, 0) !== nodes.length - 1 ||
      boundaryNodes.some((node) => hydrated.has(graphNodeRefIdentity(node)))) {
    throw invalidResponse("response.closure");
  }
  const referencedBoundary = new Set<string>();
  for (const edge of edges) {
    for (const endpoint of [edge.source, edge.target]) {
      const identity = graphNodeRefIdentity(endpoint);
      if (!hydrated.has(identity) && !boundary.has(identity)) throw invalidResponse("edges.closure");
      if (boundary.has(identity)) referencedBoundary.add(identity);
    }
  }
  if (boundaryNodes.some((node) => !referencedBoundary.has(graphNodeRefIdentity(node)))) throw invalidResponse("boundary_nodes.closure");
  for (const node of nodes) validateNodeFilter(node, expected?.filter, "nodes.filter");
  const direction = expected?.direction ?? "BOTH";
  for (const edge of edges) validateEdgeFilter(edge, expected?.filter, direction, "edges.filter");
  const requestedDepth = expected?.depth ?? 1;
  if (expected !== undefined && (workspaceId !== expected.workspaceId || !sameRef(center, expected.center) ||
      completedDepth > requestedDepth || nodes.length > (expected.maxNodes ?? 500) ||
      edges.length > (expected.maxEdges ?? 1000) || requestedDepth === 1 && edges.length > (expected.limit ?? 25) ||
      requestedDepth !== 1 && meta.nextCursor !== undefined || meta.complete && completedDepth !== requestedDepth)) {
    throw invalidResponse("response.binding");
  }
  return { workspaceId, center, nodes, edges, boundaryNodes, layerCounts, completedDepth, meta };
};

export const decodeGraphPathResult = (value: unknown, expected?: GraphPathInput): GraphPathResult => {
  if (!isRecord(value)) throw invalidResponse("response");
  assertExactKeys(value, ["workspace_id", "from", "to", "status", "nodes", "edges", "hop_count", "explored_nodes", "common_topic_suggestions"], "response");
  const workspaceId = readUuid(value.workspace_id, "workspace_id");
  const from = readNodeRef(value.from, "from");
  const to = readNodeRef(value.to, "to");
  if (sameRef(from, to)) throw invalidResponse("path.endpoints");
  const status = value.status;
  if (status !== "found" && status !== "not_found") throw invalidResponse("status");
  const nodes = readArray(value.nodes, "nodes", 9, (item, field) => decodeNode(item, field, workspaceId));
  const edges = readArray(value.edges, "edges", 8, (item, field) => decodeEdge(item, field, workspaceId));
  const hopCount = readInteger(value.hop_count, "hop_count", 0, 8);
  const exploredNodes = readInteger(value.explored_nodes, "explored_nodes", 0, 500);
  const suggestions = readArray(value.common_topic_suggestions, "common_topic_suggestions", 5, (item, field) => {
    const node = decodeNode(item, field, workspaceId);
    if (node.type !== "TOPIC" || node.topicStatus !== "ACTIVE") throw invalidResponse(field);
    return node;
  });
  requireUnique(suggestions, "common_topic_suggestions", (topic) => topic.id);
  assertStableOrder(suggestions, "common_topic_suggestions.order", (left, right) => compareStrings(left.id, right.id));
  if (expected !== undefined && (workspaceId !== expected.workspaceId || !sameRef(from, expected.from) || !sameRef(to, expected.to) ||
      exploredNodes > (expected.maxVisited ?? 500))) {
    throw invalidResponse("response.binding");
  }
  if (status === "not_found") {
    if (nodes.length !== 0 || edges.length !== 0 || hopCount !== 0) throw invalidResponse("response.not_found");
    return { workspaceId, from, to, status, nodes: [], edges: [], hopCount: 0, exploredNodes, commonTopicSuggestions: suggestions };
  }
  const maximumDepth = expected === undefined ? 8 : expected.maxDepth ?? 6;
  if (hopCount < 1 || hopCount > maximumDepth || nodes.length !== hopCount + 1 || edges.length !== hopCount || suggestions.length !== 0) {
    throw invalidResponse("response.found");
  }
  assertUniqueNodes(nodes, "nodes");
  assertUniqueEdges(edges, "edges");
  if (!sameRef(nodes[0] ?? { type: "TOPIC", id: "" }, from) || !sameRef(nodes[nodes.length - 1] ?? { type: "TOPIC", id: "" }, to)) {
    throw invalidResponse("path.endpoints");
  }
  for (const node of nodes) {
    if (!isFormalNode(node)) throw invalidResponse("nodes.status");
  }
  for (let index = 0; index < edges.length; index += 1) {
    const edge = edges[index];
    const current = nodes[index];
    const next = nodes[index + 1];
    if (edge === undefined || current === undefined || next === undefined || edge.status !== "CONFIRMED") throw invalidResponse("edges.path");
    const traversedFrom = edge.traversal === "REVERSE" ? edge.target : edge.source;
    const traversedTo = edge.traversal === "REVERSE" ? edge.source : edge.target;
    if (!sameRef(current, traversedFrom) || !sameRef(next, traversedTo) ||
        expected?.relationTypes !== undefined && expected.relationTypes.length > 0 && !expected.relationTypes.includes(edge.type) ||
        !isSymmetricGraphRelationType(edge.type) && expected?.direction === "OUTBOUND" && edge.traversal === "REVERSE" ||
        !isSymmetricGraphRelationType(edge.type) && expected?.direction === "INBOUND" && edge.traversal === "FORWARD") {
      throw invalidResponse("edges.path");
    }
  }
  return { workspaceId, from, to, status, nodes, edges, hopCount, exploredNodes, commonTopicSuggestions: [] };
};

const decodeConfirmation = (value: unknown, field: string): GraphConfirmation | null => {
  if (value === null) return null;
  if (!isRecord(value)) throw invalidResponse(field);
  assertExactKeys(value, ["method", "reference"], field);
  if (value.method !== "USER_APPROVAL" && value.method !== "SOURCE_DERIVED") throw invalidResponse(`${field}.method`);
  return { method: value.method, reference: readText(value.reference, `${field}.reference`, 512) };
};

export const decodeGraphRelationDetail = (value: unknown, expected?: GraphRelationDetailInput): GraphRelationDetail => {
  if (!isRecord(value)) throw invalidResponse("response");
  assertExactKeys(value, ["edge", "confirmation", "fingerprint", "valid_from", "valid_to", "created_at", "updated_at"], "response");
  const edge = decodeEdge(value.edge, "edge", expected?.workspaceId);
  const validFrom = value.valid_from === null ? null : readTimestamp(value.valid_from, "valid_from");
  const validTo = value.valid_to === null ? null : readTimestamp(value.valid_to, "valid_to");
  const createdAt = readTimestamp(value.created_at, "created_at");
  const updatedAt = readTimestamp(value.updated_at, "updated_at");
  if (expected !== undefined && edge.relationId !== expected.relationId || timestampNanoseconds(updatedAt) < timestampNanoseconds(createdAt) ||
      validTo !== null && (validFrom === null || timestampNanoseconds(validTo) <= timestampNanoseconds(validFrom))) {
    throw invalidResponse("response.binding");
  }
  return {
    edge,
    confirmation: decodeConfirmation(value.confirmation, "confirmation"),
    fingerprint: readHash(value.fingerprint, "fingerprint"),
    validFrom,
    validTo,
    createdAt,
    updatedAt,
  };
};

const decodeEvidenceItem = (
  value: unknown,
  field: string,
  workspaceId: string,
  relationId: string,
): GraphRelationEvidenceItem => {
  if (!isRecord(value)) throw invalidResponse(field);
  assertExactKeys(value, [
    "id", "workspace_id", "relation_id", "provenance", "reason", "applicability", "confirmation", "source_href", "span_href", "created_at",
  ], field);
  if (!isRecord(value.provenance)) throw invalidResponse(`${field}.provenance`);
  assertExactKeys(value.provenance, ["workspace_id", "source_version_id", "source_span_id"], `${field}.provenance`);
  const id = readUuid(value.id, `${field}.id`);
  const itemWorkspaceId = readUuid(value.workspace_id, `${field}.workspace_id`);
  const itemRelationId = readUuid(value.relation_id, `${field}.relation_id`);
  const provenance: GraphProvenance = {
    workspaceId: readUuid(value.provenance.workspace_id, `${field}.provenance.workspace_id`),
    sourceVersionId: readUuid(value.provenance.source_version_id, `${field}.provenance.source_version_id`),
    sourceSpanId: readUuid(value.provenance.source_span_id, `${field}.provenance.source_span_id`),
  };
  const sourceHref = readString(value.source_href, `${field}.source_href`);
  const spanHref = readString(value.span_href, `${field}.span_href`);
  const expectedSourceHref = `/api/v1/workspaces/${workspaceId}/source-versions/${provenance.sourceVersionId}`;
  if (itemWorkspaceId !== workspaceId || itemRelationId !== relationId || id === relationId ||
      provenance.workspaceId !== workspaceId || provenance.workspaceId === provenance.sourceVersionId ||
      provenance.workspaceId === provenance.sourceSpanId || provenance.sourceVersionId === provenance.sourceSpanId ||
      sourceHref !== expectedSourceHref || spanHref !== `${expectedSourceHref}/spans/${provenance.sourceSpanId}`) {
    throw invalidResponse(field);
  }
  return {
    id,
    workspaceId: itemWorkspaceId,
    relationId: itemRelationId,
    provenance,
    reason: readText(value.reason, `${field}.reason`, 4096),
    applicability: readApplicability(value.applicability, `${field}.applicability`),
    confirmation: decodeConfirmation(value.confirmation, `${field}.confirmation`),
    sourceHref,
    spanHref,
    createdAt: readTimestamp(value.created_at, `${field}.created_at`),
  };
};

export const decodeGraphRelationEvidencePage = (value: unknown, expected?: GraphRelationEvidenceInput): GraphRelationEvidencePage => {
  if (!isRecord(value)) throw invalidResponse("response");
  assertExactKeys(value, ["workspace_id", "relation_id", "items", "meta"], "response");
  const workspaceId = readUuid(value.workspace_id, "workspace_id");
  const relationId = readUuid(value.relation_id, "relation_id");
  const items = readArray(value.items, "items", 100, (item, field) => decodeEvidenceItem(item, field, workspaceId, relationId));
  requireUnique(items, "items", (item) => item.id);
  assertStableOrder(items, "items.order", (left, right) => {
    const leftCreated = timestampNanoseconds(left.createdAt);
    const rightCreated = timestampNanoseconds(right.createdAt);
    return leftCreated === rightCreated ? compareStrings(left.id, right.id) : leftCreated < rightCreated ? -1 : 1;
  });
  if (expected !== undefined && (workspaceId !== expected.workspaceId || relationId !== expected.relationId ||
      items.length > (expected.limit ?? 20))) {
    throw invalidResponse("response.binding");
  }
  return { workspaceId, relationId, items, meta: decodePageMeta(value.meta, "meta") };
};

const inputRecord = (input: unknown, allowed: readonly string[], field: string): Record<string, unknown> => {
  if (!isRecord(input)) throw invalidRequest(field);
  assertExactKeys(input, allowed, field, invalidRequest);
  return input;
};

const readInputArray = <T>(
  value: unknown,
  field: string,
  maximum: number,
  reader: (item: unknown, itemField: string) => T,
): T[] => requireUnique(readArray(value, field, maximum, reader, 0, invalidRequest), field, (item) => String(item), invalidRequest);

const encodeFilter = (input: unknown): Record<string, unknown> => {
  const value = inputRecord(input, [
    "nodeTypes", "relationTypes", "topicIds", "relationStatuses", "claimStatuses", "claimMinConfidence",
    "relationMinConfidence", "updatedAfter",
  ], "filter");
  const nodeTypes = value.nodeTypes === undefined ? undefined : readInputArray(value.nodeTypes, "filter.nodeTypes", 2, (item, field) => readNodeType(item, field, invalidRequest));
  const relationTypes = value.relationTypes === undefined ? undefined : readInputArray(value.relationTypes, "filter.relationTypes", 10, (item, field) => readRelationType(item, field, invalidRequest));
  const topicIds = value.topicIds === undefined ? undefined : readInputArray(value.topicIds, "filter.topicIds", 500, (item, field) => readUuid(item, field, invalidRequest));
  const relationStatuses = value.relationStatuses === undefined ? undefined : readInputArray(value.relationStatuses, "filter.relationStatuses", 2, (item, field) => readRelationStatus(item, field, invalidRequest));
  const claimStatuses = value.claimStatuses === undefined ? undefined : readInputArray(value.claimStatuses, "filter.claimStatuses", 2, (item, field) => readFilterClaimStatus(item, field, invalidRequest));
  const claimMinConfidence = value.claimMinConfidence === undefined ? undefined : readConfidence(value.claimMinConfidence, "filter.claimMinConfidence", false, invalidRequest);
  const relationMinConfidence = value.relationMinConfidence === undefined ? undefined : readConfidence(value.relationMinConfidence, "filter.relationMinConfidence", false, invalidRequest);
  const updatedAfter = value.updatedAfter === undefined ? undefined : readTimestamp(value.updatedAfter, "filter.updatedAfter", invalidRequest);
  return {
    ...(nodeTypes === undefined ? {} : { node_types: nodeTypes }),
    ...(relationTypes === undefined ? {} : { relation_types: relationTypes }),
    ...(topicIds === undefined ? {} : { topic_ids: topicIds }),
    ...(relationStatuses === undefined ? {} : { relation_statuses: relationStatuses }),
    ...(claimStatuses === undefined ? {} : { claim_statuses: claimStatuses }),
    ...(claimMinConfidence === undefined ? {} : { claim_min_confidence: claimMinConfidence }),
    ...(relationMinConfidence === undefined ? {} : { relation_min_confidence: relationMinConfidence }),
    ...(updatedAfter === undefined ? {} : { updated_after: updatedAfter }),
  };
};

const encodeNodeRef = (input: unknown, field: string): { ref: GraphNodeRef; wire: Record<string, unknown> } => {
  const value = inputRecord(input, ["type", "id"], field);
  const ref: GraphNodeRef = {
    type: readNodeType(value.type, `${field}.type`, invalidRequest),
    id: readUuid(value.id, `${field}.id`, invalidRequest),
  };
  return { ref, wire: { type: ref.type, id: ref.id } };
};

const optionalLimit = (value: unknown, field: string, maximum: number): number | undefined =>
  value === undefined ? undefined : readInteger(value, field, 1, maximum, invalidRequest);

const optionalCursor = (value: unknown, field: string): string | undefined =>
  value === undefined ? undefined : readCursor(value, field, invalidRequest);

const encodeGlobalInput = (input: GraphGlobalInput): Record<string, unknown> => {
  const value = inputRecord(input, ["workspaceId", "filter", "cursor", "limit"], "input");
  const filter = value.filter === undefined ? undefined : encodeFilter(value.filter);
  const cursor = optionalCursor(value.cursor, "cursor");
  const limit = optionalLimit(value.limit, "limit", 100);
  return {
    workspace_id: readUuid(value.workspaceId, "workspaceId", invalidRequest),
    ...(filter === undefined ? {} : { filter }),
    ...(cursor === undefined ? {} : { cursor }),
    ...(limit === undefined ? {} : { limit }),
  };
};

const encodeNeighborhoodInput = (input: GraphNeighborhoodInput): Record<string, unknown> => {
  const value = inputRecord(input, [
    "workspaceId", "center", "depth", "limit", "direction", "filter", "maxNodes", "maxEdges", "maxFrontier", "cursor",
  ], "input");
  const center = encodeNodeRef(value.center, "center");
  const depth = value.depth === undefined ? undefined : readInteger(value.depth, "depth", 1, 3, invalidRequest);
  const limit = optionalLimit(value.limit, "limit", 100);
  const direction = value.direction === undefined ? undefined : readDirection(value.direction, "direction", invalidRequest);
  const filter = value.filter === undefined ? undefined : encodeFilter(value.filter);
  const maxNodes = optionalLimit(value.maxNodes, "maxNodes", 500);
  const maxEdges = optionalLimit(value.maxEdges, "maxEdges", 1000);
  const maxFrontier = optionalLimit(value.maxFrontier, "maxFrontier", 500);
  const cursor = optionalCursor(value.cursor, "cursor");
  if ((depth ?? 1) !== 1 && cursor !== undefined ||
      input.filter?.nodeTypes !== undefined && input.filter.nodeTypes.length > 0 && !input.filter.nodeTypes.includes(center.ref.type) ||
      center.ref.type === "TOPIC" && input.filter?.topicIds !== undefined && input.filter.topicIds.length > 0 && !input.filter.topicIds.includes(center.ref.id)) {
    throw invalidRequest("input");
  }
  return {
    workspace_id: readUuid(value.workspaceId, "workspaceId", invalidRequest),
    center: center.wire,
    ...(depth === undefined ? {} : { depth }),
    ...(limit === undefined ? {} : { limit }),
    ...(direction === undefined ? {} : { direction }),
    ...(filter === undefined ? {} : { filter }),
    ...(maxNodes === undefined ? {} : { max_nodes: maxNodes }),
    ...(maxEdges === undefined ? {} : { max_edges: maxEdges }),
    ...(maxFrontier === undefined ? {} : { max_frontier: maxFrontier }),
    ...(cursor === undefined ? {} : { cursor }),
  };
};

const encodePathInput = (input: GraphPathInput): Record<string, unknown> => {
  const value = inputRecord(input, ["workspaceId", "from", "to", "direction", "relationTypes", "maxDepth", "maxVisited"], "input");
  const from = encodeNodeRef(value.from, "from");
  const to = encodeNodeRef(value.to, "to");
  if (sameRef(from.ref, to.ref)) throw invalidRequest("path.endpoints");
  const direction = value.direction === undefined ? undefined : readDirection(value.direction, "direction", invalidRequest);
  const relationTypes = value.relationTypes === undefined ? undefined : readInputArray(value.relationTypes, "relationTypes", 10, (item, field) => readRelationType(item, field, invalidRequest));
  const maxDepth = value.maxDepth === undefined ? undefined : readInteger(value.maxDepth, "maxDepth", 1, 8, invalidRequest);
  const maxVisited = value.maxVisited === undefined ? undefined : readInteger(value.maxVisited, "maxVisited", 2, 500, invalidRequest);
  return {
    workspace_id: readUuid(value.workspaceId, "workspaceId", invalidRequest),
    from: from.wire,
    to: to.wire,
    ...(direction === undefined ? {} : { direction }),
    ...(relationTypes === undefined ? {} : { relation_types: relationTypes }),
    ...(maxDepth === undefined ? {} : { max_depth: maxDepth }),
    ...(maxVisited === undefined ? {} : { max_visited: maxVisited }),
  };
};

const validateNodeSearchInput = (input: GraphNodeSearchInput): void => {
  const value = inputRecord(input, ["workspaceId", "query", "limit"], "input");
  readUuid(value.workspaceId, "workspaceId", invalidRequest);
  const query = readString(value.query, "query", invalidRequest);
  if (!isValidGraphNodeSearchQuery(query)) throw invalidRequest("query");
  optionalLimit(value.limit, "limit", 50);
};

const validateNodeDetailInput = (input: GraphNodeDetailInput): void => {
  const value = inputRecord(input, ["workspaceId", "nodeType", "nodeId"], "input");
  readUuid(value.workspaceId, "workspaceId", invalidRequest);
  readNodeType(value.nodeType, "nodeType", invalidRequest);
  readUuid(value.nodeId, "nodeId", invalidRequest);
};

const validateRelationDetailInput = (input: GraphRelationDetailInput): void => {
  const value = inputRecord(input, ["workspaceId", "relationId"], "input");
  readUuid(value.workspaceId, "workspaceId", invalidRequest);
  readUuid(value.relationId, "relationId", invalidRequest);
};

const validateEvidenceInput = (input: GraphRelationEvidenceInput): void => {
  const value = inputRecord(input, ["workspaceId", "relationId", "cursor", "limit"], "input");
  readUuid(value.workspaceId, "workspaceId", invalidRequest);
  readUuid(value.relationId, "relationId", invalidRequest);
  optionalCursor(value.cursor, "cursor");
  optionalLimit(value.limit, "limit", 100);
};

const decodeProblem = (value: unknown, status: number): GraphApiError => {
  if (!isRecord(value)) return new GraphApiError({ errorCode: "INVALID_RESPONSE", message: "Graph API 返回了无效 Problem。", retryable: false }, status);
  try {
    assertExactKeys(value, ["error_code", "message", "retryable", "workflow_run_id", "details"], "problem");
    const workflowRunId = value.workflow_run_id === undefined && !hasOwn(value, "workflow_run_id")
      ? undefined
      : readUuid(value.workflow_run_id, "problem.workflow_run_id");
    const details = value.details === undefined && !hasOwn(value, "details") ? undefined : value.details;
    if (details !== undefined && !isRecord(details)) throw invalidResponse("problem.details");
    return new GraphApiError({
      errorCode: readText(value.error_code, "problem.error_code", 128),
      message: readText(value.message, "problem.message", 4096),
      retryable: readBoolean(value.retryable, "problem.retryable"),
      ...(workflowRunId === undefined ? {} : { workflowRunId }),
      ...(details === undefined ? {} : { details }),
    }, status);
  } catch {
    return new GraphApiError({ errorCode: "INVALID_RESPONSE", message: "Graph API 返回了无效 Problem。", retryable: false }, status);
  }
};

const isAbortError = (value: unknown): boolean =>
  (value instanceof DOMException || value instanceof Error) && value.name === "AbortError";

const requestGraph = async <T>(
  path: string,
  method: "GET" | "POST",
  body: Record<string, unknown> | undefined,
  decoder: (value: unknown) => T,
  signal?: AbortSignal,
): Promise<T> => {
  let response: Response;
  try {
    response = await fetch(`${apiBaseUrl}${path}`, {
      method,
      headers: body === undefined
        ? { Accept: "application/json" }
        : { Accept: "application/json", "Content-Type": "application/json" },
      ...(body === undefined ? {} : { body: JSON.stringify(body) }),
      ...(signal === undefined ? {} : { signal }),
    });
  } catch (error: unknown) {
    if (isAbortError(error)) throw error;
    throw new GraphApiError({ errorCode: "NETWORK_ERROR", message: "无法连接 Graph API。", retryable: true }, null, { cause: error });
  }
  let payload: unknown;
  try {
    payload = await response.json();
  } catch (error: unknown) {
    if (isAbortError(error)) throw error;
    throw new GraphApiError({ errorCode: "INVALID_RESPONSE", message: "Graph API 返回了无效 JSON。", retryable: false }, response.status, { cause: error });
  }
  if (!response.ok) throw decodeProblem(payload, response.status);
  return decoder(payload);
};

const queryPath = (path: string, values: Record<string, string | number | undefined>): string => {
  const query = new URLSearchParams();
  for (const [key, value] of Object.entries(values)) {
    if (value !== undefined) query.set(key, String(value));
  }
  return `${path}?${query.toString()}`;
};

export const getGraphGlobalPage = async (input: GraphGlobalInput, signal?: AbortSignal): Promise<GraphGlobalPage> =>
  requestGraph("/api/v1/graph/global", "POST", encodeGlobalInput(input), (value) => decodeGraphGlobalPage(value, input), signal);

export const getGraphNeighborhood = async (input: GraphNeighborhoodInput, signal?: AbortSignal): Promise<GraphNeighborhood> =>
  requestGraph("/api/v1/graph/neighborhood", "POST", encodeNeighborhoodInput(input), (value) => decodeGraphNeighborhood(value, input), signal);

export const findGraphPath = async (input: GraphPathInput, signal?: AbortSignal): Promise<GraphPathResult> =>
  requestGraph("/api/v1/graph/path", "POST", encodePathInput(input), (value) => decodeGraphPathResult(value, input), signal);

export const searchGraphNodes = async (input: GraphNodeSearchInput, signal?: AbortSignal): Promise<GraphNodeSearchResult> => {
  validateNodeSearchInput(input);
  return requestGraph(queryPath("/api/v1/graph/nodes", {
    workspace_id: input.workspaceId,
    query: input.query,
    limit: input.limit,
  }), "GET", undefined, (value) => decodeGraphNodeSearchResult(value, input), signal);
};

export const getGraphNodeDetail = async (input: GraphNodeDetailInput, signal?: AbortSignal): Promise<GraphNode> => {
  validateNodeDetailInput(input);
  const path = `/api/v1/graph/nodes/${encodeURIComponent(input.nodeType)}/${encodeURIComponent(input.nodeId)}`;
  return requestGraph(queryPath(path, { workspace_id: input.workspaceId }), "GET", undefined, (value) => decodeGraphNode(value, input), signal);
};

export const getGraphRelationDetail = async (input: GraphRelationDetailInput, signal?: AbortSignal): Promise<GraphRelationDetail> => {
  validateRelationDetailInput(input);
  const path = `/api/v1/graph/relations/${encodeURIComponent(input.relationId)}`;
  return requestGraph(queryPath(path, { workspace_id: input.workspaceId }), "GET", undefined, (value) => decodeGraphRelationDetail(value, input), signal);
};

export const getGraphRelationEvidencePage = async (
  input: GraphRelationEvidenceInput,
  signal?: AbortSignal,
): Promise<GraphRelationEvidencePage> => {
  validateEvidenceInput(input);
  const path = `/api/v1/graph/relations/${encodeURIComponent(input.relationId)}/evidence`;
  return requestGraph(queryPath(path, {
    workspace_id: input.workspaceId,
    cursor: input.cursor,
    limit: input.limit,
  }), "GET", undefined, (value) => decodeGraphRelationEvidencePage(value, input), signal);
};
