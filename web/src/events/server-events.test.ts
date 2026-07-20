import { describe, expect, it, vi } from "vitest";

import {
  ServerEventClientError,
  connectServerEvents,
  decodeServerEventEnvelope,
  parseServerEventStream,
  type ServerEventEnvelope,
} from "./server-events";

const workspaceId = "7a000000-0000-4000-8000-000000000001";
const answerId = "7a000000-0000-4000-8000-000000000002";
const workflowRunId = "7a000000-0000-4000-8000-000000000003";

const envelope = (id = "42"): Record<string, unknown> => ({
  schema_version: 1,
  id,
  type: "answer.completed",
  occurred_at: "2026-07-20T08:09:10.123Z",
  workspace_id: workspaceId,
  resource_ref: `answer:${answerId}`,
  resource_version: 2,
  payload_summary: {
    answer_id: answerId,
    workflow_run_id: workflowRunId,
    publication_status: "completed",
    citation_count: 3,
  },
});

const frame = (value: Record<string, unknown>): string =>
  `id: ${String(value.id)}\nevent: ${String(value.type)}\ndata: ${JSON.stringify(value)}\n\n`;

const streamResponse = (chunks: string[], status = 200): Response => {
  const encoder = new TextEncoder();
  return new Response(
    new ReadableStream<Uint8Array>({
      start(controller) {
        for (const chunk of chunks) controller.enqueue(encoder.encode(chunk));
        controller.close();
      },
    }),
    { status, headers: { "Content-Type": "text/event-stream" } },
  );
};

const problemResponse = (
  status: number,
  errorCode: string,
  details?: Record<string, unknown>,
): Response =>
  new Response(
    JSON.stringify({
      error_code: errorCode,
      message: "事件流请求未完成",
      retryable: false,
      ...(details === undefined ? {} : { details }),
    }),
    { status, headers: { "Content-Type": "application/json" } },
  );

const resolvedSleep = (): Promise<void> => Promise.resolve();
const ignoreRecovery = (): void => undefined;

describe("decodeServerEventEnvelope", () => {
  it("严格解码通知并产生定向失效提示", () => {
    expect(decodeServerEventEnvelope(envelope())).toEqual({
      schemaVersion: 1,
      id: "42",
      type: "answer.completed",
      occurredAt: "2026-07-20T08:09:10.123Z",
      workspaceId,
      resourceRef: `answer:${answerId}`,
      resourceVersion: 2,
      payloadSummary: {
        answerId,
        workflowRunId,
        publicationStatus: "completed",
        citationCount: 3,
      },
      invalidations: [
        { resource: "answer", id: answerId },
        { resource: "workflow", id: workflowRunId },
      ],
    });
  });

  it.each([
    ["未来 schema", { ...envelope(), schema_version: 2 }],
    ["额外 Envelope 字段", { ...envelope(), answer_text: "secret" }],
    ["非法 UUID", { ...envelope(), workspace_id: "invalid" }],
    ["非法时间", { ...envelope(), occurred_at: "2026-07-20" }],
    ["不存在的日期", { ...envelope(), occurred_at: "2026-02-31T08:09:10Z" }],
    ["超出 int64 的 id", { ...envelope(), id: "9223372036854775808" }],
    ["非安全整数版本", { ...envelope(), resource_version: 1.5 }],
    ["非法摘要 token", { ...envelope(), payload_summary: { status: "RUNNING" } }],
    ["摘要额外正文", { ...envelope(), payload_summary: { answer_text: "secret" } }],
  ])("拒绝%s", (_name, value) => {
    expect(() => decodeServerEventEnvelope(value)).toThrow(ServerEventClientError);
  });
});

