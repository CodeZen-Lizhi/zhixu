import { useMutation, useQuery, useQueryClient, type UseMutationResult } from "@tanstack/react-query";
import { AlertTriangle, Check, ChevronDown, KeyRound, LoaderCircle, RefreshCw, Save, TestTube2 } from "lucide-react";
import { useEffect, useRef, useState, type ReactNode, type SyntheticEvent } from "react";

import {
  ModelSettingsApiError,
  canonicalizeChatModelBaseUrl,
  canonicalizeEmbeddingModelBaseUrl,
  canonicalizeModelBaseUrl,
  getModelSettings,
  testModelSettings,
  updateModelSettings,
  modelSettingsOllamaRelayUrl,
  type ChatModelProvider,
  type ChatAPIStyle,
  type ChatModelSettingsInput,
  type ChatModelSettingsSummary,
  type EmbeddingDistanceMetric,
  type EmbeddingModelProvider,
  type EmbeddingModelSettingsInput,
  type EmbeddingModelSettingsSummary,
  type EmbeddingNormalization,
  type ModelCapability,
  type ModelRuntimePhase,
  type ModelRolloutPhase,
  type ModelSecretInput,
  type ModelSettingsResponse,
  type ModelSettingsTestResult,
  type ModelTestStage,
  type ModelTestTarget,
  type ModelTestValidationReason,
} from "../../api/model-settings";
import { Badge, Button, Card, CardHeader, ErrorState } from "../../shared/ui";

import "./model-settings.css";

const modelSettingsQueryKey = ["settings", "models"] as const;
const modelTestStageLabels: Record<ModelTestStage, string> = {
  request: "请求构造",
  dns: "DNS 解析",
  connect: "连接",
  tls: "TLS 握手",
  provider_response: "Provider 响应",
  response_read: "响应读取",
  response_validation: "响应校验",
  cancelled: "请求取消",
  timeout: "请求超时",
};

type SecretAction = ModelSecretInput["action"];

interface SecretDraft {
  action: SecretAction;
  value: string;
}

interface ChatDraft {
  provider: ChatModelProvider;
  apiStyle: ChatAPIStyle;
  baseUrl: string;
  model: string;
  modelVersion: string;
  adapterVersion: string;
  secret: SecretDraft;
}

interface EmbeddingDraft {
  provider: EmbeddingModelProvider;
  baseUrl: string;
  model: string;
  dimensions: string;
  normalization: EmbeddingNormalization;
  distanceMetric: EmbeddingDistanceMetric;
  secret: SecretDraft;
}

interface ModelSettingsDraft {
  revision: number;
  chat: ChatDraft;
  embedding: EmbeddingDraft;
}

const capabilityTone = (capability: ModelCapability): "success" | "warning" | "neutral" =>
  capability === "configured" ? "success" : capability === "unavailable" ? "warning" : "neutral";

const capabilityLabel: Record<ModelCapability, string> = {
  configured: "已配置",
  disabled: "已关闭",
  unavailable: "不可用",
};

const providerLabel: Record<EmbeddingModelProvider, string> = {
  disabled: "已关闭",
  "openai-compatible": "OpenAI-compatible",
  ollama: "Ollama",
};

const chatAPIStyleLabel: Record<ChatAPIStyle, string> = {
  chat_completions: "Chat Completions",
  responses: "Responses API",
};

const chatProviderValue = (value: string): ChatModelProvider => {
  if (value === "disabled" || value === "openai-compatible") return value;
  throw new TypeError("unknown Chat provider");
};

const chatAPIStyleValue = (value: string): ChatAPIStyle => {
  if (value === "chat_completions" || value === "responses") return value;
  throw new TypeError("unknown Chat API style");
};

const embeddingProviderValue = (value: string): EmbeddingModelProvider => {
  if (value === "disabled" || value === "openai-compatible" || value === "ollama") return value;
  throw new TypeError("unknown Embedding provider");
};

const normalizationValue = (value: string): EmbeddingNormalization => {
  if (value === "none" || value === "l2") return value;
  throw new TypeError("unknown Embedding normalization");
};

const distanceMetricValue = (value: string): EmbeddingDistanceMetric => {
  if (value === "cosine" || value === "inner_product" || value === "euclidean") return value;
  throw new TypeError("unknown Embedding distance metric");
};

const runtimeTone = (fresh: boolean, phase: string): "success" | "warning" | "danger" =>
  fresh && phase === "active" ? "success" : fresh ? "warning" : "danger";

const runtimePhaseLabels: Record<ModelRuntimePhase, string> = {
  active: "已生效",
  quiescing: "正在停止",
  quiesced: "已停止",
  prepared: "已准备",
  verifying: "校验中",
  unavailable: "不可用",
};

const rolloutPhaseLabels: Record<ModelRolloutPhase, string> = {
  idle: "空闲",
  validating: "校验中",
  draining: "排空中",
  applying: "应用中",
  verifying: "验证中",
  failed: "失败",
};

