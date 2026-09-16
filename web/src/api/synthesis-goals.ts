/** 主笔记目标的严格 HTTP 边界。目标范围由服务端 AI 从资料中判定。 */
import { canonicalUuidPattern, hasExactKeys, isAbortError, isRecord } from "../shared/codec";
import { decodeProblem } from "./conversation";
import { strictJson } from "./exports";
import { SynthesisApi } from "./generated/apis/SynthesisApi";
import { generatedConfiguration, generatedRawResponse, generatedRequestInit } from "./generated-client";
import { decodeSynthesisProcessing, type SynthesisProcessing } from "./synthesis";

export type SynthesisGoalRequestStatus = "DISCOVERING" | "CATALOG_READY";

export interface SynthesisGoalRequest {
  id: string;
  workspaceId: string;
  goal: string;
  status: SynthesisGoalRequestStatus;
  errorCode: string | null;
  version: number;
  createdAt: string;
  updatedAt: string;
}

export interface SynthesisGoalProgress {
  catalogBatches: number;
  preparedBatches: number;
  selections: number;
  pending: number;
  running: number;
  succeeded: number;
  failed: number;
  recoveryRequired: number;
  selectedPoints: number;
  ready: boolean;
  preparationFailures: number;
  preparationErrorCode: string | null;
}

export interface SynthesisGoalCandidate { noteId: string; revisionId: string }
export type SynthesisGoalSelectionStatus = "PENDING" | "RUNNING" | "SUCCEEDED" | "FAILED" | "RECOVERY_REQUIRED";
export interface SynthesisGoalSelection {
  id: string;
  workspaceId: string;
  requestId: string;
  status: SynthesisGoalSelectionStatus;
  errorCode: string | null;
  retryable: boolean;
  version: number;
  createdAt: string;
  updatedAt: string;
}
export interface SynthesisGoalSelectionPage { workspaceId: string; requestId: string; items: SynthesisGoalSelection[]; nextAfterId: string | null }
export interface SynthesisGoalView {
  request: SynthesisGoalRequest;
  progress: SynthesisGoalProgress;
  processing: SynthesisProcessing | null;
  candidate: SynthesisGoalCandidate | null;
}
export interface SynthesisGoalPage { workspaceId: string; items: SynthesisGoalView[]; nextCursor: string | null }
export interface CreateSynthesisGoalInput { workspaceId: string; goal: string; idempotencyKey: string; signal?: AbortSignal }
export interface RetrySynthesisGoalSelectionInput { workspaceId: string; goalId: string; selectionId: string; expectedVersion: number; idempotencyKey: string; signal?: AbortSignal }

export class SynthesisGoalApiError extends Error {
  constructor(readonly code: "INVALID_REQUEST" | "INVALID_RESPONSE" | "HTTP_ERROR" | "NETWORK_ERROR", message: string,
    readonly errorCode: string = code, readonly status: number | null = null, readonly retryable = false) {
    super(message); this.name = "SynthesisGoalApiError";
  }
}

const invalid = (): never => { throw new SynthesisGoalApiError("INVALID_RESPONSE", "主笔记目标响应不符合契约，请刷新后重试。"); };
const object = (value: unknown, keys: readonly string[]): Record<string, unknown> => {
  if (!isRecord(value) || !hasExactKeys(value, keys)) return invalid();
  return value;
};
const uuid = (value: unknown): string => typeof value === "string" && canonicalUuidPattern.test(value) ? value : invalid();
const integer = (value: unknown, min = 0): number => typeof value === "number" && Number.isSafeInteger(value) && value >= min ? value : invalid();
const boolean = (value: unknown): boolean => typeof value === "boolean" ? value : invalid();
const choice = <T extends string>(value: unknown, choices: readonly T[]): T => choices.includes(value as T) ? value as T : invalid();
const timestamp = (value: unknown): string => {
  if (typeof value !== "string" || !/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{1,9})?Z$/.test(value) || !Number.isFinite(Date.parse(value))) return invalid();
  if (new Date(value).toISOString().slice(0, 10) !== value.slice(0, 10)) return invalid();
  return value;
};
const nullable = <T>(value: unknown, decode: (value: unknown) => T): T | null => value === null ? null : decode(value);
const goalText = (value: unknown, response = true): string => {
  if (typeof value !== "string" || value === "" || value !== value.trim() || new TextEncoder().encode(value).length > 2048 || /[\u0000-\u001f\u007f]/.test(value)) {
    if (response) return invalid();
    throw new SynthesisGoalApiError("INVALID_REQUEST", "请填写不含换行的整理目标，最多 2048 个字节。");
  }
  return value;
};
const inputId = (value: string): string => {
  if (!canonicalUuidPattern.test(value)) throw new SynthesisGoalApiError("INVALID_REQUEST", "工作区身份无效。");
  return value;
};
const inputKey = (value: string): string => {
  if (value === "" || value !== value.trim() || new TextEncoder().encode(value).length > 128 || /[\u0000-\u001f\u007f]/.test(value)) throw new SynthesisGoalApiError("INVALID_REQUEST", "操作身份无效。");
  return value;
};
const cursor = (value: string | null): string | undefined => {
  if (value === null) return undefined;
  if (value === "" || value !== value.trim() || new TextEncoder().encode(value).length > 2048) throw new SynthesisGoalApiError("INVALID_REQUEST", "分页凭据无效。");
  return value;
};

