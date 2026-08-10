/** Document Draft 创作的唯一网络边界。 */

import { authFetch } from "./auth";
import { decodeProblem } from "./conversation";
import { strictJson } from "./exports";
import { canonicalUuidPattern as uuidPattern, hasExactKeys, isAbortError, isRecord } from "../shared/codec";

export type WorkingDraftStatus = "EDITING" | "ARCHIVED";
export type DocumentLifecycleStatus = "DRAFT" | "PUBLISHED" | "ARCHIVED" | "DELETED";
export type ArticleRevisionStatus = "DRAFT" | "REVIEW" | "APPROVED" | "PUBLISHED" | "SUPERSEDED" | "ARCHIVED";
export type ArticleOptimizationMode = "NONE" | "CLARITY" | "STRUCTURE" | "COMPLETENESS";
export type ArticleRevisionCreator = "USER" | "AGENT" | "SYSTEM";
export type PublicationStatus = "PENDING" | "PUBLISHED" | "RECOVERY_REQUIRED" | "CLOSED";

export interface WorkingDraft {
  id: string;
  workspaceId: string;
  documentId: string | null;
  title: string;
  targetPath: string;
  body: string;
  status: WorkingDraftStatus;
  version: number;
  createdAt: string;
  updatedAt: string;
}

export interface WorkingDraftSummary {
  id: string;
  workspaceId: string;
  documentId: string | null;
  title: string;
  targetPath: string;
  status: WorkingDraftStatus;
  version: number;
  updatedAt: string;
}

export interface DocumentDraft {
  id: string;
  workspaceId: string;
  title: string;
  canonicalPath: string;
  lifecycleStatus: DocumentLifecycleStatus;
  currentPublishedRevisionId: string | null;
  version: number;
  createdAt: string;
  updatedAt: string;
}

export interface ArticleRevisionDraft {
  id: string;
  workspaceId: string;
  documentId: string;
  sourceVersionId: string | null;
  parentRevisionId: string | null;
  revisionNo: number;
  content: string;
  contentHash: string;
  status: ArticleRevisionStatus;
  optimizationMode: ArticleOptimizationMode;
  gitCommit: string | null;
  createdByType: ArticleRevisionCreator;
  createdAt: string;
}

export interface PublicationBinding {
  id: string;
  workspaceId: string;
  documentId: string;
  articleRevisionId: string;
  proposalId: string;
  proposalRevisionId: string;
  targetPath: string;
  contentHash: string;
  status: PublicationStatus;
  gitCommit: string | null;
  errorCode: string | null;
  version: number;
  createdAt: string;
  updatedAt: string;
  publishedAt: string | null;
  proposalHref: string;
}

export interface DocumentDraftDetail {
  document: DocumentDraft;
  currentRevision: ArticleRevisionDraft | null;
  publication: PublicationBinding | null;
}

export interface OrganizingAvailability {
  available: boolean;
  reason: string | null;
  href: string | null;
}

export interface AuthoringOverview {
  workspaceId: string;
  organizing: OrganizingAvailability;
  recentDrafts: WorkingDraftSummary[];
  pendingPublications: PublicationBinding[];
  completedDocuments: DocumentDraft[];
}

export interface CreateWorkingDraftInput {
  workspaceId: string;
  idempotencyKey: string;
  signal?: AbortSignal;
}

export interface UpdateWorkingDraftInput {
  workspaceId: string;
  draftId: string;
  expectedVersion: number;
  title: string;
  targetPath: string;
  body: string;
  idempotencyKey: string;
  signal?: AbortSignal;
}

export interface FreezeWorkingDraftInput {
  workspaceId: string;
  draftId: string;
  expectedVersion: number;
  idempotencyKey: string;
  signal?: AbortSignal;
}

export interface PublishRevisionInput {
  workspaceId: string;
  documentId: string;
  revisionId: string;
  idempotencyKey: string;
  signal?: AbortSignal;
}

export interface WorkingDraftCommandResult {
  workingDraft: WorkingDraft;
  replayed: boolean;
}

