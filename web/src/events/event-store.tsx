/* eslint-disable @typescript-eslint/consistent-type-definitions */
import { useQueryClient } from "@tanstack/react-query";
import { createContext, useCallback, useContext, useEffect, useRef, useState, type ReactNode } from "react";

import { getWorkspace } from "../api/workspace";
import { useActiveWorkspaceId } from "../app/active-workspace";
import { invalidateRagEvent, recoverRagWorkspace } from "../features/rag/event-recovery";
import { reviewQueryKeys } from "../features/review/query-keys";
import { resetSearchWorkspaceQueriesForRecovery } from "../features/search/query-keys";
import { ServerEventClientError, connectServerEvents, type ConnectServerEventsOptions, type ServerEventConnectionState, type ServerEventEnvelope } from "./server-events";

type EventStoreSnapshot = { state: ServerEventConnectionState; lastEventId?: string; lastEvent?: ServerEventEnvelope };
type ScopedEventStoreSnapshot = EventStoreSnapshot & { scopeWorkspaceId: string };
type EventStoreValue = EventStoreSnapshot & { retryRecovery: () => void };
type WorkspaceRecovery = (workspaceId: string) => Promise<void> | void;
type EventStoreContextValue = EventStoreValue & { registerRecovery: (recover: WorkspaceRecovery) => () => void };

const EventStoreContext = createContext<EventStoreContextValue>({
  state: "closed",
  retryRecovery: () => undefined,
  registerRecovery: () => () => undefined,
});
const cursorKey = (workspaceId: string) => `zhixu.event-cursor.${workspaceId}`;

const resourceId = (event: ServerEventEnvelope, resource: string): string | undefined => {
  const invalidation = event.invalidations.find((item) => item.resource === resource);
  if (invalidation !== undefined) return invalidation.id;
  const prefix = `${resource}:`;
  return event.resourceRef.startsWith(prefix) ? event.resourceRef.slice(prefix.length) : undefined;
};

const reviewLearningPathResource = (event: ServerEventEnvelope): boolean =>
  event.type.startsWith("learning_path.") ||
  event.resourceRef.startsWith("learning_path:") ||
  event.resourceRef.startsWith("learning_path_step:");

