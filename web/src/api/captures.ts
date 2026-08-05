/** Quick Capture 的唯一网络边界：只向 Feature 暴露已验证的领域投影。 */

import { authFetch } from "./auth";
import { strictJson } from "./exports";

export type CaptureKind = "TEXT" | "URL" | "FILE" | "IMAGE";

export type CaptureStatus =
  | "RECEIVED"
  | "SOURCE_SAVED"
  | "FETCHING"
  | "PROCESSING"
  | "READY"
  | "READY_DEGRADED"
  | "FETCH_FAILED"
  | "PROCESSING_FAILED";

export type CaptureStageStatus =
  | "PENDING"
  | "RUNNING"
  | "READY"
  | "FAILED"
  | "CAPABILITY_UNAVAILABLE"
  | "STALE"
  | "NOT_APPLICABLE";

export interface Capture {
  id: string;
  workspaceId: string;
  kind: CaptureKind;
  displayName: string;
  originalUrl?: string;
  sourceId: string;
  latestSourceVersionId?: string;
  status: CaptureStatus;
  fetchStatus: CaptureStageStatus;
  ingestionStatus: CaptureStageStatus;
  indexStatus: CaptureStageStatus;
  profileStatus: CaptureStageStatus;
  failureStage?: string;
  errorCode?: string;
  retryable: boolean;
  version: number;
  capturedAt: string;
  updatedAt: string;
  detailHref: string;
  profileHref?: string;
}

interface CaptureCreateBase {
  workspaceId: string;
  idempotencyKey: string;
  displayName?: string;
}

export type CaptureCreateInput =
  | (CaptureCreateBase & { kind: "TEXT"; text: string })
  | (CaptureCreateBase & { kind: "URL"; url: string })
  | (CaptureCreateBase & { kind: "FILE" | "IMAGE"; file: File });

export interface CaptureCommandResult {
  capture: Capture;
  replayed: boolean;
}

export interface CaptureListParams {
  kind?: CaptureKind;
  status?: CaptureStatus;
  limit?: number;
  cursor?: string;
}

export interface CapturePage {
  workspaceId: string;
  items: Capture[];
  nextCursor?: string;
}

export type KnowledgeProfileStatus =
  | "PENDING"
  | "RUNNING"
  | "READY"
  | "FAILED"
  | "CAPABILITY_UNAVAILABLE"
  | "STALE";

export interface KnowledgeProfileCandidate {
  label: string;
  aliases: string[];
  sourceSpanIds: string[];
}

export interface KnowledgeProfilePoint {
  text: string;
  sourceSpanIds: string[];
}

export interface KnowledgeProfileRevision {
  id: string;
  profileId: string;
  workspaceId: string;
  sourceVersionId: string;
  parseProjectionId: string;
  indexVersionId: string;
  modelRunId: string;
  modelSettingsRevision: number | null;
  promptVersion: string;
  schemaVersion: "document-knowledge-profile/v1";
  summary: string;
  topics: KnowledgeProfileCandidate[];
  terms: KnowledgeProfileCandidate[];
  knowledgePoints: KnowledgeProfilePoint[];
  examples: KnowledgeProfilePoint[];
  contentDigest: string;
  createdAt: string;
}

export interface DocumentKnowledgeProfile {
  id: string;
  workspaceId: string;
  captureId: string;
  sourceVersionId: string;
  currentRevisionId?: string;
  status: KnowledgeProfileStatus;
  errorCode?: string;
  retryable: boolean;
  version: number;
  createdAt: string;
  updatedAt: string;
  revision?: KnowledgeProfileRevision;
}

export interface KnowledgeProfileBinding {
  workspaceId: string;
  sourceVersionId: string;
  captureId?: string;
}

export interface KnowledgeProfileRetryInput extends KnowledgeProfileBinding {
  expectedVersion: number;
  idempotencyKey: string;
}

export interface KnowledgeProfileCommandResult {
  profile: DocumentKnowledgeProfile;
  replayed: boolean;
}

export interface CaptureRetryInput {
  workspaceId: string;
  captureId: string;
  expectedVersion: number;
  idempotencyKey: string;
}

export interface CaptureBinding {
  workspaceId: string;
  captureId?: string;
  kind?: CaptureKind;
}

type CaptureApiErrorKind = "INVALID_REQUEST" | "INVALID_RESPONSE" | "HTTP_ERROR" | "NETWORK_ERROR";

interface CaptureApiErrorOptions {
  status?: number | null;
  retryable?: boolean;
  workflowRunId?: string;
  details?: Readonly<Record<string, unknown>>;
  cause?: unknown;
}

export class CaptureApiError extends Error {
  readonly code: CaptureApiErrorKind;
  readonly errorCode: string;
  readonly status: number | null;
  readonly retryable: boolean;
  readonly workflowRunId: string | undefined;
  readonly details: Readonly<Record<string, unknown>> | undefined;

