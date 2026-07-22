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
export type SemanticLinkCandidateStatus =
  | "ACTIVE"
  | "DEFERRED"
  | "IGNORED"
  | "FALSE_POSITIVE"
  | "PROPOSAL_CREATED"
  | "SUPERSEDED";
export type SemanticLinkDiscoveryMethod =
  | "TITLE_ALIAS"
  | "TERM_MATCH"
  | "CLAIM_SEMANTIC_SIMILARITY"
  | "COMMON_TOPIC"
  | "SHARED_SOURCE"
  | "RAG_CO_RETRIEVAL";
export type SemanticLinkReopenedReason = "CONTENT_CHANGED";
export type SemanticLinkDecisionAction =
  | "CONFIRM"
  | "CONFIRM_WITH_RELATION_TYPE"
  | "IGNORE"
  | "FALSE_POSITIVE"
  | "DEFER"
  | "RESUME";
export type SemanticLinkScanStatus = "PENDING" | "RUNNING" | "SUCCEEDED" | "FAILED" | "CANCELLED";
export type ProposalType = "file_patch" | "knowledge_change";
export type ProposalStatus =
  | "draft"
  | "validating"
  | "ready_for_review"
  | "approved"
  | "applying"
  | "applied"
  | "verifying"
  | "completed"
  | "rejected"
  | "needs_revision"
  | "deferred"
  | "apply_failed"
  | "verify_failed"
  | "rolled_back"
  | "cancelled";
export type ProposalDecision = "approved" | "rejected";
export type WorkflowDispatchStatus = "queued" | "running" | "replayed";

export interface SemanticLinkProblem {
  errorCode: string;
  message: string;
  retryable: boolean;
  workflowRunId?: string;
  details?: Readonly<Record<string, unknown>>;
}

export class SemanticLinkApiError extends Error implements SemanticLinkProblem {
  readonly code: string;
  readonly errorCode: string;
  readonly retryable: boolean;
  readonly status: number | null;
  readonly workflowRunId?: string;
  readonly details?: Readonly<Record<string, unknown>>;

  constructor(problem: SemanticLinkProblem, status: number | null = null, options?: ErrorOptions) {
    super(problem.message, options);
    this.name = "SemanticLinkApiError";
    this.code = problem.errorCode;
    this.errorCode = problem.errorCode;
    this.retryable = problem.retryable;
    this.status = status;
    if (problem.workflowRunId !== undefined) this.workflowRunId = problem.workflowRunId;
    if (problem.details !== undefined) this.details = problem.details;
  }
}

export interface SemanticLinkCandidateEndpoint {
  type: NodeType;
  id: string;
  version: number;
  summary: string;
  excerpt: string;
}

export interface SemanticLinkCandidateEvidence {
  id: string;
  semanticHash: string;
  sourceVersionId: string;
  sourceSpanId: string;
  sourceVersionHref: string;
  sourceSpanHref: string;
  excerpt: string;
  reason: string;
}

/**
 * Candidate generation is the frozen Go-domain version envelope.  Optional
 * JSON fields are normalised to null at this boundary so components never
 * need to distinguish an omitted pointer from an explicit null.
 */
export interface SemanticLinkGeneration {
  indexVersionId: string | null;
  embeddingVersionId: string | null;
  rerankVersionId: string | null;
  modelVersion: string | null;
  modelProfileVersion: string | null;
  promptVersion: string | null;
  schemaVersion: string | null;
  ruleId: string | null;
  ruleVersion: string | null;
  modelRunId: string | null;
}

export interface SemanticLinkCandidate {
  id: string;
  workspaceId: string;
  fingerprint: string;
  status: SemanticLinkCandidateStatus;
  version: number;
  source: SemanticLinkCandidateEndpoint;
  target: SemanticLinkCandidateEndpoint;
  proposedRelationType: RelationType;
  confidence: number;
  reason: string;
  discoveryMethods: SemanticLinkDiscoveryMethod[];
  evidence: SemanticLinkCandidateEvidence[];
  generation: SemanticLinkGeneration;
  reopenedReason: SemanticLinkReopenedReason | null;
  reopenedFromCandidateId: string | null;
  proposalId: string | null;
  deferredUntil: string | null;
  createdAt: string;
  updatedAt: string;
}

export interface SemanticLinkCandidatePage {
  workspaceId: string;
  items: SemanticLinkCandidate[];
  nextCursor?: string;
}

export interface ListSemanticLinkCandidatesInput {
  workspaceId: string;
  nodeType?: NodeType;
  nodeId?: string;
  statuses?: SemanticLinkCandidateStatus[];
  relationTypes?: RelationType[];
  reopenedReasons?: SemanticLinkReopenedReason[];
  minConfidence?: number;
  cursor?: string;
  limit?: number;
}

interface SemanticLinkCandidateDecisionBase {
  candidateId: string;
  workspaceId: string;
  expectedVersion: number;
  idempotencyKey: string;
}

export type SemanticLinkCandidateDecisionInput = SemanticLinkCandidateDecisionBase & (
  | { action: "CONFIRM"; reason?: never; relationType?: never; deferredUntil?: never }
  | { action: "CONFIRM_WITH_RELATION_TYPE"; relationType: RelationType; reason?: never; deferredUntil?: never }
  | { action: "IGNORE" | "FALSE_POSITIVE"; reason: string; relationType?: never; deferredUntil?: never }
  | { action: "DEFER"; reason?: string; relationType?: never; deferredUntil?: string | null }
  | { action: "RESUME"; reason?: never; relationType?: never; deferredUntil?: null }
);

export interface SemanticLinkDecisionReceipt {
  id: string;
  candidateId: string;
  workspaceId: string;
  action: SemanticLinkDecisionAction;
  status: SemanticLinkCandidateStatus;
  version: number;
  proposalId: string | null;
  createdAt: string;
  updatedAt: string;
}

export interface TopicScope {
  kind: "TOPIC";
  topicId: string;
}

export interface SmartCollectionScope {
  kind: "SMART_COLLECTION";
  collectionId: string;
  collectionVersion?: number;
  queryHash?: string;
  readModelRevision?: string;
}

export type SemanticLinkScanScope = TopicScope | SmartCollectionScope;

export interface StartSemanticLinkScanInput {
  workspaceId: string;
  scope: SemanticLinkScanScope;
  idempotencyKey: string;
}

export interface SemanticLinkScanAcceptance {
  scanId: string;
  workflowRunId: string;
  status: SemanticLinkScanStatus;
  version: number;
  statusUrl: string;
}

export interface GetSemanticLinkScanInput {
  scanId: string;
  workspaceId: string;
}

export interface SemanticLinkScan {
  id: string;
  workspaceId: string;
  scope: SemanticLinkScanScope;
  status: SemanticLinkScanStatus;
  workflowRunId: string;
  version: number;
  statusUrl: string;
  totalCount: number;
  processedCount: number;
  candidateCount: number;
  ignoredCount: number;
  failedCount: number;
  lastError: {
    stage: string;
    code: string;
    retryable: boolean;
  } | null;
  createdAt: string;
  updatedAt: string;
  completedAt: string | null;
}

