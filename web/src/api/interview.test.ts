import { afterEach, describe, expect, it, vi } from "vitest";

const authFetchMock = vi.hoisted(() => vi.fn<typeof fetch>());

vi.mock("./auth", () => ({ authFetch: authFetchMock }));

import {
  InterviewApiError,
  completeInterview,
  decodeInterviewSessionPage,
  decodeInterviewSnapshot,
  listInterviewSessions,
  suggestInterviewMemoryCandidate,
  startInterview,
  submitInterviewTurn,
} from "./interview";

const workspaceId = "71000000-0000-4000-8000-000000000001";
const sessionId = "71000000-0000-4000-8000-000000000002";
const questionId = "71000000-0000-4000-8000-000000000003";
const claimId = "71000000-0000-4000-8000-000000000004";
const sourceVersionId = "71000000-0000-4000-8000-000000000005";
const sourceSpanId = "71000000-0000-4000-8000-000000000006";
const turnId = "71000000-0000-4000-8000-000000000007";
const reportId = "71000000-0000-4000-8000-000000000008";
const pathId = "71000000-0000-4000-8000-000000000009";
const artifactId = "71000000-0000-4000-8000-000000000010";
const revisionId = "71000000-0000-4000-8000-000000000011";
const stepId = "71000000-0000-4000-8000-000000000012";
const followUpQuestionId = "71000000-0000-4000-8000-000000000013";
const followUpTurnId = "71000000-0000-4000-8000-000000000014";
const olderSessionId = "71000000-0000-4000-8000-000000000015";
const createdAt = "2026-07-27T00:00:00Z";
const hash = "a".repeat(64);

const config = { schemaVersion: "interview/v1" as const, role: "Go engineer", scope: { claimIds: [claimId], topicIds: [] }, difficulty: "INTERMEDIATE" as const, durationMinutes: 30, questionCount: 1, maxFollowUps: 2 };
const session = (status: "ACTIVE" | "COMPLETED" = "ACTIVE") => ({ id: sessionId, workspace_id: workspaceId, config: { schema_version: config.schemaVersion, role: config.role, scope: { claim_ids: config.scope.claimIds, topic_ids: config.scope.topicIds }, difficulty: config.difficulty, duration_minutes: config.durationMinutes, question_count: config.questionCount, max_follow_ups: config.maxFollowUps }, status, version: 1, follow_up_count: 0, started_at: createdAt, ...(status === "COMPLETED" ? { ended_at: createdAt } : {}) });
const question = { id: questionId, workspace_id: workspaceId, session_id: sessionId, question_no: 1, follow_up_no: 0, claim_id: claimId, prompt: "Explain context cancellation.", status: "PENDING", created_at: createdAt };
const evidence = { schema_version: "interview-evidence/v1", claim_id: claimId, source_version_id: sourceVersionId, source_span_id: sourceSpanId, evidence_hash: hash, support_type: "SUPPORTS", source_version_href: `/api/v1/workspaces/${workspaceId}/source-versions/${sourceVersionId}`, source_span_href: `/api/v1/workspaces/${workspaceId}/source-versions/${sourceVersionId}/spans/${sourceSpanId}` };
const score = { schema_version: "interview-score/v1", correctness: { value: 0.8, rationale: "correct" }, coverage: { value: 0.7, rationale: "coverage" }, boundaries: { value: 0.6, rationale: "boundaries" }, clarity: { value: 0.9, rationale: "clear" }, errors: [], omissions: [], evidence: [evidence] };
const turn = { id: turnId, workspace_id: workspaceId, session_id: sessionId, question_id: questionId, score, decision: { follow_up_created: false }, scorer_version: "v1", created_at: createdAt };
const artifact = (kind: "INTERVIEW_DOC" | "LEARNING_PATH") => ({ kind, artifact_id: artifactId, revision_id: revisionId, artifact_version: 1 });
const report = { id: reportId, workspace_id: workspaceId, session_id: sessionId, schema_version: "interview-report/v1", summary: { questions_total: 1, answered_total: 1, skipped_total: 0, correctness: 0.8, coverage: 0.7, boundaries: 0.6, clarity: 0.9 }, strengths: [], gaps: [], expression: [], evidence: [evidence], artifact: artifact("INTERVIEW_DOC"), created_at: createdAt };
const path = { id: pathId, workspace_id: workspaceId, session_id: sessionId, report_id: reportId, artifact: artifact("LEARNING_PATH"), status: "ACTIVE", version: 1, created_at: createdAt, updated_at: createdAt };
const step = { id: stepId, workspace_id: workspaceId, path_id: pathId, step_no: 1, claim_id: claimId, source_version_id: sourceVersionId, source_span_id: sourceSpanId, evidence_hash: hash, title: "Practice cancellation", rationale: "Cover the gap", status: "PENDING", version: 1, created_at: createdAt, updated_at: createdAt };
const jsonResponse = (value: unknown, status = 200): Response => new Response(JSON.stringify(value), { status, headers: { "Content-Type": "application/json" } });

