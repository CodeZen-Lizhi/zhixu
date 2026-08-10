import { authFetch } from "./auth";
import {
  canonicalUuidPattern as uuidPattern,
  hasExactKeys,
  hasOnlyKeys,
  isAbortError,
  isRecord,
} from "../shared/codec";

export type ChatModelProvider = "disabled" | "openai-compatible";
export type EmbeddingModelProvider = ChatModelProvider | "ollama";
export type EmbeddingNormalization = "none" | "l2";
export type EmbeddingDistanceMetric = "cosine" | "inner_product" | "euclidean";
export type ModelCapability = "disabled" | "configured" | "unavailable";
export type ModelRuntimePhase = "active" | "quiescing" | "quiesced" | "prepared" | "verifying" | "unavailable";
export type ModelRolloutPhase = "idle" | "validating" | "draining" | "applying" | "verifying" | "failed";
export type ModelTestTarget = "chat" | "embedding";

export type ModelSecretInput =
  | { action: "keep" }
  | { action: "clear" }
  | { action: "replace"; value: string };

export interface ChatModelSettingsSummary {
  provider: ChatModelProvider;
  baseUrl: string;
  model: string;
  modelVersion: string;
  adapterVersion: string;
  apiKeyConfigured: boolean;
}

export interface EmbeddingModelSettingsSummary {
  provider: EmbeddingModelProvider;
  baseUrl: string;
  model: string;
  dimensions: number;
  normalization: EmbeddingNormalization;
  distanceMetric: EmbeddingDistanceMetric;
  apiKeyConfigured: boolean;
}

export interface ModelSettingsSummary {
  chat: ChatModelSettingsSummary;
  embedding: EmbeddingModelSettingsSummary;
}

export interface ModelRuntimeStatus {
  appliedRevision: number;
  phase: ModelRuntimePhase;
  fresh: boolean;
}

export interface ModelRolloutStatus {
  phase: ModelRolloutPhase;
  targetRevision: number | null;
  lastErrorCode: string | null;
  retryable: boolean;
}

export interface ModelSettingsResponse {
  desiredRevision: number;
  activeRevision: number;
  desiredSettings: ModelSettingsSummary;
  activeSettings: ModelSettingsSummary;
  runtime: {
    api: ModelRuntimeStatus;
    worker: ModelRuntimeStatus;
  };
  rollout: ModelRolloutStatus;
  restartRequired: boolean;
  capabilities: {
    chat: ModelCapability;
    embedding: ModelCapability;
  };
}

export interface ChatModelSettingsInput {
  provider: ChatModelProvider;
  baseUrl: string;
  model: string;
  modelVersion: string;
  adapterVersion: string;
  apiKey: ModelSecretInput;
}

export interface EmbeddingModelSettingsInput {
  provider: EmbeddingModelProvider;
  baseUrl: string;
  model: string;
  dimensions: number;
  normalization: EmbeddingNormalization;
  distanceMetric: EmbeddingDistanceMetric;
  apiKey: ModelSecretInput;
}

export interface UpdateModelSettingsInput {
  expectedRevision: number;
  chat: ChatModelSettingsInput;
  embedding: EmbeddingModelSettingsInput;
}

export type TestModelSettingsInput =
  | { target: "chat"; chat: ChatModelSettingsInput }
  | { target: "embedding"; embedding: EmbeddingModelSettingsInput };

export interface ModelSettingsTestResult {
  target: ModelTestTarget;
  status: "ok";
  provider: EmbeddingModelProvider;
  model: string;
}

export class ModelSettingsApiError extends Error {
  readonly code: "INVALID_REQUEST" | "INVALID_RESPONSE" | "HTTP_ERROR" | "NETWORK_ERROR";
  readonly errorCode: string;
  readonly retryable: boolean;
  readonly status: number | null;
  readonly details?: Readonly<Record<string, unknown>>;

  constructor(
    code: ModelSettingsApiError["code"],
    errorCode: string,
    message: string,
    retryable: boolean,
    status: number | null = null,
    details?: Readonly<Record<string, unknown>>,
    options?: ErrorOptions,
  ) {
    super(message, options);
    this.name = "ModelSettingsApiError";
    this.code = code;
    this.errorCode = errorCode;
    this.retryable = retryable;
    this.status = status;
    if (details !== undefined) this.details = details;
  }
}