const decodeGoalRequest = (value: unknown, workspaceId: string): SynthesisGoalRequest => {
  const v = object(value, ["id", "workspace_id", "goal", "status", "error_code", "version", "created_at", "updated_at"]);
  if (uuid(v.workspace_id) !== workspaceId) return invalid();
  const result = {
    id: uuid(v.id), workspaceId, goal: goalText(v.goal), status: choice(v.status, ["DISCOVERING", "CATALOG_READY"]),
    errorCode: nullable(v.error_code, (code) => {
      if (typeof code !== "string" || !/^[A-Z][A-Z0-9_]{0,127}$/.test(code)) return invalid();
      return code;
    }),
    version: integer(v.version, 1), createdAt: timestamp(v.created_at), updatedAt: timestamp(v.updated_at),
  };
  if (Date.parse(result.updatedAt) < Date.parse(result.createdAt)) return invalid();
  return result;
};

const decodeGoalProgress = (value: unknown, request: SynthesisGoalRequest): SynthesisGoalProgress => {
  const v = object(value, ["catalog_batches", "prepared_batches", "selections", "pending", "running", "succeeded", "failed", "recovery_required", "selected_points", "ready", "preparation_failures", "preparation_error_code"]);
  const result = {
    catalogBatches: integer(v.catalog_batches), preparedBatches: integer(v.prepared_batches), selections: integer(v.selections), pending: integer(v.pending), running: integer(v.running),
    succeeded: integer(v.succeeded), failed: integer(v.failed), recoveryRequired: integer(v.recovery_required), selectedPoints: integer(v.selected_points), ready: boolean(v.ready), preparationFailures: integer(v.preparation_failures),
    preparationErrorCode: nullable(v.preparation_error_code, (code) => typeof code === "string" && /^[A-Z][A-Z0-9_]{0,127}$/.test(code) ? code : invalid()),
  };
  const shouldBeReady = request.status === "CATALOG_READY" && result.catalogBatches === result.preparedBatches && result.selections === result.succeeded && result.pending === 0 && result.running === 0 && result.failed === 0 && result.recoveryRequired === 0;
  if ((result.preparationFailures === 0) !== (result.preparationErrorCode === null) || result.preparationFailures > result.catalogBatches - result.preparedBatches || result.preparedBatches > result.catalogBatches || result.selections !== result.pending + result.running + result.succeeded + result.failed + result.recoveryRequired || result.ready !== shouldBeReady) return invalid();
  return result;
};

export const decodeSynthesisGoalView = (value: unknown, workspaceId: string): SynthesisGoalView => {
  const v = object(value, ["request", "progress", "processing", "candidate"]);
  const request = decodeGoalRequest(v.request, workspaceId), progress = decodeGoalProgress(v.progress, request);
  const processing = nullable(v.processing, (entry) => decodeSynthesisProcessing(entry, workspaceId));
  const candidate = nullable(v.candidate, (entry) => {
    const c = object(entry, ["note_id", "revision_id"]);
    return { noteId: uuid(c.note_id), revisionId: uuid(c.revision_id) };
  });
  if (processing !== null && (processing.status === "NO_CHANGE" || processing.status === "SKIPPED" || !progress.ready || progress.selectedPoints === 0 || (processing.status === "SUCCEEDED") !== (candidate !== null))) return invalid();
  if (candidate !== null && (processing?.status !== "SUCCEEDED" || processing.revisionIds.length !== 1 || processing.revisionIds[0] !== candidate.revisionId)) return invalid();
  return { request, progress, processing, candidate };
};

