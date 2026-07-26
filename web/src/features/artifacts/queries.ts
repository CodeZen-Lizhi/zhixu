import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import {
  approveArtifactDraft,
  approveArtifactOutline,
  createArtifactPublishProposal,
  exportArtifactMarkdown,
  generateArtifactSection,
  getArtifact,
  getArtifactSectionGenerations,
  listArtifacts,
  planArtifact,
  recordArtifactGapSection,
  startArtifactRevision,
  submitArtifactOutline,
  type ArtifactCommandResult,
  type ArtifactSectionGeneration,
  type ArtifactSectionGenerationAcceptance,
  type ArtifactSectionGenerationPage,
  type GenerateArtifactSectionInput,
  type PlanArtifactInput,
  type RecordGapSectionInput,
  type RevisionCommandInput,
  type SubmitOutlineInput,
} from "../../api/artifacts";
import { getWorkflow, type WorkflowStatus } from "../../api/business";
import { useActiveWorkspaceId } from "../../app/active-workspace";
import { artifactQueryKeys } from "./query-keys";

export const artifactGenerationPollMilliseconds = 2_000;
export const artifactGenerationReceiptPollLimit = 12;
const activeWorkflowStatuses: readonly WorkflowStatus[] = ["pending", "running", "waiting_for_human", "retry_wait", "paused"];

export const isArtifactGenerationWorkflowTerminal = (status: WorkflowStatus | undefined): boolean => status === "succeeded" || status === "failed" || status === "cancelled";
export const artifactGenerationWorkflowPollInterval = (status: WorkflowStatus | undefined): number | false => status !== undefined && activeWorkflowStatuses.includes(status) ? artifactGenerationPollMilliseconds : false;
export const artifactGenerationWorkflowQueryKey = (workspaceId: string, workflowRunId: string) => ["business", workspaceId, "workflow", workflowRunId] as const;

const persistedGeneration = (value: ArtifactSectionGenerationAcceptance): ArtifactSectionGeneration | undefined => value.status === "COMPLETED" ? undefined : {
  generationId: value.generationId,
  workspaceId: value.workspaceId,
  artifactId: value.artifactId,
  sourceRevisionId: value.sourceRevisionId,
  sourceRevisionNo: value.sourceRevisionNo,
  sourceArtifactVersion: value.sourceArtifactVersion,
  sectionKey: value.sectionKey,
  workflowRunId: value.workflowRunId,
  nodeRunId: value.nodeRunId,
  status: value.status,
  version: value.version,
  createdAt: value.createdAt,
  updatedAt: value.updatedAt,
  statusUrl: value.statusUrl,
};

const mergeGeneration = (page: ArtifactSectionGenerationPage | undefined, accepted: ArtifactSectionGenerationAcceptance): ArtifactSectionGenerationPage | undefined => {
  if (page === undefined) return undefined;
  const generation = persistedGeneration(accepted);
  const items = page.items.filter((item) => item.sectionKey !== accepted.sectionKey);
  return { ...page, items: generation === undefined ? items : [...items, generation] };
};

export const useArtifacts = (cursor?: string) => {
  const workspaceId = useActiveWorkspaceId();
  return useQuery({ queryKey: artifactQueryKeys.list(workspaceId, cursor), queryFn: ({ signal }) => listArtifacts(workspaceId, { ...(cursor === undefined ? {} : { cursor }) }, signal), enabled: workspaceId !== "", retry: false });
};

export const useArtifact = (artifactId: string) => {
  const workspaceId = useActiveWorkspaceId();
  return useQuery({ queryKey: artifactQueryKeys.detail(workspaceId, artifactId), queryFn: ({ signal }) => getArtifact(workspaceId, artifactId, signal), enabled: workspaceId !== "" && artifactId !== "", retry: false });
};

export const useSectionGenerations = (artifactId: string) => {
  const workspaceId = useActiveWorkspaceId();
  return useQuery({ queryKey: artifactQueryKeys.sectionGenerations(workspaceId, artifactId), queryFn: ({ signal }) => getArtifactSectionGenerations(workspaceId, artifactId, signal), enabled: workspaceId !== "" && artifactId !== "", retry: false });
};

export const useArtifactGenerationWorkflow = (workflowRunId: string, enabled = true) => {
  const workspaceId = useActiveWorkspaceId();
  return useQuery({ queryKey: artifactGenerationWorkflowQueryKey(workspaceId, workflowRunId), queryFn: ({ signal }) => getWorkflow(workspaceId, workflowRunId, signal), enabled: enabled && workspaceId !== "" && workflowRunId !== "", retry: false, refetchInterval: (query) => artifactGenerationWorkflowPollInterval(query.state.data?.status) });
};

const useArtifactCommand = <T extends { workspaceId: string; artifactId?: string }>(execute: (input: T) => Promise<ArtifactCommandResult>) => {
  const queryClient = useQueryClient();
  return useMutation({ mutationFn: (input: T) => execute(input), onSuccess: (_result, input) => { void queryClient.invalidateQueries({ queryKey: artifactQueryKeys.all(input.workspaceId) }); if (input.artifactId !== undefined) void queryClient.invalidateQueries({ queryKey: artifactQueryKeys.detail(input.workspaceId, input.artifactId) }); } });
};

export const usePlanArtifact = () => useArtifactCommand<PlanArtifactInput>(planArtifact);
export const useSubmitArtifactOutline = () => useArtifactCommand<SubmitOutlineInput>(submitArtifactOutline);
export const useApproveArtifactOutline = () => useArtifactCommand<RevisionCommandInput>(approveArtifactOutline);
export const useStartArtifactRevision = () => useArtifactCommand<RevisionCommandInput>(startArtifactRevision);
export const useRecordArtifactGapSection = () => useArtifactCommand<RecordGapSectionInput>(recordArtifactGapSection);
export const useGenerateArtifactSection = () => {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (input: GenerateArtifactSectionInput) => generateArtifactSection(input),
    onSuccess: (result, input) => {
      queryClient.setQueryData<ArtifactSectionGenerationPage>(artifactQueryKeys.sectionGenerations(input.workspaceId, input.artifactId), (current) => mergeGeneration(current, result));
      if (result.status !== "PENDING") void queryClient.invalidateQueries({ queryKey: artifactQueryKeys.detail(input.workspaceId, input.artifactId) });
    },
    onSettled: (_result, _error, input) => {
      void queryClient.invalidateQueries({ queryKey: artifactQueryKeys.sectionGenerations(input.workspaceId, input.artifactId) });
    },
  });
};
export const useApproveArtifactDraft = () => useArtifactCommand<RevisionCommandInput>(approveArtifactDraft);
export const useExportArtifactMarkdown = () => useArtifactCommand<RevisionCommandInput>(exportArtifactMarkdown);
export const useCreateArtifactPublishProposal = () => useArtifactCommand<RevisionCommandInput>(createArtifactPublishProposal);
