import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";

import {
  TimelineApiError,
  type ImpactAnalysisResult,
  type ImpactReportV2,
  type TimelineEventV2,
} from "../../api/timeline";

const workspaceState = vi.hoisted(() => ({ id: "76000000-0000-4000-8000-000000000001" }));
const queryMocks = vi.hoisted(() => ({
  canonicalTimelineFilter: vi.fn((filter: unknown) => JSON.stringify(filter)),
  useAnalyzeImpact: vi.fn(),
  useCreateDownstreamUpdateProposal: vi.fn(),
  useImpactReport: vi.fn(),
  useTimelineEvent: vi.fn(),
  useTimelinePage: vi.fn(),
}));

vi.mock("../../app/active-workspace", () => ({ useActiveWorkspaceId: () => workspaceState.id }));
vi.mock("./queries", () => queryMocks);

import { TimelineEventPage, TimelinePage } from "./TimelinePage";

const workspaceId = "76000000-0000-4000-8000-000000000001";
const eventId = "76000000-0000-4000-8000-000000000002";
const aggregateId = "76000000-0000-4000-8000-000000000003";
const reportId = "76000000-0000-4000-8000-000000000004";
const artifactId = "76000000-0000-4000-8000-000000000005";
const revisionId = "76000000-0000-4000-8000-000000000006";
const relationId = "76000000-0000-4000-8000-000000000007";
const proposalId = "76000000-0000-4000-8000-000000000008";
const at = "2026-07-29T02:03:04Z";

const eventFixture = (): TimelineEventV2 => ({
  id: eventId,
  workspaceId,
  eventType: "CONFLICT_OPENED",
  aggregateType: "CONFLICT",
  aggregateId,
  sourceEventRef: "conflict.opened:7600",
  sourceRef: "conflict:7600",
  eventVersion: 1,
  schemaVersion: "knowledge-event/v2",
  summary: "Knowledge conflict opened",
  payload: {},
  correlation: {},
  operator: { type: "SYSTEM" },
  ownerBinding: null,
  occurredAt: at,
  createdAt: at,
});

const reportFixture = (supersededByReportId: string | null = null): ImpactReportV2 => ({
  id: reportId,
  workspaceId,
  sourceEventId: eventId,
  sourceEventRef: "conflict.opened:7600",
  sourceEventVersion: 1,
  status: "READY",
  objects: [
    {
      type: "ARTIFACT",
      id: artifactId,
      workspaceId,
      version: 3,
      action: "REGENERATE_ARTIFACT",
      reason: "Artifact cites changed evidence",
      requiresProposal: true,
      artifactBinding: {
        artifactId,
        artifactVersion: 3,
        revisionId,
        revisionNo: 2,
        contentHash: "a".repeat(64),
      },
    },
    {
      type: "RELATION",
      id: relationId,
      workspaceId,
      version: 1,
      action: "REVIEW",
      reason: "Inspect the formal relation",
      requiresProposal: false,
    },
  ],
  summary: { ARTIFACT: 1, RELATION: 1, "action:REGENERATE_ARTIFACT": 1, "action:REVIEW": 1, requires_proposal: 1 },
  fingerprint: "b".repeat(64),
  generatedAt: at,
  createdAt: at,
  version: 2,
  schemaVersion: "impact-report/v2",
  analysisVersion: "impact-analysis/v2",
  supersedesReportId: null,
  supersededByReportId,
});

const idleAnalyze = (mutate = vi.fn()) => ({
  isPending: false,
  isError: false,
  error: null,
  variables: undefined,
  data: undefined,
  mutate,
});

const idleCreate = (mutate = vi.fn()) => ({
  isPending: false,
  isError: false,
  error: null,
  variables: undefined,
  mutate,
});

const LocationProbe = () => {
  const location = useLocation();
  return <output data-testid="location">{location.pathname}{location.search}</output>;
};

