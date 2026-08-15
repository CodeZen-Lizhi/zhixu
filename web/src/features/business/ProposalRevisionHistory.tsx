import { useQuery } from "@tanstack/react-query";
import { ArrowLeft, CheckCircle2, Clock3, GitBranch, History, RotateCcw } from "lucide-react";
import { lazy, Suspense, useState } from "react";
import { Link } from "react-router-dom";

import {
  getProposalRevision,
  type ProposalRevisionHistoryBinding,
  type ProposalRevisionHistoryItem,
} from "../../api/business-revisions";
import { Badge, Button, ErrorState, UnavailableState } from "../../shared/ui";
import "./proposal-revision.css";

const MonacoDiffViewer = lazy(() => import("../../shared/MonacoDiffViewer").then((module) => ({ default: module.MonacoDiffViewer })));

const decisionLabel = (item: ProposalRevisionHistoryItem): string =>
  item.approval === undefined ? "未审批" : item.approval.decision === "approved" ? "已批准" : "已驳回";

export interface ProposalRevisionHistorySelectorProps {
  items: ProposalRevisionHistoryItem[];
  selectedRevisionId: string;
  loading: boolean;
  loadingMore: boolean;
  hasMore: boolean;
  errorMessage?: string;
  onSelect: (revisionId: string) => void;
  onRetry: () => void;
  onLoadMore: () => void;
}

export const ProposalRevisionHistorySelector = ({
  items,
  selectedRevisionId,
  loading,
  loadingMore,
  hasMore,
  errorMessage,
  onSelect,
  onRetry,
  onLoadMore,
}: ProposalRevisionHistorySelectorProps) => <section className="proposal-revision-history-selector" aria-label="修订历史">
  <header>
    <strong><History size={15} />修订历史</strong>
    <div className="proposal-revision-history-selector__actions">
      {errorMessage === undefined ? null : <span role="alert">{errorMessage}</span>}
      {errorMessage === undefined ? null : <Button variant="ghost" size="sm" onClick={onRetry}><RotateCcw size={14} />重试</Button>}
      {hasMore ? <Button variant="ghost" size="sm" onClick={onLoadMore} disabled={loadingMore}>{loadingMore ? "读取中..." : "更早版本"}</Button> : null}
    </div>
  </header>
  <ol className="proposal-revision-history-list">
    {loading && items.length === 0 ? <li className="proposal-revision-history-list__loading">正在读取...</li> : null}
    {items.map((item) => <li key={item.revisionId}>
      <button
        type="button"
        className="proposal-revision-history-list__item"
        aria-current={item.revisionId === selectedRevisionId ? "true" : undefined}
        aria-label={`Revision #${String(item.revisionNo)}，${item.current ? "当前" : "历史"}，${decisionLabel(item)}`}
        onClick={() => onSelect(item.revisionId)}
      >
        <strong>#{item.revisionNo}</strong>
        <span>{item.current ? "当前" : "历史"} · {decisionLabel(item)}</span>
        <time dateTime={item.createdAt}>{new Date(item.createdAt).toLocaleString("zh-CN")}</time>
      </button>
    </li>)}
  </ol>
</section>;

export interface ProposalRevisionHistoryViewProps {
  binding: ProposalRevisionHistoryBinding;
  item: ProposalRevisionHistoryItem;
  onReturnToLatest: () => void;
}

