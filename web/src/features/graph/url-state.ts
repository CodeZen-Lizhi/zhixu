import type {
  GraphFilterInput,
  GraphNodeRef,
  NodeType,
  RelationStatus,
  RelationType,
  TraversalDirection,
} from "../../api/graph";
import {
  defaultGraphClaimStatuses,
  defaultGraphRelationStatuses,
  graphDepths,
  graphFilterClaimStatuses,
  graphModes,
  graphNodeTypes,
  graphRelationStatuses,
  graphRelationTypes,
  graphTraversalDirections,
  type GraphDepth,
  type GraphFilterClaimStatus,
  type GraphMode,
} from "./options";
import { canonicalGraphTimestamp } from "./normalization";

export interface GraphUrlFilter extends GraphFilterInput {
  nodeTypes: NodeType[];
  relationTypes: RelationType[];
  topicIds: string[];
  relationStatuses: RelationStatus[];
  claimStatuses: GraphFilterClaimStatus[];
  claimMinConfidence?: number;
  relationMinConfidence?: number;
  updatedAfter?: string;
}

export interface GraphUrlState {
  mode: GraphMode;
  center: GraphNodeRef | null;
  from: GraphNodeRef | null;
  to: GraphNodeRef | null;
  depth: GraphDepth;
  direction: TraversalDirection;
  candidateScanId: string | null;
  filter: GraphUrlFilter;
}

const uuidPattern = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;
const jsonNumberPattern = /^-?(?:0|[1-9]\d*)(?:\.\d+)?(?:[eE][+-]?\d+)?$/;
const compareStrings = (left: string, right: string): number => left < right ? -1 : left > right ? 1 : 0;

const singleValue = (parameters: URLSearchParams, name: string): string | undefined => {
  const values = [...new Set(parameters.getAll(name))];
  return values.length === 1 ? values[0] : undefined;
};

const findOption = <T extends string>(value: string | undefined, options: readonly T[]): T | undefined =>
  value === undefined ? undefined : options.find((option) => option === value);

const readEnumList = <T extends string>(
  parameters: URLSearchParams,
  name: string,
  options: readonly T[],
  defaults: readonly T[],
): T[] => {
  const values = parameters.getAll(name);
  if (values.length === 0) return [...defaults];

  const parsed: T[] = [];
  for (const value of values.flatMap((item) => item.split(","))) {
    const option = findOption(value, options);
    if (option === undefined) return [...defaults];
    parsed.push(option);
  }
  if (parsed.length === 0) return [...defaults];
  return [...new Set(parsed)].sort(compareStrings);
};

const readUuidList = (parameters: URLSearchParams, name: string): string[] => {
  const values = parameters.getAll(name);
  if (values.length === 0) return [];
  const parsed = values.flatMap((item) => item.split(","));
  if (parsed.length === 0 || parsed.some((value) => !uuidPattern.test(value))) return [];
  const unique = [...new Set(parsed)];
  return unique.length > 500 ? [] : unique.sort(compareStrings);
};

const readConfidence = (parameters: URLSearchParams, name: string): number | undefined => {
  const value = singleValue(parameters, name);
  if (value === undefined || !jsonNumberPattern.test(value)) return undefined;
  const parsed = Number(value);
  return Number.isFinite(parsed) && parsed >= 0 && parsed <= 1 ? parsed : undefined;
};

const readNodeRef = (parameters: URLSearchParams, prefix: "center" | "from" | "to"): GraphNodeRef | null => {
  const type = findOption(singleValue(parameters, `${prefix}_type`), graphNodeTypes);
  const id = singleValue(parameters, `${prefix}_id`);
  return type === undefined || id === undefined || !uuidPattern.test(id) ? null : { type, id };
};

const readFilter = (parameters: URLSearchParams): GraphUrlFilter => {
  const updatedAfterValue = singleValue(parameters, "updated_after");
  const updatedAfter = updatedAfterValue === undefined ? undefined : canonicalGraphTimestamp(updatedAfterValue);
  const claimMinConfidence = readConfidence(parameters, "claim_min_confidence");
  const relationMinConfidence = readConfidence(parameters, "relation_min_confidence");
  return {
    nodeTypes: readEnumList(parameters, "node_types", graphNodeTypes, graphNodeTypes),
    relationTypes: readEnumList(parameters, "relation_types", graphRelationTypes, graphRelationTypes),
    topicIds: readUuidList(parameters, "topic_ids"),
    relationStatuses: readEnumList(
      parameters,
      "relation_statuses",
      graphRelationStatuses,
      defaultGraphRelationStatuses,
    ),
    claimStatuses: readEnumList(
      parameters,
      "claim_statuses",
      graphFilterClaimStatuses,
      defaultGraphClaimStatuses,
    ),
    ...(claimMinConfidence === undefined ? {} : { claimMinConfidence }),
    ...(relationMinConfidence === undefined ? {} : { relationMinConfidence }),
    ...(updatedAfter === undefined ? {} : { updatedAfter }),
  };
};

const pathFilter = (filter: GraphUrlFilter): GraphUrlFilter => ({
  nodeTypes: [...graphNodeTypes],
  relationTypes: filter.relationTypes,
  topicIds: [],
  relationStatuses: [...defaultGraphRelationStatuses],
  claimStatuses: [...defaultGraphClaimStatuses],
});

