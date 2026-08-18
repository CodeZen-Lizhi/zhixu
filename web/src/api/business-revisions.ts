import {
  BusinessApiError,
  decodeProposalDetail,
  requestBusinessJSON,
  type FilePatchRevision,
  type ProposalDetail,
} from "./business";
import { BusinessRevisionsApi } from "./generated/apis/BusinessRevisionsApi";
import {
  generatedConfiguration,
  generatedRawResponse,
  generatedRequestInit,
} from "./generated-client";
import { canonicalUuidPattern, hasOnlyKeys } from "../shared/codec";

const businessRevisionsApi = new BusinessRevisionsApi(generatedConfiguration);

export const proposalRevisionMaxBytes = 1024 * 1024;
export const proposalRevisionMetadataMaxBytes = 64 * 1024;
export const proposalRevisionConflictLimit = 1024;
export const proposalRevisionMergeAlgorithm = "git-merge-file";
export const proposalRevisionMergeAlgorithmVersion = "diff3/myers/marker32/v1";

const previewSchemaVersion = "proposal-text-merge-preview/v1";
const hashPattern = /^[0-9a-f]{64}$/;
const gitHashPattern = /^(?:[0-9a-f]{40}|[0-9a-f]{64})$/;
const rfc3339Pattern = /^\d{4}-(?:0[1-9]|1[0-2])-(?:0[1-9]|[12]\d|3[01])T(?:[01]\d|2[0-3]):[0-5]\d:[0-5]\d(?:\.\d{1,9})?(?:Z|[+-](?:[01]\d|2[0-3]):[0-5]\d)$/;
const utf8Encoder = new TextEncoder();

export interface ProposalRevisionPreviewBinding {
  workspaceId: string;
  proposalId: string;
  expectedProposalVersion: number;
  sourceRevisionId: string;
  sourceRevisionNo: number;
  sourceChangeHash: string;
  targetPath: string;
}

export interface ProposalRevisionTextSnapshot {
  content: string;
  hash: string;
  byteSize: number;
}

export interface ProposalRevisionConflict {
  id: string;
  ordinal: number;
  base: string;
  current: string;
  proposed: string;
}

export interface ProposalRevisionMergePreview {
  schemaVersion: typeof previewSchemaVersion;
  mergeAlgorithm: typeof proposalRevisionMergeAlgorithm;
  mergeAlgorithmVersion: typeof proposalRevisionMergeAlgorithmVersion;
  mergeFingerprint: string;
  proposalId: string;
  workspaceId: string;
  proposalVersion: number;
  sourceRevisionId: string;
  sourceRevisionNo: number;
  sourceChangeHash: string;
  targetPath: string;
  targetMode: "REPLACE";
  base: ProposalRevisionTextSnapshot;
  current: ProposalRevisionTextSnapshot;
  proposed: ProposalRevisionTextSnapshot;
  candidate: ProposalRevisionTextSnapshot;
  conflictCount: number;
  conflicts: ProposalRevisionConflict[];
}

export interface AppendProposalRevisionInput {
  expectedProposalVersion: number;
  sourceRevisionId: string;
  sourceRevisionNo: number;
  sourceChangeHash: string;
  expectedCurrentHash: string;
  mergeFingerprint: string;
  mergeAlgorithm: typeof proposalRevisionMergeAlgorithm;
  mergeAlgorithmVersion: typeof proposalRevisionMergeAlgorithmVersion;
  content: string;
  evidenceSummary: string;
  risk: string;
  rollbackPlan: string;
  resolvedConflictIds: string[];
}

export type AppendedProposalRevision = Extract<ProposalDetail, { type: "file_patch" }> & {
  revision: FilePatchRevision;
};

export interface AppendProposalRevisionResult {
  proposal: AppendedProposalRevision;
  replayed: boolean;
}

export interface ProposalRevisionHistoryBinding {
  workspaceId: string;
  proposalId: string;
  targetPath: string;
}

