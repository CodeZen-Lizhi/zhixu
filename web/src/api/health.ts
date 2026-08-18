/** Knowledge Health 的唯一网络边界：组件只消费这里导出的领域模型。 */

import { canonicalUuidPattern as uuidPattern, hasOnlyKeys, isAbortError, isRecord } from "../shared/codec";
import { HealthApi as GeneratedHealthApi } from "./generated/apis/HealthApi";
import type { HealthScanStartRequest as GeneratedHealthScanStartRequest } from "./generated/models";
import {
  generatedConfiguration,
  generatedRawResponse,
  generatedRequestInit,
} from "./generated-client";

export type HealthIssueType = "ORPHAN" | "DUPLICATE" | "CONFLICT" | "STALE" | "MISSING_SOURCE" | "LOW_CONFIDENCE" | "BROKEN_REFERENCE" | "INDEX_ERROR" | "SUPERSEDED_USAGE" | "REVIEW_INVALIDATED";
export type HealthSeverity = "CRITICAL" | "HIGH" | "MEDIUM" | "LOW";
export type HealthIssueStatus = "OPEN" | "ACKNOWLEDGED" | "DEFERRED" | "PROPOSAL_CREATED" | "RESOLVED" | "IGNORED" | "FALSE_POSITIVE" | "REOPENED";
export type HealthDecisionAction = "ACKNOWLEDGE" | "IGNORE" | "FALSE_POSITIVE" | "DEFER";
export type HealthDecisionRecordAction = HealthDecisionAction | "CREATE_REPAIR_PROPOSAL";
export type HealthObjectType = "TOPIC" | "CLAIM" | "RELATION" | "CONFLICT" | "SOURCE_VERSION" | "INDEX_VERSION";
export type HealthScanStatus = "PENDING" | "RUNNING" | "SUCCEEDED" | "PARTIAL" | "FAILED" | "CANCELLED";
export type HealthCoverageStatus = HealthScanStatus | "UNAVAILABLE";
export type HealthScopeType = "WORKSPACE" | "TOPIC" | "SMART_COLLECTION" | "DIRECTORY";

export interface HealthObjectRef { type: HealthObjectType; id: string }
export interface HealthObjectVersion { ref: HealthObjectRef; version: number }
export interface HealthEvidence { ref: HealthObjectRef; hash: string; summary: string }
export interface HealthRepairOption { code: string; title: string; available: boolean; unavailableReason: string | null }
export interface HealthFailure { stage: string; code: string; retryable: boolean }
export interface HealthCounters { processed: number; created: number; reopened: number; resolved: number; unchanged: number; failed: number }
export interface HealthCheckpoint { cursor: string; page: number; lastItem: HealthObjectRef | null }
interface HealthScanScopeBase { ref: string; version: number; schemaVersion: string; hash: string | null }
export interface WorkspaceHealthScanScope extends HealthScanScopeBase { type: "WORKSPACE"; hash: null; readModelRevision?: never; exactCount?: never }
export interface TopicHealthScanScope extends HealthScanScopeBase { type: "TOPIC"; hash: null; readModelRevision?: never; exactCount?: never }
export interface DirectoryHealthScanScope extends HealthScanScopeBase { type: "DIRECTORY"; hash: null; readModelRevision?: never; exactCount?: never }
export interface SmartCollectionHealthScanScope extends HealthScanScopeBase { type: "SMART_COLLECTION"; hash: string; readModelRevision: string; exactCount: number }
export type HealthScanScope = WorkspaceHealthScanScope | TopicHealthScanScope | DirectoryHealthScanScope | SmartCollectionHealthScanScope;
export interface HealthCoverage { detectorId: string; detectorVersion: string; status: HealthCoverageStatus; checkpoint: HealthCheckpoint; counters: HealthCounters; lastError: HealthFailure | null; unavailableReason: string | null }

export interface HealthScan {
  id: string;
  workspaceId: string;
  workflowRunId: string;
  scope: HealthScanScope;
  fingerprint: string;
  requestHash: string;
  maxItems: number;
  status: HealthScanStatus;
  checkpoint: HealthCheckpoint;
  counters: HealthCounters;
  coverage: HealthCoverage[];
  lastError: HealthFailure | null;
  version: number;
  createdAt: string;
  updatedAt: string;
  completedAt: string | null;
  statusUrl: string;
}

export interface HealthCapabilityUnavailable { code: string; reason: string }
export interface HealthTrendPoint { date: string; detectedCount: number; resolvedCount: number }
export interface HealthSummary {
  workspaceId: string;
  openCount: number;
  openBySeverity: Record<HealthSeverity, number>;
  openByType: Partial<Record<HealthIssueType, number>>;
  trend: HealthTrendPoint[];
  lastScan: Omit<HealthScan, "workspaceId" | "workflowRunId" | "fingerprint" | "requestHash" | "maxItems" | "checkpoint" | "lastError" | "version" | "createdAt" | "statusUrl"> | null;
  unavailable: HealthCapabilityUnavailable[];
}

export interface HealthIssueListItem {
  id: string;
  workspaceId: string;
  type: HealthIssueType;
  target: HealthObjectRef;
  detectorId: string;
  detectorVersion: string;
  severity: HealthSeverity;
  evidenceSummary: string;
  status: HealthIssueStatus;
  firstDetectedAt: string;
  lastDetectedAt: string;
  lastVerifiedAt: string;
  version: number;
  updatedAt: string;
}

export interface HealthIssuePage { workspaceId: string; items: HealthIssueListItem[]; nextCursor: string | null; hasMore: boolean }

export interface HealthProposalBinding { proposalId: string; repairOptionCode: string; fingerprint: string; objectVersions: HealthObjectVersion[]; createdAt: string }
export interface HealthIssue extends HealthIssueListItem {
  identityHash: string;
  fingerprint: string;
  evidence: HealthEvidence[];
  objectVersions: HealthObjectVersion[];
  repairOptions: HealthRepairOption[];
  statusReason: string;
  deferredUntil: string | null;
  proposal: HealthProposalBinding | null;
  resolvedAt: string | null;
  createdAt: string;
}
export interface HealthObservation { id: string; issueVersion: number; scanId: string; detectorVersion: string; fingerprint: string; evidenceFingerprint: string; targetVersions: HealthObjectVersion[]; severity: HealthSeverity; observedAt: string; evidence: HealthEvidence[] }
export interface HealthDecision { id: string; issueVersion: number; proposalId: string | null; idempotencyKey: string; action: HealthDecisionRecordAction; reason: string; deferredUntil: string | null; createdAt: string }
export interface HealthObservationPage { workspaceId: string; issueId: string; items: HealthObservation[]; nextCursor: string | null; hasMore: boolean }
export interface HealthDecisionPage { workspaceId: string; issueId: string; items: HealthDecision[]; nextCursor: string | null; hasMore: boolean }
export interface HealthIssueDetail {
  issue: HealthIssue;
  latestObservation: HealthObservation;
  observations: HealthObservation[];
  observationsNextCursor: string | null;
  observationsHasMore: boolean;
  decisions: HealthDecision[];
  decisionsNextCursor: string | null;
  decisionsHasMore: boolean;
}
export interface HealthScanAcceptance { scan: HealthScan; healthScanId: string; workflowRunId: string; statusUrl: string; replayed: boolean }

