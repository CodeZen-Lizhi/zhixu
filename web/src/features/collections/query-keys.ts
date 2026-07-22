import type { CollectionResultPage } from "../../api/collections";

export const collectionQueryKeys = {
  all: (workspaceId: string) => ["collections", workspaceId] as const,
  list: (workspaceId: string, request = "{}") => ["collections", workspaceId, "list", request] as const,
  detail: (workspaceId: string, collectionId: string) => ["collections", workspaceId, "detail", collectionId] as const,
  resultsPrefix: (workspaceId: string, collectionId: string) => ["collections", workspaceId, "results", collectionId] as const,
  results: (workspaceId: string, collectionId: string, version: number, queryHash: string, request: string) => ["collections", workspaceId, "results", collectionId, version, queryHash, request] as const,
  preview: (workspaceId: string, request: string) => ["collections", workspaceId, "preview", request] as const,
};

export type CollectionResultData = CollectionResultPage;
