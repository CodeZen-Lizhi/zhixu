import { invalidateAuthSession } from "../api/auth";
import {
  canonicalUuidPattern as uuidPattern,
  hasOnlyKeys,
  isRecord,
} from "../shared/codec";
export type ServerEventResource =
  | "conversation"
  | "question"
  | "answer"
  | "workflow"
  | "model_run"
  | "collection"
  | "export_job"
  | "health_issue"
  | "health_scan"
  | "knowledge_health"
  | "capture"
  | "knowledge_profile"
  | "working_draft"
  | "document"
  | "article_revision"
  | "document_publication"
  | "organizing_draft"
  | "organizing_snapshot"
  | "organizing_run"
  | "git_remote"
  | "git_sync_run";

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
  scopeKind?: "collection" | "workspace_attachments";
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
  reason: "cursor_expired" | "cursor_invalid" | "cursor_future";
  workspaceId: string;
}

export type ServerEventConnectionState =
  | "connecting"
  | "open"
  | "reconnecting"
  | "recovery_failed"
  | "closed";

export class ServerEventClientError extends Error {
  readonly code:
    | "INVALID_EVENT"
    | "INVALID_RESPONSE"
    | "HTTP_ERROR"
    | "CURSOR_REJECTED"
    | "RECOVERY_FAILED"
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
  "scope_kind",
  "candidate_count",
  "selected_count",
  "conflict_count",
  "degradation_count",
  "rewrite_count",
  "citation_count",
] as const;

const sequencePattern = /^[1-9][0-9]*$/;
const eventTypePattern = /^[a-z][a-z0-9_.-]{0,127}$/;
const tokenPattern = /^[a-z][a-z0-9_.-]{0,63}$/;
const resourceReferencePattern = /^[a-z][a-z0-9_.-]{0,63}:[a-z0-9][a-z0-9_.:-]*$/;
const rfc3339Pattern = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})$/;
const maximumSummaryCount = 1_000_000_000;
const maximumSequence = 9_223_372_036_854_775_807n;
const maximumEventDataCharacters = 32 * 1024;

