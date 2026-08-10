/** Collection Export 的唯一网络边界：只向 Feature 暴露已验证的任务投影。 */

import { authFetch } from "./auth";
import { canonicalUuidPattern as uuidPattern, hasOnlyKeys, isAbortError, isRecord } from "../shared/codec";

export type ExportKind = "MARKDOWN" | "METADATA_JSON";
export type ExportStatus = "PENDING" | "RUNNING" | "SUCCEEDED" | "FAILED" | "EXPIRED" | "CANCELLED";
export type ExportRedactionPolicy = "MASKED" | "FULL";
export type ExportField = "object_type" | "id" | "title" | "summary" | "status" | "topic" | "source" | "relations" | "health" | "confidence" | "created_at" | "updated_at" | "applicability";

export interface ExportJob {
  id: string;
  version: number;
  workspaceId: string;
  kind: ExportKind;
  schemaVersion: string;
  collectionId: string;
  collectionVersion: number;
  queryHash: string;
  readModelRevision: string | null;
  exactCount: number | null;
  fields: ExportField[];
  redactionPolicy: ExportRedactionPolicy;
  includeSensitive: boolean;
  status: ExportStatus;
  fileHash: string | null;
  fileSize: number;
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

export interface ExportCreateInput {
  workspaceId: string;
  collectionId: string;
  collectionVersion: number;
  queryHash: string;
  kind: ExportKind;
  fields: ExportField[];
  idempotencyKey: string;
}

export interface ExportCreateResult {
  job: ExportJob;
  replayed: boolean;
  dispatchPending: boolean;
}

export interface ExportListPage {
  workspaceId: string;
  items: ExportJob[];
  nextCursor: string | null;
}

export interface ExportDownload {
  blob: Blob;
  filename: string;
  contentType: "text/markdown" | "application/json";
}

export class ExportApiError extends Error {
  readonly code: "INVALID_REQUEST" | "INVALID_RESPONSE" | "HTTP_ERROR" | "NETWORK_ERROR";
  readonly errorCode: string;
  readonly retryable: boolean;
  readonly status: number | null;

  constructor(code: ExportApiError["code"], errorCode: string, message: string, retryable: boolean, status: number | null = null, options?: ErrorOptions) {
    super(message, options);
    this.name = "ExportApiError";
    this.code = code;
    this.errorCode = errorCode;
    this.retryable = retryable;
    this.status = status;
  }
}

const hashPattern = /^[0-9a-f]{64}$/;
const timestampPattern = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})$/;
const tokenPattern = /^[A-Z][A-Z0-9_]{0,127}$/;
const exportKinds: readonly ExportKind[] = ["MARKDOWN", "METADATA_JSON"];
const exportStatuses: readonly ExportStatus[] = ["PENDING", "RUNNING", "SUCCEEDED", "FAILED", "EXPIRED", "CANCELLED"];
const exportFields: readonly ExportField[] = ["object_type", "id", "title", "summary", "status", "topic", "source", "relations", "health", "confidence", "created_at", "updated_at", "applicability"];
const jobKeys = ["id", "version", "workspace_id", "kind", "schema_version", "collection_id", "collection_version", "query_hash", "read_model_revision", "exact_count", "fields", "redaction_policy", "include_sensitive", "status", "file_hash", "file_size", "error_code", "error_message", "attempt_count", "expires_at", "created_at", "updated_at", "started_at", "completed_at", "download_count", "last_downloaded_at", "download_url"] as const;
const problemKeys = ["error_code", "message", "retryable", "workflow_run_id", "details"] as const;
const utf8Encoder = new TextEncoder();

const invalidResponse = (field: string, status: number | null = null): ExportApiError => new ExportApiError("INVALID_RESPONSE", "INVALID_RESPONSE", `Export 响应字段无效：${field}`, false, status);
const invalidRequest = (field: string): ExportApiError => new ExportApiError("INVALID_REQUEST", "INVALID_REQUEST", `Export 请求字段无效：${field}`, false);
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

