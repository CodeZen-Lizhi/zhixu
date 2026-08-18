/** Smart Collection 的唯一网络边界：所有 unknown 响应在这里解码为领域模型。 */

import { canonicalUuidPattern as uuidPattern, hasOnlyKeys, isAbortError, isRecord } from "../shared/codec";
import { CollectionsApi as GeneratedCollectionsApi } from "./generated/apis/CollectionsApi";
import type {
  CollectionDefinitionRequest as GeneratedCollectionDefinitionRequest,
  CollectionVersionedRequest as GeneratedCollectionVersionedRequest,
} from "./generated/models";
import {
  generatedConfiguration,
  generatedRawResponse,
  generatedRequestInit,
} from "./generated-client";

export type CollectionStatus = "ACTIVE" | "ARCHIVED";
export type CollectionViewType = "LIST" | "TABLE" | "COMPACT_CARD";
export type CollectionObjectType = "TOPIC" | "CLAIM";
export type CollectionOperator =
  | "EQ"
  | "IN"
  | "GTE"
  | "LTE"
  | "BETWEEN"
  | "IS_NULL"
  | "PREFIX"
  | "CONTAINS";
export type CollectionSortDirection = "ASC" | "DESC";

export const collectionQueryFieldRegistry = [
  "object_type", "topic_id", "status", "created_at", "updated_at", "confidence", "relation_type", "health_issue_type", "source_type", "file_path", "text",
] as const;
export type CollectionQueryField = (typeof collectionQueryFieldRegistry)[number];
export const collectionSortableFieldRegistry = ["object_type", "status", "created_at", "updated_at", "confidence", "relation_type", "health_issue_type", "source_type", "file_path", "text"] as const;
export const collectionGroupFieldRegistry = ["object_type", "topic_id", "status", "relation_type", "health_issue_type", "source_type"] as const;
export const collectionViewColumnRegistry = ["object_type", "title", "summary", "status", "topic", "source", "relation_summary", "health_summary", "confidence", "created_at", "updated_at"] as const;

export interface CollectionPredicate {
  kind: "predicate";
  field: string;
  operator: CollectionOperator;
  value?: string | number | boolean | null;
  values?: (string | number | boolean | null)[];
  lower?: string | number | boolean | null;
  upper?: string | number | boolean | null;
}

export interface CollectionGroup {
  kind: "group";
  operator: "AND" | "OR";
  clauses: CollectionClause[];
}

export type CollectionClause = CollectionPredicate | CollectionGroup;

export interface CollectionSortTerm {
  field: string;
  direction: CollectionSortDirection;
}

export interface CollectionQuery {
  schema_version: "collection-query/v1";
  root: CollectionClause;
  sort?: CollectionSortTerm[];
}

export interface CollectionGroupBy {
  field: string;
}

export interface CollectionViewConfig {
  columns: string[];
  fixedColumns: string[];
  sort: CollectionSortTerm[];
  groupBy: CollectionGroupBy | null;
  density: "COMFORTABLE" | "COMPACT";
}

export interface Collection {
  id: string;
  workspaceId: string;
  name: string;
  description: string;
  querySchemaVersion: string;
  queryVersion: number;
  query: CollectionQuery;
  queryHash: string;
  viewType: CollectionViewType;
  viewConfig: CollectionViewConfig;
  status: CollectionStatus;
  cachedResultVersion: string | null;
  lastExecutedAt: string | null;
  version: number;
  createdAt: string;
  updatedAt: string;
}

interface CollectionItemBase {
  objectType: CollectionObjectType;
  id: string;
  topicId: string | null;
  title: string;
  summary: string;
  status: string;
  relationTypes: string[];
  healthSummary: CollectionHealthSummary | null;
  relationType: string | null;
  healthIssueType: string | null;
  sourceType: string | null;
  filePath: string | null;
  createdAt: string;
  updatedAt: string;
}

export interface CollectionTopicItem extends CollectionItemBase {
  objectType: "TOPIC";
  confidence: null;
  applicability: null;
  applicabilitySchemaVersion: null;
  applicabilityHash: null;
  aliases: string[];
  sourceSummaries: [];
}

export interface CollectionClaimItem extends CollectionItemBase {
  objectType: "CLAIM";
  confidence: number | null;
  applicability: Record<string, unknown> | null;
  applicabilitySchemaVersion: string | null;
  applicabilityHash: string | null;
  aliases: [];
  sourceSummaries: CollectionSourceSummary[];
}

export type CollectionItem = CollectionTopicItem | CollectionClaimItem;

export interface CollectionSourceSummary {
  sourceType: string;
  filePath: string;
  supportType: string;
  createdAt: string;
}

export interface CollectionHealthSummary {
  count: number;
  maxSeverity: string;
  issueTypes: string[];
  summary: string;
}

export interface CollectionList {
  workspaceId: string;
  items: Collection[];
  nextCursor: string | null;
}

export interface CollectionResultPage {
  workspaceId: string;
  collectionId: string | null;
  queryHash: string;
  items: CollectionItem[];
  exactCount: number;
  nextCursor: string | null;
  revisionHash: string;
  scanRevisionHash: string;
}

export interface CollectionValidation {
  workspaceId: string;
  valid: boolean;
  query: CollectionQuery;
  queryHash: string;
  viewType: CollectionViewType;
  viewConfig: CollectionViewConfig;
}

export interface CollectionDefinitionInput {
  workspaceId: string;
  name: string;
  description?: string;
  query: CollectionQuery;
  viewType: CollectionViewType;
  viewConfig: CollectionViewConfig;
}