export interface ListHealthIssuesInput { workspaceId: string; statuses?: HealthIssueStatus[]; severities?: HealthSeverity[]; types?: HealthIssueType[]; cursor?: string; limit?: number }
export interface ListHealthIssueHistoryInput { workspaceId: string; issueId: string; cursor?: string; limit?: number }
export interface StartHealthScanInput { workspaceId: string; scope: HealthScanScope; maxItems?: number; preventScopeConcurrency?: boolean; idempotencyKey: string }
export interface DecideHealthIssueInput { workspaceId: string; issueId: string; expectedVersion: number; action: HealthDecisionAction; reason?: string; deferredUntil?: string; idempotencyKey: string }
export interface CreateHealthRepairProposalInput { workspaceId: string; issueId: string; repairOptionCode: string; expectedVersion: number; idempotencyKey: string }

export class HealthApiError extends Error {
  readonly code: "INVALID_REQUEST" | "INVALID_RESPONSE" | "HTTP_ERROR" | "NETWORK_ERROR";
  readonly errorCode: string;
  readonly retryable: boolean;
  readonly status: number | null;
  constructor(code: HealthApiError["code"], errorCode: string, message: string, retryable: boolean, status: number | null = null, options?: ErrorOptions) {
    super(message, options); this.name = "HealthApiError"; this.code = code; this.errorCode = errorCode; this.retryable = retryable; this.status = status;
  }
}

const hashPattern = /^[0-9a-f]{64}$/;
const datePattern = /^\d{4}-\d{2}-\d{2}$/;
const healthApi = new GeneratedHealthApi(generatedConfiguration);
const parseUtcDate = (value: string): number | null => {
  if (!datePattern.test(value)) return null;
  const [yearText, monthText, dayText] = value.split("-");
  const year = Number(yearText); const month = Number(monthText); const day = Number(dayText);
  if (month < 1 || month > 12 || day < 1 || day > new Date(Date.UTC(year, month, 0)).getUTCDate()) return null;
  return Date.UTC(year, month - 1, day);
};
const timestampPattern = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})$/;
const issueTypes: readonly HealthIssueType[] = ["ORPHAN", "DUPLICATE", "CONFLICT", "STALE", "MISSING_SOURCE", "LOW_CONFIDENCE", "BROKEN_REFERENCE", "INDEX_ERROR", "SUPERSEDED_USAGE", "REVIEW_INVALIDATED"];
const severities: readonly HealthSeverity[] = ["CRITICAL", "HIGH", "MEDIUM", "LOW"];
const issueStatuses: readonly HealthIssueStatus[] = ["OPEN", "ACKNOWLEDGED", "DEFERRED", "PROPOSAL_CREATED", "RESOLVED", "IGNORED", "FALSE_POSITIVE", "REOPENED"];
const objectTypes: readonly HealthObjectType[] = ["TOPIC", "CLAIM", "RELATION", "CONFLICT", "SOURCE_VERSION", "INDEX_VERSION"];
const scanStatuses: readonly HealthScanStatus[] = ["PENDING", "RUNNING", "SUCCEEDED", "PARTIAL", "FAILED", "CANCELLED"];
const coverageStatuses: readonly HealthCoverageStatus[] = [...scanStatuses, "UNAVAILABLE"];
const scopeTypes: readonly HealthScopeType[] = ["WORKSPACE", "TOPIC", "SMART_COLLECTION", "DIRECTORY"];
const scopeSchemaVersions: Record<HealthScopeType, string> = {
  WORKSPACE: "health-scope/workspace/v1",
  TOPIC: "health-scope/topic/v1",
  SMART_COLLECTION: "health-scope/smart-collection/v1",
  DIRECTORY: "health-scope/directory/v1",
};
const decisionRecordActions: readonly HealthDecisionRecordAction[] = ["ACKNOWLEDGE", "IGNORE", "FALSE_POSITIVE", "DEFER", "CREATE_REPAIR_PROPOSAL"];

const exact = (value: Record<string, unknown>, keys: readonly string[], field: string): void => { if (!hasOnlyKeys(value, keys)) throw invalidResponse(field); };
const invalidResponse = (field: string): HealthApiError => new HealthApiError("INVALID_RESPONSE", "INVALID_RESPONSE", `Health 响应字段无效：${field}`, false);
const invalidRequest = (field: string): HealthApiError => new HealthApiError("INVALID_REQUEST", "INVALID_REQUEST", `Health 请求字段无效：${field}`, false);
const stringValue = (value: unknown, field: string, nonEmpty = true): string => { if (typeof value !== "string" || (nonEmpty && value.trim() === "")) throw invalidResponse(field); return value; };
const uuid = (value: unknown, field: string): string => { const result = stringValue(value, field); if (!uuidPattern.test(result)) throw invalidResponse(field); return result; };
const hash = (value: unknown, field: string): string => { const result = stringValue(value, field); if (!hashPattern.test(result)) throw invalidResponse(field); return result; };
const isValidTimestamp = (value: string): boolean => {
  if (!timestampPattern.test(value) || !Number.isFinite(Date.parse(value))) return false;
  const match = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})/.exec(value);
  if (match === null) return false;
  const year = Number(match[1]); const month = Number(match[2]); const day = Number(match[3]);
  const hour = Number(match[4]); const minute = Number(match[5]); const second = Number(match[6]);
  return month >= 1 && month <= 12 && day >= 1 && day <= new Date(Date.UTC(year, month, 0)).getUTCDate() && hour <= 23 && minute <= 59 && second <= 59;
};
const timestamp = (value: unknown, field: string): string => { const result = stringValue(value, field); if (!isValidTimestamp(result)) throw invalidResponse(field); return result; };
const optionalTimestamp = (value: unknown, field: string): string | null => value === undefined || value === null ? null : timestamp(value, field);
const integer = (value: unknown, field: string, minimum = 0): number => { if (typeof value !== "number" || !Number.isSafeInteger(value) || value < minimum) throw invalidResponse(field); return value; };
const booleanValue = (value: unknown, field: string): boolean => { if (typeof value !== "boolean") throw invalidResponse(field); return value; };
const enumValue = <T extends string>(value: unknown, choices: readonly T[], field: string): T => { const result = stringValue(value, field); if (!choices.includes(result as T)) throw invalidResponse(field); return result as T; };
const optionalCursor = (value: unknown, field: string, maxLength: number): string | null => {
  if (value === undefined || value === null) return null;
  const result = stringValue(value, field);
  if (result.length > maxLength) throw invalidResponse(field);
  return result;
};
const boundedArray = (value: unknown, field: string, maximum: number): unknown[] => { if (!Array.isArray(value) || value.length > maximum) throw invalidResponse(field); return value; };

