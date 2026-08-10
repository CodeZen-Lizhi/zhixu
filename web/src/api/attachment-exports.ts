/** Workspace attachment export 的严格网络边界。 */

import { authFetch } from "./auth";
import { strictJson } from "./exports";
import { canonicalUuidPattern as uuidPattern, hasOnlyKeys, isAbortError, isRecord } from "../shared/codec";

export type AttachmentExportStatus = "PENDING" | "RUNNING" | "SUCCEEDED" | "FAILED" | "EXPIRED" | "CANCELLED";

export interface AttachmentExportJob {
  id: string;
  workspaceId: string;
  scopeKind: "WORKSPACE_ATTACHMENTS";
  kind: "ATTACHMENTS_ZIP";
  schemaVersion: "attachment-export/v1";
  attachmentRootContractVersion: "workspace-attachments/v1";
  contentPolicy: "RAW_USER_OWNED";
  status: AttachmentExportStatus;
  version: number;
  manifestSHA256: string | null;
  entryCount: number | null;
  totalUncompressedBytes: number | null;
  archiveSHA256: string | null;
  archiveSize: number | null;
  errorCode: string | null;
  errorMessage: string | null;
  attemptCount: number;
  expiresAt: string;
  createdAt: string;
  updatedAt: string;
  startedAt: string | null;
  completedAt: string | null;
  downloadCount: number;
  lastDownloadedAt: string | null;
  downloadUrl: string | null;
}

export interface AttachmentExportCreateInput {
  workspaceId: string;
  idempotencyKey: string;
  expiresInSeconds?: number;
}

export interface AttachmentExportCreateResult {
  job: AttachmentExportJob;
  replayed: boolean;
  dispatchPending: boolean;
}

export interface AttachmentExportPage {
  workspaceId: string;
  scopeKind: "WORKSPACE_ATTACHMENTS";
  items: AttachmentExportJob[];
  nextCursor: string | null;
}

export interface AttachmentExportDownload {
  blob: Blob;
  filename: string;
}

export class AttachmentExportApiError extends Error {
  readonly code: "INVALID_REQUEST" | "INVALID_RESPONSE" | "HTTP_ERROR" | "NETWORK_ERROR";
  readonly errorCode: string;
  readonly retryable: boolean;
  readonly status: number | null;

  constructor(code: AttachmentExportApiError["code"], errorCode: string, message: string, retryable: boolean, status: number | null = null, options?: ErrorOptions) {
    super(message, options);
    this.name = "AttachmentExportApiError";
    this.code = code;
    this.errorCode = errorCode;
    this.retryable = retryable;
    this.status = status;
  }
}

const hashPattern = /^[0-9a-f]{64}$/;
const timestampPattern = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})$/;
const tokenPattern = /^[A-Z][A-Z0-9_]{0,127}$/;
const attachmentStatuses: readonly AttachmentExportStatus[] = ["PENDING", "RUNNING", "SUCCEEDED", "FAILED", "EXPIRED", "CANCELLED"];
const jobKeys = ["id", "workspace_id", "scope_kind", "kind", "schema_version", "attachment_root_contract_version", "content_policy", "status", "version", "manifest_sha256", "entry_count", "total_uncompressed_bytes", "archive_sha256", "archive_size", "error_code", "error_message", "attempt_count", "expires_at", "created_at", "updated_at", "started_at", "completed_at", "download_count", "last_downloaded_at", "download_url"] as const;
const pageKeys = ["workspace_id", "scope_kind", "items", "next_cursor"] as const;
const responseKeys = ["job", "replayed", "dispatch_pending"] as const;
const problemKeys = ["error_code", "message", "retryable", "workflow_run_id", "details"] as const;
const utf8Encoder = new TextEncoder();

