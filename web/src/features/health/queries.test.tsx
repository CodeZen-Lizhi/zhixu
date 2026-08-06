import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import type { PropsWithChildren } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";

const active = vi.hoisted(() => ({ workspaceId: "17000000-0000-4000-8000-000000000001" }));

vi.mock("../../app/active-workspace", async (importOriginal) => ({
  // eslint-disable-next-line @typescript-eslint/consistent-type-imports
  ...(await importOriginal<typeof import("../../app/active-workspace")>()),
  useActiveWorkspaceId: () => active.workspaceId,
}));

import { clearHealthIssueDetail, healthIssueHistoryQueryKey, seedHealthIssueHistoryPage, useHealthIssueDecisions, useHealthIssueObservations } from "./queries";
import { healthQueryKeys } from "./query-keys";

const workspaceId = active.workspaceId;
const otherWorkspaceId = "17000000-0000-4000-8000-000000000002";
const issueId = "17000000-0000-4000-8000-000000000003";
const otherIssueId = "17000000-0000-4000-8000-000000000004";
const observationId = "17000000-0000-4000-8000-000000000005";
const scanId = "17000000-0000-4000-8000-000000000006";
const targetId = "17000000-0000-4000-8000-000000000007";
const decisionId = "17000000-0000-4000-8000-000000000008";
const at = "2026-08-05T08:00:00Z";
const hash = "d".repeat(64);
const ref = { type: "CLAIM", id: targetId };
const observation = { id: observationId, issue_version: 2, scan_id: scanId, detector_version: "v1", fingerprint: hash, evidence_fingerprint: hash, target_versions: [{ ref, version: 1 }], severity: "HIGH", observed_at: at, evidence: [{ ref, hash, summary: "历史证据" }] };
const decision = { id: decisionId, issue_version: 2, proposal_id: null, idempotency_key: "decision-1", action: "ACKNOWLEDGE", reason: "已核实", deferred_until: null, created_at: at };

const jsonResponse = (payload: unknown): Response => new Response(JSON.stringify(payload), { status: 200, headers: { "Content-Type": "application/json" } });
const fetchUrl = (input: string | URL | Request | undefined): string => {
  if (typeof input === "string") return input;
  if (input instanceof URL) return input.href;
  if (input instanceof Request) return input.url;
  throw new Error("missing fetch URL");
};
const createHarness = () => {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  const wrapper = ({ children }: PropsWithChildren) => <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>;
  return { queryClient, wrapper };
};

afterEach(() => {
  active.workspaceId = workspaceId;
  vi.unstubAllGlobals();
});

