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
export interface SynthesisBodyReference {
  workspaceId: string; noteId: string; revisionId: string; publicationId: string; itemId: string; projectionHash: string;
}
export type SynthesisItem = { bodyReference?: SynthesisBodyReference } & (
  | { id: string; kind: "FACT"; fact: SynthesisStatement }
  | { id: string; kind: "CONFLICT"; conflict: { subject: string; alternatives: SynthesisStatement[] } }
  | { id: string; kind: "GAP"; gap: { question: string; context: string; sources: SynthesisSourceRef[]; resolution: SynthesisStatement | null } });
export interface SynthesisNote {
  id: string; workspaceId: string; documentId: string; topicKey: string; title: string; aliases: string[];
  currentRevisionId: string | null; version: number; status: SynthesisNoteStatus; workflowRunId: string | null;
  failure: SynthesisFailure | null; createdAt: string; updatedAt: string;
}
export interface SynthesisRevisionSummary {
  remergeSourceRevisionId?: string;
  id: string; revisionNo: number; articleRevisionId: string; articleRevisionNo: number; contentHash: string; createdAt: string;
}
export interface SynthesisRevision extends SynthesisRevisionSummary {
  historicalRepublish?: { attemptId: string; selectedRevisionId: string; selectedPublicationId?: string; selectedProposalCommitId?: string };
  remerge?: { attemptId: string; sourceRevisionId: string };
  workspaceId: string; noteId: string; documentId: string; parentRevisionId: string | null;
  title: string; projectionHash: string; items: SynthesisItem[];
  display?: { rendererVersion: "synthesis-markdown/v2"; fullContent: string; manualChanges: boolean; reviewRequired: boolean; historicalSources: SynthesisSourceRef[] };
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
export interface SynthesisSourceView { reference: SynthesisSourceRef; availability: SynthesisAvailability; text: string | null; snapshotText?: string | null; role?: "HISTORICAL_REVIEW" }
export interface SourceKnowledgePoint { profileRevisionId: string; kind: "KNOWLEDGE_POINT" | "EXAMPLE"; index: number; text: string; sourceSpanIds: string[] }
export interface SourceKnowledgeDirectory {
  workspaceId: string; sourceVersionId: string; status: "ANALYZED" | "UNANALYZED" | "UNAVAILABLE" | "UNRECORDED";
  profileRevisionId: string | null; parseProjectionId: string | null; summary: string; points: SourceKnowledgePoint[];
}
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
  const keys = ["id", "kind", "fact", "conflict", "gap"];
  if (isRecord(value) && Object.hasOwn(value, "body_reference")) keys.push("body_reference");
  const v = object(value, keys);
  const id = uuid(v.id);
  const body = v.body_reference === undefined ? {} : { bodyReference: (() => {
    const ref = object(v.body_reference, ["workspace_id", "note_id", "revision_id", "publication_id", "item_id", "projection_hash"]);
    scope(uuid(ref.workspace_id), workspaceId);
    return { workspaceId, noteId: uuid(ref.note_id), revisionId: uuid(ref.revision_id), publicationId: uuid(ref.publication_id), itemId: uuid(ref.item_id), projectionHash: hash(ref.projection_hash) };
  })() };