  constructor(
    code: CaptureApiErrorKind,
    errorCode: string,
    message: string,
    options: CaptureApiErrorOptions = {},
  ) {
    super(message, options.cause === undefined ? undefined : { cause: options.cause });
    this.name = "CaptureApiError";
    this.code = code;
    this.errorCode = errorCode;
    this.status = options.status ?? null;
    this.retryable = options.retryable ?? false;
    this.workflowRunId = options.workflowRunId;
    this.details = options.details;
  }
}

const uuidPattern = /^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/;
const hashPattern = /^[0-9a-f]{64}$/;
const rfc3339Pattern = /^\d{4}-(?:0[1-9]|1[0-2])-(?:0[1-9]|[12]\d|3[01])T(?:[01]\d|2[0-3]):[0-5]\d:[0-5]\d(?:\.\d{1,9})?(?:Z|[+-](?:[01]\d|2[0-3]):[0-5]\d)$/;
const errorCodePattern = /^[A-Z][A-Z0-9_]{0,127}$/;
const controlCharacterPattern = /[\u0000-\u001f\u007f]/;
const unsafeContentCharacterPattern = /[\u0000-\u0008\u000b\u000c\u000e-\u001f\u007f]/;
const textEncoder = new TextEncoder();
const maxJSONBodyBytes = 2 * 1024 * 1024;
const maxUploadBytes = 10 * 1024 * 1024;
const defaultListLimit = 30;

const requiredCaptureKeys = [
  "id",
  "workspace_id",
  "kind",
  "display_name",
  "source_id",
  "status",
  "fetch_status",
  "ingestion_status",
  "index_status",
  "profile_status",
  "retryable",
  "version",
  "captured_at",
  "updated_at",
  "detail_href",
] as const;

const optionalCaptureKeys = [
  "original_url",
  "latest_source_version_id",
  "failure_stage",
  "error_code",
  "profile_href",
] as const;

const isRecord = (value: unknown): value is Record<string, unknown> =>
  typeof value === "object" && value !== null && !Array.isArray(value);

const invalidRequest = (field: string, cause?: unknown): CaptureApiError =>
  new CaptureApiError("INVALID_REQUEST", "INVALID_REQUEST", `Capture 请求字段无效：${field}`, { cause });

const invalidResponse = (field: string, status: number | null = null, cause?: unknown): CaptureApiError =>
  new CaptureApiError("INVALID_RESPONSE", "INVALID_RESPONSE", `Capture 响应字段无效：${field}`, {
    status,
    cause,
  });

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

const readString = (
  value: unknown,
  field: string,
  maxBytes: number,
  status: number | null = null,
): string => {
  if (typeof value !== "string" || value === "" || value !== value.trim() || controlCharacterPattern.test(value)) {
    throw invalidResponse(field, status);
  }
  if (textEncoder.encode(value).byteLength > maxBytes) throw invalidResponse(field, status);
  return value;
};

const readUuid = (value: unknown, field: string, status: number | null = null): string => {
  const parsed = readString(value, field, 36, status);
  if (!uuidPattern.test(parsed)) throw invalidResponse(field, status);
  return parsed;
};

const readContent = (
  value: unknown,
  field: string,
  maxBytes: number,
  status: number | null = null,
): string => {
  if (
    typeof value !== "string"
    || value === ""
    || value !== value.trim()
    || unsafeContentCharacterPattern.test(value)
    || textEncoder.encode(value).byteLength > maxBytes
  ) {
    throw invalidResponse(field, status);
  }
  return value;
};

const readHash = (value: unknown, field: string): string => {
  const parsed = readString(value, field, 64);
  if (!hashPattern.test(parsed)) throw invalidResponse(field);
  return parsed;
};

const daysInMonth = (year: number, month: number): number => {
  if (month === 2) return year % 4 === 0 && (year % 100 !== 0 || year % 400 === 0) ? 29 : 28;
  return month === 4 || month === 6 || month === 9 || month === 11 ? 30 : 31;
};

const readTimestamp = (value: unknown, field: string, status: number | null = null): string => {
  const parsed = readString(value, field, 64, status);
  const year = Number(parsed.slice(0, 4));
  const month = Number(parsed.slice(5, 7));
  const day = Number(parsed.slice(8, 10));
  if (
    !rfc3339Pattern.test(parsed)
    || year < 1
    || day > daysInMonth(year, month)
    || !Number.isFinite(Date.parse(parsed))
  ) {
    throw invalidResponse(field, status);
  }
  return parsed;
};

const readVersion = (value: unknown, field: string, status: number | null = null): number => {
  if (typeof value !== "number" || !Number.isSafeInteger(value) || value < 1) {
    throw invalidResponse(field, status);
  }
  return value;
};

