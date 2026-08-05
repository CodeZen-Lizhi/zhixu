export const authoringQueryKeys = {
  all: (workspaceId: string) => ["authoring", workspaceId] as const,
  overview: (workspaceId: string) => [...authoringQueryKeys.all(workspaceId), "overview"] as const,
  drafts: (workspaceId: string) => [...authoringQueryKeys.all(workspaceId), "working-draft"] as const,
  draft: (workspaceId: string, draftId: string) => [...authoringQueryKeys.drafts(workspaceId), draftId] as const,
  documents: (workspaceId: string) => [...authoringQueryKeys.all(workspaceId), "document"] as const,
  document: (workspaceId: string, documentId: string) => [...authoringQueryKeys.documents(workspaceId), documentId] as const,
};
