import type { QueryClient } from "@tanstack/react-query";

export const workspaceQueryRoots = [
  "workspace",
  "business",
  "captures",
  "authoring",
  "document-history",
  "organizing",
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

/** 先停止旧 Workspace 的所有查询，再移除其完整缓存族。 */
export const clearWorkspaceRuntimeState = async (
  queryClient: QueryClient,
  workspaceId: string,
): Promise<void> => {
  if (workspaceId === "") return;

  await Promise.all(workspaceQueryRoots.map(async (root) => {
    await queryClient.cancelQueries({ queryKey: [root, workspaceId] });
  }));
  for (const root of workspaceQueryRoots) {
    queryClient.removeQueries({ queryKey: [root, workspaceId] });
  }
};
