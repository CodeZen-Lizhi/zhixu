import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  decodeSynthesisUpdateSummaries, getSynthesisUpdateSummaries, decodeAnchorRecommendation, decodeSourceKnowledgeDirectory, decodeSynthesisSourceImpacts,
  decodeSynthesisSupplement, listSynthesisSupplements, openSynthesisSupplement,
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
  it("保留精确主笔记片段引用，拒绝跨工作区、自引用和缺字段", () => {
    const original = synthesisRevisionFixture();
    const reference = { workspace_id: workspaceId, note_id: synthesisId(40), revision_id: synthesisId(41), publication_id: synthesisId(42), item_id: synthesisId(43), projection_hash: "a".repeat(64) };
    const withReference = (body: unknown) => ({ ...original, items: original.items.map((item, index) => index === 0 ? { ...item, body_reference: body } : item) });
    expect(decodeSynthesisRevision(withReference(reference), workspaceId, original.note_id).items[0]?.bodyReference).toMatchObject({ noteId: reference.note_id, revisionId: reference.revision_id, itemId: reference.item_id, projectionHash: reference.projection_hash });
    for (const bad of [null, { ...reference, workspace_id: synthesisId(99) }, { ...reference, note_id: original.note_id }, { ...reference, publication_id: undefined }, { ...reference, relation: "SHARED_SOURCE" }]) {
      expect(() => decodeSynthesisRevision(withReference(bad), workspaceId, original.note_id)).toThrow(SynthesisApiError);
    }
  });

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


it("补充来源与历史引用分开读取，拒绝片段或笔记身份漂移", async () => {
  const wire = { id: synthesisId(40), workspace_id: workspaceId, note_id: synthesisId(8), base_revision_id: synthesisId(7), item_id: synthesisId(11),
    slot: "FACT", alternative_index: -1, processing_id: synthesisId(9), reference: synthesisSourceFixture(), created_at: "2026-09-14T00:00:00Z" };
  fetchMock.mockResolvedValueOnce(json({ workspace_id: workspaceId, note_id: synthesisId(8), items: [wire], next_cursor: null }));
  const page = await listSynthesisSupplements(workspaceId, synthesisId(8));
  const item = page.items[0];
  if (!item) throw new Error("missing supplement");
  expect(item.baseRevisionId).toBe(synthesisId(7));
  expect(fetchMock.mock.calls[0]?.[0]).toBe(`/api/v1/workspaces/${workspaceId}/synthesis/notes/${synthesisId(8)}/supplements?limit=20`);
  for (const bad of [{ ...wire, note_id: synthesisId(99) }, { ...wire, alternative_index: 0 }, { ...wire, slot: "CONFLICT" }]) {
    expect(() => decodeSynthesisSupplement(bad, workspaceId, synthesisId(8))).toThrow(SynthesisApiError);
  }
  fetchMock.mockResolvedValueOnce(json({ workspace_id: workspaceId, note_id: synthesisId(8), supplement: { ...wire, reference: { ...wire.reference, excerpt_hash: "a".repeat(64) } }, availability: "AVAILABLE", text: "另一段原文" }));
  await expect(openSynthesisSupplement(workspaceId, synthesisId(8), item)).rejects.toThrow(SynthesisApiError);
  fetchMock.mockResolvedValueOnce(json({ workspace_id: workspaceId, note_id: synthesisId(8), supplement: wire, availability: "UNAVAILABLE", text: null }));
  await expect(openSynthesisSupplement(workspaceId, synthesisId(8), item)).resolves.toMatchObject({ availability: "UNAVAILABLE", text: null });
});

