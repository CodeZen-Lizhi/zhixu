import { useMutation, useQuery, useQueryClient, type UseMutationResult } from "@tanstack/react-query";
import { AlertTriangle, Check, KeyRound, LoaderCircle, RefreshCw, Save, TestTube2 } from "lucide-react";
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
  type ChatModelSettingsInput,
  type ChatModelSettingsSummary,
  type EmbeddingDistanceMetric,
  type EmbeddingModelProvider,
  type EmbeddingModelSettingsInput,
  type EmbeddingModelSettingsSummary,
  type EmbeddingNormalization,
  type ModelCapability,
  type ModelRolloutPhase,
  type ModelSecretInput,
  type ModelSettingsResponse,
  type ModelSettingsTestResult,
  type ModelTestTarget,
} from "../../api/model-settings";
import { Badge, Button, Card, CardHeader, ErrorState } from "../../shared/ui";

import "./model-settings.css";

const modelSettingsQueryKey = ["settings", "models"] as const;

type SecretAction = ModelSecretInput["action"];

interface SecretDraft {
  action: SecretAction;
  value: string;
}

interface ChatDraft {
  provider: ChatModelProvider;
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
  disabled: "Disabled",
  "openai-compatible": "OpenAI-compatible",
  ollama: "Ollama",
};