export interface ProposalRevisionHistoryApproval {
  id: string;
  proposalId: string;
  revisionId: string;
  changeHash: string;
  decision: "approved" | "rejected";
  approvedGitHead?: string;
  decidedAt: string;
}

export interface ProposalRevisionHistoryWorkflow {
  id: string;
  statusUrl: string;
}

export interface ProposalRevisionHistoryItem {
  proposalId: string;
  revisionId: string;
  revisionNo: number;
  targetPath: string;
  targetMode: "REPLACE";
  baseHash: string;
  changeHash: string;
  baseAvailable: boolean;
  current: boolean;
  approval?: ProposalRevisionHistoryApproval;
  workflow?: ProposalRevisionHistoryWorkflow;
  createdAt: string;
}

export interface ProposalRevisionHistoryPage {
  items: ProposalRevisionHistoryItem[];
  nextBeforeRevisionNo?: number;
}

export interface ProposalRevisionBaseSnapshot {
  hash: string;
  content: string;
  byteSize: number;
  schemaVersion: "proposal-base-snapshot/v1";
  createdAt: string;
}

export interface ProposalRevisionLineage {
  sourceRevisionId: string;
  sourceChangeHash: string;
  kind: "DIRECT_EDIT" | "THREE_WAY_MERGE";
  mergeAlgorithm: typeof proposalRevisionMergeAlgorithm;
  mergeAlgorithmVersion: typeof proposalRevisionMergeAlgorithmVersion;
  mergeFingerprint: string;
  createdAt: string;
}

export interface ProposalRevisionHistoryDetailBinding extends ProposalRevisionHistoryBinding {
  revisionId: string;
  revisionNo: number;
  baseHash: string;
  changeHash: string;
}

export interface ProposalRevisionHistoryDetail {
  workspaceId: string;
  proposalId: string;
  currentRevisionId: string;
  current: boolean;
  revision: FilePatchRevision & { targetMode: "REPLACE" };
  baseAvailable: boolean;
  baseSnapshot?: ProposalRevisionBaseSnapshot;
  lineage?: ProposalRevisionLineage;
  approval?: ProposalRevisionHistoryApproval;
  workflow?: ProposalRevisionHistoryWorkflow;
}

const invalidResponse = (message: string): BusinessApiError =>
  new BusinessApiError("INVALID_RESPONSE", message, false);

const record = (value: unknown, label: string): Record<string, unknown> => {
  if (typeof value !== "object" || value === null || Array.isArray(value)) {
    throw invalidResponse(`${label} 响应结构无效`);
  }
  return value as Record<string, unknown>;
};

const exact = (value: Record<string, unknown>, fields: readonly string[], label: string): void => {
  if (!fields.every((field) => Object.hasOwn(value, field)) || !hasOnlyKeys(value, fields)) {
    throw invalidResponse(`${label} 响应字段无效`);
  }
};

const exactWithOptional = (
  value: Record<string, unknown>,
  requiredFields: readonly string[],
  optionalFields: readonly string[],
  label: string,
): void => {
  if (
    !requiredFields.every((field) => Object.hasOwn(value, field))
    || !hasOnlyKeys(value, [...requiredFields, ...optionalFields])
  ) throw invalidResponse(`${label} 响应字段无效`);
};

const stringValue = (value: Record<string, unknown>, field: string, maximumBytes = 4096): string => {
  const item = value[field];
  if (typeof item !== "string" || utf8Encoder.encode(item).byteLength > maximumBytes) {
    throw invalidResponse(`Proposal Revision 响应字段无效：${field}`);
  }
  return item;
};

const nonEmptyStringValue = (value: Record<string, unknown>, field: string, maximumBytes = 4096): string => {
  const item = stringValue(value, field, maximumBytes);
  if (item === "") throw invalidResponse(`Proposal Revision 响应字段无效：${field}`);
  return item;
};

const integerValue = (value: Record<string, unknown>, field: string, minimum = 0): number => {
  const item = value[field];
  if (typeof item !== "number" || !Number.isSafeInteger(item) || item < minimum) {
    throw invalidResponse(`Proposal Revision 响应字段无效：${field}`);
  }
  return item;
};

