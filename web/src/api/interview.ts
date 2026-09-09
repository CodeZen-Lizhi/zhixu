/** Interview 与 Learning Path 的唯一 HTTP/JSON 边界。 */
import { canonicalUuidPattern as uuidPattern, hasOnlyKeys, isAbortError, isRecord } from "../shared/codec";
import { InterviewApi as GeneratedInterviewApi } from "./generated/apis/InterviewApi";
import { ReviewApi as GeneratedReviewApi } from "./generated/apis/ReviewApi";
import type { InterviewClaimConfig as GeneratedInterviewConfig } from "./generated/models";
import { decodeNoteItemRef as decodeSynthesisNoteItem, decodeNoteQuestionSource as decodeSynthesisNoteSource, decodeNoteRevisionRef as decodeSynthesisNoteRevision, type NoteItemRef, type NoteQuestionSource, type NoteRevisionRef } from "./synthesis";
import {
  generatedConfiguration,
  generatedRawResponse,
  generatedRequestInit,
} from "./generated-client";

const interviewApi = new GeneratedInterviewApi(generatedConfiguration);
const reviewApi = new GeneratedReviewApi(generatedConfiguration);

export type InterviewDifficulty = "FOUNDATION" | "INTERMEDIATE" | "ADVANCED";
export type InterviewSessionStatus = "ACTIVE" | "COMPLETED" | "CANCELLED";
export type InterviewQuestionStatus = "PENDING" | "ANSWERED" | "SKIPPED";
export type LearningPathStatus = "ACTIVE" | "PAUSED" | "COMPLETED";
export type LearningPathStepStatus = "PENDING" | "IN_PROGRESS" | "COMPLETED" | "SKIPPED";
export type LearningPathStepTargetStatus = "IN_PROGRESS" | "COMPLETED" | "SKIPPED";
export type InterviewJsonValue = string | number | boolean | null | InterviewJsonValue[] | { [key: string]: InterviewJsonValue };
export type InterviewJsonObject = Record<string, InterviewJsonValue>;

export interface InterviewEvidence {
  schemaVersion: "interview-evidence/v1";
  claimId: string;
  sourceVersionId: string;
  sourceSpanId: string;
  evidenceHash: string;
  supportType: "SUPPORTS";
  sourceVersionHref: string;
  sourceSpanHref: string;
}

export interface InterviewConfig {
  schemaVersion: "interview/v1";
  role: string;
  scope: { claimIds: string[]; topicIds: string[]; noteRevision?: NoteRevisionRef };
  difficulty: InterviewDifficulty;
  durationMinutes: number;
  questionCount: number;
  maxFollowUps: number;
}

export interface InterviewSession {
  id: string;
  workspaceId: string;
  config: InterviewConfig;
  status: InterviewSessionStatus;
  version: number;
  followUpCount: number;
  startedAt: string;
  endedAt?: string;
}

/** Interview 列表只返回可恢复的 Session 元数据，不包含题目、回答、评分或 Evidence。 */
export interface InterviewSessionPage {
  workspaceId: string;
  items: InterviewSession[];
  nextCursor?: string;
}

export interface ListInterviewSessionsInput {
  workspaceId: string;
  limit?: number;
  cursor?: string;
}

/** 题面刻意不包含答案要点、Evidence 或用户原始回答。 */
export interface InterviewQuestion {
  id: string;
  workspaceId: string;
  sessionId: string;
  questionNo: number;
  followUpNo: number;
  parentQuestionId?: string;
  claimId: string | null;
  sourceKind?: "CLAIM" | "NOTE_REVISION";
  noteItem?: NoteItemRef;
  topicId?: string;
  prompt: string;
  status: InterviewQuestionStatus;
  createdAt: string;
  answeredAt?: string;
}

export interface InterviewScoreDimension {
  value: number;
  rationale: string;
}

export interface InterviewScore {
  schemaVersion: "interview-score/v1";
  correctness: InterviewScoreDimension;
  coverage: InterviewScoreDimension;
  boundaries: InterviewScoreDimension;
  clarity: InterviewScoreDimension;
  errors: string[];
  omissions: string[];
  evidence: InterviewEvidence[];
  noteSource?: NoteQuestionSource;
}

export interface InterviewTurn {
  id: string;
  workspaceId: string;
  sessionId: string;
  questionId: string;
  score: InterviewScore;
  decision: {
    followUpCreated: boolean;
    followUpQuestionId?: string;
    nextQuestionId?: string;
  };
  scorerVersion: string;
  createdAt: string;
}

export interface InterviewArtifactBinding {
  kind: "INTERVIEW_DOC" | "LEARNING_PATH";
  artifactId: string;
  revisionId: string;
  artifactVersion: number;
}

export interface InterviewFinding {
  claimId: string | null;
  sourceKind?: "CLAIM" | "NOTE_REVISION";
  noteSource?: NoteQuestionSource;
  topicId?: string;
  detail: string;
  evidence: InterviewEvidence[];
}

export interface InterviewReport {
  id: string;
  workspaceId: string;
  sessionId: string;
  schemaVersion: "interview-report/v1";
  summary: {
    questionsTotal: number;
    answeredTotal: number;
    skippedTotal: number;
    correctness: number;
    coverage: number;
    boundaries: number;
    clarity: number;
  };
  strengths: InterviewFinding[];
  gaps: InterviewFinding[];
  expression: InterviewFinding[];
  evidence: InterviewEvidence[];
  noteSources?: NoteQuestionSource[];
  artifact: InterviewArtifactBinding & { kind: "INTERVIEW_DOC" };
  createdAt: string;
}

export interface LearningPath {
  id: string;
  workspaceId: string;
  sessionId: string;
  reportId: string;
  artifact: InterviewArtifactBinding & { kind: "LEARNING_PATH" };
  status: LearningPathStatus;
  version: number;
  createdAt: string;
  updatedAt: string;
}

export interface LearningPathStep {
  id: string;
  workspaceId: string;
  pathId: string;
  stepNo: number;
  claimId: string | null;
  sourceKind?: "CLAIM" | "NOTE_REVISION";
  noteSource?: NoteQuestionSource;
  topicId?: string;
  sourceVersionId: string | null;
  sourceSpanId: string | null;
  evidenceHash: string | null;
  title: string;
  rationale: string;
  status: LearningPathStepStatus;
  version: number;
  createdAt: string;
  updatedAt: string;
}

export interface InterviewSnapshot {
  session: InterviewSession;
  questions: InterviewQuestion[];
  turns: InterviewTurn[];
  report?: InterviewReport;
  path?: LearningPath;
  steps: LearningPathStep[];
}

export interface StartInterviewInput {
  workspaceId: string;
  config: InterviewConfig;
  idempotencyKey: string;
}

export interface SubmitInterviewTurnInput {
  workspaceId: string;
  sessionId: string;
  questionId: string;
  userAnswer: string;
  idempotencyKey: string;
}

export interface CompleteInterviewInput {
  workspaceId: string;
  sessionId: string;
  manualEnd: boolean;
  idempotencyKey: string;
}

export interface SuggestInterviewMemoryCandidateInput {
  workspaceId: string;
  sessionId: string;
  pathId: string;
  stepId: string;
  idempotencyKey: string;
}

export interface UpdateLearningPathStatusInput {
  workspaceId: string;
  pathId: string;
  expectedVersion: number;
  status: LearningPathStatus;
  idempotencyKey: string;
}

export interface UpdateLearningPathStepInput {
  workspaceId: string;
  pathId: string;
  stepId: string;
  expectedVersion: number;
  status: LearningPathStepTargetStatus;
  idempotencyKey: string;
}

export interface StartInterviewResult {
  session: InterviewSession;
  questions: InterviewQuestion[];
  replayed: boolean;
}

export interface SubmitInterviewTurnResult {
  turn: InterviewTurn;
  followUp?: InterviewQuestion;
  nextQuestion?: InterviewQuestion;
  replayed: boolean;
}

export interface CompleteInterviewResult {
  session: InterviewSession;
  report: InterviewReport;
  path: LearningPath;
  steps: LearningPathStep[];
  replayed: boolean;
}

export interface InterviewMemoryCandidateResult {
  memoryId: string;
  replayed: boolean;
}

export interface LearningPathStatusResult {
  path: LearningPath;
  replayed: boolean;
}

export interface LearningPathStepResult {
  path: LearningPath;
  step: LearningPathStep;
  replayed: boolean;
}