it("关联审批绑定原范围和冻结建议，拒绝隐式扩围并复用重试请求", async () => {
  const wire = { id: synthesisId(50), workspace_id: workspaceId, note_id: synthesisId(8), basis_revision_id: synthesisId(7), title: "Redis 专项",
    scope: { topics: ["Redis"], audiences: ["开发者"], description: "Redis 使用与机制" }, scope_version: 1, version: 1, created_at: "2026-09-14T00:00:00Z", updated_at: "2026-09-14T00:00:00Z" };
  const recommendation = { id: synthesisId(51), workspace_id: workspaceId, anchor_id: wire.id, kind: "SOURCE_ASSOCIATION", scope_version: 1, before: null, suggested: null,
    reason: "补充缓存过期知识", evidence: [synthesisSourceFixture()], model_run_id: synthesisId(52), status: "PENDING", version: 1, created_at: "2026-09-14T00:00:00Z" };
  const { decodeKnowledgeAnchor, decodeAnchorProposal, decideAnchorProposals } = await import("./synthesis");
  const anchor = decodeKnowledgeAnchor(wire, workspaceId);
  const proposal = decodeAnchorProposal(recommendation, workspaceId, wire.id);
  const input = { workspaceId, anchor, kind: "SOURCE_ASSOCIATION" as const, decision: "ACCEPTED" as const, items: [proposal], idempotencyKey: "accept-redis-source" };
  const accepted = { anchor: { ...wire, version: 2 }, items: [{ ...recommendation, status: "ACCEPTED", version: 2 }], replayed: true };
  fetchMock.mockResolvedValueOnce(json({ ...accepted, anchor: { ...accepted.anchor, scope: { ...wire.scope, topics: ["Redis", "Oracle"] } } }));
  await expect(decideAnchorProposals(input)).rejects.toThrow(SynthesisApiError);
  fetchMock.mockResolvedValueOnce(json(accepted));
  await expect(decideAnchorProposals(input)).resolves.toMatchObject({ anchor: { scope: anchor.scope }, replayed: true });
  expect(fetchMock.mock.calls[0]?.[1]?.body).toBe(fetchMock.mock.calls[1]?.[1]?.body);
  expect(new Headers(fetchMock.mock.calls[1]?.[1]?.headers).get("Idempotency-Key")).toBe("accept-redis-source");
  expect(JSON.parse(typeof fetchMock.mock.calls[1]?.[1]?.body === "string" ? fetchMock.mock.calls[1][1].body : "")).toEqual({ expected_anchor_version: 1, decision: "ACCEPTED", items: [{ proposal_id: proposal.id, expected_version: 1 }] });
});

it("片段目录绑定来源版本、画像版本和精确证据，拒绝其他片段知识点", async () => {
  const { decodeSynthesisSourceRef, getSynthesisSourceKnowledgePoints } = await import("./synthesis");
  const reference = synthesisSourceFixture();
  const point = { locator: { profile_revision_id: synthesisId(70), kind: "KNOWLEDGE_POINT", index: 0 }, text: "Redis 缓存过期", source_span_ids: [reference.source_span_id] };
  const directory = { workspace_id: workspaceId, source_version_id: reference.source.source_version_id, status: "ANALYZED", profile_status: "READY", profile_revision_id: synthesisId(70),
    parse_projection_id: reference.source.parse_projection_id, summary: "缓存机制", topics: [{ label: "Redis", source_span_ids: [reference.source_span_id] }], points: [point] };
  const wire = { workspace_id: workspaceId, note_id: synthesisId(8), revision_id: synthesisId(7), reference, directory, points: [point] };
  const request = () => getSynthesisSourceKnowledgePoints(workspaceId, synthesisId(8), synthesisId(7), decodeSynthesisSourceRef(reference, workspaceId));
  fetchMock.mockResolvedValueOnce(json(wire));
  await expect(request()).resolves.toMatchObject({ points: [{ text: "Redis 缓存过期", profileRevisionId: synthesisId(70) }] });
  fetchMock.mockResolvedValueOnce(json({ ...wire, points: [{ ...point, source_span_ids: [synthesisId(99)] }] }));
  await expect(request()).rejects.toThrow(SynthesisApiError);
  fetchMock.mockResolvedValueOnce(json({ ...wire, directory: { ...directory, source_version_id: synthesisId(99) } }));
  await expect(request()).rejects.toThrow(SynthesisApiError);
});

