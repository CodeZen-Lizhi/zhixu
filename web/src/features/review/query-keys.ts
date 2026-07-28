/** Review 资源必须按 Workspace 隔离，SSE 只能失效这些查询键。 */
export const reviewQueryKeys = {
  all: (workspaceId: string) => ["review", workspaceId] as const,
  decks: (workspaceId: string) => ["review", workspaceId, "decks"] as const,
  deck: (workspaceId: string, deckId: string) =>
    ["review", workspaceId, "decks", deckId] as const,
  cardsAll: (workspaceId: string) => ["review", workspaceId, "cards"] as const,
  cards: (workspaceId: string, deckId: string) =>
    ["review", workspaceId, "cards", deckId] as const,
  card: (workspaceId: string, cardId: string) =>
    ["review", workspaceId, "card", cardId] as const,
  dueAll: (workspaceId: string) => ["review", workspaceId, "due"] as const,
  due: (workspaceId: string, sessionId: string, deckId = "") =>
    ["review", workspaceId, "due", sessionId, deckId] as const,
  sessions: (workspaceId: string) =>
    ["review", workspaceId, "sessions"] as const,
  session: (workspaceId: string, sessionId: string) =>
    ["review", workspaceId, "sessions", sessionId] as const,
  learningPath: (workspaceId: string, answerId: string) =>
    ["review", workspaceId, "answers", answerId, "learning-path"] as const,
};