export class InterviewApiError extends Error {
  constructor(
    readonly code: "INVALID_REQUEST" | "INVALID_RESPONSE" | "HTTP_ERROR" | "NETWORK_ERROR",
    readonly errorCode: string,
    message: string,
    readonly retryable: boolean,
    readonly status: number | null = null,
    readonly workflowRunId?: string,
    readonly details?: InterviewJsonObject,
    options?: ErrorOptions,
  ) {
    super(message, options);
    this.name = "InterviewApiError";
  }
}

const hashPattern = /^[0-9a-f]{64}$/;
const timestampPattern = /^\d{4}-(?:0[1-9]|1[0-2])-(?:0[1-9]|[12]\d|3[01])T(?:[01]\d|2[0-3]):[0-5]\d:[0-5]\d(?:\.\d{1,9})?(?:Z|[+-](?:[01]\d|2[0-3]):[0-5]\d)$/;
const difficultyValues: readonly InterviewDifficulty[] = ["FOUNDATION", "INTERMEDIATE", "ADVANCED"];
const sessionStatusValues: readonly InterviewSessionStatus[] = ["ACTIVE", "COMPLETED", "CANCELLED"];
const questionStatusValues: readonly InterviewQuestionStatus[] = ["PENDING", "ANSWERED", "SKIPPED"];
const pathStatusValues: readonly LearningPathStatus[] = ["ACTIVE", "PAUSED", "COMPLETED"];
const stepStatusValues: readonly LearningPathStepStatus[] = ["PENDING", "IN_PROGRESS", "COMPLETED", "SKIPPED"];
const stepTargetStatusValues: readonly LearningPathStepTargetStatus[] = ["IN_PROGRESS", "COMPLETED", "SKIPPED"];

const bytes = (value: string): number => new TextEncoder().encode(value).length;
const invalidResponse = (field: string, status: number | null = null): InterviewApiError =>
  new InterviewApiError("INVALID_RESPONSE", "INVALID_RESPONSE", `Interview 响应字段无效：${field}`, false, status);
const invalidRequest = (field: string): InterviewApiError =>
  new InterviewApiError("INVALID_REQUEST", "INVALID_REQUEST", `Interview 请求字段无效：${field}`, false);

const noteValue = <T>(decode: () => T, field: string): T => {
  try { return decode(); } catch { throw invalidResponse(field); }
};
const decodeNoteRevisionRef = (value: unknown, workspaceId: string): NoteRevisionRef => noteValue(() => decodeSynthesisNoteRevision(value, workspaceId), "note_revision");
const decodeNoteItemRef = (value: unknown, workspaceId: string): NoteItemRef => noteValue(() => decodeSynthesisNoteItem(value, workspaceId), "note_item");
const decodeNoteQuestionSource = (value: unknown, workspaceId: string): NoteQuestionSource => noteValue(() => decodeSynthesisNoteSource(value, workspaceId), "note_source");
const sameNoteItem = (left: NoteItemRef | undefined, right: NoteItemRef | undefined): boolean =>
  left === undefined || right === undefined ? left === right : left.itemId === right.itemId && left.itemKind === right.itemKind && JSON.stringify(left.revision) === JSON.stringify(right.revision);

const exact = (value: Record<string, unknown>, keys: readonly string[], field: string): void => {
  if (!hasOnlyKeys(value, keys)) throw invalidResponse(field);
};

const stringValue = (value: unknown, field: string): string => {
  if (typeof value !== "string") throw invalidResponse(field);
  return value;
};

const requiredText = (value: unknown, field: string, maximumBytes: number): string => {
  const result = stringValue(value, field);
  if (result === "" || result !== result.trim() || result.includes("\0") || bytes(result) > maximumBytes) throw invalidResponse(field);
  return result;
};

const uuid = (value: unknown, field: string): string => {
  const result = stringValue(value, field);
  if (!uuidPattern.test(result)) throw invalidResponse(field);
  return result;
};

const optionalUuid = (value: unknown, field: string): string | undefined => value === undefined ? undefined : uuid(value, field);

const integer = (value: unknown, field: string, minimum = 0, maximum = Number.MAX_SAFE_INTEGER): number => {
  if (typeof value !== "number" || !Number.isSafeInteger(value) || value < minimum || value > maximum) throw invalidResponse(field);
  return value;
};

const finiteNumber = (value: unknown, field: string, minimum: number, maximum: number): number => {
  if (typeof value !== "number" || !Number.isFinite(value) || value < minimum || value > maximum) throw invalidResponse(field);
  return value;
};

const booleanValue = (value: unknown, field: string): boolean => {
  if (typeof value !== "boolean") throw invalidResponse(field);
  return value;
};

const enumValue = <T extends string>(value: unknown, allowed: readonly T[], field: string): T => {
  if (typeof value !== "string" || !allowed.includes(value as T)) throw invalidResponse(field);
  return value as T;
};

const daysInMonth = (year: number, month: number): number => {
  if (month === 2) {
    const leapYear = year % 4 === 0 && (year % 100 !== 0 || year % 400 === 0);
    return leapYear ? 29 : 28;
  }
  return month === 4 || month === 6 || month === 9 || month === 11 ? 30 : 31;
};

const timestamp = (value: unknown, field: string): string => {
  const result = stringValue(value, field);
  if (!timestampPattern.test(result) || !Number.isFinite(Date.parse(result))) throw invalidResponse(field);
  const calendar = /^(\d{4})-(\d{2})-(\d{2})T/.exec(result);
  if (calendar === null) throw invalidResponse(field);
  const year = Number(calendar[1]);
  const month = Number(calendar[2]);
  const day = Number(calendar[3]);
  if (day > daysInMonth(year, month)) throw invalidResponse(field);
  return result;
};

const optionalTimestamp = (value: unknown, field: string): string | undefined => value === undefined ? undefined : timestamp(value, field);

const decodeJsonValue = (value: unknown, field: string, depth = 0): InterviewJsonValue => {
  if (depth > 32) throw invalidResponse(field);
  if (value === null || typeof value === "string" || typeof value === "boolean") return value;
  if (typeof value === "number") {
    if (!Number.isFinite(value)) throw invalidResponse(field);
    return value;
  }
  if (Array.isArray(value)) {
    if (value.length > 256) throw invalidResponse(field);
    return value.map((item, index) => decodeJsonValue(item, `${field}[${String(index)}]`, depth + 1));
  }
  if (!isRecord(value) || Object.keys(value).length > 256) throw invalidResponse(field);
  return Object.fromEntries(Object.entries(value).map(([key, item]) => {
    if (key === "" || key.length > 512) throw invalidResponse(field);
    return [key, decodeJsonValue(item, `${field}.${key}`, depth + 1)];
  }));
};

const decodeJsonObject = (value: unknown, field: string): InterviewJsonObject => {
  const result = decodeJsonValue(value, field);
  if (!isRecord(result)) throw invalidResponse(field);
  return result;
};

class StrictJsonParser {
  private index = 0;
  private depth = 0;

  constructor(private readonly source: string) {}

  parse(): unknown {
    this.skipWhitespace();
    const value = this.parseValue();
    this.skipWhitespace();
    if (this.index !== this.source.length) throw new SyntaxError("unexpected trailing JSON data");
    return value;
  }

  private parseValue(): unknown {
    const current = this.source[this.index];
    if (current === "{") return this.parseObject();
    if (current === "[") return this.parseArray();
    if (current === '"') return this.parseString();
    if (this.source.startsWith("true", this.index)) { this.index += 4; return true; }
    if (this.source.startsWith("false", this.index)) { this.index += 5; return false; }
    if (this.source.startsWith("null", this.index)) { this.index += 4; return null; }
    const match = /^-?(?:0|[1-9]\d*)(?:\.\d+)?(?:[eE][+-]?\d+)?/.exec(this.source.slice(this.index));
    if (match === null) throw new SyntaxError("invalid JSON value");
    this.index += match[0].length;
    const value = Number(match[0]);
    if (!Number.isFinite(value)) throw new SyntaxError("non-finite JSON number");
    return value;
  }