const uuidValue = (value: Record<string, unknown>, field: string): string => {
  const item = stringValue(value, field, 36);
  if (!canonicalUuidPattern.test(item)) throw invalidResponse(`Proposal Revision UUID 无效：${field}`);
  return item;
};

const hashValue = (value: Record<string, unknown>, field: string): string => {
  const item = stringValue(value, field, 64);
  if (!hashPattern.test(item)) throw invalidResponse(`Proposal Revision Hash 无效：${field}`);
  return item;
};

const booleanValue = (value: Record<string, unknown>, field: string): boolean => {
  const item = value[field];
  if (typeof item !== "boolean") throw invalidResponse(`Proposal Revision 响应字段无效：${field}`);
  return item;
};

const dateTimeValue = (value: Record<string, unknown>, field: string): string => {
  const item = stringValue(value, field, 40);
  if (!rfc3339Pattern.test(item) || Number.isNaN(Date.parse(item))) {
    throw invalidResponse(`Proposal Revision 时间无效：${field}`);
  }
  return item;
};

const bound = (actual: string | number, expected: string | number, field: string): void => {
  if (actual !== expected) throw invalidResponse(`Proposal Revision 绑定不一致：${field}`);
};

const validWorkspacePath = (value: string): boolean => {
  if (value === "" || value !== value.trim() || value.startsWith("/") || value.includes("\\") || value.includes("\u0000")) return false;
  return !value.split("/").some((part) => part === "" || part === "." || part === "..");
};

const sha256Hex = async (value: string): Promise<string> => {
  const digest = await globalThis.crypto.subtle.digest("SHA-256", utf8Encoder.encode(value));
  return [...new Uint8Array(digest)].map((byte) => byte.toString(16).padStart(2, "0")).join("");
};

const decodeTextSnapshot = async (value: unknown, label: string): Promise<ProposalRevisionTextSnapshot> => {
  const snapshot = record(value, label);
  exact(snapshot, ["content", "hash", "byte_size"], label);
  const content = stringValue(snapshot, "content", proposalRevisionMaxBytes);
  const hash = hashValue(snapshot, "hash");
  const byteSize = integerValue(snapshot, "byte_size");
  if (content.includes("\u0000") || byteSize !== utf8Encoder.encode(content).byteLength || await sha256Hex(content) !== hash) {
    throw invalidResponse(`${label} 正文绑定无效`);
  }
  return { content, hash, byteSize };
};

