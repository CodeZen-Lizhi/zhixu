export const interviewQueryKeys = {
  all: (workspaceId: string) => ["interview", workspaceId] as const,
  sessions: (workspaceId: string) => ["interview", workspaceId, "sessions"] as const,
  session: (workspaceId: string, sessionId: string) => ["interview", workspaceId, "session", sessionId] as const,
};
