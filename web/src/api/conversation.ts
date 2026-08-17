import { authFetch } from "./auth";
import {
  canonicalUuidPattern as uuidPattern,
  hasOnlyKeys,
  isRecord,
} from "../shared/codec";

export type ConversationStatus = "open" | "archived";
export type SearchMode = "keyword" | "semantic" | "hybrid";
export type AnswerDepth = "concise" | "standard" | "detailed";
export type OutputFormat = "markdown" | "outline";
export type QuestionMode = "rag" | "workspace_analysis";
export type PublicationStatus = "pending" | "completed" | "refused" | "clarification_required" | "failed" | "cancelled";
export type WorkflowStatus = "pending" | "running" | "paused" | "waiting_for_human" | "retry_wait" | "succeeded" | "failed" | "cancelled";
export type RAGStage = "plan.started" | "plan.completed" | "retrieval.started" | "retrieval.completed" | "validation.started" | "validation.completed";
export type FeedbackType = "helpful" | "incorrect" | "irrelevant_citation" | "broken_citation" | "missing_source";

export interface Problem {
  errorCode: string;
  message: string;
  retryable: boolean;
  workflowRunId?: string;
  details?: Readonly<Record<string, unknown>>;
}

export class ConversationApiError extends Error implements Problem {
  readonly errorCode: string;
  readonly retryable: boolean;
  readonly status: number | null;
  readonly workflowRunId?: string;
  readonly details?: Readonly<Record<string, unknown>>;

  constructor(problem: Problem, status: number | null, options?: ErrorOptions) {
    super(problem.message, options);
    this.name = "ConversationApiError";
    this.errorCode = problem.errorCode;
    this.retryable = problem.retryable;
    this.status = status;
    if (problem.workflowRunId !== undefined) this.workflowRunId = problem.workflowRunId;
    if (problem.details !== undefined) this.details = problem.details;
  }
}

export interface QuestionScope {
  retrievalMode: SearchMode;
  sourceIds: string[];
  sourceVersionIds: string[];
  pathPrefixes: string[];
  capturedAtFrom: string | null;
  capturedAtBefore: string | null;
  allowOriginalSources: boolean;
  allowWeb: boolean;
}

export interface Conversation {
  id: string;
  workspaceId: string;
  status: ConversationStatus;
  title: string | null;
  version: number;
  lastActivityAt: string;
  createdAt: string;
  updatedAt: string;
  archivedAt: string | null;
}

export interface Question {
  id: string;
  workspaceId: string;
  conversationId: string;
  mode: QuestionMode;
  ordinal: number;
  contextThroughOrdinal: number;
  question: string;
  scope: QuestionScope;
  answerDepth: AnswerDepth;
  outputFormat: OutputFormat;
  createdAt: string;
}

export interface WorkflowProjection {
  runId: string;
  status: WorkflowStatus;
  version: number;
  updatedAt: string;
  statusUrl: string;
}

export interface CitationIdentity {
  id: string;
  workspaceId: string;
  indexVersionId: string;
  chunkId: string;
  sourceVersionId: string;
  sourceSpanId: string;
}

export interface AnswerCitation extends CitationIdentity { href: string }
export interface Assertion { id: string; text: string; kind: "FACTUAL" | "MODEL_INFERENCE"; citationIds: string[] }
export interface ConflictPosition { claimId: string; position: string; applicability: Readonly<Record<string, unknown>>; citationIds: string[]; updatedAt: string }
export interface RelatedTopic { topicId: string; name: string; citationIds: string[] }

export interface RAGAnswerResult {
  resultType: "rag_answer";
  schemaId: "agent.rag-answer";
  schemaVersion: "v2";
  modelRunRef: string;
  payload: {
    conclusion: string;
    assertions: Assertion[];
    citations: CitationIdentity[];
    conflictPositions: ConflictPosition[];
    conflictSummary: string;
    relatedTopics: RelatedTopic[];
    followUpQuestions: string[];
  };
}

export type RefusalReasonCode = "NO_RELEVANT_EVIDENCE" | "UNAPPROVED_EVIDENCE_ONLY" | "CITATION_UNRESOLVABLE" | "EVIDENCE_INSUFFICIENT" | "EXTERNAL_FACT_NOT_AUTHORIZED" | "CONFLICT_NOT_CONDITIONABLE" | "VALIDATION_EXHAUSTED";
export interface RefusalResult {
  resultType: "refusal";
  schemaId: "agent.refusal";
  schemaVersion: "v1";
  modelRunRef: string;
  payload: { reasonCode: RefusalReasonCode; summary: string; retrievalScope: string; missingRequirements: string[]; suggestedActions: string[] };
}
export interface ClarificationResult {
  resultType: "clarification";
  schemaId: "conversation.clarification";
  schemaVersion: "v1";
  modelRunRef: string;
  payload: { reason: string; question: string; suggestedScopes: string[] };
}
export interface WorkspaceAnalysisGitStatus {
  branch: string;
  head: string;
  clean: boolean;
  stagedCount: number;
  unstagedCount: number;
  untrackedCount: number;
  conflictCount: number;
}
export interface WorkspaceAnalysisBudgetSummary {
  modelCalls: 3;
  toolCalls: number;
  inputTokens: number;
  outputTokens: number;
  estimatedCostMicrounits: number | null;
}
export interface WorkspaceAnalysisProposalSuggestion { summary: string; citationIds: string[]; href: "/proposals" }
export interface WorkspaceAnalysisAnswerResult {
  resultType: "workspace_analysis";
  schemaId: "conversation.workspace_analysis_answer";
  schemaVersion: "v1";
  modelRunRef: string;
  payload: {
    answerMarkdown: string;
    citations: CitationIdentity[];
    gitStatus: WorkspaceAnalysisGitStatus;
    budget: WorkspaceAnalysisBudgetSummary;
    proposalSuggestion: WorkspaceAnalysisProposalSuggestion | null;
    terminationReason: "COMPLETED";
  };
}
export type WorkspaceAnalysisRefusalReasonCode = "WORKSPACE_ANALYSIS_EVIDENCE_INSUFFICIENT" | "WORKSPACE_ANALYSIS_CITATION_INVALID" | "WORKSPACE_ANALYSIS_FAITHFULNESS_REJECTED" | "WORKSPACE_ANALYSIS_MODEL_REFUSED";
export interface WorkspaceAnalysisRefusalResult {
  resultType: "workspace_analysis_refusal";
  schemaId: "conversation.workspace_analysis_refusal";
  schemaVersion: "v1";
  modelRunRef: string | null;
  payload: { reasonCode: WorkspaceAnalysisRefusalReasonCode; summary: string };
}
export type WorkspaceAnalysisTerminationReason = "WORKSPACE_ANALYSIS_BUDGET_EXHAUSTED" | "WORKSPACE_ANALYSIS_RECEIPT_INVALID" | "WORKSPACE_ANALYSIS_RESULT_UNKNOWN" | "WORKSPACE_ANALYSIS_DEADLINE_EXCEEDED" | "WORKSPACE_ANALYSIS_MODEL_FAILED" | "WORKSPACE_ANALYSIS_TOOL_FAILED" | "WORKSPACE_ANALYSIS_RUNTIME_FAILED" | "WORKSPACE_ANALYSIS_CANCELLED";
export interface WorkspaceAnalysisTerminationResult {
  resultType: "workspace_analysis_termination";
  schemaId: "conversation.workspace_analysis_termination";
  schemaVersion: "v1";
  modelRunRef: string | null;
  payload: { terminationReason: WorkspaceAnalysisTerminationReason; summary: string };
}
export type AnswerResult = RAGAnswerResult | RefusalResult | ClarificationResult | WorkspaceAnalysisAnswerResult | WorkspaceAnalysisRefusalResult | WorkspaceAnalysisTerminationResult;

export interface RetrievalSummary {
  rewrites: string[];
  requestedMode: SearchMode;
  effectiveMode: SearchMode;
  scope: Omit<QuestionScope, "retrievalMode">;
  indexVersionId: string | null;
  embeddingVersionId: string | null;
  candidateCount: number;
  selectedCount: number;
  conflictCount: number;
  degradations: { capability: "vector" | "rerank"; code: string; retryable: boolean }[];
}

interface AnswerBase {
  id: string;
  workspaceId: string;
  conversationId: string;
  questionId: string;
  currentStage: RAGStage | null;
  workflow: WorkflowProjection;
  version: number;
  createdAt: string;
  updatedAt: string;
}

export interface PendingAnswer extends AnswerBase {
  publicationStatus: "pending";
  citations: [];
  retrievalSummary: null;
}
export interface CompletedAnswer extends AnswerBase {
  publicationStatus: "completed";
  resultType: "rag_answer";
  assistantText: string;
  result: RAGAnswerResult;
  citations: AnswerCitation[];
  retrievalSummary: RetrievalSummary;
}
export interface RefusedAnswer extends AnswerBase {
  publicationStatus: "refused";
  resultType: "refusal";
  assistantText: string;
  result: RefusalResult;
  citations: [];
  retrievalSummary: RetrievalSummary;
}
export interface ClarificationAnswer extends AnswerBase {
  publicationStatus: "clarification_required";
  resultType: "clarification";
  assistantText: string;
  result: ClarificationResult;
  citations: [];
  retrievalSummary: RetrievalSummary;
}
export interface WorkspaceAnalysisClarificationAnswer extends AnswerBase {
  publicationStatus: "clarification_required";
  resultType: "clarification";
  assistantText: string;
  result: ClarificationResult;
  citations: [];
  retrievalSummary: null;
}
export interface WorkspaceAnalysisCompletedAnswer extends AnswerBase {
  publicationStatus: "completed";
  resultType: "workspace_analysis";
  assistantText: string;
  result: WorkspaceAnalysisAnswerResult;
  citations: AnswerCitation[];
  retrievalSummary: null;
}
export interface WorkspaceAnalysisRefusedAnswer extends AnswerBase {
  publicationStatus: "refused";
  resultType: "workspace_analysis_refusal";
  assistantText: string;
  result: WorkspaceAnalysisRefusalResult;
  citations: [];
  retrievalSummary: null;
}
export interface WorkspaceAnalysisFailedAnswer extends AnswerBase {
  publicationStatus: "failed";
  resultType: "workspace_analysis_termination";
  assistantText: string;
  result: WorkspaceAnalysisTerminationResult;
  citations: [];
  retrievalSummary: null;
}
export interface WorkspaceAnalysisCancelledAnswer extends AnswerBase {
  publicationStatus: "cancelled";
  resultType: "workspace_analysis_termination";
  assistantText: string;
  result: WorkspaceAnalysisTerminationResult;
  citations: [];
  retrievalSummary: null;
}
export type Answer = PendingAnswer | CompletedAnswer | RefusedAnswer | ClarificationAnswer | WorkspaceAnalysisClarificationAnswer | WorkspaceAnalysisCompletedAnswer | WorkspaceAnalysisRefusedAnswer | WorkspaceAnalysisFailedAnswer | WorkspaceAnalysisCancelledAnswer;

