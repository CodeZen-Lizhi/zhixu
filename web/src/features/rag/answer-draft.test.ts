import { act, renderHook, waitFor } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import { answerDraftReducer, emptyAnswerDraft, usePendingAnswerDraft } from "./answer-draft";

const answerId = "95000000-0000-4000-8000-000000000002";

describe("answerDraftReducer", () => {
  it("只拼接同 generation 的连续 chunk，reset 不保留临时正文", () => {
    const first = answerDraftReducer(emptyAnswerDraft(answerId), {
      type: "event",
      answerId,
      event: { type: "chunk", id: "2:1", generation: 2, sequence: 1, content: "第一段" },
    });
    const second = answerDraftReducer(first, {
      type: "event",
      answerId,
      event: { type: "chunk", id: "2:2", generation: 2, sequence: 2, content: "\n第二段" },
    });
    expect(second).toMatchObject({ generation: 2, sequence: 2, content: "第一段\n第二段", connectionState: "open" });

    expect(answerDraftReducer(second, {
      type: "event",
      answerId,
      event: { type: "reset", generation: 3, reason: "generation_replaced", action: "refetch" },
    })).toEqual(emptyAnswerDraft(answerId));
  });

  it("拒绝首帧缺失和 generation 漂移，忽略旧 Answer 的迟到 callback", () => {
    const initial = emptyAnswerDraft(answerId);
    expect(() => answerDraftReducer(initial, {
      type: "event",
      answerId,
      event: { type: "chunk", id: "2:2", generation: 2, sequence: 2, content: "gap" },
    })).toThrow(/不连续/);
    const first = answerDraftReducer(initial, {
      type: "event",
      answerId,
      event: { type: "chunk", id: "2:1", generation: 2, sequence: 1, content: "first" },
    });
    expect(() => answerDraftReducer(first, {
      type: "event",
      answerId,
      event: { type: "chunk", id: "3:2", generation: 3, sequence: 2, content: "wrong generation" },
    })).toThrow(/不连续/);
    expect(answerDraftReducer(first, { type: "clear", answerId: "95000000-0000-4000-8000-000000000003" })).toBe(first);
  });
});

describe("usePendingAnswerDraft", () => {
  it("只在局部状态显示 chunk，并在 end 后清空草稿、回查正式 Answer", async () => {
    let streamController: ReadableStreamDefaultController<Uint8Array> | undefined;
    const body = new ReadableStream<Uint8Array>({
      start(controller) {
        streamController = controller;
      },
    });
    const fetchMock = vi.fn<typeof fetch>().mockResolvedValue(new Response(body, {
      status: 200,
      headers: { "Content-Type": "text/event-stream" },
    }));
    vi.stubGlobal("fetch", fetchMock);
    const refetchAnswer = vi.fn(() => Promise.resolve());
    const { result, unmount } = renderHook(() => usePendingAnswerDraft({
      workspaceId: "95000000-0000-4000-8000-000000000001",
      answerId,
      enabled: true,
      refetchAnswer,
    }));
    await waitFor(() => expect(fetchMock).toHaveBeenCalledOnce());
    if (streamController === undefined) throw new Error("missing stream controller");

    act(() => {
      streamController?.enqueue(new TextEncoder().encode(
        "id: 2:1\nevent: chunk\ndata: {\"generation\":2,\"sequence\":1,\"content\":\"临时正文\"}\n\n",
      ));
    });
    await waitFor(() => expect(result.current).toMatchObject({ generation: 2, sequence: 1, content: "临时正文" }));

    act(() => {
      streamController?.enqueue(new TextEncoder().encode(
        "event: end\ndata: {\"generation\":2,\"status\":\"PUBLISHED\",\"action\":\"refetch\"}\n\n",
      ));
      streamController?.close();
    });
    await waitFor(() => expect(refetchAnswer).toHaveBeenCalledOnce());
    expect(result.current.content).toBe("");
    unmount();
  });
});