const chatProviders = ["disabled", "openai-compatible"] as const;
const embeddingProviders = ["disabled", "openai-compatible", "ollama"] as const;
const normalizations = ["none", "l2"] as const;
const distanceMetrics = ["cosine", "inner_product", "euclidean"] as const;
const capabilities = ["disabled", "configured", "unavailable"] as const;
const runtimePhases = ["active", "quiescing", "quiesced", "prepared", "verifying", "unavailable"] as const;
const rolloutPhases = ["idle", "validating", "draining", "applying", "verifying", "failed"] as const;
const testTargets = ["chat", "embedding"] as const;
const maxResponseBytes = 256 * 1024;
const maxSecretBytes = 16 * 1024;
const utf8Encoder = new TextEncoder();
const tokenPattern = /^[A-Z][A-Z0-9_]{0,127}$/;
export const modelSettingsOllamaRelayUrl = "http://127.0.0.1:11434";

const canonicalEscapedPath = (path: string): string | undefined => {
  const bytes: number[] = [];
  for (let index = 0; index < path.length;) {
    if (path[index] === "%") {
      const encoded = path.slice(index + 1, index + 3);
      if (!/^[0-9A-Fa-f]{2}$/.test(encoded)) return undefined;
      bytes.push(Number.parseInt(encoded, 16));
      index += 3;
      continue;
    }
    const codePoint = path.codePointAt(index);
    if (codePoint === undefined) return undefined;
    const character = String.fromCodePoint(codePoint);
    bytes.push(...utf8Encoder.encode(character));
    index += character.length;
  }
  return bytes.map((byte) => {
    const character = String.fromCharCode(byte);
    return /[A-Za-z0-9\-_.~$&+,/:;=@]/.test(character) ? character : `%${byte.toString(16).toUpperCase().padStart(2, "0")}`;
  }).join("");
};

export const canonicalizeModelBaseUrl = (value: string): string | undefined => {
  if (value === "" || value !== value.trim() || utf8Encoder.encode(value).byteLength > 2048 || value.includes("?") || value.includes("#")) return undefined;
  const schemeEnd = value.indexOf("://");
  if (schemeEnd < 1) return undefined;
  const remainder = value.slice(schemeEnd + 3);
  const pathStart = remainder.indexOf("/");
  const authority = pathStart === -1 ? remainder : remainder.slice(0, pathStart);
  if (authority === "" || authority.includes("@")) return undefined;
  const rawPath = pathStart === -1 ? "" : remainder.slice(pathStart);
  if (canonicalEscapedPath(rawPath) !== rawPath) return undefined;
  try {
    const endpoint = new URL(value);
    if ((endpoint.protocol !== "http:" && endpoint.protocol !== "https:") || endpoint.host === "" || endpoint.username !== "" || endpoint.password !== "" || endpoint.search !== "" || endpoint.hash !== "" || endpoint.port === "0") return undefined;
    const path = rawPath === "/" ? "" : rawPath.endsWith("/") ? rawPath.slice(0, -1) : rawPath;
    return `${endpoint.protocol}//${endpoint.host}${path}`;
  } catch {
    return undefined;
  }
};

export const canonicalizeChatModelBaseUrl = (provider: ChatModelProvider, value: string): string | undefined => {
  if (provider === "disabled") return value === "" ? "" : undefined;
  const canonical = canonicalizeModelBaseUrl(value);
  if (canonical === undefined) return undefined;
  return canonical === modelSettingsOllamaRelayUrl || new URL(canonical).protocol === "https:" ? canonical : undefined;
};

export const canonicalizeEmbeddingModelBaseUrl = (provider: EmbeddingModelProvider, value: string): string | undefined => {
  if (provider === "disabled") return value === "" ? "" : undefined;
  const canonical = canonicalizeModelBaseUrl(value);
  if (canonical === undefined) return undefined;
  if (provider === "ollama") return canonical === modelSettingsOllamaRelayUrl ? canonical : undefined;
  return new URL(canonical).protocol === "https:" ? canonical : undefined;
};

const invalidRequest = (field: string): ModelSettingsApiError =>
  new ModelSettingsApiError("INVALID_REQUEST", "INVALID_REQUEST", `模型设置请求字段无效：${field}`, false);

const invalidResponse = (field: string, status: number | null = null, cause?: unknown): ModelSettingsApiError =>
  new ModelSettingsApiError(
    "INVALID_RESPONSE",
    "INVALID_RESPONSE",
    `模型设置响应字段无效：${field}`,
    false,
    status,
    undefined,
    cause === undefined ? undefined : { cause },
  );

