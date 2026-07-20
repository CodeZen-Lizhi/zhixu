import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderHook, waitFor } from "@testing-library/react";
import type { PropsWithChildren } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";

const events = vi.hoisted(() => ({
  connectServerEvents: vi.fn(),
  ServerEventClientError: class extends Error {
    readonly code: string;

    constructor(code: string, message: string) {
      super(message);
      this.code = code;
    }
  },
}));

vi.mock("../../events", () => events);

import { ServerEventClientError } from "../../events";
import { useRagEventRecovery } from "./event-recovery";
import { ragQueryKeys } from "./query-keys";

const workspaceId = "92000000-0000-4000-8000-000000000001";

describe("RAG event recovery", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    window.sessionStorage.clear();
  });

  it("清理损坏的本地游标并在权威回查后无游标重连", async () => {
    const queryClient = new QueryClient();
    const resetQueries = vi.spyOn(queryClient, "resetQueries").mockResolvedValue();
    const invalidateQueries = vi.spyOn(queryClient, "invalidateQueries").mockResolvedValue();
    const close = vi.fn();
    events.connectServerEvents
      .mockImplementationOnce(() => {
        throw new ServerEventClientError("CURSOR_REJECTED", "Last-Event-ID 无效", false);
      })
      .mockReturnValueOnce({ close, done: Promise.resolve(), getLastEventId: () => undefined });
    const storageKey = `zhixu.rag-event-cursor.${workspaceId}`;
    window.sessionStorage.setItem(storageKey, "01");
    const wrapper = ({ children }: PropsWithChildren) => (
      <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
    );

    const { unmount } = renderHook(() => useRagEventRecovery(workspaceId), { wrapper });

    await waitFor(() => expect(events.connectServerEvents).toHaveBeenCalledTimes(2));
    expect(events.connectServerEvents.mock.calls[0]?.[0]).toMatchObject({ lastEventId: "01" });
    expect(events.connectServerEvents.mock.calls[1]?.[0]).not.toHaveProperty("lastEventId");
    expect(resetQueries).toHaveBeenCalledWith({
      queryKey: ragQueryKeys.conversations(workspaceId),
      exact: true,
    });
    expect(invalidateQueries).toHaveBeenCalledWith({ queryKey: ragQueryKeys.all(workspaceId) });
    expect(window.sessionStorage.getItem(storageKey)).toBeNull();

    unmount();
    expect(close).toHaveBeenCalledOnce();
  });

  it("权威回查失败时保留游标且不伪装成已恢复", async () => {
    const queryClient = new QueryClient();
    vi.spyOn(queryClient, "resetQueries").mockRejectedValue(new Error("query failed"));
    const consoleError = vi.spyOn(console, "error").mockImplementation(() => undefined);
    events.connectServerEvents.mockImplementationOnce(() => {
      throw new ServerEventClientError("CURSOR_REJECTED", "Last-Event-ID 无效", false);
    });
    const storageKey = `zhixu.rag-event-cursor.${workspaceId}`;
    window.sessionStorage.setItem(storageKey, "01");
    const wrapper = ({ children }: PropsWithChildren) => (
      <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
    );

    const { result } = renderHook(() => useRagEventRecovery(workspaceId), { wrapper });

    await waitFor(() => expect(consoleError).toHaveBeenCalledOnce());
    expect(result.current).toBe("closed");
    expect(events.connectServerEvents).toHaveBeenCalledOnce();
    expect(window.sessionStorage.getItem(storageKey)).toBe("01");
    expect(consoleError).toHaveBeenCalledWith("RAG SSE cursor recovery failed", expect.any(Error));
  });
});
