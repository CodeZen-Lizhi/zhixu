import { beforeEach, describe, expect, it, vi } from "vitest";

import { setCsrfToken, subscribeAuthInvalidation } from "../api/auth";
import {
  ServerEventClientError,
  connectServerEvents,
  decodeServerEventEnvelope,
  type EventSourceFactory,
  type EventSourceTransport,
} from "./server-events";

const workspaceId = "7a000000-0000-4000-8000-000000000001";
const otherWorkspaceId = "7a000000-0000-4000-8000-000000000004";
const answerId = "7a000000-0000-4000-8000-000000000002";
const workflowRunId = "7a000000-0000-4000-8000-000000000003";

const envelope = (id = "42", scopeWorkspaceId = workspaceId): Record<string, unknown> => ({
  schema_version: 1,
  id,
  type: "answer.completed",
  occurred_at: "2026-07-20T08:09:10.123Z",
  workspace_id: scopeWorkspaceId,
  resource_ref: `answer:${answerId}`,
  resource_version: 2,
  payload_summary: {
    answer_id: answerId,
    workflow_run_id: workflowRunId,
    publication_status: "completed",
    citation_count: 3,
  },
});

const problemResponse = (
  status: number,
  errorCode: string,
  details?: Record<string, unknown>,
  retryable = false,
): Response =>
  new Response(
    JSON.stringify({
      error_code: errorCode,
      message: "事件流请求未完成",
      retryable,
      ...(details === undefined ? {} : { details }),
    }),
    { status, headers: { "Content-Type": "application/json" } },
  );

const ignoreRecovery = (): void => undefined;
const resolvedSleep = (): Promise<void> => Promise.resolve();

class FakeEventSource implements EventSourceTransport {
  readyState = 0;
  onopen: ((event: Event) => void) | null = null;
  onmessage: ((event: MessageEvent<string>) => void) | null = null;
  onerror: ((event: Event) => void) | null = null;
  readonly close = vi.fn(() => {
    this.readyState = 2;
  });

  constructor(
    readonly url: string,
    readonly init: EventSourceInit,
  ) {}

  open(): void {
    this.readyState = 1;
    this.onopen?.(new Event("open"));
  }

  reconnectingError(): void {
    this.readyState = 0;
    this.onerror?.(new Event("error"));
  }

  fatalError(): void {
    this.readyState = 2;
    this.onerror?.(new Event("error"));
  }

  message(value: Record<string, unknown>, lastEventId = String(value.id)): void {
    this.rawMessage(JSON.stringify(value), lastEventId);
  }

  rawMessage(data: string, lastEventId: string): void {
    this.onmessage?.(new MessageEvent<string>("message", { data, lastEventId }));
  }
}

const eventSources: FakeEventSource[] = [];
const eventSourceFactory: EventSourceFactory = vi.fn((url: string, init: EventSourceInit) => {
  const source = new FakeEventSource(url, init);
  eventSources.push(source);
  return source;
});