const decodeObjectRef = (value: unknown, field: string): HealthObjectRef => { if (!isRecord(value)) throw invalidResponse(field); exact(value, ["type", "id"], field); return { type: enumValue(value.type, objectTypes, `${field}.type`), id: uuid(value.id, `${field}.id`) }; };
const decodeObjectVersion = (value: unknown, field: string): HealthObjectVersion => { if (!isRecord(value)) throw invalidResponse(field); exact(value, ["ref", "version"], field); return { ref: decodeObjectRef(value.ref, `${field}.ref`), version: integer(value.version, `${field}.version`, 1) }; };
const decodeEvidence = (value: unknown, field: string): HealthEvidence => { if (!isRecord(value)) throw invalidResponse(field); exact(value, ["ref", "hash", "summary"], field); return { ref: decodeObjectRef(value.ref, `${field}.ref`), hash: hash(value.hash, `${field}.hash`), summary: stringValue(value.summary, `${field}.summary`, false) }; };
const decodeFailure = (value: unknown, field: string): HealthFailure | null => { if (value === undefined || value === null) return null; if (!isRecord(value)) throw invalidResponse(field); exact(value, ["stage", "code", "retryable"], field); return { stage: stringValue(value.stage, `${field}.stage`), code: stringValue(value.code, `${field}.code`), retryable: booleanValue(value.retryable, `${field}.retryable`) }; };
const decodeCounters = (value: unknown, field: string): HealthCounters => { if (!isRecord(value)) throw invalidResponse(field); exact(value, ["processed", "created", "reopened", "resolved", "unchanged", "failed"], field); return { processed: integer(value.processed, `${field}.processed`), created: integer(value.created, `${field}.created`), reopened: integer(value.reopened, `${field}.reopened`), resolved: integer(value.resolved, `${field}.resolved`), unchanged: integer(value.unchanged, `${field}.unchanged`), failed: integer(value.failed, `${field}.failed`) }; };
const decodeCheckpoint = (value: unknown, field: string): HealthCheckpoint => { if (!isRecord(value)) throw invalidResponse(field); exact(value, ["cursor", "page", "last_item"], field); return { cursor: stringValue(value.cursor, `${field}.cursor`, false), page: integer(value.page, `${field}.page`), lastItem: value.last_item === undefined || value.last_item === null ? null : decodeObjectRef(value.last_item, `${field}.last_item`) }; };
const decodeScope = (value: unknown, field: string): HealthScanScope => {
  if (!isRecord(value)) throw invalidResponse(field);
  const type = enumValue(value.type, scopeTypes, `${field}.type`);
  if (value.schema_version !== scopeSchemaVersions[type]) throw invalidResponse(`${field}.schema_version`);
  if (type === "SMART_COLLECTION") {
    exact(value, ["type", "ref", "version", "schema_version", "hash", "read_model_revision", "exact_count"], field);
    return {
      type,
      ref: uuid(value.ref, `${field}.ref`),
      version: integer(value.version, `${field}.version`, 1),
      schemaVersion: stringValue(value.schema_version, `${field}.schema_version`),
      hash: hash(value.hash, `${field}.hash`),
      readModelRevision: hash(value.read_model_revision, `${field}.read_model_revision`),
      exactCount: (() => { const count = integer(value.exact_count, `${field}.exact_count`); if (count > 50_000) throw invalidResponse(`${field}.exact_count`); return count; })(),
    };
  }
  exact(value, ["type", "ref", "version", "schema_version", "hash"], field);
  const scopeHash = value.hash === undefined || value.hash === null || value.hash === "" ? null : hash(value.hash, `${field}.hash`);
  if (scopeHash !== null) throw invalidResponse(`${field}.hash`);
  return { type, ref: uuid(value.ref, `${field}.ref`), version: integer(value.version, `${field}.version`, 1), schemaVersion: stringValue(value.schema_version, `${field}.schema_version`), hash: null };
};
const decodeOptionalReason = (value: unknown, field: string): string | null => value === undefined || value === null || value === "" ? null : stringValue(value, field, false);
const decodeCoverage = (value: unknown, field: string): HealthCoverage => {
  if (!isRecord(value)) throw invalidResponse(field);
  exact(value, ["detector_id", "detector_version", "status", "checkpoint", "counters", "last_error", "unavailable_reason"], field);
  const result: HealthCoverage = { detectorId: stringValue(value.detector_id, `${field}.detector_id`), detectorVersion: stringValue(value.detector_version, `${field}.detector_version`), status: enumValue(value.status, coverageStatuses, `${field}.status`), checkpoint: decodeCheckpoint(value.checkpoint, `${field}.checkpoint`), counters: decodeCounters(value.counters, `${field}.counters`), lastError: decodeFailure(value.last_error, `${field}.last_error`), unavailableReason: decodeOptionalReason(value.unavailable_reason, `${field}.unavailable_reason`) };
  if (result.status === "UNAVAILABLE") {
    if (result.unavailableReason === null || result.lastError !== null) throw invalidResponse(`${field}.status`);
  } else if (result.status === "FAILED" || result.status === "PARTIAL") {
    if (result.lastError === null || result.unavailableReason !== null) throw invalidResponse(`${field}.status`);
  } else if (result.lastError !== null || result.unavailableReason !== null) throw invalidResponse(`${field}.status`);
  return result;
};
const decodeCoverageArray = (value: unknown, field: string): HealthCoverage[] => {
  const coverage = boundedArray(value, field, 64).map((item, index) => decodeCoverage(item, `${field}[${String(index)}]`));
  if (new Set(coverage.map((item) => item.detectorId)).size !== coverage.length) throw invalidResponse(`${field}.detector_id`);
  return coverage;
};
const countersEqual = (left: HealthCounters, right: HealthCounters): boolean => left.processed === right.processed && left.created === right.created && left.reopened === right.reopened && left.resolved === right.resolved && left.unchanged === right.unchanged && left.failed === right.failed;
const coverageCounters = (coverage: HealthCoverage[]): HealthCounters => coverage.reduce<HealthCounters>((total, item) => ({ processed: total.processed + item.counters.processed, created: total.created + item.counters.created, reopened: total.reopened + item.counters.reopened, resolved: total.resolved + item.counters.resolved, unchanged: total.unchanged + item.counters.unchanged, failed: total.failed + item.counters.failed }), { processed: 0, created: 0, reopened: 0, resolved: 0, unchanged: 0, failed: 0 });
const validateScanCoverage = (status: HealthScanStatus, counters: HealthCounters, coverage: HealthCoverage[], field: string): void => {
  if (!countersEqual(counters, coverageCounters(coverage))) throw invalidResponse(`${field}.counters`);
  const complete = coverage.length > 0 && coverage.every((item) => item.status === "SUCCEEDED");
  if (status === "SUCCEEDED" && !complete) throw invalidResponse(`${field}.coverage`);
  if (status === "PARTIAL" && complete) throw invalidResponse(`${field}.coverage`);
};
const decodeTrend = (value: unknown): HealthTrendPoint[] => {
  const points = boundedArray(value, "summary.trend", 7);
  if (points.length !== 7) throw invalidResponse("summary.trend");
  const decoded = points.map((item, index) => {
    const field = `summary.trend[${String(index)}]`;
    if (!isRecord(item)) throw invalidResponse(field);
    exact(item, ["date", "detected_count", "resolved_count"], field);
    const date = stringValue(item.date, `${field}.date`);
    if (parseUtcDate(date) === null) throw invalidResponse(`${field}.date`);
    return { date, detectedCount: integer(item.detected_count, `${field}.detected_count`), resolvedCount: integer(item.resolved_count, `${field}.resolved_count`) };
  });
  if (decoded.some((point, index) => index > 0 && (parseUtcDate(point.date) ?? 0) - (parseUtcDate(decoded[index - 1]?.date ?? "") ?? 0) !== 86_400_000)) throw invalidResponse("summary.trend");
  return decoded;
};

