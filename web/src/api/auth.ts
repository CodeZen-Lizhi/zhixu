import { notifyRuntimeAccessInvalidation } from "../shared/runtime-access-invalidation";

export type AuthCapability =
  | "READ_LOCAL"
  | "READ_EXTERNAL"
  | "WRITE_PROPOSAL"
  | "WRITE_KNOWLEDGE"
  | "GIT_WRITE"
  | "INDEX_MAINTENANCE"
  | "EVALUATION_RUN"
  | "MANAGE_SYSTEM_SETTINGS";

export interface SessionCredential {
  sessionId: string;
  csrfToken: string;
  expiresAt: string;
}

export interface SessionInfo {
  id: string;
  userLabel: string;
  scopes: AuthCapability[];
  createdAt: string;
  lastSeenAt: string;
  expiresAt: string;
  revokedAt?: string;
}

export interface CreateApiTokenInput {
  name: string;
  scopes: AuthCapability[];
  expiresInSeconds?: number;
}

export interface ApiTokenInfo {
  id: string;
  name: string;
  scopes: AuthCapability[];
  createdAt: string;
  lastUsedAt?: string;
  expiresAt: string;
  revokedAt?: string;
}

export interface ApiTokenCredential {
  id: string;
  name: string;
  scopes: AuthCapability[];
  expiresAt: string;
  token: string;
}

export interface ApiTokenPage {
  items: ApiTokenInfo[];
  nextCursor?: string;
}

export class AuthApiError extends Error {
  readonly code: string;
  readonly status: number | null;
  readonly retryable: boolean;

  constructor(code: string, message: string, status: number | null, retryable: boolean, options?: ErrorOptions) {
    super(message, options);
    this.name = "AuthApiError";
    this.code = code;
    this.status = status;
    this.retryable = retryable;
  }
}

const apiBaseUrl = (import.meta.env.VITE_API_BASE_URL ?? "").replace(/\/$/, "");
const csrfStorageKey = "zhixu.csrf-token";
const uuidPattern = /^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/;
const rfc3339Pattern = /^\d{4}-(?:0[1-9]|1[0-2])-(?:0[1-9]|[12]\d|3[01])T(?:[01]\d|2[0-3]):[0-5]\d:[0-5]\d(?:\.\d{1,9})?(?:Z|[+-](?:[01]\d|2[0-3]):[0-5]\d)$/;
const capabilityValues: readonly AuthCapability[] = [
  "READ_LOCAL", "READ_EXTERNAL", "WRITE_PROPOSAL", "WRITE_KNOWLEDGE", "GIT_WRITE", "INDEX_MAINTENANCE", "EVALUATION_RUN", "MANAGE_SYSTEM_SETTINGS",
];
export type AuthInvalidationReason =
  | { kind: "unauthorized" }
  | { kind: "storage_unavailable"; error: AuthApiError };

const authInvalidationListeners = new Set<(reason: AuthInvalidationReason) => void>();

/** 订阅服务端返回 401 后的全局认证失效通知。 */
export const subscribeAuthInvalidation = (listener: (reason: AuthInvalidationReason) => void): (() => void) => {
  authInvalidationListeners.add(listener);
  return () => authInvalidationListeners.delete(listener);
};

const notifyAuthInvalidation = (reason: AuthInvalidationReason): void => {
  for (const listener of [...authInvalidationListeners]) listener(reason);
};

const isRecord = (value: unknown): value is Record<string, unknown> => typeof value === "object" && value !== null && !Array.isArray(value);
const isAbortError = (value: unknown): boolean => (value instanceof DOMException || value instanceof Error) && value.name === "AbortError";

const storageFailure = (operation: "read" | "write" | "remove", cause: unknown): never => {
  const message = operation === "read"
    ? "浏览器无法读取 Session 恢复状态，请允许本站存储后重新登录。"
    : operation === "write"
      ? "浏览器无法保存 Session 恢复状态，请允许本站存储后重新登录。"
      : "浏览器无法清理 Session 恢复状态，请允许本站存储后重新登录。";
  const error = new AuthApiError("AUTH_STORAGE_UNAVAILABLE", message, null, false, { cause });
  notifyAuthInvalidation({ kind: "storage_unavailable", error });
  throw error;
};

const readStorage = (operation: "read" | "write" | "remove"): Storage | undefined => {
  if (typeof window === "undefined") return undefined;
  try {
    return window.localStorage;
  } catch (error: unknown) {
    return storageFailure(operation, error);
  }
};