  private parseObject(): Record<string, unknown> {
    this.enterContainer();
    this.index += 1;
    this.skipWhitespace();
    const entries: [string, unknown][] = [];
    const keys = new Set<string>();
    if (this.source[this.index] === "}") { this.index += 1; this.leaveContainer(); return {}; }
    for (;;) {
      if (this.source[this.index] !== '"') throw new SyntaxError("invalid JSON object key");
      const key = this.parseString();
      if (keys.has(key)) throw new SyntaxError(`duplicate JSON key: ${key}`);
      keys.add(key);
      this.skipWhitespace();
      if (this.source[this.index] !== ":") throw new SyntaxError("missing JSON object colon");
      this.index += 1;
      this.skipWhitespace();
      entries.push([key, this.parseValue()]);
      this.skipWhitespace();
      const separator = this.source[this.index];
      if (separator === "}") { this.index += 1; this.leaveContainer(); return Object.fromEntries(entries); }
      if (separator !== ",") throw new SyntaxError("invalid JSON object separator");
      this.index += 1;
      this.skipWhitespace();
    }
  }

  private parseArray(): unknown[] {
    this.enterContainer();
    this.index += 1;
    this.skipWhitespace();
    const values: unknown[] = [];
    if (this.source[this.index] === "]") { this.index += 1; this.leaveContainer(); return values; }
    for (;;) {
      values.push(this.parseValue());
      this.skipWhitespace();
      const separator = this.source[this.index];
      if (separator === "]") { this.index += 1; this.leaveContainer(); return values; }
      if (separator !== ",") throw new SyntaxError("invalid JSON array separator");
      this.index += 1;
      this.skipWhitespace();
    }
  }

  private parseString(): string {
    const start = this.index;
    this.index += 1;
    while (this.index < this.source.length) {
      const current = this.source[this.index];
      if (current === '"') {
        this.index += 1;
        const parsed: unknown = JSON.parse(this.source.slice(start, this.index));
        if (typeof parsed !== "string") throw new SyntaxError("invalid JSON string");
        return parsed;
      }
      if (current === "\\") this.index += 1;
      this.index += 1;
    }
    throw new SyntaxError("unterminated JSON string");
  }

  private skipWhitespace(): void {
    while (this.index < this.source.length && " \n\r\t".includes(this.source[this.index] ?? "")) this.index += 1;
  }

  private enterContainer(): void {
    this.depth += 1;
    if (this.depth > 256) throw new SyntaxError("JSON nesting is too deep");
  }

  private leaveContainer(): void { this.depth -= 1; }
}

const parseStrictJson = (source: string): unknown => new StrictJsonParser(source).parse();

const decodeUuidList = (value: unknown, field: string): string[] => {
  if (value === undefined) return [];
  if (!Array.isArray(value)) throw invalidResponse(field);
  const result = value.map((item, index) => uuid(item, `${field}[${String(index)}]`));
  if (new Set(result).size !== result.length) throw invalidResponse(field);
  return result;
};

const decodeConfig = (value: unknown, workspaceId: string, field = "config"): InterviewConfig => {
  if (!isRecord(value)) throw invalidResponse(field);
  exact(value, ["schema_version", "role", "scope", "difficulty", "duration_minutes", "question_count", "max_follow_ups"], field);
  if (!isRecord(value.scope)) throw invalidResponse(`${field}.scope`);
  exact(value.scope, ["claim_ids", "topic_ids", "note_revision"], `${field}.scope`);
  const claimIds = decodeUuidList(value.scope.claim_ids, `${field}.scope.claim_ids`);
  const topicIds = decodeUuidList(value.scope.topic_ids, `${field}.scope.topic_ids`);
  const noteRevision = value.scope.note_revision === undefined ? undefined : decodeNoteRevisionRef(value.scope.note_revision, workspaceId);
  if (value.schema_version !== "interview/v1" || (noteRevision === undefined ? claimIds.length === 0 && topicIds.length === 0 : value.scope.claim_ids !== undefined || value.scope.topic_ids !== undefined)) throw invalidResponse(field);
  return {
    schemaVersion: "interview/v1",
    role: requiredText(value.role, `${field}.role`, 256),
    scope: { claimIds, topicIds, ...(noteRevision === undefined ? {} : { noteRevision }) },
    difficulty: enumValue(value.difficulty, difficultyValues, `${field}.difficulty`),
    durationMinutes: integer(value.duration_minutes, `${field}.duration_minutes`, 1, 240),
    questionCount: integer(value.question_count, `${field}.question_count`, 1, 20),
    maxFollowUps: integer(value.max_follow_ups, `${field}.max_follow_ups`, 0, 20),
  };
};

const sameConfig = (left: InterviewConfig, right: InterviewConfig): boolean =>
  left.role === right.role
  && left.difficulty === right.difficulty
  && left.durationMinutes === right.durationMinutes
  && left.questionCount === right.questionCount
  && left.maxFollowUps === right.maxFollowUps
  && left.scope.claimIds.length === right.scope.claimIds.length
  && left.scope.claimIds.every((item, index) => item === right.scope.claimIds[index])
  && left.scope.topicIds.length === right.scope.topicIds.length
  && left.scope.topicIds.every((item, index) => item === right.scope.topicIds[index])
  && JSON.stringify(left.scope.noteRevision) === JSON.stringify(right.scope.noteRevision);

const decodeSession = (value: unknown, field = "session"): InterviewSession => {
  if (!isRecord(value)) throw invalidResponse(field);
  exact(value, ["id", "workspace_id", "config", "status", "version", "follow_up_count", "started_at", "ended_at"], field);
  const config = decodeConfig(value.config, uuid(value.workspace_id, `${field}.workspace_id`), `${field}.config`);
  const status = enumValue(value.status, sessionStatusValues, `${field}.status`);
  const startedAt = timestamp(value.started_at, `${field}.started_at`);
  const endedAt = optionalTimestamp(value.ended_at, `${field}.ended_at`);
  if ((status === "ACTIVE") !== (endedAt === undefined) || (endedAt !== undefined && Date.parse(endedAt) < Date.parse(startedAt))) {
    throw invalidResponse(`${field}.lifecycle`);
  }
  return {
    id: uuid(value.id, `${field}.id`),
    workspaceId: uuid(value.workspace_id, `${field}.workspace_id`),
    config,
    status,
    version: integer(value.version, `${field}.version`, 1),
    followUpCount: integer(value.follow_up_count, `${field}.follow_up_count`, 0, config.maxFollowUps),
    startedAt,
    ...(endedAt === undefined ? {} : { endedAt }),
  };
};

export const decodeInterviewSessionPage = (value: unknown): InterviewSessionPage => {
  if (!isRecord(value)) throw invalidResponse("session_page");
  exact(value, ["workspace_id", "items", "next_cursor"], "session_page");
  const workspaceId = uuid(value.workspace_id, "session_page.workspace_id");
  if (!Array.isArray(value.items) || value.items.length > 100) throw invalidResponse("session_page.items");
  const items = value.items.map((item, index) => decodeSession(item, `session_page.items[${String(index)}]`));
  if (new Set(items.map((item) => item.id)).size !== items.length || items.some((item) => item.workspaceId !== workspaceId)) {
    throw invalidResponse("session_page.items");
  }
  for (let index = 1; index < items.length; index += 1) {
    const previous = items[index - 1];
    const current = items[index];
    if (previous === undefined || current === undefined) throw invalidResponse("session_page.items");
    const previousStartedAt = Date.parse(previous.startedAt);
    const currentStartedAt = Date.parse(current.startedAt);
    if (previousStartedAt < currentStartedAt || previousStartedAt === currentStartedAt && previous.id <= current.id) {
      throw invalidResponse("session_page.order");
    }
  }
  const nextCursor = value.next_cursor === undefined ? undefined : stringValue(value.next_cursor, "session_page.next_cursor");
  if (nextCursor !== undefined && (nextCursor.length > 4096 || !/^[A-Za-z0-9_-]+$/.test(nextCursor) || items.length === 0)) {
    throw invalidResponse("session_page.next_cursor");
  }
  return { workspaceId, items, ...(nextCursor === undefined ? {} : { nextCursor }) };
};