export interface FreezeWorkingDraftResult extends WorkingDraftCommandResult {
  document: DocumentDraft;
  articleRevision: ArticleRevisionDraft;
}

export interface PublishRevisionResult {
  publication: PublicationBinding;
  replayed: boolean;
}

type AuthoringApiErrorKind = "INVALID_REQUEST" | "INVALID_RESPONSE" | "HTTP_ERROR" | "NETWORK_ERROR";

export class AuthoringApiError extends Error {
  readonly code: AuthoringApiErrorKind;
  readonly errorCode: string;
  readonly status: number | null;
  readonly retryable: boolean;
  readonly workflowRunId: string | undefined;
  readonly details: Readonly<Record<string, unknown>> | undefined;

  constructor(
    code: AuthoringApiErrorKind,
    errorCode: string,
    message: string,
    status: number | null = null,
    retryable = false,
    options: { cause?: unknown; workflowRunId?: string; details?: Readonly<Record<string, unknown>> } = {},
  ) {
    super(message, options.cause === undefined ? undefined : { cause: options.cause });
    this.name = "AuthoringApiError";
    this.code = code;
    this.errorCode = errorCode;
    this.status = status;
    this.retryable = retryable;
    this.workflowRunId = options.workflowRunId;
    this.details = options.details;
  }
}

const hashPattern = /^[0-9a-f]{64}$/;
const gitCommitPattern = /^[0-9a-f]{40,64}$/;
const timestampPattern = /^\d{4}-(?:0[1-9]|1[0-2])-(?:0[1-9]|[12]\d|3[01])T(?:[01]\d|2[0-3]):[0-5]\d:[0-5]\d(?:\.\d{1,9})?(?:Z|[+-](?:[01]\d|2[0-3]):[0-5]\d)$/;
const calendarDatePattern = /^(\d{4})-(\d{2})-(\d{2})T/;
const errorCodePattern = /^[A-Z][A-Z0-9_]{0,127}$/;
const controlPattern = /[\u0000-\u001f\u007f]/;
const lineBreakPattern = /[\r\n]/;
const encoder = new TextEncoder();
const maxBodyBytes = 10 * 1024 * 1024;

const invalidRequest = (field: string, cause?: unknown): AuthoringApiError =>
  new AuthoringApiError("INVALID_REQUEST", "INVALID_REQUEST", `创作请求字段无效：${field}`, null, false, { cause });

const invalidResponse = (field: string, status: number | null = null, cause?: unknown): AuthoringApiError =>
  new AuthoringApiError("INVALID_RESPONSE", "INVALID_RESPONSE", `创作响应字段无效：${field}`, status, false, { cause });

const exact = (
  value: unknown,
  required: readonly string[],
  optional: readonly string[],
  field: string,
  status: number | null = null,
): Record<string, unknown> => {
  if (!isRecord(value)) throw invalidResponse(field, status);
  if (!hasExactKeys(value, required, optional)) throw invalidResponse(field, status);
  return value;
};

const text = (value: unknown, field: string, maxBytes: number, allowEmpty = false): string => {
  if (typeof value !== "string" || encoder.encode(value).byteLength > maxBytes || (!allowEmpty && value.trim() === "")) {
    throw invalidResponse(field);
  }
  return value;
};

const workingText = (value: unknown, field: string, maxBytes: number, allowEmpty = false): string => {
  const parsed = text(value, field, maxBytes, allowEmpty);
  if (lineBreakPattern.test(parsed) || parsed.includes("\u0000")) throw invalidResponse(field);
  return parsed;
};

const bodyText = (value: unknown, field: string, allowEmpty = false): string => {
  const parsed = text(value, field, maxBodyBytes, allowEmpty);
  if (parsed.includes("\u0000")) throw invalidResponse(field);
  return parsed;
};

const uuid = (value: unknown, field: string): string => {
  if (typeof value !== "string" || !uuidPattern.test(value)) throw invalidResponse(field);
  return value;
};

const nullableUuid = (value: unknown, field: string): string | null => value === null ? null : uuid(value, field);

