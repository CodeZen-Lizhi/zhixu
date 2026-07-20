import type {
  GraphEdge,
  GraphGlobalPage,
  GraphNeighborhood,
  GraphNode,
  GraphNodeRef,
  GraphPathResult,
  NodeType,
  RelationStatus,
  RelationType,
} from "../../api/graph";

export const GRAPH_CANVAS_WIDTH = 1000;
export const GRAPH_CANVAS_HEIGHT = 640;
export const GRAPH_MAX_VISUAL_NODES = 60;
export const GRAPH_MAX_VISUAL_EDGES = 100;

export type GraphLayoutMode = "global" | "local" | "path";
export type GraphViewMode = "canvas" | "list";
export type GraphFallbackReason = "NODE_LIMIT" | "EDGE_LIMIT" | "LAYOUT_ERROR";

export interface GraphPoint {
  x: number;
  y: number;
}

export type GraphLockedPositions = Readonly<Record<string, GraphPoint>>;

export interface GraphViewNode {
  key: string;
  ref: GraphNodeRef;
  label: string;
  typeLabel: string;
  boundary: boolean;
  node?: GraphNode;
  supportingText?: string;
}

export interface GraphViewEdge {
  key: string;
  sourceKey: string;
  targetKey: string;
  label: string;
  statusLabel: string;
  edge: GraphEdge;
}

export interface GraphViewModel {
  nodes: GraphViewNode[];
  edges: GraphViewEdge[];
  focusNodeKey?: string;
}

export interface PositionedGraphNode extends GraphViewNode {
  position: GraphPoint;
}

export interface PositionedGraphEdge extends GraphViewEdge {
  sourcePosition: GraphPoint;
  targetPosition: GraphPoint;
  labelPosition: GraphPoint;
}

export interface GraphCanvasLayout {
  kind: "canvas";
  width: number;
  height: number;
  nodes: PositionedGraphNode[];
  edges: PositionedGraphEdge[];
}

export interface GraphListFallback {
  kind: "list";
  reason: GraphFallbackReason;
}

export type GraphLayoutResult = GraphCanvasLayout | GraphListFallback;

const nodeTypeLabels: Record<NodeType, string> = {
  TOPIC: "主题",
  CLAIM: "主张",
};

const relationTypeLabels: Record<RelationType, string> = {
  CITES: "引用",
  DERIVED_FROM: "派生自",
  BELONGS_TO: "归属于",
  SUPPORTS: "支持",
  COMPLEMENTS: "补充",
  DUPLICATES: "重复",
  CONFLICTS_WITH: "冲突",
  PREREQUISITE_OF: "前置于",
  VERSION_OF: "是版本",
  IMPACTS: "影响",
};

const relationStatusLabels: Record<RelationStatus, string> = {
  CONFIRMED: "已确认",
  STALE: "已过期",
};

export const graphNodeKey = (ref: GraphNodeRef): string => `${ref.type}:${ref.id}`;

export const graphEdgeKey = (edge: GraphEdge): string => edge.relationId;

export const nodeTypeLabel = (type: NodeType): string => nodeTypeLabels[type];

export const relationTypeLabel = (type: RelationType): string => relationTypeLabels[type];

export const relationStatusLabel = (status: RelationStatus): string => relationStatusLabels[status];

export const graphNodeLabel = (node: GraphNode): string =>
  node.type === "TOPIC" ? node.name : node.statement;

export const graphNodeRefLabel = (ref: GraphNodeRef): string =>
  `${nodeTypeLabel(ref.type)} ${ref.id.slice(0, 8)}`;

const viewNode = (node: GraphNode, supportingText?: string): GraphViewNode => ({
  key: graphNodeKey(node),
  ref: { type: node.type, id: node.id },
  label: graphNodeLabel(node),
  typeLabel: nodeTypeLabel(node.type),
  boundary: false,
  node,
  ...(supportingText === undefined ? {} : { supportingText }),
});

const boundaryViewNode = (ref: GraphNodeRef): GraphViewNode => ({
  key: graphNodeKey(ref),
  ref,
  label: graphNodeRefLabel(ref),
  typeLabel: nodeTypeLabel(ref.type),
  boundary: true,
  supportingText: "边界节点",
});

const viewEdge = (edge: GraphEdge): GraphViewEdge => ({
  key: graphEdgeKey(edge),
  sourceKey: graphNodeKey(edge.source),
  targetKey: graphNodeKey(edge.target),
  label: relationTypeLabel(edge.type),
  statusLabel: relationStatusLabel(edge.status),
  edge,
});

