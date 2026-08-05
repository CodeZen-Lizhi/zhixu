import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import {
  BusinessApiError,
  getWorkflow,
  submitWorkflowHumanDecision,
  type WorkflowHumanTask,
} from "../../api/business";
import {
  addOrganizingMaterial,
  cloneOrganizingTemplate,
  confirmOrganizingDraft,
  createOrganizingDraft,
  getOrganizingDraft,
  getOrganizingRun,
  getOrganizingSnapshot,
  listOrganizingTemplates,
  removeOrganizingMaterial,
  reviseOrganizingTemplate,
  searchOrganizingMaterials,
  setOrganizingMaterialSelection,
  suggestOrganizingMaterials,
  updateOrganizingDraft,
  type AddOrganizingMaterialInput,
  type ConfirmOrganizingDraftInput,
  type CreateOrganizingDraftInput,
  type CloneOrganizingTemplateInput,
  type OrganizingDraftCommandInput,
  type RemoveOrganizingMaterialInput,
  type SetOrganizingMaterialSelectionInput,
  type ReviseOrganizingTemplateInput,
  type OrganizingMaterialKind,
  type UpdateOrganizingDraftInput,
} from "../../api/organizing";
import { getActiveWorkspaceId, useActiveWorkspaceId } from "../../app/active-workspace";
import { organizingQueryKeys } from "./query-keys";

const remainsActive = (workspaceId: string): boolean => getActiveWorkspaceId() === workspaceId;

export const useOrganizingDraft = (draftId: string) => {
  const workspaceId = useActiveWorkspaceId();
  return useQuery({
    queryKey: organizingQueryKeys.draft(workspaceId, draftId),
    queryFn: ({ signal }) => getOrganizingDraft(workspaceId, draftId, signal),
    enabled: workspaceId !== "" && draftId !== "",
    retry: false,
  });
};

export const useOrganizingTemplates = () => {
  const workspaceId = useActiveWorkspaceId();
  return useQuery({
    queryKey: organizingQueryKeys.templates(workspaceId),
    queryFn: ({ signal }) => listOrganizingTemplates(workspaceId, signal),
    enabled: workspaceId !== "",
    retry: false,
  });
};

export const useOrganizingMaterialSearch = (query: string, kind: OrganizingMaterialKind, enabled: boolean) => {
  const workspaceId = useActiveWorkspaceId();
  const normalizedQuery = query.trim();
  const queryBytes = new TextEncoder().encode(normalizedQuery).byteLength;
  return useQuery({
    queryKey: organizingQueryKeys.materialSearch(workspaceId, normalizedQuery, kind),
    queryFn: ({ signal }) => searchOrganizingMaterials({ workspaceId, query: normalizedQuery, kind, limit: 25, signal }),
    enabled: enabled && workspaceId !== "" && queryBytes >= 2 && queryBytes <= 256,
    retry: false,
  });
};

export const useOrganizingSnapshot = (snapshotId: string) => {
  const workspaceId = useActiveWorkspaceId();
  return useQuery({
    queryKey: organizingQueryKeys.snapshot(workspaceId, snapshotId),
    queryFn: ({ signal }) => getOrganizingSnapshot(workspaceId, snapshotId, signal),
    enabled: workspaceId !== "" && snapshotId !== "",
    retry: false,
  });
};

export const useOrganizingRun = (snapshotId: string) => {
  const workspaceId = useActiveWorkspaceId();
  return useQuery({
    queryKey: organizingQueryKeys.run(workspaceId, snapshotId),
    queryFn: ({ signal }) => getOrganizingRun(workspaceId, snapshotId, signal),
    enabled: workspaceId !== "" && snapshotId !== "",
    retry: false,
    refetchInterval: (query) => {
      const status = query.state.data?.status;
      if (status === "QUEUED" || status === "STARTED") return 2_000;
      return status === "WAITING_FOR_HUMAN" ? 5_000 : false;
    },
  });
};

export const useOrganizingWorkflow = (workflowRunId: string, enabled: boolean) => {
  const workspaceId = useActiveWorkspaceId();
  return useQuery({
    queryKey: ["business", workspaceId, "workflow", workflowRunId],
    queryFn: ({ signal }) => getWorkflow(workspaceId, workflowRunId, signal),
    enabled: enabled && workspaceId !== "" && workflowRunId !== "",
    retry: false,
  });
};

