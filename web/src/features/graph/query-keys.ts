import type {
  GraphFilterInput,
  GraphGlobalInput,
  GraphNeighborhoodInput,
  GraphNodeDetailInput,
  GraphNodeSearchInput,
  GraphPathInput,
  GraphRelationDetailInput,
  GraphRelationEvidenceInput,
} from "../../api/graph";
import {
  defaultGraphClaimStatuses,
  defaultGraphRelationStatuses,
  graphNodeTypes,
  graphRelationTypes,
} from "./options";
import { canonicalGraphTimestamp } from "./normalization";

export type GraphGlobalQueryInput = Omit<GraphGlobalInput, "cursor">;
export type GraphNeighborhoodQueryInput = Omit<GraphNeighborhoodInput, "cursor">;
export type GraphRelationEvidenceQueryInput = Omit<GraphRelationEvidenceInput, "cursor">;

const sortedSet = <T extends string>(values: readonly T[] | undefined, defaults: readonly T[] = []): T[] => {
  const source = values === undefined || values.length === 0 ? defaults : values;
  return [...source].sort();
};

const canonicalTimestamp = (value: string | undefined): string | null => {
  if (value === undefined) return null;
  return canonicalGraphTimestamp(value) ?? value;
};

const canonicalFilter = (filter: GraphFilterInput | undefined) => ({
  nodeTypes: sortedSet(filter?.nodeTypes, graphNodeTypes),
  relationTypes: sortedSet(filter?.relationTypes, graphRelationTypes),
  topicIds: sortedSet(filter?.topicIds),
  relationStatuses: sortedSet(filter?.relationStatuses, defaultGraphRelationStatuses),
  claimStatuses: sortedSet(filter?.claimStatuses, defaultGraphClaimStatuses),
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
      relationTypes: sortedSet(input.relationTypes, graphRelationTypes),
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