const exact = (value: Record<string, unknown>, keys: readonly string[], field: string): void => {
  if (!hasExactKeys(value, keys)) throw invalidResponse(field);
};

const exactRequest = (value: Record<string, unknown>, keys: readonly string[], field: string): void => {
  if (!hasExactKeys(value, keys)) throw invalidRequest(field);
};

const text = (value: unknown, field: string, maxBytes = 2048): string => {
  if (typeof value !== "string" || value !== value.trim() || utf8Encoder.encode(value).byteLength > maxBytes) throw invalidResponse(field);
  return value;
};

const canonicalText = (value: unknown, field: string, maxBytes: number): string => {
  const parsed = text(value, field, maxBytes);
  if (parsed === "") throw invalidResponse(field);
  return parsed;
};

const identityText = (value: unknown, field: string, maxBytes: number): string => {
  const parsed = canonicalText(value, field, maxBytes);
  for (const character of parsed) {
    const codePoint = character.codePointAt(0);
    if (codePoint === undefined || codePoint < 0x21 || codePoint === 0x7f) throw invalidResponse(field);
  }
  return parsed;
};

const endpointText = (value: unknown, field: string): string => {
  const parsed = canonicalText(value, field, 2048);
  if (canonicalizeModelBaseUrl(parsed) !== parsed) throw invalidResponse(field);
  return parsed;
};

const bool = (value: unknown, field: string): boolean => {
  if (typeof value !== "boolean") throw invalidResponse(field);
  return value;
};

const revision = (value: unknown, field: string): number => {
  if (typeof value !== "number" || !Number.isSafeInteger(value) || value < 0) throw invalidResponse(field);
  return value;
};

const enumValue = <T extends string>(value: unknown, values: readonly T[], field: string): T => {
  if (typeof value !== "string" || !values.includes(value as T)) throw invalidResponse(field);
  return value as T;
};

const nullableRevision = (value: unknown, field: string): number | null => value === null ? null : revision(value, field);

const nullableErrorCode = (value: unknown, field: string): string | null => {
  if (value === null) return null;
  const parsed = canonicalText(value, field, 128);
  if (!tokenPattern.test(parsed)) throw invalidResponse(field);
  return parsed;
};

class StrictJsonParser {
  private index = 0;

  constructor(private readonly source: string) {}

  parse(): unknown {
    this.skipWhitespace();
    const parsed = this.parseValue();
    this.skipWhitespace();
    if (this.index !== this.source.length) throw new SyntaxError("trailing JSON data");
    return parsed;
  }

  private parseValue(): unknown {
    const current = this.source[this.index];
    if (current === "{") return this.parseObject();
    if (current === "[") return this.parseArray();
    if (current === '"') return this.parseString();
    if (this.source.startsWith("true", this.index)) { this.index += 4; return true; }
    if (this.source.startsWith("false", this.index)) { this.index += 5; return false; }
    if (this.source.startsWith("null", this.index)) { this.index += 4; return null; }
    const match = /^-?(?:0|[1-9]\d*)(?:\.\d+)?(?:[eE][+-]?\d+)?/.exec(this.source.slice(this.index));
    if (match === null) throw new SyntaxError("invalid JSON value");
    this.index += match[0].length;
    const parsed = Number(match[0]);
    if (!Number.isFinite(parsed)) throw new SyntaxError("non-finite JSON number");
    return parsed;
  }

  private parseObject(): Record<string, unknown> {
    this.index += 1;
    this.skipWhitespace();
    const entries: [string, unknown][] = [];
    const seen = new Set<string>();
    if (this.source[this.index] === "}") { this.index += 1; return {}; }
    for (;;) {
      if (this.source[this.index] !== '"') throw new SyntaxError("invalid JSON key");
      const key = this.parseString();
      if (seen.has(key)) throw new SyntaxError(`duplicate JSON key: ${key}`);
      seen.add(key);
      this.skipWhitespace();
      if (this.source[this.index] !== ":") throw new SyntaxError("missing colon");
      this.index += 1;
      this.skipWhitespace();
      entries.push([key, this.parseValue()]);
      this.skipWhitespace();
      if (this.source[this.index] === "}") { this.index += 1; return Object.fromEntries(entries); }
      if (this.source[this.index] !== ",") throw new SyntaxError("invalid JSON separator");
      this.index += 1;
      this.skipWhitespace();
    }
  }

