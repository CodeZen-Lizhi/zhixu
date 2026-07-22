import { describe, expect, it } from "vitest";

import type { WorkflowStatus } from "../../api/business";
import { getWorkflowControlAvailability } from "./WorkflowsPage";

describe("workflow control availability", () => {
  it.each<[WorkflowStatus, boolean, boolean, boolean]>([
    ["pending", true, false, true],
    ["running", true, false, true],
    ["waiting_for_human", true, false, true],
    ["retry_wait", true, false, true],
    ["paused", false, true, true],
    ["succeeded", false, false, false],
    ["failed", false, false, false],
    ["cancelled", false, false, false],
  ])(
    "%s maps to pause=%s resume=%s cancel=%s",
    (status, canPause, canResume, canCancel) => {
      expect(getWorkflowControlAvailability(status)).toEqual({
        canPause,
        canResume,
        canCancel,
      });
    },
  );
});
