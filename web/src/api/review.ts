/** Review 的唯一网络边界：严格隔离 Deck、待复习投影与服务端评分结果。 */

import { authFetch } from "./auth";
import {
  canonicalUuidPattern as uuidPattern,
  hasOnlyKeys,
  isAbortError,
  isRecord,
} from "../shared/codec";

export type ReviewDeckStatus = "ACTIVE" | "PAUSED" | "ARCHIVED";
export type ReviewCardStatus =
  "DRAFT" | "APPROVED" | "INVALIDATED" | "REJECTED";
export type ReviewCardType =
  | "SHORT_ANSWER"
  | "CLOZE"
  | "COMPARISON"
  | "SCENARIO"
  | "CODE_READING"
  | "DESIGN";
export type ReviewSessionType = "REVIEW";
export type ReviewSessionStatus = "ACTIVE" | "COMPLETED" | "CANCELLED";
export type ReviewRating = 1 | 2 | 3 | 4;
export type ReviewJsonValue =
  | string
  | number
  | boolean
  | null
  | ReviewJsonValue[]
  | { [key: string]: ReviewJsonValue };
export type ReviewJsonObject = Record<string, ReviewJsonValue>;

export interface ReviewDeck {
  id: string;
  workspaceId: string;
  name: string;
  scope: ReviewJsonObject;
  status: ReviewDeckStatus;
  dailyLimit: number;
  schedulerVersion: string;
  version: number;
  createdAt: string;
  updatedAt: string;
}

export interface ReviewEvidenceBinding {
  schemaVersion: "review-evidence/v1";
  claimId: string;
  sourceVersionId: string;
  sourceSpanId: string;
  evidenceHash: string;
}

export interface ReviewCard {
  id: string;
  workspaceId: string;
  deckId: string;
  claimId: string;
  question: string;
  answerPoints: string[];
  evidence: ReviewEvidenceBinding[];
  cardType: ReviewCardType;
  difficulty: number;
  status: ReviewCardStatus;
  fingerprint: string;
  modelVersion: string;
  invalidationReason?: string;
  invalidatedAt?: string;
  version: number;
  createdAt: string;
  updatedAt: string;
}

export interface ReviewCardPage {
  workspaceId: string;
  deckId: string;
  items: ReviewCard[];
}

/** 仅供答题前展示的最小卡片投影；故意不包含答案要点或证据正文。 */
export interface ReviewDueCard {
  id: string;
  workspaceId: string;
  deckId: string;
  question: string;
  cardType: ReviewCardType;
  difficulty: number;
  status: "APPROVED";
  version: number;
}

export interface ReviewSchedule {
  cardId: string;
  workspaceId: string;
  dueAt: string;
  intervalDays: number;
  stability: number;
  difficulty: number;
  lastReviewedAt?: string;
  schedulerVersion: string;
  paused: boolean;
  version: number;
}

export interface ReviewDueItem {
  card: ReviewDueCard;
  schedule: ReviewSchedule;
  questionRef: string;
}

export interface ReviewDuePage {
  workspaceId: string;
  items: ReviewDueItem[];
}

export interface ReviewSession {
  id: string;
  workspaceId: string;
  deckId: string;
  sessionType: ReviewSessionType;
  status: ReviewSessionStatus;
  config: ReviewJsonObject;
  startedAt: string;
  endedAt?: string;
}

export interface ReviewScoreDimension {
  value: number;
  rationale: string;
}

export interface ReviewScoreEvidence {
  schemaVersion: "review-evidence/v1";
  claimId: string;
  sourceVersionId: string;
  sourceSpanId: string;
  evidenceHash: string;
  sourceVersionHref: string;
  sourceSpanHref: string;
}

export interface ReviewScore {
  schemaVersion: "review-score/v1";
  correctness: ReviewScoreDimension;
  coverage: ReviewScoreDimension;
  boundaries: ReviewScoreDimension;
  clarity: ReviewScoreDimension;
  confidence: ReviewScoreDimension;
  errors: string[];
  omissions: string[];
  evidence: ReviewScoreEvidence[];
}

export interface ReviewAnswer {
  id: string;
  workspaceId: string;
  sessionId: string;
  cardId: string;
  questionRef: string;
  userAnswer: string;
  rating: ReviewRating;
  scorerVersion: string;
  score: ReviewScore;
  feedback: ReviewJsonObject;
  createdAt: string;
}

export interface ReviewAnswerResult {
  answer: ReviewAnswer;
  schedule: ReviewSchedule;
  replayed: boolean;
}

export type ReviewLearningPathStatus = "ACTIVE" | "PAUSED" | "COMPLETED";
export type ReviewLearningPathStepStatus =
  "PENDING" | "IN_PROGRESS" | "COMPLETED" | "SKIPPED";
export type ReviewLearningPathStepTargetStatus =
  | "IN_PROGRESS"
  | "COMPLETED"
  | "SKIPPED";

/** Review 评分缺口生成的路径；来源与 gap 仅由服务端根据持久 Answer 推导。 */
export interface ReviewLearningPath {
  id: string;
  workspaceId: string;
  originType: "REVIEW";
  reviewAnswerId: string;
  artifact: {
    kind: "LEARNING_PATH";
    artifactId: string;
    revisionId: string;
    artifactVersion: number;
  };
  sourcePolicyVersion: string;
  status: ReviewLearningPathStatus;
  version: number;
  createdAt: string;
  updatedAt: string;
}

export interface ReviewLearningPathStep {
  id: string;
  workspaceId: string;
  pathId: string;
  stepNo: number;
  claimId: string;
  topicId?: string;
  sourceVersionId: string;
  sourceSpanId: string;
  evidenceHash: string;
  title: string;
  rationale: string;
  status: ReviewLearningPathStepStatus;
  version: number;
  createdAt: string;
  updatedAt: string;
}

export interface ReviewLearningPathResult {
  path: ReviewLearningPath;
  steps: ReviewLearningPathStep[];
  replayed: boolean;
}

export interface ReviewLearningPathStatusResult {
  path: ReviewLearningPath;
  replayed: boolean;
}

export interface ReviewLearningPathStepResult {
  path: ReviewLearningPath;
  step: ReviewLearningPathStep;
  replayed: boolean;
}

export interface CreateReviewDeckInput {
  workspaceId: string;
  name: string;
  scope: ReviewJsonObject;
  dailyLimit: number;
  idempotencyKey: string;
}

export interface CreateReviewCardInput {
  workspaceId: string;
  deckId: string;
  claimId: string;
  question: string;
  answerPoints: string[];
  evidence: ReviewEvidenceBinding[];
  cardType: ReviewCardType;
  difficulty: number;
  modelVersion: string;
  idempotencyKey: string;
}

export interface EditReviewCardInput extends CreateReviewCardInput {
  cardId: string;
  expectedVersion: number;
}

export interface ReviewCardDecisionInput {
  workspaceId: string;
  cardId: string;
  expectedVersion: number;
  reason: string;
  idempotencyKey: string;
}

export interface ReviewDeckScheduleInput {
  workspaceId: string;
  deckId: string;
  expectedVersion: number;
  idempotencyKey: string;
}

export interface StartReviewSessionInput {
  workspaceId: string;
  deckId: string;
  config: ReviewJsonObject;
  idempotencyKey: string;
}

export interface CompleteReviewSessionInput {
  workspaceId: string;
  sessionId: string;
  cancelled: boolean;
  idempotencyKey: string;
}

export interface SubmitReviewAnswerInput {
  workspaceId: string;
  sessionId: string;
  cardId: string;
  questionRef: string;
  userAnswer: string;
  rating: ReviewRating;
  idempotencyKey: string;
}

export interface CreateReviewLearningPathInput {
  workspaceId: string;
  answerId: string;
  idempotencyKey: string;
}

export interface UpdateReviewLearningPathStatusInput {
  workspaceId: string;
  answerId: string;
  expectedVersion: number;
  status: ReviewLearningPathStatus;
  idempotencyKey: string;
}

export interface UpdateReviewLearningPathStepInput {
  workspaceId: string;
  answerId: string;
  stepId: string;
  expectedVersion: number;
  status: ReviewLearningPathStepTargetStatus;
  idempotencyKey: string;
}

