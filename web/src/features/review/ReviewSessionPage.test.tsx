import { fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { describe, expect, it, vi } from "vitest";

import { ReviewApiError, type ReviewAnswerResult, type ReviewDuePage } from "../../api/review";

const workspaceId = "72000000-0000-4000-8000-000000000001";
const deckId = "72000000-0000-4000-8000-000000000002";
const sessionId = "72000000-0000-4000-8000-000000000003";
const cardId = "72000000-0000-4000-8000-000000000004";
const questionRef = "review-question/v1.test-reference";
const sourceVersionId = "72000000-0000-4000-8000-000000000007";
const sourceSpanId = "72000000-0000-4000-8000-000000000008";

const mocks = vi.hoisted(() => {
	const values: Record<"due" | "submit" | "complete" | "learningPath" | "createLearningPath" | "pathStatus" | "stepStatus", Record<string, unknown>> = {
		due: {},
		submit: {},
		complete: {},
		learningPath: { data: undefined, isPending: false, isError: false, error: undefined, refetch: vi.fn() },
		createLearningPath: { isPending: false, mutate: vi.fn() },
		pathStatus: { isPending: false, mutate: vi.fn() },
		stepStatus: { isPending: false, mutate: vi.fn() },
	};
  return values;
});

vi.mock("../../app/active-workspace", () => ({ useActiveWorkspaceId: () => workspaceId }));
vi.mock("./queries", () => ({
	useReviewDue: () => mocks.due,
	useSubmitReviewAnswer: () => mocks.submit,
	useCompleteReviewSession: () => mocks.complete,
	useReviewLearningPath: () => mocks.learningPath,
	useCreateReviewLearningPath: () => mocks.createLearningPath,
	useUpdateReviewLearningPathStatus: () => mocks.pathStatus,
	useUpdateReviewLearningPathStep: () => mocks.stepStatus,
}));

import { ReviewSessionPage } from "./ReviewSessionPage";

const dueSchedule: ReviewDuePage["items"][number]["schedule"] = {
  cardId,
  workspaceId,
  dueAt: "2026-07-27T00:00:00Z",
  intervalDays: 0,
  stability: 0,
  difficulty: 0.5,
  schedulerVersion: "fsrs/v1",
  paused: false,
  version: 1,
};

const duePage: ReviewDuePage = {
  workspaceId,
  items: [{
    card: {
      id: cardId,
      workspaceId,
      deckId,
      question: "只展示的问题",
      cardType: "SHORT_ANSWER",
      difficulty: 0.5,
      status: "APPROVED",
      version: 1,
    },
    schedule: dueSchedule,
    questionRef,
  }],
};

const answerResult: ReviewAnswerResult = {
  answer: {
    id: "72000000-0000-4000-8000-000000000005",
    workspaceId,
    sessionId,
    cardId,
    questionRef,
    userAnswer: "我的回答",
    rating: 3,
    scorerVersion: "review-test-scorer/v1",
    score: {
      schemaVersion: "review-score/v1",
      correctness: { value: 1, rationale: "正确" },
      coverage: { value: 1, rationale: "完整" },
      boundaries: { value: 1, rationale: "边界清楚" },
      clarity: { value: 1, rationale: "表达清楚" },
      confidence: { value: 1, rationale: "自评一致" },
      errors: [],
      omissions: [],
      evidence: [{
        schemaVersion: "review-evidence/v1",
        claimId: "72000000-0000-4000-8000-000000000006",
        sourceVersionId,
        sourceSpanId,
        evidenceHash: "a".repeat(64),
        sourceVersionHref: "/api/v1/source-versions/72000000-0000-4000-8000-000000000007",
        sourceSpanHref: "/api/v1/source-versions/72000000-0000-4000-8000-000000000007/spans/72000000-0000-4000-8000-000000000008",
      }],
    },
    feedback: {},
    createdAt: "2026-07-27T00:00:00Z",
  },
  schedule: { ...dueSchedule, version: 2 },
  replayed: false,
};

const renderPage = () => render(<MemoryRouter initialEntries={[`/review/session?deck=${deckId}&session=${sessionId}`]}><ReviewSessionPage /></MemoryRouter>);

describe("ReviewSessionPage", () => {
  it("答题前只渲染脱敏题面，且可重试命令复用原 Idempotency-Key", () => {
    const mutate = vi.fn();
    const reset = vi.fn();
    mocks.due = { data: duePage, isPending: false, isError: false, isFetching: false, refetch: vi.fn() };
    mocks.submit = { isPending: false, isError: false, error: undefined, variables: undefined, mutate, reset };
    mocks.complete = { isPending: false, isError: false, error: undefined, variables: undefined, mutate: vi.fn() };
    const view = renderPage();

    expect(screen.getByText("只展示的问题")).toBeInTheDocument();
    expect(screen.queryByText("hidden answer")).not.toBeInTheDocument();
    expect(screen.queryByText("hidden evidence")).not.toBeInTheDocument();

    fireEvent.change(screen.getByLabelText("你的回答"), { target: { value: "我的回答" } });
    fireEvent.click(screen.getByRole("button", { name: "提交回答" }));
    const firstInput = mutate.mock.calls[0]?.[0] as { idempotencyKey: string };
    expect(firstInput).toMatchObject({ workspaceId, sessionId, cardId, questionRef, userAnswer: "我的回答", rating: 3 });

    mocks.submit = {
      isPending: false,
      isError: true,
      error: new ReviewApiError("NETWORK_ERROR", "NETWORK_ERROR", "lost response", true),
      variables: firstInput,
      mutate,
      reset,
    };
    view.rerender(<MemoryRouter initialEntries={[`/review/session?deck=${deckId}&session=${sessionId}`]}><ReviewSessionPage /></MemoryRouter>);
    fireEvent.click(screen.getByRole("button", { name: "重试原请求" }));

    expect(mutate.mock.calls[1]?.[0]).toBe(firstInput);
    expect((mutate.mock.calls[1]?.[0] as { idempotencyKey: string }).idempotencyKey).toBe(firstInput.idempotencyKey);
  });

  it("评分证据只通过应用内认证读取入口打开", () => {
    const mutate = vi.fn((_input, options: { onSuccess?: (result: ReviewAnswerResult) => void }) => options.onSuccess?.(answerResult));
    mocks.due = { data: duePage, isPending: false, isError: false, isFetching: false, refetch: vi.fn() };
    mocks.submit = { isPending: false, isError: false, error: undefined, variables: undefined, mutate, reset: vi.fn() };
    mocks.complete = { isPending: false, isError: false, error: undefined, variables: undefined, mutate: vi.fn() };
    const view = renderPage();

    fireEvent.change(screen.getByLabelText("你的回答"), { target: { value: "我的回答" } });
    fireEvent.click(screen.getByRole("button", { name: "提交回答" }));

    expect(screen.getByRole("link", { name: "来源版本" })).toHaveAttribute("href", `/documents/${sourceVersionId}`);
    expect(screen.getByRole("button", { name: "打开片段" })).toBeInTheDocument();
    expect(view.container.querySelector('a[href^="/api/v1/"]')).toBeNull();
  });
});
