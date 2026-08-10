import { collectionFieldOperators, collectionGroupFieldRegistry, collectionQueryFieldRegistry, collectionSortableFieldRegistry, collectionViewColumnRegistry, type CollectionOperator, type CollectionSortDirection, type CollectionViewType } from "../../api/collections";
import { isCanonicalUuid } from "../../shared/codec";

export interface CollectionUrlState {
  view: CollectionViewType;
  field: (typeof collectionQueryFieldRegistry)[number];
  operator: CollectionOperator;
  value: string;
  logic: "AND" | "OR";
  clauses: CollectionPredicateDraft[];
  sortField: (typeof collectionSortableFieldRegistry)[number];
  sortDirection: CollectionSortDirection;
  group: "" | (typeof collectionGroupFieldRegistry)[number];
  columns: string[];
  density: "COMFORTABLE" | "COMPACT";
  selected: string;
}

export interface CollectionPredicateDraft {
  field: (typeof collectionQueryFieldRegistry)[number];
  operator: CollectionOperator;
  value: string;
}

export interface CollectionUrlStateDefaults {
  view: CollectionViewType;
  group: CollectionUrlState["group"];
  columns: string[];
  fixedColumns?: string[];
  density: CollectionUrlState["density"];
  sortField: (typeof collectionSortableFieldRegistry)[number];
  sortDirection: CollectionSortDirection;
}

const views: readonly CollectionViewType[] = ["LIST", "TABLE", "COMPACT_CARD"];
const defaultColumns = ["object_type", "title", "summary", "status", "updated_at"];
const allowedColumns = new Set<string>(collectionViewColumnRegistry);
const pick = <T extends string>(value: string | null, allowed: readonly T[], fallback: T): T => value !== null && allowed.includes(value as T) ? value as T : fallback;
const single = (params: URLSearchParams, key: string): string | null => {
  const values = params.getAll(key);
  return values.length === 1 ? values[0] ?? null : null;
};
const defaultOperator = (field: CollectionPredicateDraft["field"]): CollectionOperator => collectionFieldOperators[field][0] ?? "EQ";
const normalizeOperator = (field: CollectionPredicateDraft["field"], value: string | null): CollectionOperator => pick(value, collectionFieldOperators[field], defaultOperator(field));
const normalizeSelected = (value: string): string => {
  const separator = value.indexOf(":");
  if (separator <= 0 || value.includes(":", separator + 1)) return "";
  const objectType = value.slice(0, separator);
  const objectId = value.slice(separator + 1);
  return (objectType === "TOPIC" || objectType === "CLAIM") && isCanonicalUuid(objectId) ? value : "";
};
const normalizeDraft = (value: unknown): CollectionPredicateDraft | null => {
  if (typeof value !== "object" || value === null || Array.isArray(value)) return null;
  const record = value as Record<string, unknown>;
  if (typeof record.field !== "string" || typeof record.operator !== "string" || typeof record.value !== "string") return null;
  const field = pick(record.field, collectionQueryFieldRegistry, "object_type");
  return { field, operator: normalizeOperator(field, record.operator), value: record.value.slice(0, 512) };
};

const parseClauses = (params: URLSearchParams, field: CollectionPredicateDraft["field"], operator: CollectionOperator, value: string): CollectionPredicateDraft[] => {
  const fallback = [{ field, operator, value }];
  const raw = single(params, "clauses");
  if (raw === null) return fallback;
  try {
    const parsed: unknown = JSON.parse(raw);
    if (!Array.isArray(parsed) || parsed.length === 0 || parsed.length > 63) return fallback;
    const clauses = parsed.map(normalizeDraft);
    return clauses.every((item): item is CollectionPredicateDraft => item !== null) ? clauses : fallback;
  } catch {
    return fallback;
  }
};

export const defaultCollectionUrlState: CollectionUrlStateDefaults = {
  view: "LIST",
  group: "",
  columns: defaultColumns,
  density: "COMFORTABLE",
  sortField: "updated_at",
  sortDirection: "DESC",
};

const normalizeColumns = (columns: string[], fixedColumns: string[] = []): string[] => {
  const normalized = columns.filter((item) => allowedColumns.has(item));
  const selected = normalized.length === 0 ? defaultColumns : [...new Set(normalized)];
  return [...new Set([...selected, ...fixedColumns.filter((item) => allowedColumns.has(item))])];
};

export const parseCollectionUrlState = (params: URLSearchParams, defaults: CollectionUrlStateDefaults = defaultCollectionUrlState): CollectionUrlState => {
  const rawField = single(params, "field");
  const field = pick(rawField, collectionQueryFieldRegistry, "object_type");
  const sortField = pick(single(params, "sort"), collectionSortableFieldRegistry, defaults.sortField);
  const rawGroup = single(params, "group");
  const group = rawGroup !== null && collectionGroupFieldRegistry.includes(rawGroup as (typeof collectionGroupFieldRegistry)[number]) ? rawGroup as CollectionUrlState["group"] : defaults.group;
  const rawColumns = single(params, "columns");
  const columns = rawColumns === null ? normalizeColumns(defaults.columns, defaults.fixedColumns) : normalizeColumns(rawColumns.split(","), defaults.fixedColumns);
  const selected = single(params, "selected") ?? "";
  const operator = normalizeOperator(field, single(params, "operator"));
  const value = (rawField === field ? (single(params, "value") ?? (field === "object_type" ? "TOPIC" : "")) : (field === "object_type" ? "TOPIC" : "")).slice(0, 512);
  const clauses = parseClauses(params, field, operator, value);
  return {
    view: pick(single(params, "view"), views, defaults.view),
    field: clauses[0]?.field ?? field,
    operator: clauses[0]?.operator ?? operator,
    value: clauses[0]?.value ?? value,
    logic: single(params, "logic") === "OR" ? "OR" : "AND",
    clauses,
    sortField,
    sortDirection: single(params, "direction") === null ? defaults.sortDirection : single(params, "direction") === "ASC" ? "ASC" : "DESC",
    group,
    columns,
    density: single(params, "density") === null ? defaults.density : single(params, "density") === "COMPACT" ? "COMPACT" : "COMFORTABLE",
    selected: normalizeSelected(selected),
  };
};

export const writeCollectionUrlState = (state: CollectionUrlState, current: URLSearchParams, defaults: CollectionUrlStateDefaults = defaultCollectionUrlState): URLSearchParams => {
  void current;
  const next = new URLSearchParams();
  const write = (key: string, value: string, fallback = "") => { if (value === "" || value === fallback) next.delete(key); else next.set(key, value); };
  write("view", state.view, defaults.view);
  write("field", state.field, "object_type");
  write("operator", state.operator, defaultOperator(state.field));
  write("value", state.value, state.field === "object_type" ? "TOPIC" : "");
  write("logic", state.logic, "AND");
  write("clauses", JSON.stringify(state.clauses), JSON.stringify([{ field: state.field, operator: state.operator, value: state.value }]));
  write("sort", state.sortField, defaults.sortField);
  write("direction", state.sortDirection, defaults.sortDirection);
  write("group", state.group, defaults.group);
  write("columns", state.columns.join(","), normalizeColumns(defaults.columns, defaults.fixedColumns).join(","));
  write("density", state.density, defaults.density);
  write("selected", state.selected);
  return next;
};
