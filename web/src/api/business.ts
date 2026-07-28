/* eslint-disable @typescript-eslint/consistent-type-definitions, @typescript-eslint/no-non-null-assertion, @typescript-eslint/restrict-template-expressions */
import { authFetch } from "./auth";
import { graphNodeRefIdentity, graphRelationTypeCompatible, isSymmetricGraphRelationType } from "./graph";
import { ApiBoundaryError } from "./system-status";
import type { ArtifactImpactBinding, ReviewCardImpactBinding } from "./timeline";

export type Page<T> = { items: T[]; nextCursor?: string };
export type SourceSecurityStatus = "pending" | "passed" | "quarantined";
export type SourceIngestionStatus = "validating" | "parsing" | "parsed" | "chunking" | "chunked" | "parse_failed" | "cancelled";
export type SourceIndexStatus = "included" | "excluded";
export type ProposalType = "file_patch" | "knowledge_change" | "publish_artifact" | "downstream_update";
export const proposalRiskLevels = ["CRITICAL", "HIGH", "MEDIUM", "LOW"] as const;
export type ProposalRiskLevel = (typeof proposalRiskLevels)[number];
export type SourceVersionItem = {
  id: string; sourceId: string; path: string; mimeType: string; byteSize: number;
  capturedAt: string; contentHash: string; securityStatus: SourceSecurityStatus;
  ingestionStatus?: SourceIngestionStatus; workflowStatus?: WorkflowStatus; indexStatus?: SourceIndexStatus;
};
export type ProposalStatus = "draft" | "validating" | "ready_for_review" | "approved" | "applying" | "applied" | "verifying" | "completed" | "rejected" | "needs_revision" | "deferred" | "apply_failed" | "verify_failed" | "rolled_back" | "cancelled";
export type ProposalSummary = {
  id: string; workspaceId: string; type: ProposalType;
  status: ProposalStatus; target: string; riskLevel: ProposalRiskLevel; risk: string; revisionId: string;
  changeHash: string; approval?: ApprovalSnapshot; createdAt: string; updatedAt: string;
};
export type WorkflowStatus = "pending" | "running" | "waiting_for_human" | "retry_wait" | "paused" | "succeeded" | "failed" | "cancelled";
export type WorkflowSummary = {
  id: string; workspaceId: string; definitionKey: string; definitionVersion: number;
  status: WorkflowStatus; version: number; createdAt: string; updatedAt: string;
  completedAt?: string; waitingForHuman: boolean; pauseRequested: boolean; cancelRequested: boolean;
};
interface ProposalRevisionBase {
  id: string;
  revisionNo: number;
  risk: string;
  rollbackPlan: string;
  changeHash: string;
  createdAt: string;
}
export interface FilePatchRevision extends ProposalRevisionBase {
  baseHash: string;
  content: string;
  evidenceSummary: string;
}
export interface KnowledgeChangeRevision extends ProposalRevisionBase {
  schemaVersion: "knowledge-relation-change/v1";
  targetRefs: { type: "RELATION_CANDIDATE"; id: string; fingerprint: string }[];
  baseVersions: { nodeType: "TOPIC" | "CLAIM"; nodeId: string; version: number }[];
  changeSet: {
    operation: "CREATE_RELATION";
    source: { type: "TOPIC" | "CLAIM"; id: string; version: number };
    target: { type: "TOPIC" | "CLAIM"; id: string; version: number };
    relationType: "CITES" | "DERIVED_FROM" | "BELONGS_TO" | "SUPPORTS" | "COMPLEMENTS" | "DUPLICATES" | "CONFLICTS_WITH" | "PREREQUISITE_OF" | "VERSION_OF" | "IMPACTS";
  };
  evidenceRefs: { candidateEvidenceId: string; semanticHash: string }[];
}
export interface PublishArtifactRevision extends ProposalRevisionBase {
  publication: {
    workspaceId: string;
    artifactId: string;
    revisionId: string;
    revisionNo: number;
    artifactVersion: number;
    contentHash: string;
    sourceCoverage: {
      sectionKey: string;
      status: "COVERED" | "PARTIAL" | "GAP";
      gaps: { code: string; description: string }[];
    }[];
    schemaVersion: "artifact-publication/v1";
  };
}
interface DownstreamUpdateBase {
  workspaceId: string;
  sourceReport: { id: string; analysisVersion: "impact-analysis/v2"; fingerprint: string };
  sourceEvent: { id: string; eventVersion: number };
  targetId: string;
  baseVersion: number;
  reason: string;
  schemaVersion: "impact-downstream-update/v1";
}
export type DownstreamUpdate =
  | (DownstreamUpdateBase & {
    targetType: "ARTIFACT";
    action: "REGENERATE_ARTIFACT";
    artifactBinding: ArtifactImpactBinding;
    reviewCardBinding?: never;
  })
  | (DownstreamUpdateBase & {
    targetType: "REVIEW_CARD";
    action: "REVALIDATE_REVIEW_CARD";
    artifactBinding?: never;
    reviewCardBinding: ReviewCardImpactBinding;
  });
export interface DownstreamUpdateRevision extends ProposalRevisionBase {
  update: DownstreamUpdate;
}
interface ProposalDetailBase {
  id: string;
  workspaceId: string;
  status: ProposalStatus;
  riskLevel: ProposalRiskLevel;
  approval?: ApprovalSnapshot;
  createdAt: string;
  updatedAt: string;
}
export type ProposalDetail =
  | (ProposalDetailBase & { type: "file_patch"; targetPath: string; revision: FilePatchRevision })
  | (ProposalDetailBase & { type: "knowledge_change"; revision: KnowledgeChangeRevision })
  | (ProposalDetailBase & { type: "publish_artifact"; revision: PublishArtifactRevision })
  | (ProposalDetailBase & { type: "downstream_update"; revision: DownstreamUpdateRevision });
export type WorkflowDetail = {
  id: string; workspaceId: string; definitionId: string; status: WorkflowStatus;
  input: unknown; output?: unknown; version: number; createdAt: string;
  updatedAt: string; completedAt?: string; pauseRequested: boolean; cancelRequested: boolean;
};
export type ProposalCurrentContent = {
  proposalId: string; workspaceId: string; targetPath: string; content: string;
  currentHash: string; baseHash: string; baseHashMatch: boolean;
};
export type ProposalApplyPreflight = {
  proposalId: string; revisionId: string; changeHash: string; baseHash: string;
  preflightPassed: true; mode: "preflight_only"; writePerformed: false;
};
export type ApprovalSnapshot = {
  id: string; proposalId: string; revisionId: string; changeHash: string;
  decision: "approved" | "rejected"; decidedAt: string;
  approvedGitHead?: string; workflowRunId?: string; workflowStatusUrl?: string;
  writebackState?: "bound" | "pending_dispatch" | "legacy_unrecoverable";
};
export type ApprovalDecisionResult = ApprovalSnapshot & {
  dispatchStatus?: "queued" | "running" | "replayed";
};
export type WorkflowControlResult = {
  workflowRunId: string; status: WorkflowStatus; version: number; statusUrl: string;
  pauseRequested: boolean; cancelRequested: boolean;
};
export type SourceVersionListParams = {
  cursor?: string;
  limit?: number;
  securityStatus?: SourceSecurityStatus;
  ingestionStatus?: SourceIngestionStatus;
  workflowStatus?: WorkflowStatus;
  indexStatus?: SourceIndexStatus;
  mimeType?: string;
};
export type ProposalListParams = {
  cursor?: string;
  limit?: number;
  status?: ProposalStatus;
  type?: ProposalType;
  risk?: ProposalRiskLevel;
  createdAfter?: string;
};
export type WorkflowListParams = {
  cursor?: string;
  limit?: number;
  status?: WorkflowStatus;
};