export const useSubmitOrganizingHumanDecision = (snapshotId: string, workflowRunId: string) => {
  const workspaceId = useActiveWorkspaceId();
  const queryClient = useQueryClient();
  const refreshAuthoritativeState = async (): Promise<void> => {
    if (!remainsActive(workspaceId)) return;
    await Promise.all([
      queryClient.invalidateQueries({ queryKey: ["business", workspaceId, "workflow", workflowRunId], refetchType: "active" }),
      queryClient.invalidateQueries({ queryKey: organizingQueryKeys.run(workspaceId, snapshotId), refetchType: "active" }),
    ]);
  };
  return useMutation({
    mutationFn: ({ task, approved, targetPath }: { task: WorkflowHumanTask; approved: boolean; targetPath: string }) =>
      submitWorkflowHumanDecision(workspaceId, workflowRunId, task, { approved, targetPath }),
    retry: false,
    onSuccess: refreshAuthoritativeState,
    onError: async (error) => {
      if (error instanceof BusinessApiError && error.status === 409) await refreshAuthoritativeState();
    },
  });
};

const useDraftMutation = <TInput extends { workspaceId: string; draftId?: string }>(mutationFn: (input: TInput) => ReturnType<typeof createOrganizingDraft>) => {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn,
    retry: false,
    onSuccess: ({ draft }, input) => {
      if (!remainsActive(input.workspaceId)) return;
      queryClient.setQueryData(organizingQueryKeys.draft(input.workspaceId, draft.id), draft);
    },
    onError: (_error, input) => {
      if (!remainsActive(input.workspaceId) || input.draftId === undefined) return;
      void queryClient.invalidateQueries({ queryKey: organizingQueryKeys.draft(input.workspaceId, input.draftId), refetchType: "active" });
    },
  });
};

export const useCreateOrganizingDraft = () => useDraftMutation<CreateOrganizingDraftInput>(createOrganizingDraft);
export const useUpdateOrganizingDraft = () => useDraftMutation<UpdateOrganizingDraftInput>(updateOrganizingDraft);
export const useSuggestOrganizingMaterials = () => useDraftMutation<OrganizingDraftCommandInput>(suggestOrganizingMaterials);
export const useAddOrganizingMaterial = () => useDraftMutation<AddOrganizingMaterialInput>(addOrganizingMaterial);
export const useRemoveOrganizingMaterial = () => useDraftMutation<RemoveOrganizingMaterialInput>(removeOrganizingMaterial);
export const useSetOrganizingMaterialSelection = () => useDraftMutation<SetOrganizingMaterialSelectionInput>(setOrganizingMaterialSelection);

const useTemplateMutation = <TInput extends { workspaceId: string }>(mutationFn: (input: TInput) => ReturnType<typeof cloneOrganizingTemplate>) => {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn,
    retry: false,
    onSuccess: ({ template }, input) => {
      if (!remainsActive(input.workspaceId)) return;
      queryClient.setQueryData(organizingQueryKeys.templates(input.workspaceId), (current: readonly typeof template[] | undefined) => {
        const templates = current ?? [];
        const index = templates.findIndex((item) => item.id === template.id);
        return index < 0 ? [...templates, template] : templates.map((item) => item.id === template.id ? template : item);
      });
    },
    onError: (_error, input) => {
      if (!remainsActive(input.workspaceId)) return;
      void queryClient.invalidateQueries({ queryKey: organizingQueryKeys.templates(input.workspaceId), refetchType: "active" });
    },
  });
};

export const useCloneOrganizingTemplate = () => useTemplateMutation<CloneOrganizingTemplateInput>(cloneOrganizingTemplate);
export const useReviseOrganizingTemplate = () => useTemplateMutation<ReviseOrganizingTemplateInput>(reviseOrganizingTemplate);

export const useConfirmOrganizingDraft = () => {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (input: ConfirmOrganizingDraftInput) => confirmOrganizingDraft(input),
    retry: false,
    onError: (_error, input) => {
      if (!remainsActive(input.workspaceId)) return;
      void queryClient.invalidateQueries({ queryKey: organizingQueryKeys.draft(input.workspaceId, input.draftId), refetchType: "active" });
    },
    onSuccess: ({ draft, snapshot }, input) => {
      if (!remainsActive(input.workspaceId)) return;
      queryClient.setQueryData(organizingQueryKeys.draft(input.workspaceId, draft.id), draft);
      queryClient.setQueryData(organizingQueryKeys.snapshot(input.workspaceId, snapshot.id), snapshot);
      void queryClient.invalidateQueries({ queryKey: organizingQueryKeys.run(input.workspaceId, snapshot.id), refetchType: "active" });
    },
  });
};