const positiveInteger = (value: unknown, field: string): number => {
  if (!Number.isSafeInteger(value) || (value as number) < 1) throw invalidResponse(field);
  return value as number;
};

const boolean = (value: unknown, field: string): boolean => {
  if (typeof value !== "boolean") throw invalidResponse(field);
  return value;
};

const timestamp = (value: unknown, field: string): string => {
  if (typeof value !== "string" || !timestampPattern.test(value) || !Number.isFinite(Date.parse(value))) {
    throw invalidResponse(field);
  }
  const calendar = calendarDatePattern.exec(value);
  if (calendar === null) throw invalidResponse(field);
  const year = Number(calendar[1]);
  const month = Number(calendar[2]);
  const day = Number(calendar[3]);
  const candidate = new Date(0);
  candidate.setUTCHours(0, 0, 0, 0);
  candidate.setUTCFullYear(year, month - 1, day);
  if (candidate.getUTCFullYear() !== year || candidate.getUTCMonth() !== month - 1 || candidate.getUTCDate() !== day) {
    throw invalidResponse(field);
  }
  return value;
};

const nullableTimestamp = (value: unknown, field: string): string | null => value === null ? null : timestamp(value, field);

const hash = (value: unknown, field: string): string => {
  if (typeof value !== "string" || !hashPattern.test(value)) throw invalidResponse(field);
  return value;
};

const nullableGitCommit = (value: unknown, field: string): string | null => {
  if (value === null) return null;
  if (typeof value !== "string" || !gitCommitPattern.test(value)) throw invalidResponse(field);
  return value;
};

const enumValue = <T extends string>(value: unknown, values: readonly T[], field: string): T => {
  if (typeof value !== "string" || !values.includes(value as T)) throw invalidResponse(field);
  return value as T;
};

const readArray = <T>(value: unknown, field: string, decode: (item: unknown, index: number) => T): T[] => {
  if (!Array.isArray(value) || value.length > 100) throw invalidResponse(field);
  return value.map(decode);
};

const assertUniqueIds = (items: readonly { id: string }[], field: string): void => {
  if (new Set(items.map((item) => item.id)).size !== items.length) throw invalidResponse(field);
};

export const decodeWorkingDraft = (value: unknown): WorkingDraft => {
  const row = exact(value, ["id", "workspace_id", "document_id", "title", "target_path", "body", "status", "version", "created_at", "updated_at"], [], "working_draft");
  const createdAt = timestamp(row.created_at, "working_draft.created_at");
  const updatedAt = timestamp(row.updated_at, "working_draft.updated_at");
  if (Date.parse(updatedAt) < Date.parse(createdAt)) throw invalidResponse("working_draft.updated_at");
  return {
    id: uuid(row.id, "working_draft.id"),
    workspaceId: uuid(row.workspace_id, "working_draft.workspace_id"),
    documentId: nullableUuid(row.document_id, "working_draft.document_id"),
    title: workingText(row.title, "working_draft.title", 512, true),
    targetPath: workingText(row.target_path, "working_draft.target_path", 4096, true),
    body: bodyText(row.body, "working_draft.body", true),
    status: enumValue(row.status, ["EDITING", "ARCHIVED"] as const, "working_draft.status"),
    version: positiveInteger(row.version, "working_draft.version"),
    createdAt,
    updatedAt,
  };
};

const decodeWorkingDraftSummary = (value: unknown): WorkingDraftSummary => {
  const row = exact(value, ["id", "workspace_id", "document_id", "title", "target_path", "status", "version", "updated_at"], [], "working_draft_summary");
  return {
    id: uuid(row.id, "working_draft_summary.id"),
    workspaceId: uuid(row.workspace_id, "working_draft_summary.workspace_id"),
    documentId: nullableUuid(row.document_id, "working_draft_summary.document_id"),
    title: workingText(row.title, "working_draft_summary.title", 512, true),
    targetPath: workingText(row.target_path, "working_draft_summary.target_path", 4096, true),
    status: enumValue(row.status, ["EDITING", "ARCHIVED"] as const, "working_draft_summary.status"),
    version: positiveInteger(row.version, "working_draft_summary.version"),
    updatedAt: timestamp(row.updated_at, "working_draft_summary.updated_at"),
  };
};

