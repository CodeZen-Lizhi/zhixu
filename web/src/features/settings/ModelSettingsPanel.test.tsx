import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import type { ModelSettingsResponse } from "../../api/model-settings";

const api = vi.hoisted(() => ({
  getModelSettings: vi.fn(),
  startModelSettingsActivation: vi.fn(),
  testModelSettings: vi.fn(),
  updateModelSettings: vi.fn(),
}));

vi.mock("../../api/model-settings", async (importOriginal) => ({
  // eslint-disable-next-line @typescript-eslint/consistent-type-imports
  ...(await importOriginal<typeof import("../../api/model-settings")>()),
  getModelSettings: api.getModelSettings,
  startModelSettingsActivation: api.startModelSettingsActivation,
  testModelSettings: api.testModelSettings,
  updateModelSettings: api.updateModelSettings,
}));

import { ModelSettingsApiError } from "../../api/model-settings";
import { ModelSettingsPanel } from "./ModelSettingsPanel";

const disabledChat = {
  provider: "disabled",
  apiStyle: "chat_completions",
  baseUrl: "",
  model: "",
  modelVersion: "",
  adapterVersion: "v1",
  apiKeyConfigured: false,
} as const;

const disabledEmbedding = {
  provider: "disabled",
  baseUrl: "",
  model: "",
  dimensions: 0,
  normalization: "l2",
  distanceMetric: "cosine",
  apiKeyConfigured: false,
} as const;

const rolloutId = "018f5f9e-7b36-7c89-8abc-1234567890ab";
const absentParticipant = {
  present: false,
  targetRevision: null,
  phase: null,
  fresh: false,
  lastErrorCode: null,
  retryable: false,
} as const;

const disabledSettings = (): ModelSettingsResponse => ({
  desiredRevision: 0,
  activeRevision: 0,
  desiredSettings: { chat: disabledChat, embedding: disabledEmbedding },
  activeSettings: { chat: disabledChat, embedding: disabledEmbedding },
  runtime: {
    api: { appliedRevision: 0, phase: "active", fresh: true },
    worker: { appliedRevision: 0, phase: "active", fresh: true },
  },
  rollout: { id: null, version: 0, phase: "idle", targetRevision: null, lastErrorCode: null, retryable: false },
  participants: { api: absentParticipant, worker: absentParticipant },
  applyRequired: false,
  restartRequired: false,
  capabilities: { chat: "disabled", embedding: "disabled" },
});

const configuredSettings = (): ModelSettingsResponse => ({
  desiredRevision: 2,
  activeRevision: 1,
  desiredSettings: {
    chat: {
      provider: "openai-compatible",
      apiStyle: "chat_completions",
      baseUrl: "https://models.example.test/v1",
      model: "chat-v2",
      modelVersion: "2026-07",
      adapterVersion: "v1",
      apiKeyConfigured: true,
    },
    embedding: {
      provider: "ollama",
      baseUrl: "http://127.0.0.1:11434",
      model: "nomic-embed-text",
      dimensions: 768,
      normalization: "l2",
      distanceMetric: "cosine",
      apiKeyConfigured: false,
    },
  },
  activeSettings: { chat: disabledChat, embedding: disabledEmbedding },
  runtime: {
    api: { appliedRevision: 1, phase: "active", fresh: true },
    worker: { appliedRevision: 1, phase: "active", fresh: true },
  },
  rollout: { id: null, version: 3, phase: "idle", targetRevision: null, lastErrorCode: null, retryable: false },
  participants: { api: absentParticipant, worker: absentParticipant },
  applyRequired: true,
  restartRequired: false,
  capabilities: { chat: "disabled", embedding: "disabled" },
});

const preparingSettings = (): ModelSettingsResponse => ({
  ...configuredSettings(),
  rollout: { id: rolloutId, version: 4, phase: "preparing", targetRevision: 2, lastErrorCode: null, retryable: false },
  participants: {
    api: { present: true, targetRevision: 2, phase: "prepared", fresh: true, lastErrorCode: null, retryable: false },
    worker: { present: true, targetRevision: 2, phase: "preparing", fresh: true, lastErrorCode: null, retryable: false },
  },
});

const failedSettings = (): ModelSettingsResponse => ({
  ...configuredSettings(),
  rollout: { id: rolloutId, version: 5, phase: "failed", targetRevision: 2, lastErrorCode: "MODEL_SETTINGS_ACTIVATION_PREPARE_FAILED", retryable: true },
  participants: {
    api: { present: true, targetRevision: 2, phase: "failed", fresh: true, lastErrorCode: "MODEL_SETTINGS_ACTIVATION_PREPARE_FAILED", retryable: true },
    worker: absentParticipant,
  },
});

