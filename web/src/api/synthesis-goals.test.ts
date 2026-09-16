import { describe, expect, it } from "vitest";
import { decodeSynthesisGoalPage, decodeSynthesisGoalSelectionPage, SynthesisGoalApiError } from "./synthesis-goals";
import { synthesisId, synthesisProcessingFixture, synthesisTime, synthesisWorkspaceId as workspaceId } from "../test/synthesis-fixtures";

const request = { id: synthesisId(30), workspace_id: workspaceId, goal: "整理 Redis 专项知识", status: "CATALOG_READY", error_code: null, version: 1, created_at: synthesisTime, updated_at: synthesisTime };
const progress = { preparation_failures: 0, preparation_error_code: null, catalog_batches: 1, prepared_batches: 1, selections: 1, pending: 0, running: 0, succeeded: 1, failed: 0, recovery_required: 0, selected_points: 2, ready: true };
const processing = { ...synthesisProcessingFixture(), status: "SUCCEEDED", revision_ids: [synthesisId(31)], failure: null };
const view = { request, progress, processing, candidate: { note_id: synthesisId(32), revision_id: synthesisId(31) } };

describe("主笔记目标 HTTP 边界", () => {
  it("只接收与目标处理记录一致的候选版本", () => {
    expect(decodeSynthesisGoalPage({ workspace_id: workspaceId, items: [view], next_cursor: null }, workspaceId).items[0]?.candidate).toEqual({ noteId: synthesisId(32), revisionId: synthesisId(31) });
    for (const invalid of [
      { ...view, candidate: { ...view.candidate, revision_id: synthesisId(33) } },
      { ...view, processing: { ...processing, workspace_id: synthesisId(99) } },
      { ...view, progress: { ...progress, ready: false } },
    ]) expect(() => decodeSynthesisGoalPage({ workspace_id: workspaceId, items: [invalid], next_cursor: null }, workspaceId)).toThrow();
  });

  it("筛选页绑定目标、按升序游标前进，并拒绝伪造的恢复状态", () => {
    const failed = { id: synthesisId(40), workspace_id: workspaceId, request_id: request.id, status: "FAILED", error_code: "SYNTHESIS_GOAL_MODEL_FAILED", retryable: true, version: 1, created_at: synthesisTime, updated_at: synthesisTime };
    expect(decodeSynthesisGoalSelectionPage({ workspace_id: workspaceId, request_id: request.id, items: [failed], next_after_id: null }, workspaceId, request.id, null).items[0]?.retryable).toBe(true);
    for (const invalid of [
      { ...failed, request_id: synthesisId(99) },
      { ...failed, status: "RECOVERY_REQUIRED", retryable: true },
      { ...failed, status: "SUCCEEDED", error_code: "SYNTHESIS_GOAL_MODEL_FAILED", retryable: false },
    ]) expect(() => decodeSynthesisGoalSelectionPage({ workspace_id: workspaceId, request_id: request.id, items: [invalid], next_after_id: null }, workspaceId, request.id, null)).toThrow(SynthesisGoalApiError);
  });
});
