import { useMutation, useQueryClient } from "@tanstack/react-query";

import {
  createConversation,
  submitFeedback,
  submitQuestion,
  type CreateConversationInput,
  type SubmitFeedbackInput,
  type SubmitQuestionInput,
} from "../../api/conversation";
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