export const EventStoreProvider = ({ children }: { children: ReactNode }) => {
  const workspaceId = useActiveWorkspaceId();
  const queryClient = useQueryClient();
  const [value, setValue] = useState<ScopedEventStoreSnapshot>({ scopeWorkspaceId: "", state: "closed" });
  const retryRecoveryRef = useRef<(() => void) | undefined>(undefined);
  const recoveryCallbacksRef = useRef(new Set<WorkspaceRecovery>());
  const activeEffectRef = useRef<{ workspaceId: string } | undefined>(undefined);
  const currentWorkspaceIdRef = useRef(workspaceId);
  currentWorkspaceIdRef.current = workspaceId;
  const retryRecovery = useCallback(() => retryRecoveryRef.current?.(), []);
  const registerRecovery = useCallback((recover: WorkspaceRecovery) => {
    recoveryCallbacksRef.current.add(recover);
    return () => recoveryCallbacksRef.current.delete(recover);
  }, []);

  useEffect(() => {
    const effectIdentity = { workspaceId };
    activeEffectRef.current = effectIdentity;
    if (!workspaceId) {
      return undefined;
    }
    setValue({ scopeWorkspaceId: workspaceId, state: "connecting" });

    let disposed = false;
    let connection: ReturnType<typeof connectServerEvents> | undefined;
    let recoveryPromise: Promise<void> | undefined;
    let reconnectInFlight = false;
    let connectionGeneration = 0;
    let activeConnectionGeneration = 0;
    const isActive = (): boolean => !disposed
      && activeEffectRef.current === effectIdentity
      && currentWorkspaceIdRef.current === workspaceId;
    const storageKey = cursorKey(workspaceId);
    const queryFamilies = [
      ["workspace", workspaceId],
      ["business", workspaceId],
      ["search", workspaceId],
      ["rag", workspaceId],
      ["collections", workspaceId],
      ["collection-exports", workspaceId],
      ["knowledge-health", workspaceId],
      ["graph", workspaceId],
      ["semantic-links", workspaceId],
    ] as const;
    const removeWorkspaceQueries = (): void => {
      for (const queryKey of queryFamilies) queryClient.removeQueries({ queryKey });
    };
    const assertActive = (): void => {
      if (isActive()) return;
      if (activeEffectRef.current === undefined || currentWorkspaceIdRef.current !== workspaceId) removeWorkspaceQueries();
      throw new DOMException("Workspace recovery cancelled", "AbortError");
    };
    const performRecovery = async (): Promise<void> => {
      assertActive();
      try {
        await resetSearchWorkspaceQueriesForRecovery(queryClient, workspaceId);
        for (const recoverWorkspace of [...recoveryCallbacksRef.current]) {
          assertActive();
          await recoverWorkspace(workspaceId);
        }
        assertActive();
        await queryClient.fetchQuery({
          queryKey: ["workspace", workspaceId],
          queryFn: async ({ signal }) => {
            const workspace = await getWorkspace(workspaceId, signal);
            assertActive();
            return workspace;
          },
          staleTime: 0,
        });
        assertActive();
        await queryClient.refetchQueries({ queryKey: ["business", workspaceId], type: "all" }, { throwOnError: true });
        assertActive();
        await recoverRagWorkspace(queryClient, workspaceId);
        assertActive();
        await queryClient.refetchQueries({ queryKey: ["collections", workspaceId], type: "all" }, { throwOnError: true });
        assertActive();
        await queryClient.refetchQueries({ queryKey: ["collection-exports", workspaceId], type: "all" }, { throwOnError: true });
        assertActive();
        await queryClient.refetchQueries({ queryKey: ["knowledge-health", workspaceId], type: "all" }, { throwOnError: true });
        assertActive();
        await queryClient.refetchQueries({ queryKey: ["graph", workspaceId], type: "all" }, { throwOnError: true });
        assertActive();
        await queryClient.refetchQueries({ queryKey: ["semantic-links", workspaceId], type: "all" }, { throwOnError: true });
        assertActive();
        await queryClient.refetchQueries({ queryKey: ["review", workspaceId], type: "all" }, { throwOnError: true });
        assertActive();
        await queryClient.refetchQueries({ queryKey: ["memory", workspaceId], type: "all" }, { throwOnError: true });
        assertActive();
        await queryClient.refetchQueries({ queryKey: ["interview", workspaceId], type: "all" }, { throwOnError: true });
        assertActive();
        window.sessionStorage.removeItem(storageKey);
      } finally {
        if (!isActive() && (activeEffectRef.current === undefined || currentWorkspaceIdRef.current !== workspaceId)) {
          removeWorkspaceQueries();
        }
      }
    };
    const recover = (): Promise<void> => {
      if (recoveryPromise !== undefined) return recoveryPromise;
      const pending = performRecovery();
      recoveryPromise = pending;
      const clearRecovery = (): void => {
        if (recoveryPromise === pending) recoveryPromise = undefined;
      };
      void pending.then(clearRecovery, clearRecovery);
      return pending;
    };
    const isActiveConnection = (generation: number): boolean =>
      isActive() && activeConnectionGeneration === generation;
    const invalidateEventQueries = async (event: ServerEventEnvelope): Promise<void> => {
      const invalidated = new Set<string>();
      const invalidate = async (queryKey: readonly unknown[]): Promise<void> => {
        const identity = JSON.stringify(queryKey);
        if (invalidated.has(identity)) return;
        invalidated.add(identity);
        await queryClient.invalidateQueries({ queryKey }, { throwOnError: true });
      };
      const markStaleWithoutRefetch = async (queryKey: readonly unknown[]): Promise<void> => {
        const identity = JSON.stringify(queryKey);
        if (invalidated.has(identity)) return;
        invalidated.add(identity);
        await queryClient.invalidateQueries({ queryKey, refetchType: "none" }, { throwOnError: true });
      };
      const proposalEvent = event.type.startsWith("proposal.") || event.type.startsWith("approval.") || event.resourceRef.startsWith("proposal:");
      const workflowEvent = event.type.startsWith("workflow.") || event.invalidations.some((item) => item.resource === "workflow");
      const sourceEvent = event.type.startsWith("ingestion.")
        || event.type.startsWith("retrieval.")
        || event.type.startsWith("index.")
        || event.type.startsWith("source.")
        || event.type.startsWith("source_version.");
      const learningPathEvent = reviewLearningPathResource(event);
      const reviewAnswerID = learningPathEvent
        ? event.payloadSummary.answerId
        : undefined;
      const reviewEvent = event.type.startsWith("review.")
        || event.resourceRef.startsWith("review_deck:")
        || event.resourceRef.startsWith("review_card:")
        || event.resourceRef.startsWith("review_schedule:")
        || event.resourceRef.startsWith("review_session:")
        || event.resourceRef.startsWith("review_answer:");
      const memoryEvent = event.type.startsWith("memory.") || event.resourceRef.startsWith("memory:");
      const interviewEvent = event.type.startsWith("interview.")
        || event.resourceRef.startsWith("interview_session:")
        || event.resourceRef.startsWith("interview_question:")
        || event.resourceRef.startsWith("interview_turn:")
        || event.resourceRef.startsWith("interview_report:")
        || (learningPathEvent && reviewAnswerID === undefined);
      if (proposalEvent) {
        await invalidate(["business", workspaceId, "proposals"]);
        const proposalId = resourceId(event, "proposal");
        await invalidate(proposalId === undefined
          ? ["business", workspaceId, "proposal"]
          : ["business", workspaceId, "proposal", proposalId]);
        await invalidate(proposalId === undefined
          ? ["business", workspaceId, "proposal-current-content"]
          : ["business", workspaceId, "proposal-current-content", proposalId]);
        if (event.type.startsWith("approval.")) await invalidate(["business", workspaceId, "workflows"]);
      }
      if (workflowEvent) {
        await invalidate(["business", workspaceId, "workflows"]);
        const workflowId = resourceId(event, "workflow");
        if (workflowId !== undefined) await invalidate(["business", workspaceId, "workflow", workflowId]);
        // Approval→Workflow 事件可能没有 proposal resourceRef；保守刷新 Change Control 读模型，避免跨标签页陈旧。
        await invalidate(["business", workspaceId, "proposals"]);
        await invalidate(["business", workspaceId, "proposal"]);
        await invalidate(["business", workspaceId, "proposal-current-content"]);
      }
      if (workflowEvent || sourceEvent) await invalidate(["business", workspaceId, "sources"]);
      // Search cursor 绑定 Active Index/result fingerprint。Index 事件只把快照标 stale，不能用旧 cursor
      // 强制 refetch，否则 409 stale 会阻止 SSE 事件游标提交并形成重放循环。
      if (sourceEvent) await markStaleWithoutRefetch(["search", workspaceId]);
      await invalidateRagEvent(queryClient, workspaceId, event);
      if (reviewEvent) await invalidate(["review", workspaceId]);
      if (memoryEvent) await invalidate(["memory", workspaceId]);
      if (interviewEvent) await invalidate(["interview", workspaceId]);
      if (reviewAnswerID !== undefined)
        await invalidate(reviewQueryKeys.learningPath(workspaceId, reviewAnswerID));

      if (event.type === "proposal.applied") {
        await invalidate(["graph", workspaceId]);
        await invalidate(["semantic-links", workspaceId]);
        await invalidate(["collections", workspaceId]);
        await invalidate(["knowledge-health", workspaceId]);
      }

      const invalidatesCollections = event.type.startsWith("collection.")
        || event.resourceRef.startsWith("collection:")
        || event.invalidations.some((item) => item.resource === "collection");
      const invalidatesExports = event.type.startsWith("export.")
        || event.resourceRef.startsWith("export_job:")
        || event.invalidations.some((item) => item.resource === "export_job");
      if (invalidatesExports) await invalidate(["collection-exports", workspaceId]);
      const invalidatesHealth = event.type.startsWith("health.")
        || event.resourceRef.startsWith("health_scan:")
        || event.resourceRef.startsWith("health_issue:")
        || event.invalidations.some((item) => item.resource === "health_issue" || item.resource === "health_scan" || item.resource === "knowledge_health");
      if (invalidatesCollections) {
        await invalidate(["collections", workspaceId]);
      } else if (invalidatesHealth) {
        await invalidate(["collections", workspaceId, "results"]);
      }
      if (invalidatesHealth) await invalidate(["knowledge-health", workspaceId]);
    };
    const options = (generation: number, lastEventId?: string): ConnectServerEventsOptions => ({
      workspaceId,
      ...(lastEventId === undefined ? {} : { lastEventId }),
      onStateChange: (state) => {
        if (!isActiveConnection(generation)) return;
        if (state === "open") retryRecoveryRef.current = undefined;
        setValue((current) => ({
          ...current,
          scopeWorkspaceId: workspaceId,
          state: (state === "reconnecting" || state === "closed") && current.state === "recovery_failed"
            ? current.state
            : state,
        }));
      },
      onRecoveryRequired: () => {
        if (!isActiveConnection(generation)) {
          return Promise.reject(new DOMException("SSE connection recovery cancelled", "AbortError"));
        }
        return recover();
      },
      onEvent: async (event) => {
        if (!isActiveConnection(generation)) return;
        await invalidateEventQueries(event);
        if (!isActiveConnection(generation)) return;
        window.sessionStorage.setItem(storageKey, event.id);
        setValue({ scopeWorkspaceId: workspaceId, state: "open", lastEventId: event.id, lastEvent: event });
      },
      onError: (error) => {
        if (!isActiveConnection(generation)) return;
        if (error.code === "RECOVERY_FAILED") retryRecoveryRef.current = retryRecoveryAttempt;
        setValue((current) => ({ ...current, scopeWorkspaceId: workspaceId, state: error.code === "RECOVERY_FAILED" ? "recovery_failed" : "reconnecting" }));
      },
    });

    function startConnection(lastEventId?: string): void {
      const generation = connectionGeneration + 1;
      connectionGeneration = generation;
      activeConnectionGeneration = generation;
      retryRecoveryRef.current = undefined;
      try {
        connection = connectServerEvents(options(generation, lastEventId));
      } catch (error: unknown) {
        if (activeConnectionGeneration === generation) activeConnectionGeneration = 0;
        throw error;
      }
    }

    function closeConnection(): void {
      activeConnectionGeneration = 0;
      const closing = connection;
      connection = undefined;
      closing?.close();
    }

    async function recoverAndReconnect(): Promise<void> {
      if (disposed || reconnectInFlight) return;
      reconnectInFlight = true;
      closeConnection();
      setValue((current) => ({ ...current, scopeWorkspaceId: workspaceId, state: "connecting" }));
      try {
        await recover();
        if (isActive()) startConnection();
      } catch {
        if (isActive()) {
          retryRecoveryRef.current = retryRecoveryAttempt;
          setValue((current) => ({ ...current, scopeWorkspaceId: workspaceId, state: "recovery_failed" }));
        }
      } finally {
        reconnectInFlight = false;
      }
    }

    function retryRecoveryAttempt(): void {
      void recoverAndReconnect();
    }

    try {
      startConnection(window.sessionStorage.getItem(storageKey) ?? undefined);
    } catch (error: unknown) {
      if (!(error instanceof ServerEventClientError) || error.code !== "CURSOR_REJECTED") {
        setValue({ scopeWorkspaceId: workspaceId, state: "closed" });
      } else {
        retryRecoveryRef.current = retryRecoveryAttempt;
        retryRecoveryAttempt();
      }
    }

    return () => {
      disposed = true;
      if (activeEffectRef.current === effectIdentity) activeEffectRef.current = undefined;
      retryRecoveryRef.current = undefined;
      closeConnection();
      removeWorkspaceQueries();
    };
  }, [queryClient, workspaceId]);

  const visibleValue: EventStoreSnapshot = value.scopeWorkspaceId === workspaceId
    ? {
        state: value.state,
        ...(value.lastEventId === undefined ? {} : { lastEventId: value.lastEventId }),
        ...(value.lastEvent === undefined ? {} : { lastEvent: value.lastEvent }),
      }
    : { state: workspaceId === "" ? "closed" : "connecting" };

  return <EventStoreContext.Provider value={{ ...visibleValue, retryRecovery, registerRecovery }}>{children}</EventStoreContext.Provider>;
};

export const useEventStore = (): EventStoreValue => useContext(EventStoreContext);
export const useRegisterWorkspaceRecovery = (recover: WorkspaceRecovery): void => {
  const { registerRecovery } = useContext(EventStoreContext);
  useEffect(() => registerRecovery(recover), [recover, registerRecovery]);
};
export const useWorkspaceEventState = () => useEventStore().state;
