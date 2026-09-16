import { SynthesisApi } from "./generated/apis/SynthesisApi";
import { generatedConfiguration } from "./generated-client";
import type { ApiResponse } from "./generated/runtime";
import { generatedRawResponse, generatedRequestInit } from "./generated-client";
import { decodeProblem } from "./conversation";
import { strictJson } from "./exports";
import { canonicalUuidPattern, hasExactKeys, isAbortError, isRecord } from "../shared/codec";
import { decodeSynthesisSourceRef, synthesisSourceIdentity, type SynthesisSourceRef, type SynthesisSourceView } from "./synthesis";
import { validManuscriptText } from "./synthesis-manuscript";

export interface SourceReviewParagraph { label: string; ordinal: number; startByte: number; endByte: number; hash: string; text: string }
export interface SourceReviewEvidence { id: string; obligations: string[]; paragraph: string; source: SynthesisSourceRef }
export interface SourceReviewTarget { noteId: string; baseRevisionId: string; targetKind: "REVISION" | "LOCAL_FILE"; fullContentHash: string; fullContent: string; paragraphs: SourceReviewParagraph[]; evidence: SourceReviewEvidence[] }
export interface SourceReview {
  id: string; workspaceId: string; originProcessingId: string; originWorkflowRunId: string; workflowRunId: string | null;
  attemptNo: number; version: number; status: "PENDING" | "PREPARED" | "RUNNING" | "REVIEWED" | "SUCCEEDED" | "REJECTED" | "STALE" | "FAILED" | "RECOVERY_REQUIRED";
  effectiveStatus: string; completed: boolean; retryable: boolean; failure: string | null; createdAt: string; completedAt: string | null;
  targets: SourceReviewTarget[]; obligationCount: number; supportedCount: number; receiptHash: string | null;
  supersedesId: string | null; latest: boolean; canRecheck: boolean; canRecover: boolean;
  recoveryWorkflowRunId: string | null; recoveryStatus: SourceReviewRecoveryStatus | null; recoveryCompletedAt: string | null;
}
export type SourceReviewRecoveryStatus = "pending" | "running" | "waiting_for_human" | "retry_wait" | "paused" | "succeeded" | "failed" | "cancelled";
export interface SourceReviewCommand { workspaceId: string; reviewId: string; originProcessingId: string; expectedVersion: number; idempotencyKey: string }
export type SourceReviewOperation = "recheck" | "recover";
export type SourceReviewScope = { workspaceId: string } & ({ processingId: string; noteId?: never; revisionId?: never } | { processingId?: never; noteId: string; revisionId: string });
export interface SourceReviewPage { items: SourceReview[]; nextAfterId: string | null }
export class SourceReviewApiError extends Error { constructor(message = "当前正文补源响应不符合契约，请保留原请求后重新读取。", readonly status: number | null = null, readonly code = "INVALID_RESPONSE") { super(message); this.name = "SourceReviewApiError"; } }
const invalid = (): never => { throw new SourceReviewApiError(); };
const object = (v: unknown, required: string[], optional: string[] = []): Record<string, unknown> => isRecord(v) && hasExactKeys(v, required, optional) ? v : invalid();
const uuid = (v: unknown): string => typeof v === "string" && canonicalUuidPattern.test(v) ? v : invalid();
const hash = (v: unknown): string => typeof v === "string" && /^[0-9a-f]{64}$/.test(v) ? v : invalid();
const integer = (v: unknown, min = 0): number => typeof v === "number" && Number.isSafeInteger(v) && v >= min ? v : invalid();
const code = (v: unknown): string => typeof v === "string" && /^[A-Z][A-Z0-9_]{0,159}$/.test(v) ? v : invalid();
const label = (v: unknown): string => typeof v === "string" && /^[A-Z0-9/]{1,32}$/.test(v) ? v : invalid();
const timestamp = (v: unknown): string => typeof v === "string" && /^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(?:\.\d{1,9})?Z$/.test(v) && Number.isFinite(Date.parse(v)) ? v : invalid();
const text = (v: unknown): string => typeof v === "string" && validManuscriptText(v) ? v : invalid();
const array = (v: unknown, max: number): unknown[] => Array.isArray(v) && v.length <= max ? v : invalid();
const evidence = (v: unknown, workspaceId: string): SourceReviewEvidence => {
  const r = object(v, ["id", "obligations", "paragraph", "source"]);
  const obligations = array(r.obligations, 65536).map(label);
  if (obligations.length === 0 || new Set(obligations).size !== obligations.length) return invalid();
  return { id: uuid(r.id), obligations, paragraph: label(r.paragraph), source: decodeSynthesisSourceRef(r.source, workspaceId) };
};
export const decodeSourceReview = (v: unknown, workspaceId: string, reviewId?: string): SourceReview => {
  const r = object(v, ["id", "workspace_id", "origin_processing_id", "origin_workflow_run_id", "workflow_run_id", "attempt_no", "version", "status", "effective_status", "completed", "retryable", "created_at", "targets", "obligation_count", "supported_count", "latest", "can_recheck", "can_recover"], ["failure", "completed_at", "receipt_hash", "supersedes_id", "recovery_workflow_run_id", "recovery_status", "recovery_completed_at"]);
  const id = uuid(r.id);
  if (uuid(r.workspace_id) !== workspaceId || reviewId !== undefined && id !== reviewId || typeof r.completed !== "boolean" || typeof r.retryable !== "boolean") return invalid();
  const status = code(r.status);
  if (!["PENDING", "PREPARED", "RUNNING", "REVIEWED", "SUCCEEDED", "REJECTED", "STALE", "FAILED", "RECOVERY_REQUIRED"].includes(status)) return invalid();
  if (typeof r.latest !== "boolean" || typeof r.can_recheck !== "boolean" || typeof r.can_recover !== "boolean" || r.can_recheck && r.can_recover || !r.latest && (r.can_recheck || r.can_recover)) return invalid();
  const supersedesId = r.supersedes_id === undefined ? null : uuid(r.supersedes_id);
  if (supersedesId === id || integer(r.attempt_no, 1) > 10) return invalid();
  const recoveryWorkflowRunId = r.recovery_workflow_run_id === undefined ? null : uuid(r.recovery_workflow_run_id);
  const recoveryStatus = decodeSourceReviewRecoveryStatus(r.recovery_status);
  const recoveryCompletedAt = r.recovery_completed_at === undefined ? null : timestamp(r.recovery_completed_at);
  if ((recoveryWorkflowRunId === null) !== (recoveryStatus === null)) return invalid();
  if (recoveryCompletedAt !== null && (recoveryWorkflowRunId === null || r.receipt_hash === undefined)) return invalid();
  const targets = array(r.targets, 128).map((entry): SourceReviewTarget => {
    const t = object(entry, ["note_id", "base_revision_id", "target_kind", "full_content_hash", "full_content", "paragraphs", "evidence"]);
    if (t.target_kind !== "REVISION" && t.target_kind !== "LOCAL_FILE") return invalid();
    const fullContent = text(t.full_content), bytes = new TextEncoder().encode(fullContent);
    const labels = new Set<string>(); let end = 0;
    const paragraphs = array(t.paragraphs, 65536).map((value): SourceReviewParagraph => {
      const p = object(value, ["label", "ordinal", "start_byte", "end_byte", "hash"]);
      const name = label(p.label), startByte = integer(p.start_byte), endByte = integer(p.end_byte, 1);
      if (labels.has(name) || startByte < end || endByte <= startByte || endByte > bytes.length) return invalid();
      labels.add(name); end = endByte;
      let paragraphText: string; try { paragraphText = new TextDecoder("utf-8", { fatal: true }).decode(bytes.slice(startByte, endByte)); } catch { return invalid(); }
      return { label: name, ordinal: integer(p.ordinal, 1), startByte, endByte, hash: hash(p.hash), text: paragraphText };
    });
    const items = array(t.evidence, 65536).map((v) => evidence(v, workspaceId));
    if (new Set(items.map((e) => e.id)).size !== items.length || items.some((e) => !labels.has(e.paragraph))) return invalid();
    return { noteId: uuid(t.note_id), baseRevisionId: uuid(t.base_revision_id), targetKind: t.target_kind, fullContentHash: hash(t.full_content_hash), fullContent, paragraphs, evidence: items };
  });
  if (new Set(targets.map((t) => t.noteId)).size !== targets.length) return invalid();
  const result: SourceReview = { supersedesId, latest: r.latest, canRecheck: r.can_recheck, canRecover: r.can_recover, recoveryWorkflowRunId, recoveryStatus, recoveryCompletedAt, id, workspaceId, originProcessingId: uuid(r.origin_processing_id), originWorkflowRunId: uuid(r.origin_workflow_run_id), workflowRunId: r.workflow_run_id === "" ? null : uuid(r.workflow_run_id), attemptNo: integer(r.attempt_no, 1), version: integer(r.version, 1), status: status as SourceReview["status"], effectiveStatus: code(r.effective_status), completed: r.completed, retryable: r.retryable, failure: r.failure === undefined ? null : code(r.failure), createdAt: timestamp(r.created_at), completedAt: r.completed_at === undefined ? null : timestamp(r.completed_at), receiptHash: r.receipt_hash === undefined ? null : hash(r.receipt_hash), targets, obligationCount: integer(r.obligation_count), supportedCount: integer(r.supported_count) };
  if (result.supportedCount > result.obligationCount || result.completed && ((result.status !== "SUCCEEDED" || result.completedAt === null) && result.recoveryCompletedAt === null || result.effectiveStatus !== "CURRENT" || result.receiptHash === null || result.obligationCount === 0 || result.supportedCount !== result.obligationCount || targets.length === 0 || targets.some((t) => t.evidence.length === 0))) return invalid();
  return result;
};
const decodeSourceReviewRecoveryStatus = (v: unknown): SourceReviewRecoveryStatus | null => {
  if (v === undefined) return null;
  if (typeof v !== "string") return invalid();
  switch (v) { case "pending": case "running": case "waiting_for_human": case "retry_wait": case "paused": case "succeeded": case "failed": case "cancelled": return v; default: return invalid(); }
};
export const sourceReviewIsActive = (review: SourceReview): boolean => ["PENDING", "PREPARED", "RUNNING"].includes(review.status) || review.status === "REVIEWED" && !review.canRecover && review.recoveryStatus === null || review.recoveryStatus !== null && ["pending", "running", "retry_wait"].includes(review.recoveryStatus);
export const decodeSourceReviewCommandResult = (v: unknown, command: SourceReviewCommand, operation: SourceReviewOperation): SourceReview => {
  const result = decodeSourceReview(v, command.workspaceId);
  if (result.originProcessingId !== command.originProcessingId || (operation === "recover" ? result.id !== command.reviewId : result.id !== command.reviewId && result.supersedesId !== command.reviewId)) return invalid();
  return result;
};
export const decodeSourceReviewPage = (v: unknown, scope: SourceReviewScope, afterId: string | null = null, limit = 20): SourceReviewPage => {
  const processing = scope.processingId !== undefined;
  const r = object(v, ["workspace_id", "items", ...(processing ? ["processing_id"] : ["note_id", "revision_id"])], ["next_after_id"]);
  if (uuid(r.workspace_id) !== scope.workspaceId || (processing ? uuid(r.processing_id) !== scope.processingId : uuid(r.note_id) !== scope.noteId || uuid(r.revision_id) !== scope.revisionId)) return invalid();
  let previous = afterId ?? "";
  const items = array(r.items, limit).map((v) => { const review = decodeSourceReview(v, scope.workspaceId); if (review.id <= previous || (processing ? review.originProcessingId !== scope.processingId : review.targets.length === 0 || review.targets.some((t) => t.noteId !== scope.noteId || t.baseRevisionId !== scope.revisionId))) return invalid(); previous = review.id; return review; });
  const nextAfterId = r.next_after_id === undefined ? null : uuid(r.next_after_id);
  if (nextAfterId !== null && (items.length !== limit || nextAfterId !== previous)) return invalid();
  return { items, nextAfterId };
};
export const decodeSourceReviewEvidence = (v: unknown, workspaceId: string, reviewId: string, expected: SourceReviewEvidence): SynthesisSourceView => {
  const r = object(v, ["workspace_id", "review_id", "evidence", "availability", "text"], ["snapshot_text"]);
  const actual = evidence(r.evidence, workspaceId);
  if (uuid(r.workspace_id) !== workspaceId || uuid(r.review_id) !== reviewId || actual.id !== expected.id || actual.paragraph !== expected.paragraph || actual.obligations.length !== expected.obligations.length || actual.obligations.some((label) => !expected.obligations.includes(label)) || synthesisSourceIdentity(actual.source) !== synthesisSourceIdentity(expected.source) || actual.source.title !== expected.source.title) return invalid();
  if (r.availability !== "AVAILABLE" && r.availability !== "STALE" && r.availability !== "UNAVAILABLE") return invalid();
  const content = r.text === null ? null : text(r.text), snapshotText = r.snapshot_text === undefined ? null : text(r.snapshot_text);
  if ((r.availability === "AVAILABLE") !== (content !== null) || r.availability === "AVAILABLE" && snapshotText !== null) return invalid();
  return { reference: actual.source, availability: r.availability, text: content, snapshotText };
};