  private parseArray(): unknown[] {
    this.index += 1;
    this.skipWhitespace();
    const values: unknown[] = [];
    if (this.source[this.index] === "]") { this.index += 1; return values; }
    for (;;) {
      values.push(this.parseValue());
      this.skipWhitespace();
      if (this.source[this.index] === "]") { this.index += 1; return values; }
      if (this.source[this.index] !== ",") throw new SyntaxError("invalid JSON separator");
      this.index += 1;
      this.skipWhitespace();
    }
  }

  private parseString(): string {
    const start = this.index;
    this.index += 1;
    while (this.index < this.source.length) {
      if (this.source[this.index] === "\\") { this.index += 2; continue; }
      if (this.source[this.index] === '"') {
        this.index += 1;
        const parsed: unknown = JSON.parse(this.source.slice(start, this.index));
        if (typeof parsed !== "string") throw new SyntaxError("invalid JSON string");
        return parsed;
      }
      this.index += 1;
    }
    throw new SyntaxError("unterminated JSON string");
  }

  private skipWhitespace(): void {
    while ([" ", "\t", "\r", "\n"].includes(this.source[this.index] ?? "")) this.index += 1;
  }
}

const strictJson = (source: string): unknown => new StrictJsonParser(source).parse();

const decodeChatSummary = (value: unknown, field: string): ChatModelSettingsSummary => {
  if (!isRecord(value)) throw invalidResponse(field);
  exact(value, ["provider", "base_url", "model", "model_version", "adapter_version", "api_key_configured"], field);
  const provider = enumValue(value.provider, chatProviders, `${field}.provider`);
  const disabled = provider === "disabled";
  const result: ChatModelSettingsSummary = {
    provider,
    baseUrl: disabled ? text(value.base_url, `${field}.base_url`) : endpointText(value.base_url, `${field}.base_url`),
    model: disabled ? text(value.model, `${field}.model`, 128) : identityText(value.model, `${field}.model`, 128),
    modelVersion: disabled ? text(value.model_version, `${field}.model_version`, 64) : identityText(value.model_version, `${field}.model_version`, 64),
    adapterVersion: identityText(value.adapter_version, `${field}.adapter_version`, 64),
    apiKeyConfigured: bool(value.api_key_configured, `${field}.api_key_configured`),
  };
  if ((disabled && (result.baseUrl !== "" || result.model !== "" || result.modelVersion !== "" || result.apiKeyConfigured))) {
    throw invalidResponse(`${field}.provider_settings`);
  }
  if (!disabled && canonicalizeChatModelBaseUrl(provider, result.baseUrl) !== result.baseUrl) throw invalidResponse(`${field}.base_url`);
  return result;
};

const decodeEmbeddingSummary = (value: unknown, field: string): EmbeddingModelSettingsSummary => {
  if (!isRecord(value)) throw invalidResponse(field);
  exact(value, ["provider", "base_url", "model", "dimensions", "normalization", "distance_metric", "api_key_configured"], field);
  const provider = enumValue(value.provider, embeddingProviders, `${field}.provider`);
  const disabled = provider === "disabled";
  const result: EmbeddingModelSettingsSummary = {
    provider,
    baseUrl: disabled ? text(value.base_url, `${field}.base_url`) : endpointText(value.base_url, `${field}.base_url`),
    model: disabled ? text(value.model, `${field}.model`, 128) : identityText(value.model, `${field}.model`, 128),
    dimensions: revision(value.dimensions, `${field}.dimensions`),
    normalization: enumValue(value.normalization, normalizations, `${field}.normalization`),
    distanceMetric: enumValue(value.distance_metric, distanceMetrics, `${field}.distance_metric`),
    apiKeyConfigured: bool(value.api_key_configured, `${field}.api_key_configured`),
  };
  if (disabled !== (result.baseUrl === "" && result.model === "" && result.dimensions === 0) || (disabled && result.apiKeyConfigured) || (provider === "ollama" && result.apiKeyConfigured)) {
    throw invalidResponse(`${field}.provider_settings`);
  }
  if (provider === "openai-compatible" && !result.apiKeyConfigured) throw invalidResponse(`${field}.api_key_configured`);
  if (!disabled && result.dimensions === 0) throw invalidResponse(`${field}.dimensions`);
  if (!disabled && canonicalizeEmbeddingModelBaseUrl(provider, result.baseUrl) !== result.baseUrl) throw invalidResponse(`${field}.base_url`);
  return result;
};

const decodeSettingsSummary = (value: unknown, field: string): ModelSettingsSummary => {
  if (!isRecord(value)) throw invalidResponse(field);
  exact(value, ["chat", "embedding"], field);
  return {
    chat: decodeChatSummary(value.chat, `${field}.chat`),
    embedding: decodeEmbeddingSummary(value.embedding, `${field}.embedding`),
  };
};

