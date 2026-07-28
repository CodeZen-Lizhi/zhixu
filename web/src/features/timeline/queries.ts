import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { useActiveWorkspaceId } from "../../app/active-workspace";
import {
  analyzeImpact,
  createDownstreamUpdateProposal,
  getImpactReport,
  getTimelineEvent,
  listTimeline,
  type AnalyzeImpactInput,
  type CreateDownstreamUpdateProposalInput,
  type ImpactReport,
  type ListTimelineInput,
  type TimelineFilter,
} from "../../api/timeline";
import { timelineQueryKeys } from "./query-keys";

const normalizeFilter = (filter: TimelineFilter): TimelineFilter => ({
  ...(filter.eventTypes === undefined ? {} : { eventTypes: [...new Set(filter.eventTypes)].sort() }),
  ...(filter.aggregateType === undefined ? {} : { aggregateType: filter.aggregateType }),
  ...(filter.aggregateId === undefined ? {} : { aggregateId: filter.aggregateId }),
  ...(filter.sourceEventRef === undefined ? {} : { sourceEventRef: filter.sourceEventRef }),
  ...(filter.occurredAfter === undefined ? {} : { occurredAfter: filter.occurredAfter }),
  ...(filter.occurredBefore === undefined ? {} : { occurredBefore: filter.occurredBefore }),
});

export const canonicalTimelineFilter = (filter: TimelineFilter): string => JSON.stringify(normalizeFilter(filter));

export const useTimelinePage = (filter: TimelineFilter, cursor?: string, limit = 25) => {
  const workspaceId = useActiveWorkspaceId();
  const normalized = normalizeFilter(filter);
  const canonical = canonicalTimelineFilter(normalized);
  const request: ListTimelineInput = { workspaceId, ...normalized, ...(cursor === undefined ? {} : { cursor }), limit };
  return useQuery({
    queryKey: timelineQueryKeys.list(workspaceId, canonical, limit, cursor),
    queryFn: ({ signal }) => listTimeline(request, signal),
    enabled: workspaceId !== "",
    retry: false,
  });
};

export const useTimelineEvent = (eventId: string) => {
  const workspaceId = useActiveWorkspaceId();
  return useQuery({
    queryKey: timelineQueryKeys.event(workspaceId, eventId),
    queryFn: ({ signal }) => getTimelineEvent(workspaceId, eventId, signal),
    enabled: workspaceId !== "" && eventId !== "",
    retry: false,
  });
};

export const useImpactReport = (reportId: string) => {
  const workspaceId = useActiveWorkspaceId();
  return useQuery({
    queryKey: timelineQueryKeys.report(workspaceId, reportId),
    queryFn: ({ signal }) => getImpactReport(workspaceId, reportId, signal),
    enabled: workspaceId !== "" && reportId !== "",
    retry: false,
  });
};

export const useAnalyzeImpact = () => {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (input: AnalyzeImpactInput) => analyzeImpact(input),
    onSuccess: (result) => {
      const workspaceId = result.report.workspaceId;
      queryClient.setQueryData<ImpactReport>(timelineQueryKeys.report(workspaceId, result.report.id), result.report);
      void queryClient.invalidateQueries({ queryKey: timelineQueryKeys.event(workspaceId, result.report.sourceEventId) });
    },
  });
};

export const useCreateDownstreamUpdateProposal = () => {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (input: CreateDownstreamUpdateProposalInput) => createDownstreamUpdateProposal(input),
    onSuccess: (proposal) => {
      const workspaceId = proposal.workspaceId;
      void queryClient.invalidateQueries({ queryKey: timelineQueryKeys.report(workspaceId, proposal.revision.update.sourceReport.id) });
      void queryClient.invalidateQueries({ queryKey: ["business", workspaceId, "proposals"] });
    },
  });
};