const modelValidationReasonLabels: Record<ModelTestValidationReason, string> = {
  invalid_response: "响应结构无效",
  model_mismatch: "模型版本不匹配",
  finish_reason_length: "输出达到长度上限",
  finish_reason_invalid: "完成原因无效",
  empty_content: "回复内容为空",
  refusal: "模型拒绝响应",
  tool_calls: "返回了未允许的工具调用",
  missing_usage: "缺少 Token 用量",
  invalid_usage: "Token 用量无效",
  response_contract_invalid: "响应契约无效",
};

const revisionLabel = (revision: number): string => `版本 ${String(revision)}`;

const isRolloutInProgress = (phase: ModelRolloutPhase): boolean => phase !== "idle" && phase !== "failed";

const initialSecret = (configured: boolean): SecretDraft => ({ action: configured ? "keep" : "clear", value: "" });

const draftFromResponse = (settings: ModelSettingsResponse): ModelSettingsDraft => ({
  revision: settings.desiredRevision,
  chat: {
    provider: settings.desiredSettings.chat.provider,
    apiStyle: settings.desiredSettings.chat.apiStyle,
    baseUrl: settings.desiredSettings.chat.baseUrl,
    model: settings.desiredSettings.chat.model,
    modelVersion: settings.desiredSettings.chat.modelVersion,
    adapterVersion: settings.desiredSettings.chat.adapterVersion,
    secret: initialSecret(settings.desiredSettings.chat.apiKeyConfigured),
  },
  embedding: {
    provider: settings.desiredSettings.embedding.provider,
    baseUrl: settings.desiredSettings.embedding.baseUrl,
    model: settings.desiredSettings.embedding.model,
    dimensions: String(settings.desiredSettings.embedding.dimensions),
    normalization: settings.desiredSettings.embedding.normalization,
    distanceMetric: settings.desiredSettings.embedding.distanceMetric,
    secret: initialSecret(settings.desiredSettings.embedding.apiKeyConfigured),
  },
});

const preserveDraftForRevision = (draft: ModelSettingsDraft, revision: number): ModelSettingsDraft => ({
  ...draft,
  revision,
  chat: { ...draft.chat, secret: { ...draft.chat.secret, value: "" } },
  embedding: { ...draft.embedding, secret: { ...draft.embedding.secret, value: "" } },
});

const sameSecretTarget = (
  provider: EmbeddingModelProvider,
  baseUrl: string,
  savedProvider: EmbeddingModelProvider,
  savedBaseUrl: string,
): boolean => {
  const normalized = canonicalizeModelBaseUrl(baseUrl.trim());
  return provider === savedProvider && normalized !== undefined && normalized === canonicalizeModelBaseUrl(savedBaseUrl);
};

const secretInput = (secret: SecretDraft): ModelSecretInput => secret.action === "replace"
  ? { action: "replace", value: secret.value }
  : { action: secret.action };

const chatInput = (draft: ChatDraft): ChatModelSettingsInput => draft.provider === "disabled"
  ? { provider: "disabled", apiStyle: draft.apiStyle, baseUrl: "", model: "", modelVersion: "", adapterVersion: draft.adapterVersion, apiKey: { action: "clear" } }
  : {
      provider: draft.provider,
      apiStyle: draft.apiStyle,
      baseUrl: draft.baseUrl.trim(),
      model: draft.model.trim(),
      modelVersion: draft.modelVersion.trim(),
      adapterVersion: draft.adapterVersion,
      apiKey: secretInput(draft.secret),
    };

const embeddingInput = (draft: EmbeddingDraft): EmbeddingModelSettingsInput => draft.provider === "disabled"
  ? { provider: "disabled", baseUrl: "", model: "", dimensions: 0, normalization: draft.normalization, distanceMetric: draft.distanceMetric, apiKey: { action: "clear" } }
  : {
      provider: draft.provider,
      baseUrl: draft.baseUrl.trim(),
      model: draft.model.trim(),
      dimensions: Number(draft.dimensions),
      normalization: draft.normalization,
      distanceMetric: draft.distanceMetric,
      apiKey: draft.provider === "ollama" ? { action: "clear" } : secretInput(draft.secret),
    };

