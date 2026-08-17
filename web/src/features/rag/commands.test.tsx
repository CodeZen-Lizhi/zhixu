import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import type { PropsWithChildren } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { CreateConversationInput, SubmitFeedbackInput, SubmitQuestionInput } from "../../api/conversation";

const api = vi.hoisted(() => ({
  createConversation: vi.fn(),
  getAnswer: vi.fn(),
  submitQuestion: vi.fn(),
  submitFeedback: vi.fn(),
}));
const business = vi.hoisted(() => ({ controlWorkflow: vi.fn() }));

vi.mock("../../api/conversation", () => api);
vi.mock("../../api/business", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  controlWorkflow: business.controlWorkflow,
}));

import { BusinessApiError } from "../../api/business";

import {
  WORKSPACE_ANALYSIS_CANCEL_MAX_ATTEMPTS,
  useCreateConversationCommand,
  useCancelWorkspaceAnalysisCommand,
  useSubmitFeedbackCommand,
  useSubmitQuestionCommand,
} from "./commands";
import { ragQueryKeys } from "./query-keys";

const workspaceId = "92000000-0000-4000-8000-000000000001";
const conversationId = "92000000-0000-4000-8000-000000000002";
const answerId = "92000000-0000-4000-8000-000000000003";
const workflowRunId = "92000000-0000-4000-8000-000000000004";
const otherWorkspaceId = "92000000-0000-4000-8000-000000000099";
const otherAnswerId = "92000000-0000-4000-8000-000000000098";
const otherConversationId = "92000000-0000-4000-8000-000000000097";
const otherWorkflowRunId = "92000000-0000-4000-8000-000000000096";

const workflowVersionConflict = () => new BusinessApiError(
  "HTTP_ERROR",
  "server-owned version conflict",
  false,
  409,
  undefined,
  { errorCode: "WORKFLOW_VERSION_CONFLICT" },
);

const pendingAnswer = (overrides: Record<string, unknown> = {}) => ({
  id: answerId,
  workspaceId,
  conversationId,
  publicationStatus: "pending",
  workflow: { runId: workflowRunId, status: "running", version: 8 },
  ...overrides,
});

const pendingAnswerAtVersion = (version: number) => pendingAnswer({
  workflow: { runId: workflowRunId, status: "running", version },
});

const wrapper = ({ children }: PropsWithChildren) => (
  <QueryClientProvider client={new QueryClient({ defaultOptions: { mutations: { retry: false } } })}>
    {children}
  </QueryClientProvider>
);