class StrictJsonParser {
  private index = 0;
  constructor(private readonly source: string) {}
  parse(): unknown { this.space(); const value = this.value(); this.space(); if (this.index !== this.source.length) throw new SyntaxError("trailing JSON data"); return value; }
  private value(): unknown {
    const current = this.source[this.index];
    if (current === "{") return this.object(); if (current === "[") return this.array(); if (current === '"') return this.string();
    if (this.source.startsWith("true", this.index)) { this.index += 4; return true; }
    if (this.source.startsWith("false", this.index)) { this.index += 5; return false; }
    if (this.source.startsWith("null", this.index)) { this.index += 4; return null; }
    const match = /^-?(?:0|[1-9]\d*)(?:\.\d+)?(?:[eE][+-]?\d+)?/.exec(this.source.slice(this.index));
    if (match === null) throw new SyntaxError("invalid JSON value"); this.index += match[0].length; const parsed = Number(match[0]); if (!Number.isFinite(parsed)) throw new SyntaxError("non-finite JSON number"); return parsed;
  }
  private object(): Record<string, unknown> {
    this.index += 1; this.space(); const entries: [string, unknown][] = []; const seen = new Set<string>();
    if (this.source[this.index] === "}") { this.index += 1; return {}; }
    for (;;) { if (this.source[this.index] !== '"') throw new SyntaxError("invalid JSON key"); const key = this.string(); if (seen.has(key)) throw new SyntaxError(`duplicate JSON key: ${key}`); seen.add(key); this.space(); if (this.source[this.index] !== ":") throw new SyntaxError("missing colon"); this.index += 1; this.space(); entries.push([key, this.value()]); this.space(); if (this.source[this.index] === "}") { this.index += 1; return Object.fromEntries(entries); } if (this.source[this.index] !== ",") throw new SyntaxError("invalid JSON separator"); this.index += 1; this.space(); }
  }
  private array(): unknown[] {
    this.index += 1; this.space(); const values: unknown[] = []; if (this.source[this.index] === "]") { this.index += 1; return values; }
    for (;;) { values.push(this.value()); this.space(); if (this.source[this.index] === "]") { this.index += 1; return values; } if (this.source[this.index] !== ",") throw new SyntaxError("invalid JSON separator"); this.index += 1; this.space(); }
  }
  private string(): string { const start = this.index; this.index += 1; while (this.index < this.source.length) { if (this.source[this.index] === "\\") { this.index += 2; continue; } if (this.source[this.index] === '"') { this.index += 1; const parsed: unknown = JSON.parse(this.source.slice(start, this.index)); if (typeof parsed !== "string") throw new SyntaxError("invalid JSON string"); return parsed; } this.index += 1; } throw new SyntaxError("unterminated JSON string"); }
  private space(): void { while (/\s/.test(this.source[this.index] ?? "")) this.index += 1; }
}

