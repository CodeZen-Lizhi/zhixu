import { ApiBoundaryError } from "../../api/system-status";
import { useSystemStatus } from "./use-system-status";

const LoadingState = () => (
  <section className="state-panel state-panel--loading" aria-live="polite">
    <span className="status-mark" aria-hidden="true" />
    <div>
      <p className="eyebrow">正在连接</p>
      <h2>读取系统真实状态</h2>
      <p>正在检查 API、数据库、认证边界、Graph 和当前应用版本。</p>
    </div>
  </section>
);

interface ErrorStateProps {
  error: Error;
  onRetry: () => void;
  retrying: boolean;
}

const ErrorState = ({ error, onRetry, retrying }: ErrorStateProps) => {
  const boundaryError = error instanceof ApiBoundaryError ? error : undefined;

  return (
    <section className="state-panel state-panel--error" role="alert">
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

export const SystemStatusPage = () => {
  const statusQuery = useSystemStatus();

  if (statusQuery.isPending) {
    return <LoadingState />;
  }

  if (statusQuery.isError) {
    return (
      <ErrorState
        error={statusQuery.error}
        onRetry={() => {
          void statusQuery.refetch();
        }}
        retrying={statusQuery.isFetching}
      />
    );
  }

  const { auth, collections, database, graph, interview, knowledgeHealth, knowledgeTimeline, memory, review, semanticLinks, rag, requestId, status, version } = statusQuery.data;
  const isReady = status === "ready"
    && database.status === "ready"
    && auth.status !== "unavailable"
    && graph.status === "ready"
    && semanticLinks.status === "ready"
    && collections.status === "ready"
    && knowledgeHealth.status === "ready"
    && knowledgeTimeline.status === "ready"
    && review.status === "ready"
    && memory.status === "ready"
    && interview.status === "ready"
    && rag.status !== "unavailable";
  const databaseUnavailable = database.status === "unavailable";
  const authUnavailable = auth.status === "unavailable";
  const graphUnavailable = graph.status === "unavailable";
  const semanticLinksUnavailable = semanticLinks.status === "unavailable";
  const collectionsUnavailable = collections.status === "unavailable";
  const knowledgeHealthUnavailable = knowledgeHealth.status === "unavailable";
  const knowledgeTimelineUnavailable = knowledgeTimeline.status === "unavailable";
  const reviewUnavailable = review.status === "unavailable";
  const memoryUnavailable = memory.status === "unavailable";
  const interviewUnavailable = interview.status === "unavailable";
  const ragUnavailable = rag.status === "unavailable";
  const headline = isReady
    ? "所有基础依赖可用"
    : databaseUnavailable
      ? "API 可用，但数据库不可用"
      : authUnavailable
        ? "认证依赖暂不可用"
        : graphUnavailable
        ? "Graph 查询暂不可用"
        : semanticLinksUnavailable
          ? "语义候选能力暂不可用"
          : collectionsUnavailable
            ? "Collection 能力暂不可用"
            : knowledgeHealthUnavailable
              ? "知识健康能力暂不可用"
              : knowledgeTimelineUnavailable
                ? "知识时间线能力暂不可用"
                : reviewUnavailable
                  ? "Review 能力暂不可用"
                  : memoryUnavailable
                    ? "Memory 能力暂不可用"
                    : interviewUnavailable
                      ? "Interview 能力暂不可用"
                : ragUnavailable
                  ? "RAG 能力暂不可用"
                  : "系统处于降级状态";
  const summary = isReady
    ? `ZHIXU 已连接数据库，认证${auth.status === "disabled" ? "已按开发模式关闭" : "已启用"}，Graph 查询可用。`
    : databaseUnavailable
      ? database.message ?? "数据库依赖暂时不可用，请检查服务配置和运行状态。"
      : authUnavailable
        ? "认证边界无法初始化，业务 API 已 fail closed；请检查认证数据库与运行配置。"
        : graphUnavailable
        ? "知识图谱查询已暂停，请稍后重试；若持续失败，请检查服务日志。"
        : semanticLinksUnavailable
          ? "正式 Graph 查询仍可用，但候选扫描与审阅暂不可用，请检查语义候选依赖。"
          : collectionsUnavailable
            ? "Collection 查询暂不可用，请检查 Collection 依赖。"
            : knowledgeHealthUnavailable
              ? "知识健康查询暂不可用，请检查 Health 依赖。"
              : knowledgeTimelineUnavailable
                ? "Knowledge Timeline 与 Impact Analysis 暂不可用，请检查其投影依赖。"
                : reviewUnavailable
                  ? "Review 学习能力暂不可用，请检查其服务依赖。"
                  : memoryUnavailable
                    ? "Memory 生命周期能力暂不可用，请检查其服务依赖。"
                    : interviewUnavailable
                      ? "Interview 学习路径能力暂不可用，请检查其服务依赖。"
                : ragUnavailable
                  ? "会话读取仍可用，但新问题提交已暂停，请检查 RAG 运行依赖。"
                  : "部分系统能力处于降级状态，请查看下方能力明细。";

  return (
    <section
      className={`state-panel ${isReady ? "state-panel--ready" : "state-panel--degraded"}`}
      aria-live="polite"
    >
      <span className="status-mark" aria-hidden="true" />
      <div className="state-content">
        <p className="eyebrow">{isReady ? "系统就绪" : "服务降级"}</p>
        <h2>{headline}</h2>
        <p>{summary}</p>

        <dl className="status-grid">
          <div>
            <dt>API</dt>
            <dd><span aria-hidden="true">●</span> 可用</dd>
          </div>
          <div>
            <dt>数据库</dt>
            <dd>
              <span aria-hidden="true">{database.status === "ready" ? "●" : "▲"}</span>{" "}
              {database.status === "ready" ? "可用" : "不可用"}
            </dd>
          </div>
          <div>
            <dt>Graph</dt>
            <dd>
              <span aria-hidden="true">{graph.status === "ready" ? "●" : "▲"}</span>{" "}
              {graph.status === "ready" ? "可用" : "不可用"}
            </dd>
          </div>
          <div>
            <dt>语义候选</dt>
            <dd>
              <span aria-hidden="true">{semanticLinks.status === "ready" ? "●" : "▲"}</span>{" "}
              {semanticLinks.status === "ready" ? "可用" : "不可用"}
            </dd>
          </div>
          <div>
            <dt>认证</dt>
            <dd><span aria-hidden="true">{auth.status === "unavailable" ? "▲" : "●"}</span> {auth.status === "ready" ? "已启用" : auth.status === "disabled" ? "开发模式关闭" : "不可用"}</dd>
          </div>
          <div>
            <dt>RAG</dt>
            <dd><span aria-hidden="true">{rag.status === "unavailable" ? "▲" : "●"}</span> {rag.status === "ready" ? "可用" : rag.status === "disabled" ? "已关闭" : "不可用"}</dd>
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

        {isReady ? null : (
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