const decodeQuestion = (value: unknown, workspaceId: string, sessionId: string, field = "question"): InterviewQuestion => {
  if (!isRecord(value)) throw invalidResponse(field);
  exact(value, ["id", "workspace_id", "session_id", "question_no", "follow_up_no", "parent_question_id", "claim_id", "topic_id", "prompt", "status", "created_at", "answered_at", "source_kind", "note_item"], field);
  const sourceKind = value.source_kind === undefined ? "CLAIM" : enumValue(value.source_kind, ["CLAIM", "NOTE_REVISION"], `${field}.source_kind`);
  const noteItem = sourceKind === "NOTE_REVISION" ? decodeNoteItemRef(value.note_item, workspaceId) : undefined;
  if (sourceKind === "NOTE_REVISION" ? value.claim_id !== null || value.topic_id !== undefined : value.note_item !== undefined) throw invalidResponse(`${field}.source`);
  const followUpNo = integer(value.follow_up_no, `${field}.follow_up_no`, 0, 20);
  const parentQuestionId = optionalUuid(value.parent_question_id, `${field}.parent_question_id`);
  const status = enumValue(value.status, questionStatusValues, `${field}.status`);
  const createdAt = timestamp(value.created_at, `${field}.created_at`);
  const answeredAt = optionalTimestamp(value.answered_at, `${field}.answered_at`);
  const result: InterviewQuestion = {
    id: uuid(value.id, `${field}.id`),
    workspaceId: uuid(value.workspace_id, `${field}.workspace_id`),
    sessionId: uuid(value.session_id, `${field}.session_id`),
    questionNo: integer(value.question_no, `${field}.question_no`, 1, 20),
    followUpNo,
    ...(parentQuestionId === undefined ? {} : { parentQuestionId }),
    claimId: sourceKind === "NOTE_REVISION" ? null : uuid(value.claim_id, `${field}.claim_id`),
    sourceKind,
    ...(noteItem === undefined ? {} : { noteItem }),
    ...(value.topic_id === undefined ? {} : { topicId: uuid(value.topic_id, `${field}.topic_id`) }),
    prompt: requiredText(value.prompt, `${field}.prompt`, 8192),
    status,
    createdAt,
    ...(answeredAt === undefined ? {} : { answeredAt }),
  };
  if (result.workspaceId !== workspaceId || result.sessionId !== sessionId) throw invalidResponse(`${field}.binding`);
  if ((followUpNo === 0) !== (parentQuestionId === undefined)) throw invalidResponse(`${field}.parent_question_id`);
  if ((status === "ANSWERED") !== (answeredAt !== undefined)) throw invalidResponse(`${field}.answered_at`);
  if (answeredAt !== undefined && Date.parse(answeredAt) < Date.parse(createdAt)) throw invalidResponse(`${field}.answered_at`);
  return result;
};

const decodeEvidence = (value: unknown, workspaceId: string, field: string, expectedClaimId?: string): InterviewEvidence => {
  if (!isRecord(value)) throw invalidResponse(field);
  exact(value, ["schema_version", "claim_id", "source_version_id", "source_span_id", "evidence_hash", "support_type", "source_version_href", "source_span_href"], field);
  const claimId = uuid(value.claim_id, `${field}.claim_id`);
  const sourceVersionId = uuid(value.source_version_id, `${field}.source_version_id`);
  const sourceSpanId = uuid(value.source_span_id, `${field}.source_span_id`);
  const evidenceHash = stringValue(value.evidence_hash, `${field}.evidence_hash`);
  const sourceVersionHref = stringValue(value.source_version_href, `${field}.source_version_href`);
  const sourceSpanHref = stringValue(value.source_span_href, `${field}.source_span_href`);
  const expectedVersionHref = `/api/v1/workspaces/${workspaceId}/source-versions/${sourceVersionId}`;
  if (
    value.schema_version !== "interview-evidence/v1"
    || value.support_type !== "SUPPORTS"
    || !hashPattern.test(evidenceHash)
    || sourceVersionHref !== expectedVersionHref
    || sourceSpanHref !== `${expectedVersionHref}/spans/${sourceSpanId}`
    || (expectedClaimId !== undefined && claimId !== expectedClaimId)
  ) throw invalidResponse(field);
  return {
    schemaVersion: "interview-evidence/v1",
    claimId,
    sourceVersionId,
    sourceSpanId,
    evidenceHash,
    supportType: "SUPPORTS",
    sourceVersionHref,
    sourceSpanHref,
  };
};

const decodeEvidenceList = (
  value: unknown,
  workspaceId: string,
  field: string,
  options: { required?: boolean; expectedClaimId?: string } = {},
): InterviewEvidence[] => {
  if (!Array.isArray(value) || value.length > 64 || (options.required === true && value.length === 0)) throw invalidResponse(field);
  const result = value.map((item, index) => decodeEvidence(item, workspaceId, `${field}[${String(index)}]`, options.expectedClaimId));
  if (new Set(result.map((item) => item.evidenceHash)).size !== result.length) throw invalidResponse(field);
  return result;
};

const decodeScoreDimension = (value: unknown, field: string): InterviewScoreDimension => {
  if (!isRecord(value)) throw invalidResponse(field);
  exact(value, ["value", "rationale"], field);
  return {
    value: finiteNumber(value.value, `${field}.value`, 0, 1),
    rationale: requiredText(value.rationale, `${field}.rationale`, 2048),
  };
};

const decodeFeedbackList = (value: unknown, field: string): string[] => {
  if (value === undefined) return [];
  if (!Array.isArray(value) || value.length > 32) throw invalidResponse(field);
  const result = value.map((item, index) => requiredText(item, `${field}[${String(index)}]`, 2048));
  if (new Set(result).size !== result.length) throw invalidResponse(field);
  return result;
};

const decodeScore = (value: unknown, workspaceId: string, field: string): InterviewScore => {
  if (!isRecord(value)) throw invalidResponse(field);
  exact(value, ["schema_version", "correctness", "coverage", "boundaries", "clarity", "errors", "omissions", "evidence", "note_source"], field);
  if (value.schema_version !== "interview-score/v1") throw invalidResponse(`${field}.schema_version`);
  const noteSource = value.note_source === undefined ? undefined : decodeNoteQuestionSource(value.note_source, workspaceId);
  const evidence = decodeEvidenceList(value.evidence, workspaceId, `${field}.evidence`, { required: noteSource === undefined });
  if (noteSource !== undefined && evidence.length !== 0) throw invalidResponse(`${field}.evidence`);
  return {
    schemaVersion: "interview-score/v1",
    correctness: decodeScoreDimension(value.correctness, `${field}.correctness`),
    coverage: decodeScoreDimension(value.coverage, `${field}.coverage`),
    boundaries: decodeScoreDimension(value.boundaries, `${field}.boundaries`),
    clarity: decodeScoreDimension(value.clarity, `${field}.clarity`),
    errors: decodeFeedbackList(value.errors, `${field}.errors`),
    omissions: decodeFeedbackList(value.omissions, `${field}.omissions`),
    evidence,
    ...(noteSource === undefined ? {} : { noteSource }),
  };
};

const decodeTurn = (value: unknown, workspaceId: string, sessionId: string, field = "turn"): InterviewTurn => {
  if (!isRecord(value)) throw invalidResponse(field);
  exact(value, ["id", "workspace_id", "session_id", "question_id", "score", "decision", "scorer_version", "created_at"], field);
  if (!isRecord(value.decision)) throw invalidResponse(`${field}.decision`);
  exact(value.decision, ["follow_up_created", "follow_up_question_id", "next_question_id"], `${field}.decision`);
  const followUpCreated = booleanValue(value.decision.follow_up_created, `${field}.decision.follow_up_created`);
  const followUpQuestionId = optionalUuid(value.decision.follow_up_question_id, `${field}.decision.follow_up_question_id`);
  const nextQuestionId = optionalUuid(value.decision.next_question_id, `${field}.decision.next_question_id`);
  const result: InterviewTurn = {
    id: uuid(value.id, `${field}.id`),
    workspaceId: uuid(value.workspace_id, `${field}.workspace_id`),
    sessionId: uuid(value.session_id, `${field}.session_id`),
    questionId: uuid(value.question_id, `${field}.question_id`),
    score: decodeScore(value.score, workspaceId, `${field}.score`),
    decision: {
      followUpCreated,
      ...(followUpQuestionId === undefined ? {} : { followUpQuestionId }),
      ...(nextQuestionId === undefined ? {} : { nextQuestionId }),
    },
    scorerVersion: requiredText(value.scorer_version, `${field}.scorer_version`, 128),
    createdAt: timestamp(value.created_at, `${field}.created_at`),
  };
  if (result.workspaceId !== workspaceId || result.sessionId !== sessionId || followUpCreated !== (followUpQuestionId !== undefined)) {
    throw invalidResponse(`${field}.binding`);
  }
  return result;
};

