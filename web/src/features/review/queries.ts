import {
  useMutation,
  useQuery,
  useQueryClient,
  type QueryClient,
} from "@tanstack/react-query";

import {
  approveReviewCard,
  completeReviewSession,
  createReviewLearningPath,
  createReviewCard,
  createReviewDeck,
  editReviewCard,
  getReviewLearningPath,
  listReviewCards,
  listReviewDecks,
  listReviewDue,
  pauseReviewDeck,
  rejectReviewCard,
  resetReviewDeck,
  resumeReviewDeck,
  startReviewSession,
  submitReviewAnswer,
  updateReviewLearningPathStatus,
  updateReviewLearningPathStep,
  type CompleteReviewSessionInput,
  type CreateReviewCardInput,
  type CreateReviewDeckInput,
  type CreateReviewLearningPathInput,
  type EditReviewCardInput,
  type ReviewCard,
  type ReviewCardDecisionInput,
  type ReviewDeck,
  type ReviewDeckScheduleInput,
  type StartReviewSessionInput,
  type SubmitReviewAnswerInput,
  type ReviewLearningPathResult,
  type UpdateReviewLearningPathStatusInput,
  type UpdateReviewLearningPathStepInput,
  ReviewApiError,
} from "../../api/review";
import { useActiveWorkspaceId } from "../../app/active-workspace";
import { reviewQueryKeys } from "./query-keys";

/** Workspace 切换或登出时清除 Review 的全部派生缓存。 */
export const clearReviewWorkspaceQueries = (
  queryClient: QueryClient,
  workspaceId: string,
): void => {
  if (workspaceId === "") return;
  const queryKey = reviewQueryKeys.all(workspaceId);
  void queryClient.cancelQueries({ queryKey });
  queryClient.removeQueries({ queryKey });
};

export const useReviewDecks = () => {
  const workspaceId = useActiveWorkspaceId();
  return useQuery({
    queryKey: reviewQueryKeys.decks(workspaceId),
    queryFn: ({ signal }) => listReviewDecks(workspaceId, signal),
    enabled: workspaceId !== "",
    retry: false,
  });
};

export const useReviewDue = (
  sessionId: string,
  deckId = "",
  enabled = true,
) => {
  const workspaceId = useActiveWorkspaceId();
  return useQuery({
    queryKey: reviewQueryKeys.due(workspaceId, sessionId, deckId),
    queryFn: ({ signal }) =>
      listReviewDue(
        workspaceId,
        sessionId,
        deckId === "" ? undefined : deckId,
        signal,
      ),
    enabled: enabled && workspaceId !== "" && sessionId !== "",
    retry: false,
  });
};

export const useReviewCards = (deckId: string) => {
  const workspaceId = useActiveWorkspaceId();
  return useQuery({
    queryKey: reviewQueryKeys.cards(workspaceId, deckId),
    queryFn: ({ signal }) => listReviewCards(workspaceId, deckId, signal),
    enabled: workspaceId !== "" && deckId !== "",
    retry: false,
  });
};

export const useCreateReviewDeck = () => {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (input: CreateReviewDeckInput) => createReviewDeck(input),
    onSuccess: (deck) => {
      queryClient.setQueryData(
        reviewQueryKeys.deck(deck.workspaceId, deck.id),
        deck,
      );
      void queryClient.invalidateQueries({
        queryKey: reviewQueryKeys.decks(deck.workspaceId),
      });
    },
  });
};

const useReviewCardCommand = <TInput>(
  execute: (input: TInput) => Promise<ReviewCard>,
) => {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (input: TInput) => execute(input),
    onSuccess: (card) => {
      queryClient.setQueryData(
        reviewQueryKeys.card(card.workspaceId, card.id),
        card,
      );
      void queryClient.invalidateQueries({
        queryKey: reviewQueryKeys.cards(card.workspaceId, card.deckId),
      });
      void queryClient.invalidateQueries({
        queryKey: reviewQueryKeys.dueAll(card.workspaceId),
      });
    },
  });
};

export const useCreateReviewCard = () =>
  useReviewCardCommand<CreateReviewCardInput>(createReviewCard);
export const useEditReviewCard = () =>
  useReviewCardCommand<EditReviewCardInput>(editReviewCard);
export const useApproveReviewCard = () =>
  useReviewCardCommand<ReviewCardDecisionInput>(approveReviewCard);
export const useRejectReviewCard = () =>
  useReviewCardCommand<ReviewCardDecisionInput>(rejectReviewCard);

