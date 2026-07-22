import { QueryClient } from "@tanstack/react-query";
import { describe, expect, it } from "vitest";

import { canonicalHealthRequest, clearHealthWorkspaceQueries } from "./queries";
import { healthQueryKeys } from "./query-keys";
import { parseHealthUrlState, writeHealthUrlState } from "./url-state";

const workspaceA = "14000000-0000-4000-8000-000000000001";
const workspaceB = "14000000-0000-4000-8000-000000000002";

describe("Health query and URL state", () => {
  it("canonicalizes issue list requests with Workspace scope", () => {
    expect(canonicalHealthRequest({ workspaceId: workspaceA, statuses: ["OPEN"], limit: 25 })).toBe(
      canonicalHealthRequest({ limit: 25, statuses: ["OPEN"], workspaceId: workspaceA }),
    );
    expect(healthQueryKeys.issues(workspaceA, canonicalHealthRequest({ limit: 25 }))).toEqual(["knowledge-health", workspaceA, "issues", "{\"limit\":25}"]);
  });

  it("normalizes URL filters and keeps only recoverable Health state", () => {
    const parsed = parseHealthUrlState(new URLSearchParams("issue=not-a-uuid&scan=14000000-0000-4000-8000-000000000003&status=OPEN&severity=BAD&type=STALE&cursor=secret"));
    expect(parsed).toMatchObject({ issueId: "", scanId: "14000000-0000-4000-8000-000000000003", status: "OPEN", severity: "", type: "STALE" });
    const written = writeHealthUrlState(parsed, new URLSearchParams("cursor=secret"));
    expect(written.get("cursor")).toBeNull();
    expect(written.get("status")).toBe("OPEN");
  });

  it("fails safe when URL parameters are duplicated", () => {
    const parsed = parseHealthUrlState(new URLSearchParams(`issue=14000000-0000-4000-8000-000000000003&issue=14000000-0000-4000-8000-000000000004&status=OPEN&status=RESOLVED`));
    expect(parsed).toMatchObject({ issueId: "", status: "" });
  });

  it("clears only the previous Workspace health cache", () => {
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    queryClient.setQueryData(healthQueryKeys.summary(workspaceA), { workspaceId: workspaceA });
    queryClient.setQueryData(healthQueryKeys.summary(workspaceB), { workspaceId: workspaceB });
    clearHealthWorkspaceQueries(queryClient, workspaceA);
    expect(queryClient.getQueryData(healthQueryKeys.summary(workspaceA))).toBeUndefined();
    expect(queryClient.getQueryData(healthQueryKeys.summary(workspaceB))).toEqual({ workspaceId: workspaceB });
  });
});