export const decodeSynthesisGoalPage = (value: unknown, workspaceId: string, limit = 20): SynthesisGoalPage => {
  const v = object(value, ["workspace_id", "items", "next_cursor"]);
  if (uuid(v.workspace_id) !== workspaceId || !Array.isArray(v.items) || v.items.length > limit) return invalid();
  const items = v.items.map((item) => decodeSynthesisGoalView(item, workspaceId));
  if (new Set(items.map((item) => item.request.id)).size !== items.length) return invalid();
  const nextCursor = nullable(v.next_cursor, (entry) => {
    if (typeof entry !== "string" || entry === "" || entry !== entry.trim() || new TextEncoder().encode(entry).length > 2048) return invalid();
    return entry;
  });
  if (nextCursor !== null && items.length !== limit) return invalid();
  return { workspaceId, items, nextCursor };
};

export const decodeSynthesisGoalSelection = (value: unknown, workspaceId: string, goalId: string): SynthesisGoalSelection => {
  const v = object(value, ["id", "workspace_id", "request_id", "status", "error_code", "retryable", "version", "created_at", "updated_at"]);
  if (uuid(v.workspace_id) !== workspaceId || uuid(v.request_id) !== goalId) return invalid();
  const result = {
    id: uuid(v.id), workspaceId, requestId: goalId, status: choice(v.status, ["PENDING", "RUNNING", "SUCCEEDED", "FAILED", "RECOVERY_REQUIRED"]),
    errorCode: nullable(v.error_code, (code) => {
      if (typeof code !== "string" || !/^[A-Z][A-Z0-9_]{0,127}$/.test(code)) return invalid();
      return code;
    }),
    retryable: boolean(v.retryable), version: integer(v.version, 1), createdAt: timestamp(v.created_at), updatedAt: timestamp(v.updated_at),
  };
  const completed = result.status === "FAILED" || result.status === "RECOVERY_REQUIRED";
  if ((result.errorCode !== null) !== completed || ["PENDING", "RUNNING", "SUCCEEDED"].includes(result.status) && result.retryable || result.status === "RECOVERY_REQUIRED" && result.retryable || Date.parse(result.updatedAt) < Date.parse(result.createdAt)) return invalid();
  return result;
};

export const decodeSynthesisGoalSelectionPage = (value: unknown, workspaceId: string, goalId: string, afterId: string | null, limit = 20): SynthesisGoalSelectionPage => {
  const v = object(value, ["workspace_id", "request_id", "items", "next_after_id"]);
  if (uuid(v.workspace_id) !== workspaceId || uuid(v.request_id) !== goalId || !Array.isArray(v.items) || v.items.length > limit) return invalid();
  const items = v.items.map((item) => decodeSynthesisGoalSelection(item, workspaceId, goalId));
  let previous = afterId ?? "";
  for (const item of items) { if (item.id <= previous) return invalid(); previous = item.id; }
  const nextAfterId = nullable(v.next_after_id, uuid);
  if (nextAfterId !== null && (items.length !== limit || nextAfterId !== previous)) return invalid();
  return { workspaceId, requestId: goalId, items, nextAfterId };
};

const api = new SynthesisApi(generatedConfiguration);
const request = async <T>(operation: () => Promise<Response>, decode: (value: unknown) => T, status = 200): Promise<T> => {
  let response: Response;
  try { response = await operation(); } catch (error: unknown) {
    if (isAbortError(error) || error instanceof SynthesisGoalApiError) throw error;
    throw new SynthesisGoalApiError("NETWORK_ERROR", "无法连接主笔记目标服务，请保留当前操作并重试。", "NETWORK_ERROR", null, true);
  }
  if (response.headers.get("Content-Type")?.split(";", 1)[0]?.trim().toLowerCase() !== "application/json") return invalid();
  let value: unknown;
  try {
    const body = await response.text();
    if (new TextEncoder().encode(body).length > 8 * 1024 * 1024) return invalid();
    value = strictJson(body);
  } catch (error: unknown) { if (isAbortError(error)) throw error; return invalid(); }
  if (!response.ok) {
    const problem = (() => { try { return decodeProblem(value); } catch { return invalid(); } })();
    throw new SynthesisGoalApiError("HTTP_ERROR", problem.message, problem.errorCode, response.status, problem.retryable);
  }
  if (response.status !== status) return invalid();
  return decode(value);
};

