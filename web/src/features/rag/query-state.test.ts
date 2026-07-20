import { describe, expect, it, vi } from "vitest";

import type { Answer } from "../../api/conversation";
import type { ServerEventEnvelope } from "../../events";
import { invalidateRagEvent } from "./event-recovery";
import { ragQueryKeys } from "./query-keys";
import { maximumPendingAnswerPolls, pendingAnswerPollInterval } from "./queries";

const workspaceId = "92000000-0000-4000-8000-000000000001";
const otherWorkspaceId = "92000000-0000-4000-8000-000000000002";
const conversationId = "92000000-0000-4000-8000-000000000003";
const answerId = "92000000-0000-4000-8000-000000000004";

describe("RAG query state", () => {
  it("所有 key 都以 Workspace 分区", () => {
    expect(ragQueryKeys.answer(workspaceId, answerId)).not.toEqual(ragQueryKeys.answer(otherWorkspaceId, answerId));
    expect(ragQueryKeys.turns(workspaceId, conversationId)).not.toEqual(ragQueryKeys.turns(otherWorkspaceId, conversationId));
  });

  it("pending 只在有界次数内轮询", () => {
    const pending = { publicationStatus: "pending" } as Answer;
    expect(pendingAnswerPollInterval(pending, maximumPendingAnswerPolls - 1)).toBe(2_000);
    expect(pendingAnswerPollInterval(pending, maximumPendingAnswerPolls)).toBe(false);
    expect(pendingAnswerPollInterval({ publicationStatus: "completed" } as Answer, 1)).toBe(false);
  });

  it("SSE 只定向失效同 Workspace 的权威查询", async () => {
    const invalidate = vi.fn<(key: readonly unknown[]) => Promise<void>>().mockResolvedValue(undefined);
    const event = {
      workspaceId,
      payloadSummary: { conversationId, answerId },
      invalidations: [
        { resource: "conversation", id: conversationId },
        { resource: "answer", id: answerId },
      ],
    } as ServerEventEnvelope;
    await invalidateRagEvent(workspaceId, event, invalidate);
    expect(invalidate).toHaveBeenCalledWith(ragQueryKeys.conversations(workspaceId));
    expect(invalidate).toHaveBeenCalledWith(ragQueryKeys.conversation(workspaceId, conversationId));
    expect(invalidate).toHaveBeenCalledWith(ragQueryKeys.turns(workspaceId, conversationId));
    expect(invalidate).toHaveBeenCalledWith(ragQueryKeys.answer(workspaceId, answerId));

    invalidate.mockClear();
    await invalidateRagEvent(otherWorkspaceId, event, invalidate);
    expect(invalidate).not.toHaveBeenCalled();
  });
});