export type WorkspaceAnalysisTimelineRunStatus = "queued" | "running" | "succeeded" | "refused" | "clarification_required" | "failed" | "cancelled";
export type WorkspaceAnalysisTimelinePhase = "inspect_workspace" | "retrieve_evidence" | "read_evidence" | "synthesize_answer" | "validate_citations" | "review_publish";
export type WorkspaceAnalysisTimelineItemKind = "node" | "model" | "tool";
export type WorkspaceAnalysisTimelineItemStatus = "pending" | "waiting" | "started" | "succeeded" | "failed" | "refused" | "unknown" | "cancelled";
export interface WorkspaceAnalysisTimelineCounter { used: number; max: number }
export type WorkspaceAnalysisTimelineToolRef =
  | { name: "ReadGitStatus" | "SearchKnowledge"; version: 2 }
  | { name: "ReadSource" | "ValidateCitation"; version: 3 };
export type WorkspaceAnalysisTimelineSummary =
  | { kind: "git"; git: WorkspaceAnalysisGitStatus }
  | { kind: "search"; search: { hitCount: number; degradationCodes: string[] } }
  | { kind: "source"; source: { evidenceRef: string; contentHash: string; truncated: boolean } }
  | { kind: "citation_validation"; citationValidation: { validCount: number; invalidCount: number; reasonCodes: string[] } }
  | { kind: "model_usage"; modelUsage: { inputTokens: number; outputTokens: number } };
export interface WorkspaceAnalysisTimelineItem {
  sequence: number;
  kind: WorkspaceAnalysisTimelineItemKind;
  phase: WorkspaceAnalysisTimelinePhase;
  status: WorkspaceAnalysisTimelineItemStatus;
  toolRef: WorkspaceAnalysisTimelineToolRef | null;
  durationMs: number | null;
  errorCode: string | null;
  summary: WorkspaceAnalysisTimelineSummary | null;
}
export interface WorkspaceAnalysisTimeline {
  schemaId: "conversation.workspace_analysis_timeline";
  schemaVersion: "v1";
  workspaceId: string;
  answerId: string;
  analysisRunId: string;
  runStatus: WorkspaceAnalysisTimelineRunStatus;
  terminationReason: "COMPLETED" | WorkspaceAnalysisRefusalReasonCode | "WORKSPACE_ANALYSIS_CLARIFICATION_REQUIRED" | WorkspaceAnalysisTerminationReason | null;
  items: WorkspaceAnalysisTimelineItem[];
  budget: {
    modelCalls: WorkspaceAnalysisTimelineCounter;
    toolCalls: WorkspaceAnalysisTimelineCounter;
    sourceReads: WorkspaceAnalysisTimelineCounter;
    inputTokens: WorkspaceAnalysisTimelineCounter;
    outputTokens: WorkspaceAnalysisTimelineCounter;
    estimatedCostMicrounits: WorkspaceAnalysisTimelineCounter | null;
  };
  latestServerEventSequence: number;
}

export interface Turn { question: Question; answer: Answer }
export interface Page<T> { items: T[]; nextCursor?: string }
export interface QuestionAcceptance { question: Question; answer: Answer; statusUrl: string }
export interface AnswerFeedback { id: string; workspaceId: string; answerId: string; feedbackType: FeedbackType; citationId: string | null; comment: string | null; createdAt: string }
export interface VersionedResource<T> { resource: T | null; etag: string | null; notModified: boolean }

export interface CreateConversationInput { workspaceId: string; idempotencyKey: string; title?: string | null }
export interface ListInput { workspaceId: string; cursor?: string; limit?: number }
export interface GetInput { workspaceId: string; id: string; ifNoneMatch?: string }
export interface SubmitQuestionInput {
  workspaceId: string; conversationId: string; idempotencyKey: string; question: string;
  mode?: QuestionMode; scope?: Partial<QuestionScope>; answerDepth?: AnswerDepth; outputFormat?: OutputFormat;
}
export interface SubmitFeedbackInput { workspaceId: string; answerId: string; idempotencyKey: string; feedbackType: FeedbackType; citationId?: string | null; comment?: string | null }

const timestampPattern = /^\d{4}-(?:0[1-9]|1[0-2])-(?:0[1-9]|[12]\d|3[01])T(?:[01]\d|2[0-3]):[0-5]\d:[0-5]\d(?:\.\d{1,9})?(?:Z|[+-](?:[01]\d|2[0-3]):[0-5]\d)$/;
const etagPattern = /^W\/\"(?:[1-9][0-9]*|answer-[1-9][0-9]*-workflow-[1-9][0-9]*-stage-(?:none|plan\.started|plan\.completed|retrieval\.started|retrieval\.completed|validation\.started|validation\.completed))\"$/;
const lowerHashPattern = /^[0-9a-f]{64}$/;
const gitHeadPattern = /^(?:[0-9a-f]{40}|[0-9a-f]{64})$/;
const ragStages = ["plan.started", "plan.completed", "retrieval.started", "retrieval.completed", "validation.started", "validation.completed"] as const;
const textEncoder = new TextEncoder();
const invalidResponse = (field: string) => new ConversationApiError({ errorCode: "INVALID_RESPONSE", message: `Conversation API 响应字段无效：${field}`, retryable: false }, null);
const invalidRequest = (field: string) => new ConversationApiError({ errorCode: "INVALID_REQUEST", message: `Conversation API 请求字段无效：${field}`, retryable: false }, null);
const exact = (value: Record<string, unknown>, keys: readonly string[], field: string): void => {
  if (!hasOnlyKeys(value, keys)) throw invalidResponse(field);
};
const string = (value: unknown, field: string): string => { if (typeof value !== "string") throw invalidResponse(field); return value };
const bounded = (value: unknown, field: string, max: number, allowEmpty = false): string => {
  const result = string(value, field);
  if ((!allowEmpty && result === "") || result.trim() !== result || result.includes("\0") || textEncoder.encode(result).length > max) throw invalidResponse(field);
  return result;
};
const uuid = (value: unknown, field: string): string => { const result = string(value, field); if (!uuidPattern.test(result)) throw invalidResponse(field); return result };
const integer = (value: unknown, field: string, min: number, max = Number.MAX_SAFE_INTEGER): number => {
  if (typeof value !== "number" || !Number.isSafeInteger(value) || value < min || value > max) throw invalidResponse(field);
  return value;
};
const boolean = (value: unknown, field: string): boolean => { if (typeof value !== "boolean") throw invalidResponse(field); return value };
const timestamp = (value: unknown, field: string): string => {
  const result = string(value, field);
  if (!timestampPattern.test(result) || !Number.isFinite(Date.parse(result))) throw invalidResponse(field);
  const [date] = result.split("T"); const parts = date?.split("-").map(Number);
  if (parts === undefined || new Date(Date.UTC(parts[0] ?? 0, (parts[1] ?? 1) - 1, parts[2] ?? 1)).toISOString().slice(0, 10) !== date) throw invalidResponse(field);
  return result;
};
const nullable = <T>(value: unknown, reader: (input: unknown) => T): T | null => value === null ? null : reader(value);
const array = <T>(value: unknown, field: string, max: number, reader: (item: unknown, field: string) => T, min = 0): T[] => {
  if (!Array.isArray(value) || value.length < min || value.length > max) throw invalidResponse(field);
  return value.map((item, index) => reader(item, `${field}[${String(index)}]`));
};
const unique = <T>(values: T[], field: string, key: (item: T) => unknown = (item) => item): T[] => {
  if (new Set(values.map(key)).size !== values.length) throw invalidResponse(field);
  return values;
};
const enumValue = <T extends string>(value: unknown, values: readonly T[], field: string): T => {
  if (typeof value !== "string" || !values.includes(value as T)) throw invalidResponse(field);
  return value as T;
};
const href = (value: unknown, expectedPrefix: string, field: string): string => {
  const result = bounded(value, field, 2048);
  if (result !== expectedPrefix || result.includes("\\") || result.includes("..")) throw invalidResponse(field);
  return result;
};
const opaqueCursor = (value: unknown, field: string): string => bounded(value, field, 2048);

const decodeScope = (value: unknown, field: string): QuestionScope => {
  if (!isRecord(value)) throw invalidResponse(field);
  exact(value, ["retrieval_mode", "source_ids", "source_version_ids", "path_prefixes", "captured_at_from", "captured_at_before", "allow_original_sources", "allow_web"], field);
  const from = nullable(value.captured_at_from, (item) => timestamp(item, `${field}.captured_at_from`));
  const before = nullable(value.captured_at_before, (item) => timestamp(item, `${field}.captured_at_before`));
  if (from !== null && before !== null && Date.parse(from) >= Date.parse(before)) throw invalidResponse(field);
  return {
    retrievalMode: enumValue(value.retrieval_mode, ["keyword", "semantic", "hybrid"], `${field}.retrieval_mode`),
    sourceIds: unique(array(value.source_ids, `${field}.source_ids`, 100, uuid), `${field}.source_ids`),
    sourceVersionIds: unique(array(value.source_version_ids, `${field}.source_version_ids`, 100, uuid), `${field}.source_version_ids`),
    pathPrefixes: unique(array(value.path_prefixes, `${field}.path_prefixes`, 100, (item, itemField) => bounded(item, itemField, 1024)), `${field}.path_prefixes`),
    capturedAtFrom: from, capturedAtBefore: before,
    allowOriginalSources: boolean(value.allow_original_sources, `${field}.allow_original_sources`),
    allowWeb: boolean(value.allow_web, `${field}.allow_web`),
  };
};

const decodeRetrievalScope = (value: unknown, field: string): Omit<QuestionScope, "retrievalMode"> => {
  if (!isRecord(value)) throw invalidResponse(field);
  exact(value, ["source_ids", "source_version_ids", "path_prefixes", "captured_at_from", "captured_at_before", "allow_original_sources", "allow_web"], field);
  const from = nullable(value.captured_at_from, (item) => timestamp(item, `${field}.captured_at_from`));
  const before = nullable(value.captured_at_before, (item) => timestamp(item, `${field}.captured_at_before`));
  if (from !== null && before !== null && Date.parse(from) >= Date.parse(before)) throw invalidResponse(field);
  return {
    sourceIds: unique(array(value.source_ids, `${field}.source_ids`, 100, uuid), `${field}.source_ids`),
    sourceVersionIds: unique(array(value.source_version_ids, `${field}.source_version_ids`, 100, uuid), `${field}.source_version_ids`),
    pathPrefixes: unique(array(value.path_prefixes, `${field}.path_prefixes`, 100, (item, itemField) => bounded(item, itemField, 1024)), `${field}.path_prefixes`),
    capturedAtFrom: from,
    capturedAtBefore: before,
    allowOriginalSources: boolean(value.allow_original_sources, `${field}.allow_original_sources`),
    allowWeb: boolean(value.allow_web, `${field}.allow_web`),
  };
};