const readBoolean = (value: unknown, field: string, status: number | null = null): boolean => {
  if (typeof value !== "boolean") throw invalidResponse(field, status);
  return value;
};

const isCaptureKind = (value: unknown): value is CaptureKind =>
  value === "TEXT" || value === "URL" || value === "FILE" || value === "IMAGE";

const readCaptureKind = (value: unknown, field: string): CaptureKind => {
  if (!isCaptureKind(value)) throw invalidResponse(field);
  return value;
};

const isCaptureStatus = (value: unknown): value is CaptureStatus =>
  value === "RECEIVED"
  || value === "SOURCE_SAVED"
  || value === "FETCHING"
  || value === "PROCESSING"
  || value === "READY"
  || value === "READY_DEGRADED"
  || value === "FETCH_FAILED"
  || value === "PROCESSING_FAILED";

const readCaptureStatus = (value: unknown, field: string): CaptureStatus => {
  if (!isCaptureStatus(value)) throw invalidResponse(field);
  return value;
};

const isCaptureStageStatus = (value: unknown): value is CaptureStageStatus =>
  value === "PENDING"
  || value === "RUNNING"
  || value === "READY"
  || value === "FAILED"
  || value === "CAPABILITY_UNAVAILABLE"
  || value === "STALE"
  || value === "NOT_APPLICABLE";

const readCaptureStageStatus = (value: unknown, field: string): CaptureStageStatus => {
  if (!isCaptureStageStatus(value)) throw invalidResponse(field);
  return value;
};

const readOptional = <T>(
  value: Record<string, unknown>,
  key: string,
  reader: (item: unknown, field: string) => T,
): T | undefined => Object.hasOwn(value, key) ? reader(value[key], key) : undefined;

const readHttpUrl = (value: unknown, field: string): string => {
  const parsedValue = readString(value, field, 8192);
  let parsed: URL;
  try {
    parsed = new URL(parsedValue);
  } catch (error: unknown) {
    throw invalidResponse(field, null, error);
  }
  if (
    (parsed.protocol !== "http:" && parsed.protocol !== "https:")
    || parsed.hostname === ""
    || parsed.username !== ""
    || parsed.password !== ""
  ) {
    throw invalidResponse(field);
  }
  return parsedValue;
};

const readErrorCode = (value: unknown, field: string): string => {
  const parsed = readString(value, field, 128);
  if (!errorCodePattern.test(parsed)) throw invalidResponse(field);
  return parsed;
};

const expectedDetailHref = (workspaceId: string, captureId: string): string =>
  `/api/v1/workspaces/${workspaceId}/captures/${captureId}`;

const expectedProfileHref = (workspaceId: string, sourceVersionId: string): string =>
  `/api/v1/workspaces/${workspaceId}/source-versions/${sourceVersionId}/knowledge-profile`;

export const decodeCapture = (payload: unknown, binding: CaptureBinding): Capture => {
  const value = exactRecord(payload, requiredCaptureKeys, optionalCaptureKeys, "capture");
  const id = readUuid(value.id, "capture.id");
  const workspaceId = readUuid(value.workspace_id, "capture.workspace_id");
  const kind = readCaptureKind(value.kind, "capture.kind");
  const status = readCaptureStatus(value.status, "capture.status");
  const originalUrl = readOptional(value, "original_url", readHttpUrl);
  const latestSourceVersionId = readOptional(value, "latest_source_version_id", readUuid);
  const failureStage = readOptional(value, "failure_stage", (item, field) => readString(item, field, 128));
  const errorCode = readOptional(value, "error_code", readErrorCode);
  const profileHref = readOptional(value, "profile_href", (item, field) => readString(item, field, 4096));
  const retryable = readBoolean(value.retryable, "capture.retryable");
  const capturedAt = readTimestamp(value.captured_at, "capture.captured_at");
  const updatedAt = readTimestamp(value.updated_at, "capture.updated_at");
  const detailHref = readString(value.detail_href, "capture.detail_href", 4096);

  if (
    workspaceId !== binding.workspaceId
    || (binding.captureId !== undefined && id !== binding.captureId)
    || (binding.kind !== undefined && kind !== binding.kind)
  ) {
    throw invalidResponse("capture.binding");
  }
  if (detailHref !== expectedDetailHref(workspaceId, id)) throw invalidResponse("capture.detail_href");
  if ((failureStage === undefined) !== (errorCode === undefined)) throw invalidResponse("capture.failure");
  if (errorCode === undefined && retryable) throw invalidResponse("capture.retryable");
  if (Date.parse(updatedAt) < Date.parse(capturedAt)) throw invalidResponse("capture.updated_at");
  if ((kind === "URL") !== (originalUrl !== undefined)) throw invalidResponse("capture.original_url");
  if (kind !== "URL" && latestSourceVersionId === undefined) {
    throw invalidResponse("capture.latest_source_version_id");
  }
  if (
    latestSourceVersionId === undefined
    && status !== "RECEIVED"
    && status !== "FETCHING"
    && status !== "FETCH_FAILED"
  ) {
    throw invalidResponse("capture.status");
  }
  if ((latestSourceVersionId === undefined) !== (profileHref === undefined)) {
    throw invalidResponse("capture.profile_href");
  }
  if (
    latestSourceVersionId !== undefined
    && profileHref !== expectedProfileHref(workspaceId, latestSourceVersionId)
  ) {
    throw invalidResponse("capture.profile_href");
  }

  return {
    id,
    workspaceId,
    kind,
    displayName: readString(value.display_name, "capture.display_name", 512),
    ...(originalUrl === undefined ? {} : { originalUrl }),
    sourceId: readUuid(value.source_id, "capture.source_id"),
    ...(latestSourceVersionId === undefined ? {} : { latestSourceVersionId }),
    status,
    fetchStatus: readCaptureStageStatus(value.fetch_status, "capture.fetch_status"),
    ingestionStatus: readCaptureStageStatus(value.ingestion_status, "capture.ingestion_status"),
    indexStatus: readCaptureStageStatus(value.index_status, "capture.index_status"),
    profileStatus: readCaptureStageStatus(value.profile_status, "capture.profile_status"),
    ...(failureStage === undefined ? {} : { failureStage }),
    ...(errorCode === undefined ? {} : { errorCode }),
    retryable,
    version: readVersion(value.version, "capture.version"),
    capturedAt,
    updatedAt,
    detailHref,
    ...(profileHref === undefined ? {} : { profileHref }),
  };
};