it("来源图谱绑定所读版本，共享文件允许不同片段但拒绝伪造依赖", async () => {
  const { decodeSynthesisSourceGraph, getSynthesisSourceGraph } = await import("./synthesis");
  const revision = decodeSynthesisRevision(synthesisRevisionFixture(), workspaceId, synthesisId(8));
  const peer = { note_id: synthesisId(80), revision_id: synthesisId(81), revision_no: 2, title: "面试复习", sources: [{ ...synthesisSourceFixture(), source_span_id: synthesisId(82) }] };
  const wire = { workspace_id: workspaceId, note_id: revision.noteId, revision_id: revision.id, sources: [synthesisSourceFixture()], shared_notes: [peer], next_after_note_id: null };
  expect(decodeSynthesisSourceGraph(wire, revision).sharedNotes[0]?.sources[0]?.sourceSpanId).toBe(synthesisId(82));
  for (const bad of [
    { ...wire, revision_id: synthesisId(99) }, { ...wire, sources: [] }, { ...wire, body_dependencies: [] },
    { ...wire, shared_notes: [{ ...peer, note_id: revision.noteId }] },
    { ...wire, shared_notes: [{ ...peer, sources: [{ ...synthesisSourceFixture(), source: { ...synthesisSourceFixture().source, source_id: synthesisId(99) } }] }] },
    { ...wire, next_after_note_id: peer.note_id },
  ]) expect(() => decodeSynthesisSourceGraph(bad, revision)).toThrow(SynthesisApiError);
  fetchMock.mockResolvedValueOnce(json(wire));
  expect(await getSynthesisSourceGraph(revision)).toMatchObject({ sharedNotes: [{ noteId: peer.note_id }] });
  expect(fetchMock.mock.calls[0]?.[0]).toBe(`${'/api/v1/workspaces/' + workspaceId}/synthesis/notes/${revision.noteId}/revisions/${revision.id}/source-graph?limit=20`);
});


it("范围分析区分终态并拒绝伪造权限、跨工作区证据和无建议冲突", () => {
  const valid = { id: synthesisId(81), workspace_id: workspaceId, note_id: synthesisId(8), anchor_id: null, basis_revision_id: synthesisId(7), expected_scope_version: null,
    kind: "INITIAL_SCOPE", status: "SUCCEEDED", recommendation: { title: "Redis", kind: "INITIAL_SCOPE", scope: { topics: ["Redis"], audiences: ["复习"], description: "Redis 复习" }, reason: "来源是 Redis 模块", evidence: [synthesisSourceFixture()] },
    proposal_id: null, error_code: null, retryable: false, version: 3, created_at: "2026-09-14T00:00:00Z", updated_at: "2026-09-14T00:00:01Z" };
  expect(decodeAnchorRecommendation(valid, workspaceId).recommendation?.scope?.topics).toEqual(["Redis"]);
  for (const invalid of [
    { ...valid, approved: true }, { ...valid, status: "NO_RECOMMENDATION" }, { ...valid, proposal_id: synthesisId(90) },
    { ...valid, recommendation: { ...valid.recommendation, evidence: [synthesisSourceFixture(synthesisId(91))] } },
    { ...valid, recommendation: { ...valid.recommendation, scope: null } },
    { ...valid, status: "RECOVERY_REQUIRED", recommendation: null, error_code: "ANCHOR_MODEL_RECOVERY_REQUIRED", retryable: true },
  ]) expect(() => decodeAnchorRecommendation(invalid, workspaceId)).toThrow(SynthesisApiError);
  expect(decodeAnchorRecommendation({ ...valid, status: "NO_RECOMMENDATION", recommendation: null }, workspaceId).recommendation).toBeNull();
});


it("区分未记录的历史目录，不接受伪造的当前画像", () => {
 const sourceVersionId = synthesisId(301);
 const missing = { workspace_id: workspaceId, source_version_id: sourceVersionId, status: "UNRECORDED" };
 expect(decodeSourceKnowledgeDirectory(missing, workspaceId, sourceVersionId).status).toBe("UNRECORDED");
 expect(() => decodeSourceKnowledgeDirectory({ ...missing, profile_status: "READY" }, workspaceId, sourceVersionId)).toThrow();
 expect(() => decodeSourceKnowledgeDirectory({ ...missing, profile_revision_id: synthesisId(302) }, workspaceId, sourceVersionId)).toThrow();
});

it("来源复核提醒严格绑定所读版本的引用和受影响片段", () => {
  const wire = synthesisRevisionFixture();
  const revision = decodeSynthesisRevision(wire, workspaceId, wire.note_id);
  const item = { id: synthesisId(300), reason: "SOURCE_REMOVED", detected_at: "2026-09-15T00:00:00Z", currently_unavailable: true, reference: synthesisSourceFixture(), item_ids: [synthesisId(11), synthesisId(12)] };
  const response = { workspace_id: workspaceId, note_id: revision.noteId, revision_id: revision.id, items: [item] };
  expect(decodeSynthesisSourceImpacts(response, revision)[0]?.itemIds).toEqual(item.item_ids);
  for (const invalid of [
    { ...response, workspace_id: synthesisId(301) },
    { ...response, items: [item, item] },
    { ...response, items: [{ ...item, item_ids: [synthesisId(11), synthesisId(11)] }] },
    { ...response, items: [{ ...item, item_ids: [synthesisId(11), synthesisId(13)] }] },
    { ...response, items: [{ ...item, reference: { ...item.reference, source_span_id: synthesisId(302) } }] },
    { ...response, items: [{ ...item, reason: "KNOWLEDGE_FALSE" }] },
  ]) expect(() => decodeSynthesisSourceImpacts(invalid, revision)).toThrow(SynthesisApiError);
});

