import { QueryClient } from "@tanstack/react-query";
import { describe, expect, it } from "vitest";

import { canonicalCollectionRequest, clearCollectionWorkspaceQueries } from "./queries";
import { collectionQueryKeys } from "./query-keys";
import { parseCollectionUrlState, writeCollectionUrlState } from "./url-state";

const workspaceA = "13000000-0000-4000-8000-000000000001";
const workspaceB = "13000000-0000-4000-8000-000000000002";
const collectionId = "13000000-0000-4000-8000-000000000003";
const queryHash = "a".repeat(64);

describe("Collection query and URL state", () => {
  it("canonicalizes query keys independent of object key insertion order", () => {
    expect(canonicalCollectionRequest({ limit: 25, cursor: "c", sort: { field: "updated_at", direction: "DESC" } })).toBe(
      canonicalCollectionRequest({ sort: { direction: "DESC", field: "updated_at" }, cursor: "c", limit: 25 }),
    );
    expect(collectionQueryKeys.results(workspaceA, collectionId, 3, queryHash, canonicalCollectionRequest({ limit: 25 }))).toEqual(["collections", workspaceA, "results", collectionId, 3, queryHash, "{\"limit\":25}"]);
  });

  it("keeps cursor out of URL and normalizes invalid recoverable state", () => {
    const parsed = parseCollectionUrlState(new URLSearchParams("view=GRID&field=bad&operator=BAD&value=claim&cursor=secret&sort=created_at&direction=ASC"));
    expect(parsed).toEqual({
      view: "LIST",
      field: "object_type",
      operator: "EQ",
      value: "TOPIC",
      logic: "AND",
      clauses: [{ field: "object_type", operator: "EQ", value: "TOPIC" }],
      sortField: "created_at",
      sortDirection: "ASC",
      group: "",
      columns: ["object_type", "title", "summary", "status", "updated_at"],
      density: "COMFORTABLE",
      selected: "",
    });
    const written = writeCollectionUrlState({ ...parsed, view: "TABLE", selected: "TOPIC:13000000-0000-4000-8000-000000000004" }, new URLSearchParams("cursor=secret"));
    expect(written.get("cursor")).toBeNull();
    expect(written.toString()).toContain("view=TABLE");
  });

  it("round-trips multi-clause state and uses saved view defaults without sorting claims", () => {
    const clauses = JSON.stringify([
      { field: "object_type", operator: "EQ", value: "TOPIC" },
      { field: "status", operator: "IN", value: "ACTIVE,CONFIRMED" },
    ]);
    const parsed = parseCollectionUrlState(new URLSearchParams({ logic: "OR", clauses }), {
      view: "TABLE",
      group: "status",
      columns: ["object_type", "title"],
      density: "COMPACT",
      sortField: "updated_at",
      sortDirection: "DESC",
    });
    expect(parsed).toMatchObject({
      view: "TABLE",
      logic: "OR",
      clauses: [
        { field: "object_type", operator: "EQ", value: "TOPIC" },
        { field: "status", operator: "IN", value: "ACTIVE,CONFIRMED" },
      ],
      group: "status",
      columns: ["object_type", "title"],
      density: "COMPACT",
      sortField: "updated_at",
      sortDirection: "DESC",
    });
    const written = writeCollectionUrlState({ ...parsed, sortField: "created_at" }, new URLSearchParams(), {
      view: "TABLE",
      group: "status",
      columns: ["object_type", "title"],
      density: "COMPACT",
      sortField: "updated_at",
      sortDirection: "DESC",
    });
    expect(written.get("clauses")).toBe(clauses);
    expect(written.get("sort")).toBe("created_at");
    expect(written.get("direction")).toBeNull();
  });

  it("rejects duplicate URL parameters and invalid selected object refs", () => {
    const parsed = parseCollectionUrlState(new URLSearchParams("sort=created_at&sort=topic_id&selected=RELATION:13000000-0000-4000-8000-000000000004&columns=topic_id,source_type"));
    expect(parsed.sortField).toBe("updated_at");
    expect(parsed.selected).toBe("");
    expect(parsed.columns).toEqual(["object_type", "title", "summary", "status", "updated_at"]);
  });

  it("preserves saved fixed columns when URL columns are edited", () => {
    const parsed = parseCollectionUrlState(new URLSearchParams("columns=object_type"), {
      view: "TABLE",
      group: "",
      columns: ["object_type", "title"],
      fixedColumns: ["title"],
      density: "COMFORTABLE",
      sortField: "updated_at",
      sortDirection: "DESC",
    });
    expect(parsed.columns).toEqual(["object_type", "title"]);
  });

  it("clears only the previous Workspace collection cache", () => {
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    queryClient.setQueryData(collectionQueryKeys.list(workspaceA), { workspaceId: workspaceA, items: [] });
    queryClient.setQueryData(collectionQueryKeys.list(workspaceB), { workspaceId: workspaceB, items: [] });
    clearCollectionWorkspaceQueries(queryClient, workspaceA);
    expect(queryClient.getQueryData(collectionQueryKeys.list(workspaceA))).toBeUndefined();
    expect(queryClient.getQueryData(collectionQueryKeys.list(workspaceB))).toEqual({ workspaceId: workspaceB, items: [] });
  });
});
