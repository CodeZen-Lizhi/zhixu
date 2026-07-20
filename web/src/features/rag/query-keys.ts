export const ragQueryKeys = {
  all: (workspaceId: string) => ["rag", workspaceId] as const,
  conversations: (workspaceId: string) => [...ragQueryKeys.all(workspaceId), "conversations"] as const,
  conversation: (workspaceId: string, conversationId: string) =>
    [...ragQueryKeys.conversations(workspaceId), "detail", conversationId] as const,
  turns: (workspaceId: string, conversationId: string) =>
    [...ragQueryKeys.conversation(workspaceId, conversationId), "turns"] as const,
  latestTurn: (workspaceId: string, conversationId: string) =>
    [...ragQueryKeys.turns(workspaceId, conversationId), "latest"] as const,
  answer: (workspaceId: string, answerId: string) =>
    [...ragQueryKeys.all(workspaceId), "answer", answerId] as const,
};
