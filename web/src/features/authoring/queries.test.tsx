import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import type { PropsWithChildren } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";

import type { WorkingDraft, WorkingDraftCommandResult } from "../../api/authoring";
import { authoringQueryKeys } from "./query-keys";

const api = vi.hoisted(() => ({
  createWorkingDraft: vi.fn(),
  freezeWorkingDraft: vi.fn(),
  getAuthoringOverview: vi.fn(),
  getDocumentDraft: vi.fn(),
  getWorkingDraft: vi.fn(),
  publishArticleRevision: vi.fn(),
  updateWorkingDraft: vi.fn(),
}));
const activeWorkspace = vi.hoisted(() => ({ id: "b1000000-0000-4000-8000-000000000001" }));

vi.mock("../../api/authoring", () => api);
vi.mock("../../app/active-workspace", () => ({
  getActiveWorkspaceId: () => activeWorkspace.id,
  useActiveWorkspaceId: () => activeWorkspace.id,
}));

import { useAuthoringOverview, useUpdateWorkingDraft, useWorkingDraft } from "./queries";

const workspaceId = "b1000000-0000-4000-8000-000000000001";
const otherWorkspaceId = "b1000000-0000-4000-8000-000000000002";
const draftId = "b1000000-0000-4000-8000-000000000003";

const draft = (version: number): WorkingDraft => ({
  id: draftId,
  workspaceId,
  documentId: null,
  title: "Java AI",
  targetPath: "notes/java-ai.md",
  body: version === 1 ? "# Java AI\n" : "# Java AI\n\nCAS\n",
  status: "EDITING",
  version,
  createdAt: "2026-08-03T08:00:00Z",
  updatedAt: `2026-08-03T08:0${String(version)}:00Z`,
});

const wrapperFor = (queryClient: QueryClient) => ({ children }: PropsWithChildren) => (
  <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
);

afterEach(() => {
  activeWorkspace.id = workspaceId;
  for (const mock of Object.values(api)) mock.mockReset();
});

describe("Authoring Query ownership", () => {
  it("keeps overview, Working Draft and Document under one Workspace root", () => {
    expect(authoringQueryKeys.overview(workspaceId)).toEqual(["authoring", workspaceId, "overview"]);
    expect(authoringQueryKeys.draft(workspaceId, draftId)).toEqual(["authoring", workspaceId, "working-draft", draftId]);
    expect(authoringQueryKeys.document(workspaceId, "b1000000-0000-4000-8000-000000000004")).toEqual([
      "authoring", workspaceId, "document", "b1000000-0000-4000-8000-000000000004",
    ]);
  });

  it("reads overview and draft with the active Workspace and cancellation signal", async () => {
    api.getAuthoringOverview.mockResolvedValue({
      workspaceId,
      organizing: { available: false, reason: "未接入", href: null },
      recentDrafts: [],
      pendingPublications: [],
      completedDocuments: [],
    });
    api.getWorkingDraft.mockResolvedValue(draft(1));
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const overview = renderHook(() => useAuthoringOverview(), { wrapper: wrapperFor(queryClient) });
    const detail = renderHook(() => useWorkingDraft(draftId), { wrapper: wrapperFor(queryClient) });

    await waitFor(() => expect(overview.result.current.isSuccess && detail.result.current.isSuccess).toBe(true));
    expect(api.getAuthoringOverview).toHaveBeenCalledWith(workspaceId, expect.any(AbortSignal));
    expect(api.getWorkingDraft).toHaveBeenCalledWith(workspaceId, draftId, expect.any(AbortSignal));
  });

  it("stores a confirmed autosave only inside its Workspace-bound detail cache", async () => {
    const result: WorkingDraftCommandResult = { workingDraft: draft(2), replayed: false };
    api.updateWorkingDraft.mockResolvedValue(result);
    const queryClient = new QueryClient({ defaultOptions: { mutations: { retry: false } } });
    const view = renderHook(() => useUpdateWorkingDraft(), { wrapper: wrapperFor(queryClient) });
    const input = {
      workspaceId,
      draftId,
      expectedVersion: 1,
      title: "Java AI",
      targetPath: "notes/java-ai.md",
      body: "# Java AI\n\nCAS\n",
      idempotencyKey: "update-1",
    };

    act(() => view.result.current.mutate(input));
    await waitFor(() => expect(view.result.current.isSuccess).toBe(true));

    expect(api.updateWorkingDraft).toHaveBeenCalledWith(input);
    expect(queryClient.getQueryData<WorkingDraft>(authoringQueryKeys.draft(workspaceId, draftId))).toEqual(result.workingDraft);
    expect(queryClient.getQueryData(authoringQueryKeys.draft(otherWorkspaceId, draftId))).toBeUndefined();
  });

  it("drops a late mutation result after the active Workspace changes", async () => {
    let resolveUpdate: ((result: WorkingDraftCommandResult) => void) | undefined;
    api.updateWorkingDraft.mockReturnValue(new Promise<WorkingDraftCommandResult>((resolve) => {
      resolveUpdate = resolve;
    }));
    const queryClient = new QueryClient({ defaultOptions: { mutations: { retry: false } } });
    const view = renderHook(() => useUpdateWorkingDraft(), { wrapper: wrapperFor(queryClient) });

    act(() => view.result.current.mutate({
      workspaceId,
      draftId,
      expectedVersion: 1,
      title: "Java AI",
      targetPath: "notes/java-ai.md",
      body: "# Java AI\n\nCAS\n",
      idempotencyKey: "update-late",
    }));
    activeWorkspace.id = otherWorkspaceId;
    act(() => {
      resolveUpdate?.({ workingDraft: draft(2), replayed: false });
    });
    await waitFor(() => expect(view.result.current.isSuccess).toBe(true));

    expect(queryClient.getQueryData(authoringQueryKeys.draft(workspaceId, draftId))).toBeUndefined();
    expect(queryClient.getQueryData(authoringQueryKeys.draft(otherWorkspaceId, draftId))).toBeUndefined();
  });
});
