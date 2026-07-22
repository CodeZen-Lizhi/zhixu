export type ServerEventResource =
  | "conversation"
  | "question"
  | "answer"
  | "workflow"
  | "model_run"
  | "collection"
  | "health_issue"
  | "health_scan"
  | "knowledge_health";

export interface ServerEventInvalidation {
  resource: ServerEventResource;
  id: string;
}

export interface ServerEventPayloadSummary {
  sourceEventId?: string;
  conversationId?: string;
  workflowRunId?: string;
  questionId?: string;
  answerId?: string;
  modelRunId?: string;
  status?: string;
  publicationStatus?: string;
  resultType?: string;
  stage?: string;
  candidateCount?: number;
  selectedCount?: number;
  conflictCount?: number;
  degradationCount?: number;
  rewriteCount?: number;
  citationCount?: number;
}

export interface ServerEventEnvelope {
  schemaVersion: 1;
  id: string;
  type: string;
  occurredAt: string;
  workspaceId: string;
  resourceRef: string;
  resourceVersion: number;
  payloadSummary: ServerEventPayloadSummary;
  invalidations: ServerEventInvalidation[];
}

export interface ServerEventRecoverySignal {
  reason: "cursor_expired";
  workspaceId: string;
}

export type ServerEventConnectionState =
  | "connecting"
  | "open"
  | "reconnecting"
  | "closed";

export class ServerEventClientError extends Error {
  readonly code:
    | "INVALID_EVENT"
    | "INVALID_RESPONSE"
    | "HTTP_ERROR"
    | "CURSOR_REJECTED"
    | "NETWORK_ERROR";
  readonly retryable: boolean;

  constructor(
    code: ServerEventClientError["code"],
    message: string,
    retryable: boolean,
    options?: ErrorOptions,
  ) {
    super(message, options);
    this.name = "ServerEventClientError";
    this.code = code;
    this.retryable = retryable;
  }
}

const envelopeKeys = [
  "schema_version",
  "id",
  "type",
  "occurred_at",
  "workspace_id",
  "resource_ref",
  "resource_version",
  "payload_summary",
] as const;

const summaryKeys = [
  "source_event_id",
  "conversation_id",
  "workflow_run_id",
  "question_id",
  "answer_id",
  "model_run_id",
  "status",
  "publication_status",
  "result_type",
  "stage",
  "candidate_count",
  "selected_count",
  "conflict_count",
  "degradation_count",
  "rewrite_count",
  "citation_count",
] as const;

const uuidPattern = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;
const sequencePattern = /^[1-9][0-9]*$/;
const eventTypePattern = /^[a-z][a-z0-9_.-]{0,127}$/;
const tokenPattern = /^[a-z][a-z0-9_.-]{0,63}$/;
const resourceReferencePattern = /^[a-z][a-z0-9_.-]{0,63}:[a-z0-9][a-z0-9_.:-]*$/;
const rfc3339Pattern = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})$/;
const maximumSummaryCount = 1_000_000_000;
const maximumSequence = 9_223_372_036_854_775_807n;
const maximumFrameCharacters = 32 * 1024;

const isRecord = (value: unknown): value is Record<string, unknown> =>
  typeof value === "object" && value !== null && !Array.isArray(value);

const assertExactKeys = (
  value: Record<string, unknown>,
  allowed: readonly string[],
  field: string,
): void => {
  const keys = Object.keys(value);
  if (keys.some((key) => !allowed.includes(key))) {
    throw invalidEvent(`${field} 包含未知字段`);
  }
};

const readUuid = (value: unknown, field: string): string => {
  if (typeof value !== "string" || !uuidPattern.test(value)) {
    throw invalidEvent(`${field} 不是规范 UUID`);
  }
  return value;
};

const readOptionalUuid = (
  value: unknown,
  field: string,
): string | undefined => value === undefined ? undefined : readUuid(value, field);

const readToken = (value: unknown, field: string): string => {
  if (typeof value !== "string" || !tokenPattern.test(value)) {
    throw invalidEvent(`${field} 不是有效 token`);
  }
  return value;
};

const readOptionalToken = (
  value: unknown,
  field: string,
): string | undefined => value === undefined ? undefined : readToken(value, field);

const readPositiveSafeInteger = (value: unknown, field: string): number => {
  if (!Number.isSafeInteger(value) || typeof value !== "number" || value <= 0) {
    throw invalidEvent(`${field} 不是正安全整数`);
  }
  return value;
};

