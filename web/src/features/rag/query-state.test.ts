import { InfiniteQueryObserver, QueryClient, QueryObserver, type InfiniteData } from "@tanstack/react-query";
import { describe, expect, it, vi } from "vitest";

import type { Answer } from "../../api/conversation";
import type { ServerEventEnvelope } from "../../events";
import { invalidateRagEvent, recoverRagWorkspace } from "./event-recovery";
import { ragQueryKeys } from "./query-keys";
import { maximumPendingAnswerPolls, pendingAnswerPollInterval } from "./queries";

const workspaceId = "92000000-0000-4000-8000-000000000001";
const otherWorkspaceId = "92000000-0000-4000-8000-000000000002";
const conversationId = "92000000-0000-4000-8000-000000000003";
const answerId = "92000000-0000-4000-8000-000000000004";
interface Page { items: string[]; nextCursor?: string }

describe("RAG query state", () => {
  it("所有 key 都以 Workspace 分区", () => {
    expect(ragQueryKeys.answer(workspaceId, answerId)).not.toEqual(ragQueryKeys.answer(otherWorkspaceId, answerId));
    expect(ragQueryKeys.turns(workspaceId, conversationId)).not.toEqual(ragQueryKeys.turns(otherWorkspaceId, conversationId));
  });

  it("pending 只在有界次数内轮询", () => {
    const pending = { publicationStatus: "pending" } as Answer;
    expect(pendingAnswerPollInterval(pending, maximumPendingAnswerPolls - 1)).toBe(2_000);
    expect(pendingAnswerPollInterval(pending, maximumPendingAnswerPolls)).toBe(false);
    expect(pendingAnswerPollInterval({ publicationStatus: "completed" } as Answer, 1)).toBe(false);
  });

  it("SSE 只定向失效同 Workspace 的权威查询", async () => {
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const reset = vi.spyOn(queryClient, "resetQueries").mockResolvedValue(undefined);
    const invalidate = vi.spyOn(queryClient, "invalidateQueries").mockResolvedValue(undefined);
    const event = {
      workspaceId,
      payloadSummary: { conversationId, answerId },
      invalidations: [
        { resource: "conversation", id: conversationId },
        { resource: "conversation", id: conversationId },
        { resource: "answer", id: answerId },
        { resource: "workflow", id: "92000000-0000-4000-8000-000000000005" },
      ],
    } as ServerEventEnvelope;
    await invalidateRagEvent(queryClient, workspaceId, event);
    expect(reset).toHaveBeenCalledOnce();
    expect(reset).toHaveBeenCalledWith(
      { queryKey: ragQueryKeys.conversations(workspaceId), exact: true },
      { throwOnError: true },
    );
    expect(invalidate).toHaveBeenCalledTimes(3);
    expect(invalidate).toHaveBeenCalledWith(
      { queryKey: ragQueryKeys.conversation(workspaceId, conversationId), exact: true },
      { throwOnError: true },
    );
    expect(invalidate).toHaveBeenCalledWith(
      { queryKey: ragQueryKeys.turns(workspaceId, conversationId) },
      { throwOnError: true },
    );
    expect(invalidate).toHaveBeenCalledWith(
      { queryKey: ragQueryKeys.answer(workspaceId, answerId), exact: true },
      { throwOnError: true },
    );

    reset.mockClear();
    invalidate.mockClear();
    await invalidateRagEvent(queryClient, otherWorkspaceId, event);
    expect(reset).not.toHaveBeenCalled();
    expect(invalidate).not.toHaveBeenCalled();
  });

  it("cursor recovery 将两页 Conversation 缓存重置为首屏且只请求第一页", async () => {
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const queryKey = ragQueryKeys.conversations(workspaceId);
    const pageParams: unknown[] = [];
    let detailCalls = 0;
    const queryFn = ({ pageParam }: { pageParam: unknown }): Promise<Page> => {
      pageParams.push(pageParam);
      return Promise.resolve({ items: ["fresh-first"] });
    };
    queryClient.setQueryData<InfiniteData<Page>>(queryKey, {
      pages: [
        { items: ["cached-first"], nextCursor: "second" },
        { items: ["cached-second"] },
      ],
      pageParams: [undefined, "second"],
    });
    const detailKey = ragQueryKeys.conversation(workspaceId, conversationId);
    queryClient.setQueryData(detailKey, { id: conversationId });
    const observer = new InfiniteQueryObserver(queryClient, {
      queryKey,
      queryFn,
      initialPageParam: undefined,
      getNextPageParam: (page) => page.nextCursor,
      staleTime: Infinity,
    });
    const detailObserver = new QueryObserver(queryClient, {
      queryKey: detailKey,
      queryFn: () => {
        detailCalls += 1;
        return Promise.resolve({ id: conversationId });
      },
      staleTime: Infinity,
    });
    const unsubscribe = observer.subscribe(() => undefined);
    const unsubscribeDetail = detailObserver.subscribe(() => undefined);

    await recoverRagWorkspace(queryClient, workspaceId);

    expect(pageParams).toEqual([undefined]);
    expect(queryClient.getQueryData<InfiniteData<Page>>(queryKey)).toEqual({
      pages: [{ items: ["fresh-first"] }],
      pageParams: [undefined],
    });
    expect(detailCalls).toBe(1);
    unsubscribe();
    unsubscribeDetail();
  });

  it("同一 SSE 事件对每个活动 RAG Query 最多触发一次权威回查", async () => {
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const calls = { list: 0, detail: 0, turns: 0, latest: 0, answer: 0 };
    const listKey = ragQueryKeys.conversations(workspaceId);
    const detailKey = ragQueryKeys.conversation(workspaceId, conversationId);
    const turnsKey = ragQueryKeys.turns(workspaceId, conversationId);
    const latestKey = ragQueryKeys.latestTurn(workspaceId, conversationId);
    const answerKey = ragQueryKeys.answer(workspaceId, answerId);
    queryClient.setQueryData<InfiniteData<Page>>(listKey, { pages: [{ items: ["cached"] }], pageParams: [undefined] });
    queryClient.setQueryData(detailKey, { id: conversationId });
    queryClient.setQueryData<InfiniteData<Page>>(turnsKey, { pages: [{ items: ["turn"] }], pageParams: [undefined] });
    queryClient.setQueryData(latestKey, { id: "latest" });
    queryClient.setQueryData(answerKey, { id: answerId });
    const observers = [
      new InfiniteQueryObserver(queryClient, {
        queryKey: listKey,
        queryFn: () => { calls.list += 1; return Promise.resolve<Page>({ items: ["fresh"] }); },
        initialPageParam: undefined,
        getNextPageParam: (page) => page.nextCursor,
        staleTime: Infinity,
      }),
      new QueryObserver(queryClient, {
        queryKey: detailKey,
        queryFn: () => { calls.detail += 1; return Promise.resolve({ id: conversationId }); },
        staleTime: Infinity,
      }),
      new InfiniteQueryObserver(queryClient, {
        queryKey: turnsKey,
        queryFn: () => { calls.turns += 1; return Promise.resolve<Page>({ items: ["fresh-turn"] }); },
        initialPageParam: undefined,
        getNextPageParam: (page) => page.nextCursor,
        staleTime: Infinity,
      }),
      new QueryObserver(queryClient, {
        queryKey: latestKey,
        queryFn: () => { calls.latest += 1; return Promise.resolve({ id: "latest" }); },
        staleTime: Infinity,
      }),
      new QueryObserver(queryClient, {
        queryKey: answerKey,
        queryFn: () => { calls.answer += 1; return Promise.resolve({ id: answerId }); },
        staleTime: Infinity,
      }),
    ];
    const unsubscribes = observers.map((observer) => observer.subscribe(() => undefined));
    const event = {
      workspaceId,
      payloadSummary: { conversationId, answerId },
      invalidations: [
        { resource: "conversation", id: conversationId },
        { resource: "conversation", id: conversationId },
        { resource: "question", id: "92000000-0000-4000-8000-000000000006" },
        { resource: "workflow", id: "92000000-0000-4000-8000-000000000007" },
        { resource: "model_run", id: "92000000-0000-4000-8000-000000000008" },
        { resource: "answer", id: answerId },
      ],
    } as ServerEventEnvelope;

    await invalidateRagEvent(queryClient, workspaceId, event);

    expect(calls).toEqual({ list: 1, detail: 1, turns: 1, latest: 1, answer: 1 });
    for (const unsubscribe of unsubscribes) unsubscribe();
  });
});