export interface ProposalApproval {
  id: string;
  proposalId: string;
  revisionId: string;
  changeHash: string;
  decision: ProposalDecision;
  approvedGitHead: string | null;
  workflowRunId: string | null;
  workflowStatusUrl: string | null;
  dispatchStatus: WorkflowDispatchStatus | null;
  decidedAt: string;
}

export interface FilePatchRevision {
  id: string;
  revisionNo: number;
  baseHash: string;
  content: string;
  evidenceSummary: string;
  risk: string;
  rollbackPlan: string;
  changeHash: string;
  createdAt: string;
}

export interface FilePatchProposal {
  proposalType: "file_patch";
  id: string;
  workspaceId: string;
  targetPath: string;
  status: ProposalStatus;
  revision: FilePatchRevision;
  approval: ProposalApproval | null;
  createdAt: string;
  updatedAt: string;
}

export interface KnowledgeChangeTargetRef {
  type: "RELATION_CANDIDATE";
  id: string;
  fingerprint: string;
}

export interface KnowledgeChangeBaseVersion {
  nodeType: NodeType;
  nodeId: string;
  version: number;
}

export interface KnowledgeChangeSet {
  operation: "CREATE_RELATION";
  source: { type: NodeType; id: string; version: number };
  target: { type: NodeType; id: string; version: number };
  relationType: RelationType;
}

export interface KnowledgeChangeEvidenceRef {
  candidateEvidenceId: string;
  semanticHash: string;
}

export interface KnowledgeChangeRevision {
  id: string;
  revisionNo: number;
  schemaVersion: "knowledge-relation-change/v1";
  targetRefs: KnowledgeChangeTargetRef[];
  baseVersions: KnowledgeChangeBaseVersion[];
  changeSet: KnowledgeChangeSet;
  evidenceRefs: KnowledgeChangeEvidenceRef[];
  risk: string;
  rollbackPlan: string;
  changeHash: string;
  createdAt: string;
}

export interface KnowledgeChangeProposal {
  proposalType: "knowledge_change";
  id: string;
  workspaceId: string;
  status: ProposalStatus;
  revision: KnowledgeChangeRevision;
  approval: ProposalApproval | null;
  createdAt: string;
  updatedAt: string;
}

export type SemanticLinkProposal = FilePatchProposal | KnowledgeChangeProposal;

export interface GetProposalInput {
  proposalId: string;
}

export interface ApproveProposalInput {
  proposalId: string;
  revisionId: string;
  changeHash: string;
  decision: ProposalDecision;
}

const apiBaseUrl = (import.meta.env.VITE_API_BASE_URL ?? "").replace(/\/$/, "");
const uuidPattern = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;
const hashPattern = /^[0-9a-f]{64}$/;
const gitHeadPattern = /^[0-9a-f]{40}$/;
const rfc3339Pattern = /^\d{4}-(?:0[1-9]|1[0-2])-(?:0[1-9]|[12]\d|3[01])T(?:[01]\d|2[0-3]):[0-5]\d:[0-5]\d(?:\.\d{1,9})?(?:Z|[+-](?:[01]\d|2[0-3]):[0-5]\d)$/;
const controlPattern = /[\u0000-\u001f\u007f-\u009f]/;
const textEncoder = new TextEncoder();
const maxCursorBytes = 2048;
// Candidate reasons/risk text share the backend Knowledge domain's 4 KiB bound.
const maxReasonBytes = 4 * 1024;
const maxReferenceBytes = 512;
const maxSummaryBytes = 4096;
const maxExcerptBytes = 4096;
const maxFreeTextBytes = 8 * 1024 * 1024;
const nodeTypes = ["TOPIC", "CLAIM"] as const;
const relationTypes = [
  "CITES",
  "DERIVED_FROM",
  "BELONGS_TO",
  "SUPPORTS",
  "COMPLEMENTS",
  "DUPLICATES",
  "CONFLICTS_WITH",
  "PREREQUISITE_OF",
  "VERSION_OF",
  "IMPACTS",
] as const;
const candidateStatuses = [
  "ACTIVE",
  "DEFERRED",
  "IGNORED",
  "FALSE_POSITIVE",
  "PROPOSAL_CREATED",
  "SUPERSEDED",
] as const;
const discoveryMethods = [
  "TITLE_ALIAS",
  "TERM_MATCH",
  "CLAIM_SEMANTIC_SIMILARITY",
  "COMMON_TOPIC",
  "SHARED_SOURCE",
  "RAG_CO_RETRIEVAL",
] as const;
const reopenedReasons = ["CONTENT_CHANGED"] as const;
const decisionActions = [
  "CONFIRM",
  "CONFIRM_WITH_RELATION_TYPE",
  "IGNORE",
  "FALSE_POSITIVE",
  "DEFER",
  "RESUME",
] as const;
const scanStatuses = ["PENDING", "RUNNING", "SUCCEEDED", "FAILED", "CANCELLED"] as const;
const proposalStatuses = [
  "draft",
  "validating",
  "ready_for_review",
  "approved",
  "applying",
  "applied",
  "verifying",
  "completed",
  "rejected",
  "needs_revision",
  "deferred",
  "apply_failed",
  "verify_failed",
  "rolled_back",
  "cancelled",
] as const;
const proposalDecisions = ["approved", "rejected"] as const;
const dispatchStatuses = ["queued", "running", "replayed"] as const;

type InvalidFactory = (field: string) => SemanticLinkApiError;

const isRecord = (value: unknown): value is Record<string, unknown> =>
  typeof value === "object" && value !== null && !Array.isArray(value);

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

const invalidResponse = (field: string): SemanticLinkApiError => new SemanticLinkApiError({
  errorCode: "INVALID_RESPONSE",
  message: `Semantic Link API 响应字段无效：${field}`,
  retryable: false,
});

const invalidRequest = (field: string): SemanticLinkApiError => new SemanticLinkApiError({
  errorCode: "INVALID_REQUEST",
  message: `Semantic Link API 请求字段无效：${field}`,
  retryable: false,
});

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

const readInteger = (
  value: unknown,
  field: string,
  minimum: number,
  maximum = Number.MAX_SAFE_INTEGER,
  fail: InvalidFactory = invalidResponse,
): number => {
  if (typeof value !== "number" || !Number.isSafeInteger(value) || value < minimum || value > maximum) {
    throw fail(field);
  }
  return value;
};

const readBoolean = (value: unknown, field: string, fail: InvalidFactory = invalidResponse): boolean => {
  if (typeof value !== "boolean") throw fail(field);
  return value;
};

const readFiniteNumber = (value: unknown, field: string, fail: InvalidFactory = invalidResponse): number => {
  if (typeof value !== "number" || !Number.isFinite(value)) throw fail(field);
  return Object.is(value, -0) ? 0 : value;
};

