import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import { type PropsWithChildren } from "react";
import { describe, expect, it, vi } from "vitest";

const workspaceId = "72000000-0000-4000-8000-000000000001";
const otherWorkspaceId = "72000000-0000-4000-8000-000000000002";
const sessionId = "72000000-0000-4000-8000-000000000010";
const deckId = "72000000-0000-4000-8000-000000000011";

vi.mock("../../app/active-workspace", () => ({
  useActiveWorkspaceId: () => workspaceId,
}));

const apiMocks = vi.hoisted(() => ({
  listReviewDecks: vi.fn(),
  listReviewCards: vi.fn(),
  listReviewDue: vi.fn(),
  createReviewCard: vi.fn(),
  editReviewCard: vi.fn(),
  approveReviewCard: vi.fn(),
  rejectReviewCard: vi.fn(),
  createReviewDeck: vi.fn(),
  pauseReviewDeck: vi.fn(),
  resumeReviewDeck: vi.fn(),
  resetReviewDeck: vi.fn(),
  startReviewSession: vi.fn(),
  completeReviewSession: vi.fn(),
  submitReviewAnswer: vi.fn(),
}));

vi.mock("../../api/review", () => apiMocks);

import {
  clearReviewWorkspaceQueries,
  useCompleteReviewSession,
  useReviewCards,
  useReviewDue,
} from "./queries";
import { reviewQueryKeys } from "./query-keys";

const wrapperFor = (queryClient: QueryClient) => ({ children }: PropsWithChildren) => (
  <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
);

describe("Review query boundary", () => {
  it("所有 Review 查询键都绑定 Workspace，且清理不会误删其他 Workspace", () => {
    const queryClient = new QueryClient();
    queryClient.setQueryData(reviewQueryKeys.decks(workspaceId), { items: ["a"] });
    queryClient.setQueryData(reviewQueryKeys.cards(workspaceId, "deck-a"), { items: ["a"] });
		queryClient.setQueryData(reviewQueryKeys.due(workspaceId, sessionId, deckId), { items: ["a"] });
    queryClient.setQueryData(reviewQueryKeys.decks(otherWorkspaceId), { items: ["b"] });

    clearReviewWorkspaceQueries(queryClient, workspaceId);

    expect(queryClient.getQueryData(reviewQueryKeys.decks(workspaceId))).toBeUndefined();
    expect(queryClient.getQueryData(reviewQueryKeys.cards(workspaceId, "deck-a"))).toBeUndefined();
		expect(queryClient.getQueryData(reviewQueryKeys.due(workspaceId, sessionId, deckId))).toBeUndefined();
    expect(queryClient.getQueryData(reviewQueryKeys.decks(otherWorkspaceId))).toEqual({ items: ["b"] });
  });

  it("Card 列表查询同时绑定活动 Workspace 与 Deck", async () => {
    const deckId = "72000000-0000-4000-8000-000000000010";
    apiMocks.listReviewCards.mockResolvedValue({ workspaceId, deckId, items: [] });
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const { result } = renderHook(() => useReviewCards(deckId), { wrapper: wrapperFor(queryClient) });

    await waitFor(() => expect(result.current.isSuccess).toBe(true));

    expect(apiMocks.listReviewCards).toHaveBeenCalledWith(workspaceId, deckId, expect.any(AbortSignal));
    expect(queryClient.getQueryData(reviewQueryKeys.cards(workspaceId, deckId))).toEqual({ workspaceId, deckId, items: [] });
  });

	it("Due 查询把活动 Workspace、Session 和 Deck 同时传给唯一 API 边界", async () => {
		apiMocks.listReviewDue.mockResolvedValue({ workspaceId, items: [] });
		const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
		const { result } = renderHook(() => useReviewDue(sessionId, deckId), { wrapper: wrapperFor(queryClient) });

    await act(async () => { await result.current.refetch(); });

		await waitFor(() => expect(apiMocks.listReviewDue).toHaveBeenCalledWith(workspaceId, sessionId, deckId, expect.any(AbortSignal)));
	});

  it("完成 Session 后保留已关闭 Session 的 Due 缓存，不触发无效重查", async () => {
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const dueKey = reviewQueryKeys.due(workspaceId, sessionId, deckId);
    const cachedDue = { workspaceId, items: [] };
    const completedSession = {
      id: sessionId,
      workspaceId,
      deckId,
      sessionType: "REVIEW" as const,
      status: "COMPLETED" as const,
      config: {},
      startedAt: "2026-07-28T00:00:00Z",
      endedAt: "2026-07-28T00:05:00Z",
    };
    queryClient.setQueryData(dueKey, cachedDue);
    apiMocks.completeReviewSession.mockResolvedValue(completedSession);
    const { result } = renderHook(() => useCompleteReviewSession(), {
      wrapper: wrapperFor(queryClient),
    });

    await act(async () => {
      await result.current.mutateAsync({
        workspaceId,
        sessionId,
        cancelled: false,
        idempotencyKey: "complete-session-test",
      });
    });

    expect(queryClient.getQueryData(dueKey)).toBe(cachedDue);
    expect(queryClient.getQueryState(dueKey)?.isInvalidated).toBe(false);
    expect(
      queryClient.getQueryData(reviewQueryKeys.session(workspaceId, sessionId)),
    ).toEqual(completedSession);
  });
});