const readOptionalCount = (
  value: unknown,
  field: string,
): number | undefined => {
  if (value === undefined) return undefined;
  if (
    typeof value !== "number" ||
    !Number.isSafeInteger(value) ||
    value < 0 ||
    value > maximumSummaryCount
  ) {
    throw invalidEvent(`${field} 不是有效计数`);
  }
  return value;
};

const invalidEvent = (message: string, cause?: unknown): ServerEventClientError =>
  new ServerEventClientError("INVALID_EVENT", message, false, { cause });

const isValidSequence = (value: string): boolean =>
  sequencePattern.test(value) && BigInt(value) <= maximumSequence;

const isValidRfc3339 = (value: string): boolean => {
  if (!rfc3339Pattern.test(value) || !Number.isFinite(Date.parse(value))) return false;
  const match = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})/.exec(value);
  if (match === null) return false;
  const year = Number(match[1]);
  const month = Number(match[2]);
  const day = Number(match[3]);
  const hour = Number(match[4]);
  const minute = Number(match[5]);
  const second = Number(match[6]);
  if (month < 1 || month > 12 || hour > 23 || minute > 59 || second > 59) return false;
  return day >= 1 && day <= new Date(Date.UTC(year, month, 0)).getUTCDate();
};

const decodePayloadSummary = (value: unknown): ServerEventPayloadSummary => {
  if (!isRecord(value)) throw invalidEvent("payload_summary 结构无效");
  assertExactKeys(value, summaryKeys, "payload_summary");

  const sourceEventId = readOptionalUuid(value.source_event_id, "payload_summary.source_event_id");
  const conversationId = readOptionalUuid(value.conversation_id, "payload_summary.conversation_id");
  const workflowRunId = readOptionalUuid(value.workflow_run_id, "payload_summary.workflow_run_id");
  const questionId = readOptionalUuid(value.question_id, "payload_summary.question_id");
  const answerId = readOptionalUuid(value.answer_id, "payload_summary.answer_id");
  const modelRunId = readOptionalUuid(value.model_run_id, "payload_summary.model_run_id");
  const status = readOptionalToken(value.status, "payload_summary.status");
  const publicationStatus = readOptionalToken(value.publication_status, "payload_summary.publication_status");
  const resultType = readOptionalToken(value.result_type, "payload_summary.result_type");
  const stage = readOptionalToken(value.stage, "payload_summary.stage");
  const candidateCount = readOptionalCount(value.candidate_count, "payload_summary.candidate_count");
  const selectedCount = readOptionalCount(value.selected_count, "payload_summary.selected_count");
  const conflictCount = readOptionalCount(value.conflict_count, "payload_summary.conflict_count");
  const degradationCount = readOptionalCount(value.degradation_count, "payload_summary.degradation_count");
  const rewriteCount = readOptionalCount(value.rewrite_count, "payload_summary.rewrite_count");
  const citationCount = readOptionalCount(value.citation_count, "payload_summary.citation_count");

  return {
    ...(sourceEventId === undefined ? {} : { sourceEventId }),
    ...(conversationId === undefined ? {} : { conversationId }),
    ...(workflowRunId === undefined ? {} : { workflowRunId }),
    ...(questionId === undefined ? {} : { questionId }),
    ...(answerId === undefined ? {} : { answerId }),
    ...(modelRunId === undefined ? {} : { modelRunId }),
    ...(status === undefined ? {} : { status }),
    ...(publicationStatus === undefined ? {} : { publicationStatus }),
    ...(resultType === undefined ? {} : { resultType }),
    ...(stage === undefined ? {} : { stage }),
    ...(candidateCount === undefined ? {} : { candidateCount }),
    ...(selectedCount === undefined ? {} : { selectedCount }),
    ...(conflictCount === undefined ? {} : { conflictCount }),
    ...(degradationCount === undefined ? {} : { degradationCount }),
    ...(rewriteCount === undefined ? {} : { rewriteCount }),
    ...(citationCount === undefined ? {} : { citationCount }),
  };
};

