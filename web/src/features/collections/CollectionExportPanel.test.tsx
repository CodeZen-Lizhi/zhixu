import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, describe, expect, it, vi } from "vitest";

import type { Collection } from "../../api/collections";
import type { ExportCreateInput, ExportCreateResult, ExportJob } from "../../api/exports";

const workspaceId = "74000000-0000-4000-8000-000000000001";
const collectionId = "74000000-0000-4000-8000-000000000002";
const hooks = vi.hoisted(() => ({ useCollectionExports: vi.fn(), useCreateCollectionExport: vi.fn(), downloadExport: vi.fn() }));

vi.mock("../../app/active-workspace", () => ({ useActiveWorkspaceId: () => workspaceId }));
vi.mock("./export-queries", () => ({ useCollectionExports: hooks.useCollectionExports, useCreateCollectionExport: hooks.useCreateCollectionExport }));
vi.mock("../../api/exports", async (importOriginal) => ({ ...(await importOriginal()), downloadExport: hooks.downloadExport }));

import { CollectionExportPanel } from "./CollectionExportPanel";

const collection: Collection = {
  id: collectionId, workspaceId, name: "Exportable Collection", description: "", querySchemaVersion: "collection-query/v1", queryVersion: 1,
  query: { schema_version: "collection-query/v1", root: { kind: "group", operator: "AND", clauses: [] } }, queryHash: "a".repeat(64), viewType: "LIST",
  viewConfig: { columns: ["title"], fixedColumns: ["title"], sort: [], groupBy: null, density: "COMFORTABLE" }, status: "ACTIVE", cachedResultVersion: null, lastExecutedAt: null, version: 2, createdAt: "2026-07-26T00:00:00Z", updatedAt: "2026-07-26T00:00:00Z",
};
const cancelled: ExportJob = {
  id: "74000000-0000-4000-8000-000000000003", version: 1, workspaceId, kind: "METADATA_JSON", schemaVersion: "export/v1", collectionId, collectionVersion: 2, queryHash: "a".repeat(64), readModelRevision: null, exactCount: null, fields: ["object_type"], redactionPolicy: "MASKED", includeSensitive: false,
  status: "CANCELLED", fileHash: null, fileSize: 0, errorCode: null, errorMessage: null, attemptCount: 1, expiresAt: "2026-07-27T00:00:00Z", createdAt: "2026-07-26T00:00:00Z", updatedAt: "2026-07-26T00:00:00Z", startedAt: "2026-07-26T00:00:01Z", completedAt: "2026-07-26T00:00:02Z", downloadCount: 0, lastDownloadedAt: null, downloadUrl: null,
};
const renderPanel = (value: Collection = collection) => render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}><CollectionExportPanel collection={value} /></QueryClientProvider>);

afterEach(() => { hooks.useCollectionExports.mockReset(); hooks.useCreateCollectionExport.mockReset(); hooks.downloadExport.mockReset(); });