const strictJson = (source: string): unknown => new StrictJsonParser(source).parse();
const decodeJob = (value: unknown): ExportJob => {
  if (!isRecord(value)) throw invalidResponse("job");
  exact(value, jobKeys, "job");
  const status = enumValue(value.status, exportStatuses, "job.status");
  const fileHash = value.file_hash === undefined || value.file_hash === null ? null : hash(value.file_hash, "job.file_hash");
  const errorCode = value.error_code === undefined || value.error_code === null ? null : token(value.error_code, "job.error_code");
  const errorMessage = optionalText(value.error_message, "job.error_message");
  if (errorMessage !== null && utf8Encoder.encode(errorMessage).byteLength > 4096) throw invalidResponse("job.error_message");
  const startedAt = optionalTimestamp(value.started_at, "job.started_at");
  const completedAt = optionalTimestamp(value.completed_at, "job.completed_at");
  const downloadUrl = optionalText(value.download_url, "job.download_url");
  const readModelRevision = value.read_model_revision === undefined || value.read_model_revision === null ? null : hash(value.read_model_revision, "job.read_model_revision");
  const exactCount = value.exact_count === undefined || value.exact_count === null ? null : positiveInteger(value.exact_count, "job.exact_count");
  if (exactCount !== null && exactCount > 10_000) throw invalidResponse("job.exact_count");
  const fields = Array.isArray(value.fields) ? value.fields.map((field, index) => enumValue(field, exportFields, `job.fields[${String(index)}]`)) : (() => { throw invalidResponse("job.fields"); })();
  if (fields.length === 0 || new Set(fields).size !== fields.length) throw invalidResponse("job.fields");
  const job: ExportJob = {
    id: uuid(value.id, "job.id"), version: positiveInteger(value.version, "job.version", 1), workspaceId: uuid(value.workspace_id, "job.workspace_id"), kind: enumValue(value.kind, exportKinds, "job.kind"), schemaVersion: text(value.schema_version, "job.schema_version"),
    collectionId: uuid(value.collection_id, "job.collection_id"), collectionVersion: positiveInteger(value.collection_version, "job.collection_version", 1), queryHash: hash(value.query_hash, "job.query_hash"), readModelRevision, exactCount, fields,
    redactionPolicy: enumValue(value.redaction_policy, ["MASKED", "FULL"] as const, "job.redaction_policy"), includeSensitive: bool(value.include_sensitive, "job.include_sensitive"), status,
    fileHash, fileSize: positiveInteger(value.file_size, "job.file_size"), errorCode, errorMessage, attemptCount: positiveInteger(value.attempt_count, "job.attempt_count"),
    expiresAt: timestamp(value.expires_at, "job.expires_at"), createdAt: timestamp(value.created_at, "job.created_at"), updatedAt: timestamp(value.updated_at, "job.updated_at"), startedAt, completedAt,
    downloadCount: positiveInteger(value.download_count, "job.download_count"), lastDownloadedAt: optionalTimestamp(value.last_downloaded_at, "job.last_downloaded_at"), downloadUrl,
  };
  const prepared = readModelRevision !== null;
  if (job.schemaVersion !== "export/v1" || Date.parse(job.expiresAt) <= Date.parse(job.createdAt) || Date.parse(job.updatedAt) < Date.parse(job.createdAt) || prepared !== (exactCount !== null) || prepared !== (fileHash !== null) || (!prepared && job.fileSize !== 0) || (job.redactionPolicy === "FULL") !== job.includeSensitive) throw invalidResponse("job.invariants");
  if ([job.startedAt, job.completedAt, job.lastDownloadedAt].some((value) => value !== null && (Date.parse(value) < Date.parse(job.createdAt) || Date.parse(value) > Date.parse(job.updatedAt)))) throw invalidResponse("job.lifecycle_time");
  if (status === "PENDING" && (prepared || job.fileSize !== 0 || startedAt !== null || completedAt !== null)) throw invalidResponse("job.pending_state");
  if (status === "RUNNING" && (startedAt === null || completedAt !== null)) throw invalidResponse("job.running_state");
  if (status === "SUCCEEDED" && (!prepared || completedAt === null || downloadUrl === null || errorCode !== null || errorMessage !== null)) throw invalidResponse("job.result_state");
  if (status !== "SUCCEEDED" && downloadUrl !== null) throw invalidResponse("job.download_url_state");
  if (status === "FAILED" && (startedAt === null || completedAt === null || errorCode === null || errorMessage === null)) throw invalidResponse("job.failed_state");
  if (status === "EXPIRED" && completedAt === null) throw invalidResponse("job.expired_state");
  if (status !== "FAILED" && (errorCode !== null || errorMessage !== null)) throw invalidResponse("job.error_state");
  if ((job.downloadCount === 0) !== (job.lastDownloadedAt === null) || (job.downloadCount > 0 && ((status !== "SUCCEEDED" && status !== "EXPIRED") || !prepared))) throw invalidResponse("job.download_state");
  if (downloadUrl !== null) {
    let parsed: URL;
    try { parsed = new URL(downloadUrl, "http://export.invalid"); } catch { throw invalidResponse("job.download_url"); }
    if (parsed.origin !== "http://export.invalid" || parsed.pathname !== `/api/v1/exports/${job.id}/download` || parsed.searchParams.get("workspace_id") !== job.workspaceId || [...parsed.searchParams.keys()].length !== 1) throw invalidResponse("job.download_url");
  }
  return job;
};