export const getCsrfToken = (): string | undefined => {
  let value: string | null | undefined;
  try {
    value = readStorage("read")?.getItem(csrfStorageKey);
  } catch (error: unknown) {
    if (error instanceof AuthApiError && error.code === "AUTH_STORAGE_UNAVAILABLE") throw error;
    return storageFailure("read", error);
  }
  return value === undefined || value === null || value === "" ? undefined : value;
};

export const setCsrfToken = (value: string): void => {
  try {
    readStorage("write")?.setItem(csrfStorageKey, value);
  } catch (error: unknown) {
    if (error instanceof AuthApiError && error.code === "AUTH_STORAGE_UNAVAILABLE") throw error;
    storageFailure("write", error);
  }
};

export const clearCsrfToken = (): void => {
  try {
    readStorage("remove")?.removeItem(csrfStorageKey);
  } catch (error: unknown) {
    if (error instanceof AuthApiError && error.code === "AUTH_STORAGE_UNAVAILABLE") throw error;
    storageFailure("remove", error);
  }
};

/** 将 REST 与 SSE 的 401 收敛到同一个本地认证失效入口。 */
export const invalidateAuthSession = (): void => {
  clearCsrfToken();
  notifyAuthInvalidation({ kind: "unauthorized" });
};

/** 订阅其他同源 Tab 的 CSRF 恢复状态变化。 */
export const subscribeCsrfTokenChanges = (listener: () => void): (() => void) => {
  if (typeof window === "undefined") return () => undefined;
  const handleStorage = (event: StorageEvent): void => {
    if (event.key !== csrfStorageKey) return;
    try {
      const storage = readStorage("read");
      if (event.storageArea !== null && event.storageArea !== storage) return;
    } catch {
      return;
    }
    listener();
  };
  window.addEventListener("storage", handleStorage);
  return () => window.removeEventListener("storage", handleStorage);
};

const stringValue = (record: Record<string, unknown>, field: string): string => {
  const value = record[field];
  if (typeof value !== "string" || value.trim() === "") throw new AuthApiError("INVALID_RESPONSE", `认证响应字段无效：${field}`, null, false);
  return value;
};

const optionalString = (record: Record<string, unknown>, field: string): string | undefined => {
  const value = record[field];
  if (value === undefined) return undefined;
  return stringValue(record, field);
};

const assertExactKeys = (record: Record<string, unknown>, allowed: readonly string[], field: string): void => {
  if (Object.keys(record).some((key) => !allowed.includes(key))) {
    throw new AuthApiError("INVALID_RESPONSE", `认证响应包含未知字段：${field}`, null, false);
  }
};

const uuidValue = (record: Record<string, unknown>, field: string, status: number | null = null): string => {
  const value = record[field];
  if (typeof value !== "string" || value.trim() === "" || !uuidPattern.test(value)) {
    throw new AuthApiError("INVALID_RESPONSE", `认证响应 UUID 无效：${field}`, status, false);
  }
  return value;
};

const dateTimeValue = (record: Record<string, unknown>, field: string): string => {
  const value = stringValue(record, field);
  const year = Number(value.slice(0, 4));
  const month = Number(value.slice(5, 7));
  const day = Number(value.slice(8, 10));
  const daysInMonth = month === 2
    ? (year % 4 === 0 && (year % 100 !== 0 || year % 400 === 0) ? 29 : 28)
    : month === 4 || month === 6 || month === 9 || month === 11 ? 30 : 31;
  if (!rfc3339Pattern.test(value) || year < 1 || day > daysInMonth || !Number.isFinite(Date.parse(value))) {
    throw new AuthApiError("INVALID_RESPONSE", `认证响应时间无效：${field}`, null, false);
  }
  return value;
};

const capability = (value: unknown): AuthCapability => {
  if (typeof value === "string" && (capabilityValues as readonly string[]).includes(value)) return value as AuthCapability;
  throw new AuthApiError("INVALID_RESPONSE", "认证响应包含未知 Capability", null, false);
};

const scopes = (value: unknown): AuthCapability[] => {
  if (!Array.isArray(value) || value.length === 0 || value.length > capabilityValues.length) throw new AuthApiError("INVALID_RESPONSE", "认证响应 scopes 无效", null, false);
  const result = value.map(capability);
  if (new Set(result).size !== result.length) throw new AuthApiError("INVALID_RESPONSE", "认证响应 scopes 重复", null, false);
  return result;
};

const readPayload = async (response: Response): Promise<unknown> => {
  if (response.status === 204) return undefined;
  try {
    return await response.json();
  } catch (error: unknown) {
    throw new AuthApiError("INVALID_RESPONSE", "认证 API 返回了无效 JSON", response.status, false, { cause: error });
  }
};

