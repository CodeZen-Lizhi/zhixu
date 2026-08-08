import { authFetch } from "./auth";

export type ActiveWorkspaceStatus = "active";
export type ActiveWorkspaceAvailability = "available" | "unavailable" | "migration_required";

export interface ActiveWorkspace {
  id: string;
  name: string;
  rootPath: string;
  status: ActiveWorkspaceStatus;
  availability: ActiveWorkspaceAvailability;
  version: number;
}

export class ActiveWorkspaceApiError extends Error {
  readonly code: string;
  readonly retryable: boolean;

  constructor(code: string, message: string, retryable: boolean, options?: ErrorOptions) {
    super(message, options);
    this.name = "ActiveWorkspaceApiError";
    this.code = code;
    this.retryable = retryable;
  }
}

const uuidPattern = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;
const controlCharacterPattern = /[\u0000-\u001f\u007f]/;
const activeWorkspaceKeys = ["id", "name", "root_path", "status", "availability", "version"] as const;
const problemKeys = ["error_code", "message", "retryable", "workflow_run_id", "details"] as const;

const isRecord = (value: unknown): value is Record<string, unknown> =>
  typeof value === "object" && value !== null && !Array.isArray(value);

const invalidResponse = (field: string): ActiveWorkspaceApiError =>
  new ActiveWorkspaceApiError("INVALID_RESPONSE", `Active Workspace 响应字段无效：${field}`, false);

const assertExactKeys = (value: Record<string, unknown>): void => {
  const keys = Object.keys(value);
  if (keys.length !== activeWorkspaceKeys.length || keys.some((key) => !activeWorkspaceKeys.includes(key as typeof activeWorkspaceKeys[number]))) {
    throw invalidResponse("workspace");
  }
};

const readString = (value: Record<string, unknown>, field: string): string => {
  const fieldValue = value[field];
  if (typeof fieldValue !== "string" || fieldValue.trim() === "") throw invalidResponse(field);
  return fieldValue;
};

const readUuid = (value: Record<string, unknown>, field: string): string => {
  const fieldValue = readString(value, field);
  if (!uuidPattern.test(fieldValue)) throw invalidResponse(field);
  return fieldValue;
};

const readRootPath = (value: Record<string, unknown>): string => {
  const rootPath = readString(value, "root_path");
  if (!rootPath.startsWith("/") || controlCharacterPattern.test(rootPath) || rootPath.includes("\\")) {
    throw invalidResponse("root_path");
  }
  return rootPath;
};

const readVersion = (value: Record<string, unknown>): number => {
  const version = value.version;
  if (typeof version !== "number" || !Number.isSafeInteger(version) || version < 1) throw invalidResponse("version");
  return version;
};

export const decodeActiveWorkspace = (value: unknown): ActiveWorkspace => {
  if (!isRecord(value)) throw invalidResponse("workspace");
  assertExactKeys(value);
  const status = readString(value, "status");
  if (status !== "active") throw invalidResponse("status");
  const availability = readString(value, "availability");
  if (availability !== "available" && availability !== "unavailable" && availability !== "migration_required") {
    throw invalidResponse("availability");
  }
  return {
    id: readUuid(value, "id"),
    name: readString(value, "name"),
    rootPath: readRootPath(value),
    status,
    availability,
    version: readVersion(value),
  };
};

const isAbortError = (value: unknown): boolean =>
  (value instanceof DOMException || value instanceof Error) && value.name === "AbortError";

const readProblem = (value: unknown, response: Response): ActiveWorkspaceApiError => {
  if (!isRecord(value)) {
    return new ActiveWorkspaceApiError("HTTP_ERROR", `Active Workspace 请求失败（HTTP ${String(response.status)}）。`, response.status >= 500);
  }
  const keys = Object.keys(value);
  const hasInvalidField = keys.some((key) => !problemKeys.includes(key as typeof problemKeys[number]));
  const invalidWorkflowRun = value.workflow_run_id !== undefined
    && (typeof value.workflow_run_id !== "string" || !uuidPattern.test(value.workflow_run_id));
  const invalidDetails = value.details !== undefined && !isRecord(value.details);
  if (
    hasInvalidField
    || typeof value.error_code !== "string"
    || value.error_code.trim() === ""
    || typeof value.message !== "string"
    || value.message.trim() === ""
    || typeof value.retryable !== "boolean"
    || invalidWorkflowRun
    || invalidDetails
  ) {
    return new ActiveWorkspaceApiError("HTTP_ERROR", `Active Workspace 请求失败（HTTP ${String(response.status)}）。`, response.status >= 500);
  }
  return new ActiveWorkspaceApiError(value.error_code, value.message, value.retryable);
};

export const getActiveWorkspace = async (signal?: AbortSignal): Promise<ActiveWorkspace> => {
  let response: Response;
  try {
    response = await authFetch("/api/v1/workspaces/active", {
      ...(signal === undefined ? {} : { signal }),
      headers: { Accept: "application/json" },
    });
  } catch (error: unknown) {
    if (isAbortError(error)) throw error;
    throw new ActiveWorkspaceApiError("NETWORK_ERROR", "无法连接 Active Workspace API。", true, {
      cause: error,
    });
  }

  let payload: unknown;
  try {
    payload = await response.json();
  } catch (error: unknown) {
    if (isAbortError(error)) throw error;
    throw new ActiveWorkspaceApiError("INVALID_RESPONSE", "Active Workspace API 返回了无效 JSON。", false, {
      cause: error,
    });
  }
  if (!response.ok) throw readProblem(payload, response);
  return decodeActiveWorkspace(payload);
};