const decodeProblem = (value: unknown, status: number): ExportApiError => {
  try {
    if (!isRecord(value)) throw invalidResponse("problem", status);
    exact(value, problemKeys, "problem");
    const errorCode = token(value.error_code, "problem.error_code");
    const message = text(value.message, "problem.message");
    const retryable = bool(value.retryable, "problem.retryable");
    if (value.workflow_run_id !== undefined) uuid(value.workflow_run_id, "problem.workflow_run_id");
    if (value.details !== undefined && !isRecord(value.details)) throw invalidResponse("problem.details", status);
    return new ExportApiError("HTTP_ERROR", errorCode, message, retryable, status);
  } catch (error: unknown) {
    return new ExportApiError("INVALID_RESPONSE", "INVALID_RESPONSE", "Export API 返回了无效 Problem。", false, status, { cause: error });
  }
};

const request = async (path: string, init: RequestInit = {}): Promise<unknown> => {
  const headers = new Headers(init.headers); headers.set("Accept", "application/json"); if (init.body !== undefined) headers.set("Content-Type", "application/json");
  let response: Response;
  try { response = await authFetch(path, { ...init, headers }); } catch (error: unknown) { if (isAbortError(error)) throw error; throw new ExportApiError("NETWORK_ERROR", "NETWORK_ERROR", "无法连接 Export API。", true, null, { cause: error }); }
  let payload: unknown;
  try { payload = strictJson(await response.text()); } catch (error: unknown) { throw new ExportApiError("INVALID_RESPONSE", "INVALID_RESPONSE", "Export API 返回了无效或包含重复字段的 JSON。", false, response.status, { cause: error }); }
  if (!response.ok) throw decodeProblem(payload, response.status);
  return payload;
};

const requireUuid = (value: string, field: string): string => { if (!uuidPattern.test(value)) throw invalidRequest(field); return value; };
const requireHash = (value: string, field: string): string => { if (!hashPattern.test(value)) throw invalidRequest(field); return value; };
const requireKey = (value: string): string => { if (value.trim() === "" || value !== value.trim() || value.length > 128) throw invalidRequest("idempotencyKey"); return value; };
const signalInit = (signal?: AbortSignal): RequestInit => signal === undefined ? {} : { signal };

const assertBinding = (job: ExportJob, workspaceId: string, collectionId?: string): ExportJob => {
  if (job.workspaceId !== workspaceId || (collectionId !== undefined && job.collectionId !== collectionId)) throw invalidResponse("job.binding");
  return job;
};

export const createExport = (input: ExportCreateInput, signal?: AbortSignal): Promise<ExportCreateResult> => {
  const workspaceId = requireUuid(input.workspaceId, "workspaceId"); const collectionId = requireUuid(input.collectionId, "collectionId");
  if (!Number.isSafeInteger(input.collectionVersion) || input.collectionVersion < 1 || !exportKinds.includes(input.kind) || input.fields.length === 0 || new Set(input.fields).size !== input.fields.length || input.fields.some((field) => !exportFields.includes(field))) throw invalidRequest("create");
  return request("/api/v1/exports", { method: "POST", headers: { "Idempotency-Key": requireKey(input.idempotencyKey) }, body: JSON.stringify({ workspace_id: workspaceId, collection_id: collectionId, collection_version: input.collectionVersion, query_hash: requireHash(input.queryHash, "queryHash"), kind: input.kind, fields: input.fields, redaction_policy: "MASKED" }), ...signalInit(signal) }).then((value) => {
    if (!isRecord(value)) throw invalidResponse("create"); exact(value, ["job", "replayed", "dispatch_pending"], "create"); const job = assertBinding(decodeJob(value.job), workspaceId, collectionId);
    return { job, replayed: bool(value.replayed, "create.replayed"), dispatchPending: bool(value.dispatch_pending, "create.dispatch_pending") };
  });
};