export class BusinessApiError extends ApiBoundaryError {
  readonly status: number | undefined;
  constructor(code: "HTTP_ERROR" | "INVALID_RESPONSE" | "NETWORK_ERROR", message: string, retryable: boolean, status?: number, options?: ErrorOptions) {
    super(code, message, retryable, options);
    this.name = "BusinessApiError";
    this.status = status;
  }
}

const uuidPattern = /^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/;
const hashPattern = /^[0-9a-f]{64}$/;
const rfc3339Pattern = /^\d{4}-(?:0[1-9]|1[0-2])-(?:0[1-9]|[12]\d|3[01])T(?:[01]\d|2[0-3]):[0-5]\d:[0-5]\d(?:\.\d{1,9})?(?:Z|[+-](?:[01]\d|2[0-3]):[0-5]\d)$/;
const maxProposalCurrentContentBytes = 1024 * 1024;
const maxKnowledgeRevisionTextBytes = 4096;
const utf8Encoder = new TextEncoder();
const record = (value: unknown, field: string): Record<string, unknown> => {
  if (typeof value !== "object" || value === null || Array.isArray(value)) throw new BusinessApiError("INVALID_RESPONSE", `响应结构无效：${field}`, false);
  return value as Record<string, unknown>;
};
const exact = (r: Record<string, unknown>, fields: readonly string[], label: string): void => {
  const allowed = new Set(fields);
  if (Object.keys(r).some((key) => !allowed.has(key))) throw new BusinessApiError("INVALID_RESPONSE", `响应包含未知字段：${label}`, false);
};
const requiredFieldValue = (r: Record<string, unknown>, field: string): unknown => {
  if (!Object.prototype.hasOwnProperty.call(r, field) || r[field] === undefined) {
    throw new BusinessApiError("INVALID_RESPONSE", `响应缺少字段：${field}`, false);
  }
  return r[field];
};
const stringValue = (r: Record<string, unknown>, field: string, required = true): string | undefined => {
  const value = r[field];
  if (value === undefined && !required) return undefined;
  if (typeof value !== "string") throw new BusinessApiError("INVALID_RESPONSE", `响应字段无效：${field}`, false);
  return value;
};
const nonEmptyStringValue = (r: Record<string, unknown>, field: string): string => {
  const value = stringValue(r, field)!;
  if (value.length === 0) throw new BusinessApiError("INVALID_RESPONSE", `响应字符串不能为空：${field}`, false);
  return value;
};
const boundedNonEmptyStringValue = (r: Record<string, unknown>, field: string, maximumBytes: number): string => {
  const value = nonEmptyStringValue(r, field);
  if (utf8Encoder.encode(value).byteLength > maximumBytes) {
    throw new BusinessApiError("INVALID_RESPONSE", `响应字符串过长：${field}`, false);
  }
  return value;
};
const boundedCurrentContent = (r: Record<string, unknown>): string => {
  const content = stringValue(r, "content")!;
  if (utf8Encoder.encode(content).byteLength > maxProposalCurrentContentBytes) {
    throw new BusinessApiError("INVALID_RESPONSE", "Proposal current-content 超过 1 MiB 上限", false);
  }
  return content;
};
const numberValue = (r: Record<string, unknown>, field: string): number => {
  const value = r[field];
  if (typeof value !== "number" || !Number.isFinite(value)) throw new BusinessApiError("INVALID_RESPONSE", `响应字段无效：${field}`, false);
  return value;
};
const integerValue = (r: Record<string, unknown>, field: string, minimum = 0): number => {
  const value = numberValue(r, field);
  if (!Number.isSafeInteger(value) || value < minimum) throw new BusinessApiError("INVALID_RESPONSE", `响应整数无效：${field}`, false);
  return value;
};
const uuidValue = (r: Record<string, unknown>, field: string): string => {
  const value = stringValue(r, field)!;
  if (!uuidPattern.test(value)) throw new BusinessApiError("INVALID_RESPONSE", `响应 UUID 无效：${field}`, false);
  return value;
};
const hashValue = (r: Record<string, unknown>, field: string): string => {
  const value = stringValue(r, field)!;
  if (!hashPattern.test(value)) throw new BusinessApiError("INVALID_RESPONSE", `响应 Hash 无效：${field}`, false);
  return value;
};
const gitHashValue = (r: Record<string, unknown>, field: string): string => {
  const value = stringValue(r, field)!;
  if (!/^([0-9a-f]{40}|[0-9a-f]{64})$/.test(value)) throw new BusinessApiError("INVALID_RESPONSE", `响应 Git Hash 无效：${field}`, false);
  return value;
};
const daysInMonth = (year: number, month: number): number => {
  if (month === 2) {
    const leap = year % 4 === 0 && (year % 100 !== 0 || year % 400 === 0);
    return leap ? 29 : 28;
  }
  return month === 4 || month === 6 || month === 9 || month === 11 ? 30 : 31;
};
const dateTimeValue = (r: Record<string, unknown>, field: string): string => {
  const value = stringValue(r, field)!;
  const year = Number(value.slice(0, 4));
  const month = Number(value.slice(5, 7));
  const day = Number(value.slice(8, 10));
  if (
    !rfc3339Pattern.test(value) ||
    year < 1 ||
    day > daysInMonth(year, month) ||
    !Number.isFinite(Date.parse(value))
  ) {
    throw new BusinessApiError("INVALID_RESPONSE", `响应时间无效：${field}`, false);
  }
  return value;
};
const literalValue = <T extends string>(r: Record<string, unknown>, field: string, allowed: readonly T[]): T => {
  const value = stringValue(r, field);
  if (!allowed.includes(value as T)) throw new BusinessApiError("INVALID_RESPONSE", `响应字段无效：${field}`, false);
  return value as T;
};
const boundValue = (actual: string, expected: string, field: string): string => {
  if (actual !== expected) throw new BusinessApiError("INVALID_RESPONSE", `响应绑定不一致：${field}`, false);
  return actual;
};
const boolValue = (r: Record<string, unknown>, field: string): boolean => {
  const value = r[field];
  if (typeof value !== "boolean") throw new BusinessApiError("INVALID_RESPONSE", `响应字段无效：${field}`, false);
  return value;
};
const recordArray = (value: unknown, field: string, minimum = 0, maximum = 100): Record<string, unknown>[] => {
  if (!Array.isArray(value)) throw new BusinessApiError("INVALID_RESPONSE", `响应字段无效：${field}`, false);
  if (value.length < minimum || value.length > maximum) throw new BusinessApiError("INVALID_RESPONSE", `响应数组长度无效：${field}`, false);
  return value.map((item, index) => record(item, `${field}[${String(index)}]`));
};
const workflowStatus = (r: Record<string, unknown>, field: string): WorkflowStatus => {
  const value = stringValue(r, field);
  switch (value) {
    case "pending":
    case "running":
    case "waiting_for_human":
    case "retry_wait":
    case "paused":
    case "succeeded":
    case "failed":
    case "cancelled":
      return value;
    default:
      throw new BusinessApiError("INVALID_RESPONSE", `响应字段无效：${field}`, false);
  }
};
const optionalLiteralValue = <T extends string>(r: Record<string, unknown>, field: string, allowed: readonly T[]): T | undefined =>
  r[field] === undefined ? undefined : literalValue(r, field, allowed);
