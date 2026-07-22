import { useQueryClient } from "@tanstack/react-query";
import { type PropsWithChildren, useEffect } from "react";
import { useLocation } from "react-router-dom";

import { useActiveWorkspaceId } from "../app/active-workspace";
import { connectServerEvents, type ServerEventEnvelope } from "./server-events";

const cursorKey = (workspaceId: string): string =>
  `zhixu.collection-health-event-cursor.${workspaceId}`;

const affectsCollection = (event: ServerEventEnvelope): boolean =>
  event.type.startsWith("collection.")
  || event.resourceRef.startsWith("collection:")
  || event.invalidations.some((item) => item.resource === "collection");

const affectsHealth = (event: ServerEventEnvelope): boolean =>
  event.type.startsWith("health.")
  || event.resourceRef.startsWith("health_scan:")
  || event.resourceRef.startsWith("health_issue:")
  || event.invalidations.some((item) =>
    item.resource === "health_issue"
    || item.resource === "health_scan"
    || item.resource === "knowledge_health");

/**
 * 在 Collection/Health 路由内消费 Workspace SSE，仅执行 Query invalidation。
 * 事件游标在所有失效完成后提交，业务终态始终由 REST 投影恢复。
 */
export const CollectionHealthEventBridge = ({ children }: PropsWithChildren) => {
  const workspaceId = useActiveWorkspaceId();
  const location = useLocation();
  const queryClient = useQueryClient();
  const enabled = location.pathname === "/collections"
    || location.pathname.startsWith("/collections/")
    || location.pathname === "/health";

  useEffect(() => {
    if (!enabled || workspaceId === "") return undefined;
    const storageKey = cursorKey(workspaceId);
    const recover = async (): Promise<void> => {
      await queryClient.refetchQueries(
        { queryKey: ["collections", workspaceId], type: "all" },
        { throwOnError: true },
      );
      await queryClient.refetchQueries(
        { queryKey: ["knowledge-health", workspaceId], type: "all" },
        { throwOnError: true },
      );
      window.sessionStorage.removeItem(storageKey);
    };
    const lastEventId = window.sessionStorage.getItem(storageKey);
    const connection = connectServerEvents({
      workspaceId,
      ...(lastEventId === null ? {} : { lastEventId }),
      onRecoveryRequired: recover,
      onEvent: async (event) => {
        const invalidatesCollection = affectsCollection(event);
        const invalidatesHealth = affectsHealth(event);
        if (invalidatesCollection) {
          await queryClient.invalidateQueries({ queryKey: ["collections", workspaceId] });
        } else if (invalidatesHealth) {
          await queryClient.invalidateQueries({ queryKey: ["collections", workspaceId, "results"] });
        }
        if (invalidatesHealth) {
          await queryClient.invalidateQueries({ queryKey: ["knowledge-health", workspaceId] });
        }
        window.sessionStorage.setItem(storageKey, event.id);
      },
    });
    return () => connection.close();
  }, [enabled, queryClient, workspaceId]);

  return children;
};
