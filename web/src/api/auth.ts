import { z } from "zod";

import { canonicalUuidPattern as uuidPattern, isAbortError } from "../shared/codec";
import { AuthApi } from "./generated/apis/AuthApi";
import type { ApiResponse } from "./generated/runtime";
import {
  AuthApiError,
  clearCsrfToken,
  getCsrfToken,
  invalidateAuthSession,
  setCsrfToken,
  subscribeAuthInvalidation,
  subscribeCsrfTokenChanges,
} from "./auth-session-state";
import {
  generatedBrowserSecurity,
  generatedConfiguration,
  generatedRawResponse,
  generatedRequestInit,
} from "./generated-client";
import { decodeApiProblem, ProblemValidationError } from "./problem";

export {
  AuthApiError,
  clearCsrfToken,
  getCsrfToken,
  invalidateAuthSession,
  setCsrfToken,
  subscribeAuthInvalidation,
  subscribeCsrfTokenChanges,
};
export type { AuthInvalidationReason } from "./auth-session-state";
export { apiBaseUrl, authFetch } from "./transport";

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

const rfc3339Pattern = /^\d{4}-(?:0[1-9]|1[0-2])-(?:0[1-9]|[12]\d|3[01])T(?:[01]\d|2[0-3]):[0-5]\d:[0-5]\d(?:\.\d{1,9})?(?:Z|[+-](?:[01]\d|2[0-3]):[0-5]\d)$/;
const capabilityValues = [
  "READ_LOCAL", "READ_EXTERNAL", "WRITE_PROPOSAL", "WRITE_KNOWLEDGE", "GIT_WRITE", "INDEX_MAINTENANCE", "EVALUATION_RUN", "MANAGE_SYSTEM_SETTINGS",
] as const satisfies readonly AuthCapability[];

const nonEmptyStringSchema = z.string().refine((value) => value.trim() !== "");
const uuidSchema = nonEmptyStringSchema.regex(uuidPattern);
const dateTimeSchema = nonEmptyStringSchema.refine((value) => {
  const year = Number(value.slice(0, 4));
  const month = Number(value.slice(5, 7));
  const day = Number(value.slice(8, 10));
  const daysInMonth = month === 2
    ? (year % 4 === 0 && (year % 100 !== 0 || year % 400 === 0) ? 29 : 28)
    : month === 4 || month === 6 || month === 9 || month === 11 ? 30 : 31;
  return rfc3339Pattern.test(value)
    && year >= 1
    && day <= daysInMonth
    && Number.isFinite(Date.parse(value));
});
const capabilitySchema = z.enum(capabilityValues);
const scopesSchema = z.array(capabilitySchema)
  .min(1)
  .max(capabilityValues.length)
  .refine((values) => new Set(values).size === values.length);
const sessionCredentialSchema = z.strictObject({
  session_id: uuidSchema,
  csrf_token: nonEmptyStringSchema.length(43),
  expires_at: dateTimeSchema,
});
const sessionInfoSchema = z.strictObject({
  id: uuidSchema,
  user_label: nonEmptyStringSchema,
  scopes: scopesSchema,
  created_at: dateTimeSchema,
  last_seen_at: dateTimeSchema,
  expires_at: dateTimeSchema,
  revoked_at: dateTimeSchema.optional(),
});
const apiTokenInfoSchema = z.strictObject({
  id: uuidSchema,
  name: nonEmptyStringSchema,
  scopes: scopesSchema,
  created_at: dateTimeSchema,
  last_used_at: dateTimeSchema.optional(),
  expires_at: dateTimeSchema,
  revoked_at: dateTimeSchema.optional(),
});
const apiTokenCredentialSchema = z.strictObject({
  id: uuidSchema,
  name: nonEmptyStringSchema,
  scopes: scopesSchema,
  expires_at: dateTimeSchema,
  token: nonEmptyStringSchema.length(43),
});
const apiTokenPageSchema = z.strictObject({
  items: z.array(apiTokenInfoSchema).max(100),
  next_cursor: nonEmptyStringSchema.max(2048).optional(),
});
const authApi = new AuthApi(generatedConfiguration);

const parseAuthResponse = <T>(schema: z.ZodType<T>, value: unknown, label: string, status: number | null = null): T => {
  const result = schema.safeParse(value);
  if (!result.success) {
    throw new AuthApiError("INVALID_RESPONSE", `${label}结构无效`, status, false);
  }
  return result.data;
};

const readPayload = async (response: Response): Promise<unknown> => {
  if (response.status === 204) return undefined;
  try {
    return await response.json();
  } catch (error: unknown) {
    if (isAbortError(error)) throw error;
    throw new AuthApiError("INVALID_RESPONSE", "认证 API 返回了无效 JSON", response.status, false, { cause: error });
  }
};

const readError = (payload: unknown, response: Response): AuthApiError => {
  try {
    const problem = decodeApiProblem(payload);
    return new AuthApiError(problem.errorCode, problem.message, response.status, problem.retryable);
  } catch (error: unknown) {
    if (!(error instanceof ProblemValidationError)) throw error;
    throw new AuthApiError("INVALID_RESPONSE", "认证 API 错误响应结构无效", response.status, false);
  }
};