export interface CollectionVersionedInput extends CollectionDefinitionInput {
  expectedVersion: number;
}

export interface CollectionArchiveInput {
  workspaceId: string;
  expectedVersion: number;
}

export class CollectionApiError extends Error {
  readonly code: "INVALID_REQUEST" | "INVALID_RESPONSE" | "HTTP_ERROR" | "NETWORK_ERROR";
  readonly errorCode: string;
  readonly retryable: boolean;
  readonly status: number | null;

  constructor(code: CollectionApiError["code"], message: string, retryable: boolean, status: number | null = null, errorCode: string = code, options?: ErrorOptions) {
    super(message, options);
    this.name = "CollectionApiError";
    this.code = code;
    this.errorCode = errorCode;
    this.retryable = retryable;
    this.status = status;
  }
}

const hashPattern = /^[0-9a-f]{64}$/;
const timestampPattern = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})$/;
const collectionsApi = new GeneratedCollectionsApi(generatedConfiguration);

const exact = (value: Record<string, unknown>, keys: readonly string[], field: string): void => {
  if (!hasOnlyKeys(value, keys)) throw invalidResponse(field);
};
const invalidResponse = (field: string): CollectionApiError => new CollectionApiError("INVALID_RESPONSE", `Collection 响应字段无效：${field}`, false);
const invalidRequest = (field: string): CollectionApiError => new CollectionApiError("INVALID_REQUEST", `Collection 请求字段无效：${field}`, false);
const stringValue = (value: unknown, field: string, nonEmpty = true): string => {
  if (typeof value !== "string" || (nonEmpty && value.trim() === "")) throw invalidResponse(field);
  return value;
};
const optionalString = (value: unknown, field: string): string | null => value === undefined || value === null ? null : stringValue(value, field);
const uuid = (value: unknown, field: string): string => {
  const result = stringValue(value, field);
  if (!uuidPattern.test(result)) throw invalidResponse(field);
  return result;
};
const hash = (value: unknown, field: string): string => {
  const result = stringValue(value, field);
  if (!hashPattern.test(result)) throw invalidResponse(field);
  return result;
};
const timestamp = (value: unknown, field: string): string => {
  const result = stringValue(value, field);
  if (!isValidTimestamp(result)) throw invalidResponse(field);
  return result;
};
const integer = (value: unknown, field: string, minimum = 0): number => {
  if (typeof value !== "number" || !Number.isSafeInteger(value) || value < minimum) throw invalidResponse(field);
  return value;
};
const finiteNumber = (value: unknown, field: string): number => {
  if (typeof value !== "number" || !Number.isFinite(value)) throw invalidResponse(field);
  return value;
};
const nullableNumber = (value: unknown, field: string): number | null => {
  if (value === undefined || value === null) return null;
  const result = finiteNumber(value, field);
  if (result < 0 || result > 1) throw invalidResponse(field);
  return result;
};
const stringArray = (value: unknown, field: string): string[] => {
  if (value === undefined) return [];
  if (!Array.isArray(value)) throw invalidResponse(field);
  return value.map((item, index) => stringValue(item, `${field}[${String(index)}]`));
};
const cursor = (value: unknown, field: string, maxLength: number): string | null => {
  if (value === undefined || value === null) return null;
  const result = stringValue(value, field);
  if (result.length > maxLength) throw invalidResponse(field);
  return result;
};
const idempotencyKey = (value: string): string => {
  if (typeof value !== "string" || value.trim() === "" || value.length > 128) throw invalidRequest("idempotencyKey");
  return value;
};

const isValidTimestamp = (value: string): boolean => {
  if (!timestampPattern.test(value) || !Number.isFinite(Date.parse(value))) return false;
  const match = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})/.exec(value);
  if (match === null) return false;
  const [, rawYear, rawMonth, rawDay, rawHour, rawMinute, rawSecond] = match;
  const year = Number(rawYear); const month = Number(rawMonth); const day = Number(rawDay);
  const hour = Number(rawHour); const minute = Number(rawMinute); const second = Number(rawSecond);
  return month >= 1 && month <= 12 && day >= 1 && day <= new Date(Date.UTC(year, month, 0)).getUTCDate() && hour <= 23 && minute <= 59 && second <= 59;
};

