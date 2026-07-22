import type { QueryClient } from "@tanstack/react-query";
import type { SearchMode } from "../../api/search";

export interface SearchQueryIdentity {
  workspaceId: string;
  query: string;
  mode: SearchMode;
  sourceVersionId: string;
  cursor: string;
}

export const searchQueryKeys = {
  workspace: (workspaceId: string) => ["search", workspaceId] as const,
  page: (identity: SearchQueryIdentity) => [
    ...searchQueryKeys.workspace(identity.workspaceId),
    "page",
    identity.query,
    identity.mode,
    identity.sourceVersionId,
    identity.cursor,
  ] as const,
};

export const clearSearchWorkspaceQueries = (queryClient: QueryClient, workspaceId: string): void => {
  if (workspaceId === "") return;
  const queryKey = searchQueryKeys.workspace(workspaceId);
  void queryClient.cancelQueries({ queryKey });
  queryClient.removeQueries({ queryKey });
};

export const resetSearchWorkspaceQueriesForRecovery = async (queryClient: QueryClient, workspaceId: string): Promise<void> => {
  if (workspaceId === "") return;
  const queryKey = searchQueryKeys.workspace(workspaceId);
  await queryClient.cancelQueries({ queryKey });
  queryClient.removeQueries({ queryKey });
};
