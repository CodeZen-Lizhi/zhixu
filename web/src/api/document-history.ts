/** Document 文件历史、比较与恢复的唯一 HTTP 边界。 */

import { authFetch } from "./auth";
import { strictJson } from "./exports";

export type DocumentHistoryEntryKind = "MANAGED" | "EXTERNAL" | "CURRENT_CHANGE";
export type ManagedProposalType = "file_patch" | "restore_document";
export const documentWorktreeRef = "WORKTREE" as const;

interface DocumentHistoryEntryBase {
  kind: DocumentHistoryEntryKind;
  commit: string | null;
  parentCommits: string[];
  authorName: string;
  authorEmail: string;
  committedAt: string | null;
  summary: string;
}

export interface ManagedDocumentHistoryEntry extends DocumentHistoryEntryBase {
  kind: "MANAGED";
  commit: string;
  committedAt: string;
  articleRevisionId: string | null;
  articleRevisionNo: number | null;
  proposalId: string | null;
  proposalRevisionId: string | null;
  approvalId: string | null;
  workflowRunId: string | null;
  writebackId: string | null;
  proposalType: ManagedProposalType | null;
  approvalDecidedAt: string | null;
}

export interface ExternalDocumentHistoryEntry extends DocumentHistoryEntryBase {
  kind: "EXTERNAL";
  commit: string;
  committedAt: string;
}

export interface CurrentDocumentHistoryEntry extends DocumentHistoryEntryBase {
  kind: "CURRENT_CHANGE";
  commit: null;
  committedAt: null;
}

export type DocumentHistoryEntry = ManagedDocumentHistoryEntry | ExternalDocumentHistoryEntry | CurrentDocumentHistoryEntry;

export interface DocumentHistoryPage {
  workspaceId: string;
  documentId: string;
  documentVersion: number;
  path: string;
  branch: string;
  head: string;
  dirty: boolean;
  items: DocumentHistoryEntry[];
  nextCursor?: string;
}

export interface DocumentHistoryCompare {
  workspaceId: string;
  documentId: string;
  path: string;
  head: string;
  left: string;
  right: string;
  leftContent: string;
  rightContent: string;
  patch: string;
  diffHash: string;
}

export interface DocumentRestorePreview {
  workspaceId: string;
  documentId: string;
  path: string;
  targetCommit: string;
  expectedHead: string;
  expectedDocumentVersion: number;
  currentContentHash: string;
  targetContentHash: string;
  currentContent: string;
  targetContent: string;
  patch: string;
  diffHash: string;
  previewHash: string;
  blockedByDirtyWorktree: boolean;
}

export const documentRestoreProposalStatuses = [
  "draft", "validating", "ready_for_review", "approved", "applying", "applied", "verifying", "completed",
  "rejected", "needs_revision", "deferred", "apply_failed", "verify_failed", "rolled_back", "cancelled",
] as const;
export type DocumentRestoreProposalStatus = (typeof documentRestoreProposalStatuses)[number];

export interface DocumentRestoreProposalResult {
  proposalId: string;
  proposalRevisionId: string;
  proposalType: "restore_document";
  status: DocumentRestoreProposalStatus;
  changeHash: string;
  replayed: boolean;
}

export interface ListDocumentHistoryInput {
  workspaceId: string;
  documentId: string;
  cursor?: string;
  limit?: number;
}

export interface CompareDocumentHistoryInput {
  workspaceId: string;
  documentId: string;
  expectedHead: string;
  expectedPath: string;
  left: string;
  right: string;
}

export interface CreateDocumentRestorePreviewInput {
  workspaceId: string;
  documentId: string;
  targetCommit: string;
  expectedHead: string;
  expectedDocumentVersion: number;
}

export interface CreateDocumentRestoreProposalInput {
  workspaceId: string;
  documentId: string;
  targetCommit: string;
  expectedHead: string;
  expectedDocumentVersion: number;
  previewHash: string;
  idempotencyKey: string;
}

type DocumentHistoryApiErrorKind = "INVALID_REQUEST" | "INVALID_RESPONSE" | "HTTP_ERROR" | "NETWORK_ERROR";

export class DocumentHistoryApiError extends Error {
  readonly code: DocumentHistoryApiErrorKind;
  readonly errorCode: string;
  readonly status: number | null;
  readonly retryable: boolean;
  readonly details: Readonly<Record<string, unknown>> | undefined;

