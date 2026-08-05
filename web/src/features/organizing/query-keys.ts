export const organizingQueryKeys = {
  all: (workspaceId: string) => ["organizing", workspaceId] as const,
  drafts: (workspaceId: string) => [...organizingQueryKeys.all(workspaceId), "draft"] as const,
  draft: (workspaceId: string, draftId: string) => [...organizingQueryKeys.drafts(workspaceId), draftId] as const,
  snapshots: (workspaceId: string) => [...organizingQueryKeys.all(workspaceId), "snapshot"] as const,
  snapshot: (workspaceId: string, snapshotId: string) => [...organizingQueryKeys.snapshots(workspaceId), snapshotId] as const,
  templates: (workspaceId: string) => [...organizingQueryKeys.all(workspaceId), "template"] as const,
  template: (workspaceId: string, templateId: string) => [...organizingQueryKeys.templates(workspaceId), templateId] as const,
  materialSearch: (workspaceId: string, query: string, kind: string) => [...organizingQueryKeys.all(workspaceId), "material-search", query, kind] as const,
  runs: (workspaceId: string) => [...organizingQueryKeys.all(workspaceId), "run"] as const,
  run: (workspaceId: string, snapshotId: string) => [...organizingQueryKeys.runs(workspaceId), snapshotId] as const,
};
