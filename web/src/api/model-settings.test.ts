import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  ModelSettingsApiError,
  canonicalizeModelBaseUrl,
  decodeModelSettingsResponse,
  decodeModelSettingsTestResult,
  getModelSettings,
  testModelSettings,
  updateModelSettings,
  type UpdateModelSettingsInput,
} from "./model-settings";

const disabledChat = {
  provider: "disabled",
  base_url: "",
  model: "",
  model_version: "",
  adapter_version: "v1",
  api_key_configured: false,
} as const;

const disabledEmbedding = {
  provider: "disabled",
  base_url: "",
  model: "",
  dimensions: 0,
  normalization: "l2",
  distance_metric: "cosine",
  api_key_configured: false,
} as const;

const disabledResponse = {
  desired_revision: 0,
  active_revision: 0,
  desired_settings: { chat: disabledChat, embedding: disabledEmbedding },
  active_settings: { chat: disabledChat, embedding: disabledEmbedding },
  runtime: {
    api: { applied_revision: 0, phase: "active", fresh: true },
    worker: { applied_revision: 0, phase: "active", fresh: true },
  },
  rollout: { phase: "idle", target_revision: null, last_error_code: null, retryable: false },
  restart_required: false,
  capabilities: { chat: "disabled", embedding: "disabled" },
} as const;

const configuredChat = {
  provider: "openai-compatible",
  base_url: "https://models.example.test/v1",
  model: "chat-v2",
  model_version: "2026-07",
  adapter_version: "v1",
  api_key_configured: true,
} as const;

const configuredEmbedding = {
  provider: "ollama",
  base_url: "http://127.0.0.1:11434",
  model: "nomic-embed-text",
  dimensions: 768,
  normalization: "l2",
  distance_metric: "cosine",
  api_key_configured: false,
} as const;

const configuredResponse = {
  ...disabledResponse,
  desired_revision: 2,
  active_revision: 1,
  desired_settings: { chat: configuredChat, embedding: configuredEmbedding },
  runtime: {
    api: { applied_revision: 1, phase: "active", fresh: true },
    worker: { applied_revision: 1, phase: "active", fresh: true },
  },
  restart_required: true,
  capabilities: { chat: "disabled", embedding: "disabled" },
} as const;

const updateInput: UpdateModelSettingsInput = {
  expectedRevision: 2,
  chat: {
    provider: "openai-compatible",
    baseUrl: "https://models.example.test/v1",
    model: "chat-v2",
    modelVersion: "2026-07",
    adapterVersion: "v1",
    apiKey: { action: "replace", value: "secret-chat-key" },
  },
  embedding: {
    provider: "ollama",
    baseUrl: "http://127.0.0.1:11434",
    model: "nomic-embed-text",
    dimensions: 768,
    normalization: "l2",
    distanceMetric: "cosine",
    apiKey: { action: "clear" },
  },
};

const jsonResponse = (body: unknown, status = 200): Response => new Response(JSON.stringify(body), {
  status,
  headers: { "Content-Type": "application/json", "Cache-Control": "no-store" },
});

const callJsonBody = (callIndex: number): unknown => {
  const body = vi.mocked(fetch).mock.calls[callIndex]?.[1]?.body;
  if (typeof body !== "string") throw new TypeError("expected JSON request body");
  return JSON.parse(body) as unknown;
};