export class ReviewApiError extends Error {
  readonly code:
    "INVALID_REQUEST" | "INVALID_RESPONSE" | "HTTP_ERROR" | "NETWORK_ERROR";
  readonly errorCode: string;
  readonly retryable: boolean;
  readonly status: number | null;

  constructor(
    code: ReviewApiError["code"],
    errorCode: string,
    message: string,
    retryable: boolean,
    status: number | null = null,
    options?: ErrorOptions,
  ) {
    super(message, options);
    this.name = "ReviewApiError";
    this.code = code;
    this.errorCode = errorCode;
    this.retryable = retryable;
    this.status = status;
  }
}

const hashPattern = /^[0-9a-f]{64}$/;
const timestampPattern =
  /^\d{4}-(?:0[1-9]|1[0-2])-(?:0[1-9]|[12]\d|3[01])T(?:[01]\d|2[0-3]):[0-5]\d:[0-5]\d(?:\.\d{1,9})?(?:Z|[+-](?:[01]\d|2[0-3]):[0-5]\d)$/;
const deckStatuses: readonly ReviewDeckStatus[] = [
  "ACTIVE",
  "PAUSED",
  "ARCHIVED",
];
const cardStatuses: readonly ReviewCardStatus[] = [
  "DRAFT",
  "APPROVED",
  "INVALIDATED",
  "REJECTED",
];
const cardTypes: readonly ReviewCardType[] = [
  "SHORT_ANSWER",
  "CLOZE",
  "COMPARISON",
  "SCENARIO",
  "CODE_READING",
  "DESIGN",
];
const evidenceInputFields = [
  "schemaVersion",
  "claimId",
  "sourceVersionId",
  "sourceSpanId",
  "evidenceHash",
] as const;
const sessionTypes: readonly ReviewSessionType[] = ["REVIEW"];
const sessionStatuses: readonly ReviewSessionStatus[] = [
  "ACTIVE",
  "COMPLETED",
  "CANCELLED",
];
const learningPathOrigins: readonly ReviewLearningPath["originType"][] = [
  "REVIEW",
];
const learningPathStatuses: readonly ReviewLearningPathStatus[] = [
  "ACTIVE",
  "PAUSED",
  "COMPLETED",
];
const learningPathStepStatuses: readonly ReviewLearningPathStepStatus[] = [
  "PENDING",
  "IN_PROGRESS",
  "COMPLETED",
  "SKIPPED",
];
const learningPathStepTargetStatuses: readonly ReviewLearningPathStepTargetStatus[] = [
  "IN_PROGRESS",
  "COMPLETED",
  "SKIPPED",
];

const invalidResponse = (
  field: string,
  status: number | null = null,
): ReviewApiError =>
  new ReviewApiError(
    "INVALID_RESPONSE",
    "INVALID_RESPONSE",
    `Review 响应字段无效：${field}`,
    false,
    status,
  );
const invalidRequest = (field: string): ReviewApiError =>
  new ReviewApiError(
    "INVALID_REQUEST",
    "INVALID_REQUEST",
    `Review 请求字段无效：${field}`,
    false,
  );
const exact = (
  value: Record<string, unknown>,
  keys: readonly string[],
  field: string,
): void => {
  if (!hasOnlyKeys(value, keys))
    throw invalidResponse(field);
};
const stringValue = (
  value: unknown,
  field: string,
  allowEmpty = false,
): string => {
  if (typeof value !== "string" || (!allowEmpty && value.trim() === ""))
    throw invalidResponse(field);
  return value;
};
const uuid = (value: unknown, field: string): string => {
  const result = stringValue(value, field);
  if (!uuidPattern.test(result)) throw invalidResponse(field);
  return result;
};
const hash = (value: unknown, field: string): string => {
  const result = stringValue(value, field);
  if (!hashPattern.test(result)) throw invalidResponse(field);
  return result;
};
const integer = (value: unknown, field: string, minimum = 0): number => {
  if (
    typeof value !== "number" ||
    !Number.isSafeInteger(value) ||
    value < minimum
  )
    throw invalidResponse(field);
  return value;
};
const finite = (
  value: unknown,
  field: string,
  minimum?: number,
  maximum?: number,
): number => {
  if (
    typeof value !== "number" ||
    !Number.isFinite(value) ||
    (minimum !== undefined && value < minimum) ||
    (maximum !== undefined && value > maximum)
  )
    throw invalidResponse(field);
  return value;
};
const bool = (value: unknown, field: string): boolean => {
  if (typeof value !== "boolean") throw invalidResponse(field);
  return value;
};
const enumValue = <T extends string>(
  value: unknown,
  allowed: readonly T[],
  field: string,
): T => {
  if (typeof value !== "string" || !allowed.includes(value as T))
    throw invalidResponse(field);
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
  if (!timestampPattern.test(result) || !Number.isFinite(Date.parse(result)))
    throw invalidResponse(field);
  const match = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})/.exec(result);
  if (match === null) throw invalidResponse(field);
  const [, rawYear, rawMonth, rawDay, rawHour, rawMinute, rawSecond] = match;
  const year = Number(rawYear);
  const month = Number(rawMonth);
  const day = Number(rawDay);
  if (
    month < 1 ||
    month > 12 ||
    day < 1 ||
    day > daysInMonth(year, month) ||
    Number(rawHour) > 23 ||
    Number(rawMinute) > 59 ||
    Number(rawSecond) > 59
  )
    throw invalidResponse(field);
  return result;
};
const optionalTimestamp = (
  value: unknown,
  field: string,
): string | undefined =>
  value === undefined ? undefined : timestamp(value, field);
const bytes = (value: string): number => new TextEncoder().encode(value).length;
const requireUuid = (value: unknown, field: string): string => {
  if (typeof value !== "string" || !uuidPattern.test(value))
    throw invalidRequest(field);
  return value;
};
const requireVersion = (value: number, field = "expectedVersion"): number => {
  if (!Number.isSafeInteger(value) || value < 1) throw invalidRequest(field);
  return value;
};
const requireKey = (value: string): string => {
  if (
    typeof value !== "string" ||
    value.trim() === "" ||
    value !== value.trim() ||
    bytes(value) > 128 ||
    /[\u0000-\u001f\u007f]/.test(value)
  )
    throw invalidRequest("idempotencyKey");
  return value;
};
const requireText = (
  value: unknown,
  field: string,
  maximumBytes: number,
  allowEmpty = false,
): string => {
  if (
    typeof value !== "string" ||
    (!allowEmpty && (value === "" || value !== value.trim())) ||
    bytes(value) > maximumBytes ||
    value.includes("\0")
  )
    throw invalidRequest(field);
  return value;
};

const decodeJsonValue = (
  value: unknown,
  field: string,
  depth = 0,
): ReviewJsonValue => {
  if (
    depth > 32 ||
    value === null ||
    typeof value === "string" ||
    typeof value === "boolean"
  )
    return value as ReviewJsonValue;
  if (typeof value === "number") {
    if (!Number.isFinite(value)) throw invalidResponse(field);
    return value;
  }
  if (Array.isArray(value)) {
    if (value.length > 256) throw invalidResponse(field);
    return value.map((item, index) =>
      decodeJsonValue(item, `${field}[${String(index)}]`, depth + 1),
    );
  }
  if (!isRecord(value) || Object.keys(value).length > 256)
    throw invalidResponse(field);
  return Object.fromEntries(
    Object.entries(value).map(([key, item]) => {
      if (key === "" || key.length > 512) throw invalidResponse(field);
      return [key, decodeJsonValue(item, `${field}.${key}`, depth + 1)];
    }),
  );
};
const decodeJsonObject = (value: unknown, field: string): ReviewJsonObject => {
  const result = decodeJsonValue(value, field);
  if (!isRecord(result)) throw invalidResponse(field);
  return result;
};
const requireJsonObject = (
  value: ReviewJsonObject,
  field: string,
): ReviewJsonObject => {
  try {
    const decoded = decodeJsonObject(value, field);
    if (bytes(JSON.stringify(decoded)) > 32 * 1024) throw invalidRequest(field);
    return decoded;
  } catch (error: unknown) {
    if (error instanceof ReviewApiError && error.code === "INVALID_REQUEST")
      throw error;
    throw invalidRequest(field);
  }
};

class StrictJsonParser {
  private index = 0;
  private depth = 0;

  constructor(private readonly source: string) {}