  constructor(
    code: DocumentHistoryApiErrorKind,
    errorCode: string,
    message: string,
    options: { status?: number | null; retryable?: boolean; details?: Readonly<Record<string, unknown>>; cause?: unknown } = {},
  ) {
    super(message, options.cause === undefined ? undefined : { cause: options.cause });
    this.name = "DocumentHistoryApiError";
    this.code = code;
    this.errorCode = errorCode;
    this.status = options.status ?? null;
    this.retryable = options.retryable ?? false;
    this.details = options.details;
  }
}

const uuidPattern = /^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/;
const objectIdPattern = /^(?:[0-9a-f]{40}|[0-9a-f]{64})$/;
const hashPattern = /^[0-9a-f]{64}$/;
const errorCodePattern = /^[A-Z][A-Z0-9_]{0,127}$/;
const rfc3339Pattern = /^\d{4}-(?:0[1-9]|1[0-2])-(?:0[1-9]|[12]\d|3[01])T(?:[01]\d|2[0-3]):[0-5]\d:[0-5]\d(?:\.\d{1,9})?(?:Z|[+-](?:[01]\d|2[0-3]):[0-5]\d)$/;
const controlPattern = /[\u0000-\u001f\u007f]/;
const unsafeContentPattern = /[\u0000]/;
const encoder = new TextEncoder();
const maxCursorBytes = 4096;
const maxContentBytes = 10 * 1024 * 1024;
const maxPatchBytes = 2 * 1024 * 1024;

const baseEntryKeys = ["kind", "commit", "parent_commits", "author_name", "author_email", "committed_at", "summary"] as const;
const managedEntryKeys = [
  ...baseEntryKeys,
  "article_revision_id",
  "article_revision_no",
  "proposal_id",
  "proposal_revision_id",
  "approval_id",
  "workflow_run_id",
  "writeback_id",
  "proposal_type",
  "approval_decided_at",
] as const;

const isRecord = (value: unknown): value is Record<string, unknown> =>
  typeof value === "object" && value !== null && !Array.isArray(value);

const invalidRequest = (field: string, cause?: unknown): DocumentHistoryApiError =>
  new DocumentHistoryApiError("INVALID_REQUEST", "INVALID_REQUEST", `文档历史请求字段无效：${field}`, { cause });

const invalidResponse = (field: string, status: number | null = null, cause?: unknown): DocumentHistoryApiError =>
  new DocumentHistoryApiError("INVALID_RESPONSE", "INVALID_RESPONSE", `文档历史响应字段无效：${field}`, { status, cause });

const staleResponse = (message: string): DocumentHistoryApiError =>
  new DocumentHistoryApiError("HTTP_ERROR", "DOCUMENT_HISTORY_CURSOR_STALE", message, { status: 409 });

const exactRecord = (
  value: unknown,
  required: readonly string[],
  optional: readonly string[],
  field: string,
  status: number | null = null,
): Record<string, unknown> => {
  if (!isRecord(value)) throw invalidResponse(field, status);
  const allowed = new Set([...required, ...optional]);
  if (Object.keys(value).some((key) => !allowed.has(key))) throw invalidResponse(field, status);
  if (required.some((key) => !Object.hasOwn(value, key))) throw invalidResponse(field, status);
  return value;
};

const readString = (value: unknown, field: string, maximumBytes: number, allowEmpty = false): string => {
  if (typeof value !== "string" || (!allowEmpty && value.trim() === "") || encoder.encode(value).byteLength > maximumBytes) {
    throw invalidResponse(field);
  }
  return value;
};

const readContent = (value: unknown, field: string, maximumBytes: number): string => {
  const parsed = readString(value, field, maximumBytes, true);
  if (unsafeContentPattern.test(parsed)) throw invalidResponse(field);
  return parsed;
};

const readPlainString = (value: unknown, field: string, maximumBytes: number, allowEmpty = false): string => {
  const parsed = readString(value, field, maximumBytes, allowEmpty);
  if (controlPattern.test(parsed)) throw invalidResponse(field);
  return parsed;
};

const readUuid = (value: unknown, field: string): string => {
  const parsed = readString(value, field, 64);
  if (!uuidPattern.test(parsed)) throw invalidResponse(field);
  return parsed;
};

