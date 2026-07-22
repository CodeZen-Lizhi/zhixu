import type { QueryClient } from "@tanstack/react-query";

import { type ServerEventEnvelope } from "../../events";
import { ragQueryKeys } from "./query-keys";

const sameQueryKey = (left: readonly unknown[], right: readonly unknown[]): boolean =>
  left.length === right.length && left.every((value, index) => value === right[index]);

export const recoverRagWorkspace = async (queryClient: QueryClient, workspaceId: string): Promise<void> => {
  const conversationListKey = ragQueryKeys.conversations(workspaceId);
  await queryClient.resetQueries(
    { queryKey: conversationListKey, exact: true },
    { throwOnError: true },
  );
  await queryClient.refetchQueries({
    type: "all",
    predicate: (query) => query.queryKey[0] === "rag"
      && query.queryKey[1] === workspaceId
      && !sameQueryKey(query.queryKey, conversationListKey),
  }, { throwOnError: true });
};

export const invalidateRagEvent = async (
  queryClient: QueryClient,
  workspaceId: string,
  event: ServerEventEnvelope,
): Promise<void> => {
  if (event.workspaceId !== workspaceId) return;
  let resetConversationList = false;
  const conversations = new Set<string>();
  const turns = new Set<string>();
  const answers = new Set<string>();
  for (const target of event.invalidations) {
    switch (target.resource) {
      case "conversation":
        resetConversationList = true;
        conversations.add(target.id);
        turns.add(target.id);
        break;
      case "answer":
        answers.add(target.id);
        break;
      case "question":
      case "workflow":
      case "model_run": {
        const conversationId = event.payloadSummary.conversationId;
        if (conversationId !== undefined) turns.add(conversationId);
        const answerId = event.payloadSummary.answerId;
        if (answerId !== undefined) answers.add(answerId);
        break;
      }
    }
  }
  if (resetConversationList) {
    await queryClient.resetQueries(
      { queryKey: ragQueryKeys.conversations(workspaceId), exact: true },
      { throwOnError: true },
    );
  }
  for (const conversationId of conversations) {
    await queryClient.invalidateQueries(
      { queryKey: ragQueryKeys.conversation(workspaceId, conversationId), exact: true },
      { throwOnError: true },
    );
  }
  for (const conversationId of turns) {
    await queryClient.invalidateQueries(
      { queryKey: ragQueryKeys.turns(workspaceId, conversationId) },
      { throwOnError: true },
    );
  }
  for (const answerId of answers) {
    await queryClient.invalidateQueries(
      { queryKey: ragQueryKeys.answer(workspaceId, answerId), exact: true },
      { throwOnError: true },
    );
  }
};
