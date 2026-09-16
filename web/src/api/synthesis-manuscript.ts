import { canonicalUuidPattern, hasExactKeys, isAbortError, isRecord } from "../shared/codec";
import { decodeProblem } from "./conversation";
import { strictJson } from "./exports";
import { SynthesisApi } from "./generated/apis/SynthesisApi";
import { generatedConfiguration, generatedRawResponse, generatedRequestInit } from "./generated-client";

export type ManuscriptStage = "CANDIDATE_MANUAL_CONTENT" | "WORKSPACE_MANUAL_CONTENT";
export interface ManuscriptBinding { workspaceId: string; processingId: string; humanTaskId: string; runId: string; nodeRunId: string; targetVersion: number }
export interface ManuscriptTarget { noteId: string; attemptId: string; captureId: string; stage?: ManuscriptStage; previewFingerprint: string; ready: boolean }
export interface ManuscriptSummary { binding: ManuscriptBinding; ready: boolean; submitted: boolean; targets: ManuscriptTarget[] }
export interface ManuscriptConflict { ordinal: number; base: string; current: string; proposed: string }
export interface ManuscriptReview { stage: ManuscriptStage; base: string; current: string; proposed: string; candidate: string; conflicts: ManuscriptConflict[] }
export interface ManuscriptDetail { binding: ManuscriptBinding; target: ManuscriptTarget; review?: ManuscriptReview }
export interface ManuscriptDecision { binding: ManuscriptBinding; noteId: string; attemptId: string; captureId: string; idempotencyKey: string; resolution: { stage: ManuscriptStage; previewFingerprint: string; acknowledgedOrdinals: number[]; finalContent: string } }
export class ManuscriptApiError extends Error {
  constructor(message: string, readonly status: number | null = null, readonly code = "INVALID_RESPONSE") { super(message); this.name = "ManuscriptApiError"; }
}
const invalid = (): never => { throw new ManuscriptApiError("主笔记裁决响应不符合契约，请刷新处理状态。"); };
const object = (value: unknown, keys: string[], optional: string[] = []): Record<string, unknown> => isRecord(value) && hasExactKeys(value, keys, optional) ? value : invalid();
const uuid = (v: unknown): string => typeof v === "string" && canonicalUuidPattern.test(v) ? v : invalid();
const hash = (v: unknown): string => typeof v === "string" && /^[0-9a-f]{64}$/.test(v) ? v : invalid();
const bool = (v: unknown): boolean => typeof v === "boolean" ? v : invalid();
const integer = (v: unknown): number => typeof v === "number" && Number.isSafeInteger(v) && v > 0 ? v : invalid();
const stage = (v: unknown): ManuscriptStage => v === "CANDIDATE_MANUAL_CONTENT" || v === "WORKSPACE_MANUAL_CONTENT" ? v : invalid();
export const validManuscriptText = (v: string): boolean => !v.includes("\0") && !/[\uD800-\uDBFF](?![\uDC00-\uDFFF])|(?<![\uD800-\uDBFF])[\uDC00-\uDFFF]/u.test(v) && new TextEncoder().encode(v).length <= 1024 * 1024;
const content = (v: unknown): string => typeof v === "string" && validManuscriptText(v) ? v : invalid();
export const manuscriptBindingKey = (b: ManuscriptBinding): string => [b.workspaceId, b.processingId, b.humanTaskId, b.runId, b.nodeRunId, b.targetVersion].join(":");
const binding = (value: unknown, workspaceId: string, processingId: string): ManuscriptBinding => {
  const v = object(value, ["workspace_id", "processing_id", "human_task_id", "run_id", "node_run_id", "target_version"]);
  if (uuid(v.workspace_id) !== workspaceId || uuid(v.processing_id) !== processingId) return invalid();
  return { workspaceId, processingId, humanTaskId: uuid(v.human_task_id), runId: uuid(v.run_id), nodeRunId: uuid(v.node_run_id), targetVersion: integer(v.target_version) };
};
const target = (value: unknown): ManuscriptTarget => {
  const v = object(value, ["note_id", "attempt_id", "capture_id", "preview_fingerprint", "ready"], ["stage"]);
  const result = { noteId: uuid(v.note_id), attemptId: uuid(v.attempt_id), captureId: uuid(v.capture_id), previewFingerprint: hash(v.preview_fingerprint), ready: bool(v.ready), ...(v.stage === undefined ? {} : { stage: stage(v.stage) }) };
  if (!result.ready && result.stage === undefined) return invalid();
  return result;
};
export const decodeManuscriptSummary = (value: unknown, workspaceId: string, processingId: string): ManuscriptSummary => {
  const v = object(value, ["binding", "ready", "submitted", "targets"]);
  if (!Array.isArray(v.targets) || v.targets.length === 0 || v.targets.length > 8) return invalid();
  const targets = v.targets.map(target), ready = bool(v.ready), submitted = bool(v.submitted);
  if (new Set(targets.map((t) => t.noteId)).size !== targets.length || ready !== targets.every((t) => t.ready) || submitted && !ready) return invalid();
  return { binding: binding(v.binding, workspaceId, processingId), ready, submitted, targets };
};
export const decodeManuscriptDetail = (value: unknown, summary: ManuscriptSummary, selected: ManuscriptTarget): ManuscriptDetail => {
  const v = object(value, ["binding", "target"], ["review"]), b = binding(v.binding, summary.binding.workspaceId, summary.binding.processingId), t = target(v.target);
  if (manuscriptBindingKey(b) !== manuscriptBindingKey(summary.binding) || JSON.stringify(t) !== JSON.stringify(selected)) return invalid();
  if (v.review === undefined) { if (!t.ready) return invalid(); return { binding: b, target: t }; }
  if (t.ready) return invalid();
  const r = object(v.review, ["stage", "base", "current", "proposed", "candidate", "conflicts"]);
  if (stage(r.stage) !== t.stage || !Array.isArray(r.conflicts) || r.conflicts.length === 0 || r.conflicts.length > 1024) return invalid();
  const conflicts = r.conflicts.map((entry, index) => { const c = object(entry, ["ordinal", "base", "current", "proposed"]); const ordinal = integer(c.ordinal); if (ordinal !== index + 1) return invalid(); return { ordinal, base: content(c.base), current: content(c.current), proposed: content(c.proposed) }; });
  return { binding: b, target: t, review: { stage: stage(r.stage), base: content(r.base), current: content(r.current), proposed: content(r.proposed), candidate: content(r.candidate), conflicts } };
};
const api = new SynthesisApi(generatedConfiguration);
const request = async <T>(operation: () => Promise<Response>, decode: (v: unknown) => T): Promise<T> => {
  let response: Response;
  try { response = await operation(); } catch (error) { if (isAbortError(error)) throw error; throw new ManuscriptApiError("网络结果未知，请保留原命令重试。", null, "NETWORK_ERROR"); }
  if (!response.ok) {
    try { const p = decodeProblem(strictJson(await response.text())); throw new ManuscriptApiError(p.message, response.status, p.errorCode); }
    catch (e) { if (e instanceof ManuscriptApiError) throw e; throw new ManuscriptApiError("裁决请求未完成。", response.status, "HTTP_ERROR"); }
  }
  try { if (response.status !== 200 || !response.headers.get("content-type")?.startsWith("application/json")) return invalid(); const raw = await response.text(); if (new TextEncoder().encode(raw).length > 16 * 1024 * 1024) return invalid(); return decode(strictJson(raw)); }
  catch (e) { if (isAbortError(e)) throw e; return invalid(); }
};
const wireBinding = (b: ManuscriptBinding) => ({ workspace_id: uuid(b.workspaceId), processing_id: uuid(b.processingId), human_task_id: uuid(b.humanTaskId), run_id: uuid(b.runId), node_run_id: uuid(b.nodeRunId), target_version: integer(b.targetVersion) });
export const getManuscriptSummary = (workspaceId: string, processingId: string, signal?: AbortSignal) => request(() => generatedRawResponse(api.getSynthesisManuscriptReviewRaw({ workspaceId: uuid(workspaceId), processingId: uuid(processingId) }, generatedRequestInit(signal))), (v) => decodeManuscriptSummary(v, workspaceId, processingId));
export const getManuscriptDetail = (summary: ManuscriptSummary, selected: ManuscriptTarget, signal?: AbortSignal) => request(() => generatedRawResponse(api.getSynthesisManuscriptNoteReviewRaw({ workspaceId: summary.binding.workspaceId, processingId: summary.binding.processingId, noteId: selected.noteId }, generatedRequestInit(signal))), (v) => decodeManuscriptDetail(v, summary, selected));
const boundSummary = (v: unknown, b: ManuscriptBinding): ManuscriptSummary => { const result = decodeManuscriptSummary(v, b.workspaceId, b.processingId); if (manuscriptBindingKey(result.binding) !== manuscriptBindingKey(b)) return invalid(); return result; };
export const decideManuscript = (c: ManuscriptDecision, signal?: AbortSignal, expectedTargets?: readonly ManuscriptTarget[]) => {
  const resolution = c.resolution;
  if (!validManuscriptText(resolution.finalContent) || c.idempotencyKey.length === 0 || c.idempotencyKey.length > 128 || resolution.acknowledgedOrdinals.length === 0 || resolution.acknowledgedOrdinals.length > 1024 || resolution.acknowledgedOrdinals.some((v, i) => integer(v) !== i + 1)) throw new ManuscriptApiError("请确认所有冲突并检查正文（UTF-8、无 NUL、最多 1 MiB）。", 400, "INVALID_REQUEST");
  return request(() => generatedRawResponse(api.decideSynthesisManuscriptRaw({ workspaceId: c.binding.workspaceId, processingId: c.binding.processingId, noteId: uuid(c.noteId), synthesisManuscriptDecision: { binding: wireBinding(c.binding), note_id: c.noteId, attempt_id: uuid(c.attemptId), capture_id: uuid(c.captureId), idempotency_key: c.idempotencyKey, resolution: { stage: stage(resolution.stage), preview_fingerprint: hash(resolution.previewFingerprint), acknowledged_ordinals: [...resolution.acknowledgedOrdinals], final_content: resolution.finalContent } } }, generatedRequestInit(signal))), (v) => {
    const result = boundSummary(v, c.binding);
    const selected = result.targets.find((t) => t.noteId === c.noteId);
    if (selected?.attemptId !== c.attemptId || selected.captureId !== c.captureId) return invalid();
    if (!selected.ready && (resolution.stage !== "CANDIDATE_MANUAL_CONTENT" || selected.stage !== "WORKSPACE_MANUAL_CONTENT" || selected.previewFingerprint === resolution.previewFingerprint)) return invalid();
    if (expectedTargets && (result.targets.length !== expectedTargets.length || result.targets.some((t) => !expectedTargets.some((prior) => t.noteId === prior.noteId && t.attemptId === prior.attemptId && t.captureId === prior.captureId)))) return invalid();
    return result;
  });
};

export const resumeManuscript = (b: ManuscriptBinding, signal?: AbortSignal) => request(() => generatedRawResponse(api.resumeSynthesisManuscriptRaw({ workspaceId: b.workspaceId, processingId: b.processingId, synthesisManuscriptResume: { binding: wireBinding(b) } }, generatedRequestInit(signal))), (v) => {
  const result = boundSummary(v, b);
  if (!result.ready || !result.submitted) return invalid();
  return result;
});
