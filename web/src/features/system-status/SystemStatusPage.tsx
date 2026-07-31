import { ApiBoundaryError, type SystemStatus } from "../../api/system-status";
import { useSystemStatus } from "./use-system-status";

type SystemStatusDisplay = "full" | "compact";

interface SystemStatusPageProps {
  display?: SystemStatusDisplay;
}

const panelClassName = (display: SystemStatusDisplay, state: string): string =>
  `state-panel state-panel--${state}${display === "compact" ? " state-panel--compact" : ""}`;

const LoadingState = ({ display }: { display: SystemStatusDisplay }) => (
  <section className={panelClassName(display, "loading")} aria-live="polite">
    <span className="status-mark" aria-hidden="true" />
    <div>
      <p className="eyebrow">正在连接</p>
      <h2>{display === "compact" ? "读取运行摘要" : "读取系统真实状态"}</h2>
      <p>{display === "compact" ? "正在确认 API、数据库与可选能力状态。" : "正在检查 API、数据库、认证边界、Graph 和当前应用版本。"}</p>
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
        <p className="eyebrow">连接失败</p>
        <h2>无法确认系统状态</h2>
        <p>{error.message}</p>
        {boundaryError === undefined ? null : (
          <p className="technical-detail">错误码：{boundaryError.code}</p>
        )}
        <button type="button" onClick={onRetry} disabled={retrying}>
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
    && system.rag.status !== "unavailable";

  if (isReady) {
    const optionalRagSummary = system.rag.status === "disabled"
      ? "RAG 是可选能力，当前已关闭。"
      : "RAG 可用。";
    return {
      headline: system.rag.status === "disabled" ? "基础服务已就绪" : "所有基础依赖可用",
      isReady: true,
      summary: `ZHIXU 已连接数据库，认证${system.auth.status === "disabled" ? "已按开发模式关闭" : "已启用"}，Graph 查询可用；${optionalRagSummary}`,
    };
  }

  if (system.database.status === "unavailable") {
    return { headline: "API 可用，但数据库不可用", isReady: false, summary: system.database.message ?? "数据库依赖暂时不可用，请检查服务配置和运行状态。" };
  }
  if (system.auth.status === "unavailable") {
    return { headline: "认证依赖暂不可用", isReady: false, summary: "认证边界无法初始化，业务 API 已 fail closed；请检查认证数据库与运行配置。" };
  }
  if (system.graph.status === "unavailable") {
    return { headline: "Graph 查询暂不可用", isReady: false, summary: "知识图谱查询已暂停，请稍后重试；若持续失败，请检查服务日志。" };
  }
  if (system.semanticLinks.status === "unavailable") {
    return { headline: "语义候选能力暂不可用", isReady: false, summary: "正式 Graph 查询仍可用，但候选扫描与审阅暂不可用，请检查语义候选依赖。" };
  }
  if (system.collections.status === "unavailable") {
    return { headline: "Collection 能力暂不可用", isReady: false, summary: "Collection 查询暂不可用，请检查 Collection 依赖。" };
  }
  if (system.knowledgeHealth.status === "unavailable") {
    return { headline: "知识健康能力暂不可用", isReady: false, summary: "知识健康查询暂不可用，请检查 Health 依赖。" };
  }
  if (system.knowledgeTimeline.status === "unavailable") {
    return { headline: "知识时间线能力暂不可用", isReady: false, summary: "Knowledge Timeline 与 Impact Analysis 暂不可用，请检查其投影依赖。" };
  }
  if (system.review.status === "unavailable") {
    return { headline: "Review 能力暂不可用", isReady: false, summary: "Review 学习能力暂不可用，请检查其服务依赖。" };
  }
  if (system.memory.status === "unavailable") {
    return { headline: "Memory 能力暂不可用", isReady: false, summary: "Memory 生命周期能力暂不可用，请检查其服务依赖。" };
  }
  if (system.interview.status === "unavailable") {
    return { headline: "Interview 能力暂不可用", isReady: false, summary: "Interview 学习路径能力暂不可用，请检查其服务依赖。" };
  }
  if (system.rag.status === "unavailable") {
    return { headline: "RAG 能力暂不可用", isReady: false, summary: "会话读取仍可用，但新问题提交已暂停，请检查 RAG 运行依赖。" };
  }

  return { headline: "系统处于降级状态", isReady: false, summary: "部分系统能力处于降级状态，请查看下方能力明细。" };
};