const readError = (payload: unknown, response: Response): AuthApiError => {
  if (!isRecord(payload)) throw new AuthApiError("INVALID_RESPONSE", "认证 API 错误响应结构无效", response.status, false);
  if (Object.keys(payload).some((key) => !["error_code", "message", "retryable", "workflow_run_id", "details"].includes(key))) {
    throw new AuthApiError("INVALID_RESPONSE", "认证 API 错误响应包含未知字段：Problem", response.status, false);
  }
  const code = stringValue(payload, "error_code");
  const message = stringValue(payload, "message");
  if (typeof payload.retryable !== "boolean") throw new AuthApiError("INVALID_RESPONSE", "认证 API 错误响应字段无效：retryable", response.status, false);
  if (payload.workflow_run_id !== undefined) uuidValue(payload, "workflow_run_id", response.status);
  if (payload.details !== undefined && !isRecord(payload.details)) throw new AuthApiError("INVALID_RESPONSE", "认证 API 错误响应字段无效：details", response.status, false);
  return new AuthApiError(code, message, response.status, payload.retryable);
};

const unsafeMethod = (method: string): boolean => !["GET", "HEAD", "OPTIONS"].includes(method.toUpperCase());

/** 为所有业务 API 统一附加同源 Cookie、CSRF 和请求媒体类型边界。 */
export const authFetch = async (path: string, init: RequestInit = {}): Promise<Response> => {
  const headers = new Headers(init.headers);
  headers.set("Accept", "application/json");
  const multipartBody = typeof FormData !== "undefined" && init.body instanceof FormData;
  if (init.body !== undefined && !multipartBody && !headers.has("Content-Type")) {
    headers.set("Content-Type", "application/json");
  }
  const method = (init.method ?? "GET").toUpperCase();
  if (unsafeMethod(method) && !headers.has("Authorization") && !path.endsWith("/auth/sessions")) {
    const csrf = getCsrfToken();
    if (csrf !== undefined) headers.set("X-CSRF-Token", csrf);
  }
  const response = await fetch(`${apiBaseUrl}${path}`, { ...init, headers, credentials: "include" });
  if (response.headers.get("X-Zhixu-Runtime-Status") === "unavailable") {
    notifyRuntimeAccessInvalidation();
  }
  if (response.status === 401 && path !== "/api/v1/auth/sessions") {
    invalidateAuthSession();
  }
  return response;
};

const authRequestFetch = async (path: string, init: RequestInit = {}): Promise<Response> => {
  try {
    return await authFetch(path, init);
  } catch (error: unknown) {
    if (isAbortError(error)) throw error;
    if (error instanceof AuthApiError) throw error;
    throw new AuthApiError("NETWORK_ERROR", "无法连接认证 API。", null, true, { cause: error });
  }
};

const request = async (path: string, init: RequestInit = {}): Promise<unknown> => {
  const response = await authRequestFetch(path, init);
  const payload = await readPayload(response);
  if (!response.ok) throw readError(payload, response);
  return payload;
};

export const decodeSessionCredential = (value: unknown): SessionCredential => {
  if (!isRecord(value)) throw new AuthApiError("INVALID_RESPONSE", "Session 凭据响应结构无效", null, false);
  assertExactKeys(value, ["session_id", "csrf_token", "expires_at"], "SessionCredential");
  const csrfToken = stringValue(value, "csrf_token");
  if (csrfToken.length !== 43) throw new AuthApiError("INVALID_RESPONSE", "CSRF Token 长度无效", null, false);
  return { sessionId: uuidValue(value, "session_id"), csrfToken, expiresAt: dateTimeValue(value, "expires_at") };
};

export const decodeSessionInfo = (value: unknown): SessionInfo => {
  if (!isRecord(value)) throw new AuthApiError("INVALID_RESPONSE", "Session 响应结构无效", null, false);
  assertExactKeys(value, ["id", "user_label", "scopes", "created_at", "last_seen_at", "expires_at", "revoked_at"], "SessionInfo");
  const revokedAt = optionalString(value, "revoked_at");
  return {
    id: uuidValue(value, "id"), userLabel: stringValue(value, "user_label"), scopes: scopes(value.scopes),
    createdAt: dateTimeValue(value, "created_at"), lastSeenAt: dateTimeValue(value, "last_seen_at"), expiresAt: dateTimeValue(value, "expires_at"),
    ...(revokedAt === undefined ? {} : { revokedAt: dateTimeValue({ revoked_at: revokedAt }, "revoked_at") }),
  };
};

