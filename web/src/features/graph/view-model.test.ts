import { describe, expect, it } from "vitest";

import type {
  GraphClaimNode,
  GraphEdge,
  GraphGlobalPage,
  GraphNeighborhood,
  GraphPathResult,
  GraphTopicNode,
} from "../../api/graph";
import {
  graphEdgeKey,
  GRAPH_MAX_VISUAL_EDGES,
  GRAPH_MAX_VISUAL_NODES,
  graphListFallbackReason,
  graphNodeKey,
  graphNodeLabel,
  graphNodeRefLabel,
  nodeTypeLabel,
  createGraphLayout,
  projectGraphGlobalPages,
  projectGraphNeighborhoodPages,
  projectGraphPath,
  relationStatusLabel,
  relationTypeLabel,
  type GraphViewModel,
} from "./view-model";

const workspaceId = "11111111-1111-4111-8111-111111111111";
const topicId = "22222222-2222-4222-8222-222222222222";
const claimId = "33333333-3333-4333-8333-333333333333";

const topic: GraphTopicNode = {
  type: "TOPIC",
  id: topicId,
  workspaceId,
  name: "分布式系统",
  description: "",
  topicStatus: "ACTIVE",
  version: 1,
  updatedAt: "2026-07-20T10:00:00Z",
};

const claim: GraphClaimNode = {
  type: "CLAIM",
  id: claimId,
  workspaceId,
  statement: "共识协议需要处理节点故障。",
  claimStatus: "CONFIRMED",
  confidence: 0.9,
  applicability: {
    schemaVersion: "knowledge-applicability/v1",
    value: {},
    hash: "a".repeat(64),
  },
  version: 2,
  updatedAt: "2026-07-20T10:01:00Z",
};

const edge: GraphEdge = {
  relationId: "44444444-4444-4444-8444-444444444444",
  workspaceId,
  source: { type: "CLAIM", id: claimId },
  target: { type: "TOPIC", id: topicId },
  type: "BELONGS_TO",
  status: "STALE",
  traversal: "FORWARD",
  confidence: 0.8,
  version: 1,
  evidenceCount: 1,
  evidenceFingerprint: "b".repeat(64),
  evidenceHref: `/api/v1/graph/relations/44444444-4444-4444-8444-444444444444/evidence?workspace_id=${workspaceId}`,
  updatedAt: "2026-07-20T10:02:00Z",
};