export const decodeProposalRevisionPreview = async (
  value: unknown,
  binding: ProposalRevisionPreviewBinding,
): Promise<ProposalRevisionMergePreview> => {
  const preview = record(value, "Proposal Revision preview");
  exact(preview, [
    "schema_version", "merge_algorithm", "merge_algorithm_version", "merge_fingerprint",
    "proposal_id", "workspace_id", "proposal_version", "source_revision_id", "source_revision_no",
    "source_change_hash", "target_path", "target_mode", "base", "current", "proposed", "candidate",
    "conflict_count", "conflicts",
  ], "Proposal Revision preview");

  const schemaVersion = stringValue(preview, "schema_version");
  const mergeAlgorithm = stringValue(preview, "merge_algorithm");
  const mergeAlgorithmVersion = stringValue(preview, "merge_algorithm_version");
  if (
    schemaVersion !== previewSchemaVersion
    || mergeAlgorithm !== proposalRevisionMergeAlgorithm
    || mergeAlgorithmVersion !== proposalRevisionMergeAlgorithmVersion
    || preview.target_mode !== "REPLACE"
  ) throw invalidResponse("Proposal Revision merge contract 无效");

  const proposalId = uuidValue(preview, "proposal_id");
  const workspaceId = uuidValue(preview, "workspace_id");
  const proposalVersion = integerValue(preview, "proposal_version", 1);
  const sourceRevisionId = uuidValue(preview, "source_revision_id");
  const sourceRevisionNo = integerValue(preview, "source_revision_no", 1);
  const sourceChangeHash = hashValue(preview, "source_change_hash");
  const targetPath = nonEmptyStringValue(preview, "target_path", 4096);
  bound(proposalId, binding.proposalId, "proposal_id");
  bound(workspaceId, binding.workspaceId, "workspace_id");
  bound(proposalVersion, binding.expectedProposalVersion, "proposal_version");
  bound(sourceRevisionId, binding.sourceRevisionId, "source_revision_id");
  bound(sourceRevisionNo, binding.sourceRevisionNo, "source_revision_no");
  bound(sourceChangeHash, binding.sourceChangeHash, "source_change_hash");
  bound(targetPath, binding.targetPath, "target_path");
  if (!validWorkspacePath(targetPath)) throw invalidResponse("Proposal Revision target_path 无效");

  const [base, current, proposed, candidate] = await Promise.all([
    decodeTextSnapshot(preview.base, "Proposal Revision base"),
    decodeTextSnapshot(preview.current, "Proposal Revision current"),
    decodeTextSnapshot(preview.proposed, "Proposal Revision proposed"),
    decodeTextSnapshot(preview.candidate, "Proposal Revision candidate"),
  ]);
  if (proposed.content.trim() === "") throw invalidResponse("Proposal Revision proposed 正文为空");

  const rawConflicts = preview.conflicts;
  if (!Array.isArray(rawConflicts) || rawConflicts.length > proposalRevisionConflictLimit) {
    throw invalidResponse("Proposal Revision conflicts 无效");
  }
  const conflicts = rawConflicts.map((item, index): ProposalRevisionConflict => {
    const conflict = record(item, `Proposal Revision conflict ${String(index + 1)}`);
    exact(conflict, ["id", "ordinal", "base", "current", "proposed"], "Proposal Revision conflict");
    const ordinal = integerValue(conflict, "ordinal", 1);
    if (ordinal !== index + 1) throw invalidResponse("Proposal Revision conflict ordinal 无效");
    return {
      id: hashValue(conflict, "id"),
      ordinal,
      base: stringValue(conflict, "base", proposalRevisionMaxBytes),
      current: stringValue(conflict, "current", proposalRevisionMaxBytes),
      proposed: stringValue(conflict, "proposed", proposalRevisionMaxBytes),
    };
  });
  if (new Set(conflicts.map((conflict) => conflict.id)).size !== conflicts.length) {
    throw invalidResponse("Proposal Revision conflict ID 重复");
  }
  const conflictCount = integerValue(preview, "conflict_count");
  if (conflictCount !== conflicts.length) throw invalidResponse("Proposal Revision conflict 数量不一致");

  return {
    schemaVersion: previewSchemaVersion,
    mergeAlgorithm: proposalRevisionMergeAlgorithm,
    mergeAlgorithmVersion: proposalRevisionMergeAlgorithmVersion,
    mergeFingerprint: hashValue(preview, "merge_fingerprint"),
    proposalId,
    workspaceId,
    proposalVersion,
    sourceRevisionId,
    sourceRevisionNo,
    sourceChangeHash,
    targetPath,
    targetMode: "REPLACE",
    base,
    current,
    proposed,
    candidate,
    conflictCount,
    conflicts,
  };
};

export const previewProposalRevision = async (
  binding: ProposalRevisionPreviewBinding,
  signal?: AbortSignal,
): Promise<ProposalRevisionMergePreview> => {
  const operation = businessRevisionsApi.previewProposalRevisionMergeRaw({
    proposalId: binding.proposalId,
    proposalRevisionMergePreviewRequest: {
      source_revision_id: binding.sourceRevisionId,
      source_change_hash: binding.sourceChangeHash,
      expected_proposal_version: binding.expectedProposalVersion,
    },
  }, generatedRequestInit(signal, { cache: "no-store" }));
  const value = await requestBusinessJSON(generatedRawResponse(operation));
  return decodeProposalRevisionPreview(value, binding);
};