const chatProviderValue = (value: string): ChatModelProvider => {
  if (value === "disabled" || value === "openai-compatible") return value;
  throw new TypeError("unknown Chat provider");
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

const isRolloutInProgress = (phase: ModelRolloutPhase): boolean => phase !== "idle" && phase !== "failed";

const initialSecret = (configured: boolean): SecretDraft => ({ action: configured ? "keep" : "clear", value: "" });

const draftFromResponse = (settings: ModelSettingsResponse): ModelSettingsDraft => ({
  revision: settings.desiredRevision,
  chat: {
    provider: settings.desiredSettings.chat.provider,
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
  ? { provider: "disabled", baseUrl: "", model: "", modelVersion: "", adapterVersion: draft.adapterVersion, apiKey: { action: "clear" } }
  : {
      provider: draft.provider,
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
      return "Chat 启用后必须填写 Base URL、模型和模型版本。";
    }
    if (draft.chat.provider !== "disabled" && canonicalizeChatModelBaseUrl(draft.chat.provider, draft.chat.baseUrl.trim()) === undefined) return `Chat Base URL 必须使用 HTTPS；本地 Ollama 仅允许固定 Relay ${modelSettingsOllamaRelayUrl}。`;
    const canKeep = settings.desiredSettings.chat.apiKeyConfigured && sameSecretTarget(draft.chat.provider, draft.chat.baseUrl, settings.desiredSettings.chat.provider, settings.desiredSettings.chat.baseUrl);
    if (draft.chat.provider !== "disabled" && draft.chat.secret.action === "keep" && !canKeep) return "Chat Provider 或 Base URL 已改变，请替换或清除 API Key。";
    if (draft.chat.provider !== "disabled" && draft.chat.secret.action === "replace" && draft.chat.secret.value.trim() === "") return "请输入新的 Chat API Key。";
  }
  if (target === undefined || target === "embedding") {
    const dimensions = Number(draft.embedding.dimensions);
    if (draft.embedding.provider !== "disabled" && (draft.embedding.baseUrl.trim() === "" || draft.embedding.model.trim() === "")) return "Embedding 启用后必须填写 Base URL 和模型。";
    if (draft.embedding.provider !== "disabled" && canonicalizeEmbeddingModelBaseUrl(draft.embedding.provider, draft.embedding.baseUrl.trim()) === undefined) return draft.embedding.provider === "ollama"
      ? `Ollama Embedding 必须使用固定 Relay ${modelSettingsOllamaRelayUrl}。`
      : "OpenAI-compatible Embedding Base URL 必须使用无凭据、查询参数和片段的 HTTPS 地址。";
    if (draft.embedding.provider !== "disabled" && (!Number.isSafeInteger(dimensions) || dimensions < 1 || dimensions > 16_000)) return "Embedding dimensions 必须是 1 到 16000 的整数。";
    const canKeep = settings.desiredSettings.embedding.apiKeyConfigured && sameSecretTarget(draft.embedding.provider, draft.embedding.baseUrl, settings.desiredSettings.embedding.provider, settings.desiredSettings.embedding.baseUrl);
    if (draft.embedding.provider === "openai-compatible" && draft.embedding.secret.action === "keep" && !canKeep) return "Embedding Provider 或 Base URL 已改变，请替换 API Key。";
    if (draft.embedding.provider === "openai-compatible" && draft.embedding.secret.action === "clear") return "OpenAI-compatible Embedding 需要 API Key；请选择保留或替换。";
    if (draft.embedding.provider === "openai-compatible" && draft.embedding.secret.action === "replace" && draft.embedding.secret.value.trim() === "") return "请输入新的 Embedding API Key。";
  }
  return undefined;
};

const settingsEqual = (left: ChatModelSettingsSummary | EmbeddingModelSettingsSummary, right: ChatModelSettingsSummary | EmbeddingModelSettingsSummary): boolean =>
  JSON.stringify(left) === JSON.stringify(right);

const SummaryValue = ({ label, children, mono = false }: { label: string; children: ReactNode; mono?: boolean }) => <div><dt>{label}</dt><dd className={mono ? "mono" : undefined}>{children}</dd></div>;

const ActiveSummary = ({ kind, summary, differs }: { kind: ModelTestTarget; summary: ChatModelSettingsSummary | EmbeddingModelSettingsSummary; differs: boolean }) => <aside className="model-settings-active" aria-label={`${kind === "chat" ? "Chat" : "Embedding"} 当前生效配置`}>
  <div className="model-settings-active__heading"><div><span>Active</span><strong>当前生效</strong></div><Badge tone={differs ? "warning" : "success"}>{differs ? "与待应用配置不同" : "与待应用配置一致"}</Badge></div>
  <dl>
    <SummaryValue label="Provider">{providerLabel[summary.provider]}</SummaryValue>
    <SummaryValue label="Base URL" mono>{summary.baseUrl || "未设置"}</SummaryValue>
    <SummaryValue label="Model" mono>{summary.model || "未设置"}</SummaryValue>
    {kind === "chat"
      ? <><SummaryValue label="Model version" mono>{(summary as ChatModelSettingsSummary).modelVersion || "未设置"}</SummaryValue><SummaryValue label="Adapter" mono>{(summary as ChatModelSettingsSummary).adapterVersion}</SummaryValue></>
      : <><SummaryValue label="Dimensions">{String((summary as EmbeddingModelSettingsSummary).dimensions)}</SummaryValue><SummaryValue label="Vector contract">{(summary as EmbeddingModelSettingsSummary).normalization} / {(summary as EmbeddingModelSettingsSummary).distanceMetric}</SummaryValue></>}
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
  if (mutation.isError && mutation.variables === target) return <p className="model-settings-error" role="alert">{mutation.error.message}</p>;
  if (mutation.data?.target === target) return <p className="model-test-result model-test-result--success" role="status"><Check size={15} />当前草稿连接测试通过，尚未保存或生效：{mutation.data.provider} / {mutation.data.model}</p>;
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
      setConflict(`${action}时配置已由其他操作更新到 Revision ${String(latest.data.desiredRevision)}；已保留本地非 Secret 草稿并清空 Secret 输入，请重新核对。`);
      return;
    }
    clearTransientSecretValues();
    const revisionHint = typeof hintedRevision === "number" ? `服务端当前至少为 Revision ${String(hintedRevision)}，` : "";
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
      setConflict(`配置已由其他操作更新到 Revision ${String(query.data.desiredRevision)}；已保留本地非 Secret 草稿并清空 Secret 输入，请重新核对。`);
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
    <CardHeader eyebrow="Model runtime" title="Chat 与 Embedding" description="待应用配置与当前生效配置分开显示。" action={<Button type="button" variant="ghost" size="sm" aria-label="刷新模型设置" onClick={() => void query.refetch()} disabled={query.isFetching || actionPending}><RefreshCw className={query.isFetching ? "is-spinning" : undefined} size={16} /></Button>} />

    <div className="model-settings-status" aria-label="模型配置版本状态">
      <div><span>Desired</span><strong>Revision {String(settings.desiredRevision)}</strong></div>
      <div><span>Active</span><strong>Revision {String(settings.activeRevision)}</strong></div>
      <div><span>API applied</span><strong>Revision {String(settings.runtime.api.appliedRevision)}</strong><Badge tone={runtimeTone(settings.runtime.api.fresh, settings.runtime.api.phase)}>{settings.runtime.api.phase}</Badge></div>
      <div><span>Worker applied</span><strong>Revision {String(settings.runtime.worker.appliedRevision)}</strong><Badge tone={runtimeTone(settings.runtime.worker.fresh, settings.runtime.worker.phase)}>{settings.runtime.worker.phase}</Badge></div>
    </div>

    {settings.restartRequired ? <div className="ui-state ui-state--warning model-settings-restart" role="status"><strong>已保存的配置尚未生效</strong><p>执行 <code>./zhixu restart</code> 后，API 与 Worker 会一起应用 Revision {String(settings.desiredRevision)}。</p></div> : <div className="model-settings-applied" role="status"><Check size={16} /><span>API 与 Worker 已应用当前 Active Revision。</span></div>}
    {rolloutLocked ? <div className="ui-state ui-state--warning" role="status"><strong>配置切换：{settings.rollout.phase}</strong><p>Target Revision {String(settings.rollout.targetRevision)}。切换结束前保存和测试已锁定。{settings.rollout.lastErrorCode ? ` ${settings.rollout.lastErrorCode}` : ""}</p></div> : null}
    {rolloutFailed ? <div className="ui-state ui-state--error" role="alert"><strong>上次配置应用失败</strong><p>Active Revision {String(settings.activeRevision)} 保持不变。错误码：<code>{settings.rollout.lastErrorCode}</code>{settings.rollout.retryable ? "。可修正待应用配置并重新测试或保存。" : "。请刷新状态后再继续。"}</p></div> : null}
    {settings.capabilities.chat === "unavailable" || settings.capabilities.embedding === "unavailable" ? <div className="ui-state ui-state--error" role="alert"><strong>部分模型能力不可用</strong><p>检查已保存密钥、Provider 和当前 Active Revision，再重新测试或应用配置。</p></div> : null}

    <form className="model-settings-form" onSubmit={submit} noValidate>
      <section className="model-settings-section" aria-labelledby="chat-settings-title">
        <header><div><span>Chat</span><h3 id="chat-settings-title">对话模型</h3></div><Badge tone={capabilityTone(settings.capabilities.chat)}>{capabilityLabel[settings.capabilities.chat]}</Badge></header>
        <div className="model-settings-section__body">
          <fieldset className="model-settings-fields" disabled={controlsDisabled}>
            <legend><span>Desired</span>待应用配置</legend>
            <label>Provider<select aria-label="Chat Provider" value={draft.chat.provider} onChange={(event) => changeDraft((current) => { const provider = chatProviderValue(event.target.value); return { ...current, chat: { ...current.chat, provider, ...(provider === "disabled" ? { baseUrl: "", model: "", modelVersion: "", secret: { action: "clear", value: "" } } : {}) } }; })}><option value="disabled">Disabled</option><option value="openai-compatible">OpenAI-compatible</option></select></label>
            <label className="model-settings-field--wide">Base URL<input type="url" maxLength={2048} aria-label="Chat Base URL" value={draft.chat.baseUrl} disabled={draft.chat.provider === "disabled" || controlsDisabled} placeholder="https://api.example.com/v1" onChange={(event) => changeDraft((current) => ({ ...current, chat: { ...current.chat, baseUrl: event.target.value } }))} /></label>
            <label>Model<input maxLength={128} aria-label="Chat Model" value={draft.chat.model} disabled={draft.chat.provider === "disabled" || controlsDisabled} onChange={(event) => changeDraft((current) => ({ ...current, chat: { ...current.chat, model: event.target.value } }))} /></label>
            <label>Model version<input maxLength={64} aria-label="Chat Model Version" value={draft.chat.modelVersion} disabled={draft.chat.provider === "disabled" || controlsDisabled} onChange={(event) => changeDraft((current) => ({ ...current, chat: { ...current.chat, modelVersion: event.target.value } }))} /></label>
            {draft.chat.provider === "openai-compatible" ? <SecretControl id="chat-api-key" label="Chat API Key" secret={draft.chat.secret} configured={settings.desiredSettings.chat.apiKeyConfigured} canKeep={chatCanKeep} disabled={controlsDisabled} onChange={(secret) => changeDraft((current) => ({ ...current, chat: { ...current.chat, secret } }))} /> : <p className="model-settings-disabled model-settings-field--wide">Chat 模型已关闭。{settings.desiredSettings.chat.apiKeyConfigured ? " 保存后会清除已保存的 Chat API Key。" : ""}</p>}
            <div className="model-settings-actions model-settings-field--wide"><Button type="button" variant="secondary" size="sm" disabled={controlsDisabled || draft.chat.provider === "disabled"} onClick={() => runTest("chat")}><TestTube2 size={15} />{testMutation.isPending && testMutation.variables === "chat" ? "测试中…" : "测试 Chat 连接"}</Button><TestFeedback target="chat" mutation={testMutation} /></div>
          </fieldset>
          <ActiveSummary kind="chat" summary={settings.activeSettings.chat} differs={!settingsEqual(settings.desiredSettings.chat, settings.activeSettings.chat)} />
        </div>
      </section>

      <section className="model-settings-section" aria-labelledby="embedding-settings-title">
        <header><div><span>Embedding</span><h3 id="embedding-settings-title">向量模型</h3></div><Badge tone={capabilityTone(settings.capabilities.embedding)}>{capabilityLabel[settings.capabilities.embedding]}</Badge></header>
        <div className="model-settings-section__body">
          <fieldset className="model-settings-fields" disabled={controlsDisabled}>
            <legend><span>Desired</span>待应用配置</legend>
            <label>Provider<select aria-label="Embedding Provider" value={draft.embedding.provider} onChange={(event) => changeDraft((current) => { const provider = embeddingProviderValue(event.target.value); return { ...current, embedding: { ...current.embedding, provider, ...(provider === "disabled" ? { baseUrl: "", model: "", dimensions: "0", secret: { action: "clear", value: "" } } : provider === "ollama" ? { baseUrl: modelSettingsOllamaRelayUrl, secret: { action: "clear", value: "" } } : {}) } }; })}><option value="disabled">Disabled</option><option value="openai-compatible">OpenAI-compatible</option><option value="ollama">Ollama</option></select></label>
            <label className="model-settings-field--wide">Base URL<input type="url" maxLength={2048} aria-label="Embedding Base URL" value={draft.embedding.baseUrl} disabled={draft.embedding.provider !== "openai-compatible" || controlsDisabled} placeholder={draft.embedding.provider === "ollama" ? modelSettingsOllamaRelayUrl : "https://api.example.com/v1"} onChange={(event) => changeDraft((current) => ({ ...current, embedding: { ...current.embedding, baseUrl: event.target.value } }))} /></label>
            <label>Model<input maxLength={128} aria-label="Embedding Model" value={draft.embedding.model} disabled={draft.embedding.provider === "disabled" || controlsDisabled} onChange={(event) => changeDraft((current) => ({ ...current, embedding: { ...current.embedding, model: event.target.value } }))} /></label>
            <label>Dimensions<input type="number" inputMode="numeric" min="1" max="16000" aria-label="Embedding Dimensions" value={draft.embedding.dimensions} disabled={draft.embedding.provider === "disabled" || controlsDisabled} onChange={(event) => changeDraft((current) => ({ ...current, embedding: { ...current.embedding, dimensions: event.target.value } }))} /></label>
            <label>Normalization<select aria-label="Embedding Normalization" value={draft.embedding.normalization} disabled={draft.embedding.provider === "disabled" || controlsDisabled} onChange={(event) => changeDraft((current) => ({ ...current, embedding: { ...current.embedding, normalization: normalizationValue(event.target.value) } }))}><option value="l2">L2</option><option value="none">None</option></select></label>
            <label>Distance<select aria-label="Embedding Distance" value={draft.embedding.distanceMetric} disabled={draft.embedding.provider === "disabled" || controlsDisabled} onChange={(event) => changeDraft((current) => ({ ...current, embedding: { ...current.embedding, distanceMetric: distanceMetricValue(event.target.value) } }))}><option value="cosine">Cosine</option><option value="inner_product">Inner product</option><option value="euclidean">Euclidean</option></select></label>
            {draft.embedding.provider === "openai-compatible" ? <SecretControl id="embedding-api-key" label="Embedding API Key" secret={draft.embedding.secret} configured={settings.desiredSettings.embedding.apiKeyConfigured} canKeep={embeddingCanKeep} disabled={controlsDisabled} onChange={(secret) => changeDraft((current) => ({ ...current, embedding: { ...current.embedding, secret } }))} /> : <p className="model-settings-disabled model-settings-field--wide">{draft.embedding.provider === "ollama" ? "Ollama 不使用 API Key。" : "Embedding 模型已关闭；Keyword 检索保持可用。"}{settings.desiredSettings.embedding.apiKeyConfigured ? " 保存后会清除已保存的 Embedding API Key。" : ""}</p>}
            <div className="model-settings-actions model-settings-field--wide"><Button type="button" variant="secondary" size="sm" disabled={controlsDisabled || draft.embedding.provider === "disabled"} onClick={() => runTest("embedding")}><TestTube2 size={15} />{testMutation.isPending && testMutation.variables === "embedding" ? "测试中…" : "测试 Embedding 连接"}</Button><TestFeedback target="embedding" mutation={testMutation} /></div>
          </fieldset>
          <ActiveSummary kind="embedding" summary={settings.activeSettings.embedding} differs={!settingsEqual(settings.desiredSettings.embedding, settings.activeSettings.embedding)} />
        </div>
      </section>

      <div className="model-settings-feedback" ref={feedbackRef} tabIndex={-1} aria-label="模型设置反馈" aria-live="polite">
        {validationError ? <p className="model-settings-error" role="alert">{validationError}</p> : null}
        {conflict ? <p className="model-settings-warning" role="alert"><AlertTriangle size={15} />{conflict}</p> : null}
        {visibleSaveError ? <p className="model-settings-error" role="alert">{saveMutation.error.message}</p> : null}
        {saveMutation.isSuccess ? <p className="model-test-result model-test-result--success" role="status"><Check size={15} />Desired Revision {String(saveMutation.data.desiredRevision)} 已保存{saveMutation.data.restartRequired ? "，等待 restart 应用" : "并已生效"}。</p> : null}
      </div>
      <div className="model-settings-save"><Button type="submit" disabled={controlsDisabled}><Save size={16} />{saveMutation.isPending ? "正在保存…" : "保存模型设置"}</Button></div>
    </form>
  </Card>;
};