describe("parseServerEventStream", () => {
  it("跨 chunk 解析 CRLF、heartbeat 与完整事件", async () => {
    const body = streamResponse([
      ": heart",
      "beat\r",
      "\n\r\nid: 42\r\nevent: answer.completed\r\ndata: ",
      `${JSON.stringify(envelope())}\r\n\r\n`,
    ]).body;
    if (body === null) throw new Error("test response body is missing");

    const parsed: ServerEventEnvelope[] = [];
    for await (const event of parseServerEventStream(body)) parsed.push(event);

    expect(parsed).toHaveLength(1);
    expect(parsed[0]?.id).toBe("42");
  });

  it("拒绝 SSE id/event 与 data Envelope 不一致", async () => {
    const body = streamResponse([frame(envelope("43")).replace("id: 43", "id: 42")]).body;
    if (body === null) throw new Error("test response body is missing");

    await expect(async () => {
      for await (const event of parseServerEventStream(body)) void event;
    }).rejects.toMatchObject({ code: "INVALID_EVENT" });
  });

  it("拒绝 EOF 截断帧与无界帧", async () => {
    const truncated = streamResponse([frame(envelope()).slice(0, -1)]).body;
    const oversized = streamResponse([`data: ${"x".repeat(33 * 1024)}`]).body;
    if (truncated === null || oversized === null) throw new Error("test response body is missing");

    await expect(async () => {
      for await (const event of parseServerEventStream(truncated)) void event;
    }).rejects.toMatchObject({ code: "INVALID_EVENT" });
    await expect(async () => {
      for await (const event of parseServerEventStream(oversized)) void event;
    }).rejects.toMatchObject({ code: "INVALID_EVENT" });
  });

  it("同一网络 chunk 可承载多个有界事件", async () => {
    const frames = Array.from({ length: 200 }, (_value, index) =>
      frame(envelope(String(index + 1))),
    ).join("");
    expect(frames.length).toBeGreaterThan(32 * 1024);
    const body = streamResponse([frames]).body;
    if (body === null) throw new Error("test response body is missing");

    const ids: string[] = [];
    for await (const event of parseServerEventStream(body)) ids.push(event.id);
    expect(ids).toHaveLength(200);
  });
});

