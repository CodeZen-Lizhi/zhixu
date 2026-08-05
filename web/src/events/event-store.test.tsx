import { QueryClient, QueryClientProvider, QueryObserver } from "@tanstack/react-query";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { StrictMode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { setActiveWorkspaceId } from "../app/active-workspace";
import { ServerEventClientError, type ConnectServerEventsOptions, type ServerEventEnvelope } from "./server-events";

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
      if (options.lastEventId === "invalid") throw new actual.ServerEventClientError("CURSOR_REJECTED", "invalid", false);
      connectionMock.options.push(options);
      return { close: connectionMock.close, done: Promise.resolve(), getLastEventId: () => options.lastEventId };
    }),
  };
});

import { EventStoreProvider, useEventStore, useRegisterWorkspaceRecovery } from "./event-store";

const workspaceA = "7a000000-0000-4000-8000-000000000001";
const workspaceB = "7a000000-0000-4000-8000-000000000002";

const workspacePayload = (id: string): Record<string, unknown> => ({
  id,
  name: id === workspaceA ? "Workspace A" : "Workspace B",
  root_path: `/tmp/${id}`,
  status: "active",
  version: 1,
  git: {
    present: true,
    repository_path: `/tmp/${id}`,
    branch: "main",
    head: "abc",
    dirty: false,
    checked_at: "2026-07-22T00:00:00Z",
  },
  warnings: [],
  created_at: "2026-07-22T00:00:00Z",
  updated_at: "2026-07-22T00:00:00Z",
});

const workspaceResponse = (id: string): Response => new Response(JSON.stringify(workspacePayload(id)), {
  status: 200,
  headers: { "Content-Type": "application/json" },
});

const Probe = () => {
  const store = useEventStore();
  return <>
    <output>{store.state}:{store.lastEventId ?? "none"}</output>
    <output>event:{store.lastEvent?.workspaceId ?? "none"}</output>
    <button type="button" onClick={store.retryRecovery}>重试恢复</button>
  </>;
};

const RecoveryRegistration = ({ recover }: { recover: (workspaceId: string) => Promise<void> | void }) => {
  useRegisterWorkspaceRecovery(recover);
  return null;
};

const renderStore = (
  queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } }),
  strict = false,
  recover?: (workspaceId: string) => Promise<void> | void,
) => {
  const store = <QueryClientProvider client={queryClient}><EventStoreProvider><Probe />{recover === undefined ? null : <RecoveryRegistration recover={recover} />}</EventStoreProvider></QueryClientProvider>;
  render(strict ? <StrictMode>{store}</StrictMode> : store);
  return queryClient;
};

const seedRecoveryQueries = (queryClient: QueryClient, workspaceId: string, order: string[]): void => {
  const queries = [
    { key: ["business", workspaceId, "sources"], label: "business" },
    { key: ["document-history", workspaceId, "documents"], label: "document-history" },
    { key: ["rag", workspaceId, "conversation", "c1"], label: "rag" },
    { key: ["collections", workspaceId, "list"], label: "collections" },
    { key: ["collection-exports", workspaceId, "collection", ""], label: "collection-exports" },
    { key: ["knowledge-health", workspaceId, "summary"], label: "knowledge-health" },
    { key: ["graph", workspaceId, "global"], label: "graph" },
    { key: ["semantic-links", workspaceId, "candidates"], label: "semantic-links" },
  ] as const;
  for (const query of queries) {
    queryClient.setQueryDefaults(query.key, {
      queryFn: () => {
        order.push(query.label);
        return Promise.resolve({ label: query.label });
      },
    });
    queryClient.setQueryData(query.key, { label: "cached" });
  }
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
});