const optionalWorkflowStatus = (r: Record<string, unknown>, field: string): WorkflowStatus | undefined =>
  r[field] === undefined ? undefined : workflowStatus(r, field);
const proposalStatus = (r: Record<string, unknown>, field: string): ProposalStatus => literalValue(r, field, ["draft", "validating", "ready_for_review", "approved", "applying", "applied", "verifying", "completed", "rejected", "needs_revision", "deferred", "apply_failed", "verify_failed", "rolled_back", "cancelled"]);
const proposalRiskLevel = (r: Record<string, unknown>, field: string): ProposalRiskLevel => literalValue(r, field, proposalRiskLevels);
const page = <T>(value: unknown, decode: (item: unknown) => T): Page<T> => {
  const r = record(value, "page");
  exact(r, ["items", "next_cursor"], "page");
  if (!Array.isArray(r.items) || r.items.length > 100) throw new BusinessApiError("INVALID_RESPONSE", "列表响应 items 无效", false);
  const nextCursor = stringValue(r, "next_cursor", false);
  if (nextCursor !== undefined && (nextCursor.length < 1 || nextCursor.length > 2048)) throw new BusinessApiError("INVALID_RESPONSE", "列表响应 cursor 无效", false);
  return { items: r.items.map(decode), ...(nextCursor === undefined || nextCursor === "" ? {} : { nextCursor }) };
};
const isAbortError = (value: unknown): boolean =>
  (value instanceof DOMException || value instanceof Error) && value.name === "AbortError";
const request = async (path: string, init?: RequestInit): Promise<unknown> => {
  let response: Response;
  const headers = new Headers(init?.headers); headers.set("Accept", "application/json"); if (init?.body !== undefined) headers.set("Content-Type", "application/json");
  try { response = await authFetch(path, { ...init, headers }); }
  catch (error: unknown) {
    if (isAbortError(error)) throw error;
    throw new BusinessApiError("NETWORK_ERROR", "无法连接业务 API。", true, undefined, { cause: error });
  }
  let payload: unknown;
  try { payload = await response.json(); }
  catch (error: unknown) {
    if (isAbortError(error)) throw error;
    throw new BusinessApiError("INVALID_RESPONSE", "业务 API 返回了无效 JSON。", false, response.status, { cause: error });
  }
  if (!response.ok) {
    const p = typeof payload === "object" && payload !== null ? payload as Record<string, unknown> : {};
    const message = typeof p.message === "string" ? p.message : `业务 API 请求失败（HTTP ${String(response.status)}）。`;
    throw new BusinessApiError("HTTP_ERROR", message, response.status >= 500, response.status);
  }
  return payload;
};

