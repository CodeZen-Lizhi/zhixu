import { describe, expect, it } from "vitest";

import {
  normalizeSearchQuery,
  normalizeSourceVersionId,
  parseSearchUrlState,
  writeSearchUrlState,
} from "./url-state";

const workspaceA = "92000000-0000-4000-8000-000000000001";
const workspaceB = "92000000-0000-4000-8000-000000000002";
const sourceVersionId = "92000000-0000-4000-8000-000000000003";

describe("Search URL state", () => {
  it("round-trips query, mode, Source Version, and Workspace-bound cursor", () => {
    const state = {
      query: "relation evidence",
      mode: "semantic" as const,
      sourceVersionId,
      cursor: "cursor-v1.opaque",
    };

    const parameters = writeSearchUrlState(state, workspaceA);

    expect(parameters.get("scope_workspace")).toBe(workspaceA);
    expect(parseSearchUrlState(parameters, workspaceA)).toEqual(state);
  });

  it("uses Hybrid as the canonical default without requiring a mode parameter", () => {
    const parameters = writeSearchUrlState({
      query: "knowledge",
      mode: "hybrid",
      sourceVersionId: "",
      cursor: "",
    }, workspaceA);

    expect(parameters.has("mode")).toBe(false);
    expect(parameters.get("scope_workspace")).toBe(workspaceA);
    expect(parseSearchUrlState(parameters, workspaceA)).toMatchObject({ mode: "hybrid", query: "knowledge" });
  });

  it("drops cursor after invalid, duplicated, or cross-Workspace state", () => {
    const invalidMode = new URLSearchParams(`query=knowledge&mode=future&cursor=page-2&scope_workspace=${workspaceA}`);
    expect(parseSearchUrlState(invalidMode, workspaceA)).toMatchObject({ mode: "hybrid", cursor: "" });

    const duplicatedQuery = new URLSearchParams(`query=one&query=two&cursor=page-2&scope_workspace=${workspaceA}`);
    expect(parseSearchUrlState(duplicatedQuery, workspaceA)).toMatchObject({ query: "", cursor: "" });

    const crossWorkspace = new URLSearchParams(`query=knowledge&mode=semantic&source_version_id=${sourceVersionId}&cursor=page-2&scope_workspace=${workspaceA}`);
    expect(parseSearchUrlState(crossWorkspace, workspaceB)).toEqual({
      query: "",
      mode: "semantic",
      sourceVersionId: "",
      cursor: "",
    });

    const duplicatedScope = new URLSearchParams(`query=knowledge&scope_workspace=${workspaceA}&scope_workspace=${workspaceB}`);
    expect(parseSearchUrlState(duplicatedScope, workspaceA)).toMatchObject({ query: "", sourceVersionId: "", cursor: "" });
  });

  it("adopts a legacy unscoped deep link once and writes the canonical Workspace scope", () => {
    const legacy = new URLSearchParams(`query=knowledge&source_version_id=${sourceVersionId}`);
    const parsed = parseSearchUrlState(legacy, workspaceA);

    expect(parsed).toMatchObject({ query: "knowledge", sourceVersionId });
    expect(writeSearchUrlState(parsed, workspaceA).get("scope_workspace")).toBe(workspaceA);
  });

  it("rejects invalid query, Source Version, and cursor boundaries", () => {
    expect(normalizeSearchQuery("   ")).toBeUndefined();
    expect(normalizeSearchQuery("a\0b")).toBeUndefined();
    expect(normalizeSearchQuery("汉".repeat(2731))).toBeUndefined();
    expect(normalizeSearchQuery("  normalized query  ")).toBe("normalized query");
    expect(normalizeSourceVersionId("not-a-uuid")).toBeUndefined();

    const parameters = new URLSearchParams({
      query: "knowledge",
      source_version_id: "not-a-uuid",
      cursor: "x".repeat(2049),
      scope_workspace: workspaceA,
    });
    expect(parseSearchUrlState(parameters, workspaceA)).toEqual({
      query: "knowledge",
      mode: "hybrid",
      sourceVersionId: "",
      cursor: "",
    });
  });
});