describe("CollectionExportPanel", () => {
  it("reuses the same key when a create response is lost", () => {
    const calls: ExportCreateInput[] = [];
    const mutate = vi.fn((input: ExportCreateInput) => { calls.push(input); });
    hooks.useCollectionExports.mockReturnValue({ data: { workspaceId, items: [], nextCursor: null }, isPending: false, isError: false, isFetching: false, refetch: vi.fn() });
    hooks.useCreateCollectionExport.mockReturnValue({ mutate, isPending: false, isError: false });
    renderPanel();
    fireEvent.click(screen.getByRole("button", { name: /创建 Markdown 导出/ }));
    fireEvent.click(screen.getByRole("button", { name: /创建 Markdown 导出/ }));
    expect(mutate).toHaveBeenCalledTimes(2);
    expect(calls[0]).toMatchObject({ workspaceId, collectionId, collectionVersion: 2, queryHash: "a".repeat(64), kind: "MARKDOWN" });
    expect(calls[1]?.idempotencyKey).toBe(calls[0]?.idempotencyKey);
  });

  it("shows historical CANCELLED without a cancel command", () => {
    hooks.useCollectionExports.mockReturnValue({ data: { workspaceId, items: [cancelled], nextCursor: null }, isPending: false, isError: false, isFetching: false, refetch: vi.fn() });
    hooks.useCreateCollectionExport.mockReturnValue({ mutate: vi.fn(), isPending: false, isError: false });
    renderPanel();
    expect(screen.getByText("已取消（历史）")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /取消/ })).not.toBeInTheDocument();
  });

  it("disables new exports for an archived Collection while retaining history", () => {
    hooks.useCollectionExports.mockReturnValue({ data: { workspaceId, items: [cancelled], nextCursor: null }, isPending: false, isError: false, isFetching: false, refetch: vi.fn() });
    hooks.useCreateCollectionExport.mockReturnValue({ mutate: vi.fn(), isPending: false, isError: false });
    renderPanel({ ...collection, status: "ARCHIVED" });
    expect(screen.getByText("归档 Collection 不可创建导出")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /创建 .* 导出/ })).not.toBeInTheDocument();
    expect(screen.getByText("已取消（历史）")).toBeInTheDocument();
  });

  it("显示 dispatch_pending、失败码，并用内存 cursor 翻页", async () => {
    const mutate = vi.fn((_: unknown, options: { onSuccess?: (result: ExportCreateResult) => void }) => options.onSuccess?.({ job: { ...cancelled, id: "74000000-0000-4000-8000-000000000004", status: "PENDING", startedAt: null, completedAt: null }, replayed: false, dispatchPending: true }));
    const failed: ExportJob = { ...cancelled, status: "FAILED", errorCode: "EXPORT_DEPENDENCY_UNAVAILABLE", errorMessage: "Worker 暂不可用" };
    hooks.useCollectionExports.mockImplementation((_id: string, cursor?: string) => ({ data: cursor === "next.cursor" ? { workspaceId, items: [], nextCursor: null } : { workspaceId, items: [failed], nextCursor: "next.cursor" }, isPending: false, isError: false, isFetching: false, refetch: vi.fn() }));
    hooks.useCreateCollectionExport.mockReturnValue({ mutate, isPending: false, isError: false });
    renderPanel();
    fireEvent.click(screen.getByRole("button", { name: /创建 Markdown 导出/ }));
    expect(screen.getByText("任务已保存，等待重新调度")).toBeInTheDocument();
    expect(screen.getByText(/EXPORT_DEPENDENCY_UNAVAILABLE：Worker 暂不可用/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "下一页" }));
    await waitFor(() => expect(hooks.useCollectionExports).toHaveBeenLastCalledWith(collectionId, "next.cursor"));
    fireEvent.click(screen.getByRole("button", { name: "上一页" }));
    await waitFor(() => expect(hooks.useCollectionExports).toHaveBeenLastCalledWith(collectionId, undefined));
  });

  it("任务进入非 PENDING 后隐藏 dispatch_pending 提示", async () => {
    const mutate = vi.fn((_: unknown, options: { onSuccess?: (result: ExportCreateResult) => void }) => options.onSuccess?.({ job: { ...cancelled, status: "PENDING", startedAt: null, completedAt: null }, replayed: false, dispatchPending: true }));
    hooks.useCollectionExports.mockReturnValue({ data: { workspaceId, items: [{ ...cancelled, status: "RUNNING", completedAt: null }], nextCursor: null }, isPending: false, isError: false, isFetching: false, refetch: vi.fn() });
    hooks.useCreateCollectionExport.mockReturnValue({ mutate, isPending: false, isError: false });
    renderPanel();
    fireEvent.click(screen.getByRole("button", { name: /创建 Markdown 导出/ }));
    await waitFor(() => expect(screen.queryByText("任务已保存，等待重新调度")).not.toBeInTheDocument());
  });

  it("从失败的 JSON 历史任务新建时保留其格式、当前绑定并生成新 key", () => {
    const calls: ExportCreateInput[] = [];
    const mutate = vi.fn((input: ExportCreateInput) => { calls.push(input); });
    const failedJson: ExportJob = { ...cancelled, status: "FAILED", errorCode: "EXPORT_FAILED", errorMessage: "JSON render failed" };
    hooks.useCollectionExports.mockReturnValue({ data: { workspaceId, items: [failedJson], nextCursor: null }, isPending: false, isError: false, isFetching: false, refetch: vi.fn() });
    hooks.useCreateCollectionExport.mockReturnValue({ mutate, isPending: false, isError: false });
    renderPanel();
    fireEvent.click(screen.getByRole("button", { name: /创建 Markdown 导出/ }));
    fireEvent.click(screen.getByRole("button", { name: "新建导出" }));
    expect(calls[1]).toMatchObject({ kind: "METADATA_JSON", collectionVersion: collection.version, queryHash: collection.queryHash });
    expect(calls[1]?.idempotencyKey).not.toBe(calls[0]?.idempotencyKey);
  });
});