class StrictJsonParser {
  private index = 0;
  private depth = 0;

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
    this.enterContainer();
    this.index += 1;
    this.skipWhitespace();
    const entries: [string, unknown][] = [];
    const keys = new Set<string>();
    if (this.source[this.index] === "}") { this.index += 1; this.leaveContainer(); return {}; }
    for (;;) {
      if (this.source[this.index] !== '"') throw new SyntaxError("invalid JSON object key");
      const key = this.parseString();
      if (keys.has(key)) throw new SyntaxError(`duplicate JSON key: ${key}`);
      keys.add(key);
      this.skipWhitespace();
      if (this.source[this.index] !== ":") throw new SyntaxError("missing JSON object colon");
      this.index += 1;
      this.skipWhitespace();
      entries.push([key, this.parseValue()]);
      this.skipWhitespace();
      const separator = this.source[this.index];
      if (separator === "}") { this.index += 1; this.leaveContainer(); return Object.fromEntries(entries); }
      if (separator !== ",") throw new SyntaxError("invalid JSON object separator");
      this.index += 1;
      this.skipWhitespace();
    }
  }

  private parseArray(): unknown[] {
    this.enterContainer();
    this.index += 1;
    this.skipWhitespace();
    const values: unknown[] = [];
    if (this.source[this.index] === "]") { this.index += 1; this.leaveContainer(); return values; }
    for (;;) {
      values.push(this.parseValue());
      this.skipWhitespace();
      const separator = this.source[this.index];
      if (separator === "]") { this.index += 1; this.leaveContainer(); return values; }
      if (separator !== ",") throw new SyntaxError("invalid JSON array separator");
      this.index += 1;
      this.skipWhitespace();
    }
  }

  private parseString(): string {
    const start = this.index;
    this.index += 1;
    while (this.index < this.source.length) {
      const current = this.source[this.index];
      if (current === '"') {
        this.index += 1;
        const parsed: unknown = JSON.parse(this.source.slice(start, this.index));
        if (typeof parsed !== "string") throw new SyntaxError("invalid JSON string");
        return parsed;
      }
      if (current === "\\") this.index += 1;
      this.index += 1;
    }
    throw new SyntaxError("unterminated JSON string");
  }

  private skipWhitespace(): void {
    while (this.index < this.source.length) {
      const current = this.source[this.index];
      if (current !== " " && current !== "\n" && current !== "\r" && current !== "\t") return;
      this.index += 1;
    }
  }

  private enterContainer(): void {
    this.depth += 1;
    if (this.depth > 256) throw new SyntaxError("JSON nesting is too deep");
  }

  private leaveContainer(): void { this.depth -= 1; }
}

const parseStrictJson = (source: string): unknown => new StrictJsonParser(source).parse();

