import { useMutation, useQuery, useQueryClient, type QueryClient } from "@tanstack/react-query";

import {
  confirmMemory,
  createMemoryCandidate,
  deleteMemory,
  editMemory,
  getMemory,
  listMemories,
  pauseMemory,
  resumeMemory,
  type CreateMemoryCandidateInput,
  type EditMemoryInput,
  type ListMemoriesInput,
  type TransitionMemoryInput,
} from "../../api/memory";
import { useActiveWorkspaceId } from "../../app/active-workspace";
import { memoryQueryKeys } from "./query-keys";

export const clearMemoryWorkspaceQueries = (queryClient: QueryClient, workspaceId: string): void => {
  if (workspaceId === "") return;
  const queryKey = memoryQueryKeys.all(workspaceId);
  void queryClient.cancelQueries({ queryKey });
  queryClient.removeQueries({ queryKey });
};

export const useMemories = (input: Omit<ListMemoriesInput, "workspaceId">) => {
  const workspaceId = useActiveWorkspaceId();
  return useQuery({
    queryKey: memoryQueryKeys.list(workspaceId, input.types ?? [], input.statuses ?? [], input.cursor ?? ""),
    queryFn: ({ signal }) => listMemories({ ...input, workspaceId }, signal),
    enabled: workspaceId !== "",
    retry: false,
  });
};

export const useMemory = (memoryId: string) => {
  const workspaceId = useActiveWorkspaceId();
  return useQuery({
    queryKey: memoryQueryKeys.item(workspaceId, memoryId),
    queryFn: ({ signal }) => getMemory(workspaceId, memoryId, signal),
    enabled: workspaceId !== "" && memoryId !== "",
    retry: false,
  });
};

const useMemoryCommand = <TInput extends { workspaceId: string },>(execute: (input: TInput) => Promise<{ memory: { id: string; workspaceId: string } }>) => {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (input: TInput) => execute(input),
    onSuccess: async ({ memory }) => {
      queryClient.setQueryData(memoryQueryKeys.item(memory.workspaceId, memory.id), memory);
      await queryClient.invalidateQueries({ queryKey: memoryQueryKeys.all(memory.workspaceId) });
    },
  });
};

export const useCreateMemoryCandidate = () => useMemoryCommand<CreateMemoryCandidateInput>(createMemoryCandidate);
export const useEditMemory = () => useMemoryCommand<EditMemoryInput>(editMemory);
export const useConfirmMemory = () => useMemoryCommand<TransitionMemoryInput>(confirmMemory);
export const usePauseMemory = () => useMemoryCommand<TransitionMemoryInput>(pauseMemory);
export const useResumeMemory = () => useMemoryCommand<TransitionMemoryInput>(resumeMemory);
export const useDeleteMemory = () => useMemoryCommand<TransitionMemoryInput>(deleteMemory);
