import type { CaptureListParams } from "../../api/captures";

const canonicalListParams = (params: CaptureListParams) => ({
  kind: params.kind ?? null,
  status: params.status ?? null,
  limit: params.limit ?? 30,
  cursor: params.cursor ?? null,
});

export const captureQueryKeys = {
  all: (workspaceId: string) => ["captures", workspaceId] as const,
  lists: (workspaceId: string) => [...captureQueryKeys.all(workspaceId), "list"] as const,
  list: (workspaceId: string, params: CaptureListParams = {}) => [
    ...captureQueryKeys.lists(workspaceId),
    canonicalListParams(params),
  ] as const,
  details: (workspaceId: string) => [...captureQueryKeys.all(workspaceId), "detail"] as const,
  detail: (workspaceId: string, captureId: string) => [
    ...captureQueryKeys.details(workspaceId),
    captureId,
  ] as const,
  profiles: (workspaceId: string) => [...captureQueryKeys.all(workspaceId), "profile"] as const,
  profile: (workspaceId: string, sourceVersionId: string) => [
    ...captureQueryKeys.profiles(workspaceId),
    sourceVersionId,
  ] as const,
};