afterEach(() => {
  authFetchMock.mockReset();
  vi.unstubAllGlobals();
});

describe("interview API", () => {
  it("使用 snake_case 请求体和稳定 Idempotency-Key 创建面试", async () => {
    authFetchMock.mockResolvedValue(jsonResponse({ session: session(), questions: [question], replayed: false }, 201));

    await expect(startInterview({ workspaceId, config, idempotencyKey: "interview-start-1" })).resolves.toMatchObject({ session: { id: sessionId }, questions: [{ prompt: question.prompt }] });
    expect(authFetchMock.mock.calls[0]?.[0]).toBe("/api/v1/review/interviews");
    const init = authFetchMock.mock.calls[0]?.[1];
    expect(new Headers(init?.headers).get("Idempotency-Key")).toBe("interview-start-1");
    if (typeof init?.body !== "string") throw new Error("expected JSON request body");
    expect(JSON.parse(init.body)).toEqual({ workspace_id: workspaceId, config: { schema_version: "interview/v1", role: "Go engineer", scope: { claim_ids: [claimId] }, difficulty: "INTERMEDIATE", duration_minutes: 30, question_count: 1, max_follow_ups: 2 } });
  });

  it("按 Workspace 游标列出可恢复会话，并拒绝列表泄露题目或失序", async () => {
    const cursor = "eyJ2ZXJzaW9uIjoxfQ";
    authFetchMock
      .mockResolvedValueOnce(jsonResponse({ workspace_id: workspaceId, items: [session()], next_cursor: cursor }))
      .mockResolvedValueOnce(jsonResponse({ workspace_id: workspaceId, items: [{ ...session(), questions: [] }] }));

    await expect(listInterviewSessions({ workspaceId, limit: 20 })).resolves.toMatchObject({
      workspaceId,
      items: [{ id: sessionId, config: { role: "Go engineer" } }],
      nextCursor: cursor,
    });
    expect(authFetchMock.mock.calls[0]?.[0]).toBe(`/api/v1/review/interviews?workspace_id=${workspaceId}&limit=20`);
    await expect(listInterviewSessions({ workspaceId, limit: 20, cursor })).rejects.toMatchObject({ code: "INVALID_RESPONSE" });
    expect(authFetchMock.mock.calls[1]?.[0]).toBe(`/api/v1/review/interviews?workspace_id=${workspaceId}&limit=20&cursor=${cursor}`);

    expect(() => decodeInterviewSessionPage({
      workspace_id: workspaceId,
      items: [
        { ...session(), id: olderSessionId, started_at: "2026-07-26T00:00:00Z" },
        session(),
      ],
    })).toThrow(InterviewApiError);
  });

  it.each([
    ["0000", "0000-02-29T00:00:00Z", "0000-02-30T00:00:00Z"],
    ["0001", "0001-02-28T00:00:00Z", "0001-02-29T00:00:00Z"],
    ["1900", "1900-02-28T00:00:00Z", "1900-02-29T00:00:00Z"],
    ["2000", "2000-02-29T00:00:00Z", "2000-02-30T00:00:00Z"],
  ])("按 Gregorian 闰年规则校验 %s 年", (_year, validTimestamp, invalidTimestamp) => {
    expect(() => decodeInterviewSessionPage({ workspace_id: workspaceId, items: [{ ...session(), started_at: validTimestamp }] })).not.toThrow();
    expect(() => decodeInterviewSessionPage({ workspace_id: workspaceId, items: [{ ...session(), started_at: invalidTimestamp }] })).toThrow(InterviewApiError);
  });

  it("在作答前拒绝包含答案要点或证据的题面", async () => {
    authFetchMock
      .mockResolvedValueOnce(jsonResponse({ session: session(), questions: [{ ...question, answer_points: ["hidden answer"] }], replayed: false }))
      .mockResolvedValueOnce(jsonResponse({ session: session(), questions: [{ ...question, evidence: [evidence] }], replayed: false }));

    await expect(startInterview({ workspaceId, config, idempotencyKey: "interview-hidden-answer" })).rejects.toBeInstanceOf(InterviewApiError);
    await expect(startInterview({ workspaceId, config, idempotencyKey: "interview-hidden-evidence" })).rejects.toMatchObject({ code: "INVALID_RESPONSE" });
  });

  it("拒绝漂移证据链接和重复 JSON key", async () => {
    authFetchMock
      .mockResolvedValueOnce(jsonResponse({ turn: { ...turn, score: { ...score, evidence: [{ ...evidence, source_span_href: "/unsafe" }] } }, replayed: false }))
      .mockResolvedValueOnce(new Response(`{"session":${JSON.stringify(session())},"questions":[],"replayed":false,"replayed":false}`, { status: 200, headers: { "Content-Type": "application/json" } }));

    await expect(submitInterviewTurn({ workspaceId, sessionId, questionId, userAnswer: "A cancellation signal.", idempotencyKey: "interview-turn-1" })).rejects.toMatchObject({ code: "INVALID_RESPONSE" });
    await expect(startInterview({ workspaceId, config, idempotencyKey: "interview-duplicate-json" })).rejects.toMatchObject({ code: "INVALID_RESPONSE" });
  });

  it("完成面试时要求报告与学习路径属于同一报告", async () => {
    authFetchMock.mockResolvedValue(jsonResponse({ session: session("COMPLETED"), report, path: { ...path, report_id: artifactId }, steps: [step], replayed: false }));
    await expect(completeInterview({ workspaceId, sessionId, manualEnd: true, idempotencyKey: "interview-complete-1" })).rejects.toMatchObject({ code: "INVALID_RESPONSE" });
  });

  it("只提交 Workspace 和步骤路径来创建待确认 Memory Candidate", async () => {
    const memoryId = "71000000-0000-4000-8000-000000000016";
    authFetchMock.mockResolvedValue(jsonResponse({ memory_id: memoryId, replayed: false }, 201));

    await expect(suggestInterviewMemoryCandidate({
      workspaceId,
      sessionId,
      pathId,
      stepId,
      idempotencyKey: "interview-memory-candidate-1",
    })).resolves.toEqual({ memoryId, replayed: false });

    expect(authFetchMock.mock.calls[0]?.[0]).toBe(`/api/v1/review/interviews/${sessionId}/learning-paths/${pathId}/steps/${stepId}/memory-candidate`);
    const init = authFetchMock.mock.calls[0]?.[1];
    expect(new Headers(init?.headers).get("Idempotency-Key")).toBe("interview-memory-candidate-1");
    if (typeof init?.body !== "string") throw new Error("expected JSON request body");
    expect(JSON.parse(init.body)).toEqual({ workspace_id: workspaceId });
  });

  it("允许恢复已经作答的追问题快照", () => {
    const answeredPrimary = { ...question, status: "ANSWERED", answered_at: createdAt };
    const answeredFollowUp = {
      ...question,
      id: followUpQuestionId,
      follow_up_no: 1,
      parent_question_id: questionId,
      prompt: "How does cancellation propagate?",
      status: "ANSWERED",
      answered_at: createdAt,
    };
    const primaryTurn = {
      ...turn,
      decision: { follow_up_created: true, follow_up_question_id: followUpQuestionId, next_question_id: followUpQuestionId },
    };
    const followUpTurn = {
      ...turn,
      id: followUpTurnId,
      question_id: followUpQuestionId,
      decision: { follow_up_created: false },
    };

    expect(decodeInterviewSnapshot({
      session: { ...session(), follow_up_count: 1, version: 3 },
      questions: [answeredPrimary, answeredFollowUp],
      turns: [primaryTurn, followUpTurn],
      steps: [],
    })).toMatchObject({ questions: [{ status: "ANSWERED" }, { status: "ANSWERED" }], turns: [{ questionId }, { questionId: followUpQuestionId }] });
  });
});
