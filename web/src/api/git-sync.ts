import { strictJson } from "./exports";
import { GitSyncApi } from "./generated/apis/GitSyncApi";
import {
  generatedConfiguration,
  generatedRawResponse,
  generatedRequestInit,
} from "./generated-client";
import { canonicalUuidPattern as uuidPattern, hasOnlyKeys, isAbortError } from "../shared/codec";

export type GitSecretAction = { action: "keep" | "clear" } | { action: "replace"; value: string };
export type GitSyncRunStatus = "PENDING" | "FETCHING" | "COMPARING" | "FAST_FORWARDING" | "PUSHING" | "VERIFYING" | "SUCCEEDED" | "CONFLICT" | "FAILED" | "STALE" | "MANUAL_RECOVERY_REQUIRED";
export type GitSyncDirection = "UNKNOWN" | "NONE" | "PULL" | "PUSH";
export type GitSyncFailureClass = "NONE" | "DIRTY" | "DETACHED" | "DIVERGED" | "AUTHENTICATION" | "OFFLINE" | "REF_DRIFT" | "NON_FAST_FORWARD" | "STALE_CONFIG" | "RESULT_UNKNOWN" | "DEPENDENCY" | "INTERNAL";
export type GitSyncIndexStatus = "NOT_REQUIRED" | "PENDING" | "RUNNING" | "SUCCEEDED" | "FAILED";
export type GitFileChangeKind = "ADDED" | "MODIFIED" | "DELETED" | "RENAMED";
export const gitSyncMaxChangedFiles = 500;

export interface GitRemoteConfig {
  workspaceId: string;
  configured: boolean;
  remoteUrl?: string;
  branch?: string;
  autoSync: boolean;
  tokenConfigured: boolean;
  revision: number;
  createdAt?: string;
  updatedAt?: string;
  replayed: boolean;
}

export interface GitFileChange {
  path: string;
  oldPath?: string;
  kind: GitFileChangeKind;
}

export interface GitSyncRun {
  id: string;
  workspaceId: string;
  configRevision: number;
  remoteUrl: string;
  branch: string;
  trigger: "MANUAL" | "AUTOMATIC" | "RETRY";
  retryOfRunId?: string;
  status: GitSyncRunStatus;
  direction: GitSyncDirection;
  failureClass: GitSyncFailureClass;
  errorCode: string;
  retryable: boolean;
  expectedHeadOid?: string;
  expectedRemoteOid?: string;
  verifiedHeadOid?: string;
  verifiedRemoteOid?: string;
  changedFiles: GitFileChange[];
  indexStatus: GitSyncIndexStatus;
  indexErrorCode: string;
  indexRetryable: boolean;
  indexVersionId?: string;
  attemptCount: number;
  version: number;
  createdAt: string;
  updatedAt: string;
  completedAt?: string;
  replayed: boolean;
}

export interface GitSyncStatus {
  config: GitRemoteConfig;
  currentRun?: GitSyncRun;
}

export interface GitSyncRunPage {
  items: GitSyncRun[];
  nextCursor?: string;
}

export interface SaveGitRemoteInput {
  expectedRevision: number;
  remoteUrl: string;
  branch: string;
  autoSync: boolean;
  token: GitSecretAction;
  idempotencyKey: string;
}

export interface TestGitRemoteInput {
  expectedRevision: number;
  remoteUrl: string;
  branch: string;
  token: GitSecretAction;
  idempotencyKey: string;
}

export interface RemoveGitRemoteInput {
  expectedRevision: number;
  idempotencyKey: string;
}

export interface CreateGitSyncRunInput {
  idempotencyKey: string;
}

export interface RetryGitSyncRunInput {
  expectedVersion: number;
  idempotencyKey: string;
}

export class GitSyncApiError extends Error {
  constructor(public readonly code: string, message: string, public readonly status: number, public readonly retryable: boolean) {
    super(message);
    this.name = "GitSyncApiError";
  }
}

