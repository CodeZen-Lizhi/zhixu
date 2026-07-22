import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { setActiveWorkspaceId } from "../app/active-workspace";
import type { ConnectServerEventsOptions, ServerEventEnvelope } from "./server-events";

const connectionMock = vi.hoisted(() => ({
  options: [] as ConnectServerEventsOptions[],
  close: vi.fn(),
}));

vi.mock("./server-events", async (importOriginal) => {
  // eslint-disable-next-line @typescript-eslint/consistent-type-imports
  const actual = await importOriginal<typeof import("./server-events")>();
  return {
    ...actual,
    connectServerEvents: vi.fn((options: ConnectServerEventsOptions) => {
      connectionMock.options.push(options);
      return {
        close: connectionMock.close,
        done: Promise.resolve(),
        getLastEventId: () => options.lastEventId,
      };
    }),
  };
});

import { CollectionHealthEventBridge } from "./collection-health-events";

const workspaceId = "7a000000-0000-4000-8000-000000000001";
const scanId = "7a000000-0000-4000-8000-000000000011";
const collectionId = "7a000000-0000-4000-8000-000000000012";

const event = (
  id: string,
  type: string,
  resourceRef: string,
  resource: ServerEventEnvelope["invalidations"][number]["resource"],
): ServerEventEnvelope => ({
  schemaVersion: 1,
  id,
  type,
  occurredAt: "2026-07-22T00:00:00Z",
  workspaceId,
  resourceRef,
  resourceVersion: 1,
  payloadSummary: {},
  invalidations: [{ resource, id: resourceRef.split(":")[1] ?? scanId }],
});

const renderBridge = (path = "/health") => {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter initialEntries={[path]}>
        <CollectionHealthEventBridge><output>ready</output></CollectionHealthEventBridge>
      </MemoryRouter>
    </QueryClientProvider>,
  );
  return queryClient;
};

const memoryStorage = (): Storage => {
  const values = new Map<string, string>();
  return {
    get length() { return values.size; },
    clear: () => values.clear(),
    getItem: (key) => values.get(key) ?? null,
    key: (index) => [...values.keys()][index] ?? null,
    removeItem: (key) => { values.delete(key); },
    setItem: (key, value) => { values.set(key, value); },
  };
};

beforeEach(() => {
  Object.defineProperty(window, "localStorage", { configurable: true, value: memoryStorage() });
  Object.defineProperty(window, "sessionStorage", { configurable: true, value: memoryStorage() });
  setActiveWorkspaceId(workspaceId);
  window.sessionStorage.clear();
});

afterEach(() => {
  setActiveWorkspaceId("");
  connectionMock.options.length = 0;
  connectionMock.close.mockClear();
});

describe("CollectionHealthEventBridge", () => {
  it("只在 Collection/Health 路由建立连接", () => {
    renderBridge("/graph");
    expect(screen.getByText("ready")).toBeInTheDocument();
    expect(connectionMock.options).toHaveLength(0);
  });

  it("Health 完成事件同时失效 Health 与 Collection results 后提交游标", async () => {
    const queryClient = renderBridge();
    const invalidate = vi.spyOn(queryClient, "invalidateQueries").mockResolvedValue(undefined);

    await act(async () => connectionMock.options[0]?.onEvent?.(
      event("43", "health.scan.completed", `health_scan:${scanId}`, "health_scan"),
    ));

    expect(invalidate).toHaveBeenCalledWith({ queryKey: ["knowledge-health", workspaceId] });
    expect(invalidate).toHaveBeenCalledWith({ queryKey: ["collections", workspaceId, "results"] });
    expect(window.sessionStorage.getItem(`zhixu.collection-health-event-cursor.${workspaceId}`)).toBe("43");
  });

  it("Collection 事件刷新 Collection family", async () => {
    const queryClient = renderBridge("/collections");
    const invalidate = vi.spyOn(queryClient, "invalidateQueries").mockResolvedValue(undefined);

    await act(async () => connectionMock.options[0]?.onEvent?.(
      event("44", "collection.updated", `collection:${collectionId}`, "collection"),
    ));

    expect(invalidate).toHaveBeenCalledWith({ queryKey: ["collections", workspaceId] });
    expect(invalidate).not.toHaveBeenCalledWith({ queryKey: ["knowledge-health", workspaceId] });
  });

  it("失效失败时不提前提交游标", async () => {
    const queryClient = renderBridge();
    vi.spyOn(queryClient, "invalidateQueries").mockRejectedValue(new Error("cache unavailable"));

    await expect(connectionMock.options[0]?.onEvent?.(
      event("45", "health.scan.completed", `health_scan:${scanId}`, "health_scan"),
    )).rejects.toThrow("cache unavailable");

    expect(window.sessionStorage.getItem(`zhixu.collection-health-event-cursor.${workspaceId}`)).toBeNull();
  });
});