export const appendProposalRevision = async (
  workspaceId: string,
  proposalId: string,
  idempotencyKey: string,
  input: AppendProposalRevisionInput,
  signal?: AbortSignal,
): Promise<AppendProposalRevisionResult> => {
  if (idempotencyKey === "" || idempotencyKey.length > 128 || idempotencyKey.trim() !== idempotencyKey || /[\u0000-\u001f\u007f]/.test(idempotencyKey)) {
    throw new BusinessApiError("INVALID_RESPONSE", "Proposal Revision Idempotency-Key 无效", false);
  }
  const operation = businessRevisionsApi.appendProposalRevisionRaw({
    proposalId,
    idempotencyKey,
    appendProposalRevisionRequest: {
      expected_proposal_version: input.expectedProposalVersion,
      source_revision_id: input.sourceRevisionId,
      source_change_hash: input.sourceChangeHash,
      expected_current_hash: input.expectedCurrentHash,
      merge_fingerprint: input.mergeFingerprint,
      merge_algorithm: input.mergeAlgorithm,
      merge_algorithm_version: input.mergeAlgorithmVersion,
      content: input.content,
      evidence_summary: input.evidenceSummary,
      risk: input.risk,
      rollback_plan: input.rollbackPlan,
      resolved_conflict_ids: [...input.resolvedConflictIds],
    },
  }, generatedRequestInit(signal, { cache: "no-store" }));
  const value = await requestBusinessJSON(generatedRawResponse(operation));
  const response = record(value, "Proposal Revision append");
  const replayed = response.replayed;
  if (typeof replayed !== "boolean") throw invalidResponse("Proposal Revision replayed 无效");
  const proposalPayload = { ...response };
  delete proposalPayload.replayed;
  const proposal = await decodeProposalDetail(proposalPayload, workspaceId, proposalId);
  if (
    proposal.type !== "file_patch"
    || proposal.status !== "ready_for_review"
    || proposal.approval !== undefined
    || proposal.version !== input.expectedProposalVersion + 1
    || proposal.revision.revisionNo !== input.sourceRevisionNo + 1
    || proposal.revision.id === input.sourceRevisionId
    || proposal.revision.baseHash !== input.expectedCurrentHash
    || proposal.revision.content !== input.content
    || proposal.revision.evidenceSummary !== input.evidenceSummary
    || proposal.revision.risk !== input.risk
    || proposal.revision.rollbackPlan !== input.rollbackPlan
  ) throw invalidResponse("Proposal Revision append 结果绑定无效");
  return { proposal, replayed };
};

const decodeHistoryApproval = (
  value: unknown,
  proposalId: string,
  revisionId: string,
  changeHash: string,
): ProposalRevisionHistoryApproval | undefined => {
  if (value === null) return undefined;
  const approval = record(value, "Proposal Revision history approval");
  exactWithOptional(approval, [
    "id", "proposal_id", "revision_id", "change_hash", "decision", "decided_at",
  ], ["approved_git_head"], "Proposal Revision history approval");
  const decodedProposalId = uuidValue(approval, "proposal_id");
  const decodedRevisionId = uuidValue(approval, "revision_id");
  const decodedChangeHash = hashValue(approval, "change_hash");
  bound(decodedProposalId, proposalId, "approval.proposal_id");
  bound(decodedRevisionId, revisionId, "approval.revision_id");
  bound(decodedChangeHash, changeHash, "approval.change_hash");
  const decision = stringValue(approval, "decision");
  if (decision !== "approved" && decision !== "rejected") {
    throw invalidResponse("Proposal Revision history approval decision 无效");
  }
  const approvedGitHead = approval.approved_git_head === undefined
    ? undefined
    : stringValue(approval, "approved_git_head", 64);
  if (approvedGitHead !== undefined && (!gitHashPattern.test(approvedGitHead) || decision !== "approved")) {
    throw invalidResponse("Proposal Revision history approval Git 绑定无效");
  }
  return {
    id: uuidValue(approval, "id"),
    proposalId: decodedProposalId,
    revisionId: decodedRevisionId,
    changeHash: decodedChangeHash,
    decision,
    ...(approvedGitHead === undefined ? {} : { approvedGitHead }),
    decidedAt: dateTimeValue(approval, "decided_at"),
  };
};

