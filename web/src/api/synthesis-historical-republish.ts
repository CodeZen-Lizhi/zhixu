import { canonicalUuidPattern, hasExactKeys, isAbortError, isRecord } from "../shared/codec";
import { decodeProblem } from "./conversation";
import { strictJson } from "./exports";
import { SynthesisApi } from "./generated/apis/SynthesisApi";
import { generatedConfiguration, generatedRawResponse, generatedRequestInit } from "./generated-client";
import { validManuscriptText } from "./synthesis-manuscript";

import type { CandidateRemergeResult } from "./synthesis-candidate-remerge";
export interface HistoricalRepublishTarget {
 workspace_id: string; note_id: string; selected_revision_id: string; selected_projection_hash: string;
 selected_publication_id?: string; selected_proposal_commit_id?: string;
 expected_revision_id: string; expected_note_version: number; expected_document_id: string; expected_document_version: number;
 expected_published_revision_id?: string; expected_publication_id?: string; expected_proposal_id?: string; expected_proposal_revision_id?: string; expected_proposal_version?: number; requires_retirement: boolean;
}
export type HistoricalRepublishBegin = HistoricalRepublishTarget & { idempotency_key: string };
export interface HistoricalRepublishApply { workspace_id: string; note_id: string; attempt_id: string; idempotency_key: string; preview_fingerprint: string; confirm_exact_restore: true; retire_current_candidate: boolean }
export interface HistoricalRepublishReview { currentScope: { id: string; scopeVersion: number; topics: string[]; audiences: string[]; description: string } | null; workspaceId: string; noteId: string; attemptId: string; state: "READY" | "APPLIED"; target: HistoricalRepublishTarget; candidate: string; currentContent: string; publishedContent: string; previewFingerprint: string; warnings: { code: string; sourceId?: string; sourceVersionId?: string }[]; result?: CandidateRemergeResult; replayed: boolean }
export class HistoricalRepublishApiError extends Error {
  constructor(message: string, readonly status: number | null = null, readonly code = "INVALID_RESPONSE") { super(message); this.name = "HistoricalRepublishApiError"; }
}
const invalid = (): never => { throw new HistoricalRepublishApiError("历史恢复响应不符合契约，请按原请求重新读取。"); };
const object = (v: unknown, required: string[], optional: string[] = []): Record<string, unknown> => isRecord(v) && hasExactKeys(v, required, optional) ? v : invalid();
const uuid = (v: unknown): string => typeof v === "string" && canonicalUuidPattern.test(v) ? v : invalid();
const hash = (v: unknown): string => typeof v === "string" && /^[0-9a-f]{64}$/.test(v) ? v : invalid();
const integer = (v: unknown): number => typeof v === "number" && Number.isSafeInteger(v) && v > 0 ? v : invalid();
const text = (v: unknown): string => typeof v === "string" && validManuscriptText(v) ? v : invalid();
export const validRemergeKey = (v: string): boolean => v.length > 0 && new TextEncoder().encode(v).length <= 200 && v.trim() === v && validManuscriptText(v);
const key = (v: string): string => validRemergeKey(v) ? v : invalid();

