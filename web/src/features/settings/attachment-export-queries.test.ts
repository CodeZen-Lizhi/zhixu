import { QueryClient } from "@tanstack/react-query";
import { describe, expect, it } from "vitest";

import type { AttachmentExportJob } from "../../api/attachment-exports";
import { attachmentExportPollInterval, attachmentExportPollMilliseconds, clearAttachmentExportWorkspaceQueries } from "./attachment-export-queries";
import { attachmentExportQueryKeys } from "./attachment-export-query-keys";

const workspaceA = "73000000-0000-4000-8000-000000000001";
const workspaceB = "73000000-0000-4000-8000-000000000002";
const baseJob: AttachmentExportJob = {
  id: "73000000-0000-4000-8000-000000000003", workspaceId: workspaceA, scopeKind: "WORKSPACE_ATTACHMENTS", kind: "ATTACHMENTS_ZIP", schemaVersion: "attachment-export/v1", attachmentRootContractVersion: "workspace-attachments/v1", contentPolicy: "RAW_USER_OWNED",
  status: "PENDING", version: 1, manifestSHA256: null, entryCount: null, totalUncompressedBytes: null, archiveSHA256: null, archiveSize: null, errorCode: null, errorMessage: null, attemptCount: 0,
  expiresAt: "2026-07-31T00:00:00Z", createdAt: "2026-07-30T00:00:00Z", updatedAt: "2026-07-30T00:00:00Z", startedAt: null, completedAt: null, downloadCount: 0, lastDownloadedAt: null, downloadUrl: null,
};

describe("Attachment Export queries", () => {
  it("binds list keys to Workspace, tagged scope and opaque cursor", () => {
    expect(attachmentExportQueryKeys.list(workspaceA)).toEqual(["workspace-attachment-exports", workspaceA, "WORKSPACE_ATTACHMENTS", ""]);
    expect(attachmentExportQueryKeys.list(workspaceA, "opaque.cursor")).toEqual(["workspace-attachment-exports", workspaceA, "WORKSPACE_ATTACHMENTS", "opaque.cursor"]);
  });

  it("polls exactly every two seconds only while a job is active", () => {
    expect(attachmentExportPollInterval({ workspaceId: workspaceA, scopeKind: "WORKSPACE_ATTACHMENTS", items: [baseJob], nextCursor: null })).toBe(attachmentExportPollMilliseconds);
    expect(attachmentExportPollInterval({ workspaceId: workspaceA, scopeKind: "WORKSPACE_ATTACHMENTS", items: [{ ...baseJob, status: "FAILED", startedAt: "2026-07-30T00:00:01Z", completedAt: "2026-07-30T00:00:02Z", errorCode: "EXPORT_ATTACHMENT_ROOT_NOT_FOUND", errorMessage: "attachments root is missing" }], nextCursor: null })).toBe(false);
  });

  it("clears only the previous Workspace attachment Export cache", () => {
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    queryClient.setQueryData(attachmentExportQueryKeys.list(workspaceA), { workspaceId: workspaceA });
    queryClient.setQueryData(attachmentExportQueryKeys.list(workspaceB), { workspaceId: workspaceB });
    clearAttachmentExportWorkspaceQueries(queryClient, workspaceA);
    expect(queryClient.getQueryData(attachmentExportQueryKeys.list(workspaceA))).toBeUndefined();
    expect(queryClient.getQueryData(attachmentExportQueryKeys.list(workspaceB))).toEqual({ workspaceId: workspaceB });
  });
});