export const decodeHealthScan = (value: unknown): HealthScan => {
  if (!isRecord(value)) throw invalidResponse("scan"); exact(value, ["id", "workspace_id", "workflow_run_id", "scope", "fingerprint", "request_hash", "max_items", "status", "checkpoint", "counters", "coverage", "last_error", "version", "created_at", "updated_at", "completed_at", "status_url"], "scan");
  const id = uuid(value.id, "scan.id"); const workspaceId = uuid(value.workspace_id, "scan.workspace_id"); const workflowRunId = uuid(value.workflow_run_id, "scan.workflow_run_id");
  const maxItems = integer(value.max_items, "scan.max_items", 1); if (maxItems > 50_000) throw invalidResponse("scan.max_items");
  const statusUrl = stringValue(value.status_url, "scan.status_url"); assertStatusUrl(statusUrl, id, workspaceId, "scan.status_url");
  const result: HealthScan = { id, workspaceId, workflowRunId, scope: decodeScope(value.scope, "scan.scope"), fingerprint: hash(value.fingerprint, "scan.fingerprint"), requestHash: hash(value.request_hash, "scan.request_hash"), maxItems, status: enumValue(value.status, scanStatuses, "scan.status"), checkpoint: decodeCheckpoint(value.checkpoint, "scan.checkpoint"), counters: decodeCounters(value.counters, "scan.counters"), coverage: decodeCoverageArray(value.coverage, "scan.coverage"), lastError: decodeFailure(value.last_error, "scan.last_error"), version: integer(value.version, "scan.version", 1), createdAt: timestamp(value.created_at, "scan.created_at"), updatedAt: timestamp(value.updated_at, "scan.updated_at"), completedAt: optionalTimestamp(value.completed_at, "scan.completed_at"), statusUrl };
  if (result.id === result.workspaceId || (result.scope.type === "WORKSPACE" && result.scope.ref !== result.workspaceId)) throw invalidResponse("scan.scope");
  if (Date.parse(result.updatedAt) < Date.parse(result.createdAt) || (result.completedAt !== null && Date.parse(result.completedAt) < Date.parse(result.createdAt))) throw invalidResponse("scan.timestamps");
  const terminal = result.status === "SUCCEEDED" || result.status === "PARTIAL" || result.status === "FAILED" || result.status === "CANCELLED";
  if (terminal !== (result.completedAt !== null)) throw invalidResponse("scan.completed_at");
  if (result.status === "FAILED" ? result.lastError === null : result.status !== "PARTIAL" && result.lastError !== null) throw invalidResponse("scan.last_error");
  validateScanCoverage(result.status, result.counters, result.coverage, "scan");
  return result;
};

