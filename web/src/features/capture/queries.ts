import { useMutation, useQuery, useQueryClient, type QueryClient } from "@tanstack/react-query";

import {
  createCapture,
  getCapture,
  getKnowledgeProfile,
  listCaptures,
  retryCapture,
  retryKnowledgeProfile,
  type CaptureCreateInput,
  type CaptureListParams,
  type CaptureRetryInput,
  type KnowledgeProfileBinding,
  type KnowledgeProfileRetryInput,
} from "../../api/captures";
import { useActiveWorkspaceId } from "../../app/active-workspace";
import { captureQueryKeys } from "./query-keys";

/** 清理离开 Workspace 后不再可见的 Capture Server State。 */
export const clearCaptureWorkspaceQueries = (queryClient: QueryClient, workspaceId: string): void => {
  if (workspaceId === "") return;
  const queryKey = captureQueryKeys.all(workspaceId);
  void queryClient.cancelQueries({ queryKey });
  queryClient.removeQueries({ queryKey });
};

export const useCaptures = (params: CaptureListParams = {}) => {
  const workspaceId = useActiveWorkspaceId();
  return useQuery({
    queryKey: captureQueryKeys.list(workspaceId, params),
    queryFn: ({ signal }) => listCaptures(workspaceId, params, signal),
    enabled: workspaceId !== "",
    retry: false,
  });
};

export const useCapture = (captureId: string) => {
  const workspaceId = useActiveWorkspaceId();
  return useQuery({
    queryKey: captureQueryKeys.detail(workspaceId, captureId),
    queryFn: ({ signal }) => getCapture(workspaceId, captureId, signal),
    enabled: workspaceId !== "" && captureId !== "",
    retry: false,
  });
};

export const useKnowledgeProfile = (binding: KnowledgeProfileBinding | undefined) => {
  const workspaceId = useActiveWorkspaceId();
  const sourceVersionId = binding?.sourceVersionId ?? "";
  return useQuery({
    queryKey: captureQueryKeys.profile(workspaceId, sourceVersionId),
    queryFn: ({ signal }) => binding === undefined
      ? Promise.reject(new Error("Profile binding is unavailable"))
      : getKnowledgeProfile(binding, signal),
    enabled: workspaceId !== "" && binding?.workspaceId === workspaceId,
    retry: false,
  });
};

export const useCreateCapture = () => {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (input: CaptureCreateInput) => createCapture(input),
    retry: false,
    onSuccess: ({ capture }, input) => {
      queryClient.setQueryData(captureQueryKeys.detail(input.workspaceId, capture.id), capture);
      void queryClient.invalidateQueries({ queryKey: captureQueryKeys.lists(input.workspaceId), refetchType: "active" });
      void queryClient.invalidateQueries({ queryKey: ["business", input.workspaceId, "sources"], refetchType: "active" });
    },
  });
};

export const useRetryKnowledgeProfile = () => {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (input: KnowledgeProfileRetryInput) => retryKnowledgeProfile(input),
    retry: false,
    onSuccess: async ({ profile }, input) => {
      queryClient.setQueryData(captureQueryKeys.profile(input.workspaceId, input.sourceVersionId), profile);
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: captureQueryKeys.lists(input.workspaceId), refetchType: "active" }),
        input.captureId === undefined
          ? Promise.resolve()
          : queryClient.invalidateQueries({ queryKey: captureQueryKeys.detail(input.workspaceId, input.captureId), exact: true, refetchType: "active" }),
      ]);
    },
  });
};

export const useRetryCapture = () => {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (input: CaptureRetryInput) => retryCapture(input),
    retry: false,
    onSuccess: async (_result, input) => {
      await Promise.all([
        queryClient.invalidateQueries({
          queryKey: captureQueryKeys.lists(input.workspaceId),
          refetchType: "active",
        }),
        queryClient.invalidateQueries({
          queryKey: captureQueryKeys.detail(input.workspaceId, input.captureId),
          exact: true,
          refetchType: "active",
        }),
      ]);
    },
  });
};