export const decodeConversation = (value: unknown): Conversation => {
  if (!isRecord(value)) throw invalidResponse("conversation");
  exact(value, ["id", "workspace_id", "status", "title", "version", "last_activity_at", "created_at", "updated_at", "archived_at"], "conversation");
  const status = enumValue(value.status, ["open", "archived"], "conversation.status");
  const archivedAt = nullable(value.archived_at, (item) => timestamp(item, "conversation.archived_at"));
  if ((status === "open") !== (archivedAt === null)) throw invalidResponse("conversation.archived_at");
  return { id: uuid(value.id, "conversation.id"), workspaceId: uuid(value.workspace_id, "conversation.workspace_id"), status,
    title: nullable(value.title, (item) => bounded(item, "conversation.title", 512)), version: integer(value.version, "conversation.version", 1),
    lastActivityAt: timestamp(value.last_activity_at, "conversation.last_activity_at"), createdAt: timestamp(value.created_at, "conversation.created_at"),
    updatedAt: timestamp(value.updated_at, "conversation.updated_at"), archivedAt };
};

const decodeQuestion = (value: unknown, field = "question"): Question => {
  if (!isRecord(value)) throw invalidResponse(field);
  exact(value, ["id", "workspace_id", "conversation_id", "mode", "ordinal", "context_through_ordinal", "question", "scope", "answer_depth", "output_format", "created_at"], field);
  const ordinal = integer(value.ordinal, `${field}.ordinal`, 1);
  const through = integer(value.context_through_ordinal, `${field}.context_through_ordinal`, 0);
  if (through >= ordinal) throw invalidResponse(`${field}.context_through_ordinal`);
  return { id: uuid(value.id, `${field}.id`), workspaceId: uuid(value.workspace_id, `${field}.workspace_id`), conversationId: uuid(value.conversation_id, `${field}.conversation_id`), mode: enumValue(value.mode, ["rag", "workspace_analysis"], `${field}.mode`),
    ordinal, contextThroughOrdinal: through, question: bounded(value.question, `${field}.question`, 8192), scope: decodeScope(value.scope, `${field}.scope`),
    answerDepth: enumValue(value.answer_depth, ["concise", "standard", "detailed"], `${field}.answer_depth`), outputFormat: enumValue(value.output_format, ["markdown", "outline"], `${field}.output_format`), createdAt: timestamp(value.created_at, `${field}.created_at`) };
};

const decodeWorkflow = (value: unknown, field: string): WorkflowProjection => {
  if (!isRecord(value)) throw invalidResponse(field);
  exact(value, ["run_id", "status", "version", "updated_at", "status_url"], field);
  const runId = uuid(value.run_id, `${field}.run_id`);
  return { runId, status: enumValue(value.status, ["pending", "running", "paused", "waiting_for_human", "retry_wait", "succeeded", "failed", "cancelled"], `${field}.status`),
    version: integer(value.version, `${field}.version`, 1), updatedAt: timestamp(value.updated_at, `${field}.updated_at`), statusUrl: href(value.status_url, `/api/v1/workflows/${runId}`, `${field}.status_url`) };
};

const decodeCitationIdentity = (value: unknown, field: string): CitationIdentity => {
  if (!isRecord(value)) throw invalidResponse(field);
  exact(value, ["id", "workspace_id", "index_version_id", "chunk_id", "source_version_id", "source_span_id"], field);
  return { id: bounded(value.id, `${field}.id`, 128), workspaceId: uuid(value.workspace_id, `${field}.workspace_id`), indexVersionId: uuid(value.index_version_id, `${field}.index_version_id`),
    chunkId: uuid(value.chunk_id, `${field}.chunk_id`), sourceVersionId: uuid(value.source_version_id, `${field}.source_version_id`), sourceSpanId: uuid(value.source_span_id, `${field}.source_span_id`) };
};
const decodeAnswerCitation = (value: unknown, field: string): AnswerCitation => {
  if (!isRecord(value)) throw invalidResponse(field);
  exact(value, ["id", "workspace_id", "index_version_id", "chunk_id", "source_version_id", "source_span_id", "href"], field);
  const identity = decodeCitationIdentity(Object.fromEntries(Object.entries(value).filter(([key]) => key !== "href")), field);
  return { ...identity, href: href(value.href, `/api/v1/workspaces/${identity.workspaceId}/source-versions/${identity.sourceVersionId}/spans/${identity.sourceSpanId}`, `${field}.href`) };
};

const decodeJsonValue = (value: unknown, field: string, depth = 0): unknown => {
  if (depth > 16) throw invalidResponse(field);
  if (value === null || typeof value === "string" || typeof value === "boolean") return value;
  if (typeof value === "number" && Number.isFinite(value)) return value;
  if (Array.isArray(value)) return value.map((item, index) => decodeJsonValue(item, `${field}[${String(index)}]`, depth + 1));
  if (isRecord(value)) {
    const output: Record<string, unknown> = {};
    for (const [key, item] of Object.entries(value)) output[key] = decodeJsonValue(item, `${field}.${key}`, depth + 1);
    return output;
  }
  throw invalidResponse(field);
};

const decodeRag = (value: Record<string, unknown>): RAGAnswerResult => {
  exact(value, ["result_type", "schema_id", "schema_version", "model_run_ref", "payload"], "result");
  if (value.result_type !== "rag_answer" || value.schema_id !== "agent.rag-answer" || value.schema_version !== "v2" || !isRecord(value.payload)) throw invalidResponse("result");
  const payload = value.payload;
  exact(payload, ["conclusion", "assertions", "citations", "conflict_positions", "conflict_summary", "related_topics", "follow_up_questions"], "result.payload");
  const citations = unique(array(payload.citations, "result.payload.citations", 500, decodeCitationIdentity, 1), "result.payload.citations", (item) => item.id);
  const citationIds = new Set(citations.map((item) => item.id));
  const assertions = unique(array(payload.assertions, "result.payload.assertions", 500, (item, field): Assertion => {
    if (!isRecord(item)) throw invalidResponse(field); exact(item, ["id", "text", "kind", "citation_ids"], field);
    const kind = enumValue(item.kind, ["FACTUAL", "MODEL_INFERENCE"], `${field}.kind`);
    const ids = unique(array(item.citation_ids, `${field}.citation_ids`, 500, (entry, entryField) => bounded(entry, entryField, 128)), `${field}.citation_ids`);
    if ((kind === "FACTUAL" && ids.length === 0) || (kind === "MODEL_INFERENCE" && ids.length !== 0) || ids.some((id) => !citationIds.has(id))) throw invalidResponse(field);
    return { id: bounded(item.id, `${field}.id`, 128), text: bounded(item.text, `${field}.text`, 4096), kind, citationIds: ids };
  }, 1), "result.payload.assertions", (item) => item.id);
  const conflicts = unique(array(payload.conflict_positions, "result.payload.conflict_positions", 500, (item, field): ConflictPosition => {
    if (!isRecord(item) || !isRecord(item.applicability)) throw invalidResponse(field); exact(item, ["claim_id", "position", "applicability", "citation_ids", "updated_at"], field);
    const ids = unique(array(item.citation_ids, `${field}.citation_ids`, 500, (entry, entryField) => bounded(entry, entryField, 128)), `${field}.citation_ids`);
    if (ids.length === 0 || ids.some((id) => !citationIds.has(id))) throw invalidResponse(field);
    return { claimId: uuid(item.claim_id, `${field}.claim_id`), position: bounded(item.position, `${field}.position`, 4096), applicability: decodeJsonValue(item.applicability, `${field}.applicability`) as Readonly<Record<string, unknown>>, citationIds: ids, updatedAt: timestamp(item.updated_at, `${field}.updated_at`) };
  }), "result.payload.conflict_positions", (item) => item.claimId);
  const conflictSummary = bounded(payload.conflict_summary, "result.payload.conflict_summary", 4096, conflicts.length === 0);
  if ((conflicts.length === 0 && conflictSummary !== "") || conflicts.length === 1) throw invalidResponse("result.payload.conflict_summary");
  const topics = unique(array(payload.related_topics, "result.payload.related_topics", 50, (item, field): RelatedTopic => {
    if (!isRecord(item)) throw invalidResponse(field); exact(item, ["topic_id", "name", "citation_ids"], field);
    const ids = unique(array(item.citation_ids, `${field}.citation_ids`, 100, (entry, entryField) => bounded(entry, entryField, 128), 1), `${field}.citation_ids`);
    if (ids.some((id) => !citationIds.has(id))) throw invalidResponse(field);
    return { topicId: uuid(item.topic_id, `${field}.topic_id`), name: bounded(item.name, `${field}.name`, 256), citationIds: ids };
  }, 1), "result.payload.related_topics", (item) => item.topicId);
  const followUps = unique(array(payload.follow_up_questions, "result.payload.follow_up_questions", 5, (item, field) => bounded(item, field, 2048), 1), "result.payload.follow_up_questions");
  return { resultType: "rag_answer", schemaId: "agent.rag-answer", schemaVersion: "v2", modelRunRef: uuid(value.model_run_ref, "result.model_run_ref"),
    payload: { conclusion: bounded(payload.conclusion, "result.payload.conclusion", 16384), assertions, citations, conflictPositions: conflicts, conflictSummary, relatedTopics: topics, followUpQuestions: followUps } };
};