const invalidationsFor = (
  resourceRef: string,
  summary: ServerEventPayloadSummary,
): ServerEventInvalidation[] => {
  const invalidations: ServerEventInvalidation[] = [];
  const seen = new Set<string>();
  const append = (resource: ServerEventResource, id: string | undefined): void => {
    if (id === undefined) return;
    const key = `${resource}:${id}`;
    if (!seen.has(key)) {
      seen.add(key);
      invalidations.push({ resource, id });
    }
  };

  append("conversation", summary.conversationId);
  append("question", summary.questionId);
  append("answer", summary.answerId);
  append("workflow", summary.workflowRunId);
  append("model_run", summary.modelRunId);

  const separator = resourceRef.indexOf(":");
  if (separator > 0) {
    const resource = resourceRef.slice(0, separator);
    const id = resourceRef.slice(separator + 1);
    if (
      uuidPattern.test(id) &&
      (resource === "conversation" ||
        resource === "question" ||
        resource === "answer" ||
        resource === "workflow" ||
        resource === "model_run" ||
        resource === "collection" ||
        resource === "health_issue" ||
        resource === "health_scan" ||
        resource === "knowledge_health")
    ) {
      append(resource, id);
    }
  }
  return invalidations;
};

export const decodeServerEventEnvelope = (value: unknown): ServerEventEnvelope => {
  if (!isRecord(value)) throw invalidEvent("SSE Envelope 结构无效");
  assertExactKeys(value, envelopeKeys, "SSE Envelope");
  if (value.schema_version !== 1) throw invalidEvent("不支持的 SSE schema_version");
  if (
    typeof value.id !== "string" ||
    !isValidSequence(value.id)
  ) {
    throw invalidEvent("SSE id 不是正十进制序号");
  }
  if (typeof value.type !== "string" || !eventTypePattern.test(value.type)) {
    throw invalidEvent("SSE type 无效");
  }
  if (
    typeof value.occurred_at !== "string" ||
    !isValidRfc3339(value.occurred_at)
  ) {
    throw invalidEvent("SSE occurred_at 无效");
  }
  const workspaceId = readUuid(value.workspace_id, "workspace_id");
  if (
    typeof value.resource_ref !== "string" ||
    value.resource_ref.length > 256 ||
    !resourceReferencePattern.test(value.resource_ref)
  ) {
    throw invalidEvent("SSE resource_ref 无效");
  }
  const resourceVersion = readPositiveSafeInteger(value.resource_version, "resource_version");
  const payloadSummary = decodePayloadSummary(value.payload_summary);

  return {
    schemaVersion: 1,
    id: value.id,
    type: value.type,
    occurredAt: value.occurred_at,
    workspaceId,
    resourceRef: value.resource_ref,
    resourceVersion,
    payloadSummary,
    invalidations: invalidationsFor(value.resource_ref, payloadSummary),
  };
};

interface ParsedFrame {
  id?: string;
  event?: string;
  data: string[];
}

const parseFrame = (lines: string[]): ServerEventEnvelope | undefined => {
  const frame: ParsedFrame = { data: [] };
  for (const line of lines) {
    if (line === "" || line.startsWith(":")) continue;
    const separator = line.indexOf(":");
    const field = separator < 0 ? line : line.slice(0, separator);
    let value = separator < 0 ? "" : line.slice(separator + 1);
    if (value.startsWith(" ")) value = value.slice(1);
    switch (field) {
      case "id":
        if (frame.id !== undefined || value.includes("\u0000")) throw invalidEvent("SSE id 行无效");
        frame.id = value;
        break;
      case "event":
        if (frame.event !== undefined) throw invalidEvent("SSE event 行重复");
        frame.event = value;
        break;
      case "data":
        frame.data.push(value);
        break;
      default:
        throw invalidEvent(`SSE 包含不支持的字段：${field}`);
    }
  }
  if (frame.id === undefined && frame.event === undefined && frame.data.length === 0) return undefined;
  if (frame.id === undefined || frame.event === undefined || frame.data.length === 0) {
    throw invalidEvent("SSE 事件帧不完整");
  }

  let raw: unknown;
  try {
    raw = JSON.parse(frame.data.join("\n"));
  } catch (error: unknown) {
    throw invalidEvent("SSE data 不是有效 JSON", error);
  }
  const event = decodeServerEventEnvelope(raw);
  if (event.id !== frame.id || event.type !== frame.event) {
    throw invalidEvent("SSE 帧 metadata 与 Envelope 不一致");
  }
  return event;
};

