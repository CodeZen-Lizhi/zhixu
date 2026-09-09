/** 合成笔记、原始来源和后台准备任务的唯一 HTTP 边界。 */
import { canonicalUuidPattern, hasExactKeys, isAbortError, isRecord } from "../shared/codec";
import { decodeProblem } from "./conversation";
import { strictJson } from "./exports";
import { SynthesisApi } from "./generated/apis/SynthesisApi";
import { InterviewApi } from "./generated/apis/InterviewApi";
import { generatedConfiguration, generatedRawResponse, generatedRequestInit } from "./generated-client";

export type SynthesisNoteStatus = "QUEUED" | "GENERATING" | "PENDING_APPROVAL" | "READY" | "FAILED" | "CONFLICT" | "CAPABILITY_UNAVAILABLE" | "RECOVERY_REQUIRED";
export type SynthesisProcessingStatus = "PENDING" | "RUNNING" | "SUCCEEDED" | "NO_CHANGE" | "SKIPPED" | "FAILED" | "RECOVERY_REQUIRED";
export type SynthesisAvailability = "AVAILABLE" | "STALE" | "UNAVAILABLE";
export interface SynthesisFailure { code: string; retryable: boolean }
export interface SynthesisSourceRef {
  source: { workspaceId: string; sourceId: string; sourceVersionId: string; contentArtifactId: string; parseProjectionId: string; contentHash: string };
  sourceSpanId: string;
  excerptHash: string;
  title: string;
}
export interface SynthesisStatement { text: string; applicability: string; sources: SynthesisSourceRef[] }
export type SynthesisItem =
  | { id: string; kind: "FACT"; fact: SynthesisStatement }
  | { id: string; kind: "CONFLICT"; conflict: { subject: string; alternatives: SynthesisStatement[] } }
  | { id: string; kind: "GAP"; gap: { question: string; context: string; sources: SynthesisSourceRef[]; resolution: SynthesisStatement | null } };
export interface SynthesisNote {
  id: string; workspaceId: string; documentId: string; topicKey: string; title: string; aliases: string[];
  currentRevisionId: string | null; version: number; status: SynthesisNoteStatus; workflowRunId: string | null;
  failure: SynthesisFailure | null; createdAt: string; updatedAt: string;
}
export interface SynthesisRevisionSummary {
  id: string; revisionNo: number; articleRevisionId: string; articleRevisionNo: number; contentHash: string; createdAt: string;
}
export interface SynthesisRevision extends SynthesisRevisionSummary {
  workspaceId: string; noteId: string; documentId: string; parentRevisionId: string | null;
  title: string; projectionHash: string; items: SynthesisItem[];
}
export interface SynthesisPublication {
  revisionId: string; articleRevisionId: string; proposalId: string; proposalRevisionId: string; contentHash: string;
}
export interface SynthesisNoteSummary {
  note: SynthesisNote; currentRevision: SynthesisRevisionSummary | null; publishedRevision: SynthesisRevisionSummary | null;
  publication: SynthesisPublication | null; itemCount: number; conflictCount: number; gapCount: number; openGapCount: number;
}
export interface SynthesisProcessing {
  id: string; workspaceId: string; sourceVersionId: string; workflowRunId: string | null; status: SynthesisProcessingStatus;
  revisionIds: string[]; failure: SynthesisFailure | null; version: number; createdAt: string; updatedAt: string; completedAt: string | null;
}
export interface SynthesisNoteDetail {
  workspaceId: string; note: SynthesisNote; currentRevision: SynthesisRevision | null; publishedRevision: SynthesisRevision | null;
  publication: SynthesisPublication | null; latestProcessing: SynthesisProcessing | null;
}
export interface SynthesisPage<T> { workspaceId: string; items: T[]; nextCursor: string | null }
export interface SynthesisSourceView { reference: SynthesisSourceRef; availability: SynthesisAvailability; text: string | null }
export interface NoteRevisionRef {
  workspaceId: string; noteId: string; revisionId: string; documentId: string; articleRevisionId: string;
  revisionNo: number; articleRevisionNo: number; contentHash: string; projectionHash: string; title: string;
}
export interface NoteItemRef { revision: NoteRevisionRef; itemId: string; itemKind: "FACT" | "CONFLICT" | "GAP" }
export interface NoteQuestionSource extends NoteItemRef { sources: SynthesisSourceRef[] }
export interface NoteInterviewOptions {
  role: string; difficulty: "FOUNDATION" | "INTERMEDIATE" | "ADVANCED"; durationMinutes: number; questionCount: number; maxFollowUps: number;
}
export interface NoteInterviewPreparation {
  id: string; workspaceId: string; noteRevision: NoteRevisionRef; status: "QUEUED" | "GENERATING" | "READY" | "FAILED" | "CAPABILITY_UNAVAILABLE" | "RECOVERY_REQUIRED";
  options: NoteInterviewOptions;
  workflowRunId: string; sessionId: string | null; failure: SynthesisFailure | null; createdAt: string; updatedAt: string;
}

