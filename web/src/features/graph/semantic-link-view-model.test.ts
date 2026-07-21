import { describe, expect, it } from "vitest";

import type { SemanticLinkCandidate } from "../../api/semantic-links";
import {
  compatibleSemanticLinkRelationTypes,
  groupSemanticLinkCandidates,
  semanticLinkCandidateActions,
} from "./semantic-link-view-model";

const candidate = (overrides: Partial<SemanticLinkCandidate> = {}): SemanticLinkCandidate => ({
  id: "92000000-0000-4000-8000-000000000001",
  workspaceId: "92000000-0000-4000-8000-000000000002",
  fingerprint: "a".repeat(64),
  status: "ACTIVE",
  version: 1,
  source: { type: "CLAIM", id: "92000000-0000-4000-8000-000000000003", version: 1, summary: "source", excerpt: "" },
  target: { type: "TOPIC", id: "92000000-0000-4000-8000-000000000004", version: 1, summary: "target", excerpt: "" },
  proposedRelationType: "BELONGS_TO",
  confidence: 0.8,
  reason: "reason",
  discoveryMethods: ["TERM_MATCH"],
  evidence: [],
  generation: {
    indexVersionId: "92000000-0000-4000-8000-000000000005",
    embeddingVersionId: null,
    rerankVersionId: null,
    modelVersion: null,
    modelProfileVersion: null,
    promptVersion: null,
    schemaVersion: null,
    ruleId: null,
    ruleVersion: null,
    modelRunId: null,
  },
  reopenedReason: null,
  reopenedFromCandidateId: null,
  proposalId: null,
  deferredUntil: null,
  createdAt: "2026-07-20T08:10:12Z",
  updatedAt: "2026-07-20T08:10:12Z",
  ...overrides,
});

describe("semantic-link candidate view model", () => {
  it("只向 typed confirm 暴露与端点方向兼容的 Relation Type", () => {
    expect(compatibleSemanticLinkRelationTypes(candidate())).toEqual(["BELONGS_TO", "IMPACTS"]);
    expect(compatibleSemanticLinkRelationTypes(candidate({
      target: { type: "CLAIM", id: "92000000-0000-4000-8000-000000000004", version: 1, summary: "target", excerpt: "" },
    }))).toEqual([
      "CITES",
      "DERIVED_FROM",
      "SUPPORTS",
      "COMPLEMENTS",
      "DUPLICATES",
      "CONFLICTS_WITH",
      "PREREQUISITE_OF",
      "VERSION_OF",
      "IMPACTS",
    ]);
  });

  it("按 Relation Type 分组并在组内按 confidence 降序", () => {
    const lower = candidate({ id: "92000000-0000-4000-8000-000000000010", confidence: 0.4 });
    const higher = candidate({ id: "92000000-0000-4000-8000-000000000011", confidence: 0.9 });
    const impacts = candidate({
      id: "92000000-0000-4000-8000-000000000012",
      proposedRelationType: "IMPACTS",
      confidence: 0.7,
    });
    const groups = groupSemanticLinkCandidates([lower, impacts, higher]);
    expect(groups.map((group) => group.relationType)).toEqual(["BELONGS_TO", "IMPACTS"]);
    expect(groups[0]?.items.map((item) => item.id)).toEqual([higher.id, lower.id]);
  });

  it("只为 ACTIVE/DEFERRED 返回合法状态动作", () => {
    expect(semanticLinkCandidateActions("ACTIVE")).toContain("DEFER");
    expect(semanticLinkCandidateActions("ACTIVE")).not.toContain("RESUME");
    expect(semanticLinkCandidateActions("DEFERRED")).toContain("RESUME");
    expect(semanticLinkCandidateActions("DEFERRED")).not.toContain("DEFER");
    expect(semanticLinkCandidateActions("PROPOSAL_CREATED")).toEqual([]);
  });
});