const runStatuses = new Set<GitSyncRunStatus>(["PENDING", "FETCHING", "COMPARING", "FAST_FORWARDING", "PUSHING", "VERIFYING", "SUCCEEDED", "CONFLICT", "FAILED", "STALE", "MANUAL_RECOVERY_REQUIRED"]);
const directions = new Set<GitSyncDirection>(["UNKNOWN", "NONE", "PULL", "PUSH"]);
const failures = new Set<GitSyncFailureClass>(["NONE", "DIRTY", "DETACHED", "DIVERGED", "AUTHENTICATION", "OFFLINE", "REF_DRIFT", "NON_FAST_FORWARD", "STALE_CONFIG", "RESULT_UNKNOWN", "DEPENDENCY", "INTERNAL"]);
const indexStatuses = new Set<GitSyncIndexStatus>(["NOT_REQUIRED", "PENDING", "RUNNING", "SUCCEEDED", "FAILED"]);
const changeKinds = new Set<GitFileChangeKind>(["ADDED", "MODIFIED", "DELETED", "RENAMED"]);
const triggers = new Set<GitSyncRun["trigger"]>(["MANUAL", "AUTOMATIC", "RETRY"]);
const activeRunStatuses = new Set<GitSyncRunStatus>(["PENDING", "FETCHING", "COMPARING", "FAST_FORWARDING", "PUSHING", "VERIFYING"]);
const oidPattern = /^(?:[0-9a-f]{40}|[0-9a-f]{64})$/;
const rfc3339Pattern = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})$/;
const errorCodePattern = /^[A-Z][A-Z0-9_]{0,127}$/;
const maxCursorBytes = 2048;
const encoder = new TextEncoder();
const gitSyncApi = new GitSyncApi(generatedConfiguration);