const invalidResponse = (field: string, status: number | null = null): AttachmentExportApiError => new AttachmentExportApiError("INVALID_RESPONSE", "INVALID_RESPONSE", `附件导出响应字段无效：${field}`, false, status);
const invalidRequest = (field: string): AttachmentExportApiError => new AttachmentExportApiError("INVALID_REQUEST", "INVALID_REQUEST", `附件导出请求字段无效：${field}`, false);
const exact = (value: Record<string, unknown>, keys: readonly string[], field: string): void => { if (!hasOnlyKeys(value, keys)) throw invalidResponse(field); };
const text = (value: unknown, field: string, allowEmpty = false): string => { if (typeof value !== "string" || (!allowEmpty && value.trim() === "")) throw invalidResponse(field); return value; };
const optionalText = (value: unknown, field: string): string | null => value === undefined || value === null ? null : text(value, field);
const uuid = (value: unknown, field: string): string => { const parsed = text(value, field); if (!uuidPattern.test(parsed)) throw invalidResponse(field); return parsed; };
const hash = (value: unknown, field: string): string => { const parsed = text(value, field); if (!hashPattern.test(parsed)) throw invalidResponse(field); return parsed; };
const token = (value: unknown, field: string): string => { const parsed = text(value, field); if (!tokenPattern.test(parsed)) throw invalidResponse(field); return parsed; };
const positiveInteger = (value: unknown, field: string, minimum = 0): number => { if (typeof value !== "number" || !Number.isSafeInteger(value) || value < minimum) throw invalidResponse(field); return value; };
const bool = (value: unknown, field: string): boolean => { if (typeof value !== "boolean") throw invalidResponse(field); return value; };
const timestamp = (value: unknown, field: string): string => { const parsed = text(value, field); if (!isTimestamp(parsed)) throw invalidResponse(field); return parsed; };
const optionalTimestamp = (value: unknown, field: string): string | null => value === undefined || value === null ? null : timestamp(value, field);
const enumValue = <T extends string>(value: unknown, values: readonly T[], field: string): T => { if (typeof value !== "string" || !values.includes(value as T)) throw invalidResponse(field); return value as T; };
const cursor = (value: unknown, field: string): string | null => { if (value === undefined || value === null) return null; const parsed = text(value, field); if (utf8Encoder.encode(parsed).byteLength > 4096) throw invalidResponse(field); return parsed; };

const isTimestamp = (value: string): boolean => {
  if (!timestampPattern.test(value) || !Number.isFinite(Date.parse(value))) return false;
  const match = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})/.exec(value);
  if (match === null) return false;
  const year = Number(match[1]); const month = Number(match[2]); const day = Number(match[3]);
  const hour = Number(match[4]); const minute = Number(match[5]); const second = Number(match[6]);
  return month >= 1 && month <= 12 && day >= 1 && day <= new Date(Date.UTC(year, month, 0)).getUTCDate() && hour <= 23 && minute <= 59 && second <= 59;
};

const decodeProblem = (value: unknown, status: number): AttachmentExportApiError => {
  try {
    if (!isRecord(value)) throw invalidResponse("problem", status);
    exact(value, problemKeys, "problem");
    const errorCode = token(value.error_code, "problem.error_code");
    const message = text(value.message, "problem.message");
    const retryable = bool(value.retryable, "problem.retryable");
    if (value.workflow_run_id !== undefined) uuid(value.workflow_run_id, "problem.workflow_run_id");
    if (value.details !== undefined && !isRecord(value.details)) throw invalidResponse("problem.details", status);
    return new AttachmentExportApiError("HTTP_ERROR", errorCode, message, retryable, status);
  } catch (error: unknown) {
    return new AttachmentExportApiError("INVALID_RESPONSE", "INVALID_RESPONSE", "附件导出 API 返回了无效 Problem。", false, status, { cause: error });
  }
};

