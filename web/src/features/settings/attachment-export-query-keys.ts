export const attachmentExportQueryKeys = {
  all: (workspaceId: string) => ["workspace-attachment-exports", workspaceId] as const,
  list: (workspaceId: string, cursor = "") => ["workspace-attachment-exports", workspaceId, "WORKSPACE_ATTACHMENTS", cursor] as const,
  detail: (workspaceId: string, exportId: string) => ["workspace-attachment-exports", workspaceId, "detail", exportId] as const,
};
