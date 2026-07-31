/** Host Controller control-plane API boundary. This never uses business auth. */

export type ControllerRuntimeStatus = "waiting_for_workspace" | "switching" | "ready" | "recovery_failed";
export type ControllerProcessStatus = "stopped" | "starting" | "prepared" | "ready" | "unavailable";
export type ControllerWorkspaceAvailability = "available" | "unavailable" | "migration_required";
export type ControllerOperationPhase = "validating" | "quiescing" | "revoking" | "applying_grant" | "preparing" | "verifying" | "committing" | "activating" | "rolling_back" | "recovering";
export type ControllerOperationResult = "succeeded" | "rejected" | "cancelled" | "rolled_back" | "failed";

export interface ControllerSession {
  controllerInstanceId: string;
  sessionId: string;
  csrfToken: string;
  expiresAt: string;
}

export interface ControllerWorkspace {
  workspaceId: string;
  name: string;
  rootPath: string;
  availability: ControllerWorkspaceAvailability;
  availabilityReason?: string;
  lastOpenedAt?: string;
}

export interface ControllerOperation {
  operationId: string;
  phase: ControllerOperationPhase;
  result?: ControllerOperationResult;
  targetWorkspaceId?: string;
  targetName?: string;
  errorCode?: string;
  retryable: boolean;
  startedAt: string;
  updatedAt: string;
}

export interface ControllerState {
  controllerInstanceId: string;
  stateVersion: number;
  runtime: {
    status: ControllerRuntimeStatus;
    api: { status: ControllerProcessStatus };
    worker: { status: ControllerProcessStatus };
  };
  activeWorkspace: ControllerWorkspace | null;
  recentWorkspaces: ControllerWorkspace[];
  operation: ControllerOperation | null;
  pollAfterMs: number;
}

export interface ControllerProblem {
  code: string;
  message: string;
  retryable: boolean;
  operationId?: string;
  fieldErrors?: Readonly<Record<string, string>>;
}

export type ControllerWorkspaceSwitchRequest =
  | { targetKind: "registered"; workspaceId: string }
  | { targetKind: "new"; name: string; rootPath: string; initializeGit: boolean };

export interface ControllerCommandOptions {
  stateVersion: number;
  idempotencyKey: string;
  signal?: AbortSignal;
}

export class ControllerApiError extends Error {
  readonly code: "INVALID_REQUEST" | "INVALID_RESPONSE" | "HTTP_ERROR" | "NETWORK_ERROR";
  readonly errorCode: string;
  readonly retryable: boolean;
  readonly status: number | null;
  readonly problem?: ControllerProblem;

  constructor(
    code: ControllerApiError["code"],
    errorCode: string,
    message: string,
    retryable: boolean,
    status: number | null = null,
    problem?: ControllerProblem,
    options?: ErrorOptions,
  ) {
    super(message, options);
    this.name = "ControllerApiError";
    this.code = code;
    this.errorCode = errorCode;
    this.retryable = retryable;
    this.status = status;
    if (problem !== undefined) this.problem = problem;
  }
}

const controlBasePath = "/control/v1";
const controlCsrfHeader = "X-Zhixu-Control-CSRF-Token";
const opaqueTokenPattern = /^[A-Za-z0-9_-]{32,128}$/;
const rfc3339Pattern = /^\d{4}-(?:0[1-9]|1[0-2])-(?:0[1-9]|[12]\d|3[01])T(?:[01]\d|2[0-3]):[0-5]\d:[0-5]\d(?:\.\d{1,9})?(?:Z|[+-](?:[01]\d|2[0-3]):[0-5]\d)$/;
const runtimeStatuses = ["waiting_for_workspace", "switching", "ready", "recovery_failed"] as const;
const processStatuses = ["stopped", "starting", "prepared", "ready", "unavailable"] as const;
const workspaceAvailabilities = ["available", "unavailable", "migration_required"] as const;
const operationPhases = ["validating", "quiescing", "revoking", "applying_grant", "preparing", "verifying", "committing", "activating", "rolling_back", "recovering"] as const;
const operationResults = ["succeeded", "rejected", "cancelled", "rolled_back", "failed"] as const;
let controllerCsrfToken: string | undefined;

const isRecord = (value: unknown): value is Record<string, unknown> => typeof value === "object" && value !== null && !Array.isArray(value);
const isAbortError = (value: unknown): boolean => (value instanceof DOMException || value instanceof Error) && value.name === "AbortError";
const invalidRequest = (field: string): ControllerApiError => new ControllerApiError("INVALID_REQUEST", "INVALID_REQUEST", `控制请求字段无效：${field}`, false);
const invalidResponse = (field: string, status: number | null = null, cause?: unknown): ControllerApiError => new ControllerApiError("INVALID_RESPONSE", "INVALID_RESPONSE", `控制响应字段无效：${field}`, false, status, undefined, cause === undefined ? undefined : { cause });