const readObjectId = (value: unknown, field: string): string => {
  const parsed = readString(value, field, 64);
  if (!objectIdPattern.test(parsed)) throw invalidResponse(field);
  return parsed;
};

const readVersionRef = (value: unknown, field: string): string => {
  if (value === documentWorktreeRef) return value;
  return readObjectId(value, field);
};

const readHash = (value: unknown, field: string): string => {
  const parsed = readString(value, field, 64);
  if (!hashPattern.test(parsed)) throw invalidResponse(field);
  return parsed;
};

const readTimestamp = (value: unknown, field: string): string => {
  const parsed = readString(value, field, 64);
  if (!rfc3339Pattern.test(parsed) || !Number.isFinite(Date.parse(parsed))) throw invalidResponse(field);
  const match = /^(\d{4})-(\d{2})-(\d{2})T/.exec(parsed);
  if (match === null) throw invalidResponse(field);
  const year = Number(match[1]);
  const month = Number(match[2]);
  const day = Number(match[3]);
  if (day > new Date(Date.UTC(year, month, 0)).getUTCDate()) throw invalidResponse(field);
  return parsed;
};

const readPositiveInteger = (value: unknown, field: string): number => {
  if (typeof value !== "number" || !Number.isSafeInteger(value) || value < 1) throw invalidResponse(field);
  return value;
};

const readBoolean = (value: unknown, field: string): boolean => {
  if (typeof value !== "boolean") throw invalidResponse(field);
  return value;
};

const readNullableUuid = (value: unknown, field: string): string | null => value === null ? null : readUuid(value, field);
const readNullableTimestamp = (value: unknown, field: string): string | null => value === null ? null : readTimestamp(value, field);
const readNullableString = (value: unknown, field: string, maximumBytes: number): string | null => value === null ? null : readPlainString(value, field, maximumBytes);

const readNullableManagedProposalType = (value: unknown, field: string): ManagedProposalType | null => {
  const proposalType = readNullableString(value, field, 128);
  if (proposalType === null || proposalType === "file_patch" || proposalType === "restore_document") return proposalType;
  throw invalidResponse(field);
};

const isCanonicalDocumentPath = (value: string): boolean => {
  const segments = value.split("/");
  return !value.startsWith("/") && !value.includes("\\") && !controlPattern.test(value)
    && value.toLowerCase().endsWith(".md")
    && !segments.some((segment) => segment === "" || segment === "." || segment === "..")
    && ![".git", ".knowledge"].includes((segments[0] ?? "").toLowerCase());
};

const readPath = (value: unknown, field: string): string => {
  const parsed = readString(value, field, 4096);
  if (!isCanonicalDocumentPath(parsed)) throw invalidResponse(field);
  return parsed;
};

const readParents = (value: unknown, field: string): string[] => {
  if (!Array.isArray(value) || value.length > 64) throw invalidResponse(field);
  const parents = value.map((item, index) => readObjectId(item, `${field}[${String(index)}]`));
  if (new Set(parents).size !== parents.length) throw invalidResponse(field);
  return parents;
};