const useReviewDeckScheduleCommand = (
  execute: (input: ReviewDeckScheduleInput) => Promise<ReviewDeck>,
) => {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (input: ReviewDeckScheduleInput) => execute(input),
    onSuccess: (deck) => {
      queryClient.setQueryData(
        reviewQueryKeys.deck(deck.workspaceId, deck.id),
        deck,
      );
      void queryClient.invalidateQueries({
        queryKey: reviewQueryKeys.decks(deck.workspaceId),
      });
      void queryClient.invalidateQueries({
        queryKey: reviewQueryKeys.dueAll(deck.workspaceId),
      });
    },
  });
};

export const usePauseReviewDeck = () =>
  useReviewDeckScheduleCommand(pauseReviewDeck);
export const useResumeReviewDeck = () =>
  useReviewDeckScheduleCommand(resumeReviewDeck);
export const useResetReviewDeck = () =>
  useReviewDeckScheduleCommand(resetReviewDeck);

export const useStartReviewSession = () => {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (input: StartReviewSessionInput) => startReviewSession(input),
    onSuccess: (session) => {
      queryClient.setQueryData(
        reviewQueryKeys.session(session.workspaceId, session.id),
        session,
      );
      void queryClient.invalidateQueries({
        queryKey: reviewQueryKeys.dueAll(session.workspaceId),
      });
    },
  });
};

export const useCompleteReviewSession = () => {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (input: CompleteReviewSessionInput) =>
      completeReviewSession(input),
    onSuccess: (session) => {
      queryClient.setQueryData(
        reviewQueryKeys.session(session.workspaceId, session.id),
        session,
      );
    },
  });
};

export const useSubmitReviewAnswer = () => {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (input: SubmitReviewAnswerInput) => submitReviewAnswer(input),
    onSuccess: (result) => {
      void queryClient.invalidateQueries({
        queryKey: reviewQueryKeys.dueAll(result.answer.workspaceId),
      });
      void queryClient.invalidateQueries({
        queryKey: reviewQueryKeys.session(
          result.answer.workspaceId,
          result.answer.sessionId,
        ),
      });
    },
  });
};

export const useReviewLearningPath = (answerId: string, enabled = true) => {
  const workspaceId = useActiveWorkspaceId();
  return useQuery({
    queryKey: reviewQueryKeys.learningPath(workspaceId, answerId),
    queryFn: ({ signal }) =>
      getReviewLearningPath(workspaceId, answerId, signal),
    enabled: enabled && workspaceId !== "" && answerId !== "",
    retry: false,
  });
};

export const useCreateReviewLearningPath = () => {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (input: CreateReviewLearningPathInput) =>
      createReviewLearningPath(input),
    onSuccess: async (result) => {
      queryClient.setQueryData(
        reviewQueryKeys.learningPath(
          result.path.workspaceId,
          result.path.reviewAnswerId,
        ),
        result,
      );
      await queryClient.invalidateQueries({
        queryKey: reviewQueryKeys.learningPath(
          result.path.workspaceId,
          result.path.reviewAnswerId,
        ),
      });
    },
  });
};

const refreshLearningPathAfterConflict = async (
  queryClient: QueryClient,
  error: unknown,
  input:
    | UpdateReviewLearningPathStatusInput
    | UpdateReviewLearningPathStepInput,
): Promise<void> => {
  if (!(error instanceof ReviewApiError) || error.status !== 409) return;
  await queryClient.invalidateQueries({
    queryKey: reviewQueryKeys.learningPath(input.workspaceId, input.answerId),
    exact: true,
    refetchType: "all",
  });
};

export const useUpdateReviewLearningPathStatus = () => {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (input: UpdateReviewLearningPathStatusInput) =>
      updateReviewLearningPathStatus(input),
    onSuccess: async (result) => {
      const key = reviewQueryKeys.learningPath(
        result.path.workspaceId,
        result.path.reviewAnswerId,
      );
      queryClient.setQueryData<ReviewLearningPathResult>(key, (current) =>
        current === undefined ? current : { ...current, path: result.path },
      );
      await queryClient.invalidateQueries({ queryKey: key });
    },
    onError: async (error, input) => {
      await refreshLearningPathAfterConflict(queryClient, error, input);
    },
  });
};

export const useUpdateReviewLearningPathStep = () => {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (input: UpdateReviewLearningPathStepInput) =>
      updateReviewLearningPathStep(input),
    onSuccess: async (result) => {
      const key = reviewQueryKeys.learningPath(
        result.path.workspaceId,
        result.path.reviewAnswerId,
      );
      queryClient.setQueryData<ReviewLearningPathResult>(key, (current) =>
        current === undefined
          ? current
          : {
              ...current,
              path: result.path,
              steps: current.steps.map((step) =>
                step.id === result.step.id ? result.step : step,
              ),
            },
      );
      await queryClient.invalidateQueries({ queryKey: key });
    },
    onError: async (error, input) => {
      await refreshLearningPathAfterConflict(queryClient, error, input);
    },
  });
};