const validateDraft = (draft: ModelSettingsDraft, settings: ModelSettingsResponse, target?: ModelTestTarget): string | undefined => {
  if (target === undefined || target === "chat") {
    if (draft.chat.provider !== "disabled" && (draft.chat.baseUrl.trim() === "" || draft.chat.model.trim() === "" || draft.chat.modelVersion.trim() === "")) {
      return "对话模型启用后必须填写基础地址（Base URL）、模型和模型版本。";
    }
    if (draft.chat.provider !== "disabled" && canonicalizeChatModelBaseUrl(draft.chat.provider, draft.chat.baseUrl.trim()) === undefined) return `对话模型基础地址（Base URL）必须使用 HTTPS；本地 Ollama 仅允许固定 Relay ${modelSettingsOllamaRelayUrl}。`;
    const canKeep = settings.desiredSettings.chat.apiKeyConfigured && sameSecretTarget(draft.chat.provider, draft.chat.baseUrl, settings.desiredSettings.chat.provider, settings.desiredSettings.chat.baseUrl);
    if (draft.chat.provider !== "disabled" && draft.chat.secret.action === "keep" && !canKeep) return "对话模型提供方或基础地址已改变，请替换或清除 API Key。";
    if (draft.chat.provider !== "disabled" && draft.chat.secret.action === "replace" && draft.chat.secret.value.trim() === "") return "请输入新的对话 API Key。";
  }
  if (target === undefined || target === "embedding") {
    const dimensions = Number(draft.embedding.dimensions);
    if (draft.embedding.provider !== "disabled" && (draft.embedding.baseUrl.trim() === "" || draft.embedding.model.trim() === "")) return "向量模型启用后必须填写基础地址（Base URL）和模型。";
    if (draft.embedding.provider !== "disabled" && canonicalizeEmbeddingModelBaseUrl(draft.embedding.provider, draft.embedding.baseUrl.trim()) === undefined) return draft.embedding.provider === "ollama"
      ? `Ollama Embedding 必须使用固定 Relay ${modelSettingsOllamaRelayUrl}。`
      : "OpenAI-compatible 向量模型的基础地址（Base URL）必须使用无凭据、查询参数和片段的 HTTPS 地址。";
    if (draft.embedding.provider !== "disabled" && (!Number.isSafeInteger(dimensions) || dimensions < 1 || dimensions > 16_000)) return "向量维度必须是 1 到 16000 的整数。";
    const canKeep = settings.desiredSettings.embedding.apiKeyConfigured && sameSecretTarget(draft.embedding.provider, draft.embedding.baseUrl, settings.desiredSettings.embedding.provider, settings.desiredSettings.embedding.baseUrl);
    if (draft.embedding.provider === "openai-compatible" && draft.embedding.secret.action === "keep" && !canKeep) return "向量模型提供方或基础地址已改变，请替换 API Key。";
    if (draft.embedding.provider === "openai-compatible" && draft.embedding.secret.action === "clear") return "OpenAI-compatible 向量模型需要 API Key；请选择保留或替换。";
    if (draft.embedding.provider === "openai-compatible" && draft.embedding.secret.action === "replace" && draft.embedding.secret.value.trim() === "") return "请输入新的向量 API Key。";
  }
  return undefined;
};

const settingsEqual = (left: ChatModelSettingsSummary | EmbeddingModelSettingsSummary, right: ChatModelSettingsSummary | EmbeddingModelSettingsSummary): boolean =>
  JSON.stringify(left) === JSON.stringify(right);

const SummaryValue = ({ label, children, mono = false }: { label: string; children: ReactNode; mono?: boolean }) => <div><dt>{label}</dt><dd className={mono ? "mono" : undefined}>{children}</dd></div>;

const ActiveSummary = ({ kind, summary, differs }: { kind: ModelTestTarget; summary: ChatModelSettingsSummary | EmbeddingModelSettingsSummary; differs: boolean }) => <aside className="model-settings-active" aria-label={`${kind === "chat" ? "Chat" : "Embedding"} 当前生效配置`}>
  <div className="model-settings-active__heading"><div><span>生效配置</span><strong>当前生效</strong></div><Badge tone={differs ? "warning" : "success"}>{differs ? "与待应用配置不同" : "与待应用配置一致"}</Badge></div>
  <dl>
    <SummaryValue label="提供方">{providerLabel[summary.provider]}</SummaryValue>
    <SummaryValue label="基础地址（Base URL）" mono>{summary.baseUrl || "未设置"}</SummaryValue>
    <SummaryValue label="模型" mono>{summary.model || "未设置"}</SummaryValue>
    {kind === "chat"
      ? <><SummaryValue label="调用接口">{chatAPIStyleLabel[(summary as ChatModelSettingsSummary).apiStyle]}</SummaryValue><SummaryValue label="模型版本" mono>{(summary as ChatModelSettingsSummary).modelVersion || "未设置"}</SummaryValue><SummaryValue label="适配器" mono>{(summary as ChatModelSettingsSummary).adapterVersion}</SummaryValue></>
      : <><SummaryValue label="维度">{String((summary as EmbeddingModelSettingsSummary).dimensions)}</SummaryValue><SummaryValue label="向量约定">{(summary as EmbeddingModelSettingsSummary).normalization} / {(summary as EmbeddingModelSettingsSummary).distanceMetric}</SummaryValue></>}
    <SummaryValue label="API Key">{summary.apiKeyConfigured ? "已安全保存" : "未配置"}</SummaryValue>
  </dl>
</aside>;

