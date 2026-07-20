import type {
  GraphFilterInput,
  GraphGlobalInput,
  GraphNeighborhoodInput,
  GraphNodeDetailInput,
  GraphNodeSearchInput,
  GraphPathInput,
  GraphRelationDetailInput,
  GraphRelationEvidenceInput,
  NodeType,
  RelationStatus,
  RelationType,
  ClaimStatus,
} from "../../api/graph";

export type GraphGlobalQueryInput = Omit<GraphGlobalInput, "cursor">;
export type GraphNeighborhoodQueryInput = Omit<GraphNeighborhoodInput, "cursor">;
export type GraphRelationEvidenceQueryInput = Omit<GraphRelationEvidenceInput, "cursor">;

const allNodeTypes: NodeType[] = ["CLAIM", "TOPIC"];
const allRelationTypes: RelationType[] = [
  "BELONGS_TO", "CITES", "COMPLEMENTS", "CONFLICTS_WITH", "DERIVED_FROM",
  "DUPLICATES", "IMPACTS", "PREREQUISITE_OF", "SUPPORTS", "VERSION_OF",
];
const defaultRelationStatuses: RelationStatus[] = ["CONFIRMED"];
const defaultClaimStatuses: ClaimStatus[] = ["CONFIRMED", "DISPUTED"];
const rfc3339Pattern = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(?:\.(\d{1,9}))?(Z|([+-])(\d{2}):(\d{2}))$/;

const sortedSet = <T extends string>(values: readonly T[] | undefined, defaults: readonly T[] = []): T[] => {
  const source = values === undefined || values.length === 0 ? defaults : values;
  return [...source].sort();
};

const canonicalTimestamp = (value: string | undefined): string | null => {
  if (value === undefined) {
    return null;
  }
  const match = rfc3339Pattern.exec(value);
  if (match === null) {
    return value;
  }
  const [, yearRaw, monthRaw, dayRaw, hourRaw, minuteRaw, secondRaw, fractionRaw = "", zone, sign, offsetHourRaw, offsetMinuteRaw] = match;
  const year = Number(yearRaw);
  const month = Number(monthRaw);
  const day = Number(dayRaw);
  const hour = Number(hourRaw);
  const minute = Number(minuteRaw);
  const second = Number(secondRaw);
  const offsetHour = Number(offsetHourRaw ?? 0);
  const offsetMinute = Number(offsetMinuteRaw ?? 0);
  if (offsetHour > 23 || offsetMinute > 59) {
    return value;
  }

  const local = new Date(0);
  local.setUTCFullYear(year, month - 1, day);
  local.setUTCHours(hour, minute, second, 0);
  if (
    local.getUTCFullYear() !== year
    || local.getUTCMonth() !== month - 1
    || local.getUTCDate() !== day
    || local.getUTCHours() !== hour
    || local.getUTCMinutes() !== minute
    || local.getUTCSeconds() !== second
  ) {
    return value;
  }

  const offsetSign = zone === "Z" || sign === "+" ? 1 : -1;
  const offsetMilliseconds = offsetSign * (offsetHour * 60 + offsetMinute) * 60_000;
  const utc = new Date(local.getTime() - offsetMilliseconds);
  const fraction = fractionRaw.replace(/0+$/, "");
  return `${utc.toISOString().slice(0, 19)}${fraction === "" ? "" : `.${fraction}`}Z`;
};

const canonicalFilter = (filter: GraphFilterInput | undefined) => ({
  nodeTypes: sortedSet(filter?.nodeTypes, allNodeTypes),
  relationTypes: sortedSet(filter?.relationTypes, allRelationTypes),
  topicIds: sortedSet(filter?.topicIds),
  relationStatuses: sortedSet(filter?.relationStatuses, defaultRelationStatuses),
  claimStatuses: sortedSet(filter?.claimStatuses, defaultClaimStatuses),
  claimMinConfidence: filter?.claimMinConfidence ?? null,
  relationMinConfidence: filter?.relationMinConfidence ?? null,
  updatedAfter: canonicalTimestamp(filter?.updatedAfter),
});

export const graphQueryKeys = {
  all: (workspaceId: string) => ["graph", workspaceId] as const,
  global: (input: GraphGlobalQueryInput) => [
    ...graphQueryKeys.all(input.workspaceId),
    "global",
    { filter: canonicalFilter(input.filter), limit: input.limit ?? 25 },
  ] as const,
  nodeSearch: (input: GraphNodeSearchInput) => [
    ...graphQueryKeys.all(input.workspaceId),
    "nodes",
    "search",
    { query: input.query, limit: input.limit ?? 20 },
  ] as const,
  node: (input: GraphNodeDetailInput) => [
    ...graphQueryKeys.all(input.workspaceId),
    "nodes",
    input.nodeType,
    input.nodeId,
  ] as const,
  neighborhood: (input: GraphNeighborhoodQueryInput) => {
    const depth = input.depth ?? 1;
    return [
      ...graphQueryKeys.all(input.workspaceId),
      "neighborhood",
      {
        center: { ...input.center },
        depth,
        ...(depth === 1 ? { limit: input.limit ?? 25 } : {}),
        direction: input.direction ?? "BOTH",
        filter: canonicalFilter(input.filter),
        maxNodes: input.maxNodes ?? 500,
        maxEdges: input.maxEdges ?? 1000,
        maxFrontier: input.maxFrontier ?? 500,
      },
    ] as const;
  },
  path: (input: GraphPathInput) => [
    ...graphQueryKeys.all(input.workspaceId),
    "path",
    {
      from: { ...input.from },
      to: { ...input.to },
      direction: input.direction ?? "BOTH",
      relationTypes: sortedSet(input.relationTypes, allRelationTypes),
      maxDepth: input.maxDepth ?? 6,
      maxVisited: input.maxVisited ?? 500,
    },
  ] as const,
  relation: (input: GraphRelationDetailInput) => [
    ...graphQueryKeys.all(input.workspaceId),
    "relations",
    input.relationId,
  ] as const,
  evidence: (input: GraphRelationEvidenceQueryInput) => [
    ...graphQueryKeys.relation(input),
    "evidence",
    { limit: input.limit ?? 20 },
  ] as const,
};
