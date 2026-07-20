import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import type { GraphViewModel } from "./view-model";
import { GraphCanvas } from "./GraphCanvas";

const topicKey = "TOPIC:22222222-2222-4222-8222-222222222222";
const claimKey = "CLAIM:33333333-3333-4333-8333-333333333333";

const model: GraphViewModel = {
  focusNodeKey: topicKey,
  nodes: [
    {
      key: topicKey,
      ref: { type: "TOPIC", id: "22222222-2222-4222-8222-222222222222" },
      label: "分布式系统",
      typeLabel: "主题",
      boundary: false,
    },
    {
      key: claimKey,
      ref: { type: "CLAIM", id: "33333333-3333-4333-8333-333333333333" },
      label: "共识协议需要处理节点故障。",
      typeLabel: "主张",
      boundary: false,
    },
  ],
  edges: [{
    key: "44444444-4444-4444-8444-444444444444",
    sourceKey: claimKey,
    targetKey: topicKey,
    label: "归属于",
    statusLabel: "已过期",
    edge: {
      relationId: "44444444-4444-4444-8444-444444444444",
      workspaceId: "11111111-1111-4111-8111-111111111111",
      source: { type: "CLAIM", id: "33333333-3333-4333-8333-333333333333" },
      target: { type: "TOPIC", id: "22222222-2222-4222-8222-222222222222" },
      type: "BELONGS_TO",
      status: "STALE",
      traversal: "FORWARD",
      confidence: 0.8,
      version: 1,
      evidenceCount: 1,
      evidenceFingerprint: "a".repeat(64),
      evidenceHref: "/evidence",
      updatedAt: "2026-07-20T10:00:00Z",
    },
  }],
};

describe("GraphCanvas", () => {
  it("在画布中分别选择节点和关系，不把查看动作隐式变成锁定", () => {
    const onSelectNode = vi.fn();
    const onSelectEdge = vi.fn();
    const { container } = render(
      <GraphCanvas
        model={model}
        mode="local"
        onSelectNode={onSelectNode}
        onSelectEdge={onSelectEdge}
      />,
    );

    expect(screen.getByRole("button", { name: "画布" })).toHaveAttribute("aria-pressed", "true");
    const nodeButton = screen.getByRole("button", { name: "选择主题：分布式系统" });
    fireEvent.click(nodeButton);

    expect(onSelectNode).toHaveBeenCalledWith(model.nodes[0], nodeButton);
    expect(nodeButton).not.toHaveAttribute("data-locked", "true");

    const edgeButton = screen.getByRole("button", { name: "选择关系：归属于，已过期" });
    fireEvent.click(edgeButton);
    expect(onSelectEdge).toHaveBeenCalledWith(model.edges[0], edgeButton);
    expect(container.querySelector("line[data-status='STALE']")).toHaveAttribute("stroke-dasharray", "10 7");
  });

  it("切换列表后仍可通过完整节点和关系列表选择详情", () => {
    const onSelectNode = vi.fn();
    const onSelectEdge = vi.fn();
    render(<GraphCanvas model={model} mode="local" onSelectNode={onSelectNode} onSelectEdge={onSelectEdge} />);

    fireEvent.click(screen.getByRole("button", { name: "列表" }));

    expect(screen.getByRole("button", { name: "列表" })).toHaveAttribute("aria-pressed", "true");
    expect(screen.getByRole("heading", { name: "节点（2）" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "关系（1）" })).toBeInTheDocument();
    const nodeButton = screen.getByRole("button", { name: "选择主张：共识协议需要处理节点故障。" });
    fireEvent.click(nodeButton);
    expect(onSelectNode).toHaveBeenCalledWith(model.nodes[1], nodeButton);
    const edgeButton = screen.getByRole("button", { name: "选择关系：归属于，已过期" });
    fireEvent.click(edgeButton);
    expect(onSelectEdge).toHaveBeenCalledWith(model.edges[0], edgeButton);
  });

  it("超过视觉上限时明确回退且不裁掉完整节点列表", () => {
    const firstNode = model.nodes[0];
    expect(firstNode).toBeDefined();
    if (firstNode === undefined) return;
    const oversized: GraphViewModel = {
      nodes: Array.from({ length: 61 }, (_, index) => ({
        ...firstNode,
        key: `TOPIC:${String(index)}`,
        ref: { type: "TOPIC", id: String(index) },
        label: `主题 ${String(index + 1)}`,
      })),
      edges: [],
    };

    render(<GraphCanvas model={oversized} mode="global" />);

    expect(screen.getByRole("status")).toHaveTextContent("节点超过画布上限（60），已切换到完整列表。");
    expect(screen.getByRole("button", { name: "画布" })).toBeDisabled();
    expect(screen.getByRole("heading", { name: "节点（61）" })).toBeInTheDocument();
    expect(screen.getAllByRole("listitem")).toHaveLength(61);
  });

  it("锁定坐标只影响布局与可见状态，不改变节点选择语义", () => {
    const onSelectNode = vi.fn();
    render(
      <GraphCanvas
        model={model}
        mode="local"
        lockedPositions={{ [claimKey]: { x: 200, y: 220 } }}
        onSelectNode={onSelectNode}
      />,
    );

    const lockedClaim = screen.getByRole("button", { name: "已锁定，选择主张：共识协议需要处理节点故障。" });
    expect(lockedClaim).toHaveAttribute("data-locked", "true");
    fireEvent.click(lockedClaim);

    expect(onSelectNode).toHaveBeenCalledWith(model.nodes[1], lockedClaim);
  });
});