const decodeArtifactBinding = <T extends InterviewArtifactBinding["kind"]>(value: unknown, kind: T, field: string): InterviewArtifactBinding & { kind: T } => {
  if (!isRecord(value)) throw invalidResponse(field);
  exact(value, ["kind", "artifact_id", "revision_id", "artifact_version"], field);
  if (value.kind !== kind) throw invalidResponse(`${field}.kind`);
  return {
    kind,
    artifactId: uuid(value.artifact_id, `${field}.artifact_id`),
    revisionId: uuid(value.revision_id, `${field}.revision_id`),
    artifactVersion: integer(value.artifact_version, `${field}.artifact_version`, 1),
  };
};

const decodeFinding = (value: unknown, workspaceId: string, field: string): InterviewFinding => {
  if (!isRecord(value)) throw invalidResponse(field);
  exact(value, ["claim_id", "topic_id", "detail", "evidence", "source_kind", "note_source"], field);
  const sourceKind = value.source_kind === undefined ? "CLAIM" : enumValue(value.source_kind, ["CLAIM", "NOTE_REVISION"], `${field}.source_kind`);
  const noteSource = sourceKind === "NOTE_REVISION" ? decodeNoteQuestionSource(value.note_source, workspaceId) : undefined;
  if (sourceKind === "NOTE_REVISION" ? value.claim_id !== null || value.topic_id !== undefined : value.note_source !== undefined) throw invalidResponse(`${field}.source`);
  const claimId = sourceKind === "NOTE_REVISION" ? null : uuid(value.claim_id, `${field}.claim_id`);
  const evidence = decodeEvidenceList(value.evidence, workspaceId, `${field}.evidence`, { required: noteSource === undefined, ...(claimId === null ? {} : { expectedClaimId: claimId }) });
  if (noteSource !== undefined && evidence.length !== 0) throw invalidResponse(`${field}.evidence`);
  return {
    claimId,
    sourceKind,
    ...(noteSource === undefined ? {} : { noteSource }),
    ...(value.topic_id === undefined ? {} : { topicId: uuid(value.topic_id, `${field}.topic_id`) }),
    detail: requiredText(value.detail, `${field}.detail`, 4096),
    evidence,
  };
};

const decodeFindingList = (value: unknown, workspaceId: string, field: string): InterviewFinding[] => {
  if (!Array.isArray(value) || value.length > 40) throw invalidResponse(field);
  return value.map((item, index) => decodeFinding(item, workspaceId, `${field}[${String(index)}]`));
};

const decodeReport = (value: unknown, workspaceId: string, sessionId: string, field = "report"): InterviewReport => {
  if (!isRecord(value)) throw invalidResponse(field);
  exact(value, ["id", "workspace_id", "session_id", "schema_version", "summary", "strengths", "gaps", "expression", "evidence", "artifact", "created_at", "note_sources"], field);
  if (!isRecord(value.summary)) throw invalidResponse(`${field}.summary`);
  exact(value.summary, ["questions_total", "answered_total", "skipped_total", "correctness", "coverage", "boundaries", "clarity"], `${field}.summary`);
  if (value.schema_version !== "interview-report/v1") throw invalidResponse(`${field}.schema_version`);
  const questionsTotal = integer(value.summary.questions_total, `${field}.summary.questions_total`, 1, 40);
  const answeredTotal = integer(value.summary.answered_total, `${field}.summary.answered_total`, 0, questionsTotal);
  const skippedTotal = integer(value.summary.skipped_total, `${field}.summary.skipped_total`, 0, questionsTotal);
  const noteSources = value.note_sources === undefined ? undefined : decodeNoteSourceList(value.note_sources, workspaceId, `${field}.note_sources`);
  const result: InterviewReport = {
    id: uuid(value.id, `${field}.id`),
    workspaceId: uuid(value.workspace_id, `${field}.workspace_id`),
    sessionId: uuid(value.session_id, `${field}.session_id`),
    schemaVersion: "interview-report/v1",
    summary: {
      questionsTotal,
      answeredTotal,
      skippedTotal,
      correctness: finiteNumber(value.summary.correctness, `${field}.summary.correctness`, 0, 1),
      coverage: finiteNumber(value.summary.coverage, `${field}.summary.coverage`, 0, 1),
      boundaries: finiteNumber(value.summary.boundaries, `${field}.summary.boundaries`, 0, 1),
      clarity: finiteNumber(value.summary.clarity, `${field}.summary.clarity`, 0, 1),
    },
    strengths: decodeFindingList(value.strengths, workspaceId, `${field}.strengths`),
    gaps: decodeFindingList(value.gaps, workspaceId, `${field}.gaps`),
    expression: decodeFindingList(value.expression, workspaceId, `${field}.expression`),
    evidence: decodeEvidenceList(value.evidence, workspaceId, `${field}.evidence`),
    ...(noteSources === undefined ? {} : { noteSources }),
    artifact: decodeArtifactBinding(value.artifact, "INTERVIEW_DOC", `${field}.artifact`),
    createdAt: timestamp(value.created_at, `${field}.created_at`),
  };
  if (result.workspaceId !== workspaceId || result.sessionId !== sessionId || answeredTotal + skippedTotal !== questionsTotal) {
    throw invalidResponse(`${field}.binding`);
  }
  if (noteSources !== undefined) {
    const knownSources = new Set(noteSources.map((source) => JSON.stringify(source)));
    if (result.evidence.length !== 0 || [...result.strengths, ...result.gaps, ...result.expression].some((finding) => finding.noteSource === undefined || !knownSources.has(JSON.stringify(finding.noteSource)))) throw invalidResponse(`${field}.note_sources`);
  } else if ([...result.strengths, ...result.gaps, ...result.expression].some((finding) => finding.noteSource !== undefined)) throw invalidResponse(`${field}.note_sources`);
  return result;
};

const decodeNoteSourceList = (value: unknown, workspaceId: string, field: string): NoteQuestionSource[] => {
  if (!Array.isArray(value) || value.length === 0 || value.length > 20) throw invalidResponse(field);
  const sources = value.map((source) => decodeNoteQuestionSource(source, workspaceId));
  if (new Set(sources.map((source) => `${source.revision.revisionId}:${source.itemId}`)).size !== sources.length) throw invalidResponse(field);
  return sources;
};

const decodeLearningPath = (value: unknown, workspaceId: string, sessionId?: string, field = "path"): LearningPath => {
  if (!isRecord(value)) throw invalidResponse(field);
  exact(value, ["id", "workspace_id", "session_id", "report_id", "artifact", "status", "version", "created_at", "updated_at"], field);
  const createdAt = timestamp(value.created_at, `${field}.created_at`);
  const updatedAt = timestamp(value.updated_at, `${field}.updated_at`);
  const result: LearningPath = {
    id: uuid(value.id, `${field}.id`),
    workspaceId: uuid(value.workspace_id, `${field}.workspace_id`),
    sessionId: uuid(value.session_id, `${field}.session_id`),
    reportId: uuid(value.report_id, `${field}.report_id`),
    artifact: decodeArtifactBinding(value.artifact, "LEARNING_PATH", `${field}.artifact`),
    status: enumValue(value.status, pathStatusValues, `${field}.status`),
    version: integer(value.version, `${field}.version`, 1),
    createdAt,
    updatedAt,
  };
  if (result.workspaceId !== workspaceId || (sessionId !== undefined && result.sessionId !== sessionId) || Date.parse(updatedAt) < Date.parse(createdAt)) {
    throw invalidResponse(`${field}.binding`);
  }
  return result;
};

