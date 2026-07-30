import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import { type PropsWithChildren } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";

import type { AttachmentExportCreateResult, AttachmentExportJob } from "../../api/attachment-exports";
import { attachmentExportQueryKeys } from "./attachment-export-query-keys";

const api = vi.hoisted(() => ({
  createAttachmentExport: vi.fn(),
  getAttachmentExport: vi.fn(),
  listAttachmentExports: vi.fn(),
}));
const workspace = vi.hoisted(() => ({ id: "73000000-0000-4000-8000-000000000001" }));

vi.mock("../../api/attachment-exports", () => api);
vi.mock("../../app/active-workspace", () => ({ useActiveWorkspaceId: () => workspace.id }));

import { useCreateAttachmentExport } from "./attachment-export-queries";

const workspaceA = "73000000-0000-4000-8000-000000000001";
const workspaceB = "73000000-0000-4000-8000-000000000002";
const job: AttachmentExportJob = {
  id: "73000000-0000-4000-8000-000000000003", workspaceId: workspaceA,
  scopeKind: "WORKSPACE_ATTACHMENTS", kind: "ATTACHMENTS_ZIP", schemaVersion: "attachment-export/v1",
  attachmentRootContractVersion: "workspace-attachments/v1", contentPolicy: "RAW_USER_OWNED",
  status: "PENDING", version: 1, manifestSHA256: null, entryCount: null, totalUncompressedBytes: null,
  archiveSHA256: null, archiveSize: null, errorCode: null, errorMessage: null, attemptCount: 0,
  expiresAt: "2026-07-31T00:00:00Z", createdAt: "2026-07-30T00:00:00Z", updatedAt: "2026-07-30T00:00:00Z",
  startedAt: null, completedAt: null, downloadCount: 0, lastDownloadedAt: null, downloadUrl: null,
};

const wrapperFor = (queryClient: QueryClient) => ({ children }: PropsWithChildren) => (
  <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
);

afterEach(() => {
  workspace.id = workspaceA;
  api.createAttachmentExport.mockReset();
});

describe("Attachment Export create mutation", () => {
  it("aborts and ignores a late success after the active Workspace changes", async () => {
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
    let resolveCreate: ((value: AttachmentExportCreateResult) => void) | undefined;
    let requestSignal: AbortSignal | undefined;
    api.createAttachmentExport.mockImplementation((_input: unknown, signal: AbortSignal) => {
      requestSignal = signal;
      return new Promise<AttachmentExportCreateResult>((resolve) => { resolveCreate = resolve; });
    });
    const view = renderHook(() => useCreateAttachmentExport(), { wrapper: wrapperFor(queryClient) });

    act(() => view.result.current.mutate({ workspaceId: workspaceA, idempotencyKey: "attachment-create-a" }));
    await waitFor(() => expect(api.createAttachmentExport).toHaveBeenCalledTimes(1));
    workspace.id = workspaceB;
    view.rerender();

    await waitFor(() => expect(requestSignal?.aborted).toBe(true));
    await act(async () => {
      resolveCreate?.({ job, replayed: false, dispatchPending: false });
      await Promise.resolve();
    });

    expect(queryClient.getQueryData(attachmentExportQueryKeys.detail(workspaceA, job.id))).toBeUndefined();
    expect(queryClient.getQueriesData({ queryKey: attachmentExportQueryKeys.all(workspaceA) })).toHaveLength(0);
    expect(queryClient.getQueriesData({ queryKey: attachmentExportQueryKeys.all(workspaceB) })).toHaveLength(0);
    expect(view.result.current.isError).toBe(false);
  });
});