const renderTimeline = (entry = "/timeline") => render(
  <MemoryRouter initialEntries={[entry]}>
    <LocationProbe />
    <Routes><Route path="/timeline" element={<TimelinePage />} /></Routes>
  </MemoryRouter>,
);

const renderEvent = () => render(
  <MemoryRouter initialEntries={[`/timeline/${eventId}`]}>
    <LocationProbe />
    <Routes>
      <Route path="/timeline/:eventId" element={<TimelineEventPage />} />
      <Route path="/proposals/:proposalId" element={<p>Proposal destination</p>} />
    </Routes>
  </MemoryRouter>,
);

beforeEach(() => {
  workspaceState.id = workspaceId;
  for (const mock of Object.values(queryMocks)) mock.mockReset();
  queryMocks.canonicalTimelineFilter.mockImplementation((filter: unknown) => JSON.stringify(filter));
  queryMocks.useTimelinePage.mockReturnValue({
    isPending: false,
    isError: false,
    error: null,
    data: { workspaceId, items: [] },
    isFetching: false,
    isSuccess: true,
    refetch: vi.fn(),
  });
  queryMocks.useTimelineEvent.mockReturnValue({
    isPending: false,
    isError: false,
    error: null,
    data: eventFixture(),
    refetch: vi.fn(),
  });
  queryMocks.useAnalyzeImpact.mockReturnValue(idleAnalyze());
  queryMocks.useImpactReport.mockReturnValue({
    isPending: false,
    isError: false,
    error: null,
    data: reportFixture(),
    refetch: vi.fn(),
  });
  queryMocks.useCreateDownstreamUpdateProposal.mockReturnValue(idleCreate());
});