export const decodeHealthSummary = (value: unknown): HealthSummary => {
  if (!isRecord(value)) throw invalidResponse("summary"); exact(value, ["workspace_id", "open_count", "open_by_severity", "open_by_type", "trend", "last_scan", "unavailable"], "summary");
  if (!isRecord(value.open_by_severity) || !isRecord(value.open_by_type)) throw invalidResponse("summary.counts");
  exact(value.open_by_severity, severities, "summary.open_by_severity");
  const severityCounts = value.open_by_severity;
  if (severities.some((severity) => severityCounts[severity] === undefined)) throw invalidResponse("summary.open_by_severity");
  const openBySeverity: Record<HealthSeverity, number> = {
    CRITICAL: severityCounts.CRITICAL === undefined ? 0 : integer(severityCounts.CRITICAL, "summary.open_by_severity.CRITICAL"),
    HIGH: severityCounts.HIGH === undefined ? 0 : integer(severityCounts.HIGH, "summary.open_by_severity.HIGH"),
    MEDIUM: severityCounts.MEDIUM === undefined ? 0 : integer(severityCounts.MEDIUM, "summary.open_by_severity.MEDIUM"),
    LOW: severityCounts.LOW === undefined ? 0 : integer(severityCounts.LOW, "summary.open_by_severity.LOW"),
  };
  const openByType: Partial<Record<HealthIssueType, number>> = {};
  for (const [key, raw] of Object.entries(value.open_by_type)) { if (!issueTypes.includes(key as HealthIssueType)) throw invalidResponse("summary.open_by_type"); openByType[key as HealthIssueType] = integer(raw, `summary.open_by_type.${key}`); }
  let lastScan: HealthSummary["lastScan"] = null;
  if (value.last_scan !== undefined && value.last_scan !== null) {
    if (!isRecord(value.last_scan)) throw invalidResponse("summary.last_scan"); exact(value.last_scan, ["id", "status", "scope", "completed_at", "counters", "coverage", "updated_at"], "summary.last_scan");
    lastScan = { id: uuid(value.last_scan.id, "summary.last_scan.id"), status: enumValue(value.last_scan.status, scanStatuses, "summary.last_scan.status"), scope: decodeScope(value.last_scan.scope, "summary.last_scan.scope"), completedAt: optionalTimestamp(value.last_scan.completed_at, "summary.last_scan.completed_at"), counters: decodeCounters(value.last_scan.counters, "summary.last_scan.counters"), coverage: decodeCoverageArray(value.last_scan.coverage, "summary.last_scan.coverage"), updatedAt: timestamp(value.last_scan.updated_at, "summary.last_scan.updated_at") };
    const terminal = lastScan.status === "SUCCEEDED" || lastScan.status === "PARTIAL" || lastScan.status === "FAILED" || lastScan.status === "CANCELLED";
    if (lastScan.scope.type === "WORKSPACE" && lastScan.scope.ref !== value.workspace_id) throw invalidResponse("summary.last_scan.scope");
    if (terminal !== (lastScan.completedAt !== null)) throw invalidResponse("summary.last_scan.completed_at");
    validateScanCoverage(lastScan.status, lastScan.counters, lastScan.coverage, "summary.last_scan");
  }
  const unavailable: HealthCapabilityUnavailable[] = value.unavailable === undefined ? [] : (() => { if (!Array.isArray(value.unavailable)) throw invalidResponse("summary.unavailable"); return value.unavailable.map((item, index) => { if (!isRecord(item)) throw invalidResponse(`summary.unavailable[${String(index)}]`); exact(item, ["code", "reason"], `summary.unavailable[${String(index)}]`); return { code: stringValue(item.code, `summary.unavailable[${String(index)}].code`), reason: stringValue(item.reason, `summary.unavailable[${String(index)}].reason`) }; }); })();
  return { workspaceId: uuid(value.workspace_id, "summary.workspace_id"), openCount: integer(value.open_count, "summary.open_count"), openBySeverity, openByType, trend: decodeTrend(value.trend), lastScan, unavailable };
};

const decodeIssueListItem = (value: unknown, field = "issue"): HealthIssueListItem => { if (!isRecord(value)) throw invalidResponse(field); exact(value, ["id", "workspace_id", "type", "target", "detector_id", "detector_version", "severity", "evidence_summary", "status", "first_detected_at", "last_detected_at", "last_verified_at", "version", "updated_at"], field); return { id: uuid(value.id, `${field}.id`), workspaceId: uuid(value.workspace_id, `${field}.workspace_id`), type: enumValue(value.type, issueTypes, `${field}.type`), target: decodeObjectRef(value.target, `${field}.target`), detectorId: stringValue(value.detector_id, `${field}.detector_id`), detectorVersion: stringValue(value.detector_version, `${field}.detector_version`), severity: enumValue(value.severity, severities, `${field}.severity`), evidenceSummary: stringValue(value.evidence_summary, `${field}.evidence_summary`, false), status: enumValue(value.status, issueStatuses, `${field}.status`), firstDetectedAt: timestamp(value.first_detected_at, `${field}.first_detected_at`), lastDetectedAt: timestamp(value.last_detected_at, `${field}.last_detected_at`), lastVerifiedAt: timestamp(value.last_verified_at, `${field}.last_verified_at`), version: integer(value.version, `${field}.version`, 1), updatedAt: timestamp(value.updated_at, `${field}.updated_at`) }; };
export const decodeHealthIssuePage = (value: unknown): HealthIssuePage => { if (!isRecord(value)) throw invalidResponse("issue_page"); exact(value, ["workspace_id", "items", "next_cursor", "has_more"], "issue_page"); const workspaceId = uuid(value.workspace_id, "issue_page.workspace_id"); const items = boundedArray(value.items, "issue_page.items", 100).map((item, index) => decodeIssueListItem(item, `issue_page.items[${String(index)}]`)); if (items.some((item) => item.workspaceId !== workspaceId)) throw invalidResponse("issue_page.items.workspace_id"); const nextCursor = optionalCursor(value.next_cursor, "issue_page.next_cursor", 2048); const hasMore = booleanValue(value.has_more, "issue_page.has_more"); if (hasMore !== (nextCursor !== null)) throw invalidResponse("issue_page.next_cursor"); return { workspaceId, items, nextCursor, hasMore }; };

