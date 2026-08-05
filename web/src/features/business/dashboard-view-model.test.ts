import { describe, expect, it } from "vitest";

import type { ProposalSummary, WorkflowSummary } from "../../api/business";
import {
  formatDashboardTimestamp,
  indexStatusDisplay,
  ingestionStatusDisplay,
  securityStatusDisplay,
  selectDashboardFocus,
  sourceFileName,
} from "./dashboard-view-model";

const workflow = (overrides: Partial<WorkflowSummary> = {}): WorkflowSummary => ({
  id: "20000000-0000-4000-8000-000000000001",
  workspaceId: "10000000-0000-4000-8000-000000000002",
  definitionKey: "knowledge_review",
  definitionVersion: 1,
  status: "waiting_for_human",
  version: 2,
  createdAt: "2026-07-30T08:00:00Z",
  updatedAt: "2026-07-31T08:00:00Z",
  waitingForHuman: true,
  pauseRequested: false,
  cancelRequested: false,
  ...overrides,
});

const proposal = (overrides: Partial<ProposalSummary> = {}): ProposalSummary => ({
  id: "30000000-0000-4000-8000-000000000001",
  workspaceId: "10000000-0000-4000-8000-000000000002",
  type: "file_patch",
  status: "ready_for_review",
  target: "docs/architecture.md",
  riskLevel: "LOW",
  risk: "小范围文案变更",
  revisionId: "40000000-0000-4000-8000-000000000001",
  changeHash: "a".repeat(64),
  createdAt: "2026-07-30T08:00:00Z",
  updatedAt: "2026-07-31T08:00:00Z",
  ...overrides,
});

describe("dashboard view model", () => {
  it("始终优先等待人工的流程，其次失败流程，最后才是提案", () => {
    const waiting = workflow();
    const failed = workflow({
      id: "20000000-0000-4000-8000-000000000002",
      definitionKey: "failed_index",
      status: "failed",
      waitingForHuman: false,
      updatedAt: "2026-08-01T08:00:00Z",
    });

    expect(selectDashboardFocus({
      waitingWorkflows: [waiting],
      failedWorkflows: [failed],
      proposals: [proposal({ riskLevel: "CRITICAL" })],
    })).toMatchObject({ kind: "workflow_waiting", href: `/workflows/${waiting.id}` });

    expect(selectDashboardFocus({
      waitingWorkflows: [],
      failedWorkflows: [failed],
      proposals: [proposal({ riskLevel: "CRITICAL" })],
    })).toMatchObject({ kind: "workflow_failed", href: `/workflows/${failed.id}` });
  });

  it("在当前五条提案内先按风险、再按更新时间选择", () => {
    const olderHigh = proposal({ id: "30000000-0000-4000-8000-000000000002", riskLevel: "HIGH", updatedAt: "2026-07-30T08:00:00Z" });
    const newerHigh = proposal({ id: "30000000-0000-4000-8000-000000000003", riskLevel: "HIGH", updatedAt: "2026-07-31T09:00:00Z" });
    const criticalWithInvalidTime = proposal({ id: "30000000-0000-4000-8000-000000000004", riskLevel: "CRITICAL", updatedAt: "invalid" });

    expect(selectDashboardFocus({ waitingWorkflows: [], failedWorkflows: [], proposals: [olderHigh, newerHigh] })).toMatchObject({ href: `/proposals/${newerHigh.id}` });
    expect(selectDashboardFocus({ waitingWorkflows: [], failedWorkflows: [], proposals: [newerHigh, criticalWithInvalidTime] })).toMatchObject({
      href: `/proposals/${criticalWithInvalidTime.id}`,
      riskLabel: "严重",
      description: "文件修改 · 严重风险",
    });
  });

  it("没有待办时只提供真实起步入口", () => {
    expect(selectDashboardFocus({ waitingWorkflows: [], failedWorkflows: [], proposals: [] })).toEqual({
      kind: "empty",
      label: "今日已收束",
      title: "今天没有等待处理的事项",
      description: "现在适合从一篇新文章或一次材料整理开始。",
      href: "/authoring/new",
      actionLabel: "新建文章",
    });
  });

  it("投影文件名、时间和资料处理状态", () => {
    expect(sourceFileName("docs/design/home.md")).toBe("home.md");
    expect(sourceFileName("docs\\design\\home.md")).toBe("home.md");
    expect(sourceFileName("/")).toBe("未命名资料");
    expect(formatDashboardTimestamp("invalid")).toBe("时间未知");
    expect(securityStatusDisplay("quarantined")).toEqual({ label: "已隔离", tone: "danger" });
    expect(ingestionStatusDisplay("parse_failed")).toEqual({ label: "解析失败", tone: "danger" });
    expect(ingestionStatusDisplay(undefined)).toEqual({ label: "未开始", tone: "neutral" });
    expect(indexStatusDisplay("included")).toEqual({ label: "已纳入", tone: "success" });
  });
});