export const decodeCaptureCommandResult = (
  payload: unknown,
  binding: CaptureBinding,
): CaptureCommandResult => {
  const value = exactRecord(payload, ["capture", "replayed"], [], "command_result");
  return {
    capture: decodeCapture(value.capture, binding),
    replayed: readBoolean(value.replayed, "command_result.replayed"),
  };
};

export const decodeCaptureList = (
  payload: unknown,
  workspaceId: string,
  params: CaptureListParams = {},
): CapturePage => {
  const value = exactRecord(payload, ["workspace_id", "items"], ["next_cursor"], "capture_list");
  const responseWorkspaceId = readUuid(value.workspace_id, "capture_list.workspace_id");
  if (responseWorkspaceId !== workspaceId) throw invalidResponse("capture_list.workspace_id");
  if (!Array.isArray(value.items)) throw invalidResponse("capture_list.items");
  const limit = params.limit ?? defaultListLimit;
  if (value.items.length > limit || value.items.length > 100) throw invalidResponse("capture_list.items");
  const items = value.items.map((item) => decodeCapture(item, { workspaceId }));
  if (new Set(items.map((item) => item.id)).size !== items.length) throw invalidResponse("capture_list.items");
  if (params.kind !== undefined && items.some((item) => item.kind !== params.kind)) {
    throw invalidResponse("capture_list.kind");
  }
  if (params.status !== undefined && items.some((item) => item.status !== params.status)) {
    throw invalidResponse("capture_list.status");
  }
  const nextCursor = readOptional(value, "next_cursor", (item, field) => readString(item, field, 4096));
  return {
    workspaceId,
    items,
    ...(nextCursor === undefined ? {} : { nextCursor }),
  };
};

const readProfileStatus = (value: unknown, field: string): KnowledgeProfileStatus => {
  if (
    value !== "PENDING"
    && value !== "RUNNING"
    && value !== "READY"
    && value !== "FAILED"
    && value !== "CAPABILITY_UNAVAILABLE"
    && value !== "STALE"
  ) {
    throw invalidResponse(field);
  }
  return value;
};

const readStringArray = (value: unknown, field: string, maximum: number): string[] => {
  if (!Array.isArray(value) || value.length > maximum) throw invalidResponse(field);
  const result = value.map((item, index) => readString(item, `${field}[${String(index)}]`, 512));
  if (new Set(result).size !== result.length) throw invalidResponse(field);
  return result;
};

const readUuidArray = (value: unknown, field: string): string[] => {
  if (!Array.isArray(value) || value.length < 1 || value.length > 64) throw invalidResponse(field);
  const result = value.map((item, index) => readUuid(item, `${field}[${String(index)}]`));
  if (new Set(result).size !== result.length) throw invalidResponse(field);
  return result;
};

const decodeProfileCandidate = (payload: unknown, field: string): KnowledgeProfileCandidate => {
  const value = exactRecord(payload, ["label", "source_span_ids"], ["aliases"], field);
  const aliases = readOptional(value, "aliases", (item, itemField) => readStringArray(item, itemField, 32));
  return {
    label: readString(value.label, `${field}.label`, 512),
    aliases: aliases ?? [],
    sourceSpanIds: readUuidArray(value.source_span_ids, `${field}.source_span_ids`),
  };
};