const decodeJob = (value: unknown): AttachmentExportJob => {
  if (!isRecord(value)) throw invalidResponse("job");
  exact(value, jobKeys, "job");
  const status = enumValue(value.status, attachmentStatuses, "job.status");
  const manifestSHA256 = value.manifest_sha256 === undefined || value.manifest_sha256 === null ? null : hash(value.manifest_sha256, "job.manifest_sha256");
  const entryCount = value.entry_count === undefined || value.entry_count === null ? null : positiveInteger(value.entry_count, "job.entry_count");
  const totalUncompressedBytes = value.total_uncompressed_bytes === undefined || value.total_uncompressed_bytes === null ? null : positiveInteger(value.total_uncompressed_bytes, "job.total_uncompressed_bytes");
  const archiveSHA256 = value.archive_sha256 === undefined || value.archive_sha256 === null ? null : hash(value.archive_sha256, "job.archive_sha256");
  const archiveSize = value.archive_size === undefined || value.archive_size === null ? null : positiveInteger(value.archive_size, "job.archive_size");
  const errorCode = value.error_code === undefined || value.error_code === null ? null : token(value.error_code, "job.error_code");
  const errorMessage = optionalText(value.error_message, "job.error_message");
  if (errorMessage !== null && utf8Encoder.encode(errorMessage).byteLength > 1024) throw invalidResponse("job.error_message");
  const startedAt = optionalTimestamp(value.started_at, "job.started_at");
  const completedAt = optionalTimestamp(value.completed_at, "job.completed_at");
  const lastDownloadedAt = optionalTimestamp(value.last_downloaded_at, "job.last_downloaded_at");
  const downloadUrl = optionalText(value.download_url, "job.download_url");
  const job: AttachmentExportJob = {
    id: uuid(value.id, "job.id"), workspaceId: uuid(value.workspace_id, "job.workspace_id"),
    scopeKind: enumValue(value.scope_kind, ["WORKSPACE_ATTACHMENTS"] as const, "job.scope_kind"),
    kind: enumValue(value.kind, ["ATTACHMENTS_ZIP"] as const, "job.kind"),
    schemaVersion: enumValue(value.schema_version, ["attachment-export/v1"] as const, "job.schema_version"),
    attachmentRootContractVersion: enumValue(value.attachment_root_contract_version, ["workspace-attachments/v1"] as const, "job.attachment_root_contract_version"),
    contentPolicy: enumValue(value.content_policy, ["RAW_USER_OWNED"] as const, "job.content_policy"),
    status, version: positiveInteger(value.version, "job.version", 1), manifestSHA256, entryCount, totalUncompressedBytes, archiveSHA256, archiveSize,
    errorCode, errorMessage, attemptCount: positiveInteger(value.attempt_count, "job.attempt_count"),
    expiresAt: timestamp(value.expires_at, "job.expires_at"), createdAt: timestamp(value.created_at, "job.created_at"), updatedAt: timestamp(value.updated_at, "job.updated_at"),
    startedAt, completedAt, downloadCount: positiveInteger(value.download_count, "job.download_count"), lastDownloadedAt, downloadUrl,
  };
  const prepared = manifestSHA256 !== null;
  if (entryCount !== null && entryCount > 10_000 || totalUncompressedBytes !== null && totalUncompressedBytes > 1_073_741_824 || archiveSize !== null && archiveSize > 1_073_741_824 ||
      Date.parse(job.expiresAt) <= Date.parse(job.createdAt) || Date.parse(job.updatedAt) < Date.parse(job.createdAt) ||
      prepared !== (entryCount !== null) || prepared !== (totalUncompressedBytes !== null) || prepared !== (archiveSHA256 !== null) || prepared !== (archiveSize !== null)) throw invalidResponse("job.archive_binding");
  if ([startedAt, completedAt, lastDownloadedAt].some((item) => item !== null && (Date.parse(item) < Date.parse(job.createdAt) || Date.parse(item) > Date.parse(job.updatedAt)))) throw invalidResponse("job.lifecycle_time");
  if (status === "PENDING" && (prepared || startedAt !== null || completedAt !== null || errorCode !== null || errorMessage !== null || job.downloadCount !== 0 || downloadUrl !== null)) throw invalidResponse("job.pending_state");
  if (status === "RUNNING" && (startedAt === null || completedAt !== null || errorCode !== null || errorMessage !== null || job.downloadCount !== 0 || downloadUrl !== null)) throw invalidResponse("job.running_state");
  if (status === "SUCCEEDED" && (!prepared || startedAt === null || completedAt === null || errorCode !== null || errorMessage !== null || downloadUrl === null)) throw invalidResponse("job.result_state");
  if (status === "FAILED" && (startedAt === null || completedAt === null || errorCode === null || errorMessage === null || job.downloadCount !== 0 || downloadUrl !== null)) throw invalidResponse("job.failed_state");
  if (status === "EXPIRED" && (completedAt === null || errorCode !== null || errorMessage !== null || downloadUrl !== null)) throw invalidResponse("job.expired_state");
  if (status === "CANCELLED" && (errorCode !== null || errorMessage !== null || job.downloadCount !== 0 || downloadUrl !== null)) throw invalidResponse("job.cancelled_state");
  if ((job.downloadCount === 0) !== (lastDownloadedAt === null) || job.downloadCount > 0 && ((status !== "SUCCEEDED" && status !== "EXPIRED") || !prepared)) throw invalidResponse("job.download_state");
  if (downloadUrl !== null) {
    let parsed: URL;
    try { parsed = new URL(downloadUrl, "http://attachment-export.invalid"); } catch { throw invalidResponse("job.download_url"); }
    if (parsed.origin !== "http://attachment-export.invalid" || parsed.pathname !== `/api/v1/workspaces/${job.workspaceId}/attachment-exports/${job.id}/download` || parsed.search !== "") throw invalidResponse("job.download_url");
  }
  return job;
};