const SecretControl = ({
  id,
  label,
  secret,
  configured,
  canKeep,
  disabled,
  onChange,
}: {
  id: string;
  label: string;
  secret: SecretDraft;
  configured: boolean;
  canKeep: boolean;
  disabled: boolean;
  onChange: (secret: SecretDraft) => void;
}) => <div className="model-secret-control model-settings-field--wide">
  <div className="model-secret-control__heading"><label htmlFor={id}><KeyRound size={14} />{label}</label><Badge tone={configured ? "success" : "neutral"}>{configured ? "已保存" : "未配置"}</Badge></div>
  <div className="model-secret-actions" role="radiogroup" aria-label={`${label}操作`}>
    <label><input type="radio" name={`${id}-action`} value="keep" checked={secret.action === "keep"} disabled={disabled || !canKeep} onChange={() => onChange({ action: "keep", value: "" })} />保留</label>
    <label><input type="radio" name={`${id}-action`} value="replace" checked={secret.action === "replace"} disabled={disabled} onChange={() => onChange({ action: "replace", value: "" })} />替换</label>
    <label><input type="radio" name={`${id}-action`} value="clear" checked={secret.action === "clear"} disabled={disabled} onChange={() => onChange({ action: "clear", value: "" })} />清除</label>
  </div>
  <input
    id={id}
    type="password"
    autoComplete="new-password"
    maxLength={16 * 1024}
    value={secret.value}
    disabled={disabled || secret.action === "clear"}
    placeholder={secret.action === "keep" ? "留空保留已保存的密钥" : secret.action === "clear" ? "保存后清除密钥" : "输入新的 API Key"}
    onChange={(event) => onChange({ action: event.target.value === "" && canKeep ? "keep" : "replace", value: event.target.value })}
  />
  {!canKeep && secret.action === "keep" ? <p className="model-settings-warning" role="alert"><AlertTriangle size={14} />目标地址已改变，不能继续使用原 API Key。</p> : null}
  {secret.action === "clear" && configured ? <p className="model-settings-warning"><AlertTriangle size={14} />保存后会清除已保存的 API Key。</p> : null}
</div>;

const TestFeedback = ({ target, mutation }: { target: ModelTestTarget; mutation: UseMutationResult<ModelSettingsTestResult, Error, ModelTestTarget> }) => {
  if (mutation.isPending && mutation.variables === target) return <p className="model-test-result" role="status"><LoaderCircle className="is-spinning" size={15} />正在从服务端测试连接…</p>;
  if (mutation.isError && mutation.variables === target && mutation.error instanceof ModelSettingsApiError && mutation.error.status === 409) return null;
  if (mutation.isError && mutation.variables === target) {
    const error = mutation.error;
    const apiError = error instanceof ModelSettingsApiError ? error : undefined;
    const diagnostic = apiError?.details?.target === target && apiError.details.stage !== undefined ? apiError.details : undefined;
    const diagnosticStage = diagnostic?.stage;
    if (diagnostic === undefined || diagnosticStage === undefined || apiError === undefined) return <p className="model-settings-error" role="alert">{error.message}{apiError === undefined ? null : <>（错误码：<code>{apiError.errorCode}</code>{apiError.retryable ? "，可重试" : "，不可重试"}）</>}</p>;
    return <div className="model-test-diagnostic" role="alert">
      <strong>{error.message}</strong>
      <dl>
        <div><dt>阶段</dt><dd>{modelTestStageLabels[diagnosticStage]}</dd></div>
        {diagnostic.provider_http_status === undefined ? null : <div><dt>Provider HTTP</dt><dd>{String(diagnostic.provider_http_status)}</dd></div>}
        {diagnostic.provider_error_code === undefined ? null : <div><dt>Provider 错误码</dt><dd><code>{diagnostic.provider_error_code}</code></dd></div>}
        {diagnostic.provider_error_type === undefined ? null : <div><dt>错误类型</dt><dd><code>{diagnostic.provider_error_type}</code></dd></div>}
        {diagnostic.provider_message === undefined ? null : <div className="model-test-diagnostic__wide"><dt>Provider 消息</dt><dd>{diagnostic.provider_message}</dd></div>}
        {diagnostic.transport_error === undefined ? null : <div className="model-test-diagnostic__wide"><dt>连接原因</dt><dd>{diagnostic.transport_error}</dd></div>}
        {diagnostic.validation_reason === undefined ? null : <div className="model-test-diagnostic__wide"><dt>校验原因</dt><dd>{modelValidationReasonLabels[diagnostic.validation_reason]}</dd></div>}
        {diagnostic.provider_request_id === undefined ? null : <div className="model-test-diagnostic__wide"><dt>请求 ID</dt><dd><code>{diagnostic.provider_request_id}</code></dd></div>}
        <div><dt>知序错误码</dt><dd><code>{apiError.errorCode}</code></dd></div>
        <div><dt>可重试</dt><dd>{apiError.retryable ? "是" : "否"}</dd></div>
      </dl>
    </div>;
  }
  if (mutation.data?.target === target) return <p className="model-test-result model-test-result--success" role="status"><Check size={15} />当前草稿连接测试通过，尚未保存或生效：{mutation.data.provider} / {mutation.data.model} / {mutation.data.apiStyle === undefined ? "Embedding" : chatAPIStyleLabel[mutation.data.apiStyle]} / <code>{mutation.data.endpointPath}</code> / {String(mutation.data.latencyMs)} ms</p>;
  return null;
};

