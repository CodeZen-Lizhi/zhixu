import { useInfiniteQuery, useQuery } from "@tanstack/react-query";

import {
  findGraphPath,
  getGraphGlobalPage,
  getGraphNeighborhood,
  getGraphNodeDetail,
  getGraphRelationDetail,
  getGraphRelationEvidencePage,
  isValidGraphNodeSearchQuery,
  searchGraphNodes,
  type GraphNodeDetailInput,
  type GraphNodeSearchInput,
  type GraphPathInput,
  type GraphRelationDetailInput,
} from "../../api/graph";
import {
  graphQueryKeys,
  type GraphGlobalQueryInput,
  type GraphNeighborhoodQueryInput,
  type GraphRelationEvidenceQueryInput,
} from "./query-keys";

export const isGraphNodeSearchEnabled = (workspaceId: string, query: string): boolean => {
  return workspaceId !== "" && isValidGraphNodeSearchQuery(query);
};

export const useGraphGlobal = (input: GraphGlobalQueryInput) => useInfiniteQuery({
  queryKey: graphQueryKeys.global(input),
  queryFn: ({ signal, pageParam }) => getGraphGlobalPage({
    ...input,
    ...(pageParam === undefined ? {} : { cursor: pageParam }),
  }, signal),
  initialPageParam: undefined as string | undefined,
  getNextPageParam: (page) => page.meta.nextCursor,
  enabled: input.workspaceId !== "",
  retry: false,
});

export const useGraphNodeSearch = (input: GraphNodeSearchInput) => useQuery({
  queryKey: graphQueryKeys.nodeSearch(input),
  queryFn: ({ signal }) => searchGraphNodes(input, signal),
  enabled: isGraphNodeSearchEnabled(input.workspaceId, input.query),
  retry: false,
});

export const useGraphNeighborhood = (input: GraphNeighborhoodQueryInput) => useInfiniteQuery({
  queryKey: graphQueryKeys.neighborhood(input),
  queryFn: ({ signal, pageParam }) => getGraphNeighborhood({
    ...input,
    ...(pageParam === undefined ? {} : { cursor: pageParam }),
  }, signal),
  initialPageParam: undefined as string | undefined,
  getNextPageParam: (page) => page.meta.nextCursor,
  enabled: input.workspaceId !== "" && input.center.id !== "",
  retry: false,
});

export const useGraphPath = (input: GraphPathInput) => useQuery({
  queryKey: graphQueryKeys.path(input),
  queryFn: ({ signal }) => findGraphPath(input, signal),
  enabled: input.workspaceId !== "" && input.from.id !== "" && input.to.id !== "",
  retry: false,
});

export const useGraphNodeDetail = (input: GraphNodeDetailInput) => useQuery({
  queryKey: graphQueryKeys.node(input),
  queryFn: ({ signal }) => getGraphNodeDetail(input, signal),
  enabled: input.workspaceId !== "" && input.nodeId !== "",
  retry: false,
});

export const useGraphRelationDetail = (input: GraphRelationDetailInput) => useQuery({
  queryKey: graphQueryKeys.relation(input),
  queryFn: ({ signal }) => getGraphRelationDetail(input, signal),
  enabled: input.workspaceId !== "" && input.relationId !== "",
  retry: false,
});

export const useGraphRelationEvidence = (input: GraphRelationEvidenceQueryInput) => useInfiniteQuery({
  queryKey: graphQueryKeys.evidence(input),
  queryFn: ({ signal, pageParam }) => getGraphRelationEvidencePage({
    ...input,
    ...(pageParam === undefined ? {} : { cursor: pageParam }),
  }, signal),
  initialPageParam: undefined as string | undefined,
  getNextPageParam: (page) => page.meta.nextCursor,
  enabled: input.workspaceId !== "" && input.relationId !== "",
  retry: false,
});
