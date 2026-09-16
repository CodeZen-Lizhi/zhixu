import { z } from "zod";
import { isAbortError, isRecord } from "../shared/codec";
import { WorkspacesApi } from "./generated/apis/WorkspacesApi";
import { generatedConfiguration, generatedRawResponse } from "./generated-client";

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
const workspacesApi = new WorkspacesApi(generatedConfiguration);

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

const scanWorkspaceResponse = async (workspaceId: string): Promise<unknown> => {
  let response: Response;
  try {
    response = await generatedRawResponse(workspacesApi.scanWorkspaceRaw({ workspaceId }));
  } catch (error: unknown) {
    if (isAbortError(error)) throw error;
    throw new WorkspaceApiError("NETWORK_ERROR", "无法连接 Workspace API。", true);
  }
  let payload: unknown;
  try {
    payload = await response.json();
  } catch (error: unknown) {
    if (isAbortError(error)) throw error;
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

export const scanWorkspace = async (id: string): Promise<WorkspaceScan> => {
  const result = decodeWorkspaceScan(await scanWorkspaceResponse(id));
  if (result.workspaceId !== id) throw invalidResponse("workspace binding");
  return result;
};

const discoveryPath = z.string().min(1).max(1024).refine((value) =>
  new TextEncoder().encode(value).length <= 1024 && !/[\\:\u0000-\u001f\u007f]/u.test(value) &&
  value.split("/").every((part) => part !== "" && ![".", "..", ".git", ".knowledge", "tmp", ".tmp"].includes(part)));
const discoveryItem = z.strictObject({
  workspace_id: z.uuid(), binding_version: z.number().int().nonnegative(), path: discoveryPath,
  stage: z.enum(["WALK", "OBSERVE", "REGISTER"]),
  code: z.enum(["DIRECTORY_READ_FAILED", "FILE_OBSERVATION_FAILED", "SOURCE_REGISTRATION_FAILED"]),
  status: z.enum(["FAILED", "RECOVERED"]), failure_count: z.number().int().positive(),
  last_failed_at: z.iso.datetime({ offset: true }), recovered_at: z.iso.datetime({ offset: true }).nullable(),
});
const discoveryPage = z.strictObject({
  workspace_id: z.uuid(), binding_version: z.number().int().nonnegative(),
  items: z.array(discoveryItem).max(100), next_cursor: z.union([z.literal(""), discoveryPath]),
});
export type DiscoveryFailure = z.infer<typeof discoveryItem>;
export type DiscoveryFailurePage = z.infer<typeof discoveryPage>;
export const decodeDiscoveryFailures = (value: unknown, workspaceId: string): DiscoveryFailurePage => {
  const parsed = discoveryPage.safeParse(value);
  if (!parsed.success) throw invalidResponse("discovery failures");
  const page = parsed.data;
  const codes = { WALK: "DIRECTORY_READ_FAILED", OBSERVE: "FILE_OBSERVATION_FAILED", REGISTER: "SOURCE_REGISTRATION_FAILED" };
  const paths = new Set<string>();
  if (page.workspace_id !== workspaceId || page.items.some((item) => {
    const duplicate = paths.has(item.path); paths.add(item.path);
    return duplicate || item.workspace_id !== workspaceId || item.binding_version !== page.binding_version ||
      item.code !== codes[item.stage] || (item.status === "FAILED") !== (item.recovered_at === null) ||
      (item.recovered_at !== null && Date.parse(item.recovered_at) < Date.parse(item.last_failed_at));
  }) || (page.next_cursor !== "" && page.next_cursor !== page.items.at(-1)?.path)) throw invalidResponse("discovery binding");
  return page;
};
export const listDiscoveryFailures = async (workspaceId: string, cursor = "", signal?: AbortSignal): Promise<DiscoveryFailurePage> => {
  if (!z.uuid().safeParse(workspaceId).success || (cursor !== "" && !discoveryPath.safeParse(cursor).success)) throw invalidResponse("discovery query");
  const response = await generatedRawResponse(workspacesApi.listWorkspaceDiscoveryFailuresRaw({ workspaceId, ...(cursor === "" ? {} : { cursor }), limit: 50 }, signal === undefined ? {} : { signal }));
  const payload: unknown = await response.json();
  if (!response.ok) {
    if (isRecord(payload) && typeof payload.error_code === "string" && typeof payload.message === "string" && typeof payload.retryable === "boolean") throw new WorkspaceApiError(payload.error_code, payload.message, payload.retryable);
    throw invalidResponse("discovery error");
  }
  return decodeDiscoveryFailures(payload, workspaceId);
};
