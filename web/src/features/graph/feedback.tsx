import type { GraphPageMeta } from "../../api/graph";
import { GraphApiError } from "../../api/graph";

const errorTitle = (error: Error): string => {
  if (!(error instanceof GraphApiError)) return "图谱请求未完成";
  switch (error.errorCode) {
    case "GRAPH_QUERY_TIMEOUT": return "查询超时";
    case "GRAPH_CURSOR_STALE": return "图谱结果已变化";
    case "GRAPH_CURSOR_INVALID": return "分页状态已失效";
    case "GRAPH_QUERY_BUDGET_EXCEEDED": return "查询超过安全预算";
    case "GRAPH_DEPENDENCY_UNAVAILABLE": return "图谱服务暂不可用";
    case "GRAPH_PROJECTION_INCONSISTENT": return "图谱投影不一致";
    case "NETWORK_ERROR": return "无法连接图谱服务";
    default: return "图谱请求未完成";
  }
};

const isCursorRecoveryError = (error: Error): boolean => error instanceof GraphApiError
  && (error.errorCode === "GRAPH_CURSOR_STALE" || error.errorCode === "GRAPH_CURSOR_INVALID");

export const GraphErrorNotice = ({
  error,
  onRetry,
  onResetToFirstPage,
}: {
  error: Error;
  onRetry?: () => void;
  onResetToFirstPage?: () => void;
}) => {
  const resetToFirstPage = isCursorRecoveryError(error) && onResetToFirstPage !== undefined;
  const action = resetToFirstPage ? onResetToFirstPage : onRetry;
  return (
    <div className="graph-notice graph-notice--error" role="alert">
      <div>
        <strong>{errorTitle(error)}</strong>
        <span>{error.message}</span>
        {error instanceof GraphApiError ? <code>{error.errorCode}</code> : null}
      </div>
      {action === undefined ? null : <button type="button" className="graph-text-button" onClick={action}>{resetToFirstPage ? "从第一页重新加载" : "重新查询"}</button>}
    </div>
  );
};

export const GraphResultNotice = ({
  meta,
  title = "结果已截断",
  message = "请缩小过滤范围或降低展开深度。",
  ariaLabel = "图谱结果状态",
}: {
  meta: GraphPageMeta | undefined;
  title?: string;
  message?: string;
  ariaLabel?: string;
}) => {
  if (meta?.truncated !== true) return null;
  return (
    <div className="graph-notice graph-notice--warning" role="status" aria-label={ariaLabel}>
      <div><strong>{title}</strong><span>{message}</span></div>
      {meta.reason === undefined ? null : <code>{meta.reason}</code>}
    </div>
  );
};

export const GraphLoading = ({ label = "正在加载图谱" }: { label?: string }) => (
  <div className="graph-loading" role="status" aria-label={label}>
    <span className="graph-loading__line" />
    <span className="graph-loading__line graph-loading__line--short" />
    <span className="graph-loading__field" />
  </div>
);