  parse(): unknown {
    this.skipWhitespace();
    const value = this.parseValue();
    this.skipWhitespace();
    if (this.index !== this.source.length)
      throw new SyntaxError("unexpected trailing JSON data");
    return value;
  }

  private parseValue(): unknown {
    const current = this.source[this.index];
    if (current === "{") return this.parseObject();
    if (current === "[") return this.parseArray();
    if (current === '"') return this.parseString();
    if (this.source.startsWith("true", this.index)) {
      this.index += 4;
      return true;
    }
    if (this.source.startsWith("false", this.index)) {
      this.index += 5;
      return false;
    }
    if (this.source.startsWith("null", this.index)) {
      this.index += 4;
      return null;
    }
    const match = /^-?(?:0|[1-9]\d*)(?:\.\d+)?(?:[eE][+-]?\d+)?/.exec(
      this.source.slice(this.index),
    );
    if (match === null) throw new SyntaxError("invalid JSON value");
    this.index += match[0].length;
    const value = Number(match[0]);
    if (!Number.isFinite(value))
      throw new SyntaxError("non-finite JSON number");
    return value;
  }

  private parseObject(): Record<string, unknown> {
    this.enterContainer();
    this.index += 1;
    this.skipWhitespace();
    const entries: [string, unknown][] = [];
    const keys = new Set<string>();
    if (this.source[this.index] === "}") {
      this.index += 1;
      this.leaveContainer();
      return {};
    }
    for (;;) {
      if (this.source[this.index] !== '"')
        throw new SyntaxError("invalid JSON object key");
      const key = this.parseString();
      if (keys.has(key)) throw new SyntaxError(`duplicate JSON key: ${key}`);
      keys.add(key);
      this.skipWhitespace();
      if (this.source[this.index] !== ":")
        throw new SyntaxError("missing JSON object colon");
      this.index += 1;
      this.skipWhitespace();
      entries.push([key, this.parseValue()]);
      this.skipWhitespace();
      const separator = this.source[this.index];
      if (separator === "}") {
        this.index += 1;
        this.leaveContainer();
        return Object.fromEntries(entries);
      }
      if (separator !== ",")
        throw new SyntaxError("invalid JSON object separator");
      this.index += 1;
      this.skipWhitespace();
    }
  }

  private parseArray(): unknown[] {
    this.enterContainer();
    this.index += 1;
    this.skipWhitespace();
    const values: unknown[] = [];
    if (this.source[this.index] === "]") {
      this.index += 1;
      this.leaveContainer();
      return values;
    }
    for (;;) {
      values.push(this.parseValue());
      this.skipWhitespace();
      const separator = this.source[this.index];
      if (separator === "]") {
        this.index += 1;
        this.leaveContainer();
        return values;
      }
      if (separator !== ",")
        throw new SyntaxError("invalid JSON array separator");
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
        const parsed: unknown = JSON.parse(
          this.source.slice(start, this.index),
        );
        if (typeof parsed !== "string")
          throw new SyntaxError("invalid JSON string");
        return parsed;
      }
      if (current === "\\") this.index += 1;
      this.index += 1;
    }
    throw new SyntaxError("unterminated JSON string");
  }

  private skipWhitespace(): void {
    while (this.index < this.source.length) {
      const current = this.source[this.index];
      if (
        current !== " " &&
        current !== "\n" &&
        current !== "\r" &&
        current !== "\t"
      )
        return;
      this.index += 1;
    }
  }

  private enterContainer(): void {
    this.depth += 1;
    if (this.depth > 256) throw new SyntaxError("JSON nesting is too deep");
  }

  private leaveContainer(): void {
    this.depth -= 1;
  }
}

const parseStrictJson = (source: string): unknown =>
  new StrictJsonParser(source).parse();

export const decodeReviewDeck = (value: unknown): ReviewDeck => {
  if (!isRecord(value)) throw invalidResponse("deck");
  exact(
    value,
    [
      "id",
      "workspace_id",
      "name",
      "scope",
      "status",
      "daily_limit",
      "scheduler_version",
      "version",
      "created_at",
      "updated_at",
    ],
    "deck",
  );
  return {
    id: uuid(value.id, "deck.id"),
    workspaceId: uuid(value.workspace_id, "deck.workspace_id"),
    name: stringValue(value.name, "deck.name"),
    scope: decodeJsonObject(value.scope, "deck.scope"),
    status: enumValue(value.status, deckStatuses, "deck.status"),
    dailyLimit: integer(value.daily_limit, "deck.daily_limit", 1),
    schedulerVersion: stringValue(
      value.scheduler_version,
      "deck.scheduler_version",
    ),
    version: integer(value.version, "deck.version", 1),
    createdAt: timestamp(value.created_at, "deck.created_at"),
    updatedAt: timestamp(value.updated_at, "deck.updated_at"),
  };
};

export const decodeReviewDeckPage = (
  value: unknown,
): { workspaceId: string; items: ReviewDeck[] } => {
  if (!isRecord(value)) throw invalidResponse("deck_page");
  exact(value, ["workspace_id", "items"], "deck_page");
  if (!Array.isArray(value.items) || value.items.length > 200)
    throw invalidResponse("deck_page.items");
  const workspaceId = uuid(value.workspace_id, "deck_page.workspace_id");
  const items = value.items.map((item) => decodeReviewDeck(item));
  if (
    items.some((item) => item.workspaceId !== workspaceId) ||
    new Set(items.map((item) => item.id)).size !== items.length
  )
    throw invalidResponse("deck_page.items");
  return { workspaceId, items };
};

const decodeReviewEvidenceBinding = (
  value: unknown,
  field: string,
  claimId?: string,
): ReviewEvidenceBinding => {
  if (!isRecord(value)) throw invalidResponse(field);
  exact(
    value,
    [
      "schema_version",
      "claim_id",
      "source_version_id",
      "source_span_id",
      "evidence_hash",
    ],
    field,
  );
  if (value.schema_version !== "review-evidence/v1")
    throw invalidResponse(`${field}.schema_version`);
  const bindingClaimId = uuid(value.claim_id, `${field}.claim_id`);
  if (claimId !== undefined && bindingClaimId !== claimId)
    throw invalidResponse(`${field}.claim_id`);
  return {
    schemaVersion: "review-evidence/v1",
    claimId: bindingClaimId,
    sourceVersionId: uuid(
      value.source_version_id,
      `${field}.source_version_id`,
    ),
    sourceSpanId: uuid(value.source_span_id, `${field}.source_span_id`),
    evidenceHash: hash(value.evidence_hash, `${field}.evidence_hash`),
  };
};

const decodeAnswerPoints = (value: unknown, field: string): string[] => {
  if (!Array.isArray(value) || value.length === 0 || value.length > 128)
    throw invalidResponse(field);
  const points = value.map((item, index) => {
    const point = stringValue(item, `${field}[${String(index)}]`);
    if (point !== point.trim() || bytes(point) > 4 * 1024)
      throw invalidResponse(`${field}[${String(index)}]`);
    return point;
  });
  if (new Set(points).size !== points.length) throw invalidResponse(field);
  return points;
};