export const projectGraphGlobalPages = (pages: readonly GraphGlobalPage[]): GraphViewModel => {
  const nodes = new Map<string, GraphViewNode>();
  for (const page of pages) {
    for (const cluster of page.clusters) {
      const key = graphNodeKey(cluster.topic);
      if (!nodes.has(key)) {
        nodes.set(key, viewNode(
          cluster.topic,
          `${String(cluster.directClaimCount)} 项直接主张 · ${String(cluster.incidentRelationCount)} 条关系`,
        ));
      }
    }
  }
  return { nodes: [...nodes.values()], edges: [] };
};

export const projectGraphNeighborhoodPages = (pages: readonly GraphNeighborhood[]): GraphViewModel => {
  const nodes = new Map<string, GraphViewNode>();
  const boundaryNodes = new Map<string, GraphViewNode>();
  const edges = new Map<string, GraphViewEdge>();
  for (const page of pages) {
    for (const node of page.nodes) {
      const key = graphNodeKey(node);
      if (!nodes.has(key)) nodes.set(key, viewNode(node));
    }
    for (const ref of page.boundaryNodes) {
      const key = graphNodeKey(ref);
      if (!boundaryNodes.has(key)) boundaryNodes.set(key, boundaryViewNode(ref));
    }
    for (const edge of page.edges) {
      const key = graphEdgeKey(edge);
      if (!edges.has(key)) edges.set(key, viewEdge(edge));
    }
  }
  for (const key of nodes.keys()) boundaryNodes.delete(key);
  const firstPage = pages[0];
  return {
    nodes: [...nodes.values(), ...boundaryNodes.values()],
    edges: [...edges.values()],
    ...(firstPage === undefined ? {} : { focusNodeKey: graphNodeKey(firstPage.center) }),
  };
};

export const projectGraphPath = (result: GraphPathResult): GraphViewModel => {
  if (result.status === "not_found") return { nodes: [], edges: [] };
  const nodes = new Map<string, GraphViewNode>();
  const edges = new Map<string, GraphViewEdge>();
  for (const node of result.nodes) {
    const key = graphNodeKey(node);
    if (!nodes.has(key)) nodes.set(key, viewNode(node));
  }
  for (const edge of result.edges) {
    const key = graphEdgeKey(edge);
    if (!edges.has(key)) edges.set(key, viewEdge(edge));
  }
  return {
    nodes: [...nodes.values()],
    edges: [...edges.values()],
    focusNodeKey: graphNodeKey(result.from),
  };
};

export const graphListFallbackReason = (model: GraphViewModel): GraphFallbackReason | undefined => {
  if (model.nodes.length > GRAPH_MAX_VISUAL_NODES) return "NODE_LIMIT";
  if (model.edges.length > GRAPH_MAX_VISUAL_EDGES) return "EDGE_LIMIT";
  return undefined;
};

const canvasCenter: GraphPoint = {
  x: GRAPH_CANVAS_WIDTH / 2,
  y: GRAPH_CANVAS_HEIGHT / 2,
};

const compareStableKeys = (left: string, right: string): number => {
  if (left < right) return -1;
  if (left > right) return 1;
  return 0;
};

const globalPositions = (nodes: readonly GraphViewNode[]): Map<string, GraphPoint> => {
  const positions = new Map<string, GraphPoint>();
  const goldenAngle = Math.PI * (3 - Math.sqrt(5));
  const outerIndex = Math.max(1, nodes.length - 1);
  nodes.forEach((node, index) => {
    if (index === 0) {
      positions.set(node.key, canvasCenter);
      return;
    }
    const radius = Math.sqrt(index / outerIndex);
    const angle = (index - 1) * goldenAngle - Math.PI / 2;
    positions.set(node.key, {
      x: canvasCenter.x + Math.cos(angle) * 400 * radius,
      y: canvasCenter.y + Math.sin(angle) * 245 * radius,
    });
  });
  return positions;
};

const localDistances = (model: GraphViewModel, focusKey: string): Map<string, number> => {
  const adjacency = new Map<string, Set<string>>();
  for (const node of model.nodes) adjacency.set(node.key, new Set());
  for (const edge of model.edges) {
    adjacency.get(edge.sourceKey)?.add(edge.targetKey);
    adjacency.get(edge.targetKey)?.add(edge.sourceKey);
  }
  const distances = new Map<string, number>([[focusKey, 0]]);
  const queue = [focusKey];
  for (const current of queue) {
    const distance = distances.get(current);
    if (distance === undefined) continue;
    for (const adjacent of adjacency.get(current) ?? []) {
      if (distances.has(adjacent)) continue;
      distances.set(adjacent, distance + 1);
      queue.push(adjacent);
    }
  }
  return distances;
};