const operators: readonly CollectionOperator[] = ["EQ", "IN", "GTE", "LTE", "BETWEEN", "IS_NULL", "PREFIX", "CONTAINS"];
const collectionStatuses: readonly string[] = ["ACTIVE", "ARCHIVED"];
export const collectionFieldOperators: Record<CollectionQueryField, readonly CollectionOperator[]> = {
  object_type: ["EQ", "IN"], topic_id: ["EQ", "IN"], status: ["EQ", "IN"], created_at: ["GTE", "LTE", "BETWEEN"], updated_at: ["GTE", "LTE", "BETWEEN"], confidence: ["GTE", "LTE", "BETWEEN", "IS_NULL"], relation_type: ["EQ", "IN"], health_issue_type: ["EQ", "IN"], source_type: ["EQ", "IN", "PREFIX"], file_path: ["EQ", "IN", "PREFIX"], text: ["CONTAINS", "PREFIX"],
};
export const collectionEnumValueRegistry = {
  object_type: ["TOPIC", "CLAIM"],
  status: ["ACTIVE", "MERGED", "DEPRECATED", "SUGGESTED", "CONFIRMED", "DISPUTED", "SUPERSEDED", "INVALID"],
  relation_type: ["BELONGS_TO", "SUPPORTS", "CITES", "DERIVED_FROM", "COMPLEMENTS", "DUPLICATES", "CONFLICTS_WITH", "PREREQUISITE_OF", "VERSION_OF", "IMPACTS"],
  health_issue_type: ["ORPHAN", "DUPLICATE", "CONFLICT", "STALE", "MISSING_SOURCE", "LOW_CONFIDENCE", "BROKEN_REFERENCE", "INDEX_ERROR", "SUPERSEDED_USAGE", "REVIEW_INVALIDATED"],
  source_type: ["markdown", "text", "pdf", "html", "file"],
} as const;
const statusValues = new Set<string>(collectionEnumValueRegistry.status);
const relationValues = new Set<string>(collectionEnumValueRegistry.relation_type);
const healthIssueValues = new Set<string>(collectionEnumValueRegistry.health_issue_type);
const sourceTypeValues = new Set<string>(collectionEnumValueRegistry.source_type);
const queryField = (value: unknown, field: string): CollectionQueryField => {
  const result = stringValue(value, field);
  if (!collectionQueryFieldRegistry.includes(result as CollectionQueryField)) throw invalidResponse(field);
  return result as CollectionQueryField;
};
const readScalar = (value: unknown, field: string): string | number | boolean | null => {
  if (value === null || typeof value === "string" || typeof value === "boolean") return value;
  if (typeof value === "number" && Number.isFinite(value)) return value;
  throw invalidResponse(field);
};
const validateFieldScalar = (queryFieldValue: CollectionQueryField, value: string | number | boolean | null, field: string): void => {
  if (queryFieldValue === "confidence") {
    if (typeof value !== "number" || value < 0 || value > 1) throw invalidResponse(field);
    return;
  }
  if (typeof value !== "string" || value.trim() === "" || value.includes("\0")) throw invalidResponse(field);
  if (queryFieldValue === "topic_id" && !uuidPattern.test(value)) throw invalidResponse(field);
  if ((queryFieldValue === "created_at" || queryFieldValue === "updated_at") && !isValidTimestamp(value)) throw invalidResponse(field);
  if (queryFieldValue === "object_type" && value !== "TOPIC" && value !== "CLAIM") throw invalidResponse(field);
  if (queryFieldValue === "status" && !statusValues.has(value)) throw invalidResponse(field);
  if (queryFieldValue === "relation_type" && !relationValues.has(value)) throw invalidResponse(field);
  if (queryFieldValue === "health_issue_type" && !healthIssueValues.has(value)) throw invalidResponse(field);
  if (queryFieldValue === "source_type" && !sourceTypeValues.has(value) && !sourceTypeValues.has(value.toLowerCase())) throw invalidResponse(field);
  if (queryFieldValue === "file_path" && (value.startsWith("/") || value.includes("\\") || value === ".." || value.startsWith("../"))) throw invalidResponse(field);
};
const decodeClause = (value: unknown, depth: number, counter = { value: 0 }): CollectionClause => {
  counter.value += 1;
  if (!isRecord(value) || depth > 3 || counter.value > 64) throw invalidResponse("query.root");
  const kind = stringValue(value.kind, "query.kind");
  if (kind === "group") {
    exact(value, ["kind", "operator", "clauses"], "query.group");
    const operator = stringValue(value.operator, "query.group.operator");
    if (operator !== "AND" && operator !== "OR") throw invalidResponse("query.group.operator");
    if (!Array.isArray(value.clauses) || value.clauses.length === 0 || value.clauses.length > 64) throw invalidResponse("query.group.clauses");
    return { kind: "group", operator, clauses: value.clauses.map((item) => decodeClause(item, depth + 1, counter)) };
  }
  if (kind !== "predicate") throw invalidResponse("query.kind");
  exact(value, ["kind", "field", "operator", "value", "values", "lower", "upper"], "query.predicate");
  const field = queryField(value.field, "query.predicate.field");
  const operatorValue = stringValue(value.operator, "query.predicate.operator");
  if (!operators.includes(operatorValue as CollectionOperator)) throw invalidResponse("query.predicate.operator");
  const operator = operatorValue as CollectionOperator;
  if (!collectionFieldOperators[field].includes(operator)) throw invalidResponse("query.predicate.operator");
  const result: CollectionPredicate = { kind: "predicate", field, operator };
  const hasValue = value.value !== undefined; const hasValues = value.values !== undefined; const hasLower = value.lower !== undefined; const hasUpper = value.upper !== undefined;
  if (["EQ", "GTE", "LTE", "PREFIX", "CONTAINS"].includes(operator) && (!hasValue || hasValues || hasLower || hasUpper)) throw invalidResponse("query.predicate.value");
  if (operator === "IN" && (hasValue || !hasValues || hasLower || hasUpper)) throw invalidResponse("query.predicate.values");
  if (operator === "BETWEEN" && (hasValue || hasValues || !hasLower || !hasUpper)) throw invalidResponse("query.predicate.range");
  if (operator === "IS_NULL" && (hasValue || hasValues || hasLower || hasUpper)) throw invalidResponse("query.predicate.is_null");
  if (hasValue) { result.value = readScalar(value.value, "query.predicate.value"); validateFieldScalar(field, result.value, "query.predicate.value"); }
  if (value.values !== undefined) {
    if (!Array.isArray(value.values) || value.values.length === 0 || value.values.length > 100) throw invalidResponse("query.predicate.values");
    result.values = value.values.map((item, index) => { const scalar = readScalar(item, `query.predicate.values[${String(index)}]`); validateFieldScalar(field, scalar, `query.predicate.values[${String(index)}]`); return scalar; });
  }
  if (value.lower !== undefined) { result.lower = readScalar(value.lower, "query.predicate.lower"); validateFieldScalar(field, result.lower, "query.predicate.lower"); }
  if (value.upper !== undefined) { result.upper = readScalar(value.upper, "query.predicate.upper"); validateFieldScalar(field, result.upper, "query.predicate.upper"); }
  if (operator === "BETWEEN" && typeof result.lower === "number" && typeof result.upper === "number" && result.lower > result.upper) throw invalidResponse("query.predicate.range");
  return result;
};
const decodeQuery = (value: unknown): CollectionQuery => {
  if (!isRecord(value)) throw invalidResponse("query");
  if (new TextEncoder().encode(JSON.stringify(value)).length > 32 * 1024) throw invalidResponse("query");
  exact(value, ["schema_version", "root", "sort"], "query");
  if (value.schema_version !== "collection-query/v1") throw invalidResponse("query.schema_version");
  if (!isRecord(value.root) || value.root.kind !== "group") throw invalidResponse("query.root");
  const result: CollectionQuery = { schema_version: "collection-query/v1", root: decodeClause(value.root, 1) };
  if (value.sort !== undefined) {
    if (!Array.isArray(value.sort) || value.sort.length > 3) throw invalidResponse("query.sort");
    result.sort = value.sort.map((item, index) => decodeSortTerm(item, `query.sort[${String(index)}]`));
  }
  return result;
};
const encodeQuery = (value: CollectionQuery): CollectionQuery => decodeQuery(value);
const decodeSortTerm = (value: unknown, field: string): CollectionSortTerm => {
  if (!isRecord(value)) throw invalidResponse(field);
  exact(value, ["field", "direction"], field);
  const direction = stringValue(value.direction, `${field}.direction`);
  if (direction !== "ASC" && direction !== "DESC") throw invalidResponse(`${field}.direction`);
  const sortField = stringValue(value.field, `${field}.field`);
  if (!collectionSortableFieldRegistry.includes(sortField as (typeof collectionSortableFieldRegistry)[number])) throw invalidResponse(`${field}.field`);
  return { field: sortField, direction };
};
const encodeViewConfig = (value: CollectionViewConfig): Record<string, unknown> => {
  decodeViewConfig({ columns: value.columns, fixed_columns: value.fixedColumns, sort: value.sort, ...(value.groupBy === null ? {} : { group_by: value.groupBy }), density: value.density });
  return { columns: value.columns, fixed_columns: value.fixedColumns, sort: value.sort, ...(value.groupBy === null ? {} : { group_by: value.groupBy }), density: value.density };
};
const decodeViewConfig = (value: unknown): CollectionViewConfig => {
  if (!isRecord(value)) throw invalidResponse("view_config");
  exact(value, ["columns", "fixed_columns", "sort", "group_by", "density"], "view_config");
  const readStrings = (input: unknown, field: string): string[] => {
    if (input === undefined) return [];
    if (!Array.isArray(input)) throw invalidResponse(field);
    const result = input.map((item, index) => stringValue(item, `${field}[${String(index)}]`));
    if (result.some((item) => !collectionViewColumnRegistry.includes(item as (typeof collectionViewColumnRegistry)[number])) || new Set(result).size !== result.length) throw invalidResponse(field);
    return result;
  };
  const sort = value.sort === undefined ? [] : (() => {
    if (!Array.isArray(value.sort) || value.sort.length > 3) throw invalidResponse("view_config.sort");
    return value.sort.map((item, index) => decodeSortTerm(item, `view_config.sort[${String(index)}]`));
  })();
  let groupBy: CollectionGroupBy | null = null;
  if (value.group_by !== undefined && value.group_by !== null) {
    if (!isRecord(value.group_by)) throw invalidResponse("view_config.group_by");
    exact(value.group_by, ["field"], "view_config.group_by");
    const groupField = stringValue(value.group_by.field, "view_config.group_by.field");
    if (!collectionGroupFieldRegistry.includes(groupField as (typeof collectionGroupFieldRegistry)[number])) throw invalidResponse("view_config.group_by.field");
    groupBy = { field: groupField };
  }
  const density = value.density === undefined ? "COMFORTABLE" : stringValue(value.density, "view_config.density");
  if (density !== "COMFORTABLE" && density !== "COMPACT") throw invalidResponse("view_config.density");
  const columns = readStrings(value.columns, "view_config.columns");
  const fixedColumns = readStrings(value.fixed_columns, "view_config.fixed_columns");
  if (fixedColumns.some((column) => !columns.includes(column))) throw invalidResponse("view_config.fixed_columns");
  return { columns, fixedColumns, sort, groupBy, density };
};
const decodeViewType = (value: unknown): CollectionViewType => {
  const result = stringValue(value, "view_type");
  if (result !== "LIST" && result !== "TABLE" && result !== "COMPACT_CARD") throw invalidResponse("view_type");
  return result;
};
const decodeCollection = (value: unknown): Collection => {
  if (!isRecord(value)) throw invalidResponse("collection");
  exact(value, ["id", "workspace_id", "name", "description", "query_schema_version", "query_version", "query", "query_hash", "view_type", "view_config", "status", "cached_result_version", "last_executed_at", "version", "created_at", "updated_at"], "collection");
  const status = stringValue(value.status, "collection.status");
  if (status !== "ACTIVE" && status !== "ARCHIVED") throw invalidResponse("collection.status");
  if (value.query_schema_version !== "collection-query/v1") throw invalidResponse("collection.query_schema_version");
  return { id: uuid(value.id, "collection.id"), workspaceId: uuid(value.workspace_id, "collection.workspace_id"), name: stringValue(value.name, "collection.name"), description: stringValue(value.description, "collection.description", false), querySchemaVersion: "collection-query/v1", queryVersion: integer(value.query_version, "collection.query_version", 1), query: decodeQuery(value.query), queryHash: hash(value.query_hash, "collection.query_hash"), viewType: decodeViewType(value.view_type), viewConfig: decodeViewConfig(value.view_config), status, cachedResultVersion: optionalString(value.cached_result_version, "collection.cached_result_version"), lastExecutedAt: value.last_executed_at === undefined || value.last_executed_at === null ? null : timestamp(value.last_executed_at, "collection.last_executed_at"), version: integer(value.version, "collection.version", 1), createdAt: timestamp(value.created_at, "collection.created_at"), updatedAt: timestamp(value.updated_at, "collection.updated_at") };
};
const decodeCollectionItem = (value: unknown): CollectionItem => {
  if (!isRecord(value)) throw invalidResponse("collection.item");
  exact(value, ["object_type", "id", "topic_id", "title", "summary", "status", "confidence", "applicability", "applicability_schema_version", "applicability_hash", "aliases", "source_summaries", "relation_types", "health_summary", "relation_type", "health_issue_type", "source_type", "file_path", "created_at", "updated_at"], "collection.item");
  const objectType = stringValue(value.object_type, "collection.item.object_type");
  if (objectType !== "TOPIC" && objectType !== "CLAIM") throw invalidResponse("collection.item.object_type");
  let applicability: Record<string, unknown> | null = null;
  if (value.applicability !== undefined && value.applicability !== null) {
    if (!isRecord(value.applicability)) throw invalidResponse("collection.item.applicability");
    applicability = value.applicability;
  }
  let sourceSummaries: CollectionSourceSummary[] = [];
  if (value.source_summaries !== undefined) {
    if (!Array.isArray(value.source_summaries)) throw invalidResponse("collection.item.source_summaries");
    sourceSummaries = value.source_summaries.map((item, index) => {
      const field = `collection.item.source_summaries[${String(index)}]`;
      if (!isRecord(item)) throw invalidResponse(field);
      exact(item, ["source_type", "file_path", "support_type", "created_at"], field);
      return { sourceType: stringValue(item.source_type, `${field}.source_type`), filePath: stringValue(item.file_path, `${field}.file_path`, false), supportType: stringValue(item.support_type, `${field}.support_type`), createdAt: timestamp(item.created_at, `${field}.created_at`) };
    });
  }
  let healthSummary: CollectionHealthSummary | null = null;
  if (value.health_summary !== undefined && value.health_summary !== null) {
    if (!isRecord(value.health_summary)) throw invalidResponse("collection.item.health_summary");
    exact(value.health_summary, ["count", "max_severity", "issue_types", "summary"], "collection.item.health_summary");
    healthSummary = { count: integer(value.health_summary.count, "collection.item.health_summary.count"), maxSeverity: stringValue(value.health_summary.max_severity, "collection.item.health_summary.max_severity"), issueTypes: stringArray(value.health_summary.issue_types, "collection.item.health_summary.issue_types"), summary: value.health_summary.summary === undefined ? "" : stringValue(value.health_summary.summary, "collection.item.health_summary.summary", false) };
  }
  const base = {
    objectType,
    id: uuid(value.id, "collection.item.id"),
    topicId: value.topic_id === undefined || value.topic_id === null ? null : uuid(value.topic_id, "collection.item.topic_id"),
    title: stringValue(value.title, "collection.item.title", false),
    summary: stringValue(value.summary, "collection.item.summary", false),
    status: stringValue(value.status, "collection.item.status"),
    relationTypes: stringArray(value.relation_types, "collection.item.relation_types"),
    healthSummary,
    relationType: optionalString(value.relation_type, "collection.item.relation_type"),
    healthIssueType: optionalString(value.health_issue_type, "collection.item.health_issue_type"),
    sourceType: optionalString(value.source_type, "collection.item.source_type"),
    filePath: optionalString(value.file_path, "collection.item.file_path"),
    createdAt: timestamp(value.created_at, "collection.item.created_at"),
    updatedAt: timestamp(value.updated_at, "collection.item.updated_at"),
  };
  const aliases = stringArray(value.aliases, "collection.item.aliases");
  const confidence = nullableNumber(value.confidence, "collection.item.confidence");
  const applicabilitySchemaVersion = optionalString(value.applicability_schema_version, "collection.item.applicability_schema_version");
  const applicabilityHash = value.applicability_hash === undefined || value.applicability_hash === "" ? null : hash(value.applicability_hash, "collection.item.applicability_hash");
  if (objectType === "TOPIC") {
    if (confidence !== null || applicability !== null || applicabilitySchemaVersion !== null || applicabilityHash !== null || sourceSummaries.length > 0) throw invalidResponse("collection.item.topic_fields");
    return { ...base, objectType, confidence: null, applicability: null, applicabilitySchemaVersion: null, applicabilityHash: null, aliases, sourceSummaries: [] };
  }
  if (aliases.length > 0) throw invalidResponse("collection.item.claim_fields");
  return { ...base, objectType, confidence, applicability, applicabilitySchemaVersion, applicabilityHash, aliases: [], sourceSummaries };
};
const decodeList = (value: unknown): CollectionList => {
  if (!isRecord(value)) throw invalidResponse("collection.list");
  exact(value, ["workspace_id", "items", "next_cursor"], "collection.list");
  if (!Array.isArray(value.items) || value.items.length > 100) throw invalidResponse("collection.list.items");
  const workspaceId = uuid(value.workspace_id, "collection.list.workspace_id");
  const items = value.items.map(decodeCollection);
  if (items.some((item) => item.workspaceId !== workspaceId)) throw invalidResponse("collection.list.items.workspace_id");
  if (new Set(items.map((item) => item.id)).size !== items.length) throw invalidResponse("collection.list.items.id");
  return { workspaceId, items, nextCursor: cursor(value.next_cursor, "collection.list.next_cursor", 32768) };
};
const decodeResultPageBase = (value: unknown, kind: "saved" | "preview"): CollectionResultPage => {
  if (!isRecord(value)) throw invalidResponse("collection.result_page");
  exact(value, ["workspace_id", "collection_id", "query_hash", "items", "exact_count", "next_cursor", "revision_hash", "scan_revision_hash"], "collection.result_page");
  if (!Array.isArray(value.items) || value.items.length > 100) throw invalidResponse("collection.result_page.items");
  let collectionId: string | null = null;
  if (kind === "saved") collectionId = uuid(value.collection_id, "collection.result_page.collection_id");
  else if (value.collection_id !== undefined && value.collection_id !== null) throw invalidResponse("collection.result_page.collection_id");
  const items = value.items.map(decodeCollectionItem);
  if (new Set(items.map((item) => `${item.objectType}:${item.id}`)).size !== items.length) throw invalidResponse("collection.result_page.items.id");
  const exactCount = integer(value.exact_count, "collection.result_page.exact_count");
  if (exactCount < items.length) throw invalidResponse("collection.result_page.exact_count");
  return { workspaceId: uuid(value.workspace_id, "collection.result_page.workspace_id"), collectionId, queryHash: hash(value.query_hash, "collection.result_page.query_hash"), items, exactCount, nextCursor: cursor(value.next_cursor, "collection.result_page.next_cursor", 32768), revisionHash: hash(value.revision_hash, "collection.result_page.revision_hash"), scanRevisionHash: hash(value.scan_revision_hash, "collection.result_page.scan_revision_hash") };
};
const decodeSavedResultPage = (value: unknown): CollectionResultPage => decodeResultPageBase(value, "saved");
const decodePreviewResultPage = (value: unknown): CollectionResultPage => decodeResultPageBase(value, "preview");
const decodeResultPage = decodeSavedResultPage;
const decodeValidation = (value: unknown): CollectionValidation => {
  if (!isRecord(value)) throw invalidResponse("collection.validation");
  exact(value, ["workspace_id", "valid", "query", "query_hash", "view_type", "view_config"], "collection.validation");
  if (value.valid !== true) throw invalidResponse("collection.validation.valid");
  return { workspaceId: uuid(value.workspace_id, "collection.validation.workspace_id"), valid: true, query: decodeQuery(value.query), queryHash: hash(value.query_hash, "collection.validation.query_hash"), viewType: decodeViewType(value.view_type), viewConfig: decodeViewConfig(value.view_config) };
};

