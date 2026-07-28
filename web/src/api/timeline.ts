/** Timeline / Impact 的唯一 HTTP 边界；页面只消费这里验证后的领域投影。 */

import { authFetch } from "./auth";

export const timelineEventTypes = [
  "PROPOSAL_CREATED", "APPROVAL_GRANTED", "APPROVAL_REJECTED", "GIT_COMMITTED",
  "RELATION_CONFIRMED", "RELATION_DEPRECATED", "CONFLICT_OPENED", "CONFLICT_TRANSITIONED",
  "CONFLICT_RESOLVED", "VERSION_PUBLISHED", "VERSION_SUPERSEDED", "HEALTH_ISSUE_DETECTED",
  "HEALTH_ISSUE_RESOLVED", "IMPACT_ANALYZED", "ARTIFACT_GENERATED", "REVIEW_CARD_INVALIDATED",
  "CORRECTIVE_EVENT",
] as const;
export type TimelineEventType = (typeof timelineEventTypes)[number];

export const timelineAggregateTypes = [
  "PROPOSAL", "APPROVAL", "GIT_COMMIT", "TOPIC", "CLAIM", "RELATION", "CONFLICT",
  "DOCUMENT", "ARTICLE_REVISION", "HEALTH_ISSUE", "IMPACT_REPORT", "ARTIFACT", "REVIEW_CARD",
] as const;
export type TimelineAggregateType = (typeof timelineAggregateTypes)[number];

export const impactObjectTypes = [
  "TOPIC", "CLAIM", "RELATION", "CONFLICT", "HEALTH_ISSUE", "PROPOSAL", "ARTICLE_REVISION",
  "AUDIT_EVENT", "ARTIFACT", "REVIEW_CARD",
] as const;
export type ImpactObjectType = (typeof impactObjectTypes)[number];

export const impactActions = [
  "REVIEW", "REINDEX", "RESOLVE_CONFLICT", "REFRESH_HEALTH", "REGENERATE_ARTIFACT",
  "REVALIDATE_REVIEW_CARD", "NO_ACTION",
] as const;
export type ImpactAction = (typeof impactActions)[number];

export type TimelineOperatorType = "USER" | "API_TOKEN" | "SYSTEM" | "UNKNOWN";
export interface TimelineOperator {
  type: TimelineOperatorType;
  id?: string;
}

export interface TimelineCorrelation {
  proposalId?: string;
  approvalId?: string;
  workflowRunId?: string;
  auditEventId?: string;
  gitCommitRef?: string;
}

export interface ArtifactImpactBinding {
  artifactId: string;
  artifactVersion: number;
  revisionId: string;
  revisionNo: number;
  contentHash: string;
}

export interface ReviewCardImpactBinding {
  cardId: string;
  cardVersion: number;
  status: "DRAFT" | "APPROVED" | "INVALIDATED" | "REJECTED";
  fingerprint: string;
  claimId: string;
  evidenceBindingFingerprint: string;
}

export type EventOwnerBinding =
  | { artifact: ArtifactImpactBinding; reviewCard?: never }
  | { artifact?: never; reviewCard: ReviewCardImpactBinding };

interface TimelineEventBase {
  id: string;
  workspaceId: string;
  eventType: TimelineEventType;
  aggregateType: TimelineAggregateType;
  aggregateId?: string;
  sourceEventRef: string;
  sourceRef: string;
  eventVersion: number;
  summary: string;
  payload: Readonly<Record<string, unknown>>;
  correlation: TimelineCorrelation;
  occurredAt: string;
  createdAt: string;
}

export interface TimelineEventV1 extends TimelineEventBase {
  schemaVersion: "knowledge-event/v1";
  operator?: never;
  ownerBinding?: never;
}

export interface TimelineEventV2 extends TimelineEventBase {
  schemaVersion: "knowledge-event/v2";
  operator: TimelineOperator;
  ownerBinding: EventOwnerBinding | null;
}

export type TimelineEvent = TimelineEventV1 | TimelineEventV2;

interface ImpactObjectBase {
  id: string;
  workspaceId: string;
  version: number;
  action: ImpactAction;
  reason: string;
  requiresProposal: boolean;
}

export interface ArtifactImpactObject extends ImpactObjectBase {
  type: "ARTIFACT";
  action: "REGENERATE_ARTIFACT";
  requiresProposal: true;
  artifactBinding: ArtifactImpactBinding;
  reviewCardBinding?: never;
}

export interface ReviewCardImpactObject extends ImpactObjectBase {
  type: "REVIEW_CARD";
  action: "REVALIDATE_REVIEW_CARD";
  requiresProposal: true;
  artifactBinding?: never;
  reviewCardBinding: ReviewCardImpactBinding;
}

export interface LegacyImpactObject extends ImpactObjectBase {
  type: Exclude<ImpactObjectType, "ARTIFACT" | "REVIEW_CARD">;
  artifactBinding?: never;
  reviewCardBinding?: never;
}

export type ImpactObject = ArtifactImpactObject | ReviewCardImpactObject | LegacyImpactObject;

interface ImpactReportBase {
  id: string;
  workspaceId: string;
  sourceEventId: string;
  sourceEventRef: string;
  sourceEventVersion: number;
  status: "READY" | "STALE" | "FAILED";
  objects: ImpactObject[];
  summary: Readonly<Record<string, number>>;
  fingerprint: string;
  errorCode?: string;
  staleReason?: string;
  generatedAt: string;
  createdAt: string;
  version: number;
}

export interface ImpactReportV1 extends ImpactReportBase {
  schemaVersion: "impact-report/v1";
  analysisVersion: "impact-analysis/v1";
  supersedesReportId?: never;
  supersededByReportId?: never;
}

export interface ImpactReportV2 extends ImpactReportBase {
  schemaVersion: "impact-report/v2";
  analysisVersion: "impact-analysis/v2";
  supersedesReportId: string | null;
  supersededByReportId: string | null;
}

export type ImpactReport = ImpactReportV1 | ImpactReportV2;

export interface ImpactProposalDraft {
  id?: string;
  workspaceId: string;
  sourceEventId: string;
  operation: ImpactAction;
  targetType: ImpactObjectType;
  targetId: string;
  baseVersion: number;
  reason: string;
  requiresApproval: true;
  requiresWriteAuthorization: true;
}

export interface TimelinePage {
  workspaceId: string;
  items: TimelineEvent[];
  nextCursor?: string;
}

export interface ImpactAnalysisResult {
  report: ImpactReport;
  proposalDrafts: ImpactProposalDraft[];
  replayed: boolean;
}

interface DownstreamUpdatePayloadBase {
  workspaceId: string;
  sourceReport: { id: string; analysisVersion: "impact-analysis/v2"; fingerprint: string };
  sourceEvent: { id: string; eventVersion: number };
  targetId: string;
  baseVersion: number;
  reason: string;
  schemaVersion: "impact-downstream-update/v1";
}