const refusalReasons = ["NO_RELEVANT_EVIDENCE", "UNAPPROVED_EVIDENCE_ONLY", "CITATION_UNRESOLVABLE", "EVIDENCE_INSUFFICIENT", "EXTERNAL_FACT_NOT_AUTHORIZED", "CONFLICT_NOT_CONDITIONABLE", "VALIDATION_EXHAUSTED"] as const;
const workspaceAnalysisRefusalReasons = ["WORKSPACE_ANALYSIS_EVIDENCE_INSUFFICIENT", "WORKSPACE_ANALYSIS_CITATION_INVALID", "WORKSPACE_ANALYSIS_FAITHFULNESS_REJECTED", "WORKSPACE_ANALYSIS_MODEL_REFUSED"] as const;
const workspaceAnalysisTerminationReasons = ["WORKSPACE_ANALYSIS_BUDGET_EXHAUSTED", "WORKSPACE_ANALYSIS_RECEIPT_INVALID", "WORKSPACE_ANALYSIS_RESULT_UNKNOWN", "WORKSPACE_ANALYSIS_DEADLINE_EXCEEDED", "WORKSPACE_ANALYSIS_MODEL_FAILED", "WORKSPACE_ANALYSIS_TOOL_FAILED", "WORKSPACE_ANALYSIS_RUNTIME_FAILED", "WORKSPACE_ANALYSIS_CANCELLED"] as const;
const workspaceAnalysisTimelineReasons = ["COMPLETED", ...workspaceAnalysisRefusalReasons, "WORKSPACE_ANALYSIS_CLARIFICATION_REQUIRED", ...workspaceAnalysisTerminationReasons] as const;
const decodeWorkspaceAnalysisGitStatus = (value: unknown, field: string): WorkspaceAnalysisGitStatus => {
  if (!isRecord(value)) throw invalidResponse(field);
  exact(value, ["branch", "head", "clean", "staged_count", "unstaged_count", "untracked_count", "conflict_count"], field);
  const stagedCount = integer(value.staged_count, `${field}.staged_count`, 0, 1_000_000);
  const unstagedCount = integer(value.unstaged_count, `${field}.unstaged_count`, 0, 1_000_000);
  const untrackedCount = integer(value.untracked_count, `${field}.untracked_count`, 0, 1_000_000);
  const conflictCount = integer(value.conflict_count, `${field}.conflict_count`, 0, 1_000_000);
  const clean = boolean(value.clean, `${field}.clean`);
  if (!gitHeadPattern.test(string(value.head, `${field}.head`)) || clean === (stagedCount + unstagedCount + untrackedCount + conflictCount !== 0)) throw invalidResponse(field);
  return { branch: bounded(value.branch, `${field}.branch`, 255), head: string(value.head, `${field}.head`), clean, stagedCount, unstagedCount, untrackedCount, conflictCount };
};
const decodeWorkspaceAnalysisBudget = (value: unknown, field: string): WorkspaceAnalysisBudgetSummary => {
  if (!isRecord(value)) throw invalidResponse(field);
  exact(value, ["model_calls", "tool_calls", "input_tokens", "output_tokens", "estimated_cost_microunits"], field);
  if (value.model_calls !== 3) throw invalidResponse(`${field}.model_calls`);
  return {
    modelCalls: 3,
    toolCalls: integer(value.tool_calls, `${field}.tool_calls`, 4, 6),
    inputTokens: integer(value.input_tokens, `${field}.input_tokens`, 0, 196_608),
    outputTokens: integer(value.output_tokens, `${field}.output_tokens`, 0, 5_376),
    estimatedCostMicrounits: nullable(value.estimated_cost_microunits, (item) => integer(item, `${field}.estimated_cost_microunits`, 0)),
  };
};
const decodeWorkspaceAnalysisSuggestion = (value: unknown, citations: ReadonlySet<string>, field: string): WorkspaceAnalysisProposalSuggestion | null => {
  if (value === null) return null;
  if (!isRecord(value)) throw invalidResponse(field);
  exact(value, ["summary", "citation_ids", "href"], field);
  const citationIds = unique(array(value.citation_ids, `${field}.citation_ids`, 500, (item, itemField) => bounded(item, itemField, 128), 1), `${field}.citation_ids`);
  if (citationIds.some((id) => !citations.has(id)) || value.href !== "/proposals") throw invalidResponse(field);
  return { summary: bounded(value.summary, `${field}.summary`, 4096), citationIds, href: "/proposals" };
};
const decodeWorkspaceAnalysisAnswer = (value: Record<string, unknown>): WorkspaceAnalysisAnswerResult => {
  exact(value, ["result_type", "schema_id", "schema_version", "model_run_ref", "payload"], "result");
  if (value.result_type !== "workspace_analysis" || value.schema_id !== "conversation.workspace_analysis_answer" || value.schema_version !== "v1" || !isRecord(value.payload)) throw invalidResponse("result");
  const payload = value.payload;
  exact(payload, ["answer_markdown", "citations", "git_status", "budget", "proposal_suggestion", "termination_reason"], "result.payload");
  const citations = unique(array(payload.citations, "result.payload.citations", 500, decodeCitationIdentity, 1), "result.payload.citations", (item) => item.id);
  if (payload.termination_reason !== "COMPLETED") throw invalidResponse("result.payload.termination_reason");
  return {
    resultType: "workspace_analysis", schemaId: "conversation.workspace_analysis_answer", schemaVersion: "v1", modelRunRef: uuid(value.model_run_ref, "result.model_run_ref"),
    payload: {
      answerMarkdown: bounded(payload.answer_markdown, "result.payload.answer_markdown", 65_536), citations,
      gitStatus: decodeWorkspaceAnalysisGitStatus(payload.git_status, "result.payload.git_status"), budget: decodeWorkspaceAnalysisBudget(payload.budget, "result.payload.budget"),
      proposalSuggestion: decodeWorkspaceAnalysisSuggestion(payload.proposal_suggestion, new Set(citations.map((citation) => citation.id)), "result.payload.proposal_suggestion"), terminationReason: "COMPLETED",
    },
  };
};
const decodeResult = (value: unknown): AnswerResult => {
  if (!isRecord(value)) throw invalidResponse("result");
  if (value.result_type === "rag_answer") return decodeRag(value);
  exact(value, ["result_type", "schema_id", "schema_version", "model_run_ref", "payload"], "result");
  if (!isRecord(value.payload)) throw invalidResponse("result.payload");
  if (value.result_type === "refusal") {
    if (value.schema_id !== "agent.refusal" || value.schema_version !== "v1") throw invalidResponse("result");
    const payload = value.payload; exact(payload, ["reason_code", "summary", "retrieval_scope", "missing_requirements", "suggested_actions"], "result.payload");
    return { resultType: "refusal", schemaId: "agent.refusal", schemaVersion: "v1", modelRunRef: uuid(value.model_run_ref, "result.model_run_ref"), payload: {
      reasonCode: enumValue(payload.reason_code, refusalReasons, "result.payload.reason_code"), summary: bounded(payload.summary, "result.payload.summary", 4096), retrievalScope: bounded(payload.retrieval_scope, "result.payload.retrieval_scope", 4096),
      missingRequirements: unique(array(payload.missing_requirements, "result.payload.missing_requirements", 50, (item, field) => bounded(item, field, 2048)), "result.payload.missing_requirements"), suggestedActions: unique(array(payload.suggested_actions, "result.payload.suggested_actions", 50, (item, field) => bounded(item, field, 2048)), "result.payload.suggested_actions") } };
  }
  if (value.result_type === "clarification") {
    if (value.schema_id !== "conversation.clarification" || value.schema_version !== "v1") throw invalidResponse("result");
    const payload = value.payload; exact(payload, ["reason", "question", "suggested_scopes"], "result.payload");
    return { resultType: "clarification", schemaId: "conversation.clarification", schemaVersion: "v1", modelRunRef: uuid(value.model_run_ref, "result.model_run_ref"), payload: {
      reason: bounded(payload.reason, "result.payload.reason", 2048), question: bounded(payload.question, "result.payload.question", 8192), suggestedScopes: unique(array(payload.suggested_scopes, "result.payload.suggested_scopes", 10, (item, field) => bounded(item, field, 2048)), "result.payload.suggested_scopes") } };
  }
  if (value.result_type === "workspace_analysis") return decodeWorkspaceAnalysisAnswer(value);
  if (value.result_type === "workspace_analysis_refusal") {
    if (value.schema_id !== "conversation.workspace_analysis_refusal" || value.schema_version !== "v1") throw invalidResponse("result");
    const payload = value.payload; exact(payload, ["reason_code", "summary"], "result.payload");
    const reasonCode = enumValue(payload.reason_code, workspaceAnalysisRefusalReasons, "result.payload.reason_code");
    const modelRunRef = nullable(value.model_run_ref, (item) => uuid(item, "result.model_run_ref"));
    if ((reasonCode === "WORKSPACE_ANALYSIS_MODEL_REFUSED") !== (modelRunRef !== null)) throw invalidResponse("result.model_run_ref");
    return { resultType: "workspace_analysis_refusal", schemaId: "conversation.workspace_analysis_refusal", schemaVersion: "v1", modelRunRef, payload: { reasonCode, summary: bounded(payload.summary, "result.payload.summary", 4096) } };
  }
  if (value.result_type === "workspace_analysis_termination") {
    if (value.schema_id !== "conversation.workspace_analysis_termination" || value.schema_version !== "v1") throw invalidResponse("result");
    const payload = value.payload; exact(payload, ["termination_reason", "summary"], "result.payload");
    return { resultType: "workspace_analysis_termination", schemaId: "conversation.workspace_analysis_termination", schemaVersion: "v1", modelRunRef: nullable(value.model_run_ref, (item) => uuid(item, "result.model_run_ref")), payload: { terminationReason: enumValue(payload.termination_reason, workspaceAnalysisTerminationReasons, "result.payload.termination_reason"), summary: bounded(payload.summary, "result.payload.summary", 4096) } };
  }
  throw invalidResponse("result.result_type");
};

const decodeSummary = (value: unknown): RetrievalSummary | null => {
  if (value === null) return null;
  if (!isRecord(value)) throw invalidResponse("retrieval_summary");
  exact(value, ["rewrites", "requested_mode", "effective_mode", "scope", "index_version_id", "embedding_version_id", "candidate_count", "selected_count", "conflict_count", "degradations"], "retrieval_summary");
  const result: RetrievalSummary = { rewrites: unique(array(value.rewrites, "retrieval_summary.rewrites", 3, (item, field) => bounded(item, field, 8192)), "retrieval_summary.rewrites"), requestedMode: enumValue(value.requested_mode, ["keyword", "semantic", "hybrid"], "retrieval_summary.requested_mode"),
    effectiveMode: enumValue(value.effective_mode, ["keyword", "semantic", "hybrid"], "retrieval_summary.effective_mode"), scope: decodeRetrievalScope(value.scope, "retrieval_summary.scope"),
    indexVersionId: nullable(value.index_version_id, (item) => uuid(item, "retrieval_summary.index_version_id")), embeddingVersionId: nullable(value.embedding_version_id, (item) => uuid(item, "retrieval_summary.embedding_version_id")),
    candidateCount: integer(value.candidate_count, "retrieval_summary.candidate_count", 0, 300), selectedCount: integer(value.selected_count, "retrieval_summary.selected_count", 0, 100), conflictCount: integer(value.conflict_count, "retrieval_summary.conflict_count", 0),
    degradations: unique(array(value.degradations, "retrieval_summary.degradations", 2, (item, field) => { if (!isRecord(item)) throw invalidResponse(field); exact(item, ["capability", "code", "retryable"], field); return { capability: enumValue(item.capability, ["vector", "rerank"], `${field}.capability`), code: bounded(item.code, `${field}.code`, 128), retryable: boolean(item.retryable, `${field}.retryable`) } }), "retrieval_summary.degradations", (item) => item.capability) };
  if (result.selectedCount > result.candidateCount || result.conflictCount > result.candidateCount ||
      (result.embeddingVersionId !== null && result.embeddingVersionId === result.indexVersionId) ||
      (result.degradations.length === 2 && result.degradations[0]?.capability !== "vector")) throw invalidResponse("retrieval_summary");
  return result;
};

