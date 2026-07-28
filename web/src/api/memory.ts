/** Memory 的严格网络边界：身份只由服务端认证上下文决定，浏览器不接受或显示 owner。 */

import { authFetch } from "./auth";

export type MemoryType = "PREFERENCE" | "EPISODIC" | "GOAL" | "FEEDBACK";
export type MemoryStatus = "CANDIDATE" | "ACTIVE" | "PAUSED" | "EXPIRED" | "DELETED";
export type MemorySourceType = "USER" | "AGENT" | "INTERVIEW";
export type MemoryJsonValue = string | number | boolean | null | MemoryJsonValue[] | { [key: string]: MemoryJsonValue };
export interface MemoryJsonObject extends Record<string, MemoryJsonValue> {
  readonly __memoryJsonObjectBrand?: never;
}

export interface MemorySource {
  type: MemorySourceType;
  ref: string;
}

export interface MemoryRecord {
  id: string;
  workspaceId: string;
  type: MemoryType;
  content: MemoryJsonObject;
  source: MemorySource;
  taskScopeId?: string;
  status: MemoryStatus;
  expiresAt?: string;
  confirmedAt?: string;
  version: number;
  createdAt: string;
  updatedAt: string;
}

export interface MemoryPage {
  workspaceId: string;
  items: MemoryRecord[];
  nextCursor?: string;
}

export interface MemoryCommandResult {
  memory: MemoryRecord;
  replayed: boolean;
}

export interface ListMemoriesInput {
  workspaceId: string;
  types?: MemoryType[];
  statuses?: MemoryStatus[];
  cursor?: string;
  limit?: number;
}

export interface CreateMemoryCandidateInput {
  workspaceId: string;
  type: MemoryType;
  content: MemoryJsonObject;
  taskScopeId?: string;
  expiresAt?: string;
  idempotencyKey: string;
}

export interface EditMemoryInput {
  workspaceId: string;
  memoryId: string;
  expectedVersion: number;
  content: MemoryJsonObject;
  taskScopeId?: string;
  expiresAt?: string;
  idempotencyKey: string;
}

export interface TransitionMemoryInput {
  workspaceId: string;
  memoryId: string;
  expectedVersion: number;
  idempotencyKey: string;
}

export class MemoryApiError extends Error {
  readonly code: "INVALID_REQUEST" | "INVALID_RESPONSE" | "HTTP_ERROR" | "NETWORK_ERROR";
  readonly errorCode: string;
  readonly retryable: boolean;
  readonly status: number | null;

  constructor(code: MemoryApiError["code"], errorCode: string, message: string, retryable: boolean, status: number | null = null, options?: ErrorOptions) {
    super(message, options);
    this.name = "MemoryApiError";
    this.code = code;
    this.errorCode = errorCode;
    this.retryable = retryable;
    this.status = status;
  }
}

const uuidPattern = /^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/;
const timestampPattern = /^\d{4}-(?:0[1-9]|1[0-2])-(?:0[1-9]|[12]\d|3[01])T(?:[01]\d|2[0-3]):[0-5]\d:[0-5]\d(?:\.\d{1,9})?(?:Z|[+-](?:[01]\d|2[0-3]):[0-5]\d)$/;
const memoryTypes: readonly MemoryType[] = ["PREFERENCE", "EPISODIC", "GOAL", "FEEDBACK"];
const memoryStatuses: readonly MemoryStatus[] = ["CANDIDATE", "ACTIVE", "PAUSED", "EXPIRED", "DELETED"];
const sourceTypes: readonly MemorySourceType[] = ["USER", "AGENT", "INTERVIEW"];

