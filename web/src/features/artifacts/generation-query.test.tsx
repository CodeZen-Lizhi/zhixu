import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook } from "@testing-library/react";
import type { PropsWithChildren } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";

import type { ArtifactSectionGenerationPage, GenerateArtifactSectionInput, PlanArtifactInput, RevisionCommandInput } from "../../api/artifacts";
import { artifactQueryKeys } from "./query-keys";
import { useGenerateArtifactSection, usePlanArtifact, useStartArtifactRevision } from "./queries";

const workspaceId = "12000000-0000-4000-8000-000000000001";
const artifactId = "12000000-0000-4000-8000-000000000002";
const revisionId = "12000000-0000-4000-8000-000000000003";
const generationId = "12000000-0000-4000-8000-000000000007";
const workflowRunId = "12000000-0000-4000-8000-000000000008";
const nodeRunId = "12000000-0000-4000-8000-000000000009";
const at = "2026-07-26T00:00:00Z";
const input: GenerateArtifactSectionInput = { workspaceId, artifactId, expectedVersion: 4, sectionKey: "gap", idempotencyKey: "artifact-generation-query-test" };
const wireAcceptance = (status: "PENDING" | "COMPLETED") => ({ generation_id: generationId, workspace_id: workspaceId, artifact_id: artifactId, source_revision_id: revisionId, source_revision_no: 4, source_artifact_version: 4, section_key: "gap", workflow_run_id: workflowRunId, node_run_id: nodeRunId, status, version: 1, created_at: at, updated_at: at, replayed: true, status_url: `/api/v1/workflows/${workflowRunId}` });
const emptyPage = (): ArtifactSectionGenerationPage => ({ workspaceId, artifactId, items: [] });
const jsonResponse = (payload: unknown, status = 202): Response => new Response(JSON.stringify(payload), { status, headers: { "Content-Type": "application/json" } });
const planInput: PlanArtifactInput = { workspaceId, type: "knowledge-note", title: "Artifact command mutation", scopeDefinition: "Validate mutation context isolation.", idempotencyKey: "artifact-command-mutation-test" };
const planResponse = () => ({
  artifact: {
    id: artifactId, workspace_id: workspaceId, type: planInput.type, title: planInput.title, status: "PLANNING", scope_definition: planInput.scopeDefinition,
    source_coverage: [], current_revision_id: revisionId, version: 1, created_at: at, updated_at: at,
    revision: { id: revisionId, artifact_id: artifactId, revision_no: 1, outline: [], sections: [], created_by: "HUMAN", content_hash: "a".repeat(64), created_at: at },
  },
  replayed: false,
});

const harness = () => {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  const wrapper = ({ children }: PropsWithChildren) => <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>;
  return { queryClient, wrapper };
};

afterEach(() => vi.unstubAllGlobals());

describe("Artifact generation mutation cache", () => {
  it("does not pass TanStack MutationFunctionContext as the API AbortSignal", async () => {
    const fetchMock = vi.fn<typeof fetch>().mockResolvedValue(new Response(JSON.stringify(planResponse()), { status: 201, headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", fetchMock);
    const { wrapper } = harness();
    const { result } = renderHook(() => usePlanArtifact(), { wrapper });

    await act(async () => { await result.current.mutateAsync(planInput); });

    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(fetchMock.mock.calls[0]?.[1]).not.toHaveProperty("signal");
  });

  it("starts a revision through the strict route and invalidates every Artifact projection", async () => {
    const response = planResponse();
    response.artifact.status = "GENERATING";
    response.artifact.version = 2;
    const fetchMock = vi.fn<typeof fetch>().mockResolvedValue(jsonResponse(response, 200));
    vi.stubGlobal("fetch", fetchMock);
    const { queryClient, wrapper } = harness();
    const listKey = artifactQueryKeys.list(workspaceId, undefined);
    const detailKey = artifactQueryKeys.detail(workspaceId, artifactId);
    const generationKey = artifactQueryKeys.sectionGenerations(workspaceId, artifactId);
    queryClient.setQueryData(listKey, { items: [] });
    queryClient.setQueryData(detailKey, { id: artifactId });
    queryClient.setQueryData(generationKey, emptyPage());
    const { result } = renderHook(() => useStartArtifactRevision(), { wrapper });
    const command: RevisionCommandInput = { workspaceId, artifactId, expectedVersion: 1, idempotencyKey: "artifact-start-revision-test" };

    await act(async () => { await result.current.mutateAsync(command); });

    expect(fetchMock.mock.calls[0]?.[0]).toBe(`/api/v1/artifacts/${artifactId}/revisions`);
    const body = fetchMock.mock.calls[0]?.[1]?.body;
    if (typeof body !== "string") throw new Error("missing start revision body");
    expect(JSON.parse(body)).toEqual({ workspace_id: workspaceId, expected_version: 1 });
    expect(queryClient.getQueryState(listKey)?.isInvalidated).toBe(true);
    expect(queryClient.getQueryState(detailKey)?.isInvalidated).toBe(true);
    expect(queryClient.getQueryState(generationKey)?.isInvalidated).toBe(true);
  });

  it("updates and invalidates the durable generation query without persisting replayed", async () => {
    vi.stubGlobal("fetch", vi.fn<typeof fetch>().mockResolvedValue(jsonResponse(wireAcceptance("PENDING"))));
    const { queryClient, wrapper } = harness();
    const key = artifactQueryKeys.sectionGenerations(workspaceId, artifactId);
    queryClient.setQueryData(key, emptyPage());
    const { result } = renderHook(() => useGenerateArtifactSection(), { wrapper });

    await act(async () => { await result.current.mutateAsync(input); });

    const page = queryClient.getQueryData<ArtifactSectionGenerationPage>(key);
    expect(page?.items).toMatchObject([{ generationId, sectionKey: "gap", status: "PENDING" }]);
    expect(page?.items[0]).not.toHaveProperty("replayed");
    expect(queryClient.getQueryState(key)?.isInvalidated).toBe(true);
  });

  it("removes a completed item and invalidates Artifact detail", async () => {
    vi.stubGlobal("fetch", vi.fn<typeof fetch>().mockResolvedValue(jsonResponse(wireAcceptance("COMPLETED"))));
    const { queryClient, wrapper } = harness();
    const generationKey = artifactQueryKeys.sectionGenerations(workspaceId, artifactId);
    const detailKey = artifactQueryKeys.detail(workspaceId, artifactId);
    queryClient.setQueryData(generationKey, { ...emptyPage(), items: [{ generationId, workspaceId, artifactId, sourceRevisionId: revisionId, sourceRevisionNo: 4, sourceArtifactVersion: 4, sectionKey: "gap", workflowRunId, nodeRunId, status: "PENDING", version: 1, createdAt: at, updatedAt: at, statusUrl: `/api/v1/workflows/${workflowRunId}` }] });
    queryClient.setQueryData(detailKey, { id: artifactId });
    const { result } = renderHook(() => useGenerateArtifactSection(), { wrapper });

    await act(async () => { await result.current.mutateAsync(input); });

    expect(queryClient.getQueryData<ArtifactSectionGenerationPage>(generationKey)?.items).toEqual([]);
    expect(queryClient.getQueryState(detailKey)?.isInvalidated).toBe(true);
  });
});
