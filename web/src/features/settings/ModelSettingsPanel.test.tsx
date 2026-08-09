import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import type { ModelSettingsResponse } from "../../api/model-settings";

const api = vi.hoisted(() => ({
  getModelSettings: vi.fn(),
  testModelSettings: vi.fn(),
  updateModelSettings: vi.fn(),
}));

vi.mock("../../api/model-settings", async (importOriginal) => ({
  // eslint-disable-next-line @typescript-eslint/consistent-type-imports
  ...(await importOriginal<typeof import("../../api/model-settings")>()),
  getModelSettings: api.getModelSettings,
  testModelSettings: api.testModelSettings,
  updateModelSettings: api.updateModelSettings,
}));

import { ModelSettingsApiError } from "../../api/model-settings";
import { ModelSettingsPanel } from "./ModelSettingsPanel";

const disabledChat = {
  provider: "disabled",
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

const disabledSettings = (): ModelSettingsResponse => ({
  desiredRevision: 0,
  activeRevision: 0,
  desiredSettings: { chat: disabledChat, embedding: disabledEmbedding },
  activeSettings: { chat: disabledChat, embedding: disabledEmbedding },
  runtime: {
    api: { appliedRevision: 0, phase: "active", fresh: true },
    worker: { appliedRevision: 0, phase: "active", fresh: true },
  },
  rollout: { phase: "idle", targetRevision: null, lastErrorCode: null, retryable: false },
  restartRequired: false,
  capabilities: { chat: "disabled", embedding: "disabled" },
});

const configuredSettings = (): ModelSettingsResponse => ({
  desiredRevision: 2,
  activeRevision: 1,
  desiredSettings: {
    chat: {
      provider: "openai-compatible",
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
  rollout: { phase: "idle", targetRevision: null, lastErrorCode: null, retryable: false },
  restartRequired: true,
  capabilities: { chat: "disabled", embedding: "disabled" },
});

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
    api.testModelSettings.mockResolvedValue({ target: "chat", status: "ok", provider: "openai-compatible", model: "chat-v2" });

    renderPanel();

    expect(await screen.findByText("已保存的配置尚未生效")).toBeInTheDocument();
    await expandModelSection("对话模型");
    await expandModelSection("向量模型");
    expect(screen.getAllByText("与待应用配置不同")).toHaveLength(2);
    expect(screen.getByText("版本 2")).toBeInTheDocument();
    expect(screen.getAllByText("版本 1").length).toBeGreaterThanOrEqual(3);
    fireEvent.click(screen.getByRole("button", { name: "测试对话连接" }));

    expect(await screen.findByText("当前草稿连接测试通过，尚未保存或生效：openai-compatible / chat-v2")).toBeInTheDocument();
    expect(screen.queryByText(/已保存或已应用/)).not.toBeInTheDocument();
    const testInput: unknown = api.testModelSettings.mock.calls[0]?.[0];
    expect(testInput).toMatchObject({
      target: "chat",
      chat: { apiKey: { action: "keep" } },
    });
  });

  it("连接测试失败时显示服务端脱敏错误并恢复操作", async () => {
    api.getModelSettings.mockResolvedValue(configuredSettings());
    api.testModelSettings.mockRejectedValue(new ModelSettingsApiError("HTTP_ERROR", "MODEL_PROVIDER_TIMEOUT", "模型提供方连接超时", true, 504));

    renderPanel();
    await screen.findByRole("heading", { name: "模型与检索" });
    await expandModelSection("对话模型");
    fireEvent.click(screen.getByRole("button", { name: "测试对话连接" }));

    expect(await screen.findByRole("alert")).toHaveTextContent("模型提供方连接超时");
    expect(screen.getByRole("button", { name: "测试对话连接" })).toBeEnabled();
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
    fireEvent.click(screen.getByRole("button", { name: "保存模型设置" }));

    expect(await screen.findByText("对话模型提供方或基础地址已改变，请替换或清除 API Key。")).toBeInTheDocument();
    expect(api.updateModelSettings).not.toHaveBeenCalled();

    const secretActions = screen.getByRole("radiogroup", { name: "对话 API Key操作" });
    fireEvent.click(within(secretActions).getByRole("radio", { name: "替换" }));
    const secret = screen.getByLabelText("对话 API Key");
    fireEvent.change(secret, { target: { value: "new-chat-secret" } });
    fireEvent.click(screen.getByRole("button", { name: "保存模型设置" }));

    await waitFor(() => expect(api.updateModelSettings).toHaveBeenCalledOnce());
    const updateInput: unknown = api.updateModelSettings.mock.calls[0]?.[0];
    expect(updateInput).toMatchObject({
      expectedRevision: 2,
      chat: {
        baseUrl: "https://new-models.example.test/v1",
        apiKey: { action: "replace", value: "new-chat-secret" },
      },
    });
    expect(await screen.findByText("待应用版本 3 已保存，等待 restart 应用。")).toBeInTheDocument();
    expect(screen.getByLabelText("对话 API Key")).toHaveValue("");
    const cachedState = {
      mutations: client.getMutationCache().getAll().map((mutation) => mutation.state),
      queries: client.getQueryCache().getAll().map((query) => query.state),
    };
    expect(JSON.stringify(cachedState)).not.toContain("new-chat-secret");
    expect(JSON.stringify(window.localStorage)).not.toContain("new-chat-secret");
    expect(JSON.stringify(window.sessionStorage)).not.toContain("new-chat-secret");
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
    fireEvent.click(screen.getByRole("button", { name: "保存模型设置" }));

    await waitFor(() => expect(api.updateModelSettings).toHaveBeenCalledOnce());
    const updateInput: unknown = api.updateModelSettings.mock.calls[0]?.[0];
    expect(updateInput).toMatchObject({ chat: { apiKey: { action: "clear" } } });
  });

  it("保存失败时显示错误且不伪装成已生效", async () => {
    api.getModelSettings.mockResolvedValue(configuredSettings());
    api.updateModelSettings.mockRejectedValue(new ModelSettingsApiError("HTTP_ERROR", "MODEL_SETTINGS_UNAVAILABLE", "模型设置暂时不可用", true, 503));

    renderPanel();
    await screen.findByRole("heading", { name: "模型与检索" });
    fireEvent.click(screen.getByRole("button", { name: "保存模型设置" }));

    expect(await screen.findByRole("alert")).toHaveTextContent("模型设置暂时不可用");
    expect(screen.queryByText(/已保存并已生效/)).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "保存模型设置" })).toBeEnabled();
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
    fireEvent.click(screen.getByRole("button", { name: "保存模型设置" }));

    expect(await screen.findByRole("alert")).toHaveTextContent("配置已由其他操作更新到版本 5");
    expect(screen.getByRole("alert")).toHaveTextContent("已保留本地非 Secret 草稿并清空 Secret 输入");
    expect(api.getModelSettings).toHaveBeenCalledTimes(2);
    expect(screen.getByText("版本 5")).toBeInTheDocument();
    expect(screen.getByLabelText("对话模型名称")).toHaveValue("local-chat-draft");
    expect(screen.getByLabelText("对话 API Key")).toHaveValue("");
    expect(within(actions).getByRole("radio", { name: "替换" })).toBeChecked();

    fireEvent.change(screen.getByLabelText("对话 API Key"), { target: { value: "retry-secret" } });
    fireEvent.click(screen.getByRole("button", { name: "保存模型设置" }));
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
    fireEvent.click(screen.getByRole("button", { name: "保存模型设置" }));

    expect(await screen.findByRole("alert")).toHaveTextContent("权威回查失败");
    expect(screen.getByRole("alert")).toHaveTextContent("服务端当前至少为版本 5");
    expect(screen.getByLabelText("对话 API Key")).toHaveValue("");
    expect(screen.getByLabelText("模型设置反馈")).toHaveFocus();
  });

  it("rollout 与依赖不可用时 fail closed，并保留刷新入口", async () => {
    const applying: ModelSettingsResponse = {
      ...configuredSettings(),
      rollout: { phase: "applying", targetRevision: 2, lastErrorCode: null, retryable: false },
      capabilities: { chat: "unavailable", embedding: "configured" },
    };
    api.getModelSettings.mockResolvedValue(applying);

    renderPanel();

    expect(await screen.findByText("配置切换：应用中")).toBeInTheDocument();
    await expandModelSection("对话模型");
    expect(screen.getByText("部分模型能力不可用")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "保存模型设置" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "测试对话连接" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "刷新模型设置" })).toBeEnabled();
  });

  it("failed rollout 保留旧 Active 说明并开放修复、测试和保存", async () => {
    const failed: ModelSettingsResponse = {
      ...configuredSettings(),
      rollout: { phase: "failed", targetRevision: 2, lastErrorCode: "MODEL_SETTINGS_ROLLOUT_PREPARE_FAILED", retryable: true },
    };
    api.getModelSettings.mockResolvedValue(failed);

    renderPanel();

    expect(await screen.findByText("上次配置应用失败")).toBeInTheDocument();
    await expandModelSection("对话模型");
    expect(screen.getByText(/MODEL_SETTINGS_ROLLOUT_PREPARE_FAILED/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "保存模型设置" })).toBeEnabled();
    expect(screen.getByRole("button", { name: "测试对话连接" })).toBeEnabled();
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
    fireEvent.click(screen.getByRole("button", { name: "保存模型设置" }));
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
