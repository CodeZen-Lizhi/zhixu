import { type KeyboardEvent, useEffect, useRef, useState } from "react";

import type { GraphEdge, GraphNode, GraphNodeRef } from "../../api/graph";
import { useGraphNodeDetail, useGraphRelationDetail, useGraphRelationEvidence } from "./queries";
import { GraphErrorNotice, GraphLoading } from "./feedback";

export type GraphSelection =
  | { kind: "node"; ref: GraphNodeRef }
  | { kind: "relation"; relationId: string }
  | null;

const formatTimestamp = (value: string): string => new Intl.DateTimeFormat("zh-CN", {
  year: "numeric", month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit", second: "2-digit",
}).format(new Date(value));

const NodeDetail = ({ node, incidentCount }: { node: GraphNode; incidentCount: number }) => (
  <div className="graph-detail__body">
    <div className={`graph-detail__type graph-detail__type--${node.type.toLowerCase()}`}>{node.type === "TOPIC" ? "Topic" : "Claim"}</div>
    <h2>{node.type === "TOPIC" ? node.name : node.statement}</h2>
    {node.type === "TOPIC" && node.description !== "" ? <p>{node.description}</p> : null}
    <dl className="graph-detail__facts">
      <div><dt>状态</dt><dd>{node.type === "TOPIC" ? node.topicStatus : node.claimStatus}</dd></div>
      <div><dt>版本</dt><dd>{node.version}</dd></div>
      <div><dt>当前结果内关系</dt><dd>{incidentCount}</dd></div>
      <div><dt>更新时间</dt><dd>{formatTimestamp(node.updatedAt)}</dd></div>
      {node.type === "CLAIM" ? <div><dt>置信度</dt><dd>{node.confidence === null ? "未提供" : node.confidence.toFixed(2)}</dd></div> : null}
    </dl>
    {node.type === "CLAIM" ? <details className="graph-detail__applicability"><summary>适用条件</summary><pre>{JSON.stringify(node.applicability.value, null, 2)}</pre></details> : null}
  </div>
);