export const ModelSettingsPanel = () => {
  const queryClient = useQueryClient();
  const query = useQuery({
    queryKey: modelSettingsQueryKey,
    queryFn: ({ signal }) => getModelSettings(signal),
    retry: false,
    refetchInterval: ({ state }) => state.data !== undefined && isRolloutInProgress(state.data.rollout.phase) ? 2_000 : false,
  });
  const hydratedRevision = useRef<number | null>(null);
  const draftDirty = useRef(false);
  const [draft, setDraft] = useState<ModelSettingsDraft | undefined>();
  const draftRef = useRef<ModelSettingsDraft | undefined>(draft);
  draftRef.current = draft;
  const requestControllers = useRef(new Set<AbortController>());
  const feedbackRef = useRef<HTMLDivElement>(null);
  const [validationError, setValidationError] = useState<string | undefined>();
  const [conflict, setConflict] = useState<string | undefined>();
  const [expandedSections, setExpandedSections] = useState<Record<ModelTestTarget, boolean>>({ chat: false, embedding: false });

  const replaceDraft = (nextDraft: ModelSettingsDraft): void => {
    draftRef.current = nextDraft;
    setDraft(nextDraft);
  };

  const clearTransientSecretValues = (): void => {
    const current = draftRef.current;
    if (current === undefined) return;
    replaceDraft({
      ...current,
      chat: { ...current.chat, secret: { ...current.chat.secret, value: "" } },
      embedding: { ...current.embedding, secret: { ...current.embedding.secret, value: "" } },
    });
  };

  async function runMutationRequest<T>(request: (signal: AbortSignal) => Promise<T>): Promise<T> {
    const controller = new AbortController();
    requestControllers.current.add(controller);
    try {
      return await request(controller.signal);
    } finally {
      requestControllers.current.delete(controller);
    }
  }

  const reloadAfterConflict = async (action: "保存" | "连接测试", error: ModelSettingsApiError): Promise<void> => {
    const conflictedDraft = draftRef.current;
    const hintedRevision = error.details?.current_revision;
    const latest = await query.refetch().catch(() => undefined);
    if (latest?.isSuccess === true && (typeof hintedRevision !== "number" || latest.data.desiredRevision >= hintedRevision)) {
      hydratedRevision.current = latest.data.desiredRevision;
      if (conflictedDraft === undefined) {
        draftDirty.current = false;
        replaceDraft(draftFromResponse(latest.data));
      } else {
        draftDirty.current = true;
        replaceDraft(preserveDraftForRevision(conflictedDraft, latest.data.desiredRevision));
      }
      setConflict(`${action}时配置已由其他操作更新到${revisionLabel(latest.data.desiredRevision)}；已保留本地非 Secret 草稿并清空 Secret 输入，请重新核对。`);
      return;
    }
    clearTransientSecretValues();
    const revisionHint = typeof hintedRevision === "number" ? `服务端当前至少为${revisionLabel(hintedRevision)}，` : "";
    setConflict(`${action}发生版本冲突；${revisionHint}权威回查失败。明文输入已清空，请刷新后重试。`);
  };

  const testMutation = useMutation({
    mutationFn: (target: ModelTestTarget) => {
      const currentDraft = draftRef.current;
      if (currentDraft === undefined) throw new Error("模型设置尚未加载。");
      return target === "chat"
        ? runMutationRequest((signal) => testModelSettings({ target, chat: chatInput(currentDraft.chat) }, signal))
        : runMutationRequest((signal) => testModelSettings({ target, embedding: embeddingInput(currentDraft.embedding) }, signal));
    },
    onError: async (error: Error) => {
      if (error instanceof ModelSettingsApiError && error.status === 409) await reloadAfterConflict("连接测试", error);
    },
  });

  useEffect(() => () => {
    draftRef.current = undefined;
    for (const controller of requestControllers.current) controller.abort();
    requestControllers.current.clear();
  }, []);

  useEffect(() => {
    if (query.data === undefined || hydratedRevision.current === query.data.desiredRevision) return;
    hydratedRevision.current = query.data.desiredRevision;
    const currentDraft = draftRef.current;
    if (draftDirty.current && currentDraft !== undefined) {
      replaceDraft(preserveDraftForRevision(currentDraft, query.data.desiredRevision));
      setConflict(`配置已由其他操作更新到${revisionLabel(query.data.desiredRevision)}；已保留本地非 Secret 草稿并清空 Secret 输入，请重新核对。`);
    } else {
      draftDirty.current = false;
      replaceDraft(draftFromResponse(query.data));
    }
    setValidationError(undefined);
  }, [query.data]);

  const saveMutation = useMutation({
    mutationFn: () => {
      const currentDraft = draftRef.current;
      if (currentDraft === undefined) throw new Error("模型设置尚未加载。");
      return runMutationRequest((signal) => updateModelSettings({ expectedRevision: currentDraft.revision, chat: chatInput(currentDraft.chat), embedding: embeddingInput(currentDraft.embedding) }, signal));
    },
    onSuccess: (response) => {
      queryClient.setQueryData(modelSettingsQueryKey, response);
      hydratedRevision.current = response.desiredRevision;
      draftDirty.current = false;
      const nextDraft = draftFromResponse(response);
      replaceDraft(nextDraft);
      setConflict(undefined);
      setValidationError(undefined);
      testMutation.reset();
    },
    onError: async (error: Error) => {
      if (!(error instanceof ModelSettingsApiError) || error.status !== 409) return;
      await reloadAfterConflict("保存", error);
    },
  });

  const visibleSaveError = saveMutation.isError && (!(saveMutation.error instanceof ModelSettingsApiError) || saveMutation.error.status !== 409);

  useEffect(() => {
    if (validationError !== undefined || conflict !== undefined || visibleSaveError) feedbackRef.current?.focus();
  }, [conflict, validationError, visibleSaveError]);

  const resetFeedback = (): void => {
    setValidationError(undefined);
    setConflict(undefined);
    saveMutation.reset();
    testMutation.reset();
  };

  const changeDraft = (updater: (current: ModelSettingsDraft) => ModelSettingsDraft): void => {
    resetFeedback();
    const current = draftRef.current;
    if (current !== undefined) {
      draftDirty.current = true;
      replaceDraft(updater(current));
    }
  };

  if (query.isError) return <Card className="model-settings-panel"><ErrorState title="模型设置不可用" description={query.error.message} onRetry={() => void query.refetch()} /></Card>;
  if (query.isPending || draft === undefined) return <Card className="model-settings-panel"><div className="model-settings-loading" role="status"><LoaderCircle className="is-spinning" size={18} /><span>正在读取模型设置…</span></div></Card>;

  const settings = query.data;
  const rolloutLocked = isRolloutInProgress(settings.rollout.phase);
  const rolloutFailed = settings.rollout.phase === "failed";
  const actionPending = saveMutation.isPending || testMutation.isPending;
  const controlsDisabled = rolloutLocked || actionPending || query.isFetching;
  const chatCanKeep = settings.desiredSettings.chat.apiKeyConfigured && sameSecretTarget(draft.chat.provider, draft.chat.baseUrl, settings.desiredSettings.chat.provider, settings.desiredSettings.chat.baseUrl);
  const embeddingCanKeep = settings.desiredSettings.embedding.apiKeyConfigured && sameSecretTarget(draft.embedding.provider, draft.embedding.baseUrl, settings.desiredSettings.embedding.provider, settings.desiredSettings.embedding.baseUrl);

  const runTest = (target: ModelTestTarget): void => {
    const error = validateDraft(draft, settings, target);
    setValidationError(error);
    setConflict(undefined);
    testMutation.reset();
    if (error !== undefined) return;
    testMutation.mutate(target);
  };

  const submit = (event: SyntheticEvent<HTMLFormElement>): void => {
    event.preventDefault();
    const error = validateDraft(draft, settings);
    setValidationError(error);
    setConflict(undefined);
    if (error !== undefined) return;
    saveMutation.mutate();
  };

  return <Card className="model-settings-panel">
    <CardHeader title="模型与检索" description="待应用配置与当前生效配置分开显示。" action={<Button type="button" variant="ghost" size="sm" aria-label="刷新模型设置" onClick={() => void query.refetch()} disabled={query.isFetching || actionPending}><RefreshCw className={query.isFetching ? "is-spinning" : undefined} size={16} /></Button>} />

    <div className="model-settings-status" aria-label="模型配置版本状态">
      <div><span>待应用配置</span><strong>{revisionLabel(settings.desiredRevision)}</strong></div>
      <div><span>当前生效</span><strong>{revisionLabel(settings.activeRevision)}</strong></div>
      <div><span>API 已应用</span><strong>{revisionLabel(settings.runtime.api.appliedRevision)}</strong><Badge tone={runtimeTone(settings.runtime.api.fresh, settings.runtime.api.phase)}>{runtimePhaseLabels[settings.runtime.api.phase]}</Badge></div>
      <div><span>工作进程已应用</span><strong>{revisionLabel(settings.runtime.worker.appliedRevision)}</strong><Badge tone={runtimeTone(settings.runtime.worker.fresh, settings.runtime.worker.phase)}>{runtimePhaseLabels[settings.runtime.worker.phase]}</Badge></div>
    </div>

    {settings.restartRequired ? <div className="ui-state ui-state--warning model-settings-restart" role="status"><strong>已保存的配置尚未生效</strong><p>执行 <code>./zhixu restart</code> 后，API 与工作进程会一起应用{revisionLabel(settings.desiredRevision)}。</p></div> : <div className="model-settings-applied" role="status"><Check size={16} /><span>API 与工作进程已应用当前生效版本。</span></div>}
    {rolloutLocked ? <div className="ui-state ui-state--warning" role="status"><strong>配置切换：{rolloutPhaseLabels[settings.rollout.phase]}</strong><p>目标为{revisionLabel(settings.rollout.targetRevision ?? settings.activeRevision)}。切换结束前保存和测试已锁定。{settings.rollout.lastErrorCode ? ` ${settings.rollout.lastErrorCode}` : ""}</p></div> : null}
    {rolloutFailed ? <div className="ui-state ui-state--error" role="alert"><strong>上次配置应用失败</strong><p>当前生效版本为{revisionLabel(settings.activeRevision)}，保持不变。错误码：<code>{settings.rollout.lastErrorCode}</code>{settings.rollout.retryable ? "。可修正待应用配置并重新测试或保存。" : "。请刷新状态后再继续。"}</p></div> : null}
    {settings.capabilities.chat === "unavailable" || settings.capabilities.embedding === "unavailable" ? <div className="ui-state ui-state--error" role="alert"><strong>部分模型能力不可用</strong><p>检查已保存密钥、提供方和当前生效版本，再重新测试或应用配置。</p></div> : null}

    <form className="model-settings-form" onSubmit={submit} noValidate>
      <section className="model-settings-section" aria-labelledby="chat-settings-title">
        <header><h3 id="chat-settings-title">对话模型</h3><div className="model-settings-section__actions"><Badge tone={capabilityTone(settings.capabilities.chat)}>{capabilityLabel[settings.capabilities.chat]}</Badge><Button type="button" variant="ghost" size="sm" className="model-settings-toggle" aria-label={`${expandedSections.chat ? "收起" : "配置"}对话模型`} aria-expanded={expandedSections.chat} aria-controls="chat-settings-content" onClick={() => setExpandedSections((current) => ({ ...current, chat: !current.chat }))}>{expandedSections.chat ? "收起" : "配置"}<ChevronDown className={expandedSections.chat ? "is-expanded" : undefined} size={16} /></Button></div></header>
        {expandedSections.chat ? <div className="model-settings-section__body" id="chat-settings-content">
          <fieldset className="model-settings-fields" disabled={controlsDisabled}>
            <legend><span>待应用</span>待应用配置</legend>
            <label>提供方<select aria-label="对话模型提供方" value={draft.chat.provider} onChange={(event) => changeDraft((current) => { const provider = chatProviderValue(event.target.value); return { ...current, chat: { ...current.chat, provider, ...(provider === "disabled" ? { baseUrl: "", model: "", modelVersion: "", secret: { action: "clear", value: "" } } : {}) } }; })}><option value="disabled">已关闭</option><option value="openai-compatible">OpenAI-compatible</option></select></label>
            <label>调用接口<select aria-label="对话模型调用接口" value={draft.chat.apiStyle} disabled={draft.chat.provider === "disabled" || controlsDisabled} onChange={(event) => changeDraft((current) => ({ ...current, chat: { ...current.chat, apiStyle: chatAPIStyleValue(event.target.value) } }))}><option value="chat_completions">Chat Completions</option><option value="responses">Responses API</option></select></label>
            <label className="model-settings-field--wide">基础地址（Base URL）<input type="url" maxLength={2048} aria-label="对话模型基础地址（Base URL）" value={draft.chat.baseUrl} disabled={draft.chat.provider === "disabled" || controlsDisabled} placeholder="https://api.example.com/v1" onChange={(event) => changeDraft((current) => ({ ...current, chat: { ...current.chat, baseUrl: event.target.value } }))} /></label>
            <label>模型<input maxLength={128} aria-label="对话模型名称" value={draft.chat.model} disabled={draft.chat.provider === "disabled" || controlsDisabled} onChange={(event) => changeDraft((current) => ({ ...current, chat: { ...current.chat, model: event.target.value } }))} /></label>
            <label>模型版本<input maxLength={64} aria-label="对话模型版本" value={draft.chat.modelVersion} disabled={draft.chat.provider === "disabled" || controlsDisabled} onChange={(event) => changeDraft((current) => ({ ...current, chat: { ...current.chat, modelVersion: event.target.value } }))} /></label>
            {draft.chat.provider === "openai-compatible" ? <SecretControl id="chat-api-key" label="对话 API Key" secret={draft.chat.secret} configured={settings.desiredSettings.chat.apiKeyConfigured} canKeep={chatCanKeep} disabled={controlsDisabled} onChange={(secret) => changeDraft((current) => ({ ...current, chat: { ...current.chat, secret } }))} /> : <p className="model-settings-disabled model-settings-field--wide">对话模型已关闭。{settings.desiredSettings.chat.apiKeyConfigured ? " 保存后会清除已保存的对话 API Key。" : ""}</p>}
            <div className="model-settings-actions model-settings-field--wide"><Button type="button" variant="secondary" size="sm" disabled={controlsDisabled || draft.chat.provider === "disabled"} onClick={() => runTest("chat")}><TestTube2 size={15} />{testMutation.isPending && testMutation.variables === "chat" ? "测试中…" : "测试对话连接"}</Button><TestFeedback target="chat" mutation={testMutation} /></div>
          </fieldset>
          <ActiveSummary kind="chat" summary={settings.activeSettings.chat} differs={!settingsEqual(settings.desiredSettings.chat, settings.activeSettings.chat)} />
        </div> : null}
      </section>

      <section className="model-settings-section" aria-labelledby="embedding-settings-title">
        <header><h3 id="embedding-settings-title">向量模型</h3><div className="model-settings-section__actions"><Badge tone={capabilityTone(settings.capabilities.embedding)}>{capabilityLabel[settings.capabilities.embedding]}</Badge><Button type="button" variant="ghost" size="sm" className="model-settings-toggle" aria-label={`${expandedSections.embedding ? "收起" : "配置"}向量模型`} aria-expanded={expandedSections.embedding} aria-controls="embedding-settings-content" onClick={() => setExpandedSections((current) => ({ ...current, embedding: !current.embedding }))}>{expandedSections.embedding ? "收起" : "配置"}<ChevronDown className={expandedSections.embedding ? "is-expanded" : undefined} size={16} /></Button></div></header>
        {expandedSections.embedding ? <div className="model-settings-section__body" id="embedding-settings-content">
          <fieldset className="model-settings-fields" disabled={controlsDisabled}>
            <legend><span>待应用</span>待应用配置</legend>
            <label>提供方<select aria-label="向量模型提供方" value={draft.embedding.provider} onChange={(event) => changeDraft((current) => { const provider = embeddingProviderValue(event.target.value); return { ...current, embedding: { ...current.embedding, provider, ...(provider === "disabled" ? { baseUrl: "", model: "", dimensions: "0", secret: { action: "clear", value: "" } } : provider === "ollama" ? { baseUrl: modelSettingsOllamaRelayUrl, secret: { action: "clear", value: "" } } : {}) } }; })}><option value="disabled">已关闭</option><option value="openai-compatible">OpenAI-compatible</option><option value="ollama">Ollama</option></select></label>
            <label className="model-settings-field--wide">基础地址（Base URL）<input type="url" maxLength={2048} aria-label="向量模型基础地址（Base URL）" value={draft.embedding.baseUrl} disabled={draft.embedding.provider !== "openai-compatible" || controlsDisabled} placeholder={draft.embedding.provider === "ollama" ? modelSettingsOllamaRelayUrl : "https://api.example.com/v1"} onChange={(event) => changeDraft((current) => ({ ...current, embedding: { ...current.embedding, baseUrl: event.target.value } }))} /></label>
            <label>模型<input maxLength={128} aria-label="向量模型名称" value={draft.embedding.model} disabled={draft.embedding.provider === "disabled" || controlsDisabled} onChange={(event) => changeDraft((current) => ({ ...current, embedding: { ...current.embedding, model: event.target.value } }))} /></label>
            <label>维度<input type="number" inputMode="numeric" min="1" max="16000" aria-label="向量维度" value={draft.embedding.dimensions} disabled={draft.embedding.provider === "disabled" || controlsDisabled} onChange={(event) => changeDraft((current) => ({ ...current, embedding: { ...current.embedding, dimensions: event.target.value } }))} /></label>
            <label>归一化<select aria-label="向量归一化" value={draft.embedding.normalization} disabled={draft.embedding.provider === "disabled" || controlsDisabled} onChange={(event) => changeDraft((current) => ({ ...current, embedding: { ...current.embedding, normalization: normalizationValue(event.target.value) } }))}><option value="l2">L2</option><option value="none">无</option></select></label>
            <label>距离度量<select aria-label="向量距离度量" value={draft.embedding.distanceMetric} disabled={draft.embedding.provider === "disabled" || controlsDisabled} onChange={(event) => changeDraft((current) => ({ ...current, embedding: { ...current.embedding, distanceMetric: distanceMetricValue(event.target.value) } }))}><option value="cosine">余弦（Cosine）</option><option value="inner_product">内积（Inner product）</option><option value="euclidean">欧氏距离（Euclidean）</option></select></label>
            {draft.embedding.provider === "openai-compatible" ? <SecretControl id="embedding-api-key" label="向量 API Key" secret={draft.embedding.secret} configured={settings.desiredSettings.embedding.apiKeyConfigured} canKeep={embeddingCanKeep} disabled={controlsDisabled} onChange={(secret) => changeDraft((current) => ({ ...current, embedding: { ...current.embedding, secret } }))} /> : <p className="model-settings-disabled model-settings-field--wide">{draft.embedding.provider === "ollama" ? "Ollama 不使用 API Key。" : "向量模型已关闭；关键词检索保持可用。"}{settings.desiredSettings.embedding.apiKeyConfigured ? " 保存后会清除已保存的向量 API Key。" : ""}</p>}
            <div className="model-settings-actions model-settings-field--wide"><Button type="button" variant="secondary" size="sm" disabled={controlsDisabled || draft.embedding.provider === "disabled"} onClick={() => runTest("embedding")}><TestTube2 size={15} />{testMutation.isPending && testMutation.variables === "embedding" ? "测试中…" : "测试向量连接"}</Button><TestFeedback target="embedding" mutation={testMutation} /></div>
          </fieldset>
          <ActiveSummary kind="embedding" summary={settings.activeSettings.embedding} differs={!settingsEqual(settings.desiredSettings.embedding, settings.activeSettings.embedding)} />
        </div> : null}
      </section>

      <div className="model-settings-feedback" ref={feedbackRef} tabIndex={-1} aria-label="模型设置反馈" aria-live="polite">
        {validationError ? <p className="model-settings-error" role="alert">{validationError}</p> : null}
        {conflict ? <p className="model-settings-warning" role="alert"><AlertTriangle size={15} />{conflict}</p> : null}
        {visibleSaveError ? <p className="model-settings-error" role="alert">{saveMutation.error.message}</p> : null}
        {saveMutation.isSuccess ? <p className="model-test-result model-test-result--success" role="status"><Check size={15} />待应用版本 {String(saveMutation.data.desiredRevision)} 已保存{saveMutation.data.restartRequired ? "，等待 restart 应用" : "并已生效"}。</p> : null}
      </div>
      <div className="model-settings-save"><Button type="submit" disabled={controlsDisabled}><Save size={16} />{saveMutation.isPending ? "正在保存…" : "保存模型设置"}</Button></div>
    </form>
  </Card>;
};
