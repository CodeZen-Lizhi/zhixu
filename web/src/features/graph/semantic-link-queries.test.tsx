import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import type { PropsWithChildren } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { graphQueryKeys } from "./query-keys";
import { semanticLinkQueryKeys } from "./semantic-link-query-keys";
import {
  clearSemanticLinkWorkspaceQueries,
  useDecideSemanticLinkCandidate,
  useSemanticLinkCandidates,
} from "./semantic-link-queries";

const workspaceId = "92000000-0000-4000-8000-000000000001";
const otherWorkspaceId = "93000000-0000-4000-8000-000000000001";
const candidateId = "92000000-0000-4000-8000-000000000002";
const decisionId = "92000000-0000-4000-8000-000000000003";
const at = "2026-07-20T08:10:12.123456789Z";

const createHarness = () => {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  const wrapper = ({ children }: PropsWithChildren) => (
    <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
  );
  return { queryClient, wrapper };
};

const jsonResponse = (payload: unknown, status = 200): Response => new Response(JSON.stringify(payload), {
  status,
  headers: { "Content-Type": "application/json" },
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("semanticLinkQueryKeys", () => {
  it("按 Workspace 隔离并规范化无序候选过滤器", () => {
    const left = semanticLinkQueryKeys.candidates({
      workspaceId,
      statuses: ["DEFERRED", "ACTIVE"],
      relationTypes: ["SUPPORTS", "BELONGS_TO"],
      reopenedReasons: ["CONTENT_CHANGED"],
      minConfidence: 0,
    });
    const right = semanticLinkQueryKeys.candidates({
      workspaceId,
      statuses: ["ACTIVE", "DEFERRED"],
      relationTypes: ["BELONGS_TO", "SUPPORTS"],
      reopenedReasons: ["CONTENT_CHANGED"],
      limit: 20,
    });
    expect(left).toEqual(right);
    expect(semanticLinkQueryKeys.candidates({ workspaceId, minConfidence: 0.8 })).not.toEqual(left);
    expect(semanticLinkQueryKeys.candidates({ workspaceId })).not.toEqual(
      semanticLinkQueryKeys.candidates({ workspaceId: otherWorkspaceId }),
    );
  });
});

describe("Semantic Link query hooks", () => {
  it("按 opaque cursor 加载下一页且把 AbortSignal 传入客户端", async () => {
    const fetchMock = vi.fn<typeof fetch>()
      .mockResolvedValueOnce(jsonResponse({ workspace_id: workspaceId, items: [], next_cursor: "opaque+next" }))
      .mockResolvedValueOnce(jsonResponse({ workspace_id: workspaceId, items: [] }));
    vi.stubGlobal("fetch", fetchMock);
    const { wrapper } = createHarness();

    const { result } = renderHook(() => useSemanticLinkCandidates({ workspaceId, limit: 20 }), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    await act(async () => { await result.current.fetchNextPage(); });

    expect(fetchMock).toHaveBeenCalledTimes(2);
    expect(fetchMock.mock.calls[1]?.[0]).toContain("cursor=opaque%2Bnext");
    expect(fetchMock.mock.calls[0]?.[1]?.signal).toBeInstanceOf(AbortSignal);
  });

  it("决策成功只失效 Candidate/Proposal，不失效正式 Graph", async () => {
    const proposalId = "92000000-0000-4000-8000-000000000004";
    vi.stubGlobal("fetch", vi.fn<typeof fetch>().mockResolvedValue(jsonResponse({
      id: decisionId,
      candidate_id: candidateId,
      workspace_id: workspaceId,
      action: "CONFIRM",
      status: "PROPOSAL_CREATED",
      version: 2,
      proposal_id: proposalId,
      created_at: at,
      updated_at: at,
    }, 201)));
    const { queryClient, wrapper } = createHarness();
    const candidateKey = semanticLinkQueryKeys.candidates({ workspaceId, statuses: ["ACTIVE"] });
    const proposalKey = semanticLinkQueryKeys.proposals(workspaceId, proposalId);
    const graphKey = graphQueryKeys.global({ workspaceId, limit: 25 });
    queryClient.setQueryData(candidateKey, { pages: [], pageParams: [] });
    queryClient.setQueryData(proposalKey, { status: "ready_for_review" });
    queryClient.setQueryData(graphKey, { pages: [], pageParams: [] });
    const { result } = renderHook(() => useDecideSemanticLinkCandidate(), { wrapper });

    await act(async () => {
      await result.current.mutateAsync({
        candidateId,
        workspaceId,
        action: "CONFIRM",
        expectedVersion: 1,
        idempotencyKey: "decision-stable-key",
      });
    });

    expect(queryClient.getQueryState(candidateKey)?.isInvalidated).toBe(true);
    expect(queryClient.getQueryState(proposalKey)?.isInvalidated).toBe(true);
    expect(queryClient.getQueryState(graphKey)?.isInvalidated).toBe(false);
  });

  it("Workspace 清理不会删除其他 Workspace 或正式 Graph 缓存", () => {
    const { queryClient } = createHarness();
    const candidateKey = semanticLinkQueryKeys.candidates({ workspaceId });
    const otherKey = semanticLinkQueryKeys.candidates({ workspaceId: otherWorkspaceId });
    const graphKey = graphQueryKeys.global({ workspaceId, limit: 25 });
    queryClient.setQueryData(candidateKey, "candidate");
    queryClient.setQueryData(otherKey, "other-candidate");
    queryClient.setQueryData(graphKey, "graph");

    clearSemanticLinkWorkspaceQueries(queryClient, workspaceId);

    expect(queryClient.getQueryData(candidateKey)).toBeUndefined();
    expect(queryClient.getQueryData(otherKey)).toBe("other-candidate");
    expect(queryClient.getQueryData(graphKey)).toBe("graph");
  });
});