const decodeHistoryWorkflow = (
  value: unknown,
  approval: ProposalRevisionHistoryApproval | undefined,
): ProposalRevisionHistoryWorkflow | undefined => {
  if (value === null) return undefined;
  const workflow = record(value, "Proposal Revision history workflow");
  exact(workflow, ["id", "status_url"], "Proposal Revision history workflow");
  const id = uuidValue(workflow, "id");
  const statusUrl = stringValue(workflow, "status_url", 96);
  if (approval?.decision !== "approved" || approval.approvedGitHead === undefined || statusUrl !== `/api/v1/workflows/${id}`) {
    throw invalidResponse("Proposal Revision history workflow 绑定无效");
  }
  return { id, statusUrl };
};

const decodeHistoricalFileRevision = (value: unknown): FilePatchRevision & { targetMode: "REPLACE" } => {
  const revision = record(value, "Proposal Revision history revision");
  exact(revision, [
    "id", "revision_no", "target_mode", "base_hash", "content", "evidence_summary",
    "risk", "rollback_plan", "change_hash", "created_at",
  ], "Proposal Revision history revision");
  if (revision.target_mode !== "REPLACE") throw invalidResponse("Proposal Revision history target_mode 无效");
  const content = stringValue(revision, "content", proposalRevisionMaxBytes);
  if (content.trim() === "" || content.includes("\u0000")) {
    throw invalidResponse("Proposal Revision history 正文无效");
  }
  return {
    id: uuidValue(revision, "id"),
    revisionNo: integerValue(revision, "revision_no", 1),
    targetMode: "REPLACE",
    baseHash: hashValue(revision, "base_hash"),
    content,
    evidenceSummary: nonEmptyStringValue(revision, "evidence_summary", proposalRevisionMetadataMaxBytes),
    risk: nonEmptyStringValue(revision, "risk", proposalRevisionMetadataMaxBytes),
    rollbackPlan: nonEmptyStringValue(revision, "rollback_plan", proposalRevisionMetadataMaxBytes),
    changeHash: hashValue(revision, "change_hash"),
    createdAt: dateTimeValue(revision, "created_at"),
  };
};

export const decodeProposalRevisionHistory = (
  value: unknown,
  binding: ProposalRevisionHistoryBinding,
  limit: number,
): ProposalRevisionHistoryPage => {
  const page = record(value, "Proposal Revision history");
  exactWithOptional(page, ["items"], ["next_before_revision_no"], "Proposal Revision history");
  if (!Array.isArray(page.items) || page.items.length > limit) {
    throw invalidResponse("Proposal Revision history items 无效");
  }
  let previousRevisionNo = Number.MAX_SAFE_INTEGER;
  let currentCount = 0;
  const revisionIds = new Set<string>();
  const items = page.items.map((value, index): ProposalRevisionHistoryItem => {
    const item = record(value, `Proposal Revision history item ${String(index + 1)}`);
    exact(item, [
      "proposal_id", "revision_id", "revision_no", "target_path", "target_mode", "base_hash",
      "change_hash", "base_available", "current", "approval", "workflow", "created_at",
    ], "Proposal Revision history item");
    const proposalId = uuidValue(item, "proposal_id");
    const revisionId = uuidValue(item, "revision_id");
    const revisionNo = integerValue(item, "revision_no", 1);
    const targetPath = nonEmptyStringValue(item, "target_path", 4096);
    const baseHash = hashValue(item, "base_hash");
    const changeHash = hashValue(item, "change_hash");
    bound(proposalId, binding.proposalId, "history.proposal_id");
    bound(targetPath, binding.targetPath, "history.target_path");
    if (!validWorkspacePath(targetPath) || item.target_mode !== "REPLACE" || revisionNo >= previousRevisionNo || revisionIds.has(revisionId)) {
      throw invalidResponse("Proposal Revision history 排序或绑定无效");
    }
    previousRevisionNo = revisionNo;
    revisionIds.add(revisionId);
    const current = booleanValue(item, "current");
    if (current) {
      currentCount += 1;
      if (index !== 0 || currentCount > 1) throw invalidResponse("Proposal Revision history current 绑定无效");
    }
    const approval = decodeHistoryApproval(item.approval, proposalId, revisionId, changeHash);
    const workflow = decodeHistoryWorkflow(item.workflow, approval);
    return {
      proposalId,
      revisionId,
      revisionNo,
      targetPath,
      targetMode: "REPLACE",
      baseHash,
      changeHash,
      baseAvailable: booleanValue(item, "base_available"),
      current,
      ...(approval === undefined ? {} : { approval }),
      ...(workflow === undefined ? {} : { workflow }),
      createdAt: dateTimeValue(item, "created_at"),
    };
  });
  const nextBeforeRevisionNo = page.next_before_revision_no === undefined
    ? undefined
    : integerValue(page, "next_before_revision_no", 2);
  if (nextBeforeRevisionNo !== undefined && (items.length === 0 || nextBeforeRevisionNo !== items.at(-1)?.revisionNo)) {
    throw invalidResponse("Proposal Revision history cursor 无效");
  }
  return { items, ...(nextBeforeRevisionNo === undefined ? {} : { nextBeforeRevisionNo }) };
};