const decodeRuntime = (value: unknown, field: string): ModelRuntimeStatus => {
  if (!isRecord(value)) throw invalidResponse(field);
  exact(value, ["applied_revision", "phase", "fresh"], field);
  return {
    appliedRevision: revision(value.applied_revision, `${field}.applied_revision`),
    phase: enumValue(value.phase, runtimePhases, `${field}.phase`),
    fresh: bool(value.fresh, `${field}.fresh`),
  };
};

const isCanonicalDisabledSummary = (summary: ModelSettingsSummary): boolean =>
  summary.chat.provider === "disabled" && summary.chat.adapterVersion === "v1" &&
  summary.embedding.provider === "disabled" && summary.embedding.normalization === "l2" && summary.embedding.distanceMetric === "cosine";

export const decodeModelSettingsResponse = (value: unknown): ModelSettingsResponse => {
  if (!isRecord(value)) throw invalidResponse("settings");
  exact(value, ["desired_revision", "active_revision", "desired_settings", "active_settings", "runtime", "rollout", "restart_required", "capabilities"], "settings");
  if (!isRecord(value.runtime)) throw invalidResponse("settings.runtime");
  exact(value.runtime, ["api", "worker"], "settings.runtime");
  if (!isRecord(value.rollout)) throw invalidResponse("settings.rollout");
  exact(value.rollout, ["phase", "target_revision", "last_error_code", "retryable"], "settings.rollout");
  if (!isRecord(value.capabilities)) throw invalidResponse("settings.capabilities");
  exact(value.capabilities, ["chat", "embedding"], "settings.capabilities");
  const result: ModelSettingsResponse = {
    desiredRevision: revision(value.desired_revision, "settings.desired_revision"),
    activeRevision: revision(value.active_revision, "settings.active_revision"),
    desiredSettings: decodeSettingsSummary(value.desired_settings, "settings.desired_settings"),
    activeSettings: decodeSettingsSummary(value.active_settings, "settings.active_settings"),
    runtime: {
      api: decodeRuntime(value.runtime.api, "settings.runtime.api"),
      worker: decodeRuntime(value.runtime.worker, "settings.runtime.worker"),
    },
    rollout: {
      phase: enumValue(value.rollout.phase, rolloutPhases, "settings.rollout.phase"),
      targetRevision: nullableRevision(value.rollout.target_revision, "settings.rollout.target_revision"),
      lastErrorCode: nullableErrorCode(value.rollout.last_error_code, "settings.rollout.last_error_code"),
      retryable: bool(value.rollout.retryable, "settings.rollout.retryable"),
    },
    restartRequired: bool(value.restart_required, "settings.restart_required"),
    capabilities: {
      chat: enumValue(value.capabilities.chat, capabilities, "settings.capabilities.chat"),
      embedding: enumValue(value.capabilities.embedding, capabilities, "settings.capabilities.embedding"),
    },
  };
  if (result.rollout.phase === "idle" && (result.rollout.targetRevision !== null || result.rollout.lastErrorCode !== null || result.rollout.retryable)) {
    throw invalidResponse("settings.rollout.idle");
  }
  if (result.rollout.phase !== "idle" && (result.rollout.targetRevision === null || result.rollout.targetRevision > result.desiredRevision)) {
    throw invalidResponse("settings.rollout.target_revision");
  }
  if (result.rollout.phase === "failed") {
    if (result.rollout.lastErrorCode === null || !result.rollout.retryable) throw invalidResponse("settings.rollout.failed");
  } else if (result.rollout.lastErrorCode !== null || result.rollout.retryable) {
    throw invalidResponse("settings.rollout.error");
  }
  if (result.activeRevision > result.desiredRevision || result.runtime.api.appliedRevision > result.desiredRevision || result.runtime.worker.appliedRevision > result.desiredRevision) {
    throw invalidResponse("settings.revisions");
  }
  if (result.desiredRevision === 0 && !isCanonicalDisabledSummary(result.desiredSettings)) {
    throw invalidResponse("settings.desired_settings");
  }
  if (result.activeRevision === 0 && !isCanonicalDisabledSummary(result.activeSettings)) {
    throw invalidResponse("settings.active_settings");
  }
  const runtimeReady = (runtime: ModelRuntimeStatus): boolean => runtime.fresh && runtime.phase === "active" && runtime.appliedRevision === result.activeRevision;
  const runtimesReady = runtimeReady(result.runtime.api) && runtimeReady(result.runtime.worker);
  const expectedRestartRequired = result.desiredRevision !== result.activeRevision || result.rollout.phase !== "idle" || !runtimesReady;
  if (result.restartRequired !== expectedRestartRequired) {
    throw invalidResponse("settings.restart_required");
  }
  const expectedCapability = (provider: EmbeddingModelProvider): ModelCapability => provider === "disabled" ? "disabled" : runtimesReady ? "configured" : "unavailable";
  if (result.capabilities.chat !== expectedCapability(result.activeSettings.chat.provider) || result.capabilities.embedding !== expectedCapability(result.activeSettings.embedding.provider)) {
    throw invalidResponse("settings.capabilities");
  }
  return result;
};

