export type SearchMode = "keyword" | "semantic" | "hybrid";
export type IndexDegradedCapability = "vector";
export type SearchDegradationCapability = "vector" | "rerank";

export interface SearchFilters {
  sourceIds?: string[];
  sourceVersionIds?: string[];
  pathPrefixes?: string[];
  capturedAtFrom?: string;
  capturedAtBefore?: string;
}

export interface SearchInput {
  workspaceId: string;
  query: string;
  retrievalMode?: SearchMode;
  filters?: SearchFilters;
  cursor?: string;
  limit?: number;
}

export interface SearchDegradation {
  capability: SearchDegradationCapability;
  errorCode: string;
  retryable: boolean;
}

export interface EvidenceSpan {
  spanId: string;
  startLine: number;
  endLine: number;
  startByte: number;
  endByte: number;
}

export interface EvidenceProvenance {
  sourceId: string;
  sourceVersionId: string;
  relativePath: string;
  capturedAt: string;
  sourceVersionHref: string;
  sourceSpanHref: string;
}

export interface StageScore {
  rank: number;
  score: number;
}

export interface LexicalScore extends StageScore {
  ftsScore: number;
  trigramScore: number;
}

export interface VectorDistance {
  rank: number;
  distance: number;
}

export interface RerankScore extends StageScore {
  modelVersion: string;
}

export interface EvidenceScores {
  lexical: LexicalScore | null;
  vector: VectorDistance | null;
  fusion: StageScore;
  rerank: RerankScore | null;
}

export interface SearchEvidence {
  chunkId: string;
  parseProjectionId: string;
  sequence: number;
  contentHash: string;
  headingPath: string[];
  span: EvidenceSpan;
  snippet: string;
  provenances: EvidenceProvenance[];
  provenanceTruncated: boolean;
  scores: EvidenceScores;
}

export interface SearchResponse {
  workspaceId: string;
  indexVersionId: string;
  embeddingVersionId: string | null;
  requestedMode: SearchMode;
  effectiveMode: SearchMode;
  indexDegradedCapabilities: IndexDegradedCapability[];
  degradations: SearchDegradation[];
  items: SearchEvidence[];
  nextCursor?: string;
}

export class SearchApiError extends Error {
  readonly code: string;
  readonly retryable: boolean;
  readonly status: number | null;
  readonly details?: Readonly<Record<string, unknown>>;

  constructor(
    code: string,
    message: string,
    retryable: boolean,
    status: number | null = null,
    details?: Readonly<Record<string, unknown>>,
    options?: ErrorOptions,
  ) {
    super(message, options);
    this.name = "SearchApiError";
    this.code = code;
    this.retryable = retryable;
    this.status = status;
    if (details !== undefined) this.details = details;
  }
}

const apiBaseUrl = (import.meta.env.VITE_API_BASE_URL ?? "").replace(/\/$/, "");
const uuidPattern = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;
const hashPattern = /^[0-9a-f]{64}$/;
const rfc3339Pattern = /^\d{4}-(?:0[1-9]|1[0-2])-(?:0[1-9]|[12]\d|3[01])T(?:[01]\d|2[0-3]):[0-5]\d:[0-5]\d(?:\.\d{1,9})?(?:Z|[+-](?:[01]\d|2[0-3]):[0-5]\d)$/;
const textEncoder = new TextEncoder();
const maxCursorBytes = 2048;
const maxSnippetBytes = 4096;

const isRecord = (value: unknown): value is Record<string, unknown> =>
  typeof value === "object" && value !== null && !Array.isArray(value);

const assertExactKeys = (
  value: Record<string, unknown>,
  allowed: readonly string[],
  field: string,
): void => {
  const allowedKeys = new Set(allowed);
  if (Object.keys(value).some((key) => !allowedKeys.has(key))) throw invalidResponse(field);
};

const invalidResponse = (field: string) =>
  new SearchApiError("INVALID_RESPONSE", `Search 响应字段无效：${field}`, false);

const invalidRequest = (field: string) =>
  new SearchApiError("INVALID_REQUEST", `Search 请求字段无效：${field}`, false);

const readString = (value: unknown, field: string): string => {
  if (typeof value !== "string") throw invalidResponse(field);
  return value;
};

const readNonEmptyString = (value: unknown, field: string): string => {
  const result = readString(value, field);
  if (result === "" || result.trim() !== result) throw invalidResponse(field);
  return result;
};

