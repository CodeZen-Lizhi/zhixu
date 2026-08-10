import { authFetch } from "./auth";
import { canonicalUuidPattern as uuidPattern, hasOnlyKeys, isRecord } from "../shared/codec";

export interface SourceSpanReference {
  workspaceId: string;
  sourceVersionId: string;
  sourceSpanId: string;
}

export interface SourceSpan {
  sourceVersion: {
    logicalName: string;
    relativePath: string;
    capturedAt: string;
  };
  spanType: string;
  startLine: number;
  endLine: number;
  excerpt: string;
  excerptTruncated: boolean;
}

export class SourceSpanApiError extends Error {
  readonly code: string;
  readonly status: number | null;
  readonly retryable: boolean;

  constructor(code: string, message: string, status: number | null, retryable: boolean, options?: ErrorOptions) {
    super(message, options);
    this.name = "SourceSpanApiError";
    this.code = code;
    this.status = status;
    this.retryable = retryable;
  }
}

const hashPattern = /^[0-9a-f]{64}$/;
const rfc3339Pattern = /^\d{4}-(?:0[1-9]|1[0-2])-(?:0[1-9]|[12]\d|3[01])T(?:[01]\d|2[0-3]):[0-5]\d:[0-5]\d(?:\.\d{1,9})?(?:Z|[+-](?:[01]\d|2[0-3]):[0-5]\d)$/;
const textEncoder = new TextEncoder();

const invalidResponse = (field: string, status: number | null = null): SourceSpanApiError =>
  new SourceSpanApiError("INVALID_RESPONSE", `Source Span 响应字段无效：${field}`, status, false);

const assertExactKeys = (value: Record<string, unknown>, allowed: readonly string[], field: string, status?: number): void => {
  if (!hasOnlyKeys(value, allowed)) throw invalidResponse(field, status ?? null);
};

const readString = (value: unknown, field: string, status?: number): string => {
  if (typeof value !== "string") throw invalidResponse(field, status ?? null);
  return value;
};

const readBoundedString = (value: unknown, field: string, maxBytes: number, allowEmpty = false, status?: number): string => {
  const result = readString(value, field, status);
  if ((!allowEmpty && result.trim() === "") || result !== result.trim() || textEncoder.encode(result).length > maxBytes) {
    throw invalidResponse(field, status ?? null);
  }
  return result;
};

const readExcerpt = (value: unknown, field: string): string => {
  const result = readString(value, field);
  if (textEncoder.encode(result).length > 4096) throw invalidResponse(field);
  return result;
};

const readUuid = (value: unknown, field: string, status?: number): string => {
  const result = readString(value, field, status);
  if (!uuidPattern.test(result)) throw invalidResponse(field, status ?? null);
  return result;
};

const daysInMonth = (year: number, month: number): number => {
  if (month === 2) return year % 4 === 0 && (year % 100 !== 0 || year % 400 === 0) ? 29 : 28;
  return month === 4 || month === 6 || month === 9 || month === 11 ? 30 : 31;
};

const readTimestamp = (value: unknown, field: string, status?: number): string => {
  const result = readString(value, field, status);
  const year = Number(result.slice(0, 4));
  const month = Number(result.slice(5, 7));
  const day = Number(result.slice(8, 10));
  if (!rfc3339Pattern.test(result) || year < 1 || day > daysInMonth(year, month) || !Number.isFinite(Date.parse(result))) {
    throw invalidResponse(field, status ?? null);
  }
  return result;
};

const readInteger = (value: unknown, field: string, minimum: number, status?: number): number => {
  if (typeof value !== "number" || !Number.isSafeInteger(value) || value < minimum) throw invalidResponse(field, status ?? null);
  return value;
};

const readProblem = (payload: unknown, status: number): SourceSpanApiError => {
  if (!isRecord(payload)) return invalidResponse("Problem", status);
  assertExactKeys(payload, ["error_code", "message", "retryable", "workflow_run_id", "details"], "Problem", status);
  const code = readBoundedString(payload.error_code, "Problem.error_code", 128, false, status);
  const message = readBoundedString(payload.message, "Problem.message", 4096, false, status);
  if (typeof payload.retryable !== "boolean") throw invalidResponse("Problem.retryable", status);
  if (payload.workflow_run_id !== undefined) readUuid(payload.workflow_run_id, "Problem.workflow_run_id", status);
  if (payload.details !== undefined && !isRecord(payload.details)) throw invalidResponse("Problem.details", status);
  return new SourceSpanApiError(code, message, status, payload.retryable);
};