export const decodeModelSettingsTestResult = (value: unknown): ModelSettingsTestResult => {
  if (!isRecord(value)) throw invalidResponse("test_result");
  exact(value, ["target", "status", "provider", "model"], "test_result");
  const target = enumValue(value.target, testTargets, "test_result.target");
  const provider = enumValue(value.provider, embeddingProviders, "test_result.provider");
  if (provider === "disabled" || (target === "chat" && provider !== "openai-compatible")) throw invalidResponse("test_result.provider");
  return {
    target,
    status: enumValue(value.status, ["ok"] as const, "test_result.status"),
    provider,
    model: identityText(value.model, "test_result.model", 128),
  };
};

const requireRevision = (value: unknown, field: string): number => {
  if (typeof value !== "number" || !Number.isSafeInteger(value) || value < 0) throw invalidRequest(field);
  return value;
};

const requireCanonical = (value: unknown, field: string, maxBytes: number, allowEmpty = false): string => {
  if (typeof value !== "string" || value !== value.trim() || (!allowEmpty && value === "") || utf8Encoder.encode(value).byteLength > maxBytes) throw invalidRequest(field);
  return value;
};

const requireIdentity = (value: unknown, field: string, maxBytes: number, allowEmpty = false): string => {
  const parsed = requireCanonical(value, field, maxBytes, allowEmpty);
  for (const character of parsed) {
    const codePoint = character.codePointAt(0);
    if (codePoint === undefined || codePoint < 0x21 || codePoint === 0x7f) throw invalidRequest(field);
  }
  return parsed;
};

const requireEndpoint = (value: unknown, field: string): string => {
  const parsed = requireCanonical(value, field, 2048);
  const canonical = canonicalizeModelBaseUrl(parsed);
  if (canonical === undefined) throw invalidRequest(field);
  return canonical;
};

const requireChatEndpoint = (value: unknown, provider: ChatModelProvider, field: string): string => {
  const canonical = canonicalizeChatModelBaseUrl(provider, requireEndpoint(value, field));
  if (canonical === undefined) throw invalidRequest(field);
  return canonical;
};

const requireEmbeddingEndpoint = (value: unknown, provider: EmbeddingModelProvider, field: string): string => {
  const canonical = canonicalizeEmbeddingModelBaseUrl(provider, requireEndpoint(value, field));
  if (canonical === undefined) throw invalidRequest(field);
  return canonical;
};

const requireEnum = <T extends string>(value: unknown, values: readonly T[], field: string): T => {
  if (typeof value !== "string" || !values.includes(value as T)) throw invalidRequest(field);
  return value as T;
};

const encodeSecret = (secret: unknown, field: string, sensitiveValues: string[]): Record<string, unknown> => {
  if (!isRecord(secret)) throw invalidRequest(field);
  const action = secret.action;
  if (action === "keep" || action === "clear") {
    if (Object.keys(secret).length !== 1) throw invalidRequest(field);
    return { action };
  }
  if (action !== "replace" || Object.keys(secret).length !== 2 || typeof secret.value !== "string" || requireIdentity(secret.value, `${field}.value`, maxSecretBytes) === "") {
    throw invalidRequest(field);
  }
  sensitiveValues.push(secret.value);
  return { action: "replace", value: secret.value };
};

