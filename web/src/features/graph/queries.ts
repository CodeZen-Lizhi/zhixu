import { type QueryClient, useInfiniteQuery, useQuery, useQueryClient } from "@tanstack/react-query";

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

/** 清理离开 Workspace 后不再可见的 Graph Server State。 */
export const clearGraphWorkspaceQueries = (queryClient: QueryClient, workspaceId: string): void => {
  if (workspaceId === "") return;
  const queryKey = graphQueryKeys.all(workspaceId);
  void queryClient.cancelQueries({ queryKey });
  queryClient.removeQueries({ queryKey });
};

/** 在详情 observer 退出后删除旧 Graph 详情与 Evidence。 */
export const clearGraphWorkspaceDetails = (queryClient: QueryClient, workspaceId: string): void => {
  if (workspaceId === "") return;
  const queryKey = graphQueryKeys.all(workspaceId);
  const predicate = (query: { queryKey: readonly unknown[] }) => {
    const resource = query.queryKey[2];
    const nodeType = query.queryKey[3];
    return resource === "relations" || resource === "nodes" && (nodeType === "TOPIC" || nodeType === "CLAIM");
  };
  void queryClient.cancelQueries({ queryKey, predicate });
  queryClient.removeQueries({ queryKey, predicate });
};

/** 删除指定 Relation 的 Evidence 分页缓存，同时保留 Relation detail。 */
export const clearGraphRelationEvidence = (
  queryClient: QueryClient,
  workspaceId: string,
  relationId: string,
): void => {
  if (workspaceId === "" || relationId === "") return;
  const queryKey = graphQueryKeys.relation({ workspaceId, relationId });
  const predicate = (query: { queryKey: readonly unknown[] }) => query.queryKey[4] === "evidence";
  void queryClient.cancelQueries({ queryKey, predicate });
  queryClient.removeQueries({ queryKey, predicate });
};

const useResetToFirstPage = (queryKey: readonly unknown[]) => {
  const queryClient = useQueryClient();
  return () => queryClient.resetQueries({ queryKey, exact: true });
};

export const useGraphGlobal = (input: GraphGlobalQueryInput) => {
  const queryKey = graphQueryKeys.global(input);
  const query = useInfiniteQuery({
    queryKey,
    queryFn: ({ signal, pageParam }) => getGraphGlobalPage({
      ...input,
      ...(pageParam === undefined ? {} : { cursor: pageParam }),
    }, signal),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (page) => page.meta.nextCursor,
    enabled: input.workspaceId !== "",
    retry: false,
  });
  return { ...query, resetToFirstPage: useResetToFirstPage(queryKey) };
};

export const useGraphNodeSearch = (input: GraphNodeSearchInput) => useQuery({
  queryKey: graphQueryKeys.nodeSearch(input),
  queryFn: ({ signal }) => searchGraphNodes(input, signal),
  enabled: isGraphNodeSearchEnabled(input.workspaceId, input.query),
  retry: false,
});

export const useGraphNeighborhood = (input: GraphNeighborhoodQueryInput) => {
  const queryKey = graphQueryKeys.neighborhood(input);
  const query = useInfiniteQuery({
    queryKey,
    queryFn: ({ signal, pageParam }) => getGraphNeighborhood({
      ...input,
      ...(pageParam === undefined ? {} : { cursor: pageParam }),
    }, signal),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (page) => page.meta.nextCursor,
    enabled: input.workspaceId !== "" && input.center.id !== "",
    retry: false,
  });
  return { ...query, resetToFirstPage: useResetToFirstPage(queryKey) };
};

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

export const useGraphRelationEvidence = (input: GraphRelationEvidenceQueryInput) => {
  const queryKey = graphQueryKeys.evidence(input);
  const query = useInfiniteQuery({
    queryKey,
    queryFn: ({ signal, pageParam }) => getGraphRelationEvidencePage({
      ...input,
      ...(pageParam === undefined ? {} : { cursor: pageParam }),
    }, signal),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (page) => page.meta.nextCursor,
    enabled: input.workspaceId !== "" && input.relationId !== "",
    retry: false,
  });
  return { ...query, resetToFirstPage: useResetToFirstPage(queryKey) };
};
