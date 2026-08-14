import { describe, expect, it, vi } from "vitest";

import {
  connectAnswerDraftStream,
  parseAnswerDraftStream,
  type AnswerDraftStreamEvent,
} from "./answer-draft-stream";

const workspaceId = "95000000-0000-4000-8000-000000000001";
const answerId = "95000000-0000-4000-8000-000000000002";
const encoder = new TextEncoder();

const response = (chunks: string[], contentType = "text/event-stream; charset=utf-8"): Response => new Response(new ReadableStream<Uint8Array>({
  start(controller) {
    for (const chunk of chunks) controller.enqueue(encoder.encode(chunk));
    controller.close();
  },
}), { status: 200, headers: { "Content-Type": contentType } });

const chunkFrame = (generation: number, sequence: number, content: string): string =>
  `id: ${String(generation)}:${String(sequence)}\nevent: chunk\ndata: ${JSON.stringify({ generation, sequence, content })}\n\n`;

const endFrame = (generation: number): string =>
  `event: end\ndata: ${JSON.stringify({ generation, status: "PUBLISHED", action: "refetch" })}\n\n`;

const resetFrames = (generation: number): string =>
  `event: reset\ndata: ${JSON.stringify({ generation, reason: "generation_replaced", action: "refetch" })}\n\n` +
  `event: end\ndata: ${JSON.stringify({ generation, status: "RESET", action: "refetch" })}\n\n`;

describe("parseAnswerDraftStream", () => {
  it("跨网络 chunk 解析 CRLF、heartbeat、草稿正文和无 id 的终止事件", async () => {
    const body = response([
      ": heart",
      "beat\r\n\r\nid: 2:1\r\nevent: chunk\r\ndata: ",
      `${JSON.stringify({ generation: 2, sequence: 1, content: "第一段" })}\r\n\r\n`,
      endFrame(2),
    ]).body;
    if (body === null) throw new Error("missing body");

    const events: AnswerDraftStreamEvent[] = [];
    for await (const event of parseAnswerDraftStream(body)) events.push(event);

    expect(events).toEqual([
      { type: "chunk", id: "2:1", generation: 2, sequence: 1, content: "第一段" },
      { type: "end", generation: 2, status: "PUBLISHED", action: "refetch" },
    ]);
  });

  it("接受租约失效的公开 draft_stale reset reason", async () => {
    const body = response([
      `event: reset\ndata: ${JSON.stringify({ generation: 0, reason: "draft_stale", action: "refetch" })}\n\n`,
      `event: end\ndata: ${JSON.stringify({ generation: 0, status: "RESET", action: "refetch" })}\n\n`,
    ]).body;
    if (body === null) throw new Error("missing body");

    const events: AnswerDraftStreamEvent[] = [];
    for await (const event of parseAnswerDraftStream(body)) events.push(event);

    expect(events[0]).toEqual({ type: "reset", generation: 0, reason: "draft_stale", action: "refetch" });
  });

  it.each([
    ["chunk id 不匹配", "id: 2:2\nevent: chunk\ndata: {\"generation\":2,\"sequence\":1,\"content\":\"x\"}\n\n"],
    ["终止事件携带 id", `id: 2:1\n${endFrame(2)}`],
    ["data 含未知字段", "id: 2:1\nevent: chunk\ndata: {\"generation\":2,\"sequence\":1,\"content\":\"x\",\"extra\":true}\n\n"],
    ["帧在 EOF 截断", chunkFrame(2, 1, "x").slice(0, -1)],
  ])("拒绝%s", async (_name, wire) => {
    const body = response([wire]).body;
    if (body === null) throw new Error("missing body");

    await expect(async () => {
      for await (const event of parseAnswerDraftStream(body)) void event;
    }).rejects.toMatchObject({ code: "INVALID_EVENT" });
  });
});