const readBoundedString = (value: unknown, field: string, maxBytes: number): string => {
  const result = readNonEmptyString(value, field);
  if (textEncoder.encode(result).length > maxBytes) throw invalidResponse(field);
  return result;
};

const readBoolean = (value: unknown, field: string): boolean => {
  if (typeof value !== "boolean") throw invalidResponse(field);
  return value;
};

const readFiniteNumber = (value: unknown, field: string): number => {
  if (typeof value !== "number" || !Number.isFinite(value)) throw invalidResponse(field);
  return value;
};

const readInteger = (value: unknown, field: string, minimum: number, maximum?: number): number => {
  const result = readFiniteNumber(value, field);
  if (!Number.isSafeInteger(result) || result < minimum || (maximum !== undefined && result > maximum)) {
    throw invalidResponse(field);
  }
  return result;
};

const readUuid = (value: unknown, field: string): string => {
  const result = readString(value, field);
  if (!uuidPattern.test(result)) throw invalidResponse(field);
  return result;
};

const readHash = (value: unknown, field: string): string => {
  const result = readString(value, field);
  if (!hashPattern.test(result)) throw invalidResponse(field);
  return result;
};

const daysInMonth = (year: number, month: number): number => {
  if (month === 2) {
    const leap = year % 4 === 0 && (year % 100 !== 0 || year % 400 === 0);
    return leap ? 29 : 28;
  }
  return month === 4 || month === 6 || month === 9 || month === 11 ? 30 : 31;
};

const readTimestamp = (value: unknown, field: string): string => {
  const result = readString(value, field);
  if (!rfc3339Pattern.test(result)) throw invalidResponse(field);
  const year = Number(result.slice(0, 4));
  const month = Number(result.slice(5, 7));
  const day = Number(result.slice(8, 10));
  if (year < 1 || day > daysInMonth(year, month) || !Number.isFinite(Date.parse(result))) {
    throw invalidResponse(field);
  }
  return result;
};

const readSearchMode = (value: unknown, field: string): SearchMode => {
  if (value === "keyword" || value === "semantic" || value === "hybrid") return value;
  throw invalidResponse(field);
};

const readRelativePath = (value: unknown, field: string): string => {
  const result = readNonEmptyString(value, field);
  const segments = result.split("/");
  if (result.startsWith("/") || result.includes("\\") || segments.some((segment) => segment === "" || segment === "." || segment === "..")) {
    throw invalidResponse(field);
  }
  return result;
};

const readStringArray = (value: unknown, field: string, allowEmpty: boolean, maxItemBytes?: number): string[] => {
  if (!Array.isArray(value) || (!allowEmpty && value.length === 0)) throw invalidResponse(field);
  const values: unknown[] = value;
  return values.map((item, index) => maxItemBytes === undefined
    ? readNonEmptyString(item, `${field}[${String(index)}]`)
    : readBoundedString(item, `${field}[${String(index)}]`, maxItemBytes));
};

const readIndexCapabilities = (value: unknown): IndexDegradedCapability[] => {
  if (!Array.isArray(value)) throw invalidResponse("index_degraded_capabilities");
  const values: unknown[] = value;
  if (values.length > 1) throw invalidResponse("index_degraded_capabilities");
  const result: IndexDegradedCapability[] = values.map((item, index) => {
    if (item !== "vector") throw invalidResponse(`index_degraded_capabilities[${String(index)}]`);
    return "vector";
  });
  if (new Set(result).size !== result.length) throw invalidResponse("index_degraded_capabilities");
  return result;
};

const readStageScore = (
  value: unknown,
  field: string,
  allowedKeys: readonly string[] = ["rank", "score"],
): StageScore => {
  if (!isRecord(value)) throw invalidResponse(field);
  assertExactKeys(value, allowedKeys, field);
  return {
    rank: readInteger(value.rank, `${field}.rank`, 1, 500),
    score: readFiniteNumber(value.score, `${field}.score`),
  };
};

const readLexicalScore = (value: unknown): LexicalScore | null => {
  if (value === null) return null;
  if (!isRecord(value)) throw invalidResponse("scores.lexical");
  assertExactKeys(value, ["rank", "score", "fts_score", "trigram_score"], "scores.lexical");
  const stage = readStageScore(value, "scores.lexical", ["rank", "score", "fts_score", "trigram_score"]);
  return {
    ...stage,
    ftsScore: readFiniteNumber(value.fts_score, "scores.lexical.fts_score"),
    trigramScore: readFiniteNumber(value.trigram_score, "scores.lexical.trigram_score"),
  };
};