const authOperationResponse = async <T>(operation: Promise<ApiResponse<T>>): Promise<Response> => {
  try {
    return await generatedRawResponse(operation);
  } catch (error: unknown) {
    if (isAbortError(error)) throw error;
    if (error instanceof AuthApiError) throw error;
    throw new AuthApiError("NETWORK_ERROR", "无法连接认证 API。", null, true, { cause: error });
  }
};

const request = async <T>(operation: Promise<ApiResponse<T>>): Promise<unknown> => {
  const response = await authOperationResponse(operation);
  const payload = await readPayload(response);
  if (!response.ok) throw readError(payload, response);
  return payload;
};

export const decodeSessionCredential = (value: unknown): SessionCredential => {
  const credential = parseAuthResponse(sessionCredentialSchema, value, "Session 凭据响应");
  return { sessionId: credential.session_id, csrfToken: credential.csrf_token, expiresAt: credential.expires_at };
};

export const decodeSessionInfo = (value: unknown): SessionInfo => {
  const session = parseAuthResponse(sessionInfoSchema, value, "Session 响应");
  return {
    id: session.id,
    userLabel: session.user_label,
    scopes: [...session.scopes],
    createdAt: session.created_at,
    lastSeenAt: session.last_seen_at,
    expiresAt: session.expires_at,
    ...(session.revoked_at === undefined ? {} : { revokedAt: session.revoked_at }),
  };
};

export const decodeApiTokenInfo = (value: unknown): ApiTokenInfo => {
  const token = parseAuthResponse(apiTokenInfoSchema, value, "API Token 响应");
  return {
    id: token.id,
    name: token.name,
    scopes: [...token.scopes],
    createdAt: token.created_at,
    ...(token.last_used_at === undefined ? {} : { lastUsedAt: token.last_used_at }),
    expiresAt: token.expires_at,
    ...(token.revoked_at === undefined ? {} : { revokedAt: token.revoked_at }),
  };
};

export const decodeApiTokenCredential = (value: unknown): ApiTokenCredential => {
  const credential = parseAuthResponse(apiTokenCredentialSchema, value, "API Token 创建响应");
  return {
    id: credential.id,
    name: credential.name,
    scopes: [...credential.scopes],
    expiresAt: credential.expires_at,
    token: credential.token,
  };
};

export const bootstrapSession = async (bootstrapToken: string): Promise<SessionCredential> => {
  const response = await authOperationResponse(authApi.exchangeBootstrapForSessionRaw({
    headers: { Authorization: `Bearer ${bootstrapToken}` },
  }));
  const payload = await readPayload(response);
  if (!response.ok) throw readError(payload, response);
  const credential = decodeSessionCredential(payload);
  setCsrfToken(credential.csrfToken);
  return credential;
};

export const getCurrentSession = async (signal?: AbortSignal): Promise<SessionInfo> =>
  decodeSessionInfo(await request(authApi.getCurrentSessionRaw(generatedRequestInit(signal))));

export const rotateSession = async (): Promise<SessionCredential> => {
  const credential = decodeSessionCredential(await request(authApi.rotateSessionRaw(generatedBrowserSecurity())));
  setCsrfToken(credential.csrfToken);
  return credential;
};

export const revokeSession = async (): Promise<void> => {
  await request(authApi.revokeCurrentSessionRaw(generatedBrowserSecurity()));
  clearCsrfToken();
};

export const decodeApiTokenPage = (payload: unknown): ApiTokenPage => {
  const page = parseAuthResponse(apiTokenPageSchema, payload, "API Token 列表响应");
  return {
    items: page.items.map(decodeApiTokenInfo),
    ...(page.next_cursor === undefined ? {} : { nextCursor: page.next_cursor }),
  };
};

export const listApiTokens = async (cursor?: string, limit = 30, signal?: AbortSignal): Promise<ApiTokenPage> => {
  if (!Number.isSafeInteger(limit) || limit < 1 || limit > 100 || (cursor !== undefined && (cursor === "" || cursor.length > 2048))) {
    throw new AuthApiError("AUTH_REQUEST_INVALID", "API Token 分页参数无效", null, false);
  }
  return decodeApiTokenPage(await request(authApi.listApiTokensRaw({
    limit,
    ...(cursor === undefined ? {} : { cursor }),
  }, generatedRequestInit(signal))));
};

export const createApiToken = async (input: CreateApiTokenInput): Promise<ApiTokenCredential> => {
  const payload = await request(authApi.createApiTokenRaw({
    ...generatedBrowserSecurity(),
    createAPITokenRequest: {
      name: input.name,
      scopes: input.scopes,
      expires_in_seconds: input.expiresInSeconds ?? 0,
    },
  }));
  return decodeApiTokenCredential(payload);
};

export const revokeApiToken = async (id: string): Promise<void> => {
  await request(authApi.revokeApiTokenRaw({ ...generatedBrowserSecurity(), tokenId: id }));
};