const decodeLearningPathStep = (value: unknown, workspaceId: string, pathId: string, field = "step"): LearningPathStep => {
  if (!isRecord(value)) throw invalidResponse(field);
  exact(value, ["id", "workspace_id", "path_id", "step_no", "claim_id", "topic_id", "source_version_id", "source_span_id", "evidence_hash", "title", "rationale", "status", "version", "created_at", "updated_at", "source_kind", "note_source"], field);
  const sourceKind = value.source_kind === undefined ? "CLAIM" : enumValue(value.source_kind, ["CLAIM", "NOTE_REVISION"], `${field}.source_kind`);
  const noteSource = sourceKind === "NOTE_REVISION" ? decodeNoteQuestionSource(value.note_source, workspaceId) : undefined;
  if (sourceKind === "NOTE_REVISION" ? value.claim_id !== null || value.topic_id !== undefined || value.source_version_id !== null || value.source_span_id !== null || value.evidence_hash !== null : value.note_source !== undefined) throw invalidResponse(`${field}.source`);
  const createdAt = timestamp(value.created_at, `${field}.created_at`);
  const updatedAt = timestamp(value.updated_at, `${field}.updated_at`);
  const evidenceHash = sourceKind === "NOTE_REVISION" ? null : stringValue(value.evidence_hash, `${field}.evidence_hash`);
  const result: LearningPathStep = {
    id: uuid(value.id, `${field}.id`),
    workspaceId: uuid(value.workspace_id, `${field}.workspace_id`),
    pathId: uuid(value.path_id, `${field}.path_id`),
    stepNo: integer(value.step_no, `${field}.step_no`, 1, 40),
    claimId: sourceKind === "NOTE_REVISION" ? null : uuid(value.claim_id, `${field}.claim_id`),
    sourceKind,
    ...(noteSource === undefined ? {} : { noteSource }),
    ...(value.topic_id === undefined ? {} : { topicId: uuid(value.topic_id, `${field}.topic_id`) }),
    sourceVersionId: sourceKind === "NOTE_REVISION" ? null : uuid(value.source_version_id, `${field}.source_version_id`),
    sourceSpanId: sourceKind === "NOTE_REVISION" ? null : uuid(value.source_span_id, `${field}.source_span_id`),
    evidenceHash,
    title: requiredText(value.title, `${field}.title`, 512),
    rationale: requiredText(value.rationale, `${field}.rationale`, 4096),
    status: enumValue(value.status, stepStatusValues, `${field}.status`),
    version: integer(value.version, `${field}.version`, 1),
    createdAt,
    updatedAt,
  };
  if (result.workspaceId !== workspaceId || result.pathId !== pathId || evidenceHash !== null && !hashPattern.test(evidenceHash) || Date.parse(updatedAt) < Date.parse(createdAt)) {
    throw invalidResponse(`${field}.binding`);
  }
  return result;
};

const decodeLearningPathSteps = (value: unknown, workspaceId: string, pathId: string, field: string): LearningPathStep[] => {
  if (!Array.isArray(value) || value.length > 40) throw invalidResponse(field);
  const result = value.map((item, index) => decodeLearningPathStep(item, workspaceId, pathId, `${field}[${String(index)}]`));
  if (new Set(result.map((item) => item.id)).size !== result.length || new Set(result.map((item) => item.stepNo)).size !== result.length) {
    throw invalidResponse(field);
  }
  return result;
};

const validateCompletionSources = (session: InterviewSession, report: InterviewReport, steps: LearningPathStep[]): void => {
  const revision = session.config.scope.noteRevision;
  if (revision === undefined) {
    if (report.noteSources !== undefined || steps.some((step) => step.noteSource !== undefined)) throw invalidResponse("completion.source");
    return;
  }
  if (report.noteSources === undefined || report.noteSources.some((source) => JSON.stringify(source.revision) !== JSON.stringify(revision))) throw invalidResponse("completion.note_revision");
  const sources = new Set(report.noteSources.map((source) => JSON.stringify(source)));
  if (steps.some((step) => step.noteSource === undefined || !sources.has(JSON.stringify(step.noteSource)))) throw invalidResponse("completion.steps.note_source");
};

const validateQuestions = (questions: InterviewQuestion[], session: InterviewSession, field: string): void => {
  if (new Set(questions.map((item) => item.id)).size !== questions.length) throw invalidResponse(field);
  if (new Set(questions.map((item) => `${String(item.questionNo)}:${String(item.followUpNo)}`)).size !== questions.length) throw invalidResponse(field);
  if (questions.some((item) => item.questionNo > session.config.questionCount)) throw invalidResponse(field);
  const byId = new Map(questions.map((item) => [item.id, item]));
  for (const question of questions) {
    if (session.config.scope.noteRevision === undefined ? question.noteItem !== undefined : question.noteItem === undefined || JSON.stringify(question.noteItem.revision) !== JSON.stringify(session.config.scope.noteRevision)) throw invalidResponse(`${field}.note_revision`);
    if (question.followUpNo === 0) continue;
    const parent = question.parentQuestionId === undefined ? undefined : byId.get(question.parentQuestionId);
    if (parent?.questionNo !== question.questionNo || parent.claimId !== question.claimId || parent.followUpNo >= question.followUpNo || JSON.stringify(parent.noteItem) !== JSON.stringify(question.noteItem)) {
      throw invalidResponse(field);
    }
  }
  if (questions.filter((item) => item.followUpNo > 0).length !== session.followUpCount) throw invalidResponse(field);
};

export const decodeInterviewSnapshot = (value: unknown): InterviewSnapshot => {
  if (!isRecord(value)) throw invalidResponse("snapshot");
  exact(value, ["session", "questions", "turns", "report", "path", "steps"], "snapshot");
  const session = decodeSession(value.session, "snapshot.session");
  if (!Array.isArray(value.questions) || value.questions.length > 40 || !Array.isArray(value.turns) || value.turns.length > 40) {
    throw invalidResponse("snapshot");
  }
  const questions = value.questions.map((item, index) => decodeQuestion(item, session.workspaceId, session.id, `snapshot.questions[${String(index)}]`));
  validateQuestions(questions, session, "snapshot.questions");
  const turns = value.turns.map((item, index) => decodeTurn(item, session.workspaceId, session.id, `snapshot.turns[${String(index)}]`));
  if (new Set(turns.map((item) => item.id)).size !== turns.length || new Set(turns.map((item) => item.questionId)).size !== turns.length) {
    throw invalidResponse("snapshot.turns");
  }
  const questionsById = new Map(questions.map((item) => [item.id, item]));
  const turnsByQuestion = new Map(turns.map((item) => [item.questionId, item]));
  for (const turn of turns) {
    const question = questionsById.get(turn.questionId);
    if (question?.status !== "ANSWERED" || turn.score.evidence.some((item) => item.claimId !== question.claimId) ||
      !sameNoteItem(question.noteItem, turn.score.noteSource)) throw invalidResponse("snapshot.turns");
    const followUp = turn.decision.followUpQuestionId === undefined ? undefined : questionsById.get(turn.decision.followUpQuestionId);
    const next = turn.decision.nextQuestionId === undefined ? undefined : questionsById.get(turn.decision.nextQuestionId);
    if (turn.decision.followUpQuestionId !== undefined && followUp?.parentQuestionId !== question.id) {
      throw invalidResponse("snapshot.turns.decision");
    }
    if (turn.decision.nextQuestionId !== undefined && next === undefined) throw invalidResponse("snapshot.turns.decision");
  }
  for (const question of questions) {
    if ((question.status === "ANSWERED") !== turnsByQuestion.has(question.id)) throw invalidResponse("snapshot.questions");
  }

  const report = value.report === undefined ? undefined : decodeReport(value.report, session.workspaceId, session.id, "snapshot.report");
  const path = value.path === undefined ? undefined : decodeLearningPath(value.path, session.workspaceId, session.id, "snapshot.path");
  if ((report === undefined) !== (path === undefined)) throw invalidResponse("snapshot.completion");
  if (path === undefined) {
    if (!Array.isArray(value.steps) || value.steps.length !== 0 || session.status === "COMPLETED") throw invalidResponse("snapshot.completion");
    return { session, questions, turns, steps: [] };
  }
  if (report === undefined || session.status !== "COMPLETED" || path.reportId !== report.id) throw invalidResponse("snapshot.completion");
  const steps = decodeLearningPathSteps(value.steps, session.workspaceId, path.id, "snapshot.steps");
  validateCompletionSources(session, report, steps);
  if (report.noteSources !== undefined && (questions.some((question) => !report.noteSources?.some((source) => sameNoteItem(question.noteItem, source))) || report.noteSources.some((source) => !questions.some((question) => sameNoteItem(question.noteItem, source))))) throw invalidResponse("snapshot.report.note_sources");
  const lastQuestions = new Map<number, InterviewQuestion>();
  for (const question of questions) {
    const previous = lastQuestions.get(question.questionNo);
    if (previous === undefined || previous.followUpNo < question.followUpNo) lastQuestions.set(question.questionNo, question);
  }
  const terminalQuestions = [...lastQuestions.values()];
  const answeredTotal = terminalQuestions.filter((item) => item.status === "ANSWERED").length;
  const skippedTotal = terminalQuestions.filter((item) => item.status === "SKIPPED").length;
  if (
    report.summary.questionsTotal !== terminalQuestions.length
    || report.summary.answeredTotal !== answeredTotal
    || report.summary.skippedTotal !== skippedTotal
    || questions.some((item) => item.status === "PENDING")
    || (path.status === "COMPLETED" && steps.some((item) => item.status !== "COMPLETED" && item.status !== "SKIPPED"))
  ) throw invalidResponse("snapshot.completion");
  return { session, questions, turns, report, path, steps };
};