export const decodeDocumentDraft = (value: unknown): DocumentDraft => {
  const row = exact(value, ["id", "workspace_id", "canonical_path", "title", "lifecycle_status", "current_published_revision_id", "version", "created_at", "updated_at"], [], "document");
  const createdAt = timestamp(row.created_at, "document.created_at");
  const updatedAt = timestamp(row.updated_at, "document.updated_at");
  const lifecycleStatus = enumValue(row.lifecycle_status, ["DRAFT", "PUBLISHED", "ARCHIVED", "DELETED"] as const, "document.lifecycle_status");
  const currentPublishedRevisionId = nullableUuid(row.current_published_revision_id, "document.current_published_revision_id");
  if (Date.parse(updatedAt) < Date.parse(createdAt)) throw invalidResponse("document.updated_at");
  if ((lifecycleStatus === "DRAFT" && currentPublishedRevisionId !== null)
    || (lifecycleStatus === "PUBLISHED" && currentPublishedRevisionId === null)) {
    throw invalidResponse("document.current_published_revision_id");
  }
  return {
    id: uuid(row.id, "document.id"),
    workspaceId: uuid(row.workspace_id, "document.workspace_id"),
    title: workingText(row.title, "document.title", 512),
    canonicalPath: workingText(row.canonical_path, "document.canonical_path", 4096),
    lifecycleStatus,
    currentPublishedRevisionId,
    version: positiveInteger(row.version, "document.version"),
    createdAt,
    updatedAt,
  };
};

export const decodeArticleRevisionDraft = (value: unknown): ArticleRevisionDraft => {
  const row = exact(value, ["id", "workspace_id", "document_id", "source_version_id", "parent_revision_id", "revision_no", "content", "content_hash", "status", "optimization_mode", "git_commit", "created_by_type", "created_at"], [], "article_revision");
  const status = enumValue(row.status, ["DRAFT", "REVIEW", "APPROVED", "PUBLISHED", "SUPERSEDED", "ARCHIVED"] as const, "article_revision.status");
  const gitCommit = nullableGitCommit(row.git_commit, "article_revision.git_commit");
  if ((status === "PUBLISHED" && gitCommit === null) || (["DRAFT", "REVIEW", "APPROVED"] as const).includes(status as "DRAFT" | "REVIEW" | "APPROVED") && gitCommit !== null) {
    throw invalidResponse("article_revision.git_commit");
  }
  return {
    id: uuid(row.id, "article_revision.id"),
    workspaceId: uuid(row.workspace_id, "article_revision.workspace_id"),
    documentId: uuid(row.document_id, "article_revision.document_id"),
    sourceVersionId: nullableUuid(row.source_version_id, "article_revision.source_version_id"),
    parentRevisionId: nullableUuid(row.parent_revision_id, "article_revision.parent_revision_id"),
    revisionNo: positiveInteger(row.revision_no, "article_revision.revision_no"),
    content: bodyText(row.content, "article_revision.content"),
    contentHash: hash(row.content_hash, "article_revision.content_hash"),
    status,
    optimizationMode: enumValue(row.optimization_mode, ["NONE", "CLARITY", "STRUCTURE", "COMPLETENESS"] as const, "article_revision.optimization_mode"),
    gitCommit,
    createdByType: enumValue(row.created_by_type, ["USER", "AGENT", "SYSTEM"] as const, "article_revision.created_by_type"),
    createdAt: timestamp(row.created_at, "article_revision.created_at"),
  };
};