const decodeSource = (value: unknown, expectedWorkspaceId: string): SourceVersionItem => {
  const r = record(value, "source_version");
  exact(r, ["id", "source_id", "workspace_id", "path", "mime_type", "byte_size", "captured_at", "content_hash", "security_status", "ingestion_status", "workflow_status", "index_status"], "source_version");
  boundValue(uuidValue(r, "workspace_id"), expectedWorkspaceId, "source_version.workspace_id");
  const ingestionStatus = optionalLiteralValue(r, "ingestion_status", ["validating", "parsing", "parsed", "chunking", "chunked", "parse_failed", "cancelled"]);
  const workflowStatusValue = optionalWorkflowStatus(r, "workflow_status");
  const indexStatus = optionalLiteralValue(r, "index_status", ["included", "excluded"]);
  return { id: uuidValue(r, "id"), sourceId: uuidValue(r, "source_id"), path: stringValue(r, "path")!, mimeType: stringValue(r, "mime_type")!, byteSize: integerValue(r, "byte_size"), capturedAt: dateTimeValue(r, "captured_at"), contentHash: hashValue(r, "content_hash"), securityStatus: literalValue(r, "security_status", ["pending", "passed", "quarantined"]), ...(ingestionStatus === undefined ? {} : { ingestionStatus }), ...(workflowStatusValue === undefined ? {} : { workflowStatus: workflowStatusValue }), ...(indexStatus === undefined ? {} : { indexStatus }) };
};
const decodeProposal = (value: unknown, expectedWorkspaceId: string): ProposalSummary => {
  const r = record(value, "proposal");
  exact(r, ["id", "workspace_id", "proposal_type", "status", "target", "risk_level", "risk", "revision_id", "change_hash", "approval", "created_at", "updated_at"], "proposal");
  const workspaceId = boundValue(uuidValue(r, "workspace_id"), expectedWorkspaceId, "proposal.workspace_id");
  const id = uuidValue(r, "id");
  const type = literalValue(r, "proposal_type", ["file_patch", "knowledge_change", "publish_artifact", "downstream_update"]);
  const revisionId = uuidValue(r, "revision_id");
  const changeHash = hashValue(r, "change_hash");
  const riskLevel = proposalRiskLevel(r, "risk_level");
  if (type !== "file_patch" && riskLevel !== "HIGH") {
    throw new BusinessApiError("INVALID_RESPONSE", "Typed Proposal 摘要必须使用 HIGH 风险等级", false);
  }
  const approval = decodeProposalApproval(r.approval, id, revisionId, changeHash, type);
  return { id, workspaceId, type, status: proposalStatus(r, "status"), target: stringValue(r, "target")!, riskLevel, risk: stringValue(r, "risk")!, revisionId, changeHash, ...(approval === undefined ? {} : { approval }), createdAt: dateTimeValue(r, "created_at"), updatedAt: dateTimeValue(r, "updated_at") };
};
const decodeWorkflow = (value: unknown, expectedWorkspaceId: string): WorkflowSummary => {
  const r = record(value, "workflow");
  exact(r, ["id", "workspace_id", "definition_key", "definition_version", "status", "version", "created_at", "updated_at", "completed_at", "waiting_for_human", "pause_requested", "cancel_requested"], "workflow");
  const status = workflowStatus(r, "status");
  const completedAt = r.completed_at === undefined ? undefined : dateTimeValue(r, "completed_at");
  const workspaceId = boundValue(uuidValue(r, "workspace_id"), expectedWorkspaceId, "workflow.workspace_id");
  return { id: uuidValue(r, "id"), workspaceId, definitionKey: stringValue(r, "definition_key")!, definitionVersion: integerValue(r, "definition_version", 1), status, version: integerValue(r, "version", 1), createdAt: dateTimeValue(r, "created_at"), updatedAt: dateTimeValue(r, "updated_at"), ...(completedAt === undefined ? {} : { completedAt }), waitingForHuman: boolValue(r, "waiting_for_human"), pauseRequested: boolValue(r, "pause_requested"), cancelRequested: boolValue(r, "cancel_requested") };
};
function decodeProposalApproval(value: unknown, expectedProposalId: string, expectedRevisionId: string, expectedChangeHash: string, proposalType: ProposalType, allowNull = false): ApprovalSnapshot | undefined {
  if (value === undefined || (allowNull && value === null)) return undefined;
  const approval = record(value, "proposal.approval");
  exact(approval, ["id", "proposal_id", "revision_id", "change_hash", "decision", "approved_git_head", "workflow_run_id", "workflow_status_url", "decided_at"], "proposal.approval");
  const id = uuidValue(approval, "id");
  const proposalId = boundValue(uuidValue(approval, "proposal_id"), expectedProposalId, "proposal.approval.proposal_id");
  const revisionId = boundValue(uuidValue(approval, "revision_id"), expectedRevisionId, "proposal.approval.revision_id");
  const changeHash = boundValue(hashValue(approval, "change_hash"), expectedChangeHash, "proposal.approval.change_hash");
  const decision = literalValue(approval, "decision", ["approved", "rejected"]);
  const decidedAt = dateTimeValue(approval, "decided_at");
  const approvedGitHead = approval.approved_git_head === undefined ? undefined : gitHashValue(approval, "approved_git_head");
  const workflowRunId = approval.workflow_run_id === undefined ? undefined : uuidValue(approval, "workflow_run_id");
  const workflowStatusUrl = stringValue(approval, "workflow_status_url", false);
  const hasWorkflowBinding = workflowRunId !== undefined || workflowStatusUrl !== undefined;
  if (hasWorkflowBinding && (workflowRunId === undefined || workflowStatusUrl !== `/api/v1/workflows/${workflowRunId}`)) {
    throw new BusinessApiError("INVALID_RESPONSE", "Proposal Approval Workflow 绑定不完整", false);
  }
  if (decision === "rejected" && (approvedGitHead !== undefined || hasWorkflowBinding)) {
    throw new BusinessApiError("INVALID_RESPONSE", "驳回 Approval 包含非法写回字段", false);
  }
  if (proposalType !== "file_patch" && (approvedGitHead !== undefined || hasWorkflowBinding)) {
    throw new BusinessApiError("INVALID_RESPONSE", "Typed Proposal Approval 不应包含 Git/Workflow 写回字段", false);
  }
  let writebackState: ApprovalSnapshot["writebackState"];
  if (proposalType === "file_patch" && decision === "approved") {
    if (hasWorkflowBinding && approvedGitHead === undefined) {
      throw new BusinessApiError("INVALID_RESPONSE", "File Patch Approval Workflow 缺少 Git 基线", false);
    }
    writebackState = hasWorkflowBinding
      ? "bound"
      : approvedGitHead === undefined
        ? "legacy_unrecoverable"
        : "pending_dispatch";
  }
  return {
    id,
    proposalId,
    revisionId,
    changeHash,
    decision,
    decidedAt,
    ...(approvedGitHead === undefined ? {} : { approvedGitHead }),
    ...(workflowRunId === undefined ? {} : { workflowRunId }),
    ...(workflowStatusUrl === undefined ? {} : { workflowStatusUrl }),
    ...(writebackState === undefined ? {} : { writebackState }),
  };
}

const decodeArtifactImpactBinding = (value: unknown, field: string): ArtifactImpactBinding => {
  const binding = record(value, field);
  exact(binding, ["artifact_id", "artifact_version", "revision_id", "revision_no", "content_hash"], field);
  const artifactId = uuidValue(binding, "artifact_id");
  const revisionId = uuidValue(binding, "revision_id");
  if (artifactId === revisionId) throw new BusinessApiError("INVALID_RESPONSE", `响应绑定不一致：${field}`, false);
  return {
    artifactId,
    artifactVersion: integerValue(binding, "artifact_version", 1),
    revisionId,
    revisionNo: integerValue(binding, "revision_no", 1),
    contentHash: hashValue(binding, "content_hash"),
  };
};

const decodeReviewCardImpactBinding = (value: unknown, field: string): ReviewCardImpactBinding => {
  const binding = record(value, field);
  exact(binding, ["card_id", "card_version", "status", "fingerprint", "claim_id", "evidence_binding_fingerprint"], field);
  const cardId = uuidValue(binding, "card_id");
  const claimId = uuidValue(binding, "claim_id");
  if (cardId === claimId) throw new BusinessApiError("INVALID_RESPONSE", `响应绑定不一致：${field}`, false);
  return {
    cardId,
    cardVersion: integerValue(binding, "card_version", 1),
    status: literalValue(binding, "status", ["DRAFT", "APPROVED", "INVALIDATED", "REJECTED"]),
    fingerprint: hashValue(binding, "fingerprint"),
    claimId,
    evidenceBindingFingerprint: hashValue(binding, "evidence_binding_fingerprint"),
  };
};