const readVectorDistance = (value: unknown): VectorDistance | null => {
  if (value === null) return null;
  if (!isRecord(value)) throw invalidResponse("scores.vector");
  assertExactKeys(value, ["rank", "distance"], "scores.vector");
  return {
    rank: readInteger(value.rank, "scores.vector.rank", 1, 500),
    distance: readFiniteNumber(value.distance, "scores.vector.distance"),
  };
};

const readRerankScore = (value: unknown): RerankScore | null => {
  if (value === null) return null;
  if (!isRecord(value)) throw invalidResponse("scores.rerank");
  assertExactKeys(value, ["rank", "score", "model_version"], "scores.rerank");
  const stage = readStageScore(value, "scores.rerank", ["rank", "score", "model_version"]);
  return {
    ...stage,
    modelVersion: readBoundedString(value.model_version, "scores.rerank.model_version", 256),
  };
};

const readSpan = (value: unknown, itemField: string): EvidenceSpan => {
  if (!isRecord(value)) throw invalidResponse(`${itemField}.span`);
  assertExactKeys(value, ["span_id", "start_line", "end_line", "start_byte", "end_byte"], `${itemField}.span`);
  const startLine = readInteger(value.start_line, `${itemField}.span.start_line`, 1);
  const endLine = readInteger(value.end_line, `${itemField}.span.end_line`, 1);
  const startByte = readInteger(value.start_byte, `${itemField}.span.start_byte`, 0);
  const endByte = readInteger(value.end_byte, `${itemField}.span.end_byte`, 0);
  if (endLine < startLine || endByte < startByte) throw invalidResponse(`${itemField}.span`);
  return {
    spanId: readUuid(value.span_id, `${itemField}.span.span_id`),
    startLine,
    endLine,
    startByte,
    endByte,
  };
};

const readProvenance = (
  value: unknown,
  field: string,
  workspaceId: string,
  spanId: string,
): EvidenceProvenance => {
  if (!isRecord(value)) throw invalidResponse(field);
  assertExactKeys(value, [
    "source_id", "source_version_id", "relative_path", "captured_at", "source_version_href", "source_span_href",
  ], field);
  const sourceVersionId = readUuid(value.source_version_id, `${field}.source_version_id`);
  const expectedVersionHref = `/api/v1/workspaces/${workspaceId}/source-versions/${sourceVersionId}`;
  const expectedSpanHref = `${expectedVersionHref}/spans/${spanId}`;
  const sourceVersionHref = readString(value.source_version_href, `${field}.source_version_href`);
  const sourceSpanHref = readString(value.source_span_href, `${field}.source_span_href`);
  if (sourceVersionHref !== expectedVersionHref || sourceSpanHref !== expectedSpanHref) {
    throw invalidResponse(`${field}.href`);
  }
  return {
    sourceId: readUuid(value.source_id, `${field}.source_id`),
    sourceVersionId,
    relativePath: readRelativePath(value.relative_path, `${field}.relative_path`),
    capturedAt: readTimestamp(value.captured_at, `${field}.captured_at`),
    sourceVersionHref,
    sourceSpanHref,
  };
};

const readDegradations = (value: unknown): SearchDegradation[] => {
  if (!Array.isArray(value)) throw invalidResponse("degradations");
  const values: unknown[] = value;
  if (values.length > 2) throw invalidResponse("degradations");
  const result = values.map((item, index): SearchDegradation => {
    const field = `degradations[${String(index)}]`;
    if (!isRecord(item)) throw invalidResponse(field);
    assertExactKeys(item, ["capability", "error_code", "retryable"], field);
    const capability = item.capability;
    if (capability !== "vector" && capability !== "rerank") throw invalidResponse(`${field}.capability`);
    return {
      capability,
      errorCode: readBoundedString(item.error_code, `${field}.error_code`, 128),
      retryable: readBoolean(item.retryable, `${field}.retryable`),
    };
  });
  if (new Set(result.map((item) => item.capability)).size !== result.length) throw invalidResponse("degradations");
  return result;
};

