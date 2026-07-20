import { useQueryClient } from "@tanstack/react-query";
import { useEffect, useMemo, useRef, useState, useSyncExternalStore } from "react";
import { Link, useSearchParams } from "react-router-dom";

import "./graph.css";

import { useActiveWorkspaceId } from "../../app/active-workspace";
import type { GraphNodeRef } from "../../api/graph";
import { GraphCanvas } from "./GraphCanvas";
import { GraphDetailPanel, type GraphSelection } from "./GraphDetailPanel";
import { GraphFilterPanel } from "./GraphFilterPanel";
import { GraphErrorNotice, GraphLoading, GraphResultNotice } from "./feedback";
import { graphDepths, graphModes, graphTraversalDirections, type GraphMode } from "./options";
import {
  clearGraphWorkspaceDetails,
  useGraphGlobal,
  useGraphNeighborhood,
  useGraphNodeSearch,
  useGraphPath,
} from "./queries";
import { parseGraphUrlState, serializeGraphUrlState, type GraphUrlState } from "./url-state";
import {
  createGraphLayout,
  graphNodeKey,
  graphNodeLabel,
  projectGraphGlobalPages,
  projectGraphNeighborhoodPages,
  projectGraphPath,
  type GraphLockedPositions,
  type GraphViewEdge,
  type GraphViewNode,
} from "./view-model";

type SearchTarget = "center" | "from" | "to";

const modeLabels: Record<GraphMode, string> = { global: "全局", local: "局部", path: "路径" };
const directionLabels = { BOTH: "双向探索", OUTBOUND: "仅出向", INBOUND: "仅入向" } as const;
const compactGraphQuery = "(max-width: 1080px)";

const subscribeCompactGraph = (onStoreChange: () => void): (() => void) => {
  if (typeof window.matchMedia !== "function") return () => undefined;
  const query = window.matchMedia(compactGraphQuery);
  query.addEventListener("change", onStoreChange);
  return () => query.removeEventListener("change", onStoreChange);
};

const getCompactGraphSnapshot = (): boolean =>
  typeof window.matchMedia === "function" && window.matchMedia(compactGraphQuery).matches;

const sameRef = (left: GraphNodeRef | null, right: GraphNodeRef | null): boolean =>
  left !== null && right !== null && left.type === right.type && left.id === right.id;

const emptyRef: GraphNodeRef = { type: "TOPIC", id: "" };
const emptyLockedPositions: GraphLockedPositions = {};

const SearchPanel = ({
  mode,
  target,
  onTargetChange,
  query,
  onQueryChange,
  search,
  onSelect,
}: {
  mode: GraphMode;
  target: SearchTarget;
  onTargetChange: (target: SearchTarget) => void;
  query: string;
  onQueryChange: (query: string) => void;
  search: ReturnType<typeof useGraphNodeSearch>;
  onSelect: (ref: GraphNodeRef) => void;
}) => (
  <section className="graph-search" aria-labelledby="graph-search-title">
    <div className="graph-panel-heading"><span className="graph-kicker">Node Search</span><h2 id="graph-search-title">定位正式节点</h2></div>
    {mode === "path" ? <div className="graph-target-switch" aria-label="路径搜索目标">
      <button type="button" aria-pressed={target === "from"} onClick={() => onTargetChange("from")}>起点</button>
      <button type="button" aria-pressed={target === "to"} onClick={() => onTargetChange("to")}>终点</button>
    </div> : null}
    <label className="graph-search__input"><span>节点名称或主张</span><input value={query} onChange={(event) => onQueryChange(event.target.value)} placeholder="输入 Topic 或 Claim" /></label>
    {search.isFetching ? <p className="graph-search__state" role="status">正在搜索</p> : null}
    {search.isError ? <GraphErrorNotice error={search.error} /> : null}
    {search.data?.matches.length === 0 ? <p className="graph-muted">没有匹配的正式节点。</p> : null}
    {search.data === undefined ? null : <ul className="graph-search-results">
      {search.data.matches.map((match) => <li key={graphNodeKey(match.node)}><button type="button" onClick={() => onSelect({ type: match.node.type, id: match.node.id })}><span>{match.kind}</span><strong>{graphNodeLabel(match.node)}</strong><small>{match.node.type}</small></button></li>)}
    </ul>}
  </section>
);