const isRecord = (value: unknown): value is Record<string, unknown> => typeof value === "object" && value !== null && !Array.isArray(value);
const invalidResponse = (field: string, status: number | null = null): MemoryApiError => new MemoryApiError("INVALID_RESPONSE", "INVALID_RESPONSE", `Memory 响应字段无效：${field}`, false, status);
const invalidRequest = (field: string): MemoryApiError => new MemoryApiError("INVALID_REQUEST", "INVALID_REQUEST", `Memory 请求字段无效：${field}`, false);
const exact = (value: Record<string, unknown>, keys: readonly string[], field: string): void => {
  if (Object.keys(value).some((key) => !keys.includes(key))) throw invalidResponse(field);
};
const stringValue = (value: unknown, field: string, allowEmpty = false): string => {
  if (typeof value !== "string" || (!allowEmpty && value.trim() === "")) throw invalidResponse(field);
  return value;
};
const uuid = (value: unknown, field: string): string => {
  const result = stringValue(value, field);
  if (!uuidPattern.test(result)) throw invalidResponse(field);
  return result;
};
const daysInMonth = (year: number, month: number): number => {
  if (month === 2) {
    const leapYear = year % 4 === 0 && (year % 100 !== 0 || year % 400 === 0);
    return leapYear ? 29 : 28;
  }
  return month === 4 || month === 6 || month === 9 || month === 11 ? 30 : 31;
};
const timestamp = (value: unknown, field: string): string => {
  const result = stringValue(value, field);
  if (!timestampPattern.test(result) || !Number.isFinite(Date.parse(result))) throw invalidResponse(field);
  const calendar = /^(\d{4})-(\d{2})-(\d{2})T/.exec(result);
  if (calendar === null) throw invalidResponse(field);
  const year = Number(calendar[1]);
  const month = Number(calendar[2]);
  const day = Number(calendar[3]);
  if (day > daysInMonth(year, month)) throw invalidResponse(field);
  return result;
};
const optionalTimestamp = (value: unknown, field: string): string | undefined => value === undefined ? undefined : timestamp(value, field);
const positiveInteger = (value: unknown, field: string): number => {
  if (typeof value !== "number" || !Number.isSafeInteger(value) || value < 1) throw invalidResponse(field);
  return value;
};
const bool = (value: unknown, field: string): boolean => {
  if (typeof value !== "boolean") throw invalidResponse(field);
  return value;
};
const enumValue = <T extends string>(value: unknown, allowed: readonly T[], field: string): T => {
  if (typeof value !== "string" || !allowed.includes(value as T)) throw invalidResponse(field);
  return value as T;
};
const bytes = (value: string): number => new TextEncoder().encode(value).length;

const decodeJsonValue = (value: unknown, field: string, depth = 0): MemoryJsonValue => {
  if (value === null || typeof value === "boolean") return value;
  if (typeof value === "string") {
    if (bytes(value) > 4096) throw invalidResponse(field);
    return value;
  }
  if (typeof value === "number") {
    if (!Number.isFinite(value)) throw invalidResponse(field);
    return value;
  }
  if (depth >= 8) throw invalidResponse(field);
  if (Array.isArray(value)) {
    if (value.length > 128) throw invalidResponse(field);
    return value.map((item, index) => decodeJsonValue(item, `${field}[${String(index)}]`, depth + 1));
  }
  if (!isRecord(value) || Object.keys(value).length > 64) throw invalidResponse(field);
  return Object.fromEntries(Object.entries(value).map(([key, item]) => {
    if (key === "" || bytes(key) > 4096) throw invalidResponse(field);
    return [key, decodeJsonValue(item, `${field}.${key}`, depth + 1)];
  }));
};
const decodeJsonObject = (value: unknown, field: string): MemoryJsonObject => {
  const result = decodeJsonValue(value, field);
  if (!isRecord(result) || Object.keys(result).length === 0 || bytes(JSON.stringify(result)) > 16 * 1024) throw invalidResponse(field);
  return result;
};
const requireJsonObject = (value: MemoryJsonObject, field: string): MemoryJsonObject => {
  try {
    return decodeJsonObject(value, field);
  } catch {
    throw invalidRequest(field);
  }
};
const requireUuid = (value: string, field: string): string => {
  if (typeof value !== "string" || !uuidPattern.test(value)) throw invalidRequest(field);
  return value;
};
const requireVersion = (value: number): number => {
  if (!Number.isSafeInteger(value) || value < 1) throw invalidRequest("expectedVersion");
  return value;
};
const requireKey = (value: string): string => {
  if (typeof value !== "string" || value === "" || value !== value.trim() || bytes(value) > 128 || /[\u0000-\u0020\u007f]/.test(value)) throw invalidRequest("idempotencyKey");
  return value;
};
const requireText = (value: string, field: string, maximumBytes: number): string => {
  if (typeof value !== "string" || value === "" || value !== value.trim() || bytes(value) > maximumBytes || value.includes("\0")) throw invalidRequest(field);
  return value;
};
const requireTimestamp = (value: string, field: string): string => {
  try {
    return timestamp(value, field);
  } catch {
    throw invalidRequest(field);
  }
};