export type DownstreamUpdatePayload = DownstreamUpdatePayloadBase & (
  | {
      targetType: "ARTIFACT";
      action: "REGENERATE_ARTIFACT";
      artifactBinding: ArtifactImpactBinding;
      reviewCardBinding?: never;
    }
  | {
      targetType: "REVIEW_CARD";
      action: "REVALIDATE_REVIEW_CARD";
      artifactBinding?: never;
      reviewCardBinding: ReviewCardImpactBinding;
    }
);

export interface DownstreamUpdateApproval {
  id: string;
  proposalId: string;
  revisionId: string;
  changeHash: string;
  decision: "approved" | "rejected";
  decidedAt: string;
}

export interface DownstreamUpdateProposal {
  proposalType: "downstream_update";
  id: string;
  workspaceId: string;
  status: "ready_for_review" | "approved" | "rejected";
  riskLevel: "HIGH";
  revision: {
    id: string;
    revisionNo: number;
    update: DownstreamUpdatePayload;
    risk: string;
    rollbackPlan: string;
    changeHash: string;
    createdAt: string;
  };
  approval: DownstreamUpdateApproval | null;
  replayed: boolean;
  createdAt: string;
  updatedAt: string;
}

export interface TimelineFilter {
  eventTypes?: TimelineEventType[];
  aggregateType?: TimelineAggregateType;
  aggregateId?: string;
  sourceEventRef?: string;
  occurredAfter?: string;
  occurredBefore?: string;
}

export interface ListTimelineInput extends TimelineFilter {
  workspaceId: string;
  cursor?: string;
  limit?: number;
}

export interface AnalyzeImpactInput {
  workspaceId: string;
  eventId: string;
  idempotencyKey: string;
}

export interface CreateDownstreamUpdateProposalInput {
  workspaceId: string;
  reportId: string;
  targetType: "ARTIFACT" | "REVIEW_CARD";
  targetId: string;
  action: "REGENERATE_ARTIFACT" | "REVALIDATE_REVIEW_CARD";
  idempotencyKey: string;
}

export class TimelineApiError extends Error {
  readonly code: "INVALID_REQUEST" | "INVALID_RESPONSE" | "HTTP_ERROR" | "NETWORK_ERROR";
  readonly errorCode: string;
  readonly retryable: boolean;
  readonly status: number | null;

  constructor(
    code: TimelineApiError["code"],
    errorCode: string,
    message: string,
    retryable: boolean,
    status: number | null = null,
    options?: ErrorOptions,
  ) {
    super(message, options);
    this.name = "TimelineApiError";
    this.code = code;
    this.errorCode = errorCode;
    this.retryable = retryable;
    this.status = status;
  }
}

const uuidPattern = /^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/;
const hashPattern = /^[0-9a-f]{64}$/;
const timestampPattern = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{1,9})?(?:Z|[+-]\d{2}:\d{2})$/;
const textEncoder = new TextEncoder();
const maxCursorBytes = 4096;
const maxIdempotencyKeyBytes = 128;
const maxTimelineItems = 100;
const maxImpactObjects = 500;

const isRecord = (value: unknown): value is Record<string, unknown> =>
  typeof value === "object" && value !== null && !Array.isArray(value);

const invalidResponse = (field: string, status: number | null = null): TimelineApiError =>
  new TimelineApiError("INVALID_RESPONSE", "INVALID_RESPONSE", `Timeline/Impact 响应字段无效：${field}`, false, status);

const invalidRequest = (field: string): TimelineApiError =>
  new TimelineApiError("INVALID_REQUEST", "INVALID_REQUEST", `Timeline/Impact 请求字段无效：${field}`, false);

const exact = (value: Record<string, unknown>, keys: readonly string[], field: string): void => {
  const allowed = new Set(keys);
  if (Object.keys(value).some((key) => !allowed.has(key))) throw invalidResponse(field);
};

const stringValue = (value: unknown, field: string, allowEmpty = false): string => {
  if (typeof value !== "string" || (!allowEmpty && value.trim() === "")) throw invalidResponse(field);
  return value;
};

const safeText = (value: unknown, field: string, maximum: number, allowEmpty = false): string => {
  const result = stringValue(value, field, allowEmpty);
  if (result.trim() !== result || textEncoder.encode(result).byteLength > maximum || /[\u0000-\u001f\u007f]/.test(result)) {
    throw invalidResponse(field);
  }
  return result;
};

const uuid = (value: unknown, field: string): string => {
  const result = stringValue(value, field);
  if (!uuidPattern.test(result)) throw invalidResponse(field);
  return result;
};

const hash = (value: unknown, field: string): string => {
  const result = stringValue(value, field);
  if (!hashPattern.test(result)) throw invalidResponse(field);
  return result;
};

const integer = (value: unknown, field: string, minimum = 0, maximum?: number): number => {
  if (typeof value !== "number" || !Number.isSafeInteger(value) || value < minimum || (maximum !== undefined && value > maximum)) {
    throw invalidResponse(field);
  }
  return value;
};

const booleanValue = (value: unknown, field: string): boolean => {
  if (typeof value !== "boolean") throw invalidResponse(field);
  return value;
};

const enumValue = <T extends string>(value: unknown, values: readonly T[], field: string): T => {
  const result = values.find((item) => item === value);
  if (result === undefined) throw invalidResponse(field);
  return result;
};

const timestamp = (value: unknown, field: string): string => {
  const result = stringValue(value, field);
  if (!timestampPattern.test(result) || !Number.isFinite(Date.parse(result))) throw invalidResponse(field);
  const matched = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})/.exec(result);
  if (matched === null) throw invalidResponse(field);
  const year = Number(matched[1]);
  const month = Number(matched[2]);
  const day = Number(matched[3]);
  const hour = Number(matched[4]);
  const minute = Number(matched[5]);
  const second = Number(matched[6]);
  const days = month === 2
    ? year % 4 === 0 && (year % 100 !== 0 || year % 400 === 0) ? 29 : 28
    : month === 4 || month === 6 || month === 9 || month === 11 ? 30 : 31;
  if (year < 1 || month < 1 || month > 12 || day < 1 || day > days || hour > 23 || minute > 59 || second > 59) {
    throw invalidResponse(field);
  }
  return result;
};

export const compareTimelineTimestamps = (left: string, right: string): number => {
  const leftMilliseconds = Date.parse(left);
  const rightMilliseconds = Date.parse(right);
  if (leftMilliseconds !== rightMilliseconds) return leftMilliseconds < rightMilliseconds ? -1 : 1;
  const subMillisecondNanoseconds = (value: string): number => {
    const fraction = /T\d{2}:\d{2}:\d{2}(?:\.(\d{1,9}))?/.exec(value)?.[1] ?? "";
    return Number(fraction.padEnd(9, "0").slice(3));
  };
  const leftNanoseconds = subMillisecondNanoseconds(left);
  const rightNanoseconds = subMillisecondNanoseconds(right);
  return leftNanoseconds === rightNanoseconds ? 0 : leftNanoseconds < rightNanoseconds ? -1 : 1;
};

