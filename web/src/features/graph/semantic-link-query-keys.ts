import type {
  ListSemanticLinkCandidatesInput,
  SemanticLinkCandidateStatus,
  RelationType,
  SemanticLinkReopenedReason,
} from "../../api/semantic-links";

type CandidateQueryInput = Omit<ListSemanticLinkCandidatesInput, "cursor">;

const sortedUnique = <T extends string>(values: readonly T[] | undefined): T[] =>
  values === undefined ? [] : [...new Set(values)].sort();

const canonicalCandidateInput = (input: CandidateQueryInput) => ({
  node: input.nodeType === undefined || input.nodeId === undefined
    ? null
    : { type: input.nodeType, id: input.nodeId },
  statuses: sortedUnique<SemanticLinkCandidateStatus>(input.statuses),
  relationTypes: sortedUnique<RelationType>(input.relationTypes),
  reopenedReasons: sortedUnique<SemanticLinkReopenedReason>(input.reopenedReasons),
  minConfidence: input.minConfidence === undefined || Object.is(input.minConfidence, -0) || input.minConfidence === 0
    ? null
    : input.minConfidence,
  limit: input.limit ?? 20,
});

/** Candidate server-state keys are intentionally separate from formal Graph keys. */
export const semanticLinkQueryKeys = {
  all: (workspaceId: string) => ["semantic-links", workspaceId] as const,
  candidateFamily: (workspaceId: string) => [...semanticLinkQueryKeys.all(workspaceId), "candidates"] as const,
  candidates: (input: CandidateQueryInput) => [
    ...semanticLinkQueryKeys.candidateFamily(input.workspaceId),
    canonicalCandidateInput(input),
  ] as const,
  scanFamily: (workspaceId: string) => [...semanticLinkQueryKeys.all(workspaceId), "scans"] as const,
  scan: (workspaceId: string, scanId: string) => [...semanticLinkQueryKeys.scanFamily(workspaceId), scanId] as const,
  proposals: (workspaceId: string, proposalId: string) => [
    ...semanticLinkQueryKeys.all(workspaceId),
    "proposals",
    proposalId,
  ] as const,
};

export type { CandidateQueryInput };
