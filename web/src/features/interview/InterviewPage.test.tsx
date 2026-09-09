import { fireEvent, render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { InterviewSnapshot, SuggestInterviewMemoryCandidateInput } from "../../api/interview";
import { decodeNoteQuestionSource } from "../../api/synthesis";
import { synthesisId, synthesisNoteRefFixture, synthesisSourceFixture } from "../../test/synthesis-fixtures";

const workspaceId = "71000000-0000-4000-8000-000000000001";
const sessionId = "71000000-0000-4000-8000-000000000002";
const questionId = "71000000-0000-4000-8000-000000000003";
const claimId = "71000000-0000-4000-8000-000000000004";
const pathId = "71000000-0000-4000-8000-000000000005";
const stepId = "71000000-0000-4000-8000-000000000006";
const reportId = "71000000-0000-4000-8000-000000000007";
const artifactId = "71000000-0000-4000-8000-000000000008";
const revisionId = "71000000-0000-4000-8000-000000000009";
const sourceVersionId = "71000000-0000-4000-8000-000000000010";
const sourceSpanId = "71000000-0000-4000-8000-000000000011";
const createdAt = "2026-07-27T00:00:00Z";
const learningStepVersion = 2;
const snapshot = {
  session: { id: sessionId, workspaceId, config: { schemaVersion: "interview/v1" as const, role: "Go engineer", scope: { claimIds: [claimId], topicIds: [] }, difficulty: "INTERMEDIATE" as const, durationMinutes: 30, questionCount: 1, maxFollowUps: 2 }, status: "ACTIVE", version: 1, followUpCount: 0 },
  questions: [{ id: questionId, workspaceId, sessionId, questionNo: 1, followUpNo: 0, claimId, prompt: "Explain context cancellation.", status: "PENDING" as const, createdAt }],
  turns: [], steps: [],
};
const completedSnapshot = {
  ...snapshot,
  session: { ...snapshot.session, status: "COMPLETED" as const, version: 5, endedAt: createdAt },
  questions: [{ ...snapshot.questions[0], status: "ANSWERED" as const, answeredAt: createdAt }],
  report: {
    id: reportId,
    workspaceId,
    sessionId,
    schemaVersion: "interview-report/v1" as const,
    summary: { questionsTotal: 1, answeredTotal: 1, skippedTotal: 0, correctness: 0.8, coverage: 0.7, boundaries: 0.6, clarity: 0.9 },
    strengths: [], gaps: [], expression: [], evidence: [],
    artifact: { kind: "INTERVIEW_DOC" as const, artifactId, revisionId, artifactVersion: 1 },
    createdAt,
  },
  path: {
    id: pathId,
    workspaceId,
    sessionId,
    reportId,
    artifact: { kind: "LEARNING_PATH" as const, artifactId, revisionId, artifactVersion: 1 },
    status: "ACTIVE" as const,
    version: 7,
    createdAt,
    updatedAt: createdAt,
  },
  steps: [{
    id: stepId,
    workspaceId,
    pathId,
    stepNo: 1,
    claimId,
    sourceVersionId,
    sourceSpanId,
    evidenceHash: "a".repeat(64),
    title: "Practice cancellation",
    rationale: "Cover the gap",
    status: "PENDING" as const,
    version: learningStepVersion,
    createdAt,
    updatedAt: createdAt,
  }],
};
let currentSnapshot: typeof snapshot | typeof completedSnapshot | InterviewSnapshot = snapshot;
const submit = vi.fn();
const updateStep = vi.fn();
const suggestCandidate = vi.fn<(input: SuggestInterviewMemoryCandidateInput, options: unknown) => void>();
const mutation = { isPending: false, isError: false, error: null, variables: undefined, mutate: submit };
const fetchNextPage = vi.fn();
const refetchSessions = vi.fn();
const sessionsQuery = {
  data: {
    pages: [{
      workspaceId,
      items: [{ ...snapshot.session, startedAt: createdAt }],
    }],
  },
  isPending: false,
  isError: false,
  error: null,
  isFetchNextPageError: false,
  isFetchingNextPage: false,
  hasNextPage: false,
  fetchNextPage,
  refetch: refetchSessions,
};

vi.mock("../../app/active-workspace", () => ({ useActiveWorkspaceId: () => workspaceId }));
vi.mock("./queries", () => ({
  useInterview: () => ({ data: currentSnapshot, isPending: false, isError: false, isFetching: false, refetch: vi.fn() }),
  useInterviewSessions: () => sessionsQuery,
  useStartInterview: () => mutation,
  useSubmitInterviewTurn: () => mutation,
  useCompleteInterview: () => mutation,
  useSuggestInterviewMemoryCandidate: () => ({ ...mutation, mutate: suggestCandidate }),
  useUpdateLearningPathStatus: () => mutation,
  useUpdateLearningPathStep: () => ({ ...mutation, mutate: updateStep }),
}));

import { InterviewSessionPage, InterviewsPage } from "./InterviewPage";

describe("InterviewsPage", () => {
  it("从服务端列表直接恢复最近会话", () => {
    render(<MemoryRouter initialEntries={["/interviews"]}><Routes><Route path="/interviews" element={<InterviewsPage />} /></Routes></MemoryRouter>);

    expect(screen.getByRole("heading", { name: "最近会话" })).toBeInTheDocument();
    expect(screen.getByText("Go engineer")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "继续面试" })).toHaveAttribute("href", `/interviews/${sessionId}`);
  });
});