export class SynthesisApiError extends Error {
  constructor(readonly code: "INVALID_REQUEST" | "INVALID_RESPONSE" | "HTTP_ERROR" | "NETWORK_ERROR", message: string,
    readonly errorCode: string = code, readonly status: number | null = null, readonly retryable = false) {
    super(message); this.name = "SynthesisApiError";
  }
}
const invalid = (): never => { throw new SynthesisApiError("INVALID_RESPONSE", "合成笔记响应不符合契约，请刷新后重试。"); };
const object = (value: unknown, keys: readonly string[]): Record<string, unknown> => {
  if (!isRecord(value) || !hasExactKeys(value, keys)) return invalid();
  return value;
};
const uuid = (value: unknown): string => typeof value === "string" && canonicalUuidPattern.test(value) ? value : invalid();
const hash = (value: unknown): string => typeof value === "string" && /^[0-9a-f]{64}$/.test(value) ? value : invalid();
const number = (value: unknown, min = 0, max = 2147483647): number => typeof value === "number" && Number.isSafeInteger(value) && value >= min && value <= max ? value : invalid();
const boolean = (value: unknown): boolean => typeof value === "boolean" ? value : invalid();
const text = (value: unknown, max = 4096, allowEmpty = false, multiline = false): string => {
  if (typeof value !== "string" || new TextEncoder().encode(value).length > max || !allowEmpty && value.trim() === "" || value.includes("\0") || !multiline && /[\u0000-\u001f\u007f]/.test(value)) return invalid();
  return value;
};
const timestamp = (value: unknown): string => {
  if (typeof value !== "string" || !/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{1,9})?Z$/.test(value) || !Number.isFinite(Date.parse(value))) return invalid();
  if (new Date(value).toISOString().slice(0, 10) !== value.slice(0, 10)) return invalid();
  return value;
};
const nullable = <T>(value: unknown, decode: (value: unknown) => T): T | null => value === null ? null : decode(value);
const array = <T>(value: unknown, decode: (value: unknown) => T, max: number, min = 0): T[] => {
  if (!Array.isArray(value) || value.length < min || value.length > max) return invalid();
  return value.map(decode);
};
const choice = <T extends string>(value: unknown, choices: readonly T[]): T => {
  for (const candidate of choices) if (candidate === value) return candidate;
  return invalid();
};
const unique = <T>(values: T[], key: (value: T) => string): T[] => new Set(values.map(key)).size === values.length ? values : invalid();
const scope = (actual: string, expected: string): void => { if (actual !== expected) invalid(); };
const failure = (value: unknown): SynthesisFailure => {
  const v = object(value, ["code", "retryable"]);
  const code = text(v.code, 128);
  if (!/^[A-Z][A-Z0-9_]*$/.test(code)) return invalid();
  return { code, retryable: boolean(v.retryable) };
};

export const decodeSynthesisSourceRef = (value: unknown, workspaceId: string): SynthesisSourceRef => {
  const v = object(value, ["source", "source_span_id", "excerpt_hash", "title"]);
  const source = object(v.source, ["workspace_id", "source_id", "source_version_id", "content_artifact_id", "parse_projection_id", "content_hash"]);
  scope(uuid(source.workspace_id), workspaceId);
  return { source: { workspaceId, sourceId: uuid(source.source_id), sourceVersionId: uuid(source.source_version_id),
    contentArtifactId: uuid(source.content_artifact_id), parseProjectionId: uuid(source.parse_projection_id), contentHash: hash(source.content_hash) },
    sourceSpanId: uuid(v.source_span_id), excerptHash: hash(v.excerpt_hash), title: text(v.title, 512) };
};
export const synthesisSourceIdentity = (reference: Omit<SynthesisSourceRef, "title">): string => JSON.stringify([
  reference.source.workspaceId, reference.source.sourceId, reference.source.sourceVersionId,
  reference.source.contentArtifactId, reference.source.parseProjectionId, reference.source.contentHash,
  reference.sourceSpanId, reference.excerptHash,
]);
const sources = (value: unknown, workspaceId: string, min = 0, max = 32): SynthesisSourceRef[] =>
  unique(array(value, (item) => decodeSynthesisSourceRef(item, workspaceId), max, min), (item) => item.sourceSpanId);