const decodeProfilePoint = (payload: unknown, field: string): KnowledgeProfilePoint => {
  const value = exactRecord(payload, ["text", "source_span_ids"], [], field);
  return {
    text: readContent(value.text, `${field}.text`, 4096),
    sourceSpanIds: readUuidArray(value.source_span_ids, `${field}.source_span_ids`),
  };
};

const readProfileArray = <T>(
  value: unknown,
  field: string,
  maximum: number,
  decoder: (item: unknown, itemField: string) => T,
): T[] => {
  if (!Array.isArray(value) || value.length > maximum) throw invalidResponse(field);
  return value.map((item, index) => decoder(item, `${field}[${String(index)}]`));
};

const decodeProfileRevision = (
  payload: unknown,
  binding: KnowledgeProfileBinding,
  expectedProfileId: string,
  expectedRevisionId: string,
): KnowledgeProfileRevision => {
  const value = exactRecord(payload, [
    "id", "profile_id", "workspace_id", "source_version_id", "parse_projection_id", "index_version_id", "model_run_id",
    "model_settings_revision", "prompt_version", "schema_version", "content", "content_digest", "created_at",
  ], [], "profile_revision");
  const id = readUuid(value.id, "profile_revision.id");
  const profileId = readUuid(value.profile_id, "profile_revision.profile_id");
  const workspaceId = readUuid(value.workspace_id, "profile_revision.workspace_id");
  const sourceVersionId = readUuid(value.source_version_id, "profile_revision.source_version_id");
  if (
    id !== expectedRevisionId
    || profileId !== expectedProfileId
    || workspaceId !== binding.workspaceId
    || sourceVersionId !== binding.sourceVersionId
  ) {
    throw invalidResponse("profile_revision.binding");
  }
  const settingsRevision = value.model_settings_revision;
  if (settingsRevision !== null && (typeof settingsRevision !== "number" || !Number.isSafeInteger(settingsRevision) || settingsRevision < 1)) {
    throw invalidResponse("profile_revision.model_settings_revision");
  }
  const content = exactRecord(value.content, ["summary", "topics", "terms", "knowledge_points", "examples"], [], "profile_revision.content");
  const schemaVersion = readString(value.schema_version, "profile_revision.schema_version", 128);
  if (schemaVersion !== "document-knowledge-profile/v1") throw invalidResponse("profile_revision.schema_version");
  return {
    id,
    profileId,
    workspaceId,
    sourceVersionId,
    parseProjectionId: readUuid(value.parse_projection_id, "profile_revision.parse_projection_id"),
    indexVersionId: readUuid(value.index_version_id, "profile_revision.index_version_id"),
    modelRunId: readUuid(value.model_run_id, "profile_revision.model_run_id"),
    modelSettingsRevision: settingsRevision,
    promptVersion: readString(value.prompt_version, "profile_revision.prompt_version", 128),
    schemaVersion,
    summary: readContent(content.summary, "profile_revision.content.summary", 16 * 1024),
    topics: readProfileArray(content.topics, "profile_revision.content.topics", 128, decodeProfileCandidate),
    terms: readProfileArray(content.terms, "profile_revision.content.terms", 128, decodeProfileCandidate),
    knowledgePoints: readProfileArray(content.knowledge_points, "profile_revision.content.knowledge_points", 256, decodeProfilePoint),
    examples: readProfileArray(content.examples, "profile_revision.content.examples", 256, decodeProfilePoint),
    contentDigest: readHash(value.content_digest, "profile_revision.content_digest"),
    createdAt: readTimestamp(value.created_at, "profile_revision.created_at"),
  };
};