it("当前来源状态与可核验历史快照分别表达", () => {
  const reference = decodeSynthesisSourceRef(synthesisSourceFixture(), workspaceId);
  const view = { workspace_id: workspaceId, note_id: synthesisId(8), revision_id: synthesisId(7), reference: synthesisSourceFixture(), availability: "UNAVAILABLE", text: null, snapshot_text: "原先版本的原文" };
  expect(decodeSynthesisSourceView(view, workspaceId, synthesisId(8), synthesisId(7), reference)).toMatchObject({ availability: "UNAVAILABLE", text: null, snapshotText: "原先版本的原文" });
  for (const value of [{ ...view, snapshot_text: null }, { ...view, snapshot_text: "" }, { ...view, text: "替换原文" }, { ...view, availability: "AVAILABLE", text: "当前内容" }]) expect(() => decodeSynthesisSourceView(value, workspaceId, synthesisId(8), synthesisId(7), reference)).toThrow(SynthesisApiError);
});

describe("主笔记列表复核摘要", () => {
  it("通过生成客户端读取精确版本，拒绝错误作用域、计数和历史身份", async () => {
    const detail = decodeSynthesisNoteDetail(synthesisDetailFixture(), workspaceId, synthesisId(8));
    const note = { ...detail, itemCount: 3, conflictCount: 1, gapCount: 1, openGapCount: 1 };
    const response = { workspace_id: workspaceId, items: [{ note_id: detail.note.id, current_revision_id: synthesisId(7), published_revision_id: synthesisId(7), items: [{ revision_id: synthesisId(7), source_review_count: 2, body_review_count: 1 }] }] };
    fetchMock.mockResolvedValue(json(response));
    expect(await getSynthesisUpdateSummaries(workspaceId, [note])).toEqual([{ noteId: detail.note.id, items: [{ revisionId: synthesisId(7), sourceReviewCount: 2, bodyReviewCount: 1 }] }]);
    expect(fetchMock.mock.calls[0]?.[0]).toEqual(expect.stringContaining(`/synthesis/update-summaries?note_ids=${detail.note.id}`));
    const first = <T,>(values: T[]): T => { const value = values[0]; if (value === undefined) throw new Error("missing fixture item"); return value; };
    for (const mutate of [
      (r: typeof response) => { r.workspace_id = synthesisId(99); },
      (r: typeof response) => { first(r.items).current_revision_id = synthesisId(99); },
      (r: typeof response) => { first(r.items).published_revision_id = ""; },
      (r: typeof response) => { first(first(r.items).items).revision_id = synthesisId(99); },
      (r: typeof response) => { first(first(r.items).items).body_review_count = -1; },
      (r: typeof response) => { first(first(r.items).items).source_review_count = 0.5; },
      (r: typeof response) => { first(r.items).items.push(first(first(r.items).items)); },
      (r: typeof response) => { r.items = []; },
      (r: typeof response) => { r.items.push(first(r.items)); },
    ]) { const bad = structuredClone(response); mutate(bad); expect(() => decodeSynthesisUpdateSummaries(bad, workspaceId, [note])).toThrow(SynthesisApiError); }
    expect(() => decodeSynthesisUpdateSummaries({ ...response, extra: true }, workspaceId, [note])).toThrow(SynthesisApiError);
  });
});

