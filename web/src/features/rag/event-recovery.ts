import { useEffect, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";

import {
  ServerEventClientError,
  connectServerEvents,
  type ServerEventConnection,
  type ServerEventConnectionState,
  type ServerEventEnvelope,
} from "../../events";
import { ragQueryKeys } from "./query-keys";

const cursorKey = (workspaceId: string): string => `zhixu.rag-event-cursor.${workspaceId}`;

export const invalidateRagEvent = async (
  workspaceId: string,
  event: ServerEventEnvelope,
  invalidate: (queryKey: readonly unknown[]) => Promise<unknown>,
): Promise<void> => {
  if (event.workspaceId !== workspaceId) return;
  for (const target of event.invalidations) {
    switch (target.resource) {
      case "conversation":
        await invalidate(ragQueryKeys.conversations(workspaceId));
        await invalidate(ragQueryKeys.conversation(workspaceId, target.id));
        await invalidate(ragQueryKeys.turns(workspaceId, target.id));
        break;
      case "answer":
        await invalidate(ragQueryKeys.answer(workspaceId, target.id));
        break;
      case "question":
      case "workflow":
      case "model_run": {
        const conversationId = event.payloadSummary.conversationId;
        if (conversationId !== undefined) await invalidate(ragQueryKeys.turns(workspaceId, conversationId));
        const answerId = event.payloadSummary.answerId;
        if (answerId !== undefined) await invalidate(ragQueryKeys.answer(workspaceId, answerId));
        break;
      }
    }
  }
};

export const useRagEventRecovery = (workspaceId: string) => {
  const queryClient = useQueryClient();
  const [state, setState] = useState<ServerEventConnectionState>("closed");

  useEffect(() => {
    if (workspaceId === "") {
      setState("closed");
      return;
    }
    const storageKey = cursorKey(workspaceId);
    const lastEventId = window.sessionStorage.getItem(storageKey) ?? undefined;
    let connection: ServerEventConnection | undefined;
    let cancelled = false;
    const recover = async (): Promise<void> => {
      await queryClient.resetQueries({ queryKey: ragQueryKeys.conversations(workspaceId), exact: true });
      await queryClient.invalidateQueries({ queryKey: ragQueryKeys.all(workspaceId) });
      window.sessionStorage.removeItem(storageKey);
    };
    const connect = (cursor?: string): ServerEventConnection => connectServerEvents({
      workspaceId,
      ...(cursor === undefined ? {} : { lastEventId: cursor }),
      onStateChange: setState,
      onEvent: async (event) => {
        if (event.invalidations.some((target) => target.resource === "conversation")) {
          await queryClient.resetQueries({ queryKey: ragQueryKeys.conversations(workspaceId), exact: true });
        }
        await invalidateRagEvent(workspaceId, event, (queryKey) =>
          queryClient.invalidateQueries({ queryKey }));
        window.sessionStorage.setItem(storageKey, event.id);
      },
      onRecoveryRequired: recover,
    });
    try {
      connection = connect(lastEventId);
    } catch (error) {
      if (!(error instanceof ServerEventClientError) || error.code !== "CURSOR_REJECTED") throw error;
      void recover().then(() => {
        if (!cancelled) connection = connect();
      }).catch((recoveryError: unknown) => {
        setState("closed");
        console.error("RAG SSE cursor recovery failed", recoveryError);
      });
    }
    return () => {
      cancelled = true;
      connection?.close();
    };
  }, [queryClient, workspaceId]);

  return state;
};