const validateRetrievalModeOutcome = (summary: RetrievalSummary): void => {
  const vectorDegraded = summary.degradations.some((item) => item.capability === "vector");
  if ((summary.requestedMode === "keyword" && (summary.effectiveMode !== "keyword" || summary.degradations.length !== 0)) ||
      (summary.requestedMode === "semantic" && (summary.effectiveMode !== "semantic" || summary.embeddingVersionId === null || vectorDegraded)) ||
      (summary.requestedMode === "hybrid" && summary.effectiveMode !== "hybrid" && summary.effectiveMode !== "keyword") ||
      (summary.requestedMode === "hybrid" && summary.effectiveMode === "keyword" && !vectorDegraded) ||
      (summary.requestedMode === "hybrid" && summary.effectiveMode === "hybrid" && summary.embeddingVersionId === null)) throw invalidResponse("retrieval_summary.mode");
};

export const decodeAnswer = (value: unknown): Answer => {
  if (!isRecord(value)) throw invalidResponse("answer");
  exact(value, ["id", "workspace_id", "conversation_id", "question_id", "publication_status", "current_stage", "result_type", "assistant_text", "citations", "result", "retrieval_summary", "workflow", "version", "created_at", "updated_at"], "answer");
  const publicationStatus = enumValue(value.publication_status, ["pending", "completed", "refused", "clarification_required", "failed", "cancelled"], "answer.publication_status");
  const id = uuid(value.id, "answer.id");
  const workspaceId = uuid(value.workspace_id, "answer.workspace_id");
  const conversationId = uuid(value.conversation_id, "answer.conversation_id");
  const questionId = uuid(value.question_id, "answer.question_id");
  const currentStage = nullable(value.current_stage, (item) => enumValue(item, ragStages, "answer.current_stage"));
  const citations = unique(array(value.citations, "answer.citations", 500, decodeAnswerCitation), "answer.citations", (item) => item.id);
  const result = value.result === undefined ? undefined : decodeResult(value.result);
  const resultType = value.result_type === undefined ? undefined : enumValue(value.result_type, ["rag_answer", "refusal", "clarification", "workspace_analysis", "workspace_analysis_refusal", "workspace_analysis_termination"], "answer.result_type");
  const assistantText = value.assistant_text === undefined ? undefined : bounded(value.assistant_text, "answer.assistant_text", 16384);
  const retrievalSummary = decodeSummary(value.retrieval_summary);
  if (publicationStatus === "pending") {
    if (result !== undefined || resultType !== undefined || assistantText !== undefined || citations.length !== 0 || retrievalSummary !== null) throw invalidResponse("answer.pending");
  } else if (result === undefined || resultType !== result.resultType || assistantText === undefined) throw invalidResponse("answer.publication");
  const isLegacyResult = result?.resultType === "rag_answer" || result?.resultType === "refusal" || result?.resultType === "clarification";
  const legacyModelRunRef = isLegacyResult ? result.modelRunRef : undefined;
  const isClarificationResult = result?.resultType === "clarification";
  if (publicationStatus !== "pending" && isLegacyResult && (legacyModelRunRef === "" || (!isClarificationResult && retrievalSummary === null))) throw invalidResponse("answer.publication");
  if (publicationStatus !== "pending" && !isLegacyResult && retrievalSummary !== null) throw invalidResponse("answer.retrieval_summary");
  if (publicationStatus === "completed" && result?.resultType === "rag_answer" && (retrievalSummary?.rewrites.length === 0 || retrievalSummary?.indexVersionId === null || retrievalSummary?.selectedCount === 0 ||
      (retrievalSummary?.effectiveMode !== "keyword" && retrievalSummary?.embeddingVersionId === null))) throw invalidResponse("answer.retrieval_summary");
  const hasRetrievalAttempt = retrievalSummary !== null && (retrievalSummary.rewrites.length !== 0 || retrievalSummary.indexVersionId !== null ||
    retrievalSummary.embeddingVersionId !== null || retrievalSummary.candidateCount !== 0 || retrievalSummary.selectedCount !== 0 || retrievalSummary.conflictCount !== 0 || retrievalSummary.degradations.length !== 0);
  if (retrievalSummary !== null && (publicationStatus === "completed" || (publicationStatus === "refused" && hasRetrievalAttempt))) validateRetrievalModeOutcome(retrievalSummary);
  if (publicationStatus === "clarification_required" && retrievalSummary !== null && (retrievalSummary.rewrites.length !== 0 || retrievalSummary.requestedMode !== retrievalSummary.effectiveMode ||
      retrievalSummary.indexVersionId !== null || retrievalSummary.embeddingVersionId !== null || retrievalSummary.candidateCount !== 0 || retrievalSummary.selectedCount !== 0 ||
      retrievalSummary.conflictCount !== 0 || retrievalSummary.degradations.length !== 0)) throw invalidResponse("answer.retrieval_summary");
  if (result?.resultType === "rag_answer") {
    const projected = new Map(citations.map((item) => [item.id, item]));
    if (assistantText !== result.payload.conclusion || result.payload.citations.some((item) => {
      const citation = projected.get(item.id);
      return citation?.workspaceId !== item.workspaceId || citation.indexVersionId !== item.indexVersionId ||
        citation.chunkId !== item.chunkId || citation.sourceVersionId !== item.sourceVersionId || citation.sourceSpanId !== item.sourceSpanId || item.workspaceId !== workspaceId;
    }) || projected.size !== result.payload.citations.length) throw invalidResponse("answer.citations");
  } else if (result?.resultType === "refusal") {
    if (assistantText !== result.payload.summary) throw invalidResponse("answer.assistant_text");
  } else if (result?.resultType === "clarification") {
    if (assistantText !== result.payload.question) throw invalidResponse("answer.assistant_text");
  } else if (result?.resultType === "workspace_analysis") {
    const projected = new Map(citations.map((item) => [item.id, item]));
    if (assistantText !== result.payload.answerMarkdown || result.payload.citations.some((item) => {
      const citation = projected.get(item.id);
      return citation?.workspaceId !== item.workspaceId || citation.indexVersionId !== item.indexVersionId || citation.chunkId !== item.chunkId ||
        citation.sourceVersionId !== item.sourceVersionId || citation.sourceSpanId !== item.sourceSpanId || item.workspaceId !== workspaceId;
    }) || projected.size !== result.payload.citations.length) throw invalidResponse("answer.citations");
  } else if (result?.resultType === "workspace_analysis_refusal" || result?.resultType === "workspace_analysis_termination") {
    if (assistantText !== result.payload.summary) throw invalidResponse("answer.assistant_text");
  }
  if (result?.resultType !== "rag_answer" && result?.resultType !== "workspace_analysis" && citations.length !== 0) throw invalidResponse("answer.citations");
  const base: AnswerBase = { id, workspaceId, conversationId, questionId, currentStage, workflow: decodeWorkflow(value.workflow, "answer.workflow"),
    version: integer(value.version, "answer.version", 1), createdAt: timestamp(value.created_at, "answer.created_at"), updatedAt: timestamp(value.updated_at, "answer.updated_at") };
  if (publicationStatus === "pending") return { ...base, publicationStatus, citations: [], retrievalSummary: null };
  if (publicationStatus === "completed" && result?.resultType === "rag_answer" && retrievalSummary !== null && assistantText !== undefined) {
    return { ...base, publicationStatus, resultType: "rag_answer", assistantText, result, citations, retrievalSummary };
  }
  if (publicationStatus === "refused" && result?.resultType === "refusal" && retrievalSummary !== null && assistantText !== undefined) {
    return { ...base, publicationStatus, resultType: "refusal", assistantText, result, citations: [], retrievalSummary };
  }
  if (publicationStatus === "clarification_required" && result?.resultType === "clarification" && retrievalSummary !== null && assistantText !== undefined) {
    return { ...base, publicationStatus, resultType: "clarification", assistantText, result, citations: [], retrievalSummary };
  }
  if (publicationStatus === "clarification_required" && result?.resultType === "clarification" && retrievalSummary === null && assistantText !== undefined && currentStage === null) {
    return { ...base, publicationStatus, resultType: "clarification", assistantText, result, citations: [], retrievalSummary: null };
  }
  if (publicationStatus === "completed" && result?.resultType === "workspace_analysis" && retrievalSummary === null && assistantText !== undefined && currentStage === null) {
    return { ...base, publicationStatus, resultType: "workspace_analysis", assistantText, result, citations, retrievalSummary: null };
  }
  if (publicationStatus === "refused" && result?.resultType === "workspace_analysis_refusal" && retrievalSummary === null && assistantText !== undefined && currentStage === null) {
    return { ...base, publicationStatus, resultType: "workspace_analysis_refusal", assistantText, result, citations: [], retrievalSummary: null };
  }
  if (publicationStatus === "failed" && result?.resultType === "workspace_analysis_termination" && result.payload.terminationReason !== "WORKSPACE_ANALYSIS_CANCELLED" && retrievalSummary === null && assistantText !== undefined && currentStage === null) {
    return { ...base, publicationStatus, resultType: "workspace_analysis_termination", assistantText, result, citations: [], retrievalSummary: null };
  }
  if (publicationStatus === "cancelled" && result?.resultType === "workspace_analysis_termination" && result.payload.terminationReason === "WORKSPACE_ANALYSIS_CANCELLED" && retrievalSummary === null && assistantText !== undefined && currentStage === null) {
    return { ...base, publicationStatus, resultType: "workspace_analysis_termination", assistantText, result, citations: [], retrievalSummary: null };
  }
  throw invalidResponse("answer.publication");
};