const decodeIssue = (value: unknown): HealthIssue => {
  if (!isRecord(value)) throw invalidResponse("issue.detail"); exact(value, ["id", "workspace_id", "type", "target", "detector_id", "detector_version", "identity_hash", "fingerprint", "severity", "evidence_summary", "evidence", "object_versions", "repair_options", "status", "status_reason", "deferred_until", "proposal", "first_detected_at", "last_detected_at", "last_verified_at", "resolved_at", "version", "created_at", "updated_at"], "issue.detail");
  const base = decodeIssueListItem(Object.fromEntries(Object.entries(value).filter(([key]) => ["id", "workspace_id", "type", "target", "detector_id", "detector_version", "severity", "evidence_summary", "status", "first_detected_at", "last_detected_at", "last_verified_at", "version", "updated_at"].includes(key))), "issue.detail.base");
  const decodeArray = <T>(raw: unknown, field: string, decoder: (item: unknown, itemField: string) => T): T[] => { if (raw === undefined) return []; return boundedArray(raw, field, 256).map((item, index) => decoder(item, `${field}[${String(index)}]`)); };
  const repairOptions = decodeArray(value.repair_options, "issue.detail.repair_options", (item, field): HealthRepairOption => { if (!isRecord(item)) throw invalidResponse(field); exact(item, ["code", "title", "available", "unavailable_reason"], field); return { code: stringValue(item.code, `${field}.code`), title: stringValue(item.title, `${field}.title`), available: booleanValue(item.available, `${field}.available`), unavailableReason: decodeOptionalReason(item.unavailable_reason, `${field}.unavailable_reason`) }; });
  let proposal: HealthProposalBinding | null = null;
  if (value.proposal !== undefined && value.proposal !== null) { if (!isRecord(value.proposal)) throw invalidResponse("issue.detail.proposal"); exact(value.proposal, ["proposal_id", "repair_option_code", "fingerprint", "object_versions", "created_at"], "issue.detail.proposal"); proposal = { proposalId: uuid(value.proposal.proposal_id, "issue.detail.proposal.proposal_id"), repairOptionCode: stringValue(value.proposal.repair_option_code, "issue.detail.proposal.repair_option_code"), fingerprint: hash(value.proposal.fingerprint, "issue.detail.proposal.fingerprint"), objectVersions: decodeArray(value.proposal.object_versions, "issue.detail.proposal.object_versions", decodeObjectVersion), createdAt: timestamp(value.proposal.created_at, "issue.detail.proposal.created_at") }; }
  return { ...base, identityHash: hash(value.identity_hash, "issue.detail.identity_hash"), fingerprint: hash(value.fingerprint, "issue.detail.fingerprint"), evidence: decodeArray(value.evidence, "issue.detail.evidence", decodeEvidence), objectVersions: decodeArray(value.object_versions, "issue.detail.object_versions", decodeObjectVersion), repairOptions, statusReason: value.status_reason === undefined ? "" : stringValue(value.status_reason, "issue.detail.status_reason", false), deferredUntil: optionalTimestamp(value.deferred_until, "issue.detail.deferred_until"), proposal, resolvedAt: optionalTimestamp(value.resolved_at, "issue.detail.resolved_at"), createdAt: timestamp(value.created_at, "issue.detail.created_at") };
};
const decodeObservation = (value: unknown, field: string): HealthObservation => { if (!isRecord(value)) throw invalidResponse(field); exact(value, ["id", "issue_version", "scan_id", "detector_version", "fingerprint", "evidence_fingerprint", "target_versions", "severity", "observed_at", "evidence"], field); if (!Array.isArray(value.target_versions) || !Array.isArray(value.evidence)) throw invalidResponse(field); return { id: uuid(value.id, `${field}.id`), issueVersion: integer(value.issue_version, `${field}.issue_version`, 1), scanId: uuid(value.scan_id, `${field}.scan_id`), detectorVersion: stringValue(value.detector_version, `${field}.detector_version`), fingerprint: hash(value.fingerprint, `${field}.fingerprint`), evidenceFingerprint: hash(value.evidence_fingerprint, `${field}.evidence_fingerprint`), targetVersions: value.target_versions.map((item, index) => decodeObjectVersion(item, `${field}.target_versions[${String(index)}]`)), severity: enumValue(value.severity, severities, `${field}.severity`), observedAt: timestamp(value.observed_at, `${field}.observed_at`), evidence: value.evidence.map((item, index) => decodeEvidence(item, `${field}.evidence[${String(index)}]`)) }; };
const decodeDecision = (value: unknown, field: string): HealthDecision => { if (!isRecord(value)) throw invalidResponse(field); exact(value, ["id", "issue_version", "proposal_id", "idempotency_key", "action", "reason", "deferred_until", "created_at"], field); return { id: uuid(value.id, `${field}.id`), issueVersion: integer(value.issue_version, `${field}.issue_version`, 1), proposalId: value.proposal_id === undefined || value.proposal_id === null ? null : uuid(value.proposal_id, `${field}.proposal_id`), idempotencyKey: stringValue(value.idempotency_key, `${field}.idempotency_key`), action: enumValue(value.action, decisionRecordActions, `${field}.action`), reason: value.reason === undefined ? "" : stringValue(value.reason, `${field}.reason`, false), deferredUntil: optionalTimestamp(value.deferred_until, `${field}.deferred_until`), createdAt: timestamp(value.created_at, `${field}.created_at`) }; };
const decodeHistoryPage = <T extends { id: string }>(value: unknown, field: string, decoder: (item: unknown, itemField: string) => T): { workspaceId: string; issueId: string; items: T[]; nextCursor: string | null; hasMore: boolean } => {
  if (!isRecord(value)) throw invalidResponse(field);
  exact(value, ["workspace_id", "issue_id", "items", "next_cursor", "has_more"], field);
  const items = boundedArray(value.items, `${field}.items`, 100).map((item, index) => decoder(item, `${field}.items[${String(index)}]`));
  if (new Set(items.map((item) => item.id)).size !== items.length) throw invalidResponse(`${field}.items.id`);
  const nextCursor = optionalCursor(value.next_cursor, `${field}.next_cursor`, 2048);
  const hasMore = booleanValue(value.has_more, `${field}.has_more`);
  if (hasMore !== (nextCursor !== null)) throw invalidResponse(`${field}.next_cursor`);
  return { workspaceId: uuid(value.workspace_id, `${field}.workspace_id`), issueId: uuid(value.issue_id, `${field}.issue_id`), items, nextCursor, hasMore };
};
export const decodeHealthObservationPage = (value: unknown): HealthObservationPage => decodeHistoryPage(value, "observation_page", decodeObservation);
export const decodeHealthDecisionPage = (value: unknown): HealthDecisionPage => decodeHistoryPage(value, "decision_page", decodeDecision);
export const decodeHealthIssueDetail = (value: unknown): HealthIssueDetail => {
  if (!isRecord(value)) throw invalidResponse("issue_detail");
  exact(value, ["issue", "latest_observation", "observations", "observations_next_cursor", "observations_has_more", "decisions", "decisions_next_cursor", "decisions_has_more"], "issue_detail");
  const issue = decodeIssue(value.issue);
  const latestObservation = decodeObservation(value.latest_observation, "issue_detail.latest_observation");
  const observations = boundedArray(value.observations, "issue_detail.observations", 25).map((item, index) => decodeObservation(item, `issue_detail.observations[${String(index)}]`));
  const decisions = boundedArray(value.decisions, "issue_detail.decisions", 25).map((item, index) => decodeDecision(item, `issue_detail.decisions[${String(index)}]`));
  if (new Set(observations.map((item) => item.id)).size !== observations.length || new Set(decisions.map((item) => item.id)).size !== decisions.length) throw invalidResponse("issue_detail.history.id");
  if (observations.length === 0 || latestObservation.fingerprint !== issue.fingerprint || latestObservation.detectorVersion !== issue.detectorVersion || latestObservation.severity !== issue.severity || JSON.stringify(latestObservation.evidence) !== JSON.stringify(issue.evidence) || JSON.stringify(latestObservation.targetVersions) !== JSON.stringify(issue.objectVersions)) throw invalidResponse("issue_detail.latest_observation");
  const observationsNextCursor = optionalCursor(value.observations_next_cursor, "issue_detail.observations_next_cursor", 2048);
  const observationsHasMore = booleanValue(value.observations_has_more, "issue_detail.observations_has_more");
  const decisionsNextCursor = optionalCursor(value.decisions_next_cursor, "issue_detail.decisions_next_cursor", 2048);
  const decisionsHasMore = booleanValue(value.decisions_has_more, "issue_detail.decisions_has_more");
  if (observationsHasMore !== (observationsNextCursor !== null)) throw invalidResponse("issue_detail.observations_next_cursor");
  if (decisionsHasMore !== (decisionsNextCursor !== null)) throw invalidResponse("issue_detail.decisions_next_cursor");
  return { issue, latestObservation, observations, observationsNextCursor, observationsHasMore, decisions, decisionsNextCursor, decisionsHasMore };
};

