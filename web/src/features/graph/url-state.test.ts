import { describe, expect, it } from "vitest";

import {
  graphFilterClaimStatuses,
  graphModes,
  graphNodeTypes,
  graphRelationStatuses,
  graphRelationTypes,
  graphTraversalDirections,
} from "./options";
import { parseGraphUrlState, serializeGraphUrlState } from "./url-state";

describe("Graph URL state", () => {
  it("可选枚举完整对应 Graph API 契约", () => {
    expect(graphModes).toEqual(["global", "local", "path"]);
    expect(graphNodeTypes).toEqual(["CLAIM", "TOPIC"]);
    expect(graphRelationTypes).toEqual([
      "BELONGS_TO", "CITES", "COMPLEMENTS", "CONFLICTS_WITH", "DERIVED_FROM",
      "DUPLICATES", "IMPACTS", "PREREQUISITE_OF", "SUPPORTS", "VERSION_OF",
    ]);
    expect(graphRelationStatuses).toEqual(["CONFIRMED", "STALE"]);
    expect(graphFilterClaimStatuses).toEqual(["CONFIRMED", "DISPUTED"]);
    expect(graphTraversalDirections).toEqual(["BOTH", "OUTBOUND", "INBOUND"]);
  });

  it("空 URL 恢复为显式安全默认", () => {
    expect(parseGraphUrlState(new URLSearchParams())).toEqual({
      mode: "global",
      center: null,
      from: null,
      to: null,
      depth: 1,
      direction: "BOTH",
      filter: {
        nodeTypes: [...graphNodeTypes],
        relationTypes: [...graphRelationTypes],
        topicIds: [],
        relationStatuses: ["CONFIRMED"],
        claimStatuses: ["CONFIRMED", "DISPUTED"],
      },
    });
  });

  it("Local deep link 只恢复中心节点并规范化全部过滤器", () => {
    const centerId = "92000000-0000-4000-8000-000000000003";
    const firstTopicId = "92000000-0000-4000-8000-000000000001";
    const secondTopicId = "92000000-0000-4000-8000-000000000002";
    const parameters = new URLSearchParams({
      mode: "local",
      center_type: "TOPIC",
      center_id: centerId,
      from_type: "CLAIM",
      from_id: "92000000-0000-4000-8000-000000000004",
      to_type: "TOPIC",
      to_id: "92000000-0000-4000-8000-000000000005",
      depth: "3",
      direction: "INBOUND",
      node_types: "TOPIC,CLAIM,TOPIC",
      relation_types: "SUPPORTS,BELONGS_TO,SUPPORTS",
      topic_ids: `${secondTopicId},${centerId},${firstTopicId},${secondTopicId}`,
      relation_statuses: "STALE,CONFIRMED,STALE",
      claim_statuses: "DISPUTED,CONFIRMED,DISPUTED",
      claim_min_confidence: "0.75",
      relation_min_confidence: "1",
      updated_after: "2026-07-20T13:45:12.123456789+08:00",
    });

    expect(parseGraphUrlState(parameters)).toEqual({
      mode: "local",
      center: { type: "TOPIC", id: centerId },
      from: null,
      to: null,
      depth: 3,
      direction: "INBOUND",
      filter: {
        nodeTypes: ["CLAIM", "TOPIC"],
        relationTypes: ["BELONGS_TO", "SUPPORTS"],
        topicIds: [firstTopicId, secondTopicId, centerId],
        relationStatuses: ["CONFIRMED", "STALE"],
        claimStatuses: ["CONFIRMED", "DISPUTED"],
        claimMinConfidence: 0.75,
        relationMinConfidence: 1,
        updatedAfter: "2026-07-20T05:45:12.123456789Z",
      },
    });
  });

  it("非法 URL 值保留有效 mode 并恢复为安全默认", () => {
    const parameters = new URLSearchParams({
      mode: "local",
      center_type: "SOURCE",
      center_id: "92000000-0000-4000-8000-0000000000AA",
      depth: "4",
      direction: "SIDEWAYS",
      node_types: "TOPIC,SOURCE",
      relation_types: "SUPPORTS,UNKNOWN",
      topic_ids: "92000000-0000-4000-8000-0000000000AA",
      relation_statuses: "STALE,UNKNOWN",
      claim_statuses: "INVALID",
      claim_min_confidence: "NaN",
      relation_min_confidence: "1.01",
      updated_after: "2026-02-30T12:00:00Z",
    });

    expect(parseGraphUrlState(parameters)).toEqual({
      mode: "local",
      center: null,
      from: null,
      to: null,
      depth: 1,
      direction: "BOTH",
      filter: {
        nodeTypes: [...graphNodeTypes],
        relationTypes: [...graphRelationTypes],
        topicIds: [],
        relationStatuses: ["CONFIRMED"],
        claimStatuses: ["CONFIRMED", "DISPUTED"],
      },
    });
    expect(parseGraphUrlState(new URLSearchParams("mode=unknown")).mode).toBe("global");
  });

  it("Path URL 只保留真实生效的过滤器且能够 round-trip", () => {
    const fromId = "92000000-0000-4000-8000-000000000004";
    const toId = "92000000-0000-4000-8000-000000000003";
    const topicId = "92000000-0000-4000-8000-000000000001";
    const state = parseGraphUrlState(new URLSearchParams({
      mode: "path",
      center_type: "TOPIC",
      center_id: topicId,
      from_type: "CLAIM",
      from_id: fromId,
      to_type: "TOPIC",
      to_id: toId,
      depth: "3",
      direction: "OUTBOUND",
      node_types: "CLAIM",
      relation_types: "SUPPORTS,BELONGS_TO,SUPPORTS",
      topic_ids: topicId,
      claim_statuses: "DISPUTED",
      claim_min_confidence: "0.5",
      relation_min_confidence: "0",
      updated_after: "2026-07-20T13:45:12Z",
    }));

    const serialized = serializeGraphUrlState(state);
    expect([...serialized.entries()]).toEqual([
      ["mode", "path"],
      ["from_type", "CLAIM"],
      ["from_id", fromId],
      ["to_type", "TOPIC"],
      ["to_id", toId],
      ["direction", "OUTBOUND"],
      ["relation_types", "BELONGS_TO,SUPPORTS"],
    ]);
    expect(state.filter).toEqual({
      nodeTypes: [...graphNodeTypes],
      relationTypes: ["BELONGS_TO", "SUPPORTS"],
      topicIds: [],
      relationStatuses: ["CONFIRMED"],
      claimStatuses: ["CONFIRMED", "DISPUTED"],
    });
    expect(parseGraphUrlState(serialized)).toEqual(state);
  });

  it("跨字段冲突会恢复到可继续操作的状态", () => {
    const centerId = "92000000-0000-4000-8000-000000000003";
    const otherTopicId = "92000000-0000-4000-8000-000000000001";
    const local = parseGraphUrlState(new URLSearchParams({
      mode: "local",
      center_type: "TOPIC",
      center_id: centerId,
      node_types: "CLAIM",
      topic_ids: otherTopicId,
    }));
    expect(local.center).toEqual({ type: "TOPIC", id: centerId });
    expect(local.filter.nodeTypes).toEqual([...graphNodeTypes]);
    expect(local.filter.topicIds).toEqual([]);

    const path = parseGraphUrlState(new URLSearchParams({
      mode: "path",
      from_type: "CLAIM",
      from_id: centerId,
      to_type: "CLAIM",
      to_id: centerId,
    }));
    expect(path.from).toEqual({ type: "CLAIM", id: centerId });
    expect(path.to).toBeNull();
  });

  it("序列化时裁剪无关端点并消除 Local 中心过滤冲突", () => {
    const centerId = "92000000-0000-4000-8000-000000000003";
    const otherId = "92000000-0000-4000-8000-000000000004";
    const defaults = parseGraphUrlState(new URLSearchParams());

    const globalParameters = serializeGraphUrlState({
      ...defaults,
      center: { type: "TOPIC", id: centerId },
      from: { type: "CLAIM", id: otherId },
      to: { type: "TOPIC", id: centerId },
    });
    expect([...globalParameters.entries()]).toEqual([["mode", "global"]]);

    const localParameters = serializeGraphUrlState({
      ...defaults,
      mode: "local",
      center: { type: "TOPIC", id: centerId },
      from: { type: "CLAIM", id: otherId },
      to: { type: "TOPIC", id: centerId },
      filter: {
        ...defaults.filter,
        nodeTypes: ["CLAIM"],
        topicIds: [otherId],
      },
    });
    expect([...localParameters.entries()]).toEqual([
      ["mode", "local"],
      ["center_type", "TOPIC"],
      ["center_id", centerId],
      ["depth", "1"],
      ["direction", "BOTH"],
    ]);
  });

  it("Topic ID 集合先去重再执行 API 上限", () => {
    const topicId = "92000000-0000-4000-8000-000000000001";
    const parameters = new URLSearchParams({
      topic_ids: Array.from({ length: 501 }, () => topicId).join(","),
    });

    expect(parseGraphUrlState(parameters).filter.topicIds).toEqual([topicId]);
  });

  it("等价 RFC3339 时刻恢复并序列化为同一 UTC 值", () => {
    const offsetState = parseGraphUrlState(new URLSearchParams({
      updated_after: "2026-07-20T10:30:00.123456789+08:00",
    }));
    const utcState = parseGraphUrlState(new URLSearchParams({
      updated_after: "2026-07-20T02:30:00.123456789Z",
    }));

    expect(offsetState).toEqual(utcState);
    expect(serializeGraphUrlState(offsetState).get("updated_after")).toBe("2026-07-20T02:30:00.123456789Z");
  });

  it("重复同值 scalar 可恢复，重复冲突值回到默认", () => {
    const centerId = "92000000-0000-4000-8000-000000000003";
    const repeated = new URLSearchParams();
    const repeatedValues: readonly (readonly [string, string])[] = [
      ["mode", "local"],
      ["center_type", "TOPIC"],
      ["center_id", centerId],
      ["depth", "2"],
      ["direction", "OUTBOUND"],
    ];
    for (const [name, value] of repeatedValues) {
      repeated.append(name, value);
      repeated.append(name, value);
    }
    const recovered = parseGraphUrlState(repeated);
    expect(recovered.mode).toBe("local");
    expect(recovered.center).toEqual({ type: "TOPIC", id: centerId });
    expect(recovered.depth).toBe(2);
    expect(recovered.direction).toBe("OUTBOUND");

    repeated.append("mode", "path");
    expect(parseGraphUrlState(repeated).mode).toBe("global");
  });
});
