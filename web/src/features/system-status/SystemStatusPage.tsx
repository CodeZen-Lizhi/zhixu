import { CheckCircle2, CircleOff, RefreshCw, TriangleAlert } from "lucide-react";
import { Link } from "react-router-dom";

import { ApiBoundaryError, type SystemStatus } from "../../api/system-status";
import { useSystemStatus } from "./use-system-status";

type SystemStatusDisplay = "full" | "compact";

interface SystemStatusPageProps {
  display?: SystemStatusDisplay;
}

const panelClassName = (display: SystemStatusDisplay, state: string): string =>
  `state-panel state-panel--${state}${display === "compact" ? " state-panel--compact" : ""}`;

const LoadingState = ({ display }: { display: SystemStatusDisplay }) => display === "compact" ? (
  <section className={panelClassName(display, "loading")} aria-live="polite">
    <span className="status-mark" aria-hidden="true" />
    <strong className="state-compact-headline">正在读取系统状态…</strong>
  </section>
) : (
  <section className={panelClassName(display, "loading")} aria-live="polite">
    <span className="status-mark" aria-hidden="true" />
    <div>
      <p className="eyebrow">正在连接</p>
      <h2>读取系统真实状态</h2>
      <p>正在检查 API、数据库、认证边界、知识图谱和当前应用版本。</p>
    </div>
  </section>
);

interface ErrorStateProps {
  display: SystemStatusDisplay;
  error: Error;
  onRetry: () => void;
  retrying: boolean;
}

const ErrorState = ({ display, error, onRetry, retrying }: ErrorStateProps) => {
  const boundaryError = error instanceof ApiBoundaryError ? error : undefined;

  return (
    <section className={panelClassName(display, "error")} role="alert">
      <span className="status-mark" aria-hidden="true" />
      <div>
        {display === "compact" ? null : <p className="eyebrow">连接失败</p>}
        <h2>无法确认系统状态</h2>
        <p>{error.message}</p>
        {boundaryError === undefined ? null : (
          <p className="technical-detail">错误码：{boundaryError.code}</p>
        )}
        <button type="button" onClick={onRetry} disabled={retrying}>
          <RefreshCw size={15} aria-hidden="true" />
          {retrying ? "正在重试…" : "重新检查"}
        </button>
      </div>
    </section>
  );
};

const getStatusPresentation = (system: SystemStatus): { headline: string; isReady: boolean; summary: string } => {
  const isReady = system.status === "ready"
    && system.database.status === "ready"
    && system.auth.status !== "unavailable"
    && system.graph.status === "ready"
    && system.semanticLinks.status === "ready"
    && system.collections.status === "ready"
    && system.knowledgeHealth.status === "ready"
    && system.knowledgeTimeline.status === "ready"
    && system.review.status === "ready"
    && system.memory.status === "ready"
    && system.interview.status === "ready"
    && system.authoring.status === "ready"
    && system.capture.status === "ready"
    && system.organizing.status === "ready"
    && system.rag.status !== "unavailable";

  if (isReady) {
    return {
      headline: "运行正常",
      isReady: true,
      summary: "API、数据库和知识工作台均可用；已关闭的可选能力列在下方。",
    };
  }

  if (system.database.status === "unavailable") {
    return { headline: "当前无法继续", isReady: false, summary: "数据库不可用，依赖持久化数据的工作流已暂停。" };
  }
  if (system.auth.status === "unavailable") {
    return { headline: "当前无法继续", isReady: false, summary: "认证边界无法初始化，受保护的业务 API 已拒绝访问。" };
  }

  return { headline: "部分功能受影响", isReady: false, summary: "核心服务仍在运行，受影响范围和恢复动作列在下方。" };
};

const compactStatusSymbol = (value: string): string => {
  if (value.includes("不可用") || value === "降级") return "▲";
  if (value.includes("关闭")) return "○";
  return "●";
};

type CapabilityState = "ready" | "disabled" | "unavailable";

interface CapabilityFact {
  id: string;
  label: string;
  state: CapabilityState;
  readyLabel?: string;
  disabledLabel?: string;
  impact?: string;
  reason?: string;
  recovery?: { kind: "retry"; label: string } | { kind: "link"; label: string; to: string };
}

const CapabilityRow = ({ fact }: { fact: CapabilityFact }) => {
  const unavailable = fact.state === "unavailable";
  const disabled = fact.state === "disabled";
  const Icon = unavailable ? TriangleAlert : disabled ? CircleOff : CheckCircle2;
  const label = unavailable ? "不可用" : disabled ? (fact.disabledLabel ?? "已关闭") : (fact.readyLabel ?? "可用");
  return <div className={`status-capability status-capability--${fact.state}`}>
    <dt>{fact.label}</dt>
    <dd><Icon size={15} aria-hidden="true" /><span>{label}</span></dd>
  </div>;
};

const CapabilityGroup = ({ title, facts }: { title: string; facts: readonly CapabilityFact[] }) => <section className="status-capability-group">
  <h3>{title}</h3>
  <dl>{facts.map((fact) => <CapabilityRow key={fact.id} fact={fact} />)}</dl>