const readProblem = (value: unknown, status: number): HealthApiError => { if (isRecord(value) && typeof value.error_code === "string" && typeof value.message === "string" && typeof value.retryable === "boolean") return new HealthApiError("HTTP_ERROR", value.error_code, value.message, value.retryable, status); return new HealthApiError("HTTP_ERROR", "HTTP_ERROR", `Health 请求失败（HTTP ${String(status)}）`, status >= 500, status); };
const request = async (operation: Promise<Response>): Promise<unknown> => { let response: Response; try { response = await operation; } catch (error: unknown) { if (isAbortError(error)) throw error; throw new HealthApiError("NETWORK_ERROR", "NETWORK_ERROR", "无法连接 Knowledge Health API。", true, null, { cause: error }); } let payload: unknown; try { payload = await response.json(); } catch (error: unknown) { if (isAbortError(error)) throw error; throw new HealthApiError("INVALID_RESPONSE", "INVALID_RESPONSE", "Knowledge Health API 返回了无效 JSON。", false, response.status, { cause: error }); } if (!response.ok) throw readProblem(payload, response.status); return payload; };
const requireWorkspace = (workspaceId: string): string => { if (!uuidPattern.test(workspaceId)) throw invalidRequest("workspaceId"); return workspaceId; };
const requireUuid = (value: string, field: string): string => { if (!uuidPattern.test(value)) throw invalidRequest(field); return value; };
const requireIdempotencyKey = (value: string): string => { if (value.trim() === "" || value.length > 128) throw invalidRequest("idempotencyKey"); return value; };
const requireVersion = (value: number): number => { if (!Number.isSafeInteger(value) || value < 1) throw invalidRequest("expectedVersion"); return value; };
const requireLimit = (value: number | undefined, fallback: number): number => { const result = value ?? fallback; if (!Number.isSafeInteger(result) || result < 1 || result > 100) throw invalidRequest("limit"); return result; };
const assertWorkspace = <T extends { workspaceId: string }>(value: T, workspaceId: string, field: string): T => { if (value.workspaceId !== workspaceId) throw invalidResponse(field); return value; };
const assertIssue = <T extends { id: string; workspaceId: string }>(value: T, workspaceId: string, issueId?: string): T => { assertWorkspace(value, workspaceId, "issue.workspace_id"); if (issueId !== undefined && value.id !== issueId) throw invalidResponse("issue.id"); return value; };
const assertStatusUrl = (value: string, scanId: string, workspaceId: string, field: string): void => {
  let parsed: URL;
  try { parsed = new URL(value, "http://zhixu.local"); } catch { throw invalidResponse(field); }
  if (parsed.origin !== "http://zhixu.local" || parsed.pathname !== `/api/v1/health/scans/${scanId}` || parsed.searchParams.size !== 1 || parsed.searchParams.get("workspace_id") !== workspaceId) throw invalidResponse(field);
};