const statement = (value: unknown, workspaceId: string): SynthesisStatement => {
  const v = object(value, ["text", "applicability", "sources"]);
  return { text: text(v.text), applicability: text(v.applicability, 2048, true), sources: sources(v.sources, workspaceId, 1) };
};
const item = (value: unknown, workspaceId: string): SynthesisItem => {
  const v = object(value, ["id", "kind", "fact", "conflict", "gap"]);
  const id = uuid(v.id);
  if (v.kind === "FACT" && v.conflict === null && v.gap === null) return { id, kind: "FACT", fact: statement(v.fact, workspaceId) };
  if (v.kind === "CONFLICT" && v.fact === null && v.gap === null) {
    const c = object(v.conflict, ["subject", "alternatives"]);
    return { id, kind: "CONFLICT", conflict: { subject: text(c.subject), alternatives: array(c.alternatives, (v) => statement(v, workspaceId), 4, 2) } };
  }
  if (v.kind === "GAP" && v.fact === null && v.conflict === null) {
    const g = object(v.gap, ["question", "context", "sources", "resolution"]);
    return { id, kind: "GAP", gap: { question: text(g.question), context: text(g.context, 2048, true), sources: sources(g.sources, workspaceId), resolution: nullable(g.resolution, (v) => statement(v, workspaceId)) } };
  }
  return invalid();
};
export const synthesisItemSources = (item: SynthesisItem): SynthesisSourceRef[] => {
  switch (item.kind) {
    case "FACT": return item.fact.sources;
    case "CONFLICT": return item.conflict.alternatives.flatMap((value) => value.sources);
    case "GAP": return [...item.gap.sources, ...(item.gap.resolution?.sources ?? [])];
  }
};
const note = (value: unknown, workspaceId: string): SynthesisNote => {
  const v = object(value, ["id", "workspace_id", "document_id", "topic_key", "title", "aliases", "current_revision_id", "version", "status", "workflow_run_id", "failure", "created_at", "updated_at"]);
  scope(uuid(v.workspace_id), workspaceId);
  const result: SynthesisNote = { id: uuid(v.id), workspaceId, documentId: uuid(v.document_id), topicKey: text(v.topic_key, 256), title: text(v.title, 512),
    aliases: unique(array(v.aliases, (v) => text(v, 256), 16), (v) => v), currentRevisionId: nullable(v.current_revision_id, uuid), version: number(v.version, 1),
    status: choice(v.status, ["QUEUED", "GENERATING", "PENDING_APPROVAL", "READY", "FAILED", "CONFLICT", "CAPABILITY_UNAVAILABLE", "RECOVERY_REQUIRED"]),
    workflowRunId: nullable(v.workflow_run_id, uuid), failure: nullable(v.failure, failure), createdAt: timestamp(v.created_at), updatedAt: timestamp(v.updated_at) };
  const failed = ["FAILED", "CONFLICT", "CAPABILITY_UNAVAILABLE", "RECOVERY_REQUIRED"].includes(result.status);
  if (failed !== (result.failure !== null) || result.status === "RECOVERY_REQUIRED" && result.failure?.retryable ||
    result.status === "GENERATING" && result.workflowRunId === null || ["READY", "PENDING_APPROVAL"].includes(result.status) && result.currentRevisionId === null || Date.parse(result.updatedAt) < Date.parse(result.createdAt)) return invalid();
  return result;
};
const summaryFields = ["id", "revision_no", "article_revision_id", "article_revision_no", "content_hash", "created_at"];
const revisionSummaryFields = (v: Record<string, unknown>): SynthesisRevisionSummary => ({ id: uuid(v.id), revisionNo: number(v.revision_no, 1), articleRevisionId: uuid(v.article_revision_id), articleRevisionNo: number(v.article_revision_no, 1), contentHash: hash(v.content_hash), createdAt: timestamp(v.created_at) });
const revisionSummary = (value: unknown): SynthesisRevisionSummary => revisionSummaryFields(object(value, summaryFields));
export const decodeSynthesisRevision = (value: unknown, workspaceId: string, noteId: string): SynthesisRevision => {
  const v = object(value, [...summaryFields, "workspace_id", "note_id", "document_id", "parent_revision_id", "title", "projection_hash", "items"]);
  scope(uuid(v.workspace_id), workspaceId); scope(uuid(v.note_id), noteId);
  const items = unique(array(v.items, (v) => item(v, workspaceId), 128, 1), (v) => v.id);
  const catalog = new Map<string, string>();
  for (const reference of items.flatMap(synthesisItemSources)) {
    const encoded = synthesisSourceIdentity(reference);
    const previous = catalog.get(reference.sourceSpanId);
    if (previous !== undefined && previous !== encoded) return invalid();
    catalog.set(reference.sourceSpanId, encoded);
  }
  if (catalog.size > 256) return invalid();
  const result = { ...revisionSummaryFields(v), workspaceId, noteId, documentId: uuid(v.document_id), parentRevisionId: nullable(v.parent_revision_id, uuid), title: text(v.title, 512), projectionHash: hash(v.projection_hash), items };
  if ((result.revisionNo === 1) !== (result.parentRevisionId === null) || result.parentRevisionId === result.id) return invalid();
  return result;
};
const publication = (value: unknown, revision: SynthesisRevisionSummary | null): SynthesisPublication => {
  const v = object(value, ["revision_id", "article_revision_id", "proposal_id", "proposal_revision_id", "content_hash"]);
  const p = { revisionId: uuid(v.revision_id), articleRevisionId: uuid(v.article_revision_id), proposalId: uuid(v.proposal_id), proposalRevisionId: uuid(v.proposal_revision_id), contentHash: hash(v.content_hash) };
  if (p.revisionId !== revision?.id || p.articleRevisionId !== revision.articleRevisionId || p.contentHash !== revision.contentHash) return invalid();
  return p;
};
const verifyRevisions = (note: SynthesisNote, current: SynthesisRevisionSummary | null, published: SynthesisRevisionSummary | null): void => {
  if (note.currentRevisionId !== (current?.id ?? null) || published !== null && (current === null || published.revisionNo > current.revisionNo) || published?.id === current?.id && published !== null && current !== null && JSON.stringify(published) !== JSON.stringify(current)) invalid();
};
const noteSummary = (value: unknown, workspaceId: string): SynthesisNoteSummary => {
  const v = object(value, ["note", "current_revision", "published_revision", "publication", "item_count", "conflict_count", "gap_count", "open_gap_count"]);
  const n = note(v.note, workspaceId), current = nullable(v.current_revision, revisionSummary), published = nullable(v.published_revision, revisionSummary);
  verifyRevisions(n, current, published);
  const result = { note: n, currentRevision: current, publishedRevision: published, publication: nullable(v.publication, (v) => publication(v, current)),
    itemCount: number(v.item_count, 0, 128), conflictCount: number(v.conflict_count, 0, 128), gapCount: number(v.gap_count, 0, 128), openGapCount: number(v.open_gap_count, 0, 128) };
  if (result.conflictCount + result.gapCount > result.itemCount || result.openGapCount > result.gapCount || current === null && result.itemCount !== 0) return invalid();
  return result;
};
export const decodeSynthesisProcessing = (value: unknown, workspaceId: string): SynthesisProcessing => {
  const v = object(value, ["id", "workspace_id", "source_version_id", "workflow_run_id", "status", "revision_ids", "failure", "version", "created_at", "updated_at", "completed_at"]);
  scope(uuid(v.workspace_id), workspaceId);
  const result: SynthesisProcessing = { id: uuid(v.id), workspaceId, sourceVersionId: uuid(v.source_version_id), workflowRunId: nullable(v.workflow_run_id, uuid),
    status: choice(v.status, ["PENDING", "RUNNING", "SUCCEEDED", "NO_CHANGE", "SKIPPED", "FAILED", "RECOVERY_REQUIRED"]),
    revisionIds: unique(array(v.revision_ids, uuid, 8), (v) => v), failure: nullable(v.failure, failure), version: number(v.version, 1), createdAt: timestamp(v.created_at), updatedAt: timestamp(v.updated_at), completedAt: nullable(v.completed_at, timestamp) };
  const terminal = result.status !== "PENDING" && result.status !== "RUNNING";
  const failed = result.status === "FAILED" || result.status === "RECOVERY_REQUIRED";
  if (terminal !== (result.completedAt !== null) || failed !== (result.failure !== null) || result.status === "RECOVERY_REQUIRED" && result.failure?.retryable ||
    result.status === "RUNNING" && result.workflowRunId === null || result.status === "SUCCEEDED" && result.revisionIds.length === 0 ||
    ["NO_CHANGE", "SKIPPED"].includes(result.status) && result.revisionIds.length !== 0 || Date.parse(result.updatedAt) < Date.parse(result.createdAt) ||
    result.completedAt !== null && (Date.parse(result.completedAt) < Date.parse(result.createdAt) || Date.parse(result.completedAt) > Date.parse(result.updatedAt))) return invalid();
  return result;
};
export const decodeSynthesisNoteDetail = (value: unknown, workspaceId: string, noteId: string): SynthesisNoteDetail => {
  const v = object(value, ["workspace_id", "note", "current_revision", "published_revision", "publication", "latest_processing"]);
  scope(uuid(v.workspace_id), workspaceId);
  const n = note(v.note, workspaceId); scope(n.id, noteId);
  const current = nullable(v.current_revision, (v) => decodeSynthesisRevision(v, workspaceId, noteId));
  const published = nullable(v.published_revision, (v) => decodeSynthesisRevision(v, workspaceId, noteId));
  verifyRevisions(n, current, published);
  if (current !== null && current.documentId !== n.documentId || published !== null && published.documentId !== n.documentId) return invalid();
  return { workspaceId, note: n, currentRevision: current, publishedRevision: published,
    publication: nullable(v.publication, (v) => publication(v, current)), latestProcessing: nullable(v.latest_processing, (v) => decodeSynthesisProcessing(v, workspaceId)) };
};
const page = <T>(value: unknown, workspaceId: string, decode: (v: unknown) => T, limit: number, noteId?: string): SynthesisPage<T> => {
  const v = object(value, ["workspace_id", "items", "next_cursor", ...(noteId === undefined ? [] : ["note_id"])]);
  scope(uuid(v.workspace_id), workspaceId); if (noteId !== undefined) scope(uuid(v.note_id), noteId);
  const result = { workspaceId, items: array(v.items, decode, limit), nextCursor: nullable(v.next_cursor, (v) => text(v, 2048)) };
  if (result.nextCursor !== null && result.items.length !== limit) return invalid();
  return result;
};
export const decodeSynthesisNotePage = (value: unknown, workspaceId: string, limit = 20): SynthesisPage<SynthesisNoteSummary> => {
  const result = page(value, workspaceId, (v) => noteSummary(v, workspaceId), limit);
  unique(result.items, (v) => v.note.id); return result;
};
export const decodeSynthesisProcessingPage = (value: unknown, workspaceId: string, limit = 20): SynthesisPage<SynthesisProcessing> => {
  const result = page(value, workspaceId, (v) => decodeSynthesisProcessing(v, workspaceId), limit);
  unique(result.items, (v) => v.id); return result;
};
export const decodeSynthesisSourceView = (value: unknown, workspaceId: string, noteId: string, revisionId: string, expected: SynthesisSourceRef): SynthesisSourceView => {
  const v = object(value, ["workspace_id", "note_id", "revision_id", "reference", "availability", "text"]);
  scope(uuid(v.workspace_id), workspaceId); scope(uuid(v.note_id), noteId); scope(uuid(v.revision_id), revisionId);
  const reference = decodeSynthesisSourceRef(v.reference, workspaceId);
  if (synthesisSourceIdentity(reference) !== synthesisSourceIdentity(expected)) return invalid();
  const result = { reference, availability: choice(v.availability, ["AVAILABLE", "STALE", "UNAVAILABLE"]), text: nullable(v.text, (v) => text(v, 16384, false, true)) };
  if ((result.availability === "AVAILABLE") !== (result.text !== null)) return invalid();
  return result;
};