export const decodeKnowledgeProfile = (
  payload: unknown,
  binding: KnowledgeProfileBinding,
): DocumentKnowledgeProfile => {
  const root = exactRecord(payload, ["profile", "revision"], [], "knowledge_profile");
  const value = exactRecord(root.profile, [
    "id", "workspace_id", "capture_id", "source_version_id", "status", "retryable", "version", "created_at", "updated_at",
  ], ["current_revision_id", "error_code"], "profile");
  const id = readUuid(value.id, "profile.id");
  const workspaceId = readUuid(value.workspace_id, "profile.workspace_id");
  const captureId = readUuid(value.capture_id, "profile.capture_id");
  const sourceVersionId = readUuid(value.source_version_id, "profile.source_version_id");
  if (
    workspaceId !== binding.workspaceId
    || sourceVersionId !== binding.sourceVersionId
    || (binding.captureId !== undefined && captureId !== binding.captureId)
  ) {
    throw invalidResponse("profile.binding");
  }
  const status = readProfileStatus(value.status, "profile.status");
  const currentRevisionId = readOptional(value, "current_revision_id", readUuid);
  const errorCode = readOptional(value, "error_code", readErrorCode);
  const retryable = readBoolean(value.retryable, "profile.retryable");
  if ((status === "FAILED" || status === "CAPABILITY_UNAVAILABLE") !== (errorCode !== undefined)) {
    throw invalidResponse("profile.error_code");
  }
  if (retryable && errorCode === undefined) throw invalidResponse("profile.retryable");
  if ((currentRevisionId === undefined) !== (root.revision === null)) throw invalidResponse("profile.revision");
  if ((status === "READY" || status === "STALE") && currentRevisionId === undefined) {
    throw invalidResponse("profile.current_revision_id");
  }
  const revision = currentRevisionId === undefined
    ? undefined
    : decodeProfileRevision(root.revision, binding, id, currentRevisionId);
  if (revision !== undefined && (revision.topics.length === 0 || revision.knowledgePoints.length === 0)) {
    throw invalidResponse("profile_revision.content");
  }
  const createdAt = readTimestamp(value.created_at, "profile.created_at");
  const updatedAt = readTimestamp(value.updated_at, "profile.updated_at");
  if (Date.parse(updatedAt) < Date.parse(createdAt)) throw invalidResponse("profile.updated_at");
  return {
    id,
    workspaceId,
    captureId,
    sourceVersionId,
    ...(currentRevisionId === undefined ? {} : { currentRevisionId }),
    status,
    ...(errorCode === undefined ? {} : { errorCode }),
    retryable,
    version: readVersion(value.version, "profile.version"),
    createdAt,
    updatedAt,
    ...(revision === undefined ? {} : { revision }),
  };
};

export const decodeKnowledgeProfileCommandResult = (
  payload: unknown,
  binding: KnowledgeProfileBinding,
): KnowledgeProfileCommandResult => {
  const value = exactRecord(payload, ["profile", "revision", "replayed"], [], "profile_command_result");
  return {
    profile: decodeKnowledgeProfile({ profile: value.profile, revision: value.revision }, binding),
    replayed: readBoolean(value.replayed, "profile_command_result.replayed"),
  };
};

const decodeProblem = (payload: unknown, status: number): CaptureApiError => {
  try {
    const value = exactRecord(
      payload,
      ["error_code", "message", "retryable"],
      ["workflow_run_id", "details"],
      "Problem",
      status,
    );
    const errorCode = readErrorCode(value.error_code, "Problem.error_code");
    const message = readString(value.message, "Problem.message", 4096, status);
    const retryable = readBoolean(value.retryable, "Problem.retryable", status);
    const workflowRunId = Object.hasOwn(value, "workflow_run_id")
      ? readUuid(value.workflow_run_id, "Problem.workflow_run_id", status)
      : undefined;
    let details: Readonly<Record<string, unknown>> | undefined;
    if (Object.hasOwn(value, "details")) {
      if (!isRecord(value.details)) throw invalidResponse("Problem.details", status);
      details = value.details;
    }
    return new CaptureApiError("HTTP_ERROR", errorCode, message, {
      status,
      retryable,
      ...(workflowRunId === undefined ? {} : { workflowRunId }),
      ...(details === undefined ? {} : { details }),
    });
  } catch (error: unknown) {
    return new CaptureApiError(
      "INVALID_RESPONSE",
      "INVALID_RESPONSE",
      "Capture API 返回了无效 Problem。",
      { status, cause: error },
    );
  }
};

const isAbortError = (value: unknown): boolean =>
  value instanceof DOMException
    ? value.name === "AbortError"
    : isRecord(value) && value.name === "AbortError";

