import { AuthApiError, authFetch } from "../api/auth";

const uuidPattern = /^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/;
const cursorPattern = /^([1-9]\d*):([1-9]\d*)$/;
const maximumChunkBytes = 64 * 1024;
const maximumFrameCharacters = maximumChunkBytes * 6 + 1024;
const resetReasons = ["draft_unavailable", "draft_stale", "generation_replaced", "aborted", "superseded"] as const;
const endStatuses = ["PUBLISHED", "RESET"] as const;

export type AnswerDraftConnectionState = "connecting" | "open" | "reconnecting" | "closed";
export type AnswerDraftRecoveryReason = "cursor_rejected" | "gap" | "reset" | "end" | "invalid_stream";

export type AnswerDraftStreamEvent =
  | { type: "chunk"; id: string; generation: number; sequence: number; content: string }
  | { type: "reset"; generation: number; reason: (typeof resetReasons)[number]; action: "refetch" }
  | { type: "end"; generation: number; status: (typeof endStatuses)[number]; action: "refetch" };

export class AnswerDraftStreamError extends Error {
  readonly code: "INVALID_EVENT" | "INVALID_RESPONSE" | "CURSOR_REJECTED" | "SEQUENCE_GAP" | "HTTP_ERROR" | "NETWORK_ERROR" | "RECOVERY_FAILED";
  readonly retryable: boolean;

  constructor(code: AnswerDraftStreamError["code"], message: string, retryable: boolean, options?: ErrorOptions) {
    super(message, options);
    this.name = "AnswerDraftStreamError";
    this.code = code;
    this.retryable = retryable;
  }
}

interface DraftCursor {
  generation: number;
  sequence: number;
}

interface ParsedFrame {
  id?: string;
  event?: string;
  data: string[];
}

interface Problem {
  errorCode: string;
  retryable: boolean;
}

type DraftFetcher = (path: string, init?: RequestInit) => Promise<Response>;

export interface ConnectAnswerDraftStreamOptions {
  workspaceId: string;
  answerId: string;
  lastEventId?: string;
  fetcher?: DraftFetcher;
  onEvent: (event: AnswerDraftStreamEvent) => void | Promise<void>;
  onRecoveryRequired: (reason: AnswerDraftRecoveryReason) => void | Promise<void>;
  onStateChange?: (state: AnswerDraftConnectionState) => void;
  onError?: (error: AnswerDraftStreamError) => void;
  initialBackoffMs?: number;
  maxBackoffMs?: number;
  sleep?: (milliseconds: number, signal: AbortSignal) => Promise<void>;
  random?: () => number;
}

export interface AnswerDraftStreamConnection {
  close: () => void;
  done: Promise<void>;
  getLastEventId: () => string | undefined;
}

const isRecord = (value: unknown): value is Record<string, unknown> =>
  typeof value === "object" && value !== null && !Array.isArray(value);

const exact = (value: Record<string, unknown>, keys: readonly string[], field: string): void => {
  const actual = Object.keys(value);
  if (actual.length !== keys.length || actual.some((key) => !keys.includes(key))) {
    throw invalidEvent(`${field} 字段无效`);
  }
};

const invalidEvent = (message: string, cause?: unknown): AnswerDraftStreamError =>
  new AnswerDraftStreamError("INVALID_EVENT", message, false, { cause });

const safeInteger = (value: unknown, field: string, allowZero = false): number => {
  if (typeof value !== "number" || !Number.isSafeInteger(value) || value < (allowZero ? 0 : 1)) {
    throw invalidEvent(`${field} 不是有效整数`);
  }
  return value;
};

const enumValue = <T extends string>(value: unknown, values: readonly T[], field: string): T => {
  if (typeof value !== "string" || !values.includes(value as T)) throw invalidEvent(`${field} 无效`);
  return value as T;
};

const parseCursor = (value: string): DraftCursor => {
  const match = cursorPattern.exec(value);
  if (match === null) throw new AnswerDraftStreamError("CURSOR_REJECTED", "草稿流游标格式无效", false);
  const generation = Number(match[1]);
  const sequence = Number(match[2]);
  if (!Number.isSafeInteger(generation) || !Number.isSafeInteger(sequence)) {
    throw new AnswerDraftStreamError("CURSOR_REJECTED", "草稿流游标超出安全范围", false);
  }
  return { generation, sequence };
};