const expectCancelVersions = (versions: readonly number[]): void => {
  for (const [index, version] of versions.entries()) {
    expect(business.controlWorkflow).toHaveBeenNthCalledWith(index + 1, workspaceId, workflowRunId, "cancel", version);
  }
};

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

  it("停止工作区分析复用 Workflow cancel 的版本检查，并只失效该轮权威事实", async () => {
    const queryClient = new QueryClient({ defaultOptions: { mutations: { retry: false } } });
    const Wrapper = ({ children }: PropsWithChildren) => <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>;
    const timelineKey = ragQueryKeys.analysisTimeline(workspaceId, answerId);
    const answerKey = ragQueryKeys.answer(workspaceId, answerId);
    const turnsKey = ragQueryKeys.turns(workspaceId, conversationId);
    const otherAnswerKey = ragQueryKeys.answer(otherWorkspaceId, answerId);
    queryClient.setQueryData(timelineKey, { answerId });
    queryClient.setQueryData(answerKey, { id: answerId });
    queryClient.setQueryData(turnsKey, { items: [] });
    queryClient.setQueryData(otherAnswerKey, { id: answerId });
    business.controlWorkflow.mockResolvedValue({ workflowRunId, status: "running", version: 8, cancelRequested: true });

    const { result } = renderHook(useCancelWorkspaceAnalysisCommand, { wrapper: Wrapper });
    await act(async () => {
      await result.current.mutateAsync({
        workspaceId,
        conversationId,
        answerId,
        workflowRunId,
        expectedVersion: 7,
      });
    });

    expect(business.controlWorkflow).toHaveBeenCalledWith(workspaceId, workflowRunId, "cancel", 7);
    await waitFor(() => {
      expect(queryClient.getQueryState(answerKey)?.isInvalidated).toBe(true);
      expect(queryClient.getQueryState(timelineKey)?.isInvalidated).toBe(true);
      expect(queryClient.getQueryState(turnsKey)?.isInvalidated).toBe(true);
    });
    expect(queryClient.getQueryState(otherAnswerKey)?.isInvalidated).toBe(false);
  });

  it("成功响应未确认 cancel_requested 时 fail closed", async () => {
    business.controlWorkflow.mockResolvedValue({
      workflowRunId,
      status: "running",
      version: 8,
      cancelRequested: false,
    });

    const { result } = renderHook(useCancelWorkspaceAnalysisCommand, { wrapper });
    await expect(result.current.mutateAsync({ workspaceId, conversationId, answerId, workflowRunId, expectedVersion: 7 }))
      .rejects.toMatchObject({ code: "INVALID_RESPONSE" });

    expect(api.getAnswer).not.toHaveBeenCalled();
    expect(business.controlWorkflow).toHaveBeenCalledTimes(1);
  });

  it("只在精确 Workflow 版本冲突时重取权威 Answer 并重试", async () => {
    business.controlWorkflow
      .mockRejectedValueOnce(workflowVersionConflict())
      .mockResolvedValueOnce({ workflowRunId, status: "running", version: 9, cancelRequested: true });
    api.getAnswer.mockResolvedValue({ resource: pendingAnswer(), etag: null, notModified: false });

    const { result } = renderHook(useCancelWorkspaceAnalysisCommand, { wrapper });
    await act(async () => {
      await result.current.mutateAsync({ workspaceId, conversationId, answerId, workflowRunId, expectedVersion: 7 });
    });

    expect(api.getAnswer).toHaveBeenCalledTimes(1);
    expect(api.getAnswer).toHaveBeenCalledWith({ workspaceId, id: answerId });
    expect(business.controlWorkflow).toHaveBeenNthCalledWith(1, workspaceId, workflowRunId, "cancel", 7);
    expect(business.controlWorkflow).toHaveBeenNthCalledWith(2, workspaceId, workflowRunId, "cancel", 8);
  });

  it("连续两个版本冲突后按逐轮权威版本成功取消", async () => {
    const firstConflict = workflowVersionConflict();
    const secondConflict = workflowVersionConflict();
    business.controlWorkflow
      .mockRejectedValueOnce(firstConflict)
      .mockRejectedValueOnce(secondConflict)
      .mockResolvedValueOnce({ workflowRunId, status: "running", version: 10, cancelRequested: true });
    api.getAnswer
      .mockResolvedValueOnce({ resource: pendingAnswerAtVersion(8), etag: null, notModified: false })
      .mockResolvedValueOnce({ resource: pendingAnswerAtVersion(9), etag: null, notModified: false });

    const { result } = renderHook(useCancelWorkspaceAnalysisCommand, { wrapper });
    await result.current.mutateAsync({ workspaceId, conversationId, answerId, workflowRunId, expectedVersion: 7 });

    expect(api.getAnswer).toHaveBeenCalledTimes(2);
    expect(business.controlWorkflow).toHaveBeenCalledTimes(3);
    expect(business.controlWorkflow).toHaveBeenNthCalledWith(1, workspaceId, workflowRunId, "cancel", 7);
    expect(business.controlWorkflow).toHaveBeenNthCalledWith(2, workspaceId, workflowRunId, "cancel", 8);
    expect(business.controlWorkflow).toHaveBeenNthCalledWith(3, workspaceId, workflowRunId, "cancel", 9);
  });

  it("允许恰好四次权威版本恢复后在第五次 cancel 成功", async () => {
    expect(WORKSPACE_ANALYSIS_CANCEL_MAX_ATTEMPTS).toBe(5);
    for (let index = 0; index < WORKSPACE_ANALYSIS_CANCEL_MAX_ATTEMPTS - 1; index += 1) {
      business.controlWorkflow.mockRejectedValueOnce(workflowVersionConflict());
      api.getAnswer.mockResolvedValueOnce({
        resource: pendingAnswerAtVersion(8 + index),
        etag: null,
        notModified: false,
      });
    }
    business.controlWorkflow.mockResolvedValueOnce({ workflowRunId, status: "running", version: 12, cancelRequested: true });

    const { result } = renderHook(useCancelWorkspaceAnalysisCommand, { wrapper });
    await result.current.mutateAsync({ workspaceId, conversationId, answerId, workflowRunId, expectedVersion: 7 });

    expect(api.getAnswer).toHaveBeenCalledTimes(WORKSPACE_ANALYSIS_CANCEL_MAX_ATTEMPTS - 1);
    expect(business.controlWorkflow).toHaveBeenCalledTimes(WORKSPACE_ANALYSIS_CANCEL_MAX_ATTEMPTS);
    expectCancelVersions([7, 8, 9, 10, 11]);
  });

  it("第五次版本冲突耗尽上限后不再刷新或发送第六次 cancel", async () => {
    const conflicts = Array.from({ length: WORKSPACE_ANALYSIS_CANCEL_MAX_ATTEMPTS }, () => workflowVersionConflict());
    for (const conflict of conflicts) business.controlWorkflow.mockRejectedValueOnce(conflict);
    for (let index = 0; index < WORKSPACE_ANALYSIS_CANCEL_MAX_ATTEMPTS - 1; index += 1) {
      api.getAnswer.mockResolvedValueOnce({
        resource: pendingAnswerAtVersion(8 + index),
        etag: null,
        notModified: false,
      });
    }

    const { result } = renderHook(useCancelWorkspaceAnalysisCommand, { wrapper });
    await expect(result.current.mutateAsync({ workspaceId, conversationId, answerId, workflowRunId, expectedVersion: 7 }))
      .rejects.toBe(conflicts[WORKSPACE_ANALYSIS_CANCEL_MAX_ATTEMPTS - 1]);

    expect(api.getAnswer).toHaveBeenCalledTimes(WORKSPACE_ANALYSIS_CANCEL_MAX_ATTEMPTS - 1);
    expect(business.controlWorkflow).toHaveBeenCalledTimes(WORKSPACE_ANALYSIS_CANCEL_MAX_ATTEMPTS);
    expectCancelVersions([7, 8, 9, 10, 11]);
  });

  it("后续 cancel 返回控制冲突时立即停止", async () => {
    const controlConflict = new BusinessApiError(
      "HTTP_ERROR",
      "control conflict",
      false,
      409,
      undefined,
      { errorCode: "WORKFLOW_CONTROL_CONFLICT" },
    );
    business.controlWorkflow
      .mockRejectedValueOnce(workflowVersionConflict())
      .mockRejectedValueOnce(controlConflict);
    api.getAnswer.mockResolvedValueOnce({ resource: pendingAnswerAtVersion(8), etag: null, notModified: false });

    const { result } = renderHook(useCancelWorkspaceAnalysisCommand, { wrapper });
    await expect(result.current.mutateAsync({ workspaceId, conversationId, answerId, workflowRunId, expectedVersion: 7 }))
      .rejects.toBe(controlConflict);

    expect(api.getAnswer).toHaveBeenCalledTimes(1);
    expect(business.controlWorkflow).toHaveBeenCalledTimes(2);
  });

  it("非版本冲突不刷新 Answer 或重试 cancel", async () => {
    const error = new BusinessApiError(
      "HTTP_ERROR",
      "control conflict",
      false,
      409,
      undefined,
      { errorCode: "WORKFLOW_CONTROL_CONFLICT" },
    );
    business.controlWorkflow.mockRejectedValue(error);

    const { result } = renderHook(useCancelWorkspaceAnalysisCommand, { wrapper });
    await expect(result.current.mutateAsync({ workspaceId, conversationId, answerId, workflowRunId, expectedVersion: 7 })).rejects.toBe(error);

    expect(api.getAnswer).not.toHaveBeenCalled();
    expect(business.controlWorkflow).toHaveBeenCalledTimes(1);
  });

  it.each([
    ["网络错误", new BusinessApiError("NETWORK_ERROR", "network unavailable", true)],
    ["无合法 Problem code 的 409", new BusinessApiError("HTTP_ERROR", "invalid problem", false, 409)],
    ["未知 409", new BusinessApiError(
      "HTTP_ERROR",
      "unknown conflict",
      false,
      409,
      undefined,
      { errorCode: "WORKFLOW_OTHER_CONFLICT" },
    )],
    ["错误状态的版本码", new BusinessApiError(
      "HTTP_ERROR",
      "server error",
      false,
      500,
      undefined,
      { errorCode: "WORKFLOW_VERSION_CONFLICT" },
    )],
  ])("%s 不触发版本恢复", async (_name, error) => {
    business.controlWorkflow.mockRejectedValue(error);

    const { result } = renderHook(useCancelWorkspaceAnalysisCommand, { wrapper });
    await expect(result.current.mutateAsync({ workspaceId, conversationId, answerId, workflowRunId, expectedVersion: 7 }))
      .rejects.toBe(error);

    expect(api.getAnswer).not.toHaveBeenCalled();
    expect(business.controlWorkflow).toHaveBeenCalledTimes(1);
  });

  it("权威 Answer 刷新失败时不再发送 cancel", async () => {
    const conflict = workflowVersionConflict();
    const refreshError = new BusinessApiError("NETWORK_ERROR", "answer unavailable", true);
    business.controlWorkflow.mockRejectedValueOnce(conflict);
    api.getAnswer.mockRejectedValueOnce(refreshError);

    const { result } = renderHook(useCancelWorkspaceAnalysisCommand, { wrapper });
    await expect(result.current.mutateAsync({ workspaceId, conversationId, answerId, workflowRunId, expectedVersion: 7 }))
      .rejects.toBe(refreshError);

    expect(api.getAnswer).toHaveBeenCalledTimes(1);
    expect(business.controlWorkflow).toHaveBeenCalledTimes(1);
  });

  it.each([
    ["相等", 8],
    ["倒退", 7],
  ])("后续权威版本%s时立即停止", async (_name, secondRefreshedVersion) => {
    const firstConflict = workflowVersionConflict();
    const secondConflict = workflowVersionConflict();
    business.controlWorkflow
      .mockRejectedValueOnce(firstConflict)
      .mockRejectedValueOnce(secondConflict);
    api.getAnswer
      .mockResolvedValueOnce({ resource: pendingAnswerAtVersion(8), etag: null, notModified: false })
      .mockResolvedValueOnce({ resource: pendingAnswerAtVersion(secondRefreshedVersion), etag: null, notModified: false });

    const { result } = renderHook(useCancelWorkspaceAnalysisCommand, { wrapper });
    await expect(result.current.mutateAsync({ workspaceId, conversationId, answerId, workflowRunId, expectedVersion: 7 }))
      .rejects.toBe(secondConflict);

    expect(api.getAnswer).toHaveBeenCalledTimes(2);
    expect(business.controlWorkflow).toHaveBeenCalledTimes(2);
    expect(business.controlWorkflow).toHaveBeenNthCalledWith(2, workspaceId, workflowRunId, "cancel", 8);
  });

  it.each([
    ["workspace 漂移", { workspaceId: otherWorkspaceId }],
    ["answer 漂移", { id: otherAnswerId }],
    ["conversation 漂移", { conversationId: otherConversationId }],
    ["Workflow Run 漂移", { workflow: { runId: otherWorkflowRunId, status: "running", version: 8 } }],
    ["Answer 已终态", { publicationStatus: "cancelled" }],
    ["Workflow 已终态", { workflow: { runId: workflowRunId, status: "succeeded", version: 8 } }],
    ["权威版本未推进", { workflow: { runId: workflowRunId, status: "running", version: 7 } }],
  ])("刷新后%s时 fail closed", async (_name, overrides) => {
    const conflict = workflowVersionConflict();
    business.controlWorkflow.mockRejectedValue(conflict);
    api.getAnswer.mockResolvedValue({ resource: pendingAnswer(overrides), etag: null, notModified: false });

    const { result } = renderHook(useCancelWorkspaceAnalysisCommand, { wrapper });
    await expect(result.current.mutateAsync({ workspaceId, conversationId, answerId, workflowRunId, expectedVersion: 7 })).rejects.toBe(conflict);

    expect(api.getAnswer).toHaveBeenCalledTimes(1);
    expect(business.controlWorkflow).toHaveBeenCalledTimes(1);
  });

  it("刷新后 Answer 不存在时 fail closed", async () => {
    const conflict = workflowVersionConflict();
    business.controlWorkflow.mockRejectedValue(conflict);
    api.getAnswer.mockResolvedValue({ resource: null, etag: null, notModified: true });

    const { result } = renderHook(useCancelWorkspaceAnalysisCommand, { wrapper });
    await expect(result.current.mutateAsync({ workspaceId, conversationId, answerId, workflowRunId, expectedVersion: 7 })).rejects.toBe(conflict);

    expect(business.controlWorkflow).toHaveBeenCalledTimes(1);
  });
});