const requestJSON = async (
  path: string,
  init: RequestInit,
  successStatuses: readonly number[],
): Promise<unknown> => {
  let response: Response;
  try {
    response = await authFetch(path, init);
  } catch (error: unknown) {
    if (isAbortError(error)) throw error;
    throw new CaptureApiError("NETWORK_ERROR", "NETWORK_ERROR", "无法连接 Capture API。", {
      retryable: true,
      cause: error,
    });
  }

  const contentType = response.headers.get("Content-Type")?.split(";", 1)[0]?.trim().toLowerCase();
  if (contentType !== "application/json") throw invalidResponse("Content-Type", response.status);

  let source: string;
  try {
    source = await response.text();
  } catch (error: unknown) {
    if (isAbortError(error)) throw error;
    throw invalidResponse("JSON", response.status, error);
  }

  let payload: unknown;
  try {
    payload = strictJson(source);
  } catch (error: unknown) {
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

const requireIdempotencyKey = (value: unknown): string => {
  if (
    typeof value !== "string"
    || value === ""
    || value !== value.trim()
    || textEncoder.encode(value).byteLength > 128
    || controlCharacterPattern.test(value)
  ) {
    throw invalidRequest("idempotencyKey");
  }
  return value;
};

const requireOptionalDisplayName = (value: unknown): string | undefined => {
  if (value === undefined) return undefined;
  if (typeof value !== "string") throw invalidRequest("displayName");
  const parsed = value.trim();
  if (parsed === "") return undefined;
  if (textEncoder.encode(parsed).byteLength > 512 || controlCharacterPattern.test(parsed)) {
    throw invalidRequest("displayName");
  }
  return parsed;
};

const requireText = (value: unknown): string => {
  if (typeof value !== "string" || value.includes("\u0000")) throw invalidRequest("text");
  if (value.trim() === "") throw invalidRequest("text");
  return value;
};

const requireURL = (value: unknown): string => {
  if (typeof value !== "string") throw invalidRequest("url");
  const parsedValue = value.trim();
  if (parsedValue === "" || textEncoder.encode(parsedValue).byteLength > 8192 || controlCharacterPattern.test(parsedValue)) {
    throw invalidRequest("url");
  }
  let parsed: URL;
  try {
    parsed = new URL(parsedValue);
  } catch (error: unknown) {
    throw invalidRequest("url", error);
  }
  if (
    (parsed.protocol !== "http:" && parsed.protocol !== "https:")
    || parsed.hostname === ""
    || parsed.username !== ""
    || parsed.password !== ""
  ) {
    throw invalidRequest("url");
  }
  return parsedValue;
};

const requireFile = (value: unknown): File => {
  if (typeof File === "undefined" || !(value instanceof File)) throw invalidRequest("file");
  if (
    !Number.isSafeInteger(value.size)
    || value.size < 1
    || value.size > maxUploadBytes
    || value.name.trim() === ""
    || textEncoder.encode(value.name).byteLength > 512
    || controlCharacterPattern.test(value.name)
  ) {
    throw invalidRequest("file");
  }
  return value;
};

const requireListParams = (params: CaptureListParams): Required<Pick<CaptureListParams, "limit">> & CaptureListParams => {
  const limit = params.limit ?? defaultListLimit;
  if (!Number.isSafeInteger(limit) || limit < 1 || limit > 100) throw invalidRequest("limit");
  if (params.kind !== undefined && !isCaptureKind(params.kind)) throw invalidRequest("kind");
  if (params.status !== undefined && !isCaptureStatus(params.status)) throw invalidRequest("status");
  if (
    params.cursor !== undefined
    && (
      params.cursor === ""
      || params.cursor !== params.cursor.trim()
      || textEncoder.encode(params.cursor).byteLength > 4096
      || controlCharacterPattern.test(params.cursor)
    )
  ) {
    throw invalidRequest("cursor");
  }
  return { ...params, limit };
};

const signalInit = (signal?: AbortSignal): RequestInit => signal === undefined ? {} : { signal };

const encodeMultipart = (
  kind: "FILE" | "IMAGE",
  file: File,
  displayName: string | undefined,
): FormData => {
  const formData = new FormData();
  formData.set("kind", kind);
  if (displayName !== undefined) formData.set("display_name", displayName);
  formData.set("file", file, file.name);
  return formData;
};

export const createCapture = async (
  input: CaptureCreateInput,
  signal?: AbortSignal,
): Promise<CaptureCommandResult> => {
  const workspaceId = requireUuid(input.workspaceId, "workspaceId");
  const idempotencyKey = requireIdempotencyKey(input.idempotencyKey);
  const displayName = requireOptionalDisplayName(input.displayName);
  const path = `/api/v1/workspaces/${encodeURIComponent(workspaceId)}/${input.kind === "FILE" || input.kind === "IMAGE" ? "capture-files" : "captures"}`;

  if (input.kind === "TEXT") {
    const body = JSON.stringify({
      kind: input.kind,
      ...(displayName === undefined ? {} : { display_name: displayName }),
      text: requireText(input.text),
    });
    if (textEncoder.encode(body).byteLength > maxJSONBodyBytes) throw invalidRequest("text");
    const payload = await requestJSON(path, {
      method: "POST",
      headers: { "Content-Type": "application/json", "Idempotency-Key": idempotencyKey },
      body,
      ...signalInit(signal),
    }, [200, 201]);
    return decodeCaptureCommandResult(payload, { workspaceId, kind: input.kind });
  }

  if (input.kind === "URL") {
    const body = JSON.stringify({
      kind: input.kind,
      ...(displayName === undefined ? {} : { display_name: displayName }),
      url: requireURL(input.url),
    });
    const payload = await requestJSON(path, {
      method: "POST",
      headers: { "Content-Type": "application/json", "Idempotency-Key": idempotencyKey },
      body,
      ...signalInit(signal),
    }, [200, 201]);
    return decodeCaptureCommandResult(payload, { workspaceId, kind: input.kind });
  }

  const file = requireFile(input.file);
  const body = encodeMultipart(input.kind, file, displayName);
  const payload = await requestJSON(path, {
    method: "POST",
    headers: { "Idempotency-Key": idempotencyKey },
    body,
    ...signalInit(signal),
  }, [200, 201]);
  return decodeCaptureCommandResult(payload, { workspaceId, kind: input.kind });
};

export const listCaptures = async (
  workspaceIdValue: string,
  paramsValue: CaptureListParams = {},
  signal?: AbortSignal,
): Promise<CapturePage> => {
  const workspaceId = requireUuid(workspaceIdValue, "workspaceId");
  const params = requireListParams(paramsValue);
  const query = new URLSearchParams();
  if (params.kind !== undefined) query.set("kind", params.kind);
  if (params.status !== undefined) query.set("status", params.status);
  if (params.limit !== defaultListLimit || paramsValue.limit !== undefined) query.set("limit", String(params.limit));
  if (params.cursor !== undefined) query.set("cursor", params.cursor);
  const suffix = query.size === 0 ? "" : `?${query.toString()}`;
  const payload = await requestJSON(
    `/api/v1/workspaces/${encodeURIComponent(workspaceId)}/captures${suffix}`,
    signalInit(signal),
    [200],
  );
  return decodeCaptureList(payload, workspaceId, params);
};

export const getCapture = async (
  workspaceIdValue: string,
  captureIdValue: string,
  signal?: AbortSignal,
): Promise<Capture> => {
  const workspaceId = requireUuid(workspaceIdValue, "workspaceId");
  const captureId = requireUuid(captureIdValue, "captureId");
  const payload = await requestJSON(
    `/api/v1/workspaces/${encodeURIComponent(workspaceId)}/captures/${encodeURIComponent(captureId)}`,
    signalInit(signal),
    [200],
  );
  return decodeCapture(payload, { workspaceId, captureId });
};

export const retryCapture = async (
  input: CaptureRetryInput,
  signal?: AbortSignal,
): Promise<CaptureCommandResult> => {
  const workspaceId = requireUuid(input.workspaceId, "workspaceId");
  const captureId = requireUuid(input.captureId, "captureId");
  if (!Number.isSafeInteger(input.expectedVersion) || input.expectedVersion < 1) {
    throw invalidRequest("expectedVersion");
  }
  const payload = await requestJSON(
    `/api/v1/workspaces/${encodeURIComponent(workspaceId)}/captures/${encodeURIComponent(captureId)}/retry`,
    {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        "Idempotency-Key": requireIdempotencyKey(input.idempotencyKey),
      },
      body: JSON.stringify({ expected_version: input.expectedVersion }),
      ...signalInit(signal),
    },
    [200, 202],
  );
  return decodeCaptureCommandResult(payload, { workspaceId, captureId });
};

const profilePath = (workspaceId: string, sourceVersionId: string): string =>
  `/api/v1/workspaces/${encodeURIComponent(workspaceId)}/source-versions/${encodeURIComponent(sourceVersionId)}/knowledge-profile`;

export const getKnowledgeProfile = async (
  bindingValue: KnowledgeProfileBinding,
  signal?: AbortSignal,
): Promise<DocumentKnowledgeProfile> => {
  const binding: KnowledgeProfileBinding = {
    workspaceId: requireUuid(bindingValue.workspaceId, "workspaceId"),
    sourceVersionId: requireUuid(bindingValue.sourceVersionId, "sourceVersionId"),
    ...(bindingValue.captureId === undefined ? {} : { captureId: requireUuid(bindingValue.captureId, "captureId") }),
  };
  const payload = await requestJSON(
    profilePath(binding.workspaceId, binding.sourceVersionId),
    signalInit(signal),
    [200],
  );
  return decodeKnowledgeProfile(payload, binding);
};

export const retryKnowledgeProfile = async (
  input: KnowledgeProfileRetryInput,
  signal?: AbortSignal,
): Promise<KnowledgeProfileCommandResult> => {
  const binding: KnowledgeProfileBinding = {
    workspaceId: requireUuid(input.workspaceId, "workspaceId"),
    sourceVersionId: requireUuid(input.sourceVersionId, "sourceVersionId"),
    ...(input.captureId === undefined ? {} : { captureId: requireUuid(input.captureId, "captureId") }),
  };
  if (!Number.isSafeInteger(input.expectedVersion) || input.expectedVersion < 1) {
    throw invalidRequest("expectedVersion");
  }
  const payload = await requestJSON(
    `${profilePath(binding.workspaceId, binding.sourceVersionId)}/retry`,
    {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        "Idempotency-Key": requireIdempotencyKey(input.idempotencyKey),
      },
      body: JSON.stringify({ expected_version: input.expectedVersion }),
      ...signalInit(signal),
    },
    [200, 202],
  );
  return decodeKnowledgeProfileCommandResult(payload, binding);
};