class StrictJsonParser {
  private index = 0;

  constructor(private readonly source: string) {}

  parse(): unknown {
    this.skipWhitespace();
    const value = this.parseValue();
    this.skipWhitespace();
    if (this.index !== this.source.length) throw new SyntaxError("unexpected trailing JSON data");
    return value;
  }

  private parseValue(): unknown {
    const current = this.source[this.index];
    if (current === "{") return this.parseObject();
    if (current === "[") return this.parseArray();
    if (current === '"') return this.parseString();
    if (this.source.startsWith("true", this.index)) { this.index += 4; return true; }
    if (this.source.startsWith("false", this.index)) { this.index += 5; return false; }
    if (this.source.startsWith("null", this.index)) { this.index += 4; return null; }
    const match = /^-?(?:0|[1-9]\d*)(?:\.\d+)?(?:[eE][+-]?\d+)?/.exec(this.source.slice(this.index));
    if (match === null) throw new SyntaxError("invalid JSON value");
    this.index += match[0].length;
    const value = Number(match[0]);
    if (!Number.isFinite(value)) throw new SyntaxError("non-finite JSON number");
    return value;
  }

  private parseObject(): Record<string, unknown> {
    this.index += 1;
    this.skipWhitespace();
    const entries: [string, unknown][] = [];
    const keys = new Set<string>();
    if (this.source[this.index] === "}") { this.index += 1; return {}; }
    for (;;) {
      if (this.source[this.index] !== '"') throw new SyntaxError("invalid JSON object key");
      const key = this.parseString();
      if (keys.has(key)) throw new SyntaxError("duplicate JSON key");
      keys.add(key);
      this.skipWhitespace();
      if (this.source[this.index] !== ":") throw new SyntaxError("missing JSON object colon");
      this.index += 1;
      this.skipWhitespace();
      entries.push([key, this.parseValue()]);
      this.skipWhitespace();
      if (this.source[this.index] === "}") { this.index += 1; return Object.fromEntries(entries); }
      if (this.source[this.index] !== ",") throw new SyntaxError("invalid JSON object separator");
      this.index += 1;
      this.skipWhitespace();
    }
  }

  private parseArray(): unknown[] {
    this.index += 1;
    this.skipWhitespace();
    const values: unknown[] = [];
    if (this.source[this.index] === "]") { this.index += 1; return values; }
    for (;;) {
      values.push(this.parseValue());
      this.skipWhitespace();
      if (this.source[this.index] === "]") { this.index += 1; return values; }
      if (this.source[this.index] !== ",") throw new SyntaxError("invalid JSON array separator");
      this.index += 1;
      this.skipWhitespace();
    }
  }

  private parseString(): string {
    const start = this.index;
    this.index += 1;
    while (this.index < this.source.length) {
      if (this.source[this.index] === '"') {
        this.index += 1;
        const parsed: unknown = JSON.parse(this.source.slice(start, this.index));
        if (typeof parsed !== "string") throw new SyntaxError("invalid JSON string");
        return parsed;
      }
      if (this.source[this.index] === "\\") this.index += 1;
      this.index += 1;
    }
    throw new SyntaxError("unterminated JSON string");
  }

