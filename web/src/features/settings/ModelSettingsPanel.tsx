import { useMutation, useQuery, useQueryClient, type UseMutationResult } from "@tanstack/react-query";
import { AlertTriangle, Check, ChevronDown, KeyRound, LoaderCircle, Play, RefreshCw, Save, TestTube2 } from "lucide-react";
import { useEffect, useRef, useState, type ReactNode, type SyntheticEvent } from "react";

import {
  ModelSettingsApiError,
  canonicalizeChatModelBaseUrl,
  canonicalizeEmbeddingModelBaseUrl,
  canonicalizeModelBaseUrl,
  getModelSettings,
  startModelSettingsActivation,
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
  type ModelParticipantPhase,
  type ModelLocalOperationPhase,
  type ModelLocalRuntimePhase,
  type ModelRuntimePhase,
  type ModelRolloutPhase,
  type ModelSecretInput,
  type ModelSettingsResponse,
  type ModelSettingsTestResult,
  type ModelTestStage,
  type ModelTestTarget,
  type ModelTestValidationReason,
} from "../../api/model-settings";
import { isAbortError } from "../../shared/codec";
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
  legacyLocal: boolean;
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

const localRuntimePhaseLabel: Record<ModelLocalRuntimePhase, string> = {
  stopped: "未运行",
  starting: "正在启动",
  pulling: "正在准备模型",
  checking: "正在检查",
  ready: "已就绪",
  stopping: "正在停止",
  failed: "暂不可用",
};

const localRuntimeTransitional = new Set<ModelLocalRuntimePhase>(["starting", "pulling", "checking", "stopping"]);
const localOperationTransitional = new Set<ModelLocalOperationPhase>(["queued", "starting", "checking", "pulling", "verifying", "probing"]);

const shouldPollModelSettings = (settings: ModelSettingsResponse): boolean =>
  isRolloutInProgress(settings.rollout.phase)
  || localRuntimeTransitional.has(settings.localRuntime.phase)
  || (settings.localRuntime.operationPhase !== null && localOperationTransitional.has(settings.localRuntime.operationPhase));

const providerLabel: Record<EmbeddingModelProvider, string> = {
  disabled: "已关闭",
  "openai-compatible": "OpenAI-compatible",
  ollama: "本地 Ollama",
};

const chatAPIStyleLabel: Record<ChatAPIStyle, string> = {
  chat_completions: "Chat Completions",
  responses: "Responses API",
};