const decodeCreate = (value: unknown): AttachmentExportCreateResult => {
  if (!isRecord(value)) throw invalidResponse("create");
  exact(value, responseKeys, "create");
  return { job: decodeJob(value.job), replayed: bool(value.replayed, "create.replayed"), dispatchPending: bool(value.dispatch_pending, "create.dispatch_pending") };
};

const decodePage = (value: unknown): AttachmentExportPage => {
  if (!isRecord(value)) throw invalidResponse("page");
  exact(value, pageKeys, "page");
  if (!Array.isArray(value.items) || value.items.length > 100) throw invalidResponse("page.items");
  return {
    workspaceId: uuid(value.workspace_id, "page.workspace_id"),
    scopeKind: enumValue(value.scope_kind, ["WORKSPACE_ATTACHMENTS"] as const, "page.scope_kind"),
    items: value.items.map(decodeJob),
    nextCursor: cursor(value.next_cursor, "page.next_cursor"),
  };
};

const request = async (path: string, init: RequestInit = {}): Promise<unknown> => {
  const headers = new Headers(init.headers);
  headers.set("Accept", "application/json");
  if (init.body !== undefined) headers.set("Content-Type", "application/json");
  let response: Response;
  try { response = await authFetch(path, { ...init, headers }); } catch (error: unknown) { if (isAbortError(error)) throw error; throw new AttachmentExportApiError("NETWORK_ERROR", "NETWORK_ERROR", "无法连接附件导出 API。", true, null, { cause: error }); }
  let payload: unknown;
  try { payload = strictJson(await response.text()); } catch (error: unknown) { throw new AttachmentExportApiError("INVALID_RESPONSE", "INVALID_RESPONSE", "附件导出 API 返回了无效或包含重复字段的 JSON。", false, response.status, { cause: error }); }
  if (!response.ok) throw decodeProblem(payload, response.status);
  return payload;
};

const requireUuid = (value: string, field: string): string => { if (!uuidPattern.test(value)) throw invalidRequest(field); return value; };
const requireKey = (value: string): string => { if (value.trim() === "" || value !== value.trim() || value.length > 128) throw invalidRequest("idempotencyKey"); return value; };
const signalInit = (signal?: AbortSignal): RequestInit => signal === undefined ? {} : { signal };
const assertBinding = (job: AttachmentExportJob, workspaceId: string): AttachmentExportJob => { if (job.workspaceId !== workspaceId) throw invalidResponse("job.binding"); return job; };
const sha256Hex = async (blob: Blob): Promise<string> => {
  const digest = await globalThis.crypto.subtle.digest("SHA-256", await blob.arrayBuffer());
  return Array.from(new Uint8Array(digest), (byte) => byte.toString(16).padStart(2, "0")).join("");
};

export const createAttachmentExport = (input: AttachmentExportCreateInput, signal?: AbortSignal): Promise<AttachmentExportCreateResult> => {
  const workspaceId = requireUuid(input.workspaceId, "workspaceId");
  const expiresInSeconds = input.expiresInSeconds;
  if (expiresInSeconds !== undefined && (!Number.isSafeInteger(expiresInSeconds) || expiresInSeconds < 1 || expiresInSeconds > 604800)) throw invalidRequest("expiresInSeconds");
  return request(`/api/v1/workspaces/${encodeURIComponent(workspaceId)}/attachment-exports`, {
    method: "POST", headers: { "Idempotency-Key": requireKey(input.idempotencyKey) },
    body: JSON.stringify({ kind: "ATTACHMENTS_ZIP", schema_version: "attachment-export/v1", attachment_root_contract_version: "workspace-attachments/v1", content_policy: "RAW_USER_OWNED", ...(expiresInSeconds === undefined ? {} : { expires_in_seconds: expiresInSeconds }) }),
    ...signalInit(signal),
  }).then(decodeCreate).then((result) => { assertBinding(result.job, workspaceId); return result; });
};

