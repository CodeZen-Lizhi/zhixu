import { useInfiniteQuery, useMutation, useQuery, useQueryClient, type QueryClient } from "@tanstack/react-query";

import {
  completeInterview,
  getInterview,
  listInterviewSessions,
  suggestInterviewMemoryCandidate,
  startInterview,
  submitInterviewTurn,
  updateLearningPathStatus,
  updateLearningPathStep,
  type InterviewSnapshot,
} from "../../api/interview";
import { useActiveWorkspaceId } from "../../app/active-workspace";
import { interviewQueryKeys } from "./query-keys";

/** Workspace 切换或登出时清除 Interview 与 Learning Path 的全部缓存。 */
export const clearInterviewWorkspaceQueries = (queryClient: QueryClient, workspaceId: string): void => {
  if (workspaceId === "") return;
  const queryKey = interviewQueryKeys.all(workspaceId);
  void queryClient.cancelQueries({ queryKey });
  queryClient.removeQueries({ queryKey });
};

export const useInterview = (sessionId: string) => {
  const workspaceId = useActiveWorkspaceId();
  return useQuery({
    queryKey: interviewQueryKeys.session(workspaceId, sessionId),
    queryFn: ({ signal }) => getInterview(workspaceId, sessionId, signal),
    enabled: workspaceId !== "" && sessionId !== "",
    retry: false,
  });
};

export const useInterviewSessions = () => {
  const workspaceId = useActiveWorkspaceId();
  return useInfiniteQuery({
    queryKey: interviewQueryKeys.sessions(workspaceId),
    queryFn: ({ signal, pageParam }) => listInterviewSessions({
      workspaceId,
      limit: 20,
      ...(pageParam === undefined ? {} : { cursor: pageParam }),
    }, signal),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (page) => page.nextCursor,
    enabled: workspaceId !== "",
    retry: false,
  });
};

const refetchSession = (queryClient: QueryClient, workspaceId: string, sessionId: string): Promise<void> =>
  queryClient.invalidateQueries({ queryKey: interviewQueryKeys.session(workspaceId, sessionId) });

export const useStartInterview = () => {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: startInterview,
    onSuccess: (result) => {
      queryClient.setQueryData<InterviewSnapshot>(interviewQueryKeys.session(result.session.workspaceId, result.session.id), {
        session: result.session,
        questions: result.questions,
        turns: [],
        steps: [],
      });
      void queryClient.invalidateQueries({ queryKey: interviewQueryKeys.sessions(result.session.workspaceId) });
    },
  });
};

export const useSubmitInterviewTurn = () => {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: submitInterviewTurn,
    onSuccess: (result) => {
      void queryClient.invalidateQueries({ queryKey: interviewQueryKeys.sessions(result.turn.workspaceId) });
      return refetchSession(queryClient, result.turn.workspaceId, result.turn.sessionId);
    },
  });
};

export const useCompleteInterview = () => {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: completeInterview,
    onSuccess: (result) => {
      const key = interviewQueryKeys.session(result.session.workspaceId, result.session.id);
      queryClient.setQueryData<InterviewSnapshot>(key, (current) => current === undefined ? current : {
        ...current,
        session: result.session,
        report: result.report,
        path: result.path,
        steps: result.steps,
      });
      void queryClient.invalidateQueries({ queryKey: interviewQueryKeys.sessions(result.session.workspaceId) });
      return refetchSession(queryClient, result.session.workspaceId, result.session.id);
    },
  });
};

export const useSuggestInterviewMemoryCandidate = () => useMutation({ mutationFn: suggestInterviewMemoryCandidate });

export const useUpdateLearningPathStatus = () => {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: updateLearningPathStatus,
    onSuccess: (result) => {
      const { path } = result;
      const key = interviewQueryKeys.session(path.workspaceId, path.sessionId);
      queryClient.setQueryData<InterviewSnapshot>(key, (current) => current === undefined ? current : { ...current, path });
      return refetchSession(queryClient, path.workspaceId, path.sessionId);
    },
  });
};

export const useUpdateLearningPathStep = () => {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: updateLearningPathStep,
    onSuccess: (result) => {
      const { path, step } = result;
      const key = interviewQueryKeys.session(path.workspaceId, path.sessionId);
      queryClient.setQueryData<InterviewSnapshot>(key, (current) => current === undefined ? current : {
        ...current,
        path,
        steps: current.steps.map((item) => item.id === step.id ? step : item),
      });
      return refetchSession(queryClient, path.workspaceId, path.sessionId);
    },
  });
};
