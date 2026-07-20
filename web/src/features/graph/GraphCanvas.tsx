import { useId, useMemo, useState } from "react";

import {
  createGraphLayout,
  graphNodeRefLabel,
  type GraphFallbackReason,
  type GraphLayoutMode,
  type GraphLockedPositions,
  type GraphPoint,
  type GraphViewEdge,
  type GraphViewMode,
  type GraphViewModel,
  type GraphViewNode,
} from "./view-model";

export interface GraphCanvasProps {
  model: GraphViewModel;
  mode: GraphLayoutMode;
  defaultView?: GraphViewMode;
  lockedPositions?: GraphLockedPositions;
  selectedNodeKey?: string | undefined;
  selectedEdgeKey?: string | undefined;
  onSelectNode?: (node: GraphViewNode, trigger: HTMLElement) => void;
  onSelectEdge?: (edge: GraphViewEdge, trigger: HTMLElement) => void;
}

const fallbackMessages: Record<GraphFallbackReason, string> = {
  NODE_LIMIT: "节点超过画布上限（60），已切换到完整列表。",
  EDGE_LIMIT: "关系超过画布上限（100），已切换到完整列表。",
  LAYOUT_ERROR: "画布布局无法完成，已切换到完整列表。",
};

const positionStyle = (point: GraphPoint, width: number, height: number) => ({
  left: `${String((point.x / width) * 100)}%`,
  top: `${String((point.y / height) * 100)}%`,
});

interface GraphListProps {
  model: GraphViewModel;
  lockedPositions: GraphLockedPositions;
  selectedNodeKey?: string | undefined;
  selectedEdgeKey?: string | undefined;
  onNode: (node: GraphViewNode, trigger: HTMLElement) => void;
  onEdge: (edge: GraphViewEdge, trigger: HTMLElement) => void;
}

const GraphList = ({
  model,
  lockedPositions,
  selectedNodeKey,
  selectedEdgeKey,
  onNode,
  onEdge,
}: GraphListProps) => {
  const headingId = `graph-list-${useId().replaceAll(":", "")}`;
  const nodesByKey = new Map(model.nodes.map((node) => [node.key, node]));
  return (
    <div className="graph-list" aria-label="图谱完整列表">
      <section className="graph-list__section" aria-labelledby={`${headingId}-nodes`}>
        <h3 id={`${headingId}-nodes`}>节点（{model.nodes.length}）</h3>
        {model.nodes.length === 0 ? <p className="graph-list__empty">暂无节点</p> : (
          <ol className="graph-list__items">
            {model.nodes.map((node) => {
              const locked = lockedPositions[node.key] !== undefined;
              return (
                <li key={node.key} className="graph-list__item">
                  <button
                    type="button"
                    className={`graph-list__button graph-list__button--${node.ref.type.toLowerCase()}`}
                    data-selected={selectedNodeKey === node.key ? "true" : "false"}
                    data-locked={locked ? "true" : "false"}
                    aria-label={`${locked ? "已锁定，" : ""}选择${node.typeLabel}：${node.label}`}
                    onClick={(event) => { onNode(node, event.currentTarget); }}
                  >
                    <span className="graph-list__type">{node.typeLabel}</span>
                    <span className="graph-list__label">{node.label}</span>
                    {locked ? <span className="graph-list__meta">已锁定</span> : node.boundary ? <span className="graph-list__meta">边界</span> : null}
                  </button>
                </li>
              );
            })}
          </ol>
        )}
      </section>
      <section className="graph-list__section" aria-labelledby={`${headingId}-edges`}>
        <h3 id={`${headingId}-edges`}>关系（{model.edges.length}）</h3>
        {model.edges.length === 0 ? <p className="graph-list__empty">暂无关系</p> : (
          <ul className="graph-list__items">
            {model.edges.map((edge) => {
              const source = nodesByKey.get(edge.sourceKey);
              const target = nodesByKey.get(edge.targetKey);
              return (
                <li key={edge.key} className="graph-list__item">
                  <button
                    type="button"
                    className="graph-list__button graph-list__button--edge"
                    data-selected={selectedEdgeKey === edge.key ? "true" : "false"}
                    aria-label={`选择关系：${edge.label}，${edge.statusLabel}`}
                    onClick={(event) => { onEdge(edge, event.currentTarget); }}
                  >
                    <span className="graph-list__label">
                      {source?.label ?? graphNodeRefLabel(edge.edge.source)} → {target?.label ?? graphNodeRefLabel(edge.edge.target)}
                    </span>
                    <span className="graph-list__meta">{edge.label} · {edge.statusLabel}</span>
                  </button>
                </li>
              );
            })}
          </ul>
        )}
      </section>
    </div>
  );
};