const decodeHistoryEntry = (value: unknown, index: number, expectedHead: string): DocumentHistoryEntry => {
  const field = `history.items[${String(index)}]`;
  if (!isRecord(value) || typeof value.kind !== "string") throw invalidResponse(field);
  const keys = value.kind === "MANAGED" ? managedEntryKeys : baseEntryKeys;
  const row = exactRecord(value, keys, [], field);
  const parentCommits = readParents(row.parent_commits, `${field}.parent_commits`);
  const summary = readPlainString(row.summary, `${field}.summary`, 4096);

  if (row.kind === "CURRENT_CHANGE") {
    if (row.commit !== null || row.committed_at !== null || index !== 0 || parentCommits.length !== 1 || parentCommits[0] !== expectedHead) {
      throw invalidResponse(field);
    }
    return {
      kind: "CURRENT_CHANGE",
      commit: null,
      parentCommits,
      authorName: readPlainString(row.author_name, `${field}.author_name`, 512, true),
      authorEmail: readPlainString(row.author_email, `${field}.author_email`, 512, true),
      committedAt: null,
      summary,
    };
  }

  const commit = readObjectId(row.commit, `${field}.commit`);
  const committedAt = readTimestamp(row.committed_at, `${field}.committed_at`);
  const base = {
    commit,
    parentCommits,
    authorName: readPlainString(row.author_name, `${field}.author_name`, 512, true),
    authorEmail: readPlainString(row.author_email, `${field}.author_email`, 512, true),
    committedAt,
    summary,
  };
  if (row.kind === "EXTERNAL") return { kind: "EXTERNAL", ...base };
  if (row.kind !== "MANAGED") throw invalidResponse(`${field}.kind`);

  const articleRevisionId = readNullableUuid(row.article_revision_id, `${field}.article_revision_id`);
  const articleRevisionNo = row.article_revision_no === null ? null : readPositiveInteger(row.article_revision_no, `${field}.article_revision_no`);
  const proposalId = readNullableUuid(row.proposal_id, `${field}.proposal_id`);
  const proposalRevisionId = readNullableUuid(row.proposal_revision_id, `${field}.proposal_revision_id`);
  const approvalId = readNullableUuid(row.approval_id, `${field}.approval_id`);
  const workflowRunId = readNullableUuid(row.workflow_run_id, `${field}.workflow_run_id`);
  const writebackId = readNullableUuid(row.writeback_id, `${field}.writeback_id`);
  const proposalType = readNullableManagedProposalType(row.proposal_type, `${field}.proposal_type`);
  const approvalDecidedAt = readNullableTimestamp(row.approval_decided_at, `${field}.approval_decided_at`);
  if ((articleRevisionId === null) !== (articleRevisionNo === null)) throw invalidResponse(`${field}.article_revision`);
  const proposalBindings = [proposalRevisionId, approvalId, workflowRunId, writebackId, proposalType];
  if ((proposalId === null && proposalBindings.some((binding) => binding !== null))
    || (approvalDecidedAt !== null && approvalId === null)) {
    throw invalidResponse(`${field}.proposal_binding`);
  }
  if (articleRevisionId === null && proposalId === null) throw invalidResponse(`${field}.managed_binding`);
  return {
    kind: "MANAGED",
    ...base,
    articleRevisionId,
    articleRevisionNo,
    proposalId,
    proposalRevisionId,
    approvalId,
    workflowRunId,
    writebackId,
    proposalType,
    approvalDecidedAt,
  };
};

export const decodeDocumentHistoryPage = (
  payload: unknown,
  binding: { workspaceId: string; documentId: string },
): DocumentHistoryPage => {
  const row = exactRecord(payload, [
    "workspace_id", "document_id", "document_version", "path", "branch", "head", "dirty", "items",
  ], ["next_cursor"], "history");
  const workspaceId = readUuid(row.workspace_id, "history.workspace_id");
  const documentId = readUuid(row.document_id, "history.document_id");
  if (workspaceId !== binding.workspaceId || documentId !== binding.documentId) throw invalidResponse("history.binding");
  const head = readObjectId(row.head, "history.head");
  if (!Array.isArray(row.items) || row.items.length > 51) throw invalidResponse("history.items");
  const items = row.items.map((item, index) => decodeHistoryEntry(item, index, head));
  const commits = items.flatMap((item) => item.commit === null ? [] : [item.commit]);
  if (new Set(commits).size !== commits.length || items.filter((item) => item.kind === "CURRENT_CHANGE").length > 1) {
    throw invalidResponse("history.items");
  }
  const dirty = readBoolean(row.dirty, "history.dirty");
  if (!dirty && items.some((item) => item.kind === "CURRENT_CHANGE")) throw invalidResponse("history.dirty");
  const nextCursor = row.next_cursor === undefined ? undefined : readPlainString(row.next_cursor, "history.next_cursor", maxCursorBytes);
  return {
    workspaceId,
    documentId,
    documentVersion: readPositiveInteger(row.document_version, "history.document_version"),
    path: readPath(row.path, "history.path"),
    branch: readPlainString(row.branch, "history.branch", 512),
    head,
    dirty,
    items,
    ...(nextCursor === undefined ? {} : { nextCursor }),
  };
};

