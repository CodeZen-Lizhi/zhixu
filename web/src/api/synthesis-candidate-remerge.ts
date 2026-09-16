import { canonicalUuidPattern, hasExactKeys, isAbortError, isRecord } from "../shared/codec";
import { decodeProblem } from "./conversation";
import { strictJson } from "./exports";
import { SynthesisApi } from "./generated/apis/SynthesisApi";
import { generatedConfiguration, generatedRawResponse, generatedRequestInit } from "./generated-client";
import { validManuscriptText, type ManuscriptReview } from "./synthesis-manuscript";

export interface CandidateRemergeTarget {
  workspace_id: string; note_id: string; expected_revision_id: string; expected_note_version: number;
  expected_document_id: string; expected_document_version: number; expected_publication_id: string;
  expected_proposal_id: string; expected_proposal_revision_id: string; expected_proposal_version: number;
}
export type CandidateRemergeBegin = CandidateRemergeTarget & { idempotency_key: string };
export interface CandidateRemergeApply {
  workspace_id: string; note_id: string; attempt_id: string; idempotency_key: string;
  resolution?: { stage: "WORKSPACE_MANUAL_CONTENT"; preview_fingerprint: string; acknowledged_ordinals: number[]; final_content: string };
}
export interface CandidateRemergeResult { revisionId: string; articleRevisionId: string; publicationId?: string; proposalId?: string; proposalRevisionId?: string }
export type CandidateRemergeReview = {
  workspaceId: string; noteId: string; attemptId: string; previewFingerprint: string; replayed: boolean;
} & (
  { state: "READY"; candidate: string; review?: never; result?: never } |
  { state: "CONFLICTS"; review: ManuscriptReview; candidate?: never; result?: never } |
  { state: "APPLIED"; result: CandidateRemergeResult; candidate?: never; review?: never }
);
export class CandidateRemergeApiError extends Error {
  constructor(message: string, readonly status: number | null = null, readonly code = "INVALID_RESPONSE") { super(message); this.name = "CandidateRemergeApiError"; }
}
const invalid = (): never => { throw new CandidateRemergeApiError("重新合并响应不符合契约，请按原请求重新读取。"); };
const object = (v: unknown, required: string[], optional: string[] = []): Record<string, unknown> => isRecord(v) && hasExactKeys(v, required, optional) ? v : invalid();
const uuid = (v: unknown): string => typeof v === "string" && canonicalUuidPattern.test(v) ? v : invalid();
const hash = (v: unknown): string => typeof v === "string" && /^[0-9a-f]{64}$/.test(v) ? v : invalid();
const integer = (v: unknown): number => typeof v === "number" && Number.isSafeInteger(v) && v > 0 ? v : invalid();
const text = (v: unknown): string => typeof v === "string" && validManuscriptText(v) ? v : invalid();
export const validRemergeKey = (v: string): boolean => v.length > 0 && new TextEncoder().encode(v).length <= 200 && v.trim() === v && validManuscriptText(v);
const key = (v: string): string => validRemergeKey(v) ? v : invalid();
const targetIds = ["workspace_id", "note_id", "expected_revision_id", "expected_document_id", "expected_publication_id", "expected_proposal_id", "expected_proposal_revision_id"];
const targetVersions = ["expected_note_version", "expected_document_version", "expected_proposal_version"];
export const decodeCandidateRemergeTarget = (v: unknown, workspaceId: string, noteId: string): CandidateRemergeTarget => {
  const r = object(v, [...targetIds, ...targetVersions]);
  for (const name of targetIds) uuid(r[name]); for (const name of targetVersions) integer(r[name]);
  if (r.workspace_id !== workspaceId || r.note_id !== noteId) return invalid();
  return { workspace_id: workspaceId, note_id: noteId, expected_revision_id: uuid(r.expected_revision_id), expected_note_version: integer(r.expected_note_version), expected_document_id: uuid(r.expected_document_id), expected_document_version: integer(r.expected_document_version), expected_publication_id: uuid(r.expected_publication_id), expected_proposal_id: uuid(r.expected_proposal_id), expected_proposal_revision_id: uuid(r.expected_proposal_revision_id), expected_proposal_version: integer(r.expected_proposal_version) };
};
export const decodeCandidateRemergeReview = (value: unknown, workspaceId: string, noteId: string, attemptId?: string): CandidateRemergeReview => {
  const r = object(value, ["workspace_id", "note_id", "attempt_id", "state", "preview_fingerprint", "replayed"], ["review", "result", "candidate"]);
  if (uuid(r.workspace_id) !== workspaceId || uuid(r.note_id) !== noteId || attemptId !== undefined && uuid(r.attempt_id) !== attemptId || typeof r.replayed !== "boolean") return invalid();
  const common = { workspaceId, noteId, attemptId: uuid(r.attempt_id), previewFingerprint: hash(r.preview_fingerprint), replayed: r.replayed };
  if (r.state === "READY") { if (r.review !== undefined || r.result !== undefined) return invalid(); return { ...common, state: r.state, candidate: text(r.candidate) }; }
  if (r.state === "CONFLICTS") {
    if (r.result !== undefined || r.candidate !== undefined) return invalid();
    const v = object(r.review, ["stage", "base", "current", "proposed", "candidate", "conflicts"]);
    if (v.stage !== "WORKSPACE_MANUAL_CONTENT" || !Array.isArray(v.conflicts) || v.conflicts.length === 0 || v.conflicts.length > 1024) return invalid();
    const conflicts = v.conflicts.map((entry, i) => { const c = object(entry, ["ordinal", "base", "current", "proposed"]); if (integer(c.ordinal) !== i + 1) return invalid(); return { ordinal: i + 1, base: text(c.base), current: text(c.current), proposed: text(c.proposed) }; });
    return { ...common, state: r.state, review: { stage: v.stage, base: text(v.base), current: text(v.current), proposed: text(v.proposed), candidate: text(v.candidate), conflicts } };
  }
  if (r.state === "APPLIED") {
    if (r.review !== undefined || r.candidate !== undefined) return invalid();
    const v = object(r.result, ["revision_id", "article_revision_id"], ["publication_id", "proposal_id", "proposal_revision_id"]);
    if ((v.proposal_id === undefined) !== (v.proposal_revision_id === undefined) || v.proposal_id !== undefined && v.publication_id === undefined) return invalid();
    return { ...common, state: r.state, result: { revisionId: uuid(v.revision_id), articleRevisionId: uuid(v.article_revision_id), ...(v.publication_id === undefined ? {} : { publicationId: uuid(v.publication_id) }), ...(v.proposal_id === undefined ? {} : { proposalId: uuid(v.proposal_id), proposalRevisionId: uuid(v.proposal_revision_id) }) } };
  }
  return invalid();
};
const api = new SynthesisApi(generatedConfiguration);
const request = async <T>(operation: () => Promise<Response>, decode: (v: unknown) => T): Promise<T> => {
  let response: Response;
  try { response = await operation(); } catch (e) { if (isAbortError(e)) throw e; throw new CandidateRemergeApiError("请求结果未知，请保留原命令与恢复链接。", null, "NETWORK_ERROR"); }
  if (!response.ok) { try { const p = decodeProblem(strictJson(await response.text())); throw new CandidateRemergeApiError(p.message, response.status, p.errorCode); } catch (e) { if (e instanceof CandidateRemergeApiError) throw e; throw new CandidateRemergeApiError("请求未完成，请保留编辑后重新读取。", response.status, "HTTP_ERROR"); } }
  try { if (response.status !== 200 || !response.headers.get("content-type")?.startsWith("application/json")) return invalid(); const raw = await response.text(); if (new TextEncoder().encode(raw).length > 16 * 1024 * 1024) return invalid(); return decode(strictJson(raw)); } catch (e) { if (isAbortError(e)) throw e; return invalid(); }
};
export const beginCandidateRemerge = (c: CandidateRemergeBegin, signal?: AbortSignal) => request(() => generatedRawResponse(api.beginSynthesisCandidateRemergeRaw({ workspaceId: uuid(c.workspace_id), noteId: uuid(c.note_id), synthesisCandidateRemergeBegin: { ...c, idempotency_key: key(c.idempotency_key) } }, generatedRequestInit(signal))), (v) => decodeCandidateRemergeReview(v, c.workspace_id, c.note_id));
export const readCandidateRemerge = (workspaceId: string, noteId: string, beginKey: string, signal?: AbortSignal) => request(() => generatedRawResponse(api.getSynthesisCandidateRemergeRaw({ workspaceId: uuid(workspaceId), noteId: uuid(noteId), key: key(beginKey) }, generatedRequestInit(signal))), (v) => decodeCandidateRemergeReview(v, workspaceId, noteId));
export const applyCandidateRemerge = (c: CandidateRemergeApply, signal?: AbortSignal) => {
  const resolution = c.resolution;
  if (resolution && (!validManuscriptText(resolution.final_content) || !/^[0-9a-f]{64}$/.test(resolution.preview_fingerprint) || resolution.acknowledged_ordinals.length < 1 || resolution.acknowledged_ordinals.length > 1024 || resolution.acknowledged_ordinals.some((n, i) => n !== i + 1))) return Promise.reject(new CandidateRemergeApiError("请确认每一处冲突并检查完整正文。", 400, "INVALID_REQUEST"));
  return request(() => generatedRawResponse(api.applySynthesisCandidateRemergeRaw({ workspaceId: uuid(c.workspace_id), noteId: uuid(c.note_id), attemptId: uuid(c.attempt_id), synthesisCandidateRemergeApply: { ...c, idempotency_key: key(c.idempotency_key) } }, generatedRequestInit(signal))), (v) => { const r = decodeCandidateRemergeReview(v, c.workspace_id, c.note_id, c.attempt_id); if (r.state !== "APPLIED") return invalid(); return r; });
};

export const getCandidateRemergeTarget = (workspaceId: string, noteId: string, signal?: AbortSignal) => request(() => generatedRawResponse(api.getSynthesisCandidateRemergeTargetRaw({ workspaceId: uuid(workspaceId), noteId: uuid(noteId) }, generatedRequestInit(signal))), (v) => decodeCandidateRemergeTarget(v, workspaceId, noteId));
export const resumeCandidateRemerge = (workspaceId: string, noteId: string, attemptId: string, signal?: AbortSignal) => request(() => generatedRawResponse(api.resumeSynthesisCandidateRemergeRaw({ workspaceId: uuid(workspaceId), noteId: uuid(noteId), attemptId: uuid(attemptId), body: {} }, generatedRequestInit(signal))), (v) => decodeCandidateRemergeReview(v, workspaceId, noteId, attemptId));
