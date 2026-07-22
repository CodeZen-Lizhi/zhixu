export const healthQueryKeys = {
  all: (workspaceId: string) => ["knowledge-health", workspaceId] as const,
  summary: (workspaceId: string) => ["knowledge-health", workspaceId, "summary"] as const,
  issues: (workspaceId: string, request: string) => ["knowledge-health", workspaceId, "issues", request] as const,
  issue: (workspaceId: string, issueId: string) => ["knowledge-health", workspaceId, "issue", issueId] as const,
  scan: (workspaceId: string, scanId: string) => ["knowledge-health", workspaceId, "scan", scanId] as const,
};