export const decodePublicationBinding = (value: unknown): PublicationBinding => {
  const row = exact(value, ["id", "workspace_id", "document_id", "article_revision_id", "proposal_id", "proposal_revision_id", "target_path", "content_hash", "status", "git_commit", "error_code", "version", "created_at", "updated_at", "published_at", "proposal_href"], [], "publication");
  const status = enumValue(row.status, ["PENDING", "PUBLISHED", "RECOVERY_REQUIRED", "CLOSED"] as const, "publication.status");
  const gitCommit = nullableGitCommit(row.git_commit, "publication.git_commit");
  const errorCode = row.error_code === null ? null : text(row.error_code, "publication.error_code", 128);
  const publishedAt = nullableTimestamp(row.published_at, "publication.published_at");
  const proposalId = uuid(row.proposal_id, "publication.proposal_id");
  const proposalHref = text(row.proposal_href, "publication.proposal_href", 2048);
  const createdAt = timestamp(row.created_at, "publication.created_at");
  const updatedAt = timestamp(row.updated_at, "publication.updated_at");
  if (proposalHref !== `/proposals/${proposalId}`) throw invalidResponse("publication.proposal_href");
  if (Date.parse(updatedAt) < Date.parse(createdAt) || (publishedAt !== null && Date.parse(publishedAt) < Date.parse(createdAt))) {
    throw invalidResponse("publication.updated_at");
  }
  if (status === "PENDING" && (gitCommit !== null || errorCode !== null || publishedAt !== null)) throw invalidResponse("publication.state");
  if (status === "PUBLISHED" && (gitCommit === null || errorCode !== null || publishedAt === null)) throw invalidResponse("publication.state");
  if ((status === "RECOVERY_REQUIRED" || status === "CLOSED") && (gitCommit !== null || errorCode === null || publishedAt !== null)) throw invalidResponse("publication.state");
  return {
    id: uuid(row.id, "publication.id"),
    workspaceId: uuid(row.workspace_id, "publication.workspace_id"),
    documentId: uuid(row.document_id, "publication.document_id"),
    articleRevisionId: uuid(row.article_revision_id, "publication.article_revision_id"),
    proposalId,
    proposalRevisionId: uuid(row.proposal_revision_id, "publication.proposal_revision_id"),
    targetPath: workingText(row.target_path, "publication.target_path", 4096),
    contentHash: hash(row.content_hash, "publication.content_hash"),
    status,
    gitCommit,
    errorCode,
    version: positiveInteger(row.version, "publication.version"),
    createdAt,
    updatedAt,
    publishedAt,
    proposalHref,
  };
};

const assertWorkspace = <T extends { workspaceId: string }>(value: T, workspaceId: string, field: string): T => {
  if (value.workspaceId !== workspaceId) throw invalidResponse(field);
  return value;
};

const requireUuid = (value: string, field: string): string => {
  if (!uuidPattern.test(value)) throw invalidRequest(field);
  return value;
};

const requireKey = (value: string): string => {
  if (value.trim() === "" || value !== value.trim() || encoder.encode(value).byteLength > 128 || controlPattern.test(value)) {
    throw invalidRequest("idempotencyKey");
  }
  return value;
};

const requireVersion = (value: number): number => {
  if (!Number.isSafeInteger(value) || value < 1) throw invalidRequest("expectedVersion");
  return value;
};

const requireDraftText = (value: string, field: string, maxBytes: number, lineBreaks = true): string => {
  if (encoder.encode(value).byteLength > maxBytes || (!lineBreaks && lineBreakPattern.test(value))) throw invalidRequest(field);
  return value;
};

const withSignal = (signal: AbortSignal | undefined): RequestInit => signal === undefined ? {} : { signal };
const decodeHttpProblem = (value: unknown, status: number): AuthoringApiError => {
  try {
    const problem = decodeProblem(value);
    if (!errorCodePattern.test(problem.errorCode)) throw invalidResponse("problem.error_code", status);
    return new AuthoringApiError("HTTP_ERROR", problem.errorCode, problem.message, status, problem.retryable, {
      ...(problem.workflowRunId === undefined ? {} : { workflowRunId: problem.workflowRunId }),
      ...(problem.details === undefined ? {} : { details: problem.details }),
    });
  } catch (error: unknown) {
    if (error instanceof AuthoringApiError && error.code === "HTTP_ERROR") return error;
    return invalidResponse("problem", status, error);
  }
};