const readConfidence = (value: unknown, field: string, fail: InvalidFactory = invalidResponse): number => {
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

const readGitHead = (value: unknown, field: string, fail: InvalidFactory = invalidResponse): string => {
  const result = readString(value, field, fail);
  if (!gitHeadPattern.test(result)) throw fail(field);
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

const readCursor = (value: unknown, field: string, fail: InvalidFactory = invalidResponse): string => {
  const result = readText(value, field, maxCursorBytes, false, fail);
  if (textEncoder.encode(result).length > maxCursorBytes) throw fail(field);
  return result;
};

const readNullableTimestamp = (value: unknown, field: string): string | null =>
  value === null ? null : readTimestamp(value, field);

const readOptionalUuid = (value: unknown, field: string): string | null =>
  value === null ? null : readUuid(value, field);

const readEnum = <T extends string>(
  value: unknown,
  field: string,
  allowed: readonly T[],
  fail: InvalidFactory = invalidResponse,
): T => {
  if (typeof value !== "string" || !allowed.includes(value as T)) throw fail(field);
  return value as T;
};

const readArray = <T>(
  value: unknown,
  field: string,
  maximum: number,
  reader: (item: unknown, field: string) => T,
  minimum = 0,
): T[] => {
  if (!Array.isArray(value) || value.length < minimum || value.length > maximum) throw invalidResponse(field);
  return value.map((item, index) => reader(item, `${field}[${String(index)}]`));
};

const unique = <T>(values: T[], field: string, identity: (value: T) => string): T[] => {
  if (new Set(values.map(identity)).size !== values.length) throw invalidResponse(field);
  return values;
};

const readHref = (value: unknown, expected: string, field: string): string => {
  const result = readText(value, field, maxCursorBytes);
  if (result !== expected || result.includes("\\") || result.includes("..")) throw invalidResponse(field);
  return result;
};

const readPath = (value: unknown, field: string): string => {
  const result = readText(value, field, 1024);
  const parts = result.split("/");
  if (result.startsWith("/") || result.includes("\\") || parts.some((part) => part === "" || part === "." || part === "..")) {
    throw invalidResponse(field);
  }
  return result;
};

const readNodeType = (value: unknown, field: string, fail: InvalidFactory = invalidResponse): NodeType =>
  readEnum(value, field, nodeTypes, fail);

const readRelationType = (value: unknown, field: string, fail: InvalidFactory = invalidResponse): RelationType =>
  readEnum(value, field, relationTypes, fail);

const readCandidateStatus = (value: unknown, field: string): SemanticLinkCandidateStatus =>
  readEnum(value, field, candidateStatuses);

const readDiscoveryMethod = (value: unknown, field: string): SemanticLinkDiscoveryMethod =>
  readEnum(value, field, discoveryMethods);

const readReopenedReason = (value: unknown, field: string): SemanticLinkReopenedReason =>
  readEnum(value, field, reopenedReasons);

const readDecisionAction = (value: unknown, field: string, fail: InvalidFactory = invalidResponse): SemanticLinkDecisionAction =>
  readEnum(value, field, decisionActions, fail);

const readScanStatus = (value: unknown, field: string): SemanticLinkScanStatus =>
  readEnum(value, field, scanStatuses);

const readProposalStatus = (value: unknown, field: string): ProposalStatus =>
  readEnum(value, field, proposalStatuses);

const readProposalDecision = (value: unknown, field: string, fail: InvalidFactory = invalidResponse): ProposalDecision =>
  readEnum(value, field, proposalDecisions, fail);

const readDispatchStatus = (value: unknown, field: string): WorkflowDispatchStatus =>
  readEnum(value, field, dispatchStatuses);

const readCandidateEndpoint = (value: unknown, field: string): SemanticLinkCandidateEndpoint => {
  if (!isRecord(value)) throw invalidResponse(field);
  assertExactKeys(value, ["type", "id", "version", "summary", "excerpt"], field);
  return {
    type: readNodeType(value.type, `${field}.type`),
    id: readUuid(value.id, `${field}.id`),
    version: readInteger(value.version, `${field}.version`, 1),
    summary: readText(value.summary, `${field}.summary`, maxSummaryBytes),
    excerpt: readText(value.excerpt, `${field}.excerpt`, maxExcerptBytes, true),
  };
};

const readCandidateEvidence = (value: unknown, field: string, workspaceId: string): SemanticLinkCandidateEvidence => {
  if (!isRecord(value)) throw invalidResponse(field);
  assertExactKeys(value, [
    "id",
    "semantic_hash",
    "source_version_id",
    "source_span_id",
    "source_version_href",
    "source_span_href",
    "excerpt",
    "reason",
  ], field);
  const sourceVersionId = readUuid(value.source_version_id, `${field}.source_version_id`);
  const sourceSpanId = readUuid(value.source_span_id, `${field}.source_span_id`);
  const sourceVersionHref = readHref(
    value.source_version_href,
    `/api/v1/workspaces/${workspaceId}/source-versions/${sourceVersionId}`,
    `${field}.source_version_href`,
  );
  const sourceSpanHref = readHref(
    value.source_span_href,
    `${sourceVersionHref}/spans/${sourceSpanId}`,
    `${field}.source_span_href`,
  );
  return {
    id: readUuid(value.id, `${field}.id`),
    semanticHash: readHash(value.semantic_hash, `${field}.semantic_hash`),
    sourceVersionId,
    sourceSpanId,
    sourceVersionHref,
    sourceSpanHref,
    excerpt: readText(value.excerpt, `${field}.excerpt`, maxExcerptBytes),
    reason: readText(value.reason, `${field}.reason`, maxReasonBytes),
  };
};

const readOptionalGenerationUuid = (value: unknown, field: string): string | null =>
  value === undefined || value === null ? null : readUuid(value, field);

const readRequiredNullableGenerationUuid = (value: unknown, field: string): string | null =>
  value === null ? null : readUuid(value, field);

const readOptionalGenerationReference = (value: unknown, field: string): string | null =>
  value === undefined ? null : readText(value, field, maxReferenceBytes);

/** Decode the exact Go-domain generation envelope and reject legacy fake fields. */
const readGeneration = (value: unknown, field: string): SemanticLinkGeneration => {
  if (!isRecord(value)) throw invalidResponse(field);
  assertExactKeys(value, [
    "index_version_id",
    "embedding_version_id",
    "rerank_version_id",
    "model_version",
    "model_profile_version",
    "prompt_version",
    "schema_version",
    "rule_id",
    "rule_version",
    "model_run_id",
  ], field);

  const result: SemanticLinkGeneration = {
    indexVersionId: readRequiredNullableGenerationUuid(value.index_version_id, `${field}.index_version_id`),
    embeddingVersionId: readRequiredNullableGenerationUuid(value.embedding_version_id, `${field}.embedding_version_id`),
    rerankVersionId: readRequiredNullableGenerationUuid(value.rerank_version_id, `${field}.rerank_version_id`),
    modelVersion: readOptionalGenerationReference(value.model_version, `${field}.model_version`),
    modelProfileVersion: readOptionalGenerationReference(value.model_profile_version, `${field}.model_profile_version`),
    promptVersion: readOptionalGenerationReference(value.prompt_version, `${field}.prompt_version`),
    schemaVersion: readOptionalGenerationReference(value.schema_version, `${field}.schema_version`),
    ruleId: readOptionalGenerationUuid(value.rule_id, `${field}.rule_id`),
    ruleVersion: readOptionalGenerationReference(value.rule_version, `${field}.rule_version`),
    modelRunId: readOptionalGenerationUuid(value.model_run_id, `${field}.model_run_id`),
  };

  if (result.embeddingVersionId !== null &&
      (result.indexVersionId === null || result.embeddingVersionId === result.indexVersionId)) {
    throw invalidResponse(`${field}.embedding_version_id`);
  }
  if (result.rerankVersionId !== null && result.indexVersionId === null) {
    throw invalidResponse(`${field}.rerank_version_id`);
  }

  const hasRule = result.ruleId !== null || result.ruleVersion !== null;
  const hasModel = result.modelVersion !== null || result.modelProfileVersion !== null ||
    result.promptVersion !== null || result.schemaVersion !== null;
  if (hasRule && hasModel) throw invalidResponse(`${field}.kind`);
  if (!hasRule && !hasModel && result.indexVersionId === null) throw invalidResponse(field);
  if (hasRule && (result.ruleId === null || result.ruleVersion === null)) {
    throw invalidResponse(`${field}.rule_version`);
  }
  if (hasModel && (result.modelVersion === null || result.modelProfileVersion === null ||
      result.promptVersion === null || result.schemaVersion === null)) {
    throw invalidResponse(`${field}.model_version`);
  }
  return result;
};

const readCandidate = (value: unknown, field: string, workspaceId: string): SemanticLinkCandidate => {
  if (!isRecord(value)) throw invalidResponse(field);
  assertExactKeys(value, [
    "id",
    "workspace_id",
    "fingerprint",
    "status",
    "version",
    "source",
    "target",
    "proposed_relation_type",
    "confidence",
    "reason",
    "discovery_methods",
    "evidence",
    "generation",
    "reopened_reason",
    "reopened_from_candidate_id",
    "proposal_id",
    "deferred_until",
    "created_at",
    "updated_at",
  ], field);
  const candidateWorkspaceId = readUuid(value.workspace_id, `${field}.workspace_id`);
  if (candidateWorkspaceId !== workspaceId) throw invalidResponse(`${field}.workspace_id`);
  const source = readCandidateEndpoint(value.source, `${field}.source`);
  const target = readCandidateEndpoint(value.target, `${field}.target`);
  const proposedRelationType = readRelationType(value.proposed_relation_type, `${field}.proposed_relation_type`);
  if (source.type === target.type && source.id === target.id ||
      !graphRelationTypeCompatible(proposedRelationType, source.type, target.type) ||
      isSymmetricGraphRelationType(proposedRelationType) && graphNodeRefIdentity(source) > graphNodeRefIdentity(target)) {
    throw invalidResponse(`${field}.endpoints`);
  }
  return {
    id: readUuid(value.id, `${field}.id`),
    workspaceId: candidateWorkspaceId,
    fingerprint: readHash(value.fingerprint, `${field}.fingerprint`),
    status: readCandidateStatus(value.status, `${field}.status`),
    version: readInteger(value.version, `${field}.version`, 1),
    source,
    target,
    proposedRelationType,
    confidence: readConfidence(value.confidence, `${field}.confidence`),
    reason: readText(value.reason, `${field}.reason`, maxReasonBytes),
    discoveryMethods: unique(
      readArray(value.discovery_methods, `${field}.discovery_methods`, discoveryMethods.length, readDiscoveryMethod, 1),
      `${field}.discovery_methods`,
      (item) => item,
    ),
    evidence: unique(
      readArray(value.evidence, `${field}.evidence`, 100, (item, itemField) => readCandidateEvidence(item, itemField, workspaceId), 1),
      `${field}.evidence`,
      (item) => item.id,
    ),
    generation: readGeneration(value.generation, `${field}.generation`),
    reopenedReason: value.reopened_reason === null ? null : readReopenedReason(value.reopened_reason, `${field}.reopened_reason`),
    reopenedFromCandidateId: readOptionalUuid(value.reopened_from_candidate_id, `${field}.reopened_from_candidate_id`),
    proposalId: readOptionalUuid(value.proposal_id, `${field}.proposal_id`),
    deferredUntil: value.deferred_until === null ? null : readTimestamp(value.deferred_until, `${field}.deferred_until`),
    createdAt: readTimestamp(value.created_at, `${field}.created_at`),
    updatedAt: readTimestamp(value.updated_at, `${field}.updated_at`),
  };
};

export const decodeSemanticLinkCandidatePage = (
  value: unknown,
  request?: Pick<ListSemanticLinkCandidatesInput, "workspaceId" | "nodeType" | "nodeId" | "statuses" | "relationTypes" | "reopenedReasons" | "minConfidence" | "limit">,
): SemanticLinkCandidatePage => {
  if (!isRecord(value)) throw invalidResponse("candidate_page");
  assertExactKeys(value, ["workspace_id", "items", "next_cursor"], "candidate_page");
  const workspaceId = readUuid(value.workspace_id, "candidate_page.workspace_id");
  if (request?.workspaceId !== undefined && workspaceId !== request.workspaceId) throw invalidResponse("candidate_page.workspace_id");
  const items = readArray(value.items, "candidate_page.items", 100, (item, field) => readCandidate(item, field, workspaceId));
  if (request !== undefined) {
    if ((request.nodeType === undefined) !== (request.nodeId === undefined)) throw invalidResponse("candidate_page.request.node");
    const limit = request.limit ?? 20;
    if (items.length > limit) throw invalidResponse("candidate_page.items");
    const statuses = request.statuses === undefined ? null : new Set(request.statuses);
    const relationTypes = request.relationTypes === undefined ? null : new Set(request.relationTypes);
    const reopenedReasons = request.reopenedReasons === undefined ? null : new Set(request.reopenedReasons);
    const statusOrder: Record<SemanticLinkCandidateStatus, number> = {
      ACTIVE: 0,
      DEFERRED: 1,
      IGNORED: 2,
      FALSE_POSITIVE: 3,
      PROPOSAL_CREATED: 4,
      SUPERSEDED: 5,
    };
    items.forEach((candidate, index) => {
      const sourceMatchesNode = request.nodeType !== undefined && request.nodeId !== undefined &&
        candidate.source.type === request.nodeType && candidate.source.id === request.nodeId;
      const targetMatchesNode = request.nodeType !== undefined && request.nodeId !== undefined &&
        candidate.target.type === request.nodeType && candidate.target.id === request.nodeId;
      // Topic-scoped results may be Claim pairs produced by a Topic scan. The
      // backend repository owns the persisted BELONGS_TO membership predicate.
      const matchesTopicClaimPair = request.nodeType === "TOPIC" &&
        candidate.source.type === "CLAIM" && candidate.target.type === "CLAIM";
      if ((statuses !== null && !statuses.has(candidate.status)) ||
          (relationTypes !== null && !relationTypes.has(candidate.proposedRelationType)) ||
          (reopenedReasons !== null && (candidate.reopenedReason === null || !reopenedReasons.has(candidate.reopenedReason))) ||
          (request.minConfidence !== undefined && candidate.confidence < request.minConfidence) ||
          (request.nodeType !== undefined && request.nodeId !== undefined && !sourceMatchesNode && !targetMatchesNode && !matchesTopicClaimPair)) {
        throw invalidResponse(`candidate_page.items[${String(index)}]`);
      }
      const previous = items[index - 1];
      if (previous !== undefined) {
        const previousTime = Date.parse(previous.updatedAt);
        const currentTime = Date.parse(candidate.updatedAt);
        if (statusOrder[previous.status] > statusOrder[candidate.status] ||
            statusOrder[previous.status] === statusOrder[candidate.status] &&
              (previousTime < currentTime || previousTime === currentTime && previous.id >= candidate.id)) {
          throw invalidResponse("candidate_page.items.order");
        }
      }
    });
  }
  return {
    workspaceId,
    items,
    ...(value.next_cursor === undefined ? {} : { nextCursor: readCursor(value.next_cursor, "candidate_page.next_cursor") }),
  };
};

const readDecisionReceipt = (
  value: unknown,
  request?: Pick<SemanticLinkCandidateDecisionInput, "candidateId" | "workspaceId" | "action" | "expectedVersion">,
): SemanticLinkDecisionReceipt => {
  if (!isRecord(value)) throw invalidResponse("decision_receipt");
  assertExactKeys(value, [
    "id",
    "candidate_id",
    "workspace_id",
    "action",
    "status",
    "version",
    "proposal_id",
    "created_at",
    "updated_at",
  ], "decision_receipt");
  const result = {
    id: readUuid(value.id, "decision_receipt.id"),
    candidateId: readUuid(value.candidate_id, "decision_receipt.candidate_id"),
    workspaceId: readUuid(value.workspace_id, "decision_receipt.workspace_id"),
    action: readDecisionAction(value.action, "decision_receipt.action"),
    status: readCandidateStatus(value.status, "decision_receipt.status"),
    version: readInteger(value.version, "decision_receipt.version", 1),
    proposalId: readOptionalUuid(value.proposal_id, "decision_receipt.proposal_id"),
    createdAt: readTimestamp(value.created_at, "decision_receipt.created_at"),
    updatedAt: readTimestamp(value.updated_at, "decision_receipt.updated_at"),
  };
  if (request !== undefined && (result.candidateId !== request.candidateId || result.workspaceId !== request.workspaceId ||
      result.action !== request.action || result.version !== request.expectedVersion + 1)) {
    throw invalidResponse("decision_receipt.binding");
  }
  const confirms = result.action === "CONFIRM" || result.action === "CONFIRM_WITH_RELATION_TYPE";
  if (confirms !== (result.proposalId !== null)) throw invalidResponse("decision_receipt.proposal_id");
  const expectedStatus = confirms
    ? "PROPOSAL_CREATED"
    : result.action === "IGNORE"
      ? "IGNORED"
      : result.action === "FALSE_POSITIVE"
        ? "FALSE_POSITIVE"
        : result.action === "DEFER"
          ? "DEFERRED"
          : "ACTIVE";
  if (result.status !== expectedStatus) throw invalidResponse("decision_receipt.status");
  return result;
};

const readScanScope = (value: unknown, field: string): SemanticLinkScanScope => {
  if (!isRecord(value)) throw invalidResponse(field);
  const kind = readEnum(value.kind, `${field}.kind`, ["TOPIC", "SMART_COLLECTION"] as const);
  if (kind === "SMART_COLLECTION") {
    assertExactKeys(value, ["kind", "collection_id", "collection_version", "query_hash", "read_model_revision"], field);
    return {
      kind,
      collectionId: readUuid(value.collection_id, `${field}.collection_id`),
      collectionVersion: readInteger(value.collection_version, `${field}.collection_version`, 1),
      queryHash: readHash(value.query_hash, `${field}.query_hash`),
      readModelRevision: readHash(value.read_model_revision, `${field}.read_model_revision`),
    };
  }
  assertExactKeys(value, ["kind", "topic_id"], field);
  return {
    kind,
    topicId: readUuid(value.topic_id, `${field}.topic_id`),
  };
};

const semanticLinkScanStatusHref = (scanId: string, workspaceId: string): string =>
  `/api/v1/graph/candidate-scans/${scanId}?workspace_id=${workspaceId}`;

export const decodeSemanticLinkScanAcceptance = (value: unknown, workspaceId: string): SemanticLinkScanAcceptance => {
  if (!isRecord(value)) throw invalidResponse("scan_acceptance");
  assertExactKeys(value, ["scan_id", "workflow_run_id", "status", "version", "status_url"], "scan_acceptance");
  const scanId = readUuid(value.scan_id, "scan_acceptance.scan_id");
  const workflowRunId = readUuid(value.workflow_run_id, "scan_acceptance.workflow_run_id");
  const expectedWorkspaceId = readUuid(workspaceId, "scan_acceptance.workspace_id");
  return {
    scanId,
    workflowRunId,
    status: readScanStatus(value.status, "scan_acceptance.status"),
    version: readInteger(value.version, "scan_acceptance.version", 1),
    statusUrl: readHref(value.status_url, semanticLinkScanStatusHref(scanId, expectedWorkspaceId), "scan_acceptance.status_url"),
  };
};

export const decodeSemanticLinkScan = (value: unknown, request?: GetSemanticLinkScanInput): SemanticLinkScan => {
  if (!isRecord(value)) throw invalidResponse("scan");
  assertExactKeys(value, [
    "id",
    "workspace_id",
    "scope",
    "status",
    "workflow_run_id",
    "version",
    "status_url",
    "total_count",
    "processed_count",
    "candidate_count",
    "ignored_count",
    "failed_count",
    "last_error",
    "created_at",
    "updated_at",
    "completed_at",
  ], "scan");
  const id = readUuid(value.id, "scan.id");
  const workspaceId = readUuid(value.workspace_id, "scan.workspace_id");
  if (request?.scanId !== undefined && id !== request.scanId) throw invalidResponse("scan.id");
  if (request?.workspaceId !== undefined && workspaceId !== request.workspaceId) throw invalidResponse("scan.workspace_id");
  const workflowRunId = readUuid(value.workflow_run_id, "scan.workflow_run_id");
  let lastError: SemanticLinkScan["lastError"] = null;
  if (value.last_error !== null) {
    if (!isRecord(value.last_error)) throw invalidResponse("scan.last_error");
    assertExactKeys(value.last_error, ["stage", "code", "retryable"], "scan.last_error");
    lastError = {
      stage: readText(value.last_error.stage, "scan.last_error.stage", 128),
      code: readText(value.last_error.code, "scan.last_error.code", 128),
      retryable: readBoolean(value.last_error.retryable, "scan.last_error.retryable"),
    };
  }
  const status = readScanStatus(value.status, "scan.status");
  if ((status === "FAILED") !== (lastError !== null)) throw invalidResponse("scan.last_error");
  return {
    id,
    workspaceId,
    scope: readScanScope(value.scope, "scan.scope"),
    status,
    workflowRunId,
    version: readInteger(value.version, "scan.version", 1),
    statusUrl: readHref(value.status_url, semanticLinkScanStatusHref(id, workspaceId), "scan.status_url"),
    totalCount: readInteger(value.total_count, "scan.total_count", 0),
    processedCount: readInteger(value.processed_count, "scan.processed_count", 0),
    candidateCount: readInteger(value.candidate_count, "scan.candidate_count", 0),
    ignoredCount: readInteger(value.ignored_count, "scan.ignored_count", 0),
    failedCount: readInteger(value.failed_count, "scan.failed_count", 0),
    lastError,
    createdAt: readTimestamp(value.created_at, "scan.created_at"),
    updatedAt: readTimestamp(value.updated_at, "scan.updated_at"),
    completedAt: readNullableTimestamp(value.completed_at, "scan.completed_at"),
  };
};

const readApproval = (value: unknown, field: string): ProposalApproval => {
  if (!isRecord(value)) throw invalidResponse(field);
  assertExactKeys(value, [
    "id",
    "proposal_id",
    "revision_id",
    "change_hash",
    "decision",
    "approved_git_head",
    "workflow_run_id",
    "workflow_status_url",
    "dispatch_status",
    "decided_at",
  ], field);
  const workflowRunId = value.workflow_run_id === undefined ? null : readUuid(value.workflow_run_id, `${field}.workflow_run_id`);
  const workflowStatusUrl = value.workflow_status_url === undefined
    ? null
    : workflowRunId === null
      ? readHref(value.workflow_status_url, "/api/v1/workflows/null", `${field}.workflow_status_url`)
      : readHref(value.workflow_status_url, `/api/v1/workflows/${workflowRunId}`, `${field}.workflow_status_url`);
  return {
    id: readUuid(value.id, `${field}.id`),
    proposalId: readUuid(value.proposal_id, `${field}.proposal_id`),
    revisionId: readUuid(value.revision_id, `${field}.revision_id`),
    changeHash: readHash(value.change_hash, `${field}.change_hash`),
    decision: readProposalDecision(value.decision, `${field}.decision`),
    approvedGitHead: value.approved_git_head === undefined ? null : readGitHead(value.approved_git_head, `${field}.approved_git_head`),
    workflowRunId,
    workflowStatusUrl,
    dispatchStatus: value.dispatch_status === undefined ? null : readDispatchStatus(value.dispatch_status, `${field}.dispatch_status`),
    decidedAt: readTimestamp(value.decided_at, `${field}.decided_at`),
  };
};

const readFilePatchRevision = (value: unknown, field: string): FilePatchRevision => {
  if (!isRecord(value)) throw invalidResponse(field);
  assertExactKeys(value, [
    "id",
    "revision_no",
    "base_hash",
    "content",
    "evidence_summary",
    "risk",
    "rollback_plan",
    "change_hash",
    "created_at",
  ], field);
  return {
    id: readUuid(value.id, `${field}.id`),
    revisionNo: readInteger(value.revision_no, `${field}.revision_no`, 1),
    baseHash: readHash(value.base_hash, `${field}.base_hash`),
    content: readText(value.content, `${field}.content`, maxFreeTextBytes, true),
    evidenceSummary: readText(value.evidence_summary, `${field}.evidence_summary`, maxReasonBytes),
    risk: readText(value.risk, `${field}.risk`, maxReasonBytes),
    rollbackPlan: readText(value.rollback_plan, `${field}.rollback_plan`, maxReasonBytes),
    changeHash: readHash(value.change_hash, `${field}.change_hash`),
    createdAt: readTimestamp(value.created_at, `${field}.created_at`),
  };
};

const readKnowledgeChangeRevision = (value: unknown, field: string): KnowledgeChangeRevision => {
  if (!isRecord(value)) throw invalidResponse(field);
  assertExactKeys(value, [
    "id",
    "revision_no",
    "schema_version",
    "target_refs",
    "base_versions",
    "change_set",
    "evidence_refs",
    "risk",
    "rollback_plan",
    "change_hash",
    "created_at",
  ], field);
  const targetRefs = unique(readArray(value.target_refs, `${field}.target_refs`, 100, (item, itemField) => {
    if (!isRecord(item)) throw invalidResponse(itemField);
    assertExactKeys(item, ["type", "id", "fingerprint"], itemField);
    return {
      type: readEnum(item.type, `${itemField}.type`, ["RELATION_CANDIDATE"] as const),
      id: readUuid(item.id, `${itemField}.id`),
      fingerprint: readHash(item.fingerprint, `${itemField}.fingerprint`),
    };
  }, 1), `${field}.target_refs`, (item) => item.id);
  const baseVersions = unique(readArray(value.base_versions, `${field}.base_versions`, 100, (item, itemField) => {
    if (!isRecord(item)) throw invalidResponse(itemField);
    assertExactKeys(item, ["node_type", "node_id", "version"], itemField);
    return {
      nodeType: readNodeType(item.node_type, `${itemField}.node_type`),
      nodeId: readUuid(item.node_id, `${itemField}.node_id`),
      version: readInteger(item.version, `${itemField}.version`, 1),
    };
  }, 1), `${field}.base_versions`, (item) => `${item.nodeType}:${item.nodeId}`);
  const changeSetValue = value.change_set;
  if (!isRecord(changeSetValue)) throw invalidResponse(`${field}.change_set`);
  assertExactKeys(changeSetValue, ["operation", "source", "target", "relation_type"], `${field}.change_set`);
  const readChangeEndpoint = (item: unknown, endpointField: string) => {
    if (!isRecord(item)) throw invalidResponse(endpointField);
    assertExactKeys(item, ["type", "id", "version"], endpointField);
    return {
      type: readNodeType(item.type, `${endpointField}.type`),
      id: readUuid(item.id, `${endpointField}.id`),
      version: readInteger(item.version, `${endpointField}.version`, 1),
    };
  };
  const evidenceRefs = unique(readArray(value.evidence_refs, `${field}.evidence_refs`, 100, (item, itemField) => {
    if (!isRecord(item)) throw invalidResponse(itemField);
    assertExactKeys(item, ["candidate_evidence_id", "semantic_hash"], itemField);
    return {
      candidateEvidenceId: readUuid(item.candidate_evidence_id, `${itemField}.candidate_evidence_id`),
      semanticHash: readHash(item.semantic_hash, `${itemField}.semantic_hash`),
    };
  }, 1), `${field}.evidence_refs`, (item) => item.candidateEvidenceId);
  return {
    id: readUuid(value.id, `${field}.id`),
    revisionNo: readInteger(value.revision_no, `${field}.revision_no`, 1),
    schemaVersion: readEnum(value.schema_version, `${field}.schema_version`, ["knowledge-relation-change/v1"] as const),
    targetRefs,
    baseVersions,
    changeSet: {
      operation: readEnum(changeSetValue.operation, `${field}.change_set.operation`, ["CREATE_RELATION"] as const),
      source: readChangeEndpoint(changeSetValue.source, `${field}.change_set.source`),
      target: readChangeEndpoint(changeSetValue.target, `${field}.change_set.target`),
      relationType: readRelationType(changeSetValue.relation_type, `${field}.change_set.relation_type`),
    },
    evidenceRefs,
    risk: readText(value.risk, `${field}.risk`, maxReasonBytes),
    rollbackPlan: readText(value.rollback_plan, `${field}.rollback_plan`, maxReasonBytes),
    changeHash: readHash(value.change_hash, `${field}.change_hash`),
    createdAt: readTimestamp(value.created_at, `${field}.created_at`),
  };
};

export const decodeSemanticLinkProposal = (value: unknown, request?: GetProposalInput): SemanticLinkProposal => {
  if (!isRecord(value)) throw invalidResponse("proposal");
  const proposalTypeValue = value.proposal_type;
  const proposalType = proposalTypeValue === undefined
    ? "file_patch"
    : readEnum(proposalTypeValue, "proposal.proposal_type", ["file_patch", "knowledge_change"] as const);
  if (proposalType === "file_patch") {
    assertExactKeys(value, ["proposal_type", "id", "workspace_id", "target_path", "status", "revision", "approval", "created_at", "updated_at"], "proposal");
    const id = readUuid(value.id, "proposal.id");
    if (request?.proposalId !== undefined && id !== request.proposalId) throw invalidResponse("proposal.id");
    return {
      proposalType,
      id,
      workspaceId: readUuid(value.workspace_id, "proposal.workspace_id"),
      targetPath: readPath(value.target_path, "proposal.target_path"),
      status: readProposalStatus(value.status, "proposal.status"),
      revision: readFilePatchRevision(value.revision, "proposal.revision"),
      approval: value.approval === undefined || value.approval === null ? null : readApproval(value.approval, "proposal.approval"),
      createdAt: readTimestamp(value.created_at, "proposal.created_at"),
      updatedAt: readTimestamp(value.updated_at, "proposal.updated_at"),
    };
  }
  assertExactKeys(value, ["proposal_type", "id", "workspace_id", "status", "revision", "approval", "created_at", "updated_at"], "proposal");
  const id = readUuid(value.id, "proposal.id");
  if (request?.proposalId !== undefined && id !== request.proposalId) throw invalidResponse("proposal.id");
  return {
    proposalType,
    id,
    workspaceId: readUuid(value.workspace_id, "proposal.workspace_id"),
    status: readProposalStatus(value.status, "proposal.status"),
    revision: readKnowledgeChangeRevision(value.revision, "proposal.revision"),
    approval: value.approval === undefined || value.approval === null ? null : readApproval(value.approval, "proposal.approval"),
    createdAt: readTimestamp(value.created_at, "proposal.created_at"),
    updatedAt: readTimestamp(value.updated_at, "proposal.updated_at"),
  };
};

export const decodeProposalApproval = (value: unknown): ProposalApproval =>
  readApproval(value, "approval");

export const decodeSemanticLinkProblem = (value: unknown, status: number | null = null): SemanticLinkProblem => {
  if (!isRecord(value)) throw new SemanticLinkApiError({
    errorCode: "INVALID_RESPONSE",
    message: "Semantic Link API 返回了无效 Problem。",
    retryable: false,
  }, status);
  assertExactKeys(value, ["error_code", "message", "retryable", "workflow_run_id", "details"], "problem");
  return {
    errorCode: readText(value.error_code, "problem.error_code", 128),
    message: readText(value.message, "problem.message", maxReasonBytes),
    retryable: readBoolean(value.retryable, "problem.retryable"),
    ...(value.workflow_run_id === undefined ? {} : { workflowRunId: readUuid(value.workflow_run_id, "problem.workflow_run_id") }),
    ...(value.details === undefined
      ? {}
      : {
        details: (() => {
          if (!isRecord(value.details)) throw invalidResponse("problem.details");
          return value.details;
        })(),
      }),
  };
};

const readJson = async (response: Response): Promise<unknown> => {
  try {
    return await response.json();
  } catch {
    throw new SemanticLinkApiError({
      errorCode: "INVALID_RESPONSE",
      message: "Semantic Link API 返回了无效 JSON。",
      retryable: false,
    }, response.status);
  }
};

const request = async <T>(path: string, init: RequestInit, decode: (value: unknown) => T): Promise<T> => {
  let response: Response;
  try {
    response = await fetch(`${apiBaseUrl}${path}`, init);
  } catch {
    throw new SemanticLinkApiError({
      errorCode: "NETWORK_ERROR",
      message: "无法连接 Semantic Link API。",
      retryable: true,
    });
  }
  const payload = await readJson(response);
  if (!response.ok) {
    try {
      throw new SemanticLinkApiError(decodeSemanticLinkProblem(payload, response.status), response.status);
    } catch (error) {
      if (error instanceof SemanticLinkApiError && error.errorCode !== "INVALID_RESPONSE") throw error;
      throw new SemanticLinkApiError({
        errorCode: "INVALID_RESPONSE",
        message: "Semantic Link API 返回了无效错误响应。",
        retryable: false,
      }, response.status, error instanceof Error ? { cause: error } : undefined);
    }
  }
  return decode(payload);
};

const validateIdempotencyKey = (value: string): string => {
  const key = readText(value, "idempotencyKey", 128, false, invalidRequest);
  if (key.length < 1 || key.length > 128) throw invalidRequest("idempotencyKey");
  return key;
};

const serializeScanScope = (scope: SemanticLinkScanScope): Record<string, unknown> => {
  if (scope.kind === "SMART_COLLECTION") {
    return {
      kind: scope.kind,
      collection_id: readUuid(scope.collectionId, "scope.collectionId", invalidRequest),
    };
  }
  return {
    kind: scope.kind,
    topic_id: readUuid(scope.topicId, "scope.topicId", invalidRequest),
  };
};

export const listSemanticLinkCandidates = async (
  input: ListSemanticLinkCandidatesInput,
  signal?: AbortSignal,
): Promise<SemanticLinkCandidatePage> => {
  const workspaceId = readUuid(input.workspaceId, "workspaceId", invalidRequest);
  if ((input.nodeType === undefined) !== (input.nodeId === undefined)) throw invalidRequest("nodeType/nodeId");
  const query = new URLSearchParams({ workspace_id: workspaceId });
  if (input.nodeType !== undefined && input.nodeId !== undefined) {
    query.set("node_type", readNodeType(input.nodeType, "nodeType", invalidRequest));
    query.set("node_id", readUuid(input.nodeId, "nodeId", invalidRequest));
  }
  if (input.statuses !== undefined) {
    const values = unique(input.statuses.map((item, index) =>
      readEnum(item, `statuses[${String(index)}]`, candidateStatuses, invalidRequest)), "statuses", (item) => item);
    values.forEach((item) => query.append("status", item));
  }
  if (input.relationTypes !== undefined) {
    const values = unique(input.relationTypes.map((item, index) =>
      readEnum(item, `relationTypes[${String(index)}]`, relationTypes, invalidRequest)), "relationTypes", (item) => item);
    values.forEach((item) => query.append("relation_type", item));
  }
  if (input.reopenedReasons !== undefined) {
    const values = unique(input.reopenedReasons.map((item, index) =>
      readEnum(item, `reopenedReasons[${String(index)}]`, reopenedReasons, invalidRequest)), "reopenedReasons", (item) => item);
    values.forEach((item) => query.append("reopened_reason", item));
  }
  if (input.minConfidence !== undefined) {
    query.set("min_confidence", String(readConfidence(input.minConfidence, "minConfidence", invalidRequest)));
  }
  if (input.cursor !== undefined) query.set("cursor", readCursor(input.cursor, "cursor", invalidRequest));
  if (input.limit !== undefined) query.set("limit", String(readInteger(input.limit, "limit", 1, 100, invalidRequest)));
  return request(`/api/v1/graph/candidates?${query.toString()}`, {
    method: "GET",
    headers: { Accept: "application/json" },
    ...(signal === undefined ? {} : { signal }),
  }, (payload) => decodeSemanticLinkCandidatePage(payload, input));
};

export const decideSemanticLinkCandidate = async (
  input: SemanticLinkCandidateDecisionInput,
): Promise<SemanticLinkDecisionReceipt> => {
  if (!isRecord(input)) throw invalidRequest("decision");
  const candidateId = readUuid(input.candidateId, "candidateId", invalidRequest);
  const workspaceId = readUuid(input.workspaceId, "workspaceId", invalidRequest);
  const action = readDecisionAction(input.action, "action", invalidRequest);
  const body: Record<string, unknown> = {
    workspace_id: workspaceId,
    action,
    expected_version: readInteger(input.expectedVersion, "expectedVersion", 1, Number.MAX_SAFE_INTEGER, invalidRequest),
  };
  const baseKeys = ["candidateId", "workspaceId", "action", "expectedVersion", "idempotencyKey"];
  switch (action) {
    case "CONFIRM":
      assertExactKeys(input, baseKeys, "decision", invalidRequest);
      break;
    case "CONFIRM_WITH_RELATION_TYPE":
      assertExactKeys(input, [...baseKeys, "relationType"], "decision", invalidRequest);
      body.relation_type = readRelationType(input.relationType, "relationType", invalidRequest);
      break;
    case "IGNORE":
    case "FALSE_POSITIVE":
      assertExactKeys(input, [...baseKeys, "reason"], "decision", invalidRequest);
      body.reason = readText(input.reason, "reason", maxReasonBytes, false, invalidRequest);
      break;
    case "DEFER":
      assertExactKeys(input, [...baseKeys, "reason", "deferredUntil"], "decision", invalidRequest);
      if (input.reason !== undefined) body.reason = readText(input.reason, "reason", maxReasonBytes, false, invalidRequest);
      if (input.deferredUntil !== undefined) body.deferred_until = input.deferredUntil === null
        ? null
        : readTimestamp(input.deferredUntil, "deferredUntil", invalidRequest);
      break;
    case "RESUME":
      assertExactKeys(input, [...baseKeys, "deferredUntil"], "decision", invalidRequest);
      if (input.deferredUntil !== undefined) {
        if (input.deferredUntil !== null) throw invalidRequest("deferredUntil");
        body.deferred_until = null;
      }
      break;
  }
  return request(`/api/v1/graph/candidates/${encodeURIComponent(candidateId)}/decisions`, {
    method: "POST",
    headers: {
      Accept: "application/json",
      "Content-Type": "application/json",
      "Idempotency-Key": validateIdempotencyKey(input.idempotencyKey),
    },
    body: JSON.stringify(body),
  }, (payload) => readDecisionReceipt(payload, input));
};

export const startSemanticLinkScan = async (input: StartSemanticLinkScanInput): Promise<SemanticLinkScanAcceptance> => {
  const workspaceId = readUuid(input.workspaceId, "workspaceId", invalidRequest);
  return request("/api/v1/graph/candidate-scans", {
    method: "POST",
    headers: {
      Accept: "application/json",
      "Content-Type": "application/json",
      "Idempotency-Key": validateIdempotencyKey(input.idempotencyKey),
    },
    body: JSON.stringify({
      workspace_id: workspaceId,
      scope: serializeScanScope(input.scope),
    }),
  }, (payload) => decodeSemanticLinkScanAcceptance(payload, workspaceId));
};

export const getSemanticLinkScan = async (
  input: GetSemanticLinkScanInput,
  signal?: AbortSignal,
): Promise<SemanticLinkScan> => {
  const scanId = readUuid(input.scanId, "scanId", invalidRequest);
  const workspaceId = readUuid(input.workspaceId, "workspaceId", invalidRequest);
  return request(`/api/v1/graph/candidate-scans/${encodeURIComponent(scanId)}?workspace_id=${encodeURIComponent(workspaceId)}`, {
    method: "GET",
    headers: { Accept: "application/json" },
    ...(signal === undefined ? {} : { signal }),
  }, (payload) => decodeSemanticLinkScan(payload, { scanId, workspaceId }));
};

export const getSemanticLinkProposal = async (
  input: GetProposalInput,
  signal?: AbortSignal,
): Promise<SemanticLinkProposal> => {
  const proposalId = readUuid(input.proposalId, "proposalId", invalidRequest);
  return request(`/api/v1/proposals/${encodeURIComponent(proposalId)}`, {
    method: "GET",
    headers: { Accept: "application/json" },
    ...(signal === undefined ? {} : { signal }),
  }, (payload) => decodeSemanticLinkProposal(payload, { proposalId }));
};

export const approveSemanticLinkProposal = async (
  input: ApproveProposalInput,
): Promise<ProposalApproval> => {
  const proposalId = readUuid(input.proposalId, "proposalId", invalidRequest);
  return request(`/api/v1/proposals/${encodeURIComponent(proposalId)}/approvals`, {
    method: "POST",
    headers: {
      Accept: "application/json",
      "Content-Type": "application/json",
    },
    body: JSON.stringify({
      revision_id: readUuid(input.revisionId, "revisionId", invalidRequest),
      change_hash: readHash(input.changeHash, "changeHash", false, invalidRequest),
      decision: readProposalDecision(input.decision, "decision", invalidRequest),
    }),
  }, decodeProposalApproval);
};
import { graphNodeRefIdentity, graphRelationTypeCompatible, isSymmetricGraphRelationType } from "./graph";
