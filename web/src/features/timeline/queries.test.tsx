import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import type { PropsWithChildren } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";

import type { TimelineFilter } from "../../api/timeline";

const workspaceState = vi.hoisted(() => ({ id: "75000000-0000-4000-8000-000000000001" }));
const apiMocks = vi.hoisted(() => ({
  analyzeImpact: vi.fn(),
  createDownstreamUpdateProposal: vi.fn(),
  getImpactReport: vi.fn(),
  getTimelineEvent: vi.fn(),
  listTimeline: vi.fn(),
}));

vi.mock("../../app/active-workspace", () => ({ useActiveWorkspaceId: () => workspaceState.id }));
vi.mock("../../api/timeline", () => apiMocks);

import {
  canonicalTimelineFilter,
  useAnalyzeImpact,
  useCreateDownstreamUpdateProposal,
  useTimelinePage,
} from "./queries";
import { clearTimelineWorkspaceQueries, timelineQueryKeys } from "./query-keys";

const workspaceId = "75000000-0000-4000-8000-000000000001";
const otherWorkspaceId = "75000000-0000-4000-8000-000000000002";
const eventId = "75000000-0000-4000-8000-000000000003";
const reportId = "75000000-0000-4000-8000-000000000004";
const proposalId = "75000000-0000-4000-8000-000000000005";

const wrapperFor = (queryClient: QueryClient) => ({ children }: PropsWithChildren) => (
  <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
);

const queryClient = (): QueryClient => new QueryClient({
  defaultOptions: { queries: { retry: false, gcTime: Infinity }, mutations: { retry: false } },
});

const impactReport = () => ({
  id: reportId,
  workspaceId,
  sourceEventId: eventId,
  sourceEventRef: "conflict.opened:7500",
  sourceEventVersion: 1,
  status: "READY" as const,
  objects: [],
  summary: {},
  fingerprint: "a".repeat(64),
  generatedAt: "2026-07-29T00:00:00Z",
  createdAt: "2026-07-29T00:00:00Z",
  version: 2,
  schemaVersion: "impact-report/v2" as const,
  analysisVersion: "impact-analysis/v2" as const,
  supersedesReportId: null,
  supersededByReportId: null,
});

afterEach(() => {
  workspaceState.id = workspaceId;
  for (const mock of Object.values(apiMocks)) mock.mockReset();
});

describe("Timeline query boundary", () => {
  it("canonicalizes filters and binds every list window to the active Workspace", async () => {
    apiMocks.listTimeline.mockImplementation((input: { workspaceId: string }) => Promise.resolve({ workspaceId: input.workspaceId, items: [] }));
    const client = queryClient();
    const filter: TimelineFilter = { eventTypes: ["VERSION_SUPERSEDED", "CONFLICT_OPENED", "CONFLICT_OPENED"], aggregateType: "CONFLICT" };
    const { result, rerender } = renderHook(() => useTimelinePage(filter, undefined, 25), { wrapper: wrapperFor(client) });

    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(apiMocks.listTimeline).toHaveBeenCalledWith({
      workspaceId,
      eventTypes: ["CONFLICT_OPENED", "VERSION_SUPERSEDED"],
      aggregateType: "CONFLICT",
      limit: 25,
    }, expect.any(AbortSignal));
    const canonical = canonicalTimelineFilter(filter);
    expect(client.getQueryData(timelineQueryKeys.list(workspaceId, canonical, 25))).toEqual({ workspaceId, items: [] });

    workspaceState.id = otherWorkspaceId;
    rerender();
    await waitFor(() => expect(apiMocks.listTimeline).toHaveBeenCalledTimes(2));
    expect(apiMocks.listTimeline.mock.calls[1]?.[0]).toMatchObject({ workspaceId: otherWorkspaceId });
  });

  it("does not issue a Timeline request without an active Workspace", () => {
    workspaceState.id = "";
    const client = queryClient();
    const { result } = renderHook(() => useTimelinePage({}, undefined, 25), { wrapper: wrapperFor(client) });

    expect(result.current.fetchStatus).toBe("idle");
    expect(apiMocks.listTimeline).not.toHaveBeenCalled();
  });

  it("clears only the selected Workspace Timeline cache", () => {
    const client = queryClient();
    client.setQueryData(timelineQueryKeys.list(workspaceId, "{}", 25), { workspaceId, items: [] });
    client.setQueryData(timelineQueryKeys.event(workspaceId, eventId), { id: eventId });
    client.setQueryData(timelineQueryKeys.list(otherWorkspaceId, "{}", 25), { workspaceId: otherWorkspaceId, items: [] });

    clearTimelineWorkspaceQueries(client, workspaceId);

    expect(client.getQueryData(timelineQueryKeys.list(workspaceId, "{}", 25))).toBeUndefined();
    expect(client.getQueryData(timelineQueryKeys.event(workspaceId, eventId))).toBeUndefined();
    expect(client.getQueryData(timelineQueryKeys.list(otherWorkspaceId, "{}", 25))).toEqual({ workspaceId: otherWorkspaceId, items: [] });
  });

  it("stores an Impact result without racing the asynchronous Timeline projector", async () => {
    const report = impactReport();
    apiMocks.analyzeImpact.mockResolvedValue({ report, proposalDrafts: [], replayed: false });
    const client = queryClient();
    const listKey = timelineQueryKeys.list(workspaceId, "{}", 25);
    const eventKey = timelineQueryKeys.event(workspaceId, eventId);
    client.setQueryData(listKey, { workspaceId, items: [] });
    client.setQueryData(eventKey, { id: eventId });
    const { result } = renderHook(() => useAnalyzeImpact(), { wrapper: wrapperFor(client) });

    await act(async () => {
      await result.current.mutateAsync({ workspaceId, eventId, idempotencyKey: "impact-query-1" });
    });

    expect(apiMocks.analyzeImpact).toHaveBeenCalledWith({ workspaceId, eventId, idempotencyKey: "impact-query-1" });
    expect(client.getQueryData(timelineQueryKeys.report(workspaceId, reportId))).toEqual(report);
    await waitFor(() => {
      expect(client.getQueryState(listKey)?.isInvalidated).toBe(false);
      expect(client.getQueryState(eventKey)?.isInvalidated).toBe(true);
    });
  });

  it("invalidates the source report after downstream Proposal creation", async () => {
    apiMocks.createDownstreamUpdateProposal.mockResolvedValue({
      id: proposalId,
      workspaceId,
      revision: { update: { sourceReport: { id: reportId } } },
    });
    const client = queryClient();
    const reportKey = timelineQueryKeys.report(workspaceId, reportId);
    const proposalListKey = ["business", workspaceId, "proposals", "ready_for_review"] as const;
    client.setQueryData(reportKey, impactReport());
    client.setQueryData(proposalListKey, { items: [] });
    const { result } = renderHook(() => useCreateDownstreamUpdateProposal(), { wrapper: wrapperFor(client) });

    await act(async () => {
      await result.current.mutateAsync({
        workspaceId,
        reportId,
        targetType: "ARTIFACT",
        targetId: "75000000-0000-4000-8000-000000000006",
        action: "REGENERATE_ARTIFACT",
        idempotencyKey: "downstream-query-1",
      });
    });

    expect(apiMocks.createDownstreamUpdateProposal).toHaveBeenCalledWith(expect.objectContaining({ workspaceId, reportId, idempotencyKey: "downstream-query-1" }));
    await waitFor(() => {
      expect(client.getQueryState(reportKey)?.isInvalidated).toBe(true);
      expect(client.getQueryState(proposalListKey)?.isInvalidated).toBe(true);
    });
  });
});