export const listCollectionExports = (workspaceIdValue: string, collectionIdValue: string, params: { cursor?: string; limit?: number } = {}, signal?: AbortSignal): Promise<ExportListPage> => {
  const workspaceId = requireUuid(workspaceIdValue, "workspaceId"); const collectionId = requireUuid(collectionIdValue, "collectionId"); const limit = params.limit ?? 25;
  if (!Number.isSafeInteger(limit) || limit < 1 || limit > 100 || (params.cursor !== undefined && (params.cursor.trim() === "" || utf8Encoder.encode(params.cursor).byteLength > 4096))) throw invalidRequest("list");
  const query = new URLSearchParams({ collection_id: collectionId, limit: String(limit), ...(params.cursor === undefined ? {} : { cursor: params.cursor }) });
  return request(`/api/v1/workspaces/${encodeURIComponent(workspaceId)}/exports?${query}`, signalInit(signal)).then((value) => {
    if (!isRecord(value)) throw invalidResponse("list"); exact(value, ["workspace_id", "items", "next_cursor"], "list"); if (!Array.isArray(value.items) || value.items.length > 100) throw invalidResponse("list.items");
    const pageWorkspaceId = uuid(value.workspace_id, "list.workspace_id"); if (pageWorkspaceId !== workspaceId) throw invalidResponse("list.workspace_id");
    const items = value.items.map(decodeJob).map((job) => assertBinding(job, workspaceId, collectionId)); if (new Set(items.map((job) => job.id)).size !== items.length) throw invalidResponse("list.items");
    return { workspaceId, items, nextCursor: cursor(value.next_cursor, "list.next_cursor") };
  });
};

export const getExport = (workspaceIdValue: string, exportIdValue: string, signal?: AbortSignal): Promise<ExportJob> => {
  const workspaceId = requireUuid(workspaceIdValue, "workspaceId"); const exportId = requireUuid(exportIdValue, "exportId");
  return request(`/api/v1/exports/${encodeURIComponent(exportId)}?workspace_id=${encodeURIComponent(workspaceId)}`, signalInit(signal)).then(decodeJob).then((job) => { if (job.id !== exportId) throw invalidResponse("job.id"); return assertBinding(job, workspaceId); });
};

export const downloadExport = async (workspaceIdValue: string, job: ExportJob, signal?: AbortSignal): Promise<ExportDownload> => {
  const workspaceId = requireUuid(workspaceIdValue, "workspaceId"); assertBinding(job, workspaceId);
  if (job.status !== "SUCCEEDED" || job.downloadUrl === null) throw invalidRequest("download");
  let response: Response;
  try { response = await authFetch(job.downloadUrl, signalInit(signal)); } catch (error: unknown) { if (isAbortError(error)) throw error; throw new ExportApiError("NETWORK_ERROR", "NETWORK_ERROR", "无法下载 Export 结果。", true, null, { cause: error }); }
  if (!response.ok) {
    let payload: unknown; try { payload = strictJson(await response.text()); } catch { throw new ExportApiError("INVALID_RESPONSE", "INVALID_RESPONSE", "Export 下载错误响应无效。", false, response.status); }
    throw decodeProblem(payload, response.status);
  }
  const type = response.headers.get("Content-Type")?.split(";", 1)[0]?.toLowerCase(); const contentType = type === "text/markdown" || type === "application/json" ? type : undefined;
  const expectedContentType = job.kind === "MARKDOWN" ? "text/markdown" : "application/json";
  const expectedFilename = `collection-${job.collectionId}-${job.id}.${job.kind === "MARKDOWN" ? "md" : "json"}`;
  const contentDisposition = response.headers.get("Content-Disposition");
  if (contentType !== expectedContentType || response.headers.get("X-Content-Type-Options")?.toLowerCase() !== "nosniff" || response.headers.get("Cache-Control") !== "private, no-store" || response.headers.get("Content-Length") !== String(job.fileSize) || contentDisposition !== `attachment; filename="${expectedFilename}"`) throw invalidResponse("download.headers", response.status);
  const blob = await response.blob(); if (!Number.isSafeInteger(blob.size) || blob.size !== job.fileSize) throw invalidResponse("download.size", response.status);
  return { blob, filename: expectedFilename, contentType };
};

export { decodeJob, strictJson };