const optionalUuid = (value: unknown, field: string): string | undefined =>
  value === undefined ? undefined : uuid(value, field);

const nullableUuid = (value: unknown, field: string): string | null =>
  value === null ? null : uuid(value, field);

const cursor = (value: unknown, field: string): string | undefined => {
  if (value === undefined) return undefined;
  const result = safeText(value, field, maxCursorBytes);
  return result;
};

const decodeArtifactBinding = (value: unknown, field: string): ArtifactImpactBinding => {
  if (!isRecord(value)) throw invalidResponse(field);
  exact(value, ["artifact_id", "artifact_version", "revision_id", "revision_no", "content_hash"], field);
  const artifactId = uuid(value.artifact_id, `${field}.artifact_id`);
  const revisionId = uuid(value.revision_id, `${field}.revision_id`);
  if (artifactId === revisionId) throw invalidResponse(field);
  return {
    artifactId,
    artifactVersion: integer(value.artifact_version, `${field}.artifact_version`, 1),
    revisionId,
    revisionNo: integer(value.revision_no, `${field}.revision_no`, 1),
    contentHash: hash(value.content_hash, `${field}.content_hash`),
  };
};

const decodeReviewCardBinding = (value: unknown, field: string): ReviewCardImpactBinding => {
  if (!isRecord(value)) throw invalidResponse(field);
  exact(value, ["card_id", "card_version", "status", "fingerprint", "claim_id", "evidence_binding_fingerprint"], field);
  const cardId = uuid(value.card_id, `${field}.card_id`);
  const claimId = uuid(value.claim_id, `${field}.claim_id`);
  if (cardId === claimId) throw invalidResponse(field);
  return {
    cardId,
    cardVersion: integer(value.card_version, `${field}.card_version`, 1),
    status: enumValue(value.status, ["DRAFT", "APPROVED", "INVALIDATED", "REJECTED"] as const, `${field}.status`),
    fingerprint: hash(value.fingerprint, `${field}.fingerprint`),
    claimId,
    evidenceBindingFingerprint: hash(value.evidence_binding_fingerprint, `${field}.evidence_binding_fingerprint`),
  };
};

const decodeOwnerBinding = (value: unknown, field: string): EventOwnerBinding => {
  if (!isRecord(value)) throw invalidResponse(field);
  exact(value, ["artifact", "review_card"], field);
  const artifact = value.artifact === undefined ? undefined : decodeArtifactBinding(value.artifact, `${field}.artifact`);
  const reviewCard = value.review_card === undefined ? undefined : decodeReviewCardBinding(value.review_card, `${field}.review_card`);
  if ((artifact === undefined) === (reviewCard === undefined)) throw invalidResponse(field);
  if (artifact !== undefined) return { artifact };
  if (reviewCard !== undefined) return { reviewCard };
  throw invalidResponse(field);
};

const decodeCorrelation = (value: unknown, field: string): TimelineCorrelation => {
  if (!isRecord(value)) throw invalidResponse(field);
  exact(value, ["proposal_id", "approval_id", "workflow_run_id", "audit_event_id", "git_commit_ref"], field);
  const proposalId = optionalUuid(value.proposal_id, `${field}.proposal_id`);
  const approvalId = optionalUuid(value.approval_id, `${field}.approval_id`);
  const workflowRunId = optionalUuid(value.workflow_run_id, `${field}.workflow_run_id`);
  const auditEventId = optionalUuid(value.audit_event_id, `${field}.audit_event_id`);
  const gitCommitRef = value.git_commit_ref === undefined ? undefined : safeText(value.git_commit_ref, `${field}.git_commit_ref`, 512);
  return {
    ...(proposalId === undefined ? {} : { proposalId }),
    ...(approvalId === undefined ? {} : { approvalId }),
    ...(workflowRunId === undefined ? {} : { workflowRunId }),
    ...(auditEventId === undefined ? {} : { auditEventId }),
    ...(gitCommitRef === undefined ? {} : { gitCommitRef }),
  };
};

const decodeOperator = (value: unknown, field: string): TimelineOperator => {
  if (!isRecord(value)) throw invalidResponse(field);
  exact(value, ["type", "id"], field);
  const type = enumValue(value.type, ["USER", "API_TOKEN", "SYSTEM", "UNKNOWN"] as const, `${field}.type`);
  const id = optionalUuid(value.id, `${field}.id`);
  if ((type === "SYSTEM" || type === "UNKNOWN") && id !== undefined) throw invalidResponse(field);
  return { type, ...(id === undefined ? {} : { id }) };
};

export const decodeTimelineEvent = (value: unknown): TimelineEvent => {
  if (!isRecord(value)) throw invalidResponse("event");
  const schemaVersion = enumValue(value.schema_version, ["knowledge-event/v1", "knowledge-event/v2"] as const, "event.schema_version");
  const commonKeys = [
    "id", "workspace_id", "event_type", "aggregate_type", "aggregate_id", "source_event_ref", "source_ref",
    "event_version", "schema_version", "summary", "payload", "correlation", "occurred_at", "created_at",
  ];
  exact(value, schemaVersion === "knowledge-event/v1" ? commonKeys : [...commonKeys, "operator", "owner_binding"], "event");
  if (!isRecord(value.payload)) throw invalidResponse("event.payload");
  const event: TimelineEventBase = {
    id: uuid(value.id, "event.id"),
    workspaceId: uuid(value.workspace_id, "event.workspace_id"),
    eventType: enumValue(value.event_type, timelineEventTypes, "event.event_type"),
    aggregateType: enumValue(value.aggregate_type, timelineAggregateTypes, "event.aggregate_type"),
    ...(value.aggregate_id === undefined ? {} : { aggregateId: uuid(value.aggregate_id, "event.aggregate_id") }),
    sourceEventRef: safeText(value.source_event_ref, "event.source_event_ref", 512),
    sourceRef: safeText(value.source_ref, "event.source_ref", 512),
    eventVersion: integer(value.event_version, "event.event_version", 1),
    summary: safeText(value.summary, "event.summary", 4096, true),
    payload: value.payload,
    correlation: decodeCorrelation(value.correlation, "event.correlation"),
    occurredAt: timestamp(value.occurred_at, "event.occurred_at"),
    createdAt: timestamp(value.created_at, "event.created_at"),
  };
  if (compareTimelineTimestamps(event.createdAt, event.occurredAt) < 0) throw invalidResponse("event.timestamps");
  if (schemaVersion === "knowledge-event/v1") {
    if (event.eventType === "ARTIFACT_GENERATED" || event.eventType === "REVIEW_CARD_INVALIDATED") throw invalidResponse("event.schema_version");
    return { ...event, schemaVersion };
  }
  if (!Object.hasOwn(value, "owner_binding")) throw invalidResponse("event.owner_binding");
  const ownerBinding = value.owner_binding === null ? null : decodeOwnerBinding(value.owner_binding, "event.owner_binding");
  const v2 = { ...event, schemaVersion, operator: decodeOperator(value.operator, "event.operator"), ownerBinding } satisfies TimelineEventV2;
  if (v2.eventType === "ARTIFACT_GENERATED") {
    if (v2.aggregateType !== "ARTIFACT" || v2.aggregateId === undefined || v2.ownerBinding === null || !("artifact" in v2.ownerBinding) || v2.ownerBinding.artifact.artifactId !== v2.aggregateId) {
      throw invalidResponse("event.artifact_binding");
    }
  } else if (v2.eventType === "REVIEW_CARD_INVALIDATED") {
    if (v2.aggregateType !== "REVIEW_CARD" || v2.aggregateId === undefined || v2.ownerBinding === null || !("reviewCard" in v2.ownerBinding) || v2.ownerBinding.reviewCard.cardId !== v2.aggregateId || v2.ownerBinding.reviewCard.status !== "INVALIDATED") {
      throw invalidResponse("event.review_card_binding");
    }
  } else if (v2.ownerBinding !== null) {
    throw invalidResponse("event.owner_binding");
  }
  return v2;
};