describe("connectServerEvents", () => {
  it("拒绝缺少超窗权威回查处理器的连接", () => {
    expect(() => connectServerEvents({
      workspaceId,
      onRecoveryRequired: undefined as never,
    })).toThrow(
      expect.objectContaining({ code: "INVALID_RESPONSE", retryable: false }),
    );
  });

  it("断线后使用最近已验证事件作为 Last-Event-ID 重连", async () => {
    const requests: RequestInit[] = [];
    const fetcher = vi
      .fn<(input: RequestInfo | URL, init?: RequestInit) => Promise<Response>>()
      .mockImplementation(async (_input, init) => {
        requests.push(init ?? {});
        if (requests.length === 1) return streamResponse([frame(envelope("42"))]);
        return new Promise<Response>((_resolve, reject) => {
          init?.signal?.addEventListener("abort", () => reject(new DOMException("aborted", "AbortError")));
        });
      });
    const received: string[] = [];
    const connection = connectServerEvents({
      workspaceId,
      fetcher,
      onEvent: (event) => {
        received.push(event.id);
      },
      onRecoveryRequired: ignoreRecovery,
      sleep: resolvedSleep,
      initialBackoffMs: 1,
      maxBackoffMs: 1,
    });

    await vi.waitFor(() => expect(requests).toHaveLength(2));
    expect(received).toEqual(["42"]);
    expect(new Headers(requests[0]?.headers).get("Last-Event-ID")).toBeNull();
    expect(new Headers(requests[1]?.headers).get("Last-Event-ID")).toBe("42");

    connection.close();
    await connection.done;
  });

  it("409 expired 先要求回查，再无游标恢复连接", async () => {
    const headers: (string | null)[] = [];
    const recoveries: string[] = [];
    let finishRefetch: (() => void) | undefined;
    const refetchDone = new Promise<void>((resolve) => {
      finishRefetch = resolve;
    });
    const fetcher = vi
      .fn<(input: RequestInfo | URL, init?: RequestInit) => Promise<Response>>()
      .mockImplementation(async (_input, init) => {
        headers.push(new Headers(init?.headers).get("Last-Event-ID"));
        if (headers.length === 1) {
          return problemResponse(409, "SSE_CURSOR_EXPIRED", { action: "refetch" });
        }
        return new Promise<Response>((_resolve, reject) => {
          init?.signal?.addEventListener("abort", () => reject(new DOMException("aborted", "AbortError")));
        });
      });
    const connection = connectServerEvents({
      workspaceId,
      lastEventId: "41",
      fetcher,
      onRecoveryRequired: async (signal) => {
        recoveries.push(signal.reason);
        await refetchDone;
      },
      sleep: resolvedSleep,
    });

    await vi.waitFor(() => expect(recoveries).toEqual(["cursor_expired"]));
    expect(headers).toEqual(["41"]);
    finishRefetch?.();
    await vi.waitFor(() => expect(headers).toHaveLength(2));
    expect(headers).toEqual(["41", null]);
    expect(recoveries).toEqual(["cursor_expired"]);

    connection.close();
    await connection.done;
  });

  it.each(["SSE_CURSOR_INVALID", "SSE_CURSOR_FUTURE"])(
    "400 %s 报告不可恢复错误且不重连",
    async (errorCode) => {
      const fetcher = vi.fn(() => Promise.resolve(problemResponse(400, errorCode)));
      const errors: ServerEventClientError[] = [];
      const connection = connectServerEvents({
        workspaceId,
        lastEventId: "41",
        fetcher,
        onError: (error) => errors.push(error),
        onRecoveryRequired: ignoreRecovery,
        sleep: resolvedSleep,
      });

      await connection.done;
      expect(fetcher).toHaveBeenCalledTimes(1);
      expect(errors).toHaveLength(1);
      expect(errors[0]).toMatchObject({ code: "CURSOR_REJECTED", retryable: false });
    },
  );

  it("取消会中止 fetch 并释放 reader", async () => {
    let cancelled = false;
    const response = new Response(
      new ReadableStream<Uint8Array>({
        cancel() {
          cancelled = true;
        },
      }),
      { status: 200, headers: { "Content-Type": "text/event-stream" } },
    );
    const fetcher = vi.fn(() => Promise.resolve(response));
    const connection = connectServerEvents({
      workspaceId,
      fetcher,
      onRecoveryRequired: ignoreRecovery,
    });

    await vi.waitFor(() => expect(fetcher).toHaveBeenCalledOnce());
    connection.close();
    await connection.done;
    expect(cancelled).toBe(true);
  });

  it("onEvent 失败时不提交游标并在重连后重放同一事件", async () => {
    const headers: (string | null)[] = [];
    const deliveries: string[] = [];
    let deliveryAttempt = 0;
    const fetcher = vi
      .fn<(input: RequestInfo | URL, init?: RequestInit) => Promise<Response>>()
      .mockImplementation((_input, init) => {
        headers.push(new Headers(init?.headers).get("Last-Event-ID"));
        if (headers.length <= 2) return Promise.resolve(streamResponse([frame(envelope("42"))]));
        return new Promise<Response>((_resolve, reject) => {
          init?.signal?.addEventListener("abort", () => reject(new DOMException("aborted", "AbortError")));
        });
      });
    const connection = connectServerEvents({
      workspaceId,
      fetcher,
      onRecoveryRequired: ignoreRecovery,
      onEvent: (event) => {
        deliveries.push(event.id);
        deliveryAttempt += 1;
        return deliveryAttempt === 1
          ? Promise.reject(new Error("consumer unavailable"))
          : Promise.resolve();
      },
      sleep: resolvedSleep,
      initialBackoffMs: 1,
      maxBackoffMs: 1,
    });

    await vi.waitFor(() => expect(headers).toHaveLength(3));
    expect(headers).toEqual([null, null, "42"]);
    expect(deliveries).toEqual(["42", "42"]);
    expect(connection.getLastEventId()).toBe("42");

    connection.close();
    await connection.done;
  });
});