describe("Timeline workbench", () => {
  it("shows an explicit boundary when no Workspace is active", () => {
    workspaceState.id = "";
    renderTimeline();

    expect(screen.getByText("先连接 Workspace")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "连接 Workspace" })).toHaveAttribute("href", "/workspace");
  });

  it("renders loading, empty, and retryable 503 states without fake success", () => {
    queryMocks.useTimelinePage.mockReturnValueOnce({
      isPending: true,
      isError: false,
      error: null,
      data: undefined,
      isFetching: true,
      isSuccess: false,
      refetch: vi.fn(),
    });
    const loading = renderTimeline();
    expect(screen.getByText("正在读取 Timeline 事件…")).toBeInTheDocument();
    loading.unmount();

    renderTimeline();
    expect(screen.getByText("没有匹配事件")).toBeInTheDocument();
  });

  it("offers an explicit retry for a 503 list response", () => {
    const refetch = vi.fn();
    queryMocks.useTimelinePage.mockReturnValue({
      isPending: false,
      isError: true,
      error: new TimelineApiError("HTTP_ERROR", "KNOWLEDGE_TIMELINE_UNAVAILABLE", "dependency unavailable", true, 503),
      data: undefined,
      isFetching: false,
      isSuccess: false,
      refetch,
    });
    renderTimeline();

    expect(screen.getByText("Timeline / Impact 暂不可用")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "重试" }));
    expect(refetch).toHaveBeenCalledTimes(1);
  });

  it("keeps a local cursor history and clears it as soon as a URL filter changes", async () => {
    queryMocks.useTimelinePage.mockReturnValue({
      isPending: false,
      isError: false,
      error: null,
      data: { workspaceId, items: [eventFixture()], nextCursor: "opaque-next" },
      isFetching: false,
      isSuccess: true,
      refetch: vi.fn(),
    });
    renderTimeline("/timeline?aggregate_type=CONFLICT");

    fireEvent.click(screen.getByRole("button", { name: "下一页" }));
    await waitFor(() => expect(queryMocks.useTimelinePage).toHaveBeenLastCalledWith({ aggregateType: "CONFLICT" }, "opaque-next", 25));
    fireEvent.click(screen.getByRole("button", { name: "上一页" }));
    await waitFor(() => expect(queryMocks.useTimelinePage).toHaveBeenLastCalledWith({ aggregateType: "CONFLICT" }, undefined, 25));

    fireEvent.click(screen.getByRole("button", { name: "下一页" }));
    await waitFor(() => expect(queryMocks.useTimelinePage).toHaveBeenLastCalledWith({ aggregateType: "CONFLICT" }, "opaque-next", 25));

    fireEvent.change(screen.getByLabelText("Source event ref"), { target: { value: "conflict.opened:7600" } });
    fireEvent.click(screen.getByRole("button", { name: "应用筛选" }));
    await waitFor(() => {
      expect(screen.getByTestId("location")).toHaveTextContent("source_event_ref=conflict.opened%3A7600");
      expect(queryMocks.useTimelinePage).toHaveBeenLastCalledWith({ aggregateType: "CONFLICT", sourceEventRef: "conflict.opened:7600" }, undefined, 25);
    });
  });

  it("retries a failed later page without dropping its cursor", async () => {
    const refetch = vi.fn();
    queryMocks.useTimelinePage.mockImplementation((_filter: unknown, cursor: string | undefined) => cursor === undefined
      ? {
          isPending: false,
          isError: false,
          error: null,
          data: { workspaceId, items: [eventFixture()], nextCursor: "opaque-next" },
          isFetching: false,
          isSuccess: true,
          refetch: vi.fn(),
        }
      : {
          isPending: false,
          isError: true,
          error: new TimelineApiError("HTTP_ERROR", "KNOWLEDGE_TIMELINE_UNAVAILABLE", "page unavailable", true, 500),
          data: undefined,
          isFetching: false,
          isSuccess: false,
          refetch,
        });
    const view = renderTimeline();

    fireEvent.click(screen.getByRole("button", { name: "下一页" }));
    await waitFor(() => expect(screen.getByText("事件流读取失败")).toBeInTheDocument());
    fireEvent.click(screen.getByRole("button", { name: "重试" }));
    expect(refetch).toHaveBeenCalledTimes(1);
    view.rerender(
      <MemoryRouter initialEntries={["/timeline"]}>
        <LocationProbe />
        <Routes><Route path="/timeline" element={<TimelinePage />} /></Routes>
      </MemoryRouter>,
    );
    expect(queryMocks.useTimelinePage).toHaveBeenLastCalledWith({}, "opaque-next", 25);
  });

  it("renders a stable 404 event state", () => {
    queryMocks.useTimelineEvent.mockReturnValue({
      isPending: false,
      isError: true,
      error: new TimelineApiError("HTTP_ERROR", "KNOWLEDGE_TIMELINE_NOT_FOUND", "not found", false, 404),
      data: undefined,
      refetch: vi.fn(),
    });
    renderEvent();

    expect(screen.getByText("事件不存在")).toBeInTheDocument();
    expect(screen.getByText("资源不存在，或不属于当前 Workspace。")).toBeInTheDocument();
  });

  it("reuses an Impact Idempotency-Key after an unknown result and rotates it for a new intent", () => {
    const original = { workspaceId, eventId, idempotencyKey: "impact-original-key" };
    const mutate = vi.fn();
    queryMocks.useAnalyzeImpact.mockReturnValue({
      isPending: false,
      isError: true,
      error: new TimelineApiError("NETWORK_ERROR", "NETWORK_ERROR", "lost response", true),
      variables: original,
      data: undefined,
      mutate,
    });
    renderEvent();

    fireEvent.click(screen.getByRole("button", { name: "重试原分析请求" }));
    expect(mutate.mock.calls[0]?.[0]).toBe(original);
    fireEvent.click(screen.getByRole("button", { name: "发起新的分析意图" }));
    expect(mutate.mock.calls[1]?.[0]).toMatchObject({ workspaceId, eventId });
    expect(mutate.mock.calls[1]?.[0]).not.toEqual(original);
  });

  it("only offers Proposal creation for a latest owner-backed target and navigates on success", async () => {
    const report = reportFixture();
    const analyzeResult: ImpactAnalysisResult = { report, proposalDrafts: [], replayed: false };
    const analyzeMutate = vi.fn((_variables: unknown, options?: { onSuccess?: (result: ImpactAnalysisResult) => void }) => {
      options?.onSuccess?.(analyzeResult);
    });
    const createMutate = vi.fn((_variables: unknown, options?: { onSuccess?: (proposal: { id: string }) => void }) => {
      options?.onSuccess?.({ id: proposalId });
    });
    queryMocks.useAnalyzeImpact.mockReturnValue(idleAnalyze(analyzeMutate));
    queryMocks.useCreateDownstreamUpdateProposal.mockReturnValue(idleCreate(createMutate));
    renderEvent();

    fireEvent.click(screen.getByRole("button", { name: "发起分析" }));
    expect(await screen.findByText("impact-analysis/v2 · READY")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /打开 Artifact/ })).toHaveAttribute("href", `/artifacts/${artifactId}`);
    expect(screen.getAllByRole("button", { name: "创建 Proposal" })).toHaveLength(1);
    expect(screen.getByText("Inspect the formal relation").closest("article")).toHaveTextContent("只读影响");

    fireEvent.click(screen.getByRole("button", { name: "创建 Proposal" }));
    expect(createMutate.mock.calls[0]?.[0]).toMatchObject({
      workspaceId,
      reportId,
      targetType: "ARTIFACT",
      targetId: artifactId,
      action: "REGENERATE_ARTIFACT",
    });
    expect(await screen.findByText("Proposal destination")).toBeInTheDocument();
    expect(screen.getByTestId("location")).toHaveTextContent(`/proposals/${proposalId}`);
  });

  it("does not expose a Proposal action for a superseded report", async () => {
    const report = reportFixture("76000000-0000-4000-8000-000000000009");
    const analyzeMutate = vi.fn((_variables: unknown, options?: { onSuccess?: (result: ImpactAnalysisResult) => void }) => {
      options?.onSuccess?.({ report, proposalDrafts: [], replayed: false });
    });
    queryMocks.useAnalyzeImpact.mockReturnValue(idleAnalyze(analyzeMutate));
    queryMocks.useImpactReport.mockReturnValue({ isPending: false, isError: false, error: null, data: report, refetch: vi.fn() });
    renderEvent();

    fireEvent.click(screen.getByRole("button", { name: "发起分析" }));
    expect(await screen.findByText("已被替代")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "创建 Proposal" })).not.toBeInTheDocument();
  });

  it("retries an unknown Proposal result with the exact report/target/action/key binding", async () => {
    const variables = {
      workspaceId,
      reportId,
      targetType: "ARTIFACT" as const,
      targetId: artifactId,
      action: "REGENERATE_ARTIFACT" as const,
      idempotencyKey: "downstream-original-key",
    };
    const report = reportFixture();
    const analyzeMutate = vi.fn((_input: unknown, options?: { onSuccess?: (result: ImpactAnalysisResult) => void }) => {
      options?.onSuccess?.({ report, proposalDrafts: [], replayed: false });
    });
    const createMutate = vi.fn();
    queryMocks.useAnalyzeImpact.mockReturnValue(idleAnalyze(analyzeMutate));
    queryMocks.useCreateDownstreamUpdateProposal.mockReturnValue({
      isPending: false,
      isError: true,
      error: new TimelineApiError("NETWORK_ERROR", "NETWORK_ERROR", "lost response", true),
      variables,
      mutate: createMutate,
    });
    renderEvent();

    fireEvent.click(screen.getByRole("button", { name: "发起分析" }));
    fireEvent.click(await screen.findByRole("button", { name: "重试原创建请求" }));
    expect(createMutate.mock.calls[0]?.[0]).toBe(variables);
  });
});