const GraphLegend = () => (
  <section className="graph-legend" aria-labelledby="graph-legend-title">
    <div className="graph-panel-heading"><span className="graph-kicker">Legend</span><h2 id="graph-legend-title">图例</h2></div>
    <ul><li><span className="graph-legend__node graph-legend__node--topic" aria-hidden="true" />Topic</li><li><span className="graph-legend__node graph-legend__node--claim" aria-hidden="true" />Claim</li><li><span className="graph-legend__edge" aria-hidden="true" />Confirmed</li><li><span className="graph-legend__edge graph-legend__edge--stale" aria-hidden="true" />Stale</li></ul>
  </section>
);

const EmptyGraph = ({ mode }: { mode: GraphMode }) => (
  <section className="graph-empty">
    <span className="graph-kicker">Empty</span>
    <h2>{mode === "global" ? "当前筛选下没有全局 Topic 聚类" : "当前范围没有可展示的正式关系"}</h2>
  </section>
);

export const GraphPage = () => {
  const workspaceId = useActiveWorkspaceId();
  const queryClient = useQueryClient();
  const [parameters, setParameters] = useSearchParams();
  const urlState = useMemo(() => parseGraphUrlState(parameters), [parameters]);
  const graphIdentity = `${workspaceId}\0${parameters.toString()}`;
  const setUrlState = (next: GraphUrlState) => setParameters(serializeGraphUrlState(next));
  const filter = urlState.filter;
  const localReady = urlState.mode === "local" && urlState.center !== null;
  const pathReady = urlState.mode === "path" && urlState.from !== null && urlState.to !== null && !sameRef(urlState.from, urlState.to);

  const globalQuery = useGraphGlobal({
    workspaceId: urlState.mode === "global" ? workspaceId : "",
    filter,
    limit: 25,
  });
  const neighborhoodQuery = useGraphNeighborhood({
    workspaceId: localReady ? workspaceId : "",
    center: urlState.center ?? emptyRef,
    depth: urlState.depth,
    direction: urlState.direction,
    filter,
    limit: 25,
    maxNodes: 500,
    maxEdges: 1000,
    maxFrontier: 500,
  });
  const pathQuery = useGraphPath({
    workspaceId: pathReady ? workspaceId : "",
    from: urlState.from ?? emptyRef,
    to: urlState.to ?? emptyRef,
    direction: urlState.direction,
    relationTypes: filter.relationTypes,
    maxDepth: 6,
    maxVisited: 500,
  });
  const [searchText, setSearchText] = useState("");
  const [searchTarget, setSearchTarget] = useState<SearchTarget>("center");
  const searchQuery = useGraphNodeSearch({ workspaceId, query: searchText, limit: 20 });

  const model = useMemo(() => {
    if (urlState.mode === "global") return projectGraphGlobalPages(globalQuery.data?.pages ?? []);
    if (urlState.mode === "local") return projectGraphNeighborhoodPages(neighborhoodQuery.data?.pages ?? []);
    return pathQuery.data === undefined ? { nodes: [], edges: [] } : projectGraphPath(pathQuery.data);
  }, [globalQuery.data, neighborhoodQuery.data, pathQuery.data, urlState.mode]);
  const meta = urlState.mode === "global"
    ? globalQuery.data?.pages.at(-1)?.meta
    : urlState.mode === "local" ? neighborhoodQuery.data?.pages.at(-1)?.meta : undefined;
  const queryPending = urlState.mode === "global" ? globalQuery.isPending : urlState.mode === "local" ? localReady && neighborhoodQuery.isPending : pathReady && pathQuery.isPending;
  const queryError = urlState.mode === "global" ? globalQuery.error : urlState.mode === "local" ? neighborhoodQuery.error : pathQuery.error;
  const [selection, setSelection] = useState<GraphSelection>(null);
  const detailTrigger = useRef<HTMLElement | null>(null);
  const [lockedPositions, setLockedPositions] = useState<GraphLockedPositions>({});
  const [fixedLayout, setFixedLayout] = useState(false);
  const [sessionIdentity, setSessionIdentity] = useState(graphIdentity);
  const [detailCacheClearRequest, setDetailCacheClearRequest] = useState({ workspaceId: "", sequence: 0 });
  const compactDetail = useSyncExternalStore(subscribeCompactGraph, getCompactGraphSnapshot, () => false);
  const sessionActive = sessionIdentity === graphIdentity;
  const activeSelection = sessionActive ? selection : null;
  const activeLockedPositions = sessionActive ? lockedPositions : emptyLockedPositions;
  const activeFixedLayout = sessionActive ? fixedLayout : false;

  useEffect(() => {
    setSelection(null);
    setLockedPositions({});
    setFixedLayout(false);
    setSessionIdentity(graphIdentity);
  }, [graphIdentity]);
  useEffect(() => setSearchTarget(urlState.mode === "path" ? "from" : "center"), [urlState.mode]);
  useEffect(() => {
    if (detailCacheClearRequest.sequence === 0) return;
    clearGraphWorkspaceDetails(queryClient, detailCacheClearRequest.workspaceId);
  }, [detailCacheClearRequest, queryClient]);
  useEffect(() => {
    const trigger = detailTrigger.current;
    if (activeSelection === null && trigger?.isConnected === true) {
      trigger.focus();
      detailTrigger.current = null;
    }
  }, [activeSelection]);

  const changeMode = (mode: GraphMode) => setUrlState({ ...urlState, mode });
  const selectSearchResult = (ref: GraphNodeRef) => {
    if (urlState.mode !== "path") {
      setUrlState({ ...urlState, mode: "local", center: ref, depth: 1, direction: "BOTH" });
    } else if (searchTarget === "from") {
      setUrlState({ ...urlState, from: ref });
      setSearchTarget("to");
    } else {
      setUrlState({ ...urlState, to: ref });
    }
    setSearchText("");
  };
  const selectNode = (node: GraphViewNode, trigger: HTMLElement) => {
    if (urlState.mode === "global") {
      setUrlState({ ...urlState, mode: "local", center: node.ref, depth: 1, direction: "BOTH" });
      return;
    }
    detailTrigger.current = trigger;
    setSelection({ kind: "node", ref: node.ref });
  };
  const selectEdge = (edge: GraphViewEdge, trigger: HTMLElement) => {
    detailTrigger.current = trigger;
    setSelection({ kind: "relation", relationId: edge.edge.relationId });
  };
  const closeDetail = () => {
    setSelection(null);
  };
  const resetActiveToFirstPage = () => {
    detailTrigger.current = null;
    setSelection(null);
    setLockedPositions({});
    setFixedLayout(false);
    setDetailCacheClearRequest((current) => ({ workspaceId, sequence: current.sequence + 1 }));
    if (urlState.mode === "global") void globalQuery.resetToFirstPage();
    else if (urlState.mode === "local") void neighborhoodQuery.resetToFirstPage();
  };
  const retryActive = () => {
    if (urlState.mode === "global") void globalQuery.refetch();
    else if (urlState.mode === "local") void neighborhoodQuery.refetch();
    else void pathQuery.refetch();
  };
  const loadNext = () => {
    if (urlState.mode === "global") void globalQuery.fetchNextPage();
    else if (urlState.mode === "local") void neighborhoodQuery.fetchNextPage();
  };
  const hasNextPage = urlState.mode === "global" ? globalQuery.hasNextPage : urlState.mode === "local" ? neighborhoodQuery.hasNextPage : false;
  const fetchingNextPage = urlState.mode === "global" ? globalQuery.isFetchingNextPage : urlState.mode === "local" ? neighborhoodQuery.isFetchingNextPage : false;
  const graphLayout = useMemo(
    () => createGraphLayout(model, urlState.mode, activeLockedPositions),
    [activeLockedPositions, model, urlState.mode],
  );
  const displayedLockedPositions = graphLayout.kind === "canvas" ? activeLockedPositions : emptyLockedPositions;
  const displayedFixedLayout = graphLayout.kind === "canvas" && activeFixedLayout;
  const selectedNodeKey = activeSelection?.kind === "node" ? graphNodeKey(activeSelection.ref) : undefined;
  const selectedEdgeKey = activeSelection?.kind === "relation" ? activeSelection.relationId : undefined;
  const selectedNodePosition = graphLayout.kind === "canvas" && selectedNodeKey !== undefined
    ? graphLayout.nodes.find((node) => node.key === selectedNodeKey)?.position
    : undefined;
  const detailModalOpen = compactDetail && activeSelection !== null;

  useEffect(() => {
    if (graphLayout.kind !== "list") return;
    setLockedPositions((current) => Object.keys(current).length === 0 ? current : {});
    setFixedLayout((current) => current ? false : current);
  }, [graphLayout.kind]);

  const toggleSelectedNodePin = () => {
    if (selectedNodeKey === undefined || selectedNodePosition === undefined) return;
    setLockedPositions((current) => {
      if (current[selectedNodeKey] !== undefined) {
        return Object.fromEntries(Object.entries(current).filter(([key]) => key !== selectedNodeKey));
      }
      return { ...current, [selectedNodeKey]: selectedNodePosition };
    });
    setFixedLayout(false);
  };
  const toggleFixedLayout = () => {
    if (activeFixedLayout) {
      setFixedLayout(false);
      setLockedPositions({});
      return;
    }
    if (graphLayout.kind === "canvas") {
      setLockedPositions(Object.fromEntries(graphLayout.nodes.map((node) => [node.key, node.position])));
      setFixedLayout(true);
    }
  };

  if (workspaceId === "") {
    return <section className="graph-gate"><span className="graph-kicker">知序 · Graph</span><h1>先连接一个 Workspace</h1><p>图谱查询只读取当前 Workspace 中的正式 Topic、Claim 与 Relation。</p><Link to="/">返回 Workspace 配置</Link></section>;
  }

  const pathNotFound = pathQuery.data?.status === "not_found" ? pathQuery.data : undefined;
  const missingScope = urlState.mode === "local" && !localReady || urlState.mode === "path" && !pathReady;

  return <div className="graph-page">
    <header className="graph-header" {...(detailModalOpen ? { inert: true } : {})}>
      <div><Link to="/" className="graph-brand">知序 · ZHIXU</Link><h1>知识图谱</h1></div>
      <nav aria-label="产品导航"><Link to="/">Workspace</Link><Link to="/chat">证据研究台</Link></nav>
      <div className="graph-workspace-id"><span>Workspace</span><code>{workspaceId}</code></div>
    </header>
    <div className="graph-mode-switch" aria-label="图谱模式" {...(detailModalOpen ? { inert: true } : {})}>
      {graphModes.map((mode) => <button type="button" key={mode} aria-pressed={urlState.mode === mode} onClick={() => changeMode(mode)}>{modeLabels[mode]}</button>)}
    </div>
    <div className="graph-layout">
      <aside className="graph-controls" aria-label="图谱查询控制" {...(detailModalOpen ? { inert: true } : {})}>
        <SearchPanel mode={urlState.mode} target={searchTarget} onTargetChange={setSearchTarget} query={searchText} onQueryChange={setSearchText} search={searchQuery} onSelect={selectSearchResult} />
        {urlState.mode === "local" ? <section className="graph-scope"><div className="graph-panel-heading"><span className="graph-kicker">Neighborhood</span><h2>展开范围</h2></div><label><span>深度</span><select value={urlState.depth} onChange={(event) => setUrlState({ ...urlState, depth: Number(event.target.value) as typeof urlState.depth })}>{graphDepths.map((depth) => <option key={depth} value={depth}>{depth} 跳</option>)}</select></label><label><span>方向</span><select value={urlState.direction} onChange={(event) => setUrlState({ ...urlState, direction: event.target.value as typeof urlState.direction })}>{graphTraversalDirections.map((direction) => <option key={direction} value={direction}>{directionLabels[direction]}</option>)}</select></label>{urlState.center === null ? <p className="graph-muted">通过节点搜索选择中心。</p> : <div className="graph-endpoint"><span>中心</span><code>{urlState.center.type} · {urlState.center.id}</code></div>}</section> : null}
        {urlState.mode === "path" ? <section className="graph-scope"><div className="graph-panel-heading"><span className="graph-kicker">Shortest Path</span><h2>路径端点</h2></div>{(["from", "to"] as const).map((endpoint) => { const ref = urlState[endpoint]; return <div className="graph-endpoint" key={endpoint}><span>{endpoint === "from" ? "起点" : "终点"}</span><code>{ref === null ? "尚未选择" : `${ref.type} · ${ref.id}`}</code>{ref === null ? null : <button type="button" className="graph-icon-button" aria-label={`清除${endpoint === "from" ? "起点" : "终点"}`} onClick={() => setUrlState({ ...urlState, [endpoint]: null })}>×</button>}</div>; })}<label><span>方向</span><select value={urlState.direction} onChange={(event) => setUrlState({ ...urlState, direction: event.target.value as typeof urlState.direction })}>{graphTraversalDirections.map((direction) => <option key={direction} value={direction}>{directionLabels[direction]}</option>)}</select></label></section> : null}
        <GraphFilterPanel filter={urlState.filter} pathOnly={urlState.mode === "path"} onChange={(nextFilter) => setUrlState({ ...urlState, filter: nextFilter })} />
        <GraphLegend />
      </aside>
      <main className="graph-workspace" {...(detailModalOpen ? { inert: true } : {})}>
        <div className="graph-toolbar"><div><span>{modeLabels[urlState.mode]}</span><strong>{String(model.nodes.length)} nodes · {String(model.edges.length)} edges</strong></div><button type="button" className="graph-secondary-command" aria-pressed={displayedFixedLayout} disabled={model.nodes.length === 0 || graphLayout.kind !== "canvas"} onClick={toggleFixedLayout}>{displayedFixedLayout ? "释放固定布局" : "固定当前布局"}</button></div>
        <GraphResultNotice meta={meta} />
        {queryError instanceof Error ? <GraphErrorNotice error={queryError} onRetry={retryActive} {...(urlState.mode === "global" || urlState.mode === "local"
          ? { onResetToFirstPage: resetActiveToFirstPage } : {})} /> : null}
        {queryPending ? <GraphLoading /> : null}
        {missingScope ? <section className="graph-empty"><span className="graph-kicker">Scope</span><h2>{urlState.mode === "local" ? "选择一个中心节点" : "选择不同的起点和终点"}</h2></section> : null}
        {pathNotFound === undefined ? null : <section className="graph-path-result" role="status" aria-label="路径查询结果"><strong>未找到正式关系路径</strong><span>已探索 {pathNotFound.exploredNodes} 个节点。</span>{pathNotFound.commonTopicSuggestions.map((topic) => <button type="button" className="graph-text-button" key={topic.id} onClick={() => setUrlState({ ...urlState, mode: "local", center: { type: "TOPIC", id: topic.id }, depth: 1 })}>共同 Topic 建议：{topic.name}</button>)}</section>}
        {!queryPending && queryError === null && !missingScope && pathNotFound === undefined && model.nodes.length === 0 ? <EmptyGraph mode={urlState.mode} /> : null}
        {model.nodes.length === 0 ? null : <GraphCanvas model={model} mode={urlState.mode} lockedPositions={displayedLockedPositions} selectedNodeKey={selectedNodeKey} selectedEdgeKey={selectedEdgeKey} onSelectNode={selectNode} onSelectEdge={selectEdge} />}
        {hasNextPage ? <button type="button" className="graph-load-more" disabled={fetchingNextPage} onClick={loadNext}>{fetchingNextPage ? "正在加载" : "加载下一页"}</button> : null}
      </main>
      <GraphDetailPanel workspaceId={workspaceId} selection={activeSelection} edges={model.edges.map((edge) => edge.edge)} pinned={selectedNodeKey !== undefined && displayedLockedPositions[selectedNodeKey] !== undefined} canPin={selectedNodePosition !== undefined} modal={compactDetail} onTogglePin={toggleSelectedNodePin} onClose={closeDetail} />
    </div>
  </div>;
};