export const createSynthesisGoal = (input: CreateSynthesisGoalInput): Promise<SynthesisGoalRequest> => {
  const goal = goalText(input.goal, false), workspaceId = inputId(input.workspaceId);
  return request(() => generatedRawResponse(api.createSynthesisGoalRaw({ workspaceId, idempotencyKey: inputKey(input.idempotencyKey), createSynthesisGoal: { goal } }, generatedRequestInit(input.signal))), (value) => {
    const v = object(value, ["request", "replayed"]), result = decodeGoalRequest(v.request, workspaceId);
    if (typeof v.replayed !== "boolean" || result.goal !== goal) return invalid();
    return result;
  }, 202);
};
export const listSynthesisGoals = (workspaceId: string, after: string | null = null, signal?: AbortSignal): Promise<SynthesisGoalPage> => {
  const validWorkspace = inputId(workspaceId), next = cursor(after);
  return request(() => generatedRawResponse(api.listSynthesisGoalsRaw({ workspaceId: validWorkspace, limit: 20, ...(next === undefined ? {} : { cursor: next }) }, generatedRequestInit(signal))), (value) => decodeSynthesisGoalPage(value, validWorkspace));
};
export const getSynthesisGoal = (workspaceId: string, goalId: string, signal?: AbortSignal): Promise<SynthesisGoalView> => {
  const validWorkspace = inputId(workspaceId), validGoal = inputId(goalId);
  return request(() => generatedRawResponse(api.getSynthesisGoalRaw({ workspaceId: validWorkspace, goalId: validGoal }, generatedRequestInit(signal))), (value) => {
    const result = decodeSynthesisGoalView(value, validWorkspace); if (result.request.id !== validGoal) return invalid(); return result;
  });
};
export const listSynthesisGoalSelections = (workspaceId: string, goalId: string, afterId: string | null = null, signal?: AbortSignal): Promise<SynthesisGoalSelectionPage> => {
  const validWorkspace = inputId(workspaceId), validGoal = inputId(goalId);
  if (afterId !== null) inputId(afterId);
  return request(() => generatedRawResponse(api.listSynthesisGoalSelectionsRaw({ workspaceId: validWorkspace, goalId: validGoal, limit: 20, ...(afterId === null ? {} : { afterId }) }, generatedRequestInit(signal))), (value) => decodeSynthesisGoalSelectionPage(value, validWorkspace, validGoal, afterId));
};
export const retrySynthesisGoalSelection = (input: RetrySynthesisGoalSelectionInput): Promise<SynthesisGoalSelection> => {
  if (!Number.isSafeInteger(input.expectedVersion) || input.expectedVersion < 1) throw new SynthesisGoalApiError("INVALID_REQUEST", "筛选版本无效。");
  const workspaceId = inputId(input.workspaceId), goalId = inputId(input.goalId), selectionId = inputId(input.selectionId);
  return request(() => generatedRawResponse(api.retrySynthesisGoalSelectionRaw({ workspaceId, goalId, selectionId, idempotencyKey: inputKey(input.idempotencyKey), synthesisRetryRequest: { expected_version: input.expectedVersion } }, generatedRequestInit(input.signal))), (value) => {
    const result = decodeSynthesisGoalSelection(value, workspaceId, goalId);
    if (result.id !== selectionId || result.version <= input.expectedVersion) return invalid();
    return result;
  }, 202);
};

export interface PromoteSynthesisSourceInput { workspaceId: string; sourceVersionId: string; idempotencyKey: string; signal?: AbortSignal }
export const promoteSynthesisSource = (input: PromoteSynthesisSourceInput): Promise<SynthesisGoalRequest> => {
  const workspaceId = inputId(input.workspaceId), sourceVersionId = inputId(input.sourceVersionId);
  return request(() => generatedRawResponse(api.promoteSynthesisSourceRaw({ workspaceId, sourceVersionId, idempotencyKey: inputKey(input.idempotencyKey), requestBody: {} }, generatedRequestInit(input.signal))), (value) => {
    const v = object(value, ["source_version_id", "request", "replayed"]);
    const result = decodeGoalRequest(v.request, workspaceId);
    if (uuid(v.source_version_id) !== sourceVersionId || typeof v.replayed !== "boolean" || result.status !== "CATALOG_READY") return invalid();
    return result;
  }, 202);
};