export const decodeDocumentHistoryCompare = (
  payload: unknown,
  binding: CompareDocumentHistoryInput,
): DocumentHistoryCompare => {
  const row = exactRecord(payload, [
    "workspace_id", "document_id", "path", "head", "left", "right", "left_content", "right_content", "patch", "diff_hash",
  ], [], "compare");
  const workspaceId = readUuid(row.workspace_id, "compare.workspace_id");
  const documentId = readUuid(row.document_id, "compare.document_id");
  const left = readVersionRef(row.left, "compare.left");
  const right = readVersionRef(row.right, "compare.right");
  const head = readObjectId(row.head, "compare.head");
  if (workspaceId !== binding.workspaceId || documentId !== binding.documentId || left !== binding.left || right !== binding.right || left === right) {
    throw invalidResponse("compare.binding");
  }
  if (head !== binding.expectedHead) throw staleResponse("比较期间 HEAD 已变化，请重新读取文档历史。");
  const path = readPath(row.path, "compare.path");
  if (path !== binding.expectedPath) throw staleResponse("比较期间 Document path 已变化，请重新读取文档历史。");
  return {
    workspaceId,
    documentId,
    path,
    head,
    left,
    right,
    leftContent: readContent(row.left_content, "compare.left_content", maxContentBytes),
    rightContent: readContent(row.right_content, "compare.right_content", maxContentBytes),
    patch: readContent(row.patch, "compare.patch", maxPatchBytes),
    diffHash: readHash(row.diff_hash, "compare.diff_hash"),
  };
};

export const decodeDocumentRestorePreview = (
  payload: unknown,
  binding: CreateDocumentRestorePreviewInput,
): DocumentRestorePreview => {
  const row = exactRecord(payload, [
    "workspace_id", "document_id", "path", "target_commit", "expected_head", "expected_document_version",
    "current_content_hash", "target_content_hash", "current_content", "target_content", "patch", "diff_hash", "preview_hash",
    "blocked_by_dirty_worktree",
  ], [], "restore_preview");
  const workspaceId = readUuid(row.workspace_id, "restore_preview.workspace_id");
  const documentId = readUuid(row.document_id, "restore_preview.document_id");
  const targetCommit = readObjectId(row.target_commit, "restore_preview.target_commit");
  const expectedHead = readObjectId(row.expected_head, "restore_preview.expected_head");
  const expectedDocumentVersion = readPositiveInteger(row.expected_document_version, "restore_preview.expected_document_version");
  if (workspaceId !== binding.workspaceId || documentId !== binding.documentId || targetCommit !== binding.targetCommit) {
    throw invalidResponse("restore_preview.binding");
  }
  if (expectedHead !== binding.expectedHead || expectedDocumentVersion !== binding.expectedDocumentVersion) {
    throw staleResponse("恢复预览期间 HEAD 或 Document version 已变化，请重新读取历史。");
  }
  return {
    workspaceId,
    documentId,
    path: readPath(row.path, "restore_preview.path"),
    targetCommit,
    expectedHead,
    expectedDocumentVersion,
    currentContentHash: readHash(row.current_content_hash, "restore_preview.current_content_hash"),
    targetContentHash: readHash(row.target_content_hash, "restore_preview.target_content_hash"),
    currentContent: readContent(row.current_content, "restore_preview.current_content", maxContentBytes),
    targetContent: readContent(row.target_content, "restore_preview.target_content", maxContentBytes),
    patch: readContent(row.patch, "restore_preview.patch", maxPatchBytes),
    diffHash: readHash(row.diff_hash, "restore_preview.diff_hash"),
    previewHash: readHash(row.preview_hash, "restore_preview.preview_hash"),
    blockedByDirtyWorktree: readBoolean(row.blocked_by_dirty_worktree, "restore_preview.blocked_by_dirty_worktree"),
  };
};

export const decodeDocumentRestoreProposal = (payload: unknown): DocumentRestoreProposalResult => {
  const row = exactRecord(payload, [
    "proposal_id", "proposal_revision_id", "proposal_type", "status", "change_hash", "replayed",
  ], [], "restore_proposal");
  const status = readPlainString(row.status, "restore_proposal.status", 64);
  if (row.proposal_type !== "restore_document" || !documentRestoreProposalStatuses.includes(status as DocumentRestoreProposalStatus)) {
    throw invalidResponse("restore_proposal.state");
  }
  return {
    proposalId: readUuid(row.proposal_id, "restore_proposal.proposal_id"),
    proposalRevisionId: readUuid(row.proposal_revision_id, "restore_proposal.proposal_revision_id"),
    proposalType: "restore_document",
    status: status as DocumentRestoreProposalStatus,
    changeHash: readHash(row.change_hash, "restore_proposal.change_hash"),
    replayed: readBoolean(row.replayed, "restore_proposal.replayed"),
  };
};

