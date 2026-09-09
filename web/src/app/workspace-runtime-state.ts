import type { QueryClient } from "@tanstack/react-query";

export const workspaceQueryRoots = [
  "workspace",
  "business",
  "captures",
  "authoring",
  "document-history",
  "organizing",
  "synthesis",
  "search",
  "rag",
  "collections",
  "collection-exports",
  "workspace-attachment-exports",
  "knowledge-health",
  "graph",
  "semantic-links",
  "timeline",
  "review",
  "memory",
  "interview",
  "artifacts",
] as const;

const isWorkspaceQueryKey = (queryKey: readonly unknown[], workspaceId: string): boolean => {
  if (queryKey[0] === "active-workspace") return false;
  if (typeof queryKey[0] === "string" && workspaceQueryRoots.includes(queryKey[0] as typeof workspaceQueryRoots[number])) {
    return queryKey[1] === workspaceId;
  }
  return queryKey[0] === "settings" && queryKey[1] === "git-sync" && queryKey[2] === workspaceId;
};

/** 先停止旧 Workspace 的所有查询，再移除其完整缓存族。 */
export const clearWorkspaceRuntimeState = async (
  queryClient: QueryClient,
  workspaceId: string,
): Promise<void> => {
  if (workspaceId === "") return;

  await queryClient.cancelQueries({ predicate: (query) => isWorkspaceQueryKey(query.queryKey, workspaceId) });
  queryClient.removeQueries({ predicate: (query) => isWorkspaceQueryKey(query.queryKey, workspaceId) });
  queryClient.getMutationCache().clear();
};