const readEvidence = (value: unknown, index: number, workspaceId: string): SearchEvidence => {
  const field = `items[${String(index)}]`;
  if (!isRecord(value) || !isRecord(value.scores)) throw invalidResponse(field);
  assertExactKeys(value, [
    "chunk_id", "parse_projection_id", "sequence", "content_hash", "heading_path", "span", "snippet",
    "provenances", "provenance_truncated", "scores",
  ], field);
  assertExactKeys(value.scores, ["lexical", "vector", "fusion", "rerank"], `${field}.scores`);
  const span = readSpan(value.span, field);
  if (!Array.isArray(value.provenances) || value.provenances.length === 0 || value.provenances.length > 8) {
    throw invalidResponse(`${field}.provenances`);
  }
  const provenances = value.provenances.map((item, provenanceIndex) =>
    readProvenance(item, `${field}.provenances[${String(provenanceIndex)}]`, workspaceId, span.spanId));
  const headingPath = readStringArray(value.heading_path, `${field}.heading_path`, true, 256);
  if (headingPath.length > 32) throw invalidResponse(`${field}.heading_path`);
  return {
    chunkId: readUuid(value.chunk_id, `${field}.chunk_id`),
    parseProjectionId: readUuid(value.parse_projection_id, `${field}.parse_projection_id`),
    sequence: readInteger(value.sequence, `${field}.sequence`, 0),
    contentHash: readHash(value.content_hash, `${field}.content_hash`),
    headingPath,
    span,
    snippet: readBoundedString(value.snippet, `${field}.snippet`, maxSnippetBytes),
    provenances,
    provenanceTruncated: readBoolean(value.provenance_truncated, `${field}.provenance_truncated`),
    scores: {
      lexical: readLexicalScore(value.scores.lexical),
      vector: readVectorDistance(value.scores.vector),
      fusion: readStageScore(value.scores.fusion, "scores.fusion"),
      rerank: readRerankScore(value.scores.rerank),
    },
  };
};

export const decodeSearchResponse = (value: unknown): SearchResponse => {
  if (!isRecord(value) || !Array.isArray(value.items) || value.items.length > 100) throw invalidResponse("response");
  assertExactKeys(value, [
    "workspace_id", "index_version_id", "embedding_version_id", "requested_mode", "effective_mode",
    "index_degraded_capabilities", "degradations", "items", "next_cursor",
  ], "response");
  const workspaceId = readUuid(value.workspace_id, "workspace_id");
  const embeddingVersionId = value.embedding_version_id === null
    ? null
    : readUuid(value.embedding_version_id, "embedding_version_id");
  const nextCursor = value.next_cursor;
  if (nextCursor !== undefined && (
    typeof nextCursor !== "string" || nextCursor === "" || nextCursor.trim() !== nextCursor ||
    textEncoder.encode(nextCursor).length > maxCursorBytes
  )) {
    throw invalidResponse("next_cursor");
  }
  return {
    workspaceId,
    indexVersionId: readUuid(value.index_version_id, "index_version_id"),
    embeddingVersionId,
    requestedMode: readSearchMode(value.requested_mode, "requested_mode"),
    effectiveMode: readSearchMode(value.effective_mode, "effective_mode"),
    indexDegradedCapabilities: readIndexCapabilities(value.index_degraded_capabilities),
    degradations: readDegradations(value.degradations),
    items: value.items.map((item, index) => readEvidence(item, index, workspaceId)),
    ...(nextCursor === undefined ? {} : { nextCursor }),
  };
};

const validateInputUuid = (value: string, field: string): void => {
  if (!uuidPattern.test(value)) throw invalidRequest(field);
};

const validateInputTimestamp = (value: string, field: string): void => {
  try {
    readTimestamp(value, field);
  } catch {
    throw invalidRequest(field);
  }
};

const validateInputMode = (value: unknown): void => {
  if (value === undefined || value === "keyword" || value === "semantic" || value === "hybrid") return;
  throw invalidRequest("retrievalMode");
};