const decodeImpactObject = (value: unknown, field: string): ImpactObject => {
  if (!isRecord(value)) throw invalidResponse(field);
  const type = enumValue(value.type, impactObjectTypes, `${field}.type`);
  const commonKeys = ["type", "id", "workspace_id", "version", "action", "reason", "requires_proposal"];
  if (type === "ARTIFACT") exact(value, [...commonKeys, "artifact_binding"], field);
  else if (type === "REVIEW_CARD") exact(value, [...commonKeys, "review_card_binding"], field);
  else exact(value, commonKeys, field);
  const base: ImpactObjectBase = {
    id: uuid(value.id, `${field}.id`),
    workspaceId: uuid(value.workspace_id, `${field}.workspace_id`),
    version: integer(value.version, `${field}.version`, 1),
    action: enumValue(value.action, impactActions, `${field}.action`),
    reason: safeText(value.reason, `${field}.reason`, 4096, true),
    requiresProposal: booleanValue(value.requires_proposal, `${field}.requires_proposal`),
  };
  if (base.action === "NO_ACTION" && base.requiresProposal) throw invalidResponse(`${field}.requires_proposal`);
  if (type === "ARTIFACT") {
    const artifactBinding = decodeArtifactBinding(value.artifact_binding, `${field}.artifact_binding`);
    if (artifactBinding.artifactId !== base.id || artifactBinding.artifactVersion !== base.version || base.action !== "REGENERATE_ARTIFACT" || !base.requiresProposal) {
      throw invalidResponse(field);
    }
    return { ...base, type, action: "REGENERATE_ARTIFACT", requiresProposal: true, artifactBinding };
  }
  if (type === "REVIEW_CARD") {
    const reviewCardBinding = decodeReviewCardBinding(value.review_card_binding, `${field}.review_card_binding`);
    if (reviewCardBinding.cardId !== base.id || reviewCardBinding.cardVersion !== base.version || base.action !== "REVALIDATE_REVIEW_CARD" || !base.requiresProposal) {
      throw invalidResponse(field);
    }
    return { ...base, type, action: "REVALIDATE_REVIEW_CARD", requiresProposal: true, reviewCardBinding };
  }
  return { ...base, type };
};

const summarizeObjects = (objects: readonly ImpactObject[]): Record<string, number> => {
  const summary: Record<string, number> = {};
  for (const object of objects) {
    summary[object.type] = (summary[object.type] ?? 0) + 1;
    const action = `action:${object.action}`;
    summary[action] = (summary[action] ?? 0) + 1;
    if (object.requiresProposal) summary.requires_proposal = (summary.requires_proposal ?? 0) + 1;
  }
  return summary;
};

const decodeSummary = (value: unknown, objects: readonly ImpactObject[], field: string): Readonly<Record<string, number>> => {
  if (!isRecord(value)) throw invalidResponse(field);
  const expected = summarizeObjects(objects);
  const actualKeys = Object.keys(value).sort();
  const expectedKeys = Object.keys(expected).sort();
  if (actualKeys.length !== expectedKeys.length || actualKeys.some((key, index) => key !== expectedKeys[index])) throw invalidResponse(field);
  const result: Record<string, number> = {};
  for (const key of expectedKeys) {
    const count = integer(value[key], `${field}.${key}`, 1, maxImpactObjects);
    if (count !== expected[key]) throw invalidResponse(`${field}.${key}`);
    result[key] = count;
  }
  return result;
};

const optionalSafeText = (value: unknown, field: string, maximum: number): string | undefined =>
  value === undefined ? undefined : safeText(value, field, maximum);