const readProblem = (value: unknown, status: number): CollectionApiError => {
  if (isRecord(value) && typeof value.error_code === "string" && typeof value.message === "string" && typeof value.retryable === "boolean") return new CollectionApiError("HTTP_ERROR", value.message, value.retryable, status, value.error_code);
  return new CollectionApiError("HTTP_ERROR", `Collection 请求失败（HTTP ${String(status)}）`, status >= 500, status);
};
const request = async (operation: Promise<Response>): Promise<unknown> => {
  let response: Response;
  try { response = await operation; }
  catch (error: unknown) { if (isAbortError(error)) throw error; throw new CollectionApiError("NETWORK_ERROR", "无法连接 Collection API。", true, null, "NETWORK_ERROR", { cause: error }); }
  let payload: unknown;
  try { payload = parseStrictJson(await response.text()); } catch (error: unknown) { if (isAbortError(error)) throw error; throw new CollectionApiError("INVALID_RESPONSE", "Collection API 返回了无效或包含重复字段的 JSON。", false, response.status, "INVALID_RESPONSE", { cause: error }); }
  if (!response.ok) throw readProblem(payload, response.status);
  return payload;
};
const requireWorkspace = (workspaceId: string): string => { if (!uuidPattern.test(workspaceId)) throw invalidRequest("workspaceId"); return workspaceId; };
const requireCollectionId = (collectionId: string): string => { if (!uuidPattern.test(collectionId)) throw invalidRequest("collectionId"); return collectionId; };
const requireHash = (value: string, field: string): string => { if (!hashPattern.test(value)) throw invalidRequest(field); return value; };
const boundedLimit = (limit: number | undefined, fallback: number): number => {
  const value = limit ?? fallback;
  if (!Number.isSafeInteger(value) || value < 1 || value > 100) throw invalidRequest("limit");
  return value;
};
const definitionBody = (input: CollectionDefinitionInput): GeneratedCollectionDefinitionRequest => {
  const name = input.name.trim();
  if (name === "" || name.length > 256) throw invalidRequest("name");
  if ((input.description ?? "").length > 4096) throw invalidRequest("description");
  return { workspace_id: requireWorkspace(input.workspaceId), name, description: input.description ?? "", query: encodeQuery(input.query), view_type: input.viewType, view_config: encodeViewConfig(input.viewConfig) };
};
const versionedDefinitionBody = (input: CollectionVersionedInput): GeneratedCollectionVersionedRequest => {
  if (!Number.isSafeInteger(input.expectedVersion) || input.expectedVersion < 1) throw invalidRequest("expectedVersion");
  return { ...definitionBody(input), expected_version: input.expectedVersion };
};
const assertWorkspace = <T extends { workspaceId: string }>(value: T, workspaceId: string, field: string): T => {
  if (value.workspaceId !== workspaceId) throw invalidResponse(field);
  return value;
};
const assertCollection = (value: Collection, workspaceId: string, collectionId?: string): Collection => {
  assertWorkspace(value, workspaceId, "collection.workspace_id");
  if (collectionId !== undefined && value.id !== collectionId) throw invalidResponse("collection.id");
  return value;
};
const assertResultPage = (value: CollectionResultPage, workspaceId: string, collectionId: string | null, queryHash?: string): CollectionResultPage => {
  assertWorkspace(value, workspaceId, "collection.result_page.workspace_id");
  if (value.collectionId !== collectionId) throw invalidResponse("collection.result_page.collection_id");
  if (queryHash !== undefined && value.queryHash !== queryHash) throw invalidResponse("collection.result_page.query_hash");
  return value;
};