const localPositions = (model: GraphViewModel): Map<string, GraphPoint> => {
  const positions = new Map<string, GraphPoint>();
  const firstNode = model.nodes[0];
  if (firstNode === undefined) return positions;
  const focusKey = model.focusNodeKey !== undefined && model.nodes.some((node) => node.key === model.focusNodeKey)
    ? model.focusNodeKey
    : firstNode.key;
  positions.set(focusKey, canvasCenter);
  const distances = localDistances(model, focusKey);
  const maxConnectedDistance = Math.max(0, ...distances.values());
  const groups = new Map<number, GraphViewNode[]>();
  for (const node of model.nodes) {
    if (node.key === focusKey) continue;
    const distance = distances.get(node.key) ?? maxConnectedDistance + 1;
    const group = groups.get(distance) ?? [];
    group.push(node);
    groups.set(distance, group);
  }
  for (const [distance, group] of [...groups.entries()].sort(([left], [right]) => left - right)) {
    group.sort((left, right) => compareStableKeys(left.key, right.key));
    const radiusX = Math.min(400, 190 * distance);
    const radiusY = Math.min(245, 210 * distance);
    group.forEach((node, index) => {
      const angle = (2 * Math.PI * index) / group.length - Math.PI / 2;
      positions.set(node.key, {
        x: canvasCenter.x + Math.cos(angle) * radiusX,
        y: canvasCenter.y + Math.sin(angle) * radiusY,
      });
    });
  }
  return positions;
};

const pathPositions = (nodes: readonly GraphViewNode[]): Map<string, GraphPoint> => {
  const positions = new Map<string, GraphPoint>();
  const horizontalInset = 160;
  if (nodes.length === 1) {
    const onlyNode = nodes[0];
    if (onlyNode !== undefined) positions.set(onlyNode.key, canvasCenter);
    return positions;
  }
  nodes.forEach((node, index) => {
    positions.set(node.key, {
      x: horizontalInset + (index / Math.max(1, nodes.length - 1)) * (GRAPH_CANVAS_WIDTH - horizontalInset * 2),
      y: canvasCenter.y,
    });
  });
  return positions;
};

const pointIsInsideCanvas = (point: GraphPoint): boolean =>
  Number.isFinite(point.x)
  && Number.isFinite(point.y)
  && point.x >= 0
  && point.x <= GRAPH_CANVAS_WIDTH
  && point.y >= 0
  && point.y <= GRAPH_CANVAS_HEIGHT;

export const createGraphLayout = (
  model: GraphViewModel,
  mode: GraphLayoutMode,
  lockedPositions: GraphLockedPositions = {},
): GraphLayoutResult => {
  const limitReason = graphListFallbackReason(model);
  if (limitReason !== undefined) return { kind: "list", reason: limitReason };
  if (new Set(model.nodes.map((node) => node.key)).size !== model.nodes.length) {
    return { kind: "list", reason: "LAYOUT_ERROR" };
  }
  if (new Set(model.edges.map((edge) => edge.key)).size !== model.edges.length) {
    return { kind: "list", reason: "LAYOUT_ERROR" };
  }

  const positions = mode === "global"
    ? globalPositions(model.nodes)
    : mode === "local"
      ? localPositions(model)
      : pathPositions(model.nodes);
  for (const node of model.nodes) {
    const locked = lockedPositions[node.key];
    if (locked === undefined) continue;
    if (!pointIsInsideCanvas(locked)) return { kind: "list", reason: "LAYOUT_ERROR" };
    positions.set(node.key, locked);
  }

  const positionedNodes: PositionedGraphNode[] = [];
  for (const node of model.nodes) {
    const position = positions.get(node.key);
    if (position === undefined || !pointIsInsideCanvas(position)) {
      return { kind: "list", reason: "LAYOUT_ERROR" };
    }
    positionedNodes.push({ ...node, position });
  }

  const positionedEdges: PositionedGraphEdge[] = [];
  for (const [index, edge] of model.edges.entries()) {
    const sourcePosition = positions.get(edge.sourceKey);
    const targetPosition = positions.get(edge.targetKey);
    if (sourcePosition === undefined || targetPosition === undefined) {
      return { kind: "list", reason: "LAYOUT_ERROR" };
    }
    const deltaX = targetPosition.x - sourcePosition.x;
    const deltaY = targetPosition.y - sourcePosition.y;
    const length = Math.hypot(deltaX, deltaY);
    const offset = length === 0 ? 0 : ((index % 3) - 1) * 14;
    positionedEdges.push({
      ...edge,
      sourcePosition,
      targetPosition,
      labelPosition: {
        x: (sourcePosition.x + targetPosition.x) / 2 - (deltaY / Math.max(length, 1)) * offset,
        y: (sourcePosition.y + targetPosition.y) / 2 + (deltaX / Math.max(length, 1)) * offset,
      },
    });
  }

  return {
    kind: "canvas",
    width: GRAPH_CANVAS_WIDTH,
    height: GRAPH_CANVAS_HEIGHT,
    nodes: positionedNodes,
    edges: positionedEdges,
  };
};
