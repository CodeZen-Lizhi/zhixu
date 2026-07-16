export interface WorkspaceGitStatus {
  present: boolean;
  repositoryPath: string;
  branch: string;
  head: string;
  dirty: boolean;
  checkedAt: string;
}

export interface Workspace {
  id: string;
  name: string;
  rootPath: string;
  status: "active";
  version: number;
  git: WorkspaceGitStatus;
  warnings: string[];
  createdAt: string;
  updatedAt: string;
}

export interface ScannedFile {
  relativePath: string;
  byteSize: number;
  contentHash: string;
  mediaType: string;
  sourceId: string;
  sourceVersionId: string;
  contentArtifactId: string;
  contentArtifactCreated: boolean;
}

export interface WorkspaceScan {
  workspaceId: string;
  files: ScannedFile[];
  count: number;
}

export interface CreateWorkspaceInput {
  name: string;
  rootPath: string;
  initializeGit: boolean;
}

export class WorkspaceApiError extends Error {
  readonly code: string;
  readonly retryable: boolean;

  constructor(code: string, message: string, retryable: boolean) {
    super(message);
    this.name = "WorkspaceApiError";
    this.code = code;
    this.retryable = retryable;
  }
}

const apiBaseUrl = (import.meta.env.VITE_API_BASE_URL ?? "").replace(/\/$/, "");

const isRecord = (value: unknown): value is Record<string, unknown> =>
  typeof value === "object" && value !== null && !Array.isArray(value);

const stringField = (record: Record<string, unknown>, field: string): string => {
  const value = record[field];
  if (typeof value !== "string") throw invalidResponse(field);
  return value;
};

const booleanField = (record: Record<string, unknown>, field: string): boolean => {
  const value = record[field];
  if (typeof value !== "boolean") throw invalidResponse(field);
  return value;
};

const numberField = (record: Record<string, unknown>, field: string): number => {
  const value = record[field];
  if (typeof value !== "number" || !Number.isFinite(value)) throw invalidResponse(field);
  return value;
};

const invalidResponse = (field: string) =>
  new WorkspaceApiError("INVALID_RESPONSE", `Workspace 响应字段无效：${field}`, false);

export const decodeWorkspace = (value: unknown): Workspace => {
  if (!isRecord(value) || !isRecord(value.git)) throw invalidResponse("workspace");
  const rawWarnings: unknown = value.warnings ?? [];
  if (!Array.isArray(rawWarnings) || rawWarnings.some((item: unknown) => typeof item !== "string")) {
    throw invalidResponse("warnings");
  }
  const warnings = rawWarnings.map((item: unknown) => item as string);
  const status = stringField(value, "status");
  if (status !== "active") throw invalidResponse("status");
  return {
    id: stringField(value, "id"),
    name: stringField(value, "name"),
    rootPath: stringField(value, "root_path"),
    status,
    version: numberField(value, "version"),
    warnings,
    createdAt: stringField(value, "created_at"),
    updatedAt: stringField(value, "updated_at"),
    git: {
      present: booleanField(value.git, "present"),
      repositoryPath: stringField(value.git, "repository_path"),
      branch: stringField(value.git, "branch"),
      head: stringField(value.git, "head"),
      dirty: booleanField(value.git, "dirty"),
      checkedAt: stringField(value.git, "checked_at"),
    },
  };
};

export const decodeWorkspaceScan = (value: unknown): WorkspaceScan => {
  if (!isRecord(value) || !Array.isArray(value.files)) throw invalidResponse("scan");
  const files = value.files.map((item, index) => {
    if (!isRecord(item)) throw invalidResponse(`files[${String(index)}]`);
    return {
      relativePath: stringField(item, "relative_path"),
      byteSize: numberField(item, "byte_size"),
      contentHash: stringField(item, "content_hash"),
      mediaType: stringField(item, "media_type"),
      sourceId: stringField(item, "source_id"),
      sourceVersionId: stringField(item, "source_version_id"),
      contentArtifactId: stringField(item, "content_artifact_id"),
      contentArtifactCreated: booleanField(item, "content_artifact_created"),
    };
  });
  const count = numberField(value, "count");
  if (count !== files.length) throw invalidResponse("count");
  return { workspaceId: stringField(value, "workspace_id"), files, count };
};

const request = async (path: string, init?: RequestInit): Promise<unknown> => {
  let response: Response;
  try {
    response = await fetch(`${apiBaseUrl}${path}`, {
      headers: { Accept: "application/json", ...(init?.body === undefined ? {} : { "Content-Type": "application/json" }) },
      ...init,
    });
  } catch {
    throw new WorkspaceApiError("NETWORK_ERROR", "无法连接 Workspace API。", true);
  }
  let payload: unknown;
  try {
    payload = await response.json();
  } catch {
    throw new WorkspaceApiError("INVALID_RESPONSE", "Workspace API 返回了无效 JSON。", false);
  }
  if (!response.ok) {
    if (isRecord(payload) && typeof payload.error_code === "string" && typeof payload.message === "string") {
      throw new WorkspaceApiError(payload.error_code, payload.message, payload.retryable === true);
    }
    throw new WorkspaceApiError("HTTP_ERROR", `Workspace 请求失败（HTTP ${String(response.status)}）。`, response.status >= 500);
  }
  return payload;
};

export const createWorkspace = async (input: CreateWorkspaceInput): Promise<Workspace> =>
  decodeWorkspace(await request("/api/v1/workspaces", {
    method: "POST",
    body: JSON.stringify({ name: input.name, root_path: input.rootPath, initialize_git: input.initializeGit }),
  }));

export const getWorkspace = async (id: string, signal?: AbortSignal): Promise<Workspace> =>
  decodeWorkspace(await request(`/api/v1/workspaces/${encodeURIComponent(id)}`, signal === undefined ? undefined : { signal }));

export const scanWorkspace = async (id: string): Promise<WorkspaceScan> =>
  decodeWorkspaceScan(await request(`/api/v1/workspaces/${encodeURIComponent(id)}/scan`, { method: "POST" }));