const workspaceAnalysisPhases = ["inspect_workspace", "retrieve_evidence", "read_evidence", "synthesize_answer", "validate_citations", "review_publish"] as const;
const workspaceAnalysisFailedItemErrors = ["WORKSPACE_ANALYSIS_BUDGET_EXHAUSTED", "WORKSPACE_ANALYSIS_RECEIPT_INVALID", "WORKSPACE_ANALYSIS_DEADLINE_EXCEEDED", "WORKSPACE_ANALYSIS_MODEL_FAILED", "WORKSPACE_ANALYSIS_TOOL_FAILED", "WORKSPACE_ANALYSIS_RUNTIME_FAILED"] as const;
const workspaceAnalysisStableCode = (value: unknown, field: string): string => {
  const code = bounded(value, field, 128);
  if (!/^[A-Z][A-Z0-9_]*$/.test(code)) throw invalidResponse(field);
  return code;
};
const validWorkspaceAnalysisItemError = (status: WorkspaceAnalysisTimelineItemStatus, code: string): boolean => {
  if (status === "failed") return workspaceAnalysisFailedItemErrors.includes(code as typeof workspaceAnalysisFailedItemErrors[number]);
  if (status === "refused") return workspaceAnalysisRefusalReasons.includes(code as WorkspaceAnalysisRefusalReasonCode);
  if (status === "unknown") return code === "WORKSPACE_ANALYSIS_RESULT_UNKNOWN";
  if (status === "cancelled") return code === "WORKSPACE_ANALYSIS_CANCELLED";
  return false;
};
const requireSortedCodes = (values: string[], field: string): string[] => {
  if (values.some((value, index) => {
    const previous = values[index - 1];
    return previous !== undefined && previous >= value;
  })) throw invalidResponse(field);
  return values;
};
const decodeWorkspaceAnalysisTimelineCounter = (value: unknown, field: string): WorkspaceAnalysisTimelineCounter => {
  if (!isRecord(value)) throw invalidResponse(field);
  exact(value, ["used", "max"], field);
  const max = integer(value.max, `${field}.max`, 1);
  const used = integer(value.used, `${field}.used`, 0, max);
  return { used, max };
};
const decodeWorkspaceAnalysisTimelineSummary = (value: unknown, field: string): WorkspaceAnalysisTimelineSummary | null => {
  if (value === null) return null;
  if (!isRecord(value)) throw invalidResponse(field);
  const kind = enumValue(value.kind, ["git", "search", "source", "citation_validation", "model_usage"], `${field}.kind`);
  if (kind === "git") {
    exact(value, ["kind", "git"], field);
    return { kind, git: decodeWorkspaceAnalysisGitStatus(value.git, `${field}.git`) };
  }
  if (kind === "search") {
    exact(value, ["kind", "search"], field);
    if (!isRecord(value.search)) throw invalidResponse(`${field}.search`);
    exact(value.search, ["hit_count", "degradation_codes"], `${field}.search`);
    return { kind, search: { hitCount: integer(value.search.hit_count, `${field}.search.hit_count`, 0, 5), degradationCodes: requireSortedCodes(unique(array(value.search.degradation_codes, `${field}.search.degradation_codes`, 16, workspaceAnalysisStableCode), `${field}.search.degradation_codes`), `${field}.search.degradation_codes`) } };
  }
  if (kind === "source") {
    exact(value, ["kind", "source"], field);
    if (!isRecord(value.source)) throw invalidResponse(`${field}.source`);
    exact(value.source, ["evidence_ref", "content_hash", "truncated"], `${field}.source`);
    const evidenceRef = bounded(value.source.evidence_ref, `${field}.source.evidence_ref`, 2);
    const contentHash = string(value.source.content_hash, `${field}.source.content_hash`);
    if (!/^E[1-3]$/.test(evidenceRef) || !lowerHashPattern.test(contentHash)) throw invalidResponse(`${field}.source`);
    return { kind, source: { evidenceRef, contentHash, truncated: boolean(value.source.truncated, `${field}.source.truncated`) } };
  }
  if (kind === "citation_validation") {
    exact(value, ["kind", "citation_validation"], field);
    if (!isRecord(value.citation_validation)) throw invalidResponse(`${field}.citation_validation`);
    exact(value.citation_validation, ["valid_count", "invalid_count", "reason_codes"], `${field}.citation_validation`);
    const validCount = integer(value.citation_validation.valid_count, `${field}.citation_validation.valid_count`, 0, 3);
    const invalidCount = integer(value.citation_validation.invalid_count, `${field}.citation_validation.invalid_count`, 0, 3);
    const reasonCodes = requireSortedCodes(unique(array(value.citation_validation.reason_codes, `${field}.citation_validation.reason_codes`, 4, (entry, entryField) => enumValue(entry, ["OK", "BINDING_MISMATCH", "CITATION_UNRESOLVABLE", "EVIDENCE_INELIGIBLE"], entryField), 1), `${field}.citation_validation.reason_codes`), `${field}.citation_validation.reason_codes`);
    if (validCount + invalidCount < 1 || validCount + invalidCount > 3) throw invalidResponse(`${field}.citation_validation`);
    return { kind, citationValidation: { validCount, invalidCount, reasonCodes } };
  }
  exact(value, ["kind", "model_usage"], field);
  if (!isRecord(value.model_usage)) throw invalidResponse(`${field}.model_usage`);
  exact(value.model_usage, ["input_tokens", "output_tokens"], `${field}.model_usage`);
  return { kind, modelUsage: { inputTokens: integer(value.model_usage.input_tokens, `${field}.model_usage.input_tokens`, 0, 65_536), outputTokens: integer(value.model_usage.output_tokens, `${field}.model_usage.output_tokens`, 0, 5_376) } };
};
const decodeWorkspaceAnalysisTimelineItem = (value: unknown, field: string): WorkspaceAnalysisTimelineItem => {
  if (!isRecord(value)) throw invalidResponse(field);
  exact(value, ["sequence", "kind", "phase", "status", "tool_ref", "duration_ms", "error_code", "summary"], field);
  const kind = enumValue(value.kind, ["node", "model", "tool"], `${field}.kind`);
  const phase = enumValue(value.phase, workspaceAnalysisPhases, `${field}.phase`);
  const status = enumValue(value.status, ["pending", "waiting", "started", "succeeded", "failed", "refused", "unknown", "cancelled"], `${field}.status`);
  const toolRef = nullable(value.tool_ref, (item): WorkspaceAnalysisTimelineToolRef => {
    if (!isRecord(item)) throw invalidResponse(`${field}.tool_ref`);
    exact(item, ["name", "version"], `${field}.tool_ref`);
    const version = integer(item.version, `${field}.tool_ref.version`, 2, 3);
    if (version !== 2 && version !== 3) throw invalidResponse(`${field}.tool_ref.version`);
    const name = enumValue(item.name, ["ReadGitStatus", "SearchKnowledge", "ReadSource", "ValidateCitation"], `${field}.tool_ref.name`);
    if (version === 2) {
      if (name !== "ReadGitStatus" && name !== "SearchKnowledge") throw invalidResponse(`${field}.tool_ref`);
      return { name, version };
    }
    if (name !== "ReadSource" && name !== "ValidateCitation") throw invalidResponse(`${field}.tool_ref`);
    return { name, version };
  });
  const durationMs = nullable(value.duration_ms, (item) => integer(item, `${field}.duration_ms`, 0, 3_600_000));
  const errorCode = nullable(value.error_code, (item) => workspaceAnalysisStableCode(item, `${field}.error_code`));
  const summary = decodeWorkspaceAnalysisTimelineSummary(value.summary, `${field}.summary`);
  const item = { sequence: integer(value.sequence, `${field}.sequence`, 1, 32), kind, phase, status, toolRef, durationMs, errorCode, summary };
  const expectedTool = phase === "inspect_workspace" ? { name: "ReadGitStatus", version: 2 } : phase === "retrieve_evidence" ? { name: "SearchKnowledge", version: 2 } : phase === "read_evidence" ? { name: "ReadSource", version: 3 } : phase === "validate_citations" ? { name: "ValidateCitation", version: 3 } : undefined;
  if ((kind === "node" && toolRef !== null) || (kind === "model" && (toolRef !== null || !["retrieve_evidence", "synthesize_answer", "review_publish"].includes(phase)))) throw invalidResponse(field);
  if (kind === "tool") {
    if (expectedTool === undefined || toolRef?.name !== expectedTool.name || toolRef.version !== expectedTool.version) throw invalidResponse(field);
  }
  const active = status === "pending" || status === "waiting" || status === "started";
  if ((active && (durationMs !== null || errorCode !== null || summary !== null)) || (!active && (durationMs === null || (status === "succeeded" ? errorCode !== null : errorCode === null) || (status !== "succeeded" && summary !== null)))) throw invalidResponse(field);
  if (!active && status !== "succeeded" && (errorCode === null || !validWorkspaceAnalysisItemError(status, errorCode))) throw invalidResponse(`${field}.error_code`);
  if (status === "succeeded") {
    const expectedSummary = kind === "node" ? null : kind === "model" ? "model_usage" : phase === "inspect_workspace" ? "git" : phase === "retrieve_evidence" ? "search" : phase === "read_evidence" ? "source" : "citation_validation";
    if ((expectedSummary === null && summary !== null) || (expectedSummary !== null && summary?.kind !== expectedSummary)) throw invalidResponse(field);
    if (kind === "model" && summary?.kind === "model_usage") {
      const maximumOutputTokens = phase === "retrieve_evidence" ? 256 : phase === "synthesize_answer" ? 4_096 : 1_024;
      if (summary.modelUsage.outputTokens > maximumOutputTokens) throw invalidResponse(`${field}.summary.model_usage.output_tokens`);
    }
  }
  return item;
};
const hasCompleteWorkspaceAnalysisProjection = (items: WorkspaceAnalysisTimelineItem[], budget: WorkspaceAnalysisTimeline["budget"]): boolean => {
  const counts = new Map<WorkspaceAnalysisTimelinePhase, number>();
  let modelCalls = 0;
  let toolCalls = 0;
  let inputTokens = 0;
  let outputTokens = 0;
  for (const item of items) {
    if (item.kind === "node") continue;
    if (item.status !== "succeeded") return false;
    counts.set(item.phase, (counts.get(item.phase) ?? 0) + 1);
    if (item.kind === "model" && item.summary?.kind === "model_usage") {
      modelCalls += 1;
      inputTokens += item.summary.modelUsage.inputTokens;
      outputTokens += item.summary.modelUsage.outputTokens;
    } else if (item.kind === "tool") {
      toolCalls += 1;
    }
  }
  const sourceReads = counts.get("read_evidence") ?? 0;
  return counts.get("inspect_workspace") === 1 && counts.get("retrieve_evidence") === 2 && sourceReads >= 1 && sourceReads <= 3 &&
    counts.get("synthesize_answer") === 1 && counts.get("validate_citations") === 1 && counts.get("review_publish") === 1 &&
    modelCalls === budget.modelCalls.used && toolCalls === budget.toolCalls.used && sourceReads === budget.sourceReads.used &&
    inputTokens === budget.inputTokens.used && outputTokens === budget.outputTokens.used;
};
export const decodeWorkspaceAnalysisTimeline = (value: unknown): WorkspaceAnalysisTimeline => {
  if (!isRecord(value)) throw invalidResponse("workspace_analysis_timeline");
  exact(value, ["schema_id", "schema_version", "workspace_id", "answer_id", "analysis_run_id", "run_status", "termination_reason", "items", "budget", "latest_server_event_sequence"], "workspace_analysis_timeline");
  if (value.schema_id !== "conversation.workspace_analysis_timeline" || value.schema_version !== "v1" || !isRecord(value.budget)) throw invalidResponse("workspace_analysis_timeline");
  exact(value.budget, ["model_calls", "tool_calls", "source_reads", "input_tokens", "output_tokens", "estimated_cost_microunits"], "workspace_analysis_timeline.budget");
  const runStatus = enumValue(value.run_status, ["queued", "running", "succeeded", "refused", "clarification_required", "failed", "cancelled"], "workspace_analysis_timeline.run_status");
  const terminationReason = nullable(value.termination_reason, (item) => enumValue(item, workspaceAnalysisTimelineReasons, "workspace_analysis_timeline.termination_reason"));
  const validReason = (runStatus === "queued" || runStatus === "running") ? terminationReason === null : runStatus === "succeeded" ? terminationReason === "COMPLETED" : runStatus === "refused" ? workspaceAnalysisRefusalReasons.includes(terminationReason as WorkspaceAnalysisRefusalReasonCode) : runStatus === "clarification_required" ? terminationReason === "WORKSPACE_ANALYSIS_CLARIFICATION_REQUIRED" : runStatus === "failed" ? terminationReason !== null && terminationReason !== "WORKSPACE_ANALYSIS_CANCELLED" && workspaceAnalysisTerminationReasons.includes(terminationReason as WorkspaceAnalysisTerminationReason) : terminationReason === "WORKSPACE_ANALYSIS_CANCELLED";
  if (!validReason) throw invalidResponse("workspace_analysis_timeline.termination_reason");
  const items = array(value.items, "workspace_analysis_timeline.items", 32, decodeWorkspaceAnalysisTimelineItem);
  if (items.some((item, index) => item.sequence !== index + 1)) throw invalidResponse("workspace_analysis_timeline.items");
  const modelCalls = decodeWorkspaceAnalysisTimelineCounter(value.budget.model_calls, "workspace_analysis_timeline.budget.model_calls");
  const toolCalls = decodeWorkspaceAnalysisTimelineCounter(value.budget.tool_calls, "workspace_analysis_timeline.budget.tool_calls");
  const sourceReads = decodeWorkspaceAnalysisTimelineCounter(value.budget.source_reads, "workspace_analysis_timeline.budget.source_reads");
  const inputTokens = decodeWorkspaceAnalysisTimelineCounter(value.budget.input_tokens, "workspace_analysis_timeline.budget.input_tokens");
  const outputTokens = decodeWorkspaceAnalysisTimelineCounter(value.budget.output_tokens, "workspace_analysis_timeline.budget.output_tokens");
  const estimatedCostMicrounits = nullable(value.budget.estimated_cost_microunits, (item) => decodeWorkspaceAnalysisTimelineCounter(item, "workspace_analysis_timeline.budget.estimated_cost_microunits"));
  if (modelCalls.max !== 3 || toolCalls.max !== 6 || sourceReads.max !== 3 || inputTokens.max !== 196_608 || outputTokens.max > 5_376 || outputTokens.max < 1_281) throw invalidResponse("workspace_analysis_timeline.budget");
  const workspaceId = uuid(value.workspace_id, "workspace_analysis_timeline.workspace_id");
  const answerId = uuid(value.answer_id, "workspace_analysis_timeline.answer_id");
  const analysisRunId = uuid(value.analysis_run_id, "workspace_analysis_timeline.analysis_run_id");
  if (new Set([workspaceId, answerId, analysisRunId]).size !== 3) throw invalidResponse("workspace_analysis_timeline.identity");
  const budget = { modelCalls, toolCalls, sourceReads, inputTokens, outputTokens, estimatedCostMicrounits };
  if (runStatus === "succeeded" && (modelCalls.used !== 3 || toolCalls.used < 4 || !hasCompleteWorkspaceAnalysisProjection(items, budget))) throw invalidResponse("workspace_analysis_timeline.success");
  return { schemaId: "conversation.workspace_analysis_timeline", schemaVersion: "v1", workspaceId, answerId, analysisRunId, runStatus, terminationReason, items, budget, latestServerEventSequence: integer(value.latest_server_event_sequence, "workspace_analysis_timeline.latest_server_event_sequence", 0) };
};