export const decodeNoteRevisionRef = (value: unknown, workspaceId: string): NoteRevisionRef => {
  const v = object(value, ["workspace_id", "note_id", "revision_id", "document_id", "article_revision_id", "revision_no", "article_revision_no", "content_hash", "projection_hash", "title"]);
  scope(uuid(v.workspace_id), workspaceId);
  return { workspaceId, noteId: uuid(v.note_id), revisionId: uuid(v.revision_id), documentId: uuid(v.document_id), articleRevisionId: uuid(v.article_revision_id), revisionNo: number(v.revision_no, 1), articleRevisionNo: number(v.article_revision_no, 1), contentHash: hash(v.content_hash), projectionHash: hash(v.projection_hash), title: text(v.title, 512) };
};
export const decodeNoteItemRef = (value: unknown, workspaceId: string): NoteItemRef => {
  const v = object(value, ["revision", "item_id", "item_kind"]);
  return { revision: decodeNoteRevisionRef(v.revision, workspaceId), itemId: uuid(v.item_id), itemKind: choice(v.item_kind, ["FACT", "CONFLICT", "GAP"]) };
};
export const decodeNoteQuestionSource = (value: unknown, workspaceId: string): NoteQuestionSource => {
  const v = object(value, ["revision", "item_id", "item_kind", "sources"]);
  const item = decodeNoteItemRef({ revision: v.revision, item_id: v.item_id, item_kind: v.item_kind }, workspaceId);
  const references = array(v.sources, (reference) => decodeSynthesisSourceRef(reference, workspaceId), 256, item.itemKind === "GAP" ? 0 : 1);
  const known = new Map<string, string>();
  for (const reference of references) {
    const identity = synthesisSourceIdentity(reference);
    const previous = known.get(reference.sourceSpanId);
    if (previous !== undefined && previous !== identity) return invalid();
    known.set(reference.sourceSpanId, identity);
  }
  return { ...item, sources: references };
};
const preparation = (value: unknown, workspaceId: string, noteId: string): NoteInterviewPreparation => {
  const v = object(value, ["id", "workspace_id", "note_revision", "options", "status", "workflow_run_id", "session_id", "failure", "created_at", "updated_at"]);
  scope(uuid(v.workspace_id), workspaceId);
  const options = object(v.options, ["role", "difficulty", "duration_minutes", "question_count", "max_follow_ups"]);
  const result: NoteInterviewPreparation = { id: uuid(v.id), workspaceId, noteRevision: decodeNoteRevisionRef(v.note_revision, workspaceId),
    options: { role: text(options.role, 256), difficulty: choice(options.difficulty, ["FOUNDATION", "INTERMEDIATE", "ADVANCED"]), durationMinutes: number(options.duration_minutes, 1, 240), questionCount: number(options.question_count, 1, 20), maxFollowUps: number(options.max_follow_ups, 0, 20) },
    status: choice(v.status, ["QUEUED", "GENERATING", "READY", "FAILED", "CAPABILITY_UNAVAILABLE", "RECOVERY_REQUIRED"]), workflowRunId: uuid(v.workflow_run_id),
    sessionId: nullable(v.session_id, uuid), failure: nullable(v.failure, failure), createdAt: timestamp(v.created_at), updatedAt: timestamp(v.updated_at) };
  scope(result.noteRevision.noteId, noteId);
  if ((result.status === "READY") !== (result.sessionId !== null) || ["FAILED", "CAPABILITY_UNAVAILABLE", "RECOVERY_REQUIRED"].includes(result.status) !== (result.failure !== null) || result.status === "RECOVERY_REQUIRED" && result.failure?.retryable || Date.parse(result.updatedAt) < Date.parse(result.createdAt)) return invalid();
  return result;
};
export const decodeNoteInterviewPreparation = (value: unknown, workspaceId: string, noteId: string): NoteInterviewPreparation => {
  const envelope = object(value, ["preparation", "replayed"]); boolean(envelope.replayed);
  return preparation(envelope.preparation, workspaceId, noteId);
};

