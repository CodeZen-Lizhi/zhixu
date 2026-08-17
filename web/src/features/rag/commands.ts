import { useMutation, useQueryClient } from "@tanstack/react-query";

import {
  createConversation,
  getAnswer,
  submitFeedback,
  submitQuestion,
  type CreateConversationInput,
  type SubmitFeedbackInput,
  type SubmitQuestionInput,
} from "../../api/conversation";
import { BusinessApiError, controlWorkflow } from "../../api/business";
import { ragQueryKeys } from "./query-keys";

export const createCommandId = (): string => crypto.randomUUID();

export const useCreateConversationCommand = () => {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (input: CreateConversationInput) => createConversation(input),
    retry: false,
    onSuccess: ({ resource }, input) => {
      if (resource === null) return;
      queryClient.setQueryData(ragQueryKeys.conversation(input.workspaceId, resource.id), resource);
      void queryClient.resetQueries({ queryKey: ragQueryKeys.conversations(input.workspaceId), exact: true });
    },
  });
};

export const useSubmitQuestionCommand = () => {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (input: SubmitQuestionInput) => submitQuestion(input),
    retry: false,
    onSuccess: ({ answer }, input) => {
      queryClient.setQueryData(ragQueryKeys.answer(input.workspaceId, answer.id), answer);
      void queryClient.invalidateQueries({ queryKey: ragQueryKeys.turns(input.workspaceId, input.conversationId) });
      void queryClient.invalidateQueries({ queryKey: ragQueryKeys.conversation(input.workspaceId, input.conversationId) });
      void queryClient.resetQueries({ queryKey: ragQueryKeys.conversations(input.workspaceId), exact: true });
    },
  });
};

export const useSubmitFeedbackCommand = () => {
  return useMutation({
    mutationFn: (input: SubmitFeedbackInput) => submitFeedback(input),
    retry: false,
  });
};

export interface CancelWorkspaceAnalysisInput {
  workspaceId: string;
  conversationId: string;
  answerId: string;
  workflowRunId: string;
  expectedVersion: number;
}

export const WORKSPACE_ANALYSIS_CANCEL_MAX_ATTEMPTS = 5;

const isWorkflowVersionConflict = (error: unknown): error is BusinessApiError =>
  error instanceof BusinessApiError
  && error.status === 409
  && error.errorCode === "WORKFLOW_VERSION_CONFLICT";

const cancellableWorkflowStatuses = new Set(["pending", "running", "paused", "waiting_for_human", "retry_wait"]);

const cancelWorkspaceAnalysis = async (input: CancelWorkspaceAnalysisInput) => {
  let expectedVersion = input.expectedVersion;
  let attempt = 1;
  for (;;) {
    try {
      const result = await controlWorkflow(input.workspaceId, input.workflowRunId, "cancel", expectedVersion);
      if (!result.cancelRequested) {
        throw new BusinessApiError("INVALID_RESPONSE", "Workflow 取消响应未确认停止请求", false);
      }
      return result;
    } catch (error: unknown) {
      if (!isWorkflowVersionConflict(error) || attempt >= WORKSPACE_ANALYSIS_CANCEL_MAX_ATTEMPTS) throw error;

      const refreshed = (await getAnswer({ workspaceId: input.workspaceId, id: input.answerId })).resource;
      if (refreshed?.id !== input.answerId) throw error;
      const refreshedWorkflowVersion = refreshed.workflow.version;
      if (
        refreshed.workspaceId !== input.workspaceId
        || refreshed.conversationId !== input.conversationId
        || refreshed.publicationStatus !== "pending"
        || refreshed.workflow.runId !== input.workflowRunId
        || !cancellableWorkflowStatuses.has(refreshed.workflow.status)
        || refreshedWorkflowVersion <= expectedVersion
      ) throw error;

      expectedVersion = refreshedWorkflowVersion;
      attempt += 1;
    }
  }
};

export const useCancelWorkspaceAnalysisCommand = () => {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: cancelWorkspaceAnalysis,
    retry: false,
    onSuccess: (_result, input) => {
      void queryClient.invalidateQueries({ queryKey: ragQueryKeys.answer(input.workspaceId, input.answerId), exact: true });
      void queryClient.invalidateQueries({ queryKey: ragQueryKeys.analysisTimeline(input.workspaceId, input.answerId), exact: true });
      void queryClient.invalidateQueries({ queryKey: ragQueryKeys.turns(input.workspaceId, input.conversationId) });
    },
  });
};