export const listCollections = (workspaceId: string, params: { status?: CollectionStatus[]; cursor?: string; limit?: number } = {}, signal?: AbortSignal): Promise<CollectionList> => {
  const expectedWorkspaceId = requireWorkspace(workspaceId);
  if (params.cursor !== undefined && (params.cursor.trim() === "" || params.cursor.length > 32768)) throw invalidRequest("cursor");
  const statuses = params.status?.map((status) => {
    if (!collectionStatuses.includes(status)) throw invalidRequest("status");
    return status;
  });
  return request(generatedRawResponse(collectionsApi.listCollectionsRaw({
    workspaceId: expectedWorkspaceId,
    ...(statuses === undefined || statuses.length === 0 ? {} : { status: statuses }),
    limit: boundedLimit(params.limit, 50),
    ...(params.cursor === undefined ? {} : { cursor: params.cursor }),
  }, generatedRequestInit(signal)))).then(decodeList).then((value) => assertWorkspace(value, expectedWorkspaceId, "collection.list.workspace_id"));
};
export const getCollection = (workspaceId: string, collectionId: string, signal?: AbortSignal): Promise<Collection> => {
  const expectedWorkspaceId = requireWorkspace(workspaceId);
  const expectedCollectionId = requireCollectionId(collectionId);
  return request(generatedRawResponse(collectionsApi.getCollectionRaw({
    collectionId: expectedCollectionId,
    workspaceId: expectedWorkspaceId,
  }, generatedRequestInit(signal)))).then(decodeCollection).then((value) => assertCollection(value, expectedWorkspaceId, expectedCollectionId));
};
export const getCollectionResults = (workspaceId: string, collectionId: string, expectedQueryHash: string, params: { cursor?: string; limit?: number } = {}, signal?: AbortSignal): Promise<CollectionResultPage> => {
  const expectedWorkspaceId = requireWorkspace(workspaceId);
  const expectedCollectionId = requireCollectionId(collectionId);
  const expectedHash = requireHash(expectedQueryHash, "queryHash");
  const requestCursor = params.cursor;
  if (requestCursor !== undefined && (requestCursor.trim() === "" || requestCursor.length > 32768)) throw invalidRequest("cursor");
  return request(generatedRawResponse(collectionsApi.getCollectionResultsRaw({
    collectionId: expectedCollectionId,
    workspaceId: expectedWorkspaceId,
    limit: boundedLimit(params.limit, 25),
    ...(requestCursor === undefined ? {} : { cursor: requestCursor }),
  }, generatedRequestInit(signal)))).then(decodeSavedResultPage).then((value) => assertResultPage(value, expectedWorkspaceId, expectedCollectionId, expectedHash));
};
export const previewCollection = (input: CollectionDefinitionInput & { cursor?: string; limit?: number }, signal?: AbortSignal): Promise<CollectionResultPage> => {
  const expectedWorkspaceId = requireWorkspace(input.workspaceId);
  if (input.cursor !== undefined && (input.cursor.trim() === "" || input.cursor.length > 32768)) throw invalidRequest("cursor");
  return request(generatedRawResponse(collectionsApi.previewCollectionRaw({
    collectionPreviewRequest: {
      workspace_id: expectedWorkspaceId,
      query: encodeQuery(input.query),
      view_type: input.viewType,
      view_config: encodeViewConfig(input.viewConfig),
      ...(input.cursor === undefined ? {} : { cursor: input.cursor }),
      limit: boundedLimit(input.limit, 25),
    },
  }, generatedRequestInit(signal)))).then(decodePreviewResultPage).then((value) => assertResultPage(value, expectedWorkspaceId, null));
};
export const validateCollection = (input: CollectionDefinitionInput, signal?: AbortSignal): Promise<CollectionValidation> => {
  const expectedWorkspaceId = requireWorkspace(input.workspaceId);
  return request(generatedRawResponse(collectionsApi.validateCollectionRaw({
    collectionValidationRequest: {
      workspace_id: expectedWorkspaceId,
      query: encodeQuery(input.query),
      view_type: input.viewType,
      view_config: encodeViewConfig(input.viewConfig),
    },
  }, generatedRequestInit(signal)))).then(decodeValidation).then((value) => assertWorkspace(value, expectedWorkspaceId, "collection.validation.workspace_id"));
};
export const createCollection = (input: CollectionDefinitionInput, idempotencyKeyValue: string, signal?: AbortSignal): Promise<Collection> => {
  const expectedWorkspaceId = requireWorkspace(input.workspaceId);
  return request(generatedRawResponse(collectionsApi.createCollectionRaw({
    idempotencyKey: idempotencyKey(idempotencyKeyValue),
    collectionDefinitionRequest: definitionBody(input),
  }, generatedRequestInit(signal)))).then(decodeCollection).then((value) => assertCollection(value, expectedWorkspaceId));
};
export const updateCollection = (collectionId: string, input: CollectionVersionedInput, idempotencyKeyValue: string, signal?: AbortSignal): Promise<Collection> => {
  const expectedWorkspaceId = requireWorkspace(input.workspaceId);
  const expectedCollectionId = requireCollectionId(collectionId);
  return request(generatedRawResponse(collectionsApi.updateCollectionRaw({
    collectionId: expectedCollectionId,
    idempotencyKey: idempotencyKey(idempotencyKeyValue),
    collectionVersionedRequest: versionedDefinitionBody(input),
  }, generatedRequestInit(signal)))).then(decodeCollection).then((value) => assertCollection(value, expectedWorkspaceId, expectedCollectionId));
};
export const archiveCollection = (collectionId: string, input: CollectionArchiveInput, idempotencyKeyValue: string, signal?: AbortSignal): Promise<Collection> => {
  const expectedWorkspaceId = requireWorkspace(input.workspaceId);
  const expectedCollectionId = requireCollectionId(collectionId);
  if (!Number.isSafeInteger(input.expectedVersion) || input.expectedVersion < 1) throw invalidRequest("expectedVersion");
  return request(generatedRawResponse(collectionsApi.archiveCollectionRaw({
    collectionId: expectedCollectionId,
    idempotencyKey: idempotencyKey(idempotencyKeyValue),
    collectionArchiveRequest: { workspace_id: expectedWorkspaceId, expected_version: input.expectedVersion },
  }, generatedRequestInit(signal)))).then(decodeCollection).then((value) => assertCollection(value, expectedWorkspaceId, expectedCollectionId));
};

export { decodeClause, decodeCollection, decodeCollectionItem, decodeList, decodePreviewResultPage, decodeQuery, decodeResultPage, decodeSavedResultPage, decodeValidation, decodeViewConfig };
