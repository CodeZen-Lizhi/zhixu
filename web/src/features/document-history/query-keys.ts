import type { QueryClient } from "@tanstack/react-query";

export const documentHistoryQueryKeys = {
  all: (workspaceId: string) => ["document-history", workspaceId] as const,
  documents: (workspaceId: string) => [...documentHistoryQueryKeys.all(workspaceId), "documents"] as const,
  document: (workspaceId: string, documentId: string) => [...documentHistoryQueryKeys.documents(workspaceId), documentId] as const,
  history: (workspaceId: string, documentId: string, head: string, cursor: string, limit: number) => [
    ...documentHistoryQueryKeys.document(workspaceId, documentId),
    "history",
    head === "" ? "HEAD_DISCOVERY" : head,
    cursor === "" ? "FIRST_PAGE" : cursor,
    limit,
  ] as const,
  comparisons: (workspaceId: string, documentId: string) => [
    ...documentHistoryQueryKeys.document(workspaceId, documentId),
    "comparisons",
  ] as const,
  compare: (
    workspaceId: string,
    documentId: string,
    head: string,
    path: string,
    documentVersion: number,
    left: string,
    right: string,
  ) => [
    ...documentHistoryQueryKeys.comparisons(workspaceId, documentId),
    head,
    path,
    documentVersion,
    left,
    right,
  ] as const,
};

export const resetDocumentHistoryWorkspaceQueriesForRecovery = async (
  queryClient: QueryClient,
  workspaceId: string,
): Promise<void> => {
  if (workspaceId === "") return;
  const queryKey = documentHistoryQueryKeys.all(workspaceId);
  await queryClient.cancelQueries({ queryKey });
  queryClient.removeQueries({ queryKey });
};