const record = (value: unknown, field: string): Record<string, unknown> => {
  if (typeof value !== "object" || value === null || Array.isArray(value)) throw boundary(field);
  return value as Record<string, unknown>;
};
const exact = (value: Record<string, unknown>, fields: readonly string[], field: string): void => {
  if (!hasOnlyKeys(value, fields)) throw boundary(field);
};
const required = (value: Record<string, unknown>, fields: readonly string[], field: string): void => {
  if (fields.some((key) => !Object.prototype.hasOwnProperty.call(value, key) || value[key] === undefined)) throw boundary(`${field}.required`);
};
const stringValue = (value: unknown, field: string, allowEmpty = false): string => {
  if (typeof value !== "string" || (!allowEmpty && value.length === 0)) throw boundary(field);
  return value;
};
const optionalString = (value: unknown, field: string): string | undefined => value === null || value === undefined ? undefined : stringValue(value, field);
const bool = (value: unknown, field: string): boolean => {
  if (typeof value !== "boolean") throw boundary(field);
  return value;
};
const integer = (value: unknown, field: string): number => {
  if (typeof value !== "number" || !Number.isSafeInteger(value) || value < 0) throw boundary(field);
  return value;
};
const positiveInteger = (value: unknown, field: string): number => {
  const result = integer(value, field);
  if (result < 1) throw boundary(field);
  return result;
};
const uuid = (value: unknown, field: string): string => {
  const result = stringValue(value, field);
  if (!uuidPattern.test(result)) throw boundary(field);
  return result;
};
const optionalUUID = (value: unknown, field: string): string | undefined => value === null || value === undefined ? undefined : uuid(value, field);
const oid = (value: unknown, field: string): string => {
  const result = stringValue(value, field);
  if (!oidPattern.test(result)) throw boundary(field);
  return result;
};
const optionalOID = (value: unknown, field: string): string | undefined => value === null || value === undefined ? undefined : oid(value, field);
const timestamp = (value: unknown, field: string): string => {
  const result = stringValue(value, field);
  if (!rfc3339Pattern.test(result) || Number.isNaN(Date.parse(result))) throw boundary(field);
  const match = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})/.exec(result);
  if (match === null) throw boundary(field);
  const year = Number(match[1]); const month = Number(match[2]); const day = Number(match[3]);
  const hour = Number(match[4]); const minute = Number(match[5]); const second = Number(match[6]);
  if (month < 1 || month > 12 || day < 1 || day > new Date(Date.UTC(year, month, 0)).getUTCDate() || hour > 23 || minute > 59 || second > 59) throw boundary(field);
  return result;
};
const optionalTimestamp = (value: unknown, field: string): string | undefined => value === null || value === undefined ? undefined : timestamp(value, field);
const enumValue = <T extends string>(value: unknown, values: Set<T>, field: string): T => {
  if (typeof value !== "string" || !values.has(value as T)) throw boundary(field);
  return value as T;
};
const boundary = (field: string): GitSyncApiError => new GitSyncApiError("INVALID_RESPONSE", `Git 同步响应字段无效：${field}`, 0, false);
const invalidRequest = (field: string): GitSyncApiError => new GitSyncApiError("INVALID_REQUEST", `Git 同步请求字段无效：${field}`, 0, false);
const errorCode = (value: unknown, field: string): string => {
  const result = stringValue(value, field, true);
  if (result !== "" && !errorCodePattern.test(result)) throw boundary(field);
  return result;
};
const validRemoteURL = (value: unknown, field: string): string => {
  const result = stringValue(value, field);
  let parsed: URL;
  try { parsed = new URL(result); } catch { throw boundary(field); }
  if (result !== result.trim() || parsed.protocol !== "https:" || parsed.username !== "" || parsed.password !== "" || parsed.search !== "" || parsed.hash !== "" || encoder.encode(result).byteLength > 2048) throw boundary(field);
  return result;
};
const validBranch = (value: unknown, field: string): string => {
  const result = stringValue(value, field);
  if (encoder.encode(result).byteLength > 255 || result !== result.trim() || result.startsWith("-") || result.startsWith("/") || result.endsWith("/") || result.endsWith(".") || result.includes("..") || result.includes("@{") || /[ ~^:?*[\\\x00-\x1f\x7f]/.test(result) || result.split("/").some((part) => part === "" || part.startsWith(".") || part.endsWith(".lock"))) throw boundary(field);
  return result;
};
const validRelativePath = (value: unknown, field: string): string => {
  const result = stringValue(value, field);
  if (encoder.encode(result).byteLength > 4096 || result !== result.trim() || result === "." || result.startsWith("/") || result.startsWith("../") || result.includes("\u0000") || result.split("/").some((part) => part === "." || part === ".." || part === "")) throw boundary(field);
  return result;
};

export const decodeGitRemoteConfig = (input: unknown): GitRemoteConfig => {
  const value = record(input, "config");
  exact(value, ["workspace_id", "configured", "remote_url", "branch", "auto_sync", "token_configured", "revision", "created_at", "updated_at", "replayed"], "config");
  required(value, ["workspace_id", "configured", "remote_url", "branch", "auto_sync", "token_configured", "revision", "created_at", "updated_at"], "config");
  const configured = bool(value.configured, "configured");
  const remoteUrl = value.remote_url === null || value.remote_url === undefined ? undefined : validRemoteURL(value.remote_url, "remote_url");
  const branch = value.branch === null || value.branch === undefined ? undefined : validBranch(value.branch, "branch");
  const autoSync = bool(value.auto_sync, "auto_sync");
  const tokenConfigured = bool(value.token_configured, "token_configured");
  const revision = integer(value.revision, "revision");
  const createdAt = optionalTimestamp(value.created_at, "created_at");
  const updatedAt = optionalTimestamp(value.updated_at, "updated_at");
  const timestampsPresent = createdAt !== undefined && updatedAt !== undefined;
  if ((createdAt === undefined) !== (updatedAt === undefined) || revision === 0 && timestampsPresent || revision > 0 && !timestampsPresent) throw boundary("config.timestamps");
  if (timestampsPresent && Date.parse(updatedAt) < Date.parse(createdAt)) throw boundary("updated_at");
  if (configured) {
    if (remoteUrl === undefined || branch === undefined || revision === 0) throw boundary("configured");
  } else if (remoteUrl !== undefined || branch !== undefined || autoSync || tokenConfigured) {
    throw boundary("configured");
  }
  return {
    workspaceId: uuid(value.workspace_id, "workspace_id"), configured,
    ...(remoteUrl === undefined ? {} : { remoteUrl }), ...(branch === undefined ? {} : { branch }),
    autoSync, tokenConfigured, revision,
    ...(createdAt === undefined ? {} : { createdAt }), ...(updatedAt === undefined ? {} : { updatedAt }),
    replayed: value.replayed === undefined ? false : bool(value.replayed, "replayed"),
  };
};

export const decodeGitSyncRun = (input: unknown): GitSyncRun => {
  const value = record(input, "run");
  exact(value, ["id", "workspace_id", "config_revision", "remote_url", "branch", "trigger", "retry_of_run_id", "status", "direction", "failure_class", "error_code", "retryable", "expected_head_oid", "expected_remote_oid", "verified_head_oid", "verified_remote_oid", "changed_files", "index_status", "index_error_code", "index_retryable", "index_version_id", "attempt_count", "version", "created_at", "updated_at", "completed_at", "replayed"], "run");
  required(value, ["id", "workspace_id", "config_revision", "remote_url", "branch", "trigger", "retry_of_run_id", "status", "direction", "failure_class", "error_code", "retryable", "expected_head_oid", "expected_remote_oid", "verified_head_oid", "verified_remote_oid", "changed_files", "index_status", "index_error_code", "index_retryable", "index_version_id", "attempt_count", "version", "created_at", "updated_at", "completed_at"], "run");
  if (!Array.isArray(value.changed_files) || value.changed_files.length > gitSyncMaxChangedFiles) throw boundary("changed_files");
  const changes = value.changed_files.map((item, index): GitFileChange => {
    const change = record(item, `changed_files[${String(index)}]`);
    exact(change, ["path", "old_path", "kind"], `changed_files[${String(index)}]`);
    required(change, ["path", "old_path", "kind"], `changed_files[${String(index)}]`);
    const oldPath = change.old_path === null || change.old_path === undefined ? undefined : validRelativePath(change.old_path, "old_path");
    const kind = enumValue(change.kind, changeKinds, "kind");
    if ((kind === "RENAMED") !== (oldPath !== undefined)) throw boundary("old_path");
    const path = validRelativePath(change.path, "path");
    if (oldPath === path) throw boundary("old_path");
    return { path, ...(oldPath === undefined ? {} : { oldPath }), kind };
  });
  const trigger = enumValue(value.trigger, triggers, "trigger");
  const retryOfRunId = optionalUUID(value.retry_of_run_id, "retry_of_run_id");
  if ((trigger === "RETRY") !== (retryOfRunId !== undefined)) throw boundary("retry_of_run_id");
  const status = enumValue(value.status, runStatuses, "status");
  const direction = enumValue(value.direction, directions, "direction");
  const failureClass = enumValue(value.failure_class, failures, "failure_class");
  const decodedErrorCode = errorCode(value.error_code, "error_code");
  const retryable = bool(value.retryable, "retryable");
  const expectedHeadOid = optionalOID(value.expected_head_oid, "expected_head_oid");
  const expectedRemoteOid = optionalOID(value.expected_remote_oid, "expected_remote_oid");
  const verifiedHeadOid = optionalOID(value.verified_head_oid, "verified_head_oid");
  const verifiedRemoteOid = optionalOID(value.verified_remote_oid, "verified_remote_oid");
  const expectedRefsPresent = expectedHeadOid !== undefined && expectedRemoteOid !== undefined;
  const verifiedRefsPresent = verifiedHeadOid !== undefined && verifiedRemoteOid !== undefined;
  if ((expectedHeadOid === undefined) !== (expectedRemoteOid === undefined) || expectedRefsPresent && expectedHeadOid.length !== expectedRemoteOid.length) throw boundary("expected_refs");
  if ((verifiedHeadOid === undefined) !== (verifiedRemoteOid === undefined) || verifiedRefsPresent && verifiedHeadOid.length !== verifiedRemoteOid.length) throw boundary("verified_refs");
  const indexStatus = enumValue(value.index_status, indexStatuses, "index_status");
  const indexErrorCode = errorCode(value.index_error_code, "index_error_code");
  const indexRetryable = bool(value.index_retryable, "index_retryable");
  const indexVersionId = optionalUUID(value.index_version_id, "index_version_id");
  const attemptCount = integer(value.attempt_count, "attempt_count");
  if (attemptCount > 1000) throw boundary("attempt_count");
  const createdAt = timestamp(value.created_at, "created_at");
  const updatedAt = timestamp(value.updated_at, "updated_at");
  const completedAt = optionalTimestamp(value.completed_at, "completed_at");
  if (Date.parse(updatedAt) < Date.parse(createdAt) || completedAt !== undefined && Date.parse(completedAt) < Date.parse(createdAt)) throw boundary("run.timestamps");

  const active = activeRunStatuses.has(status);
  if (active) {
    if (completedAt !== undefined || failureClass !== "NONE" || decodedErrorCode !== "" || retryable || verifiedRefsPresent) throw boundary("run.active_state");
  } else if (completedAt === undefined) {
    throw boundary("completed_at");
  } else if (status === "SUCCEEDED") {
    if (failureClass !== "NONE" || decodedErrorCode !== "" || retryable || !verifiedRefsPresent || verifiedHeadOid !== verifiedRemoteOid) throw boundary("run.success_state");
  } else if (failureClass === "NONE" || decodedErrorCode === "" || verifiedRefsPresent) {
    throw boundary("run.failure_state");
  }

  if (direction === "UNKNOWN" ? expectedRefsPresent : !expectedRefsPresent) throw boundary("run.direction_refs");
  switch (status) {
    case "PENDING": case "FETCHING": case "COMPARING":
      if (direction !== "UNKNOWN") throw boundary("direction");
      break;
    case "FAST_FORWARDING":
      if (direction !== "PULL") throw boundary("direction");
      break;
    case "PUSHING":
      if (direction !== "PUSH") throw boundary("direction");
      break;
    case "VERIFYING":
      if (direction !== "PULL" && direction !== "PUSH") throw boundary("direction");
      break;
    case "SUCCEEDED":
      if (!expectedRefsPresent || direction === "UNKNOWN") throw boundary("run.success_refs");
      if (direction === "NONE" && (expectedHeadOid !== expectedRemoteOid || verifiedHeadOid !== expectedHeadOid) ||
        direction === "PULL" && (expectedHeadOid === expectedRemoteOid || verifiedHeadOid !== expectedRemoteOid) ||
        direction === "PUSH" && (expectedHeadOid === expectedRemoteOid || verifiedHeadOid !== expectedHeadOid)) throw boundary("run.success_refs");
      break;
  }

  if (status === "SUCCEEDED" && direction === "PULL" ? indexStatus === "NOT_REQUIRED" : indexStatus !== "NOT_REQUIRED") throw boundary("run.index_direction");
  switch (indexStatus) {
    case "NOT_REQUIRED": case "PENDING": case "RUNNING":
      if (indexErrorCode !== "" || indexRetryable || indexVersionId !== undefined) throw boundary("run.index_state");
      break;
    case "SUCCEEDED":
      if (indexErrorCode !== "" || indexRetryable) throw boundary("run.index_state");
      break;
    case "FAILED":
      if (indexErrorCode === "" || indexVersionId !== undefined) throw boundary("run.index_state");
      break;
  }

  return {
    id: uuid(value.id, "id"), workspaceId: uuid(value.workspace_id, "workspace_id"),
    configRevision: positiveInteger(value.config_revision, "config_revision"), remoteUrl: validRemoteURL(value.remote_url, "remote_url"),
    branch: validBranch(value.branch, "branch"), trigger,
    ...(retryOfRunId === undefined ? {} : { retryOfRunId }), status,
    direction, failureClass, errorCode: decodedErrorCode, retryable,
    ...(expectedHeadOid === undefined ? {} : { expectedHeadOid }), ...(expectedRemoteOid === undefined ? {} : { expectedRemoteOid }),
    ...(verifiedHeadOid === undefined ? {} : { verifiedHeadOid }), ...(verifiedRemoteOid === undefined ? {} : { verifiedRemoteOid }),
    changedFiles: changes, indexStatus,
    indexErrorCode, indexRetryable,
    ...(indexVersionId === undefined ? {} : { indexVersionId }), attemptCount,
    version: positiveInteger(value.version, "version"), createdAt,
    updatedAt,
    ...(completedAt === undefined ? {} : { completedAt }),
    replayed: value.replayed === undefined ? false : bool(value.replayed, "replayed"),
  };
};

export const decodeGitSyncStatus = (input: unknown): GitSyncStatus => {
  const value = record(input, "status");
  exact(value, ["config", "current_run"], "status");
  required(value, ["config", "current_run"], "status");
  return { config: decodeGitRemoteConfig(value.config), ...(value.current_run === null ? {} : { currentRun: decodeGitSyncRun(value.current_run) }) };
};

const parseResponse = async (response: Response): Promise<unknown> => {
  let body: unknown;
  try {
    body = strictJson(await response.text());
  } catch (error: unknown) {
    if (isAbortError(error)) throw error;
    throw new GitSyncApiError("INVALID_RESPONSE", "Git 同步 API 返回了无效或包含重复字段的 JSON。", response.status, false);
  }
  if (response.ok) return body;
  try {
    const problem = record(body, "problem");
    exact(problem, ["error_code", "message", "retryable", "workflow_run_id", "details"], "problem");
    const code = stringValue(problem.error_code, "problem.error_code");
    const message = stringValue(problem.message, "problem.message");
    if (!errorCodePattern.test(code) || encoder.encode(message).byteLength > 1024) throw boundary("problem");
    if (problem.workflow_run_id !== undefined) uuid(problem.workflow_run_id, "problem.workflow_run_id");
    if (problem.details !== undefined && (typeof problem.details !== "object" || problem.details === null || Array.isArray(problem.details))) throw boundary("problem.details");
    throw new GitSyncApiError(code, message, response.status, bool(problem.retryable, "problem.retryable"));
  } catch (error: unknown) {
    if (error instanceof GitSyncApiError && error.code !== "INVALID_RESPONSE") throw error;
    throw new GitSyncApiError("INVALID_RESPONSE", "Git 同步 API 返回了无效 Problem。", response.status, false);
  }
};

const request = async (operation: Promise<Response>): Promise<unknown> => {
  try {
    return await parseResponse(await operation);
  } catch (error: unknown) {
    if (error instanceof GitSyncApiError || isAbortError(error)) throw error;
    throw new GitSyncApiError("NETWORK_ERROR", "无法连接 Git 同步服务。", 0, true);
  }
};

const requireWorkspaceID = (value: string): string => { if (!uuidPattern.test(value)) throw invalidRequest("workspaceId"); return value; };
const requireRunID = (value: string): string => { if (!uuidPattern.test(value)) throw invalidRequest("runId"); return value; };
const requireIdempotencyKey = (value: string): string => {
  if (value.trim() === "" || value !== value.trim() || encoder.encode(value).byteLength > 128 || /[\u0000-\u001f\u007f]/.test(value)) throw invalidRequest("idempotencyKey");
  return value;
};
const requireRevision = (value: number, field: string, minimum = 0): void => { if (!Number.isSafeInteger(value) || value < minimum) throw invalidRequest(field); };
type GitTestSecretAction = { action: "keep" } | { action: "replace"; value: string };
const requireTestSecretAction = (value: GitSecretAction): GitTestSecretAction => {
  if (value.action === "clear") throw invalidRequest("token.action");
  if ("value" in value) return { action: "replace", value: value.value };
  return { action: "keep" };
};

export const getGitSyncStatus = async (workspaceIdValue: string, signal?: AbortSignal): Promise<GitSyncStatus> => {
  const workspaceId = requireWorkspaceID(workspaceIdValue);
  const status = decodeGitSyncStatus(await request(generatedRawResponse(gitSyncApi.getGitSyncStatusRaw(
    { workspaceId },
    generatedRequestInit(signal),
  ))));
  if (status.config.workspaceId !== workspaceId || status.currentRun !== undefined && status.currentRun.workspaceId !== workspaceId) throw boundary("status.workspace_binding");
  return status;
};
export const getGitRemoteConfig = async (workspaceIdValue: string, signal?: AbortSignal): Promise<GitRemoteConfig> => {
  const workspaceId = requireWorkspaceID(workspaceIdValue);
  const config = decodeGitRemoteConfig(await request(generatedRawResponse(gitSyncApi.getGitRemoteConfigRaw(
    { workspaceId },
    generatedRequestInit(signal),
  ))));
  if (config.workspaceId !== workspaceId) throw boundary("config.workspace_binding"); return config;
};
export const saveGitRemoteConfig = async (workspaceIdValue: string, input: SaveGitRemoteInput, signal?: AbortSignal): Promise<GitRemoteConfig> => {
  const workspaceId = requireWorkspaceID(workspaceIdValue); requireRevision(input.expectedRevision, "expectedRevision");
  const config = decodeGitRemoteConfig(await request(generatedRawResponse(gitSyncApi.saveGitRemoteConfigRaw({
    idempotencyKey: requireIdempotencyKey(input.idempotencyKey),
    workspaceId,
    saveGitRemoteConfigRequest: {
      expected_revision: input.expectedRevision,
      remote_url: input.remoteUrl,
      branch: input.branch,
      auto_sync: input.autoSync,
      token: input.token,
    },
  }, generatedRequestInit(signal)))));
  if (config.workspaceId !== workspaceId) throw boundary("config.workspace_binding"); return config;
};
export const removeGitRemoteConfig = async (workspaceIdValue: string, input: RemoveGitRemoteInput, signal?: AbortSignal): Promise<GitRemoteConfig> => {
  const workspaceId = requireWorkspaceID(workspaceIdValue); requireRevision(input.expectedRevision, "expectedRevision");
  const config = decodeGitRemoteConfig(await request(generatedRawResponse(gitSyncApi.removeGitRemoteConfigRaw({
    idempotencyKey: requireIdempotencyKey(input.idempotencyKey),
    workspaceId,
    removeGitRemoteConfigRequest: { expected_revision: input.expectedRevision },
  }, generatedRequestInit(signal)))));
  if (config.workspaceId !== workspaceId) throw boundary("config.workspace_binding"); return config;
};
export const testGitRemoteConfig = async (workspaceIdValue: string, input: TestGitRemoteInput, signal?: AbortSignal): Promise<{ remoteUrl: string; branch: string }> => {
  const workspaceId = requireWorkspaceID(workspaceIdValue); requireRevision(input.expectedRevision, "expectedRevision");
  const value = record(await request(generatedRawResponse(gitSyncApi.testGitRemoteConfigRaw({
    workspaceId,
    testGitRemoteConfigRequest: {
      expected_revision: input.expectedRevision,
      remote_url: input.remoteUrl,
      branch: input.branch,
      token: requireTestSecretAction(input.token),
    },
  }, generatedRequestInit(signal, {
    headers: {
      "Content-Type": "application/json",
      "Idempotency-Key": requireIdempotencyKey(input.idempotencyKey),
    },
  })))), "test");
  exact(value, ["status", "remote_url", "branch"], "test");
  if (value.status !== "ok") throw boundary("status");
  return { remoteUrl: validRemoteURL(value.remote_url, "remote_url"), branch: validBranch(value.branch, "branch") };
};
export const createGitSyncRun = async (workspaceIdValue: string, input: CreateGitSyncRunInput, signal?: AbortSignal): Promise<GitSyncRun> => {
  const workspaceId = requireWorkspaceID(workspaceIdValue);
  const run = decodeGitSyncRun(await request(generatedRawResponse(gitSyncApi.createGitSyncRunRaw({
    idempotencyKey: requireIdempotencyKey(input.idempotencyKey),
    workspaceId,
  }, generatedRequestInit(signal)))));
  if (run.workspaceId !== workspaceId) throw boundary("run.workspace_binding"); return run;
};
export const retryGitSyncRun = async (workspaceIdValue: string, runIdValue: string, input: RetryGitSyncRunInput, signal?: AbortSignal): Promise<GitSyncRun> => {
  const workspaceId = requireWorkspaceID(workspaceIdValue); const runId = requireRunID(runIdValue); requireRevision(input.expectedVersion, "expectedVersion", 1);
  const run = decodeGitSyncRun(await request(generatedRawResponse(gitSyncApi.retryGitSyncRunRaw({
    idempotencyKey: requireIdempotencyKey(input.idempotencyKey),
    workspaceId,
    runId,
    retryGitSyncRunRequest: { expected_version: input.expectedVersion },
  }, generatedRequestInit(signal)))));
  if (run.workspaceId !== workspaceId) throw boundary("run.workspace_binding"); return run;
};
export const listGitSyncRuns = async (workspaceIdValue: string, cursor?: string, limit = 10, signal?: AbortSignal): Promise<GitSyncRunPage> => {
  const workspaceId = requireWorkspaceID(workspaceIdValue);
  if (cursor !== undefined && (cursor.trim() === "" || encoder.encode(cursor).byteLength > maxCursorBytes) || !Number.isSafeInteger(limit) || limit < 1 || limit > 100) throw invalidRequest("list");
  const value = record(await request(generatedRawResponse(gitSyncApi.listGitSyncRunsRaw({
    workspaceId,
    limit,
    ...(cursor === undefined ? {} : { cursor }),
  }, generatedRequestInit(signal)))), "runs");
  exact(value, ["items", "next_cursor"], "runs");
  if (!Array.isArray(value.items) || value.items.length > 100) throw boundary("items");
  const nextCursor = optionalString(value.next_cursor, "next_cursor");
  if (nextCursor !== undefined && (encoder.encode(nextCursor).byteLength > maxCursorBytes || nextCursor.trim() !== nextCursor || value.items.length === 0)) throw boundary("next_cursor");
  const items = value.items.map(decodeGitSyncRun);
  if (items.some((item) => item.workspaceId !== workspaceId)) throw boundary("runs.workspace_binding");
  return { items, ...(nextCursor === undefined ? {} : { nextCursor }) };
};