describe("connectAnswerDraftStream", () => {
  it("拒绝仅具有 SSE 前缀的非 SSE Content-Type", async () => {
    const onEvent = vi.fn();
    const onRecoveryRequired = vi.fn();
    const onError = vi.fn();
    const connection = connectAnswerDraftStream({
      workspaceId,
      answerId,
      fetcher: () => Promise.resolve(response([endFrame(1)], "text/event-streaming")),
      onEvent,
      onRecoveryRequired,
      onError,
    });

    await connection.done;

    expect(onEvent).not.toHaveBeenCalled();
    expect(onError).toHaveBeenCalledWith(expect.objectContaining({ code: "INVALID_RESPONSE", retryable: false }));
    expect(onRecoveryRequired).toHaveBeenCalledWith("invalid_stream");
  });

  it("传输中断后使用已成功处理的 Last-Event-ID 重连，end 后回查正式 Answer", async () => {
    const requests: RequestInit[] = [];
    const fetcher = vi.fn((_path: string, init?: RequestInit): Promise<Response> => {
      requests.push(init ?? {});
      return Promise.resolve(requests.length === 1
        ? response([chunkFrame(2, 1, "第一段")])
        : response([chunkFrame(2, 2, "第二段"), endFrame(2)]));
    });
    const events: AnswerDraftStreamEvent[] = [];
    const recoveries: string[] = [];

    const connection = connectAnswerDraftStream({
      workspaceId,
      answerId,
      fetcher,
      sleep: () => Promise.resolve(),
      random: () => 0.5,
      onEvent: (event) => { events.push(event); },
      onRecoveryRequired: (reason) => { recoveries.push(reason); },
    });
    await connection.done;

    expect(fetcher).toHaveBeenCalledTimes(2);
    expect(new Headers(requests[0]?.headers).get("Accept")).toBe("text/event-stream");
    expect(new Headers(requests[0]?.headers).get("Last-Event-ID")).toBeNull();
    expect(new Headers(requests[1]?.headers).get("Last-Event-ID")).toBe("2:1");
    expect(events.filter((event) => event.type === "chunk").map((event) => event.content)).toEqual(["第一段", "第二段"]);
    expect(recoveries).toEqual(["end"]);
    expect(connection.getLastEventId()).toBeUndefined();
  });

  it("序号断档不进入 feature，清空游标并要求正式 Answer 回查", async () => {
    const onEvent = vi.fn();
    const onRecoveryRequired = vi.fn();
    const onError = vi.fn();
    const connection = connectAnswerDraftStream({
      workspaceId,
      answerId,
      fetcher: () => Promise.resolve(response([chunkFrame(2, 2, "越过第一段")])),
      onEvent,
      onRecoveryRequired,
      onError,
    });

    await connection.done;

    expect(onEvent).not.toHaveBeenCalled();
    expect(onError).toHaveBeenCalledWith(expect.objectContaining({ code: "SEQUENCE_GAP", retryable: false }));
    expect(onRecoveryRequired).toHaveBeenCalledWith("gap");
    expect(connection.getLastEventId()).toBeUndefined();
  });

  it("旧 generation 收到 reset 后先回查，再无游标连接当前 generation", async () => {
    const headers: Headers[] = [];
    const sleep = vi.fn(() => Promise.resolve());
    const fetcher = vi.fn((_path: string, init?: RequestInit): Promise<Response> => {
      headers.push(new Headers(init?.headers));
      return Promise.resolve(headers.length === 1
        ? response([resetFrames(3)])
        : response([chunkFrame(3, 1, "新一代草稿"), endFrame(3)]));
    });
    const recoveries: string[] = [];
    const events: AnswerDraftStreamEvent[] = [];
    const connection = connectAnswerDraftStream({
      workspaceId,
      answerId,
      lastEventId: "2:1",
      fetcher,
      sleep,
      onEvent: (event) => { events.push(event); },
      onRecoveryRequired: (reason) => { recoveries.push(reason); },
    });

    await connection.done;

    expect(headers[0]?.get("Last-Event-ID")).toBe("2:1");
    expect(headers[1]?.get("Last-Event-ID")).toBeNull();
    expect(sleep).toHaveBeenCalledOnce();
    expect(recoveries).toEqual(["reset", "end"]);
    expect(events.find((event) => event.type === "chunk")).toMatchObject({ generation: 3, sequence: 1, content: "新一代草稿" });
  });

  it("主动关闭会取消 fetch 与 reader，不上报伪网络错误", async () => {
    let requestSignal: AbortSignal | null | undefined;
    let readerCancelled = 0;
    const body = new ReadableStream<Uint8Array>({
      cancel() {
        readerCancelled += 1;
      },
    });
    const onError = vi.fn();
    const fetcher = vi.fn((_path: string, init?: RequestInit): Promise<Response> => {
      requestSignal = init?.signal;
      return Promise.resolve(new Response(body, { status: 200, headers: { "Content-Type": "text/event-stream" } }));
    });
    const connection = connectAnswerDraftStream({
      workspaceId,
      answerId,
      fetcher,
      onEvent: vi.fn(),
      onRecoveryRequired: vi.fn(),
      onError,
    });
    await vi.waitFor(() => expect(fetcher).toHaveBeenCalledOnce());

    connection.close();
    await connection.done;

    expect(requestSignal?.aborted).toBe(true);
    expect(readerCancelled).toBe(1);
    expect(onError).not.toHaveBeenCalled();
  });
});