const decodeDownstreamUpdate = (value: unknown, expectedWorkspaceId: string): DownstreamUpdate => {
  const update = record(value, "proposal.revision.update");
  exact(update, ["workspace_id", "source_report", "source_event", "target_type", "target_id", "base_version", "action", "artifact_binding", "review_card_binding", "reason", "schema_version"], "proposal.revision.update");
  const sourceReport = record(update.source_report, "proposal.revision.update.source_report");
  const sourceEvent = record(update.source_event, "proposal.revision.update.source_event");
  exact(sourceReport, ["id", "analysis_version", "fingerprint"], "proposal.revision.update.source_report");
  exact(sourceEvent, ["id", "event_version"], "proposal.revision.update.source_event");

  const workspaceId = boundValue(uuidValue(update, "workspace_id"), expectedWorkspaceId, "proposal.revision.update.workspace_id");
  const sourceReportId = uuidValue(sourceReport, "id");
  const sourceEventId = uuidValue(sourceEvent, "id");
  if (new Set([workspaceId, sourceReportId, sourceEventId]).size !== 3) {
    throw new BusinessApiError("INVALID_RESPONSE", "Downstream Update 来源身份绑定重复", false);
  }
  const common = {
    workspaceId,
    sourceReport: {
      id: sourceReportId,
      analysisVersion: literalValue(sourceReport, "analysis_version", ["impact-analysis/v2"]),
      fingerprint: hashValue(sourceReport, "fingerprint"),
    },
    sourceEvent: { id: sourceEventId, eventVersion: integerValue(sourceEvent, "event_version", 1) },
    targetId: uuidValue(update, "target_id"),
    baseVersion: integerValue(update, "base_version", 1),
    reason: boundedNonEmptyStringValue(update, "reason", maxKnowledgeRevisionTextBytes),
    schemaVersion: literalValue(update, "schema_version", ["impact-downstream-update/v1"]),
  };
  const targetType = literalValue(update, "target_type", ["ARTIFACT", "REVIEW_CARD"]);
  const action = literalValue(update, "action", ["REGENERATE_ARTIFACT", "REVALIDATE_REVIEW_CARD"]);
  const artifactBinding = update.artifact_binding === undefined
    ? undefined
    : decodeArtifactImpactBinding(update.artifact_binding, "proposal.revision.update.artifact_binding");
  const reviewCardBinding = update.review_card_binding === undefined
    ? undefined
    : decodeReviewCardImpactBinding(update.review_card_binding, "proposal.revision.update.review_card_binding");

  if (targetType === "ARTIFACT") {
    if (artifactBinding === undefined || action !== "REGENERATE_ARTIFACT" || reviewCardBinding !== undefined || artifactBinding.artifactId !== common.targetId || artifactBinding.artifactVersion !== common.baseVersion) {
      throw new BusinessApiError("INVALID_RESPONSE", "Artifact Downstream Update 绑定不一致", false);
    }
    return { ...common, targetType, action, artifactBinding };
  }
  if (reviewCardBinding === undefined) {
    throw new BusinessApiError("INVALID_RESPONSE", "Review Card Downstream Update 绑定不一致", false);
  }
  if (action !== "REVALIDATE_REVIEW_CARD" || artifactBinding !== undefined || reviewCardBinding.cardId !== common.targetId || reviewCardBinding.cardVersion !== common.baseVersion) {
    throw new BusinessApiError("INVALID_RESPONSE", "Review Card Downstream Update 绑定不一致", false);
  }
  return { ...common, targetType, action, reviewCardBinding };
};

export const listSourceVersions = (workspaceId: string, params: SourceVersionListParams = {}, signal?: AbortSignal): Promise<Page<SourceVersionItem>> => {
  const query = new URLSearchParams({ ...(params.cursor ? { cursor: params.cursor } : {}), ...(params.limit ? { limit: String(params.limit) } : {}), ...(params.securityStatus ? { security_status: params.securityStatus } : {}), ...(params.ingestionStatus ? { ingestion_status: params.ingestionStatus } : {}), ...(params.workflowStatus ? { workflow_status: params.workflowStatus } : {}), ...(params.indexStatus ? { index_status: params.indexStatus } : {}), ...(params.mimeType ? { mime_type: params.mimeType } : {}) });
  return request(`/api/v1/workspaces/${encodeURIComponent(workspaceId)}/source-versions${query.size ? `?${query}` : ""}`, signal === undefined ? undefined : { signal }).then((v) => page(v, (item) => decodeSource(item, workspaceId)));
};
export const getSourceVersion = (workspaceId: string, sourceVersionId: string, signal?: AbortSignal): Promise<SourceVersionItem> => request(
  `/api/v1/workspaces/${encodeURIComponent(workspaceId)}/source-versions/${encodeURIComponent(sourceVersionId)}`,
  signal === undefined ? undefined : { signal },
).then((value) => {
  const r = record(value, "source_version");
  exact(r, ["workspace_id", "source_id", "source_version_id", "source_type", "logical_name", "relative_path", "content_hash", "byte_size", "media_type", "security_status", "ingestion_status", "workflow_status", "index_status", "captured_at"], "source_version");
  boundValue(uuidValue(r, "workspace_id"), workspaceId, "source_version.workspace_id");
  boundValue(uuidValue(r, "source_version_id"), sourceVersionId, "source_version.source_version_id");
  stringValue(r, "source_type");
  stringValue(r, "logical_name");
  const ingestionStatus = optionalLiteralValue(r, "ingestion_status", ["validating", "parsing", "parsed", "chunking", "chunked", "parse_failed", "cancelled"]);
  const workflowStatusValue = optionalWorkflowStatus(r, "workflow_status");
  const indexStatus = optionalLiteralValue(r, "index_status", ["included", "excluded"]);
  return {
    id: uuidValue(r, "source_version_id"),
    sourceId: uuidValue(r, "source_id"),
    path: stringValue(r, "relative_path")!,
    mimeType: stringValue(r, "media_type")!,
    byteSize: integerValue(r, "byte_size"),
    capturedAt: dateTimeValue(r, "captured_at"),
    contentHash: hashValue(r, "content_hash"),
    securityStatus: literalValue(r, "security_status", ["pending", "passed", "quarantined"]),
    ...(ingestionStatus === undefined ? {} : { ingestionStatus }),
    ...(workflowStatusValue === undefined ? {} : { workflowStatus: workflowStatusValue }),
    ...(indexStatus === undefined ? {} : { indexStatus }),
  };
});
export const listProposals = (workspaceId: string, params: ProposalListParams = {}, signal?: AbortSignal): Promise<Page<ProposalSummary>> => {
  const query = new URLSearchParams({ ...(params.cursor ? { cursor: params.cursor } : {}), ...(params.limit ? { limit: String(params.limit) } : {}), ...(params.status ? { status: params.status } : {}), ...(params.type ? { proposal_type: params.type } : {}), ...(params.risk ? { risk: params.risk } : {}), ...(params.createdAfter ? { created_after: params.createdAfter } : {}) });
  return request(`/api/v1/workspaces/${encodeURIComponent(workspaceId)}/proposals${query.size ? `?${query}` : ""}`, signal === undefined ? undefined : { signal }).then((v) => page(v, (item) => decodeProposal(item, workspaceId)));
};
export const listWorkflows = (workspaceId: string, params: WorkflowListParams = {}, signal?: AbortSignal): Promise<Page<WorkflowSummary>> => {
  const query = new URLSearchParams({ ...(params.cursor ? { cursor: params.cursor } : {}), ...(params.limit ? { limit: String(params.limit) } : {}), ...(params.status ? { status: params.status } : {}) });
  return request(`/api/v1/workspaces/${encodeURIComponent(workspaceId)}/workflows${query.size ? `?${query}` : ""}`, signal === undefined ? undefined : { signal }).then((v) => page(v, (item) => decodeWorkflow(item, workspaceId)));
};

