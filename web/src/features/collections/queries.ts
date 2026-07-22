import { useMutation, useQuery, useQueryClient, type QueryClient } from "@tanstack/react-query";
import { useActiveWorkspaceId } from "../../app/active-workspace";
import { archiveCollection, createCollection, getCollection, getCollectionResults, listCollections, previewCollection, updateCollection, type CollectionArchiveInput, type CollectionDefinitionInput, type CollectionVersionedInput } from "../../api/collections";
import { collectionQueryKeys } from "./query-keys";

const stable = (value: unknown): unknown => {
  if (Array.isArray(value)) return value.map(stable);
  if (value !== null && typeof value === "object") {
    return Object.fromEntries(Object.entries(value).filter(([, item]) => item !== undefined).sort(([left], [right]) => left.localeCompare(right)).map(([key, item]) => [key, stable(item)]));
  }
  return value;
};

export const canonicalCollectionRequest = (value: unknown): string => JSON.stringify(stable(value));

export const clearCollectionWorkspaceQueries = (queryClient: QueryClient, workspaceId: string): void => {
  if (workspaceId === "") return;
  const queryKey = collectionQueryKeys.all(workspaceId);
  void queryClient.cancelQueries({ queryKey });
  queryClient.removeQueries({ queryKey });
};

export const useCollections = (request: { status?: ("ACTIVE" | "ARCHIVED")[]; cursor?: string; limit?: number } = {}) => {
  const workspaceId = useActiveWorkspaceId();
  const requestKey = canonicalCollectionRequest(request);
  return useQuery({ queryKey: collectionQueryKeys.list(workspaceId, requestKey), queryFn: ({ signal }) => listCollections(workspaceId, request, signal), enabled: workspaceId !== "", retry: false });
};

export const useCollection = (collectionId: string) => {
  const workspaceId = useActiveWorkspaceId();
  return useQuery({ queryKey: collectionQueryKeys.detail(workspaceId, collectionId), queryFn: ({ signal }) => getCollection(workspaceId, collectionId, signal), enabled: workspaceId !== "" && collectionId !== "", retry: false });
};

export const useCollectionResults = (collectionId: string, binding: { version: number; queryHash: string } | undefined, request: { cursor?: string; limit?: number }) => {
  const workspaceId = useActiveWorkspaceId();
  const requestKey = canonicalCollectionRequest(request);
  const version = binding?.version ?? 0;
  const queryHash = binding?.queryHash ?? "";
  return useQuery({ queryKey: collectionQueryKeys.results(workspaceId, collectionId, version, queryHash, requestKey), queryFn: ({ signal }) => binding === undefined ? Promise.reject(new Error("Collection result binding is unavailable")) : getCollectionResults(workspaceId, collectionId, binding.queryHash, request, signal), enabled: workspaceId !== "" && collectionId !== "" && binding !== undefined, retry: false });
};

export const useCollectionPreview = (input: CollectionDefinitionInput | null) => {
  const workspaceId = useActiveWorkspaceId();
  const requestKey = input === null ? "" : canonicalCollectionRequest(input);
  return useQuery({ queryKey: collectionQueryKeys.preview(workspaceId, requestKey), queryFn: ({ signal }) => input === null ? Promise.reject(new Error("Collection preview input is unavailable")) : previewCollection(input, signal), enabled: workspaceId !== "" && input !== null, retry: false });
};

export const useCreateCollection = () => {
  const workspaceId = useActiveWorkspaceId();
  const queryClient = useQueryClient();
  return useMutation({ mutationFn: ({ input, idempotencyKey }: { input: CollectionDefinitionInput; idempotencyKey: string }) => createCollection(input, idempotencyKey), onSuccess: () => { void queryClient.invalidateQueries({ queryKey: collectionQueryKeys.all(workspaceId) }); } });
};

export const useUpdateCollection = (collectionId: string) => {
  const workspaceId = useActiveWorkspaceId();
  const queryClient = useQueryClient();
  return useMutation({ mutationFn: ({ input, idempotencyKey }: { input: CollectionVersionedInput; idempotencyKey: string }) => updateCollection(collectionId, input, idempotencyKey), onSuccess: (collection) => { queryClient.setQueryData(collectionQueryKeys.detail(workspaceId, collection.id), collection); void queryClient.invalidateQueries({ queryKey: collectionQueryKeys.all(workspaceId) }); } });
};

export const useArchiveCollection = (collectionId: string) => {
  const workspaceId = useActiveWorkspaceId();
  const queryClient = useQueryClient();
  return useMutation({ mutationFn: ({ input, idempotencyKey }: { input: CollectionArchiveInput; idempotencyKey: string }) => archiveCollection(collectionId, input, idempotencyKey), onSuccess: (collection) => { queryClient.setQueryData(collectionQueryKeys.detail(workspaceId, collection.id), collection); void queryClient.invalidateQueries({ queryKey: collectionQueryKeys.all(workspaceId) }); void queryClient.invalidateQueries({ queryKey: collectionQueryKeys.resultsPrefix(workspaceId, collection.id) }); } });
};