const encodeChat = (input: unknown, field: string, sensitiveValues: string[]): Record<string, unknown> => {
  if (!isRecord(input)) throw invalidRequest(field);
  exactRequest(input, ["provider", "baseUrl", "model", "modelVersion", "adapterVersion", "apiKey"], field);
  const provider = requireEnum(input.provider, chatProviders, `${field}.provider`);
  const baseUrl = provider === "disabled" ? requireCanonical(input.baseUrl, `${field}.baseUrl`, 2048, true) : requireChatEndpoint(input.baseUrl, provider, `${field}.baseUrl`);
  const model = requireIdentity(input.model, `${field}.model`, 128, provider === "disabled");
  const modelVersion = requireIdentity(input.modelVersion, `${field}.modelVersion`, 64, provider === "disabled");
  const adapterVersion = requireIdentity(input.adapterVersion, `${field}.adapterVersion`, 64);
  const apiKey = encodeSecret(input.apiKey, `${field}.apiKey`, sensitiveValues);
  if (provider === "disabled" && (baseUrl !== "" || model !== "" || modelVersion !== "" || apiKey.action !== "clear")) throw invalidRequest(`${field}.providerSettings`);
  return { provider, base_url: baseUrl, model, model_version: modelVersion, adapter_version: adapterVersion, api_key: apiKey };
};

const encodeEmbedding = (input: unknown, field: string, sensitiveValues: string[]): Record<string, unknown> => {
  if (!isRecord(input)) throw invalidRequest(field);
  exactRequest(input, ["provider", "baseUrl", "model", "dimensions", "normalization", "distanceMetric", "apiKey"], field);
  const provider = requireEnum(input.provider, embeddingProviders, `${field}.provider`);
  const baseUrl = provider === "disabled" ? requireCanonical(input.baseUrl, `${field}.baseUrl`, 2048, true) : requireEmbeddingEndpoint(input.baseUrl, provider, `${field}.baseUrl`);
  const model = requireIdentity(input.model, `${field}.model`, 128, provider === "disabled");
  const dimensions = requireRevision(input.dimensions, `${field}.dimensions`);
  const normalization = requireEnum(input.normalization, normalizations, `${field}.normalization`);
  const distanceMetric = requireEnum(input.distanceMetric, distanceMetrics, `${field}.distanceMetric`);
  const apiKey = encodeSecret(input.apiKey, `${field}.apiKey`, sensitiveValues);
  if (provider === "disabled" && (baseUrl !== "" || model !== "" || dimensions !== 0 || apiKey.action !== "clear")) throw invalidRequest(`${field}.providerSettings`);
  if (provider !== "disabled" && (dimensions < 1 || dimensions > 16_000)) throw invalidRequest(`${field}.dimensions`);
  if (provider === "ollama" && apiKey.action !== "clear") throw invalidRequest(`${field}.apiKey`);
  return { provider, base_url: baseUrl, model, dimensions, normalization, distance_metric: distanceMetric, api_key: apiKey };
};

const decodeProblem = (value: unknown, status: number): ModelSettingsApiError => {
  try {
    if (!isRecord(value)) throw invalidResponse("problem", status);
    const allowed = ["error_code", "message", "retryable", "workflow_run_id", "details"] as const;
    if (!hasOnlyKeys(value, allowed)) throw invalidResponse("problem", status);
    const errorCode = canonicalText(value.error_code, "problem.error_code", 128);
    if (!tokenPattern.test(errorCode)) throw invalidResponse("problem.error_code", status);
    const message = canonicalText(value.message, "problem.message", 4096);
    const retryable = bool(value.retryable, "problem.retryable");
    if (value.workflow_run_id !== undefined && (typeof value.workflow_run_id !== "string" || !uuidPattern.test(value.workflow_run_id))) throw invalidResponse("problem.workflow_run_id", status);
    const rawDetails = value.details;
    let details: Readonly<Record<string, unknown>> | undefined;
    if (rawDetails !== undefined) {
      if (!isRecord(rawDetails)) throw invalidResponse("problem.details", status);
      exact(rawDetails, ["current_revision"], "problem.details");
      details = { current_revision: revision(rawDetails.current_revision, "problem.details.current_revision") };
    }
    return new ModelSettingsApiError("HTTP_ERROR", errorCode, message, retryable, status, details);
  } catch (error: unknown) {
    if (error instanceof ModelSettingsApiError && error.code === "HTTP_ERROR") return error;
    return new ModelSettingsApiError("INVALID_RESPONSE", "INVALID_RESPONSE", "模型设置 API 返回了无效 Problem。", false, status, undefined, { cause: error });
  }
};

const containsSensitiveValue = (value: unknown, sensitiveValues: readonly string[]): boolean => {
  if (typeof value === "string") return sensitiveValues.some((sensitive) => value.includes(sensitive));
  if (Array.isArray(value)) return value.some((item) => containsSensitiveValue(item, sensitiveValues));
  if (!isRecord(value)) return false;
  return Object.entries(value).some(([key, item]) => containsSensitiveValue(key, sensitiveValues) || containsSensitiveValue(item, sensitiveValues));
};