const appliedSettings = (): ModelSettingsResponse => {
  const configured = configuredSettings();
  return {
    ...configured,
    activeRevision: 2,
    activeSettings: configured.desiredSettings,
    runtime: {
      api: { appliedRevision: 2, phase: "active", fresh: true },
      worker: { appliedRevision: 2, phase: "active", fresh: true },
    },
    rollout: { id: null, version: 7, phase: "idle", targetRevision: null, lastErrorCode: null, retryable: false },
    participants: { api: absentParticipant, worker: absentParticipant },
    applyRequired: false,
    capabilities: { chat: "configured", embedding: "configured" },
  };
};

const renderPanel = () => {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  return {
    client,
    ...render(
      <QueryClientProvider client={client}>
        <ModelSettingsPanel />
      </QueryClientProvider>,
    ),
  };
};

const expandModelSection = async (name: "对话模型" | "向量模型"): Promise<void> => {
  fireEvent.click(await screen.findByRole("button", { name: `配置${name}` }));
};

afterEach(() => {
  api.getModelSettings.mockReset();
  api.startModelSettingsActivation.mockReset();
  api.testModelSettings.mockReset();
  api.updateModelSettings.mockReset();
  window.localStorage.clear();
  window.sessionStorage.clear();
});

describe("ModelSettingsPanel", () => {
  it("展示 loading 和 canonical disabled 状态，Keyword 不被伪装成不可用", async () => {
    let resolveSettings: ((value: ModelSettingsResponse) => void) | undefined;
    api.getModelSettings.mockReturnValue(new Promise<ModelSettingsResponse>((resolve) => { resolveSettings = resolve; }));

    renderPanel();
    expect(screen.getByRole("status")).toHaveTextContent("正在读取模型设置");
    resolveSettings?.(disabledSettings());

    expect(await screen.findByRole("heading", { name: "模型与检索" })).toBeInTheDocument();
    await expandModelSection("对话模型");
    await expandModelSection("向量模型");
    expect(screen.getByText("对话模型已关闭。")).toBeInTheDocument();
    expect(screen.getByText("向量模型已关闭；关键词检索保持可用。")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "测试对话连接" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "测试向量连接" })).toBeDisabled();
  });

  it("同时展示 desired/active/applied 差异并测试已保存的 Chat Key", async () => {
    api.getModelSettings.mockResolvedValue(configuredSettings());
    api.testModelSettings.mockResolvedValue({ target: "chat", status: "ok", provider: "openai-compatible", model: "chat-v2", apiStyle: "responses", endpointPath: "/v1/responses", latencyMs: 12 });

    renderPanel();

    expect(await screen.findByText("配置已保存，尚未应用")).toBeInTheDocument();
    expect(screen.queryByText(/zhixu restart/)).not.toBeInTheDocument();
    expect(screen.queryByText(/重启容器/)).not.toBeInTheDocument();
    await expandModelSection("对话模型");
    await expandModelSection("向量模型");
    expect(screen.getAllByText("与待应用配置不同")).toHaveLength(2);
    expect(screen.getByText("版本 2")).toBeInTheDocument();
    expect(screen.getAllByText("版本 1").length).toBeGreaterThanOrEqual(3);
    fireEvent.change(screen.getByRole("combobox", { name: "对话模型调用接口" }), { target: { value: "responses" } });
    fireEvent.click(screen.getByRole("button", { name: "测试对话连接" }));

    expect(await screen.findByText(/当前草稿连接测试通过，尚未保存或生效：openai-compatible \/ chat-v2 \/ Responses API/)).toHaveTextContent("/v1/responses");
    expect(screen.queryByText(/已保存或已应用/)).not.toBeInTheDocument();
    const testInput: unknown = api.testModelSettings.mock.calls[0]?.[0];
    expect(testInput).toMatchObject({
      target: "chat",
      chat: { apiStyle: "responses", apiKey: { action: "keep" } },
    });
  });

  it("按 dirty draft 切换保存按钮，并让仅保存不启动应用", async () => {
    const initial = configuredSettings();
    const saved = {
      ...initial,
      desiredRevision: 3,
      desiredSettings: {
        ...initial.desiredSettings,
        chat: { ...initial.desiredSettings.chat, model: "chat-v3" },
      },
    };
    api.getModelSettings.mockResolvedValue(initial);
    api.updateModelSettings.mockResolvedValue(saved);

    renderPanel();
    expect(await screen.findByRole("button", { name: "应用配置" })).toBeEnabled();
    expect(screen.queryByRole("button", { name: "仅保存" })).not.toBeInTheDocument();
    await expandModelSection("对话模型");
    fireEvent.change(screen.getByLabelText("对话模型名称"), { target: { value: "chat-v3" } });

    expect(screen.getByRole("button", { name: "保存并应用" })).toBeEnabled();
    expect(screen.getByRole("button", { name: "仅保存" })).toBeEnabled();
    fireEvent.click(screen.getByRole("button", { name: "仅保存" }));

    expect(await screen.findByText("版本 3已保存，尚未应用。")).toBeInTheDocument();
    expect(api.updateModelSettings).toHaveBeenCalledOnce();
    expect(api.startModelSettingsActivation).not.toHaveBeenCalled();
  });

  it("连接测试失败时显示服务端脱敏错误并恢复操作", async () => {
    api.getModelSettings.mockResolvedValue(configuredSettings());
    api.testModelSettings.mockRejectedValue(new ModelSettingsApiError("HTTP_ERROR", "MODEL_PROVIDER_TIMEOUT", "模型提供方连接超时", true, 504));

    renderPanel();
    await screen.findByRole("heading", { name: "模型与检索" });
    await expandModelSection("对话模型");
    fireEvent.click(screen.getByRole("button", { name: "测试对话连接" }));

    expect(await screen.findByRole("alert")).toHaveTextContent("模型提供方连接超时");
    expect(screen.getByRole("alert")).toHaveTextContent("错误码：MODEL_PROVIDER_TIMEOUT");
    expect(screen.getByRole("button", { name: "测试对话连接" })).toBeEnabled();
  });

  it("连接测试展示 Provider HTTP 诊断而不触发 Session 401 语义", async () => {
    api.getModelSettings.mockResolvedValue(configuredSettings());
    api.testModelSettings.mockRejectedValue(new ModelSettingsApiError(
      "HTTP_ERROR",
      "MODEL_CHAT_UNAUTHORIZED",
      "模型 Provider 返回错误",
      false,
      502,
      {
        target: "chat",
        stage: "provider_response",
        provider_http_status: 401,
        provider_error_code: "invalid_api_key",
        provider_error_type: "authentication_error",
        provider_message: "Invalid API key",
        provider_request_id: "req_chat_401",
      },
    ));

    renderPanel();
    await expandModelSection("对话模型");
    fireEvent.click(screen.getByRole("button", { name: "测试对话连接" }));

    const diagnostic = await screen.findByRole("alert");
    expect(diagnostic).toHaveTextContent("Provider HTTP401");
    expect(diagnostic).toHaveTextContent("invalid_api_key");
    expect(diagnostic).toHaveTextContent("authentication_error");
    expect(diagnostic).toHaveTextContent("Invalid API key");
    expect(diagnostic).toHaveTextContent("req_chat_401");
    expect(diagnostic).toHaveTextContent("知序错误码MODEL_CHAT_UNAUTHORIZED");
    expect(diagnostic).toHaveTextContent("可重试否");
  });

  it("连接测试展示 TLS EOF 的安全传输原因", async () => {
    api.getModelSettings.mockResolvedValue(configuredSettings());
    api.testModelSettings.mockRejectedValue(new ModelSettingsApiError(
      "HTTP_ERROR",
      "MODEL_CHAT_REQUEST_FAILED",
      "模型 Provider 连接失败",
      false,
      502,
      { target: "chat", stage: "tls", transport_error: "EOF" },
    ));

    renderPanel();
    await expandModelSection("对话模型");
    fireEvent.click(screen.getByRole("button", { name: "测试对话连接" }));

    const diagnostic = await screen.findByRole("alert");
    expect(diagnostic).toHaveTextContent("阶段TLS 握手");
    expect(diagnostic).toHaveTextContent("连接原因EOF");
  });

  it("连接测试展示稳定的响应校验原因", async () => {
    api.getModelSettings.mockResolvedValue(configuredSettings());
    api.testModelSettings.mockRejectedValue(new ModelSettingsApiError(
      "HTTP_ERROR",
      "MODEL_CHAT_RESPONSE_INVALID",
      "模型 Provider 响应校验失败",
      false,
      502,
      { target: "chat", stage: "response_validation", validation_reason: "finish_reason_length" },
    ));

    renderPanel();
    await expandModelSection("对话模型");
    fireEvent.click(screen.getByRole("button", { name: "测试对话连接" }));

    const diagnostic = await screen.findByRole("alert");
    expect(diagnostic).toHaveTextContent("校验原因输出达到长度上限");
    expect(diagnostic).not.toHaveTextContent("provider-output-secret-canary");
  });

  it("在请求前拒绝带凭据、查询参数或片段的 Endpoint", async () => {
    api.getModelSettings.mockResolvedValue(configuredSettings());

    renderPanel();
    await expandModelSection("对话模型");
    const baseUrl = await screen.findByLabelText("对话模型基础地址（Base URL）");
    fireEvent.change(baseUrl, { target: { value: "https://user@example.test/v1?key=value" } });
    fireEvent.click(screen.getByRole("button", { name: "测试对话连接" }));

    expect(await screen.findByText(/对话模型基础地址（Base URL）必须使用 HTTPS/)).toBeInTheDocument();
    expect(screen.getByLabelText("模型设置反馈")).toHaveFocus();
    expect(api.testModelSettings).not.toHaveBeenCalled();
  });

  it("Ollama Embedding 切换为固定 Relay 且不允许编辑地址", async () => {
    const initial = configuredSettings();
    initial.desiredSettings.embedding = { ...initial.desiredSettings.embedding, provider: "openai-compatible", baseUrl: "https://models.example.test/v1", apiKeyConfigured: true };
    api.getModelSettings.mockResolvedValue(initial);

    renderPanel();
    await expandModelSection("向量模型");
    fireEvent.change(await screen.findByLabelText("向量模型提供方"), { target: { value: "ollama" } });

    expect(screen.getByLabelText("向量模型基础地址（Base URL）")).toHaveValue("http://127.0.0.1:11434");
    expect(screen.getByLabelText("向量模型基础地址（Base URL）")).toBeDisabled();
  });

  it("Endpoint 改变时拒绝 keep，替换保存后立即清空密码输入", async () => {
    const initial = configuredSettings();
    api.getModelSettings.mockResolvedValue(initial);
    api.updateModelSettings.mockImplementation(() => Promise.resolve({
      ...initial,
      desiredRevision: 3,
      desiredSettings: {
        ...initial.desiredSettings,
        chat: { ...initial.desiredSettings.chat, baseUrl: "https://new-models.example.test/v1", apiKeyConfigured: true },
      },
    }));

    const { client } = renderPanel();
    await expandModelSection("对话模型");
    const baseUrl = await screen.findByLabelText("对话模型基础地址（Base URL）");
    fireEvent.change(baseUrl, { target: { value: "https://new-models.example.test/v1" } });
    fireEvent.click(screen.getByRole("button", { name: "仅保存" }));

    expect(await screen.findByText("对话模型提供方或基础地址已改变，请替换或清除 API Key。")).toBeInTheDocument();
    expect(api.updateModelSettings).not.toHaveBeenCalled();

    const secretActions = screen.getByRole("radiogroup", { name: "对话 API Key操作" });
    fireEvent.click(within(secretActions).getByRole("radio", { name: "替换" }));
    const secret = screen.getByLabelText("对话 API Key");
    fireEvent.change(secret, { target: { value: "new-chat-secret" } });
    fireEvent.click(screen.getByRole("button", { name: "仅保存" }));

    await waitFor(() => expect(api.updateModelSettings).toHaveBeenCalledOnce());
    const updateInput: unknown = api.updateModelSettings.mock.calls[0]?.[0];
    expect(updateInput).toMatchObject({
      expectedRevision: 2,
      chat: {
        baseUrl: "https://new-models.example.test/v1",
        apiKey: { action: "replace", value: "new-chat-secret" },
      },
    });
    expect(await screen.findByText("版本 3已保存，尚未应用。")).toBeInTheDocument();
    expect(screen.getByLabelText("对话 API Key")).toHaveValue("");
    const cachedState = {
      mutations: client.getMutationCache().getAll().map((mutation) => mutation.state),
      queries: client.getQueryCache().getAll().map((query) => query.state),
    };
    expect(JSON.stringify(cachedState)).not.toContain("new-chat-secret");
    expect(JSON.stringify(window.localStorage)).not.toContain("new-chat-secret");
    expect(JSON.stringify(window.sessionStorage)).not.toContain("new-chat-secret");
  });

  it("保存并应用使用 PUT 返回的 exact revision，启动失败仍保留已保存状态并销毁 Secret", async () => {
    const initial = configuredSettings();
    const saved: ModelSettingsResponse = {
      ...initial,
      desiredRevision: 3,
      desiredSettings: {
        ...initial.desiredSettings,
        chat: { ...initial.desiredSettings.chat, model: "chat-v3" },
      },
    };
    api.getModelSettings.mockResolvedValueOnce(initial).mockResolvedValue(saved);
    api.updateModelSettings.mockResolvedValue(saved);
    api.startModelSettingsActivation.mockRejectedValue(new ModelSettingsApiError("NETWORK_ERROR", "NETWORK_ERROR", "应用请求连接中断", true));

    const { client } = renderPanel();
    await expandModelSection("对话模型");
    fireEvent.change(screen.getByLabelText("对话模型名称"), { target: { value: "chat-v3" } });
    const actions = screen.getByRole("radiogroup", { name: "对话 API Key操作" });
    fireEvent.click(within(actions).getByRole("radio", { name: "替换" }));
    fireEvent.change(screen.getByLabelText("对话 API Key"), { target: { value: "save-apply-secret" } });
    fireEvent.click(screen.getByRole("button", { name: "保存并应用" }));

    expect(await screen.findByRole("alert")).toHaveTextContent("版本 3已保存，但应用请求未确认");
    expect(screen.getByRole("alert")).toHaveTextContent("应用请求连接中断");
    expect(api.updateModelSettings).toHaveBeenCalledOnce();
    expect(api.startModelSettingsActivation).toHaveBeenCalledOnce();
    expect(api.startModelSettingsActivation.mock.calls[0]?.[0]).toEqual({ expectedRevision: 3 });
    expect(JSON.stringify(api.startModelSettingsActivation.mock.calls[0]?.[0])).not.toContain("save-apply-secret");
    expect(screen.getByLabelText("对话 API Key")).toHaveValue("");
    expect(screen.getByText("配置已保存，尚未应用")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "应用配置" })).toBeEnabled();
    const cacheState = {
      mutations: client.getMutationCache().getAll().map((mutation) => mutation.state),
      queries: client.getQueryCache().getAll().map((query) => query.state),
    };
    expect(JSON.stringify(cacheState)).not.toContain("save-apply-secret");
    expect(document.body.textContent).not.toContain("save-apply-secret");
    expect(JSON.stringify(window.localStorage)).not.toContain("save-apply-secret");
    expect(JSON.stringify(window.sessionStorage)).not.toContain("save-apply-secret");
  });

  it("应用响应丢失后通过 GET 恢复同一 target，而不重复启动", async () => {
    const initial = configuredSettings();
    const preparing = preparingSettings();
    api.getModelSettings.mockResolvedValueOnce(initial).mockResolvedValue(preparing);
    api.startModelSettingsActivation.mockRejectedValue(new ModelSettingsApiError("NETWORK_ERROR", "NETWORK_ERROR", "响应丢失", true));

    renderPanel();
    fireEvent.click(await screen.findByRole("button", { name: "应用配置" }));

    expect(await screen.findByText("已从服务端恢复版本 2的应用状态。")).toBeInTheDocument();
    expect(screen.getByText("配置应用：准备中")).toBeInTheDocument();
    expect(screen.queryByText(/应用请求未确认/)).not.toBeInTheDocument();
    expect(api.startModelSettingsActivation).toHaveBeenCalledOnce();
    expect(api.getModelSettings).toHaveBeenCalledTimes(2);
  });

  it("应用响应丢失但同一 target 已失败时展示权威失败状态", async () => {
    api.getModelSettings.mockResolvedValueOnce(configuredSettings()).mockResolvedValue(failedSettings());
    api.startModelSettingsActivation.mockRejectedValue(new ModelSettingsApiError("NETWORK_ERROR", "NETWORK_ERROR", "响应丢失", true));

    renderPanel();
    fireEvent.click(await screen.findByRole("button", { name: "应用配置" }));

    expect(await screen.findByText("配置应用失败")).toBeInTheDocument();
    expect(screen.queryByText(/应用请求未确认/)).not.toBeInTheDocument();
    expect(api.startModelSettingsActivation).toHaveBeenCalledOnce();
    expect(api.getModelSettings).toHaveBeenCalledTimes(2);
  });

  it("non-terminal 响应立即进入 2 秒轮询，terminal 后最终回查并停止", async () => {
    const initial = configuredSettings();
    const preparing = preparingSettings();
    const applied = appliedSettings();
    api.getModelSettings.mockResolvedValueOnce(initial).mockResolvedValueOnce(applied).mockResolvedValue(applied);
    api.startModelSettingsActivation.mockResolvedValue(preparing);

    renderPanel();
    fireEvent.click(await screen.findByRole("button", { name: "应用配置" }));
    expect(await screen.findByText("配置应用：准备中")).toBeInTheDocument();

    await waitFor(() => expect(api.getModelSettings).toHaveBeenCalledTimes(3), { timeout: 4_500 });
    expect(await screen.findByText("API 与工作进程已应用当前生效版本。")).toBeInTheDocument();
    const terminalCalls = api.getModelSettings.mock.calls.length;
    await new Promise((resolve) => window.setTimeout(resolve, 2_200));
    expect(api.getModelSettings).toHaveBeenCalledTimes(terminalCalls);
  }, 8_000);

  it("页面重新可见和网络恢复时都重新读取权威状态", async () => {
    api.getModelSettings.mockResolvedValue(configuredSettings());

    renderPanel();
    await screen.findByRole("button", { name: "应用配置" });
    expect(api.getModelSettings).toHaveBeenCalledOnce();

    window.dispatchEvent(new Event("visibilitychange"));
    await waitFor(() => expect(api.getModelSettings).toHaveBeenCalledTimes(2));

    window.dispatchEvent(new Event("offline"));
    window.dispatchEvent(new Event("online"));
    await waitFor(() => expect(api.getModelSettings).toHaveBeenCalledTimes(3));
  });

  it("显式 clear 发送清除动作且不把空字符串当作替换", async () => {
    const initial = configuredSettings();
    api.getModelSettings.mockResolvedValue(initial);
    api.updateModelSettings.mockResolvedValue({
      ...initial,
      desiredRevision: 3,
      desiredSettings: { ...initial.desiredSettings, chat: { ...initial.desiredSettings.chat, apiKeyConfigured: false } },
    });

    renderPanel();
    await expandModelSection("对话模型");
    const secretActions = await screen.findByRole("radiogroup", { name: "对话 API Key操作" });
    fireEvent.click(within(secretActions).getByRole("radio", { name: "清除" }));
    expect(screen.getByText("保存后会清除已保存的 API Key。")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "仅保存" }));

    await waitFor(() => expect(api.updateModelSettings).toHaveBeenCalledOnce());
    const updateInput: unknown = api.updateModelSettings.mock.calls[0]?.[0];
    expect(updateInput).toMatchObject({ chat: { apiKey: { action: "clear" } } });
  });

  it("保存失败时显示错误且不伪装成已生效", async () => {
    api.getModelSettings.mockResolvedValue(configuredSettings());
    api.updateModelSettings.mockRejectedValue(new ModelSettingsApiError("HTTP_ERROR", "MODEL_SETTINGS_UNAVAILABLE", "模型设置暂时不可用", true, 503));

    renderPanel();
    await expandModelSection("对话模型");
    fireEvent.change(screen.getByLabelText("对话模型名称"), { target: { value: "chat-v3" } });
    fireEvent.click(screen.getByRole("button", { name: "仅保存" }));

    expect(await screen.findByRole("alert")).toHaveTextContent("模型设置暂时不可用");
    expect(screen.queryByText(/已保存并已生效/)).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "仅保存" })).toBeEnabled();
  });

  it("409 后更新 Revision、保留非 Secret 草稿并要求重新输入 Secret", async () => {
    const initial = configuredSettings();
    const latest = {
      ...configuredSettings(),
      desiredRevision: 5,
      desiredSettings: {
        ...configuredSettings().desiredSettings,
        chat: { ...configuredSettings().desiredSettings.chat, model: "remote-chat-v5" },
      },
    };
    api.getModelSettings.mockResolvedValueOnce(initial).mockResolvedValueOnce(latest);
    api.updateModelSettings
      .mockRejectedValueOnce(new ModelSettingsApiError("HTTP_ERROR", "MODEL_SETTINGS_REVISION_CONFLICT", "版本冲突", false, 409, { current_revision: 5 }))
      .mockResolvedValueOnce({
        ...latest,
        desiredRevision: 6,
        desiredSettings: {
          ...latest.desiredSettings,
          chat: { ...latest.desiredSettings.chat, model: "local-chat-draft" },
        },
      });

    renderPanel();
    await expandModelSection("对话模型");
    fireEvent.change(await screen.findByLabelText("对话模型名称"), { target: { value: "local-chat-draft" } });
    const actions = screen.getByRole("radiogroup", { name: "对话 API Key操作" });
    fireEvent.click(within(actions).getByRole("radio", { name: "替换" }));
    fireEvent.change(screen.getByLabelText("对话 API Key"), { target: { value: "conflicted-secret" } });
    fireEvent.click(screen.getByRole("button", { name: "仅保存" }));

    expect(await screen.findByRole("alert")).toHaveTextContent("配置已由其他操作更新到版本 5");
    expect(screen.getByRole("alert")).toHaveTextContent("已保留本地非 Secret 草稿并清空 Secret 输入");
    expect(api.getModelSettings).toHaveBeenCalledTimes(2);
    expect(screen.getByText("版本 5")).toBeInTheDocument();
    expect(screen.getByLabelText("对话模型名称")).toHaveValue("local-chat-draft");
    expect(screen.getByLabelText("对话 API Key")).toHaveValue("");
    expect(within(actions).getByRole("radio", { name: "替换" })).toBeChecked();

    fireEvent.change(screen.getByLabelText("对话 API Key"), { target: { value: "retry-secret" } });
    fireEvent.click(screen.getByRole("button", { name: "仅保存" }));
    await waitFor(() => expect(api.updateModelSettings).toHaveBeenCalledTimes(2));
    expect(api.updateModelSettings.mock.calls[1]?.[0]).toMatchObject({
      expectedRevision: 5,
      chat: { model: "local-chat-draft", apiKey: { action: "replace", value: "retry-secret" } },
    });
  });

  it("连接测试 409 同样保留本地非 Secret 草稿并清空 Secret", async () => {
    const initial = configuredSettings();
    const latest = {
      ...configuredSettings(),
      desiredRevision: 5,
      desiredSettings: {
        ...configuredSettings().desiredSettings,
        chat: { ...configuredSettings().desiredSettings.chat, model: "chat-v5" },
      },
    };
    api.getModelSettings.mockResolvedValueOnce(initial).mockResolvedValueOnce(latest);
    api.testModelSettings.mockRejectedValue(new ModelSettingsApiError(
      "HTTP_ERROR",
      "MODEL_SETTINGS_REVISION_CONFLICT",
      "版本冲突",
      false,
      409,
      { current_revision: 5 },
    ));

    renderPanel();
    await expandModelSection("对话模型");
    fireEvent.change(await screen.findByLabelText("对话模型名称"), { target: { value: "local-chat-draft" } });
    const actions = screen.getByRole("radiogroup", { name: "对话 API Key操作" });
    fireEvent.click(within(actions).getByRole("radio", { name: "替换" }));
    fireEvent.change(screen.getByLabelText("对话 API Key"), { target: { value: "conflicted-secret" } });
    fireEvent.click(screen.getByRole("button", { name: "测试对话连接" }));

    expect(await screen.findByRole("alert")).toHaveTextContent("连接测试时配置已由其他操作更新到版本 5");
    expect(screen.getByLabelText("对话模型名称")).toHaveValue("local-chat-draft");
    expect(screen.getByLabelText("对话 API Key")).toHaveValue("");
    expect(api.getModelSettings).toHaveBeenCalledTimes(2);
  });

  it("手动刷新遇到外部 Revision 变化时保留 dirty 草稿", async () => {
    const initial = configuredSettings();
    const latest = { ...configuredSettings(), desiredRevision: 5 };
    api.getModelSettings.mockResolvedValueOnce(initial).mockResolvedValueOnce(latest);

    renderPanel();
    await expandModelSection("对话模型");
    fireEvent.change(await screen.findByLabelText("对话模型名称"), { target: { value: "local-chat-draft" } });
    const actions = screen.getByRole("radiogroup", { name: "对话 API Key操作" });
    fireEvent.click(within(actions).getByRole("radio", { name: "替换" }));
    fireEvent.change(screen.getByLabelText("对话 API Key"), { target: { value: "refresh-secret" } });
    fireEvent.click(screen.getByRole("button", { name: "刷新模型设置" }));

    expect(await screen.findByRole("alert")).toHaveTextContent("配置已由其他操作更新到版本 5");
    expect(screen.getByLabelText("对话模型名称")).toHaveValue("local-chat-draft");
    expect(screen.getByLabelText("对话 API Key")).toHaveValue("");
    expect(screen.getByText("版本 5")).toBeInTheDocument();
  });

  it("409 回查未达到服务端 Revision 时不把旧缓存伪装成恢复成功，并清空明文", async () => {
    const initial = configuredSettings();
    api.getModelSettings.mockResolvedValue(initial);
    api.updateModelSettings.mockRejectedValue(new ModelSettingsApiError(
      "HTTP_ERROR",
      "MODEL_SETTINGS_REVISION_CONFLICT",
      "版本冲突",
      false,
      409,
      { current_revision: 5 },
    ));

    renderPanel();
    await expandModelSection("对话模型");
    const actions = await screen.findByRole("radiogroup", { name: "对话 API Key操作" });
    fireEvent.click(within(actions).getByRole("radio", { name: "替换" }));
    fireEvent.change(screen.getByLabelText("对话 API Key"), { target: { value: "conflicted-secret" } });
    fireEvent.click(screen.getByRole("button", { name: "仅保存" }));

    expect(await screen.findByRole("alert")).toHaveTextContent("权威回查失败");
    expect(screen.getByRole("alert")).toHaveTextContent("服务端当前至少为版本 5");
    expect(screen.getByLabelText("对话 API Key")).toHaveValue("");
    expect(screen.getByLabelText("模型设置反馈")).toHaveFocus();
  });

  it("rollout 与依赖不可用时 fail closed，并保留刷新入口", async () => {
    const applying: ModelSettingsResponse = {
      ...configuredSettings(),
      rollout: { id: rolloutId, version: 4, phase: "preparing", targetRevision: 2, lastErrorCode: null, retryable: false },
      participants: {
        api: { present: true, targetRevision: 2, phase: "prepared", fresh: true, lastErrorCode: null, retryable: false },
        worker: absentParticipant,
      },
      capabilities: { chat: "unavailable", embedding: "configured" },
    };
    api.getModelSettings.mockResolvedValue(applying);

    renderPanel();

    expect(await screen.findByText("配置应用：准备中")).toBeInTheDocument();
    await expandModelSection("对话模型");
    expect(screen.getByText("部分模型能力不可用")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "应用配置" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "测试对话连接" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "刷新模型设置" })).toBeEnabled();
  });

  it("failed rollout 保留旧 Active 说明并开放修复、测试和保存", async () => {
    api.getModelSettings.mockResolvedValue(failedSettings());

    renderPanel();

    expect(await screen.findByText("配置应用失败")).toBeInTheDocument();
    await expandModelSection("对话模型");
    expect(screen.getAllByText(/MODEL_SETTINGS_ACTIVATION_PREPARE_FAILED/).length).toBeGreaterThanOrEqual(1);
    expect(screen.getByRole("button", { name: "重试应用" })).toBeEnabled();
    expect(screen.getByRole("button", { name: "测试对话连接" })).toBeEnabled();
  });

  it("单边运行时不健康时明确显示降级，而不声称旧版本仍在服务", async () => {
    const applied = appliedSettings();
    api.getModelSettings.mockResolvedValue({
      ...applied,
      runtime: { ...applied.runtime, worker: { appliedRevision: 2, phase: "unavailable", fresh: false } },
      rollout: { id: rolloutId, version: 8, phase: "failed", targetRevision: 2, lastErrorCode: "MODEL_RUNTIME_NOT_READY", retryable: true },
      participants: {
        api: { present: true, targetRevision: 2, phase: "prepared", fresh: true, lastErrorCode: null, retryable: false },
        worker: { present: true, targetRevision: 2, phase: "failed", fresh: true, lastErrorCode: "MODEL_RUNTIME_NOT_READY", retryable: true },
      },
      applyRequired: true,
      capabilities: { chat: "unavailable", embedding: "unavailable" },
    });

    renderPanel();

    expect(await screen.findByText(/API 或工作进程处于降级状态/)).toBeInTheDocument();
    expect(screen.queryByText(/旧的版本 2继续服务/)).not.toBeInTheDocument();
    expect(screen.getAllByText(/MODEL_RUNTIME_NOT_READY/)).toHaveLength(2);
    expect(screen.getByRole("button", { name: "重试应用" })).toBeEnabled();
  });

  it("卸载面板时中止携带瞬时 Secret 的保存请求", async () => {
    api.getModelSettings.mockResolvedValue(configuredSettings());
    let requestSignal: AbortSignal | undefined;
    api.updateModelSettings.mockImplementation((_input: unknown, signal?: AbortSignal) => {
      requestSignal = signal;
      return new Promise<ModelSettingsResponse>((_resolve, reject) => {
        signal?.addEventListener("abort", () => reject(new DOMException("aborted", "AbortError")), { once: true });
      });
    });

    const { unmount } = renderPanel();
    await expandModelSection("对话模型");
    const actions = await screen.findByRole("radiogroup", { name: "对话 API Key操作" });
    fireEvent.click(within(actions).getByRole("radio", { name: "替换" }));
    fireEvent.change(screen.getByLabelText("对话 API Key"), { target: { value: "transient-secret" } });
    fireEvent.click(screen.getByRole("button", { name: "保存并应用" }));
    await waitFor(() => expect(requestSignal).toBeDefined());

    unmount();

    expect(requestSignal?.aborted).toBe(true);
  });

  it("GET 失败显示可恢复错误而不是空配置", async () => {
    api.getModelSettings.mockRejectedValue(new ModelSettingsApiError("NETWORK_ERROR", "NETWORK_ERROR", "模型设置服务不可用", true));

    renderPanel();

    expect(await screen.findByRole("alert")).toHaveTextContent("模型设置服务不可用");
    expect(screen.getByRole("button", { name: "重试" })).toBeInTheDocument();
    expect(screen.queryByText("对话模型已关闭。")).not.toBeInTheDocument();
  });
});