const decodePage = <T>(value: unknown, reader: (item: unknown, field?: string) => T): Page<T> => {
  if (!isRecord(value)) throw invalidResponse("page"); exact(value, ["items", "next_cursor"], "page");
  const items = array(value.items, "page.items", 100, (item, field) => reader(item, field));
  return { items, ...(value.next_cursor === undefined ? {} : { nextCursor: opaqueCursor(value.next_cursor, "page.next_cursor") }) };
};
export const decodeConversationPage = (value: unknown): Page<Conversation> => decodePage(value, decodeConversation);
export const decodeTurnPage = (value: unknown): Page<Turn> => decodePage(value, (item, field = "turn") => {
  if (!isRecord(item)) throw invalidResponse(field);
  exact(item, ["question", "answer"], field);
  const question = decodeQuestion(item.question, `${field}.question`);
  const answer = decodeAnswer(item.answer);
  if (answer.questionId !== question.id || answer.conversationId !== question.conversationId || answer.workspaceId !== question.workspaceId) throw invalidResponse(field);
  const resultType = "resultType" in answer ? answer.resultType : undefined;
  if (question.mode === "rag" && (answer.publicationStatus === "failed" || answer.publicationStatus === "cancelled" || resultType === "workspace_analysis" || resultType === "workspace_analysis_refusal" || resultType === "workspace_analysis_termination")) throw invalidResponse(`${field}.mode`);
  if (question.mode === "workspace_analysis" && (resultType === "rag_answer" || resultType === "refusal")) throw invalidResponse(`${field}.mode`);
  if ((question.mode === "rag" && answer.publicationStatus === "clarification_required" && answer.retrievalSummary === null) || (question.mode === "workspace_analysis" && answer.publicationStatus === "clarification_required" && answer.retrievalSummary !== null)) throw invalidResponse(`${field}.mode`);
  return { question, answer };
});
export const decodeQuestionAcceptance = (value: unknown): QuestionAcceptance => {
  if (!isRecord(value)) throw invalidResponse("acceptance");
  exact(value, ["question", "answer", "status_url"], "acceptance");
  const question = decodeQuestion(value.question);
  const answer = decodeAnswer(value.answer);
  if (answer.questionId !== question.id || answer.conversationId !== question.conversationId || answer.workspaceId !== question.workspaceId) throw invalidResponse("acceptance");
  const statusUrl = bounded(value.status_url, "acceptance.status_url", 2048);
  const separator = statusUrl.indexOf("?");
  if (separator < 0 || statusUrl.slice(separator + 1).includes("?") || statusUrl.slice(0, separator) !== `/api/v1/answers/${answer.id}`) throw invalidResponse("acceptance.status_url");
  const parameters = new URLSearchParams(statusUrl.slice(separator + 1));
  if ([...parameters.keys()].length !== 1 || parameters.getAll("workspace_id").length !== 1 || parameters.get("workspace_id") !== answer.workspaceId) throw invalidResponse("acceptance.status_url");
  return { question, answer, statusUrl };
};
export const decodeAnswerFeedback = (value: unknown): AnswerFeedback => {
  if (!isRecord(value)) throw invalidResponse("feedback");
  exact(value, ["id", "workspace_id", "answer_id", "feedback_type", "citation_id", "comment", "created_at"], "feedback");
  const feedbackType = enumValue(value.feedback_type, ["helpful", "incorrect", "irrelevant_citation", "broken_citation", "missing_source"], "feedback.feedback_type");
  const citationId = nullable(value.citation_id, (item) => bounded(item, "feedback.citation_id", 128));
  const citationType = feedbackType === "irrelevant_citation" || feedbackType === "broken_citation";
  if ((citationType && citationId === null) || (!citationType && citationId !== null)) throw invalidResponse("feedback.citation_id");
  return { id: uuid(value.id, "feedback.id"), workspaceId: uuid(value.workspace_id, "feedback.workspace_id"), answerId: uuid(value.answer_id, "feedback.answer_id"), feedbackType, citationId, comment: nullable(value.comment, (item) => bounded(item, "feedback.comment", 2048)), createdAt: timestamp(value.created_at, "feedback.created_at") };
};
export const decodeProblem = (value: unknown): Problem => { if (!isRecord(value)) throw invalidResponse("problem"); exact(value, ["error_code", "message", "retryable", "workflow_run_id", "details"], "problem"); const details = value.details; if (details !== undefined && !isRecord(details)) throw invalidResponse("problem.details"); return { errorCode: bounded(value.error_code, "problem.error_code", 256), message: bounded(value.message, "problem.message", 4096), retryable: boolean(value.retryable, "problem.retryable"), ...(value.workflow_run_id === undefined ? {} : { workflowRunId: uuid(value.workflow_run_id, "problem.workflow_run_id") }), ...(details === undefined ? {} : { details: decodeJsonValue(details, "problem.details") as Readonly<Record<string, unknown>> }) } };