export const getProposal = (workspaceId: string, id: string, signal?: AbortSignal): Promise<ProposalDetail> => request(`/api/v1/proposals/${encodeURIComponent(id)}`, signal === undefined ? undefined : { signal }).then((v) => {
  const r = record(v, "proposal");
  const type = literalValue(r, "proposal_type", ["file_patch", "knowledge_change", "publish_artifact", "downstream_update"]);
  exact(r, type === "file_patch"
    ? ["proposal_type", "id", "workspace_id", "target_path", "status", "risk_level", "revision", "approval", "created_at", "updated_at"]
    : ["proposal_type", "id", "workspace_id", "status", "risk_level", "revision", "approval", "created_at", "updated_at"], "proposal");
  const approvalValue = requiredFieldValue(r, "approval");
  const revision = record(r.revision, "proposal.revision");
  const base = {
    id: boundValue(uuidValue(r, "id"), id, "proposal.id"),
    workspaceId: boundValue(uuidValue(r, "workspace_id"), workspaceId, "proposal.workspace_id"),
    status: proposalStatus(r, "status"),
    riskLevel: proposalRiskLevel(r, "risk_level"),
    createdAt: dateTimeValue(r, "created_at"),
    updatedAt: dateTimeValue(r, "updated_at"),
  };
  if (type === "file_patch") {
    exact(revision, ["id", "revision_no", "base_hash", "content", "evidence_summary", "risk", "rollback_plan", "change_hash", "created_at"], "proposal.revision");
    const decodedRevision: FilePatchRevision = {
      id: uuidValue(revision, "id"),
      revisionNo: integerValue(revision, "revision_no", 1),
      baseHash: hashValue(revision, "base_hash"),
      content: nonEmptyStringValue(revision, "content"),
      evidenceSummary: nonEmptyStringValue(revision, "evidence_summary"),
      risk: nonEmptyStringValue(revision, "risk"),
      rollbackPlan: nonEmptyStringValue(revision, "rollback_plan"),
      changeHash: hashValue(revision, "change_hash"),
      createdAt: dateTimeValue(revision, "created_at"),
    };
    const approval = decodeProposalApproval(approvalValue, id, decodedRevision.id, decodedRevision.changeHash, type, true);
    return {
      ...base,
      type,
      targetPath: nonEmptyStringValue(r, "target_path"),
      revision: decodedRevision,
      ...(approval === undefined ? {} : { approval }),
    };
  }
  if (type === "knowledge_change") {
    exact(revision, ["id", "revision_no", "schema_version", "target_refs", "base_versions", "change_set", "evidence_refs", "risk", "rollback_plan", "change_hash", "created_at"], "proposal.revision");
    if (base.riskLevel !== "HIGH") throw new BusinessApiError("INVALID_RESPONSE", "Knowledge Change 必须使用 HIGH 风险等级", false);
    const changeSet = record(revision.change_set, "proposal.revision.change_set");
    exact(changeSet, ["operation", "source", "target", "relation_type"], "proposal.revision.change_set");
    const decodeEndpoint = (value: unknown, field: string) => {
      const endpoint = record(value, field);
      exact(endpoint, ["type", "id", "version"], field);
      return { type: literalValue(endpoint, "type", ["TOPIC", "CLAIM"]), id: uuidValue(endpoint, "id"), version: integerValue(endpoint, "version", 1) };
    };
    const decodedRevision: KnowledgeChangeRevision = {
      id: uuidValue(revision, "id"),
      revisionNo: integerValue(revision, "revision_no", 1),
      schemaVersion: literalValue(revision, "schema_version", ["knowledge-relation-change/v1"]),
      targetRefs: recordArray(revision.target_refs, "proposal.revision.target_refs", 1, 100).map((item) => {
        exact(item, ["type", "id", "fingerprint"], "proposal.revision.target_ref");
        return { type: literalValue(item, "type", ["RELATION_CANDIDATE"]), id: uuidValue(item, "id"), fingerprint: hashValue(item, "fingerprint") };
      }),
      baseVersions: recordArray(revision.base_versions, "proposal.revision.base_versions", 2, 2).map((item) => {
        exact(item, ["node_type", "node_id", "version"], "proposal.revision.base_version");
        return { nodeType: literalValue(item, "node_type", ["TOPIC", "CLAIM"]), nodeId: uuidValue(item, "node_id"), version: integerValue(item, "version", 1) };
      }),
      changeSet: {
        operation: literalValue(changeSet, "operation", ["CREATE_RELATION"]),
        source: decodeEndpoint(changeSet.source, "proposal.revision.change_set.source"),
        target: decodeEndpoint(changeSet.target, "proposal.revision.change_set.target"),
        relationType: literalValue(changeSet, "relation_type", ["CITES", "DERIVED_FROM", "BELONGS_TO", "SUPPORTS", "COMPLEMENTS", "DUPLICATES", "CONFLICTS_WITH", "PREREQUISITE_OF", "VERSION_OF", "IMPACTS"]),
      },
      evidenceRefs: recordArray(revision.evidence_refs, "proposal.revision.evidence_refs", 1, 100).map((item) => {
        exact(item, ["candidate_evidence_id", "semantic_hash"], "proposal.revision.evidence_ref");
        return { candidateEvidenceId: uuidValue(item, "candidate_evidence_id"), semanticHash: hashValue(item, "semantic_hash") };
      }),
      risk: boundedNonEmptyStringValue(revision, "risk", maxKnowledgeRevisionTextBytes),
      rollbackPlan: boundedNonEmptyStringValue(revision, "rollback_plan", maxKnowledgeRevisionTextBytes),
      changeHash: hashValue(revision, "change_hash"),
      createdAt: dateTimeValue(revision, "created_at"),
    };
    const nodeKey = (type: "TOPIC" | "CLAIM", nodeId: string): string => `${type}:${nodeId}`;
    const sourceKey = nodeKey(decodedRevision.changeSet.source.type, decodedRevision.changeSet.source.id);
    const targetKey = nodeKey(decodedRevision.changeSet.target.type, decodedRevision.changeSet.target.id);
    const baseVersions = new Map(decodedRevision.baseVersions.map((item) => [nodeKey(item.nodeType, item.nodeId), item.version]));
    if (
      sourceKey === targetKey ||
      !graphRelationTypeCompatible(decodedRevision.changeSet.relationType, decodedRevision.changeSet.source.type, decodedRevision.changeSet.target.type) ||
      isSymmetricGraphRelationType(decodedRevision.changeSet.relationType) &&
        graphNodeRefIdentity(decodedRevision.changeSet.source) > graphNodeRefIdentity(decodedRevision.changeSet.target) ||
      baseVersions.size !== 2 ||
      baseVersions.get(sourceKey) !== decodedRevision.changeSet.source.version ||
      baseVersions.get(targetKey) !== decodedRevision.changeSet.target.version
    ) {
      throw new BusinessApiError("INVALID_RESPONSE", "Knowledge Change 端点与基线版本不一致", false);
    }
    const targetRefKeys = new Set(decodedRevision.targetRefs.map((item) => `${item.type}:${item.id}:${item.fingerprint}`));
    const evidenceRefKeys = new Set(decodedRevision.evidenceRefs.map((item) => `${item.candidateEvidenceId}:${item.semanticHash}`));
    if (targetRefKeys.size !== decodedRevision.targetRefs.length || evidenceRefKeys.size !== decodedRevision.evidenceRefs.length) {
      throw new BusinessApiError("INVALID_RESPONSE", "Knowledge Change 包含重复引用", false);
    }
    const approval = decodeProposalApproval(approvalValue, id, decodedRevision.id, decodedRevision.changeHash, type, true);
    return {
      ...base,
      type,
      revision: decodedRevision,
      ...(approval === undefined ? {} : { approval }),
    };
  }

  if (type === "downstream_update") {
    exact(revision, ["id", "revision_no", "update", "risk", "rollback_plan", "change_hash", "created_at"], "proposal.revision");
    if (base.riskLevel !== "HIGH") throw new BusinessApiError("INVALID_RESPONSE", "Downstream Update Proposal 必须使用 HIGH 风险等级", false);
    const decodedRevision: DownstreamUpdateRevision = {
      id: uuidValue(revision, "id"),
      revisionNo: integerValue(revision, "revision_no", 1),
      update: decodeDownstreamUpdate(revision.update, base.workspaceId),
      risk: boundedNonEmptyStringValue(revision, "risk", maxKnowledgeRevisionTextBytes),
      rollbackPlan: boundedNonEmptyStringValue(revision, "rollback_plan", maxKnowledgeRevisionTextBytes),
      changeHash: hashValue(revision, "change_hash"),
      createdAt: dateTimeValue(revision, "created_at"),
    };
    const approval = decodeProposalApproval(approvalValue, id, decodedRevision.id, decodedRevision.changeHash, type, true);
    return {
      ...base,
      type,
      revision: decodedRevision,
      ...(approval === undefined ? {} : { approval }),
    };
  }

  exact(revision, ["id", "revision_no", "publication", "risk", "rollback_plan", "change_hash", "created_at"], "proposal.revision");
  if (base.riskLevel !== "HIGH") throw new BusinessApiError("INVALID_RESPONSE", "Artifact 发布 Proposal 必须使用 HIGH 风险等级", false);
  const publication = record(revision.publication, "proposal.revision.publication");
  exact(publication, ["workspace_id", "artifact_id", "revision_id", "revision_no", "artifact_version", "content_hash", "source_coverage", "schema_version"], "proposal.revision.publication");
  const publicationWorkspaceId = boundValue(uuidValue(publication, "workspace_id"), base.workspaceId, "proposal.revision.publication.workspace_id");
  const artifactId = uuidValue(publication, "artifact_id");
  const artifactRevisionId = uuidValue(publication, "revision_id");
  if (new Set([publicationWorkspaceId, artifactId, artifactRevisionId]).size !== 3) {
    throw new BusinessApiError("INVALID_RESPONSE", "Artifact 发布身份绑定重复", false);
  }
  const sourceCoverage = recordArray(publication.source_coverage, "proposal.revision.publication.source_coverage", 1, Number.MAX_SAFE_INTEGER).map((item) => {
    exact(item, ["section_key", "status", "gaps"], "proposal.revision.publication.source_coverage[]");
    const status = literalValue(item, "status", ["COVERED", "PARTIAL", "GAP"]);
    const gaps = recordArray(item.gaps, "proposal.revision.publication.source_coverage[].gaps", 0, Number.MAX_SAFE_INTEGER).map((gap) => {
      exact(gap, ["code", "description"], "proposal.revision.publication.source_coverage[].gap");
      return {
        code: boundedNonEmptyStringValue(gap, "code", 128),
        description: boundedNonEmptyStringValue(gap, "description", maxKnowledgeRevisionTextBytes),
      };
    });
    if (status === "COVERED" ? gaps.length !== 0 : gaps.length === 0) {
      throw new BusinessApiError("INVALID_RESPONSE", "Artifact 发布来源覆盖与知识缺口不一致", false);
    }
    if (new Set(gaps.map((gap) => `${gap.code}\u0000${gap.description}`)).size !== gaps.length) {
      throw new BusinessApiError("INVALID_RESPONSE", "Artifact 发布包含重复知识缺口", false);
    }
    return { sectionKey: boundedNonEmptyStringValue(item, "section_key", 128), status, gaps };
  });
  if (new Set(sourceCoverage.map((item) => item.sectionKey)).size !== sourceCoverage.length) {
    throw new BusinessApiError("INVALID_RESPONSE", "Artifact 发布包含重复章节覆盖", false);
  }
  const decodedRevision: PublishArtifactRevision = {
    id: uuidValue(revision, "id"),
    revisionNo: integerValue(revision, "revision_no", 1),
    publication: {
      workspaceId: publicationWorkspaceId,
      artifactId,
      revisionId: artifactRevisionId,
      revisionNo: integerValue(publication, "revision_no", 1),
      artifactVersion: integerValue(publication, "artifact_version", 1),
      contentHash: hashValue(publication, "content_hash"),
      sourceCoverage,
      schemaVersion: literalValue(publication, "schema_version", ["artifact-publication/v1"]),
    },
    risk: boundedNonEmptyStringValue(revision, "risk", maxKnowledgeRevisionTextBytes),
    rollbackPlan: boundedNonEmptyStringValue(revision, "rollback_plan", maxKnowledgeRevisionTextBytes),
    changeHash: hashValue(revision, "change_hash"),
    createdAt: dateTimeValue(revision, "created_at"),
  };
  const approval = decodeProposalApproval(approvalValue, id, decodedRevision.id, decodedRevision.changeHash, type, true);
  return {
    ...base,
    type,
    revision: decodedRevision,
    ...(approval === undefined ? {} : { approval }),
  };
});
export const getProposalCurrentContent = (workspaceId: string, id: string, binding: { targetPath: string; baseHash: string }, signal?: AbortSignal): Promise<ProposalCurrentContent> => request(`/api/v1/proposals/${encodeURIComponent(id)}/current-content`, signal === undefined ? undefined : { signal }).then((v) => {
  const r = record(v, "proposal_current_content");
  exact(r, ["proposal_id", "workspace_id", "target_path", "content", "current_hash", "base_hash", "base_hash_match"], "proposal_current_content");
  const currentHash = hashValue(r, "current_hash");
  const baseHash = boundValue(hashValue(r, "base_hash"), binding.baseHash, "proposal_current_content.base_hash");
  const baseHashMatch = boolValue(r, "base_hash_match");
  if (baseHashMatch !== (currentHash === baseHash)) throw new BusinessApiError("INVALID_RESPONSE", "Proposal current-content Hash 语义不一致", false);
  return {
    proposalId: boundValue(uuidValue(r, "proposal_id"), id, "proposal_current_content.proposal_id"),
    workspaceId: boundValue(uuidValue(r, "workspace_id"), workspaceId, "proposal_current_content.workspace_id"),
    targetPath: boundValue(stringValue(r, "target_path")!, binding.targetPath, "proposal_current_content.target_path"),
    content: boundedCurrentContent(r),
    currentHash,
    baseHash,
    baseHashMatch,
  };
});
export const preflightProposal = (id: string, input: { revisionId: string; changeHash: string }): Promise<ProposalApplyPreflight> => request(`/api/v1/proposals/${encodeURIComponent(id)}/apply-preflight`, { method: "POST", body: JSON.stringify({ revision_id: input.revisionId, approved_change_hash: input.changeHash }) }).then((v) => {
  const r = record(v, "proposal_apply_preflight");
  exact(r, ["proposal_id", "revision_id", "change_hash", "base_hash", "preflight_passed", "mode", "write_performed"], "proposal_apply_preflight");
  if (r.preflight_passed !== true || r.mode !== "preflight_only" || r.write_performed !== false) throw new BusinessApiError("INVALID_RESPONSE", "Proposal preflight 响应语义无效", false);
  return {
    proposalId: boundValue(uuidValue(r, "proposal_id"), id, "proposal_apply_preflight.proposal_id"),
    revisionId: boundValue(uuidValue(r, "revision_id"), input.revisionId, "proposal_apply_preflight.revision_id"),
    changeHash: boundValue(hashValue(r, "change_hash"), input.changeHash, "proposal_apply_preflight.change_hash"),
    baseHash: hashValue(r, "base_hash"),
    preflightPassed: true,
    mode: "preflight_only",
    writePerformed: false,
  };
});
export const getWorkflow = (workspaceId: string, id: string, signal?: AbortSignal): Promise<WorkflowDetail> => request(`/api/v1/workflows/${encodeURIComponent(id)}`, signal === undefined ? undefined : { signal }).then((v) => {
  const r = record(v, "workflow");
  exact(r, ["id", "workspace_id", "definition_id", "status", "input", "output", "version", "created_at", "updated_at", "completed_at", "pause_requested", "cancel_requested"], "workflow");
  const input = requiredFieldValue(r, "input");
  const completedAt = r.completed_at === undefined ? undefined : dateTimeValue(r, "completed_at");
  return { id: boundValue(uuidValue(r, "id"), id, "workflow.id"), workspaceId: boundValue(uuidValue(r, "workspace_id"), workspaceId, "workflow.workspace_id"), definitionId: uuidValue(r, "definition_id"), status: workflowStatus(r, "status"), input, ...(r.output === undefined ? {} : { output: r.output }), version: integerValue(r, "version", 1), createdAt: dateTimeValue(r, "created_at"), updatedAt: dateTimeValue(r, "updated_at"), ...(completedAt === undefined ? {} : { completedAt }), pauseRequested: boolValue(r, "pause_requested"), cancelRequested: boolValue(r, "cancel_requested") };
});
export const decideProposal = (id: string, input: { revisionId: string; changeHash: string; decision: "approved" | "rejected"; proposalType: ProposalType }): Promise<ApprovalDecisionResult> => request(`/api/v1/proposals/${encodeURIComponent(id)}/approvals`, { method: "POST", headers: { "Idempotency-Key": `m9-approval-${id}-${input.decision}-${input.changeHash}` }, body: JSON.stringify({ revision_id: input.revisionId, change_hash: input.changeHash, decision: input.decision }) }).then((value) => {
  const r = record(value, "approval_decision");
  exact(r, ["id", "proposal_id", "revision_id", "change_hash", "decision", "approved_git_head", "workflow_run_id", "workflow_status_url", "dispatch_status", "decided_at"], "approval_decision");
  const decision = literalValue(r, "decision", ["approved", "rejected"]);
  boundValue(decision, input.decision, "approval_decision.decision");
  const approvedGitHead = r.approved_git_head === undefined ? undefined : gitHashValue(r, "approved_git_head");
  const workflowRunId = r.workflow_run_id === undefined ? undefined : uuidValue(r, "workflow_run_id");
  const workflowStatusUrl = stringValue(r, "workflow_status_url", false);
  const dispatchStatus = r.dispatch_status === undefined ? undefined : literalValue(r, "dispatch_status", ["queued", "running", "replayed"]);
  if (decision === "approved" && input.proposalType === "file_patch") {
    if (approvedGitHead === undefined || workflowRunId === undefined || workflowStatusUrl !== `/api/v1/workflows/${workflowRunId}` || dispatchStatus === undefined) {
      throw new BusinessApiError("INVALID_RESPONSE", "批准响应缺少写回 Workflow 绑定", false);
    }
  } else if (decision === "approved" && input.proposalType !== "file_patch") {
    if (approvedGitHead !== undefined || workflowRunId !== undefined || workflowStatusUrl !== undefined || dispatchStatus !== undefined) {
      throw new BusinessApiError("INVALID_RESPONSE", "Typed Proposal Approval 响应包含非法 Git/Workflow 字段", false);
    }
  } else if (approvedGitHead !== undefined || workflowRunId !== undefined || workflowStatusUrl !== undefined || dispatchStatus !== undefined) {
    throw new BusinessApiError("INVALID_RESPONSE", "驳回响应包含非法写回字段", false);
  }
  return {
    id: uuidValue(r, "id"),
    proposalId: boundValue(uuidValue(r, "proposal_id"), id, "approval_decision.proposal_id"),
    revisionId: boundValue(uuidValue(r, "revision_id"), input.revisionId, "approval_decision.revision_id"),
    changeHash: boundValue(hashValue(r, "change_hash"), input.changeHash, "approval_decision.change_hash"),
    decision,
    decidedAt: dateTimeValue(r, "decided_at"),
    ...(approvedGitHead === undefined ? {} : { approvedGitHead }),
    ...(workflowRunId === undefined ? {} : { workflowRunId }),
    ...(workflowStatusUrl === undefined ? {} : { workflowStatusUrl }),
    ...(dispatchStatus === undefined ? {} : { dispatchStatus }),
  };
});
export const controlWorkflow = (id: string, action: "pause" | "resume" | "cancel", expectedVersion: number): Promise<WorkflowControlResult> => request(`/api/v1/workflows/${encodeURIComponent(id)}/${action}`, { method: "POST", headers: { "Idempotency-Key": `m9-workflow-${id}-${action}-${expectedVersion}` }, body: JSON.stringify({ expected_version: expectedVersion }) }).then((value) => {
  const r = record(value, "workflow_control");
  exact(r, ["workflow_run_id", "status", "version", "status_url", "pause_requested", "cancel_requested"], "workflow_control");
  const workflowRunId = boundValue(uuidValue(r, "workflow_run_id"), id, "workflow_control.workflow_run_id");
  const statusUrl = stringValue(r, "status_url")!;
  if (statusUrl !== `/api/v1/workflows/${workflowRunId}`) throw new BusinessApiError("INVALID_RESPONSE", "Workflow 控制响应 URL 绑定不一致", false);
  const version = integerValue(r, "version", 1);
  if (version <= expectedVersion) throw new BusinessApiError("INVALID_RESPONSE", "Workflow 控制响应未推进版本", false);
  return { workflowRunId, status: workflowStatus(r, "status"), version, statusUrl, pauseRequested: boolValue(r, "pause_requested"), cancelRequested: boolValue(r, "cancel_requested") };
});