const exact = (value: Record<string, unknown>, keys: readonly string[], field: string, status: number | null = null): void => {
  const actual = Object.keys(value);
  if (actual.some((key) => !keys.includes(key))) throw invalidResponse(field, status);
};

const text = (value: unknown, field: string, status: number | null = null, allowEmpty = false): string => {
  if (typeof value !== "string" || (!allowEmpty && value.trim() === "")) throw invalidResponse(field, status);
  return value;
};

const optionalText = (value: unknown, field: string, status: number | null = null): string | undefined => value === undefined ? undefined : text(value, field, status);
const opaqueToken = (value: unknown, field: string, status: number | null = null): string => {
  const parsed = text(value, field, status);
  if (!opaqueTokenPattern.test(parsed)) throw invalidResponse(field, status);
  return parsed;
};
const enumValue = <T extends string>(value: unknown, values: readonly T[], field: string, status: number | null = null): T => {
  if (typeof value !== "string" || !values.includes(value as T)) throw invalidResponse(field, status);
  return value as T;
};
const nonNegativeInteger = (value: unknown, field: string, status: number | null = null, minimum = 0): number => {
  if (typeof value !== "number" || !Number.isSafeInteger(value) || value < minimum) throw invalidResponse(field, status);
  return value;
};
const booleanValue = (value: unknown, field: string, status: number | null = null): boolean => {
  if (typeof value !== "boolean") throw invalidResponse(field, status);
  return value;
};
const dateTime = (value: unknown, field: string, status: number | null = null): string => {
  const parsed = text(value, field, status);
  const year = Number(parsed.slice(0, 4));
  const month = Number(parsed.slice(5, 7));
  const day = Number(parsed.slice(8, 10));
  const daysInMonth = month === 2
    ? (year % 4 === 0 && (year % 100 !== 0 || year % 400 === 0) ? 29 : 28)
    : month === 4 || month === 6 || month === 9 || month === 11 ? 30 : 31;
  if (!rfc3339Pattern.test(parsed) || year < 1 || day > daysInMonth || !Number.isFinite(Date.parse(parsed))) throw invalidResponse(field, status);
  return parsed;
};
const required = (record: Record<string, unknown>, field: string, status: number | null = null): unknown => {
  if (!Object.prototype.hasOwnProperty.call(record, field)) throw invalidResponse(field, status);
  return record[field];
};
const requireOpaqueRequestValue = (value: string, field: string): string => {
  if (value === "" || value !== value.trim() || value.length > 2048 || /[\u0000-\u001f\u007f]/.test(value)) throw invalidRequest(field);
  return value;
};
const requireIdempotencyKey = (value: string): string => {
  if (value === "" || value !== value.trim() || value.length > 128 || /[\u0000-\u001f\u007f]/.test(value)) throw invalidRequest("idempotencyKey");
  return value;
};
const quotedStateVersion = (value: number): string => {
  if (!Number.isSafeInteger(value) || value < 1) throw invalidRequest("stateVersion");
  return `"${String(value)}"`;
};

export const getControllerCsrfToken = (): string | undefined => controllerCsrfToken;
export const clearControllerCsrfToken = (): void => { controllerCsrfToken = undefined; };

export const decodeControllerSession = (value: unknown): ControllerSession => {
  if (!isRecord(value)) throw invalidResponse("session");
  exact(value, ["controller_instance_id", "session_id", "csrf_token", "expires_at"], "session");
  const controllerInstanceId = opaqueToken(required(value, "controller_instance_id"), "session.controller_instance_id");
  const sessionId = opaqueToken(required(value, "session_id"), "session.session_id");
  const csrfToken = opaqueToken(required(value, "csrf_token"), "session.csrf_token");
  return { controllerInstanceId, sessionId, csrfToken, expiresAt: dateTime(required(value, "expires_at"), "session.expires_at") };
};