export const GraphCanvas = ({
  model,
  mode,
  defaultView = "canvas",
  lockedPositions,
  selectedNodeKey,
  selectedEdgeKey,
  onSelectNode,
  onSelectEdge,
}: GraphCanvasProps) => {
  const markerId = `graph-arrow-${useId().replaceAll(":", "")}`;
  const [requestedView, setRequestedView] = useState<GraphViewMode>(defaultView);
  const activeLockedPositions = lockedPositions ?? {};
  const layout = useMemo(
    () => createGraphLayout(model, mode, activeLockedPositions),
    [activeLockedPositions, mode, model],
  );
  const forcedFallback = layout.kind === "list";
  const currentView: GraphViewMode = forcedFallback ? "list" : requestedView;
  const selectNode = (node: GraphViewNode, trigger: HTMLElement): void => {
    onSelectNode?.(node, trigger);
  };

  const selectEdge = (edge: GraphViewEdge, trigger: HTMLElement): void => {
    onSelectEdge?.(edge, trigger);
  };

  return (
    <section className="graph-visualization" aria-label="知识图谱视图">
      <div className="graph-view-switch" role="group" aria-label="展示方式">
        <button
          type="button"
          className="graph-view-switch__button graph-view-switch__button--canvas"
          aria-pressed={currentView === "canvas"}
          disabled={forcedFallback}
          onClick={() => { setRequestedView("canvas"); }}
        >画布</button>
        <button
          type="button"
          className="graph-view-switch__button graph-view-switch__button--list"
          aria-pressed={currentView === "list"}
          onClick={() => { setRequestedView("list"); }}
        >列表</button>
      </div>

      {layout.kind === "list" ? (
        <p className="graph-fallback" role="status">{fallbackMessages[layout.reason]}</p>
      ) : null}

      {currentView === "canvas" && layout.kind === "canvas" ? (
        <div
          className={`graph-canvas graph-canvas--${mode}`}
          style={{ aspectRatio: `${String(layout.width)} / ${String(layout.height)}` }}
        >
          <svg
            className="graph-canvas__edges"
            viewBox={`0 0 ${String(layout.width)} ${String(layout.height)}`}
            preserveAspectRatio="none"
            aria-hidden="true"
          >
            <defs>
              <marker id={markerId} markerWidth="8" markerHeight="8" refX="7" refY="4" orient="auto">
                <path className="graph-edge__arrow" d="M0,0 L8,4 L0,8 Z" />
              </marker>
            </defs>
            {layout.edges.map((edge) => (
              <line
                key={edge.key}
                className={`graph-edge graph-edge--${edge.edge.status.toLowerCase()}`}
                data-status={edge.edge.status}
                x1={edge.sourcePosition.x}
                y1={edge.sourcePosition.y}
                x2={edge.targetPosition.x}
                y2={edge.targetPosition.y}
                stroke="currentColor"
                strokeWidth="2"
                strokeDasharray={edge.edge.status === "STALE" ? "10 7" : undefined}
                markerEnd={`url(#${markerId})`}
                vectorEffect="non-scaling-stroke"
              />
            ))}
          </svg>
          <div className="graph-canvas__controls">
            {layout.nodes.map((node) => {
              const { position, ...viewNode } = node;
              const locked = activeLockedPositions[node.key] !== undefined;
              return (
                <button
                  key={node.key}
                  type="button"
                  className={`graph-node graph-node--${node.ref.type.toLowerCase()}${node.boundary ? " graph-node--boundary" : ""}`}
                  style={positionStyle(position, layout.width, layout.height)}
                  data-node-type={node.ref.type}
                  data-selected={selectedNodeKey === node.key ? "true" : "false"}
                  data-locked={locked ? "true" : "false"}
                  aria-label={`${locked ? "已锁定，" : ""}选择${node.typeLabel}：${node.label}`}
                  title="选择节点"
                  onClick={(event) => { selectNode(viewNode, event.currentTarget); }}
                >
                  <span className="graph-node__type">{node.typeLabel}</span>
                  <span className="graph-node__label">{node.label}</span>
                  {node.boundary ? <span className="graph-node__meta">边界</span> : null}
                </button>
              );
            })}
            {layout.edges.map((edge) => {
              const viewEdge: GraphViewEdge = {
                key: edge.key,
                sourceKey: edge.sourceKey,
                targetKey: edge.targetKey,
                label: edge.label,
                statusLabel: edge.statusLabel,
                edge: edge.edge,
              };
              return (
                <button
                  key={edge.key}
                  type="button"
                  className={`graph-edge-label graph-edge-label--${edge.edge.status.toLowerCase()}`}
                  style={positionStyle(edge.labelPosition, layout.width, layout.height)}
                  data-selected={selectedEdgeKey === edge.key ? "true" : "false"}
                  aria-label={`选择关系：${edge.label}，${edge.statusLabel}`}
                  onClick={(event) => { selectEdge(viewEdge, event.currentTarget); }}
                >
                  <span className="graph-edge-label__type">{edge.label}</span>
                  <span className="graph-edge-label__status">{edge.statusLabel}</span>
                </button>
              );
            })}
          </div>
        </div>
      ) : (
        <GraphList
          model={model}
          lockedPositions={activeLockedPositions}
          {...(selectedNodeKey === undefined ? {} : { selectedNodeKey })}
          {...(selectedEdgeKey === undefined ? {} : { selectedEdgeKey })}
          onNode={selectNode}
          onEdge={selectEdge}
        />
      )}
    </section>
  );
};