export const getHealthSummary = (workspaceId: string, signal?: AbortSignal): Promise<HealthSummary> => { const expectedWorkspaceId = requireWorkspace(workspaceId); return request(generatedRawResponse(healthApi.getHealthSummaryRaw({ workspaceId: expectedWorkspaceId }, generatedRequestInit(signal)))).then(decodeHealthSummary).then((value) => assertWorkspace(value, expectedWorkspaceId, "summary.workspace_id")); };
export const listHealthIssues = (input: ListHealthIssuesInput, signal?: AbortSignal): Promise<HealthIssuePage> => { const expectedWorkspaceId = requireWorkspace(input.workspaceId); let cursor: string | undefined; if (input.cursor !== undefined) { if (input.cursor.trim() === "" || input.cursor.length > 2048) throw invalidRequest("cursor"); cursor = input.cursor; } const statuses = input.statuses?.map((item) => enumValue(item, issueStatuses, "status")); const requestedSeverities = input.severities?.map((item) => enumValue(item, severities, "severity")); const types = input.types?.map((item) => enumValue(item, issueTypes, "type")); return request(generatedRawResponse(healthApi.listHealthIssuesRaw({ workspaceId: expectedWorkspaceId, limit: requireLimit(input.limit, 25), ...(cursor === undefined ? {} : { cursor }), ...(statuses === undefined || statuses.length === 0 ? {} : { status: statuses }), ...(requestedSeverities === undefined || requestedSeverities.length === 0 ? {} : { severity: requestedSeverities }), ...(types === undefined || types.length === 0 ? {} : { type: types }) }, generatedRequestInit(signal)))).then(decodeHealthIssuePage).then((value) => assertWorkspace(value, expectedWorkspaceId, "issue_page.workspace_id")); };
export const getHealthIssue = (workspaceId: string, issueId: string, signal?: AbortSignal): Promise<HealthIssueDetail> => { const expectedWorkspaceId = requireWorkspace(workspaceId); const expectedIssueId = requireUuid(issueId, "issueId"); return request(generatedRawResponse(healthApi.getHealthIssueRaw({ issueId: expectedIssueId, workspaceId: expectedWorkspaceId }, generatedRequestInit(signal)))).then(decodeHealthIssueDetail).then((detail) => ({ ...detail, issue: assertIssue(detail.issue, expectedWorkspaceId, expectedIssueId) })); };
const listHealthIssueHistory = <T extends HealthObservationPage | HealthDecisionPage>(input: ListHealthIssueHistoryInput, kind: "observations" | "decisions", decoder: (value: unknown) => T, signal?: AbortSignal): Promise<T> => {
  const expectedWorkspaceId = requireWorkspace(input.workspaceId);
  const expectedIssueId = requireUuid(input.issueId, "issueId");
  const limit = requireLimit(input.limit, 25);
  let cursor: string | undefined;
  if (input.cursor !== undefined) {
    if (input.cursor.trim() === "" || input.cursor.length > 2048) throw invalidRequest("cursor");
    cursor = input.cursor;
  }
  const operation = kind === "observations"
    ? generatedRawResponse(healthApi.listHealthIssueObservationsRaw({ issueId: expectedIssueId, workspaceId: expectedWorkspaceId, limit, ...(cursor === undefined ? {} : { cursor }) }, generatedRequestInit(signal)))
    : generatedRawResponse(healthApi.listHealthIssueDecisionsRaw({ issueId: expectedIssueId, workspaceId: expectedWorkspaceId, limit, ...(cursor === undefined ? {} : { cursor }) }, generatedRequestInit(signal)));
  return request(operation).then(decoder).then((page) => {
    assertWorkspace(page, expectedWorkspaceId, `${kind}_page.workspace_id`);
    if (page.issueId !== expectedIssueId) throw invalidResponse(`${kind}_page.issue_id`);
    return page;
  });
};
export const listHealthIssueObservations = (input: ListHealthIssueHistoryInput, signal?: AbortSignal): Promise<HealthObservationPage> => listHealthIssueHistory(input, "observations", decodeHealthObservationPage, signal);
export const listHealthIssueDecisions = (input: ListHealthIssueHistoryInput, signal?: AbortSignal): Promise<HealthDecisionPage> => listHealthIssueHistory(input, "decisions", decodeHealthDecisionPage, signal);
export const getHealthScan = (workspaceId: string, scanId: string, signal?: AbortSignal): Promise<HealthScan> => { const expectedWorkspaceId = requireWorkspace(workspaceId); const expectedScanId = requireUuid(scanId, "scanId"); return request(generatedRawResponse(healthApi.getHealthScanRaw({ scanId: expectedScanId, workspaceId: expectedWorkspaceId }, generatedRequestInit(signal)))).then(decodeHealthScan).then((scan) => { assertWorkspace(scan, expectedWorkspaceId, "scan.workspace_id"); if (scan.id !== expectedScanId) throw invalidResponse("scan.id"); return scan; }); };
const encodeScope = (scope: HealthScanScope): GeneratedHealthScanStartRequest["scope"] => {
  const type = scope.type;
  if (!scopeTypes.includes(type)) throw invalidRequest("scope.type");
  if (scope.schemaVersion !== scopeSchemaVersions[type]) throw invalidRequest("scope.schemaVersion");
  const ref = requireUuid(scope.ref, "scope.ref");
  const version = requireVersion(scope.version);
  if (type === "SMART_COLLECTION") {
    if (!hashPattern.test(scope.hash) || !hashPattern.test(scope.readModelRevision) || !Number.isSafeInteger(scope.exactCount) || scope.exactCount < 0 || scope.exactCount > 50_000) throw invalidRequest("scope.binding");
    return { type, ref, version, schema_version: scope.schemaVersion, hash: scope.hash, read_model_revision: scope.readModelRevision, exact_count: scope.exactCount };
  }
  return { type, ref, version, schema_version: scope.schemaVersion };
};
export const startHealthScan = (input: StartHealthScanInput, signal?: AbortSignal): Promise<HealthScanAcceptance> => { const expectedWorkspaceId = requireWorkspace(input.workspaceId); const maxItems = input.maxItems ?? 5000; if (!Number.isSafeInteger(maxItems) || maxItems < 1 || maxItems > 50_000) throw invalidRequest("maxItems"); return request(generatedRawResponse(healthApi.startHealthScanRaw({ idempotencyKey: requireIdempotencyKey(input.idempotencyKey), healthScanStartRequest: { workspace_id: expectedWorkspaceId, scope: encodeScope(input.scope), max_items: maxItems, prevent_scope_concurrency: input.preventScopeConcurrency ?? true } }, generatedRequestInit(signal)))).then((value) => { if (!isRecord(value)) throw invalidResponse("scan_acceptance"); exact(value, ["scan", "health_scan_id", "workflow_run_id", "status_url", "replayed"], "scan_acceptance"); const scan = decodeHealthScan(value.scan); assertWorkspace(scan, expectedWorkspaceId, "scan_acceptance.scan.workspace_id"); const healthScanId = uuid(value.health_scan_id, "scan_acceptance.health_scan_id"); const workflowRunId = uuid(value.workflow_run_id, "scan_acceptance.workflow_run_id"); const statusUrl = stringValue(value.status_url, "scan_acceptance.status_url"); if (scan.id !== healthScanId || scan.workflowRunId !== workflowRunId || scan.statusUrl !== statusUrl) throw invalidResponse("scan_acceptance.binding"); return { scan, healthScanId, workflowRunId, statusUrl, replayed: booleanValue(value.replayed, "scan_acceptance.replayed") }; }); };
export const decideHealthIssue = (input: DecideHealthIssueInput, signal?: AbortSignal): Promise<HealthIssue> => { const expectedWorkspaceId = requireWorkspace(input.workspaceId); const expectedIssueId = requireUuid(input.issueId, "issueId"); const reason = input.reason?.trim(); if (input.action === "DEFER" && (input.deferredUntil === undefined || reason === undefined || reason === "" || !isValidTimestamp(input.deferredUntil) || Date.parse(input.deferredUntil) <= Date.now())) throw invalidRequest("deferredUntil"); if ((input.action === "IGNORE" || input.action === "FALSE_POSITIVE") && (reason ?? "") === "") throw invalidRequest("reason"); return request(generatedRawResponse(healthApi.decideHealthIssueRaw({ issueId: expectedIssueId, workspaceId: expectedWorkspaceId, idempotencyKey: requireIdempotencyKey(input.idempotencyKey), healthDecisionRequest: { workspace_id: expectedWorkspaceId, expected_version: requireVersion(input.expectedVersion), action: input.action, ...(reason === undefined ? {} : { reason }), ...(input.deferredUntil === undefined ? {} : { deferred_until: input.deferredUntil }) } }, generatedRequestInit(signal)))).then(decodeIssue).then((issue) => assertIssue(issue, expectedWorkspaceId, expectedIssueId)); };
export const createHealthRepairProposal = (input: CreateHealthRepairProposalInput, signal?: AbortSignal): Promise<never> => { const expectedWorkspaceId = requireWorkspace(input.workspaceId); const expectedIssueId = requireUuid(input.issueId, "issueId"); if (input.repairOptionCode.trim() === "" || !Number.isSafeInteger(input.expectedVersion) || input.expectedVersion < 1) throw invalidRequest("repairOption"); const body = JSON.stringify({ workspace_id: expectedWorkspaceId, expected_version: input.expectedVersion, repair_option_code: input.repairOptionCode }); return request(generatedRawResponse(healthApi.createHealthRepairProposalRaw({ issueId: expectedIssueId, workspaceId: expectedWorkspaceId, idempotencyKey: requireIdempotencyKey(input.idempotencyKey) }, generatedRequestInit(signal, { body })))).then(() => { throw invalidResponse("repair_proposal"); }); };

export const workspaceHealthScope = (workspaceId: string): HealthScanScope => ({ type: "WORKSPACE", ref: requireWorkspace(workspaceId), version: 1, schemaVersion: "health-scope/workspace/v1", hash: null });
export { decodeCoverage, decodeIssue, decodeIssueListItem, decodeObjectRef, decodeScope };