const normalizeConfigInput = (value: InterviewConfig): InterviewConfig => {
  if (!isRecord(value)) throw invalidRequest("config");
  const allowedConfigKeys = ["schemaVersion", "role", "scope", "difficulty", "durationMinutes", "questionCount", "maxFollowUps"];
  if (!hasOnlyKeys(value, allowedConfigKeys) || !isRecord(value.scope)) throw invalidRequest("config");
  if (!hasOnlyKeys(value.scope, ["claimIds", "topicIds"])) throw invalidRequest("config.scope");
  if (typeof value.role !== "string" || value.role === "" || value.role !== value.role.trim() || value.role.includes("\0") || bytes(value.role) > 256) {
    throw invalidRequest("config.role");
  }
  const normalizeIds = (items: unknown, field: string): string[] => {
    if (!Array.isArray(items)) throw invalidRequest(field);
    const result = items.map((item) => {
      if (typeof item !== "string" || !uuidPattern.test(item)) throw invalidRequest(field);
      return item;
    });
    if (new Set(result).size !== result.length) throw invalidRequest(field);
    return [...result].sort();
  };
  const claimIds = normalizeIds(value.scope.claimIds, "config.scope.claimIds");
  const topicIds = normalizeIds(value.scope.topicIds, "config.scope.topicIds");
  if (claimIds.length === 0 && topicIds.length === 0) throw invalidRequest("config.scope");
  if (!difficultyValues.includes(value.difficulty)) throw invalidRequest("config.difficulty");
  if (!Number.isSafeInteger(value.durationMinutes) || value.durationMinutes < 1 || value.durationMinutes > 240) throw invalidRequest("config.durationMinutes");
  if (!Number.isSafeInteger(value.questionCount) || value.questionCount < 1 || value.questionCount > 20) throw invalidRequest("config.questionCount");
  if (!Number.isSafeInteger(value.maxFollowUps) || value.maxFollowUps < 0 || value.maxFollowUps > 20) throw invalidRequest("config.maxFollowUps");
  return {
    schemaVersion: "interview/v1",
    role: value.role,
    scope: { claimIds, topicIds },
    difficulty: value.difficulty,
    durationMinutes: value.durationMinutes,
    questionCount: value.questionCount,
    maxFollowUps: value.maxFollowUps,
  };
};

const requireUuid = (value: string, field: string): string => {
  if (typeof value !== "string" || !uuidPattern.test(value)) throw invalidRequest(field);
  return value;
};

const requireIdempotencyKey = (value: string): string => {
  if (typeof value !== "string" || value === "" || value !== value.trim() || bytes(value) > 128 || /[\u0000-\u001f\u007f]/.test(value)) {
    throw invalidRequest("idempotencyKey");
  }
  return value;
};

const requireVersion = (value: number): number => {
  if (!Number.isSafeInteger(value) || value < 1) throw invalidRequest("expectedVersion");
  return value;
};

const requireUserAnswer = (value: string): string => {
  if (typeof value !== "string" || value.includes("\0") || bytes(value) > 64 * 1024) throw invalidRequest("userAnswer");
  return value;
};

const requirePathStatus = (value: LearningPathStatus): LearningPathStatus => {
  if (!pathStatusValues.includes(value)) throw invalidRequest("status");
  return value;
};

const requireStepStatus = (value: LearningPathStepTargetStatus): LearningPathStepTargetStatus => {
  if (!stepTargetStatusValues.includes(value)) throw invalidRequest("status");
  return value;
};

const decodeProblem = (value: unknown, status: number): InterviewApiError => {
  if (!isRecord(value)) return new InterviewApiError("HTTP_ERROR", "HTTP_ERROR", `Interview API 返回 HTTP ${String(status)}。`, status >= 500, status);
  exact(value, ["error_code", "message", "retryable", "workflow_run_id", "details"], "problem");
  const workflowRunId = value.workflow_run_id === undefined ? undefined : uuid(value.workflow_run_id, "problem.workflow_run_id");
  const details = value.details === undefined ? undefined : decodeJsonObject(value.details, "problem.details");
  return new InterviewApiError(
    "HTTP_ERROR",
    requiredText(value.error_code, "problem.error_code", 256),
    requiredText(value.message, "problem.message", 4096),
    booleanValue(value.retryable, "problem.retryable"),
    status,
    workflowRunId,
    details,
  );
};

const request = async (operation: Promise<Response>, expectedStatuses: readonly number[] = [200]): Promise<unknown> => {
  let response: Response;
  try {
    response = await operation;
  } catch (error: unknown) {
    if (isAbortError(error)) throw error;
    throw new InterviewApiError("NETWORK_ERROR", "NETWORK_ERROR", "无法连接 Interview API。", true, null, undefined, undefined, { cause: error });
  }
  let payload: unknown;
  try {
    payload = parseStrictJson(await response.text());
  } catch (error: unknown) {
    if (isAbortError(error)) throw error;
    throw new InterviewApiError("INVALID_RESPONSE", "INVALID_RESPONSE", "Interview API 返回了无效或包含重复字段的 JSON。", false, response.status, undefined, undefined, { cause: error });
  }
  if (!response.ok) throw decodeProblem(payload, response.status);
  if (!expectedStatuses.includes(response.status)) throw invalidResponse("http_status", response.status);
  return payload;
};

const configToWire = (config: InterviewConfig): GeneratedInterviewConfig => ({
  schema_version: config.schemaVersion,
  role: config.role,
  scope: {
    ...(config.scope.claimIds.length === 0 ? {} : { claim_ids: config.scope.claimIds }),
    ...(config.scope.topicIds.length === 0 ? {} : { topic_ids: config.scope.topicIds }),
  },
  difficulty: config.difficulty,
  duration_minutes: config.durationMinutes,
  question_count: config.questionCount,
  max_follow_ups: config.maxFollowUps,
});

export const startInterview = async (input: StartInterviewInput): Promise<StartInterviewResult> => {
  const workspaceId = requireUuid(input.workspaceId, "workspaceId");
  const config = normalizeConfigInput(input.config);
  const payload = await request(generatedRawResponse(interviewApi.startInterviewV2Raw({
    idempotencyKey: requireIdempotencyKey(input.idempotencyKey),
    startInterviewRequest: { workspace_id: workspaceId, config: configToWire(config) },
  }, generatedRequestInit())), [200, 201]);
  if (!isRecord(payload)) throw invalidResponse("start");
  exact(payload, ["session", "questions", "replayed"], "start");
  const session = decodeSession(payload.session, "start.session");
  if (!Array.isArray(payload.questions) || payload.questions.length !== config.questionCount) throw invalidResponse("start.questions");
  const questions = payload.questions.map((item, index) => decodeQuestion(item, workspaceId, session.id, `start.questions[${String(index)}]`));
  validateQuestions(questions, session, "start.questions");
  if (
    session.workspaceId !== workspaceId
    || session.status !== "ACTIVE"
    || !sameConfig(session.config, config)
    || questions.some((item) => item.status !== "PENDING" || item.followUpNo !== 0)
  ) throw invalidResponse("start.binding");
  return { session, questions, replayed: booleanValue(payload.replayed, "start.replayed") };
};

export const listInterviewSessions = async (input: ListInterviewSessionsInput, signal?: AbortSignal): Promise<InterviewSessionPage> => {
  const workspaceId = requireUuid(input.workspaceId, "workspaceId");
  const limit = input.limit ?? 20;
  if (!Number.isSafeInteger(limit) || limit < 1 || limit > 100) throw invalidRequest("limit");
  const cursor = input.cursor;
  if (cursor !== undefined && (cursor === "" || cursor !== cursor.trim() || cursor.length > 4096 || !/^[A-Za-z0-9_-]+$/.test(cursor))) {
    throw invalidRequest("cursor");
  }
  const page = decodeInterviewSessionPage(await request(
    generatedRawResponse(interviewApi.listInterviewsV2Raw({
      workspaceId,
      limit,
      ...(cursor === undefined ? {} : { cursor }),
    }, generatedRequestInit(signal))),
  ));
  if (page.workspaceId !== workspaceId) throw invalidResponse("session_page.workspace_id");
  return page;
};

