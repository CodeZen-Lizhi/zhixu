import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import type { PropsWithChildren } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";

import type { OrganizingCommandResult, OrganizingDraft, OrganizingSnapshot, OrganizingTemplate } from "../../api/organizing";
import { organizingQueryKeys } from "./query-keys";

const api = vi.hoisted(() => ({
  addOrganizingMaterial: vi.fn(),
  cloneOrganizingTemplate: vi.fn(),
  confirmOrganizingDraft: vi.fn(),
  createOrganizingDraft: vi.fn(),
  getOrganizingDraft: vi.fn(),
  getOrganizingRun: vi.fn(),
  getOrganizingSnapshot: vi.fn(),
  listOrganizingTemplates: vi.fn(),
  removeOrganizingMaterial: vi.fn(),
  setOrganizingMaterialSelection: vi.fn(),
  reviseOrganizingTemplate: vi.fn(),
  searchOrganizingMaterials: vi.fn(),
  suggestOrganizingMaterials: vi.fn(),
  updateOrganizingDraft: vi.fn(),
}));
const activeWorkspace = vi.hoisted(() => ({ id: "b1000000-0000-4000-8000-000000000001" }));

vi.mock("../../api/organizing", () => api);
vi.mock("../../app/active-workspace", () => ({
  getActiveWorkspaceId: () => activeWorkspace.id,
  useActiveWorkspaceId: () => activeWorkspace.id,
}));

import { useCloneOrganizingTemplate, useConfirmOrganizingDraft, useOrganizingDraft, useOrganizingMaterialSearch, useOrganizingTemplates, useReviseOrganizingTemplate, useUpdateOrganizingDraft } from "./queries";

const workspaceId = "b1000000-0000-4000-8000-000000000001";
const otherWorkspaceId = "b1000000-0000-4000-8000-000000000002";
const draftId = "b1000000-0000-4000-8000-000000000003";
const snapshotId = "b1000000-0000-4000-8000-000000000004";

const draft = (version: number): OrganizingDraft => ({
  id: draftId,
  workspaceId,
  intent: "整理恢复契约",
  status: "EDITING",
  templateRevisionId: "b1000000-0000-4000-8000-000000000005",
  confirmedSnapshotId: null,
  version,
  materials: [],
  createdAt: "2026-08-03T08:00:00Z",
  updatedAt: `2026-08-03T08:0${String(version)}:00Z`,
});

const snapshot: OrganizingSnapshot = {
  id: snapshotId,
  workspaceId,
  draftId,
  draftVersion: 2,
  templateId: "b1000000-0000-4000-8000-000000000006",
  templateRevisionId: "b1000000-0000-4000-8000-000000000005",
  templateHash: "a".repeat(64),
  intent: "整理恢复契约",
  canonicalHash: "b".repeat(64),
  materials: [{ position: 0, reference: { kind: "DOCUMENT_REVISION", documentId: "b1000000-0000-4000-8000-000000000007", articleRevisionId: "b1000000-0000-4000-8000-000000000008", revisionNo: 1, contentHash: "c".repeat(64), originCollectionId: null }, evidence: [] }],
  createdAt: "2026-08-03T08:03:00Z",
};

