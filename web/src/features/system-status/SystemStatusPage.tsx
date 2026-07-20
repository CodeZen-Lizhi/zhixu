import { ApiBoundaryError } from "../../api/system-status";
import { useSystemStatus } from "./use-system-status";

const LoadingState = () => (
  <section className="state-panel state-panel--loading" aria-live="polite">
    <span className="status-mark" aria-hidden="true" />
    <div>
      <p className="eyebrow">正在连接</p>
      <h2>读取系统真实状态</h2>
      <p>正在检查 API、数据库和当前应用版本。</p>
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

  const { database, rag, requestId, status, version } = statusQuery.data;
  const isReady = status === "ready" && database.status === "ready";
  const databaseUnavailable = database.status === "unavailable";

  return (
    <section
      className={`state-panel ${isReady ? "state-panel--ready" : "state-panel--degraded"}`}
      aria-live="polite"
    >
      <span className="status-mark" aria-hidden="true" />
      <div className="state-content">
        <p className="eyebrow">{isReady ? "系统就绪" : "服务降级"}</p>
        <h2>{isReady ? "所有基础依赖可用" : databaseUnavailable ? "API 可用，但数据库不可用" : "RAG 能力暂不可用"}</h2>
        <p>
          {isReady
            ? "ZHIXU 已连接数据库，可以继续后续功能开发。"
            : databaseUnavailable
              ? database.message ?? "数据库依赖暂时不可用，请检查服务配置和运行状态。"
              : "会话读取仍可用，但新问题提交已暂停，请检查 RAG 运行依赖。"}
        </p>

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
            <dt>RAG</dt>
            <dd><span aria-hidden="true">{rag.status === "unavailable" ? "▲" : "●"}</span> {rag.status === "ready" ? "可用" : rag.status === "disabled" ? "已关闭" : "不可用"}</dd>
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
