import { describe, expect, it } from "vitest";

import {
  parseInboxUrlState,
  parseProposalUrlState,
  parseWorkflowUrlState,
  proposalStatusOptions,
  writeInboxUrlState,
  writeProposalUrlState,
  writeWorkflowUrlState,
} from "./url-state";

describe("business URL state", () => {
  it("keeps only typed Inbox filters and removes opaque pagination cursors", () => {
    const parsed = parseInboxUrlState(new URLSearchParams({
      security_status: "passed",
      ingestion_status: "parsed",
      workflow_status: "running",
      index_status: "included",
      mime_type: "text/markdown",
      cursor: "next-page",
      cursor_workspace: "10000000-0000-4000-8000-000000000002",
    }));

    expect(parsed).toEqual({
      securityStatus: "passed",
      ingestionStatus: "parsed",
      workflowStatus: "running",
      indexStatus: "included",
      mimeType: "text/markdown",
    });
    const canonical = writeInboxUrlState(parsed);
    expect(canonical.get("cursor")).toBeNull();
    expect(canonical.get("cursor_workspace")).toBeNull();
  });

  it("drops duplicate and invalid filter state without preserving legacy cursor parameters", () => {
    const duplicated = new URLSearchParams("status=ready_for_review&status=approved&cursor=secret&cursor_workspace=old-workspace");
    expect(parseProposalUrlState(duplicated)).toMatchObject({ status: "" });
    expect(writeProposalUrlState(parseProposalUrlState(duplicated)).toString()).not.toContain("cursor");

    expect(parseProposalUrlState(new URLSearchParams("status=future&risk=unknown&created_after=2026-02-31"))).toMatchObject({
      status: "",
      risk: "",
      createdDate: "",
    });
    expect(parseProposalUrlState(new URLSearchParams("risk=high")).risk).toBe("");
  });

  it("round-trips every public Proposal status without accepting unknown values", () => {
    for (const [status] of proposalStatusOptions) {
      const written = writeProposalUrlState({ status, type: "", risk: "", createdDate: "" });
      expect(parseProposalUrlState(written).status).toBe(status);
    }
    expect(parseProposalUrlState(new URLSearchParams("status=future")).status).toBe("");
  });

  it.each(["CRITICAL", "HIGH", "MEDIUM", "LOW"] as const)("round-trips the uppercase Proposal risk level %s", (risk) => {
    const written = writeProposalUrlState({ status: "", type: "", risk, createdDate: "" });

    expect(written.get("risk")).toBe(risk);
    expect(parseProposalUrlState(written).risk).toBe(risk);
  });

  it("round-trips the publish_artifact Proposal filter", () => {
    const written = writeProposalUrlState({ status: "", type: "publish_artifact", risk: "HIGH", createdDate: "" });

    expect(written.toString()).toBe("proposal_type=publish_artifact&risk=HIGH");
    expect(parseProposalUrlState(written)).toMatchObject({ type: "publish_artifact", risk: "HIGH" });
  });

  it("round-trips the downstream_update Proposal filter", () => {
    const written = writeProposalUrlState({ status: "ready_for_review", type: "downstream_update", risk: "HIGH", createdDate: "" });

    expect(written.toString()).toBe("status=ready_for_review&proposal_type=downstream_update&risk=HIGH");
    expect(parseProposalUrlState(written)).toMatchObject({ status: "ready_for_review", type: "downstream_update", risk: "HIGH" });
  });

  it("normalizes Workflow state and excludes pagination from the canonical URL", () => {
    const state = parseWorkflowUrlState(new URLSearchParams("status=paused&cursor=page-2&cursor_workspace=old-workspace"));
    expect(state).toEqual({ status: "paused" });
    expect(writeWorkflowUrlState(state).toString()).toBe("status=paused");

    const duplicated = new URLSearchParams("status=paused&status=running&cursor=page-2");
    expect(parseWorkflowUrlState(duplicated)).toEqual({ status: "" });
  });
});