export const decodeApiTokenInfo = (value: unknown): ApiTokenInfo => {
  if (!isRecord(value)) throw new AuthApiError("INVALID_RESPONSE", "API Token 响应结构无效", null, false);
  assertExactKeys(value, ["id", "name", "scopes", "created_at", "last_used_at", "expires_at", "revoked_at"], "APITokenInfo");
  const lastUsedAt = optionalString(value, "last_used_at");
  const revokedAt = optionalString(value, "revoked_at");
  return {
    id: uuidValue(value, "id"), name: stringValue(value, "name"), scopes: scopes(value.scopes), createdAt: dateTimeValue(value, "created_at"),
    ...(lastUsedAt === undefined ? {} : { lastUsedAt: dateTimeValue({ last_used_at: lastUsedAt }, "last_used_at") }),
    expiresAt: dateTimeValue(value, "expires_at"),
    ...(revokedAt === undefined ? {} : { revokedAt: dateTimeValue({ revoked_at: revokedAt }, "revoked_at") }),
  };
};

export const decodeApiTokenCredential = (value: unknown): ApiTokenCredential => {
  if (!isRecord(value)) throw new AuthApiError("INVALID_RESPONSE", "API Token 创建响应结构无效", null, false);
  assertExactKeys(value, ["id", "name", "scopes", "expires_at", "token"], "APITokenCredential");
  const token = stringValue(value, "token");
  if (token.length !== 43) throw new AuthApiError("INVALID_RESPONSE", "API Token 长度无效", null, false);
  return {
    id: uuidValue(value, "id"),
    name: stringValue(value, "name"),
    scopes: scopes(value.scopes),
    expiresAt: dateTimeValue(value, "expires_at"),
    token,
  };
};

export const bootstrapSession = async (bootstrapToken: string): Promise<SessionCredential> => {
  const response = await authRequestFetch("/api/v1/auth/sessions", { method: "POST", headers: { Authorization: `Bearer ${bootstrapToken}` } });
  const payload = await readPayload(response);
  if (!response.ok) throw readError(payload, response);
  const credential = decodeSessionCredential(payload);
  setCsrfToken(credential.csrfToken);
  return credential;
};

export const getCurrentSession = async (signal?: AbortSignal): Promise<SessionInfo> => decodeSessionInfo(await request("/api/v1/auth/session", signal === undefined ? undefined : { signal }));

export const rotateSession = async (): Promise<SessionCredential> => {
  const credential = decodeSessionCredential(await request("/api/v1/auth/session/rotate", { method: "POST" }));
  setCsrfToken(credential.csrfToken);
  return credential;
};

export const revokeSession = async (): Promise<void> => {
  await request("/api/v1/auth/session", { method: "DELETE" });
  clearCsrfToken();
};

export const decodeApiTokenPage = (payload: unknown): ApiTokenPage => {
  if (!isRecord(payload)) throw new AuthApiError("INVALID_RESPONSE", "API Token 列表响应结构无效", null, false);
  assertExactKeys(payload, ["items", "next_cursor"], "APITokenPage");
  if (!Array.isArray(payload.items) || payload.items.length > 100) throw new AuthApiError("INVALID_RESPONSE", "API Token 列表响应结构无效", null, false);
  const nextCursor = optionalString(payload, "next_cursor");
  if (nextCursor !== undefined && nextCursor.length > 2048) throw new AuthApiError("INVALID_RESPONSE", "API Token cursor 无效", null, false);
  return { items: payload.items.map(decodeApiTokenInfo), ...(nextCursor === undefined ? {} : { nextCursor }) };
};

export const listApiTokens = async (cursor?: string, limit = 30, signal?: AbortSignal): Promise<ApiTokenPage> => {
  if (!Number.isSafeInteger(limit) || limit < 1 || limit > 100 || (cursor !== undefined && (cursor === "" || cursor.length > 2048))) {
    throw new AuthApiError("AUTH_REQUEST_INVALID", "API Token 分页参数无效", null, false);
  }
  const query = new URLSearchParams({ limit: String(limit) });
  if (cursor !== undefined) query.set("cursor", cursor);
  return decodeApiTokenPage(await request(`/api/v1/auth/api-tokens?${query.toString()}`, signal === undefined ? undefined : { signal }));
};

export const createApiToken = async (input: CreateApiTokenInput): Promise<ApiTokenCredential> => {
  const payload = await request("/api/v1/auth/api-tokens", { method: "POST", body: JSON.stringify({ name: input.name, scopes: input.scopes, expires_in_seconds: input.expiresInSeconds ?? 0 }) });
  return decodeApiTokenCredential(payload);
};

export const revokeApiToken = async (id: string): Promise<void> => {
  await request(`/api/v1/auth/api-tokens/${encodeURIComponent(id)}`, { method: "DELETE" });
};