it("v2严格允许零可信项并逐字保存人工全文，不把历史参考当当前来源", () => {
  const original = synthesisRevisionFixture();
  const display = { renderer_version: "synthesis-markdown/v2", full_content: "# 标题\n\n人工批注：  保留空格。\n", manual_changes: true, review_required: true, historical_sources: [synthesisSourceFixture()] };
  const value = { ...original, items: [], display };
  const decoded = decodeSynthesisRevision(value, workspaceId, original.note_id);
  expect(decoded.items).toEqual([]);
  expect(decoded.display?.fullContent).toBe(display.full_content);
  expect(decoded.display?.historicalSources).toHaveLength(1);
  expect(decodeSynthesisRevision({ ...value, display: { ...display, full_content: "\ufeff原文" } }, workspaceId, original.note_id).display?.fullContent).toBe("\ufeff原文");
  for (const bad of [null, { ...display, renderer_version: "v3" }, { ...display, review_required: false }, { ...display, full_content: "\0" }, { ...display, full_content: "\ud800" }, { ...display, full_content: "中".repeat(350000) }, { ...display, machine_items: original.items }, { ...display, historical_sources: [{ ...synthesisSourceFixture(), source: { ...synthesisSourceFixture().source, workspace_id: synthesisId(99) } }] }]) {
    expect(() => decodeSynthesisRevision({ ...value, display: bad }, workspaceId, original.note_id)).toThrow(SynthesisApiError);
  }
  expect(() => decodeSynthesisRevision({ ...original, items: [] }, workspaceId, original.note_id)).toThrow(SynthesisApiError);
  expect(() => decodeSynthesisRevision({ ...value, items: original.items }, workspaceId, original.note_id)).toThrow(SynthesisApiError);
  const source = { workspace_id: workspaceId, note_id: original.note_id, revision_id: original.id, reference: synthesisSourceFixture(), availability: "AVAILABLE", text: "原文", role: "HISTORICAL_REVIEW" };
  expect(decodeSynthesisSourceView(source, workspaceId, original.note_id, original.id, decodeSynthesisSourceRef(synthesisSourceFixture(), workspaceId)).role).toBe("HISTORICAL_REVIEW");
  expect(() => decodeSynthesisSourceView({ ...source, role: "TRUSTED" }, workspaceId, original.note_id, original.id, decodeSynthesisSourceRef(synthesisSourceFixture(), workspaceId))).toThrow(SynthesisApiError);
});

it("冲突观点允许共享精确来源，但相同span跨来源身份仍拒绝", () => {
  const revision = synthesisRevisionFixture();
  const display = { renderer_version: "synthesis-markdown/v2", full_content: "全文", manual_changes: false, review_required: false, historical_sources: [] };
  expect(decodeSynthesisRevision({ ...revision, display }, workspaceId, revision.note_id).items).toHaveLength(3);
  const items = revision.items.map((item) => item.conflict === null ? item : { ...item, conflict: { ...item.conflict, alternatives: item.conflict.alternatives.map((alternative, index) => index === 0 ? alternative : { ...alternative, sources: [{ ...synthesisSourceFixture(), source: { ...synthesisSourceFixture().source, source_id: synthesisId(99) } }] }) } });
  expect(() => decodeSynthesisRevision({ ...revision, items, display }, workspaceId, revision.note_id)).toThrow(SynthesisApiError);
});

it("remerge provenance names the exact parent without inventing a new model run", () => {
  const original = synthesisRevisionFixture();
  const display = { renderer_version: "synthesis-markdown/v2", full_content: "人工与最新文件合并全文", manual_changes: true, review_required: true, historical_sources: [synthesisSourceFixture()] };
  const value = { ...original, revision_no: 2, parent_revision_id: synthesisId(90), items: [], display, remerge: { attempt_id: synthesisId(91), source_revision_id: synthesisId(90) } };
  expect(decodeSynthesisRevision(value, workspaceId, original.note_id).remerge?.sourceRevisionId).toBe(synthesisId(90));
  for (const remerge of [null, { ...value.remerge, source_revision_id: original.id }, { ...value.remerge, source_revision_id: synthesisId(92) }, { ...value.remerge, model_run_id: synthesisId(93) }, { attempt_id: synthesisId(91) }]) expect(() => decodeSynthesisRevision({ ...value, remerge }, workspaceId, original.note_id)).toThrow(SynthesisApiError);
});

it("公开历史恢复来源身份，不把旧模型证明显示为新生成", () => {
 const original = synthesisRevisionFixture();
 const provenance = { attempt_id: synthesisId(90), selected_revision_id: synthesisId(91) };
 const restored = { ...original, historical_republish: provenance };
 expect(decodeSynthesisRevision(restored, workspaceId, original.note_id).historicalRepublish).toEqual({ attemptId: synthesisId(90), selectedRevisionId: synthesisId(91) });
 for (const bad of [{ ...provenance, selected_revision_id: original.id }, { ...provenance, authority: {} }, { ...provenance, selected_publication_id: synthesisId(92) }]) expect(() => decodeSynthesisRevision({ ...original, historical_republish: bad }, workspaceId, original.note_id)).toThrow();
});