beforeEach(() => {
  eventSources.length = 0;
  vi.clearAllMocks();
  window.localStorage.clear();
});

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

  it("将 Collection、Export 与 Health resource_ref 解码为失效提示", () => {
    expect(decodeServerEventEnvelope({
      ...envelope("43"),
      type: "health.scan.completed",
      resource_ref: `health_scan:${workflowRunId}`,
      payload_summary: {},
    }).invalidations).toEqual([{ resource: "health_scan", id: workflowRunId }]);
    expect(decodeServerEventEnvelope({
      ...envelope("44"),
      type: "collection.updated",
      resource_ref: `collection:${answerId}`,
      payload_summary: {},
    }).invalidations).toEqual([{ resource: "collection", id: answerId }]);
    const attachmentExport = decodeServerEventEnvelope({
      ...envelope("45"),
      type: "export.completed",
      resource_ref: `export_job:${answerId}`,
      payload_summary: { status: "succeeded", scope_kind: "workspace_attachments" },
    });
    expect(attachmentExport.payloadSummary.scopeKind).toBe("workspace_attachments");
    expect(attachmentExport.invalidations).toEqual([{ resource: "export_job", id: answerId }]);
  });

  it("将 Capture 与 Knowledge Profile resource_ref 解码为定向失效提示", () => {
    expect(decodeServerEventEnvelope({
      ...envelope("46"),
      type: "capture.updated",
      resource_ref: `capture:${answerId}`,
      payload_summary: {},
    }).invalidations).toEqual([{ resource: "capture", id: answerId }]);
    expect(decodeServerEventEnvelope({
      ...envelope("47"),
      type: "profile.ready",
      resource_ref: `knowledge_profile:${workflowRunId}`,
      payload_summary: {},
    }).invalidations).toEqual([{ resource: "knowledge_profile", id: workflowRunId }]);
  });

  it("将 Authoring resource_ref 解码为定向失效提示", () => {
    expect(decodeServerEventEnvelope({
      ...envelope("48"),
      type: "authoring.draft.updated",
      resource_ref: `working_draft:${workflowRunId}`,
      payload_summary: {},
    }).invalidations).toEqual([{ resource: "working_draft", id: workflowRunId }]);
  });

  it("严格解码 Git Remote 与 Git Sync Run 资源", () => {
    expect(decodeServerEventEnvelope({
      ...envelope("49"), type: "git.sync.run.updated", resource_ref: `git_sync_run:${workflowRunId}`, payload_summary: {},
    }).invalidations).toEqual([{ resource: "git_sync_run", id: workflowRunId }]);
    expect(decodeServerEventEnvelope({
      ...envelope("50"), type: "git.remote.updated", resource_ref: `git_remote:${answerId}`, payload_summary: {},
    }).invalidations).toEqual([{ resource: "git_remote", id: answerId }]);
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
    ["非法导出范围", { ...envelope(), payload_summary: { scope_kind: "WORKSPACE_ATTACHMENTS" } }],
    ["摘要额外正文", { ...envelope(), payload_summary: { answer_text: "secret" } }],
  ])("拒绝%s", (_name, value) => {
    expect(() => decodeServerEventEnvelope(value)).toThrow(ServerEventClientError);
  });
});