describe("Health history query hooks", () => {
  it("keeps observation and decision history keys isolated by Workspace and Issue", () => {
    expect(healthIssueHistoryQueryKey(workspaceId, issueId, "observations")).not.toEqual(healthIssueHistoryQueryKey(workspaceId, issueId, "decisions"));
    expect(healthIssueHistoryQueryKey(workspaceId, issueId, "observations")).not.toEqual(healthIssueHistoryQueryKey(otherWorkspaceId, issueId, "observations"));
    expect(healthIssueHistoryQueryKey(workspaceId, issueId, "observations")).not.toEqual(healthIssueHistoryQueryKey(workspaceId, otherIssueId, "observations"));
  });

  it("stays idle until enabled, then follows the opaque observation cursor with AbortSignal", async () => {
    const fetchMock = vi.fn<typeof fetch>()
      .mockResolvedValueOnce(jsonResponse({ workspace_id: workspaceId, issue_id: issueId, items: [observation], next_cursor: "observation+next", has_more: true }))
      .mockResolvedValueOnce(jsonResponse({ workspace_id: workspaceId, issue_id: issueId, items: [], has_more: false }));
    vi.stubGlobal("fetch", fetchMock);
    const { wrapper } = createHarness();

    const { result, rerender } = renderHook(({ enabled }) => useHealthIssueObservations(issueId, enabled), { initialProps: { enabled: false }, wrapper });
    expect(fetchMock).not.toHaveBeenCalled();
    rerender({ enabled: true });
    await waitFor(() => expect(result.current.isSuccess && !result.current.isFetching).toBe(true));
    let nextPageResult: Awaited<ReturnType<typeof result.current.fetchNextPage>> | undefined;
    await act(async () => { nextPageResult = await result.current.fetchNextPage(); });

    expect(fetchMock).toHaveBeenCalledTimes(2);
    expect(fetchUrl(fetchMock.mock.calls[0]?.[0])).toContain(`/api/v1/health/issues/${issueId}/observations?workspace_id=${workspaceId}&limit=25`);
    expect(fetchUrl(fetchMock.mock.calls[1]?.[0])).toContain("cursor=observation%2Bnext");
    expect(fetchMock.mock.calls[0]?.[1]?.signal).toBeInstanceOf(AbortSignal);
    expect(nextPageResult?.data?.pages).toHaveLength(2);
  });

  it("loads decision history from its independent endpoint", async () => {
    const fetchMock = vi.fn<typeof fetch>().mockResolvedValue(jsonResponse({ workspace_id: workspaceId, issue_id: issueId, items: [decision], has_more: false }));
    vi.stubGlobal("fetch", fetchMock);
    const { wrapper } = createHarness();
    const { result } = renderHook(() => useHealthIssueDecisions(issueId, true), { wrapper });

    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data?.pages[0]?.items[0]).toMatchObject({ id: decisionId, action: "ACKNOWLEDGE" });
    expect(fetchUrl(fetchMock.mock.calls[0]?.[0])).toContain(`/api/v1/health/issues/${issueId}/decisions?workspace_id=${workspaceId}&limit=25`);
  });

  it("reuses the detail first page and fetches only its continuation cursor", async () => {
    const fetchMock = vi.fn<typeof fetch>().mockResolvedValue(jsonResponse({ workspace_id: workspaceId, issue_id: issueId, items: [], has_more: false }));
    vi.stubGlobal("fetch", fetchMock);
    const { queryClient, wrapper } = createHarness();
    seedHealthIssueHistoryPage(queryClient, "observations", { workspaceId, issueId, items: [], nextCursor: "from-detail", hasMore: true });

    const { result } = renderHook(() => useHealthIssueObservations(issueId, true), { wrapper });
    expect(result.current.data?.pages).toHaveLength(1);
    expect(fetchMock).not.toHaveBeenCalled();
    await act(async () => { await result.current.fetchNextPage(); });

    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(fetchUrl(fetchMock.mock.calls[0]?.[0])).toContain("cursor=from-detail");
    await waitFor(() => expect(result.current.data?.pages).toHaveLength(2));
  });

  it("clears detail and both history caches without touching another Issue", () => {
    const { queryClient } = createHarness();
    const detailKey = healthQueryKeys.issue(workspaceId, issueId);
    const observationKey = healthIssueHistoryQueryKey(workspaceId, issueId, "observations");
    const decisionKey = healthIssueHistoryQueryKey(workspaceId, issueId, "decisions");
    const otherKey = healthIssueHistoryQueryKey(workspaceId, otherIssueId, "observations");
    queryClient.setQueryData(detailKey, "detail");
    queryClient.setQueryData(observationKey, "observations");
    queryClient.setQueryData(decisionKey, "decisions");
    queryClient.setQueryData(otherKey, "other");

    clearHealthIssueDetail(queryClient, workspaceId, issueId);

    expect(queryClient.getQueryData(detailKey)).toBeUndefined();
    expect(queryClient.getQueryData(observationKey)).toBeUndefined();
    expect(queryClient.getQueryData(decisionKey)).toBeUndefined();
    expect(queryClient.getQueryData(otherKey)).toBe("other");
  });
});
