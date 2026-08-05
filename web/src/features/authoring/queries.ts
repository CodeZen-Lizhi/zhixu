import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import {
  createWorkingDraft,
  freezeWorkingDraft,
  getAuthoringOverview,
  getDocumentDraft,
  getWorkingDraft,
  publishArticleRevision,
  updateWorkingDraft,
  type CreateWorkingDraftInput,
  type DocumentDraftDetail,
  type FreezeWorkingDraftInput,
  type PublishRevisionInput,
  type UpdateWorkingDraftInput,
} from "../../api/authoring";
import { getActiveWorkspaceId, useActiveWorkspaceId } from "../../app/active-workspace";
import { authoringQueryKeys } from "./query-keys";

const remainsActive = (workspaceId: string): boolean => getActiveWorkspaceId() === workspaceId;

export const useAuthoringOverview = () => {
  const workspaceId = useActiveWorkspaceId();
  return useQuery({
    queryKey: authoringQueryKeys.overview(workspaceId),
    queryFn: ({ signal }) => getAuthoringOverview(workspaceId, signal),
    enabled: workspaceId !== "",
    retry: false,
  });
};

export const useWorkingDraft = (draftId: string) => {
  const workspaceId = useActiveWorkspaceId();
  return useQuery({
    queryKey: authoringQueryKeys.draft(workspaceId, draftId),
    queryFn: ({ signal }) => getWorkingDraft(workspaceId, draftId, signal),
    enabled: workspaceId !== "" && draftId !== "",
    retry: false,
  });
};

export const useDocumentDraft = (documentId: string) => {
  const workspaceId = useActiveWorkspaceId();
  return useQuery({
    queryKey: authoringQueryKeys.document(workspaceId, documentId),
    queryFn: ({ signal }) => getDocumentDraft(workspaceId, documentId, signal),
    enabled: workspaceId !== "" && documentId !== "",
    retry: false,
  });
};

export const useCreateWorkingDraft = () => {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (input: CreateWorkingDraftInput) => createWorkingDraft(input),
    retry: false,
    onSuccess: ({ workingDraft }, input) => {
      if (!remainsActive(input.workspaceId)) return;
      queryClient.setQueryData(authoringQueryKeys.draft(input.workspaceId, workingDraft.id), workingDraft);
      void queryClient.invalidateQueries({ queryKey: authoringQueryKeys.overview(input.workspaceId), refetchType: "active" });
    },
  });
};

export const useUpdateWorkingDraft = () => {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (input: UpdateWorkingDraftInput) => updateWorkingDraft(input),
    retry: false,
    onSuccess: ({ workingDraft }, input) => {
      if (!remainsActive(input.workspaceId)) return;
      queryClient.setQueryData(authoringQueryKeys.draft(input.workspaceId, input.draftId), workingDraft);
      void queryClient.invalidateQueries({ queryKey: authoringQueryKeys.overview(input.workspaceId), refetchType: "active" });
    },
  });
};

export const useFreezeWorkingDraft = () => {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (input: FreezeWorkingDraftInput) => freezeWorkingDraft(input),
    retry: false,
    onSuccess: (result, input) => {
      if (!remainsActive(input.workspaceId)) return;
      queryClient.setQueryData(authoringQueryKeys.draft(input.workspaceId, input.draftId), result.workingDraft);
      queryClient.setQueryData(authoringQueryKeys.document(input.workspaceId, result.document.id), {
        document: result.document,
        currentRevision: result.articleRevision,
        publication: null,
      });
      void queryClient.invalidateQueries({ queryKey: authoringQueryKeys.overview(input.workspaceId), refetchType: "active" });
    },
  });
};

export const usePublishArticleRevision = () => {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (input: PublishRevisionInput) => publishArticleRevision(input),
    retry: false,
    onSuccess: ({ publication }, input) => {
      if (!remainsActive(input.workspaceId)) return;
      queryClient.setQueryData<DocumentDraftDetail>(authoringQueryKeys.document(input.workspaceId, input.documentId), (current) =>
        current === undefined ? current : { ...current, publication });
      void queryClient.invalidateQueries({ queryKey: authoringQueryKeys.overview(input.workspaceId), refetchType: "active" });
    },
  });
};
