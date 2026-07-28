import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook } from "@testing-library/react";
import { type PropsWithChildren } from "react";
import { describe, expect, it, vi } from "vitest";

const apiMocks = vi.hoisted(() => ({
  confirmMemory: vi.fn(),
  createMemoryCandidate: vi.fn(),
  deleteMemory: vi.fn(),
  editMemory: vi.fn(),
  getMemory: vi.fn(),
  listMemories: vi.fn(),
  pauseMemory: vi.fn(),
  resumeMemory: vi.fn(),
}));

vi.mock("../../api/memory", () => apiMocks);

import { clearMemoryWorkspaceQueries, useConfirmMemory } from "./queries";
import { memoryQueryKeys } from "./query-keys";

const workspaceId = "73000000-0000-4000-8000-000000000001";
const otherWorkspaceId = "73000000-0000-4000-8000-000000000002";
const memoryId = "73000000-0000-4000-8000-000000000003";

const wrapperFor = (queryClient: QueryClient) => ({ children }: PropsWithChildren) => (
  <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
);

describe("Memory query keys", () => {
  it("按 Workspace 隔离并只清除离开的 Workspace 缓存", () => {
    const queryClient = new QueryClient();
    queryClient.setQueryData(memoryQueryKeys.list(workspaceId, [], []), { items: ["old"] });
    queryClient.setQueryData(memoryQueryKeys.list(otherWorkspaceId, [], []), { items: ["current"] });

    clearMemoryWorkspaceQueries(queryClient, workspaceId);

    expect(queryClient.getQueryData(memoryQueryKeys.list(workspaceId, [], []))).toBeUndefined();
    expect(queryClient.getQueryData(memoryQueryKeys.list(otherWorkspaceId, [], []))).toEqual({ items: ["current"] });
  });

  it("命令适配器不会把 TanStack MutationContext 误传为 AbortSignal", async () => {
    const input = { workspaceId, memoryId, expectedVersion: 1, idempotencyKey: "memory-confirm-test" };
    apiMocks.confirmMemory.mockResolvedValue({ memory: { id: memoryId, workspaceId }, replayed: false });
    const queryClient = new QueryClient({ defaultOptions: { mutations: { retry: false } } });
    const { result } = renderHook(() => useConfirmMemory(), { wrapper: wrapperFor(queryClient) });

    await act(async () => { await result.current.mutateAsync(input); });

    expect(apiMocks.confirmMemory).toHaveBeenCalledWith(input);
    expect(apiMocks.confirmMemory.mock.calls[0]).toHaveLength(1);
  });
});
