import { authFetch } from "./auth";

export type SystemOverallStatus = "ready" | "degraded";
export type DatabaseStatus = "ready" | "unavailable";
export type GraphCapabilityStatus = "ready" | "unavailable";
export type SemanticLinkCapabilityStatus = "ready" | "unavailable";
export type RAGCapabilityStatus = "ready" | "disabled" | "unavailable";
export type CollectionCapabilityStatus = "ready" | "unavailable";
export type KnowledgeHealthCapabilityStatus = "ready" | "unavailable";
export type KnowledgeTimelineCapabilityStatus = "ready" | "unavailable";
export type LearningCapabilityStatus = "ready" | "unavailable";
export type AuthCapabilityStatus = "ready" | "disabled" | "unavailable";

export interface SystemStatus {
  status: SystemOverallStatus;
  version: string;
  database: {
    status: DatabaseStatus;
    message?: string;
  };
  graph: {
    status: GraphCapabilityStatus;
    reason?: "graph_dependencies_unavailable";
  };
  semanticLinks: {
    status: SemanticLinkCapabilityStatus;
    reason?: "semantic_link_dependencies_unavailable";
  };
  rag: {
    status: RAGCapabilityStatus;
    reason?: "rag_dependencies_unavailable";
  };
  collections: {
    status: CollectionCapabilityStatus;
  };
  knowledgeHealth: {
    status: KnowledgeHealthCapabilityStatus;
  };
  knowledgeTimeline: {
    status: KnowledgeTimelineCapabilityStatus;
  };
  review: {
    status: LearningCapabilityStatus;
  };
  memory: {
    status: LearningCapabilityStatus;
  };
  interview: {
    status: LearningCapabilityStatus;
  };
  auth: {
    status: AuthCapabilityStatus;
    reason?: "auth_dependencies_unavailable";
  };
  requestId: string;
}

export class ApiBoundaryError extends Error {
  readonly code: "HTTP_ERROR" | "INVALID_RESPONSE" | "NETWORK_ERROR";
  readonly retryable: boolean;

  constructor(
    code: ApiBoundaryError["code"],
    message: string,
    retryable: boolean,
    options?: ErrorOptions,
  ) {
    super(message, options);
    this.name = "ApiBoundaryError";
    this.code = code;
    this.retryable = retryable;
  }
}

const isRecord = (value: unknown): value is Record<string, unknown> =>
  typeof value === "object" && value !== null && !Array.isArray(value);

const readNonEmptyString = (
  value: unknown,
  field: string,
): string => {
  if (typeof value !== "string" || value.trim() === "") {
    throw new ApiBoundaryError(
      "INVALID_RESPONSE",
      `系统状态响应缺少有效字段：${field}`,
      false,
    );
  }

  return value;
};

const readOverallStatus = (value: unknown): SystemOverallStatus => {
  if (value === "ready" || value === "degraded") {
    return value;
  }

  throw new ApiBoundaryError(
    "INVALID_RESPONSE",
    "系统状态响应包含未知 status",
    false,
  );
};

const readDatabaseStatus = (value: unknown): DatabaseStatus => {
  if (value === "ready" || value === "unavailable") {
    return value;
  }

  throw new ApiBoundaryError(
    "INVALID_RESPONSE",
    "系统状态响应包含未知 database.status",
    false,
  );
};

const readGraphStatus = (value: unknown): GraphCapabilityStatus => {
  if (value === "ready" || value === "unavailable") return value;
  throw new ApiBoundaryError("INVALID_RESPONSE", "系统状态响应包含未知 graph.status", false);
};

const readSemanticLinkStatus = (value: unknown): SemanticLinkCapabilityStatus => {
  if (value === "ready" || value === "unavailable") return value;
  throw new ApiBoundaryError("INVALID_RESPONSE", "系统状态响应包含未知 semantic_links.status", false);
};

const readRAGStatus = (value: unknown): RAGCapabilityStatus => {
  if (value === "ready" || value === "disabled" || value === "unavailable") return value;
  throw new ApiBoundaryError("INVALID_RESPONSE", "系统状态响应包含未知 rag.status", false);
};

const readBoundedCapabilityStatus = (value: unknown, field: string): "ready" | "unavailable" => {
  if (value === "ready" || value === "unavailable") return value;
  throw new ApiBoundaryError("INVALID_RESPONSE", `系统状态响应包含未知 ${field}.status`, false);
};

const readAuthStatus = (value: unknown): AuthCapabilityStatus => {
  if (value === "ready" || value === "disabled" || value === "unavailable") return value;
  throw new ApiBoundaryError("INVALID_RESPONSE", "系统状态响应包含未知 auth.status", false);
};