</section>;

const CapabilityIssue = ({ fact, onRetry, retrying }: { fact: CapabilityFact; onRetry: () => void; retrying: boolean }) => {
  const unavailable = fact.state === "unavailable";
  const Icon = unavailable ? TriangleAlert : CircleOff;
  const stateLabel = unavailable ? "不可用" : (fact.disabledLabel ?? "已关闭");
  return <article className={`status-impact-item status-impact-item--${fact.state}`}>
    <header>
      <span className="status-impact-item__identity"><Icon size={16} aria-hidden="true" /><strong>{fact.label}</strong></span>
      <span className="status-impact-item__state">{stateLabel}</span>
    </header>
    <dl>
      <div><dt>影响</dt><dd>{fact.impact}</dd></div>
      <div><dt>原因</dt><dd>{fact.reason}</dd></div>
    </dl>
    {fact.recovery?.kind === "link"
      ? <Link to={fact.recovery.to}>{fact.recovery.label}</Link>
      : <button type="button" aria-label={fact.recovery?.label ?? `重新检查${fact.label}`} onClick={onRetry} disabled={retrying}><RefreshCw size={14} aria-hidden="true" />{retrying ? "正在检查…" : "重新检查"}</button>}
  </article>;
};

const formatCheckedAt = (value: number): string => new Intl.DateTimeFormat("zh-CN", {
  hour: "2-digit", minute: "2-digit", second: "2-digit", hour12: false,
}).format(new Date(value));