describe("Graph view model", () => {
  it("为节点、引用和关系提供稳定 key 与中文标签", () => {
    expect(graphNodeKey(topic)).toBe(`TOPIC:${topicId}`);
    expect(graphNodeKey(claim)).toBe(`CLAIM:${claimId}`);
    expect(graphEdgeKey(edge)).toBe(edge.relationId);
    expect(graphNodeLabel(topic)).toBe("分布式系统");
    expect(graphNodeLabel(claim)).toBe("共识协议需要处理节点故障。");
    expect(graphNodeRefLabel({ type: "CLAIM", id: claimId })).toBe("主张 33333333");
    expect(nodeTypeLabel("TOPIC")).toBe("主题");
    expect(relationTypeLabel("BELONGS_TO")).toBe("归属于");
    expect(relationStatusLabel("STALE")).toBe("已过期");
  });

  it("合并全局分页时按主题 key 去重并保留服务端顺序", () => {
    const page = (clusters: GraphGlobalPage["clusters"]): GraphGlobalPage => ({
      workspaceId,
      clusters,
      meta: {
        fingerprint: "c".repeat(64),
        complete: true,
        truncated: false,
      },
    });
    const cluster = {
      topic,
      directClaimCount: 3,
      incidentRelationCount: 2,
      clusterScore: 5,
      updatedAt: topic.updatedAt,
    };

    const model = projectGraphGlobalPages([page([cluster]), page([cluster])]);

    expect(model.nodes).toHaveLength(1);
    expect(model.nodes[0]).toMatchObject({
      key: `TOPIC:${topicId}`,
      label: "分布式系统",
      typeLabel: "主题",
      boundary: false,
      supportingText: "3 项直接主张 · 2 条关系",
    });
    expect(model.edges).toEqual([]);
  });

  it("合并局部分页时去重节点和关系，并用后续实体替换边界引用", () => {
    const neighborhood = (
      nodes: GraphNeighborhood["nodes"],
      boundaryNodes: GraphNeighborhood["boundaryNodes"],
    ): GraphNeighborhood => ({
      workspaceId,
      center: { type: "TOPIC", id: topicId },
      nodes,
      edges: [edge],
      boundaryNodes,
      layerCounts: [1],
      completedDepth: 1,
      meta: {
        fingerprint: "d".repeat(64),
        complete: true,
        truncated: false,
      },
    });

    const model = projectGraphNeighborhoodPages([
      neighborhood([topic], [{ type: "CLAIM", id: claimId }]),
      neighborhood([topic, claim], []),
    ]);

    expect(model.focusNodeKey).toBe(`TOPIC:${topicId}`);
    expect(model.nodes.map((node) => [node.key, node.boundary])).toEqual([
      [`TOPIC:${topicId}`, false],
      [`CLAIM:${claimId}`, false],
    ]);
    expect(model.edges).toEqual([
      expect.objectContaining({
        key: edge.relationId,
        sourceKey: `CLAIM:${claimId}`,
        targetKey: `TOPIC:${topicId}`,
        label: "归属于",
        statusLabel: "已过期",
      }),
    ]);
  });

  it("只把已证明路径投影到画布，并保留路径顺序", () => {
    const found: GraphPathResult = {
      status: "found",
      workspaceId,
      from: { type: "CLAIM", id: claimId },
      to: { type: "TOPIC", id: topicId },
      nodes: [claim, topic],
      edges: [edge],
      hopCount: 1,
      exploredNodes: 2,
      commonTopicSuggestions: [],
    };
    const notFound: GraphPathResult = {
      status: "not_found",
      workspaceId,
      from: found.from,
      to: found.to,
      nodes: [],
      edges: [],
      hopCount: 0,
      exploredNodes: 2,
      commonTopicSuggestions: [topic],
    };

    expect(projectGraphPath(found)).toMatchObject({
      focusNodeKey: `CLAIM:${claimId}`,
      nodes: [
        { key: `CLAIM:${claimId}` },
        { key: `TOPIC:${topicId}` },
      ],
      edges: [{ key: edge.relationId }],
    });
    expect(projectGraphPath(notFound)).toEqual({ nodes: [], edges: [] });
  });

  it("节点或关系超过视觉上限时要求完整列表 fallback", () => {
    const node = projectGraphGlobalPages([{
      workspaceId,
      clusters: [{
        topic,
        directClaimCount: 0,
        incidentRelationCount: 0,
        clusterScore: 0,
        updatedAt: topic.updatedAt,
      }],
      meta: { fingerprint: "e".repeat(64), complete: true, truncated: false },
    }]).nodes[0];
    expect(node).toBeDefined();
    if (node === undefined) return;

    expect(graphListFallbackReason({
      nodes: Array.from({ length: GRAPH_MAX_VISUAL_NODES + 1 }, (_, index) => ({
        ...node,
        key: `TOPIC:${String(index)}`,
        ref: { type: "TOPIC", id: String(index) },
      })),
      edges: [],
    })).toBe("NODE_LIMIT");
    expect(graphListFallbackReason({
      nodes: [node],
      edges: Array.from({ length: GRAPH_MAX_VISUAL_EDGES + 1 }, (_, index) => ({
        key: String(index),
        sourceKey: node.key,
        targetKey: node.key,
        label: "支持",
        statusLabel: "已确认",
        edge: { ...edge, relationId: `relation-${String(index)}` },
      })),
    })).toBe("EDGE_LIMIT");
  });

  it("按模式生成确定性受限布局，并优先使用锁定坐标", () => {
    const model = projectGraphNeighborhoodPages([{
      workspaceId,
      center: { type: "TOPIC", id: topicId },
      nodes: [topic, claim],
      edges: [edge],
      boundaryNodes: [],
      layerCounts: [1],
      completedDepth: 1,
      meta: { fingerprint: "f".repeat(64), complete: true, truncated: false },
    }]);
    const local = createGraphLayout(model, "local");
    const repeated = createGraphLayout(model, "local");
    const locked = createGraphLayout(model, "local", {
      [`CLAIM:${claimId}`]: { x: 120, y: 140 },
    });

    expect(local).toEqual(repeated);
    expect(local).toMatchObject({
      kind: "canvas",
      nodes: [
        { key: `TOPIC:${topicId}`, position: { x: 500, y: 320 } },
        { key: `CLAIM:${claimId}` },
      ],
      edges: [{ key: edge.relationId }],
    });
    if (local.kind === "canvas") {
      const topicPosition = local.nodes.find((node) => node.key === `TOPIC:${topicId}`)?.position;
      const claimPosition = local.nodes.find((node) => node.key === `CLAIM:${claimId}`)?.position;
      expect(topicPosition).toBeDefined();
      expect(claimPosition).toBeDefined();
      if (topicPosition !== undefined && claimPosition !== undefined) {
        expect(Math.hypot(topicPosition.x - claimPosition.x, topicPosition.y - claimPosition.y)).toBeGreaterThanOrEqual(200);
      }
    }
    expect(locked).toMatchObject({
      kind: "canvas",
      nodes: [
        { key: `TOPIC:${topicId}` },
        { key: `CLAIM:${claimId}`, position: { x: 120, y: 140 } },
      ],
    });
  });

  it("全局与路径布局分别保持聚类排序和路径顺序", () => {
    const secondTopic: GraphTopicNode = {
      ...topic,
      id: "55555555-5555-4555-8555-555555555555",
      name: "数据库",
    };
    const globalModel = projectGraphGlobalPages([{
      workspaceId,
      clusters: [topic, secondTopic].map((item) => ({
        topic: item,
        directClaimCount: 0,
        incidentRelationCount: 0,
        clusterScore: 0,
        updatedAt: item.updatedAt,
      })),
      meta: { fingerprint: "1".repeat(64), complete: true, truncated: false },
    }]);
    const firstNode = globalModel.nodes[0];
    const secondNode = globalModel.nodes[1];
    expect(firstNode).toBeDefined();
    expect(secondNode).toBeDefined();
    if (firstNode === undefined || secondNode === undefined) return;
    const pathModel: GraphViewModel = {
      nodes: [firstNode, secondNode],
      edges: [],
    };

    const global = createGraphLayout(globalModel, "global");
    expect(global).toMatchObject({
      kind: "canvas",
      nodes: [
        { key: `TOPIC:${topicId}`, position: { x: 500, y: 320 } },
        { key: `TOPIC:${secondTopic.id}` },
      ],
    });
    if (global.kind === "canvas") {
      const firstPosition = global.nodes[0]?.position;
      const secondPosition = global.nodes[1]?.position;
      expect(firstPosition).toBeDefined();
      expect(secondPosition).toBeDefined();
      if (firstPosition !== undefined && secondPosition !== undefined) {
        const horizontallySeparated = Math.abs(firstPosition.x - secondPosition.x) >= 220;
        const verticallySeparated = Math.abs(firstPosition.y - secondPosition.y) >= 120;
        expect(horizontallySeparated || verticallySeparated).toBe(true);
      }
    }
    const path = createGraphLayout(pathModel, "path");
    expect(path).toMatchObject({
      kind: "canvas",
      nodes: [
        { key: `TOPIC:${topicId}`, position: { y: 320 } },
        { key: `TOPIC:${secondTopic.id}`, position: { y: 320 } },
      ],
    });
    if (path.kind === "canvas") {
      expect(path.nodes[0]?.position.x).toBeGreaterThanOrEqual(160);
      expect(path.nodes[1]?.position.x).toBeLessThanOrEqual(840);
    }
  });

  it("非法锁定坐标或断边会明确要求列表 fallback", () => {
    const model = projectGraphNeighborhoodPages([{
      workspaceId,
      center: { type: "TOPIC", id: topicId },
      nodes: [topic, claim],
      edges: [edge],
      boundaryNodes: [],
      layerCounts: [1],
      completedDepth: 1,
      meta: { fingerprint: "2".repeat(64), complete: true, truncated: false },
    }]);

    expect(createGraphLayout(model, "local", {
      [`TOPIC:${topicId}`]: { x: Number.NaN, y: 320 },
    })).toEqual({ kind: "list", reason: "LAYOUT_ERROR" });
    expect(createGraphLayout({
      ...model,
      nodes: model.nodes.filter((node) => node.key !== `CLAIM:${claimId}`),
    }, "local")).toEqual({ kind: "list", reason: "LAYOUT_ERROR" });
  });
});