  private skipWhitespace(): void {
    while (this.index < this.source.length && " \n\r\t".includes(this.source[this.index] ?? "")) this.index += 1;
  }
}

const parseStrictJson = (source: string): unknown => new StrictJsonParser(source).parse();

/** 将用户输入收敛为可安全发送给 Memory 命令的受限 JSON 对象。 */
export const parseMemoryJsonObject = (source: string): MemoryJsonObject | undefined => {
  try {
    return decodeJsonObject(parseStrictJson(source), "memory.content");
  } catch {
    return undefined;
  }
};

const decodeSource = (value: unknown, field: string): MemorySource => {
  if (!isRecord(value)) throw invalidResponse(field);
  exact(value, ["type", "ref"], field);
  const ref = stringValue(value.ref, `${field}.ref`);
  if (ref !== ref.trim() || bytes(ref) > 512 || ref.includes("\0")) throw invalidResponse(`${field}.ref`);
  return { type: enumValue(value.type, sourceTypes, `${field}.type`), ref };
};

export const decodeMemory = (value: unknown): MemoryRecord => {
  if (!isRecord(value)) throw invalidResponse("memory");
  // Owner 和 confirmer 属于认证身份，任何出现都按响应越界处理。
  exact(value, ["id", "workspace_id", "type", "content", "source", "task_scope_id", "status", "expires_at", "confirmed_at", "version", "created_at", "updated_at"], "memory");
  const type = enumValue(value.type, memoryTypes, "memory.type");
  const expiresAt = optionalTimestamp(value.expires_at, "memory.expires_at");
  const confirmedAt = optionalTimestamp(value.confirmed_at, "memory.confirmed_at");
  const record: MemoryRecord = {
    id: uuid(value.id, "memory.id"),
    workspaceId: uuid(value.workspace_id, "memory.workspace_id"),
    type,
    content: decodeJsonObject(value.content, "memory.content"),
    source: decodeSource(value.source, "memory.source"),
    status: enumValue(value.status, memoryStatuses, "memory.status"),
    version: positiveInteger(value.version, "memory.version"),
    createdAt: timestamp(value.created_at, "memory.created_at"),
    updatedAt: timestamp(value.updated_at, "memory.updated_at"),
    ...(value.task_scope_id === undefined ? {} : { taskScopeId: uuid(value.task_scope_id, "memory.task_scope_id") }),
    ...(expiresAt === undefined ? {} : { expiresAt }),
    ...(confirmedAt === undefined ? {} : { confirmedAt }),
  };
  if ((record.type === "EPISODIC" && record.expiresAt === undefined) || (record.status === "CANDIDATE" && record.confirmedAt !== undefined) || ((record.status === "ACTIVE" || record.status === "PAUSED") && record.confirmedAt === undefined)) throw invalidResponse("memory.lifecycle");
  return record;
};

export const decodeMemoryPage = (value: unknown): MemoryPage => {
  if (!isRecord(value)) throw invalidResponse("memory_page");
  exact(value, ["workspace_id", "items", "next_cursor"], "memory_page");
  if (!Array.isArray(value.items) || value.items.length > 100) throw invalidResponse("memory_page.items");
  const workspaceId = uuid(value.workspace_id, "memory_page.workspace_id");
  const items = value.items.map((item) => decodeMemory(item));
  if (items.some((item) => item.workspaceId !== workspaceId) || new Set(items.map((item) => item.id)).size !== items.length) throw invalidResponse("memory_page.items");
  const nextCursor = value.next_cursor === undefined ? undefined : stringValue(value.next_cursor, "memory_page.next_cursor");
  return { workspaceId, items, ...(nextCursor === undefined ? {} : { nextCursor }) };
};

const decodeCommandResult = (value: unknown): MemoryCommandResult => {
  if (!isRecord(value)) throw invalidResponse("memory_command");
  exact(value, ["memory", "replayed"], "memory_command");
  return { memory: decodeMemory(value.memory), replayed: bool(value.replayed, "memory_command.replayed") };
};