// 组件仅消费从生成的 Raw 客户端解码得到的投影。
export interface SourceReviewClient {
  list(scope: SourceReviewScope, afterId: string | null, signal?: AbortSignal): Promise<SourceReviewPage>;
  get(workspaceId: string, reviewId: string, signal?: AbortSignal): Promise<SourceReview>;
  open(workspaceId: string, reviewId: string, evidence: SourceReviewEvidence, signal?: AbortSignal): Promise<SynthesisSourceView>;
  recheck(command: SourceReviewCommand, signal?: AbortSignal): Promise<SourceReview>;
  recover(command: SourceReviewCommand, signal?: AbortSignal): Promise<SourceReview>;
}


export interface SourceReviewRawAPI {
  listSynthesisProcessingSourceReviewsRaw(params: { workspaceId: string; processingId: string; limit: number; afterId?: string }, init: RequestInit): Promise<ApiResponse<unknown>>;
  listSynthesisRevisionSourceReviewsRaw(params: { workspaceId: string; noteId: string; revisionId: string; limit: number; afterId?: string }, init: RequestInit): Promise<ApiResponse<unknown>>;
  getSynthesisSourceReviewRaw(params: { workspaceId: string; reviewId: string }, init: RequestInit): Promise<ApiResponse<unknown>>;
  openSynthesisSourceReviewEvidenceRaw(params: { workspaceId: string; reviewId: string; evidenceId: string }, init: RequestInit): Promise<ApiResponse<unknown>>;
  recheckSynthesisSourceReviewRaw(params: { workspaceId: string; reviewId: string; synthesisSourceReviewCommand: { expected_version: number; idempotency_key: string } }, init: RequestInit): Promise<ApiResponse<unknown>>;
  recoverSynthesisSourceReviewRaw(params: { workspaceId: string; reviewId: string; synthesisSourceReviewCommand: { expected_version: number; idempotency_key: string } }, init: RequestInit): Promise<ApiResponse<unknown>>;
}
const requestReview = async <T>(operation: Promise<ApiResponse<unknown>>, decode: (v: unknown) => T): Promise<T> => {
  let response: Response;
  try { response = await generatedRawResponse(operation); } catch (e) { if (isAbortError(e)) throw e; throw new SourceReviewApiError("请求结果未知，请保留原请求并重新读取。", null, "NETWORK_ERROR"); }
  if (!response.ok) { try { const problem = decodeProblem(strictJson(await response.text())); throw new SourceReviewApiError(problem.message, response.status, problem.errorCode); } catch (e) { if (e instanceof SourceReviewApiError) throw e; throw new SourceReviewApiError("响应无法确认，请保留原请求并重新读取。", null, "INVALID_RESPONSE"); } }
  if (response.status !== 200 || !response.headers.get("content-type")?.startsWith("application/json")) return invalid();
  const raw = await response.text(); if (new TextEncoder().encode(raw).length > 32 * 1024 * 1024) return invalid();
  return decode(strictJson(raw));
};
/** 生成后的具体 SynthesisApi 实现此端口。 */
export const createSourceReviewClient = (api: SourceReviewRawAPI): SourceReviewClient => ({
  list: (scope, afterId, signal) => {
    const common = { workspaceId: uuid(scope.workspaceId), limit: 20, ...(afterId === null ? {} : { afterId: uuid(afterId) }) };
    const operation = scope.processingId !== undefined ? api.listSynthesisProcessingSourceReviewsRaw({ ...common, processingId: uuid(scope.processingId) }, generatedRequestInit(signal)) : api.listSynthesisRevisionSourceReviewsRaw({ ...common, noteId: uuid(scope.noteId), revisionId: uuid(scope.revisionId) }, generatedRequestInit(signal));
    return requestReview(operation, (v) => decodeSourceReviewPage(v, scope, afterId));
  },
  get: (workspaceId, reviewId, signal) => requestReview(api.getSynthesisSourceReviewRaw({ workspaceId: uuid(workspaceId), reviewId: uuid(reviewId) }, generatedRequestInit(signal)), (v) => decodeSourceReview(v, workspaceId, reviewId)),
  open: (workspaceId, reviewId, expected, signal) => requestReview(api.openSynthesisSourceReviewEvidenceRaw({ workspaceId: uuid(workspaceId), reviewId: uuid(reviewId), evidenceId: uuid(expected.id) }, generatedRequestInit(signal)), (v) => decodeSourceReviewEvidence(v, workspaceId, reviewId, expected)),
  recheck: (command, signal) => requestReview(api.recheckSynthesisSourceReviewRaw(commandParams(command), generatedRequestInit(signal)), (v) => decodeSourceReviewCommandResult(v, command, "recheck")),
  recover: (command, signal) => requestReview(api.recoverSynthesisSourceReviewRaw(commandParams(command), generatedRequestInit(signal)), (v) => decodeSourceReviewCommandResult(v, command, "recover")),
});

