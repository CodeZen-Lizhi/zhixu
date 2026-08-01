export type RuntimeAccessStatus = "waiting" | "ready" | "unavailable";

export type RuntimeAccess =
  | { status: "ready"; activeWorkspaceId: string; pollAfterMs: number }
  | { status: "waiting" | "unavailable"; activeWorkspaceId: null; pollAfterMs: number };

export class RuntimeAccessApiError extends Error {
  readonly code: "INVALID_RESPONSE" | "HTTP_ERROR" | "NETWORK_ERROR";
  readonly errorCode: string;
  readonly retryable: boolean;
  readonly status: number | null;

  constructor(
    code: RuntimeAccessApiError["code"],
    errorCode: string,
    message: string,
    retryable: boolean,
    status: number | null = null,
    options?: ErrorOptions,
  ) {
    super(message, options);
    this.name = "RuntimeAccessApiError";
    this.code = code;
    this.errorCode = errorCode;
    this.retryable = retryable;
    this.status = status;
  }
}

const runtimeAccessPath = "/host/v1/runtime";
const uuidPattern = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;
const runtimeStatuses = ["waiting", "ready", "unavailable"] as const;
const isRecord = (value: unknown): value is Record<string, unknown> => typeof value === "object" && value !== null && !Array.isArray(value);

const invalidResponse = (field: string, status: number | null = null, cause?: unknown): RuntimeAccessApiError =>
  new RuntimeAccessApiError("INVALID_RESPONSE", "INVALID_RESPONSE", `运行时访问响应字段无效：${field}`, false, status, cause === undefined ? undefined : { cause });

const decodeProblem = (value: unknown, status: number): RuntimeAccessApiError => {
  if (!isRecord(value) || Object.keys(value).some((key) => !["code", "message", "retryable"].includes(key))) {
    return invalidResponse("Problem", status);
  }
  if (typeof value.code !== "string" || value.code.trim() === "" || typeof value.message !== "string" || value.message.trim() === "" || typeof value.retryable !== "boolean") {
    return invalidResponse("Problem", status);
  }
  return new RuntimeAccessApiError("HTTP_ERROR", value.code, value.message, value.retryable, status);
};

export const decodeRuntimeAccess = (value: unknown): RuntimeAccess => {
  if (!isRecord(value) || Object.keys(value).some((key) => !["status", "active_workspace_id", "poll_after_ms"].includes(key))) {
    throw invalidResponse("runtime");
  }
  if (typeof value.status !== "string" || !runtimeStatuses.includes(value.status as RuntimeAccessStatus)) throw invalidResponse("runtime.status");
  if (typeof value.poll_after_ms !== "number" || !Number.isSafeInteger(value.poll_after_ms) || value.poll_after_ms < 500 || value.poll_after_ms > 5000) {
    throw invalidResponse("runtime.poll_after_ms");
  }
  const status = value.status as RuntimeAccessStatus;
  if (status === "ready") {
    if (typeof value.active_workspace_id !== "string" || !uuidPattern.test(value.active_workspace_id)) throw invalidResponse("runtime.active_workspace_id");
    return { status, activeWorkspaceId: value.active_workspace_id, pollAfterMs: value.poll_after_ms };
  }
  if (value.active_workspace_id !== null) throw invalidResponse("runtime.active_workspace_id");
  return { status, activeWorkspaceId: null, pollAfterMs: value.poll_after_ms };
};

export const getRuntimeAccess = async (signal?: AbortSignal): Promise<RuntimeAccess> => {
  let response: Response;
  try {
    response = await fetch(runtimeAccessPath, {
      method: "GET",
      headers: { Accept: "application/json" },
      credentials: "include",
      ...(signal === undefined ? {} : { signal }),
    });
  } catch (error: unknown) {
    if ((error instanceof Error || error instanceof DOMException) && error.name === "AbortError") throw error;
    throw new RuntimeAccessApiError("NETWORK_ERROR", "NETWORK_ERROR", "运行时访问请求失败", true, null, { cause: error });
  }
  let body: unknown;
  try {
    body = await response.json();
  } catch (error: unknown) {
    throw invalidResponse("JSON", response.status, error);
  }
  if (!response.ok) throw decodeProblem(body, response.status);
  return decodeRuntimeAccess(body);
};