export const GraphDetailPanel = ({
  workspaceId,
  selection,
  edges,
  pinned,
  canPin,
  modal,
  onTogglePin,
  onClose,
}: {
  workspaceId: string;
  selection: GraphSelection;
  edges: GraphEdge[];
  pinned: boolean;
  canPin: boolean;
  modal: boolean;
  onTogglePin: () => void;
  onClose: () => void;
}) => {
  const panelRef = useRef<HTMLElement>(null);
  const nodeRef = selection?.kind === "node" ? selection.ref : undefined;
  const relationId = selection?.kind === "relation" ? selection.relationId : "";
  const nodeQuery = useGraphNodeDetail({
    workspaceId: nodeRef === undefined ? "" : workspaceId,
    nodeType: nodeRef?.type ?? "TOPIC",
    nodeId: nodeRef?.id ?? "",
  });
  const relationQuery = useGraphRelationDetail({ workspaceId: relationId === "" ? "" : workspaceId, relationId });
  const [evidenceOpen, setEvidenceOpen] = useState(false);
  const evidenceQuery = useGraphRelationEvidence({
    workspaceId: evidenceOpen && relationId !== "" ? workspaceId : "",
    relationId: evidenceOpen ? relationId : "",
    limit: 20,
  });
  useEffect(() => {
    setEvidenceOpen(false);
    if (selection !== null) panelRef.current?.focus();
  }, [modal, selection]);

  const handleKeyDown = (event: KeyboardEvent<HTMLElement>): void => {
    if (!modal || selection === null) return;
    if (event.key === "Escape") {
      event.preventDefault();
      onClose();
      return;
    }
    if (event.key !== "Tab") return;
    const panel = panelRef.current;
    if (panel === null) return;
    const focusable = [...panel.querySelectorAll<HTMLElement>(
      "button:not([disabled]), a[href], input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex='-1'])",
    )];
    const first = focusable[0];
    const last = focusable.at(-1);
    if (first === undefined || last === undefined) {
      event.preventDefault();
      panel.focus();
      return;
    }
    if (event.shiftKey && (document.activeElement === first || !panel.contains(document.activeElement))) {
      event.preventDefault();
      last.focus();
    } else if (!event.shiftKey && (document.activeElement === last || document.activeElement === panel)) {
      event.preventDefault();
      first.focus();
    }
  };

  const incidentCount = nodeRef === undefined ? 0 : edges.filter((edge) =>
    (edge.source.type === nodeRef.type && edge.source.id === nodeRef.id)
    || (edge.target.type === nodeRef.type && edge.target.id === nodeRef.id)).length;
  const evidence = evidenceQuery.data?.pages.flatMap((page) => page.items) ?? [];

  return (
    <aside
      ref={panelRef}
      tabIndex={selection === null ? undefined : -1}
      className={`graph-detail${selection === null ? "" : " is-open"}${modal && selection !== null ? " is-modal" : ""}`}
      role={modal && selection !== null ? "dialog" : "complementary"}
      {...(modal && selection !== null ? { "aria-modal": true } : {})}
      aria-label="图谱详情"
      onKeyDown={handleKeyDown}
    >
      <div className="graph-detail__header">
        <div><span className="graph-kicker">Inspector</span><strong>{selection === null ? "未选择对象" : selection.kind === "node" ? "节点详情" : "关系详情"}</strong></div>
        {selection === null ? null : <button type="button" className="graph-icon-button" aria-label="关闭详情" title="关闭详情" onClick={onClose}>×</button>}
      </div>
      {selection === null ? <div className="graph-detail__empty"><p>选择图中的节点或关系，查看服务端事实与证据。</p></div> : null}
      {selection?.kind === "node" ? <>
        {nodeQuery.isPending ? <GraphLoading label="正在加载节点详情" /> : null}
        {nodeQuery.isError ? <GraphErrorNotice error={nodeQuery.error} onRetry={() => { void nodeQuery.refetch(); }} /> : null}
        {nodeQuery.data === undefined ? null : <NodeDetail node={nodeQuery.data} incidentCount={incidentCount} />}
        {nodeQuery.data === undefined || !canPin ? null : <button type="button" className="graph-secondary-command" aria-pressed={pinned} onClick={onTogglePin}>{pinned ? "解除节点锁定" : "锁定节点位置"}</button>}
      </> : null}
      {selection?.kind === "relation" ? <>
        {relationQuery.isPending ? <GraphLoading label="正在加载关系详情" /> : null}
        {relationQuery.isError ? <GraphErrorNotice error={relationQuery.error} onRetry={() => { void relationQuery.refetch(); }} /> : null}
        {relationQuery.data === undefined ? null : <div className="graph-detail__body">
          <div className={`graph-detail__type graph-detail__type--${relationQuery.data.edge.status.toLowerCase()}`}>Relation · {relationQuery.data.edge.status}</div>
          <h2>{relationQuery.data.edge.type}</h2>
          <dl className="graph-detail__facts">
            <div><dt>Source</dt><dd>{relationQuery.data.edge.source.type} · {relationQuery.data.edge.source.id}</dd></div>
            <div><dt>Target</dt><dd>{relationQuery.data.edge.target.type} · {relationQuery.data.edge.target.id}</dd></div>
            <div><dt>Traversal</dt><dd>{relationQuery.data.edge.traversal}</dd></div>
            <div><dt>置信度</dt><dd>{relationQuery.data.edge.confidence === null ? "未提供" : relationQuery.data.edge.confidence.toFixed(2)}</dd></div>
            <div><dt>版本</dt><dd>{relationQuery.data.edge.version}</dd></div>
            <div><dt>Evidence</dt><dd>{relationQuery.data.edge.evidenceCount}</dd></div>
          </dl>
          {relationQuery.data.confirmation === null ? null : <p className="graph-detail__confirmation">{relationQuery.data.confirmation.method} · {relationQuery.data.confirmation.reference}</p>}
          {relationQuery.data.edge.evidenceCount === 0 ? <p className="graph-muted">当前关系没有 Evidence。</p> : <button type="button" className="graph-secondary-command" aria-expanded={evidenceOpen} onClick={() => setEvidenceOpen((open) => !open)}>{evidenceOpen ? "收起关系证据" : "加载关系证据"}</button>}
          {!evidenceOpen ? null : <div className="graph-evidence" aria-live="polite">
            {evidenceQuery.isPending ? <GraphLoading label="正在加载关系证据" /> : null}
            {evidenceQuery.isError ? <GraphErrorNotice error={evidenceQuery.error} onRetry={() => { void evidenceQuery.refetch(); }} /> : null}
            {evidence.map((item) => <article key={item.id}><p>{item.reason}</p><dl><div><dt>确认</dt><dd>{item.confirmation?.method ?? "未确认"}</dd></div><div><dt>时间</dt><dd>{formatTimestamp(item.createdAt)}</dd></div></dl><a href={item.spanHref} target="_blank" rel="noreferrer">打开来源段落</a></article>)}
            {evidenceQuery.hasNextPage ? <button type="button" className="graph-text-button" disabled={evidenceQuery.isFetchingNextPage} onClick={() => { void evidenceQuery.fetchNextPage(); }}>{evidenceQuery.isFetchingNextPage ? "加载中" : "加载更多证据"}</button> : null}
          </div>}
        </div>}
      </> : null}
    </aside>
  );
};