const api = new SynthesisApi(generatedConfiguration), interviewApi = new InterviewApi(generatedConfiguration);
const request = async <T>(operation: () => Promise<Response>, decode: (value: unknown) => T, status = 200): Promise<T> => {
  let response: Response;
  try { response = await operation(); } catch (error: unknown) {
    if (isAbortError(error) || error instanceof SynthesisApiError) throw error;
    throw new SynthesisApiError("NETWORK_ERROR", "无法连接合成笔记服务，请保留当前操作并重试。", "NETWORK_ERROR", null, true);
  }
  if (response.headers.get("Content-Type")?.split(";", 1)[0]?.trim().toLowerCase() !== "application/json") return invalid();
  let value: unknown;
  try {
    const body = await response.text();
    if (new TextEncoder().encode(body).length > 8 * 1024 * 1024) return invalid();
    value = strictJson(body);
  } catch (error: unknown) {
    if (isAbortError(error)) throw error;
    return invalid();
  }
  if (!response.ok) {
    const problem = (() => { try { return decodeProblem(value); } catch { return invalid(); } })();
    throw new SynthesisApiError("HTTP_ERROR", problem.message, problem.errorCode, response.status, problem.retryable);
  }
  if (response.status !== status) return invalid();
  return decode(value);
};
const inputId = (value: string): string => {
  if (!canonicalUuidPattern.test(value)) throw new SynthesisApiError("INVALID_REQUEST", "资源身份无效。");
  return value;
};
const inputKey = (value: string): string => {
  if (value === "" || value !== value.trim() || new TextEncoder().encode(value).length > 128 || /[\u0000-\u001f\u007f]/.test(value)) throw new SynthesisApiError("INVALID_REQUEST", "操作身份无效。");
  return value;
};
const listInput = (workspaceId: string, cursor: string | null) => {
  if (cursor !== null && (cursor === "" || cursor !== cursor.trim() || cursor.length > 2048)) throw new SynthesisApiError("INVALID_REQUEST", "分页凭据无效。");
  return { workspaceId: inputId(workspaceId), limit: 20, ...(cursor === null ? {} : { cursor }) };
};
export const listSynthesisNotes = (workspaceId: string, cursor: string | null = null, signal?: AbortSignal): Promise<SynthesisPage<SynthesisNoteSummary>> =>
  request(() => generatedRawResponse(api.listSynthesisNotesRaw(listInput(workspaceId, cursor), generatedRequestInit(signal))), (v) => decodeSynthesisNotePage(v, workspaceId));
