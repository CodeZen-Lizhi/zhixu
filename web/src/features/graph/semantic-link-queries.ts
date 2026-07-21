import { type QueryClient, useInfiniteQuery, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import {
  SemanticLinkApiError,
  approveSemanticLinkProposal,
  decideSemanticLinkCandidate,
  getSemanticLinkProposal,
  getSemanticLinkScan,
  listSemanticLinkCandidates,
  startSemanticLinkScan,
  type ApproveProposalInput,
  type GetSemanticLinkScanInput,
  type ListSemanticLinkCandidatesInput,
  type SemanticLinkCandidateDecisionInput,
  type StartSemanticLinkScanInput,
} from "../../api/semantic-links";
import { semanticLinkQueryKeys, type CandidateQueryInput } from "./semantic-link-query-keys";

/** 清理 Workspace 切换后不再可见的候选、扫描和 Proposal 查询缓存。 */
export const clearSemanticLinkWorkspaceQueries = (queryClient: QueryClient, workspaceId: string): void => {
  if (workspaceId === "") return;
  const queryKey = semanticLinkQueryKeys.all(workspaceId);
  void queryClient.cancelQueries({ queryKey });
  queryClient.removeQueries({ queryKey });
};

/** 生成一次逻辑决策使用的幂等键；重试同一 mutation 时必须复用该值。 */
export const createSemanticLinkIdempotencyKey = (scope = "candidate-decision"): string =>
  `${scope}:${crypto.randomUUID()}`;

export const useSemanticLinkCandidates = (input: CandidateQueryInput, enabled = true) => {
  const queryClient = useQueryClient();
  const queryKey = semanticLinkQueryKeys.candidates(input);
  const query = useInfiniteQuery({
    queryKey,
    queryFn: ({ signal, pageParam }) => listSemanticLinkCandidates({
      ...input,
      ...(pageParam === undefined ? {} : { cursor: pageParam }),
    }, signal),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (page) => page.nextCursor,
    enabled: enabled && input.workspaceId !== "",
    retry: false,
  });
  return {
    ...query,
    resetToFirstPage: () => queryClient.resetQueries({ queryKey, exact: true }),
  };
};

export const useDecideSemanticLinkCandidate = () => {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (input: SemanticLinkCandidateDecisionInput) => decideSemanticLinkCandidate(input),
    retry: false,
    onSuccess: (receipt, input) => {
      void queryClient.invalidateQueries({ queryKey: semanticLinkQueryKeys.candidateFamily(input.workspaceId) });
      if (receipt.proposalId !== null) {
        void queryClient.invalidateQueries({ queryKey: semanticLinkQueryKeys.proposals(input.workspaceId, receipt.proposalId) });
      }
    },
  });
};

export const useStartSemanticLinkScan = () => useMutation({
  mutationFn: (input: StartSemanticLinkScanInput) => startSemanticLinkScan(input),
  retry: false,
});

export const useSemanticLinkScan = (input: GetSemanticLinkScanInput, enabled = true) => useQuery({
  queryKey: semanticLinkQueryKeys.scan(input.workspaceId, input.scanId),
  queryFn: ({ signal }) => getSemanticLinkScan(input, signal),
  enabled: enabled && input.workspaceId !== "" && input.scanId !== "",
  retry: false,
  refetchInterval: (query) => {
    const status = query.state.data?.status;
    return status === "PENDING" || status === "RUNNING" ? 2_000 : false;
  },
});

export const useSemanticLinkProposal = (workspaceId: string, proposalId: string, enabled = true) => useQuery({
  queryKey: semanticLinkQueryKeys.proposals(workspaceId, proposalId),
  queryFn: async ({ signal }) => {
    const proposal = await getSemanticLinkProposal({ proposalId }, signal);
    if (proposal.workspaceId !== workspaceId) throw new SemanticLinkApiError({
      errorCode: "INVALID_RESPONSE",
      message: "Semantic Link Proposal 与当前 Workspace 不一致。",
      retryable: false,
    });
    return proposal;
  },
  enabled: enabled && workspaceId !== "" && proposalId !== "",
  retry: false,
});

export const useApproveSemanticLinkProposal = (workspaceId: string) => {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (input: ApproveProposalInput) => approveSemanticLinkProposal(input),
    retry: false,
    onSuccess: (_approval, input) => {
      void queryClient.invalidateQueries({ queryKey: semanticLinkQueryKeys.proposals(workspaceId, input.proposalId) });
    },
  });
};

export type { CandidateQueryInput, ListSemanticLinkCandidatesInput };