export const decodeControllerWorkspace = (value: unknown): ControllerWorkspace => {
  if (!isRecord(value)) throw invalidResponse("workspace");
  exact(value, ["workspace_id", "name", "root_path", "availability", "availability_reason", "last_opened_at"], "workspace");
  const availabilityReason = optionalText(value.availability_reason, "workspace.availability_reason");
  const lastOpenedAt = value.last_opened_at === undefined ? undefined : dateTime(value.last_opened_at, "workspace.last_opened_at");
  return {
    workspaceId: text(required(value, "workspace_id"), "workspace.workspace_id"),
    name: text(required(value, "name"), "workspace.name"),
    rootPath: text(required(value, "root_path"), "workspace.root_path"),
    availability: enumValue(required(value, "availability"), workspaceAvailabilities, "workspace.availability"),
    ...(availabilityReason === undefined ? {} : { availabilityReason }),
    ...(lastOpenedAt === undefined ? {} : { lastOpenedAt }),
  };
};

export const decodeControllerOperation = (value: unknown): ControllerOperation => {
  if (!isRecord(value)) throw invalidResponse("operation");
  exact(value, ["operation_id", "phase", "result", "target_workspace_id", "target_name", "error_code", "retryable", "started_at", "updated_at"], "operation");
  const result = value.result === undefined ? undefined : enumValue(value.result, operationResults, "operation.result");
  const targetWorkspaceId = optionalText(value.target_workspace_id, "operation.target_workspace_id");
  const targetName = optionalText(value.target_name, "operation.target_name");
  const errorCode = optionalText(value.error_code, "operation.error_code");
  const operation: ControllerOperation = {
    operationId: text(required(value, "operation_id"), "operation.operation_id"),
    phase: enumValue(required(value, "phase"), operationPhases, "operation.phase"),
    retryable: booleanValue(required(value, "retryable"), "operation.retryable"),
    startedAt: dateTime(required(value, "started_at"), "operation.started_at"),
    updatedAt: dateTime(required(value, "updated_at"), "operation.updated_at"),
    ...(result === undefined ? {} : { result }),
    ...(targetWorkspaceId === undefined ? {} : { targetWorkspaceId }),
    ...(targetName === undefined ? {} : { targetName }),
    ...(errorCode === undefined ? {} : { errorCode }),
  };
  if (Date.parse(operation.updatedAt) < Date.parse(operation.startedAt)) throw invalidResponse("operation.timestamps");
  return operation;
};

export const decodeControllerState = (value: unknown): ControllerState => {
  if (!isRecord(value) || !isRecord(value.runtime) || !isRecord(value.runtime.api) || !isRecord(value.runtime.worker)) throw invalidResponse("state");
  exact(value, ["controller_instance_id", "state_version", "runtime", "active_workspace", "recent_workspaces", "operation", "poll_after_ms"], "state");
  exact(value.runtime, ["status", "api", "worker"], "state.runtime");
  exact(value.runtime.api, ["status"], "state.runtime.api");
  exact(value.runtime.worker, ["status"], "state.runtime.worker");
  if (!Array.isArray(value.recent_workspaces)) throw invalidResponse("state.recent_workspaces");
  const activeWorkspace = value.active_workspace === null ? null : decodeControllerWorkspace(value.active_workspace);
  const operation = value.operation === null ? null : decodeControllerOperation(value.operation);
  return {
    controllerInstanceId: opaqueToken(required(value, "controller_instance_id"), "state.controller_instance_id"),
    stateVersion: nonNegativeInteger(required(value, "state_version"), "state.state_version", null, 1),
    runtime: {
      status: enumValue(required(value.runtime, "status"), runtimeStatuses, "state.runtime.status"),
      api: { status: enumValue(required(value.runtime.api, "status"), processStatuses, "state.runtime.api.status") },
      worker: { status: enumValue(required(value.runtime.worker, "status"), processStatuses, "state.runtime.worker.status") },
    },
    activeWorkspace,
    recentWorkspaces: value.recent_workspaces.map(decodeControllerWorkspace),
    operation,
    pollAfterMs: nonNegativeInteger(required(value, "poll_after_ms"), "state.poll_after_ms", null, 1),
  };
};

export const decodeControllerProblem = (value: unknown, status: number | null = null): ControllerProblem => {
  if (!isRecord(value)) throw invalidResponse("problem", status);
  exact(value, ["code", "message", "retryable", "operation_id", "field_errors"], "problem", status);
  const operationId = optionalText(value.operation_id, "problem.operation_id", status);
  let fieldErrors: Readonly<Record<string, string>> | undefined;
  if (value.field_errors !== undefined) {
    if (!isRecord(value.field_errors)) throw invalidResponse("problem.field_errors", status);
    const parsedFieldErrors: Record<string, string> = {};
    for (const [field, message] of Object.entries(value.field_errors)) {
      if (field.trim() === "" || typeof message !== "string" || message.trim() === "") throw invalidResponse("problem.field_errors", status);
      parsedFieldErrors[field] = message;
    }
    fieldErrors = Object.freeze(parsedFieldErrors);
  }
  return {
    code: text(required(value, "code", status), "problem.code", status),
    message: text(required(value, "message", status), "problem.message", status),
    retryable: booleanValue(required(value, "retryable", status), "problem.retryable", status),
    ...(operationId === undefined ? {} : { operationId }),
    ...(fieldErrors === undefined ? {} : { fieldErrors }),
  };
};