describe("connectServerEvents", () => {
  it("创建 message-mode credentialed EventSource，并把普通错误交给原生重连", () => {
    const states: string[] = [];
    const fetcher = vi.fn();
    const connection = connectServerEvents({
      workspaceId,
      lastEventId: "41",
      eventSourceFactory,
      fetcher,
      onRecoveryRequired: ignoreRecovery,
      onStateChange: (state) => states.push(state),
    });

    expect(eventSources).toHaveLength(1);
    expect(eventSources[0]?.url).toBe(
      `/api/v1/events?workspace_id=${workspaceId}&event_format=message&last_event_id=41`,
    );
    expect(eventSources[0]?.init).toEqual({ withCredentials: true });
    eventSources[0]?.open();
    eventSources[0]?.reconnectingError();

    expect(states).toEqual(["connecting", "open", "reconnecting"]);
    expect(fetcher).not.toHaveBeenCalled();
    expect(eventSources).toHaveLength(1);
    connection.close();
  });

  it("拒绝缺少权威回查处理器或非法本地游标的连接", () => {
    expect(() => connectServerEvents({
      workspaceId,
      eventSourceFactory,
      onRecoveryRequired: undefined as never,
    })).toThrow(expect.objectContaining({ code: "INVALID_RESPONSE", retryable: false }));
    expect(() => connectServerEvents({
      workspaceId,
      lastEventId: "01",
      eventSourceFactory,
      onRecoveryRequired: ignoreRecovery,
    })).toThrow(expect.objectContaining({ code: "CURSOR_REJECTED", retryable: false }));
    expect(eventSources).toHaveLength(0);
  });

  it("严格校验并串行提交消息，对 committed/queued 精确重复保持幂等", async () => {
    let finishFirst: (() => void) | undefined;
    const firstPending = new Promise<void>((resolve) => {
      finishFirst = resolve;
    });
    const received: string[] = [];
    const connection = connectServerEvents({
      workspaceId,
      lastEventId: "41",
      eventSourceFactory,
      onRecoveryRequired: ignoreRecovery,
      onEvent: async (event) => {
        received.push(event.id);
        if (event.id === "42") await firstPending;
      },
    });
    const source = eventSources[0];
    source?.message(envelope("41"));
    source?.message(envelope("42"));
    source?.message(envelope("42"));
    source?.message(envelope("43"));

    await vi.waitFor(() => expect(received).toEqual(["42"]));
    finishFirst?.();
    await vi.waitFor(() => expect(received).toEqual(["42", "43"]));
    connection.close();
  });

  it.each([
    ["非法 JSON", (source: FakeEventSource) => source.rawMessage("{", "42")],
    ["metadata 不一致", (source: FakeEventSource) => source.message(envelope("42"), "43")],
    ["Workspace 不一致", (source: FakeEventSource) => source.message(envelope("42", otherWorkspaceId))],
    ["data 超限", (source: FakeEventSource) => source.rawMessage("x".repeat(32 * 1024 + 1), "42")],
  ])("%s 时 fail closed", async (_name, deliver) => {
    const errors: ServerEventClientError[] = [];
    const connection = connectServerEvents({
      workspaceId,
      eventSourceFactory,
      onRecoveryRequired: ignoreRecovery,
      onError: (error) => errors.push(error),
    });
    const source = eventSources[0];
    if (source === undefined) throw new Error("missing fake EventSource");
    deliver(source);
    await connection.done;

    expect(errors).toHaveLength(1);
    expect(errors[0]).toMatchObject({ code: "INVALID_EVENT", retryable: false });
    expect(source.close).toHaveBeenCalledOnce();
  });

  it("倒退 ID fail closed", async () => {
    const errors: ServerEventClientError[] = [];
    const connection = connectServerEvents({
      workspaceId,
      eventSourceFactory,
      onRecoveryRequired: ignoreRecovery,
      onError: (error) => errors.push(error),
    });
    eventSources[0]?.message(envelope("43"));
    await vi.waitFor(() => expect(errors).toHaveLength(0));
    eventSources[0]?.message(envelope("42"));
    await connection.done;
    expect(errors[0]).toMatchObject({ code: "INVALID_EVENT", retryable: false });
  });

  it("onEvent 失败不提交游标，并从旧 committed cursor 重放且隔离旧 generation", async () => {
    const deliveries: string[] = [];
    let attempt = 0;
    const connection = connectServerEvents({
      workspaceId,
      lastEventId: "41",
      eventSourceFactory,
      fetcher: () => Promise.reject(new TypeError("network unavailable")),
      onRecoveryRequired: ignoreRecovery,
      sleep: resolvedSleep,
      initialBackoffMs: 1,
      maxBackoffMs: 1,
      onEvent: (event) => {
        deliveries.push(event.id);
        attempt += 1;
        return attempt === 1 ? Promise.reject(new Error("consumer unavailable")) : Promise.resolve();
      },
    });
    const first = eventSources[0];
    first?.message(envelope("42"));

    await vi.waitFor(() => expect(eventSources).toHaveLength(2));
    expect(first?.close).toHaveBeenCalledOnce();
    expect(eventSources[1]?.url).toContain("last_event_id=41");
    first?.message(envelope("43"));
    eventSources[1]?.message(envelope("42"));
    await vi.waitFor(() => expect(deliveries).toEqual(["42", "42"]));

    eventSources[1]?.fatalError();
    await vi.waitFor(() => expect(eventSources).toHaveLength(3));
    expect(eventSources[2]?.url).toContain("last_event_id=42");
    connection.close();
  });

  it("队列溢出时等待 active handler 收敛后再从 committed cursor 重建", async () => {
    let finishHandler: (() => void) | undefined;
    const handlerPending = new Promise<void>((resolve) => {
      finishHandler = resolve;
    });
    const connection = connectServerEvents({
      workspaceId,
      eventSourceFactory,
      onRecoveryRequired: ignoreRecovery,
      sleep: resolvedSleep,
      onEvent: () => handlerPending,
    });
    const first = eventSources[0];
    for (let id = 1; id <= 101; id += 1) first?.message(envelope(String(id)));

    expect(first?.close).toHaveBeenCalledOnce();
    expect(eventSources).toHaveLength(1);
    finishHandler?.();
    await vi.waitFor(() => expect(eventSources).toHaveLength(2));
    expect(eventSources[1]?.url).not.toContain("last_event_id");
    connection.close();
  });

  it("fatal CLOSED 用最小 Fetch probe 诊断，取消 200 body 后重建", async () => {
    let cancelCalls = 0;
    const probeResponse = new Response(new ReadableStream<Uint8Array>({
      cancel() {
        cancelCalls += 1;
      },
    }), { status: 200, headers: { "Content-Type": "text/event-stream; charset=utf-8" } });
    const fetcher = vi.fn<(input: RequestInfo | URL, init?: RequestInit) => Promise<Response>>(
      () => Promise.resolve(probeResponse),
    );
    const connection = connectServerEvents({
      workspaceId,
      lastEventId: "41",
      eventSourceFactory,
      fetcher,
      onRecoveryRequired: ignoreRecovery,
    });
    eventSources[0]?.fatalError();

    await vi.waitFor(() => expect(eventSources).toHaveLength(2));
    const [input, init] = fetcher.mock.calls[0] ?? [];
    expect(input).toBe(`/api/v1/events?workspace_id=${workspaceId}&event_format=message&last_event_id=41`);
    expect(init).toMatchObject({ method: "GET", credentials: "include", cache: "no-store" });
    expect(new Headers(init?.headers).get("Last-Event-ID")).toBe("41");
    expect(new Headers(init?.headers).get("X-CSRF-Token")).toBeNull();
    expect(cancelCalls).toBe(1);
    connection.close();
  });

  it("401 probe 通过共享入口失效认证并停止", async () => {
    setCsrfToken("c".repeat(43));
    const invalidated = vi.fn();
    const unsubscribe = subscribeAuthInvalidation(invalidated);
    const response = problemResponse(401, "AUTH_UNAUTHORIZED", undefined, true);
    const decodeProblem = vi.spyOn(response, "json");
    const fetcher = vi.fn(() => Promise.resolve(response));
    const connection = connectServerEvents({
      workspaceId,
      eventSourceFactory,
      fetcher,
      onRecoveryRequired: ignoreRecovery,
    });
    eventSources[0]?.fatalError();
    await connection.done;

    expect(window.localStorage.getItem("zhixu.csrf-token")).toBeNull();
    expect(invalidated).toHaveBeenCalledOnce();
    expect(decodeProblem).not.toHaveBeenCalled();
    expect(eventSources).toHaveLength(1);
    unsubscribe();
  });

  it.each([
    [409, "SSE_CURSOR_EXPIRED", { action: "refetch" }, "cursor_expired"],
    [400, "SSE_CURSOR_INVALID", undefined, "cursor_invalid"],
    [400, "SSE_CURSOR_FUTURE", undefined, "cursor_future"],
  ] as const)("HTTP %i %s 权威回查成功后清 cursor 重建", async (status, errorCode, details, expectedReason) => {
    const recoveries: string[] = [];
    const fetcher = vi.fn(() => Promise.resolve(problemResponse(status, errorCode, details)));
    const connection = connectServerEvents({
      workspaceId,
      lastEventId: "41",
      eventSourceFactory,
      fetcher,
      onRecoveryRequired: (signal) => {
        recoveries.push(signal.reason);
      },
    });
    eventSources[0]?.fatalError();

    await vi.waitFor(() => expect(eventSources).toHaveLength(2));
    expect(recoveries).toEqual([expectedReason]);
    expect(eventSources[1]?.url).not.toContain("last_event_id");
    connection.close();
  });

  it("权威回查失败保留游标并进入 recovery_failed", async () => {
    const states: string[] = [];
    const errors: ServerEventClientError[] = [];
    const connection = connectServerEvents({
      workspaceId,
      lastEventId: "41",
      eventSourceFactory,
      fetcher: () => Promise.resolve(problemResponse(409, "SSE_CURSOR_EXPIRED", { action: "refetch" })),
      onRecoveryRequired: () => Promise.reject(new Error("database unavailable")),
      onStateChange: (state) => states.push(state),
      onError: (error) => errors.push(error),
    });
    eventSources[0]?.fatalError();
    await connection.done;

    expect(errors[0]).toMatchObject({ code: "RECOVERY_FAILED", retryable: true });
    expect(states.at(-1)).toBe("recovery_failed");
    expect(eventSources).toHaveLength(1);
  });

  it.each([
    ["503", () => Promise.resolve(problemResponse(503, "SSE_STORE_UNAVAILABLE"))],
    ["network", () => Promise.reject(new TypeError("network unavailable"))],
    ["abort without connector cancellation", () => Promise.reject(new DOMException("upstream aborted", "AbortError"))],
  ])("%s fatal probe 使用有界 fallback 重建", async (_name, response) => {
    const connection = connectServerEvents({
      workspaceId,
      eventSourceFactory,
      fetcher: vi.fn(response),
      onRecoveryRequired: ignoreRecovery,
      sleep: resolvedSleep,
      initialBackoffMs: 1,
      maxBackoffMs: 1,
      random: () => 0.5,
    });
    eventSources[0]?.fatalError();
    await vi.waitFor(() => expect(eventSources).toHaveLength(2));
    connection.close();
  });

  it("无效 probe Content-Type fail closed", async () => {
    const errors: ServerEventClientError[] = [];
    let cancelCalls = 0;
    const response = new Response(new ReadableStream<Uint8Array>({
      cancel() {
        cancelCalls += 1;
      },
    }), { status: 200, headers: { "Content-Type": "text/event-stream-fake" } });
    const connection = connectServerEvents({
      workspaceId,
      eventSourceFactory,
      fetcher: () => Promise.resolve(response),
      onRecoveryRequired: ignoreRecovery,
      onError: (error) => errors.push(error),
    });
    eventSources[0]?.fatalError();
    await connection.done;
    expect(errors[0]).toMatchObject({ code: "INVALID_RESPONSE", retryable: false });
    expect(cancelCalls).toBe(1);
    expect(eventSources).toHaveLength(1);
  });

  it("close 幂等关闭 EventSource 并 Abort 正在进行的 probe", async () => {
    let probeSignal: AbortSignal | undefined;
    const fetcher = vi.fn((_input: RequestInfo | URL, init?: RequestInit) => {
      probeSignal = init?.signal ?? undefined;
      return new Promise<Response>((_resolve, reject) => {
        init?.signal?.addEventListener("abort", () => reject(new DOMException("aborted", "AbortError")));
      });
    });
    const connection = connectServerEvents({
      workspaceId,
      eventSourceFactory,
      fetcher,
      onRecoveryRequired: ignoreRecovery,
    });
    const first = eventSources[0];
    first?.fatalError();
    await vi.waitFor(() => expect(fetcher).toHaveBeenCalledOnce());

    connection.close();
    connection.close();
    await connection.done;
    expect(first?.close).toHaveBeenCalledOnce();
    expect(probeSignal?.aborted).toBe(true);
    expect(eventSources).toHaveLength(1);
  });

  it("close 后仍释放忽略 Abort 并迟到返回的 probe body", async () => {
    let resolveProbe: ((response: Response) => void) | undefined;
    let cancelCalls = 0;
    const response = new Response(new ReadableStream<Uint8Array>({
      cancel() {
        cancelCalls += 1;
      },
    }), { status: 200, headers: { "Content-Type": "text/event-stream" } });
    const fetcher = vi.fn(() => new Promise<Response>((resolve) => {
      resolveProbe = resolve;
    }));
    const connection = connectServerEvents({
      workspaceId,
      eventSourceFactory,
      fetcher,
      onRecoveryRequired: ignoreRecovery,
    });
    eventSources[0]?.fatalError();
    await vi.waitFor(() => expect(fetcher).toHaveBeenCalledOnce());

    connection.close();
    resolveProbe?.(response);
    await vi.waitFor(() => expect(cancelCalls).toBe(1));
    expect(eventSources).toHaveLength(1);
  });
});