export const decodeImpactReport = (value: unknown): ImpactReport => {
  if (!isRecord(value)) throw invalidResponse("impact_report");
  const schemaVersion = enumValue(value.schema_version, ["impact-report/v1", "impact-report/v2"] as const, "impact_report.schema_version");
  const baseKeys = [
    "id", "workspace_id", "source_event_id", "source_event_ref", "source_event_version", "status", "objects", "summary",
    "fingerprint", "error_code", "stale_reason", "schema_version", "generated_at", "created_at", "version",
  ];
  exact(value, schemaVersion === "impact-report/v1" ? baseKeys : [...baseKeys, "analysis_version", "supersedes_report_id", "superseded_by_report_id"], "impact_report");
  if (!Array.isArray(value.objects) || value.objects.length > maxImpactObjects) throw invalidResponse("impact_report.objects");
  const objects = value.objects.map((item, index) => decodeImpactObject(item, `impact_report.objects[${String(index)}]`));
  for (let index = 1; index < objects.length; index += 1) {
    const previous = objects[index - 1];
    const current = objects[index];
    if (previous === undefined || current === undefined || `${previous.type}:${previous.id}` >= `${current.type}:${current.id}`) {
      throw invalidResponse("impact_report.objects");
    }
  }
  const errorCode = optionalSafeText(value.error_code, "impact_report.error_code", 256);
  const staleReason = optionalSafeText(value.stale_reason, "impact_report.stale_reason", 4096);
  const report: ImpactReportBase = {
    id: uuid(value.id, "impact_report.id"),
    workspaceId: uuid(value.workspace_id, "impact_report.workspace_id"),
    sourceEventId: uuid(value.source_event_id, "impact_report.source_event_id"),
    sourceEventRef: safeText(value.source_event_ref, "impact_report.source_event_ref", 512),
    sourceEventVersion: integer(value.source_event_version, "impact_report.source_event_version", 1),
    status: enumValue(value.status, ["READY", "STALE", "FAILED"] as const, "impact_report.status"),
    objects,
    summary: decodeSummary(value.summary, objects, "impact_report.summary"),
    fingerprint: hash(value.fingerprint, "impact_report.fingerprint"),
    ...(errorCode === undefined ? {} : { errorCode }),
    ...(staleReason === undefined ? {} : { staleReason }),
    generatedAt: timestamp(value.generated_at, "impact_report.generated_at"),
    createdAt: timestamp(value.created_at, "impact_report.created_at"),
    version: integer(value.version, "impact_report.version", 1),
  };
  if (objects.some((object) => object.workspaceId !== report.workspaceId) || compareTimelineTimestamps(report.createdAt, report.generatedAt) < 0) {
    throw invalidResponse("impact_report.binding");
  }
  if ((report.status === "FAILED") !== (report.errorCode !== undefined) || (report.status === "STALE") !== (report.staleReason !== undefined) || (report.status !== "FAILED" && report.errorCode !== undefined) || (report.status !== "STALE" && report.staleReason !== undefined)) {
    throw invalidResponse("impact_report.status");
  }
  if (schemaVersion === "impact-report/v1") {
    if (objects.some((object) => object.type === "ARTIFACT" || object.type === "REVIEW_CARD")) throw invalidResponse("impact_report.objects");
    return { ...report, schemaVersion, analysisVersion: "impact-analysis/v1" };
  }
  if (value.analysis_version !== "impact-analysis/v2") throw invalidResponse("impact_report.analysis_version");
  const supersedesReportId = nullableUuid(value.supersedes_report_id, "impact_report.supersedes_report_id");
  const supersededByReportId = nullableUuid(value.superseded_by_report_id, "impact_report.superseded_by_report_id");
  if (supersedesReportId === report.id || supersededByReportId === report.id || (supersedesReportId !== null && supersedesReportId === supersededByReportId)) {
    throw invalidResponse("impact_report.supersession");
  }
  return { ...report, schemaVersion, analysisVersion: "impact-analysis/v2", supersedesReportId, supersededByReportId };
};

const decodeProposalDraft = (value: unknown, field: string): ImpactProposalDraft => {
  if (!isRecord(value)) throw invalidResponse(field);
  exact(value, ["id", "workspace_id", "source_event_id", "operation", "target_type", "target_id", "base_version", "reason", "requires_approval", "requires_write_authorization"], field);
  const id = value.id === undefined ? undefined : safeText(value.id, `${field}.id`, 256);
  const operation = enumValue(value.operation, impactActions, `${field}.operation`);
  const targetType = enumValue(value.target_type, impactObjectTypes, `${field}.target_type`);
  if (operation === "NO_ACTION" || !booleanValue(value.requires_approval, `${field}.requires_approval`) || !booleanValue(value.requires_write_authorization, `${field}.requires_write_authorization`)) {
    throw invalidResponse(field);
  }
  return {
    ...(id === undefined ? {} : { id }),
    workspaceId: uuid(value.workspace_id, `${field}.workspace_id`),
    sourceEventId: uuid(value.source_event_id, `${field}.source_event_id`),
    operation,
    targetType,
    targetId: uuid(value.target_id, `${field}.target_id`),
    baseVersion: integer(value.base_version, `${field}.base_version`, 1),
    reason: safeText(value.reason, `${field}.reason`, 4096, true),
    requiresApproval: true,
    requiresWriteAuthorization: true,
  };
};

export const decodeTimelinePage = (value: unknown): TimelinePage => {
  if (!isRecord(value)) throw invalidResponse("timeline_page");
  exact(value, ["workspace_id", "items", "next_cursor"], "timeline_page");
  if (!Array.isArray(value.items) || value.items.length > maxTimelineItems) throw invalidResponse("timeline_page.items");
  const workspaceId = uuid(value.workspace_id, "timeline_page.workspace_id");
  const items = value.items.map(decodeTimelineEvent);
  if (items.some((item) => item.workspaceId !== workspaceId) || new Set(items.map((item) => item.id)).size !== items.length) throw invalidResponse("timeline_page.items");
  for (let index = 1; index < items.length; index += 1) {
    const previous = items[index - 1];
    const current = items[index];
    if (previous === undefined || current === undefined) throw invalidResponse("timeline_page.items");
    const timeOrder = compareTimelineTimestamps(previous.occurredAt, current.occurredAt);
    if (timeOrder < 0 || timeOrder === 0 && previous.id <= current.id) throw invalidResponse("timeline_page.items");
  }
  const nextCursor = cursor(value.next_cursor, "timeline_page.next_cursor");
  return { workspaceId, items, ...(nextCursor === undefined ? {} : { nextCursor }) };
};

export const decodeImpactAnalysisResult = (value: unknown): ImpactAnalysisResult => {
  if (!isRecord(value)) throw invalidResponse("impact_analysis");
  exact(value, ["report", "proposal_drafts", "replayed"], "impact_analysis");
  if (!Array.isArray(value.proposal_drafts) || value.proposal_drafts.length > maxImpactObjects) throw invalidResponse("impact_analysis.proposal_drafts");
  const report = decodeImpactReport(value.report);
  const proposalDrafts = value.proposal_drafts.map((item, index) => decodeProposalDraft(item, `impact_analysis.proposal_drafts[${String(index)}]`));
  if (proposalDrafts.some((draft) => draft.workspaceId !== report.workspaceId || draft.sourceEventId !== report.sourceEventId)) throw invalidResponse("impact_analysis.proposal_drafts");
  return { report, proposalDrafts, replayed: booleanValue(value.replayed, "impact_analysis.replayed") };
};