export const getInterview = async (workspaceId: string, sessionId: string, signal?: AbortSignal): Promise<InterviewSnapshot> => {
  const expectedWorkspaceId = requireUuid(workspaceId, "workspaceId");
  const expectedSessionId = requireUuid(sessionId, "sessionId");
  const result = decodeInterviewSnapshot(await request(
    generatedRawResponse(interviewApi.getInterviewV2Raw({
      sessionId: expectedSessionId,
      workspaceId: expectedWorkspaceId,
    }, generatedRequestInit(signal))),
  ));
  if (result.session.workspaceId !== expectedWorkspaceId || result.session.id !== expectedSessionId) throw invalidResponse("snapshot.binding");
  return result;
};

export const submitInterviewTurn = async (input: SubmitInterviewTurnInput): Promise<SubmitInterviewTurnResult> => {
  const workspaceId = requireUuid(input.workspaceId, "workspaceId");
  const sessionId = requireUuid(input.sessionId, "sessionId");
  const questionId = requireUuid(input.questionId, "questionId");
  const payload = await request(generatedRawResponse(interviewApi.submitInterviewTurnV2Raw({
    sessionId,
    idempotencyKey: requireIdempotencyKey(input.idempotencyKey),
    submitInterviewTurnRequest: {
      workspace_id: workspaceId,
      question_id: questionId,
      user_answer: requireUserAnswer(input.userAnswer),
    },
  }, generatedRequestInit())));
  if (!isRecord(payload)) throw invalidResponse("turn_result");
  exact(payload, ["turn", "follow_up", "next_question", "replayed"], "turn_result");
  const turn = decodeTurn(payload.turn, workspaceId, sessionId, "turn_result.turn");
  const followUp = payload.follow_up === undefined ? undefined : decodeQuestion(payload.follow_up, workspaceId, sessionId, "turn_result.follow_up");
  const nextQuestion = payload.next_question === undefined ? undefined : decodeQuestion(payload.next_question, workspaceId, sessionId, "turn_result.next_question");
  if (
    turn.questionId !== questionId
    || (followUp === undefined) !== (!turn.decision.followUpCreated)
    || (followUp !== undefined && (followUp.id !== turn.decision.followUpQuestionId || followUp.parentQuestionId !== questionId || followUp.status !== "PENDING"))
    || (nextQuestion === undefined) !== (turn.decision.nextQuestionId === undefined)
    || (nextQuestion !== undefined && (nextQuestion.id !== turn.decision.nextQuestionId || nextQuestion.status !== "PENDING"))
  ) throw invalidResponse("turn_result.binding");
  if (followUp !== undefined && !sameNoteItem(followUp.noteItem, turn.score.noteSource) ||
      nextQuestion !== undefined && (turn.score.noteSource === undefined ? nextQuestion.noteItem !== undefined : JSON.stringify(nextQuestion.noteItem?.revision) !== JSON.stringify(turn.score.noteSource.revision))) throw invalidResponse("turn_result.note_source");
  return {
    turn,
    ...(followUp === undefined ? {} : { followUp }),
    ...(nextQuestion === undefined ? {} : { nextQuestion }),
    replayed: booleanValue(payload.replayed, "turn_result.replayed"),
  };
};

export const completeInterview = async (input: CompleteInterviewInput): Promise<CompleteInterviewResult> => {
  const workspaceId = requireUuid(input.workspaceId, "workspaceId");
  const sessionId = requireUuid(input.sessionId, "sessionId");
  if (typeof input.manualEnd !== "boolean") throw invalidRequest("manualEnd");
  const payload = await request(generatedRawResponse(interviewApi.completeInterviewV2Raw({
    sessionId,
    idempotencyKey: requireIdempotencyKey(input.idempotencyKey),
    completeInterviewRequest: { workspace_id: workspaceId, manual_end: input.manualEnd },
  }, generatedRequestInit())));
  if (!isRecord(payload)) throw invalidResponse("complete");
  exact(payload, ["session", "report", "path", "steps", "replayed"], "complete");
  const session = decodeSession(payload.session, "complete.session");
  const report = decodeReport(payload.report, workspaceId, sessionId, "complete.report");
  const path = decodeLearningPath(payload.path, workspaceId, sessionId, "complete.path");
  const steps = decodeLearningPathSteps(payload.steps, workspaceId, path.id, "complete.steps");
  if (session.workspaceId !== workspaceId || session.id !== sessionId || session.status !== "COMPLETED" || path.reportId !== report.id) {
    throw invalidResponse("complete.binding");
  }
  validateCompletionSources(session, report, steps);
  return { session, report, path, steps, replayed: booleanValue(payload.replayed, "complete.replayed") };
};

export const suggestInterviewMemoryCandidate = async (input: SuggestInterviewMemoryCandidateInput): Promise<InterviewMemoryCandidateResult> => {
  const workspaceId = requireUuid(input.workspaceId, "workspaceId");
  const sessionId = requireUuid(input.sessionId, "sessionId");
  const pathId = requireUuid(input.pathId, "pathId");
  const stepId = requireUuid(input.stepId, "stepId");
  const payload = await request(
    generatedRawResponse(interviewApi.suggestInterviewMemoryCandidateRaw({
      sessionId,
      pathId,
      stepId,
      idempotencyKey: requireIdempotencyKey(input.idempotencyKey),
      interviewMemoryCandidateRequest: { workspace_id: workspaceId },
    }, generatedRequestInit())),
    [200, 201],
  );
  if (!isRecord(payload)) throw invalidResponse("memory_candidate");
  exact(payload, ["memory_id", "replayed"], "memory_candidate");
  return {
    memoryId: uuid(payload.memory_id, "memory_candidate.memory_id"),
    replayed: booleanValue(payload.replayed, "memory_candidate.replayed"),
  };
};

export const updateLearningPathStatus = async (input: UpdateLearningPathStatusInput): Promise<LearningPathStatusResult> => {
  const workspaceId = requireUuid(input.workspaceId, "workspaceId");
  const pathId = requireUuid(input.pathId, "pathId");
  const expectedVersion = requireVersion(input.expectedVersion);
  const status = requirePathStatus(input.status);
  const payload = await request(generatedRawResponse(reviewApi.updateLearningPathStatusRaw({
    pathId,
    idempotencyKey: requireIdempotencyKey(input.idempotencyKey),
    updateLearningPathStatusRequest: { workspace_id: workspaceId, expected_version: expectedVersion, status },
  }, generatedRequestInit())));
  if (!isRecord(payload)) throw invalidResponse("path_status");
  exact(payload, ["path", "replayed"], "path_status");
  const path = decodeLearningPath(payload.path, workspaceId, undefined, "path_status.path");
  if (path.id !== pathId || path.status !== status || path.version !== expectedVersion + 1) throw invalidResponse("path_status.binding");
  return { path, replayed: booleanValue(payload.replayed, "path_status.replayed") };
};

export const updateLearningPathStep = async (input: UpdateLearningPathStepInput): Promise<LearningPathStepResult> => {
  const workspaceId = requireUuid(input.workspaceId, "workspaceId");
  const pathId = requireUuid(input.pathId, "pathId");
  const stepId = requireUuid(input.stepId, "stepId");
  const expectedVersion = requireVersion(input.expectedVersion);
  const status = requireStepStatus(input.status);
  const payload = await request(generatedRawResponse(reviewApi.updateLearningPathStepV2Raw({
    pathId,
    stepId,
    idempotencyKey: requireIdempotencyKey(input.idempotencyKey),
    updateLearningPathStepRequest: { workspace_id: workspaceId, expected_version: expectedVersion, status },
  }, generatedRequestInit())));
  if (!isRecord(payload)) throw invalidResponse("path_step");
  exact(payload, ["path", "step", "replayed"], "path_step");
  const path = decodeLearningPath(payload.path, workspaceId, undefined, "path_step.path");
  const step = decodeLearningPathStep(payload.step, workspaceId, pathId, "path_step.step");
  if (path.id !== pathId || path.version !== expectedVersion + 1 || step.id !== stepId || step.status !== status) {
    throw invalidResponse("path_step.binding");
  }
  return { path, step, replayed: booleanValue(payload.replayed, "path_step.replayed") };
};