export const decodeSourceSpan = (payload: unknown, reference: SourceSpanReference): SourceSpan => {
  if (!isRecord(payload)) throw invalidResponse("root");
  assertExactKeys(payload, [
    "source_version", "parse_projection_id", "span_id", "span_type", "start_line", "end_line", "start_byte", "end_byte",
    "selector", "excerpt_hash", "parser_version", "schema_version", "excerpt", "excerpt_truncated",
  ], "root");
  if (!isRecord(payload.source_version)) throw invalidResponse("source_version");
  const sourceVersion = payload.source_version;
  assertExactKeys(sourceVersion, [
    "workspace_id", "source_id", "source_version_id", "source_type", "logical_name", "relative_path", "content_hash", "byte_size",
    "media_type", "security_status", "ingestion_status", "workflow_status", "index_status", "captured_at",
  ], "source_version");
  const workspaceId = readUuid(sourceVersion.workspace_id, "source_version.workspace_id");
  const sourceVersionId = readUuid(sourceVersion.source_version_id, "source_version.source_version_id");
  const sourceSpanId = readUuid(payload.span_id, "span_id");
  if (workspaceId !== reference.workspaceId || sourceVersionId !== reference.sourceVersionId || sourceSpanId !== reference.sourceSpanId) {
    throw invalidResponse("identity");
  }
  readUuid(sourceVersion.source_id, "source_version.source_id");
  readBoundedString(sourceVersion.source_type, "source_version.source_type", 128);
  const logicalName = readBoundedString(sourceVersion.logical_name, "source_version.logical_name", 1024);
  const relativePath = readBoundedString(sourceVersion.relative_path, "source_version.relative_path", 4096);
  const contentHash = readString(sourceVersion.content_hash, "source_version.content_hash");
  if (!hashPattern.test(contentHash)) throw invalidResponse("source_version.content_hash");
  const byteSize = readInteger(sourceVersion.byte_size, "source_version.byte_size", 0);
  readBoundedString(sourceVersion.media_type, "source_version.media_type", 256);
  const securityStatus = readBoundedString(sourceVersion.security_status, "source_version.security_status", 128);
  if (!(["pending", "passed", "quarantined"] as const).includes(securityStatus as "pending" | "passed" | "quarantined")) {
    throw invalidResponse("source_version.security_status");
  }
  const optionalState = (field: "ingestion_status" | "workflow_status" | "index_status", allowed: readonly string[]): void => {
    const value = sourceVersion[field];
    if (value === undefined) return;
    const state = readBoundedString(value, `source_version.${field}`, 128);
    if (!allowed.includes(state)) throw invalidResponse(`source_version.${field}`);
  };
  optionalState("ingestion_status", ["validating", "parsing", "parsed", "chunking", "chunked", "parse_failed", "cancelled"]);
  optionalState("workflow_status", ["pending", "running", "waiting_for_human", "retry_wait", "paused", "succeeded", "failed", "cancelled"]);
  optionalState("index_status", ["included", "excluded"]);
  const capturedAt = readTimestamp(sourceVersion.captured_at, "source_version.captured_at");
  readUuid(payload.parse_projection_id, "parse_projection_id");
  const spanType = readBoundedString(payload.span_type, "span_type", 128);
  const startLine = readInteger(payload.start_line, "start_line", 1);
  const endLine = readInteger(payload.end_line, "end_line", 1);
  const startByte = readInteger(payload.start_byte, "start_byte", 0);
  const endByte = readInteger(payload.end_byte, "end_byte", 0);
  if (endLine < startLine || endByte < startByte || endByte > byteSize || !isRecord(payload.selector)) throw invalidResponse("range");
  const excerptHash = readString(payload.excerpt_hash, "excerpt_hash");
  if (!hashPattern.test(excerptHash)) throw invalidResponse("excerpt_hash");
  readBoundedString(payload.parser_version, "parser_version", 256);
  readBoundedString(payload.schema_version, "schema_version", 256);
  const excerpt = readExcerpt(payload.excerpt, "excerpt");
  if (typeof payload.excerpt_truncated !== "boolean") throw invalidResponse("excerpt_truncated");
  return { sourceVersion: { logicalName, relativePath, capturedAt }, spanType, startLine, endLine, excerpt, excerptTruncated: payload.excerpt_truncated };
};

export const getSourceSpan = async (reference: SourceSpanReference, signal?: AbortSignal): Promise<SourceSpan> => {
  const identifiers: readonly (readonly [string, string])[] = [
    ["workspaceId", reference.workspaceId],
    ["sourceVersionId", reference.sourceVersionId],
    ["sourceSpanId", reference.sourceSpanId],
  ];
  for (const [field, value] of identifiers) {
    if (!uuidPattern.test(value)) throw new SourceSpanApiError("INVALID_REQUEST", `Source Span 请求字段无效：${field}`, null, false);
  }
  const path = `/api/v1/workspaces/${encodeURIComponent(reference.workspaceId)}/source-versions/${encodeURIComponent(reference.sourceVersionId)}/spans/${encodeURIComponent(reference.sourceSpanId)}`;
  let response: Response;
  try {
    response = await authFetch(path, signal === undefined ? undefined : { signal });
  } catch (error: unknown) {
    if (error instanceof DOMException && error.name === "AbortError") throw error;
    throw new SourceSpanApiError("NETWORK_ERROR", "无法连接 Source Span API。", null, true, { cause: error });
  }
  let payload: unknown;
  try {
    payload = await response.json();
  } catch (error: unknown) {
    throw new SourceSpanApiError("INVALID_RESPONSE", "Source Span API 返回了无效 JSON。", response.status, false, { cause: error });
  }
  if (!response.ok) throw readProblem(payload, response.status);
  return decodeSourceSpan(payload, reference);
};
