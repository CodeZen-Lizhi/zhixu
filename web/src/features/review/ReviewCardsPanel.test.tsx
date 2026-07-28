import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { type PropsWithChildren } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { CreateReviewCardInput, EditReviewCardInput, ReviewCard, ReviewCardDecisionInput, ReviewCardPage, ReviewDeck } from "../../api/review";

const workspaceId = "72000000-0000-4000-8000-000000000001";
const deckId = "72000000-0000-4000-8000-000000000002";
const cardId = "72000000-0000-4000-8000-000000000003";
const claimId = "72000000-0000-4000-8000-000000000004";
const sourceVersionId = "72000000-0000-4000-8000-000000000005";
const sourceSpanId = "72000000-0000-4000-8000-000000000006";
const evidenceHash = "a".repeat(64);
const createdAt = "2026-07-27T08:00:00Z";

vi.mock("../../app/active-workspace", () => ({ useActiveWorkspaceId: () => workspaceId }));

const apiMocks = vi.hoisted(() => ({
  listReviewCards: vi.fn<(workspaceId: string, deckId: string, signal?: AbortSignal) => Promise<ReviewCardPage>>(),
  createReviewCard: vi.fn<(input: CreateReviewCardInput) => Promise<ReviewCard>>(),
  editReviewCard: vi.fn<(input: EditReviewCardInput) => Promise<ReviewCard>>(),
  approveReviewCard: vi.fn<(input: ReviewCardDecisionInput) => Promise<ReviewCard>>(),
  rejectReviewCard: vi.fn<(input: ReviewCardDecisionInput) => Promise<ReviewCard>>(),
  listReviewDecks: vi.fn(),
  listReviewDue: vi.fn(),
  createReviewDeck: vi.fn(),
  pauseReviewDeck: vi.fn(),
  resumeReviewDeck: vi.fn(),
  resetReviewDeck: vi.fn(),
  startReviewSession: vi.fn(),
  completeReviewSession: vi.fn(),
  submitReviewAnswer: vi.fn(),
}));

vi.mock("../../api/review", () => ({
  ...apiMocks,
  ReviewApiError: class ReviewApiError extends Error {
    readonly retryable = false;
  },
}));

import { ReviewCardsPanel } from "./ReviewCardsPanel";

const deck: ReviewDeck = {
  id: deckId,
  workspaceId,
  name: "Go 并发",
  scope: {},
  status: "ACTIVE",
  dailyLimit: 20,
  schedulerVersion: "fsrs/v1",
  version: 1,
  createdAt,
  updatedAt: createdAt,
};

const card: ReviewCard = {
  id: cardId,
  workspaceId,
  deckId,
  claimId,
  question: "channel close 的 happens-before 语义是什么？",
  answerPoints: ["close 先于零值接收"],
  evidence: [{ schemaVersion: "review-evidence/v1", claimId, sourceVersionId, sourceSpanId, evidenceHash }],
  cardType: "SHORT_ANSWER",
  difficulty: 0.5,
  status: "DRAFT",
  fingerprint: "b".repeat(64),
  modelVersion: "manual",
  version: 1,
  createdAt,
  updatedAt: createdAt,
};

const renderPanel = () => {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  const Wrapper = ({ children }: PropsWithChildren) => <QueryClientProvider client={client}>{children}</QueryClientProvider>;
  return render(<ReviewCardsPanel decks={[deck]} />, { wrapper: Wrapper });
};