const isAbortError = (value: unknown): boolean => (value instanceof DOMException || value instanceof Error) && value.name === "AbortError";
const readProblem = (value: unknown, status: number): MemoryApiError => {
  if (!isRecord(value)) return new MemoryApiError("HTTP_ERROR", "HTTP_ERROR", `Memory API 返回 HTTP ${String(status)}。`, status >= 500, status);
  exact(value, ["error_code", "message", "retryable", "workflow_run_id", "details"], "problem");
  const errorCode = stringValue(value.error_code, "problem.error_code");
  const message = stringValue(value.message, "problem.message");
  const retryable = bool(value.retryable, "problem.retryable");
  if (value.workflow_run_id !== undefined) uuid(value.workflow_run_id, "problem.workflow_run_id");
  if (value.details !== undefined) decodeJsonObject(value.details, "problem.details");
  return new MemoryApiError("HTTP_ERROR", errorCode, message, retryable, status);
};
const request = async (path: string, init: RequestInit = {}, expectedStatuses?: readonly number[]): Promise<unknown> => {
  const headers = new Headers(init.headers);
  headers.set("Accept", "application/json");
  if (init.body !== undefined) headers.set("Content-Type", "application/json");
  let response: Response;
  try {
    response = await authFetch(path, { ...init, headers });
  } catch (error: unknown) {
    if (isAbortError(error)) throw error;
    throw new MemoryApiError("NETWORK_ERROR", "NETWORK_ERROR", "无法连接 Memory API。", true, null, { cause: error });
  }
  let payload: unknown;
  try {
    payload = parseStrictJson(await response.text());
  } catch (error: unknown) {
    throw new MemoryApiError("INVALID_RESPONSE", "INVALID_RESPONSE", "Memory API 返回了无效或包含重复字段的 JSON。", false, response.status, { cause: error });
  }
  if (!response.ok) throw readProblem(payload, response.status);
  if (expectedStatuses !== undefined && !expectedStatuses.includes(response.status)) throw invalidResponse("http_status", response.status);
  return payload;
};

const memoryPath = (memoryId: string): string => `/api/v1/memories/${encodeURIComponent(requireUuid(memoryId, "memoryId"))}`;
const commandHeaders = (key: string): HeadersInit => ({ "Idempotency-Key": requireKey(key) });
const assertBinding = (memory: MemoryRecord, workspaceId: string, memoryId?: string): MemoryRecord => {
  if (memory.workspaceId !== workspaceId || (memoryId !== undefined && memory.id !== memoryId)) throw invalidResponse("memory.binding");
  return memory;
};
interface MemoryMutablePayload {
  content: MemoryJsonObject;
  task_scope_id?: string;
  expires_at?: string;
}

const mutablePayload = (input: Pick<CreateMemoryCandidateInput, "content" | "taskScopeId" | "expiresAt">): MemoryMutablePayload => {
  const payload: MemoryMutablePayload = { content: requireJsonObject(input.content, "content") };
  if (input.taskScopeId !== undefined) payload.task_scope_id = requireUuid(input.taskScopeId, "taskScopeId");
  if (input.expiresAt !== undefined) payload.expires_at = requireTimestamp(input.expiresAt, "expiresAt");
  return payload;
};

const candidatePayload = (input: CreateMemoryCandidateInput) => {
  const type = enumValue(input.type, memoryTypes, "type");
  if (type === "EPISODIC" && input.expiresAt === undefined) throw invalidRequest("expiresAt");
  return { workspace_id: requireUuid(input.workspaceId, "workspaceId"), type, ...mutablePayload(input) };
};