const encodeSearchInput = (input: SearchInput): Record<string, unknown> => {
  validateInputUuid(input.workspaceId, "workspaceId");
  if (typeof input.query !== "string" || input.query.includes("\0") ||
      input.query.trim() === "" || textEncoder.encode(input.query.trim()).length > 8 * 1024) {
    throw invalidRequest("query");
  }
  if (input.limit !== undefined && (!Number.isSafeInteger(input.limit) || input.limit < 1 || input.limit > 100)) {
    throw invalidRequest("limit");
  }
  if (input.cursor !== undefined && (
    typeof input.cursor !== "string" || input.cursor === "" || input.cursor.trim() !== input.cursor ||
    textEncoder.encode(input.cursor).length > maxCursorBytes
  )) {
    throw invalidRequest("cursor");
  }
  validateInputMode(input.retrievalMode);
  const filters = input.filters;
  let encodedFilters: Record<string, unknown> | undefined;
  if (filters !== undefined) {
    for (const [field, values] of [["sourceIds", filters.sourceIds], ["sourceVersionIds", filters.sourceVersionIds]] as const) {
      if (values !== undefined) {
        if (values.length > 100) throw invalidRequest(field);
        values.forEach((value) => validateInputUuid(value, field));
      }
    }
    if (filters.pathPrefixes !== undefined && (filters.pathPrefixes.length > 100 || filters.pathPrefixes.some((value) => value.trim() === ""))) {
      throw invalidRequest("pathPrefixes");
    }
    if (filters.capturedAtFrom !== undefined) validateInputTimestamp(filters.capturedAtFrom, "capturedAtFrom");
    if (filters.capturedAtBefore !== undefined) validateInputTimestamp(filters.capturedAtBefore, "capturedAtBefore");
    encodedFilters = {
      ...(filters.sourceIds === undefined ? {} : { source_ids: filters.sourceIds }),
      ...(filters.sourceVersionIds === undefined ? {} : { source_version_ids: filters.sourceVersionIds }),
      ...(filters.pathPrefixes === undefined ? {} : { path_prefixes: filters.pathPrefixes }),
      ...(filters.capturedAtFrom === undefined ? {} : { captured_at_from: filters.capturedAtFrom }),
      ...(filters.capturedAtBefore === undefined ? {} : { captured_at_before: filters.capturedAtBefore }),
    };
  }
  return {
    workspace_id: input.workspaceId,
    query: input.query,
    ...(input.retrievalMode === undefined ? {} : { retrieval_mode: input.retrievalMode }),
    ...(encodedFilters === undefined ? {} : { filters: encodedFilters }),
    ...(input.cursor === undefined ? {} : { cursor: input.cursor }),
    ...(input.limit === undefined ? {} : { limit: input.limit }),
  };
};

const decodeProblem = (value: unknown, status: number): SearchApiError => {
  if (!isRecord(value)) {
    return new SearchApiError("INVALID_RESPONSE", "Search API 返回了无效 Problem。", false, status);
  }
  try {
    assertExactKeys(value, ["error_code", "message", "retryable", "workflow_run_id", "details"], "problem");
    if (value.workflow_run_id !== undefined) readUuid(value.workflow_run_id, "problem.workflow_run_id");
    const details = value.details;
    if (details !== undefined && !isRecord(details)) throw invalidResponse("problem.details");
    return new SearchApiError(
      readNonEmptyString(value.error_code, "problem.error_code"),
      readNonEmptyString(value.message, "problem.message"),
      readBoolean(value.retryable, "problem.retryable"),
      status,
      details,
    );
  } catch {
    return new SearchApiError("INVALID_RESPONSE", "Search API 返回了无效 Problem。", false, status);
  }
};

export const search = async (input: SearchInput, signal?: AbortSignal): Promise<SearchResponse> => {
  const body = encodeSearchInput(input);
  let response: Response;
  try {
    response = await fetch(`${apiBaseUrl}/api/v1/search`, {
      method: "POST",
      headers: { Accept: "application/json", "Content-Type": "application/json" },
      body: JSON.stringify(body),
      ...(signal === undefined ? {} : { signal }),
    });
  } catch (error: unknown) {
    if (error instanceof DOMException && error.name === "AbortError") throw error;
    throw new SearchApiError("NETWORK_ERROR", "无法连接 Search API。", true, null, undefined, { cause: error });
  }

  let payload: unknown;
  try {
    payload = await response.json();
  } catch (error: unknown) {
    throw new SearchApiError("INVALID_RESPONSE", "Search API 返回了无效 JSON。", false, response.status, undefined, { cause: error });
  }
  if (!response.ok) throw decodeProblem(payload, response.status);
  const result = decodeSearchResponse(payload);
  if (result.workspaceId !== input.workspaceId) throw invalidResponse("workspace_id");
  if (result.requestedMode !== (input.retrievalMode ?? "hybrid")) throw invalidResponse("requested_mode");
  return result;
};
