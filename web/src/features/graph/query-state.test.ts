import { describe, expect, it } from "vitest";

import { graphQueryKeys } from "./query-keys";
import { isGraphNodeSearchEnabled } from "./queries";

const workspaceId = "92000000-0000-4000-8000-000000000001";
const otherWorkspaceId = "92000000-0000-4000-8000-000000000002";
const topicId = "92000000-0000-4000-8000-000000000003";
const claimId = "92000000-0000-4000-8000-000000000004";
const otherClaimId = "92000000-0000-4000-8000-000000000005";
const relationId = "92000000-0000-4000-8000-000000000006";

describe("Graph query state", () => {
  it("所有查询均以 Workspace 分区", () => {
    expect(graphQueryKeys.global({ workspaceId })).not.toEqual(graphQueryKeys.global({ workspaceId: otherWorkspaceId }));
    expect(graphQueryKeys.nodeSearch({ workspaceId, query: "恢复" })).not.toEqual(
      graphQueryKeys.nodeSearch({ workspaceId: otherWorkspaceId, query: "恢复" }),
    );
    expect(graphQueryKeys.node({ workspaceId, nodeType: "TOPIC", nodeId: topicId })).not.toEqual(
      graphQueryKeys.node({ workspaceId: otherWorkspaceId, nodeType: "TOPIC", nodeId: topicId }),
    );
    expect(graphQueryKeys.neighborhood({ workspaceId, center: { type: "TOPIC", id: topicId } })).not.toEqual(
      graphQueryKeys.neighborhood({ workspaceId: otherWorkspaceId, center: { type: "TOPIC", id: topicId } }),
    );
    expect(graphQueryKeys.path({
      workspaceId, from: { type: "TOPIC", id: topicId }, to: { type: "CLAIM", id: claimId },
    })).not.toEqual(graphQueryKeys.path({
      workspaceId: otherWorkspaceId, from: { type: "TOPIC", id: topicId }, to: { type: "CLAIM", id: claimId },
    }));
    expect(graphQueryKeys.relation({ workspaceId, relationId })).not.toEqual(
      graphQueryKeys.relation({ workspaceId: otherWorkspaceId, relationId }),
    );
    expect(graphQueryKeys.evidence({ workspaceId, relationId })).not.toEqual(
      graphQueryKeys.evidence({ workspaceId: otherWorkspaceId, relationId }),
    );
  });

  it("集合过滤顺序和显式默认值不会制造第二份缓存", () => {
    const first = graphQueryKeys.neighborhood({
      workspaceId,
      center: { type: "TOPIC", id: topicId },
      filter: {
        nodeTypes: ["TOPIC", "CLAIM"],
        relationTypes: ["SUPPORTS", "BELONGS_TO"],
        topicIds: [topicId, otherWorkspaceId],
        relationStatuses: ["STALE", "CONFIRMED"],
        claimStatuses: ["DISPUTED", "CONFIRMED"],
      },
    });
    const second = graphQueryKeys.neighborhood({
      workspaceId,
      center: { type: "TOPIC", id: topicId },
      depth: 1,
      limit: 25,
      direction: "BOTH",
      maxNodes: 500,
      maxEdges: 1000,
      maxFrontier: 500,
      filter: {
        nodeTypes: ["CLAIM", "TOPIC"],
        relationTypes: ["BELONGS_TO", "SUPPORTS"],
        topicIds: [otherWorkspaceId, topicId],
        relationStatuses: ["CONFIRMED", "STALE"],
        claimStatuses: ["CONFIRMED", "DISPUTED"],
      },
    });
    expect(first).toEqual(second);
  });

  it("等价 RFC3339 时区使用同一缓存 key", () => {
    expect(graphQueryKeys.global({
      workspaceId,
      filter: { updatedAfter: "2026-07-20T10:30:00.123456789+08:00" },
    })).toEqual(graphQueryKeys.global({
      workspaceId,
      filter: { updatedAfter: "2026-07-20T02:30:00.123456789Z" },
    }));
  });

  it("时区换算跨出四位年份时保留 API 可接受值", () => {
    const underflow = "0001-01-01T00:00:00+23:00";
    const overflow = "9999-12-31T23:59:59-23:00";

    expect(graphQueryKeys.global({ workspaceId, filter: { updatedAfter: underflow } })[3].filter.updatedAfter)
      .toBe(underflow);
    expect(graphQueryKeys.global({ workspaceId, filter: { updatedAfter: overflow } })[3].filter.updatedAfter)
      .toBe(overflow);
  });

  it("中心、路径、关系和搜索条件都参与 key", () => {
    expect(graphQueryKeys.neighborhood({ workspaceId, center: { type: "TOPIC", id: topicId } })).not.toEqual(
      graphQueryKeys.neighborhood({ workspaceId, center: { type: "CLAIM", id: claimId } }),
    );
    expect(graphQueryKeys.path({
      workspaceId, from: { type: "TOPIC", id: topicId }, to: { type: "CLAIM", id: claimId },
    })).not.toEqual(graphQueryKeys.path({
      workspaceId, from: { type: "TOPIC", id: topicId }, to: { type: "CLAIM", id: otherClaimId },
    }));
    expect(graphQueryKeys.evidence({ workspaceId, relationId, limit: 20 })).not.toEqual(
      graphQueryKeys.evidence({ workspaceId, relationId, limit: 50 }),
    );
    expect(graphQueryKeys.nodeSearch({ workspaceId, query: "恢复", limit: 20 })).not.toEqual(
      graphQueryKeys.nodeSearch({ workspaceId, query: "回滚", limit: 20 }),
    );
  });

  it("节点搜索只在 Workspace 和 2..256 UTF-8 bytes 查询有效时启用", () => {
    expect(isGraphNodeSearchEnabled(workspaceId, "ab")).toBe(true);
    expect(isGraphNodeSearchEnabled(workspaceId, "知")).toBe(true);
    expect(isGraphNodeSearchEnabled("", "ab")).toBe(false);
    expect(isGraphNodeSearchEnabled(workspaceId, "a")).toBe(false);
    expect(isGraphNodeSearchEnabled(workspaceId, " ab")).toBe(false);
    expect(isGraphNodeSearchEnabled(workspaceId, "a\nb")).toBe(false);
    expect(isGraphNodeSearchEnabled(workspaceId, "a\u0000b")).toBe(false);
    expect(isGraphNodeSearchEnabled(workspaceId, "\ud800x")).toBe(false);
    expect(isGraphNodeSearchEnabled(workspaceId, "x".repeat(257))).toBe(false);
  });

  it("多跳快照忽略页大小，一跳分页仍按页大小隔离", () => {
    const center = { type: "TOPIC" as const, id: topicId };
    expect(graphQueryKeys.neighborhood({ workspaceId, center, depth: 2, limit: 25 })).toEqual(
      graphQueryKeys.neighborhood({ workspaceId, center, depth: 2, limit: 100 }),
    );
    expect(graphQueryKeys.neighborhood({ workspaceId, center, depth: 1, limit: 25 })).not.toEqual(
      graphQueryKeys.neighborhood({ workspaceId, center, depth: 1, limit: 100 }),
    );
  });
});