const assertExactKeys = (
  value: Record<string, unknown>,
  allowed: readonly string[],
  field: string,
): void => {
  if (!hasOnlyKeys(value, allowed)) {
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

const readOptionalExportScopeKind = (
  value: unknown,
  field: string,
): "collection" | "workspace_attachments" | undefined => {
  if (value === undefined) return undefined;
  if (value !== "collection" && value !== "workspace_attachments") {
    throw invalidEvent(`${field} 不是有效导出范围`);
  }
  return value;
};

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
  const scopeKind = readOptionalExportScopeKind(value.scope_kind, "payload_summary.scope_kind");
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
    ...(scopeKind === undefined ? {} : { scopeKind }),
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
        resource === "export_job" ||
        resource === "health_issue" ||
        resource === "health_scan" ||
        resource === "knowledge_health" ||
        resource === "capture" ||
        resource === "knowledge_profile" ||
        resource === "working_draft" ||
        resource === "document" ||
        resource === "article_revision" ||
        resource === "document_publication" ||
        resource === "organizing_draft" ||
        resource === "organizing_snapshot" ||
        resource === "organizing_run" ||
        resource === "git_remote" ||
        resource === "git_sync_run")
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
  if (!hasOnlyKeys(value, allowed) ||
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

export interface EventSourceTransport {
  readonly readyState: number;
  onopen: ((event: Event) => void) | null;
  onmessage: ((event: MessageEvent<string>) => void) | null;
  onerror: ((event: Event) => void) | null;
  close: () => void;
}

export type EventSourceFactory = (
  url: string,
  eventSourceInitDict: EventSourceInit,
) => EventSourceTransport;

export interface ConnectServerEventsOptions {
  workspaceId: string;
  lastEventId?: string;
  baseUrl?: string;
  fetcher?: Fetcher;
  eventSourceFactory?: EventSourceFactory;
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

const eventSourceConnecting = 0;
const eventSourceClosed = 2;
const maximumQueuedEvents = 100;

interface ConnectionGeneration {
  url: string;
  active: boolean;
  source?: EventSourceTransport;
  observedLastEventId?: string;
  queue: ServerEventEnvelope[];
  queuedIds: Set<string>;
  draining: boolean;
  drainPromise: Promise<void>;
}

const decodeMessageEvent = (message: MessageEvent<string>): ServerEventEnvelope => {
  if (typeof message.data !== "string" || message.data.length > maximumEventDataCharacters) {
    throw invalidEvent("SSE message data 超过大小限制或类型无效");
  }
  let raw: unknown;
  try {
    raw = JSON.parse(message.data);
  } catch (error: unknown) {
    throw invalidEvent("SSE message data 不是有效 JSON", error);
  }
  const event = decodeServerEventEnvelope(raw);
  if (message.lastEventId !== event.id) {
    throw invalidEvent("SSE MessageEvent.lastEventId 与 Envelope id 不一致");
  }
  return event;
};

export const connectServerEvents = (
  options: ConnectServerEventsOptions,
): ServerEventConnection => {
  validateConnectOptions(options);
  const fetcher = options.fetcher ?? fetch;
  const sleep = options.sleep ?? defaultSleep;
  const random = options.random ?? Math.random;
  const eventSourceFactory = options.eventSourceFactory ?? ((url, init) => new EventSource(url, init));
  const baseUrl = (options.baseUrl ?? import.meta.env.VITE_API_BASE_URL ?? "").replace(/\/$/, "");
  const initialBackoffMs = Math.max(1, options.initialBackoffMs ?? 500);
  const maxBackoffMs = Math.max(initialBackoffMs, options.maxBackoffMs ?? 15_000);
  let committedLastEventId = options.lastEventId;
  let activeGeneration: ConnectionGeneration | undefined;
  let transitionSequence = 0;
  let transitionController: AbortController | undefined;
  let fallbackBackoffMs = initialBackoffMs;
  let stopped = false;
  let resolveDone: (() => void) | undefined;
  const done = new Promise<void>((resolve) => {
    resolveDone = resolve;
  });

  const buildUrl = (): string => {
    const query = new URLSearchParams({
      workspace_id: options.workspaceId,
      event_format: "message",
    });
    if (committedLastEventId !== undefined) query.set("last_event_id", committedLastEventId);
    return `${baseUrl}/api/v1/events?${query.toString()}`;
  };

  const isActiveGeneration = (generation: ConnectionGeneration): boolean =>
    !stopped && generation.active && activeGeneration === generation;

  const abortTransition = (): void => {
    transitionSequence += 1;
    transitionController?.abort();
    transitionController = undefined;
  };

  const deactivateGeneration = (generation: ConnectionGeneration): boolean => {
    if (!generation.active) return false;
    generation.active = false;
    generation.queue.length = 0;
    generation.queuedIds.clear();
    if (generation.source !== undefined) {
      generation.source.onopen = null;
      generation.source.onmessage = null;
      generation.source.onerror = null;
      generation.source.close();
    }
    if (activeGeneration === generation) activeGeneration = undefined;
    return true;
  };

  const finish = (
    error?: ServerEventClientError,
    state: ServerEventConnectionState = "closed",
  ): void => {
    if (stopped) return;
    stopped = true;
    abortTransition();
    if (activeGeneration !== undefined) deactivateGeneration(activeGeneration);
    if (error !== undefined) options.onError?.(error);
    options.onStateChange?.(state);
    resolveDone?.();
  };

  const recoverCursor = async (reason: ServerEventRecoverySignal["reason"]): Promise<void> => {
    try {
      await options.onRecoveryRequired({ reason, workspaceId: options.workspaceId });
    } catch (error: unknown) {
      throw new ServerEventClientError("RECOVERY_FAILED", "SSE 游标恢复所需的权威资源回查失败", true, { cause: error });
    }
    committedLastEventId = undefined;
  };

  const startTransition = (): { id: number; controller: AbortController } => {
    abortTransition();
    const controller = new AbortController();
    transitionController = controller;
    return { id: transitionSequence, controller };
  };

  const isActiveTransition = (id: number, controller: AbortController): boolean =>
    !stopped && transitionSequence === id && transitionController === controller && !controller.signal.aborted;

  const scheduleFallback = (
    error: ServerEventClientError,
    drainPromise?: Promise<void>,
  ): void => {
    if (stopped) return;
    options.onError?.(error);
    options.onStateChange?.("reconnecting");
    const transition = startTransition();
    const run = async (): Promise<void> => {
      if (drainPromise !== undefined) await drainPromise.catch(() => undefined);
      if (!isActiveTransition(transition.id, transition.controller)) return;
      const delay = Math.round(fallbackBackoffMs * (0.8 + random() * 0.4));
      fallbackBackoffMs = Math.min(maxBackoffMs, fallbackBackoffMs * 2);
      try {
        await sleep(delay, transition.controller.signal);
      } catch (sleepError: unknown) {
        if (!isActiveTransition(transition.id, transition.controller)) return;
        options.onError?.(new ServerEventClientError(
          "NETWORK_ERROR",
          "SSE fallback 重连等待失败",
          true,
          { cause: sleepError },
        ));
      }
      if (isActiveTransition(transition.id, transition.controller)) startGeneration();
    };
    void run();
  };

  const retryGeneration = (
    generation: ConnectionGeneration,
    error: ServerEventClientError,
    waitForDrain: boolean,
  ): void => {
    if (!deactivateGeneration(generation)) return;
    scheduleFallback(error, waitForDrain ? generation.drainPromise : undefined);
  };

  const drainGeneration = (generation: ConnectionGeneration): void => {
    if (!isActiveGeneration(generation) || generation.draining) return;
    generation.draining = true;
    generation.drainPromise = (async () => {
      while (isActiveGeneration(generation)) {
        const event = generation.queue.shift();
        if (event === undefined) return;
        try {
          await options.onEvent?.(event);
        } catch (error: unknown) {
          retryGeneration(
            generation,
            new ServerEventClientError("NETWORK_ERROR", "SSE 事件处理失败，将从已提交游标重放", true, { cause: error }),
            false,
          );
          return;
        }
        if (!isActiveGeneration(generation)) return;
        committedLastEventId = event.id;
        generation.queuedIds.delete(event.id);
        fallbackBackoffMs = initialBackoffMs;
      }
    })().finally(() => {
      generation.draining = false;
      if (isActiveGeneration(generation) && generation.queue.length > 0) drainGeneration(generation);
    });
  };

  const handleMessage = (
    generation: ConnectionGeneration,
    message: MessageEvent<string>,
  ): void => {
    if (!isActiveGeneration(generation)) return;
    let event: ServerEventEnvelope;
    try {
      event = decodeMessageEvent(message);
      if (event.workspaceId !== options.workspaceId) throw invalidEvent("SSE Workspace binding 不一致");
      if (event.id === committedLastEventId || generation.queuedIds.has(event.id)) return;
      if (
        generation.observedLastEventId !== undefined &&
        BigInt(event.id) <= BigInt(generation.observedLastEventId)
      ) {
        throw invalidEvent("SSE event id 未单调递增");
      }
    } catch (error: unknown) {
      const classified = error instanceof ServerEventClientError
        ? error
        : invalidEvent("SSE message 无效", error);
      if (deactivateGeneration(generation)) finish(classified);
      return;
    }

    if (generation.queuedIds.size >= maximumQueuedEvents) {
      retryGeneration(
        generation,
        new ServerEventClientError("NETWORK_ERROR", "SSE 待处理事件队列已满，将从已提交游标重放", true),
        true,
      );
      return;
    }
    generation.observedLastEventId = event.id;
    generation.queuedIds.add(event.id);
    generation.queue.push(event);
    drainGeneration(generation);
  };

  const diagnoseFatalConnection = (generation: ConnectionGeneration): void => {
    const transition = startTransition();
    const run = async (): Promise<void> => {
      await generation.drainPromise.catch(() => undefined);
      if (!isActiveTransition(transition.id, transition.controller)) return;
      const headers = new Headers({ Accept: "text/event-stream" });
      if (committedLastEventId !== undefined) headers.set("Last-Event-ID", committedLastEventId);

      let response: Response;
      try {
        response = await fetcher(generation.url, {
          method: "GET",
          headers,
          signal: transition.controller.signal,
          credentials: "include",
          cache: "no-store",
        });
      } catch (error: unknown) {
        if (!isActiveTransition(transition.id, transition.controller)) return;
        scheduleFallback(new ServerEventClientError("NETWORK_ERROR", "SSE fatal 状态诊断失败", true, { cause: error }));
        return;
      }
      if (!isActiveTransition(transition.id, transition.controller)) {
        try {
          await response.body?.cancel();
        } catch {
          // The connector is already closed; cancellation is best-effort cleanup only.
        }
        return;
      }

      if (response.status !== 200) {
        if (response.status === 401) {
          let terminalCause: unknown;
          try {
            invalidateAuthSession();
          } catch (error: unknown) {
            terminalCause = error;
          }
          try {
            await response.body?.cancel();
          } catch (error: unknown) {
            terminalCause ??= error;
          }
          finish(new ServerEventClientError(
            "HTTP_ERROR",
            terminalCause === undefined
              ? "SSE 请求未认证（HTTP 401）"
              : "SSE 401 后无法完整清理本地认证恢复状态",
            false,
            { cause: terminalCause },
          ));
          return;
        }

        let problem: Problem;
        try {
          problem = await decodeProblem(response);
        } catch (error: unknown) {
          if (!isActiveTransition(transition.id, transition.controller)) return;
          finish(error instanceof ServerEventClientError
            ? error
            : new ServerEventClientError("INVALID_RESPONSE", "SSE Problem 响应无效", false, { cause: error }));
          return;
        }
        if (!isActiveTransition(transition.id, transition.controller)) return;

        try {
          if (
            response.status === 409 &&
            problem.errorCode === "SSE_CURSOR_EXPIRED" &&
            problem.action === "refetch"
          ) {
            await recoverCursor("cursor_expired");
            fallbackBackoffMs = initialBackoffMs;
            if (isActiveTransition(transition.id, transition.controller)) startGeneration();
            return;
          }
          if (
            response.status === 400 &&
            (problem.errorCode === "SSE_CURSOR_INVALID" || problem.errorCode === "SSE_CURSOR_FUTURE")
          ) {
            await recoverCursor(problem.errorCode === "SSE_CURSOR_INVALID" ? "cursor_invalid" : "cursor_future");
            fallbackBackoffMs = initialBackoffMs;
            if (isActiveTransition(transition.id, transition.controller)) startGeneration();
            return;
          }
        } catch (error: unknown) {
          if (!isActiveTransition(transition.id, transition.controller)) return;
          finish(error instanceof ServerEventClientError
            ? error
            : new ServerEventClientError("RECOVERY_FAILED", "SSE 游标恢复失败", true, { cause: error }), "recovery_failed");
          return;
        }

        const failure = new ServerEventClientError(
          "HTTP_ERROR",
          `SSE 请求失败（HTTP ${String(response.status)}，${problem.errorCode}）`,
          problem.retryable || response.status >= 500,
        );
        if (failure.retryable) scheduleFallback(failure);
        else finish(failure);
        return;
      }

      const mediaType = response.headers.get("Content-Type")
        ?.split(";", 1)[0]
        ?.trim()
        .toLowerCase();
      if (mediaType !== "text/event-stream") {
        let cancellationError: unknown;
        try {
          await response.body?.cancel();
        } catch (error: unknown) {
          cancellationError = error;
        }
        finish(new ServerEventClientError(
          "INVALID_RESPONSE",
          "SSE 响应 Content-Type 无效",
          false,
          { cause: cancellationError },
        ));
        return;
      }
      try {
        await response.body?.cancel();
      } catch (error: unknown) {
        if (!isActiveTransition(transition.id, transition.controller)) return;
        scheduleFallback(new ServerEventClientError("NETWORK_ERROR", "SSE 诊断响应取消失败", true, { cause: error }));
        return;
      }
      if (isActiveTransition(transition.id, transition.controller)) startGeneration();
    };
    void run();
  };

  function startGeneration(): void {
    if (stopped) return;
    abortTransition();
    const generation: ConnectionGeneration = {
      url: buildUrl(),
      active: true,
      ...(committedLastEventId === undefined ? {} : { observedLastEventId: committedLastEventId }),
      queue: [],
      queuedIds: new Set<string>(),
      draining: false,
      drainPromise: Promise.resolve(),
    };
    activeGeneration = generation;
    options.onStateChange?.("connecting");

    try {
      generation.source = eventSourceFactory(generation.url, { withCredentials: true });
    } catch (error: unknown) {
      deactivateGeneration(generation);
      finish(new ServerEventClientError("INVALID_RESPONSE", "无法创建浏览器 EventSource", false, { cause: error }));
      return;
    }
    generation.source.onopen = () => {
      if (isActiveGeneration(generation)) options.onStateChange?.("open");
    };
    generation.source.onmessage = (message) => {
      handleMessage(generation, message);
    };
    generation.source.onerror = () => {
      if (!isActiveGeneration(generation) || generation.source === undefined) return;
      if (generation.source.readyState === eventSourceConnecting) {
        options.onStateChange?.("reconnecting");
        return;
      }
      if (generation.source.readyState === eventSourceClosed) {
        if (!deactivateGeneration(generation)) return;
        options.onStateChange?.("reconnecting");
        diagnoseFatalConnection(generation);
        return;
      }
      const failure = new ServerEventClientError("INVALID_RESPONSE", "EventSource error 状态无效", false);
      if (deactivateGeneration(generation)) finish(failure);
    };
  }

  startGeneration();
  return {
    close: () => finish(),
    done,
  };
};