export const getSynthesisNote = (workspaceId: string, noteId: string, signal?: AbortSignal): Promise<SynthesisNoteDetail> =>
  request(() => generatedRawResponse(api.getSynthesisNoteRaw({ workspaceId: inputId(workspaceId), noteId: inputId(noteId) }, generatedRequestInit(signal))), (v) => decodeSynthesisNoteDetail(v, workspaceId, noteId));
export const listSynthesisRevisions = (workspaceId: string, noteId: string, cursor: string | null = null, signal?: AbortSignal): Promise<SynthesisPage<SynthesisRevisionSummary>> =>
  request(() => generatedRawResponse(api.listSynthesisRevisionsRaw({ ...listInput(workspaceId, cursor), noteId: inputId(noteId) }, generatedRequestInit(signal))), (v) => {
    const result = page(v, workspaceId, revisionSummary, 20, noteId); unique(result.items, (v) => v.id);
    if (result.items.some((item, index) => index > 0 && item.revisionNo >= (result.items[index - 1]?.revisionNo ?? 0))) return invalid();
    return result;
  });
export const getSynthesisRevision = (workspaceId: string, noteId: string, revisionId: string, signal?: AbortSignal): Promise<SynthesisRevision> =>
  request(() => generatedRawResponse(api.getSynthesisRevisionRaw({ workspaceId: inputId(workspaceId), noteId: inputId(noteId), revisionId: inputId(revisionId) }, generatedRequestInit(signal))), (v) => {
    const value = object(v, ["workspace_id", "note_id", "revision"]); scope(uuid(value.workspace_id), workspaceId); scope(uuid(value.note_id), noteId);
    const revision = decodeSynthesisRevision(value.revision, workspaceId, noteId); scope(revision.id, revisionId); return revision;
  });