const decodeEvent = (frame: ParsedFrame): AnswerDraftStreamEvent | undefined => {
  if (frame.id === undefined && frame.event === undefined && frame.data.length === 0) return undefined;
  if (frame.event === undefined || frame.data.length !== 1) throw invalidEvent("草稿 SSE 事件帧不完整");
  let value: unknown;
  try {
    value = JSON.parse(frame.data[0] ?? "");
  } catch (error: unknown) {
    throw invalidEvent("草稿 SSE data 不是有效 JSON", error);
  }
  if (!isRecord(value)) throw invalidEvent("草稿 SSE data 结构无效");

  if (frame.event === "chunk") {
    if (frame.id === undefined) throw invalidEvent("草稿 chunk 缺少 id");
    exact(value, ["generation", "sequence", "content"], "草稿 chunk");
    const generation = safeInteger(value.generation, "chunk.generation");
    const sequence = safeInteger(value.sequence, "chunk.sequence");
    const cursor = parseCursor(frame.id);
    if (cursor.generation !== generation || cursor.sequence !== sequence) throw invalidEvent("草稿 chunk 与 id 不一致");
    if (typeof value.content !== "string" || value.content === "" || new TextEncoder().encode(value.content).length > maximumChunkBytes) {
      throw invalidEvent("草稿 chunk 内容无效");
    }
    return { type: "chunk", id: frame.id, generation, sequence, content: value.content };
  }

  if (frame.id !== undefined) throw invalidEvent("草稿终止事件不能携带 id");
  if (frame.event === "reset") {
    exact(value, ["generation", "reason", "action"], "草稿 reset");
    return {
      type: "reset",
      generation: safeInteger(value.generation, "reset.generation", true),
      reason: enumValue(value.reason, resetReasons, "reset.reason"),
      action: enumValue(value.action, ["refetch"] as const, "reset.action"),
    };
  }
  if (frame.event === "end") {
    exact(value, ["generation", "status", "action"], "草稿 end");
    return {
      type: "end",
      generation: safeInteger(value.generation, "end.generation", true),
      status: enumValue(value.status, endStatuses, "end.status"),
      action: enumValue(value.action, ["refetch"] as const, "end.action"),
    };
  }
  throw invalidEvent("草稿 SSE event 类型无效");
};

const parseFrame = (lines: string[]): AnswerDraftStreamEvent | undefined => {
  const frame: ParsedFrame = { data: [] };
  for (const line of lines) {
    if (line === "" || line.startsWith(":")) continue;
    const separator = line.indexOf(":");
    const field = separator < 0 ? line : line.slice(0, separator);
    let value = separator < 0 ? "" : line.slice(separator + 1);
    if (value.startsWith(" ")) value = value.slice(1);
    switch (field) {
      case "id":
        if (frame.id !== undefined || value.includes("\u0000")) throw invalidEvent("草稿 SSE id 行无效");
        frame.id = value;
        break;
      case "event":
        if (frame.event !== undefined) throw invalidEvent("草稿 SSE event 行重复");
        frame.event = value;
        break;
      case "data":
        frame.data.push(value);
        break;
      default:
        throw invalidEvent(`草稿 SSE 包含不支持的字段：${field}`);
    }
  }
  return decodeEvent(frame);
};

export async function* parseAnswerDraftStream(
  body: ReadableStream<Uint8Array>,
  signal?: AbortSignal,
): AsyncGenerator<AnswerDraftStreamEvent> {
  const reader = body.getReader();
  const decoder = new TextDecoder("utf-8", { fatal: true });
  let buffer = "";
  let cancelPromise: Promise<void> | undefined;
  const cancelReader = (reason?: unknown): Promise<void> => {
    cancelPromise ??= reader.cancel(reason).catch(() => undefined);
    return cancelPromise;
  };
  const abort = (): void => { void cancelReader(signal?.reason); };
  if (signal?.aborted === true) abort();
  else signal?.addEventListener("abort", abort, { once: true });
  try {
    for (;;) {
      const result = await reader.read();
      if (result.done) break;
      try {
        buffer += decoder.decode(result.value, { stream: true });
      } catch (error: unknown) {
        throw invalidEvent("草稿 SSE 不是有效 UTF-8", error);
      }
      const trailingCarriageReturn = buffer.endsWith("\r");
      const complete = trailingCarriageReturn ? buffer.slice(0, -1) : buffer;
      buffer = complete.replaceAll("\r\n", "\n").replaceAll("\r", "\n") + (trailingCarriageReturn ? "\r" : "");
      let boundary = buffer.indexOf("\n\n");
      while (boundary >= 0) {
        if (boundary > maximumFrameCharacters) throw invalidEvent("草稿 SSE 事件帧超过大小限制");
        const event = parseFrame(buffer.slice(0, boundary).split("\n"));
        buffer = buffer.slice(boundary + 2);
        if (event !== undefined) yield event;
        boundary = buffer.indexOf("\n\n");
      }
      if (buffer.length > maximumFrameCharacters) throw invalidEvent("草稿 SSE 事件帧超过大小限制");
    }
    try {
      buffer += decoder.decode();
    } catch (error: unknown) {
      throw invalidEvent("草稿 SSE 以不完整 UTF-8 结束", error);
    }
    buffer = buffer.replaceAll("\r\n", "\n").replaceAll("\r", "\n");
    if (buffer !== "") throw invalidEvent("草稿 SSE 以不完整事件帧结束");
  } finally {
    signal?.removeEventListener("abort", abort);
    await cancelReader(signal?.reason);
    reader.releaseLock();
  }
}