export const decodeReviewCard = (value: unknown): ReviewCard => {
  if (!isRecord(value)) throw invalidResponse("card");
  exact(
    value,
    [
      "id",
      "workspace_id",
      "deck_id",
      "claim_id",
      "question",
      "answer_points",
      "evidence",
      "card_type",
      "difficulty",
      "status",
      "fingerprint",
      "model_version",
      "invalidation_reason",
      "invalidated_at",
      "version",
      "created_at",
      "updated_at",
    ],
    "card",
  );
  const claimId = uuid(value.claim_id, "card.claim_id");
  if (
    !Array.isArray(value.evidence) ||
    value.evidence.length === 0 ||
    value.evidence.length > 128
  )
    throw invalidResponse("card.evidence");
  const evidence = value.evidence.map((item, index) =>
    decodeReviewEvidenceBinding(
      item,
      `card.evidence[${String(index)}]`,
      claimId,
    ),
  );
  if (
    new Set(evidence.map((item) => item.evidenceHash)).size !== evidence.length
  )
    throw invalidResponse("card.evidence");
  const status = enumValue(value.status, cardStatuses, "card.status");
  const invalidationReason =
    value.invalidation_reason === undefined
      ? undefined
      : stringValue(value.invalidation_reason, "card.invalidation_reason");
  const invalidatedAt = optionalTimestamp(
    value.invalidated_at,
    "card.invalidated_at",
  );
  if (
    (status === "INVALIDATED") !==
    (invalidationReason !== undefined && invalidatedAt !== undefined)
  )
    throw invalidResponse("card.invalidation");
  return {
    id: uuid(value.id, "card.id"),
    workspaceId: uuid(value.workspace_id, "card.workspace_id"),
    deckId: uuid(value.deck_id, "card.deck_id"),
    claimId,
    question: stringValue(value.question, "card.question"),
    answerPoints: decodeAnswerPoints(value.answer_points, "card.answer_points"),
    evidence,
    cardType: enumValue(value.card_type, cardTypes, "card.card_type"),
    difficulty: finite(value.difficulty, "card.difficulty", 0, 1),
    status,
    fingerprint: hash(value.fingerprint, "card.fingerprint"),
    modelVersion: stringValue(value.model_version, "card.model_version"),
    ...(invalidationReason === undefined ? {} : { invalidationReason }),
    ...(invalidatedAt === undefined ? {} : { invalidatedAt }),
    version: integer(value.version, "card.version", 1),
    createdAt: timestamp(value.created_at, "card.created_at"),
    updatedAt: timestamp(value.updated_at, "card.updated_at"),
  };
};

export const decodeReviewCardPage = (value: unknown): ReviewCardPage => {
  if (!isRecord(value)) throw invalidResponse("card_page");
  exact(value, ["workspace_id", "deck_id", "items"], "card_page");
  if (!Array.isArray(value.items) || value.items.length > 200)
    throw invalidResponse("card_page.items");
  const workspaceId = uuid(value.workspace_id, "card_page.workspace_id");
  const deckId = uuid(value.deck_id, "card_page.deck_id");
  const items = value.items.map((item) => decodeReviewCard(item));
  if (
    items.some(
      (item) => item.workspaceId !== workspaceId || item.deckId !== deckId,
    ) ||
    new Set(items.map((item) => item.id)).size !== items.length
  )
    throw invalidResponse("card_page.items");
  return { workspaceId, deckId, items };
};

const decodeReviewSchedule = (
  value: unknown,
  field: string,
): ReviewSchedule => {
  if (!isRecord(value)) throw invalidResponse(field);
  exact(
    value,
    [
      "card_id",
      "workspace_id",
      "due_at",
      "interval_days",
      "stability",
      "difficulty",
      "last_reviewed_at",
      "scheduler_version",
      "paused",
      "version",
    ],
    field,
  );
  const lastReviewedAt = optionalTimestamp(
    value.last_reviewed_at,
    `${field}.last_reviewed_at`,
  );
  return {
    cardId: uuid(value.card_id, `${field}.card_id`),
    workspaceId: uuid(value.workspace_id, `${field}.workspace_id`),
    dueAt: timestamp(value.due_at, `${field}.due_at`),
    intervalDays: finite(value.interval_days, `${field}.interval_days`, 0),
    stability: finite(value.stability, `${field}.stability`, 0),
    difficulty: finite(value.difficulty, `${field}.difficulty`, 0, 1),
    ...(lastReviewedAt === undefined ? {} : { lastReviewedAt }),
    schedulerVersion: stringValue(
      value.scheduler_version,
      `${field}.scheduler_version`,
    ),
    paused: bool(value.paused, `${field}.paused`),
    version: integer(value.version, `${field}.version`, 1),
  };
};

const decodeReviewDueCard = (value: unknown, field: string): ReviewDueCard => {
  if (!isRecord(value)) throw invalidResponse(field);
  // `answer_points`、`evidence` 或其它完整 Card 字段在此投影中都是协议违规，避免答题前泄露。
  exact(
    value,
    [
      "id",
      "workspace_id",
      "deck_id",
      "question",
      "card_type",
      "difficulty",
      "status",
      "version",
    ],
    field,
  );
  const status = enumValue(value.status, cardStatuses, `${field}.status`);
  if (status !== "APPROVED") throw invalidResponse(`${field}.status`);
  return {
    id: uuid(value.id, `${field}.id`),
    workspaceId: uuid(value.workspace_id, `${field}.workspace_id`),
    deckId: uuid(value.deck_id, `${field}.deck_id`),
    question: stringValue(value.question, `${field}.question`),
    cardType: enumValue(value.card_type, cardTypes, `${field}.card_type`),
    difficulty: finite(value.difficulty, `${field}.difficulty`, 0, 1),
    status: "APPROVED",
    version: integer(value.version, `${field}.version`, 1),
  };
};

export const decodeReviewDuePage = (value: unknown): ReviewDuePage => {
  if (!isRecord(value)) throw invalidResponse("due_page");
  exact(value, ["workspace_id", "items"], "due_page");
  if (!Array.isArray(value.items) || value.items.length > 200)
    throw invalidResponse("due_page.items");
  const workspaceId = uuid(value.workspace_id, "due_page.workspace_id");
  const seen = new Set<string>();
  const items = value.items.map((item, index) => {
    const field = `due_page.items[${String(index)}]`;
    if (!isRecord(item)) throw invalidResponse(field);
    exact(item, ["card", "schedule", "question_ref"], field);
    const card = decodeReviewDueCard(item.card, `${field}.card`);
    const schedule = decodeReviewSchedule(item.schedule, `${field}.schedule`);
    const questionRef = stringValue(item.question_ref, `${field}.question_ref`);
    if (bytes(questionRef) > 512)
      throw invalidResponse(`${field}.question_ref`);
    if (
      card.workspaceId !== workspaceId ||
      schedule.workspaceId !== workspaceId ||
      card.id !== schedule.cardId ||
      schedule.paused ||
      seen.has(card.id)
    )
      throw invalidResponse(field);
    seen.add(card.id);
    return { card, schedule, questionRef };
  });
  return { workspaceId, items };
};

export const decodeReviewSession = (value: unknown): ReviewSession => {
  if (!isRecord(value)) throw invalidResponse("session");
  exact(
    value,
    [
      "id",
      "workspace_id",
      "deck_id",
      "session_type",
      "status",
      "config",
      "started_at",
      "ended_at",
    ],
    "session",
  );
  const deckId = uuid(value.deck_id, "session.deck_id");
  const sessionType = enumValue(
    value.session_type,
    sessionTypes,
    "session.session_type",
  );
  const endedAt = optionalTimestamp(value.ended_at, "session.ended_at");
  return {
    id: uuid(value.id, "session.id"),
    workspaceId: uuid(value.workspace_id, "session.workspace_id"),
    deckId,
    sessionType,
    status: enumValue(value.status, sessionStatuses, "session.status"),
    config: decodeJsonObject(value.config, "session.config"),
    startedAt: timestamp(value.started_at, "session.started_at"),
    ...(endedAt === undefined ? {} : { endedAt }),
  };
};