export const openSynthesisSource = (workspaceId: string, noteId: string, revisionId: string, reference: SynthesisSourceRef, signal?: AbortSignal): Promise<SynthesisSourceView> =>
  request(() => generatedRawResponse(api.openSynthesisSourceRaw({ workspaceId: inputId(workspaceId), noteId: inputId(noteId), revisionId: inputId(revisionId), sourceSpanId: inputId(reference.sourceSpanId) }, generatedRequestInit(signal))), (v) => decodeSynthesisSourceView(v, workspaceId, noteId, revisionId, reference));
export const listSynthesisProcessing = (workspaceId: string, cursor: string | null = null, signal?: AbortSignal): Promise<SynthesisPage<SynthesisProcessing>> =>
  request(() => generatedRawResponse(api.listSynthesisProcessingRaw(listInput(workspaceId, cursor), generatedRequestInit(signal))), (v) => decodeSynthesisProcessingPage(v, workspaceId));
export const getSynthesisProcessing = (workspaceId: string, processingId: string, signal?: AbortSignal): Promise<SynthesisProcessing> =>
  request(() => generatedRawResponse(api.getSynthesisProcessingRaw({ workspaceId: inputId(workspaceId), processingId: inputId(processingId) }, generatedRequestInit(signal))), (v) => {
    const value = object(v, ["workspace_id", "processing"]); scope(uuid(value.workspace_id), workspaceId);
    const processing = decodeSynthesisProcessing(value.processing, workspaceId); scope(processing.id, processingId); return processing;
  });