const decodeDownstreamUpdatePayload = (value: unknown, field: string): DownstreamUpdatePayload => {
  if (!isRecord(value) || !isRecord(value.source_report) || !isRecord(value.source_event)) throw invalidResponse(field);
  exact(value, ["workspace_id", "source_report", "source_event", "target_type", "target_id", "base_version", "action", "artifact_binding", "review_card_binding", "reason", "schema_version"], field);
  exact(value.source_report, ["id", "analysis_version", "fingerprint"], `${field}.source_report`);
  exact(value.source_event, ["id", "event_version"], `${field}.source_event`);
  const targetType = enumValue(value.target_type, ["ARTIFACT", "REVIEW_CARD"] as const, `${field}.target_type`);
  const action = enumValue(value.action, ["REGENERATE_ARTIFACT", "REVALIDATE_REVIEW_CARD"] as const, `${field}.action`);
  const artifactBinding = value.artifact_binding === undefined ? undefined : decodeArtifactBinding(value.artifact_binding, `${field}.artifact_binding`);
  const reviewCardBinding = value.review_card_binding === undefined ? undefined : decodeReviewCardBinding(value.review_card_binding, `${field}.review_card_binding`);
  if (value.schema_version !== "impact-downstream-update/v1" || value.source_report.analysis_version !== "impact-analysis/v2" || (artifactBinding === undefined) === (reviewCardBinding === undefined)) {
    throw invalidResponse(field);
  }
  const targetId = uuid(value.target_id, `${field}.target_id`);
  const baseVersion = integer(value.base_version, `${field}.base_version`, 1);
  const base: DownstreamUpdatePayloadBase = {
    workspaceId: uuid(value.workspace_id, `${field}.workspace_id`),
    sourceReport: { id: uuid(value.source_report.id, `${field}.source_report.id`), analysisVersion: "impact-analysis/v2", fingerprint: hash(value.source_report.fingerprint, `${field}.source_report.fingerprint`) },
    sourceEvent: { id: uuid(value.source_event.id, `${field}.source_event.id`), eventVersion: integer(value.source_event.event_version, `${field}.source_event.event_version`, 1) },
    targetId,
    baseVersion,
    reason: safeText(value.reason, `${field}.reason`, 4096),
    schemaVersion: "impact-downstream-update/v1",
  };
  if (targetType === "ARTIFACT") {
    if (action !== "REGENERATE_ARTIFACT" || artifactBinding === undefined || reviewCardBinding !== undefined || artifactBinding.artifactId !== targetId || artifactBinding.artifactVersion !== baseVersion) {
      throw invalidResponse(field);
    }
    return { ...base, targetType, action, artifactBinding };
  }
  if (action !== "REVALIDATE_REVIEW_CARD" || reviewCardBinding === undefined || artifactBinding !== undefined || reviewCardBinding.cardId !== targetId || reviewCardBinding.cardVersion !== baseVersion) {
    throw invalidResponse(field);
  }
  return { ...base, targetType, action, reviewCardBinding };
};

const decodeDownstreamUpdateApproval = (
  value: unknown,
  expectedProposalId: string,
  expectedRevisionId: string,
  expectedChangeHash: string,
): DownstreamUpdateApproval => {
  if (!isRecord(value)) throw invalidResponse("downstream_update_proposal.approval");
  exact(value, ["id", "proposal_id", "revision_id", "change_hash", "decision", "decided_at"], "downstream_update_proposal.approval");
  const proposalId = uuid(value.proposal_id, "downstream_update_proposal.approval.proposal_id");
  const revisionId = uuid(value.revision_id, "downstream_update_proposal.approval.revision_id");
  const changeHash = hash(value.change_hash, "downstream_update_proposal.approval.change_hash");
  if (proposalId !== expectedProposalId || revisionId !== expectedRevisionId || changeHash !== expectedChangeHash) {
    throw invalidResponse("downstream_update_proposal.approval.binding");
  }
  return {
    id: uuid(value.id, "downstream_update_proposal.approval.id"),
    proposalId,
    revisionId,
    changeHash,
    decision: enumValue(value.decision, ["approved", "rejected"] as const, "downstream_update_proposal.approval.decision"),
    decidedAt: timestamp(value.decided_at, "downstream_update_proposal.approval.decided_at"),
  };
};

export const decodeDownstreamUpdateProposal = (value: unknown): DownstreamUpdateProposal => {
  if (!isRecord(value) || !isRecord(value.revision)) throw invalidResponse("downstream_update_proposal");
  exact(value, ["proposal_type", "id", "workspace_id", "status", "risk_level", "revision", "approval", "replayed", "created_at", "updated_at"], "downstream_update_proposal");
  exact(value.revision, ["id", "revision_no", "update", "risk", "rollback_plan", "change_hash", "created_at"], "downstream_update_proposal.revision");
  if (value.proposal_type !== "downstream_update" || value.risk_level !== "HIGH") throw invalidResponse("downstream_update_proposal");
  const proposalId = uuid(value.id, "downstream_update_proposal.id");
  const workspaceId = uuid(value.workspace_id, "downstream_update_proposal.workspace_id");
  const status = enumValue(value.status, ["ready_for_review", "approved", "rejected"] as const, "downstream_update_proposal.status");
  const revisionId = uuid(value.revision.id, "downstream_update_proposal.revision.id");
  const changeHash = hash(value.revision.change_hash, "downstream_update_proposal.revision.change_hash");
  const update = decodeDownstreamUpdatePayload(value.revision.update, "downstream_update_proposal.revision.update");
  if (update.workspaceId !== workspaceId) throw invalidResponse("downstream_update_proposal.workspace_id");
  const approval = value.approval === null
    ? null
    : decodeDownstreamUpdateApproval(value.approval, proposalId, revisionId, changeHash);
  if (
    status === "ready_for_review" && approval !== null ||
    status === "approved" && approval?.decision !== "approved" ||
    status === "rejected" && approval?.decision !== "rejected"
  ) throw invalidResponse("downstream_update_proposal.approval");
  const createdAt = timestamp(value.created_at, "downstream_update_proposal.created_at");
  const updatedAt = timestamp(value.updated_at, "downstream_update_proposal.updated_at");
  const revisionCreatedAt = timestamp(value.revision.created_at, "downstream_update_proposal.revision.created_at");
  if (
    compareTimelineTimestamps(updatedAt, createdAt) < 0 ||
    compareTimelineTimestamps(revisionCreatedAt, createdAt) < 0 ||
    approval !== null && (compareTimelineTimestamps(approval.decidedAt, createdAt) < 0 || compareTimelineTimestamps(updatedAt, approval.decidedAt) < 0)
  ) throw invalidResponse("downstream_update_proposal.timestamps");
  const replayed = booleanValue(value.replayed, "downstream_update_proposal.replayed");
  if (!replayed && status !== "ready_for_review") throw invalidResponse("downstream_update_proposal.replayed");
  return {
    proposalType: "downstream_update",
    id: proposalId,
    workspaceId,
    status,
    riskLevel: "HIGH",
    revision: {
      id: revisionId,
      revisionNo: integer(value.revision.revision_no, "downstream_update_proposal.revision.revision_no", 1),
      update,
      risk: safeText(value.revision.risk, "downstream_update_proposal.revision.risk", 4096),
      rollbackPlan: safeText(value.revision.rollback_plan, "downstream_update_proposal.revision.rollback_plan", 4096),
      changeHash,
      createdAt: revisionCreatedAt,
    },
    approval,
    replayed,
    createdAt,
    updatedAt,
  };
};

class StrictJsonParser {
  private index = 0;
  private depth = 0;

  constructor(private readonly source: string) {}

  parse(): unknown {
    this.skipWhitespace();
    const value = this.parseValue();
    this.skipWhitespace();
    if (this.index !== this.source.length) throw new SyntaxError("trailing JSON data");
    return value;
  }