const decodeScoreDimension = (
  value: unknown,
  field: string,
): ReviewScoreDimension => {
  if (!isRecord(value)) throw invalidResponse(field);
  exact(value, ["value", "rationale"], field);
  return {
    value: finite(value.value, `${field}.value`, 0, 1),
    rationale: stringValue(value.rationale, `${field}.rationale`),
  };
};
const decodeScoreStrings = (value: unknown, field: string): string[] => {
  if (value === undefined) return [];
  if (!Array.isArray(value) || value.length > 128) throw invalidResponse(field);
  return value.map((item, index) => {
    const result = stringValue(item, `${field}[${String(index)}]`);
    if (bytes(result) > 4 * 1024)
      throw invalidResponse(`${field}[${String(index)}]`);
    return result;
  });
};
const decodeScoreEvidence = (
  value: unknown,
  field: string,
  workspaceId: string,
): ReviewScoreEvidence => {
  if (!isRecord(value)) throw invalidResponse(field);
  exact(
    value,
    [
      "schema_version",
      "claim_id",
      "source_version_id",
      "source_span_id",
      "evidence_hash",
      "source_version_href",
      "source_span_href",
    ],
    field,
  );
  if (value.schema_version !== "review-evidence/v1")
    throw invalidResponse(`${field}.schema_version`);
  const sourceVersionId = uuid(
    value.source_version_id,
    `${field}.source_version_id`,
  );
  const sourceSpanId = uuid(value.source_span_id, `${field}.source_span_id`);
  const sourceVersionHref = stringValue(
    value.source_version_href,
    `${field}.source_version_href`,
  );
  const sourceSpanHref = stringValue(
    value.source_span_href,
    `${field}.source_span_href`,
  );
  if (
    sourceVersionHref !==
      `/api/v1/workspaces/${workspaceId}/source-versions/${sourceVersionId}` ||
    sourceSpanHref !== `${sourceVersionHref}/spans/${sourceSpanId}`
  )
    throw invalidResponse(`${field}.href`);
  return {
    schemaVersion: "review-evidence/v1",
    claimId: uuid(value.claim_id, `${field}.claim_id`),
    sourceVersionId,
    sourceSpanId,
    evidenceHash: hash(value.evidence_hash, `${field}.evidence_hash`),
    sourceVersionHref,
    sourceSpanHref,
  };
};
const decodeScore = (value: unknown, workspaceId: string): ReviewScore => {
  if (!isRecord(value)) throw invalidResponse("answer.score");
  exact(
    value,
    [
      "schema_version",
      "correctness",
      "coverage",
      "boundaries",
      "clarity",
      "confidence",
      "errors",
      "omissions",
      "evidence",
    ],
    "answer.score",
  );
  if (value.schema_version !== "review-score/v1")
    throw invalidResponse("answer.score.schema_version");
  if (
    !Array.isArray(value.evidence) ||
    value.evidence.length === 0 ||
    value.evidence.length > 128
  )
    throw invalidResponse("answer.score.evidence");
  const evidence = value.evidence.map((item, index) =>
    decodeScoreEvidence(
      item,
      `answer.score.evidence[${String(index)}]`,
      workspaceId,
    ),
  );
  if (
    new Set(evidence.map((item) => `${item.sourceSpanId}:${item.evidenceHash}`))
      .size !== evidence.length
  )
    throw invalidResponse("answer.score.evidence");
  return {
    schemaVersion: "review-score/v1",
    correctness: decodeScoreDimension(
      value.correctness,
      "answer.score.correctness",
    ),
    coverage: decodeScoreDimension(value.coverage, "answer.score.coverage"),
    boundaries: decodeScoreDimension(
      value.boundaries,
      "answer.score.boundaries",
    ),
    clarity: decodeScoreDimension(value.clarity, "answer.score.clarity"),
    confidence: decodeScoreDimension(
      value.confidence,
      "answer.score.confidence",
    ),
    errors: decodeScoreStrings(value.errors, "answer.score.errors"),
    omissions: decodeScoreStrings(value.omissions, "answer.score.omissions"),
    evidence,
  };
};
const decodeReviewAnswer = (value: unknown): ReviewAnswer => {
  if (!isRecord(value)) throw invalidResponse("answer");
  exact(
    value,
    [
      "id",
      "workspace_id",
      "session_id",
      "card_id",
      "question_ref",
      "user_answer",
      "rating",
      "scorer_version",
      "score",
      "feedback",
      "created_at",
    ],
    "answer",
  );
  const workspaceId = uuid(value.workspace_id, "answer.workspace_id");
  const rating = integer(value.rating, "answer.rating", 1);
  if (rating < 1 || rating > 4) throw invalidResponse("answer.rating");
  const questionRef = stringValue(value.question_ref, "answer.question_ref");
  if (bytes(questionRef) > 512) throw invalidResponse("answer.question_ref");
  const scorerVersion = stringValue(
    value.scorer_version,
    "answer.scorer_version",
  );
  if (bytes(scorerVersion) > 128)
    throw invalidResponse("answer.scorer_version");
  const userAnswer = stringValue(value.user_answer, "answer.user_answer", true);
  if (bytes(userAnswer) > 64 * 1024)
    throw invalidResponse("answer.user_answer");
  const feedback = decodeJsonObject(value.feedback, "answer.feedback");
  if (bytes(JSON.stringify(feedback)) > 32 * 1024)
    throw invalidResponse("answer.feedback");
  return {
    id: uuid(value.id, "answer.id"),
    workspaceId,
    sessionId: uuid(value.session_id, "answer.session_id"),
    cardId: uuid(value.card_id, "answer.card_id"),
    questionRef,
    userAnswer,
    rating: rating as ReviewRating,
    scorerVersion,
    score: decodeScore(value.score, workspaceId),
    feedback,
    createdAt: timestamp(value.created_at, "answer.created_at"),
  };
};

export const decodeReviewAnswerResult = (
  value: unknown,
): ReviewAnswerResult => {
  if (!isRecord(value)) throw invalidResponse("answer_result");
  exact(value, ["answer", "schedule", "replayed"], "answer_result");
  const answer = decodeReviewAnswer(value.answer);
  const schedule = decodeReviewSchedule(
    value.schedule,
    "answer_result.schedule",
  );
  if (
    answer.workspaceId !== schedule.workspaceId ||
    answer.cardId !== schedule.cardId
  )
    throw invalidResponse("answer_result.binding");
  return {
    answer,
    schedule,
    replayed: bool(value.replayed, "answer_result.replayed"),
  };
};

const decodeReviewLearningPath = (
  value: unknown,
  workspaceId: string,
  answerId?: string,
  field = "learning_path",
): ReviewLearningPath => {
  if (!isRecord(value)) throw invalidResponse(field);
  exact(
    value,
    [
      "id",
      "workspace_id",
      "origin_type",
      "review_answer_id",
      "artifact",
      "source_policy_version",
      "status",
      "version",
      "created_at",
      "updated_at",
    ],
    field,
  );
  if (!isRecord(value.artifact)) throw invalidResponse(`${field}.artifact`);
  exact(
    value.artifact,
    ["kind", "artifact_id", "revision_id", "artifact_version"],
    `${field}.artifact`,
  );
  if (value.artifact.kind !== "LEARNING_PATH")
    throw invalidResponse(`${field}.artifact.kind`);
  const createdAt = timestamp(value.created_at, `${field}.created_at`);
  const updatedAt = timestamp(value.updated_at, `${field}.updated_at`);
  const result: ReviewLearningPath = {
    id: uuid(value.id, `${field}.id`),
    workspaceId: uuid(value.workspace_id, `${field}.workspace_id`),
    originType: enumValue(
      value.origin_type,
      learningPathOrigins,
      `${field}.origin_type`,
    ),
    reviewAnswerId: uuid(value.review_answer_id, `${field}.review_answer_id`),
    artifact: {
      kind: "LEARNING_PATH",
      artifactId: uuid(
        value.artifact.artifact_id,
        `${field}.artifact.artifact_id`,
      ),
      revisionId: uuid(
        value.artifact.revision_id,
        `${field}.artifact.revision_id`,
      ),
      artifactVersion: integer(
        value.artifact.artifact_version,
        `${field}.artifact.artifact_version`,
        1,
      ),
    },
    sourcePolicyVersion: stringValue(
      value.source_policy_version,
      `${field}.source_policy_version`,
    ),
    status: enumValue(value.status, learningPathStatuses, `${field}.status`),
    version: integer(value.version, `${field}.version`, 1),
    createdAt,
    updatedAt,
  };
  if (
    result.workspaceId !== workspaceId ||
    (answerId !== undefined && result.reviewAnswerId !== answerId) ||
    Date.parse(updatedAt) < Date.parse(createdAt)
  ) {
    throw invalidResponse(`${field}.binding`);
  }
  return result;
};