const customTemplate = (version: number): OrganizingTemplate => ({
  id: "b1000000-0000-4000-8000-000000000009",
  workspaceId,
  key: "recovery-custom",
  name: "恢复自定义模板",
  description: "用于验证 Workspace 模板缓存。",
  builtIn: false,
  kind: "TOPIC_ARTICLE",
  currentRevisionId: "b1000000-0000-4000-8000-000000000010",
  version,
  currentRevision: {
    id: "b1000000-0000-4000-8000-000000000010",
    templateId: "b1000000-0000-4000-8000-000000000009",
    workspaceId,
    revisionNo: version,
    kind: "TOPIC_ARTICLE",
    schemaVersion: "organizing-template/v1",
    canonicalHash: "a".repeat(64),
    declaration: {
      schemaVersion: "organizing-template/v1",
      kind: "TOPIC_ARTICLE",
      name: "恢复自定义模板",
      description: "用于验证 Workspace 模板缓存。",
      materials: { allowedKinds: ["SOURCE_VERSION"], minMaterials: 1, maxMaterials: 10 },
      sections: [
        { key: "overview", title: "概览", required: true },
        { key: "conflicts", title: "冲突", required: true },
        { key: "gaps", title: "知识缺口", required: true },
        { key: "sources", title: "来源", required: true },
      ],
      presentation: { audience: "工程师", language: "zh-CN", tone: "严谨", length: "MEDIUM", includeCode: false, includeExamples: false, includeFaq: false },
      output: { directory: "articles", filenamePattern: "{slug}.md" },
      additionalInstructions: "",
    },
    createdAt: "2026-08-03T08:00:00Z",
  },
  createdAt: "2026-08-03T08:00:00Z",
  updatedAt: "2026-08-03T08:00:00Z",
});

const wrapperFor = (queryClient: QueryClient) => ({ children }: PropsWithChildren) => <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>;

afterEach(() => {
  activeWorkspace.id = workspaceId;
  for (const mock of Object.values(api)) mock.mockReset();
});