const compactStatusSymbol = (value: string): string => {
  if (value.includes("不可用") || value === "降级") return "▲";
  if (value.includes("关闭")) return "○";
  return "●";
};

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

  const { auth, collections, database, graph, interview, knowledgeHealth, knowledgeTimeline, memory, review, semanticLinks, rag, requestId, version } = statusQuery.data;
  const presentation = getStatusPresentation(statusQuery.data);
  const compactFacts = [
    { label: "API", value: "可用" },
    { label: "数据库", value: database.status === "ready" ? "可用" : "不可用" },
    { label: "Graph", value: graph.status === "ready" ? "可用" : "不可用" },
    { label: "RAG", value: rag.status === "ready" ? "可用" : rag.status === "disabled" ? "可选能力已关闭" : "不可用" },
  ] as const;

  return (
    <section
      className={panelClassName(display, presentation.isReady ? "ready" : "degraded")}
      aria-live="polite"
    >
      <span className="status-mark" aria-hidden="true" />
      <div className="state-content">
        <p className="eyebrow">{display === "compact" ? "运行摘要" : presentation.isReady ? "系统就绪" : "服务降级"}</p>
        <h2>{presentation.headline}</h2>
        <p>{presentation.summary}</p>

        {display === "compact" ? (
          <dl className="status-compact-grid">
            {compactFacts.map((fact) => (
              <div key={fact.label}>
                <dt>{fact.label}</dt>
                <dd><span aria-hidden="true">{compactStatusSymbol(fact.value)}</span> {fact.value}</dd>
              </div>
            ))}
          </dl>
        ) : (
          <dl className="status-grid">
            <div>
              <dt>API</dt>
              <dd><span aria-hidden="true">●</span> 可用</dd>
            </div>
            <div>
              <dt>数据库</dt>
              <dd><span aria-hidden="true">{database.status === "ready" ? "●" : "▲"}</span> {database.status === "ready" ? "可用" : "不可用"}</dd>
            </div>
            <div>
              <dt>Graph</dt>
              <dd><span aria-hidden="true">{graph.status === "ready" ? "●" : "▲"}</span> {graph.status === "ready" ? "可用" : "不可用"}</dd>
            </div>
            <div>
              <dt>语义候选</dt>
              <dd><span aria-hidden="true">{semanticLinks.status === "ready" ? "●" : "▲"}</span> {semanticLinks.status === "ready" ? "可用" : "不可用"}</dd>
            </div>
            <div>
              <dt>认证</dt>
              <dd><span aria-hidden="true">{auth.status === "unavailable" ? "▲" : auth.status === "disabled" ? "○" : "●"}</span> {auth.status === "ready" ? "已启用" : auth.status === "disabled" ? "开发模式关闭" : "不可用"}</dd>
            </div>
            <div>
              <dt>RAG</dt>
              <dd><span aria-hidden="true">{rag.status === "unavailable" ? "▲" : rag.status === "disabled" ? "○" : "●"}</span> {rag.status === "ready" ? "可用" : rag.status === "disabled" ? "可选能力已关闭" : "不可用"}</dd>
            </div>
            <div>
              <dt>Collections</dt>
              <dd><span aria-hidden="true">{collections.status === "ready" ? "●" : "▲"}</span> {collections.status === "ready" ? "可用" : "不可用"}</dd>
            </div>
            <div>
              <dt>知识健康</dt>
              <dd><span aria-hidden="true">{knowledgeHealth.status === "ready" ? "●" : "▲"}</span> {knowledgeHealth.status === "ready" ? "可用" : "不可用"}</dd>
            </div>
            <div>
              <dt>知识时间线</dt>
              <dd><span aria-hidden="true">{knowledgeTimeline.status === "ready" ? "●" : "▲"}</span> {knowledgeTimeline.status === "ready" ? "可用" : "不可用"}</dd>
            </div>
            <div>
              <dt>Review</dt>
              <dd><span aria-hidden="true">{review.status === "ready" ? "●" : "▲"}</span> {review.status === "ready" ? "可用" : "不可用"}</dd>
            </div>
            <div>
              <dt>Memory</dt>
              <dd><span aria-hidden="true">{memory.status === "ready" ? "●" : "▲"}</span> {memory.status === "ready" ? "可用" : "不可用"}</dd>
            </div>
            <div>
              <dt>Interview</dt>
              <dd><span aria-hidden="true">{interview.status === "ready" ? "●" : "▲"}</span> {interview.status === "ready" ? "可用" : "不可用"}</dd>
            </div>
            <div>
              <dt>版本</dt>
              <dd>{version}</dd>
            </div>
            <div>
              <dt>请求 ID</dt>
              <dd className="request-id">{requestId}</dd>
            </div>
          </dl>
        )}

        {presentation.isReady ? null : (
          <button
            type="button"
            onClick={() => {
              void statusQuery.refetch();
            }}
            disabled={statusQuery.isFetching}
          >
            {statusQuery.isFetching ? "正在重试…" : "重新检查"}
          </button>
        )}
      </div>
    </section>
  );
};
