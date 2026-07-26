/** Workspace 是 Artifact server state 的强制缓存边界。 */
export const artifactQueryKeys = {
  all: (workspaceId: string) => ["artifacts", workspaceId] as const,
  list: (workspaceId: string, cursor: string | undefined) => ["artifacts", workspaceId, "list", cursor ?? "first"] as const,
  detail: (workspaceId: string, artifactId: string) => ["artifacts", workspaceId, "detail", artifactId] as const,
  sectionGenerations: (workspaceId: string, artifactId: string) => ["artifacts", workspaceId, artifactId, "section-generations"] as const,
};
