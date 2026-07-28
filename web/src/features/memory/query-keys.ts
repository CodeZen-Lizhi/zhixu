/** Memory 查询键始终以 Workspace 为首级边界。 */
export const memoryQueryKeys = {
  all: (workspaceId: string) => ["memory", workspaceId] as const,
  list: (workspaceId: string, types: readonly string[], statuses: readonly string[], cursor = "") => ["memory", workspaceId, "list", [...types].sort().join(","), [...statuses].sort().join(","), cursor] as const,
  item: (workspaceId: string, memoryId: string) => ["memory", workspaceId, "item", memoryId] as const,
};