describe("ReviewCardsPanel", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    apiMocks.listReviewCards.mockResolvedValue({ workspaceId, deckId, items: [card] });
  });

  it("从服务端列表恢复 Card，并用当前版本提交审批", async () => {
    apiMocks.approveReviewCard.mockResolvedValue({ ...card, status: "APPROVED", version: 2 });
    renderPanel();

    expect(await screen.findByText(card.question)).toBeVisible();
    fireEvent.click(screen.getByRole("button", { name: "审批" }));

    await waitFor(() => expect(apiMocks.approveReviewCard).toHaveBeenCalledTimes(1));
    const input = apiMocks.approveReviewCard.mock.calls[0]?.[0];
    expect(input).toMatchObject({
      workspaceId,
      cardId,
      expectedVersion: 1,
      reason: "",
    });
    expect(input?.idempotencyKey).toMatch(/^review-card-approve-/);
  });

  it("创建 Card 时只提交正式 Claim、Evidence 与编辑内容", async () => {
    apiMocks.listReviewCards.mockResolvedValue({ workspaceId, deckId, items: [] });
    apiMocks.createReviewCard.mockResolvedValue(card);
    renderPanel();

    await screen.findByText("这个 Deck 还没有 Card");
    const createButtons = screen.getAllByRole("button", { name: "新建 Card" });
    expect(createButtons).not.toHaveLength(0);
    fireEvent.click(createButtons[0] as HTMLButtonElement);
    expect(screen.queryByLabelText("Evidence 摘要（可选）")).not.toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Claim ID"), { target: { value: claimId } });
    fireEvent.change(screen.getByLabelText("问题"), { target: { value: card.question } });
    fireEvent.change(screen.getByLabelText("答案要点（每行一项）"), { target: { value: "要点一\n要点二" } });
    fireEvent.change(screen.getByLabelText("Source Version ID"), { target: { value: sourceVersionId } });
    fireEvent.change(screen.getByLabelText("Source Span ID"), { target: { value: sourceSpanId } });
    fireEvent.change(screen.getByLabelText("Evidence Hash"), { target: { value: evidenceHash } });
    fireEvent.click(screen.getByRole("button", { name: "创建 DRAFT" }));

    await waitFor(() => expect(apiMocks.createReviewCard).toHaveBeenCalledTimes(1));
    const input = apiMocks.createReviewCard.mock.calls[0]?.[0];
    expect(input).toMatchObject({
      workspaceId,
      deckId,
      claimId,
      answerPoints: ["要点一", "要点二"],
    });
    expect(input?.evidence).toEqual([{ schemaVersion: "review-evidence/v1", claimId, sourceVersionId, sourceSpanId, evidenceHash }]);
    expect(input?.idempotencyKey).toMatch(/^review-card-create-/);
  });

  it("编辑多 Evidence Card 时保留未修改的 Evidence", async () => {
    const secondEvidence = {
      schemaVersion: "review-evidence/v1" as const,
      claimId,
      sourceVersionId: "72000000-0000-4000-8000-000000000007",
      sourceSpanId: "72000000-0000-4000-8000-000000000008",
      evidenceHash: "c".repeat(64),
    };
    const multiEvidenceCard = { ...card, evidence: [...card.evidence, secondEvidence] };
    apiMocks.listReviewCards.mockResolvedValue({ workspaceId, deckId, items: [multiEvidenceCard] });
    apiMocks.editReviewCard.mockResolvedValue({ ...multiEvidenceCard, question: "更新后的问题", difficulty: 0.65, version: 2 });
    renderPanel();

    expect(await screen.findByText(card.question)).toBeVisible();
    fireEvent.click(screen.getByRole("button", { name: "编辑" }));
    fireEvent.change(screen.getByLabelText("问题"), { target: { value: "更新后的问题" } });
    fireEvent.change(screen.getByLabelText("难度"), { target: { value: "0.65" } });
    fireEvent.click(screen.getByRole("button", { name: "保存并重新校验证据" }));

    await waitFor(() => expect(apiMocks.editReviewCard).toHaveBeenCalledTimes(1));
    const input = apiMocks.editReviewCard.mock.calls[0]?.[0];
    expect(input).toMatchObject({
      workspaceId,
      deckId,
      cardId,
      expectedVersion: 1,
      question: "更新后的问题",
      difficulty: 0.65,
    });
    expect(input?.evidence).toEqual(multiEvidenceCard.evidence);
    expect(input?.idempotencyKey).toMatch(/^review-card-edit-/);
  });
});
