import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  decodeNoteInterviewPreparation, decodeNoteQuestionSource, decodeSynthesisNoteDetail, decodeSynthesisProcessing, decodeSynthesisProcessingPage,
  decodeSynthesisRevision, decodeSynthesisSourceRef, decodeSynthesisSourceView, getNoteInterviewPreparation, listNoteInterviewPreparations,
  listSynthesisNotes, listSynthesisProcessing, prepareNoteInterview, retrySynthesisProcessing, SynthesisApiError,
} from "./synthesis";
import { synthesisDetailFixture, synthesisId, synthesisNoteRefFixture, synthesisPreparationFixture, synthesisProcessingFixture, synthesisRevisionFixture, synthesisSourceFixture, synthesisWorkspaceId as workspaceId } from "../test/synthesis-fixtures";

const fetchMock = vi.fn<typeof fetch>();
const json = (value: unknown, status = 200) => new Response(JSON.stringify(value), { status, headers: { "Content-Type": "application/json" } });
beforeEach(() => vi.stubGlobal("fetch", fetchMock));
afterEach(() => { fetchMock.mockReset(); vi.unstubAllGlobals(); });

describe("合成笔记 HTTP 边界", () => {
  it("区分事实、冲突和缺口，保留已发布的不可变版本", () => {
    const detail = decodeSynthesisNoteDetail(synthesisDetailFixture(), workspaceId, synthesisId(8));
    expect(detail.currentRevision?.items.map((item) => item.kind)).toEqual(["FACT", "CONFLICT", "GAP"]);
    expect(detail.publishedRevision?.id).toBe(synthesisId(7));
    const source = { revision: synthesisNoteRefFixture(), item_id: synthesisId(12), item_kind: "CONFLICT", sources: [synthesisSourceFixture(), synthesisSourceFixture()] };
    expect(decodeNoteQuestionSource(source, workspaceId).sources).toHaveLength(2);
    expect(() => decodeNoteQuestionSource({ ...source, sources: [synthesisSourceFixture(), { ...synthesisSourceFixture(), excerpt_hash: "f".repeat(64) }] }, workspaceId)).toThrow(SynthesisApiError);
  });

  it("拒绝不符合条目联合、发布身份、Workspace 和可空字段的响应", () => {
    const detail = synthesisDetailFixture();
    for (const value of [
      { ...detail, model_run_id: synthesisId(100) },
      { ...detail, workspace_id: synthesisId(100) },
      { ...detail, publication: { ...detail.publication, revision_id: synthesisId(100) } },
      { ...detail, note: { ...detail.note, workflow_run_id: undefined } },
      { ...detail, current_revision: { ...detail.current_revision, items: detail.current_revision.items.map((item, index) => index === 0 ? { ...item, gap: { question: "forged" } } : item) } },
      { ...detail, published_revision: { ...detail.published_revision, projection_hash: "e".repeat(64) } },
    ]) expect(() => decodeSynthesisNoteDetail(value, workspaceId, synthesisId(8))).toThrow(SynthesisApiError);
    expect(() => decodeSynthesisRevision({ ...synthesisRevisionFixture(), items: [{ ...synthesisRevisionFixture().items[0], answer_points: ["private"] }] }, workspaceId, synthesisId(8))).toThrow(SynthesisApiError);
  });

  it("没有笔记的首次失败仍可查询，UNKNOWN 不能宣称可重试", async () => {
    const processing = synthesisProcessingFixture();
    fetchMock.mockResolvedValueOnce(json({ workspace_id: workspaceId, items: [processing], next_cursor: null }));
    expect(await listSynthesisProcessing(workspaceId)).toMatchObject({ items: [{ status: "FAILED", revisionIds: [], failure: { retryable: true } }] });
    expect(fetchMock.mock.calls[0]?.[0]).toBe(`/api/v1/workspaces/${workspaceId}/synthesis/processing?limit=20`);
    expect(() => decodeSynthesisProcessing({ ...processing, status: "RECOVERY_REQUIRED" }, workspaceId)).toThrow(SynthesisApiError);
    expect(decodeSynthesisProcessing({ ...processing, status: "RECOVERY_REQUIRED", failure: { code: "SYNTHESIS_RESULT_UNKNOWN", retryable: false } }, workspaceId).failure?.retryable).toBe(false);
    expect(() => decodeSynthesisProcessingPage({ workspace_id: workspaceId, items: [], next_cursor: "a.b" }, workspaceId)).toThrow(SynthesisApiError);
  });

  it("STALE 不返回原文，不允许来源被重定向，标题不是来源身份", () => {
    const reference = decodeSynthesisSourceRef(synthesisSourceFixture(), workspaceId);
    const view = { workspace_id: workspaceId, note_id: synthesisId(8), revision_id: synthesisId(7), reference: { ...synthesisSourceFixture(), title: "更新后的展示标题" }, availability: "STALE", text: null };
    expect(decodeSynthesisSourceView(view, workspaceId, synthesisId(8), synthesisId(7), reference)).toMatchObject({ availability: "STALE", text: null });
    expect(() => decodeSynthesisSourceView({ ...view, text: "new text" }, workspaceId, synthesisId(8), synthesisId(7), reference)).toThrow(SynthesisApiError);
    expect(() => decodeSynthesisSourceView({ ...view, reference: { ...view.reference, source_span_id: synthesisId(20) } }, workspaceId, synthesisId(8), synthesisId(7), reference)).toThrow(SynthesisApiError);
  });

  it("重试只发送原 CAS 和幂等身份，响应丢失后保持原请求", async () => {
    const input = { workspaceId, processingId: synthesisId(15), expectedVersion: 1, idempotencyKey: "synthesis-retry-once" };
    const processing = { ...synthesisProcessingFixture(), status: "PENDING", failure: null, version: 2, completed_at: null };
    fetchMock.mockRejectedValueOnce(new Error("response lost")).mockResolvedValueOnce(json({ workspace_id: workspaceId, processing, replayed: true }, 202));
    await expect(retrySynthesisProcessing(input)).rejects.toMatchObject({ code: "NETWORK_ERROR" });
    await expect(retrySynthesisProcessing(input)).resolves.toMatchObject({ status: "PENDING", version: 2 });
    for (const call of fetchMock.mock.calls) {
      expect(call[0]).toBe(`/api/v1/workspaces/${workspaceId}/synthesis/processing/${synthesisId(15)}/retry`);
      expect(new Headers(call[1]?.headers).get("Idempotency-Key")).toBe(input.idempotencyKey);
      expect(call[1]?.body).toBe('{"expected_version":1}');
    }
  });

  it("面试准备可刷新恢复，不能返回隐藏答案或改变选项", async () => {
    const preparation = synthesisPreparationFixture();
    const options = { role: "知识复习", difficulty: "INTERMEDIATE" as const, durationMinutes: 30, questionCount: 6, maxFollowUps: 3 };
    fetchMock.mockResolvedValueOnce(json({ preparation, replayed: false }, 202))
      .mockResolvedValueOnce(json({ items: [preparation] })).mockResolvedValueOnce(json({ preparation, replayed: false }))
      .mockResolvedValueOnce(json({ preparation: { ...preparation, options: { ...preparation.options, role: "other role" } }, replayed: false }, 202));
    await expect(prepareNoteInterview(workspaceId, synthesisId(8), options, "prepare-once")).resolves.toMatchObject({ status: "QUEUED", options });
    await expect(listNoteInterviewPreparations(workspaceId, synthesisId(8))).resolves.toHaveLength(1);
    await expect(getNoteInterviewPreparation(workspaceId, synthesisId(8), preparation.id)).resolves.toMatchObject({ id: preparation.id });
    await expect(prepareNoteInterview(workspaceId, synthesisId(8), options, "prepare-twice")).rejects.toMatchObject({ code: "INVALID_RESPONSE" });
    expect(() => decodeNoteInterviewPreparation({ preparation: { ...preparation, answer_points: ["hidden"] }, replayed: false }, workspaceId, synthesisId(8))).toThrow(SynthesisApiError);
  });

  it("拒绝重复 JSON、错误 Content-Type 和非约定 HTTP 状态", async () => {
    fetchMock.mockResolvedValueOnce(new Response(`{"workspace_id":"${workspaceId}","items":[],"items":[],"next_cursor":null}`, { headers: { "Content-Type": "application/json" } }))
      .mockResolvedValueOnce(new Response("{}", { headers: { "Content-Type": "text/html" } }))
      .mockResolvedValueOnce(json({ workspace_id: workspaceId, items: [], next_cursor: null }, 202));
    for (let attempt = 0; attempt < 3; attempt += 1) await expect(listSynthesisNotes(workspaceId)).rejects.toMatchObject({ code: "INVALID_RESPONSE" });
  });
});