afterEach(() => {
  setActiveWorkspaceId("");
  window.sessionStorage.clear();
  connectionMock.options.length = 0;
  connectionMock.close.mockClear();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("EventStoreProvider", () => {
  it("每个活动 Workspace 只建立一个连接，切换时关闭旧连接并清空旧事件快照", async () => {
    setActiveWorkspaceId(workspaceA);
    const queryClient = renderStore();
    queryClient.setQueryData(["workspace", workspaceA], { id: workspaceA });
    queryClient.setQueryData(["business", workspaceA, "proposal", "p1"], { id: "p1" });
    queryClient.setQueryData(["search", workspaceA, "page", "query"], { items: [] });
    queryClient.setQueryData(["rag", workspaceA, "conversation", "c1"], { id: "c1" });
    queryClient.setQueryData(["collections", workspaceA, "list"], { id: "collection" });
    queryClient.setQueryData(["knowledge-health", workspaceA, "summary"], { id: "health" });
    queryClient.setQueryData(["graph", workspaceA, "global"], { id: "graph" });
    queryClient.setQueryData(["semantic-links", workspaceA, "candidates"], { id: "semantic" });
    expect(connectionMock.options).toHaveLength(1);
    expect(connectionMock.options[0]?.workspaceId).toBe(workspaceA);

    await act(async () => connectionMock.options[0]?.onEvent?.({
      schemaVersion: 1,
      id: "41",
      type: "source.updated",
      occurredAt: "2026-07-22T00:00:00Z",
      workspaceId: workspaceA,
      resourceRef: "source:7a000000-0000-4000-8000-000000000010",
      resourceVersion: 1,
      payloadSummary: {},
      invalidations: [],
    }));
    expect(screen.getByText("open:41")).toBeInTheDocument();

    act(() => setActiveWorkspaceId(workspaceB));
    expect(connectionMock.close).toHaveBeenCalledTimes(1);
    expect(connectionMock.options).toHaveLength(2);
    expect(connectionMock.options[1]?.workspaceId).toBe(workspaceB);
    await waitFor(() => expect(queryClient.getQueryData(["workspace", workspaceA])).toBeUndefined());
    expect(queryClient.getQueryData(["business", workspaceA, "proposal", "p1"])).toBeUndefined();
    expect(queryClient.getQueryData(["search", workspaceA, "page", "query"])).toBeUndefined();
    expect(queryClient.getQueryData(["rag", workspaceA, "conversation", "c1"])).toBeUndefined();
    expect(queryClient.getQueryData(["collections", workspaceA, "list"])).toBeUndefined();
    expect(queryClient.getQueryData(["knowledge-health", workspaceA, "summary"])).toBeUndefined();
    expect(queryClient.getQueryData(["graph", workspaceA, "global"])).toBeUndefined();
    expect(queryClient.getQueryData(["semantic-links", workspaceA, "candidates"])).toBeUndefined();
    expect(screen.getByText("connecting:none")).toBeInTheDocument();
    act(() => connectionMock.options[1]?.onStateChange?.("open"));
    act(() => connectionMock.options[0]?.onStateChange?.("closed"));
    expect(screen.getByText("open:none")).toBeInTheDocument();
  });

  it("Workspace 切换会在新连接启动前清除旧事件快照", async () => {
    setActiveWorkspaceId(workspaceA);
    const queryClient = renderStore();
    vi.spyOn(queryClient, "invalidateQueries").mockResolvedValue(undefined);
    const oldEvent: ServerEventEnvelope = {
      schemaVersion: 1,
      id: "42",
      type: "answer.completed",
      occurredAt: "2026-07-22T00:00:00Z",
      workspaceId: workspaceA,
      resourceRef: "answer:7a000000-0000-4000-8000-000000000010",
      resourceVersion: 1,
      payloadSummary: {},
      invalidations: [{ resource: "answer", id: "7a000000-0000-4000-8000-000000000010" }],
    };

    await act(async () => connectionMock.options[0]?.onEvent?.(oldEvent));
    expect(screen.getByText("open:42")).toBeInTheDocument();
    expect(screen.getByText(`event:${workspaceA}`)).toBeInTheDocument();

    act(() => setActiveWorkspaceId(workspaceB));

    expect(screen.getByText("connecting:none")).toBeInTheDocument();
    expect(screen.getByText("event:none")).toBeInTheDocument();
  });

  it("StrictMode 双挂载会关闭旧连接且旧回调不能覆盖活动状态", () => {
    setActiveWorkspaceId(workspaceA);
    renderStore(undefined, true);

    expect(connectionMock.options).toHaveLength(2);
    expect(connectionMock.close).toHaveBeenCalledTimes(1);
    act(() => connectionMock.options[1]?.onStateChange?.("open"));
    act(() => connectionMock.options[0]?.onStateChange?.("closed"));
    expect(screen.getByText("open:none")).toBeInTheDocument();
  });

  it("Query 失效失败时不提前提交持久游标", async () => {
    setActiveWorkspaceId(workspaceA);
    const queryClient = renderStore();
    vi.spyOn(queryClient, "invalidateQueries").mockRejectedValue(new Error("cache unavailable"));
    const event: ServerEventEnvelope = {
      schemaVersion: 1, id: "42", type: "workflow.updated", occurredAt: "2026-07-22T00:00:00Z",
      workspaceId: workspaceA, resourceRef: "workflow:7a000000-0000-4000-8000-000000000010", resourceVersion: 1,
      payloadSummary: {}, invalidations: [{ resource: "workflow", id: "7a000000-0000-4000-8000-000000000010" }],
    };

    await expect(connectionMock.options[0]?.onEvent?.(event)).rejects.toThrow("cache unavailable");
    expect(window.sessionStorage.getItem(`zhixu.event-cursor.${workspaceA}`)).toBeNull();
    expect(screen.getByText("connecting:none")).toBeInTheDocument();
  });

  it("提交 RAG 事件游标且只失效 RAG Query", async () => {
    setActiveWorkspaceId(workspaceA);
    const queryClient = renderStore();
    const invalidate = vi.spyOn(queryClient, "invalidateQueries").mockResolvedValue(undefined);
    const event: ServerEventEnvelope = {
      schemaVersion: 1, id: "42", type: "answer.completed", occurredAt: "2026-07-22T00:00:00Z",
      workspaceId: workspaceA, resourceRef: "answer:7a000000-0000-4000-8000-000000000010", resourceVersion: 1,
      payloadSummary: {}, invalidations: [{ resource: "answer", id: "7a000000-0000-4000-8000-000000000010" }],
    };

    await act(async () => connectionMock.options[0]?.onEvent?.(event));
    expect(window.sessionStorage.getItem(`zhixu.event-cursor.${workspaceA}`)).toBe("42");
    expect(invalidate).toHaveBeenCalledWith({ queryKey: ["rag", workspaceA, "answer", "7a000000-0000-4000-8000-000000000010"], exact: true }, { throwOnError: true });
    expect(invalidate).not.toHaveBeenCalledWith({ queryKey: ["business", workspaceA, "sources"] });
    expect(screen.getByText("open:42")).toBeInTheDocument();
  });

  it("Model Run 事件失效 RAG Query 而不误刷 Business", async () => {
    setActiveWorkspaceId(workspaceA);
    const queryClient = renderStore();
    const invalidate = vi.spyOn(queryClient, "invalidateQueries").mockResolvedValue(undefined);
    const modelRunId = "7a000000-0000-4000-8000-000000000013";

    await act(async () => connectionMock.options[0]?.onEvent?.({
      schemaVersion: 1, id: "45", type: "model_run.completed", occurredAt: "2026-07-22T00:00:02Z",
      workspaceId: workspaceA, resourceRef: `model_run:${modelRunId}`, resourceVersion: 2,
      payloadSummary: { modelRunId, answerId: "7a000000-0000-4000-8000-000000000014", status: "succeeded" }, invalidations: [{ resource: "model_run", id: modelRunId }],
    }));

    expect(invalidate).toHaveBeenCalledWith({ queryKey: ["rag", workspaceA, "answer", "7a000000-0000-4000-8000-000000000014"], exact: true }, { throwOnError: true });
    expect(invalidate).not.toHaveBeenCalledWith({ queryKey: ["business", workspaceA, "proposals"] });
    expect(window.sessionStorage.getItem(`zhixu.event-cursor.${workspaceA}`)).toBe("45");
  });

  it("Capture 事件只把当前 Workspace 的 Capture Query 标为失效", async () => {
    setActiveWorkspaceId(workspaceA);
    const queryClient = renderStore();
    const invalidate = vi.spyOn(queryClient, "invalidateQueries").mockResolvedValue(undefined);
    const captureId = "7a000000-0000-4000-8000-000000000015";

    await act(async () => connectionMock.options[0]?.onEvent?.({
      schemaVersion: 1, id: "46", type: "capture.updated", occurredAt: "2026-07-22T00:00:03Z",
      workspaceId: workspaceA, resourceRef: `capture:${captureId}`, resourceVersion: 2,
      payloadSummary: {}, invalidations: [{ resource: "capture", id: captureId }],
    }));

    expect(invalidate).toHaveBeenCalledWith({ queryKey: ["captures", workspaceA] }, { throwOnError: true });
    expect(invalidate).not.toHaveBeenCalledWith({ queryKey: ["captures", workspaceB] }, { throwOnError: true });
    expect(invalidate).not.toHaveBeenCalledWith({ queryKey: ["business", workspaceA, "sources"] }, { throwOnError: true });
    expect(window.sessionStorage.getItem(`zhixu.event-cursor.${workspaceA}`)).toBe("46");
  });

  it("Git Sync 事件只失效所属 Workspace 的 Git 设置查询", async () => {
    setActiveWorkspaceId(workspaceA);
    const queryClient = renderStore();
    const invalidate = vi.spyOn(queryClient, "invalidateQueries").mockResolvedValue(undefined);
    await act(async () => connectionMock.options[0]?.onEvent?.({
      schemaVersion: 1, id: "461", type: "git.sync.run.updated", occurredAt: "2026-07-22T00:00:03Z",
      workspaceId: workspaceA, resourceRef: "git_sync_run:7a000000-0000-4000-8000-000000000015", resourceVersion: 2,
      payloadSummary: {}, invalidations: [{ resource: "git_sync_run", id: "7a000000-0000-4000-8000-000000000015" }],
    }));
    expect(invalidate).toHaveBeenCalledWith({ queryKey: ["settings", "git-sync", workspaceA] }, { throwOnError: true });
    expect(invalidate).not.toHaveBeenCalledWith({ queryKey: ["settings", "git-sync", workspaceB] }, { throwOnError: true });
    expect(window.sessionStorage.getItem(`zhixu.event-cursor.${workspaceA}`)).toBe("461");
  });

  it("Workflow 事件定向失效 Business Query", async () => {
    setActiveWorkspaceId(workspaceA);
    const queryClient = renderStore();
    const invalidate = vi.spyOn(queryClient, "invalidateQueries").mockResolvedValue(undefined);
    const workflowId = "7a000000-0000-4000-8000-000000000010";

    await act(async () => connectionMock.options[0]?.onEvent?.({
      schemaVersion: 1, id: "42", type: "workflow.run.progress", occurredAt: "2026-07-22T00:00:00Z",
      workspaceId: workspaceA, resourceRef: `workflow.run:${workflowId}`, resourceVersion: 1,
      payloadSummary: { workflowRunId: workflowId }, invalidations: [{ resource: "workflow", id: workflowId }],
    }));

    expect(invalidate).toHaveBeenCalledWith({ queryKey: ["business", workspaceA, "workflows"] }, { throwOnError: true });
    expect(invalidate).toHaveBeenCalledWith({ queryKey: ["business", workspaceA, "workflow", workflowId] }, { throwOnError: true });
    expect(invalidate).toHaveBeenCalledWith({ queryKey: ["business", workspaceA, "sources"] }, { throwOnError: true });
    expect(invalidate).toHaveBeenCalledWith({ queryKey: ["business", workspaceA, "proposals"] }, { throwOnError: true });
    expect(invalidate).toHaveBeenCalledWith({ queryKey: ["business", workspaceA, "proposal"] }, { throwOnError: true });
    expect(invalidate).toHaveBeenCalledWith({ queryKey: ["business", workspaceA, "proposal-current-content"] }, { throwOnError: true });
    expect(invalidate).toHaveBeenCalledWith({ queryKey: ["captures", workspaceA] }, { throwOnError: true });
    expect(invalidate).toHaveBeenCalledWith({ queryKey: ["organizing", workspaceA] }, { throwOnError: true });
    expect(invalidate).not.toHaveBeenCalledWith({ queryKey: ["rag", workspaceA] });
  });

  it("Index 激活事件把 Search 标 stale 但不使用旧 cursor refetch 或阻塞 SSE 游标", async () => {
    setActiveWorkspaceId(workspaceA);
    const queryClient = renderStore();
    const searchKey = ["search", workspaceA, "page", "query", "semantic", "", "page-2"] as const;
    const searchRequest = vi.fn().mockRejectedValue(new Error("RETRIEVAL_SEARCH_CURSOR_STALE"));
    queryClient.setQueryData(searchKey, { items: [{ id: "cached" }] });
    const observer = new QueryObserver(queryClient, { queryKey: searchKey, queryFn: searchRequest, staleTime: Infinity });
    const unsubscribe = observer.subscribe(() => undefined);
    const invalidate = vi.spyOn(queryClient, "invalidateQueries");

    await act(async () => connectionMock.options[0]?.onEvent?.({
      schemaVersion: 1,
      id: "47",
      type: "index.activated",
      occurredAt: "2026-07-22T00:00:04Z",
      workspaceId: workspaceA,
      resourceRef: "index_version:7a000000-0000-4000-8000-000000000016",
      resourceVersion: 1,
      payloadSummary: {},
      invalidations: [],
    }));

    expect(invalidate).toHaveBeenCalledWith({ queryKey: ["business", workspaceA, "sources"] }, { throwOnError: true });
    expect(invalidate).toHaveBeenCalledWith({ queryKey: ["search", workspaceA], refetchType: "none" }, { throwOnError: true });
    expect(searchRequest).not.toHaveBeenCalled();
    expect(observer.getCurrentResult().isStale).toBe(true);
    expect(window.sessionStorage.getItem(`zhixu.event-cursor.${workspaceA}`)).toBe("47");
    unsubscribe();
  });

  it("Proposal 事件同时失效详情与 Revision 绑定的 current-content", async () => {
    setActiveWorkspaceId(workspaceA);
    const queryClient = renderStore();
    const invalidate = vi.spyOn(queryClient, "invalidateQueries").mockResolvedValue(undefined);
    const proposalId = "7a000000-0000-4000-8000-000000000015";

    await act(async () => connectionMock.options[0]?.onEvent?.({
      schemaVersion: 1,
      id: "46",
      type: "proposal.updated",
      occurredAt: "2026-07-22T00:00:03Z",
      workspaceId: workspaceA,
      resourceRef: `proposal:${proposalId}`,
      resourceVersion: 2,
      payloadSummary: {},
      invalidations: [],
    }));

    expect(invalidate).toHaveBeenCalledWith({ queryKey: ["business", workspaceA, "proposals"] }, { throwOnError: true });
    expect(invalidate).toHaveBeenCalledWith({ queryKey: ["business", workspaceA, "proposal", proposalId] }, { throwOnError: true });
    expect(invalidate).toHaveBeenCalledWith({ queryKey: ["business", workspaceA, "proposal-current-content", proposalId] }, { throwOnError: true });
    expect(window.sessionStorage.getItem(`zhixu.event-cursor.${workspaceA}`)).toBe("46");
  });

  it("Knowledge Proposal 应用后刷新正式 Relation 的所有读模型", async () => {
    setActiveWorkspaceId(workspaceA);
    const queryClient = renderStore();
    const invalidate = vi.spyOn(queryClient, "invalidateQueries").mockResolvedValue(undefined);
    const proposalId = "7a000000-0000-4000-8000-000000000015";

    await act(async () => connectionMock.options[0]?.onEvent?.({
      schemaVersion: 1,
      id: "47",
      type: "proposal.applied",
      occurredAt: "2026-07-22T00:00:04Z",
      workspaceId: workspaceA,
      resourceRef: `proposal:${proposalId}`,
      resourceVersion: 3,
      payloadSummary: { status: "applied" },
      invalidations: [],
    }));

    expect(invalidate).toHaveBeenCalledWith({ queryKey: ["graph", workspaceA] }, { throwOnError: true });
    expect(invalidate).toHaveBeenCalledWith({ queryKey: ["semantic-links", workspaceA] }, { throwOnError: true });
    expect(invalidate).toHaveBeenCalledWith({ queryKey: ["collections", workspaceA] }, { throwOnError: true });
    expect(invalidate).toHaveBeenCalledWith({ queryKey: ["knowledge-health", workspaceA] }, { throwOnError: true });
    expect(invalidate).toHaveBeenCalledWith({ queryKey: ["authoring", workspaceA] }, { throwOnError: true });
    expect(invalidate).toHaveBeenCalledWith({ queryKey: ["document-history", workspaceA] });
    expect(window.sessionStorage.getItem(`zhixu.event-cursor.${workspaceA}`)).toBe("47");
  });

  it("Document History 旧 cursor 变 stale 时仍提交实时事件游标", async () => {
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const staleHistoryKey = [
      "document-history", workspaceA, "documents", "7a000000-0000-4000-8000-000000000020",
      "history", "a".repeat(40), "stale-cursor", 30,
    ] as const;
    const staleRequest = vi.fn().mockRejectedValue(new Error("DOCUMENT_HISTORY_CURSOR_STALE"));
    queryClient.setQueryData(staleHistoryKey, { items: [{ kind: "EXTERNAL" }] });
    const observer = new QueryObserver(queryClient, { queryKey: staleHistoryKey, queryFn: staleRequest, staleTime: Infinity });
    const unsubscribe = observer.subscribe(() => undefined);
    setActiveWorkspaceId(workspaceA);
    renderStore(queryClient);

    await expect(act(async () => connectionMock.options[0]?.onEvent?.({
      schemaVersion: 1,
      id: "4710",
      type: "proposal.applied",
      occurredAt: "2026-07-22T00:00:05Z",
      workspaceId: workspaceA,
      resourceRef: "proposal:7a000000-0000-4000-8000-000000000015",
      resourceVersion: 4,
      payloadSummary: { status: "applied" },
      invalidations: [],
    }))).resolves.toBeUndefined();

    expect(staleRequest).toHaveBeenCalledOnce();
    expect(window.sessionStorage.getItem(`zhixu.event-cursor.${workspaceA}`)).toBe("4710");
    unsubscribe();
  });

  it("Authoring 事件只失效当前 Workspace 的创作查询族", async () => {
    setActiveWorkspaceId(workspaceA);
    const queryClient = renderStore();
    const invalidate = vi.spyOn(queryClient, "invalidateQueries").mockResolvedValue(undefined);

    await act(async () => connectionMock.options[0]?.onEvent?.({
      schemaVersion: 1,
      id: "470",
      type: "authoring.working_draft.updated",
      occurredAt: "2026-07-22T00:00:04Z",
      workspaceId: workspaceA,
      resourceRef: "working_draft:7a000000-0000-4000-8000-000000000017",
      resourceVersion: 2,
      payloadSummary: { status: "editing" },
      invalidations: [{ resource: "working_draft", id: "7a000000-0000-4000-8000-000000000017" }],
    }));

    expect(invalidate).toHaveBeenCalledWith({ queryKey: ["authoring", workspaceA] }, { throwOnError: true });
    expect(invalidate).not.toHaveBeenCalledWith({ queryKey: ["authoring", workspaceB] }, { throwOnError: true });
    expect(window.sessionStorage.getItem(`zhixu.event-cursor.${workspaceA}`)).toBe("470");
  });

  it("Organizing 事件只失效当前 Workspace 的整理查询族", async () => {
    setActiveWorkspaceId(workspaceA);
    const queryClient = renderStore();
    const invalidate = vi.spyOn(queryClient, "invalidateQueries").mockResolvedValue(undefined);

    await act(async () => connectionMock.options[0]?.onEvent?.({
      schemaVersion: 1,
      id: "471",
      type: "organizing.snapshot.confirmed",
      occurredAt: "2026-07-22T00:00:05Z",
      workspaceId: workspaceA,
      resourceRef: "organizing_snapshot:7a000000-0000-4000-8000-000000000018",
      resourceVersion: 1,
      payloadSummary: {},
      invalidations: [{ resource: "organizing_snapshot", id: "7a000000-0000-4000-8000-000000000018" }],
    }));

    expect(invalidate).toHaveBeenCalledWith({ queryKey: ["organizing", workspaceA] }, { throwOnError: true });
    expect(invalidate).not.toHaveBeenCalledWith({ queryKey: ["organizing", workspaceB] }, { throwOnError: true });
    expect(window.sessionStorage.getItem(`zhixu.event-cursor.${workspaceA}`)).toBe("471");
  });

  it("Collection 事件刷新自身，Health 事件同时刷新 Issue 与 Collection result 摘要", async () => {
    setActiveWorkspaceId(workspaceA);
    const queryClient = renderStore();
    const invalidate = vi.spyOn(queryClient, "invalidateQueries").mockResolvedValue(undefined);

    await act(async () => connectionMock.options[0]?.onEvent?.({
      schemaVersion: 1, id: "43", type: "health.scan.completed", occurredAt: "2026-07-22T00:00:00Z",
      workspaceId: workspaceA, resourceRef: "health_scan:7a000000-0000-4000-8000-000000000011", resourceVersion: 1,
      payloadSummary: {}, invalidations: [{ resource: "health_scan", id: "7a000000-0000-4000-8000-000000000011" }],
    }));
    await act(async () => connectionMock.options[0]?.onEvent?.({
      schemaVersion: 1, id: "44", type: "collection.updated", occurredAt: "2026-07-22T00:00:01Z",
      workspaceId: workspaceA, resourceRef: "collection:7a000000-0000-4000-8000-000000000012", resourceVersion: 2,
      payloadSummary: {}, invalidations: [{ resource: "collection", id: "7a000000-0000-4000-8000-000000000012" }],
    }));

    expect(invalidate).toHaveBeenCalledWith({ queryKey: ["knowledge-health", workspaceA] }, { throwOnError: true });
    expect(invalidate).toHaveBeenCalledWith({ queryKey: ["collections", workspaceA, "results"] }, { throwOnError: true });
    expect(invalidate).toHaveBeenCalledWith({ queryKey: ["collections", workspaceA] }, { throwOnError: true });
    expect(invalidate).not.toHaveBeenCalledWith({ queryKey: ["business", workspaceA] });
    expect(invalidate.mock.calls.filter(([input]) => input?.queryKey?.[0] === "knowledge-health")).toHaveLength(1);
    expect(invalidate.mock.calls.filter(([input]) => input?.queryKey?.[0] === "collections")).toHaveLength(2);
    expect(window.sessionStorage.getItem(`zhixu.event-cursor.${workspaceA}`)).toBe("44");
  });

  it("Export 事件按 scope_kind 只失效对应 Export 查询，历史事件兼容性失效两类", async () => {
    setActiveWorkspaceId(workspaceA);
    const queryClient = renderStore();
    const invalidate = vi.spyOn(queryClient, "invalidateQueries").mockResolvedValue(undefined);
    await act(async () => connectionMock.options[0]?.onEvent?.({
      schemaVersion: 1, id: "48", type: "export.completed", occurredAt: "2026-07-22T00:00:04Z",
      workspaceId: workspaceA, resourceRef: "export_job:7a000000-0000-4000-8000-000000000012", resourceVersion: 2,
      payloadSummary: { status: "succeeded", scopeKind: "collection" }, invalidations: [{ resource: "export_job", id: "7a000000-0000-4000-8000-000000000012" }],
    }));
    expect(invalidate).toHaveBeenCalledWith({ queryKey: ["collection-exports", workspaceA] }, { throwOnError: true });
    expect(invalidate).not.toHaveBeenCalledWith({ queryKey: ["workspace-attachment-exports", workspaceA] }, { throwOnError: true });
    expect(invalidate).not.toHaveBeenCalledWith({ queryKey: ["collections", workspaceA] }, { throwOnError: true });

    invalidate.mockClear();
    await act(async () => connectionMock.options[0]?.onEvent?.({
      schemaVersion: 1, id: "49", type: "export.completed", occurredAt: "2026-07-22T00:00:05Z",
      workspaceId: workspaceA, resourceRef: "export_job:7a000000-0000-4000-8000-000000000013", resourceVersion: 2,
      payloadSummary: { status: "succeeded", scopeKind: "workspace_attachments" }, invalidations: [{ resource: "export_job", id: "7a000000-0000-4000-8000-000000000013" }],
    }));
    expect(invalidate).toHaveBeenCalledWith({ queryKey: ["workspace-attachment-exports", workspaceA] }, { throwOnError: true });
    expect(invalidate).not.toHaveBeenCalledWith({ queryKey: ["collection-exports", workspaceA] }, { throwOnError: true });

    invalidate.mockClear();
    await act(async () => connectionMock.options[0]?.onEvent?.({
      schemaVersion: 1, id: "50", type: "export.completed", occurredAt: "2026-07-22T00:00:06Z",
      workspaceId: workspaceA, resourceRef: "export_job:7a000000-0000-4000-8000-000000000014", resourceVersion: 2,
      payloadSummary: { status: "succeeded" }, invalidations: [{ resource: "export_job", id: "7a000000-0000-4000-8000-000000000014" }],
    }));
    expect(invalidate).toHaveBeenCalledWith({ queryKey: ["collection-exports", workspaceA] }, { throwOnError: true });
    expect(invalidate).toHaveBeenCalledWith({ queryKey: ["workspace-attachment-exports", workspaceA] }, { throwOnError: true });
  });

  it("Review 事件只失效当前 Workspace 的 Review 查询", async () => {
    setActiveWorkspaceId(workspaceA);
    const queryClient = renderStore();
    const invalidate = vi.spyOn(queryClient, "invalidateQueries").mockResolvedValue(undefined);

    await act(async () => connectionMock.options[0]?.onEvent?.({
      schemaVersion: 1, id: "49", type: "review.answer.submitted", occurredAt: "2026-07-22T00:00:05Z",
      workspaceId: workspaceA, resourceRef: "review_answer:7a000000-0000-4000-8000-000000000012", resourceVersion: 2,
      payloadSummary: {}, invalidations: [],
    }));

    expect(invalidate).toHaveBeenCalledWith({ queryKey: ["review", workspaceA] }, { throwOnError: true });
    expect(invalidate).not.toHaveBeenCalledWith({ queryKey: ["review", workspaceB] }, { throwOnError: true });
    expect(window.sessionStorage.getItem(`zhixu.event-cursor.${workspaceA}`)).toBe("49");
  });

  it("Memory 事件只失效当前 Workspace 的 Memory 查询", async () => {
    setActiveWorkspaceId(workspaceA);
    const queryClient = renderStore();
    const invalidate = vi.spyOn(queryClient, "invalidateQueries").mockResolvedValue(undefined);

    await act(async () => connectionMock.options[0]?.onEvent?.({
      schemaVersion: 1, id: "50", type: "memory.confirmed", occurredAt: "2026-07-22T00:00:06Z",
      workspaceId: workspaceA, resourceRef: "memory:7a000000-0000-4000-8000-000000000012", resourceVersion: 2,
      payloadSummary: {}, invalidations: [],
    }));

    expect(invalidate).toHaveBeenCalledWith({ queryKey: ["memory", workspaceA] }, { throwOnError: true });
    expect(invalidate).not.toHaveBeenCalledWith({ queryKey: ["memory", workspaceB] }, { throwOnError: true });
    expect(window.sessionStorage.getItem(`zhixu.event-cursor.${workspaceA}`)).toBe("50");
  });

  it("权威回查失败时保留游标并展示 recovery_failed", async () => {
    window.sessionStorage.setItem(`zhixu.event-cursor.${workspaceA}`, "41");
    vi.stubGlobal("fetch", vi.fn().mockRejectedValue(new Error("database unavailable")));
    setActiveWorkspaceId(workspaceA);
    renderStore();

    await expect(connectionMock.options[0]?.onRecoveryRequired({ reason: "cursor_expired", workspaceId: workspaceA })).rejects.toMatchObject({ code: "NETWORK_ERROR" });
    act(() => connectionMock.options[0]?.onError?.(new ServerEventClientError("RECOVERY_FAILED", "failed", true)));
    act(() => connectionMock.options[0]?.onStateChange?.("reconnecting"));
    expect(window.sessionStorage.getItem(`zhixu.event-cursor.${workspaceA}`)).toBe("41");
    expect(screen.getByText("recovery_failed:none")).toBeInTheDocument();
    act(() => connectionMock.options[0]?.onStateChange?.("closed"));
    expect(screen.getByText("recovery_failed:none")).toBeInTheDocument();
  });

  it("Workspace 权威回查失败前也会先移除 Search 旧 cursor 窗口", async () => {
    window.sessionStorage.setItem(`zhixu.event-cursor.${workspaceA}`, "41");
    vi.stubGlobal("fetch", vi.fn().mockRejectedValue(new Error("database unavailable")));
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const searchKey = ["search", workspaceA, "page", "query", "hybrid", "", "stale-cursor"] as const;
    queryClient.setQueryData(searchKey, { items: [{ id: "stale" }] });
    setActiveWorkspaceId(workspaceA);
    renderStore(queryClient);

    await expect(connectionMock.options[0]?.onRecoveryRequired({
      reason: "cursor_expired",
      workspaceId: workspaceA,
    })).rejects.toMatchObject({ code: "NETWORK_ERROR" });

    expect(queryClient.getQueryData(searchKey)).toBeUndefined();
    expect(window.sessionStorage.getItem(`zhixu.event-cursor.${workspaceA}`)).toBe("41");
  });

  it("SSE recovery 移除旧 Document History cursor 窗口且不重放 stale 请求", async () => {
    window.sessionStorage.setItem(`zhixu.event-cursor.${workspaceA}`, "41");
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(workspaceResponse(workspaceA)));
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const staleHistoryKey = [
      "document-history", workspaceA, "documents", "7a000000-0000-4000-8000-000000000020",
      "history", "a".repeat(40), "stale-cursor", 30,
    ] as const;
    const staleRequest = vi.fn().mockRejectedValue(new Error("DOCUMENT_HISTORY_CURSOR_STALE"));
    queryClient.setQueryData(staleHistoryKey, { items: [{ kind: "EXTERNAL" }] });
    const observer = new QueryObserver(queryClient, { queryKey: staleHistoryKey, queryFn: staleRequest, staleTime: Infinity });
    const unsubscribe = observer.subscribe(() => undefined);
    setActiveWorkspaceId(workspaceA);
    renderStore(queryClient);

    await expect(connectionMock.options[0]?.onRecoveryRequired({
      reason: "cursor_expired",
      workspaceId: workspaceA,
    })).resolves.toBeUndefined();

    expect(staleRequest).not.toHaveBeenCalled();
    expect(queryClient.getQueryData(staleHistoryKey)).toBeUndefined();
    expect(window.sessionStorage.getItem(`zhixu.event-cursor.${workspaceA}`)).toBeNull();
    unsubscribe();
  });

  it("Search 恢复丢弃旧 cursor 窗口并执行当前页面的首屏回调", async () => {
    window.sessionStorage.setItem(`zhixu.event-cursor.${workspaceA}`, "41");
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(workspaceResponse(workspaceA)));
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const searchKey = ["search", workspaceA, "page", "query", "hybrid", "", "stale-cursor"] as const;
    const staleRequest = vi.fn().mockRejectedValue(new Error("RETRIEVAL_SEARCH_CURSOR_STALE"));
    queryClient.setQueryDefaults(searchKey, { queryFn: staleRequest });
    queryClient.setQueryData(searchKey, { items: [{ id: "stale" }] });
    const recoverSearch = vi.fn().mockResolvedValue(undefined);
    setActiveWorkspaceId(workspaceA);
    renderStore(queryClient, false, recoverSearch);

    await expect(connectionMock.options[0]?.onRecoveryRequired({
      reason: "cursor_expired",
      workspaceId: workspaceA,
    })).resolves.toBeUndefined();

    expect(staleRequest).not.toHaveBeenCalled();
    expect(queryClient.getQueryData(searchKey)).toBeUndefined();
    expect(recoverSearch).toHaveBeenCalledOnce();
    expect(recoverSearch).toHaveBeenCalledWith(workspaceA);
    expect(window.sessionStorage.getItem(`zhixu.event-cursor.${workspaceA}`)).toBeNull();
  });

  it("Search 首屏恢复回调失败时保留 SSE 游标且不复活旧窗口", async () => {
    window.sessionStorage.setItem(`zhixu.event-cursor.${workspaceA}`, "41");
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(workspaceResponse(workspaceA)));
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const searchKey = ["search", workspaceA, "page", "query", "hybrid", "", "stale-cursor"] as const;
    const staleRequest = vi.fn().mockRejectedValue(new Error("RETRIEVAL_SEARCH_CURSOR_STALE"));
    queryClient.setQueryDefaults(searchKey, { queryFn: staleRequest });
    queryClient.setQueryData(searchKey, { items: [{ id: "stale" }] });
    const recoverSearch = vi.fn().mockRejectedValue(new Error("search unavailable"));
    setActiveWorkspaceId(workspaceA);
    renderStore(queryClient, false, recoverSearch);

    await expect(connectionMock.options[0]?.onRecoveryRequired({
      reason: "cursor_expired",
      workspaceId: workspaceA,
    })).rejects.toThrow("search unavailable");

    expect(staleRequest).not.toHaveBeenCalled();
    expect(queryClient.getQueryData(searchKey)).toBeUndefined();
    expect(window.sessionStorage.getItem(`zhixu.event-cursor.${workspaceA}`)).toBe("41");
  });

  it("错误 Workspace 响应不会写入请求缓存或清除游标", async () => {
    window.sessionStorage.setItem(`zhixu.event-cursor.${workspaceA}`, "41");
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(workspaceResponse(workspaceB)));
    setActiveWorkspaceId(workspaceA);
    const queryClient = renderStore();

    await expect(connectionMock.options[0]?.onRecoveryRequired({ reason: "cursor_expired", workspaceId: workspaceA })).rejects.toMatchObject({ code: "INVALID_RESPONSE" });
    act(() => connectionMock.options[0]?.onError?.(new ServerEventClientError("RECOVERY_FAILED", "failed", true)));

    expect(window.sessionStorage.getItem(`zhixu.event-cursor.${workspaceA}`)).toBe("41");
    expect(queryClient.getQueryData(["workspace", workspaceA])).toBeUndefined();
    expect(queryClient.getQueryData(["workspace", workspaceB])).toBeUndefined();
    expect(screen.getByText("recovery_failed:none")).toBeInTheDocument();
  });

  it("Workspace 切换后触发旧 recovery 不会重建旧缓存", async () => {
    const fetcher = vi.fn().mockResolvedValue(workspaceResponse(workspaceA));
    vi.stubGlobal("fetch", fetcher);
    setActiveWorkspaceId(workspaceA);
    const queryClient = renderStore();
    queryClient.setQueryData(["workspace", workspaceA], { id: workspaceA });
    const oldConnection = connectionMock.options[0];

    act(() => setActiveWorkspaceId(workspaceB));
    await expect(oldConnection?.onRecoveryRequired({ reason: "cursor_expired", workspaceId: workspaceA })).rejects.toMatchObject({ name: "AbortError" });

    expect(fetcher).not.toHaveBeenCalled();
    expect(queryClient.getQueryData(["workspace", workspaceA])).toBeUndefined();
    expect(connectionMock.options[1]?.workspaceId).toBe(workspaceB);
  });

  it("pending recovery 中切换 Workspace 会再次清理迟到的旧缓存", async () => {
    window.sessionStorage.setItem(`zhixu.event-cursor.${workspaceA}`, "41");
    let resolveFetch: ((response: Response) => void) | undefined;
    const fetchPending = new Promise<Response>((resolve) => { resolveFetch = resolve; });
    const fetcher = vi.fn(() => fetchPending);
    vi.stubGlobal("fetch", fetcher);
    setActiveWorkspaceId(workspaceA);
    const queryClient = renderStore();
    const refetch = vi.spyOn(queryClient, "refetchQueries");

    const recovery = Promise.resolve(connectionMock.options[0]?.onRecoveryRequired({ reason: "cursor_expired", workspaceId: workspaceA }));
    await vi.waitFor(() => expect(fetcher).toHaveBeenCalledOnce());
    act(() => setActiveWorkspaceId(workspaceB));
    resolveFetch?.(workspaceResponse(workspaceA));

    const [result] = await Promise.allSettled([recovery]);
    expect(result.status).toBe("rejected");
    expect(refetch).not.toHaveBeenCalled();
    expect(queryClient.getQueryData(["workspace", workspaceA])).toBeUndefined();
    expect(window.sessionStorage.getItem(`zhixu.event-cursor.${workspaceA}`)).toBe("41");
  });

  it("本地游标损坏时完成回查并以无游标连接", async () => {
    window.sessionStorage.setItem(`zhixu.event-cursor.${workspaceA}`, "invalid");
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const order: string[] = [];
    seedRecoveryQueries(queryClient, workspaceA, order);
    const fetcher = vi
      .fn<(input: RequestInfo | URL, init?: RequestInit) => Promise<Response>>()
      .mockImplementation(() => {
        order.push("workspace");
        return Promise.resolve(workspaceResponse(workspaceA));
      });
    vi.stubGlobal("fetch", fetcher);
    setActiveWorkspaceId(workspaceA);
    renderStore(queryClient);

    await vi.waitFor(() => expect(connectionMock.options).toHaveLength(1));
    expect(connectionMock.options[0]?.lastEventId).toBeUndefined();
    const request = fetcher.mock.calls[0];
    const requestUrl = request?.[0];
    expect(typeof requestUrl).toBe("string");
    if (typeof requestUrl !== "string") throw new Error("Workspace recovery request URL is not a string");
    expect(requestUrl).toContain(`/api/v1/workspaces/${workspaceA}`);
    expect(request?.[1]?.signal).toBeInstanceOf(AbortSignal);
    expect(order).toEqual(["workspace", "business", "rag", "collections", "collection-exports", "knowledge-health", "graph", "semantic-links"]);
    expect(queryClient.getQueryData(["document-history", workspaceA, "documents"])).toBeUndefined();
    expect(window.sessionStorage.getItem(`zhixu.event-cursor.${workspaceA}`)).toBeNull();
  });

  it("自动 recovery 与快速手动重试共享一次权威回查", async () => {
    window.sessionStorage.setItem(`zhixu.event-cursor.${workspaceA}`, "41");
    let resolveFetch: ((response: Response) => void) | undefined;
    const fetchPending = new Promise<Response>((resolve) => { resolveFetch = resolve; });
    const order: string[] = [];
    const fetcher = vi.fn(() => {
      order.push("workspace");
      return fetchPending;
    });
    vi.stubGlobal("fetch", fetcher);
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    seedRecoveryQueries(queryClient, workspaceA, order);
    setActiveWorkspaceId(workspaceA);
    renderStore(queryClient);
    const oldConnection = connectionMock.options[0];

    const automaticRecovery = Promise.resolve(oldConnection?.onRecoveryRequired({ reason: "cursor_expired", workspaceId: workspaceA }));
    await vi.waitFor(() => expect(fetcher).toHaveBeenCalledOnce());
    act(() => oldConnection?.onError?.(new ServerEventClientError("RECOVERY_FAILED", "failed", true)));
    fireEvent.click(screen.getByRole("button", { name: "重试恢复" }));
    fireEvent.click(screen.getByRole("button", { name: "重试恢复" }));
    resolveFetch?.(workspaceResponse(workspaceA));
    await automaticRecovery;

    await vi.waitFor(() => expect(connectionMock.options).toHaveLength(2));
    expect(fetcher).toHaveBeenCalledTimes(1);
    expect(connectionMock.close).toHaveBeenCalledTimes(1);
    expect(order).toEqual(["workspace", "business", "rag", "collections", "collection-exports", "knowledge-health", "graph", "semantic-links"]);
    expect(window.sessionStorage.getItem(`zhixu.event-cursor.${workspaceA}`)).toBeNull();
    act(() => connectionMock.options[1]?.onStateChange?.("open"));
    act(() => oldConnection?.onStateChange?.("closed"));
    act(() => oldConnection?.onError?.(new ServerEventClientError("NETWORK_ERROR", "late", true)));
    expect(screen.getByText("open:none")).toBeInTheDocument();
  });

  it("shared recovery 失败后旧连接的迟到回调不会隐藏重试状态", async () => {
    window.sessionStorage.setItem(`zhixu.event-cursor.${workspaceA}`, "41");
    let rejectFetch: ((error: Error) => void) | undefined;
    const fetchPending = new Promise<Response>((_resolve, reject) => { rejectFetch = reject; });
    const fetcher = vi.fn(() => fetchPending);
    vi.stubGlobal("fetch", fetcher);
    setActiveWorkspaceId(workspaceA);
    renderStore();
    const oldConnection = connectionMock.options[0];

    const automaticRecovery = Promise.resolve(oldConnection?.onRecoveryRequired({ reason: "cursor_expired", workspaceId: workspaceA }));
    await vi.waitFor(() => expect(fetcher).toHaveBeenCalledOnce());
    act(() => oldConnection?.onError?.(new ServerEventClientError("RECOVERY_FAILED", "failed", true)));
    fireEvent.click(screen.getByRole("button", { name: "重试恢复" }));
    rejectFetch?.(new Error("database unavailable"));

    await expect(automaticRecovery).rejects.toMatchObject({ code: "NETWORK_ERROR" });
    expect(await screen.findByText("recovery_failed:none")).toBeInTheDocument();
    act(() => oldConnection?.onStateChange?.("closed"));
    act(() => oldConnection?.onError?.(new ServerEventClientError("NETWORK_ERROR", "late", true)));

    expect(screen.getByText("recovery_failed:none")).toBeInTheDocument();
    expect(connectionMock.options).toHaveLength(1);
    expect(window.sessionStorage.getItem(`zhixu.event-cursor.${workspaceA}`)).toBe("41");
  });

  it("本地坏游标首次回查失败后允许用户重试恢复", async () => {
    window.sessionStorage.setItem(`zhixu.event-cursor.${workspaceA}`, "invalid");
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const order: string[] = [];
    seedRecoveryQueries(queryClient, workspaceA, order);
    const fetcher = vi.fn()
      .mockRejectedValueOnce(new Error("database unavailable"))
      .mockResolvedValueOnce(workspaceResponse(workspaceA));
    vi.stubGlobal("fetch", fetcher);
    setActiveWorkspaceId(workspaceA);
    renderStore(queryClient);

    expect(await screen.findByText("recovery_failed:none")).toBeInTheDocument();
    expect(connectionMock.options).toHaveLength(0);
    expect(window.sessionStorage.getItem(`zhixu.event-cursor.${workspaceA}`)).toBe("invalid");

    fireEvent.click(screen.getByRole("button", { name: "重试恢复" }));

    await vi.waitFor(() => expect(connectionMock.options).toHaveLength(1));
    expect(connectionMock.options[0]?.lastEventId).toBeUndefined();
    expect(fetcher).toHaveBeenCalledTimes(2);
    expect(order).toEqual(["business", "rag", "collections", "collection-exports", "knowledge-health", "graph", "semantic-links"]);
    expect(window.sessionStorage.getItem(`zhixu.event-cursor.${workspaceA}`)).toBeNull();
  });

  it("Interview 和 Learning Path 事件只失效当前 Workspace 的 Interview 查询", async () => {
    setActiveWorkspaceId(workspaceA);
    const queryClient = renderStore();
    const invalidate = vi.spyOn(queryClient, "invalidateQueries").mockResolvedValue(undefined);

    await act(async () => connectionMock.options[0]?.onEvent?.({
      schemaVersion: 1, id: "83", type: "learning_path.step.updated", occurredAt: "2026-07-27T00:00:00Z",
      workspaceId: workspaceA, resourceRef: "learning_path_step:7a000000-0000-4000-8000-000000000010", resourceVersion: 2,
      payloadSummary: {}, invalidations: [],
    }));

    expect(invalidate).toHaveBeenCalledWith({ queryKey: ["interview", workspaceA] }, { throwOnError: true });
    expect(invalidate).not.toHaveBeenCalledWith({ queryKey: ["review", workspaceA] }, { throwOnError: true });
    expect(invalidate).not.toHaveBeenCalledWith({ queryKey: ["interview", workspaceB] }, { throwOnError: true });
  });
});