const requiredIds = ["workspace_id", "note_id", "selected_revision_id", "expected_revision_id", "expected_document_id"];
const optionalIds = ["selected_publication_id", "selected_proposal_commit_id", "expected_published_revision_id", "expected_publication_id", "expected_proposal_id", "expected_proposal_revision_id"];
export const decodeHistoricalRepublishTarget = (value: unknown, workspaceId: string, noteId: string, selectedId?: string): HistoricalRepublishTarget => {
 const r = object(value, [...requiredIds, "selected_projection_hash", "expected_note_version", "expected_document_version", "requires_retirement"], [...optionalIds, "expected_proposal_version"]);
 for (const field of requiredIds) uuid(r[field]); for (const field of optionalIds) if (r[field] !== undefined) uuid(r[field]);
 if (r.workspace_id !== workspaceId || r.note_id !== noteId || selectedId !== undefined && r.selected_revision_id !== selectedId || typeof r.requires_retirement !== "boolean" || (r.selected_publication_id === undefined) !== (r.selected_proposal_commit_id === undefined) || (r.expected_proposal_id === undefined) !== (r.expected_proposal_revision_id === undefined) || (r.expected_proposal_id === undefined) !== (r.expected_proposal_version === undefined)) return invalid();
 return { workspace_id: workspaceId, note_id: noteId, selected_revision_id: uuid(r.selected_revision_id), selected_projection_hash: hash(r.selected_projection_hash), expected_revision_id: uuid(r.expected_revision_id), expected_note_version: integer(r.expected_note_version), expected_document_id: uuid(r.expected_document_id), expected_document_version: integer(r.expected_document_version), requires_retirement: r.requires_retirement, ...Object.fromEntries(optionalIds.filter((f) => r[f] !== undefined).map((f) => [f, uuid(r[f])])), ...(r.expected_proposal_version === undefined ? {} : { expected_proposal_version: integer(r.expected_proposal_version) }) };
};
export const decodeHistoricalRepublishReview = (value: unknown, workspaceId: string, noteId: string, selectedId: string, attemptId?: string): HistoricalRepublishReview => {
 const r = object(value, ["current_scope", "workspace_id", "note_id", "attempt_id", "state", "target", "candidate", "current_content", "published_content", "preview_fingerprint", "warnings", "replayed"], ["result"]);
 if (uuid(r.workspace_id) !== workspaceId || uuid(r.note_id) !== noteId || attemptId !== undefined && uuid(r.attempt_id) !== attemptId || (r.state !== "READY" && r.state !== "APPLIED") || typeof r.replayed !== "boolean" || !Array.isArray(r.warnings) || r.warnings.length > 1024 || (r.state === "APPLIED") !== (r.result !== undefined)) return invalid();
 let currentScope: HistoricalRepublishReview["currentScope"] = null;
 if (r.current_scope !== null) {
  const c = object(r.current_scope, ["id", "scope_version", "scope"]), scope = object(c.scope, ["topics", "audiences", "description"]);
  const strings = (value: unknown, max: number): string[] => { if (!Array.isArray(value) || value.length < 1 || value.length > max) return invalid(); return value.map((v: unknown) => { const s = text(v); return s.length > 0 && new TextEncoder().encode(s).length <= 256 ? s : invalid(); }); };
  const description = text(scope.description); if (!description || new TextEncoder().encode(description).length > 2048) return invalid();
  currentScope = { id: uuid(c.id), scopeVersion: integer(c.scope_version), topics: strings(scope.topics, 64), audiences: strings(scope.audiences, 32), description };
 }
 let result: CandidateRemergeResult | undefined;
 if (r.result !== undefined) { const v = object(r.result, ["revision_id", "article_revision_id"], ["publication_id", "proposal_id", "proposal_revision_id"]); if ((v.proposal_id === undefined) !== (v.proposal_revision_id === undefined) || v.proposal_id !== undefined && v.publication_id === undefined) return invalid(); result = { revisionId: uuid(v.revision_id), articleRevisionId: uuid(v.article_revision_id), ...(v.publication_id === undefined ? {} : { publicationId: uuid(v.publication_id) }), ...(v.proposal_id === undefined ? {} : { proposalId: uuid(v.proposal_id), proposalRevisionId: uuid(v.proposal_revision_id) }) }; }
 return { currentScope, workspaceId, noteId, attemptId: uuid(r.attempt_id), state: r.state, target: decodeHistoricalRepublishTarget(r.target, workspaceId, noteId, selectedId), candidate: text(r.candidate), currentContent: text(r.current_content), publishedContent: text(r.published_content), previewFingerprint: hash(r.preview_fingerprint), warnings: r.warnings.map((value) => { const w = object(value, ["code"], ["source_id", "source_version_id"]); if (typeof w.code !== "string" || w.code.length < 1 || w.code.length > 128) return invalid(); return { code: w.code, ...(w.source_id === undefined ? {} : { sourceId: uuid(w.source_id) }), ...(w.source_version_id === undefined ? {} : { sourceVersionId: uuid(w.source_version_id) }) }; }), ...(result ? { result } : {}), replayed: r.replayed };
};
const api = new SynthesisApi(generatedConfiguration);
const request = async <T>(operation: () => Promise<Response>, decode: (v: unknown) => T): Promise<T> => {
  let response: Response;
  try { response = await operation(); } catch (e) { if (isAbortError(e)) throw e; throw new HistoricalRepublishApiError("请求结果未知，请保留原命令与恢复链接。", null, "NETWORK_ERROR"); }
  if (!response.ok) { try { const p = decodeProblem(strictJson(await response.text())); throw new HistoricalRepublishApiError(p.message, response.status, p.errorCode); } catch (e) { if (e instanceof HistoricalRepublishApiError) throw e; throw new HistoricalRepublishApiError("请求未完成，请保留编辑后重新读取。", response.status, "HTTP_ERROR"); } }
  try { if (response.status !== 200 || !response.headers.get("content-type")?.startsWith("application/json")) return invalid(); const raw = await response.text(); if (new TextEncoder().encode(raw).length > 16 * 1024 * 1024) return invalid(); return decode(strictJson(raw)); } catch (e) { if (isAbortError(e)) throw e; return invalid(); }
};