export const parseGraphUrlState = (parameters: URLSearchParams): GraphUrlState => {
  const mode = findOption(singleValue(parameters, "mode"), graphModes) ?? "global";
  const depth = mode === "local"
    ? graphDepths.find((option) => String(option) === singleValue(parameters, "depth")) ?? 1
    : 1;
  const direction = mode === "local" || mode === "path"
    ? findOption(singleValue(parameters, "direction"), graphTraversalDirections) ?? "BOTH"
    : "BOTH";
  const parsedFilter = readFilter(parameters);
  const filter = mode === "path" ? pathFilter(parsedFilter) : parsedFilter;
  const center = mode === "local" ? readNodeRef(parameters, "center") : null;
  const from = mode === "path" ? readNodeRef(parameters, "from") : null;
  let to = mode === "path" ? readNodeRef(parameters, "to") : null;
  if (center !== null && !filter.nodeTypes.includes(center.type)) {
    filter.nodeTypes = [...graphNodeTypes];
  }
  if (center?.type === "TOPIC" && filter.topicIds.length > 0 && !filter.topicIds.includes(center.id)) {
    filter.topicIds = [];
  }
  if (from !== null && to !== null && from.type === to.type && from.id === to.id) to = null;
  return {
    mode,
    center,
    from,
    to,
    depth,
    direction,
    candidateScanId: (() => {
      const value = singleValue(parameters, "candidate_scan_id");
      return value !== undefined && uuidPattern.test(value) ? value : null;
    })(),
    filter,
  };
};

const sameValues = <T extends string>(left: readonly T[], right: readonly T[]): boolean =>
  left.length === right.length && left.every((value, index) => value === right[index]);

const setNonDefaultList = <T extends string>(
  parameters: URLSearchParams,
  name: string,
  values: readonly T[],
  defaults: readonly T[],
): void => {
  if (!sameValues(values, defaults)) parameters.set(name, values.join(","));
};

const normalizedFilter = (filter: GraphUrlFilter): GraphUrlFilter => {
  const parameters = new URLSearchParams();
  parameters.set("node_types", filter.nodeTypes.join(","));
  parameters.set("relation_types", filter.relationTypes.join(","));
  if (filter.topicIds.length > 0) parameters.set("topic_ids", filter.topicIds.join(","));
  parameters.set("relation_statuses", filter.relationStatuses.join(","));
  parameters.set("claim_statuses", filter.claimStatuses.join(","));
  if (filter.claimMinConfidence !== undefined) {
    parameters.set("claim_min_confidence", String(filter.claimMinConfidence));
  }
  if (filter.relationMinConfidence !== undefined) {
    parameters.set("relation_min_confidence", String(filter.relationMinConfidence));
  }
  if (filter.updatedAfter !== undefined) parameters.set("updated_after", filter.updatedAfter);
  return readFilter(parameters);
};

const validNodeRef = (value: GraphNodeRef | null): value is GraphNodeRef =>
  value !== null && findOption(value.type, graphNodeTypes) !== undefined && uuidPattern.test(value.id);

export const serializeGraphUrlState = (state: GraphUrlState): URLSearchParams => {
  const parameters = new URLSearchParams();
  const mode = findOption(state.mode, graphModes) ?? "global";
  const normalized = normalizedFilter(state.filter);
  const filter = mode === "path" ? pathFilter(normalized) : normalized;
  const center = mode === "local" && validNodeRef(state.center) ? state.center : null;
  const from = mode === "path" && validNodeRef(state.from) ? state.from : null;
  let to = mode === "path" && validNodeRef(state.to) ? state.to : null;
  if (center !== null && !filter.nodeTypes.includes(center.type)) filter.nodeTypes = [...graphNodeTypes];
  if (center?.type === "TOPIC" && filter.topicIds.length > 0 && !filter.topicIds.includes(center.id)) {
    filter.topicIds = [];
  }
  if (from !== null && to !== null && from.type === to.type && from.id === to.id) to = null;
  parameters.set("mode", mode);
  if (state.candidateScanId !== null && uuidPattern.test(state.candidateScanId)) {
    parameters.set("candidate_scan_id", state.candidateScanId);
  }

  if (mode === "local") {
    if (center !== null) {
      parameters.set("center_type", center.type);
      parameters.set("center_id", center.id);
    }
    const depth = graphDepths.find((option) => option === state.depth) ?? 1;
    const direction = findOption(state.direction, graphTraversalDirections) ?? "BOTH";
    parameters.set("depth", String(depth));
    parameters.set("direction", direction);
  } else if (mode === "path") {
    if (from !== null) {
      parameters.set("from_type", from.type);
      parameters.set("from_id", from.id);
    }
    if (to !== null) {
      parameters.set("to_type", to.type);
      parameters.set("to_id", to.id);
    }
    parameters.set("direction", findOption(state.direction, graphTraversalDirections) ?? "BOTH");
  }

  setNonDefaultList(parameters, "node_types", filter.nodeTypes, graphNodeTypes);
  setNonDefaultList(parameters, "relation_types", filter.relationTypes, graphRelationTypes);
  if (filter.topicIds.length > 0) parameters.set("topic_ids", filter.topicIds.join(","));
  setNonDefaultList(
    parameters,
    "relation_statuses",
    filter.relationStatuses,
    defaultGraphRelationStatuses,
  );
  setNonDefaultList(parameters, "claim_statuses", filter.claimStatuses, defaultGraphClaimStatuses);
  if (filter.claimMinConfidence !== undefined) {
    parameters.set("claim_min_confidence", String(filter.claimMinConfidence));
  }
  if (filter.relationMinConfidence !== undefined) {
    parameters.set("relation_min_confidence", String(filter.relationMinConfidence));
  }
  if (filter.updatedAfter !== undefined) parameters.set("updated_after", filter.updatedAfter);
  return parameters;
};
