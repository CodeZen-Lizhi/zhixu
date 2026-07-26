import { describe, expect, it } from "vitest";

import {
  artifactGenerationPollMilliseconds,
  artifactGenerationReceiptPollLimit,
  artifactGenerationWorkflowPollInterval,
  artifactGenerationWorkflowQueryKey,
  isArtifactGenerationWorkflowTerminal,
} from "./queries";
import { artifactQueryKeys } from "./query-keys";

describe("Artifact generation queries", () => {
  it("uses the single frozen Workspace and Artifact scoped generation key", () => {
    expect(artifactQueryKeys.sectionGenerations("workspace", "artifact")).toEqual(["artifacts", "workspace", "artifact", "section-generations"]);
    expect(artifactGenerationReceiptPollLimit).toBe(12);
  });

  it("reuses the authoritative Workflow cache identity and polls only active statuses", () => {
    expect(artifactGenerationWorkflowQueryKey("workspace", "workflow")).toEqual(["business", "workspace", "workflow", "workflow"]);
    for (const status of ["pending", "running", "waiting_for_human", "retry_wait", "paused"] as const) {
      expect(artifactGenerationWorkflowPollInterval(status)).toBe(artifactGenerationPollMilliseconds);
      expect(isArtifactGenerationWorkflowTerminal(status)).toBe(false);
    }
    for (const status of ["succeeded", "failed", "cancelled"] as const) {
      expect(artifactGenerationWorkflowPollInterval(status)).toBe(false);
      expect(isArtifactGenerationWorkflowTerminal(status)).toBe(true);
    }
    expect(artifactGenerationWorkflowPollInterval(undefined)).toBe(false);
  });
});
