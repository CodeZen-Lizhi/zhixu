import type { GraphPageMeta } from "../../api/graph";
import { GraphApiError } from "../../api/graph";

const errorTitle = (error: Error): string => {
  if (!(error instanceof GraphApiError)) return "图谱请求未完成";
  switch (error.errorCode) {
    case "GRAPH_QUERY_TIMEOUT": return "查询超时";
    case "GRAPH_CURSOR_STALE": return "图谱结果已变化";
    case "GRAPH_CURSOR_INVALID": return "分页状态已失效";
    case "GRAPH_QUERY_BUDGET_EXCEEDED": return "查询超过安全预算";
    case "GRAPH_DEPENDENCY_UNAVAILABLE": return "Graph 服务暂不可用";
    case "GRAPH_PROJECTION_INCONSISTENT": return "图谱投影不一致";
    case "NETWORK_ERROR": return "无法连接 Graph 服务";
    default: return "图谱请求未完成";
  }
};

export const GraphErrorNotice = ({ error, onRetry }: { error: Error; onRetry?: () => void }) => (
  <div className="graph-notice graph-notice--error" role="alert">
    <div>
      <strong>{errorTitle(error)}</strong>
      <span>{error.message}</span>
      {error instanceof GraphApiError ? <code>{error.errorCode}</code> : null}
    </div>
    {onRetry === undefined ? null : <button type="button" className="graph-text-button" onClick={onRetry}>重新查询</button>}
  </div>
);

export const GraphResultNotice = ({ meta }: { meta: GraphPageMeta | undefined }) => {
  if (meta?.truncated !== true) return null;
  return (
    <div className="graph-notice graph-notice--warning" role="status" aria-label="图谱结果状态">
      <div><strong>结果已截断</strong><span>请缩小过滤范围或降低展开深度。</span></div>
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