const chatProviderValue = (value: string): ChatModelProvider => {
  if (value === "disabled" || value === "openai-compatible" || value === "ollama") return value;
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

const changeChatProvider = (draft: ChatDraft, provider: ChatModelProvider): ChatDraft => {
  switch (provider) {
    case "disabled":
      return { ...draft, provider, legacyLocal: false, baseUrl: "", model: "", modelVersion: "", secret: { action: "clear", value: "" } };
    case "ollama":
      return { ...draft, provider, legacyLocal: false, apiStyle: "chat_completions", baseUrl: modelSettingsOllamaRelayUrl, secret: { action: "clear", value: "" } };
    case "openai-compatible":
      return draft.provider === "ollama" || draft.legacyLocal
        ? { ...draft, provider, legacyLocal: false, baseUrl: "", secret: { action: "clear", value: "" } }
        : { ...draft, provider, legacyLocal: false };
  }
};

const runtimeTone = (fresh: boolean, phase: string): "success" | "warning" | "danger" =>
  fresh && phase === "active" ? "success" : fresh ? "warning" : "danger";

const runtimePhaseLabels: Record<ModelRuntimePhase, string> = {
  active: "已生效",
  unavailable: "不可用",
};

const rolloutPhaseLabels: Record<ModelRolloutPhase, string> = {
  idle: "空闲",
  preparing: "准备中",
  arming: "切换准备中",
  activating: "生效中",
  failed: "失败",
};

const participantPhaseLabels: Record<ModelParticipantPhase, string> = {
  preparing: "正在构造",
  prepared: "已准备",
  armed: "等待切换",
  activated: "已应用",
  failed: "准备失败",
  aborted: "已终止",
  retired: "已退役",
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

const runtimeReadyForRevision = (runtime: ModelSettingsResponse["runtime"]["api"], revision: number): boolean =>
  runtime.fresh && runtime.phase === "active" && runtime.appliedRevision === revision;

const snapshotHasTarget = (settings: ModelSettingsResponse, targetRevision: number): boolean =>
  (settings.rollout.phase !== "idle" && settings.rollout.targetRevision === targetRevision)
  || (settings.activeRevision === targetRevision
    && runtimeReadyForRevision(settings.runtime.api, targetRevision)
    && runtimeReadyForRevision(settings.runtime.worker, targetRevision));

const initialSecret = (configured: boolean): SecretDraft => ({ action: configured ? "keep" : "clear", value: "" });

const isLegacyLocalChat = (summary: ChatModelSettingsSummary): boolean =>
  summary.provider === "openai-compatible" && summary.baseUrl === modelSettingsOllamaRelayUrl;

const draftFromResponse = (settings: ModelSettingsResponse): ModelSettingsDraft => ({
  revision: settings.desiredRevision,
  chat: {
    provider: settings.desiredSettings.chat.provider,
    legacyLocal: isLegacyLocalChat(settings.desiredSettings.chat),
    apiStyle: settings.desiredSettings.chat.apiStyle,
    baseUrl: settings.desiredSettings.chat.baseUrl,
    model: settings.desiredSettings.chat.model,
    modelVersion: settings.desiredSettings.chat.modelVersion,
    adapterVersion: settings.desiredSettings.chat.adapterVersion,
    secret: isLegacyLocalChat(settings.desiredSettings.chat) ? { action: "clear", value: "" } : initialSecret(settings.desiredSettings.chat.apiKeyConfigured),
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
  provider: ChatModelProvider,
  baseUrl: string,
  savedProvider: ChatModelProvider,
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
      provider: draft.provider === "ollama" || draft.legacyLocal ? "ollama" : draft.provider,
      apiStyle: draft.provider === "ollama" || draft.legacyLocal ? "chat_completions" : draft.apiStyle,
      baseUrl: draft.provider === "ollama" || draft.legacyLocal ? modelSettingsOllamaRelayUrl : draft.baseUrl.trim(),
      model: draft.model.trim(),
      modelVersion: draft.modelVersion.trim(),
      adapterVersion: draft.adapterVersion,
      apiKey: draft.provider === "ollama" || draft.legacyLocal ? { action: "clear" } : secretInput(draft.secret),
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
    const chatProvider = draft.chat.provider === "ollama" || draft.chat.legacyLocal ? "ollama" : draft.chat.provider;
    if (draft.chat.provider !== "disabled" && (draft.chat.baseUrl.trim() === "" || draft.chat.model.trim() === "" || draft.chat.modelVersion.trim() === "")) {
      return chatProvider === "ollama" ? "本地 Ollama 对话模型必须填写模型和模型版本。" : "对话模型启用后必须填写基础地址（Base URL）、模型和模型版本。";
    }
    if (draft.chat.provider !== "disabled" && canonicalizeChatModelBaseUrl(chatProvider, draft.chat.baseUrl.trim()) === undefined) return chatProvider === "ollama"
      ? "本地 Ollama 的运行位置由知序管理。"
      : "对话模型基础地址（Base URL）必须使用 HTTPS。";
    const canKeep = settings.desiredSettings.chat.apiKeyConfigured && sameSecretTarget(draft.chat.provider, draft.chat.baseUrl, settings.desiredSettings.chat.provider, settings.desiredSettings.chat.baseUrl);
    if (chatProvider === "openai-compatible" && draft.chat.secret.action === "keep" && !canKeep) return "对话模型提供方或基础地址已改变，请替换或清除 API Key。";
    if (chatProvider === "openai-compatible" && draft.chat.secret.action === "replace" && draft.chat.secret.value.trim() === "") return "请输入新的对话 API Key。";
  }
  if (target === undefined || target === "embedding") {
    const dimensions = Number(draft.embedding.dimensions);
    if (draft.embedding.provider !== "disabled" && (draft.embedding.baseUrl.trim() === "" || draft.embedding.model.trim() === "")) return "向量模型启用后必须填写基础地址（Base URL）和模型。";
    if (draft.embedding.provider !== "disabled" && canonicalizeEmbeddingModelBaseUrl(draft.embedding.provider, draft.embedding.baseUrl.trim()) === undefined) return draft.embedding.provider === "ollama"
      ? "本地 Ollama 的运行位置由知序管理。"
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

const ActiveSummary = ({ kind, summary, differs }: { kind: ModelTestTarget; summary: ChatModelSettingsSummary | EmbeddingModelSettingsSummary; differs: boolean }) => {
  const legacyLocalChat = kind === "chat" && isLegacyLocalChat(summary as ChatModelSettingsSummary);
  const managedLocalChat = kind === "chat" && (summary.provider === "ollama" || legacyLocalChat);
  return <aside className="model-settings-active" aria-label={`${kind === "chat" ? "Chat" : "Embedding"} 当前生效配置`}>
    <div className="model-settings-active__heading"><div><span>生效配置</span><strong>当前生效</strong></div><Badge tone={differs ? "warning" : "success"}>{differs ? "与待应用配置不同" : "与待应用配置一致"}</Badge></div>
    <dl>
    <SummaryValue label="提供方">{managedLocalChat ? `本地 Ollama${legacyLocalChat ? "（旧配置）" : ""}` : providerLabel[summary.provider]}</SummaryValue>
    <SummaryValue label={managedLocalChat ? "运行位置" : "基础地址（Base URL）"} mono={!managedLocalChat}>{managedLocalChat ? "本机（系统管理）" : summary.baseUrl || "未设置"}</SummaryValue>
    <SummaryValue label="模型" mono>{summary.model || "未设置"}</SummaryValue>
    {kind === "chat"
      ? <><SummaryValue label="调用接口">{chatAPIStyleLabel[(summary as ChatModelSettingsSummary).apiStyle]}</SummaryValue><SummaryValue label="模型版本" mono>{(summary as ChatModelSettingsSummary).modelVersion || "未设置"}</SummaryValue><SummaryValue label="适配器" mono>{(summary as ChatModelSettingsSummary).adapterVersion}</SummaryValue></>
      : <><SummaryValue label="维度">{String((summary as EmbeddingModelSettingsSummary).dimensions)}</SummaryValue><SummaryValue label="向量约定">{(summary as EmbeddingModelSettingsSummary).normalization} / {(summary as EmbeddingModelSettingsSummary).distanceMetric}</SummaryValue></>}
    <SummaryValue label="API Key">{managedLocalChat ? legacyLocalChat && summary.apiKeyConfigured ? "旧配置已保存，下次保存时清除" : "不使用" : summary.apiKeyConfigured ? "已安全保存" : "未配置"}</SummaryValue>
    </dl>
  </aside>;
};

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

interface ModelTestMutationVariables { target: ModelTestTarget; idempotencyKey: string }

const TestFeedback = ({ target, mutation }: { target: ModelTestTarget; mutation: UseMutationResult<ModelSettingsTestResult, Error, ModelTestMutationVariables> }) => {
	if (mutation.isPending && mutation.variables.target === target) return <p className="model-test-result" role="status"><LoaderCircle className="is-spinning" size={15} />正在从服务端测试连接…</p>;
	if (mutation.isError && mutation.variables.target === target && mutation.error instanceof ModelSettingsApiError && mutation.error.status === 409) return null;
	if (mutation.isError && mutation.variables.target === target) {
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

const ParticipantProgress = ({ role, participant }: {
  role: "API" | "工作进程";
  participant: ModelSettingsResponse["participants"]["api"];
}) => <div className="model-activation-participant">
  <div>
    <strong>{role}</strong>
    <Badge tone={!participant.present ? "neutral" : participant.phase === "failed" || !participant.fresh ? "danger" : participant.phase === "activated" ? "success" : "warning"}>
      {!participant.present || participant.phase === null ? "等待登记" : participantPhaseLabels[participant.phase]}
    </Badge>
  </div>
  <p>{participant.present
    ? `${participant.targetRevision === null ? "目标版本待确认" : revisionLabel(participant.targetRevision)}，${participant.fresh ? "状态正常" : "状态已过期"}`
    : "尚未报告候选运行时进度。"}</p>
  {participant.lastErrorCode === null ? null : <p className="model-settings-error">错误码：<code>{participant.lastErrorCode}</code>{participant.retryable ? "，可重试" : "，不可重试"}</p>}
</div>;

const ActivationProgress = ({ settings }: { settings: ModelSettingsResponse }) => {
  if (settings.rollout.phase === "idle") return null;
  const failed = settings.rollout.phase === "failed";
  const targetRevision = settings.rollout.targetRevision ?? settings.desiredRevision;
  const activeServing = runtimeReadyForRevision(settings.runtime.api, settings.activeRevision)
    && runtimeReadyForRevision(settings.runtime.worker, settings.activeRevision);
  const activeStatus = activeServing
    ? `旧的${revisionLabel(settings.activeRevision)}继续服务`
    : `${revisionLabel(settings.activeRevision)}保持为当前生效版本，但 API 或工作进程处于降级状态`;
  const phaseMessage = settings.rollout.phase === "preparing"
    ? `${activeStatus}；两端正在构造并检查完整候选运行时。`
    : settings.rollout.phase === "arming"
      ? `${activeStatus}；新的模型工作会短暂等待，进行中的工作不受影响。`
      : settings.rollout.phase === "activating"
        ? `${revisionLabel(targetRevision)}已经提交；API 与工作进程正在完成本地切换。`
        : `应用未提交；${activeStatus}。`;
  return <div className={`ui-state ${failed ? "ui-state--error" : "ui-state--warning"}`} role={failed ? "alert" : "status"}>
    <strong>{failed ? "配置应用失败" : `配置应用：${rolloutPhaseLabels[settings.rollout.phase]}`}</strong>
    <p>目标为{revisionLabel(targetRevision)}。{phaseMessage}</p>
    {settings.rollout.lastErrorCode === null ? null : <p>总体错误码：<code>{settings.rollout.lastErrorCode}</code>{settings.rollout.retryable ? "，可以重试应用。" : "，请先修正配置或运行环境。"}</p>}
    <div className="model-activation-participants" aria-label="API 与工作进程应用进度">
      <ParticipantProgress role="API" participant={settings.participants.api} />
      <ParticipantProgress role="工作进程" participant={settings.participants.worker} />
    </div>
  </div>;
};

export const ModelSettingsPanel = () => {
  const queryClient = useQueryClient();
  const query = useQuery({
    queryKey: modelSettingsQueryKey,
    queryFn: ({ signal }) => getModelSettings(signal),
    retry: false,
    refetchInterval: ({ state }) => state.data !== undefined && shouldPollModelSettings(state.data) ? 2_000 : false,
    refetchOnReconnect: "always",
    refetchOnWindowFocus: "always",
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
  const [recoveredActivationTarget, setRecoveredActivationTarget] = useState<number | undefined>();
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
		mutationFn: ({ target, idempotencyKey }: { target: ModelTestTarget; idempotencyKey: string }) => {
      const currentDraft = draftRef.current;
      if (currentDraft === undefined) throw new Error("模型设置尚未加载。");
      return target === "chat"
			? runMutationRequest((signal) => testModelSettings({ target, chat: chatInput(currentDraft.chat), idempotencyKey }, signal))
			: runMutationRequest((signal) => testModelSettings({ target, embedding: embeddingInput(currentDraft.embedding), idempotencyKey }, signal));
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

  const observedLiveActivation = useRef(false);
  useEffect(() => {
    if (query.data === undefined) return;
    if (isRolloutInProgress(query.data.rollout.phase)) {
      observedLiveActivation.current = true;
      return;
    }
    if (!observedLiveActivation.current) return;
    observedLiveActivation.current = false;
    void query.refetch();
  }, [query.data?.rollout.id, query.data?.rollout.phase]);

  const activationMutation = useMutation({
    mutationFn: (targetRevision: number) => runMutationRequest((signal) => startModelSettingsActivation({ expectedRevision: targetRevision }, signal)),
    onMutate: () => {
      setRecoveredActivationTarget(undefined);
      setConflict(undefined);
    },
    onSuccess: (response) => {
      queryClient.setQueryData(modelSettingsQueryKey, response);
      if (!isRolloutInProgress(response.rollout.phase)) void query.refetch();
    },
    onError: async (error: Error, targetRevision: number) => {
      if (isAbortError(error)) return;
      const latest = await query.refetch().catch(() => undefined);
      if (latest?.isSuccess === true && snapshotHasTarget(latest.data, targetRevision)) setRecoveredActivationTarget(targetRevision);
    },
  });

  const saveMutation = useMutation({
    mutationFn: (intent: "save_only" | "save_and_apply") => {
      void intent;
      const currentDraft = draftRef.current;
      if (currentDraft === undefined) throw new Error("模型设置尚未加载。");
      return runMutationRequest((signal) => updateModelSettings({ expectedRevision: currentDraft.revision, chat: chatInput(currentDraft.chat), embedding: embeddingInput(currentDraft.embedding) }, signal));
    },
    onSuccess: (response, intent) => {
      queryClient.setQueryData(modelSettingsQueryKey, response);
      hydratedRevision.current = response.desiredRevision;
      draftDirty.current = false;
      const nextDraft = draftFromResponse(response);
      replaceDraft(nextDraft);
      setConflict(undefined);
      setValidationError(undefined);
      testMutation.reset();
      if (intent === "save_and_apply") activationMutation.mutate(response.desiredRevision);
    },
    onError: async (error: Error) => {
      if (!(error instanceof ModelSettingsApiError) || error.status !== 409) return;
      await reloadAfterConflict("保存", error);
    },
  });

  const visibleSaveError = saveMutation.isError && (!(saveMutation.error instanceof ModelSettingsApiError) || saveMutation.error.status !== 409);
  const visibleActivationError = activationMutation.isError && recoveredActivationTarget !== activationMutation.variables;

  useEffect(() => {
    if (validationError !== undefined || conflict !== undefined || visibleSaveError || visibleActivationError) feedbackRef.current?.focus();
  }, [conflict, validationError, visibleActivationError, visibleSaveError]);

  const resetFeedback = (): void => {
    setValidationError(undefined);
    setConflict(undefined);
    saveMutation.reset();
    activationMutation.reset();
    setRecoveredActivationTarget(undefined);
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
  const retryingFailedTarget = rolloutFailed && settings.rollout.targetRevision === settings.desiredRevision;
  const actionPending = saveMutation.isPending || activationMutation.isPending || testMutation.isPending;
  const controlsDisabled = rolloutLocked || actionPending || query.isFetching;
  const chatCanKeep = settings.desiredSettings.chat.apiKeyConfigured && sameSecretTarget(draft.chat.provider, draft.chat.baseUrl, settings.desiredSettings.chat.provider, settings.desiredSettings.chat.baseUrl);
  const embeddingCanKeep = settings.desiredSettings.embedding.apiKeyConfigured && sameSecretTarget(draft.embedding.provider, draft.embedding.baseUrl, settings.desiredSettings.embedding.provider, settings.desiredSettings.embedding.baseUrl);

  const runTest = (target: ModelTestTarget): void => {
    const error = validateDraft(draft, settings, target);
    setValidationError(error);
    setConflict(undefined);
    testMutation.reset();
    if (error !== undefined) return;
		testMutation.mutate({ target, idempotencyKey: `model-settings-test-${crypto.randomUUID()}` });
  };

  const save = (intent: "save_only" | "save_and_apply"): void => {
    const error = validateDraft(draft, settings);
    setValidationError(error);
    setConflict(undefined);
    if (error !== undefined) return;
    saveMutation.mutate(intent);
  };

  const submit = (event: SyntheticEvent<HTMLFormElement>): void => {
    event.preventDefault();
    if (!draftDirty.current) {
      activationMutation.mutate(settings.desiredRevision);
      return;
    }
    const error = validateDraft(draft, settings);
    setValidationError(error);
    setConflict(undefined);
    if (error !== undefined) return;
    saveMutation.mutate("save_and_apply");
  };

  return <Card className="model-settings-panel">
    <CardHeader title="模型与检索" description="待应用配置与当前生效配置分开显示。" action={<Button type="button" variant="ghost" size="sm" aria-label="刷新模型设置" onClick={() => void query.refetch()} disabled={query.isFetching || actionPending}><RefreshCw className={query.isFetching ? "is-spinning" : undefined} size={16} /></Button>} />

    <div className="model-settings-status" aria-label="模型配置版本状态">
      <div><span>待应用配置</span><strong>{revisionLabel(settings.desiredRevision)}</strong></div>
      <div><span>当前生效</span><strong>{revisionLabel(settings.activeRevision)}</strong></div>
      <div><span>API 已应用</span><strong>{revisionLabel(settings.runtime.api.appliedRevision)}</strong><Badge tone={runtimeTone(settings.runtime.api.fresh, settings.runtime.api.phase)}>{runtimePhaseLabels[settings.runtime.api.phase]}</Badge></div>
      <div><span>工作进程已应用</span><strong>{revisionLabel(settings.runtime.worker.appliedRevision)}</strong><Badge tone={runtimeTone(settings.runtime.worker.fresh, settings.runtime.worker.phase)}>{runtimePhaseLabels[settings.runtime.worker.phase]}</Badge></div>
      <div><span>本地模型运行时</span><strong>{localRuntimePhaseLabel[settings.localRuntime.phase]}</strong><Badge tone={settings.localRuntime.phase === "ready" ? "success" : settings.localRuntime.phase === "failed" ? "danger" : settings.localRuntime.phase === "stopped" ? "neutral" : "warning"}>{localRuntimePhaseLabel[settings.localRuntime.phase]}</Badge></div>
    </div>

    {settings.rollout.phase === "idle" && settings.applyRequired ? <div className="ui-state ui-state--warning" role="status"><strong>{settings.desiredRevision === settings.activeRevision ? "模型运行状态需要恢复" : "配置已保存，尚未应用"}</strong><p>{settings.desiredRevision === settings.activeRevision ? `${revisionLabel(settings.activeRevision)}的 API 或工作进程状态尚未一致。` : `${revisionLabel(settings.desiredRevision)}已安全保存，当前仍使用${revisionLabel(settings.activeRevision)}。`} 可直接应用配置。</p></div> : null}
    {!settings.applyRequired && settings.rollout.phase === "idle" ? <div className="model-settings-applied" role="status"><Check size={16} /><span>API 与工作进程已应用当前生效版本。</span></div> : null}
    <ActivationProgress settings={settings} />
    {settings.capabilities.chat === "unavailable" || settings.capabilities.embedding === "unavailable" ? <div className="ui-state ui-state--error" role="alert"><strong>部分模型能力不可用</strong><p>检查已保存密钥、提供方和当前生效版本，再重新测试或应用配置。</p></div> : null}

    <form className="model-settings-form" onSubmit={submit} noValidate>
      <section className="model-settings-section" aria-labelledby="chat-settings-title">
        <header><h3 id="chat-settings-title">对话模型</h3><div className="model-settings-section__actions"><Badge tone={capabilityTone(settings.capabilities.chat)}>{capabilityLabel[settings.capabilities.chat]}</Badge><Button type="button" variant="ghost" size="sm" className="model-settings-toggle" aria-label={`${expandedSections.chat ? "收起" : "配置"}对话模型`} aria-expanded={expandedSections.chat} aria-controls="chat-settings-content" onClick={() => setExpandedSections((current) => ({ ...current, chat: !current.chat }))}>{expandedSections.chat ? "收起" : "配置"}<ChevronDown className={expandedSections.chat ? "is-expanded" : undefined} size={16} /></Button></div></header>
        {expandedSections.chat ? <div className="model-settings-section__body" id="chat-settings-content">
          <fieldset className="model-settings-fields" disabled={controlsDisabled}>
            <legend><span>待应用</span>待应用配置</legend>
            <label>提供方<select aria-label="对话模型提供方" value={draft.chat.legacyLocal ? "ollama" : draft.chat.provider} onChange={(event) => changeDraft((current) => ({ ...current, chat: changeChatProvider(current.chat, chatProviderValue(event.target.value)) }))}><option value="disabled">已关闭</option><option value="openai-compatible">OpenAI-compatible</option><option value="ollama">本地 Ollama</option></select></label>
            <label>调用接口<select aria-label="对话模型调用接口" value={draft.chat.apiStyle} disabled={draft.chat.provider !== "openai-compatible" || draft.chat.legacyLocal || controlsDisabled} onChange={(event) => changeDraft((current) => ({ ...current, chat: { ...current.chat, apiStyle: chatAPIStyleValue(event.target.value) } }))}><option value="chat_completions">Chat Completions</option><option value="responses" disabled>Responses API（仅历史配置）</option></select></label>
            {draft.chat.provider === "ollama" || draft.chat.legacyLocal ? <div className="model-settings-managed-location model-settings-field--wide"><span>运行位置</span><strong>本机（系统管理）</strong></div> : <label className="model-settings-field--wide">基础地址（Base URL）<input type="url" maxLength={2048} aria-label="对话模型基础地址（Base URL）" value={draft.chat.baseUrl} disabled={draft.chat.provider === "disabled" || controlsDisabled} placeholder="https://api.example.com/v1" onChange={(event) => changeDraft((current) => ({ ...current, chat: { ...current.chat, baseUrl: event.target.value } }))} /></label>}
            <label>模型<input maxLength={128} aria-label="对话模型名称" value={draft.chat.model} disabled={draft.chat.provider === "disabled" || controlsDisabled} onChange={(event) => changeDraft((current) => ({ ...current, chat: { ...current.chat, model: event.target.value } }))} /></label>
            <label>模型版本<input maxLength={64} aria-label="对话模型版本" value={draft.chat.modelVersion} disabled={draft.chat.provider === "disabled" || controlsDisabled} onChange={(event) => changeDraft((current) => ({ ...current, chat: { ...current.chat, modelVersion: event.target.value } }))} /></label>
            {draft.chat.provider === "openai-compatible" && !draft.chat.legacyLocal ? <SecretControl id="chat-api-key" label="对话 API Key" secret={draft.chat.secret} configured={settings.desiredSettings.chat.apiKeyConfigured} canKeep={chatCanKeep} disabled={controlsDisabled} onChange={(secret) => changeDraft((current) => ({ ...current, chat: { ...current.chat, secret } }))} /> : <p className="model-settings-disabled model-settings-field--wide">{draft.chat.legacyLocal ? "这是旧式本地配置；运行位置由知序管理，不显示 API Key。下次保存设置时会转换为显式本地配置，并固定使用 Chat Completions。" : draft.chat.provider === "ollama" ? "本地 Ollama 由知序管理，不使用 API Key，固定使用 Chat Completions。" : "对话模型已关闭。"}{settings.desiredSettings.chat.apiKeyConfigured ? " 保存后会清除已保存的对话 API Key。" : ""}</p>}
			<div className="model-settings-actions model-settings-field--wide"><Button type="button" variant="secondary" size="sm" disabled={controlsDisabled || draft.chat.provider === "disabled"} onClick={() => runTest("chat")}><TestTube2 size={15} />{testMutation.isPending && testMutation.variables.target === "chat" ? "测试中…" : "测试对话连接"}</Button><TestFeedback target="chat" mutation={testMutation} /></div>
          </fieldset>
          <ActiveSummary kind="chat" summary={settings.activeSettings.chat} differs={!settingsEqual(settings.desiredSettings.chat, settings.activeSettings.chat)} />
        </div> : null}
      </section>

      <section className="model-settings-section" aria-labelledby="embedding-settings-title">
        <header><h3 id="embedding-settings-title">向量模型</h3><div className="model-settings-section__actions"><Badge tone={capabilityTone(settings.capabilities.embedding)}>{capabilityLabel[settings.capabilities.embedding]}</Badge><Button type="button" variant="ghost" size="sm" className="model-settings-toggle" aria-label={`${expandedSections.embedding ? "收起" : "配置"}向量模型`} aria-expanded={expandedSections.embedding} aria-controls="embedding-settings-content" onClick={() => setExpandedSections((current) => ({ ...current, embedding: !current.embedding }))}>{expandedSections.embedding ? "收起" : "配置"}<ChevronDown className={expandedSections.embedding ? "is-expanded" : undefined} size={16} /></Button></div></header>
        {expandedSections.embedding ? <div className="model-settings-section__body" id="embedding-settings-content">
          <fieldset className="model-settings-fields" disabled={controlsDisabled}>
            <legend><span>待应用</span>待应用配置</legend>
            <label>提供方<select aria-label="向量模型提供方" value={draft.embedding.provider} onChange={(event) => changeDraft((current) => { const provider = embeddingProviderValue(event.target.value); return { ...current, embedding: { ...current.embedding, provider, ...(provider === "disabled" ? { baseUrl: "", model: "", dimensions: "0", secret: { action: "clear", value: "" } } : provider === "ollama" ? { baseUrl: modelSettingsOllamaRelayUrl, secret: { action: "clear", value: "" } } : {}) } }; })}><option value="disabled">已关闭</option><option value="openai-compatible">OpenAI-compatible</option><option value="ollama">本地 Ollama</option></select></label>
            {draft.embedding.provider === "ollama" ? <div className="model-settings-managed-location model-settings-field--wide"><span>运行位置</span><strong>本机（系统管理）</strong></div> : <label className="model-settings-field--wide">基础地址（Base URL）<input type="url" maxLength={2048} aria-label="向量模型基础地址（Base URL）" value={draft.embedding.baseUrl} disabled={draft.embedding.provider === "disabled" || controlsDisabled} placeholder="https://api.example.com/v1" onChange={(event) => changeDraft((current) => ({ ...current, embedding: { ...current.embedding, baseUrl: event.target.value } }))} /></label>}
            <label>模型<input maxLength={128} aria-label="向量模型名称" value={draft.embedding.model} disabled={draft.embedding.provider === "disabled" || controlsDisabled} onChange={(event) => changeDraft((current) => ({ ...current, embedding: { ...current.embedding, model: event.target.value } }))} /></label>
            <label>维度<input type="number" inputMode="numeric" min="1" max="16000" aria-label="向量维度" value={draft.embedding.dimensions} disabled={draft.embedding.provider === "disabled" || controlsDisabled} onChange={(event) => changeDraft((current) => ({ ...current, embedding: { ...current.embedding, dimensions: event.target.value } }))} /></label>
            <label>归一化<select aria-label="向量归一化" value={draft.embedding.normalization} disabled={draft.embedding.provider === "disabled" || controlsDisabled} onChange={(event) => changeDraft((current) => ({ ...current, embedding: { ...current.embedding, normalization: normalizationValue(event.target.value) } }))}><option value="l2">L2</option><option value="none">无</option></select></label>
            <label>距离度量<select aria-label="向量距离度量" value={draft.embedding.distanceMetric} disabled={draft.embedding.provider === "disabled" || controlsDisabled} onChange={(event) => changeDraft((current) => ({ ...current, embedding: { ...current.embedding, distanceMetric: distanceMetricValue(event.target.value) } }))}><option value="cosine">余弦（Cosine）</option><option value="inner_product">内积（Inner product）</option><option value="euclidean">欧氏距离（Euclidean）</option></select></label>
            {draft.embedding.provider === "openai-compatible" ? <SecretControl id="embedding-api-key" label="向量 API Key" secret={draft.embedding.secret} configured={settings.desiredSettings.embedding.apiKeyConfigured} canKeep={embeddingCanKeep} disabled={controlsDisabled} onChange={(secret) => changeDraft((current) => ({ ...current, embedding: { ...current.embedding, secret } }))} /> : <p className="model-settings-disabled model-settings-field--wide">{draft.embedding.provider === "ollama" ? "本地 Ollama 由知序管理，不使用 API Key。" : "向量模型已关闭；关键词检索保持可用。"}{settings.desiredSettings.embedding.apiKeyConfigured ? " 保存后会清除已保存的向量 API Key。" : ""}</p>}
			<div className="model-settings-actions model-settings-field--wide"><Button type="button" variant="secondary" size="sm" disabled={controlsDisabled || draft.embedding.provider === "disabled"} onClick={() => runTest("embedding")}><TestTube2 size={15} />{testMutation.isPending && testMutation.variables.target === "embedding" ? "测试中…" : "测试向量连接"}</Button><TestFeedback target="embedding" mutation={testMutation} /></div>
          </fieldset>
          <ActiveSummary kind="embedding" summary={settings.activeSettings.embedding} differs={!settingsEqual(settings.desiredSettings.embedding, settings.activeSettings.embedding)} />
        </div> : null}
      </section>

      <div className="model-settings-feedback" ref={feedbackRef} tabIndex={-1} aria-label="模型设置反馈" aria-live="polite">
        {validationError ? <p className="model-settings-error" role="alert">{validationError}</p> : null}
        {conflict ? <p className="model-settings-warning" role="alert"><AlertTriangle size={15} />{conflict}</p> : null}
        {visibleSaveError ? <p className="model-settings-error" role="alert">{saveMutation.error.message}</p> : null}
        {visibleActivationError ? <p className="model-settings-error" role="alert">{revisionLabel(activationMutation.variables)}已保存，但应用请求未确认：{activationMutation.error.message}。权威状态未显示应用正在进行，可重试“应用配置”。</p> : null}
        {recoveredActivationTarget === undefined ? null : <p className="model-test-result model-test-result--success" role="status"><Check size={15} />已从服务端恢复{revisionLabel(recoveredActivationTarget)}的应用状态。</p>}
        {saveMutation.isSuccess ? <p className="model-test-result model-test-result--success" role="status"><Check size={15} />{revisionLabel(saveMutation.data.desiredRevision)}已保存{settings.activeRevision === saveMutation.data.desiredRevision && !settings.applyRequired ? "并已生效" : isRolloutInProgress(settings.rollout.phase) ? "，正在应用" : "，尚未应用"}。</p> : null}
      </div>
      <div className="model-settings-save">
        {draftDirty.current ? <Button type="button" variant="secondary" disabled={controlsDisabled} onClick={() => save("save_only")}><Save size={16} />{saveMutation.isPending && saveMutation.variables === "save_only" ? "正在保存…" : "仅保存"}</Button> : null}
        {draftDirty.current || settings.applyRequired || (rolloutFailed && settings.rollout.retryable) ? <Button type="submit" disabled={controlsDisabled}>{draftDirty.current ? <Save size={16} /> : <Play size={16} />}{saveMutation.isPending ? "正在保存…" : activationMutation.isPending ? "正在启动应用…" : draftDirty.current ? "保存并应用" : retryingFailedTarget ? "重试应用" : "应用配置"}</Button> : null}
      </div>
    </form>
  </Card>;
};