export async function* parseServerEventStream(
  body: ReadableStream<Uint8Array>,
  signal?: AbortSignal,
): AsyncGenerator<ServerEventEnvelope> {
  const reader = body.getReader();
  const decoder = new TextDecoder("utf-8", { fatal: true });
  let buffer = "";
  const abort = (): void => {
    void reader.cancel(signal?.reason);
  };
  signal?.addEventListener("abort", abort, { once: true });
  try {
    for (;;) {
      const result = await reader.read();
      if (result.done) break;
      try {
        buffer += decoder.decode(result.value, { stream: true });
      } catch (error: unknown) {
        throw invalidEvent("SSE stream 不是有效 UTF-8", error);
      }
      const trailingCarriageReturn = buffer.endsWith("\r");
      const completeText = trailingCarriageReturn ? buffer.slice(0, -1) : buffer;
      buffer = completeText.replaceAll("\r\n", "\n").replaceAll("\r", "\n") +
        (trailingCarriageReturn ? "\r" : "");
      let boundary = buffer.indexOf("\n\n");
      while (boundary >= 0) {
        if (boundary > maximumFrameCharacters) throw invalidEvent("SSE 事件帧超过大小限制");
        const event = parseFrame(buffer.slice(0, boundary).split("\n"));
        buffer = buffer.slice(boundary + 2);
        if (event !== undefined) yield event;
        boundary = buffer.indexOf("\n\n");
      }
      if (buffer.length > maximumFrameCharacters) {
        throw invalidEvent("SSE 事件帧超过大小限制");
      }
    }
    try {
      buffer += decoder.decode();
    } catch (error: unknown) {
      throw invalidEvent("SSE stream 以不完整 UTF-8 结束", error);
    }
    buffer = buffer.replaceAll("\r\n", "\n").replaceAll("\r", "\n");
    if (buffer !== "") throw invalidEvent("SSE stream 以不完整事件帧结束");
  } finally {
    signal?.removeEventListener("abort", abort);
    if (signal?.aborted === true) await reader.cancel(signal.reason).catch(() => undefined);
    reader.releaseLock();
  }
}

interface Problem {
  errorCode: string;
  retryable: boolean;
  action?: string;
}

const decodeProblem = async (response: Response): Promise<Problem> => {
  let value: unknown;
  try {
    value = await response.json();
  } catch (error: unknown) {
    throw new ServerEventClientError("INVALID_RESPONSE", "SSE 错误响应不是有效 JSON", false, { cause: error });
  }
  if (!isRecord(value)) {
    throw new ServerEventClientError("INVALID_RESPONSE", "SSE Problem 响应结构无效", false);
  }
  const allowed = ["error_code", "message", "retryable", "workflow_run_id", "details"] as const;
  if (Object.keys(value).some((key) => !allowed.includes(key as (typeof allowed)[number])) ||
      typeof value.error_code !== "string" || value.error_code === "" ||
      typeof value.message !== "string" || value.message === "" || typeof value.retryable !== "boolean" ||
      (value.workflow_run_id !== undefined && (typeof value.workflow_run_id !== "string" || !uuidPattern.test(value.workflow_run_id)))) {
    throw new ServerEventClientError("INVALID_RESPONSE", "SSE Problem 响应结构无效", false);
  }
  let action: string | undefined;
  if (value.details !== undefined) {
    if (!isRecord(value.details) || (value.details.action !== undefined && typeof value.details.action !== "string")) {
      throw new ServerEventClientError("INVALID_RESPONSE", "SSE Problem details 结构无效", false);
    }
    action = value.details.action;
  }
  return { errorCode: value.error_code, retryable: value.retryable, ...(action === undefined ? {} : { action }) };
};

type Fetcher = (input: RequestInfo | URL, init?: RequestInit) => Promise<Response>;

export interface ConnectServerEventsOptions {
  workspaceId: string;
  lastEventId?: string;
  baseUrl?: string;
  fetcher?: Fetcher;
  onEvent?: (event: ServerEventEnvelope) => void | Promise<void>;
  onRecoveryRequired: (signal: ServerEventRecoverySignal) => void | Promise<void>;
  onStateChange?: (state: ServerEventConnectionState) => void;
  onError?: (error: ServerEventClientError) => void;
  initialBackoffMs?: number;
  maxBackoffMs?: number;
  sleep?: (milliseconds: number, signal: AbortSignal) => Promise<void>;
  random?: () => number;
}

export interface ServerEventConnection {
  close: () => void;
  done: Promise<void>;
  getLastEventId: () => string | undefined;
}

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
  error instanceof DOMException && error.name === "AbortError";