  if (v.kind === "FACT" && v.conflict === null && v.gap === null) return { ...body, id, kind: "FACT", fact: statement(v.fact, workspaceId) };
  if (v.kind === "CONFLICT" && v.fact === null && v.gap === null) {
    const c = object(v.conflict, ["subject", "alternatives"]);
    return { ...body, id, kind: "CONFLICT", conflict: { subject: text(c.subject), alternatives: array(c.alternatives, (v) => statement(v, workspaceId), 4, 2) } };
  }
  if (v.kind === "GAP" && v.fact === null && v.conflict === null) {
    const g = object(v.gap, ["question", "context", "sources", "resolution"]);
    return { ...body, id, kind: "GAP", gap: { question: text(g.question), context: text(g.context, 2048, true), sources: sources(g.sources, workspaceId), resolution: nullable(g.resolution, (v) => statement(v, workspaceId)) } };
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
const revisionSummaryFields = (v: Record<string, unknown>): SynthesisRevisionSummary => ({ id: uuid(v.id), revisionNo: number(v.revision_no, 1), articleRevisionId: uuid(v.article_revision_id), articleRevisionNo: number(v.article_revision_no, 1), contentHash: hash(v.content_hash), createdAt: timestamp(v.created_at), ...(Object.hasOwn(v, "remerge_source_revision_id") ? { remergeSourceRevisionId: uuid(v.remerge_source_revision_id) } : {}) });
const revisionSummary = (value: unknown): SynthesisRevisionSummary => revisionSummaryFields(object(value, [...summaryFields, ...(isRecord(value) && Object.hasOwn(value, "remerge_source_revision_id") ? ["remerge_source_revision_id"] : [])]));
export const decodeSynthesisRevision = (value: unknown, workspaceId: string, noteId: string): SynthesisRevision => {
  const v = object(value, [...summaryFields, "workspace_id", "note_id", "document_id", "parent_revision_id", "title", "projection_hash", "items", ...(isRecord(value) && Object.hasOwn(value, "display") ? ["display"] : []), ...(isRecord(value) && Object.hasOwn(value, "remerge") ? ["remerge"] : []), ...(isRecord(value) && Object.hasOwn(value, "historical_republish") ? ["historical_republish"] : [])]);
  scope(uuid(v.workspace_id), workspaceId); scope(uuid(v.note_id), noteId);
  const display = Object.hasOwn(v, "display") ? (() => {
    const d = object(v.display, ["renderer_version", "full_content", "manual_changes", "review_required", "historical_sources"]);
    const fullContent = text(d.full_content, 1 << 20, true, true);
    // TextEncoder 会替换非法 UTF-16；应拒绝输入，避免改变字节。
    if (new TextDecoder("utf-8", { fatal: true, ignoreBOM: true }).decode(new TextEncoder().encode(fullContent)) !== fullContent) return invalid();
    const result = { rendererVersion: choice(d.renderer_version, ["synthesis-markdown/v2"]), fullContent,
      manualChanges: boolean(d.manual_changes), reviewRequired: boolean(d.review_required),
      historicalSources: unique(array(d.historical_sources, (ref) => decodeSynthesisSourceRef(ref, workspaceId), 256), synthesisSourceIdentity) };
    if (result.manualChanges && !result.reviewRequired) return invalid();
    return result;
  })() : undefined;
  const items = unique(array(v.items, (v) => item(v, workspaceId), 128, display === undefined ? 1 : 0), (v) => v.id);
  if (display?.manualChanges && items.length > 0) return invalid();
  if (items.some((item) => item.bodyReference?.noteId === noteId)) return invalid();
  const catalog = new Map<string, string>();
  for (const reference of items.flatMap(synthesisItemSources)) {
    const encoded = synthesisSourceIdentity(reference);
    const previous = catalog.get(reference.sourceSpanId);
    if (previous !== undefined && previous !== encoded) return invalid();
    catalog.set(reference.sourceSpanId, encoded);
  }
  if (catalog.size > 256) return invalid();
  const historicalSpans = new Set<string>();
  for (const reference of display?.historicalSources ?? []) {
    if (catalog.has(reference.sourceSpanId) || historicalSpans.has(reference.sourceSpanId)) return invalid();
    historicalSpans.add(reference.sourceSpanId);
  }
  const historicalRepublish = Object.hasOwn(v, "historical_republish") ? (() => {
    const raw = v.historical_republish;
    const h = object(raw, ["attempt_id", "selected_revision_id", ...(isRecord(raw) && Object.hasOwn(raw, "selected_publication_id") ? ["selected_publication_id", "selected_proposal_commit_id"] : [])]);
    if (h.selected_revision_id === v.id || v.remerge !== undefined) return invalid();
    return { attemptId: uuid(h.attempt_id), selectedRevisionId: uuid(h.selected_revision_id), ...(h.selected_publication_id === undefined ? {} : { selectedPublicationId: uuid(h.selected_publication_id), selectedProposalCommitId: uuid(h.selected_proposal_commit_id) }) };
  })() : undefined;
  const remerge = Object.hasOwn(v, "remerge") ? (() => { const p = object(v.remerge, ["attempt_id", "source_revision_id"]); return { attemptId: uuid(p.attempt_id), sourceRevisionId: uuid(p.source_revision_id) }; })() : undefined;
  if (remerge && (!display || remerge.sourceRevisionId !== v.parent_revision_id || remerge.sourceRevisionId === v.id)) return invalid();
  const result = { ...revisionSummaryFields(v), ...(historicalRepublish ? { historicalRepublish } : {}), ...(remerge ? { remerge } : {}), workspaceId, noteId, documentId: uuid(v.document_id), parentRevisionId: nullable(v.parent_revision_id, uuid), title: text(v.title, 512), projectionHash: hash(v.projection_hash), items, ...(display === undefined ? {} : { display }) };
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
  const v = object(value, ["workspace_id", "note_id", "revision_id", "reference", "availability", "text", ...(isRecord(value) && Object.hasOwn(value, "snapshot_text") ? ["snapshot_text"] : []), ...(isRecord(value) && Object.hasOwn(value, "role") ? ["role"] : [])]);
  scope(uuid(v.workspace_id), workspaceId); scope(uuid(v.note_id), noteId); scope(uuid(v.revision_id), revisionId);
  const reference = decodeSynthesisSourceRef(v.reference, workspaceId);
  if (synthesisSourceIdentity(reference) !== synthesisSourceIdentity(expected)) return invalid();
  const result = { reference, availability: choice(v.availability, ["AVAILABLE", "STALE", "UNAVAILABLE"]), text: nullable(v.text, (v) => text(v, 16384, false, true)), snapshotText: Object.hasOwn(v, "snapshot_text") ? text(v.snapshot_text, 16384, false, true) : null };
  if ((result.availability === "AVAILABLE") !== (result.text !== null) || (result.availability === "AVAILABLE" && result.snapshotText !== null)) return invalid();
  return { ...result, ...(Object.hasOwn(v, "role") ? { role: choice(v.role, ["HISTORICAL_REVIEW"]) } : {}) };
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

export interface SynthesisSourceSupplement {
  id: string; workspaceId: string; noteId: string; baseRevisionId: string; itemId: string;
  slot: "FACT" | "CONFLICT" | "GAP_CONTEXT" | "GAP_RESOLUTION"; alternativeIndex: number;
  processingId: string; reference: SynthesisSourceRef; createdAt: string;
}
export const decodeSynthesisSupplement = (value: unknown, workspaceId: string, noteId: string): SynthesisSourceSupplement => {
  const v = object(value, ["id", "workspace_id", "note_id", "base_revision_id", "item_id", "slot", "alternative_index", "processing_id", "reference", "created_at"]);
  scope(uuid(v.workspace_id), workspaceId); scope(uuid(v.note_id), noteId);
  const slot = choice(v.slot, ["FACT", "CONFLICT", "GAP_CONTEXT", "GAP_RESOLUTION"]);
  const alternativeIndex = number(v.alternative_index, -1, 3);
  if ((slot === "CONFLICT") !== (alternativeIndex >= 0)) return invalid();
  return { id: uuid(v.id), workspaceId, noteId, baseRevisionId: uuid(v.base_revision_id), itemId: uuid(v.item_id), slot, alternativeIndex,
    processingId: uuid(v.processing_id), reference: decodeSynthesisSourceRef(v.reference, workspaceId), createdAt: timestamp(v.created_at) };
};
export const listSynthesisSupplements = (workspaceId: string, noteId: string, cursor: string | null = null, signal?: AbortSignal): Promise<SynthesisPage<SynthesisSourceSupplement>> =>
  request(() => generatedRawResponse(api.listSynthesisSupplementsRaw({ ...listInput(workspaceId, cursor), noteId: inputId(noteId) }, generatedRequestInit(signal))), (v) => {
    const result = page(v, workspaceId, (value) => decodeSynthesisSupplement(value, workspaceId, noteId), 20, noteId);
    unique(result.items, (item) => item.id);
    return result;
  });
export const openSynthesisSupplement = (workspaceId: string, noteId: string, expected: SynthesisSourceSupplement, signal?: AbortSignal): Promise<SynthesisSourceView> =>
  request(() => generatedRawResponse(api.openSynthesisSupplementRaw({ workspaceId: inputId(workspaceId), noteId: inputId(noteId), supplementId: inputId(expected.id) }, generatedRequestInit(signal))), (value) => {
    const v = object(value, ["workspace_id", "note_id", "supplement", "availability", "text", ...(isRecord(value) && Object.hasOwn(value, "snapshot_text") ? ["snapshot_text"] : [])]);
    scope(uuid(v.workspace_id), workspaceId); scope(uuid(v.note_id), noteId);
    const supplement = decodeSynthesisSupplement(v.supplement, workspaceId, noteId);
    if (JSON.stringify(supplement) !== JSON.stringify(expected)) return invalid();
    const result = { reference: supplement.reference, availability: choice(v.availability, ["AVAILABLE", "STALE", "UNAVAILABLE"]), text: nullable(v.text, (v) => text(v, 16384, false, true)), snapshotText: Object.hasOwn(v, "snapshot_text") ? text(v.snapshot_text, 16384, false, true) : null };
    if ((result.availability === "AVAILABLE") !== (result.text !== null) || (result.availability === "AVAILABLE" && result.snapshotText !== null)) return invalid();
    return result;
  });

export interface AnchorScope { topics: string[]; audiences: string[]; description: string }
export interface KnowledgeAnchor {
  id: string; workspaceId: string; noteId: string; basisRevisionId: string; title: string; scope: AnchorScope;
  scopeVersion: number; version: number; createdAt: string; updatedAt: string;
}
export interface AnchorProposal {
  id: string; workspaceId: string; anchorId: string; kind: "SCOPE_ADJUSTMENT" | "SOURCE_ASSOCIATION";
  scopeVersion: number; before: AnchorScope | null; suggested: AnchorScope | null; reason: string;
  evidence: SynthesisSourceRef[]; modelRunId: string; status: "PENDING" | "ACCEPTED" | "REJECTED"; version: number; createdAt: string;
}
export interface AnchorPage<T> { items: T[]; nextAfterId: string | null }
const decodeAnchorScope = (value: unknown): AnchorScope => {
  const v = object(value, ["topics", "audiences", "description"]);
  return { topics: unique(array(v.topics, (x) => text(x, 256), 64, 1), (x) => x), audiences: unique(array(v.audiences, (x) => text(x, 256), 32, 1), (x) => x), description: text(v.description, 2048) };
};
export const decodeKnowledgeAnchor = (value: unknown, workspaceId: string): KnowledgeAnchor => {
  const v = object(value, ["id", "workspace_id", "note_id", "basis_revision_id", "title", "scope", "scope_version", "version", "created_at", "updated_at"]);
  scope(uuid(v.workspace_id), workspaceId);
  const result = { id: uuid(v.id), workspaceId, noteId: uuid(v.note_id), basisRevisionId: uuid(v.basis_revision_id), title: text(v.title, 512), scope: decodeAnchorScope(v.scope),
    scopeVersion: number(v.scope_version, 1), version: number(v.version, 1), createdAt: timestamp(v.created_at), updatedAt: timestamp(v.updated_at) };
  if (Date.parse(result.updatedAt) < Date.parse(result.createdAt)) return invalid();
  return result;
};
export const decodeAnchorProposal = (value: unknown, workspaceId: string, anchorId: string): AnchorProposal => {
  const v = object(value, ["id", "workspace_id", "anchor_id", "kind", "scope_version", "before", "suggested", "reason", "evidence", "model_run_id", "status", "version", "created_at"]);
  scope(uuid(v.workspace_id), workspaceId); scope(uuid(v.anchor_id), anchorId);
  const result: AnchorProposal = { id: uuid(v.id), workspaceId, anchorId, kind: choice(v.kind, ["SCOPE_ADJUSTMENT", "SOURCE_ASSOCIATION"]), scopeVersion: number(v.scope_version, 1),
    before: nullable(v.before, decodeAnchorScope), suggested: nullable(v.suggested, decodeAnchorScope), reason: text(v.reason, 2048), evidence: sources(v.evidence, workspaceId, 1, 32),
    modelRunId: uuid(v.model_run_id), status: choice(v.status, ["PENDING", "ACCEPTED", "REJECTED"]), version: number(v.version, 1), createdAt: timestamp(v.created_at) };
  if (result.kind === "SCOPE_ADJUSTMENT" ? result.before === null || result.suggested === null : result.before !== null || result.suggested !== null) return invalid();
  return result;
};
const anchorPage = <T extends { id: string }>(value: unknown, decode: (value: unknown) => T, afterId: string | null): AnchorPage<T> => {
  const v = object(value, ["items", "next_after_id"]);
  const items = unique(array(v.items, decode, 20), (item) => item.id);
  let previous = afterId ?? "";
  for (const item of items) { if (item.id <= previous) return invalid(); previous = item.id; }
  const nextAfterId = nullable(v.next_after_id, uuid);
  if (nextAfterId !== null && (items.length !== 20 || nextAfterId !== previous)) return invalid();
  return { items, nextAfterId };
};
export const listKnowledgeAnchors = (workspaceId: string, noteId: string, afterId: string | null = null, signal?: AbortSignal): Promise<AnchorPage<KnowledgeAnchor>> =>
  request(() => generatedRawResponse(api.listKnowledgeAnchorsRaw({ workspaceId: inputId(workspaceId), noteId: inputId(noteId), limit: 20, ...(afterId === null ? {} : { afterId: inputId(afterId) }) }, generatedRequestInit(signal))), (value) => anchorPage(value, (v) => {
    const anchor = decodeKnowledgeAnchor(v, workspaceId); scope(anchor.noteId, noteId); return anchor;
  }, afterId));
export const listAnchorProposals = (workspaceId: string, anchorId: string, kind: AnchorProposal["kind"], afterId: string | null = null, signal?: AbortSignal): Promise<AnchorPage<AnchorProposal>> => {
  const params = { workspaceId: inputId(workspaceId), anchorId: inputId(anchorId), limit: 20, ...(afterId === null ? {} : { afterId: inputId(afterId) }) };
  return request(() => generatedRawResponse(kind === "SCOPE_ADJUSTMENT" ? api.listAnchorScopeProposalsRaw(params, generatedRequestInit(signal)) : api.listAnchorAssociationsRaw(params, generatedRequestInit(signal))),
    (value) => anchorPage(value, (v) => { const proposal = decodeAnchorProposal(v, workspaceId, anchorId); if (proposal.kind !== kind) return invalid(); return proposal; }, afterId));
};
export interface AnchorDecisionInput {
  workspaceId: string; anchor: KnowledgeAnchor; kind: AnchorProposal["kind"]; decision: "ACCEPTED" | "REJECTED";
  items: AnchorProposal[]; idempotencyKey: string; signal?: AbortSignal;
}
export const decideAnchorProposals = (input: AnchorDecisionInput): Promise<{ anchor: KnowledgeAnchor; items: AnchorProposal[]; replayed: boolean }> => {
  if (input.anchor.workspaceId !== input.workspaceId || input.items.length < 1 || input.items.length > 32 || input.kind === "SCOPE_ADJUSTMENT" && input.items.length !== 1 ||
    new Set(input.items.map((item) => item.id)).size !== input.items.length || input.items.some((item) => item.workspaceId !== input.workspaceId || item.anchorId !== input.anchor.id || item.kind !== input.kind || item.status !== "PENDING" || item.scopeVersion !== input.anchor.scopeVersion)) {
    throw new SynthesisApiError("INVALID_REQUEST", "审核基线已变化，请刷新后重新选择。");
  }
  const params = { workspaceId: inputId(input.workspaceId), anchorId: inputId(input.anchor.id), idempotencyKey: text(input.idempotencyKey, 128),
    decideAnchorRequest: { expected_anchor_version: input.anchor.version, decision: input.decision, items: input.items.map((item) => ({ proposal_id: item.id, expected_version: item.version })) } };
  return request(() => generatedRawResponse(input.kind === "SCOPE_ADJUSTMENT" ? api.decideAnchorScopeProposalsRaw(params, generatedRequestInit(input.signal)) : api.decideAnchorAssociationsRaw(params, generatedRequestInit(input.signal))), (value) => {
    const v = object(value, ["anchor", "items", "replayed"]);
    const anchor = decodeKnowledgeAnchor(v.anchor, input.workspaceId); scope(anchor.id, input.anchor.id); scope(anchor.noteId, input.anchor.noteId);
    const items = array(v.items, (item) => decodeAnchorProposal(item, input.workspaceId, anchor.id), 32, 1);
    if (anchor.version !== input.anchor.version + 1 || items.length !== input.items.length || items.some((item, index) => item.id !== input.items[index]?.id || item.status !== input.decision || item.kind !== input.kind || item.version !== input.items[index].version + 1) ||
      anchor.scopeVersion !== input.anchor.scopeVersion + (input.kind === "SCOPE_ADJUSTMENT" && input.decision === "ACCEPTED" ? 1 : 0)) return invalid();
    const expectedScope = input.kind === "SCOPE_ADJUSTMENT" && input.decision === "ACCEPTED" ? input.items[0]?.suggested : input.anchor.scope;
    if (anchor.basisRevisionId !== input.anchor.basisRevisionId || anchor.title !== input.anchor.title || anchor.createdAt !== input.anchor.createdAt || JSON.stringify(anchor.scope) !== JSON.stringify(expectedScope) ||
      items.some((item, index) => JSON.stringify({ ...item, status: "PENDING", version: 1 }) !== JSON.stringify(input.items[index]))) return invalid();
    return { anchor, items, replayed: boolean(v.replayed) };
  });
};

const knowledgePoint = (value: unknown, profileRevisionId: string): SourceKnowledgePoint => {
  const v = object(value, ["locator", "text", "source_span_ids"]);
  const locator = object(v.locator, ["profile_revision_id", "kind", "index"]);
  scope(uuid(locator.profile_revision_id), profileRevisionId);
  return { profileRevisionId, kind: choice(locator.kind, ["KNOWLEDGE_POINT", "EXAMPLE"]), index: number(locator.index, 0, 255),
    text: text(v.text, 4096, false, true), sourceSpanIds: unique(array(v.source_span_ids, uuid, 64, 1), (id) => id) };
};
export const decodeSourceKnowledgeDirectory = (value: unknown, workspaceId: string, sourceVersionId: string): SourceKnowledgeDirectory => {
  if (!isRecord(value) || !hasExactKeys(value, ["workspace_id", "source_version_id", "status"], ["profile_status", "profile_revision_id", "parse_projection_id", "summary", "topics", "terms", "points"])) return invalid();
  scope(uuid(value.workspace_id), workspaceId); scope(uuid(value.source_version_id), sourceVersionId);
  const status = choice(value.status, ["ANALYZED", "UNANALYZED", "UNAVAILABLE", "UNRECORDED"]);
  if (value.profile_status !== undefined) choice(value.profile_status, ["PENDING", "RUNNING", "READY", "STALE", "FAILED", "CAPABILITY_UNAVAILABLE"]);
  if (status !== "ANALYZED") {
    if (status === "UNRECORDED" && value.profile_status !== undefined) return invalid();
    if (["profile_revision_id", "parse_projection_id", "summary", "topics", "terms", "points"].some((key) => Object.hasOwn(value, key))) return invalid();
    if (status === "UNANALYZED" && value.profile_status !== undefined && value.profile_status !== "PENDING" && value.profile_status !== "RUNNING" ||
      status === "UNAVAILABLE" && value.profile_status !== "FAILED" && value.profile_status !== "CAPABILITY_UNAVAILABLE") return invalid();
    return { workspaceId, sourceVersionId, status, profileRevisionId: null, parseProjectionId: null, summary: "", points: [] };
  }
  if (value.profile_status !== "READY" && value.profile_status !== "STALE") return invalid();
  const profileRevisionId = uuid(value.profile_revision_id);
  const points = unique(array(value.points, (point) => knowledgePoint(point, profileRevisionId), 512, 1), (point) => `${point.kind}:${String(point.index)}`);
  for (const [key, minimum] of [["topics", 1], ["terms", 0]] as const) {
    if (value[key] === undefined && minimum === 0) continue;
    array(value[key], (entry) => {
      if (!isRecord(entry) || !hasExactKeys(entry, ["label", "source_span_ids"], ["aliases"])) return invalid();
      text(entry.label, 512); array(entry.source_span_ids, uuid, 64, 1);
      if (entry.aliases !== undefined) unique(array(entry.aliases, (alias) => text(alias, 512), 32), (alias) => alias);
    }, 128, minimum);
  }
  return { workspaceId, sourceVersionId, status, profileRevisionId, parseProjectionId: uuid(value.parse_projection_id), summary: text(value.summary, 16384, false, true), points };
};
export const getSynthesisSourceKnowledgePoints = (workspaceId: string, noteId: string, revisionId: string, reference: SynthesisSourceRef, signal?: AbortSignal): Promise<{ directory: SourceKnowledgeDirectory; points: SourceKnowledgePoint[] }> =>
  request(() => generatedRawResponse(api.getSynthesisSourceKnowledgePointsRaw({ workspaceId: inputId(workspaceId), noteId: inputId(noteId), revisionId: inputId(revisionId), sourceSpanId: inputId(reference.sourceSpanId) }, generatedRequestInit(signal))), (value) => {
    const v = object(value, ["workspace_id", "note_id", "revision_id", "reference", "directory", "points"]);
    scope(uuid(v.workspace_id), workspaceId); scope(uuid(v.note_id), noteId); scope(uuid(v.revision_id), revisionId);
    if (JSON.stringify(decodeSynthesisSourceRef(v.reference, workspaceId)) !== JSON.stringify(reference)) return invalid();
    const directory = decodeSourceKnowledgeDirectory(v.directory, workspaceId, reference.source.sourceVersionId);
    const points = array(v.points, (point) => knowledgePoint(point, directory.profileRevisionId ?? ""), 512);
    if (points.length > 0 && (directory.status !== "ANALYZED" || directory.parseProjectionId !== reference.source.parseProjectionId) ||
      points.some((point) => !point.sourceSpanIds.includes(reference.sourceSpanId) || !directory.points.some((candidate) => JSON.stringify(candidate) === JSON.stringify(point)))) return invalid();
    return { directory, points };
  });

export interface SynthesisSharedSourceNote { noteId: string; revisionId: string; revisionNo: number; title: string; sources: SynthesisSourceRef[] }
export interface SynthesisSourceGraph { sources: SynthesisSourceRef[]; sharedNotes: SynthesisSharedSourceNote[]; nextAfterNoteId: string | null }
export const decodeSynthesisSourceGraph = (value: unknown, revision: SynthesisRevision, afterNoteId: string | null = null): SynthesisSourceGraph => {
  const v = object(value, ["workspace_id", "note_id", "revision_id", "sources", "shared_notes", "next_after_note_id"]);
  scope(uuid(v.workspace_id), revision.workspaceId); scope(uuid(v.note_id), revision.noteId); scope(uuid(v.revision_id), revision.id);
  const references = sources(v.sources, revision.workspaceId, 0, 256);
  const expected = new Set(revision.items.flatMap(synthesisItemSources).map(synthesisSourceIdentity));
  if (references.length !== expected.size || references.some((ref) => !expected.has(synthesisSourceIdentity(ref)))) return invalid();
  const sourceIds = new Set(references.map((ref) => ref.source.sourceId));
  let previous = afterNoteId ?? "";
  const sharedNotes = array(v.shared_notes, (value): SynthesisSharedSourceNote => {
    const n = object(value, ["note_id", "revision_id", "revision_no", "title", "sources"]);
    const noteId = uuid(n.note_id);
    if (noteId === revision.noteId || noteId <= previous) return invalid();
    previous = noteId;
    const references = sources(n.sources, revision.workspaceId, 1, 256);
    if (references.some((ref) => !sourceIds.has(ref.source.sourceId))) return invalid();
    return { noteId, revisionId: uuid(n.revision_id), revisionNo: number(n.revision_no, 1), title: text(n.title, 512), sources: references };
  }, 20);
  const nextAfterNoteId = nullable(v.next_after_note_id, uuid);
  if (nextAfterNoteId !== null && (sharedNotes.length !== 20 || nextAfterNoteId !== previous)) return invalid();
  return { sources: references, sharedNotes, nextAfterNoteId };
};
export const getSynthesisSourceGraph = (revision: SynthesisRevision, afterNoteId: string | null = null, signal?: AbortSignal): Promise<SynthesisSourceGraph> =>
  request(() => generatedRawResponse(api.getSynthesisSourceGraphRaw({ workspaceId: inputId(revision.workspaceId), noteId: inputId(revision.noteId), revisionId: inputId(revision.id), limit: 20, ...(afterNoteId === null ? {} : { afterNoteId: inputId(afterNoteId) }) }, generatedRequestInit(signal))), (value) => decodeSynthesisSourceGraph(value, revision, afterNoteId));

export interface AnchorFusionRequest {
  id: string; workspaceId: string; anchorId: string; noteId: string; proposalId: string; scopeVersion: number;
  sources: SynthesisSourceRef[]; status: "PENDING" | "DISPATCHED" | "STALE"; processingId: string | null; createdAt: string; updatedAt: string;
}
export const decodeAnchorFusionRecent = (value: unknown, anchor: KnowledgeAnchor): AnchorFusionRequest[] => {
  const v = object(value, ["workspace_id", "anchor_id", "items"]);
  scope(uuid(v.workspace_id), anchor.workspaceId); scope(uuid(v.anchor_id), anchor.id);
  return unique(array(v.items, (value) => {
    const r = object(value, ["id", "workspace_id", "anchor_id", "note_id", "proposal_id", "scope_version", "sources", "status", "processing_id", "created_at", "updated_at"]);
    scope(uuid(r.workspace_id), anchor.workspaceId); scope(uuid(r.anchor_id), anchor.id); scope(uuid(r.note_id), anchor.noteId);
    const result: AnchorFusionRequest = { id: uuid(r.id), workspaceId: anchor.workspaceId, anchorId: anchor.id, noteId: anchor.noteId, proposalId: uuid(r.proposal_id), scopeVersion: number(r.scope_version, 1),
      sources: sources(r.sources, anchor.workspaceId, 1, 256), status: choice(r.status, ["PENDING", "DISPATCHED", "STALE"]), processingId: nullable(r.processing_id, uuid), createdAt: timestamp(r.created_at), updatedAt: timestamp(r.updated_at) };
    if ((result.status === "DISPATCHED") !== (result.processingId !== null) || Date.parse(result.updatedAt) < Date.parse(result.createdAt) || new Set(result.sources.map((ref) => JSON.stringify(ref.source))).size !== 1) return invalid();
    return result;
  }, 20), (item) => item.id);
};
export const listRecentAnchorFusionRequests = (anchor: KnowledgeAnchor, signal?: AbortSignal): Promise<AnchorFusionRequest[]> =>
  request(() => generatedRawResponse(api.listRecentAnchorFusionRequestsRaw({ workspaceId: inputId(anchor.workspaceId), anchorId: inputId(anchor.id) }, generatedRequestInit(signal))), (value) => decodeAnchorFusionRecent(value, anchor));

export interface AnchorRecommendation {
  id: string; workspaceId: string; noteId: string; anchorId: string | null; basisRevisionId: string; expectedScopeVersion: number | null;
  kind: "INITIAL_SCOPE" | "SOURCE_ASSOCIATION" | "SCOPE_ADJUSTMENT";
  status: "PENDING" | "RUNNING" | "SUCCEEDED" | "NO_RECOMMENDATION" | "FAILED" | "RECOVERY_REQUIRED";
  recommendation: { title: string; kind: AnchorRecommendation["kind"]; scope: AnchorScope | null; reason: string; evidence: SynthesisSourceRef[] } | null;
  proposalId: string | null; errorCode: string | null; retryable: boolean; version: number; createdAt: string; updatedAt: string;
}
export const decodeAnchorRecommendation = (value: unknown, workspaceId: string): AnchorRecommendation => {
  const v = object(value, ["id", "workspace_id", "note_id", "anchor_id", "basis_revision_id", "expected_scope_version", "kind", "status", "recommendation", "proposal_id", "error_code", "retryable", "version", "created_at", "updated_at"]);
  scope(uuid(v.workspace_id), workspaceId);
  const result: AnchorRecommendation = { id: uuid(v.id), workspaceId, noteId: uuid(v.note_id), anchorId: nullable(v.anchor_id, uuid), basisRevisionId: uuid(v.basis_revision_id), expectedScopeVersion: nullable(v.expected_scope_version, (v) => number(v, 1)),
    kind: choice(v.kind, ["INITIAL_SCOPE", "SOURCE_ASSOCIATION", "SCOPE_ADJUSTMENT"]), status: choice(v.status, ["PENDING", "RUNNING", "SUCCEEDED", "NO_RECOMMENDATION", "FAILED", "RECOVERY_REQUIRED"]),
    recommendation: nullable(v.recommendation, (value) => { const r = object(value, ["title", "kind", "scope", "reason", "evidence"]); return { title: text(r.title, 512), kind: choice(r.kind, ["INITIAL_SCOPE", "SOURCE_ASSOCIATION", "SCOPE_ADJUSTMENT"]), scope: nullable(r.scope, decodeAnchorScope), reason: text(r.reason, 4096), evidence: sources(r.evidence, workspaceId, 1, 32) }; }),
    proposalId: nullable(v.proposal_id, uuid), errorCode: nullable(v.error_code, (v) => text(v, 128)), retryable: boolean(v.retryable), version: number(v.version, 1), createdAt: timestamp(v.created_at), updatedAt: timestamp(v.updated_at) };
  const initial = result.kind === "INITIAL_SCOPE";
  if (initial ? result.anchorId !== null || result.expectedScopeVersion !== null : result.anchorId === null || result.expectedScopeVersion === null) return invalid();
  if (Date.parse(result.updatedAt) < Date.parse(result.createdAt)) return invalid();
  const failed = result.status === "FAILED" || result.status === "RECOVERY_REQUIRED";
  if (failed ? result.errorCode === null || result.recommendation !== null || result.proposalId !== null : result.errorCode !== null || result.retryable) return invalid();
  if (result.status === "RECOVERY_REQUIRED" && result.retryable) return invalid();
  if (result.status === "SUCCEEDED") {
    const recommendation = result.recommendation;
    if (recommendation === null || (recommendation.kind === "INITIAL_SCOPE") !== initial || (recommendation.kind === "SOURCE_ASSOCIATION") !== (recommendation.scope === null) || initial !== (result.proposalId === null)) return invalid();
  } else if (result.recommendation !== null || result.proposalId !== null) return invalid();
  return result;
};
export const listAnchorRecommendations = (workspaceId: string, noteId: string, afterId: string | null = null, signal?: AbortSignal): Promise<AnchorPage<AnchorRecommendation>> =>
  request(() => generatedRawResponse(api.listAnchorRecommendationsRaw({ workspaceId: inputId(workspaceId), noteId: inputId(noteId), limit: 20, ...(afterId === null ? {} : { afterId: inputId(afterId) }) }, generatedRequestInit(signal))), (value) => anchorPage(value, (v) => { const result = decodeAnchorRecommendation(v, workspaceId); scope(result.noteId, noteId); return result; }, afterId));
export const getAnchorRecommendation = (workspaceId: string, requestId: string, signal?: AbortSignal): Promise<AnchorRecommendation> =>
  request(() => generatedRawResponse(api.getAnchorRecommendationRaw({ workspaceId: inputId(workspaceId), requestId: inputId(requestId) }, generatedRequestInit(signal))), (value) => { const result = decodeAnchorRecommendation(value, workspaceId); scope(result.id, requestId); return result; });
export interface AnchorAnalysisInput { note: SynthesisNote; idempotencyKey: string; signal?: AbortSignal }
export const requestAnchorRecommendation = (input: AnchorAnalysisInput): Promise<AnchorRecommendation> => {
  if (input.note.currentRevisionId === null) throw new SynthesisApiError("INVALID_REQUEST", "笔记尚无可分析的内容。");
  const basis = input.note.currentRevisionId;
  return request(() => generatedRawResponse(api.requestAnchorRecommendationRaw({ workspaceId: inputId(input.note.workspaceId), idempotencyKey: text(input.idempotencyKey, 128), requestAnchorRecommendation: { note_id: inputId(input.note.id), basis_revision_id: inputId(basis), expected_note_version: input.note.version } }, generatedRequestInit(input.signal))), (value) => { const result = decodeAnchorRecommendation(value, input.note.workspaceId); scope(result.noteId, input.note.id); scope(result.basisRevisionId, basis); if (result.kind !== "INITIAL_SCOPE") return invalid(); return result; }, 202);
};
export const retryAnchorRecommendation = (input: { request: AnchorRecommendation; idempotencyKey: string; signal?: AbortSignal }): Promise<AnchorRecommendation> => {
  const prior = input.request;
  if (prior.status !== "FAILED" || !prior.retryable) throw new SynthesisApiError("INVALID_REQUEST", "此分析不能直接重试。");
  return request(() => generatedRawResponse(api.retryAnchorRecommendationRaw({ workspaceId: inputId(prior.workspaceId), requestId: inputId(prior.id), idempotencyKey: text(input.idempotencyKey, 128), retryAnchorRecommendation: { expected_version: prior.version } }, generatedRequestInit(input.signal))), (value) => { const result = decodeAnchorRecommendation(value, prior.workspaceId); scope(result.id, prior.id); scope(result.noteId, prior.noteId); scope(result.basisRevisionId, prior.basisRevisionId); if (result.version <= prior.version) return invalid(); return result; }, 202);
};
export const confirmInitialAnchor = (input: { note: SynthesisNote; request: AnchorRecommendation; title: string; scope: AnchorScope; idempotencyKey: string; signal?: AbortSignal }): Promise<KnowledgeAnchor> => {
  const { note, request: recommendation } = input;
  if (recommendation.status !== "SUCCEEDED" || recommendation.kind !== "INITIAL_SCOPE" || recommendation.recommendation?.scope === null || recommendation.recommendation === null || recommendation.noteId !== note.id || recommendation.workspaceId !== note.workspaceId || recommendation.basisRevisionId !== note.currentRevisionId) throw new SynthesisApiError("INVALID_REQUEST", "笔记内容已变化，请重新分析维护范围。");
  const title = text(input.title, 512); const confirmedScope = decodeAnchorScope(input.scope);
  return request(() => generatedRawResponse(api.createKnowledgeAnchorRaw({ workspaceId: inputId(note.workspaceId), idempotencyKey: text(input.idempotencyKey, 128), createAnchorRequest: { note_id: inputId(note.id), basis_revision_id: inputId(recommendation.basisRevisionId), expected_note_version: note.version, title, scope: confirmedScope } }, generatedRequestInit(input.signal))), (value) => { const v = object(value, ["anchor", "replayed"]); boolean(v.replayed); const anchor = decodeKnowledgeAnchor(v.anchor, note.workspaceId); scope(anchor.noteId, note.id); scope(anchor.basisRevisionId, recommendation.basisRevisionId); if (anchor.title !== title || JSON.stringify(anchor.scope) !== JSON.stringify(confirmedScope)) return invalid(); return anchor; }, 201);
};

export interface SynthesisSourceImpact {
  id: string; reason: "SOURCE_REMOVED" | "SOURCE_QUARANTINED"; detectedAt: string; currentlyUnavailable: boolean;
  reference: SynthesisSourceRef; itemIds: string[];
}
export const decodeSynthesisSourceImpacts = (value: unknown, revision: SynthesisRevision): SynthesisSourceImpact[] => {
  const v = object(value, ["workspace_id", "note_id", "revision_id", "items"]);
  scope(uuid(v.workspace_id), revision.workspaceId); scope(uuid(v.note_id), revision.noteId); scope(uuid(v.revision_id), revision.id);
  const seen = new Set<string>();
  return array(v.items, (value): SynthesisSourceImpact => {
    const item = object(value, ["id", "reason", "detected_at", "currently_unavailable", "reference", "item_ids"]);
    const id = uuid(item.id);
    if (item.reason !== "SOURCE_REMOVED" && item.reason !== "SOURCE_QUARANTINED") return invalid();
    const reference = decodeSynthesisSourceRef(item.reference, revision.workspaceId);
    const identity = synthesisSourceIdentity(reference);
    const expected = revision.items.filter((candidate) => synthesisItemSources(candidate).some((ref) => synthesisSourceIdentity(ref) === identity)).map((item) => item.id);
    const itemIds = array(item.item_ids, uuid, 128);
    const key = `${id}:${reference.sourceSpanId}`;
    if (expected.length === 0 || itemIds.length !== expected.length || new Set(itemIds).size !== itemIds.length || itemIds.some((id) => !expected.includes(id)) || seen.has(key)) return invalid();
    seen.add(key);
    return { id, reason: item.reason, detectedAt: timestamp(item.detected_at), currentlyUnavailable: boolean(item.currently_unavailable), reference, itemIds };
  }, 512);
};
export const getSynthesisSourceImpacts = (revision: SynthesisRevision, signal?: AbortSignal): Promise<SynthesisSourceImpact[]> =>
  request(() => generatedRawResponse(api.getSynthesisSourceImpactsRaw({ workspaceId: inputId(revision.workspaceId), noteId: inputId(revision.noteId), revisionId: inputId(revision.id) }, generatedRequestInit(signal))), (value) => decodeSynthesisSourceImpacts(value, revision));

export interface SynthesisBodyImpact {
  id: string; itemId: string; upstreamNoteId: string; upstreamRevisionId: string; upstreamItemId: string;
  upstreamPublicationId: string; publicationId: string; publishedRevisionId: string;
  reason: "CONTENT_CHANGED" | "ITEM_MISSING"; detectedAt: string;
}
export interface SynthesisBodyImpactPage { items: SynthesisBodyImpact[]; nextAfterId: string | null }
export const decodeSynthesisBodyImpacts = (value: unknown, revision: SynthesisRevision, afterId: string | null = null, limit = 20): SynthesisBodyImpactPage => {
  const v = object(value, ["workspace_id", "note_id", "revision_id", "items", "next_after_id"]);
  scope(uuid(v.workspace_id), revision.workspaceId); scope(uuid(v.note_id), revision.noteId); scope(uuid(v.revision_id), revision.id);
  let previous = afterId ?? "";
  const seen = new Set<string>();
  const items = array(v.items, (value): SynthesisBodyImpact => {
    const item = object(value, ["id", "item_id", "upstream_note_id", "upstream_revision_id", "upstream_item_id", "upstream_publication_id", "publication_id", "published_revision_id", "reason", "detected_at"]);
    const id = uuid(item.id), itemId = uuid(item.item_id), upstreamNoteId = uuid(item.upstream_note_id), upstreamRevisionId = uuid(item.upstream_revision_id), upstreamItemId = uuid(item.upstream_item_id), upstreamPublicationId = uuid(item.upstream_publication_id), publicationId = uuid(item.publication_id), publishedRevisionId = uuid(item.published_revision_id);
    const ref = revision.items.find((candidate) => candidate.id === itemId)?.bodyReference;
    const key = `${itemId}:${publicationId}`;
    if (id <= previous || seen.has(key) || ref?.workspaceId !== revision.workspaceId || ref.noteId !== upstreamNoteId || ref.revisionId !== upstreamRevisionId || ref.itemId !== upstreamItemId || ref.publicationId !== upstreamPublicationId || upstreamNoteId === revision.noteId || upstreamRevisionId === publishedRevisionId || upstreamPublicationId === publicationId || (item.reason !== "CONTENT_CHANGED" && item.reason !== "ITEM_MISSING")) return invalid();
    previous = id; seen.add(key);
    return { id, itemId, upstreamNoteId, upstreamRevisionId, upstreamItemId, upstreamPublicationId, publicationId, publishedRevisionId, reason: item.reason, detectedAt: timestamp(item.detected_at) };
  }, limit);
  const nextAfterId = v.next_after_id === null ? null : uuid(v.next_after_id);
  if (nextAfterId !== null && (items.length !== limit || nextAfterId !== previous)) return invalid();
  return { items, nextAfterId };
};
export const getSynthesisBodyImpacts = (revision: SynthesisRevision, afterId: string | null = null, signal?: AbortSignal): Promise<SynthesisBodyImpactPage> =>
  request(() => generatedRawResponse(api.getSynthesisBodyImpactsRaw({ workspaceId: inputId(revision.workspaceId), noteId: inputId(revision.noteId), revisionId: inputId(revision.id), limit: 20, ...(afterId === null ? {} : { afterId: inputId(afterId) }) }, generatedRequestInit(signal))), (value) => decodeSynthesisBodyImpacts(value, revision, afterId));

export interface SynthesisUpdateSummary { revisionId: string; sourceReviewCount: number; bodyReviewCount: number }
export interface SynthesisNoteUpdateSummary { noteId: string; items: SynthesisUpdateSummary[] }
export const decodeSynthesisUpdateSummaries = (value: unknown, workspaceId: string, notes: SynthesisNoteSummary[]): SynthesisNoteUpdateSummary[] => {
  const v = object(value, ["workspace_id", "items"]);
  scope(uuid(v.workspace_id), workspaceId);
  const seen = new Set<string>();
  const result = array(v.items, (raw): SynthesisNoteUpdateSummary => {
    const n = object(raw, ["note_id", "current_revision_id", "published_revision_id", "items"]);
    const noteId = uuid(n.note_id), note = notes.find((item) => item.note.id === noteId);
    if (note?.note.workspaceId !== workspaceId || seen.has(noteId)) return invalid();
    seen.add(noteId);
    const current = uuid(n.current_revision_id), published = n.published_revision_id === "" ? null : uuid(n.published_revision_id);
    if (current !== note.currentRevision?.id || current !== note.note.currentRevisionId || published !== (note.publishedRevision?.id ?? null)) return invalid();
    const revisions = new Set<string>();
    const items = array(n.items, (raw): SynthesisUpdateSummary => {
      const r = object(raw, ["revision_id", "source_review_count", "body_review_count"]);
      const revisionId = uuid(r.revision_id);
      if (revisions.has(revisionId) || (revisionId !== current && revisionId !== published)) return invalid();
      revisions.add(revisionId);
      if (typeof r.source_review_count !== "number" || !Number.isSafeInteger(r.source_review_count) || r.source_review_count < 0 || typeof r.body_review_count !== "number" || !Number.isSafeInteger(r.body_review_count) || r.body_review_count < 0) return invalid();
      return { revisionId, sourceReviewCount: r.source_review_count, bodyReviewCount: r.body_review_count };
    }, 2);
    if (!revisions.has(current) || (published !== null && !revisions.has(published))) return invalid();
    return { noteId, items };
  }, 50);
  if (result.length !== notes.length) return invalid();
  return result;
};
export const getSynthesisUpdateSummaries = (workspaceId: string, notes: SynthesisNoteSummary[], signal?: AbortSignal): Promise<SynthesisNoteUpdateSummary[]> => {
  if (notes.length < 1 || notes.length > 50 || new Set(notes.map((n) => n.note.id)).size !== notes.length) return invalid();
  return request(() => generatedRawResponse(api.getSynthesisUpdateSummariesRaw({ workspaceId: inputId(workspaceId), noteIds: notes.map((n) => inputId(n.note.id)).join(",") }, generatedRequestInit(signal))), (value) => decodeSynthesisUpdateSummaries(value, workspaceId, notes));
};