const decodeProblem = (payload: unknown, status: number): DocumentHistoryApiError => {
  try {
    const row = exactRecord(payload, ["error_code", "message", "retryable"], ["workflow_run_id", "details"], "Problem", status);
    const errorCode = readString(row.error_code, "Problem.error_code", 128);
    if (!errorCodePattern.test(errorCode)) throw invalidResponse("Problem.error_code", status);
    const details = row.details === undefined ? undefined : isRecord(row.details) ? row.details : (() => { throw invalidResponse("Problem.details", status); })();
    if (row.workflow_run_id !== undefined) readUuid(row.workflow_run_id, "Problem.workflow_run_id");
    return new DocumentHistoryApiError("HTTP_ERROR", errorCode, readString(row.message, "Problem.message", 4096), {
      status,
      retryable: readBoolean(row.retryable, "Problem.retryable"),
      ...(details === undefined ? {} : { details }),
    });
  } catch (error: unknown) {
    return new DocumentHistoryApiError("INVALID_RESPONSE", "INVALID_RESPONSE", "文档历史 API 返回了无效 Problem。", { status, cause: error });
  }
};

const isAbortError = (value: unknown): boolean =>
  value instanceof DOMException ? value.name === "AbortError" : isRecord(value) && value.name === "AbortError";

const request = async (path: string, init: RequestInit, successStatuses: readonly number[]): Promise<unknown> => {
  const headers = new Headers(init.headers);
  headers.set("Accept", "application/json");
  if (init.body !== undefined) headers.set("Content-Type", "application/json");
  let response: Response;
  try {
    response = await authFetch(path, { ...init, headers });
  } catch (error: unknown) {
    if (isAbortError(error)) throw error;
    throw new DocumentHistoryApiError("NETWORK_ERROR", "NETWORK_ERROR", "无法连接文档历史 API。", { retryable: true, cause: error });
  }
  const contentType = response.headers.get("Content-Type")?.split(";", 1)[0]?.trim().toLowerCase();
  if (contentType !== "application/json") throw invalidResponse("Content-Type", response.status);
  let payload: unknown;
  try {
    payload = strictJson(await response.text());
  } catch (error: unknown) {
    if (isAbortError(error)) throw error;
    throw invalidResponse("JSON", response.status, error);
  }
  if (!response.ok) throw decodeProblem(payload, response.status);
  if (!successStatuses.includes(response.status)) throw invalidResponse("status", response.status);
  return payload;
};

const requireUuid = (value: unknown, field: string): string => {
  if (typeof value !== "string" || !uuidPattern.test(value)) throw invalidRequest(field);
  return value;
};

const requireObjectId = (value: unknown, field: string): string => {
  if (typeof value !== "string" || !objectIdPattern.test(value)) throw invalidRequest(field);
  return value;
};

const requirePath = (value: unknown, field: string): string => {
  if (typeof value !== "string" || encoder.encode(value).byteLength > 4096 || !isCanonicalDocumentPath(value)) {
    throw invalidRequest(field);
  }
  return value;
};

const requireVersionRef = (value: unknown, field: string): string => value === documentWorktreeRef ? value : requireObjectId(value, field);
const requireHash = (value: unknown, field: string): string => {
  if (typeof value !== "string" || !hashPattern.test(value)) throw invalidRequest(field);
  return value;
};

const requirePositiveInteger = (value: unknown, field: string, maximum?: number): number => {
  if (typeof value !== "number" || !Number.isSafeInteger(value) || value < 1 || (maximum !== undefined && value > maximum)) {
    throw invalidRequest(field);
  }
  return value;
};

const requireCursor = (value: unknown): string => {
  if (typeof value !== "string" || value === "" || value !== value.trim() || encoder.encode(value).byteLength > maxCursorBytes || controlPattern.test(value)) {
    throw invalidRequest("cursor");
  }
  return value;
};

const requireIdempotencyKey = (value: unknown): string => {
  if (typeof value !== "string" || value === "" || value !== value.trim() || encoder.encode(value).byteLength > 128 || controlPattern.test(value)) {
    throw invalidRequest("idempotencyKey");
  }
  return value;
};