const contentTypeIs = (response: Response, expected: string): boolean => response.headers.get("Content-Type")?.split(";", 1)[0]?.trim().toLowerCase() === expected;
const decodeJson = async (response: Response): Promise<unknown> => {
  if (!contentTypeIs(response, "application/json")) throw invalidResponse("content_type", response.status);
  try {
    return await response.json();
  } catch (error: unknown) {
    if (isAbortError(error)) throw error;
    throw invalidResponse("json", response.status, error);
  }
};

const request = async (path: string, init: RequestInit, expectedStatus: number, decoder: (value: unknown) => unknown): Promise<unknown> => {
  let response: Response;
  try {
    response = await fetch(`${controlBasePath}${path}`, { ...init, credentials: "include" });
  } catch (error: unknown) {
    if (isAbortError(error)) throw error;
    throw new ControllerApiError("NETWORK_ERROR", "NETWORK_ERROR", "无法连接本机 Workspace 控制器。", true, null, undefined, { cause: error });
  }
  if (!response.ok || response.status !== expectedStatus) {
    if (!contentTypeIs(response, "application/problem+json")) throw invalidResponse("problem.content_type", response.status);
    let value: unknown;
    try {
      value = await response.json();
    } catch (error: unknown) {
      if (isAbortError(error)) throw error;
      throw invalidResponse("problem.json", response.status, error);
    }
    let problem: ControllerProblem;
    try {
      problem = decodeControllerProblem(value, response.status);
    } catch (error: unknown) {
      if (error instanceof ControllerApiError) throw error;
      throw invalidResponse("problem", response.status, error);
    }
    throw new ControllerApiError("HTTP_ERROR", problem.code, problem.message, problem.retryable, response.status, problem);
  }
  return decoder(await decodeJson(response));
};

const readSession = (value: unknown): ControllerSession => {
  const session = decodeControllerSession(value);
  controllerCsrfToken = session.csrfToken;
  return session;
};

const commandHeaders = (options: ControllerCommandOptions): Headers => {
  const csrfToken = controllerCsrfToken;
  if (csrfToken === undefined) throw invalidRequest("controllerCsrfToken");
  const headers = new Headers({ Accept: "application/json", "Content-Type": "application/json" });
  headers.set(controlCsrfHeader, csrfToken);
  headers.set("Idempotency-Key", requireIdempotencyKey(options.idempotencyKey));
  headers.set("If-Match", quotedStateVersion(options.stateVersion));
  return headers;
};

export const consumeControllerBootstrapTokenFromFragment = (): string | undefined => {
  if (typeof window === "undefined" || typeof history === "undefined") return undefined;
  const hash = window.location.hash;
  if (!hash.startsWith("#control=")) return undefined;
  const token = /^#control=([A-Za-z0-9_-]{32,128})$/.exec(hash)?.[1];
  try {
    history.replaceState(history.state, "", `${window.location.pathname}${window.location.search}`);
  } catch {
    return undefined;
  }
  return token;
};

export const exchangeControllerSession = (bootstrapToken: string, signal?: AbortSignal): Promise<ControllerSession> => {
  if (!opaqueTokenPattern.test(bootstrapToken)) return Promise.reject(invalidRequest("bootstrapToken"));
  return request("/sessions", {
    method: "POST",
    headers: { Accept: "application/json", Authorization: `Bearer ${bootstrapToken}` },
    ...(signal === undefined ? {} : { signal }),
  }, 201, readSession) as Promise<ControllerSession>;
};

export const getControllerSession = (signal?: AbortSignal): Promise<ControllerSession> => request("/session", {
  method: "GET", headers: { Accept: "application/json" }, ...(signal === undefined ? {} : { signal }),
}, 200, readSession) as Promise<ControllerSession>;