export const listAttachmentExports = (workspaceIdValue: string, options: { cursor?: string; limit?: number } = {}, signal?: AbortSignal): Promise<AttachmentExportPage> => {
  const workspaceId = requireUuid(workspaceIdValue, "workspaceId");
  if (options.cursor !== undefined && (options.cursor.trim() === "" || utf8Encoder.encode(options.cursor).byteLength > 4096) || options.limit !== undefined && (!Number.isSafeInteger(options.limit) || options.limit < 1 || options.limit > 100)) throw invalidRequest("list");
  const query = new URLSearchParams();
  if (options.limit !== undefined) query.set("limit", String(options.limit));
  if (options.cursor !== undefined) query.set("cursor", options.cursor);
  const suffix = query.size === 0 ? "" : `?${query.toString()}`;
  return request(`/api/v1/workspaces/${encodeURIComponent(workspaceId)}/attachment-exports${suffix}`, signalInit(signal)).then(decodePage).then((page) => {
    if (page.workspaceId !== workspaceId || page.items.some((job) => job.workspaceId !== workspaceId)) throw invalidResponse("page.binding");
    return page;
  });
};

export const getAttachmentExport = (workspaceIdValue: string, exportIdValue: string, signal?: AbortSignal): Promise<AttachmentExportJob> => {
  const workspaceId = requireUuid(workspaceIdValue, "workspaceId");
  const exportId = requireUuid(exportIdValue, "exportId");
  return request(`/api/v1/workspaces/${encodeURIComponent(workspaceId)}/attachment-exports/${encodeURIComponent(exportId)}`, signalInit(signal)).then(decodeJob).then((job) => {
    if (job.id !== exportId) throw invalidResponse("job.id");
    return assertBinding(job, workspaceId);
  });
};

export const downloadAttachmentExport = async (workspaceIdValue: string, job: AttachmentExportJob, signal?: AbortSignal): Promise<AttachmentExportDownload> => {
  const workspaceId = requireUuid(workspaceIdValue, "workspaceId");
  assertBinding(job, workspaceId);
  if (job.status !== "SUCCEEDED" || job.downloadUrl === null || job.archiveSize === null) throw invalidRequest("download");
  let response: Response;
  try { response = await authFetch(job.downloadUrl, signalInit(signal)); } catch (error: unknown) { if (isAbortError(error)) throw error; throw new AttachmentExportApiError("NETWORK_ERROR", "NETWORK_ERROR", "无法下载附件归档。", true, null, { cause: error }); }
  if (!response.ok) {
    let payload: unknown;
    try { payload = strictJson(await response.text()); } catch { throw new AttachmentExportApiError("INVALID_RESPONSE", "INVALID_RESPONSE", "附件归档下载错误响应无效。", false, response.status); }
    throw decodeProblem(payload, response.status);
  }
  const filename = `workspace-attachments-${job.id}.zip`;
  const type = response.headers.get("Content-Type")?.split(";", 1)[0]?.toLowerCase();
  if (type !== "application/zip" || response.headers.get("Content-Disposition") !== `attachment; filename="${filename}"` ||
      response.headers.get("Content-Length") !== String(job.archiveSize) || response.headers.get("Cache-Control") !== "private, no-store" ||
      response.headers.get("X-Content-Type-Options")?.toLowerCase() !== "nosniff") throw invalidResponse("download.headers", response.status);
  const blob = await response.blob();
  if (!Number.isSafeInteger(blob.size) || blob.size !== job.archiveSize) throw invalidResponse("download.size", response.status);
  if (await sha256Hex(blob) !== job.archiveSHA256) throw invalidResponse("download.sha256", response.status);
  return { blob, filename };
};

export { decodeJob as decodeAttachmentExportJob };