const validateConnectOptions = (options: ConnectServerEventsOptions): void => {
  readUuid(options.workspaceId, "workspaceId");
  if (typeof options.onRecoveryRequired !== "function") {
    throw new ServerEventClientError(
      "INVALID_RESPONSE",
      "SSE 连接必须提供超窗后的权威资源回查处理器",
      false,
    );
  }
  if (options.lastEventId !== undefined && !isValidSequence(options.lastEventId)) {
    throw new ServerEventClientError("CURSOR_REJECTED", "Last-Event-ID 无效", false);
  }
};

export const connectServerEvents = (
  options: ConnectServerEventsOptions,
): ServerEventConnection => {
  validateConnectOptions(options);
  const controller = new AbortController();
  const fetcher = options.fetcher ?? fetch;
  const sleep = options.sleep ?? defaultSleep;
  const random = options.random ?? Math.random;
  const baseUrl = (options.baseUrl ?? import.meta.env.VITE_API_BASE_URL ?? "").replace(/\/$/, "");
  const initialBackoffMs = Math.max(1, options.initialBackoffMs ?? 500);
  const maxBackoffMs = Math.max(initialBackoffMs, options.maxBackoffMs ?? 15_000);
  let lastEventId = options.lastEventId;
  const connectionAborted = (): boolean => controller.signal.aborted;

  const run = async (): Promise<void> => {
    let backoffMs = initialBackoffMs;
    let firstAttempt = true;
    options.onStateChange?.("connecting");
    while (!connectionAborted()) {
      if (!firstAttempt) options.onStateChange?.("reconnecting");
      firstAttempt = false;
      try {
        const headers = new Headers({ Accept: "text/event-stream" });
        if (lastEventId !== undefined) headers.set("Last-Event-ID", lastEventId);
        const response = await fetcher(
          `${baseUrl}/api/v1/events?workspace_id=${encodeURIComponent(options.workspaceId)}`,
          { headers, signal: controller.signal },
        );
        if (!response.ok) {
          const problem = await decodeProblem(response);
          if (
            response.status === 409 &&
            problem.errorCode === "SSE_CURSOR_EXPIRED" &&
            problem.action === "refetch"
          ) {
            await options.onRecoveryRequired({
              reason: "cursor_expired",
              workspaceId: options.workspaceId,
            });
            lastEventId = undefined;
            backoffMs = initialBackoffMs;
            continue;
          }
          if (
            response.status === 400 &&
            (problem.errorCode === "SSE_CURSOR_INVALID" || problem.errorCode === "SSE_CURSOR_FUTURE")
          ) {
            throw new ServerEventClientError("CURSOR_REJECTED", problem.errorCode, false);
          }
          throw new ServerEventClientError(
            "HTTP_ERROR",
            `SSE 请求失败（HTTP ${String(response.status)}，${problem.errorCode}）`,
            problem.retryable || response.status >= 500,
          );
        }
        if (!response.headers.get("Content-Type")?.toLowerCase().startsWith("text/event-stream")) {
          throw new ServerEventClientError("INVALID_RESPONSE", "SSE 响应 Content-Type 无效", false);
        }
        if (response.body === null) {
          throw new ServerEventClientError("INVALID_RESPONSE", "SSE 响应缺少 body", false);
        }
        options.onStateChange?.("open");
        for await (const event of parseServerEventStream(response.body, controller.signal)) {
          if (event.workspaceId !== options.workspaceId) throw invalidEvent("SSE Workspace binding 不一致");
          if (lastEventId !== undefined && BigInt(event.id) <= BigInt(lastEventId)) {
            throw invalidEvent("SSE event id 未单调递增");
          }
          await options.onEvent?.(event);
          lastEventId = event.id;
          backoffMs = initialBackoffMs;
        }
      } catch (error: unknown) {
        if (connectionAborted() || isAbortError(error)) break;
        const classified = error instanceof ServerEventClientError
          ? error
          : new ServerEventClientError("NETWORK_ERROR", "SSE 连接中断", true, { cause: error });
        options.onError?.(classified);
        if (!classified.retryable) break;
      }

      if (connectionAborted()) break;
      const jittered = Math.round(backoffMs * (0.8 + random() * 0.4));
      try {
        await sleep(jittered, controller.signal);
      } catch (error: unknown) {
        if (!connectionAborted() && !isAbortError(error)) {
          options.onError?.(new ServerEventClientError("NETWORK_ERROR", "SSE 重连等待失败", true, { cause: error }));
        }
      }
      backoffMs = Math.min(maxBackoffMs, backoffMs * 2);
    }
    options.onStateChange?.("closed");
  };

  const done = run();
  return {
    close: () => controller.abort(),
    done,
    getLastEventId: () => lastEventId,
  };
};