interface AuthoringJsonResponse {
  payload: unknown;
  status: number;
}

const request = async (
  path: string,
  init: RequestInit,
  successStatuses: readonly number[],
): Promise<AuthoringJsonResponse> => {
  let response: Response;
  try {
    response = await authFetch(path, init);
  } catch (error: unknown) {
    if (isAbortError(error)) throw error;
    throw new AuthoringApiError("NETWORK_ERROR", "NETWORK_ERROR", "无法连接创作服务。", null, true, { cause: error });
  }
  const contentType = response.headers.get("Content-Type")?.split(";", 1)[0]?.trim().toLowerCase();
  if (contentType !== "application/json") throw invalidResponse("content_type", response.status);

  let source: string;
  try {
    source = await response.text();
  } catch (error: unknown) {
    if (isAbortError(error)) throw error;
    throw invalidResponse("json", response.status, error);
  }
  let payload: unknown;
  try {
    payload = strictJson(source);
  } catch (error: unknown) {
    throw invalidResponse("json", response.status, error);
  }
  if (!response.ok) throw decodeHttpProblem(payload, response.status);
  if (!successStatuses.includes(response.status)) throw invalidResponse("status", response.status);
  return { payload, status: response.status };
};

const commandInit = (idempotencyKey: string, body: Readonly<Record<string, unknown>>, signal?: AbortSignal): RequestInit => ({
  method: "POST",
  headers: { "Idempotency-Key": requireKey(idempotencyKey) },
  body: JSON.stringify(body),
  ...withSignal(signal),
});

const decodeWorkingDraftCommandResult = (value: unknown, workspaceId: string, draftId?: string): WorkingDraftCommandResult => {
  const row = exact(value, ["working_draft", "replayed"], [], "working_draft_command");
  const workingDraft = assertWorkspace(decodeWorkingDraft(row.working_draft), workspaceId, "working_draft.workspace_id");
  if (draftId !== undefined && workingDraft.id !== draftId) throw invalidResponse("working_draft.id");
  return { workingDraft, replayed: boolean(row.replayed, "working_draft_command.replayed") };
};

export const createWorkingDraft = async (input: CreateWorkingDraftInput): Promise<WorkingDraftCommandResult> => {
  const workspaceId = requireUuid(input.workspaceId, "workspaceId");
  const { payload, status } = await request(
    `/api/v1/workspaces/${encodeURIComponent(workspaceId)}/authoring/working-drafts`,
    commandInit(input.idempotencyKey, {}, input.signal),
    [200, 201],
  );
  const result = decodeWorkingDraftCommandResult(payload, workspaceId);
  if ((status === 200) !== result.replayed) throw invalidResponse("working_draft_command.status", status);
  return result;
};

export const getWorkingDraft = async (workspaceIdValue: string, draftIdValue: string, signal?: AbortSignal): Promise<WorkingDraft> => {
  const workspaceId = requireUuid(workspaceIdValue, "workspaceId");
  const draftId = requireUuid(draftIdValue, "draftId");
  const { payload } = await request(
    `/api/v1/workspaces/${encodeURIComponent(workspaceId)}/authoring/working-drafts/${encodeURIComponent(draftId)}`,
    withSignal(signal),
    [200],
  );
  const draft = assertWorkspace(decodeWorkingDraft(payload), workspaceId, "working_draft.workspace_id");
  if (draft.id !== draftId) throw invalidResponse("working_draft.id");
  return draft;
};

export const updateWorkingDraft = async (input: UpdateWorkingDraftInput): Promise<WorkingDraftCommandResult> => {
  const workspaceId = requireUuid(input.workspaceId, "workspaceId");
  const draftId = requireUuid(input.draftId, "draftId");
  const expectedVersion = requireVersion(input.expectedVersion);
  const { payload } = await request(
    `/api/v1/workspaces/${encodeURIComponent(workspaceId)}/authoring/working-drafts/${encodeURIComponent(draftId)}`,
    {
      method: "PUT",
      headers: { "Idempotency-Key": requireKey(input.idempotencyKey) },
      body: JSON.stringify({
        expected_version: expectedVersion,
        title: requireDraftText(input.title, "title", 512, false),
        target_path: requireDraftText(input.targetPath, "targetPath", 4096, false),
        body: requireDraftText(input.body, "body", maxBodyBytes),
      }),
      ...withSignal(input.signal),
    },
    [200],
  );
  return decodeWorkingDraftCommandResult(payload, workspaceId, draftId);
};

