import { QueryClient } from "@tanstack/react-query";
import { describe, expect, it } from "vitest";

import type { ExportJob } from "../../api/exports";
import { clearCollectionExportWorkspaceQueries, collectionExportPollInterval, collectionExportPollMilliseconds } from "./export-queries";
import { collectionExportQueryKeys } from "./export-query-keys";

const workspaceA = "73000000-0000-4000-8000-000000000001";
const workspaceB = "73000000-0000-4000-8000-000000000002";
const collectionId = "73000000-0000-4000-8000-000000000003";
const baseJob: ExportJob = {
  id: "73000000-0000-4000-8000-000000000004", version: 1, workspaceId: workspaceA, kind: "MARKDOWN", schemaVersion: "export/v1", collectionId, collectionVersion: 1, queryHash: "a".repeat(64), readModelRevision: null, exactCount: null, fields: ["object_type"], redactionPolicy: "MASKED", includeSensitive: false,
  status: "PENDING", fileHash: null, fileSize: 0, errorCode: null, errorMessage: null, attemptCount: 0, expiresAt: "2026-07-27T00:00:00Z", createdAt: "2026-07-26T00:00:00Z", updatedAt: "2026-07-26T00:00:00Z", startedAt: null, completedAt: null, downloadCount: 0, lastDownloadedAt: null, downloadUrl: null,
};

describe("Collection Export queries", () => {
  it("binds list keys to Workspace, Collection and opaque cursor", () => {
    expect(collectionExportQueryKeys.list(workspaceA, collectionId)).toEqual(["collection-exports", workspaceA, collectionId, ""]);
    expect(collectionExportQueryKeys.list(workspaceA, collectionId, "opaque.cursor")).toEqual(["collection-exports", workspaceA, collectionId, "opaque.cursor"]);
  });

  it("polls exactly every two seconds only while a job is active", () => {
    expect(collectionExportPollInterval({ workspaceId: workspaceA, items: [baseJob], nextCursor: null })).toBe(collectionExportPollMilliseconds);
    expect(collectionExportPollInterval({ workspaceId: workspaceA, items: [{ ...baseJob, status: "SUCCEEDED", fileHash: "b".repeat(64), fileSize: 1, readModelRevision: "c".repeat(64), exactCount: 1, startedAt: "2026-07-26T00:00:01Z", completedAt: "2026-07-26T00:00:02Z", downloadUrl: "/api/v1/exports/73000000-0000-4000-8000-000000000004/download?workspace_id=73000000-0000-4000-8000-000000000001" }], nextCursor: null })).toBe(false);
  });

  it("clears only the previous Workspace Export cache", () => {
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    queryClient.setQueryData(collectionExportQueryKeys.list(workspaceA, collectionId), { workspaceId: workspaceA });
    queryClient.setQueryData(collectionExportQueryKeys.list(workspaceB, collectionId), { workspaceId: workspaceB });
    clearCollectionExportWorkspaceQueries(queryClient, workspaceA);
    expect(queryClient.getQueryData(collectionExportQueryKeys.list(workspaceA, collectionId))).toBeUndefined();
    expect(queryClient.getQueryData(collectionExportQueryKeys.list(workspaceB, collectionId))).toEqual({ workspaceId: workspaceB });
  });
});
