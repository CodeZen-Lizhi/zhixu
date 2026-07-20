import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import type { PropsWithChildren } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { CreateConversationInput, SubmitFeedbackInput, SubmitQuestionInput } from "../../api/conversation";

const api = vi.hoisted(() => ({
  createConversation: vi.fn(),
  submitQuestion: vi.fn(),
  submitFeedback: vi.fn(),
}));

vi.mock("../../api/conversation", () => api);

import {
  useCreateConversationCommand,
  useSubmitFeedbackCommand,
  useSubmitQuestionCommand,
} from "./commands";

const workspaceId = "92000000-0000-4000-8000-000000000001";
const conversationId = "92000000-0000-4000-8000-000000000002";
const answerId = "92000000-0000-4000-8000-000000000003";

const wrapper = ({ children }: PropsWithChildren) => (
  <QueryClientProvider client={new QueryClient({ defaultOptions: { mutations: { retry: false } } })}>
    {children}
  </QueryClientProvider>
);

describe("RAG commands", () => {
  beforeEach(() => vi.clearAllMocks());

  it("create 重放保持调用方提供的幂等键", async () => {
    const input: CreateConversationInput = { workspaceId, idempotencyKey: "create-fixed" };
    api.createConversation.mockResolvedValue({ resource: null, etag: null, notModified: true });
    const { result } = renderHook(useCreateConversationCommand, { wrapper });
    act(() => result.current.mutate(input));
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    act(() => result.current.mutate(input));
    await waitFor(() => expect(api.createConversation).toHaveBeenCalledTimes(2));
    expect(api.createConversation).toHaveBeenNthCalledWith(1, input);
    expect(api.createConversation).toHaveBeenNthCalledWith(2, input);
  });

  it("question 与 feedback 重放不在 hook 内改写幂等键", async () => {
    const questionInput: SubmitQuestionInput = { workspaceId, conversationId, idempotencyKey: "question-fixed", question: "如何恢复？" };
    const feedbackInput: SubmitFeedbackInput = { workspaceId, answerId, idempotencyKey: "feedback-fixed", feedbackType: "helpful" };
    api.submitQuestion.mockRejectedValue(new Error("response lost"));
    api.submitFeedback.mockRejectedValue(new Error("response lost"));
    const question = renderHook(useSubmitQuestionCommand, { wrapper });
    const feedback = renderHook(useSubmitFeedbackCommand, { wrapper });
    act(() => question.result.current.mutate(questionInput));
    act(() => feedback.result.current.mutate(feedbackInput));
    await waitFor(() => expect(question.result.current.isError && feedback.result.current.isError).toBe(true));
    act(() => question.result.current.mutate(questionInput));
    act(() => feedback.result.current.mutate(feedbackInput));
    await waitFor(() => expect(api.submitQuestion).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(api.submitFeedback).toHaveBeenCalledTimes(2));
    expect(api.submitQuestion).toHaveBeenNthCalledWith(1, questionInput);
    expect(api.submitQuestion).toHaveBeenNthCalledWith(2, questionInput);
    expect(api.submitFeedback).toHaveBeenNthCalledWith(1, feedbackInput);
    expect(api.submitFeedback).toHaveBeenNthCalledWith(2, feedbackInput);
  });
});