const decodeReviewLearningPathStep = (
  value: unknown,
  workspaceId: string,
  pathId: string,
  field: string,
): ReviewLearningPathStep => {
  if (!isRecord(value)) throw invalidResponse(field);
  exact(
    value,
    [
      "id",
      "workspace_id",
      "path_id",
      "step_no",
      "claim_id",
      "topic_id",
      "source_version_id",
      "source_span_id",
      "evidence_hash",
      "title",
      "rationale",
      "status",
      "version",
      "created_at",
      "updated_at",
    ],
    field,
  );
  const createdAt = timestamp(value.created_at, `${field}.created_at`);
  const updatedAt = timestamp(value.updated_at, `${field}.updated_at`);
  const result: ReviewLearningPathStep = {
    id: uuid(value.id, `${field}.id`),
    workspaceId: uuid(value.workspace_id, `${field}.workspace_id`),
    pathId: uuid(value.path_id, `${field}.path_id`),
    stepNo: integer(value.step_no, `${field}.step_no`, 1),
    claimId: uuid(value.claim_id, `${field}.claim_id`),
    ...(value.topic_id === undefined
      ? {}
      : { topicId: uuid(value.topic_id, `${field}.topic_id`) }),
    sourceVersionId: uuid(
      value.source_version_id,
      `${field}.source_version_id`,
    ),
    sourceSpanId: uuid(value.source_span_id, `${field}.source_span_id`),
    evidenceHash: hash(value.evidence_hash, `${field}.evidence_hash`),
    title: stringValue(value.title, `${field}.title`),
    rationale: stringValue(value.rationale, `${field}.rationale`),
    status: enumValue(
      value.status,
      learningPathStepStatuses,
      `${field}.status`,
    ),
    version: integer(value.version, `${field}.version`, 1),
    createdAt,
    updatedAt,
  };
  if (
    result.workspaceId !== workspaceId ||
    result.pathId !== pathId ||
    Date.parse(updatedAt) < Date.parse(createdAt)
  )
    throw invalidResponse(`${field}.binding`);
  return result;
};

const decodeReviewLearningPathSteps = (
  value: unknown,
  workspaceId: string,
  pathId: string,
  field: string,
): ReviewLearningPathStep[] => {
  if (!Array.isArray(value) || value.length > 40) throw invalidResponse(field);
  const steps = value.map((item, index) =>
    decodeReviewLearningPathStep(
      item,
      workspaceId,
      pathId,
      `${field}[${String(index)}]`,
    ),
  );
  if (
    new Set(steps.map((step) => step.id)).size !== steps.length ||
    new Set(steps.map((step) => step.stepNo)).size !== steps.length
  ) {
    throw invalidResponse(field);
  }
  return steps;
};

export const decodeReviewLearningPathResult = (
  value: unknown,
  workspaceId: string,
  answerId: string,
): ReviewLearningPathResult => {
  if (!isRecord(value)) throw invalidResponse("learning_path_result");
  exact(value, ["path", "steps", "replayed"], "learning_path_result");
  const path = decodeReviewLearningPath(
    value.path,
    workspaceId,
    answerId,
    "learning_path_result.path",
  );
  const steps = decodeReviewLearningPathSteps(
    value.steps,
    workspaceId,
    path.id,
    "learning_path_result.steps",
  );
  return {
    path,
    steps,
    replayed: bool(value.replayed, "learning_path_result.replayed"),
  };
};

const decodeReviewLearningPathStatusResult = (
  value: unknown,
  workspaceId: string,
  answerId: string,
): ReviewLearningPathStatusResult => {
  if (!isRecord(value)) throw invalidResponse("learning_path_status_result");
  exact(value, ["path", "replayed"], "learning_path_status_result");
  return {
    path: decodeReviewLearningPath(
      value.path,
      workspaceId,
      answerId,
      "learning_path_status_result.path",
    ),
    replayed: bool(value.replayed, "learning_path_status_result.replayed"),
  };
};

const decodeReviewLearningPathStepResult = (
  value: unknown,
  workspaceId: string,
  answerId: string,
): ReviewLearningPathStepResult => {
  if (!isRecord(value)) throw invalidResponse("learning_path_step_result");
  exact(value, ["path", "step", "replayed"], "learning_path_step_result");
  const path = decodeReviewLearningPath(
    value.path,
    workspaceId,
    answerId,
    "learning_path_step_result.path",
  );
  return {
    path,
    step: decodeReviewLearningPathStep(
      value.step,
      workspaceId,
      path.id,
      "learning_path_step_result.step",
    ),
    replayed: bool(value.replayed, "learning_path_step_result.replayed"),
  };
};

const readProblem = (value: unknown, status: number): ReviewApiError => {
  if (!isRecord(value))
    return new ReviewApiError(
      "HTTP_ERROR",
      "HTTP_ERROR",
      `Review API 返回 HTTP ${String(status)}。`,
      status >= 500,
      status,
    );
  exact(
    value,
    ["error_code", "message", "retryable", "workflow_run_id", "details"],
    "problem",
  );
  const errorCode = stringValue(value.error_code, "problem.error_code");
  const message = stringValue(value.message, "problem.message");
  const retryable = bool(value.retryable, "problem.retryable");
  if (value.workflow_run_id !== undefined)
    uuid(value.workflow_run_id, "problem.workflow_run_id");
  if (value.details !== undefined)
    decodeJsonObject(value.details, "problem.details");
  return new ReviewApiError(
    "HTTP_ERROR",
    errorCode,
    message,
    retryable,
    status,
  );
};
const request = async (
  path: string,
  init: RequestInit = {},
  expectedStatuses?: readonly number[],
): Promise<unknown> => {
  const headers = new Headers(init.headers);
  headers.set("Accept", "application/json");
  if (init.body !== undefined) headers.set("Content-Type", "application/json");
  let response: Response;
  try {
    response = await authFetch(path, { ...init, headers });
  } catch (error: unknown) {
    if (isAbortError(error)) throw error;
    if (error instanceof ReviewApiError) throw error;
    throw new ReviewApiError(
      "NETWORK_ERROR",
      "NETWORK_ERROR",
      "无法连接 Review API。",
      true,
      null,
      { cause: error },
    );
  }
  let payload: unknown;
  try {
    payload = parseStrictJson(await response.text());
  } catch (error: unknown) {
    throw new ReviewApiError(
      "INVALID_RESPONSE",
      "INVALID_RESPONSE",
      "Review API 返回了无效或包含重复字段的 JSON。",
      false,
      response.status,
      { cause: error },
    );
  }
  if (!response.ok) throw readProblem(payload, response.status);
  if (
    expectedStatuses !== undefined &&
    !expectedStatuses.includes(response.status)
  )
    throw invalidResponse("http_status", response.status);
  return payload;
};
const deckPath = (deckId: string): string =>
  `/api/v1/review/decks/${encodeURIComponent(requireUuid(deckId, "deckId"))}`;
const cardPath = (cardId: string): string =>
  `/api/v1/review/cards/${encodeURIComponent(requireUuid(cardId, "cardId"))}`;
const schedulePath = (
  input: ReviewDeckScheduleInput,
  action: "pause" | "resume" | "reset",
): string => `${deckPath(input.deckId)}/schedule/${action}`;
const commandHeaders = (idempotencyKey: string): HeadersInit => ({
  "Idempotency-Key": requireKey(idempotencyKey),
});
const assertDeck = (
  deck: ReviewDeck,
  workspaceId: string,
  deckId?: string,
): ReviewDeck => {
  if (
    deck.workspaceId !== workspaceId ||
    (deckId !== undefined && deck.id !== deckId)
  )
    throw invalidResponse("deck.binding");
  return deck;
};
const assertCard = (
  card: ReviewCard,
  workspaceId: string,
  deckId?: string,
  cardId?: string,
): ReviewCard => {
  if (
    card.workspaceId !== workspaceId ||
    (deckId !== undefined && card.deckId !== deckId) ||
    (cardId !== undefined && card.id !== cardId)
  )
    throw invalidResponse("card.binding");
  return card;
};
const requireAnswerPoints = (value: string[]): string[] => {
  if (!Array.isArray(value) || value.length === 0 || value.length > 128)
    throw invalidRequest("answerPoints");
  const points = value.map((point, index) =>
    requireText(point, `answerPoints[${String(index)}]`, 4 * 1024),
  );
  if (new Set(points).size !== points.length)
    throw invalidRequest("answerPoints");
  return points;
};
const requireEvidence = (
  value: unknown,
  claimId: string,
): Record<string, string>[] => {
  if (!Array.isArray(value) || value.length === 0 || value.length > 128)
    throw invalidRequest("evidence");
  const seen = new Set<string>();
  return value.map((item, index) => {
    const field = `evidence[${String(index)}]`;
    if (
      !isRecord(item) ||
      !hasOnlyKeys(item, evidenceInputFields) ||
      item.schemaVersion !== "review-evidence/v1" ||
      requireUuid(item.claimId, `${field}.claimId`) !== claimId
    )
      throw invalidRequest(field);
    const evidenceHash = item.evidenceHash;
    if (
      typeof evidenceHash !== "string" ||
      !hashPattern.test(evidenceHash) ||
      seen.has(evidenceHash)
    )
      throw invalidRequest(`${field}.evidenceHash`);
    seen.add(evidenceHash);
    return {
      schema_version: "review-evidence/v1",
      claim_id: claimId,
      source_version_id: requireUuid(
        item.sourceVersionId,
        `${field}.sourceVersionId`,
      ),
      source_span_id: requireUuid(item.sourceSpanId, `${field}.sourceSpanId`),
      evidence_hash: evidenceHash,
    };
  });
};
const reviewCardBody = (
  input: CreateReviewCardInput,
): Record<string, unknown> => {
  const workspaceId = requireUuid(input.workspaceId, "workspaceId");
  const claimId = requireUuid(input.claimId, "claimId");
  if (!cardTypes.includes(input.cardType)) throw invalidRequest("cardType");
  if (
    typeof input.difficulty !== "number" ||
    !Number.isFinite(input.difficulty) ||
    input.difficulty < 0 ||
    input.difficulty > 1
  )
    throw invalidRequest("difficulty");
  return {
    workspace_id: workspaceId,
    claim_id: claimId,
    question: requireText(input.question, "question", 8192),
    answer_points: requireAnswerPoints(input.answerPoints),
    evidence: requireEvidence(input.evidence, claimId),
    card_type: input.cardType,
    difficulty: input.difficulty,
    model_version: requireText(input.modelVersion, "modelVersion", 8192),
  };
};

