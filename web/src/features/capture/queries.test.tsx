import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import type { PropsWithChildren } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";

import type { Capture, CaptureCommandResult, CaptureRetryInput } from "../../api/captures";
import { captureQueryKeys } from "./query-keys";

const api = vi.hoisted(() => ({
  createCapture: vi.fn(),
  getCapture: vi.fn(),
  listCaptures: vi.fn(),
  retryCapture: vi.fn(),
}));
const activeWorkspace = vi.hoisted(() => ({ id: "95000000-0000-4000-8000-000000000001" }));

vi.mock("../../api/captures", () => api);
vi.mock("../../app/active-workspace", () => ({ useActiveWorkspaceId: () => activeWorkspace.id }));

import { useCaptures, useRetryCapture } from "./queries";

const workspaceId = "95000000-0000-4000-8000-000000000001";
const captureId = "95000000-0000-4000-8000-000000000003";

const capture = (version: number): Capture => ({
  id: captureId,
  workspaceId,
  kind: "URL",
  displayName: "example.com",
  originalUrl: "https://example.com/article",
  sourceId: "95000000-0000-4000-8000-000000000004",
  status: version === 3 ? "FETCH_FAILED" : "RECEIVED",
  fetchStatus: version === 3 ? "FAILED" : "PENDING",
  ingestionStatus: "PENDING",
  indexStatus: "PENDING",
  profileStatus: "PENDING",
  ...(version === 3 ? { failureStage: "FETCH", errorCode: "CAPTURE_FETCH_TIMEOUT" } : {}),
  retryable: version === 3,
  version,
  capturedAt: "2026-08-02T10:00:00Z",
  updatedAt: "2026-08-02T10:01:00Z",
  detailHref: `/api/v1/workspaces/${workspaceId}/captures/${captureId}`,
});

const wrapperFor = (queryClient: QueryClient) => ({ children }: PropsWithChildren) => (
  <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
);

afterEach(() => {
  activeWorkspace.id = workspaceId;
  api.createCapture.mockReset();
  api.getCapture.mockReset();
  api.listCaptures.mockReset();
  api.retryCapture.mockReset();
});

describe("Capture Query ownership", () => {
  it("canonicalizes every list condition inside a Workspace-bound key", () => {
    expect(captureQueryKeys.list(workspaceId)).toEqual([
      "captures",
      workspaceId,
      "list",
      { kind: null, status: null, limit: 30, cursor: null },
    ]);
    expect(captureQueryKeys.list(workspaceId, {
      kind: "URL",
      status: "FETCH_FAILED",
      limit: 10,
      cursor: "next",
    })).toEqual([
      "captures",
      workspaceId,
      "list",
      { kind: "URL", status: "FETCH_FAILED", limit: 10, cursor: "next" },
    ]);
  });

  it("uses the active Workspace for list reads", async () => {
    api.listCaptures.mockResolvedValue({ workspaceId, items: [] });
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const params = { kind: "URL" as const, limit: 5 };
    const view = renderHook(() => useCaptures(params), { wrapper: wrapperFor(queryClient) });

    await waitFor(() => expect(view.result.current.isSuccess).toBe(true));
    expect(api.listCaptures.mock.calls[0]?.[0]).toBe(workspaceId);
    expect(api.listCaptures.mock.calls[0]?.[1]).toBe(params);
    expect(api.listCaptures.mock.calls[0]?.[2]).toBeInstanceOf(AbortSignal);
  });

  it("preserves the exact retry variables and refetches without optimistic detail replacement", async () => {
    const result: CaptureCommandResult = { capture: capture(4), replayed: false };
    api.retryCapture.mockResolvedValue(result);
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
    });
    queryClient.setQueryData(captureQueryKeys.detail(workspaceId, captureId), capture(3));
    const invalidate = vi.spyOn(queryClient, "invalidateQueries");
    const view = renderHook(() => useRetryCapture(), { wrapper: wrapperFor(queryClient) });
    const input: CaptureRetryInput = {
      workspaceId,
      captureId,
      expectedVersion: 3,
      idempotencyKey: "quick-capture-retry-original",
    };

    act(() => view.result.current.mutate(input));
    await waitFor(() => expect(view.result.current.isSuccess).toBe(true));

    expect(api.retryCapture.mock.calls[0]?.[0]).toBe(input);
    expect(view.result.current.variables).toBe(input);
    expect(invalidate).toHaveBeenCalledWith({
      queryKey: captureQueryKeys.lists(workspaceId),
      refetchType: "active",
    });
    expect(invalidate).toHaveBeenCalledWith({
      queryKey: captureQueryKeys.detail(workspaceId, captureId),
      exact: true,
      refetchType: "active",
    });
    expect(queryClient.getQueryData<Capture>(captureQueryKeys.detail(workspaceId, captureId))?.version).toBe(3);
  });
});