export const ProposalRevisionHistoryView = ({ binding, item, onReturnToLatest }: ProposalRevisionHistoryViewProps) => {
  const [diffError, setDiffError] = useState("");
  const query = useQuery({
    queryKey: ["business", binding.workspaceId, "proposal-revision", binding.proposalId, item.revisionId],
    queryFn: ({ signal }) => getProposalRevision({
      ...binding,
      revisionId: item.revisionId,
      revisionNo: item.revisionNo,
      baseHash: item.baseHash,
      changeHash: item.changeHash,
    }, signal),
    retry: false,
    gcTime: 0,
  });

  return <section className="proposal-revision-history-view" aria-label={`历史 Revision #${String(item.revisionNo)}`}>
    <header className="proposal-revision-history-view__header">
      <div>
        <p className="eyebrow">只读历史</p>
        <h2>历史 Revision #{item.revisionNo}</h2>
        <p>该视图只展示创建时冻结的基线与建议正文，不读取当前 Workspace。</p>
      </div>
      <Button variant="secondary" onClick={onReturnToLatest}><ArrowLeft size={15} />返回最新版本</Button>
    </header>

    {query.isPending ? <p className="proposal-revision-loading">正在读取历史 Revision...</p> : null}
    {query.isError ? <ErrorState title="历史 Revision 读取失败" description={query.error.message} onRetry={() => void query.refetch()} /> : null}
    {query.data ? <div className="proposal-revision-history-layout">
      <main>
        <div className="proposal-revision-editor-heading">
          <h3>冻结基线 -&gt; Revision #{item.revisionNo}</h3>
          <span>{item.baseAvailable ? "不可变快照" : "基线快照不可用"}</span>
        </div>
        {!query.data.baseAvailable || query.data.baseSnapshot === undefined
          ? <UnavailableState title="历史基线不可用" description="该 Revision 没有可验证的创建时基线快照，因此不会用今天的 Workspace 正文代替。" />
          : diffError !== ""
            ? <ErrorState title="历史差异查看器加载失败" description={diffError} onRetry={() => setDiffError("")} />
            : <div className="proposal-revision-diff-shell">
              <Suspense fallback={<p className="proposal-revision-loading">正在加载历史差异查看器...</p>}>
                <MonacoDiffViewer
                  identityKey={`${binding.workspaceId}:${binding.proposalId}:${item.revisionId}:history`}
                  original={query.data.baseSnapshot.content}
                  modified={query.data.revision.content}
                  originalModelPath={`inmemory://zhixu/workspaces/${binding.workspaceId}/proposals/${binding.proposalId}/revisions/${item.revisionId}/historical-base.md`}
                  modifiedModelPath={`inmemory://zhixu/workspaces/${binding.workspaceId}/proposals/${binding.proposalId}/revisions/${item.revisionId}/historical-proposed.md`}
                  language="markdown"
                  onReady={() => setDiffError("")}
                  onError={(error) => setDiffError(error.message)}
                />
              </Suspense>
            </div>}
      </main>
      <aside className="proposal-revision-history-facts">
        <div>
          <span>版本状态</span>
          <strong><Badge tone="neutral">历史只读</Badge></strong>
        </div>
        <div><span>Revision ID</span><strong className="mono">{query.data.revision.id}</strong></div>
        <div><span>Change Hash</span><strong className="mono">{query.data.revision.changeHash}</strong></div>
        <div><span>创建时间</span><strong><Clock3 size={14} />{new Date(query.data.revision.createdAt).toLocaleString("zh-CN")}</strong></div>
        {query.data.lineage ? <div><span>来源 Revision</span><strong><GitBranch size={14} />#{item.revisionNo - 1} · <code>{query.data.lineage.sourceRevisionId}</code></strong></div> : null}
        <div><span>审批</span><strong>{query.data.approval === undefined ? "未审批" : <><CheckCircle2 size={14} />{query.data.approval.decision === "approved" ? "已批准" : "已驳回"}</>}</strong></div>
        {query.data.approval ? <div><span>决策时间</span><strong>{new Date(query.data.approval.decidedAt).toLocaleString("zh-CN")}</strong></div> : null}
        {query.data.workflow ? <div><span>写回 Workflow</span><strong><Link className="table-link" to={`/workflows/${query.data.workflow.id}`}>{query.data.workflow.id}</Link></strong></div> : null}
        <div><span>证据摘要</span><strong>{query.data.revision.evidenceSummary}</strong></div>
        <div><span>风险说明</span><strong>{query.data.revision.risk}</strong></div>
        <div><span>回滚计划</span><strong>{query.data.revision.rollbackPlan}</strong></div>
      </aside>
    </div> : null}
  </section>;
};