export const freezeWorkingDraft = async (input: FreezeWorkingDraftInput): Promise<FreezeWorkingDraftResult> => {
  const workspaceId = requireUuid(input.workspaceId, "workspaceId");
  const draftId = requireUuid(input.draftId, "draftId");
  const { payload, status } = await request(
    `/api/v1/workspaces/${encodeURIComponent(workspaceId)}/authoring/working-drafts/${encodeURIComponent(draftId)}/freeze`,
    commandInit(input.idempotencyKey, { expected_version: requireVersion(input.expectedVersion) }, input.signal),
    [200, 201],
  );
  const row = exact(payload, ["working_draft", "document", "article_revision", "replayed"], [], "freeze");
  const workingDraft = assertWorkspace(decodeWorkingDraft(row.working_draft), workspaceId, "freeze.working_draft.workspace_id");
  const document = assertWorkspace(decodeDocumentDraft(row.document), workspaceId, "freeze.document.workspace_id");
  const articleRevision = assertWorkspace(decodeArticleRevisionDraft(row.article_revision), workspaceId, "freeze.article_revision.workspace_id");
  if (workingDraft.id !== draftId || workingDraft.documentId !== document.id || articleRevision.documentId !== document.id) {
    throw invalidResponse("freeze.binding");
  }
  const replayed = boolean(row.replayed, "freeze.replayed");
  if ((status === 200) !== replayed) throw invalidResponse("freeze.status", status);
  return { workingDraft, document, articleRevision, replayed };
};

export const publishArticleRevision = async (input: PublishRevisionInput): Promise<PublishRevisionResult> => {
  const workspaceId = requireUuid(input.workspaceId, "workspaceId");
  const documentId = requireUuid(input.documentId, "documentId");
  const revisionId = requireUuid(input.revisionId, "revisionId");
  const { payload, status } = await request(
    `/api/v1/workspaces/${encodeURIComponent(workspaceId)}/documents/${encodeURIComponent(documentId)}/revisions/${encodeURIComponent(revisionId)}/publish-proposals`,
    commandInit(input.idempotencyKey, {}, input.signal),
    [200, 201],
  );
  const row = exact(payload, ["publication", "replayed"], [], "publish");
  const publication = assertWorkspace(decodePublicationBinding(row.publication), workspaceId, "publish.publication.workspace_id");
  if (publication.documentId !== documentId || publication.articleRevisionId !== revisionId) throw invalidResponse("publish.binding");
  const replayed = boolean(row.replayed, "publish.replayed");
  if ((status === 200) !== replayed) throw invalidResponse("publish.status", status);
  return { publication, replayed };
};

export const getDocumentDraft = async (workspaceIdValue: string, documentIdValue: string, signal?: AbortSignal): Promise<DocumentDraftDetail> => {
  const workspaceId = requireUuid(workspaceIdValue, "workspaceId");
  const documentId = requireUuid(documentIdValue, "documentId");
  const { payload } = await request(
    `/api/v1/workspaces/${encodeURIComponent(workspaceId)}/documents/${encodeURIComponent(documentId)}`,
    withSignal(signal),
    [200],
  );
  const row = exact(payload, ["document", "current_revision", "publication"], [], "document_detail");
  const document = assertWorkspace(decodeDocumentDraft(row.document), workspaceId, "document_detail.document.workspace_id");
  if (document.id !== documentId) throw invalidResponse("document_detail.document.id");
  const currentRevision = row.current_revision === null
    ? null
    : assertWorkspace(decodeArticleRevisionDraft(row.current_revision), workspaceId, "document_detail.current_revision.workspace_id");
  const publication = row.publication === null
    ? null
    : assertWorkspace(decodePublicationBinding(row.publication), workspaceId, "document_detail.publication.workspace_id");
  if ((currentRevision !== null && currentRevision.documentId !== documentId) || (publication !== null && publication.documentId !== documentId)) {
    throw invalidResponse("document_detail.binding");
  }
  return { document, currentRevision, publication };
};

