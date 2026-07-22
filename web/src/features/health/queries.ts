import { useMutation, useQuery, useQueryClient, type QueryClient } from "@tanstack/react-query";
import { useActiveWorkspaceId } from "../../app/active-workspace";
import { createHealthRepairProposal, decideHealthIssue, getHealthIssue, getHealthScan, getHealthSummary, listHealthIssues, startHealthScan, type CreateHealthRepairProposalInput, type DecideHealthIssueInput, type HealthIssueDetail, type ListHealthIssuesInput, type StartHealthScanInput } from "../../api/health";
import { healthQueryKeys } from "./query-keys";

const stable = (value: unknown): unknown => {
  if (Array.isArray(value)) return value.map(stable);
  if (value !== null && typeof value === "object") {
    return Object.fromEntries(Object.entries(value).filter(([, item]) => item !== undefined).sort(([left], [right]) => left.localeCompare(right)).map(([key, item]) => [key, stable(item)]));
  }
  return value;
};
const normalizeHealthRequest = (value: unknown): unknown => {
  if (value === null || typeof value !== "object" || Array.isArray(value)) return value;
  const record = { ...(value as Record<string, unknown>) };
  for (const key of ["statuses", "severities", "types"] as const) {
    if (Array.isArray(record[key])) record[key] = [...new Set(record[key] as string[])].sort();
  }
  return record;
};
export const canonicalHealthRequest = (value: unknown): string => JSON.stringify(stable(normalizeHealthRequest(value)));
const normalizeIssueRequest = (input: Omit<ListHealthIssuesInput, "workspaceId">): Omit<ListHealthIssuesInput, "workspaceId"> => ({
  ...input,
  ...(input.statuses === undefined ? {} : { statuses: [...new Set(input.statuses)].sort() }),
  ...(input.severities === undefined ? {} : { severities: [...new Set(input.severities)].sort() }),
  ...(input.types === undefined ? {} : { types: [...new Set(input.types)].sort() }),
});
export const clearHealthWorkspaceQueries = (queryClient: QueryClient, workspaceId: string): void => { if (workspaceId === "") return; const queryKey = healthQueryKeys.all(workspaceId); void queryClient.cancelQueries({ queryKey }); queryClient.removeQueries({ queryKey }); };
export const useHealthSummary = () => { const workspaceId = useActiveWorkspaceId(); return useQuery({ queryKey: healthQueryKeys.summary(workspaceId), queryFn: ({ signal }) => getHealthSummary(workspaceId, signal), enabled: workspaceId !== "", retry: false }); };
export const useHealthIssues = (input: Omit<ListHealthIssuesInput, "workspaceId">) => { const workspaceId = useActiveWorkspaceId(); const request = { ...normalizeIssueRequest(input), workspaceId }; const requestKey = canonicalHealthRequest(request); return useQuery({ queryKey: healthQueryKeys.issues(workspaceId, requestKey), queryFn: ({ signal }) => listHealthIssues(request, signal), enabled: workspaceId !== "", retry: false }); };
export const clearHealthIssueDetail = (queryClient: QueryClient, workspaceId: string, issueId: string): void => { if (workspaceId !== "" && issueId !== "") queryClient.removeQueries({ queryKey: healthQueryKeys.issue(workspaceId, issueId) }); };
export const useClearHealthIssueDetail = () => { const workspaceId = useActiveWorkspaceId(); const queryClient = useQueryClient(); return (issueId: string) => clearHealthIssueDetail(queryClient, workspaceId, issueId); };
export const useHealthIssue = (issueId: string) => { const workspaceId = useActiveWorkspaceId(); return useQuery({ queryKey: healthQueryKeys.issue(workspaceId, issueId), queryFn: ({ signal }) => getHealthIssue(workspaceId, issueId, signal), enabled: workspaceId !== "" && issueId !== "", retry: false }); };
export const useHealthScan = (scanId: string) => { const workspaceId = useActiveWorkspaceId(); return useQuery({ queryKey: healthQueryKeys.scan(workspaceId, scanId), queryFn: ({ signal }) => getHealthScan(workspaceId, scanId, signal), enabled: workspaceId !== "" && scanId !== "", retry: false, refetchInterval: (query) => query.state.data?.status === "PENDING" || query.state.data?.status === "RUNNING" ? 2000 : false }); };
export const useStartHealthScan = () => { const workspaceId = useActiveWorkspaceId(); const queryClient = useQueryClient(); return useMutation({ mutationFn: (input: Omit<StartHealthScanInput, "workspaceId">) => startHealthScan({ ...input, workspaceId }), onSuccess: (result) => { void queryClient.invalidateQueries({ queryKey: healthQueryKeys.summary(workspaceId) }); queryClient.setQueryData(healthQueryKeys.scan(workspaceId, result.healthScanId), result.scan); } }); };
export const useDecideHealthIssue = () => { const workspaceId = useActiveWorkspaceId(); const queryClient = useQueryClient(); return useMutation({ mutationFn: (input: Omit<DecideHealthIssueInput, "workspaceId">) => decideHealthIssue({ ...input, workspaceId }), onSuccess: (issue) => { queryClient.setQueryData<HealthIssueDetail>(healthQueryKeys.issue(workspaceId, issue.id), (current) => current === undefined ? current : { ...current, issue }); void queryClient.invalidateQueries({ queryKey: healthQueryKeys.all(workspaceId) }); } }); };
export const useCreateHealthRepairProposal = () => { const workspaceId = useActiveWorkspaceId(); const queryClient = useQueryClient(); return useMutation({ mutationFn: (input: Omit<CreateHealthRepairProposalInput, "workspaceId">) => createHealthRepairProposal({ ...input, workspaceId }), onSettled: (_data, _error, variables) => { void queryClient.invalidateQueries({ queryKey: healthQueryKeys.all(workspaceId) }); void queryClient.invalidateQueries({ queryKey: healthQueryKeys.issue(workspaceId, variables.issueId) }); } }); };
