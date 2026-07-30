import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import type { AttachmentExportJob } from "../../api/attachment-exports";

const workspaceId = "74000000-0000-4000-8000-000000000001";
const secondWorkspaceId = "74000000-0000-4000-8000-000000000099";
const hooks = vi.hoisted(() => ({ useAttachmentExports: vi.fn(), useCreateAttachmentExport: vi.fn(), downloadAttachmentExport: vi.fn() }));
const workspace = vi.hoisted(() => ({ id: "74000000-0000-4000-8000-000000000001" }));

vi.mock("../../app/active-workspace", () => ({ useActiveWorkspaceId: () => workspace.id }));
vi.mock("./attachment-export-queries", () => ({ useAttachmentExports: hooks.useAttachmentExports, useCreateAttachmentExport: hooks.useCreateAttachmentExport }));
vi.mock("../../api/attachment-exports", async (importOriginal) => ({ ...(await importOriginal()), downloadAttachmentExport: hooks.downloadAttachmentExport }));

import { AttachmentExportPanel } from "./AttachmentExportPanel";

const baseJob: AttachmentExportJob = {
  id: "74000000-0000-4000-8000-000000000002", workspaceId, scopeKind: "WORKSPACE_ATTACHMENTS", kind: "ATTACHMENTS_ZIP", schemaVersion: "attachment-export/v1", attachmentRootContractVersion: "workspace-attachments/v1", contentPolicy: "RAW_USER_OWNED",
  status: "FAILED", version: 3, manifestSHA256: null, entryCount: null, totalUncompressedBytes: null, archiveSHA256: null, archiveSize: null, errorCode: "EXPORT_ATTACHMENT_ROOT_NOT_FOUND", errorMessage: "attachments root is missing", attemptCount: 1,
  expiresAt: "2026-07-31T00:00:00Z", createdAt: "2026-07-30T00:00:00Z", updatedAt: "2026-07-30T00:00:02Z", startedAt: "2026-07-30T00:00:01Z", completedAt: "2026-07-30T00:00:02Z", downloadCount: 0, lastDownloadedAt: null, downloadUrl: null,
};

const renderPanel = () => {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const panel = () => <QueryClientProvider client={client}><AttachmentExportPanel /></QueryClientProvider>;
  const view = render(panel());
  return { ...view, rerenderPanel: () => view.rerender(panel()) };
};

afterEach(() => {
	workspace.id = workspaceId;
  hooks.useAttachmentExports.mockReset();
  hooks.useCreateAttachmentExport.mockReset();
  hooks.downloadAttachmentExport.mockReset();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("AttachmentExportPanel", () => {
  it("reuses one key for response-loss retry and allocates a new key after a failed job", () => {
    const mutate = vi.fn();
    hooks.useAttachmentExports.mockReturnValue({ data: { workspaceId, scopeKind: "WORKSPACE_ATTACHMENTS", items: [baseJob], nextCursor: null }, isError: false, isPending: false, isFetching: false, refetch: vi.fn() });
    hooks.useCreateAttachmentExport.mockReturnValue({ mutate, isPending: false, isError: true, error: new Error("response lost") });
    renderPanel();

    fireEvent.click(screen.getByRole("button", { name: "创建附件导出" }));
    fireEvent.click(screen.getByRole("button", { name: "重试" }));
    fireEvent.click(screen.getByRole("button", { name: "新建附件导出" }));

    expect(mutate).toHaveBeenCalledTimes(3);
    const first = mutate.mock.calls[0]?.[0] as { idempotencyKey: string };
    const retry = mutate.mock.calls[1]?.[0] as { idempotencyKey: string };
    const newAfterFailure = mutate.mock.calls[2]?.[0] as { idempotencyKey: string };
    expect(first.idempotencyKey).toBe(retry.idempotencyKey);
    expect(newAfterFailure.idempotencyKey).not.toBe(first.idempotencyKey);
    expect(screen.getByText("RAW_USER_OWNED")).toBeInTheDocument();
    expect(screen.getByRole("alert")).toHaveTextContent("response lost");
  });

  it("downloads a succeeded archive through the strict API client and refreshes server facts", async () => {
    const refetch = vi.fn().mockResolvedValue(undefined);
    const succeeded = { ...baseJob, status: "SUCCEEDED" as const, manifestSHA256: "a".repeat(64), entryCount: 2, totalUncompressedBytes: 13, archiveSHA256: "b".repeat(64), archiveSize: 29, errorCode: null, errorMessage: null, completedAt: "2026-07-30T00:00:02Z", downloadUrl: `/api/v1/workspaces/${workspaceId}/attachment-exports/${baseJob.id}/download` };
    hooks.useAttachmentExports.mockReturnValue({ data: { workspaceId, scopeKind: "WORKSPACE_ATTACHMENTS", items: [succeeded], nextCursor: null }, isError: false, isPending: false, isFetching: false, refetch });
    hooks.useCreateAttachmentExport.mockReturnValue({ mutate: vi.fn(), isPending: false, isError: false });
    hooks.downloadAttachmentExport.mockResolvedValue({ blob: new Blob(["archive"]), filename: `workspace-attachments-${baseJob.id}.zip` });
    vi.stubGlobal("URL", { ...URL, createObjectURL: vi.fn(() => "blob:attachment-export"), revokeObjectURL: vi.fn() });
    vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => undefined);
    renderPanel();

    fireEvent.click(screen.getByRole("button", { name: "下载 ZIP" }));
    await waitFor(() => expect(hooks.downloadAttachmentExport).toHaveBeenCalledWith(workspaceId, succeeded));
    await waitFor(() => expect(refetch).toHaveBeenCalled());
    expect(screen.getByText(/2 个文件，原始数据 13 B/)).toBeInTheDocument();
  });

  it("does not leak a late dispatch warning across Workspace changes", () => {
	const mutate = vi.fn();
	hooks.useAttachmentExports.mockReturnValue({ data: { workspaceId, scopeKind: "WORKSPACE_ATTACHMENTS", items: [], nextCursor: null }, isError: false, isPending: false, isFetching: false, refetch: vi.fn() });
	hooks.useCreateAttachmentExport.mockReturnValue({ mutate, isPending: false, isError: false });
	const view = renderPanel();

	fireEvent.click(screen.getByRole("button", { name: "创建附件导出" }));
	workspace.id = secondWorkspaceId;
	view.rerenderPanel();
	fireEvent.click(screen.getByRole("button", { name: "创建附件导出" }));
	const options = mutate.mock.calls[0]?.[1] as { onSuccess: (result: { dispatchPending: boolean; job: AttachmentExportJob }) => void };
	act(() => options.onSuccess({ dispatchPending: true, job: baseJob }));
	expect(screen.queryByText("任务已保存，等待重新调度")).not.toBeInTheDocument();
	fireEvent.click(screen.getByRole("button", { name: "创建附件导出" }));
	const firstSecondWorkspaceAttempt = mutate.mock.calls[1]?.[0] as { idempotencyKey: string; workspaceId: string };
	const retriedSecondWorkspaceAttempt = mutate.mock.calls[2]?.[0] as { idempotencyKey: string; workspaceId: string };
	expect(firstSecondWorkspaceAttempt).toMatchObject({ workspaceId: secondWorkspaceId });
	expect(retriedSecondWorkspaceAttempt).toEqual(firstSecondWorkspaceAttempt);

	workspace.id = workspaceId;
	view.rerenderPanel();
	expect(screen.queryByText("任务已保存，等待重新调度")).not.toBeInTheDocument();
  });
});
