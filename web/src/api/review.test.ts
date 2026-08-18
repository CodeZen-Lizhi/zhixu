import { afterEach, describe, expect, it, vi } from "vitest";

import {
  ReviewApiError,
  approveReviewCard,
  createReviewCard,
  createReviewDeck,
  decodeReviewAnswerResult,
  decodeReviewCard,
  decodeReviewDeck,
  decodeReviewDuePage,
  decodeReviewSession,
  editReviewCard,
  listReviewCards,
  listReviewDecks,
  listReviewDue,
  startReviewSession,
  submitReviewAnswer,
} from "./review";

const workspaceId = "72000000-0000-4000-8000-000000000001";
const deckId = "72000000-0000-4000-8000-000000000002";
const cardId = "72000000-0000-4000-8000-000000000003";
const sessionId = "72000000-0000-4000-8000-000000000004";
const answerId = "72000000-0000-4000-8000-000000000005";
const questionRef = "review-question/v1.test-reference";
const claimId = "72000000-0000-4000-8000-000000000006";
const sourceVersionId = "72000000-0000-4000-8000-000000000007";
const sourceSpanId = "72000000-0000-4000-8000-000000000008";
const hash = "a".repeat(64);
const createdAt = "2026-07-27T00:00:00Z";
const updatedAt = "2026-07-27T00:01:00Z";

const deck = {
  id: deckId,
  workspace_id: workspaceId,
  name: "Go foundations",
  scope: {},
  status: "ACTIVE",
  daily_limit: 20,
  scheduler_version: "fsrs/v1",
  version: 1,
  created_at: createdAt,
  updated_at: updatedAt,
};
const evidenceBinding = {
  schema_version: "review-evidence/v1",
  claim_id: claimId,
  source_version_id: sourceVersionId,
  source_span_id: sourceSpanId,
  evidence_hash: hash,
};
const reviewCard = {
  id: cardId,
  workspace_id: workspaceId,
  deck_id: deckId,
  claim_id: claimId,
  question: "What does a context cancellation signal?",
  answer_points: ["It tells downstream work to stop."],
  evidence: [evidenceBinding],
  card_type: "SHORT_ANSWER",
  difficulty: 0.5,
  status: "DRAFT",
  fingerprint: "b".repeat(64),
  model_version: "review-card/v1",
  version: 1,
  created_at: createdAt,
  updated_at: updatedAt,
};
const dueCard = {
  id: cardId,
  workspace_id: workspaceId,
  deck_id: deckId,
  question: "What does a context cancellation signal?",
  card_type: "SHORT_ANSWER",
  difficulty: 0.5,
  status: "APPROVED",
  version: 2,
};
const schedule = {
  card_id: cardId,
  workspace_id: workspaceId,
  due_at: createdAt,
  interval_days: 0,
  stability: 0,
  difficulty: 0.5,
  scheduler_version: "fsrs/v1",
  paused: false,
  version: 3,
};
const score = {
  schema_version: "review-score/v1",
  correctness: { value: 0.8, rationale: "server verified correctness" },
  coverage: { value: 0.8, rationale: "server verified coverage" },
  boundaries: { value: 0.7, rationale: "server verified boundaries" },
  clarity: { value: 0.9, rationale: "server verified clarity" },
  confidence: { value: 0.75, rationale: "self-rating is separate" },
  errors: [],
  omissions: ["missing cancellation propagation"],
  evidence: [{
    schema_version: "review-evidence/v1",
    claim_id: claimId,
    source_version_id: sourceVersionId,
    source_span_id: sourceSpanId,
    evidence_hash: hash,
    source_version_href: `/api/v1/workspaces/${workspaceId}/source-versions/${sourceVersionId}`,
    source_span_href: `/api/v1/workspaces/${workspaceId}/source-versions/${sourceVersionId}/spans/${sourceSpanId}`,
  }],
};
const answerResult = {
  answer: {
    id: answerId,
    workspace_id: workspaceId,
    session_id: sessionId,
    card_id: cardId,
    question_ref: questionRef,
    user_answer: "It tells downstream work to stop.",
    rating: 3,
    scorer_version: "review-test-scorer/v1",
    score,
    feedback: { schema_version: "review-score/v1", errors: [], omissions: ["missing cancellation propagation"], evidence_count: 1 },
    created_at: updatedAt,
  },
  schedule: { ...schedule, due_at: "2026-07-28T00:00:00Z", interval_days: 1, stability: 1, version: 4 },
  replayed: false,
};

const jsonResponse = (value: unknown, status = 200): Response => new Response(JSON.stringify(value), { status, headers: { "Content-Type": "application/json" } });