  private parseValue(): unknown {
    const current = this.source[this.index];
    if (current === "{") return this.parseObject();
    if (current === "[") return this.parseArray();
    if (current === '"') return this.parseString();
    if (this.source.startsWith("true", this.index)) { this.index += 4; return true; }
    if (this.source.startsWith("false", this.index)) { this.index += 5; return false; }
    if (this.source.startsWith("null", this.index)) { this.index += 4; return null; }
    const matched = /^-?(?:0|[1-9]\d*)(?:\.\d+)?(?:[eE][+-]?\d+)?/.exec(this.source.slice(this.index));
    if (matched === null) throw new SyntaxError("invalid JSON value");
    this.index += matched[0].length;
    const number = Number(matched[0]);
    if (!Number.isFinite(number)) throw new SyntaxError("non-finite JSON number");
    return number;
  }

  private parseObject(): Record<string, unknown> {
    this.enterContainer();
    this.index += 1;
    this.skipWhitespace();
    const entries: [string, unknown][] = [];
    const keys = new Set<string>();
    if (this.source[this.index] === "}") { this.index += 1; this.leaveContainer(); return {}; }
    for (;;) {
      if (this.source[this.index] !== '"') throw new SyntaxError("invalid JSON object key");
      const key = this.parseString();
      if (keys.has(key)) throw new SyntaxError(`duplicate JSON key: ${key}`);
      keys.add(key);
      this.skipWhitespace();
      if (this.source[this.index] !== ":") throw new SyntaxError("missing JSON object colon");
      this.index += 1;
      this.skipWhitespace();
      entries.push([key, this.parseValue()]);
      this.skipWhitespace();
      if (this.source[this.index] === "}") { this.index += 1; this.leaveContainer(); return Object.fromEntries(entries); }
      if (this.source[this.index] !== ",") throw new SyntaxError("invalid JSON object separator");
      this.index += 1;
      this.skipWhitespace();
    }
  }

  private parseArray(): unknown[] {
    this.enterContainer();
    this.index += 1;
    this.skipWhitespace();
    const result: unknown[] = [];
    if (this.source[this.index] === "]") { this.index += 1; this.leaveContainer(); return result; }
    for (;;) {
      result.push(this.parseValue());
      this.skipWhitespace();
      if (this.source[this.index] === "]") { this.index += 1; this.leaveContainer(); return result; }
      if (this.source[this.index] !== ",") throw new SyntaxError("invalid JSON array separator");
      this.index += 1;
      this.skipWhitespace();
    }
  }

  private parseString(): string {
    const start = this.index;
    this.index += 1;
    while (this.index < this.source.length) {
      const current = this.source[this.index];
      if (current === '"') {
        this.index += 1;
        const parsed: unknown = JSON.parse(this.source.slice(start, this.index));
        if (typeof parsed !== "string") throw new SyntaxError("invalid JSON string");
        return parsed;
      }
      if (current === "\\") this.index += 1;
      this.index += 1;
    }
    throw new SyntaxError("unterminated JSON string");
  }

  private skipWhitespace(): void {
    while (this.index < this.source.length && " \n\r\t".includes(this.source[this.index] ?? "")) this.index += 1;
  }

  private enterContainer(): void {
    this.depth += 1;
    if (this.depth > 256) throw new SyntaxError("JSON nesting is too deep");
  }

  private leaveContainer(): void { this.depth -= 1; }
}

const strictJson = (source: string): unknown => new StrictJsonParser(source).parse();

const isAbortError = (value: unknown): boolean =>
  (value instanceof DOMException || value instanceof Error) && value.name === "AbortError";

const decodeProblem = (value: unknown, status: number): TimelineApiError => {
  if (!isRecord(value)) throw invalidResponse("problem", status);
  exact(value, ["error_code", "message", "retryable", "workflow_run_id", "details"], "problem");
  const workflowRunId = value.workflow_run_id === undefined ? undefined : uuid(value.workflow_run_id, "problem.workflow_run_id");
  if (value.details !== undefined && !isRecord(value.details)) throw invalidResponse("problem.details", status);
  const error = new TimelineApiError(
    "HTTP_ERROR",
    safeText(value.error_code, "problem.error_code", 256),
    safeText(value.message, "problem.message", 4096),
    booleanValue(value.retryable, "problem.retryable"),
    status,
  );
  if (workflowRunId !== undefined) void workflowRunId;
  return error;
};

const request = async <T>(
  path: string,
  init: RequestInit | undefined,
  expectedStatuses: readonly number[],
  decode: (value: unknown, status: number) => T,
): Promise<T> => {
  let response: Response;
  try {
    response = await authFetch(path, init);
  } catch (error: unknown) {
    if (isAbortError(error)) throw error;
    throw new TimelineApiError("NETWORK_ERROR", "NETWORK_ERROR", "无法连接 Timeline/Impact API。", true, null, { cause: error });
  }
  let payload: unknown;
  try {
    payload = strictJson(await response.text());
  } catch (error: unknown) {
    if (isAbortError(error)) throw error;
    throw new TimelineApiError("INVALID_RESPONSE", "INVALID_RESPONSE", "Timeline/Impact API 返回了无效或重复字段 JSON。", false, response.status, { cause: error });
  }
  if (!response.ok) throw decodeProblem(payload, response.status);
  if (!expectedStatuses.includes(response.status)) {
    throw new TimelineApiError("INVALID_RESPONSE", "UNEXPECTED_STATUS", `Timeline/Impact API 返回了未约定状态码 ${String(response.status)}。`, false, response.status);
  }
  try {
    return decode(payload, response.status);
  } catch (error: unknown) {
    if (error instanceof TimelineApiError) throw new TimelineApiError(error.code, error.errorCode, error.message, error.retryable, response.status, { cause: error });
    throw error;
  }
};

const requireUuid = (value: string, field: string): string => {
  if (!uuidPattern.test(value)) throw invalidRequest(field);
  return value;
};

const requireKey = (value: string): string => {
  if (typeof value !== "string" || value.trim() !== value || value === "" || textEncoder.encode(value).byteLength > maxIdempotencyKeyBytes || /[\u0000-\u001f\u007f]/.test(value)) {
    throw invalidRequest("idempotencyKey");
  }
  return value;
};

const requireTimestamp = (value: string, field: string): string => {
  try {
    return timestamp(value, field);
  } catch {
    throw invalidRequest(field);
  }
};

const requireSafeText = (value: string, field: string, maximum: number): string => {
  try {
    return safeText(value, field, maximum);
  } catch {
    throw invalidRequest(field);
  }
};

