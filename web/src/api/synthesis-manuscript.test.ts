import { afterEach, expect, it, vi } from "vitest";
import { decodeManuscriptDetail, decodeManuscriptSummary, decideManuscript, getManuscriptDetail, getManuscriptSummary, validManuscriptText } from "./synthesis-manuscript";
import { synthesisId as id } from "../test/synthesis-fixtures";
const binding = { workspace_id: id(1), processing_id: id(2), human_task_id: id(3), run_id: id(4), node_run_id: id(5), target_version: 2 };
const target = { note_id: id(6), attempt_id: id(7), capture_id: id(8), stage: "CANDIDATE_MANUAL_CONTENT", preview_fingerprint: "a".repeat(64), ready: false };
const wire = { binding, ready: false, submitted: false, targets: [target] };
const review = { stage: target.stage, base: "base", current: "current", proposed: "proposed", candidate: "current", conflicts: [{ ordinal: 1, base: "base", current: "current", proposed: "proposed" }] };
const json = (v: unknown) => new Response(JSON.stringify(v), { headers: { "Content-Type": "application/json" } });
afterEach(() => vi.unstubAllGlobals());
it("summary fails closed for empty/oversized/private/mismatched/readiness identities", () => {
  const decode = (v: unknown) => decodeManuscriptSummary(v, id(1), id(2));
  expect(decode(wire).targets).toHaveLength(1);
  for (const bad of [{ ...wire, targets: [] }, { ...wire, targets: Array(9).fill(target) }, { ...wire, targets: [target, target] }, { ...wire, ready: true }, { ...wire, submitted: true }, { ...wire, prepared: {} }, { ...wire, binding: { ...binding, workspace_id: id(99) } }, { ...wire, targets: [{ ...target, stage: "UNKNOWN" }] }]) expect(() => decode(bad)).toThrow();
});
it("detail binds task, selected note, attempt, capture, stage and fingerprint, rejects raw byte DTO", () => {
  const summary = decodeManuscriptSummary(wire, id(1), id(2)); const selected = summary.targets[0]; if (!selected) throw new Error("fixture");
  const detail = { binding, target, review };
  expect(decodeManuscriptDetail(detail, summary, selected).review?.base).toBe("base");
  for (const bad of [{ ...detail, binding: { ...binding, human_task_id: id(90) } }, { ...detail, target: { ...target, note_id: id(90) } }, { ...detail, target: { ...target, attempt_id: id(90) } }, { ...detail, target: { ...target, capture_id: id(90) } }, { ...detail, target: { ...target, preview_fingerprint: "b".repeat(64) } }, { ...detail, review: { ...review, stage: "WORKSPACE_MANUAL_CONTENT" } }, { ...detail, review: { ...review, conflicts: [{ Ordinal: 1, Base: "YmFzZQ==" }] } }, { ...detail, review: { ...review, base: "\ud800" } }]) expect(() => decodeManuscriptDetail(bad, summary, selected)).toThrow();
});
it("UTF-8 permits exact 1MiB/BOM, rejects NUL, unpaired surrogate and excess bytes", () => {
  expect(validManuscriptText("\ufeff中文😀")).toBe(true); expect(validManuscriptText("x".repeat(1048576))).toBe(true);
  for (const v of ["\0", "\ud800", "\udc00", "中".repeat(349526)]) expect(validManuscriptText(v)).toBe(false);
});
it("generated transport reads summary then selected detail and sends immutable route/body identities", async () => {
  const calls: { url: string; body: unknown }[] = [];
  vi.stubGlobal("fetch", vi.fn<typeof fetch>().mockImplementation((input, init) => {
    const url = typeof input === "string" ? input : input instanceof URL ? input.href : input.url; calls.push({ url, body: typeof init?.body === "string" ? JSON.parse(init.body) : null });
    return Promise.resolve(json(url.endsWith("/decisions") ? { ...wire, ready: true, submitted: true, targets: [{ ...target, ready: true }] } : url.endsWith(id(6)) ? { binding, target, review } : wire));
  }));
  const summary = await getManuscriptSummary(id(1), id(2)), selected = summary.targets[0]; if (!selected) throw new Error("fixture");
  await getManuscriptDetail(summary, selected);
  await decideManuscript({ binding: summary.binding, noteId: selected.noteId, attemptId: selected.attemptId, captureId: selected.captureId, idempotencyKey: "same-command", resolution: { stage: "CANDIDATE_MANUAL_CONTENT", previewFingerprint: selected.previewFingerprint, acknowledgedOrdinals: [1], finalContent: "合并结果" } });
  expect(calls.map((c) => c.url)).toEqual([`/api/v1/workspaces/${id(1)}/synthesis/processing/${id(2)}/manuscript-review`, `/api/v1/workspaces/${id(1)}/synthesis/processing/${id(2)}/manuscript-review/${id(6)}`, `/api/v1/workspaces/${id(1)}/synthesis/processing/${id(2)}/manuscript-review/${id(6)}/decisions`]);
  expect(calls[2]?.body).toEqual({ binding, note_id: id(6), attempt_id: id(7), capture_id: id(8), idempotency_key: "same-command", resolution: { stage: target.stage, preview_fingerprint: target.preview_fingerprint, acknowledged_ordinals: [1], final_content: "合并结果" } });
});