const withSignal = (signal?: AbortSignal): RequestInit => signal === undefined ? {} : { signal };

export const isDocumentHistoryId = (value: string): boolean => uuidPattern.test(value);

export const listDocumentHistory = async (
  input: ListDocumentHistoryInput,
  signal?: AbortSignal,
): Promise<DocumentHistoryPage> => {
  const workspaceId = requireUuid(input.workspaceId, "workspaceId");
  const documentId = requireUuid(input.documentId, "documentId");
  const limit = requirePositiveInteger(input.limit ?? 30, "limit", 50);
  const query = new URLSearchParams({ limit: String(limit) });
  if (input.cursor !== undefined) query.set("cursor", requireCursor(input.cursor));
  const payload = await request(
    `/api/v1/workspaces/${encodeURIComponent(workspaceId)}/documents/${encodeURIComponent(documentId)}/history?${query.toString()}`,
    withSignal(signal),
    [200],
  );
  return decodeDocumentHistoryPage(payload, { workspaceId, documentId });
};

export const compareDocumentHistory = async (
  input: CompareDocumentHistoryInput,
  signal?: AbortSignal,
): Promise<DocumentHistoryCompare> => {
  const workspaceId = requireUuid(input.workspaceId, "workspaceId");
  const documentId = requireUuid(input.documentId, "documentId");
  const expectedHead = requireObjectId(input.expectedHead, "expectedHead");
  const expectedPath = requirePath(input.expectedPath, "expectedPath");
  const left = requireVersionRef(input.left, "left");
  const right = requireVersionRef(input.right, "right");
  if (left === right) throw invalidRequest("compare");
  const query = new URLSearchParams({ left, right });
  const binding = { workspaceId, documentId, expectedHead, expectedPath, left, right };
  const payload = await request(
    `/api/v1/workspaces/${encodeURIComponent(workspaceId)}/documents/${encodeURIComponent(documentId)}/history/compare?${query.toString()}`,
    withSignal(signal),
    [200],
  );
  return decodeDocumentHistoryCompare(payload, binding);
};

export const createDocumentRestorePreview = async (
  input: CreateDocumentRestorePreviewInput,
  signal?: AbortSignal,
): Promise<DocumentRestorePreview> => {
  const binding = {
    workspaceId: requireUuid(input.workspaceId, "workspaceId"),
    documentId: requireUuid(input.documentId, "documentId"),
    targetCommit: requireObjectId(input.targetCommit, "targetCommit"),
    expectedHead: requireObjectId(input.expectedHead, "expectedHead"),
    expectedDocumentVersion: requirePositiveInteger(input.expectedDocumentVersion, "expectedDocumentVersion"),
  };
  const payload = await request(
    `/api/v1/workspaces/${encodeURIComponent(binding.workspaceId)}/documents/${encodeURIComponent(binding.documentId)}/restore-previews`,
    {
      method: "POST",
      body: JSON.stringify({ target_commit: binding.targetCommit, expected_document_version: binding.expectedDocumentVersion }),
      ...withSignal(signal),
    },
    [200],
  );
  return decodeDocumentRestorePreview(payload, binding);
};

export const createDocumentRestoreProposal = async (
  input: CreateDocumentRestoreProposalInput,
  signal?: AbortSignal,
): Promise<DocumentRestoreProposalResult> => {
  const workspaceId = requireUuid(input.workspaceId, "workspaceId");
  const documentId = requireUuid(input.documentId, "documentId");
  const payload = await request(
    `/api/v1/workspaces/${encodeURIComponent(workspaceId)}/documents/${encodeURIComponent(documentId)}/restore-proposals`,
    {
      method: "POST",
      headers: { "Idempotency-Key": requireIdempotencyKey(input.idempotencyKey) },
      body: JSON.stringify({
        target_commit: requireObjectId(input.targetCommit, "targetCommit"),
        expected_head: requireObjectId(input.expectedHead, "expectedHead"),
        expected_document_version: requirePositiveInteger(input.expectedDocumentVersion, "expectedDocumentVersion"),
        preview_hash: requireHash(input.previewHash, "previewHash"),
      }),
      ...withSignal(signal),
    },
    [200, 201],
  );
  return decodeDocumentRestoreProposal(payload);
};