export interface RetrySynthesisInput { workspaceId: string; processingId: string; expectedVersion: number; idempotencyKey: string; signal?: AbortSignal }
export const retrySynthesisProcessing = (input: RetrySynthesisInput): Promise<SynthesisProcessing> => {
  if (!Number.isSafeInteger(input.expectedVersion) || input.expectedVersion < 1) throw new SynthesisApiError("INVALID_REQUEST", "重试版本无效。");
  return request(() => generatedRawResponse(api.retrySynthesisProcessingRaw({ workspaceId: inputId(input.workspaceId), processingId: inputId(input.processingId), idempotencyKey: inputKey(input.idempotencyKey), synthesisRetryRequest: { expected_version: input.expectedVersion } }, generatedRequestInit(input.signal))), (v) => {
    const value = object(v, ["workspace_id", "processing", "replayed"]); scope(uuid(value.workspace_id), input.workspaceId); boolean(value.replayed);
    const processing = decodeSynthesisProcessing(value.processing, input.workspaceId); scope(processing.id, input.processingId);
    if (processing.version <= input.expectedVersion) return invalid(); return processing;
  }, 202);
};
const prepareOptions = (options: NoteInterviewOptions) => {
  if (options.role.trim() === "" || options.role !== options.role.trim() || new TextEncoder().encode(options.role).length > 256 || /[\u0000-\u001f\u007f]/.test(options.role) ||
    !["FOUNDATION", "INTERMEDIATE", "ADVANCED"].includes(options.difficulty) || !Number.isSafeInteger(options.durationMinutes) || options.durationMinutes < 1 || options.durationMinutes > 240 ||
    !Number.isSafeInteger(options.questionCount) || options.questionCount < 1 || options.questionCount > 20 || !Number.isSafeInteger(options.maxFollowUps) || options.maxFollowUps < 0 || options.maxFollowUps > 20) throw new SynthesisApiError("INVALID_REQUEST", "面试设置超出允许范围。");
  return { role: options.role, difficulty: options.difficulty, duration_minutes: options.durationMinutes, question_count: options.questionCount, max_follow_ups: options.maxFollowUps };
};
export const prepareNoteInterview = (workspaceId: string, noteId: string, options: NoteInterviewOptions, idempotencyKey: string, preparationId?: string, signal?: AbortSignal): Promise<NoteInterviewPreparation> => {
  const parameters = { workspaceId: inputId(workspaceId), noteId: inputId(noteId), interviewNotePreparationOptions: prepareOptions(options), idempotencyKey: inputKey(idempotencyKey) };
  return request(() => generatedRawResponse(preparationId === undefined
    ? interviewApi.prepareSynthesisNoteInterviewRaw(parameters, generatedRequestInit(signal))
    : interviewApi.retrySynthesisNoteInterviewPreparationRaw({ ...parameters, preparationId: inputId(preparationId) }, generatedRequestInit(signal))), (v) => {
      const result = decodeNoteInterviewPreparation(v, workspaceId, noteId);
      if (JSON.stringify(prepareOptions(result.options)) !== JSON.stringify(parameters.interviewNotePreparationOptions)) return invalid();
      return result;
    }, 202);
};
export const getNoteInterviewPreparation = (workspaceId: string, noteId: string, preparationId: string, signal?: AbortSignal): Promise<NoteInterviewPreparation> =>
  request(() => generatedRawResponse(interviewApi.getSynthesisNoteInterviewPreparationRaw({ workspaceId: inputId(workspaceId), noteId: inputId(noteId), preparationId: inputId(preparationId) }, generatedRequestInit(signal))), (v) => {
    const result = decodeNoteInterviewPreparation(v, workspaceId, noteId); scope(result.id, preparationId); return result;
  });
export const listNoteInterviewPreparations = (workspaceId: string, noteId: string, signal?: AbortSignal): Promise<NoteInterviewPreparation[]> =>
  request(() => generatedRawResponse(interviewApi.listSynthesisNoteInterviewPreparationsRaw({ workspaceId: inputId(workspaceId), noteId: inputId(noteId) }, generatedRequestInit(signal))), (v) => {
    const value = object(v, ["items"]); return unique(array(value.items, (v) => preparation(v, workspaceId, noteId), 20), (v) => v.id);
  });