const queryForList = (input: ListTimelineInput): URLSearchParams => {
  const query = new URLSearchParams();
  const eventTypes = input.eventTypes ?? [];
  if (eventTypes.length > 32 || new Set(eventTypes).size !== eventTypes.length || eventTypes.some((item) => !timelineEventTypes.includes(item))) throw invalidRequest("eventTypes");
  for (const eventType of eventTypes) query.append("event_type", eventType);
  if (input.aggregateType !== undefined) {
    if (!timelineAggregateTypes.includes(input.aggregateType)) throw invalidRequest("aggregateType");
    query.set("aggregate_type", input.aggregateType);
  }
  if (input.aggregateId !== undefined) query.set("aggregate_id", requireUuid(input.aggregateId, "aggregateId"));
  if (input.sourceEventRef !== undefined) query.set("source_event_ref", requireSafeText(input.sourceEventRef, "sourceEventRef", 512));
  if (input.occurredAfter !== undefined) query.set("occurred_after", requireTimestamp(input.occurredAfter, "occurredAfter"));
  if (input.occurredBefore !== undefined) query.set("occurred_before", requireTimestamp(input.occurredBefore, "occurredBefore"));
  if (input.occurredAfter !== undefined && input.occurredBefore !== undefined && compareTimelineTimestamps(input.occurredAfter, input.occurredBefore) > 0) throw invalidRequest("occurredRange");
  const limit = input.limit ?? 25;
  if (!Number.isSafeInteger(limit) || limit < 1 || limit > maxTimelineItems) throw invalidRequest("limit");
  query.set("limit", String(limit));
  if (input.cursor !== undefined) query.set("cursor", requireSafeText(input.cursor, "cursor", maxCursorBytes));
  return query;
};

export const listTimeline = (input: ListTimelineInput, signal?: AbortSignal): Promise<TimelinePage> => {
  const workspaceId = requireUuid(input.workspaceId, "workspaceId");
  const query = queryForList(input);
  const limit = input.limit ?? 25;
  return request(
    `/api/v1/workspaces/${encodeURIComponent(workspaceId)}/timeline?${query.toString()}`,
    signal === undefined ? { headers: { Accept: "application/json" } } : { headers: { Accept: "application/json" }, signal },
    [200],
    (value) => {
      const page = decodeTimelinePage(value);
      if (page.workspaceId !== workspaceId || page.items.length > limit) throw invalidResponse("timeline_page.binding");
      return page;
    },
  );
};

export const getTimelineEvent = (workspaceIdValue: string, eventIdValue: string, signal?: AbortSignal): Promise<TimelineEvent> => {
  const workspaceId = requireUuid(workspaceIdValue, "workspaceId");
  const eventId = requireUuid(eventIdValue, "eventId");
  return request(
    `/api/v1/workspaces/${encodeURIComponent(workspaceId)}/timeline/${encodeURIComponent(eventId)}`,
    signal === undefined ? { headers: { Accept: "application/json" } } : { headers: { Accept: "application/json" }, signal },
    [200],
    (value) => {
      const event = decodeTimelineEvent(value);
      if (event.workspaceId !== workspaceId || event.id !== eventId) throw invalidResponse("event.binding");
      return event;
    },
  );
};

export const analyzeImpact = (input: AnalyzeImpactInput, signal?: AbortSignal): Promise<ImpactAnalysisResult> => {
  const workspaceId = requireUuid(input.workspaceId, "workspaceId");
  const eventId = requireUuid(input.eventId, "eventId");
  const idempotencyKey = requireKey(input.idempotencyKey);
  return request(
    `/api/v1/workspaces/${encodeURIComponent(workspaceId)}/timeline/${encodeURIComponent(eventId)}/impact-analysis`,
    { method: "POST", headers: { "Content-Type": "application/json", "Idempotency-Key": idempotencyKey }, body: "{}", ...(signal === undefined ? {} : { signal }) },
    [200, 201],
    (value, status) => {
      const result = decodeImpactAnalysisResult(value);
      if (result.report.workspaceId !== workspaceId || result.report.sourceEventId !== eventId) throw invalidResponse("impact_analysis.binding");
      if (result.replayed !== (status === 200)) throw invalidResponse("impact_analysis.replayed");
      return result;
    },
  );
};

export const getImpactReport = (workspaceIdValue: string, reportIdValue: string, signal?: AbortSignal): Promise<ImpactReport> => {
  const workspaceId = requireUuid(workspaceIdValue, "workspaceId");
  const reportId = requireUuid(reportIdValue, "reportId");
  return request(
    `/api/v1/workspaces/${encodeURIComponent(workspaceId)}/impact-reports/${encodeURIComponent(reportId)}`,
    signal === undefined ? { headers: { Accept: "application/json" } } : { headers: { Accept: "application/json" }, signal },
    [200],
    (value) => {
      const report = decodeImpactReport(value);
      if (report.workspaceId !== workspaceId || report.id !== reportId) throw invalidResponse("impact_report.binding");
      return report;
    },
  );
};

export const createDownstreamUpdateProposal = (input: CreateDownstreamUpdateProposalInput, signal?: AbortSignal): Promise<DownstreamUpdateProposal> => {
  const workspaceId = requireUuid(input.workspaceId, "workspaceId");
  const reportId = requireUuid(input.reportId, "reportId");
  const targetId = requireUuid(input.targetId, "targetId");
  const idempotencyKey = requireKey(input.idempotencyKey);
  if (!["ARTIFACT", "REVIEW_CARD"].some((value) => value === input.targetType)) throw invalidRequest("targetType");
  if (!["REGENERATE_ARTIFACT", "REVALIDATE_REVIEW_CARD"].some((value) => value === input.action)) throw invalidRequest("action");
  if (
    input.targetType === "ARTIFACT" && input.action !== "REGENERATE_ARTIFACT" ||
    input.targetType === "REVIEW_CARD" && input.action !== "REVALIDATE_REVIEW_CARD"
  ) throw invalidRequest("targetAction");
  return request(
    `/api/v1/workspaces/${encodeURIComponent(workspaceId)}/impact-reports/${encodeURIComponent(reportId)}/proposals`,
    {
      method: "POST",
      headers: { "Content-Type": "application/json", "Idempotency-Key": idempotencyKey },
      body: JSON.stringify({ target_type: input.targetType, target_id: targetId, action: input.action }),
      ...(signal === undefined ? {} : { signal }),
    },
    [200, 201],
    (value, status) => {
      const proposal = decodeDownstreamUpdateProposal(value);
      if (proposal.workspaceId !== workspaceId || proposal.revision.update.sourceReport.id !== reportId || proposal.revision.update.targetType !== input.targetType || proposal.revision.update.targetId !== targetId || proposal.revision.update.action !== input.action) {
        throw invalidResponse("downstream_update_proposal.binding");
      }
      if (proposal.replayed !== (status === 200)) throw invalidResponse("downstream_update_proposal.replayed");
      return proposal;
    },
  );
};