export const decodeSystemStatus = (value: unknown): SystemStatus => {
  if (!isRecord(value) || !isRecord(value.database) || !isRecord(value.graph) || !isRecord(value.semantic_links) || !isRecord(value.rag) || !isRecord(value.collections) || !isRecord(value.knowledge_health) || !isRecord(value.knowledge_timeline) || !isRecord(value.review) || !isRecord(value.memory) || !isRecord(value.interview) || !isRecord(value.auth)) {
    throw new ApiBoundaryError(
      "INVALID_RESPONSE",
      "系统状态响应结构无效",
      false,
    );
  }
  assertExactKeys(value, ["status", "version", "database", "graph", "semantic_links", "rag", "collections", "knowledge_health", "knowledge_timeline", "review", "memory", "interview", "auth", "request_id"], "root");
  assertExactKeys(value.database, ["status", "message"], "database");
  assertExactKeys(value.graph, ["status", "reason"], "graph");
  assertExactKeys(value.semantic_links, ["status", "reason"], "semantic_links");
  assertExactKeys(value.rag, ["status", "reason"], "rag");
  assertExactKeys(value.collections, ["status"], "collections");
  assertExactKeys(value.knowledge_health, ["status"], "knowledge_health");
  assertExactKeys(value.knowledge_timeline, ["status"], "knowledge_timeline");
  assertExactKeys(value.review, ["status"], "review");
  assertExactKeys(value.memory, ["status"], "memory");
  assertExactKeys(value.interview, ["status"], "interview");
  assertExactKeys(value.auth, ["status", "reason"], "auth");

  const message = value.database.message;
  if (message !== undefined && typeof message !== "string") {
    throw new ApiBoundaryError(
      "INVALID_RESPONSE",
      "系统状态响应包含无效 database.message",
      false,
    );
  }
  const graphReason = value.graph.reason;
  if (graphReason !== undefined && graphReason !== "graph_dependencies_unavailable") {
    throw new ApiBoundaryError("INVALID_RESPONSE", "系统状态响应包含无效 graph.reason", false);
  }
  const semanticLinkReason = value.semantic_links.reason;
  if (semanticLinkReason !== undefined && semanticLinkReason !== "semantic_link_dependencies_unavailable") {
    throw new ApiBoundaryError("INVALID_RESPONSE", "系统状态响应包含无效 semantic_links.reason", false);
  }
  const reason = value.rag.reason;
  if (reason !== undefined && reason !== "rag_dependencies_unavailable") {
    throw new ApiBoundaryError("INVALID_RESPONSE", "系统状态响应包含无效 rag.reason", false);
  }
  const authReason = value.auth.reason;
  if (authReason !== undefined && authReason !== "auth_dependencies_unavailable") {
    throw new ApiBoundaryError("INVALID_RESPONSE", "系统状态响应包含无效 auth.reason", false);
  }

  return {
    status: readOverallStatus(value.status),
    version: readNonEmptyString(value.version, "version"),
    database: {
      status: readDatabaseStatus(value.database.status),
      ...(message === undefined ? {} : { message }),
    },
    graph: {
      status: readGraphStatus(value.graph.status),
      ...(graphReason === undefined ? {} : { reason: graphReason }),
    },
    semanticLinks: {
      status: readSemanticLinkStatus(value.semantic_links.status),
      ...(semanticLinkReason === undefined ? {} : { reason: semanticLinkReason }),
    },
    rag: {
      status: readRAGStatus(value.rag.status),
      ...(reason === undefined ? {} : { reason }),
    },
    collections: { status: readBoundedCapabilityStatus(value.collections.status, "collections") },
    knowledgeHealth: { status: readBoundedCapabilityStatus(value.knowledge_health.status, "knowledge_health") },
    knowledgeTimeline: { status: readBoundedCapabilityStatus(value.knowledge_timeline.status, "knowledge_timeline") },
    review: { status: readBoundedCapabilityStatus(value.review.status, "review") },
    memory: { status: readBoundedCapabilityStatus(value.memory.status, "memory") },
    interview: { status: readBoundedCapabilityStatus(value.interview.status, "interview") },
    auth: { status: readAuthStatus(value.auth.status), ...(authReason === undefined ? {} : { reason: authReason }) },
    requestId: readNonEmptyString(value.request_id, "request_id"),
  };
};


export const fetchSystemStatus = async (
  signal?: AbortSignal,
): Promise<SystemStatus> => {
  let response: Response;
  try {
    response = await authFetch("/api/v1/system/status", {
      headers: { Accept: "application/json" },
      ...(signal === undefined ? {} : { signal }),
    });
  } catch (error: unknown) {
    if (error instanceof DOMException && error.name === "AbortError") {
      throw error;
    }
    throw new ApiBoundaryError(
      "NETWORK_ERROR",
      "无法连接 ZHIXU API，请确认服务已启动。",
      true,
      { cause: error },
    );
  }

  if (!response.ok) {
    throw new ApiBoundaryError(
      "HTTP_ERROR",
      `系统状态请求失败（HTTP ${String(response.status)}）`,
      response.status >= 500,
    );
  }

  let payload: unknown;
  try {
    payload = await response.json();
  } catch (error: unknown) {
    throw new ApiBoundaryError(
      "INVALID_RESPONSE",
      "系统状态响应不是有效 JSON",
      false,
      { cause: error },
    );
  }

  return decodeSystemStatus(payload);
};
const assertExactKeys = (value: Record<string, unknown>, allowed: readonly string[], field: string): void => {
  const keys = new Set(allowed);
  if (Object.keys(value).some((key) => !keys.has(key))) {
    throw new ApiBoundaryError("INVALID_RESPONSE", `系统状态响应包含未知字段：${field}`, false);
  }
};
