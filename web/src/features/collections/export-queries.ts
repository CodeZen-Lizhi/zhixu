import { useMutation, useQuery, useQueryClient, type QueryClient } from "@tanstack/react-query";

import { createExport, getExport, listCollectionExports, type ExportCreateInput, type ExportJob, type ExportListPage } from "../../api/exports";
import { useActiveWorkspaceId } from "../../app/active-workspace";
import { collectionExportQueryKeys } from "./export-query-keys";

export const collectionExportPollMilliseconds = 2_000;
export const isActiveCollectionExport = (job: ExportJob): boolean => job.status === "PENDING" || job.status === "RUNNING";
export const collectionExportPollInterval = (page: ExportListPage | undefined): number | false => page?.items.some(isActiveCollectionExport) === true ? collectionExportPollMilliseconds : false;

export const clearCollectionExportWorkspaceQueries = (queryClient: QueryClient, workspaceId: string): void => {
  if (workspaceId === "") return;
  const queryKey = collectionExportQueryKeys.all(workspaceId);
  void queryClient.cancelQueries({ queryKey });
  queryClient.removeQueries({ queryKey });
};

export const useCollectionExports = (collectionId: string, cursor?: string) => {
  const workspaceId = useActiveWorkspaceId();
  return useQuery({
    queryKey: collectionExportQueryKeys.list(workspaceId, collectionId, cursor),
    queryFn: ({ signal }) => listCollectionExports(workspaceId, collectionId, { ...(cursor === undefined ? {} : { cursor }) }, signal),
    enabled: workspaceId !== "" && collectionId !== "",
    retry: false,
    refetchInterval: (query) => collectionExportPollInterval(query.state.data),
  });
};

export const useCollectionExport = (exportId: string, polling = false) => {
  const workspaceId = useActiveWorkspaceId();
  return useQuery({
    queryKey: collectionExportQueryKeys.detail(workspaceId, exportId),
    queryFn: ({ signal }) => getExport(workspaceId, exportId, signal),
    enabled: workspaceId !== "" && exportId !== "",
    retry: false,
    refetchInterval: (query) => polling && query.state.data !== undefined && isActiveCollectionExport(query.state.data) ? collectionExportPollMilliseconds : false,
  });
};

export const useCreateCollectionExport = () => {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (input: ExportCreateInput) => createExport(input),
    onSuccess: (result, input) => {
      queryClient.setQueryData(collectionExportQueryKeys.detail(input.workspaceId, result.job.id), result.job);
      void queryClient.invalidateQueries({ queryKey: collectionExportQueryKeys.all(input.workspaceId) });
    },
  });
};