beforeEach(() => {
  window.localStorage.clear();
  window.sessionStorage.clear();
  vi.stubGlobal("fetch", vi.fn());
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("model settings API boundary", () => {
  it("严格解码 disabled bootstrap 与 desired/active/runtime 差异", () => {
    expect(decodeModelSettingsResponse(disabledResponse)).toMatchObject({
      desiredRevision: 0,
      activeRevision: 0,
      desiredSettings: { chat: { provider: "disabled" }, embedding: { provider: "disabled", dimensions: 0 } },
      runtime: { api: { appliedRevision: 0, phase: "active", fresh: true } },
      restartRequired: false,
    });

    expect(decodeModelSettingsResponse(configuredResponse)).toMatchObject({
      desiredRevision: 2,
      activeRevision: 1,
      desiredSettings: {
        chat: { provider: "openai-compatible", apiKeyConfigured: true },
        embedding: { provider: "ollama", dimensions: 768, apiKeyConfigured: false },
      },
      restartRequired: true,
    });
    expect(decodeModelSettingsResponse({
      ...configuredResponse,
      rollout: { phase: "failed", target_revision: 2, last_error_code: "MODEL_SETTINGS_ROLLOUT_PREPARE_FAILED", retryable: true },
    })).toMatchObject({ rollout: { phase: "failed", retryable: true } });
  });

  it("拒绝未知字段、Secret 回显、非法状态和不一致 revision", () => {
    expect(() => decodeModelSettingsResponse({ ...disabledResponse, ignored: true })).toThrow(ModelSettingsApiError);
    expect(() => decodeModelSettingsResponse({
      ...disabledResponse,
      desired_settings: { ...disabledResponse.desired_settings, chat: { ...disabledChat, api_key: "leaked" } },
    })).toThrow(ModelSettingsApiError);
    expect(() => decodeModelSettingsResponse({
      ...disabledResponse,
      runtime: { ...disabledResponse.runtime, api: { ...disabledResponse.runtime.api, phase: "restarting" } },
    })).toThrow(ModelSettingsApiError);
    expect(() => decodeModelSettingsResponse({ ...disabledResponse, desired_revision: 1, restart_required: false })).toThrow(ModelSettingsApiError);
    expect(() => decodeModelSettingsResponse({ ...disabledResponse, restart_required: true })).toThrow(ModelSettingsApiError);
    expect(() => decodeModelSettingsResponse({ ...configuredResponse, active_revision: 3 })).toThrow(ModelSettingsApiError);
    expect(() => decodeModelSettingsResponse({
      ...configuredResponse,
      rollout: { phase: "failed", target_revision: 2, last_error_code: null, retryable: true },
    })).toThrow(ModelSettingsApiError);
    expect(() => decodeModelSettingsResponse({
      ...disabledResponse,
      runtime: { ...disabledResponse.runtime, api: { ...disabledResponse.runtime.api, applied_revision: 1 } },
    })).toThrow(ModelSettingsApiError);
    expect(() => decodeModelSettingsResponse({ ...disabledResponse, capabilities: { chat: "configured", embedding: "disabled" } })).toThrow(ModelSettingsApiError);
    expect(() => decodeModelSettingsResponse({
      ...disabledResponse,
      desired_settings: { ...disabledResponse.desired_settings, chat: { ...disabledChat, adapter_version: "v2" } },
    })).toThrow(ModelSettingsApiError);
    expect(() => decodeModelSettingsResponse({
      ...disabledResponse,
      active_settings: { ...disabledResponse.active_settings, embedding: { ...disabledEmbedding, normalization: "none" } },
    })).toThrow(ModelSettingsApiError);
    expect(() => decodeModelSettingsResponse({
      ...configuredResponse,
      desired_settings: { ...configuredResponse.desired_settings, chat: { ...configuredChat, model: "" } },
    })).toThrow(ModelSettingsApiError);
    expect(() => decodeModelSettingsResponse({
      ...configuredResponse,
      desired_settings: {
        ...configuredResponse.desired_settings,
        embedding: { ...configuredEmbedding, provider: "openai-compatible", base_url: "https://models.example.test/v1", api_key_configured: false },
      },
    })).toThrow(ModelSettingsApiError);
  });

  it("编码请求时拒绝多余字段、非法 Endpoint 和不符合领域约束的 Secret", async () => {
    const withExtraField = { ...updateInput, unexpected: true };
    await expect(updateModelSettings(withExtraField)).rejects.toMatchObject({ code: "INVALID_REQUEST" });
    await expect(updateModelSettings({
      ...updateInput,
      chat: { ...updateInput.chat, baseUrl: "https://models.example.test/v1?credential=leak" },
    })).rejects.toMatchObject({ code: "INVALID_REQUEST" });
    await expect(updateModelSettings({
      ...updateInput,
      chat: { ...updateInput.chat, baseUrl: "https://models.example.test/v1?" },
    })).rejects.toMatchObject({ code: "INVALID_REQUEST" });
    await expect(updateModelSettings({
      ...updateInput,
      chat: { ...updateInput.chat, baseUrl: "https://@models.example.test/v1" },
    })).rejects.toMatchObject({ code: "INVALID_REQUEST" });
    await expect(updateModelSettings({
      ...updateInput,
      chat: { ...updateInput.chat, baseUrl: "https://models.example.test/a%2Fb" },
    })).rejects.toMatchObject({ code: "INVALID_REQUEST" });
    await expect(updateModelSettings({
      ...updateInput,
      chat: { ...updateInput.chat, baseUrl: "http://models.example.test/v1" },
    })).rejects.toMatchObject({ code: "INVALID_REQUEST" });
    await expect(updateModelSettings({
      ...updateInput,
      embedding: { ...updateInput.embedding, baseUrl: "https://ollama.example.test" },
    })).rejects.toMatchObject({ code: "INVALID_REQUEST" });
    await expect(updateModelSettings({
      ...updateInput,
      embedding: { ...updateInput.embedding, provider: "openai-compatible", apiKey: { action: "replace", value: "embedding-key" } },
    })).rejects.toMatchObject({ code: "INVALID_REQUEST" });
    await expect(updateModelSettings({
      ...updateInput,
      chat: { ...updateInput.chat, apiKey: { action: "replace", value: "secret with spaces" } },
    })).rejects.toMatchObject({ code: "INVALID_REQUEST" });
    expect(fetch).not.toHaveBeenCalled();
  });

  it("按后端身份规则规范化 Endpoint 后再编码请求", async () => {
    vi.mocked(fetch).mockResolvedValueOnce(jsonResponse({ ...configuredResponse, desired_revision: 3 }));

    await updateModelSettings({
      ...updateInput,
      chat: { ...updateInput.chat, baseUrl: "HTTPS://MODELS.EXAMPLE.TEST:443/v1/" },
    });

    expect(canonicalizeModelBaseUrl("HTTPS://MODELS.EXAMPLE.TEST:443/v1/")).toBe("https://models.example.test/v1");
    expect(callJsonBody(0)).toMatchObject({ chat: { base_url: "https://models.example.test/v1" } });
  });

  it("GET 使用 no-store，拒绝重复字段、非 JSON 空白和非 JSON Content-Type", async () => {
    vi.mocked(fetch)
      .mockResolvedValueOnce(jsonResponse(disabledResponse))
      .mockResolvedValueOnce(new Response(`{"desired_revision":0,"desired_revision":1}`, { headers: { "Content-Type": "application/json", "Cache-Control": "no-store" } }))
      .mockResolvedValueOnce(new Response(`\u00a0${JSON.stringify(disabledResponse)}`, { headers: { "Content-Type": "application/json", "Cache-Control": "no-store" } }))
      .mockResolvedValueOnce(new Response(JSON.stringify(disabledResponse), { headers: { "Content-Type": "text/plain" } }));

    await expect(getModelSettings()).resolves.toMatchObject({ desiredRevision: 0 });
    expect(vi.mocked(fetch).mock.calls[0]?.[0]).toBe("/api/v1/settings/models");
    expect(vi.mocked(fetch).mock.calls[0]?.[1]?.cache).toBe("no-store");
    await expect(getModelSettings()).rejects.toMatchObject({ code: "INVALID_RESPONSE", errorCode: "INVALID_RESPONSE" });
    await expect(getModelSettings()).rejects.toMatchObject({ code: "INVALID_RESPONSE", errorCode: "INVALID_RESPONSE" });
    await expect(getModelSettings()).rejects.toMatchObject({ code: "INVALID_RESPONSE", errorCode: "INVALID_RESPONSE" });
  });

  it("拒绝缺失 no-store 的响应并保留 AbortError identity", async () => {
    vi.mocked(fetch)
      .mockResolvedValueOnce(new Response(JSON.stringify(disabledResponse), { headers: { "Content-Type": "application/json" } }))
      .mockImplementationOnce((_input, init) => new Promise<Response>((_resolve, reject) => {
        init?.signal?.addEventListener("abort", () => reject(new DOMException("aborted", "AbortError")), { once: true });
      }));

    await expect(getModelSettings()).rejects.toMatchObject({ code: "INVALID_RESPONSE" });
    const controller = new AbortController();
    const pending = getModelSettings(controller.signal);
    controller.abort();
    await expect(pending).rejects.toMatchObject({ name: "AbortError" });
  });

  it("PUT 只编码 write-only Secret action，响应成功后不保留明文", async () => {
    vi.mocked(fetch).mockResolvedValueOnce(jsonResponse({ ...configuredResponse, desired_revision: 3 }));

    await expect(updateModelSettings(updateInput)).resolves.toMatchObject({ desiredRevision: 3 });

    const call = vi.mocked(fetch).mock.calls[0];
    expect(call?.[0]).toBe("/api/v1/settings/models");
    expect(call?.[1]?.method).toBe("PUT");
    expect(callJsonBody(0)).toEqual({
      expected_revision: 2,
      chat: {
        provider: "openai-compatible",
        base_url: "https://models.example.test/v1",
        model: "chat-v2",
        model_version: "2026-07",
        adapter_version: "v1",
        api_key: { action: "replace", value: "secret-chat-key" },
      },
      embedding: {
        provider: "ollama",
        base_url: "http://127.0.0.1:11434",
        model: "nomic-embed-text",
        dimensions: 768,
        normalization: "l2",
        distance_metric: "cosine",
        api_key: { action: "clear" },
      },
    });
    expect(JSON.stringify(window.localStorage)).not.toContain("secret-chat-key");
    expect(JSON.stringify(window.sessionStorage)).not.toContain("secret-chat-key");
  });

  it("成功响应不会因短 Secret 与普通响应文本重合而被误判为泄漏", async () => {
    vi.mocked(fetch).mockResolvedValueOnce(jsonResponse({ ...configuredResponse, desired_revision: 3 }));

    await expect(updateModelSettings({
      ...updateInput,
      chat: { ...updateInput.chat, apiKey: { action: "replace", value: "v" } },
    })).resolves.toMatchObject({ desiredRevision: 3 });
  });

  it("409 Problem 保留稳定错误且不回显请求 Secret", async () => {
    vi.mocked(fetch)
      .mockResolvedValueOnce(jsonResponse({
        error_code: "MODEL_SETTINGS_REVISION_CONFLICT",
        message: "模型配置版本冲突",
        retryable: false,
        details: { current_revision: 4 },
      }, 409))
      .mockResolvedValueOnce(jsonResponse({
        error_code: "MODEL_SETTINGS_INVALID",
        message: "模型配置无效",
        retryable: false,
        details: { api_key: "secret-chat-key" },
      }, 400));

    const error = await updateModelSettings(updateInput).catch((value: unknown) => value);
    expect(error).toMatchObject({
      code: "HTTP_ERROR",
      errorCode: "MODEL_SETTINGS_REVISION_CONFLICT",
      status: 409,
      details: { current_revision: 4 },
    });
    expect(String(error)).not.toContain("secret-chat-key");
    await expect(updateModelSettings(updateInput)).rejects.toMatchObject({ code: "INVALID_RESPONSE" });
  });

  it("服务端错误文本意外回显本次 Secret 时 fail closed 且错误对象不保留明文", async () => {
    vi.mocked(fetch).mockResolvedValueOnce(jsonResponse({
      error_code: "MODEL_SETTINGS_INVALID",
      message: "Provider rejected secret-chat-key",
      retryable: false,
    }, 400));

    const error = await updateModelSettings(updateInput).catch((value: unknown) => value);
    expect(error).toMatchObject({ code: "INVALID_RESPONSE", errorCode: "INVALID_RESPONSE", status: 400 });
    expect(JSON.stringify(error)).not.toContain("secret-chat-key");
    expect(String(error)).not.toContain("secret-chat-key");
  });

  it("连接测试只发送目标 draft 并校验响应 binding", async () => {
    vi.mocked(fetch)
      .mockResolvedValueOnce(jsonResponse({ target: "embedding", status: "ok", provider: "ollama", model: "nomic-embed-text" }))
      .mockResolvedValueOnce(jsonResponse({ target: "chat", status: "ok", provider: "openai-compatible", model: "wrong-model" }));

    await expect(testModelSettings({ target: "embedding", embedding: updateInput.embedding })).resolves.toEqual({
      target: "embedding",
      status: "ok",
      provider: "ollama",
      model: "nomic-embed-text",
    });
    expect(callJsonBody(0)).toEqual({
      target: "embedding",
      embedding: {
        provider: "ollama",
        base_url: "http://127.0.0.1:11434",
        model: "nomic-embed-text",
        dimensions: 768,
        normalization: "l2",
        distance_metric: "cosine",
        api_key: { action: "clear" },
      },
    });

    await expect(testModelSettings({ target: "chat", chat: updateInput.chat })).rejects.toMatchObject({ code: "INVALID_RESPONSE" });
    expect(decodeModelSettingsTestResult({ target: "chat", status: "ok", provider: "openai-compatible", model: "chat-v2" })).toEqual({ target: "chat", status: "ok", provider: "openai-compatible", model: "chat-v2" });
  });

  it("连接测试在网络请求前拒绝 disabled Provider", async () => {
    await expect(testModelSettings({
      target: "chat",
      chat: {
        provider: "disabled",
        baseUrl: "",
        model: "",
        modelVersion: "",
        adapterVersion: "v1",
        apiKey: { action: "clear" },
      },
    })).rejects.toMatchObject({ code: "INVALID_REQUEST" });
    expect(fetch).not.toHaveBeenCalled();
  });
});