export const listReviewDecks = async (
  workspaceId: string,
  signal?: AbortSignal,
): Promise<{ workspaceId: string; items: ReviewDeck[] }> => {
  const expectedWorkspaceId = requireUuid(workspaceId, "workspaceId");
  const query = new URLSearchParams({
    workspace_id: expectedWorkspaceId,
    limit: "50",
  });
  const page = decodeReviewDeckPage(
    await request(
      `/api/v1/review/decks?${query}`,
      signal === undefined ? {} : { signal },
      [200],
    ),
  );
  if (page.workspaceId !== expectedWorkspaceId)
    throw invalidResponse("deck_page.workspace_id");
  return page;
};

export const getReviewDeck = async (
  workspaceId: string,
  deckId: string,
  signal?: AbortSignal,
): Promise<ReviewDeck> => {
  const expectedWorkspaceId = requireUuid(workspaceId, "workspaceId");
  const expectedDeckId = requireUuid(deckId, "deckId");
  const deck = decodeReviewDeck(
    await request(
      `${deckPath(expectedDeckId)}?${new URLSearchParams({ workspace_id: expectedWorkspaceId })}`,
      signal === undefined ? {} : { signal },
      [200],
    ),
  );
  return assertDeck(deck, expectedWorkspaceId, expectedDeckId);
};

export const listReviewCards = async (
  workspaceId: string,
  deckId: string,
  signal?: AbortSignal,
): Promise<ReviewCardPage> => {
  const expectedWorkspaceId = requireUuid(workspaceId, "workspaceId");
  const expectedDeckId = requireUuid(deckId, "deckId");
  const query = new URLSearchParams({
    workspace_id: expectedWorkspaceId,
    limit: "200",
  });
  const page = decodeReviewCardPage(
    await request(
      `${deckPath(expectedDeckId)}/cards?${query}`,
      signal === undefined ? {} : { signal },
      [200],
    ),
  );
  if (
    page.workspaceId !== expectedWorkspaceId ||
    page.deckId !== expectedDeckId
  )
    throw invalidResponse("card_page.binding");
  return page;
};

export const createReviewCard = async (
  input: CreateReviewCardInput,
  signal?: AbortSignal,
): Promise<ReviewCard> => {
  const workspaceId = requireUuid(input.workspaceId, "workspaceId");
  const deckId = requireUuid(input.deckId, "deckId");
  const card = decodeReviewCard(
    await request(
      `${deckPath(deckId)}/cards`,
      {
        method: "POST",
        headers: commandHeaders(input.idempotencyKey),
        body: JSON.stringify(reviewCardBody(input)),
        ...(signal === undefined ? {} : { signal }),
      },
      [200, 201],
    ),
  );
  return assertCard(card, workspaceId, deckId);
};

export const editReviewCard = async (
  input: EditReviewCardInput,
  signal?: AbortSignal,
): Promise<ReviewCard> => {
  const workspaceId = requireUuid(input.workspaceId, "workspaceId");
  const deckId = requireUuid(input.deckId, "deckId");
  const cardId = requireUuid(input.cardId, "cardId");
  const card = decodeReviewCard(
    await request(
      cardPath(cardId),
      {
        method: "PUT",
        headers: commandHeaders(input.idempotencyKey),
        body: JSON.stringify({
          ...reviewCardBody(input),
          expected_version: requireVersion(input.expectedVersion),
        }),
        ...(signal === undefined ? {} : { signal }),
      },
      [200],
    ),
  );
  return assertCard(card, workspaceId, deckId, cardId);
};

const decideReviewCard = async (
  input: ReviewCardDecisionInput,
  action: "approve" | "reject" | "invalidate",
  signal?: AbortSignal,
): Promise<ReviewCard> => {
  const workspaceId = requireUuid(input.workspaceId, "workspaceId");
  const cardId = requireUuid(input.cardId, "cardId");
  const reason = requireText(
    input.reason,
    "reason",
    1024,
    action !== "invalidate",
  );
  const card = decodeReviewCard(
    await request(
      `${cardPath(cardId)}/${action}`,
      {
        method: "POST",
        headers: commandHeaders(input.idempotencyKey),
        body: JSON.stringify({
          workspace_id: workspaceId,
          expected_version: requireVersion(input.expectedVersion),
          reason,
        }),
        ...(signal === undefined ? {} : { signal }),
      },
      [200, 201],
    ),
  );
  return assertCard(card, workspaceId, undefined, cardId);
};

export const approveReviewCard = (
  input: ReviewCardDecisionInput,
  signal?: AbortSignal,
): Promise<ReviewCard> => decideReviewCard(input, "approve", signal);
export const rejectReviewCard = (
  input: ReviewCardDecisionInput,
  signal?: AbortSignal,
): Promise<ReviewCard> => decideReviewCard(input, "reject", signal);
export const invalidateReviewCard = (
  input: ReviewCardDecisionInput,
  signal?: AbortSignal,
): Promise<ReviewCard> => decideReviewCard(input, "invalidate", signal);

export const createReviewDeck = async (
  input: CreateReviewDeckInput,
  signal?: AbortSignal,
): Promise<ReviewDeck> => {
  const workspaceId = requireUuid(input.workspaceId, "workspaceId");
  const name = requireText(input.name, "name", 256);
  if (
    !Number.isSafeInteger(input.dailyLimit) ||
    input.dailyLimit < 1 ||
    input.dailyLimit > 1000
  )
    throw invalidRequest("dailyLimit");
  const deck = decodeReviewDeck(
    await request(
      "/api/v1/review/decks",
      {
        method: "POST",
        headers: commandHeaders(input.idempotencyKey),
        body: JSON.stringify({
          workspace_id: workspaceId,
          name,
          scope: requireJsonObject(input.scope, "scope"),
          daily_limit: input.dailyLimit,
        }),
        ...(signal === undefined ? {} : { signal }),
      },
      [200, 201],
    ),
  );
  return assertDeck(deck, workspaceId);
};

const changeReviewDeckSchedule = async (
  input: ReviewDeckScheduleInput,
  action: "pause" | "resume" | "reset",
  signal?: AbortSignal,
): Promise<ReviewDeck> => {
  const workspaceId = requireUuid(input.workspaceId, "workspaceId");
  const deckId = requireUuid(input.deckId, "deckId");
  const deck = decodeReviewDeck(
    await request(
      schedulePath({ ...input, workspaceId, deckId }, action),
      {
        method: "POST",
        headers: commandHeaders(input.idempotencyKey),
        body: JSON.stringify({
          workspace_id: workspaceId,
          expected_version: requireVersion(input.expectedVersion),
        }),
        ...(signal === undefined ? {} : { signal }),
      },
      [200],
    ),
  );
  return assertDeck(deck, workspaceId, deckId);
};

