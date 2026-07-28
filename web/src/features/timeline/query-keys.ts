import type { QueryClient } from "@tanstack/react-query";

export const timelineQueryKeys = {
  all: (workspaceId: string) => ["timeline", workspaceId] as const,
  lists: (workspaceId: string) => ["timeline", workspaceId, "events"] as const,
  list: (workspaceId: string, canonicalFilter: string, limit: number, cursor?: string) =>
    ["timeline", workspaceId, "events", canonicalFilter, limit, cursor ?? "first"] as const,
  event: (workspaceId: string, eventId: string) => ["timeline", workspaceId, "event", eventId] as const,
  reports: (workspaceId: string) => ["timeline", workspaceId, "impact-reports"] as const,
  report: (workspaceId: string, reportId: string) => ["timeline", workspaceId, "impact-reports", reportId] as const,
};

export const clearTimelineWorkspaceQueries = (queryClient: QueryClient, workspaceId: string): void => {
  if (workspaceId === "") return;
  const queryKey = timelineQueryKeys.all(workspaceId);
  void queryClient.cancelQueries({ queryKey });
  queryClient.removeQueries({ queryKey });
};