const decodeProblem = async (response: Response): Promise<Problem> => {
  let value: unknown;
  try {
    value = await response.json();
  } catch (error: unknown) {
    throw new AnswerDraftStreamError("INVALID_RESPONSE", "草稿流错误响应不是有效 JSON", false, { cause: error });
  }
  if (!isRecord(value)) throw new AnswerDraftStreamError("INVALID_RESPONSE", "草稿流 Problem 结构无效", false);
  const keys = ["error_code", "message", "retryable", "workflow_run_id", "details"] as const;
  if (Object.keys(value).some((key) => !keys.includes(key as (typeof keys)[number])) ||
      typeof value.error_code !== "string" || value.error_code === "" || value.error_code.length > 256 ||
      typeof value.message !== "string" || value.message === "" || value.message.length > 4096 ||
      typeof value.retryable !== "boolean" ||
      (value.workflow_run_id !== undefined && (typeof value.workflow_run_id !== "string" || !uuidPattern.test(value.workflow_run_id))) ||
      (value.details !== undefined && !isRecord(value.details))) {
    throw new AnswerDraftStreamError("INVALID_RESPONSE", "草稿流 Problem 结构无效", false);
  }
  return { errorCode: value.error_code, retryable: value.retryable };
};

const defaultSleep = async (milliseconds: number, signal: AbortSignal): Promise<void> =>
  new Promise<void>((resolve, reject) => {
    const abort = (): void => {
      globalThis.clearTimeout(timeout);
      reject(new DOMException("aborted", "AbortError"));
    };
    const timeout = globalThis.setTimeout(() => {
      signal.removeEventListener("abort", abort);
      resolve();
    }, milliseconds);
    if (signal.aborted) abort();
    else signal.addEventListener("abort", abort, { once: true });
  });

const isAbortError = (error: unknown): boolean =>
  (error instanceof DOMException || error instanceof Error) && error.name === "AbortError";

