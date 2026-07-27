export const collectionExportQueryKeys = {
  all: (workspaceId: string) => ["collection-exports", workspaceId] as const,
  list: (workspaceId: string, collectionId: string, cursor = "") => ["collection-exports", workspaceId, collectionId, cursor] as const,
  detail: (workspaceId: string, exportId: string) => ["collection-exports", workspaceId, "detail", exportId] as const,
};
