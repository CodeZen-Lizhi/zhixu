import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";

import type { HealthDecision, HealthIssue, HealthIssueDetail, HealthIssueListItem, HealthObservation, HealthScan, HealthSummary } from "../../api/health";

const workspaceId = "16000000-0000-4000-8000-000000000001";
const issueId = "16000000-0000-4000-8000-000000000002";
const scanId = "16000000-0000-4000-8000-000000000003";
const workflowRunId = "16000000-0000-4000-8000-000000000004";
const targetId = "16000000-0000-4000-8000-000000000005";
const at = "2026-07-22T00:00:00Z";
const hash = "c".repeat(64);

const hooks = vi.hoisted(() => ({
  activeWorkspaceId: "16000000-0000-4000-8000-000000000001",
  useClearHealthIssueDetail: vi.fn(),
  useDecideHealthIssue: vi.fn(),
  useHealthIssue: vi.fn(),
  useHealthIssueDecisions: vi.fn(),
  useHealthIssueObservations: vi.fn(),
  useHealthIssues: vi.fn(),
  useHealthScan: vi.fn(),
  useHealthSummary: vi.fn(),
  useStartHealthScan: vi.fn(),
}));

vi.mock("../../app/active-workspace", async (importOriginal) => ({
  // eslint-disable-next-line @typescript-eslint/consistent-type-imports
  ...(await importOriginal<typeof import("../../app/active-workspace")>()),
  useActiveWorkspaceId: () => hooks.activeWorkspaceId,
}));
vi.mock("./queries", () => hooks);

import { HealthPage } from "./HealthPage";

const checkpoint = { cursor: "", page: 0, lastItem: null };
const counters = { processed: 10, created: 2, reopened: 1, resolved: 0, unchanged: 7, failed: 1 };
const coverage = [{ detectorId: "missing-source", detectorVersion: "v1", status: "PARTIAL" as const, checkpoint, counters, lastError: { stage: "evidence", code: "SOURCE_UNAVAILABLE", retryable: true }, unavailableReason: null }];
const trend = Array.from({ length: 7 }, (_item, index) => ({ date: `2026-07-${String(16 + index).padStart(2, "0")}`, detectedCount: index, resolvedCount: 6 - index }));
const scan: HealthScan = {
  id: scanId,
  workspaceId,
  workflowRunId,
  scope: { type: "WORKSPACE", ref: workspaceId, version: 1, schemaVersion: "health-scope/workspace/v1", hash: null },
  fingerprint: hash,
  requestHash: hash,
  maxItems: 5000,
  status: "PARTIAL",
  checkpoint,
  counters,
  coverage,
  lastError: null,
  version: 2,
  createdAt: at,
  updatedAt: at,
  completedAt: at,
  statusUrl: `/api/v1/health/scans/${scanId}?workspace_id=${workspaceId}`,
};
const summary: HealthSummary = {
  workspaceId,
  openCount: 1,
  openBySeverity: { CRITICAL: 0, HIGH: 1, MEDIUM: 0, LOW: 0 },
  openByType: { MISSING_SOURCE: 1 },
  trend,
  lastScan: { id: scan.id, status: scan.status, scope: scan.scope, counters: scan.counters, coverage: scan.coverage, completedAt: at, updatedAt: scan.updatedAt },
  unavailable: [{ code: "REVIEW_INVALIDATED", reason: "Review 未启用" }],
};
const issueListItem: HealthIssueListItem = {
  id: issueId,
  workspaceId,
  type: "MISSING_SOURCE",
  target: { type: "CLAIM", id: targetId },
  detectorId: "missing-source",
  detectorVersion: "v1",
  severity: "HIGH",
  evidenceSummary: "Claim 缺少来源",
  status: "OPEN",
  firstDetectedAt: at,
  lastDetectedAt: at,
  lastVerifiedAt: at,
  version: 4,
  updatedAt: at,
};
const issue: HealthIssue = {
  ...issueListItem,
  identityHash: hash,
  fingerprint: hash,
  evidence: [{ ref: { type: "CLAIM", id: targetId }, hash, summary: "Evidence 摘要" }],
  objectVersions: [{ ref: { type: "CLAIM", id: targetId }, version: 2 }],
  repairOptions: [
    { code: "attach_source", title: "补充来源", available: false, unavailableReason: "Repair Proposal 尚未启用" },
    { code: "review", title: "发起复核", available: false, unavailableReason: "Review 未启用" },
  ],
  statusReason: "",
  deferredUntil: null,
  proposal: null,
  resolvedAt: null,
  createdAt: at,
};
const observation: HealthObservation = {
  id: "16000000-0000-4000-8000-000000000006",
  issueVersion: 4,
  scanId,
  detectorVersion: "v1",
  fingerprint: hash,
  evidenceFingerprint: hash,
  targetVersions: [{ ref: { type: "CLAIM", id: targetId }, version: 2 }],
  severity: "HIGH",
  observedAt: at,
  evidence: [{ ref: { type: "CLAIM", id: targetId }, hash, summary: "历史 Evidence" }],
};
const decision: HealthDecision = {
  id: "16000000-0000-4000-8000-000000000007",
  issueVersion: 4,
  proposalId: null,
  idempotencyKey: "decision-1",
  action: "ACKNOWLEDGE",
  reason: "已核实",
  deferredUntil: null,
  createdAt: at,
};
const currentObservation: HealthObservation = { ...observation, evidence: issue.evidence };
const detail: HealthIssueDetail = {
  issue,
  latestObservation: currentObservation,
  observations: [observation],
  observationsNextCursor: null,
  observationsHasMore: false,
  decisions: [],
  decisionsNextCursor: null,
  decisionsHasMore: false,
};
const idleHistory = {
  data: undefined,
  isPending: true,
  isError: false,
  isFetchNextPageError: false,
  isFetchingNextPage: false,
  hasNextPage: false,
  error: new Error("history unavailable"),
  refetch: vi.fn(),
  fetchNextPage: vi.fn(),
};