export const connectAnswerDraftStream = (
  options: ConnectAnswerDraftStreamOptions,
): AnswerDraftStreamConnection => {
  if (!uuidPattern.test(options.workspaceId) || !uuidPattern.test(options.answerId) || options.workspaceId === options.answerId) {
    throw new AnswerDraftStreamError("INVALID_RESPONSE", "草稿流资源绑定无效", false);
  }
  let cursor = options.lastEventId;
  let parsedCursor = cursor === undefined ? undefined : parseCursor(cursor);
  const fetcher = options.fetcher ?? authFetch;
  const sleep = options.sleep ?? defaultSleep;
  const random = options.random ?? Math.random;
  const initialBackoffMs = Math.max(1, options.initialBackoffMs ?? 500);
  const maxBackoffMs = Math.max(initialBackoffMs, options.maxBackoffMs ?? 15_000);
  const controller = new AbortController();
  const connectionAborted = (): boolean => controller.signal.aborted;
  const deliver = async (event: AnswerDraftStreamEvent): Promise<void> => {
    try {
      await options.onEvent(event);
    } catch (error: unknown) {
      throw new AnswerDraftStreamError("INVALID_EVENT", "草稿流事件无法进入本地状态", false, { cause: error });
    }
  };
  const recover = async (reason: AnswerDraftRecoveryReason): Promise<void> => {
    try {
      await options.onRecoveryRequired(reason);
    } catch (error: unknown) {
      throw new AnswerDraftStreamError("RECOVERY_FAILED", "草稿流恢复所需的正式 Answer 回查失败", false, { cause: error });
    }
  };

  const run = async (): Promise<void> => {
    let firstAttempt = true;
    let backoffMs = initialBackoffMs;
    options.onStateChange?.("connecting");
    while (!connectionAborted()) {
      if (!firstAttempt) options.onStateChange?.("reconnecting");
      firstAttempt = false;
      const attemptController = new AbortController();
      const abortAttempt = (): void => { attemptController.abort(controller.signal.reason); };
      if (connectionAborted()) abortAttempt();
      else controller.signal.addEventListener("abort", abortAttempt, { once: true });
      let restartAfterReset = false;
      try {
        const headers = new Headers({ Accept: "text/event-stream" });
        if (cursor !== undefined) headers.set("Last-Event-ID", cursor);
        const response = await fetcher(
          `/api/v1/answers/${encodeURIComponent(options.answerId)}/stream?workspace_id=${encodeURIComponent(options.workspaceId)}`,
          { method: "GET", headers, signal: attemptController.signal },
        );
        if (!response.ok) {
          const problem = await decodeProblem(response);
          if (response.status === 400 && cursor !== undefined &&
              (problem.errorCode === "ANSWER_DRAFT_STREAM_CURSOR_INVALID" || problem.errorCode === "ANSWER_DRAFT_STREAM_CURSOR_FUTURE")) {
            await recover("cursor_rejected");
            cursor = undefined;
            parsedCursor = undefined;
            continue;
          }
          throw new AnswerDraftStreamError(
            "HTTP_ERROR",
            `草稿流请求失败（HTTP ${String(response.status)}，${problem.errorCode}）`,
            problem.retryable || response.status >= 500,
          );
        }
        const contentType = response.headers.get("Content-Type");
        const mediaType = contentType?.split(";", 1)[0]?.trim().toLowerCase();
        if (mediaType !== "text/event-stream") {
          throw new AnswerDraftStreamError("INVALID_RESPONSE", "草稿流响应 Content-Type 无效", false);
        }
        if (response.body === null) throw new AnswerDraftStreamError("INVALID_RESPONSE", "草稿流响应缺少 body", false);
        options.onStateChange?.("open");
        let terminal = false;
        let resetSeen = false;
        for await (const event of parseAnswerDraftStream(response.body, attemptController.signal)) {
          if (event.type === "chunk") {
            const contiguous = parsedCursor === undefined
              ? event.sequence === 1
              : event.generation === parsedCursor.generation && event.sequence === parsedCursor.sequence + 1;
            if (!contiguous) throw new AnswerDraftStreamError("SEQUENCE_GAP", "草稿流序号不连续", false);
            await deliver(event);
            cursor = event.id;
            parsedCursor = { generation: event.generation, sequence: event.sequence };
            backoffMs = initialBackoffMs;
            continue;
          }
          await deliver(event);
          cursor = undefined;
          parsedCursor = undefined;
          if (event.type === "reset") {
            resetSeen = true;
            await recover("reset");
            continue;
          }
          if (!resetSeen) await recover("end");
          terminal = event.status === "PUBLISHED";
          restartAfterReset = event.status === "RESET";
          break;
        }
        if (terminal) return;
        if (!restartAfterReset) {
          throw new AnswerDraftStreamError("NETWORK_ERROR", "草稿流连接在终止事件前中断", true);
        }
      } catch (error: unknown) {
        if (connectionAborted() || isAbortError(error)) break;
        const classified = error instanceof AnswerDraftStreamError
          ? error
          : error instanceof AuthApiError
            ? new AnswerDraftStreamError("HTTP_ERROR", "草稿流认证状态不可用", error.retryable, { cause: error })
          : new AnswerDraftStreamError("NETWORK_ERROR", "草稿流连接中断", true, { cause: error });
        options.onError?.(classified);
        if (!classified.retryable) {
          cursor = undefined;
          parsedCursor = undefined;
          if (classified.code !== "RECOVERY_FAILED") {
            try {
              await recover(classified.code === "SEQUENCE_GAP" ? "gap" : "invalid_stream");
            } catch (recoveryError: unknown) {
              options.onError?.(recoveryError instanceof AnswerDraftStreamError
                ? recoveryError
                : new AnswerDraftStreamError("RECOVERY_FAILED", "草稿流恢复失败", false, { cause: recoveryError }));
            }
          }
          break;
        }
        options.onStateChange?.("reconnecting");
      } finally {
        controller.signal.removeEventListener("abort", abortAttempt);
        if (!attemptController.signal.aborted) attemptController.abort();
      }
      if (connectionAborted()) continue;
      const jittered = Math.round(backoffMs * (0.8 + random() * 0.4));
      try {
        await sleep(jittered, controller.signal);
      } catch (error: unknown) {
        if (!connectionAborted() && !isAbortError(error)) {
          options.onError?.(new AnswerDraftStreamError("NETWORK_ERROR", "草稿流重连等待失败", true, { cause: error }));
        }
      }
      backoffMs = Math.min(maxBackoffMs, backoffMs * 2);
    }
  };

  const done = run().finally(() => options.onStateChange?.("closed"));
  return { close: () => controller.abort(), done, getLastEventId: () => cursor };
};