export const pauseReviewDeck = (
  input: ReviewDeckScheduleInput,
  signal?: AbortSignal,
): Promise<ReviewDeck> => changeReviewDeckSchedule(input, "pause", signal);
export const resumeReviewDeck = (
  input: ReviewDeckScheduleInput,
  signal?: AbortSignal,
): Promise<ReviewDeck> => changeReviewDeckSchedule(input, "resume", signal);
export const resetReviewDeck = (
  input: ReviewDeckScheduleInput,
  signal?: AbortSignal,
): Promise<ReviewDeck> => changeReviewDeckSchedule(input, "reset", signal);

export const listReviewDue = async (
  workspaceId: string,
  sessionId: string,
  deckId?: string,
  signal?: AbortSignal,
): Promise<ReviewDuePage> => {
  const expectedWorkspaceId = requireUuid(workspaceId, "workspaceId");
  const expectedSessionId = requireUuid(sessionId, "sessionId");
  const query = new URLSearchParams({
    workspace_id: expectedWorkspaceId,
    session_id: expectedSessionId,
    limit: "20",
  });
  const expectedDeckId =
    deckId === undefined ? undefined : requireUuid(deckId, "deckId");
  if (expectedDeckId !== undefined) query.set("deck_id", expectedDeckId);
  const page = decodeReviewDuePage(
    await request(
      `/api/v1/review/due?${query}`,
      signal === undefined ? {} : { signal },
      [200],
    ),
  );
  if (
    page.workspaceId !== expectedWorkspaceId ||
    page.items.some(
      (item) =>
        item.card.workspaceId !== expectedWorkspaceId ||
        (expectedDeckId !== undefined && item.card.deckId !== expectedDeckId),
    )
  )
    throw invalidResponse("due_page.binding");
  return page;
};

export const startReviewSession = async (
  input: StartReviewSessionInput,
  signal?: AbortSignal,
): Promise<ReviewSession> => {
  const workspaceId = requireUuid(input.workspaceId, "workspaceId");
  const deckId = requireUuid(input.deckId, "deckId");
  const session = decodeReviewSession(
    await request(
      "/api/v1/review/sessions",
      {
        method: "POST",
        headers: commandHeaders(input.idempotencyKey),
        body: JSON.stringify({
          workspace_id: workspaceId,
          deck_id: deckId,
          session_type: "REVIEW",
          config: requireJsonObject(input.config, "config"),
        }),
        ...(signal === undefined ? {} : { signal }),
      },
      [200, 201],
    ),
  );
  if (session.workspaceId !== workspaceId || session.deckId !== deckId)
    throw invalidResponse("session.binding");
  return session;
};

export const completeReviewSession = async (
  input: CompleteReviewSessionInput,
  signal?: AbortSignal,
): Promise<ReviewSession> => {
  const workspaceId = requireUuid(input.workspaceId, "workspaceId");
  const sessionId = requireUuid(input.sessionId, "sessionId");
  const session = decodeReviewSession(
    await request(
      `/api/v1/review/sessions/${encodeURIComponent(sessionId)}/complete`,
      {
        method: "POST",
        headers: commandHeaders(input.idempotencyKey),
        body: JSON.stringify({
          workspace_id: workspaceId,
          cancelled: input.cancelled,
        }),
        ...(signal === undefined ? {} : { signal }),
      },
      [200],
    ),
  );
  if (session.workspaceId !== workspaceId || session.id !== sessionId)
    throw invalidResponse("session.binding");
  return session;
};

export const submitReviewAnswer = async (
  input: SubmitReviewAnswerInput,
  signal?: AbortSignal,
): Promise<ReviewAnswerResult> => {
  const workspaceId = requireUuid(input.workspaceId, "workspaceId");
  const sessionId = requireUuid(input.sessionId, "sessionId");
  const cardId = requireUuid(input.cardId, "cardId");
  const questionRef = requireText(input.questionRef, "questionRef", 512);
  const userAnswer = requireText(
    input.userAnswer,
    "userAnswer",
    64 * 1024,
    true,
  );
  if (![1, 2, 3, 4].includes(input.rating)) throw invalidRequest("rating");
  const result = decodeReviewAnswerResult(
    await request(
      `/api/v1/review/sessions/${encodeURIComponent(sessionId)}/answers`,
      {
        method: "POST",
        headers: commandHeaders(input.idempotencyKey),
        // Score、答案要点、证据与调度均是服务端可信事实，浏览器只提交自己的回答和自评。
        body: JSON.stringify({
          workspace_id: workspaceId,
          card_id: cardId,
          question_ref: questionRef,
          user_answer: userAnswer,
          rating: input.rating,
        }),
        ...(signal === undefined ? {} : { signal }),
      },
      [200, 201],
    ),
  );
  if (
    result.answer.workspaceId !== workspaceId ||
    result.answer.sessionId !== sessionId ||
    result.answer.cardId !== cardId ||
    result.answer.questionRef !== questionRef
  )
    throw invalidResponse("answer_result.binding");
  return result;
};

const reviewLearningPathPath = (answerId: string): string =>
  `/api/v1/review/answers/${encodeURIComponent(requireUuid(answerId, "answerId"))}/learning-path`;

export const getReviewLearningPath = async (
  workspaceId: string,
  answerId: string,
  signal?: AbortSignal,
): Promise<ReviewLearningPathResult> => {
  const validWorkspaceId = requireUuid(workspaceId, "workspaceId");
  const validAnswerId = requireUuid(answerId, "answerId");
  const query = new URLSearchParams({ workspace_id: validWorkspaceId });
  return decodeReviewLearningPathResult(
    await request(
      `${reviewLearningPathPath(validAnswerId)}?${query.toString()}`,
      {
        ...(signal === undefined ? {} : { signal }),
      },
      [200],
    ),
    validWorkspaceId,
    validAnswerId,
  );
};

export const createReviewLearningPath = async (
  input: CreateReviewLearningPathInput,
  signal?: AbortSignal,
): Promise<ReviewLearningPathResult> => {
  const workspaceId = requireUuid(input.workspaceId, "workspaceId");
  const answerId = requireUuid(input.answerId, "answerId");
  return decodeReviewLearningPathResult(
    await request(
      reviewLearningPathPath(answerId),
      {
        method: "POST",
        headers: commandHeaders(input.idempotencyKey),
        // Gap、Evidence 和 Artifact 绑定由服务端从已持久化的 Review Answer 推导，客户端只请求创建。
        body: JSON.stringify({ workspace_id: workspaceId }),
        ...(signal === undefined ? {} : { signal }),
      },
      [200, 201],
    ),
    workspaceId,
    answerId,
  );
};

export const updateReviewLearningPathStatus = async (
  input: UpdateReviewLearningPathStatusInput,
  signal?: AbortSignal,
): Promise<ReviewLearningPathStatusResult> => {
  const workspaceId = requireUuid(input.workspaceId, "workspaceId");
  const answerId = requireUuid(input.answerId, "answerId");
  if (!learningPathStatuses.includes(input.status))
    throw invalidRequest("status");
  return decodeReviewLearningPathStatusResult(
    await request(
      `${reviewLearningPathPath(answerId)}/status`,
      {
        method: "PUT",
        headers: commandHeaders(input.idempotencyKey),
        body: JSON.stringify({
          workspace_id: workspaceId,
          expected_version: requireVersion(input.expectedVersion),
          status: input.status,
        }),
        ...(signal === undefined ? {} : { signal }),
      },
      [200],
    ),
    workspaceId,
    answerId,
  );
};

export const updateReviewLearningPathStep = async (
  input: UpdateReviewLearningPathStepInput,
  signal?: AbortSignal,
): Promise<ReviewLearningPathStepResult> => {
  const workspaceId = requireUuid(input.workspaceId, "workspaceId");
  const answerId = requireUuid(input.answerId, "answerId");
  const stepId = requireUuid(input.stepId, "stepId");
  if (!learningPathStepTargetStatuses.includes(input.status))
    throw invalidRequest("status");
  return decodeReviewLearningPathStepResult(
    await request(
      `${reviewLearningPathPath(answerId)}/steps/${encodeURIComponent(stepId)}`,
      {
        method: "PUT",
        headers: commandHeaders(input.idempotencyKey),
        body: JSON.stringify({
          workspace_id: workspaceId,
          expected_version: requireVersion(input.expectedVersion),
          status: input.status,
        }),
        ...(signal === undefined ? {} : { signal }),
      },
      [200],
    ),
    workspaceId,
    answerId,
  );
};