export const getControllerState = async (signal?: AbortSignal): Promise<ControllerState> => {
  let response: Response;
  try {
    response = await fetch(`${controlBasePath}/state`, { method: "GET", headers: { Accept: "application/json" }, credentials: "include", ...(signal === undefined ? {} : { signal }) });
  } catch (error: unknown) {
    if (isAbortError(error)) throw error;
    throw new ControllerApiError("NETWORK_ERROR", "NETWORK_ERROR", "无法连接本机 Workspace 控制器。", true, null, undefined, { cause: error });
  }
  if (!response.ok || response.status !== 200) {
    if (!contentTypeIs(response, "application/problem+json")) throw invalidResponse("problem.content_type", response.status);
    let value: unknown;
    try { value = await response.json(); } catch (error: unknown) { if (isAbortError(error)) throw error; throw invalidResponse("problem.json", response.status, error); }
    const problem = decodeControllerProblem(value, response.status);
    throw new ControllerApiError("HTTP_ERROR", problem.code, problem.message, problem.retryable, response.status, problem);
  }
  const state = decodeControllerState(await decodeJson(response));
  if (response.headers.get("ETag") !== quotedStateVersion(state.stateVersion)) throw invalidResponse("state.etag", response.status);
  return state;
};

export const getControllerOperation = (operationId: string, signal?: AbortSignal): Promise<ControllerOperation> => {
  const id = requireOpaqueRequestValue(operationId, "operationId");
  return request(`/workspace-switches/${encodeURIComponent(id)}`, {
    method: "GET", headers: { Accept: "application/json" }, ...(signal === undefined ? {} : { signal }),
  }, 200, (value) => {
    if (!isRecord(value)) throw invalidResponse("operation_response");
    exact(value, ["operation"], "operation_response");
    return decodeControllerOperation(required(value, "operation"));
  }) as Promise<ControllerOperation>;
};

export const startControllerWorkspaceSwitch = (input: ControllerWorkspaceSwitchRequest, options: ControllerCommandOptions): Promise<ControllerOperation> => {
  let body: Record<string, unknown>;
  if (input.targetKind === "registered") {
    body = { target_kind: "registered", workspace_id: requireOpaqueRequestValue(input.workspaceId, "workspaceId") };
  } else {
    if (typeof input.initializeGit !== "boolean") throw invalidRequest("initializeGit");
    body = {
      target_kind: "new",
      name: requireOpaqueRequestValue(input.name, "name"),
      root_path: requireOpaqueRequestValue(input.rootPath, "rootPath"),
      initialize_git: input.initializeGit,
    };
  }
  return request("/workspace-switches", {
    method: "POST", headers: commandHeaders(options), body: JSON.stringify(body), ...(options.signal === undefined ? {} : { signal: options.signal }),
  }, 202, (value) => {
    if (!isRecord(value)) throw invalidResponse("workspace_switch_response");
    exact(value, ["operation"], "workspace_switch_response");
    return decodeControllerOperation(required(value, "operation"));
  }) as Promise<ControllerOperation>;
};

export const checkControllerWorkspaceAvailability = (workspaceId: string, options: ControllerCommandOptions): Promise<ControllerWorkspace> => {
  const id = requireOpaqueRequestValue(workspaceId, "workspaceId");
  return request(`/workspaces/${encodeURIComponent(id)}/availability-checks`, {
    method: "POST", headers: commandHeaders(options), body: "{}", ...(options.signal === undefined ? {} : { signal: options.signal }),
  }, 200, (value) => {
    if (!isRecord(value)) throw invalidResponse("availability_response");
    exact(value, ["workspace"], "availability_response");
    return decodeControllerWorkspace(required(value, "workspace"));
  }) as Promise<ControllerWorkspace>;
};

export const removeControllerWorkspace = async (workspaceId: string, options: ControllerCommandOptions): Promise<void> => {
  const id = requireOpaqueRequestValue(workspaceId, "workspaceId");
  let response: Response;
  try {
    response = await fetch(`${controlBasePath}/workspaces/${encodeURIComponent(id)}`, {
      method: "DELETE", headers: commandHeaders(options), credentials: "include", ...(options.signal === undefined ? {} : { signal: options.signal }),
    });
  } catch (error: unknown) {
    if (isAbortError(error)) throw error;
    throw new ControllerApiError("NETWORK_ERROR", "NETWORK_ERROR", "无法连接本机 Workspace 控制器。", true, null, undefined, { cause: error });
  }
  if (response.status === 204) return;
  if (!contentTypeIs(response, "application/problem+json")) throw invalidResponse("problem.content_type", response.status);
  let value: unknown;
  try { value = await response.json(); } catch (error: unknown) { if (isAbortError(error)) throw error; throw invalidResponse("problem.json", response.status, error); }
  const problem = decodeControllerProblem(value, response.status);
  throw new ControllerApiError("HTTP_ERROR", problem.code, problem.message, problem.retryable, response.status, problem);
};