export const SystemStatusPage = ({ display = "full" }: SystemStatusPageProps) => {
  const statusQuery = useSystemStatus();

  if (statusQuery.isPending) {
    return <LoadingState display={display} />;
  }

  if (statusQuery.isError) {
    return (
      <ErrorState
        display={display}
        error={statusQuery.error}
        onRetry={() => {
          void statusQuery.refetch();
        }}
        retrying={statusQuery.isFetching}
      />
    );
  }

  const { auth, authoring, capture, collections, database, graph, interview, knowledgeHealth, knowledgeTimeline, memory, organizing, review, semanticLinks, rag, requestId, version } = statusQuery.data;
  const presentation = getStatusPresentation(statusQuery.data);
  const compactFacts = [
    { label: "API", value: "可用" },
    { label: "数据库", value: database.status === "ready" ? "可用" : "不可用" },
    { label: "知识图谱", value: graph.status === "ready" ? "可用" : "不可用" },
    { label: "RAG", value: rag.status === "ready" ? "可用" : rag.status === "disabled" ? "可选能力已关闭" : "不可用" },
  ] as const;

  if (display === "compact") {
    return (
      <section className={panelClassName(display, presentation.isReady ? "ready" : "degraded")} aria-live="polite">
        <span className="status-mark" aria-hidden="true" />
        <div className="state-content">
          <strong className="state-compact-headline">{presentation.headline}</strong>
          <dl className="status-compact-grid">
            {compactFacts.map((fact) => (
              <div key={fact.label}>
                <dt>{fact.label}</dt>
                <dd><span aria-hidden="true">{compactStatusSymbol(fact.value)}</span> {fact.value}</dd>
              </div>
            ))}
          </dl>
        {presentation.isReady ? null : (
          <button type="button" onClick={() => { void statusQuery.refetch(); }} disabled={statusQuery.isFetching}>
            <RefreshCw size={15} aria-hidden="true" />
            {statusQuery.isFetching ? "正在重试…" : "重新检查"}
          </button>
        )}
        </div>
      </section>
    );
  }

  const groups: readonly { title: string; facts: readonly CapabilityFact[] }[] = [
    {
      title: "核心运行",
      facts: [
        { id: "api", label: "API", state: "ready" },
        { id: "database", label: "数据库", state: database.status, impact: "依赖持久化数据的工作流暂不可用。", reason: database.message ?? "数据库连接或运行配置不可用。", recovery: { kind: "retry", label: "重新检查数据库" } },
        { id: "auth", label: "认证", state: auth.status, readyLabel: "已启用", disabledLabel: "开发模式", impact: auth.status === "disabled" ? "当前实例没有登录保护，仅适合受控开发环境。" : "受保护的业务 API 将拒绝访问。", reason: auth.status === "disabled" ? "认证由运行配置明确关闭。" : "认证依赖未能初始化。", recovery: auth.status === "disabled" ? { kind: "link", label: "检查访问权限", to: "/settings?section=access" } : { kind: "retry", label: "重新检查认证" } },
        { id: "graph", label: "知识图谱", state: graph.status, impact: "知识图谱查询与依赖它的探索入口暂不可用。", reason: "知识图谱查询依赖未就绪。", recovery: { kind: "retry", label: "重新检查知识图谱" } },
      ],
    },
    {
      title: "内容工作台",
      facts: [
        { id: "capture", label: "快速记录", state: capture.status, impact: "原始资料仍保留，但新的快速记录请求暂不可用。", reason: "快速记录服务依赖未就绪。", recovery: { kind: "retry", label: "重新检查快速记录" } },
        { id: "authoring", label: "创作", state: authoring.status, impact: "现有资料仍可读取，但草稿与发布流程暂不可用。", reason: "创作服务依赖未就绪。", recovery: { kind: "retry", label: "重新检查创作" } },
        { id: "organizing", label: "整理", state: organizing.status, impact: "资料与文章仍可读取，但材料确认和整理流程暂不可用。", reason: "整理服务依赖未就绪。", recovery: { kind: "retry", label: "重新检查整理" } },
        { id: "collections", label: "集合", state: collections.status, impact: "已保存集合与条件查询暂不可用。", reason: "集合服务依赖未就绪。", recovery: { kind: "retry", label: "重新检查集合" } },
        { id: "semantic-links", label: "语义候选", state: semanticLinks.status, impact: "正式知识图谱仍可用，但候选扫描与审阅暂不可用。", reason: "语义候选依赖未就绪。", recovery: { kind: "retry", label: "重新检查语义候选" } },
      ],
    },
    {
      title: "知识与学习",
      facts: [
        { id: "knowledge-health", label: "知识健康", state: knowledgeHealth.status, impact: "知识健康扫描与问题列表暂不可用。", reason: "知识健康依赖未就绪。", recovery: { kind: "retry", label: "重新检查知识健康" } },
        { id: "knowledge-timeline", label: "知识时间线", state: knowledgeTimeline.status, impact: "知识时间线与影响分析暂不可用。", reason: "时间线投影依赖未就绪。", recovery: { kind: "retry", label: "重新检查知识时间线" } },
        { id: "review", label: "复习", state: review.status, impact: "主动回忆与复习调度暂不可用。", reason: "复习服务依赖未就绪。", recovery: { kind: "retry", label: "重新检查复习" } },
        { id: "memory", label: "记忆", state: memory.status, impact: "长期上下文管理暂不可用。", reason: "记忆服务依赖未就绪。", recovery: { kind: "retry", label: "重新检查记忆" } },
        { id: "interview", label: "访谈", state: interview.status, impact: "模拟面试与学习路径暂不可用。", reason: "访谈服务依赖未就绪。", recovery: { kind: "retry", label: "重新检查访谈" } },
        { id: "rag", label: "RAG", state: rag.status, disabledLabel: "已关闭（可选）", impact: rag.status === "disabled" ? "问答增强入口不会运行，其他知识能力不受影响。" : "新问题提交暂不可用，已有会话仍可读取。", reason: rag.status === "disabled" ? "可选 RAG 能力未启用。" : "RAG 运行依赖未就绪。", recovery: rag.status === "disabled" ? { kind: "link", label: "配置模型与检索", to: "/settings?section=models" } : { kind: "retry", label: "重新检查 RAG" } },
      ],
    },
  ];
  const affectedFacts = groups.flatMap((group) => group.facts).filter((fact) => fact.state !== "ready");
  const readyFactCount = groups.flatMap((group) => group.facts).filter((fact) => fact.state === "ready").length;
  const factCount = groups.reduce((count, group) => count + group.facts.length, 0);
  const checkedAt = new Date(statusQuery.dataUpdatedAt);

  return (
    <section
      className={`${panelClassName(display, presentation.isReady ? "ready" : "degraded")} state-panel--full`}
      aria-live="polite"
    >
      <header className="status-overview">
        <span className="status-mark" aria-hidden="true" />
        <div>
          <p className="eyebrow">{presentation.isReady ? "系统就绪" : "服务降级"}</p>
          <h2>{presentation.headline}</h2>
          <p>{presentation.summary}</p>
          <time dateTime={checkedAt.toISOString()}>最近检查 {formatCheckedAt(statusQuery.dataUpdatedAt)}</time>
        </div>
        <button
          type="button"
          aria-label="重新检查系统状态"
          onClick={() => { void statusQuery.refetch(); }}
          disabled={statusQuery.isFetching}
        >
          <RefreshCw size={15} aria-hidden="true" />
          {statusQuery.isFetching ? "正在检查…" : "重新检查"}
        </button>
      </header>
      {affectedFacts.length === 0 ? null : <section className="status-impact" aria-labelledby="status-impact-title">
        <h3 id="status-impact-title">需要关注</h3>
        <div className="status-impact-list">{affectedFacts.map((fact) => <CapabilityIssue key={fact.id} fact={fact} onRetry={() => { void statusQuery.refetch(); }} retrying={statusQuery.isFetching} />)}</div>
      </section>}
      <p className="status-healthy-summary"><CheckCircle2 size={16} aria-hidden="true" /><span><strong>其余服务正常</strong><small>{readyFactCount} 项能力通过检查</small></span></p>
      <details className="status-technical" id="status-technical">
        <summary><span>技术详情</span><small>{factCount} 项运行事实</small></summary>
        <div className="status-capability-groups">
          {groups.map((group) => <CapabilityGroup key={group.title} title={group.title} facts={group.facts} />)}
        </div>
        <footer className="status-metadata">
          <span>版本 <strong>{version}</strong></span>
          <span>请求 ID <code>{requestId}</code></span>
        </footer>
      </details>
    </section>
  );
};