const request = async (path: string, init: RequestInit = {}, sensitiveValues: readonly string[] = []): Promise<unknown> => {
  let response: Response;
  try {
    response = await authFetch(path, init);
  } catch (error: unknown) {
    if (isAbortError(error)) throw error;
    throw new ModelSettingsApiError("NETWORK_ERROR", "NETWORK_ERROR", "无法连接模型设置 API。", true);
  }
  const contentType = response.headers.get("Content-Type")?.split(";", 1)[0]?.trim().toLowerCase();
  if (contentType !== "application/json" && contentType?.endsWith("+json") !== true) throw invalidResponse("content_type", response.status);
  const cacheDirectives = response.headers.get("Cache-Control")?.split(",").map((directive) => directive.trim().toLowerCase()) ?? [];
  if (!cacheDirectives.includes("no-store")) throw invalidResponse("cache_control", response.status);
  const declaredLength = Number(response.headers.get("Content-Length"));
  if (Number.isFinite(declaredLength) && declaredLength > maxResponseBytes) throw invalidResponse("response_size", response.status);
  let source: string;
  try {
    source = await response.text();
  } catch (error: unknown) {
    if (isAbortError(error)) throw error;
    throw invalidResponse("body", response.status, error);
  }
  if (utf8Encoder.encode(source).byteLength > maxResponseBytes) throw invalidResponse("response_size", response.status);
  let payload: unknown;
  try {
    payload = strictJson(source);
  } catch (error: unknown) {
    throw invalidResponse("json", response.status, sensitiveValues.length === 0 ? error : undefined);
  }
  if (!response.ok && sensitiveValues.length > 0 && containsSensitiveValue(payload, sensitiveValues)) {
    throw new ModelSettingsApiError("INVALID_RESPONSE", "INVALID_RESPONSE", "模型设置 API 返回了包含敏感值的无效响应。", false, response.status);
  }
  if (!response.ok) throw decodeProblem(payload, response.status);
  return payload;
};

const signalInit = (signal?: AbortSignal): RequestInit => signal === undefined ? {} : { signal };

export const getModelSettings = async (signal?: AbortSignal): Promise<ModelSettingsResponse> =>
  decodeModelSettingsResponse(await request("/api/v1/settings/models", { ...signalInit(signal), cache: "no-store" }));

export const updateModelSettings = async (input: UpdateModelSettingsInput, signal?: AbortSignal): Promise<ModelSettingsResponse> => {
  if (!isRecord(input)) throw invalidRequest("body");
  exactRequest(input, ["expectedRevision", "chat", "embedding"], "body");
  const sensitiveValues: string[] = [];
  const body = {
    expected_revision: requireRevision(input.expectedRevision, "expectedRevision"),
    chat: encodeChat(input.chat, "chat", sensitiveValues),
    embedding: encodeEmbedding(input.embedding, "embedding", sensitiveValues),
  };
  return decodeModelSettingsResponse(await request("/api/v1/settings/models", {
    ...signalInit(signal),
    cache: "no-store",
    method: "PUT",
    body: JSON.stringify(body),
  }, sensitiveValues));
};

export const testModelSettings = async (input: TestModelSettingsInput, signal?: AbortSignal): Promise<ModelSettingsTestResult> => {
  if (!isRecord(input)) throw invalidRequest("body");
  const target = requireEnum(input.target, testTargets, "target");
  exactRequest(input, target === "chat" ? ["target", "chat"] : ["target", "embedding"], "body");
  const sensitiveValues: string[] = [];
  const body = input.target === "chat"
    ? { target: "chat", chat: encodeChat(input.chat, "chat", sensitiveValues) }
    : { target: "embedding", embedding: encodeEmbedding(input.embedding, "embedding", sensitiveValues) };
  if (input.target === "chat" ? input.chat.provider === "disabled" : input.embedding.provider === "disabled") throw invalidRequest("provider");
  const result = decodeModelSettingsTestResult(await request("/api/v1/settings/models/test", {
    ...signalInit(signal),
    cache: "no-store",
    method: "POST",
    body: JSON.stringify(body),
  }, sensitiveValues));
  const draft = input.target === "chat" ? input.chat : input.embedding;
  if (result.target !== target || result.provider !== draft.provider || result.model !== draft.model) throw invalidResponse("test_result.binding");
  return result;
};
