/* eslint-disable @typescript-eslint/consistent-type-definitions, @typescript-eslint/no-non-null-assertion, @typescript-eslint/restrict-template-expressions */
import { authFetch } from "./auth";
import { graphNodeRefIdentity, graphRelationTypeCompatible, isSymmetricGraphRelationType } from "./graph";
import { ApiBoundaryError } from "./system-status";
import type { ArtifactImpactBinding, ReviewCardImpactBinding } from "./timeline";
import {
  canonicalUuidPattern as uuidPattern,
  hasOnlyKeys,
  isAbortError,
} from "../shared/codec";

export type Page<T> = { items: T[]; nextCursor?: string };
export type SourceSecurityStatus = "pending" | "passed" | "quarantined";
export type SourceIngestionStatus = "validating" | "parsing" | "parsed" | "chunking" | "chunked" | "parse_failed" | "cancelled";
export type SourceIndexStatus = "included" | "excluded";
export type ProposalType = "file_patch" | "restore_document" | "knowledge_change" | "publish_artifact" | "downstream_update";
export type ProposalTargetMode = "REPLACE" | "CREATE_ONLY";
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
  targetMode: ProposalTargetMode;
  baseHash: string;
  content: string;
  evidenceSummary: string;
}
export interface RestoreDocumentBinding {
  workspaceId: string;
  documentId: string;
  targetCommit: string;
  expectedHead: string;
  expectedDocumentVersion: number;
  previewHash: string;
  currentContentHash: string;
  targetContentHash: string;
  schemaVersion: "document-restore/v1";
}
export interface RestoreDocumentRevision extends FilePatchRevision {
  targetMode: "REPLACE";
  restore: RestoreDocumentBinding;
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
  | (ProposalDetailBase & { type: "restore_document"; targetPath: string; revision: RestoreDocumentRevision })
  | (ProposalDetailBase & { type: "knowledge_change"; revision: KnowledgeChangeRevision })
  | (ProposalDetailBase & { type: "publish_artifact"; revision: PublishArtifactRevision })
  | (ProposalDetailBase & { type: "downstream_update"; revision: DownstreamUpdateRevision });
export type WorkflowDetail = {
  id: string; workspaceId: string; definitionId: string; status: WorkflowStatus;
  input: unknown; output?: unknown; version: number; createdAt: string;
  updatedAt: string; completedAt?: string; pauseRequested: boolean; cancelRequested: boolean;
  humanTask?: WorkflowHumanTask;
};
export type WorkflowFormalReviewEvidence = {
  kind: "SOURCE_VERSION";
  sourceVersionId: string;
  sourceSpanId: string;
  contentHash: string;
  excerptHash: string;
};
export type WorkflowDocumentReviewEvidence = {
  kind: "DOCUMENT_REVISION";
  documentId: string;
  articleRevisionId: string;
  revisionNo: number;
  contentHash: string;
};
export type WorkflowReviewEvidence = WorkflowFormalReviewEvidence | WorkflowDocumentReviewEvidence;
export type WorkflowTopicOutlineReview = {
  kind: "TOPIC_OUTLINE";
  schemaVersion: 1;
  workspaceId: string;
  runId: string;
  taskId: string;
  nodeRunId: string;
  snapshotId: string;
  snapshotHash: string;
  templateRevisionId: string;
  templateHash: string;
  outline: {
    key: string;
    title: string;
    supports: WorkflowReviewEvidence[];
    gapCode: "EVIDENCE_UNAVAILABLE" | null;
  }[];
};
export type WorkflowMergeCategory = "DUPLICATE" | "COMPLEMENTARY" | "CONFLICT" | "UNIQUE";
export type WorkflowMergeComparisonReview = {
  kind: "MERGE_COMPARISON";
  schemaVersion: 1;
  workspaceId: string;
  runId: string;
  taskId: string;
  nodeRunId: string;
  snapshotId: string;
  snapshotHash: string;
  artifactId: string;
  revisionHash: string;
  defaultTargetPath: string;
  diffHash: string;
  diffPreview: string;
  diffTruncated: boolean;
  conflictCount: number;
  evidenceCount: number;
  documentCount: number;
  categories: { category: WorkflowMergeCategory; count: number }[];
  comparison: (WorkflowReviewEvidence & { category: WorkflowMergeCategory })[];
};
export type WorkflowHumanTaskReview = WorkflowTopicOutlineReview | WorkflowMergeComparisonReview;
export type WorkflowHumanTask = {
  id: string; runId: string; nodeRunId: string; status: "pending"; targetVersion: number;
  decisionKind: "approval" | "approval_with_target_path"; createdAt: string; expiresAt?: string;
  review: WorkflowHumanTaskReview | null;
};
export type ProposalCurrentContent = {
  proposalId: string; workspaceId: string; targetPath: string; content: string;
  targetMode: ProposalTargetMode; currentHash: string; baseHash: string; baseHashMatch: boolean;
};
export type ProposalApplyPreflight = {
  proposalId: string; revisionId: string; changeHash: string; targetMode: ProposalTargetMode; baseHash: string;
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

const hashPattern = /^[0-9a-f]{64}$/;
const absenceTokenPattern = /^workspace-target-absent\/v1:[0-9a-f]{64}$/;
const emptyAbsenceToken = `workspace-target-absent/v1:${"0".repeat(64)}`;
const rfc3339Pattern = /^\d{4}-(?:0[1-9]|1[0-2])-(?:0[1-9]|[12]\d|3[01])T(?:[01]\d|2[0-3]):[0-5]\d:[0-5]\d(?:\.\d{1,9})?(?:Z|[+-](?:[01]\d|2[0-3]):[0-5]\d)$/;
const maxProposalCurrentContentBytes = 1024 * 1024;
const maxKnowledgeRevisionTextBytes = 4096;
const maxWorkflowDiffPreviewBytes = 32 * 1024;
const utf8Encoder = new TextEncoder();
const fileWritebackProposalType = (value: ProposalType): value is "file_patch" | "restore_document" =>
  value === "file_patch" || value === "restore_document";
const sha256Hex = async (value: string): Promise<string> => {
  const digest = await globalThis.crypto.subtle.digest("SHA-256", utf8Encoder.encode(value));
  return [...new Uint8Array(digest)].map((byte) => byte.toString(16).padStart(2, "0")).join("");
};
const validWorkspaceTargetPath = (value: string): boolean => {
  if (value === "" || value !== value.trim() || value.startsWith("/") || value.includes("\\") || value.includes("\u0000")) return false;
  const parts = value.split("/");
  return !parts.some((part) => part === "" || part === "." || part === "..");
};
const validWorkspaceMarkdownPath = (value: string): boolean => {
  const lower = value.toLowerCase();
  return validWorkspaceTargetPath(value)
    && utf8Encoder.encode(value).byteLength <= 4096
    && !/[\u0001-\u001f\u007f]/.test(value)
    && value.split("/")[0] !== ".git"
    && value.split("/")[0] !== ".knowledge"
    && (lower.endsWith(".md") || lower.endsWith(".markdown"));
};
const record = (value: unknown, field: string): Record<string, unknown> => {
  if (typeof value !== "object" || value === null || Array.isArray(value)) throw new BusinessApiError("INVALID_RESPONSE", `响应结构无效：${field}`, false);
  return value as Record<string, unknown>;
};
const exact = (r: Record<string, unknown>, fields: readonly string[], label: string): void => {
  if (!hasOnlyKeys(r, fields)) throw new BusinessApiError("INVALID_RESPONSE", `响应包含未知字段：${label}`, false);
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
const targetBaseValue = (r: Record<string, unknown>, field: string, targetMode: ProposalTargetMode): string => {
  const value = stringValue(r, field)!;
  const valid = targetMode === "REPLACE"
    ? hashPattern.test(value)
    : absenceTokenPattern.test(value) && value !== emptyAbsenceToken;
  if (!valid) throw new BusinessApiError("INVALID_RESPONSE", `响应目标基线无效：${field}`, false);
  return value;
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

type WorkflowReviewBinding = { workspaceId: string; runId: string; taskId: string; nodeRunId: string };
const workflowMergeCategories = ["DUPLICATE", "COMPLEMENTARY", "CONFLICT", "UNIQUE"] as const;

const decodeWorkflowReviewEvidence = (value: unknown, field: string, extraFields: readonly string[] = []): WorkflowReviewEvidence => {
  const evidence = record(value, field);
  const kind = literalValue(evidence, "kind", ["SOURCE_VERSION", "DOCUMENT_REVISION"] as const);
  if (kind === "SOURCE_VERSION") {
    exact(evidence, ["kind", "source_version_id", "source_span_id", "content_hash", "excerpt_hash", ...extraFields], field);
    return {
      kind,
      sourceVersionId: uuidValue(evidence, "source_version_id"),
      sourceSpanId: uuidValue(evidence, "source_span_id"),
      contentHash: hashValue(evidence, "content_hash"),
      excerptHash: hashValue(evidence, "excerpt_hash"),
    };
  }
  exact(evidence, ["kind", "document_id", "article_revision_id", "revision_no", "content_hash", ...extraFields], field);
  return {
    kind,
    documentId: uuidValue(evidence, "document_id"),
    articleRevisionId: uuidValue(evidence, "article_revision_id"),
    revisionNo: integerValue(evidence, "revision_no", 1),
    contentHash: hashValue(evidence, "content_hash"),
  };
};

const assertUniqueWorkflowEvidence = (items: readonly WorkflowReviewEvidence[], field: string): void => {
  const identities = items.map((item) => item.kind === "SOURCE_VERSION"
    ? `SOURCE_VERSION\u0000${item.sourceVersionId}\u0000${item.sourceSpanId}`
    : `DOCUMENT_REVISION\u0000${item.documentId}\u0000${item.articleRevisionId}`);
  if (new Set(identities).size !== identities.length) {
    throw new BusinessApiError("INVALID_RESPONSE", `Workflow 审阅证据重复：${field}`, false);
  }
};

const decodeWorkflowReviewBinding = (review: Record<string, unknown>, expected: WorkflowReviewBinding): WorkflowReviewBinding => ({
  workspaceId: boundValue(uuidValue(review, "workspace_id"), expected.workspaceId, "workflow.human_task.review.workspace_id"),
  runId: boundValue(uuidValue(review, "run_id"), expected.runId, "workflow.human_task.review.run_id"),
  taskId: boundValue(uuidValue(review, "task_id"), expected.taskId, "workflow.human_task.review.task_id"),
  nodeRunId: boundValue(uuidValue(review, "node_run_id"), expected.nodeRunId, "workflow.human_task.review.node_run_id"),
});

const decodeWorkflowHumanTaskReview = (value: unknown, expected: WorkflowReviewBinding): WorkflowHumanTaskReview | null => {
  if (value === null) return null;
  const review = record(value, "workflow.human_task.review");
  const kind = literalValue(review, "kind", ["TOPIC_OUTLINE", "MERGE_COMPARISON"] as const);
  if (integerValue(review, "schema_version", 1) !== 1) {
    throw new BusinessApiError("INVALID_RESPONSE", "Workflow 审阅投影版本无效", false);
  }
  const binding = decodeWorkflowReviewBinding(review, expected);
  const snapshotId = uuidValue(review, "snapshot_id");
  const snapshotHash = hashValue(review, "snapshot_hash");
  if (kind === "TOPIC_OUTLINE") {
    exact(review, ["kind", "schema_version", "workspace_id", "run_id", "task_id", "node_run_id", "snapshot_id", "snapshot_hash", "template_revision_id", "template_hash", "outline"], "workflow.human_task.review");
    const outline = recordArray(requiredFieldValue(review, "outline"), "workflow.human_task.review.outline", 1, 24).map((rawSection, sectionIndex) => {
      const field = `workflow.human_task.review.outline[${String(sectionIndex)}]`;
      exact(rawSection, ["key", "title", "supports", "gap_code"], field);
      const supports = recordArray(requiredFieldValue(rawSection, "supports"), `${field}.supports`, 0, 32)
        .map((item, evidenceIndex) => decodeWorkflowReviewEvidence(item, `${field}.supports[${String(evidenceIndex)}]`));
      assertUniqueWorkflowEvidence(supports, `${field}.supports`);
      const rawGapCode = requiredFieldValue(rawSection, "gap_code");
      const gapCode = rawGapCode === null
        ? null
        : literalValue(rawSection, "gap_code", ["EVIDENCE_UNAVAILABLE"] as const);
      if ((supports.length > 0) === (gapCode !== null)) {
        throw new BusinessApiError("INVALID_RESPONSE", "Workflow 大纲证据与 GAP 语义不一致", false);
      }
      return {
        key: boundedNonEmptyStringValue(rawSection, "key", 128),
        title: boundedNonEmptyStringValue(rawSection, "title", 512),
        supports,
        gapCode,
      };
    });
    if (new Set(outline.map((section) => section.key)).size !== outline.length) {
      throw new BusinessApiError("INVALID_RESPONSE", "Workflow 大纲章节 Key 重复", false);
    }
    return {
      kind,
      schemaVersion: 1,
      ...binding,
      snapshotId,
      snapshotHash,
      templateRevisionId: uuidValue(review, "template_revision_id"),
      templateHash: hashValue(review, "template_hash"),
      outline,
    };
  }

  exact(review, ["kind", "schema_version", "workspace_id", "run_id", "task_id", "node_run_id", "snapshot_id", "snapshot_hash", "artifact_id", "revision_hash", "default_target_path", "diff_hash", "diff_preview", "diff_truncated", "conflict_count", "evidence_count", "document_count", "categories", "comparison"], "workflow.human_task.review");
  const conflictCount = integerValue(review, "conflict_count");
  const evidenceCount = integerValue(review, "evidence_count");
  const documentCount = integerValue(review, "document_count");
  const categories = recordArray(requiredFieldValue(review, "categories"), "workflow.human_task.review.categories", 4, 4).map((rawCategory, index) => {
    const field = `workflow.human_task.review.categories[${String(index)}]`;
    exact(rawCategory, ["category", "count"], field);
    const category = literalValue(rawCategory, "category", workflowMergeCategories);
    if (category !== workflowMergeCategories[index]) {
      throw new BusinessApiError("INVALID_RESPONSE", "Workflow 合并分类顺序无效", false);
    }
    return { category, count: integerValue(rawCategory, "count") };
  });
  const categoryTotal = categories.reduce((total, category) => total + category.count, 0);
  const conflictCategory = categories[2];
  const materialCount = evidenceCount + documentCount;
  if (!Number.isSafeInteger(materialCount) || !Number.isSafeInteger(categoryTotal) || categoryTotal !== materialCount || conflictCategory?.count !== conflictCount) {
    throw new BusinessApiError("INVALID_RESPONSE", "Workflow 合并分类计数不一致", false);
  }
  const comparison = recordArray(requiredFieldValue(review, "comparison"), "workflow.human_task.review.comparison", 0, 64).map((rawItem, index) => {
    const field = `workflow.human_task.review.comparison[${String(index)}]`;
    return {
      category: literalValue(rawItem, "category", workflowMergeCategories),
      ...decodeWorkflowReviewEvidence(rawItem, field, ["category"]),
    };
  });
  assertUniqueWorkflowEvidence(comparison, "workflow.human_task.review.comparison");
  const comparisonCounts = new Map<WorkflowMergeCategory, number>(workflowMergeCategories.map((category) => [category, 0]));
  for (const item of comparison) comparisonCounts.set(item.category, (comparisonCounts.get(item.category) ?? 0) + 1);
  const previewEvidenceCount = comparison.filter((item) => item.kind === "SOURCE_VERSION").length;
  const previewDocumentCount = comparison.length - previewEvidenceCount;
  if (comparison.length !== Math.min(materialCount, 64) || previewEvidenceCount > evidenceCount || previewDocumentCount > documentCount ||
      categories.some((category) => (comparisonCounts.get(category.category) ?? 0) > category.count)) {
    throw new BusinessApiError("INVALID_RESPONSE", "Workflow 合并证据预览计数无效", false);
  }
  const diffPreview = boundedNonEmptyStringValue(review, "diff_preview", maxWorkflowDiffPreviewBytes);
  const diffTruncated = boolValue(review, "diff_truncated");
  const defaultTargetPath = boundedNonEmptyStringValue(review, "default_target_path", 4096);
  const diffPreviewBytes = utf8Encoder.encode(diffPreview).byteLength;
  if (!validWorkspaceMarkdownPath(defaultTargetPath) || !diffPreview.startsWith("--- /dev/null\n+++ b/organized-result.md\n@@ ")
    || diffTruncated && diffPreviewBytes < maxWorkflowDiffPreviewBytes - 3) {
    throw new BusinessApiError("INVALID_RESPONSE", "Workflow 合并 Diff 预览无效", false);
  }
  return {
    kind,
    schemaVersion: 1,
    ...binding,
    snapshotId,
    snapshotHash,
    artifactId: uuidValue(review, "artifact_id"),
    revisionHash: hashValue(review, "revision_hash"),
    defaultTargetPath,
    diffHash: hashValue(review, "diff_hash"),
    diffPreview,
    diffTruncated,
    conflictCount,
    evidenceCount,
    documentCount,
    categories,
    comparison,
  };
};

const decodeWorkflowHumanTask = (value: unknown, workspaceId: string, runId: string): WorkflowHumanTask | undefined => {
  if (value === null) return undefined;
  const task = record(value, "workflow.human_task");
  exact(task, ["id", "run_id", "node_run_id", "status", "expected_input_schema", "target_version", "expires_at", "created_at", "review"], "workflow.human_task");
  literalValue(task, "status", ["pending"] as const);
  const schema = record(task.expected_input_schema, "workflow.human_task.expected_input_schema");
  exact(schema, ["type", "required", "properties", "additionalProperties"], "workflow.human_task.expected_input_schema");
  if (schema.type !== "object" || schema.additionalProperties !== false || !Array.isArray(schema.required)) throw new BusinessApiError("INVALID_RESPONSE", "Workflow Human Task Schema 无效", false);
  const required = schema.required.map((item) => {
    if (typeof item !== "string") throw new BusinessApiError("INVALID_RESPONSE", "Workflow Human Task required 无效", false);
    return item;
  });
  if (new Set(required).size !== required.length || !required.includes("approved") || required.some((item) => item !== "approved" && item !== "target_path")) throw new BusinessApiError("INVALID_RESPONSE", "Workflow Human Task required 不受支持", false);
  const properties = record(schema.properties, "workflow.human_task.expected_input_schema.properties");
  const decisionKind = Object.hasOwn(properties, "target_path") ? "approval_with_target_path" : "approval";
  exact(properties, decisionKind === "approval" ? ["approved"] : ["approved", "target_path"], "workflow.human_task.expected_input_schema.properties");
  const approved = record(properties.approved, "workflow.human_task.expected_input_schema.properties.approved");
  exact(approved, ["type"], "workflow.human_task.expected_input_schema.properties.approved");
  if (approved.type !== "boolean") throw new BusinessApiError("INVALID_RESPONSE", "Workflow Human Task approved Schema 无效", false);
  if (decisionKind === "approval_with_target_path") {
    const targetPath = record(properties.target_path, "workflow.human_task.expected_input_schema.properties.target_path");
    exact(targetPath, ["type"], "workflow.human_task.expected_input_schema.properties.target_path");
    if (targetPath.type !== "string") throw new BusinessApiError("INVALID_RESPONSE", "Workflow Human Task target_path Schema 无效", false);
  }
  const expiresAt = task.expires_at === null ? undefined : dateTimeValue(task, "expires_at");
  const id = uuidValue(task, "id");
  const boundRunId = boundValue(uuidValue(task, "run_id"), runId, "workflow.human_task.run_id");
  const nodeRunId = uuidValue(task, "node_run_id");
  const review = decodeWorkflowHumanTaskReview(requiredFieldValue(task, "review"), { workspaceId, runId: boundRunId, taskId: id, nodeRunId });
  if (review?.kind === "TOPIC_OUTLINE" && decisionKind !== "approval") {
    throw new BusinessApiError("INVALID_RESPONSE", "Workflow 大纲审阅决策 Schema 不一致", false);
  }
  if (review?.kind === "MERGE_COMPARISON" && decisionKind !== "approval_with_target_path") {
    throw new BusinessApiError("INVALID_RESPONSE", "Workflow 合并审阅决策 Schema 不一致", false);
  }
  return {
    id, runId: boundRunId, nodeRunId, status: "pending", targetVersion: integerValue(task, "target_version", 1),
    decisionKind, createdAt: dateTimeValue(task, "created_at"), ...(expiresAt === undefined ? {} : { expiresAt }),
    review,
  };
};
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
  const type = literalValue(r, "proposal_type", ["file_patch", "restore_document", "knowledge_change", "publish_artifact", "downstream_update"]);
  const revisionId = uuidValue(r, "revision_id");
  const changeHash = hashValue(r, "change_hash");
  const riskLevel = proposalRiskLevel(r, "risk_level");
  const target = stringValue(r, "target")!;
  if (fileWritebackProposalType(type) && !validWorkspaceTargetPath(target)) {
    throw new BusinessApiError("INVALID_RESPONSE", "文件型 Proposal 摘要目标路径无效", false);
  }
  if (type !== "file_patch" && riskLevel !== "HIGH") {
    throw new BusinessApiError("INVALID_RESPONSE", "Typed Proposal 摘要必须使用 HIGH 风险等级", false);
  }
  const approval = decodeProposalApproval(r.approval, id, revisionId, changeHash, type);
  return { id, workspaceId, type, status: proposalStatus(r, "status"), target, riskLevel, risk: stringValue(r, "risk")!, revisionId, changeHash, ...(approval === undefined ? {} : { approval }), createdAt: dateTimeValue(r, "created_at"), updatedAt: dateTimeValue(r, "updated_at") };
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
  if (!fileWritebackProposalType(proposalType) && (approvedGitHead !== undefined || hasWorkflowBinding)) {
    throw new BusinessApiError("INVALID_RESPONSE", "Typed Proposal Approval 不应包含 Git/Workflow 写回字段", false);
  }
  let writebackState: ApprovalSnapshot["writebackState"];
  if (fileWritebackProposalType(proposalType) && decision === "approved") {
    if (hasWorkflowBinding && approvedGitHead === undefined) {
      throw new BusinessApiError("INVALID_RESPONSE", "文件型 Proposal Approval Workflow 缺少 Git 基线", false);
    }
    if (proposalType === "restore_document" && approvedGitHead === undefined) {
      throw new BusinessApiError("INVALID_RESPONSE", "Document Restore Approval 缺少预览 Git 基线", false);
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

export const getProposal = (workspaceId: string, id: string, signal?: AbortSignal): Promise<ProposalDetail> => request(`/api/v1/proposals/${encodeURIComponent(id)}`, signal === undefined ? undefined : { signal }).then(async (v) => {
  const r = record(v, "proposal");
  const type = literalValue(r, "proposal_type", ["file_patch", "restore_document", "knowledge_change", "publish_artifact", "downstream_update"]);
  exact(r, fileWritebackProposalType(type)
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
  if (fileWritebackProposalType(type)) {
    exact(revision, type === "restore_document"
      ? ["id", "revision_no", "target_mode", "base_hash", "content", "evidence_summary", "risk", "rollback_plan", "change_hash", "created_at", "restore"]
      : ["id", "revision_no", "target_mode", "base_hash", "content", "evidence_summary", "risk", "rollback_plan", "change_hash", "created_at"], "proposal.revision");
    const targetMode = type === "restore_document"
      ? literalValue(revision, "target_mode", ["REPLACE"] as const)
      : literalValue(revision, "target_mode", ["REPLACE", "CREATE_ONLY"] as const);
    const decodedRevision: FilePatchRevision = {
      id: uuidValue(revision, "id"),
      revisionNo: integerValue(revision, "revision_no", 1),
      targetMode,
      baseHash: targetBaseValue(revision, "base_hash", targetMode),
      content: nonEmptyStringValue(revision, "content"),
      evidenceSummary: nonEmptyStringValue(revision, "evidence_summary"),
      risk: nonEmptyStringValue(revision, "risk"),
      rollbackPlan: nonEmptyStringValue(revision, "rollback_plan"),
      changeHash: hashValue(revision, "change_hash"),
      createdAt: dateTimeValue(revision, "created_at"),
    };
    const targetPath = nonEmptyStringValue(r, "target_path");
    if (!validWorkspaceTargetPath(targetPath)) throw new BusinessApiError("INVALID_RESPONSE", "文件型 Proposal 目标路径无效", false);
    const approval = decodeProposalApproval(approvalValue, id, decodedRevision.id, decodedRevision.changeHash, type, true);
    if (type === "restore_document") {
      if (base.riskLevel !== "HIGH") throw new BusinessApiError("INVALID_RESPONSE", "Document Restore Proposal 必须使用 HIGH 风险等级", false);
      const restore = record(revision.restore, "proposal.revision.restore");
      exact(restore, [
        "workspace_id", "document_id", "target_commit", "expected_head", "expected_document_version",
        "preview_hash", "current_content_hash", "target_content_hash", "schema_version",
      ], "proposal.revision.restore");
      const restoreWorkspaceId = boundValue(uuidValue(restore, "workspace_id"), base.workspaceId, "proposal.revision.restore.workspace_id");
      const documentId = uuidValue(restore, "document_id");
      const targetCommit = gitHashValue(restore, "target_commit");
      const expectedHead = gitHashValue(restore, "expected_head");
      const currentContentHash = boundValue(hashValue(restore, "current_content_hash"), decodedRevision.baseHash, "proposal.revision.restore.current_content_hash");
      const targetContentHash = hashValue(restore, "target_content_hash");
      if (restoreWorkspaceId === documentId || targetCommit === expectedHead || targetCommit.length !== expectedHead.length || currentContentHash === targetContentHash) {
        throw new BusinessApiError("INVALID_RESPONSE", "Document Restore 来源身份不一致", false);
      }
      if (await sha256Hex(decodedRevision.content) !== targetContentHash) {
        throw new BusinessApiError("INVALID_RESPONSE", "Document Restore 目标正文与 Content Hash 不一致", false);
      }
      if (approval?.decision === "approved" && approval.approvedGitHead !== expectedHead) {
        throw new BusinessApiError("INVALID_RESPONSE", "Document Restore Approval 未绑定预览 HEAD", false);
      }
      const restoreBinding: RestoreDocumentBinding = {
        workspaceId: restoreWorkspaceId,
        documentId,
        targetCommit,
        expectedHead,
        expectedDocumentVersion: integerValue(restore, "expected_document_version", 1),
        previewHash: hashValue(restore, "preview_hash"),
        currentContentHash,
        targetContentHash,
        schemaVersion: literalValue(restore, "schema_version", ["document-restore/v1"]),
      };
      return {
        ...base,
        type,
        targetPath,
        revision: { ...decodedRevision, targetMode: "REPLACE", restore: restoreBinding },
        ...(approval === undefined ? {} : { approval }),
      };
    }
    return {
      ...base,
      type,
      targetPath,
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
export const getProposalCurrentContent = (workspaceId: string, id: string, binding: { targetPath: string; targetMode: ProposalTargetMode; baseHash: string }, signal?: AbortSignal): Promise<ProposalCurrentContent> => request(`/api/v1/proposals/${encodeURIComponent(id)}/current-content`, signal === undefined ? undefined : { signal }).then((v) => {
  const r = record(v, "proposal_current_content");
  exact(r, ["proposal_id", "workspace_id", "target_path", "target_mode", "content", "current_hash", "base_hash", "base_hash_match"], "proposal_current_content");
  const targetMode = boundValue(literalValue(r, "target_mode", ["REPLACE", "CREATE_ONLY"] as const), binding.targetMode, "proposal_current_content.target_mode") as ProposalTargetMode;
  const currentHash = targetBaseValue(r, "current_hash", targetMode);
  const baseHash = boundValue(targetBaseValue(r, "base_hash", targetMode), binding.baseHash, "proposal_current_content.base_hash");
  const baseHashMatch = boolValue(r, "base_hash_match");
  if (baseHashMatch !== (currentHash === baseHash)) throw new BusinessApiError("INVALID_RESPONSE", "Proposal current-content Hash 语义不一致", false);
  const content = boundedCurrentContent(r);
  if (targetMode === "CREATE_ONLY" && content !== "") throw new BusinessApiError("INVALID_RESPONSE", "CREATE_ONLY 审阅基线必须为空", false);
  return {
    proposalId: boundValue(uuidValue(r, "proposal_id"), id, "proposal_current_content.proposal_id"),
    workspaceId: boundValue(uuidValue(r, "workspace_id"), workspaceId, "proposal_current_content.workspace_id"),
    targetPath: boundValue(stringValue(r, "target_path")!, binding.targetPath, "proposal_current_content.target_path"),
    targetMode,
    content,
    currentHash,
    baseHash,
    baseHashMatch,
  };
});
export const preflightProposal = (id: string, input: { revisionId: string; changeHash: string; targetMode: ProposalTargetMode; baseHash: string }): Promise<ProposalApplyPreflight> => request(`/api/v1/proposals/${encodeURIComponent(id)}/apply-preflight`, { method: "POST", body: JSON.stringify({ revision_id: input.revisionId, approved_change_hash: input.changeHash }) }).then((v) => {
  const r = record(v, "proposal_apply_preflight");
  exact(r, ["proposal_id", "revision_id", "change_hash", "target_mode", "base_hash", "preflight_passed", "mode", "write_performed"], "proposal_apply_preflight");
  if (r.preflight_passed !== true || r.mode !== "preflight_only" || r.write_performed !== false) throw new BusinessApiError("INVALID_RESPONSE", "Proposal preflight 响应语义无效", false);
  const targetMode = boundValue(literalValue(r, "target_mode", ["REPLACE", "CREATE_ONLY"] as const), input.targetMode, "proposal_apply_preflight.target_mode") as ProposalTargetMode;
  return {
    proposalId: boundValue(uuidValue(r, "proposal_id"), id, "proposal_apply_preflight.proposal_id"),
    revisionId: boundValue(uuidValue(r, "revision_id"), input.revisionId, "proposal_apply_preflight.revision_id"),
    changeHash: boundValue(hashValue(r, "change_hash"), input.changeHash, "proposal_apply_preflight.change_hash"),
    targetMode,
    baseHash: boundValue(targetBaseValue(r, "base_hash", targetMode), input.baseHash, "proposal_apply_preflight.base_hash"),
    preflightPassed: true,
    mode: "preflight_only",
    writePerformed: false,
  };
});
export const getWorkflow = (workspaceId: string, id: string, signal?: AbortSignal): Promise<WorkflowDetail> => request(`/api/v1/workflows/${encodeURIComponent(id)}`, {
  ...(signal === undefined ? {} : { signal }),
  headers: { "X-Workspace-ID": workspaceId },
}).then((v) => {
  const r = record(v, "workflow");
  exact(r, ["id", "workspace_id", "definition_id", "status", "input", "output", "version", "created_at", "updated_at", "completed_at", "pause_requested", "cancel_requested", "human_task"], "workflow");
  const input = requiredFieldValue(r, "input");
  const completedAt = r.completed_at === undefined ? undefined : dateTimeValue(r, "completed_at");
  const runId = boundValue(uuidValue(r, "id"), id, "workflow.id");
  const humanTask = decodeWorkflowHumanTask(requiredFieldValue(r, "human_task"), workspaceId, runId);
  const status = workflowStatus(r, "status");
  if ((status === "waiting_for_human") !== (humanTask !== undefined)) throw new BusinessApiError("INVALID_RESPONSE", "Workflow 与 Human Task 状态不一致", false);
  return { id: runId, workspaceId: boundValue(uuidValue(r, "workspace_id"), workspaceId, "workflow.workspace_id"), definitionId: uuidValue(r, "definition_id"), status, input, ...(r.output === undefined ? {} : { output: r.output }), version: integerValue(r, "version", 1), createdAt: dateTimeValue(r, "created_at"), updatedAt: dateTimeValue(r, "updated_at"), ...(completedAt === undefined ? {} : { completedAt }), pauseRequested: boolValue(r, "pause_requested"), cancelRequested: boolValue(r, "cancel_requested"), ...(humanTask === undefined ? {} : { humanTask }) };
});

export const submitWorkflowHumanDecision = (
  workspaceId: string,
  runId: string,
  task: WorkflowHumanTask,
  decision: { approved: boolean; targetPath?: string },
): Promise<void> => {
  if (task.runId !== runId || task.targetVersion < 1 || task.review === null) throw new BusinessApiError("INVALID_RESPONSE", "Workflow Human Task 绑定无效", false);
  const targetPath = decision.targetPath?.trim() ?? "";
  if (task.decisionKind === "approval_with_target_path" && decision.approved && !validWorkspaceMarkdownPath(targetPath)) {
    throw new BusinessApiError("INVALID_RESPONSE", "合并目标必须是 Workspace 内的相对路径", false);
  }
  const payload = task.decisionKind === "approval_with_target_path"
    ? { approved: decision.approved, target_path: targetPath }
    : { approved: decision.approved };
  return request(`/api/v1/workflows/${encodeURIComponent(runId)}/human-tasks/${encodeURIComponent(task.id)}/decision`, {
    method: "POST",
    headers: { "Idempotency-Key": `workflow-human-${task.id}-${String(task.targetVersion)}`, "X-Workspace-ID": workspaceId },
    body: JSON.stringify({ target_version: task.targetVersion, decision: payload }),
  }).then((value) => {
    const response = record(value, "workflow_human_decision");
    exact(response, ["id", "run_id", "node_run_id", "status", "target_version", "decision", "submitted_at"], "workflow_human_decision");
    boundValue(uuidValue(response, "id"), task.id, "workflow_human_decision.id");
    boundValue(uuidValue(response, "run_id"), runId, "workflow_human_decision.run_id");
    boundValue(uuidValue(response, "node_run_id"), task.nodeRunId, "workflow_human_decision.node_run_id");
    literalValue(response, "status", ["submitted"] as const);
    if (integerValue(response, "target_version", 1) !== task.targetVersion) throw new BusinessApiError("INVALID_RESPONSE", "Workflow Human Task 提交结果无效", false);
    dateTimeValue(response, "submitted_at");
    const returnedDecision = record(response.decision, "workflow_human_decision.decision");
    exact(returnedDecision, Object.keys(payload), "workflow_human_decision.decision");
    if (returnedDecision.approved !== decision.approved || ("target_path" in payload && returnedDecision.target_path !== payload.target_path)) throw new BusinessApiError("INVALID_RESPONSE", "Workflow Human Task 决策回执不一致", false);
  });
};
export const decideProposal = (id: string, input: { revisionId: string; changeHash: string; decision: "approved" | "rejected"; proposalType: ProposalType }): Promise<ApprovalDecisionResult> => request(`/api/v1/proposals/${encodeURIComponent(id)}/approvals`, { method: "POST", headers: { "Idempotency-Key": `m9-approval-${id}-${input.decision}-${input.changeHash}` }, body: JSON.stringify({ revision_id: input.revisionId, change_hash: input.changeHash, decision: input.decision }) }).then((value) => {
  const r = record(value, "approval_decision");
  exact(r, ["id", "proposal_id", "revision_id", "change_hash", "decision", "approved_git_head", "workflow_run_id", "workflow_status_url", "dispatch_status", "decided_at"], "approval_decision");
  const decision = literalValue(r, "decision", ["approved", "rejected"]);
  boundValue(decision, input.decision, "approval_decision.decision");
  const approvedGitHead = r.approved_git_head === undefined ? undefined : gitHashValue(r, "approved_git_head");
  const workflowRunId = r.workflow_run_id === undefined ? undefined : uuidValue(r, "workflow_run_id");
  const workflowStatusUrl = stringValue(r, "workflow_status_url", false);
  const dispatchStatus = r.dispatch_status === undefined ? undefined : literalValue(r, "dispatch_status", ["queued", "running", "replayed"]);
  if (decision === "approved" && fileWritebackProposalType(input.proposalType)) {
    if (approvedGitHead === undefined || workflowRunId === undefined || workflowStatusUrl !== `/api/v1/workflows/${workflowRunId}` || dispatchStatus === undefined) {
      throw new BusinessApiError("INVALID_RESPONSE", "批准响应缺少写回 Workflow 绑定", false);
    }
  } else if (decision === "approved" && !fileWritebackProposalType(input.proposalType)) {
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
export const controlWorkflow = (workspaceId: string, id: string, action: "pause" | "resume" | "cancel", expectedVersion: number): Promise<WorkflowControlResult> => request(`/api/v1/workflows/${encodeURIComponent(id)}/${action}`, { method: "POST", headers: { "Idempotency-Key": `m9-workflow-${id}-${action}-${expectedVersion}`, "X-Workspace-ID": workspaceId }, body: JSON.stringify({ expected_version: expectedVersion }) }).then((value) => {
  const r = record(value, "workflow_control");
  exact(r, ["workflow_run_id", "status", "version", "status_url", "pause_requested", "cancel_requested"], "workflow_control");
  const workflowRunId = boundValue(uuidValue(r, "workflow_run_id"), id, "workflow_control.workflow_run_id");
  const statusUrl = stringValue(r, "status_url")!;
  if (statusUrl !== `/api/v1/workflows/${workflowRunId}`) throw new BusinessApiError("INVALID_RESPONSE", "Workflow 控制响应 URL 绑定不一致", false);
  const version = integerValue(r, "version", 1);
  if (version <= expectedVersion) throw new BusinessApiError("INVALID_RESPONSE", "Workflow 控制响应未推进版本", false);
  return { workflowRunId, status: workflowStatus(r, "status"), version, statusUrl, pauseRequested: boolValue(r, "pause_requested"), cancelRequested: boolValue(r, "cancel_requested") };
});