export const listProposalRevisions = async (
  binding: ProposalRevisionHistoryBinding,
  params: { limit?: number; beforeRevisionNo?: number } = {},
  signal?: AbortSignal,
): Promise<ProposalRevisionHistoryPage> => {
  const limit = params.limit ?? 30;
  if (!Number.isSafeInteger(limit) || limit < 1 || limit > 100) {
    throw new BusinessApiError("INVALID_RESPONSE", "Proposal Revision history limit 无效", false);
  }
  if (params.beforeRevisionNo !== undefined && (!Number.isSafeInteger(params.beforeRevisionNo) || params.beforeRevisionNo <= 1)) {
    throw new BusinessApiError("INVALID_RESPONSE", "Proposal Revision history cursor 无效", false);
  }
  const operation = businessRevisionsApi.listProposalRevisionsRaw({
    proposalId: binding.proposalId,
    limit,
    ...(params.beforeRevisionNo === undefined ? {} : { beforeRevisionNo: params.beforeRevisionNo }),
  }, generatedRequestInit(signal, { cache: "no-store" }));
  const value = await requestBusinessJSON(generatedRawResponse(operation));
  return decodeProposalRevisionHistory(value, binding, limit);
};

export const decodeProposalRevisionHistoryDetail = async (
  value: unknown,
  binding: ProposalRevisionHistoryDetailBinding,
): Promise<ProposalRevisionHistoryDetail> => {
  const detail = record(value, "Proposal Revision history detail");
  exact(detail, [
    "workspace_id", "proposal_id", "current_revision_id", "current", "revision", "base_available",
    "base_snapshot", "lineage", "approval", "workflow",
  ], "Proposal Revision history detail");
  const workspaceId = uuidValue(detail, "workspace_id");
  const proposalId = uuidValue(detail, "proposal_id");
  const currentRevisionId = uuidValue(detail, "current_revision_id");
  bound(workspaceId, binding.workspaceId, "detail.workspace_id");
  bound(proposalId, binding.proposalId, "detail.proposal_id");
  const revision = decodeHistoricalFileRevision(detail.revision);
  bound(revision.id, binding.revisionId, "detail.revision.id");
  bound(revision.revisionNo, binding.revisionNo, "detail.revision.revision_no");
  bound(revision.baseHash, binding.baseHash, "detail.revision.base_hash");
  bound(revision.changeHash, binding.changeHash, "detail.revision.change_hash");
  const current = booleanValue(detail, "current");
  if (current !== (currentRevisionId === revision.id)) throw invalidResponse("Proposal Revision history current 绑定无效");

  const baseAvailable = booleanValue(detail, "base_available");
  let baseSnapshot: ProposalRevisionBaseSnapshot | undefined;
  if (detail.base_snapshot !== null) {
    const snapshot = record(detail.base_snapshot, "Proposal Revision base snapshot");
    exact(snapshot, ["hash", "content", "byte_size", "schema_version", "created_at"], "Proposal Revision base snapshot");
    const content = stringValue(snapshot, "content", proposalRevisionMaxBytes);
    const hash = hashValue(snapshot, "hash");
    const byteSize = integerValue(snapshot, "byte_size");
    if (
      !baseAvailable
      || snapshot.schema_version !== "proposal-base-snapshot/v1"
      || content.includes("\u0000")
      || hash !== revision.baseHash
      || byteSize !== utf8Encoder.encode(content).byteLength
      || await sha256Hex(content) !== hash
    ) throw invalidResponse("Proposal Revision base snapshot 绑定无效");
    baseSnapshot = {
      hash,
      content,
      byteSize,
      schemaVersion: "proposal-base-snapshot/v1",
      createdAt: dateTimeValue(snapshot, "created_at"),
    };
  } else if (baseAvailable) {
    throw invalidResponse("Proposal Revision base snapshot 缺失");
  }

  let lineage: ProposalRevisionLineage | undefined;
  if (detail.lineage !== null) {
    const value = record(detail.lineage, "Proposal Revision lineage");
    exact(value, [
      "source_revision_id", "source_change_hash", "kind", "merge_algorithm", "merge_algorithm_version",
      "merge_fingerprint", "created_at",
    ], "Proposal Revision lineage");
    const sourceRevisionId = uuidValue(value, "source_revision_id");
    const kind = stringValue(value, "kind");
    if (
      revision.revisionNo <= 1
      || sourceRevisionId === revision.id
      || (kind !== "DIRECT_EDIT" && kind !== "THREE_WAY_MERGE")
      || value.merge_algorithm !== proposalRevisionMergeAlgorithm
      || value.merge_algorithm_version !== proposalRevisionMergeAlgorithmVersion
    ) throw invalidResponse("Proposal Revision lineage 绑定无效");
    lineage = {
      sourceRevisionId,
      sourceChangeHash: hashValue(value, "source_change_hash"),
      kind,
      mergeAlgorithm: proposalRevisionMergeAlgorithm,
      mergeAlgorithmVersion: proposalRevisionMergeAlgorithmVersion,
      mergeFingerprint: hashValue(value, "merge_fingerprint"),
      createdAt: dateTimeValue(value, "created_at"),
    };
  } else if (revision.revisionNo > 1) {
    throw invalidResponse("Proposal Revision lineage 缺失");
  }
  const approval = decodeHistoryApproval(detail.approval, proposalId, revision.id, revision.changeHash);
  const workflow = decodeHistoryWorkflow(detail.workflow, approval);
  return {
    workspaceId,
    proposalId,
    currentRevisionId,
    current,
    revision,
    baseAvailable,
    ...(baseSnapshot === undefined ? {} : { baseSnapshot }),
    ...(lineage === undefined ? {} : { lineage }),
    ...(approval === undefined ? {} : { approval }),
    ...(workflow === undefined ? {} : { workflow }),
  };
};

export const getProposalRevision = async (
  binding: ProposalRevisionHistoryDetailBinding,
  signal?: AbortSignal,
): Promise<ProposalRevisionHistoryDetail> => {
  const operation = businessRevisionsApi.getProposalRevisionRaw({
    proposalId: binding.proposalId,
    revisionId: binding.revisionId,
  }, generatedRequestInit(signal, { cache: "no-store" }));
  const value = await requestBusinessJSON(generatedRawResponse(operation));
  return decodeProposalRevisionHistoryDetail(value, binding);
};

export const createProposalRevisionIdempotencyKey = (): string =>
  `proposal-revision-${globalThis.crypto.randomUUID()}`;