describe("InterviewSessionPage", () => {
  beforeEach(() => {
    currentSnapshot = snapshot;
    submit.mockReset();
    updateStep.mockReset();
    suggestCandidate.mockReset();
  });

  it("作答前只显示题面，并提交学习者回答", () => {
    render(<MemoryRouter initialEntries={[`/interviews/${sessionId}`]}><Routes><Route path="/interviews/:sessionId" element={<InterviewSessionPage />} /></Routes></MemoryRouter>);

    expect(screen.getByText("Explain context cancellation.")).toBeInTheDocument();
    expect(screen.queryByText("hidden answer")).not.toBeInTheDocument();
    expect(screen.queryByText("hidden evidence")).not.toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("你的回答"), { target: { value: "It signals cancellation." } });
    fireEvent.click(screen.getByRole("button", { name: "提交回答" }));
    expect(submit).toHaveBeenCalledWith(expect.objectContaining({ workspaceId, sessionId, questionId, userAnswer: "It signals cancellation." }), expect.any(Object));
  });

  it("更新学习步骤时使用 Learning Path 版本做 CAS", () => {
    const pathVersion = completedSnapshot.path.version;
    currentSnapshot = completedSnapshot;
    render(<MemoryRouter initialEntries={[`/interviews/${sessionId}`]}><Routes><Route path="/interviews/:sessionId" element={<InterviewSessionPage />} /></Routes></MemoryRouter>);

    fireEvent.click(screen.getByRole("button", { name: "开始" }));

    expect(updateStep).toHaveBeenCalledWith(expect.objectContaining({
      workspaceId,
      pathId,
      stepId,
      expectedVersion: pathVersion,
      status: "IN_PROGRESS",
    }), expect.any(Object));
    expect(updateStep).not.toHaveBeenCalledWith(expect.objectContaining({ expectedVersion: learningStepVersion }), expect.any(Object));
  });

  it("只在用户点击学习步骤后创建待确认 Memory Candidate", () => {
    currentSnapshot = completedSnapshot;
    render(<MemoryRouter initialEntries={[`/interviews/${sessionId}`]}><Routes><Route path="/interviews/:sessionId" element={<InterviewSessionPage />} /></Routes></MemoryRouter>);

    expect(suggestCandidate).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "创建记忆候选" }));

    const input = suggestCandidate.mock.calls[0]?.[0];
    expect(input).toMatchObject({
      workspaceId,
      sessionId,
      pathId,
      stepId,
    });
    expect(input?.idempotencyKey).toMatch(/^interview-memory-candidate-/);
  });

  it("同一笔记条目的多条报告结论均保留且来源冻结，不使用重复行身份或正式证据入口", () => {
    const noteSource = decodeNoteQuestionSource({ revision: synthesisNoteRefFixture(workspaceId), item_id: synthesisId(11), item_kind: "FACT", sources: [synthesisSourceFixture(workspaceId)] }, workspaceId);
    const question = snapshot.questions[0];
    const step = completedSnapshot.steps[0];
    if (question === undefined || step === undefined) throw new Error("interview fixture missing");
    const detail = "Review the frozen note and its original sources.";
    const finding = { claimId: null, sourceKind: "NOTE_REVISION" as const, noteSource, detail, evidence: [] };
    currentSnapshot = {
      ...completedSnapshot,
      session: { ...completedSnapshot.session, startedAt: createdAt, config: { ...completedSnapshot.session.config, questionCount: 2, scope: { claimIds: [], topicIds: [], noteRevision: noteSource.revision } } },
      questions: [1, 2].map((number) => ({ ...question, id: synthesisId(40 + number), questionNo: number, claimId: null, sourceKind: "NOTE_REVISION" as const,
        noteItem: { revision: noteSource.revision, itemId: noteSource.itemId, itemKind: noteSource.itemKind }, status: "SKIPPED" as const })),
      report: { ...completedSnapshot.report, summary: { questionsTotal: 2, answeredTotal: 0, skippedTotal: 2, correctness: 0, coverage: 0, boundaries: 0, clarity: 0 }, gaps: [finding, finding], noteSources: [noteSource] },
      steps: [1, 2].map((number) => ({ ...step, id: synthesisId(50 + number), stepNo: number, claimId: null, sourceKind: "NOTE_REVISION" as const, noteSource,
        sourceVersionId: null, sourceSpanId: null, evidenceHash: null })),
    };
    const client = new QueryClient();
    const consoleErrors = vi.spyOn(console, "error");
    try {
      render(<QueryClientProvider client={client}><MemoryRouter initialEntries={[`/interviews/${sessionId}`]}><Routes><Route path="/interviews/:sessionId" element={<InterviewSessionPage />} /></Routes></MemoryRouter></QueryClientProvider>);
      expect(screen.getAllByText(detail)).toHaveLength(2);
      expect(screen.getByText(/评分使用确定性规则/)).toBeInTheDocument();
      const links = screen.getAllByRole("link", { name: "缓存失效策略 · 版本 1" });
      expect(links).toHaveLength(5);
      links.forEach((link) => expect(link).toHaveAttribute("href", `/authoring/notes/${noteSource.revision.noteId}?revision_id=${noteSource.revision.revisionId}`));
      expect(screen.queryByRole("button", { name: "打开步骤证据" })).not.toBeInTheDocument();
      expect(consoleErrors).not.toHaveBeenCalled();
    } finally {
      consoleErrors.mockRestore();
      client.clear();
    }
  });
});