describe("Organizing Query ownership", () => {
  it("binds every resource family to Workspace identity", () => {
    expect(organizingQueryKeys.draft(workspaceId, draftId)).toEqual(["organizing", workspaceId, "draft", draftId]);
    expect(organizingQueryKeys.snapshot(workspaceId, snapshotId)).toEqual(["organizing", workspaceId, "snapshot", snapshotId]);
    expect(organizingQueryKeys.templates(workspaceId)).toEqual(["organizing", workspaceId, "template"]);
    expect(organizingQueryKeys.materialSearch(workspaceId, "恢复", "CLAIM")).toEqual(["organizing", workspaceId, "material-search", "恢复", "CLAIM"]);
    expect(organizingQueryKeys.run(workspaceId, snapshotId)).toEqual(["organizing", workspaceId, "run", snapshotId]);
  });

  it("reads Draft and Template list with active Workspace cancellation", async () => {
    api.getOrganizingDraft.mockResolvedValue(draft(1));
    api.listOrganizingTemplates.mockResolvedValue([]);
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const detail = renderHook(() => useOrganizingDraft(draftId), { wrapper: wrapperFor(queryClient) });
    const templates = renderHook(() => useOrganizingTemplates(), { wrapper: wrapperFor(queryClient) });

    await waitFor(() => expect(detail.result.current.isSuccess && templates.result.current.isSuccess).toBe(true));
    expect(api.getOrganizingDraft).toHaveBeenCalledWith(workspaceId, draftId, expect.any(AbortSignal));
    expect(api.listOrganizingTemplates).toHaveBeenCalledWith(workspaceId, expect.any(AbortSignal));
  });

  it("searches materials with a Workspace-bound key and cancellation", async () => {
    api.searchOrganizingMaterials.mockResolvedValue([]);
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const view = renderHook(() => useOrganizingMaterialSearch("恢复", "CLAIM", true), { wrapper: wrapperFor(queryClient) });

    await waitFor(() => expect(view.result.current.isSuccess).toBe(true));
    expect(api.searchOrganizingMaterials).toHaveBeenCalledWith(expect.objectContaining({ workspaceId, query: "恢复", kind: "CLAIM", limit: 25 }));
  });

  it("stores a mutation only in the matching Workspace Draft cache", async () => {
    const result: OrganizingCommandResult = { draft: draft(2), replayed: false };
    api.updateOrganizingDraft.mockResolvedValue(result);
    const queryClient = new QueryClient({ defaultOptions: { mutations: { retry: false } } });
    const view = renderHook(() => useUpdateOrganizingDraft(), { wrapper: wrapperFor(queryClient) });
    const input = { workspaceId, draftId, expectedVersion: 1, intent: "整理恢复契约", templateRevisionId: draft(1).templateRevisionId ?? "", idempotencyKey: "organizing-update-1" };

    act(() => view.result.current.mutate(input));
    await waitFor(() => expect(view.result.current.isSuccess).toBe(true));

    expect(queryClient.getQueryData(organizingQueryKeys.draft(workspaceId, draftId))).toEqual(result.draft);
    expect(queryClient.getQueryData(organizingQueryKeys.draft(otherWorkspaceId, draftId))).toBeUndefined();
  });

  it("updates custom-template cache only for the mutation Workspace", async () => {
    const first = customTemplate(1);
    const revised = customTemplate(2);
    api.cloneOrganizingTemplate.mockResolvedValue({ template: first, replayed: false });
    api.reviseOrganizingTemplate.mockResolvedValue({ template: revised, replayed: false });
    const queryClient = new QueryClient({ defaultOptions: { mutations: { retry: false } } });
    queryClient.setQueryData(organizingQueryKeys.templates(workspaceId), []);
    queryClient.setQueryData(organizingQueryKeys.templates(otherWorkspaceId), []);
    const clone = renderHook(() => useCloneOrganizingTemplate(), { wrapper: wrapperFor(queryClient) });
    const revise = renderHook(() => useReviseOrganizingTemplate(), { wrapper: wrapperFor(queryClient) });

    act(() => clone.result.current.mutate({ workspaceId, templateId: first.id, name: first.name, idempotencyKey: "organizing-template-clone-1" }));
    await waitFor(() => expect(clone.result.current.isSuccess).toBe(true));
    act(() => revise.result.current.mutate({ workspaceId, templateId: first.id, expectedVersion: 1, declaration: first.currentRevision.declaration, idempotencyKey: "organizing-template-revise-1" }));
    await waitFor(() => expect(revise.result.current.isSuccess).toBe(true));

    expect(queryClient.getQueryData(organizingQueryKeys.templates(workspaceId))).toEqual([revised]);
    expect(queryClient.getQueryData(organizingQueryKeys.templates(otherWorkspaceId))).toEqual([]);
  });

  it("invalidates the active server Draft after a failed CAS command", async () => {
    api.updateOrganizingDraft.mockRejectedValue(new Error("version conflict"));
    const queryClient = new QueryClient({ defaultOptions: { mutations: { retry: false } } });
    const invalidate = vi.spyOn(queryClient, "invalidateQueries");
    const view = renderHook(() => useUpdateOrganizingDraft(), { wrapper: wrapperFor(queryClient) });

    act(() => view.result.current.mutate({ workspaceId, draftId, expectedVersion: 1, intent: "整理恢复契约", templateRevisionId: draft(1).templateRevisionId ?? "", idempotencyKey: "organizing-update-conflict" }));
    await waitFor(() => expect(view.result.current.isError).toBe(true));

    expect(invalidate).toHaveBeenCalledWith({ queryKey: organizingQueryKeys.draft(workspaceId, draftId), refetchType: "active" });
  });

  it("stores confirmed Snapshot and ignores late old-Workspace results", async () => {
    api.confirmOrganizingDraft.mockResolvedValue({ draft: { ...draft(3), status: "CONFIRMED", confirmedSnapshotId: snapshot.id }, snapshot, dispatchStatus: "PENDING", replayed: false });
    const queryClient = new QueryClient({ defaultOptions: { mutations: { retry: false } } });
    const view = renderHook(() => useConfirmOrganizingDraft(), { wrapper: wrapperFor(queryClient) });
    const input = { workspaceId, draftId, expectedVersion: 2, templateRevisionId: snapshot.templateRevisionId, idempotencyKey: "organizing-confirm-1" };

    activeWorkspace.id = otherWorkspaceId;
    act(() => view.result.current.mutate(input));
    await waitFor(() => expect(view.result.current.isSuccess).toBe(true));

    expect(queryClient.getQueryData(organizingQueryKeys.snapshot(workspaceId, snapshotId))).toBeUndefined();
    expect(queryClient.getQueryData(organizingQueryKeys.snapshot(otherWorkspaceId, snapshotId))).toBeUndefined();
  });
});