const validateUuid = (value: unknown, field: string): string => { if (typeof value !== "string" || !uuidPattern.test(value)) throw invalidRequest(field); return value };
const validateText = (value: unknown, field: string, max: number, allowEmpty = false): string => { if (typeof value !== "string" || (!allowEmpty && value.trim() === "") || value.trim() !== value || value.includes("\0") || textEncoder.encode(value).length > max) throw invalidRequest(field); return value };
const validateKey = (value: unknown): string => validateText(value, "idempotencyKey", 128);
const query = (input: ListInput | GetInput): string => {
  validateUuid(input.workspaceId, "workspaceId");
  const params = new URLSearchParams({ workspace_id: input.workspaceId });
  const cursor: unknown = Reflect.get(input, "cursor");
  if (cursor !== undefined) params.set("cursor", validateText(cursor, "cursor", 2048));
  const limit: unknown = Reflect.get(input, "limit");
  if (limit !== undefined) {
    if (typeof limit !== "number" || !Number.isSafeInteger(limit) || limit < 1 || limit > 100) throw invalidRequest("limit");
    params.set("limit", String(limit));
  }
  return params.toString();
};
const request = async <T>(path: string, init: RequestInit, decoder: (value: unknown) => T): Promise<{ value: T; response: Response }> => {
  let response: Response; try { response = await authFetch(path, init) } catch (error: unknown) { if (error instanceof DOMException && error.name === "AbortError") throw error; throw new ConversationApiError({ errorCode: "NETWORK_ERROR", message: "无法连接 Conversation API。", retryable: true }, null, { cause: error }) }
  let payload: unknown; try { payload = await response.json() } catch (error: unknown) { throw new ConversationApiError({ errorCode: "INVALID_RESPONSE", message: "Conversation API 返回了无效 JSON。", retryable: false }, response.status, { cause: error }) }
  if (!response.ok) { try { throw new ConversationApiError(decodeProblem(payload), response.status) } catch (error: unknown) { if (error instanceof ConversationApiError && error.errorCode !== "INVALID_RESPONSE") throw error; throw new ConversationApiError({ errorCode: "INVALID_RESPONSE", message: "Conversation API 返回了无效 Problem。", retryable: false }, response.status, { cause: error }) } }
  try { return { value: decoder(payload), response } } catch (error: unknown) { if (error instanceof ConversationApiError) throw new ConversationApiError({ errorCode: error.errorCode, message: error.message, retryable: false }, response.status, { cause: error }); throw error }
};
const jsonHeaders = (key?: string): HeadersInit => ({ Accept: "application/json", "Content-Type": "application/json", ...(key === undefined ? {} : { "Idempotency-Key": key }) });
const withSignal = (signal?: AbortSignal): Pick<RequestInit, "signal"> => signal === undefined ? {} : { signal };
const readEtag = (response: Response): string => { const value = response.headers.get("ETag"); if (value === null || !etagPattern.test(value)) throw invalidResponse("ETag"); return value };

export const createConversation = async (input: CreateConversationInput, signal?: AbortSignal): Promise<VersionedResource<Conversation>> => {
  validateUuid(input.workspaceId, "workspaceId"); const key = validateKey(input.idempotencyKey); const title = input.title === undefined || input.title === null ? input.title : validateText(input.title, "title", 512);
  const result = await request("/api/v1/conversations", { method: "POST", headers: jsonHeaders(key), body: JSON.stringify({ workspace_id: input.workspaceId, ...(title === undefined ? {} : { title }) }), ...withSignal(signal) }, decodeConversation);
  return { resource: result.value, etag: readEtag(result.response), notModified: false };
};
export const listConversations = async (input: ListInput, signal?: AbortSignal): Promise<Page<Conversation>> => (await request(`/api/v1/conversations?${query(input)}`, { method: "GET", headers: { Accept: "application/json" }, ...withSignal(signal) }, decodeConversationPage)).value;
const getVersioned = async <T>(path: string, input: GetInput, decoder: (value: unknown) => T, signal?: AbortSignal): Promise<VersionedResource<T>> => {
  validateUuid(input.id, "id"); if (input.ifNoneMatch !== undefined && !etagPattern.test(input.ifNoneMatch)) throw invalidRequest("ifNoneMatch");
  let response: Response; try { response = await authFetch(`${path}?${query(input)}`, { method: "GET", headers: { Accept: "application/json", ...(input.ifNoneMatch === undefined ? {} : { "If-None-Match": input.ifNoneMatch }) }, ...withSignal(signal) }) } catch (error: unknown) { if (error instanceof DOMException && error.name === "AbortError") throw error; throw new ConversationApiError({ errorCode: "NETWORK_ERROR", message: "无法连接 Conversation API。", retryable: true }, null, { cause: error }) }
  if (response.status === 304) return { resource: null, etag: input.ifNoneMatch ?? null, notModified: true };
  let payload: unknown; try { payload = await response.json() } catch { throw new ConversationApiError({ errorCode: "INVALID_RESPONSE", message: "Conversation API 返回了无效 JSON。", retryable: false }, response.status) }
  if (!response.ok) { try { throw new ConversationApiError(decodeProblem(payload), response.status) } catch (error: unknown) { if (error instanceof ConversationApiError && error.errorCode !== "INVALID_RESPONSE") throw error; throw new ConversationApiError({ errorCode: "INVALID_RESPONSE", message: "Conversation API 返回了无效 Problem。", retryable: false }, response.status) } }
  return { resource: decoder(payload), etag: readEtag(response), notModified: false };
};
export const getConversation = (input: GetInput, signal?: AbortSignal): Promise<VersionedResource<Conversation>> => getVersioned(`/api/v1/conversations/${validateUuid(input.id, "id")}`, input, decodeConversation, signal);
export const submitQuestion = async (input: SubmitQuestionInput, signal?: AbortSignal): Promise<QuestionAcceptance> => {
  validateUuid(input.workspaceId, "workspaceId"); validateUuid(input.conversationId, "conversationId"); const key = validateKey(input.idempotencyKey); const questionText = validateText(input.question, "question", 8192);
  const mode = input.scope?.retrievalMode ?? "hybrid"; if (!["keyword", "semantic", "hybrid"].includes(mode)) throw invalidRequest("scope.retrievalMode");
  const sourceIds = input.scope?.sourceIds ?? []; const sourceVersionIds = input.scope?.sourceVersionIds ?? []; for (const [field, ids] of [["sourceIds", sourceIds], ["sourceVersionIds", sourceVersionIds]] as const) { if (ids.length > 100 || new Set(ids).size !== ids.length) throw invalidRequest(field); ids.forEach((id) => validateUuid(id, field)); }
  const paths = input.scope?.pathPrefixes ?? []; if (paths.length > 100 || new Set(paths).size !== paths.length) throw invalidRequest("pathPrefixes"); paths.forEach((path) => validateText(path, "pathPrefixes", 1024));
  const from = input.scope?.capturedAtFrom ?? null; const before = input.scope?.capturedAtBefore ?? null; for (const [field, value] of [["capturedAtFrom", from], ["capturedAtBefore", before]] as const) if (value !== null) { try { timestamp(value, field) } catch { throw invalidRequest(field) } }
  if (from !== null && before !== null && Date.parse(from) >= Date.parse(before)) throw invalidRequest("scope.timeRange");
  const depth = input.answerDepth ?? "standard"; const format = input.outputFormat ?? "markdown"; if (!["concise", "standard", "detailed"].includes(depth)) throw invalidRequest("answerDepth"); if (!["markdown", "outline"].includes(format)) throw invalidRequest("outputFormat");
  const body = { workspace_id: input.workspaceId, ...(input.mode === undefined ? {} : { mode: input.mode }), question: questionText, scope: { retrieval_mode: mode, source_ids: sourceIds, source_version_ids: sourceVersionIds, path_prefixes: paths, captured_at_from: from, captured_at_before: before, allow_original_sources: input.scope?.allowOriginalSources ?? false, allow_web: input.scope?.allowWeb ?? false }, answer_depth: depth, output_format: format };
  return (await request(`/api/v1/conversations/${input.conversationId}/questions`, { method: "POST", headers: jsonHeaders(key), body: JSON.stringify(body), ...withSignal(signal) }, decodeQuestionAcceptance)).value;
};
export const listTurns = async (input: ListInput & { conversationId: string }, signal?: AbortSignal): Promise<Page<Turn>> => { validateUuid(input.conversationId, "conversationId"); return (await request(`/api/v1/conversations/${input.conversationId}/turns?${query(input)}`, { method: "GET", headers: { Accept: "application/json" }, ...withSignal(signal) }, decodeTurnPage)).value };
export const getLatestTurn = async (input: { workspaceId: string; conversationId: string }, signal?: AbortSignal): Promise<Turn | null> => {
  validateUuid(input.workspaceId, "workspaceId"); validateUuid(input.conversationId, "conversationId");
  const page = (await request(`/api/v1/conversations/${input.conversationId}/turns?workspace_id=${input.workspaceId}&latest=true`, { method: "GET", headers: { Accept: "application/json" }, ...withSignal(signal) }, decodeTurnPage)).value;
  if (page.items.length > 1 || page.nextCursor !== undefined) throw invalidResponse("latest_turn");
  return page.items[0] ?? null;
};
export const getAnswer = (input: GetInput, signal?: AbortSignal): Promise<VersionedResource<Answer>> => getVersioned(`/api/v1/answers/${validateUuid(input.id, "id")}`, input, decodeAnswer, signal);
export const getWorkspaceAnalysisTimeline = async (input: { workspaceId: string; answerId: string }, signal?: AbortSignal): Promise<WorkspaceAnalysisTimeline> => {
  const workspaceId = validateUuid(input.workspaceId, "workspaceId");
  const answerId = validateUuid(input.answerId, "answerId");
  const timeline = (await request(`/api/v1/answers/${answerId}/analysis-timeline?workspace_id=${workspaceId}`, { method: "GET", headers: { Accept: "application/json" }, ...withSignal(signal) }, decodeWorkspaceAnalysisTimeline)).value;
  if (timeline.workspaceId !== workspaceId || timeline.answerId !== answerId) throw invalidResponse("workspace_analysis_timeline.binding");
  return timeline;
};
export const submitFeedback = async (input: SubmitFeedbackInput, signal?: AbortSignal): Promise<AnswerFeedback> => {
  validateUuid(input.workspaceId, "workspaceId"); validateUuid(input.answerId, "answerId"); const key = validateKey(input.idempotencyKey); if (!["helpful", "incorrect", "irrelevant_citation", "broken_citation", "missing_source"].includes(input.feedbackType)) throw invalidRequest("feedbackType");
  const citationType = input.feedbackType === "irrelevant_citation" || input.feedbackType === "broken_citation"; const citation = input.citationId ?? null; if ((citationType && citation === null) || (!citationType && citation !== null)) throw invalidRequest("citationId"); if (citation !== null) validateText(citation, "citationId", 128);
  const comment = input.comment ?? null; if (comment !== null) validateText(comment, "comment", 2048);
  return (await request(`/api/v1/answers/${input.answerId}/feedback`, { method: "POST", headers: jsonHeaders(key), body: JSON.stringify({ workspace_id: input.workspaceId, feedback_type: input.feedbackType, citation_id: citation, comment }), ...withSignal(signal) }, decodeAnswerFeedback)).value;
};