afterEach(() => vi.unstubAllGlobals());

describe("review API", () => {
  it.each([
    ["0000", "0000-02-29T00:00:00Z", "0000-02-30T00:00:00Z"],
    ["0001", "0001-02-28T00:00:00Z", "0001-02-29T00:00:00Z"],
    ["1900", "1900-02-28T00:00:00Z", "1900-02-29T00:00:00Z"],
    ["2000", "2000-02-29T00:00:00Z", "2000-02-30T00:00:00Z"],
  ])("按 Gregorian 闰年规则校验 %s 年", (_year, validTimestamp, invalidTimestamp) => {
    expect(() => decodeReviewDeck({ ...deck, created_at: validTimestamp })).not.toThrow();
    expect(() => decodeReviewDeck({ ...deck, created_at: invalidTimestamp })).toThrow(ReviewApiError);
  });

  it("仅接受绑定 Deck 的 REVIEW Session 响应", () => {
    const session = {
      id: sessionId,
      workspace_id: workspaceId,
      deck_id: deckId,
      session_type: "REVIEW",
      status: "ACTIVE",
      config: {},
      started_at: createdAt,
    };

    expect(decodeReviewSession(session)).toMatchObject({ id: sessionId, deckId, sessionType: "REVIEW" });
    expect(() => decodeReviewSession({ ...session, session_type: "INTERVIEW" })).toThrow(ReviewApiError);
    const withoutDeck: Partial<typeof session> = { ...session };
    delete withoutDeck.deck_id;
    expect(() => decodeReviewSession(withoutDeck)).toThrow(ReviewApiError);
  });

  it("仅以 REVIEW 类型启动绑定 Deck 的会话", async () => {
    const session = {
      id: sessionId,
      workspace_id: workspaceId,
      deck_id: deckId,
      session_type: "REVIEW",
      status: "ACTIVE",
      config: {},
      started_at: createdAt,
    };
    const fetchMock = vi.fn<typeof fetch>().mockResolvedValue(jsonResponse(session, 201));
    vi.stubGlobal("fetch", fetchMock);

    await expect(startReviewSession({ workspaceId, deckId, config: {}, idempotencyKey: "review-session-start-1" })).resolves.toMatchObject({
      id: sessionId,
      deckId,
      sessionType: "REVIEW",
    });
    const init = fetchMock.mock.calls[0]?.[1];
    expect(new Headers(init?.headers).get("Idempotency-Key")).toBe("review-session-start-1");
    if (typeof init?.body !== "string") throw new Error("expected JSON request body");
    expect(JSON.parse(init.body)).toEqual({ workspace_id: workspaceId, deck_id: deckId, session_type: "REVIEW", config: {} });
  });

  it("loads Workspace-bound decks and creates a deck with an Idempotency-Key", async () => {
    const fetchMock = vi.fn<typeof fetch>()
      .mockResolvedValueOnce(jsonResponse({ workspace_id: workspaceId, items: [deck] }))
      .mockResolvedValueOnce(jsonResponse(deck, 201));
    vi.stubGlobal("fetch", fetchMock);

    await expect(listReviewDecks(workspaceId)).resolves.toMatchObject({ workspaceId, items: [{ id: deckId }] });
    await expect(createReviewDeck({ workspaceId, name: "Go foundations", scope: {}, dailyLimit: 20, idempotencyKey: "review-deck-create-1" })).resolves.toMatchObject({ id: deckId });

    expect(fetchMock.mock.calls[0]?.[0]).toBe(`/api/v1/review/decks?workspace_id=${workspaceId}&limit=50`);
    const createInit = fetchMock.mock.calls[1]?.[1];
    expect(new Headers(createInit?.headers).get("Idempotency-Key")).toBe("review-deck-create-1");
    if (typeof createInit?.body !== "string") throw new Error("expected JSON request body");
    expect(JSON.parse(createInit.body)).toEqual({ workspace_id: workspaceId, name: "Go foundations", scope: {}, daily_limit: 20 });
  });

  it("lists, creates, edits, and approves Workspace-bound cards with strict command contracts", async () => {
    const edited = { ...reviewCard, question: "How does context cancellation propagate?", version: 2 };
    const approved = { ...edited, status: "APPROVED", version: 3 };
    const fetchMock = vi.fn<typeof fetch>()
      .mockResolvedValueOnce(jsonResponse({ workspace_id: workspaceId, deck_id: deckId, items: [reviewCard] }))
      .mockResolvedValueOnce(jsonResponse(reviewCard, 201))
      .mockResolvedValueOnce(jsonResponse(edited))
      .mockResolvedValueOnce(jsonResponse(approved));
    vi.stubGlobal("fetch", fetchMock);

    await expect(listReviewCards(workspaceId, deckId)).resolves.toMatchObject({
      workspaceId,
      deckId,
      items: [{ id: cardId, status: "DRAFT", evidence: [{ sourceSpanId }] }],
    });
    const createInput = {
      workspaceId,
      deckId,
      claimId,
      question: reviewCard.question,
      answerPoints: reviewCard.answer_points,
      evidence: [{
        schemaVersion: "review-evidence/v1" as const,
        claimId,
        sourceVersionId,
        sourceSpanId,
        evidenceHash: hash,
      }],
      cardType: "SHORT_ANSWER" as const,
      difficulty: 0.5,
      modelVersion: "review-card/v1",
      idempotencyKey: "review-card-create-1",
    };
    await expect(createReviewCard(createInput)).resolves.toMatchObject({ id: cardId, version: 1 });
    await expect(editReviewCard({
      ...createInput,
      cardId,
      question: edited.question,
      expectedVersion: 1,
      idempotencyKey: "review-card-edit-1",
    })).resolves.toMatchObject({ id: cardId, question: edited.question, version: 2 });
    await expect(approveReviewCard({
      workspaceId,
      cardId,
      expectedVersion: 2,
      reason: "",
      idempotencyKey: "review-card-approve-1",
    })).resolves.toMatchObject({ id: cardId, status: "APPROVED", version: 3 });

    expect(fetchMock.mock.calls[0]?.[0]).toBe(`/api/v1/review/decks/${deckId}/cards?workspace_id=${workspaceId}&limit=200`);
    expect(fetchMock.mock.calls[1]?.[0]).toBe(`/api/v1/review/decks/${deckId}/cards`);
    expect(fetchMock.mock.calls[2]?.[0]).toBe(`/api/v1/review/cards/${cardId}`);
    expect(fetchMock.mock.calls[3]?.[0]).toBe(`/api/v1/review/cards/${cardId}/approve`);

    const createInit = fetchMock.mock.calls[1]?.[1];
    const editInit = fetchMock.mock.calls[2]?.[1];
    const approveInit = fetchMock.mock.calls[3]?.[1];
    expect(new Headers(createInit?.headers).get("Idempotency-Key")).toBe("review-card-create-1");
    expect(new Headers(editInit?.headers).get("Idempotency-Key")).toBe("review-card-edit-1");
    expect(new Headers(approveInit?.headers).get("Idempotency-Key")).toBe("review-card-approve-1");
    if (typeof createInit?.body !== "string" || typeof editInit?.body !== "string" || typeof approveInit?.body !== "string") throw new Error("expected JSON request bodies");
    expect(JSON.parse(createInit.body)).toEqual({
      workspace_id: workspaceId,
      claim_id: claimId,
      question: reviewCard.question,
      answer_points: reviewCard.answer_points,
      evidence: [evidenceBinding],
      card_type: "SHORT_ANSWER",
      difficulty: 0.5,
      model_version: "review-card/v1",
    });
    expect(JSON.parse(editInit.body)).toMatchObject({ question: edited.question, expected_version: 1 });
    expect(JSON.parse(approveInit.body)).toEqual({ workspace_id: workspaceId, expected_version: 2, reason: "" });
  });

  it("rejects quote fields in Card and post-answer evidence responses", () => {
    expect(() => decodeReviewCard({
      ...reviewCard,
      evidence: [{ ...evidenceBinding, quote: "untrusted browser quote" }],
    })).toThrow(ReviewApiError);
    expect(() => decodeReviewAnswerResult({
      ...answerResult,
      answer: {
        ...answerResult.answer,
        score: {
          ...score,
          evidence: [{ ...score.evidence[0], quote: "untrusted server echo" }],
        },
      },
    })).toThrow(ReviewApiError);
  });

  it("rejects a client-authored evidence quote before sending a Card command", async () => {
    const fetchMock = vi.fn<typeof fetch>();
    vi.stubGlobal("fetch", fetchMock);
    const clientAuthoredEvidence = {
      schemaVersion: "review-evidence/v1" as const,
      claimId,
      sourceVersionId,
      sourceSpanId,
      evidenceHash: hash,
      quote: "untrusted browser quote",
    };

    await expect(createReviewCard({
      workspaceId,
      deckId,
      claimId,
      question: reviewCard.question,
      answerPoints: reviewCard.answer_points,
      evidence: [clientAuthoredEvidence],
      cardType: "SHORT_ANSWER",
      difficulty: 0.5,
      modelVersion: "review-card/v1",
      idempotencyKey: "review-card-quote",
    })).rejects.toMatchObject({ code: "INVALID_REQUEST" });
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("accepts only the safe due-card projection before an answer is submitted", async () => {
    const fetchMock = vi.fn<typeof fetch>().mockResolvedValue(jsonResponse({ workspace_id: workspaceId, items: [{ card: dueCard, schedule, question_ref: questionRef }] }));
    vi.stubGlobal("fetch", fetchMock);

		await expect(listReviewDue(workspaceId, sessionId, deckId)).resolves.toMatchObject({ workspaceId, items: [{ card: { id: cardId, question: dueCard.question } }] });
		expect(fetchMock.mock.calls[0]?.[0]).toBe(`/api/v1/review/due?workspace_id=${workspaceId}&session_id=${sessionId}&deck_id=${deckId}&limit=20`);

    expect(() => decodeReviewDuePage({
      workspace_id: workspaceId,
      items: [{ card: { ...dueCard, answer_points: ["hidden answer"] }, schedule, question_ref: questionRef }],
    })).toThrow(ReviewApiError);
    expect(() => decodeReviewDuePage({
      workspace_id: workspaceId,
      items: [{ card: { ...dueCard, evidence: [] }, schedule, question_ref: questionRef }],
    })).toThrow(ReviewApiError);
  });

  it("submits only the learner answer and rating, then validates post-answer score evidence", async () => {
    const fetchMock = vi.fn<typeof fetch>().mockResolvedValue(jsonResponse(answerResult, 201));
    vi.stubGlobal("fetch", fetchMock);

    await expect(submitReviewAnswer({
      workspaceId,
      sessionId,
      cardId,
      questionRef,
      userAnswer: "It tells downstream work to stop.",
      rating: 3,
      idempotencyKey: "review-answer-1",
    })).resolves.toMatchObject({ answer: { id: answerId, score: { evidence: [{ sourceSpanId }] } }, schedule: { version: 4 } });

    const call = fetchMock.mock.calls[0];
    expect(call?.[0]).toBe(`/api/v1/review/sessions/${sessionId}/answers`);
    expect(new Headers(call?.[1]?.headers).get("Idempotency-Key")).toBe("review-answer-1");
    if (typeof call?.[1]?.body !== "string") throw new Error("expected JSON request body");
    expect(JSON.parse(call[1].body)).toEqual({
      workspace_id: workspaceId,
      card_id: cardId,
      question_ref: questionRef,
      user_answer: "It tells downstream work to stop.",
      rating: 3,
    });
  });

  it("fails closed for drifted evidence hrefs, duplicate JSON keys, and response Workspace drift", async () => {
    const fetchMock = vi.fn<typeof fetch>()
      .mockResolvedValueOnce(jsonResponse({ ...answerResult, answer: { ...answerResult.answer, score: { ...score, evidence: [{ ...score.evidence[0], source_span_href: "/unsafe" }] } } }))
      .mockResolvedValueOnce(new Response(`{"workspace_id":"${workspaceId}","workspace_id":"${workspaceId}","items":[]}`, { status: 200, headers: { "Content-Type": "application/json" } }))
      .mockResolvedValueOnce(jsonResponse({ workspace_id: deckId, items: [] }));
    vi.stubGlobal("fetch", fetchMock);
    const input = { workspaceId, sessionId, cardId, questionRef, userAnswer: "answer", rating: 3 as const, idempotencyKey: "review-answer-2" };

    await expect(submitReviewAnswer(input)).rejects.toMatchObject({ code: "INVALID_RESPONSE" });
    await expect(listReviewDecks(workspaceId)).rejects.toMatchObject({ code: "INVALID_RESPONSE" });
    await expect(listReviewDecks(workspaceId)).rejects.toMatchObject({ code: "INVALID_RESPONSE" });
  });

  it("keeps review state conflicts visible to the caller", async () => {
    vi.stubGlobal("fetch", vi.fn<typeof fetch>().mockResolvedValue(jsonResponse({ error_code: "REVIEW_SCHEDULE_CONFLICT", message: "stale schedule", retryable: false }, 409)));
    await expect(submitReviewAnswer({ workspaceId, sessionId, cardId, questionRef, userAnswer: "answer", rating: 3, idempotencyKey: "review-answer-conflict" })).rejects.toMatchObject({
      code: "HTTP_ERROR",
      errorCode: "REVIEW_SCHEDULE_CONFLICT",
      retryable: false,
      status: 409,
    });
  });
});