export const getHistoricalRepublishTarget = (workspaceId: string, noteId: string, selectedId: string, signal?: AbortSignal) => request(() => generatedRawResponse(api.getSynthesisHistoricalRepublishTargetRaw({ workspaceId: uuid(workspaceId), noteId: uuid(noteId), selectedRevisionId: uuid(selectedId) }, generatedRequestInit(signal))), (v) => decodeHistoricalRepublishTarget(v, workspaceId, noteId, selectedId));
export const beginHistoricalRepublish = (c: HistoricalRepublishBegin, signal?: AbortSignal) => request(() => generatedRawResponse(api.beginSynthesisHistoricalRepublishRaw({ workspaceId: uuid(c.workspace_id), noteId: uuid(c.note_id), synthesisHistoricalRepublishBegin: { ...c, idempotency_key: key(c.idempotency_key) } }, generatedRequestInit(signal))), (v) => decodeHistoricalRepublishReview(v, c.workspace_id, c.note_id, c.selected_revision_id));
export const readHistoricalRepublish = (workspaceId: string, noteId: string, selectedId: string, beginKey: string, signal?: AbortSignal) => request(() => generatedRawResponse(api.getSynthesisHistoricalRepublishRaw({ workspaceId: uuid(workspaceId), noteId: uuid(noteId), key: key(beginKey) }, generatedRequestInit(signal))), (v) => decodeHistoricalRepublishReview(v, workspaceId, noteId, selectedId));
export const applyHistoricalRepublish = (c: HistoricalRepublishApply, selectedId: string, signal?: AbortSignal) => request(() => generatedRawResponse(api.applySynthesisHistoricalRepublishRaw({ workspaceId: uuid(c.workspace_id), noteId: uuid(c.note_id), attemptId: uuid(c.attempt_id), synthesisHistoricalRepublishApply: { ...c, idempotency_key: key(c.idempotency_key), preview_fingerprint: hash(c.preview_fingerprint) } }, generatedRequestInit(signal))), (v) => { const r = decodeHistoricalRepublishReview(v, c.workspace_id, c.note_id, selectedId, c.attempt_id); if (r.state !== "APPLIED") return invalid(); return r; });
export const resumeHistoricalRepublish = (workspaceId: string, noteId: string, selectedId: string, attemptId: string, signal?: AbortSignal) => request(() => generatedRawResponse(api.resumeSynthesisHistoricalRepublishRaw({ workspaceId: uuid(workspaceId), noteId: uuid(noteId), attemptId: uuid(attemptId), body: {} }, generatedRequestInit(signal))), (v) => decodeHistoricalRepublishReview(v, workspaceId, noteId, selectedId, attemptId));