export const getAuthoringOverview = async (workspaceIdValue: string, signal?: AbortSignal): Promise<AuthoringOverview> => {
  const workspaceId = requireUuid(workspaceIdValue, "workspaceId");
  const { payload } = await request(
    `/api/v1/workspaces/${encodeURIComponent(workspaceId)}/authoring/overview`,
    withSignal(signal),
    [200],
  );
  const row = exact(payload, ["workspace_id", "organizing", "recent_drafts", "pending_publications", "completed_documents"], [], "overview");
  if (uuid(row.workspace_id, "overview.workspace_id") !== workspaceId) throw invalidResponse("overview.workspace_id");
  const organizingRow = exact(row.organizing, ["available", "reason", "href"], [], "overview.organizing");
  const organizing: OrganizingAvailability = {
    available: boolean(organizingRow.available, "overview.organizing.available"),
    reason: organizingRow.reason === null ? null : text(organizingRow.reason, "overview.organizing.reason", 1024),
    href: organizingRow.href === null ? null : text(organizingRow.href, "overview.organizing.href", 2048),
  };
  if (organizing.available !== (organizing.href !== null) || (organizing.available && organizing.href !== "/authoring/organize") || (!organizing.available && organizing.reason === null)) {
    throw invalidResponse("overview.organizing");
  }
  const recentDrafts = readArray(row.recent_drafts, "overview.recent_drafts", (item) => assertWorkspace(decodeWorkingDraftSummary(item), workspaceId, "overview.recent_drafts.workspace_id"));
  const pendingPublications = readArray(row.pending_publications, "overview.pending_publications", (item) => assertWorkspace(decodePublicationBinding(item), workspaceId, "overview.pending_publications.workspace_id"));
  const completedDocuments = readArray(row.completed_documents, "overview.completed_documents", (item) => assertWorkspace(decodeDocumentDraft(item), workspaceId, "overview.completed_documents.workspace_id"));
  assertUniqueIds(recentDrafts, "overview.recent_drafts");
  assertUniqueIds(pendingPublications, "overview.pending_publications");
  assertUniqueIds(completedDocuments, "overview.completed_documents");
  if (pendingPublications.some((publication) => publication.status !== "PENDING" && publication.status !== "RECOVERY_REQUIRED")) throw invalidResponse("overview.pending_publications");
  if (completedDocuments.some((document) => document.lifecycleStatus !== "PUBLISHED")) throw invalidResponse("overview.completed_documents");
  return { workspaceId, organizing, recentDrafts, pendingPublications, completedDocuments };
};

export const isPublishableDraft = (draft: Pick<WorkingDraft, "title" | "targetPath" | "body">): boolean => {
  const title = draft.title.trim();
  const targetPath = draft.targetPath.trim();
  const pathSegments = targetPath.split("/");
  return title !== ""
    && title === draft.title
    && draft.body.trim() !== ""
    && encoder.encode(draft.title).byteLength <= 512
    && encoder.encode(targetPath).byteLength <= 4096
    && encoder.encode(draft.body).byteLength <= maxBodyBytes
    && !lineBreakPattern.test(draft.title)
    && !controlPattern.test(targetPath)
    && !draft.body.includes("\u0000")
    && targetPath === draft.targetPath
    && targetPath.toLowerCase().endsWith(".md")
    && !targetPath.startsWith("/")
    && !targetPath.includes("\\")
    && pathSegments.every((segment) => segment !== "" && segment !== "." && segment !== "..")
    && ![".git", ".knowledge"].includes((pathSegments[0] ?? "").toLowerCase());
};
