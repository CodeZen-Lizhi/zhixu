import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { Link, MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";

import type { Collection, CollectionDefinitionInput, CollectionItem, CollectionResultPage } from "../../api/collections";
import type { StartHealthScanInput } from "../../api/health";
import { parseCollectionUrlState } from "./url-state";

const workspaceId = "15000000-0000-4000-8000-000000000001";
const otherWorkspaceId = "15000000-0000-4000-8000-000000000009";
const collectionId = "15000000-0000-4000-8000-000000000002";
const otherCollectionId = "15000000-0000-4000-8000-000000000099";
const at = "2026-07-22T00:00:00Z";

const hooks = vi.hoisted(() => ({
  activeWorkspaceId: "15000000-0000-4000-8000-000000000001",
  useCollection: vi.fn(),
  useCollectionPreview: vi.fn(),
  useCollectionResults: vi.fn(),
  useCollections: vi.fn(),
  useCreateCollection: vi.fn(),
  useStartSemanticLinkScan: vi.fn(),
  useStartHealthScan: vi.fn(),
}));

vi.mock("../../app/active-workspace", async (importOriginal) => ({
  // eslint-disable-next-line @typescript-eslint/consistent-type-imports
  ...(await importOriginal<typeof import("../../app/active-workspace")>()),
  useActiveWorkspaceId: () => hooks.activeWorkspaceId,
}));
vi.mock("./queries", async (importOriginal) => ({
  // eslint-disable-next-line @typescript-eslint/consistent-type-imports
  ...(await importOriginal<typeof import("./queries")>()),
  useCollection: hooks.useCollection,
  useCollectionPreview: hooks.useCollectionPreview,
  useCollectionResults: hooks.useCollectionResults,
  useCollections: hooks.useCollections,
  useCreateCollection: hooks.useCreateCollection,
}));
vi.mock("../graph/semantic-link-queries", () => ({ createSemanticLinkIdempotencyKey: () => "candidate-scan:test", useStartSemanticLinkScan: hooks.useStartSemanticLinkScan }));
vi.mock("../health/queries", () => ({ useStartHealthScan: hooks.useStartHealthScan }));

import { buildCollectionQuery, CollectionDetailPage, CollectionsPage } from "./CollectionsPage";

const item = (index: number, objectType: "TOPIC" | "CLAIM"): CollectionItem => objectType === "TOPIC" ? ({
  objectType,
  id: `15000000-0000-4000-8000-00000000000${String(index)}`,
  topicId: null,
  title: `${objectType} ${String(index)}`,
  summary: `Summary ${String(index)}`,
  status: "ACTIVE",
  confidence: null,
  applicability: null,
  applicabilitySchemaVersion: null,
  applicabilityHash: null,
  aliases: [],
  sourceSummaries: [],
  relationTypes: [],
  healthSummary: null,
  relationType: null,
  healthIssueType: null,
  sourceType: null,
  filePath: null,
  createdAt: at,
  updatedAt: at,
}) : ({
  objectType,
  id: `15000000-0000-4000-8000-00000000000${String(index)}`,
  topicId: null,
  title: `${objectType} ${String(index)}`,
  summary: `Summary ${String(index)}`,
  status: "ACTIVE",
  confidence: 0.82,
  applicability: null,
  applicabilitySchemaVersion: null,
  applicabilityHash: null,
  aliases: [],
  sourceSummaries: [],
  relationTypes: [],
  healthSummary: null,
  relationType: null,
  healthIssueType: null,
  sourceType: null,
  filePath: null,
  createdAt: at,
  updatedAt: at,
});
const collection: Collection = {
  id: collectionId,
  workspaceId,
  name: "Active knowledge",
  description: "",
  querySchemaVersion: "collection-query/v1",
  queryVersion: 1,
  query: { schema_version: "collection-query/v1", root: { kind: "group", operator: "AND", clauses: [{ kind: "predicate", field: "object_type", operator: "EQ", value: "TOPIC" }] } },
  queryHash: "a".repeat(64),
  viewType: "LIST",
  viewConfig: { columns: ["object_type", "title"], fixedColumns: ["title"], sort: [], groupBy: null, density: "COMFORTABLE" },
  status: "ACTIVE",
  cachedResultVersion: null,
  lastExecutedAt: null,
  version: 2,
  createdAt: at,
  updatedAt: at,
};
const page: CollectionResultPage = { workspaceId, collectionId, queryHash: "a".repeat(64), items: [item(3, "TOPIC"), item(4, "CLAIM")], exactCount: 2, nextCursor: null, revisionHash: "b".repeat(64), scanRevisionHash: "c".repeat(64) };

afterEach(() => {
  hooks.activeWorkspaceId = workspaceId;
  hooks.useCollection.mockReset();
  hooks.useCollectionPreview.mockReset();
  hooks.useCollectionResults.mockReset();
  hooks.useCollections.mockReset();
  hooks.useCreateCollection.mockReset();
  hooks.useStartSemanticLinkScan.mockReset();
  hooks.useStartHealthScan.mockReset();
});

describe("CollectionsPage", () => {
  it("renders LIST/TABLE/COMPACT_CARD from the same result refs", () => {
    hooks.useCollection.mockReturnValue({ data: collection, isPending: false, isError: false, refetch: vi.fn() });
    hooks.useCollectionResults.mockReturnValue({ data: page, isPending: false, isFetching: false, isError: false, error: null, refetch: vi.fn() });
    hooks.useStartHealthScan.mockReturnValue({ mutate: vi.fn(), isPending: false, isError: false });
    hooks.useStartSemanticLinkScan.mockReturnValue({ mutate: vi.fn(), isPending: false, isError: false });

    render(<MemoryRouter initialEntries={[`/collections/${collectionId}`]}><Routes><Route path="/collections/:collectionId" element={<CollectionDetailPage />} /></Routes></MemoryRouter>);

    expect(screen.getByLabelText("Collection 列表视图")).toHaveTextContent("TOPIC 3");
    fireEvent.click(screen.getByRole("button", { name: /TABLE/ }));
    expect(screen.getByLabelText("Collection 表格视图")).toHaveTextContent("TOPIC 3");
    expect(screen.getByLabelText("Collection 表格视图")).toHaveTextContent("CLAIM 4");
    fireEvent.click(screen.getByRole("button", { name: /COMPACT_CARD/ }));
    const cardView = screen.getByLabelText("Collection 卡片视图");
    expect(within(cardView).getByText("TOPIC 3")).toBeInTheDocument();
    expect(within(cardView).getByText("CLAIM 4")).toBeInTheDocument();
  });

  it("creates a saved Collection from the current preview query", () => {
    const mutate = vi.fn<(payload: { input: CollectionDefinitionInput; idempotencyKey: string }, options?: unknown) => void>();
    hooks.useCollections.mockReturnValue({ data: { workspaceId, items: [], nextCursor: null }, isPending: false, isError: false, refetch: vi.fn() });
    hooks.useCollectionPreview.mockReturnValue({ data: { ...page, collectionId: null }, isPending: false, isError: false, isFetching: false, refetch: vi.fn() });
    hooks.useCreateCollection.mockReturnValue({ mutate, isPending: false, isError: false });

    render(<MemoryRouter initialEntries={["/collections"]}><Routes><Route path="/collections" element={<CollectionsPage />} /></Routes></MemoryRouter>);
    fireEvent.change(screen.getByPlaceholderText("集合名称"), { target: { value: "保存的集合" } });
    fireEvent.click(screen.getByRole("button", { name: /保存 Collection/ }));

    expect(mutate).toHaveBeenCalledTimes(1);
    const [payload] = mutate.mock.calls[0] ?? [];
    expect(payload?.input.workspaceId).toBe(workspaceId);
    expect(payload?.input.name).toBe("保存的集合");
    expect(payload?.input.viewType).toBe("LIST");
    expect(payload?.idempotencyKey).toContain("collection-create-");
    fireEvent.click(screen.getByRole("button", { name: /保存 Collection/ }));
    expect(mutate.mock.calls[1]?.[0].idempotencyKey).toBe(payload?.idempotencyKey);
    fireEvent.change(screen.getByPlaceholderText("集合名称"), { target: { value: "另一个集合" } });
    fireEvent.click(screen.getByRole("button", { name: /保存 Collection/ }));
    expect(mutate.mock.calls[2]?.[0].idempotencyKey).not.toBe(payload?.idempotencyKey);
  });

  it("consumes Collection list cursors so definitions after the first 50 remain reachable and resets on Workspace change", async () => {
    const firstPage = Array.from({ length: 50 }, (_, index) => ({ ...collection, id: `15000000-0000-4000-8000-${String(index + 100).padStart(12, "0")}`, name: `Collection ${String(index + 1)}` }));
    const lastCollection = { ...collection, id: otherCollectionId, name: "Collection 51" };
    hooks.useCollections.mockImplementation((request: { cursor?: string; limit?: number }) => ({ data: request.cursor ? { workspaceId: hooks.activeWorkspaceId, items: [lastCollection], nextCursor: null } : { workspaceId: hooks.activeWorkspaceId, items: firstPage, nextCursor: "list.next" }, isPending: false, isError: false, refetch: vi.fn() }));
    hooks.useCollectionPreview.mockReturnValue({ data: { ...page, collectionId: null }, isPending: false, isError: false, isFetching: false, refetch: vi.fn() });
    hooks.useCreateCollection.mockReturnValue({ mutate: vi.fn(), isPending: false, isError: false });

    const rendered = render(<MemoryRouter initialEntries={["/collections"]}><Routes><Route path="/collections" element={<CollectionsPage />} /></Routes></MemoryRouter>);
    expect(screen.getByText("Collection 50")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: /下一页/ }));
    expect(await screen.findByText("Collection 51")).toBeInTheDocument();
    expect(hooks.useCollections).toHaveBeenLastCalledWith({ cursor: "list.next", limit: 50 });

    hooks.activeWorkspaceId = otherWorkspaceId;
    rendered.rerender(<MemoryRouter initialEntries={["/collections"]}><Routes><Route path="/collections" element={<CollectionsPage />} /></Routes></MemoryRouter>);
    await waitFor(() => expect(hooks.useCollections).toHaveBeenLastCalledWith({ limit: 50 }));
  });

  it("saves a multi-clause Query Builder request", async () => {
    const mutate = vi.fn<(payload: { input: CollectionDefinitionInput; idempotencyKey: string }, options?: unknown) => void>();
    hooks.useCollections.mockReturnValue({ data: { workspaceId, items: [], nextCursor: null }, isPending: false, isError: false, refetch: vi.fn() });
    hooks.useCollectionPreview.mockReturnValue({ data: { ...page, collectionId: null }, isPending: false, isError: false, isFetching: false, refetch: vi.fn() });
    hooks.useCreateCollection.mockReturnValue({ mutate, isPending: false, isError: false });

    render(<MemoryRouter initialEntries={["/collections"]}><Routes><Route path="/collections" element={<CollectionsPage />} /></Routes></MemoryRouter>);
    fireEvent.click(screen.getByRole("button", { name: "新增条件" }));
    fireEvent.change(screen.getByLabelText("组合"), { target: { value: "OR" } });
    const secondClause = screen.getByLabelText("Collection 条件 2");
    fireEvent.change(within(secondClause).getByLabelText("字段"), { target: { value: "status" } });
    await waitFor(() => expect(within(screen.getByLabelText("Collection 条件 2")).getByLabelText("字段")).toHaveValue("status"));
    fireEvent.change(within(screen.getByLabelText("Collection 条件 2")).getByLabelText("条件 2 值"), { target: { value: "CONFIRMED" } });
    fireEvent.change(screen.getByPlaceholderText("集合名称"), { target: { value: "多条件集合" } });
    fireEvent.click(screen.getByRole("button", { name: /保存 Collection/ }));

    const [payload] = mutate.mock.calls[0] ?? [];
    expect(payload?.input.query.root).toMatchObject({ kind: "group", operator: "OR" });
    expect(payload?.input.query.root.kind === "group" ? payload.input.query.root.clauses : []).toHaveLength(2);
  });

  it("uses controlled enum values and two inputs for confidence BETWEEN", async () => {
    const mutate = vi.fn<(payload: { input: CollectionDefinitionInput; idempotencyKey: string }, options?: unknown) => void>();
    hooks.useCollections.mockReturnValue({ data: { workspaceId, items: [], nextCursor: null }, isPending: false, isError: false, refetch: vi.fn() });
    hooks.useCollectionPreview.mockReturnValue({ data: { ...page, collectionId: null }, isPending: false, isError: false, isFetching: false, refetch: vi.fn() });
    hooks.useCreateCollection.mockReturnValue({ mutate, isPending: false, isError: false });

    render(<MemoryRouter initialEntries={["/collections"]}><Routes><Route path="/collections" element={<CollectionsPage />} /></Routes></MemoryRouter>);
    fireEvent.change(within(screen.getByLabelText("Collection 条件 1")).getByLabelText("Operator"), { target: { value: "IN" } });
    await waitFor(() => expect(within(screen.getByLabelText("Collection 条件 1")).getByLabelText("Operator")).toHaveValue("IN"));
    expect(within(screen.getByLabelText("Collection 条件 1")).getByLabelText("条件 1 值")).toHaveAttribute("multiple");
    fireEvent.change(within(screen.getByLabelText("Collection 条件 1")).getByLabelText("字段"), { target: { value: "confidence" } });
    await waitFor(() => expect(within(screen.getByLabelText("Collection 条件 1")).getByLabelText("字段")).toHaveValue("confidence"));
    fireEvent.change(within(screen.getByLabelText("Collection 条件 1")).getByLabelText("Operator"), { target: { value: "BETWEEN" } });
    await waitFor(() => expect(within(screen.getByLabelText("Collection 条件 1")).getByLabelText("Operator")).toHaveValue("BETWEEN"));
    fireEvent.change(within(screen.getByLabelText("Collection 条件 1")).getByLabelText("条件 1 下界"), { target: { value: "0.2" } });
    fireEvent.change(within(screen.getByLabelText("Collection 条件 1")).getByLabelText("条件 1 上界"), { target: { value: "0.8" } });
    expect(within(screen.getByLabelText("Collection 条件 1")).getByLabelText("条件 1 下界")).toHaveAttribute("type", "number");
    expect(within(screen.getByLabelText("Collection 条件 1")).getByLabelText("条件 1 上界")).toHaveAttribute("type", "number");
    fireEvent.change(screen.getByPlaceholderText("集合名称"), { target: { value: "置信度集合" } });
    fireEvent.click(screen.getByRole("button", { name: /保存 Collection/ }));
    const [payload] = mutate.mock.calls[0] ?? [];
    const predicate = payload?.input.query.root.kind === "group" ? payload.input.query.root.clauses[0] : undefined;
    expect(predicate).toMatchObject({ field: "confidence", operator: "BETWEEN", lower: 0.2, upper: 0.8 });
  });

  it("restores saved view config and starts downstream scans with bounded bindings", () => {
    const healthScan = vi.fn<(payload: Omit<StartHealthScanInput, "workspaceId">, options?: unknown) => void>();
    const relationScan = vi.fn();
    const savedView: Collection = {
      ...collection,
      viewType: "TABLE",
      viewConfig: { ...collection.viewConfig, columns: ["object_type", "title"], groupBy: { field: "object_type" }, density: "COMPACT" },
    };
    const emptyPage: CollectionResultPage = { ...page, exactCount: 0 };
    hooks.useCollection.mockReturnValue({ data: savedView, isPending: false, isError: false, refetch: vi.fn() });
    hooks.useCollectionResults.mockReturnValue({ data: emptyPage, isPending: false, isFetching: false, isError: false, error: null, refetch: vi.fn() });
    hooks.useStartHealthScan.mockReturnValue({ mutate: healthScan, isPending: false, isError: false });
    hooks.useStartSemanticLinkScan.mockReturnValue({ mutate: relationScan, isPending: false, isError: false });

    render(<MemoryRouter initialEntries={[`/collections/${collectionId}`]}><Routes><Route path="/collections/:collectionId" element={<CollectionDetailPage />} /></Routes></MemoryRouter>);

    expect(screen.getByLabelText("Collection 表格视图").closest(".collection-view-density")).toHaveClass("collection-view-density--compact");
    expect(screen.queryByRole("columnheader", { name: "summary" })).not.toBeInTheDocument();
    const fixedTitle = screen.getByLabelText("title");
    expect(fixedTitle).toBeDisabled();
    expect(fixedTitle).toBeChecked();
    fireEvent.click(fixedTitle);
    expect(fixedTitle).toBeChecked();
    fireEvent.click(screen.getByRole("button", { name: /启动 Health Scan/ }));
    const [healthPayload] = healthScan.mock.calls[0] ?? [];
    expect(healthPayload?.scope).toMatchObject({ type: "SMART_COLLECTION", ref: collectionId, readModelRevision: emptyPage.scanRevisionHash, exactCount: 0 });
    fireEvent.click(screen.getByRole("button", { name: /启动 Health Scan/ }));
    expect(healthScan.mock.calls[1]?.[0].idempotencyKey).toBe(healthPayload?.idempotencyKey);
    fireEvent.click(screen.getByRole("button", { name: /启动关系分析/ }));
    expect(relationScan).toHaveBeenCalledWith(expect.objectContaining({
      workspaceId,
      scope: { kind: "SMART_COLLECTION", collectionId },
      idempotencyKey: "candidate-scan:test",
    }), expect.any(Object));
  });

  it("does not send an old result cursor after the route Collection changes", async () => {
    hooks.useCollection.mockImplementation((id: string) => ({ data: { ...collection, id, queryHash: id === collectionId ? "a".repeat(64) : "d".repeat(64) }, isPending: false, isError: false, refetch: vi.fn() }));
    hooks.useCollectionResults.mockImplementation((id: string, binding: { version: number; queryHash: string } | undefined) => ({ data: { ...page, collectionId: id, queryHash: binding?.queryHash ?? "a".repeat(64), nextCursor: "result.next" }, isPending: false, isFetching: false, isError: false, error: null, refetch: vi.fn() }));
    hooks.useStartHealthScan.mockReturnValue({ mutate: vi.fn(), isPending: false, isError: false });
    hooks.useStartSemanticLinkScan.mockReturnValue({ mutate: vi.fn(), isPending: false, isError: false });

    render(<MemoryRouter initialEntries={[`/collections/${collectionId}`]}><Link to={`/collections/${otherCollectionId}`}>切换集合</Link><Routes><Route path="/collections/:collectionId" element={<CollectionDetailPage />} /></Routes></MemoryRouter>);
    fireEvent.click(screen.getByRole("button", { name: /下一页/ }));
    await waitFor(() => expect(hooks.useCollectionResults).toHaveBeenLastCalledWith(collectionId, { version: 2, queryHash: "a".repeat(64) }, { cursor: "result.next", limit: 25 }));
    fireEvent.click(screen.getByRole("link", { name: "切换集合" }));
    await waitFor(() => expect(hooks.useCollectionResults).toHaveBeenLastCalledWith(otherCollectionId, { version: 2, queryHash: "d".repeat(64) }, { limit: 25 }));
  });

  it.each([
    [5000, false],
    [5001, true],
    [50000, true],
  ])("enforces the 5000 item Health Scan capacity at exact_count=%i", (exactCount, disabled) => {
    hooks.useCollection.mockReturnValue({ data: collection, isPending: false, isError: false, refetch: vi.fn() });
    hooks.useCollectionResults.mockReturnValue({ data: { ...page, exactCount }, isPending: false, isFetching: false, isError: false, error: null, refetch: vi.fn() });
    hooks.useStartHealthScan.mockReturnValue({ mutate: vi.fn(), isPending: false, isError: false });
    hooks.useStartSemanticLinkScan.mockReturnValue({ mutate: vi.fn(), isPending: false, isError: false });

    render(<MemoryRouter initialEntries={[`/collections/${collectionId}`]}><Routes><Route path="/collections/:collectionId" element={<CollectionDetailPage />} /></Routes></MemoryRouter>);
    const button = screen.getByRole("button", { name: /启动 Health Scan/ });
    if (disabled) {
      expect(button).toBeDisabled();
      expect(screen.getByText(new RegExp(`当前集合有 ${String(exactCount)} 个对象，单次 Health Scan 最多处理 5000 个`))).toBeInTheDocument();
    } else {
      expect(button).toBeEnabled();
      expect(screen.getByText("将扫描 5000 个对象。")).toBeInTheDocument();
    }
  });

  it("converts datetime-local query values to RFC3339", () => {
    const local = "2026-07-22T09:30";
    const state = {
      ...parseCollectionUrlState(new URLSearchParams()),
      field: "created_at" as const,
      operator: "GTE" as const,
      value: local,
      logic: "AND" as const,
      clauses: [{ field: "created_at" as const, operator: "GTE" as const, value: local }],
      selected: "",
    };
    const built = buildCollectionQuery(state);
    const first = built.root.kind === "group" ? built.root.clauses[0] : undefined;
    expect(first?.kind === "predicate" ? first.value : undefined).toBe(new Date(local).toISOString());
  });
});
