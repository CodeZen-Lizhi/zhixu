import { useMutation, useQuery, useQueryClient, type QueryClient } from "@tanstack/react-query";
import { useEffect, useRef } from "react";

import { createAttachmentExport, getAttachmentExport, listAttachmentExports, type AttachmentExportCreateInput, type AttachmentExportJob, type AttachmentExportPage } from "../../api/attachment-exports";
import { useActiveWorkspaceId } from "../../app/active-workspace";
import { attachmentExportQueryKeys } from "./attachment-export-query-keys";

export const attachmentExportPollMilliseconds = 2_000;
export const isActiveAttachmentExport = (job: AttachmentExportJob): boolean => job.status === "PENDING" || job.status === "RUNNING";
export const attachmentExportPollInterval = (page: AttachmentExportPage | undefined): number | false => page?.items.some(isActiveAttachmentExport) === true ? attachmentExportPollMilliseconds : false;

export const clearAttachmentExportWorkspaceQueries = (queryClient: QueryClient, workspaceId: string): void => {
  if (workspaceId === "") return;
  const queryKey = attachmentExportQueryKeys.all(workspaceId);
  void queryClient.cancelQueries({ queryKey });
  queryClient.removeQueries({ queryKey });
};

export const useAttachmentExports = (cursor?: string) => {
  const workspaceId = useActiveWorkspaceId();
  return useQuery({
    queryKey: attachmentExportQueryKeys.list(workspaceId, cursor),
    queryFn: ({ signal }) => listAttachmentExports(workspaceId, { ...(cursor === undefined ? {} : { cursor }) }, signal),
    enabled: workspaceId !== "",
    retry: false,
    refetchInterval: (query) => attachmentExportPollInterval(query.state.data),
  });
};

export const useAttachmentExport = (exportId: string, polling = false) => {
  const workspaceId = useActiveWorkspaceId();
  return useQuery({
    queryKey: attachmentExportQueryKeys.detail(workspaceId, exportId),
    queryFn: ({ signal }) => getAttachmentExport(workspaceId, exportId, signal),
    enabled: workspaceId !== "" && exportId !== "",
    retry: false,
    refetchInterval: (query) => polling && query.state.data !== undefined && isActiveAttachmentExport(query.state.data) ? attachmentExportPollMilliseconds : false,
  });
};

export const useCreateAttachmentExport = () => {
  const queryClient = useQueryClient();
  const workspaceId = useActiveWorkspaceId();
  const activeWorkspace = useRef(workspaceId);
  activeWorkspace.current = workspaceId;
  const previousWorkspace = useRef(workspaceId);
  const inFlight = useRef<{ workspaceId: string; controller: AbortController } | null>(null);
  const mutation = useMutation({
    mutationFn: (input: AttachmentExportCreateInput) => {
      inFlight.current?.controller.abort();
      const controller = new AbortController();
      inFlight.current = { workspaceId: input.workspaceId, controller };
      return createAttachmentExport(input, controller.signal).finally(() => {
        if (inFlight.current?.controller === controller) inFlight.current = null;
      });
    },
    onSuccess: (result, input) => {
      if (activeWorkspace.current !== input.workspaceId) return;
      queryClient.setQueryData(attachmentExportQueryKeys.detail(input.workspaceId, result.job.id), result.job);
      void queryClient.invalidateQueries({ queryKey: attachmentExportQueryKeys.all(input.workspaceId) });
    },
  });
  useEffect(() => {
    if (previousWorkspace.current === workspaceId) return;
    if (inFlight.current?.workspaceId !== workspaceId) {
      inFlight.current?.controller.abort();
      inFlight.current = null;
    }
    mutation.reset();
    previousWorkspace.current = workspaceId;
  }, [mutation, workspaceId]);
  useEffect(() => () => inFlight.current?.controller.abort(), []);
  return mutation;
};