export const listMemories = async (input: ListMemoriesInput, signal?: AbortSignal): Promise<MemoryPage> => {
  const workspaceId = requireUuid(input.workspaceId, "workspaceId");
  const limit = input.limit ?? 50;
  if (!Number.isSafeInteger(limit) || limit < 1 || limit > 100) throw invalidRequest("limit");
  const query = new URLSearchParams({ workspace_id: workspaceId, limit: String(limit) });
  for (const type of input.types ?? []) query.append("type", enumValue(type, memoryTypes, "types"));
  for (const status of input.statuses ?? []) query.append("status", enumValue(status, memoryStatuses, "statuses"));
  if (new Set(input.types ?? []).size !== (input.types?.length ?? 0) || new Set(input.statuses ?? []).size !== (input.statuses?.length ?? 0)) throw invalidRequest("filters");
  if (input.cursor !== undefined) query.set("cursor", requireText(input.cursor, "cursor", 4096));
  const page = decodeMemoryPage(await request(`/api/v1/memories?${query}`, signal === undefined ? {} : { signal }, [200]));
  if (page.workspaceId !== workspaceId) throw invalidResponse("memory_page.workspace_id");
  return page;
};

export const getMemory = async (workspaceId: string, memoryId: string, signal?: AbortSignal): Promise<MemoryRecord> => {
  const expectedWorkspaceId = requireUuid(workspaceId, "workspaceId");
  const expectedMemoryId = requireUuid(memoryId, "memoryId");
  return assertBinding(decodeMemory(await request(`${memoryPath(expectedMemoryId)}?${new URLSearchParams({ workspace_id: expectedWorkspaceId })}`, signal === undefined ? {} : { signal }, [200])), expectedWorkspaceId, expectedMemoryId);
};

export const createMemoryCandidate = async (input: CreateMemoryCandidateInput, signal?: AbortSignal): Promise<MemoryCommandResult> => {
  const payload = candidatePayload(input);
  const result = decodeCommandResult(await request("/api/v1/memories", { method: "POST", headers: commandHeaders(input.idempotencyKey), body: JSON.stringify(payload), ...(signal === undefined ? {} : { signal }) }, [200, 201]));
  return { ...result, memory: assertBinding(result.memory, payload.workspace_id) };
};

export const editMemory = async (input: EditMemoryInput, signal?: AbortSignal): Promise<MemoryCommandResult> => {
  const payload = {
    workspace_id: requireUuid(input.workspaceId, "workspaceId"),
    expected_version: requireVersion(input.expectedVersion),
    ...mutablePayload(input),
  };
  const memoryId = requireUuid(input.memoryId, "memoryId");
  const result = decodeCommandResult(await request(memoryPath(memoryId), { method: "PUT", headers: commandHeaders(input.idempotencyKey), body: JSON.stringify(payload), ...(signal === undefined ? {} : { signal }) }, [200]));
  return { ...result, memory: assertBinding(result.memory, payload.workspace_id, memoryId) };
};

const transition = async (input: TransitionMemoryInput, action: "confirm" | "pause" | "resume" | "delete", signal?: AbortSignal): Promise<MemoryCommandResult> => {
  const workspaceId = requireUuid(input.workspaceId, "workspaceId");
  const memoryId = requireUuid(input.memoryId, "memoryId");
  const basePath = memoryPath(memoryId);
  const path = action === "delete" ? basePath : `${basePath}/${action}`;
  const result = decodeCommandResult(await request(path, { method: action === "delete" ? "DELETE" : "POST", headers: commandHeaders(input.idempotencyKey), body: JSON.stringify({ workspace_id: workspaceId, expected_version: requireVersion(input.expectedVersion) }), ...(signal === undefined ? {} : { signal }) }, [200]));
  return { ...result, memory: assertBinding(result.memory, workspaceId, memoryId) };
};

export const confirmMemory = (input: TransitionMemoryInput, signal?: AbortSignal): Promise<MemoryCommandResult> => transition(input, "confirm", signal);
export const pauseMemory = (input: TransitionMemoryInput, signal?: AbortSignal): Promise<MemoryCommandResult> => transition(input, "pause", signal);
export const resumeMemory = (input: TransitionMemoryInput, signal?: AbortSignal): Promise<MemoryCommandResult> => transition(input, "resume", signal);
export const deleteMemory = (input: TransitionMemoryInput, signal?: AbortSignal): Promise<MemoryCommandResult> => transition(input, "delete", signal);
