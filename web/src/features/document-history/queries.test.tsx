import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import type { PropsWithChildren } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { documentHistoryQueryKeys } from "./query-keys";

const api = vi.hoisted(() => ({
  compareDocumentHistory: vi.fn(),
  createDocumentRestorePreview: vi.fn(),
  createDocumentRestoreProposal: vi.fn(),
  isDocumentHistoryId: (value: string) => value.length === 36,
  listDocumentHistory: vi.fn(),
}));
const activeWorkspace = vi.hoisted(() => ({ id: "e1000000-0000-4000-8000-000000000001" }));

vi.mock("../../api/document-history", () => api);
vi.mock("../../app/active-workspace", () => ({
  getActiveWorkspaceId: () => activeWorkspace.id,
  useActiveWorkspaceId: () => activeWorkspace.id,
}));

import {
  useCreateDocumentRestoreProposal,
  useDocumentHistoryCompare,
  useDocumentHistoryPage,
  useDocumentHistoryRecovery,
} from "./queries";

const workspaceId = "e1000000-0000-4000-8000-000000000001";
const documentId = "e1000000-0000-4000-8000-000000000002";
const head = "a".repeat(40);
const left = "b".repeat(40);
const right = "c".repeat(40);

const wrapperFor = (client: QueryClient) => ({ children }: PropsWithChildren) => <QueryClientProvider client={client}>{children}</QueryClientProvider>;

afterEach(() => {
  activeWorkspace.id = workspaceId;
  for (const mock of [api.compareDocumentHistory, api.createDocumentRestorePreview, api.createDocumentRestoreProposal, api.listDocumentHistory]) mock.mockReset();
});

describe("Document History Query ownership", () => {
  it("binds list and compare keys to Workspace, Document, baseline, cursor and refs", () => {
    expect(documentHistoryQueryKeys.history(workspaceId, documentId, head, "signed-cursor", 30)).toEqual([
      "document-history", workspaceId, "documents", documentId, "history", head, "signed-cursor", 30,
    ]);
    expect(documentHistoryQueryKeys.compare(workspaceId, documentId, head, "notes/a.md", 7, left, right)).toEqual([
      "document-history", workspaceId, "documents", documentId, "comparisons", head, "notes/a.md", 7, left, right,
    ]);
  });

  it("passes the active Workspace and AbortSignal while keeping HEAD out of the server cursor request", async () => {
    api.listDocumentHistory.mockResolvedValue({ workspaceId, documentId, documentVersion: 1, path: "notes/a.md", branch: "main", head, dirty: false, items: [] });
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const view = renderHook(() => useDocumentHistoryPage(documentId, head, "signed-cursor", 30), { wrapper: wrapperFor(client) });

    await waitFor(() => expect(view.result.current.isSuccess).toBe(true));
    expect(api.listDocumentHistory).toHaveBeenCalledWith({ workspaceId, documentId, cursor: "signed-cursor", limit: 30 }, expect.any(AbortSignal));
  });

  it("does not compare until both explicit refs and the discovered HEAD are available", async () => {
    api.compareDocumentHistory.mockResolvedValue({ workspaceId, documentId, path: "notes/a.md", head, left, right, leftContent: "a", rightContent: "b", patch: "p", diffHash: "d".repeat(64) });
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const disabled = renderHook(() => useDocumentHistoryCompare({ documentId, left, right }, "", "", 0), { wrapper: wrapperFor(client) });

    expect(disabled.result.current.fetchStatus).toBe("idle");
    expect(api.compareDocumentHistory).not.toHaveBeenCalled();
    disabled.unmount();

    const enabled = renderHook(() => useDocumentHistoryCompare({ documentId, left, right }, head, "notes/a.md", 7), { wrapper: wrapperFor(client) });
    await waitFor(() => expect(enabled.result.current.isSuccess).toBe(true));
    expect(api.compareDocumentHistory).toHaveBeenCalledWith({ workspaceId, documentId, expectedHead: head, expectedPath: "notes/a.md", left, right }, expect.any(AbortSignal));
  });

  it("invalidates only the current Workspace Proposal list after a restore command", async () => {
    api.createDocumentRestoreProposal.mockResolvedValue({ proposalId: documentId, proposalRevisionId: documentId, proposalType: "restore_document", status: "ready_for_review", changeHash: "d".repeat(64), replayed: false });
    const client = new QueryClient({ defaultOptions: { mutations: { retry: false } } });
    const invalidate = vi.spyOn(client, "invalidateQueries");
    const view = renderHook(() => useCreateDocumentRestoreProposal(), { wrapper: wrapperFor(client) });
    const command = {
      workspaceId,
      documentId,
      targetCommit: left,
      expectedHead: head,
      expectedDocumentVersion: 2,
      previewHash: "d".repeat(64),
      idempotencyKey: "restore-intent-1",
    };

    act(() => view.result.current.mutate(command));
    await waitFor(() => expect(view.result.current.isSuccess).toBe(true));
    expect(invalidate).toHaveBeenCalledWith({ queryKey: ["business", workspaceId, "proposals"] });
  });

  it("resets local cursor state before recovering the authoritative first page", async () => {
    const order: string[] = [];
    api.listDocumentHistory.mockImplementation(() => {
      order.push("fetch");
      return Promise.resolve({ workspaceId, documentId, documentVersion: 7, path: "notes/a.md", branch: "main", head, dirty: false, items: [] });
    });
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const reset = vi.fn(() => order.push("reset"));
    const view = renderHook(() => useDocumentHistoryRecovery(documentId, 30, reset), { wrapper: wrapperFor(client) });

    await act(async () => view.result.current(workspaceId));

    expect(order).toEqual(["reset", "fetch"]);
    expect(api.listDocumentHistory).toHaveBeenCalledWith({ workspaceId, documentId, limit: 30 }, expect.any(AbortSignal));
    expect(client.getQueryData(documentHistoryQueryKeys.history(workspaceId, documentId, "", "", 30))).toBeDefined();

    await act(async () => view.result.current("e1000000-0000-4000-8000-000000000099"));
    expect(api.listDocumentHistory).toHaveBeenCalledOnce();
  });
});
