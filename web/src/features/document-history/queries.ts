import { useMutation, useQuery, useQueryClient, type QueryClient } from "@tanstack/react-query";
import { useCallback } from "react";

import {
  compareDocumentHistory,
  createDocumentRestorePreview,
  createDocumentRestoreProposal,
  isDocumentHistoryId,
  listDocumentHistory,
  type CompareDocumentHistoryInput,
  type CreateDocumentRestorePreviewInput,
  type CreateDocumentRestoreProposalInput,
} from "../../api/document-history";
import { getActiveWorkspaceId, useActiveWorkspaceId } from "../../app/active-workspace";
import { documentHistoryQueryKeys } from "./query-keys";

export const useDocumentHistoryPage = (documentId: string, head = "", cursor = "", limit = 30) => {
  const workspaceId = useActiveWorkspaceId();
  return useQuery({
    queryKey: documentHistoryQueryKeys.history(workspaceId, documentId, head, cursor, limit),
    queryFn: ({ signal }) => listDocumentHistory({
      workspaceId,
      documentId,
      ...(cursor === "" ? {} : { cursor }),
      limit,
    }, signal),
    enabled: workspaceId !== "" && isDocumentHistoryId(documentId),
    retry: false,
  });
};

export const recoverDocumentHistoryFirstPage = async (
  queryClient: QueryClient,
  workspaceId: string,
  documentId: string,
  limit = 30,
): Promise<void> => {
  await queryClient.fetchQuery({
    queryKey: documentHistoryQueryKeys.history(workspaceId, documentId, "", "", limit),
    queryFn: ({ signal }) => listDocumentHistory({ workspaceId, documentId, limit }, signal),
    staleTime: 0,
  });
};

export const useDocumentHistoryRecovery = (
  documentId: string,
  limit: number,
  resetLocalState: () => void,
) => {
  const workspaceId = useActiveWorkspaceId();
  const queryClient = useQueryClient();
  return useCallback(async (recoveringWorkspaceId: string): Promise<void> => {
    if (recoveringWorkspaceId !== workspaceId || !isDocumentHistoryId(documentId)) return;
    resetLocalState();
    await recoverDocumentHistoryFirstPage(queryClient, workspaceId, documentId, limit);
  }, [documentId, limit, queryClient, resetLocalState, workspaceId]);
};

export const useDocumentHistoryCompare = (
  input: Pick<CompareDocumentHistoryInput, "documentId" | "left" | "right"> | undefined,
  head: string,
  path: string,
  documentVersion: number,
) => {
  const workspaceId = useActiveWorkspaceId();
  const documentId = input?.documentId ?? "";
  const left = input?.left ?? "";
  const right = input?.right ?? "";
  return useQuery({
    queryKey: documentHistoryQueryKeys.compare(workspaceId, documentId, head, path, documentVersion, left, right),
    queryFn: ({ signal }) => compareDocumentHistory({ workspaceId, documentId, expectedHead: head, expectedPath: path, left, right }, signal),
    enabled: workspaceId !== "" && head !== "" && path !== "" && documentVersion > 0
      && input !== undefined && isDocumentHistoryId(documentId) && left !== right,
    retry: false,
  });
};

export const useCreateDocumentRestorePreview = () => useMutation({
  mutationFn: (input: CreateDocumentRestorePreviewInput) => createDocumentRestorePreview(input),
  retry: false,
});

export const useCreateDocumentRestoreProposal = () => {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (input: CreateDocumentRestoreProposalInput) => createDocumentRestoreProposal(input),
    retry: false,
    onSuccess: (_result, input) => {
      if (getActiveWorkspaceId() !== input.workspaceId) return;
      void queryClient.invalidateQueries({ queryKey: ["business", input.workspaceId, "proposals"] });
    },
  });
};