const commandKey = (v: unknown): string => typeof v === "string" && v.length > 0 && v === v.trim() && validManuscriptText(v) && new TextEncoder().encode(v).length <= 128 ? v : invalid();
const commandParams = (c: SourceReviewCommand) => ({ workspaceId: uuid(c.workspaceId), reviewId: uuid(c.reviewId), synthesisSourceReviewCommand: { expected_version: integer(c.expectedVersion, 1), idempotency_key: commandKey(c.idempotencyKey) } });

export interface PendingSourceReviewCommand { command: SourceReviewCommand; operation: SourceReviewOperation; resultReviewId: string | null }
const pendingKeys = ["sr_workspace", "sr_scope", "sr_review", "sr_processing", "sr_version", "sr_key", "sr_action", "sr_result"];
const scopeKey = (scope: SourceReviewScope) => scope.processingId !== undefined ? `p:${scope.processingId}` : `n:${scope.noteId}:${scope.revisionId}`;
export const readPendingSourceReviewCommand = (params: URLSearchParams, scope: SourceReviewScope): PendingSourceReviewCommand | null => {
  if (!pendingKeys.some((k) => params.has(k))) return null;
  for (const name of pendingKeys) if (params.getAll(name).length !== (name === "sr_result" && !params.has(name) ? 0 : 1)) return invalid();
  const operation = params.get("sr_action");
  if (operation !== "recheck" && operation !== "recover") return invalid();
  const version = params.get("sr_version");
  if (version === null || !/^[1-9][0-9]*$/.test(version)) return invalid();
  const command = { workspaceId: uuid(params.get("sr_workspace")), reviewId: uuid(params.get("sr_review")), originProcessingId: uuid(params.get("sr_processing")), expectedVersion: integer(Number(version), 1), idempotencyKey: commandKey(params.get("sr_key")) };
  const savedScope = params.get("sr_scope");
  if (savedScope === null || !/^p:[0-9a-f-]{36}$|^n:[0-9a-f-]{36}:[0-9a-f-]{36}$/.test(savedScope)) return invalid();
  if (command.workspaceId !== scope.workspaceId || savedScope !== scopeKey(scope)) return null;
  if (scope.processingId !== undefined && command.originProcessingId !== scope.processingId) return invalid();
  return { command, operation, resultReviewId: params.has("sr_result") ? uuid(params.get("sr_result")) : null };
};
export const writePendingSourceReviewCommand = (params: URLSearchParams, scope: SourceReviewScope, pending: PendingSourceReviewCommand | null): URLSearchParams => {
  const next = new URLSearchParams(params); pendingKeys.forEach((k) => next.delete(k));
  if (pending) { const c = pending.command; next.set("sr_workspace", c.workspaceId); next.set("sr_scope", scopeKey(scope)); next.set("sr_review", c.reviewId); next.set("sr_processing", c.originProcessingId); next.set("sr_version", String(c.expectedVersion)); next.set("sr_key", c.idempotencyKey); next.set("sr_action", pending.operation); if (pending.resultReviewId) next.set("sr_result", pending.resultReviewId); }
  return next;
};

export const sourceReviewClient = createSourceReviewClient(new SynthesisApi(generatedConfiguration));