afterEach(() => {
  hooks.activeWorkspaceId = workspaceId;
  Object.values(hooks).forEach((hook) => { if (typeof hook === "function") hook.mockReset(); });
});

describe("HealthPage", () => {
  it("restores scan state from URL and lazy-loads Evidence details", async () => {
    const clearDetail = vi.fn();
    hooks.useHealthSummary.mockReturnValue({ data: summary, isPending: false, isError: false, refetch: vi.fn() });
    hooks.useHealthScan.mockReturnValue({ data: scan, isPending: false, isError: false, refetch: vi.fn() });
    hooks.useHealthIssues.mockReturnValue({ data: { workspaceId, items: [issueListItem], nextCursor: null, hasMore: false }, isPending: false, isError: false, refetch: vi.fn() });
    hooks.useHealthIssue.mockReturnValue({ data: detail, isPending: false, isError: false, refetch: vi.fn() });
    hooks.useHealthIssueObservations.mockReturnValue({ ...idleHistory, refetch: vi.fn(), fetchNextPage: vi.fn() });
    hooks.useHealthIssueDecisions.mockReturnValue({ ...idleHistory, refetch: vi.fn(), fetchNextPage: vi.fn() });
    hooks.useStartHealthScan.mockReturnValue({ mutate: vi.fn(), isPending: false, isError: false });
    hooks.useDecideHealthIssue.mockReturnValue({ mutate: vi.fn(), isPending: false, isError: false });
    hooks.useClearHealthIssueDetail.mockReturnValue(clearDetail);

    render(<MemoryRouter initialEntries={[`/health?scan=${scanId}`]}><Routes><Route path="/health" element={<HealthPage />} /></Routes></MemoryRouter>);

    expect(screen.getAllByText("部分完成").length).toBeGreaterThan(0);
    expect(screen.getByText("07-16")).toBeInTheDocument();
    expect(screen.getAllByText("新增").length).toBeGreaterThan(0);
    expect(screen.queryByText("Evidence 摘要")).not.toBeInTheDocument();
    const trigger = screen.getByRole("button", { name: "查看证据" });
    fireEvent.click(trigger);
    expect(await screen.findByText("Evidence 摘要")).toBeInTheDocument();
    fireEvent.keyDown(screen.getByRole("dialog"), { key: "Escape" });
    await waitFor(() => expect(trigger).toHaveFocus());
    expect(clearDetail).toHaveBeenCalledWith(issueId);
  });

  it("lazy-loads bounded observation and decision history with load-more recovery", async () => {
    const fetchMoreObservations = vi.fn();
    const fetchMoreDecisions = vi.fn();
    hooks.useHealthSummary.mockReturnValue({ data: summary, isPending: false, isError: false, refetch: vi.fn() });
    hooks.useHealthScan.mockReturnValue({ data: scan, isPending: false, isError: false, refetch: vi.fn() });
    hooks.useHealthIssues.mockReturnValue({ data: { workspaceId, items: [issueListItem], nextCursor: null, hasMore: false }, isPending: false, isError: false, refetch: vi.fn() });
    hooks.useHealthIssue.mockReturnValue({ data: detail, isPending: false, isError: false, refetch: vi.fn() });
    hooks.useHealthIssueObservations.mockImplementation((_selectedIssueId: string, enabled: boolean) => enabled ? {
      data: { pages: [{ workspaceId, issueId, items: [observation], nextCursor: "observation.next", hasMore: true }] },
      isPending: false,
      isError: false,
      isFetchNextPageError: false,
      isFetchingNextPage: false,
      hasNextPage: true,
      error: new Error("unused"),
      refetch: vi.fn(),
      fetchNextPage: fetchMoreObservations,
    } : { ...idleHistory, refetch: vi.fn(), fetchNextPage: fetchMoreObservations });
    hooks.useHealthIssueDecisions.mockImplementation((_selectedIssueId: string, enabled: boolean) => enabled ? {
      data: { pages: [{ workspaceId, issueId, items: [decision], nextCursor: "decision.next", hasMore: true }] },
      isPending: false,
      isError: false,
      isFetchNextPageError: true,
      isFetchingNextPage: false,
      hasNextPage: true,
      error: new Error("下一页暂时不可用"),
      refetch: vi.fn(),
      fetchNextPage: fetchMoreDecisions,
    } : { ...idleHistory, refetch: vi.fn(), fetchNextPage: fetchMoreDecisions });
    hooks.useStartHealthScan.mockReturnValue({ mutate: vi.fn(), isPending: false, isError: false });
    hooks.useDecideHealthIssue.mockReturnValue({ mutate: vi.fn(), isPending: false, isError: false });
    hooks.useClearHealthIssueDetail.mockReturnValue(vi.fn());

    render(<MemoryRouter initialEntries={["/health"]}><Routes><Route path="/health" element={<HealthPage />} /></Routes></MemoryRouter>);
    fireEvent.click(screen.getByRole("button", { name: "查看证据" }));
    expect(hooks.useHealthIssueObservations).toHaveBeenLastCalledWith(issueId, false);
    expect(hooks.useHealthIssueDecisions).toHaveBeenLastCalledWith(issueId, false);

    fireEvent.mouseDown(await screen.findByRole("tab", { name: "观测历史" }), { button: 0, ctrlKey: false });
    expect(await screen.findByText("历史 Evidence")).toBeInTheDocument();
    expect(hooks.useHealthIssueObservations).toHaveBeenLastCalledWith(issueId, true);
    fireEvent.click(screen.getByRole("button", { name: "加载更多观测" }));
    expect(fetchMoreObservations).toHaveBeenCalledTimes(1);

    fireEvent.mouseDown(screen.getByRole("tab", { name: "决策历史" }), { button: 0, ctrlKey: false });
    expect(await screen.findByText("已核实")).toBeInTheDocument();
    expect(hooks.useHealthIssueDecisions).toHaveBeenLastCalledWith(issueId, true);
    expect(screen.getByRole("alert")).toHaveTextContent("加载更多决策失败");
    fireEvent.click(screen.getByRole("button", { name: "重试加载更多决策" }));
    expect(fetchMoreDecisions).toHaveBeenCalledTimes(1);
  });

  it("reuses decision idempotency only for the same payload and keeps repair unavailable", async () => {
    const decide = vi.fn<(payload: { idempotencyKey: string }, options?: unknown) => void>();
    hooks.useHealthSummary.mockReturnValue({ data: summary, isPending: false, isError: false, refetch: vi.fn() });
    hooks.useHealthScan.mockReturnValue({ data: scan, isPending: false, isError: false, refetch: vi.fn() });
    hooks.useHealthIssues.mockReturnValue({ data: { workspaceId, items: [issueListItem], nextCursor: null, hasMore: false }, isPending: false, isError: false, refetch: vi.fn() });
    hooks.useHealthIssue.mockReturnValue({ data: detail, isPending: false, isError: false, refetch: vi.fn() });
    hooks.useHealthIssueObservations.mockReturnValue({ ...idleHistory, refetch: vi.fn(), fetchNextPage: vi.fn() });
    hooks.useHealthIssueDecisions.mockReturnValue({ ...idleHistory, refetch: vi.fn(), fetchNextPage: vi.fn() });
    hooks.useStartHealthScan.mockReturnValue({ mutate: vi.fn(), isPending: false, isError: false });
    hooks.useDecideHealthIssue.mockReturnValue({ mutate: decide, isPending: false, isError: false });
    hooks.useClearHealthIssueDetail.mockReturnValue(vi.fn());

    render(<MemoryRouter initialEntries={["/health"]}><Routes><Route path="/health" element={<HealthPage />} /></Routes></MemoryRouter>);
    fireEvent.click(screen.getByRole("button", { name: "查看证据" }));
    fireEvent.click(await screen.findByRole("button", { name: /确认/ }));
    expect(decide).toHaveBeenCalledWith(expect.objectContaining({ issueId, expectedVersion: 4, action: "ACKNOWLEDGE" }), expect.any(Object));
    fireEvent.click(screen.getByRole("button", { name: /确认/ }));
    const firstKey = decide.mock.calls[0]?.[0]?.idempotencyKey;
    expect(decide.mock.calls[1]?.[0]?.idempotencyKey).toBe(firstKey);
    fireEvent.change(screen.getByLabelText("原因"), { target: { value: "不影响当前知识" } });
    fireEvent.click(screen.getByRole("button", { name: "忽略" }));
    expect(decide.mock.calls[2]?.[0]?.idempotencyKey).not.toBe(firstKey);

    const proposalButtons = screen.getAllByRole("button", { name: /创建提案/ });
    expect(proposalButtons[0]).toBeDisabled();
    expect(proposalButtons[1]).toBeDisabled();
  });

  it("does not reuse an Issue cursor after filters or Workspace change", async () => {
    hooks.useHealthSummary.mockReturnValue({ data: summary, isPending: false, isError: false, refetch: vi.fn() });
    hooks.useHealthScan.mockReturnValue({ data: scan, isPending: false, isError: false, refetch: vi.fn() });
    hooks.useHealthIssue.mockReturnValue({ data: detail, isPending: false, isError: false, refetch: vi.fn() });
    hooks.useHealthIssueObservations.mockReturnValue({ ...idleHistory, refetch: vi.fn(), fetchNextPage: vi.fn() });
    hooks.useHealthIssueDecisions.mockReturnValue({ ...idleHistory, refetch: vi.fn(), fetchNextPage: vi.fn() });
    hooks.useStartHealthScan.mockReturnValue({ mutate: vi.fn(), isPending: false, isError: false });
    hooks.useDecideHealthIssue.mockReturnValue({ mutate: vi.fn(), isPending: false, isError: false });
    hooks.useClearHealthIssueDetail.mockReturnValue(vi.fn());
    hooks.useHealthIssues.mockImplementation((input: { cursor?: string; statuses?: string[]; severities?: string[]; types?: string[]; limit?: number }) => ({ data: { workspaceId: hooks.activeWorkspaceId, items: [issueListItem], nextCursor: input.cursor ? null : "issue.next", hasMore: input.cursor === undefined }, isPending: false, isError: false, refetch: vi.fn() }));

    const view = <MemoryRouter initialEntries={["/health"]}><Routes><Route path="/health" element={<HealthPage />} /></Routes></MemoryRouter>;
    const rendered = render(view);
    fireEvent.click(screen.getByRole("button", { name: /下一页/ }));
    await waitFor(() => expect(hooks.useHealthIssues).toHaveBeenLastCalledWith(expect.objectContaining({ cursor: "issue.next", limit: 25 })));
    fireEvent.change(screen.getByLabelText("严重度"), { target: { value: "HIGH" } });
    await waitFor(() => expect(hooks.useHealthIssues).toHaveBeenLastCalledWith(expect.objectContaining({ severities: ["HIGH"], limit: 25 })));
    expect(hooks.useHealthIssues.mock.calls.at(-1)?.[0]).not.toHaveProperty("cursor");
    hooks.activeWorkspaceId = "16000000-0000-0000-8000-000000000009";
    rendered.rerender(view);
    await waitFor(() => expect(hooks.useHealthIssues.mock.calls.at(-1)?.[0]).not.toHaveProperty("cursor"));
  });
});
