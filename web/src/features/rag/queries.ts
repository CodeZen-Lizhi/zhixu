import { useEffect, useRef } from "react";
import { useInfiniteQuery, useQuery } from "@tanstack/react-query";

import {
  getAnswer,
  getConversation,
  getLatestTurn,
  getWorkspaceAnalysisTimeline,
  listConversations,
  listTurns,
  type Answer,
  type QuestionMode,
  type WorkspaceAnalysisTimeline,
} from "../../api/conversation";
import { ragQueryKeys } from "./query-keys";

export const maximumPendingAnswerPolls = 144;
export const maximumWorkspaceAnalysisPendingAnswerPolls = 732;
export const pendingAnswerPollMilliseconds = 5_000;
export const maximumAnalysisTimelinePolls = 732;

export const pendingAnswerPollInterval = (
  answer: Answer | undefined,
  completedFetches: number,
  maximumPolls = maximumPendingAnswerPolls,
): number | false => answer?.publicationStatus === "pending" && completedFetches < maximumPolls
  ? pendingAnswerPollMilliseconds
  : false;

export const useConversationList = (workspaceId: string) => useInfiniteQuery({
  queryKey: ragQueryKeys.conversations(workspaceId),
  queryFn: ({ signal, pageParam }) => listConversations({ workspaceId, limit: 50, ...(pageParam === undefined ? {} : { cursor: pageParam }) }, signal),
  initialPageParam: undefined as string | undefined,
  getNextPageParam: (page) => page.nextCursor,
  enabled: workspaceId !== "",
});

export const useConversation = (workspaceId: string, conversationId: string) => useQuery({
  queryKey: ragQueryKeys.conversation(workspaceId, conversationId),
  queryFn: async ({ signal }) => (await getConversation({ workspaceId, id: conversationId }, signal)).resource,
  enabled: workspaceId !== "" && conversationId !== "",
});

export const useConversationTurns = (workspaceId: string, conversationId: string) => useInfiniteQuery({
  queryKey: ragQueryKeys.turns(workspaceId, conversationId),
  queryFn: ({ signal, pageParam }) => listTurns({ workspaceId, conversationId, limit: 50, ...(pageParam === undefined ? {} : { cursor: pageParam }) }, signal),
  initialPageParam: undefined as string | undefined,
  getNextPageParam: (page) => page.nextCursor,
  enabled: workspaceId !== "" && conversationId !== "",
});

export const useLatestTurn = (workspaceId: string, conversationId: string) => useQuery({
  queryKey: ragQueryKeys.latestTurn(workspaceId, conversationId),
  queryFn: ({ signal }) => getLatestTurn({ workspaceId, conversationId }, signal),
  enabled: workspaceId !== "" && conversationId !== "",
  retry: false,
});

export const useAnswer = (workspaceId: string, answerId: string, polling = false, mode: QuestionMode = "rag") => {
  const attempts = useRef(0);
  useEffect(() => { attempts.current = 0; }, [answerId, workspaceId]);
  return useQuery({
    queryKey: ragQueryKeys.answer(workspaceId, answerId),
    queryFn: async ({ signal }) => {
      attempts.current += 1;
      return (await getAnswer({ workspaceId, id: answerId }, signal)).resource;
    },
    enabled: workspaceId !== "" && answerId !== "",
    retry: false,
    refetchInterval: (query) => polling
      ? pendingAnswerPollInterval(
        query.state.data ?? undefined,
        attempts.current,
        mode === "workspace_analysis" ? maximumWorkspaceAnalysisPendingAnswerPolls : maximumPendingAnswerPolls,
      )
      : false,
  });
};

const analysisTimelinePollInterval = (
  timeline: WorkspaceAnalysisTimeline | undefined,
  completedFetches: number,
): number | false => (timeline?.runStatus === "queued" || timeline?.runStatus === "running") && completedFetches < maximumAnalysisTimelinePolls
  ? pendingAnswerPollMilliseconds
  : false;

export const useWorkspaceAnalysisTimeline = (workspaceId: string, answerId: string, enabled = true, polling = true) => {
  const attempts = useRef(0);
  useEffect(() => { attempts.current = 0; }, [answerId, workspaceId]);
  return useQuery({
    queryKey: ragQueryKeys.analysisTimeline(workspaceId, answerId),
    queryFn: async ({ signal }) => {
      attempts.current += 1;
      return getWorkspaceAnalysisTimeline({ workspaceId, answerId }, signal);
    },
    enabled: enabled && workspaceId !== "" && answerId !== "",
    retry: false,
    refetchInterval: (query) => polling ? analysisTimelinePollInterval(query.state.data, attempts.current) : false,
  });
};
