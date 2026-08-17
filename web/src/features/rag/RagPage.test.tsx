import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClientProvider } from "@tanstack/react-query";
import { useState } from "react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { Answer, Turn, WorkspaceAnalysisTimeline as WorkspaceAnalysisTimelineValue } from "../../api/conversation";
import { createQueryClient } from "../../app/query-client";
import { renderWithAppProviders } from "../../test/render";
import { AnswerPublication, CitationInspector, mergeLatestTurn, RagPage } from "./RagPage";
import { type AnswerDraftState } from "./answer-draft";
import { ragQueryKeys } from "./query-keys";
import { WorkspaceAnalysisTimeline } from "./WorkspaceAnalysisTimeline";

const pageState = vi.hoisted(() => ({
  workspaceId: "92000000-0000-4000-8000-000000000001",
  turns: [] as Turn[],
  questionMutate: vi.fn(),
  timelineCalls: [] as unknown[][],
  timelineData: undefined as WorkspaceAnalysisTimelineValue | undefined,
  timelineError: false,
  timelineRefetch: vi.fn(),
}));

vi.mock("../../app/active-workspace", () => ({ useActiveWorkspaceId: () => pageState.workspaceId }));
vi.mock("./queries", () => ({
  useConversationList: () => ({ data: { pages: [{ items: [] }] }, isPending: false, isError: false, hasNextPage: false }),
  useConversation: () => ({ data: { status: "open" }, isError: false }),
  useConversationTurns: () => ({ data: { pages: [{ items: pageState.turns }] }, isPending: false, isError: false, hasNextPage: false }),
  useLatestTurn: () => ({ data: null, refetch: vi.fn() }),
  useAnswer: () => ({ data: undefined }),
  useWorkspaceAnalysisTimeline: (...args: unknown[]) => {
    pageState.timelineCalls.push(args);
    return { data: pageState.timelineData, isError: pageState.timelineError, refetch: pageState.timelineRefetch };
  },
}));
vi.mock("./commands", () => ({
  createCommandId: () => "command-id",
  useCreateConversationCommand: () => ({ mutate: vi.fn(), isPending: false, isError: false }),
  useSubmitFeedbackCommand: () => ({ mutate: vi.fn(), isPending: false, isError: false }),
  useSubmitQuestionCommand: () => ({ mutate: pageState.questionMutate, isPending: false, isError: false }),
  useCancelWorkspaceAnalysisCommand: () => ({ mutate: vi.fn(), isPending: false, isSuccess: false, isError: false }),
}));

const workspaceId = "92000000-0000-4000-8000-000000000001";
const conversationId = "92000000-0000-4000-8000-000000000002";
const questionId = "92000000-0000-4000-8000-000000000003";
const answerId = "92000000-0000-4000-8000-000000000004";
const runId = "92000000-0000-4000-8000-000000000005";
const modelRunId = "92000000-0000-4000-8000-000000000006";
const citationId = "citation-1";

const base = {
  id: answerId, workspaceId, conversationId, questionId,
  currentStage: "retrieval.started" as const,
  workflow: { runId, status: "running" as const, version: 2, updatedAt: "2026-07-20T02:00:00Z", statusUrl: `/api/v1/workflows/${runId}` },
  version: 1, createdAt: "2026-07-20T02:00:00Z", updatedAt: "2026-07-20T02:00:00Z",
};

const summary = {
  rewrites: ["RAG 发布规则"], requestedMode: "hybrid" as const, effectiveMode: "keyword" as const,
  scope: { sourceIds: [], sourceVersionIds: [], pathPrefixes: [], capturedAtFrom: null, capturedAtBefore: null, allowOriginalSources: false, allowWeb: false },
  indexVersionId: null, embeddingVersionId: null, candidateCount: 3, selectedCount: 1, conflictCount: 1,
  degradations: [{ capability: "vector" as const, code: "EMBEDDING_UNAVAILABLE", retryable: true }],
};

const workspaceAnalysisAnswer = (overrides: Partial<Answer> = {}): Answer => ({
  ...base,
  currentStage: null,
  workflow: { ...base.workflow, status: "succeeded", version: 4 },
  publicationStatus: "completed",
  resultType: "workspace_analysis",
  assistantText: "已完成工作区分析",
  citations: [{ id: citationId, workspaceId, indexVersionId: runId, chunkId: questionId, sourceVersionId: modelRunId, sourceSpanId: conversationId, href: `/api/v1/source-spans/${conversationId}` }],
  retrievalSummary: null,
  result: {
    resultType: "workspace_analysis",
    schemaId: "conversation.workspace_analysis_answer",
    schemaVersion: "v1",
    modelRunRef: modelRunId,
    payload: {
      answerMarkdown: "已完成工作区分析",
      citations: [{ id: citationId, workspaceId, indexVersionId: runId, chunkId: questionId, sourceVersionId: modelRunId, sourceSpanId: conversationId }],
      gitStatus: { branch: "main", head: "a".repeat(64), clean: false, stagedCount: 1, unstagedCount: 2, untrackedCount: 0, conflictCount: 0 },
      budget: { modelCalls: 3, toolCalls: 4, inputTokens: 128, outputTokens: 64, estimatedCostMicrounits: null },
      proposalSuggestion: { summary: "建议通过既有提案流程处理", citationIds: [citationId], href: "/proposals" },
      terminationReason: "COMPLETED",
    },
  },
  ...overrides,
} as Answer);

const renderPage = () => render(
  <QueryClientProvider client={createQueryClient()}>
    <MemoryRouter initialEntries={[`/chat/${conversationId}`]}>
      <Routes><Route path="/chat/:conversationId" element={<RagPage />} /></Routes>
    </MemoryRouter>
  </QueryClientProvider>,
);

beforeEach(() => {
  pageState.turns = [];
  pageState.questionMutate.mockReset();
  pageState.timelineCalls = [];
  pageState.timelineData = undefined;
  pageState.timelineError = false;
  pageState.timelineRefetch.mockReset();
});

describe("AnswerPublication", () => {
  it("pending 只展示权威阶段，不展示草稿回答", () => {
    const answer: Answer = { ...base, publicationStatus: "pending", citations: [], retrievalSummary: null };
    renderWithAppProviders(<AnswerPublication answer={answer} onCitation={vi.fn()} />);
    expect(screen.getByText("正在检索证据")).toBeInTheDocument();
    expect(screen.getByText("工作流 · 运行中")).toBeInTheDocument();
    expect(screen.queryByText("已校验回答")).not.toBeInTheDocument();
  });

  it("pending 展示明确标记的纯文本草稿，正式 REST Answer 到达后立即替换", () => {
    const pending: Answer = { ...base, publicationStatus: "pending", citations: [], retrievalSummary: null };
    const completed: Answer = {
      ...base,
      publicationStatus: "refused",
      resultType: "refusal",
      assistantText: "正式拒答",
      citations: [],
      retrievalSummary: summary,
      result: {
        resultType: "refusal",
        schemaId: "agent.refusal",
        schemaVersion: "v1",
        modelRunRef: modelRunId,
        payload: { reasonCode: "NO_RELEVANT_EVIDENCE", summary: "正式拒答", retrievalScope: "workspace", missingRequirements: [], suggestedActions: [] },
      },
    };
    const draft: AnswerDraftState = {
      answerId,
      generation: 2,
      sequence: 2,
      content: "<strong>未校验正文</strong>",
      bytes: 31,
      connectionState: "open",
    };
    const Harness = () => {
      const [answer, setAnswer] = useState<Answer>(pending);
      return <><button type="button" onClick={() => setAnswer(completed)}>发布正式结果</button><AnswerPublication answer={answer} draft={draft} onCitation={vi.fn()} /></>;
    };
    renderWithAppProviders(<Harness />);

    const draftRegion = screen.getByRole("article", { name: "生成中草稿" });
    expect(draftRegion).toHaveTextContent("<strong>未校验正文</strong>");
    expect(draftRegion).toHaveAttribute("data-draft-generation", "2");
    expect(draftRegion).toHaveAttribute("data-draft-sequence", "2");
    expect(draftRegion.querySelector("strong")).toBeNull();
    expect(draftRegion.querySelector(".rag-answer__copy")).not.toHaveAttribute("aria-live");

    fireEvent.click(screen.getByRole("button", { name: "发布正式结果" }));
    expect(screen.queryByRole("article", { name: "生成中草稿" })).not.toBeInTheDocument();
    expect(screen.getByText("正式拒答")).toBeInTheDocument();
  });

  it("completed 披露推断、冲突、退化并使用服务端 citation href", () => {
    const onCitation = vi.fn();
    const answer: Answer = {
      ...base, publicationStatus: "completed", resultType: "rag_answer", assistantText: "最终回答",
      citations: [{ id: citationId, workspaceId, indexVersionId: runId, chunkId: questionId, sourceVersionId: modelRunId, sourceSpanId: conversationId, href: `/api/v1/source-spans/${conversationId}` }],
      retrievalSummary: summary,
      result: { resultType: "rag_answer", schemaId: "agent.rag-answer", schemaVersion: "v2", modelRunRef: modelRunId, payload: {
        conclusion: "最终回答", citations: [{ id: citationId, workspaceId, indexVersionId: runId, chunkId: questionId, sourceVersionId: modelRunId, sourceSpanId: conversationId }],
        assertions: [{ id: "a1", text: "推断", kind: "MODEL_INFERENCE", citationIds: [] }],
        conflictPositions: [{ claimId: questionId, position: "旧流程仍适用", applicability: { version: "v1" }, citationIds: [citationId], updatedAt: "2026-07-20T02:00:00Z" }],
        conflictSummary: "版本差异", relatedTopics: [{ topicId: runId, name: "发布门禁", citationIds: [citationId] }], followUpQuestions: ["如何回滚？"],
      } },
    };
    renderWithAppProviders(<AnswerPublication answer={answer} onCitation={onCitation} />);
    expect(screen.getByText("最终回答")).toBeInTheDocument();
    expect(screen.getByText(/模型推断/)).toBeInTheDocument();
    expect(screen.getByText("旧流程仍适用")).toBeInTheDocument();
    expect(screen.getByText(/退化：vector/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: /打开段落证据/ }));
    expect(onCitation).toHaveBeenCalledWith(answer.citations[0]);
  });

  it("clarification 只引导继续提问，不提供必然失败的反馈", () => {
    const onPrompt = vi.fn();
    const answer: Answer = { ...base, publicationStatus: "clarification_required", resultType: "clarification", assistantText: "请补充版本", citations: [], retrievalSummary: summary,
      result: { resultType: "clarification", schemaId: "conversation.clarification", schemaVersion: "v1", modelRunRef: modelRunId, payload: { reason: "缺少版本", question: "你指哪个版本？", suggestedScopes: ["仅查询 v2"] } } };
    renderWithAppProviders(<AnswerPublication answer={answer} onCitation={vi.fn()} onPrompt={onPrompt} />);
    expect(screen.queryByRole("button", { name: "提交反馈" })).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "仅查询 v2" }));
    expect(onPrompt).toHaveBeenCalledWith("仅查询 v2");
  });

  it("工作区分析成功只给出提案入口，不发起 Proposal 创建", () => {
    const fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);
    const answer = workspaceAnalysisAnswer();
    renderWithAppProviders(<AnswerPublication answer={answer} onCitation={vi.fn()} />);

    expect(screen.getByText("已完成工作区分析")).toBeInTheDocument();
    expect(screen.getByText("main")).toBeInTheDocument();
    const proposalLink = screen.getByRole("link", { name: "查看提案" });
    expect(proposalLink).toHaveAttribute("href", "/proposals");
    fireEvent.click(proposalLink);
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("工作区分析拒答、澄清与终止都展示各自的稳定终态", () => {
    const refusal: Answer = {
      ...workspaceAnalysisAnswer(), publicationStatus: "refused", resultType: "workspace_analysis_refusal", assistantText: "证据不足，无法确认变更范围", citations: [],
      result: { resultType: "workspace_analysis_refusal", schemaId: "conversation.workspace_analysis_refusal", schemaVersion: "v1", modelRunRef: null, payload: { reasonCode: "WORKSPACE_ANALYSIS_EVIDENCE_INSUFFICIENT", summary: "证据不足，无法确认变更范围" } },
    } as Answer;
    const clarification: Answer = {
      ...workspaceAnalysisAnswer(), publicationStatus: "clarification_required", resultType: "clarification", assistantText: "请说明要检查的发布版本", citations: [], retrievalSummary: null,
      result: { resultType: "clarification", schemaId: "conversation.clarification", schemaVersion: "v1", modelRunRef: modelRunId, payload: { reason: "缺少版本范围", question: "请说明要检查的发布版本", suggestedScopes: ["检查 v2 发布流程"] } },
    };
    const termination: Answer = {
      ...workspaceAnalysisAnswer(), publicationStatus: "cancelled", resultType: "workspace_analysis_termination", assistantText: "用户已停止分析", citations: [],
      result: { resultType: "workspace_analysis_termination", schemaId: "conversation.workspace_analysis_termination", schemaVersion: "v1", modelRunRef: null, payload: { terminationReason: "WORKSPACE_ANALYSIS_CANCELLED", summary: "用户已停止分析" } },
    } as Answer;

    const { rerender } = renderWithAppProviders(<AnswerPublication answer={refusal} onCitation={vi.fn()} />);
    expect(screen.getByText("现有证据不足以形成可靠结论")).toBeInTheDocument();
    expect(screen.getByText("证据不足，无法确认变更范围")).toBeInTheDocument();
    rerender(<QueryClientProvider client={createQueryClient()}><MemoryRouter><AnswerPublication answer={clarification} onCitation={vi.fn()} /></MemoryRouter></QueryClientProvider>);
    expect(screen.getByText("请说明要检查的发布版本")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "检查 v2 发布流程" })).toBeInTheDocument();
    rerender(<QueryClientProvider client={createQueryClient()}><MemoryRouter><AnswerPublication answer={termination} onCitation={vi.fn()} /></MemoryRouter></QueryClientProvider>);
    expect(screen.getByText("分析已取消")).toBeInTheDocument();
    expect(screen.getByText("WORKSPACE_ANALYSIS_CANCELLED")).toBeInTheDocument();
    const runtimeFailure: Extract<Answer, { publicationStatus: "failed" }> = {
      ...workspaceAnalysisAnswer(),
      publicationStatus: "failed",
      resultType: "workspace_analysis_termination",
      assistantText: "运行时失败，已安全终止分析",
      citations: [],
      retrievalSummary: null,
      result: {
        resultType: "workspace_analysis_termination",
        schemaId: "conversation.workspace_analysis_termination",
        schemaVersion: "v1",
        modelRunRef: null,
        payload: {
          terminationReason: "WORKSPACE_ANALYSIS_RUNTIME_FAILED",
          summary: "运行时失败，已安全终止分析",
        },
      },
    };
    rerender(<QueryClientProvider client={createQueryClient()}><MemoryRouter><AnswerPublication answer={runtimeFailure} onCitation={vi.fn()} /></MemoryRouter></QueryClientProvider>);
    expect(screen.getByText("分析未完成")).toBeInTheDocument();
    expect(screen.getByText("WORKSPACE_ANALYSIS_RUNTIME_FAILED")).toBeInTheDocument();
  });
});

describe("RagPage workspace analysis composer", () => {
  it("切换模式后拒绝不支持的 Scope，清除后才允许按工作区分析提交", () => {
    renderPage();
    fireEvent.change(screen.getByLabelText("问题"), { target: { value: "检查当前变更" } });
    fireEvent.change(screen.getByLabelText("来源 ID（逗号分隔）"), { target: { value: questionId } });
    fireEvent.click(screen.getByRole("radio", { name: "工作区分析" }));

    expect(screen.getByRole("radio", { name: "工作区分析" })).toHaveAttribute("aria-checked", "true");
    expect(screen.getByRole("alert")).toHaveTextContent("当前范围不支持工作区分析");
    expect(screen.getByRole("button", { name: "提交问题" })).toBeDisabled();
    fireEvent.click(screen.getByRole("button", { name: "清除范围限制" }));
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "提交问题" })).toBeEnabled();

    fireEvent.click(screen.getByRole("button", { name: "提交问题" }));
    expect(pageState.questionMutate).toHaveBeenCalledWith(expect.objectContaining({ mode: "workspace_analysis" }), expect.anything());
  });

  it("模式分段控件支持 Radio Group 方向键", () => {
    renderPage();
    const rag = screen.getByRole("radio", { name: "证据问答" });
    const analysis = screen.getByRole("radio", { name: "工作区分析" });
    rag.focus();

    fireEvent.keyDown(rag, { key: "ArrowRight" });
    expect(analysis).toHaveAttribute("aria-checked", "true");
    expect(analysis).toHaveFocus();
    expect(analysis).toHaveAttribute("tabindex", "0");
    expect(rag).toHaveAttribute("tabindex", "-1");

    fireEvent.keyDown(analysis, { key: "ArrowLeft" });
    expect(rag).toHaveAttribute("aria-checked", "true");
    expect(rag).toHaveFocus();
  });

  it("只为最新 pending 工作区分析自动轮询", () => {
    const pendingAnswer = (id: string, linkedQuestionId: string): Answer => ({
      ...base,
      id,
      questionId: linkedQuestionId,
      publicationStatus: "pending",
      currentStage: null,
      citations: [],
      retrievalSummary: null,
    });
    const pendingTurn = (ordinal: number, linkedQuestionId: string, linkedAnswerId: string): Turn => ({
      question: {
        id: linkedQuestionId,
        workspaceId,
        conversationId,
        mode: "workspace_analysis",
        ordinal,
        contextThroughOrdinal: ordinal - 1,
        question: `检查 ${String(ordinal)}`,
        scope: { retrievalMode: "hybrid", sourceIds: [], sourceVersionIds: [], pathPrefixes: [], capturedAtFrom: null, capturedAtBefore: null, allowOriginalSources: false, allowWeb: false },
        answerDepth: "standard",
        outputFormat: "markdown",
        createdAt: "2026-07-20T02:00:00Z",
      },
      answer: pendingAnswer(linkedAnswerId, linkedQuestionId),
    });
    const olderAnswerId = "92000000-0000-4000-8000-000000000007";
    const latestAnswerId = "92000000-0000-4000-8000-000000000008";
    pageState.turns = [
      pendingTurn(1, "92000000-0000-4000-8000-000000000009", olderAnswerId),
      pendingTurn(2, "92000000-0000-4000-8000-00000000000a", latestAnswerId),
    ];

    renderPage();

    const olderCalls = pageState.timelineCalls.filter((call) => call[1] === olderAnswerId);
    const latestCalls = pageState.timelineCalls.filter((call) => call[1] === latestAnswerId);
    expect(olderCalls.length).toBeGreaterThan(0);
    expect(olderCalls.every((call) => call[2] === false && call[3] === false)).toBe(true);
    expect(latestCalls.length).toBeGreaterThan(0);
    expect(latestCalls.every((call) => call[2] === true && call[3] === true)).toBe(true);

    fireEvent.click(screen.getByRole("button", { name: "查看当前进度" }));
    expect(pageState.timelineCalls.filter((call) => call[1] === olderAnswerId).at(-1)?.slice(2)).toEqual([true, true]);
  });
});

describe("WorkspaceAnalysisTimeline", () => {
  it("看到终态快照后立即失效正式 Answer 与 Turns", async () => {
    const queryClient = createQueryClient();
    const invalidate = vi.spyOn(queryClient, "invalidateQueries");
    pageState.timelineData = {
      schemaId: "conversation.workspace_analysis_timeline",
      schemaVersion: "v1",
      workspaceId,
      answerId,
      analysisRunId: "92000000-0000-4000-8000-00000000000e",
      runStatus: "failed",
      terminationReason: "WORKSPACE_ANALYSIS_TOOL_FAILED",
      items: [],
      budget: {
        modelCalls: { used: 0, max: 3 }, toolCalls: { used: 0, max: 6 }, sourceReads: { used: 0, max: 3 },
        inputTokens: { used: 0, max: 196_608 }, outputTokens: { used: 0, max: 5_376 }, estimatedCostMicrounits: null,
      },
      latestServerEventSequence: 4,
    };
    const pending: Answer = { ...base, publicationStatus: "pending", currentStage: null, citations: [], retrievalSummary: null };
    render(<QueryClientProvider client={queryClient}><WorkspaceAnalysisTimeline workspaceId={workspaceId} conversationId={conversationId} answer={pending} autoRefresh /></QueryClientProvider>);

    await waitFor(() => expect(invalidate).toHaveBeenCalledWith(
      { queryKey: ragQueryKeys.answer(workspaceId, answerId), exact: true },
      { throwOnError: true },
    ));
    expect(invalidate).toHaveBeenCalledWith(
      { queryKey: ragQueryKeys.turns(workspaceId, conversationId) },
      { throwOnError: true },
    );
  });

  it("正式 Answer 终态后刷新已展开的权威 Timeline", async () => {
    const queryClient = createQueryClient();
    const invalidate = vi.spyOn(queryClient, "invalidateQueries");
    pageState.timelineData = {
      schemaId: "conversation.workspace_analysis_timeline",
      schemaVersion: "v1",
      workspaceId,
      answerId,
      analysisRunId: "92000000-0000-4000-8000-00000000000e",
      runStatus: "running",
      terminationReason: null,
      items: [],
      budget: {
        modelCalls: { used: 1, max: 3 }, toolCalls: { used: 1, max: 6 }, sourceReads: { used: 0, max: 3 },
        inputTokens: { used: 16, max: 196_608 }, outputTokens: { used: 8, max: 5_376 }, estimatedCostMicrounits: null,
      },
      latestServerEventSequence: 2,
    };
    render(<QueryClientProvider client={queryClient}><WorkspaceAnalysisTimeline workspaceId={workspaceId} conversationId={conversationId} answer={workspaceAnalysisAnswer()} autoRefresh={false} /></QueryClientProvider>);

    fireEvent.click(screen.getByRole("button", { name: "查看分析过程" }));

    await waitFor(() => expect(invalidate).toHaveBeenCalledWith({
      queryKey: ragQueryKeys.analysisTimeline(workspaceId, answerId),
      exact: true,
    }, { throwOnError: true }));
  });

  it("终态 Answer 与 Turns 同步失败后可重试，成功后不重复失效", async () => {
    const queryClient = createQueryClient();
    const invalidate = vi.spyOn(queryClient, "invalidateQueries").mockRejectedValueOnce(new Error("answer refresh failed"));
    pageState.timelineData = {
      schemaId: "conversation.workspace_analysis_timeline",
      schemaVersion: "v1",
      workspaceId,
      answerId,
      analysisRunId: "92000000-0000-4000-8000-00000000000e",
      runStatus: "failed",
      terminationReason: "WORKSPACE_ANALYSIS_TOOL_FAILED",
      items: [],
      budget: {
        modelCalls: { used: 0, max: 3 }, toolCalls: { used: 0, max: 6 }, sourceReads: { used: 0, max: 3 },
        inputTokens: { used: 0, max: 196_608 }, outputTokens: { used: 0, max: 5_376 }, estimatedCostMicrounits: null,
      },
      latestServerEventSequence: 4,
    };
    const pending: Answer = { ...base, publicationStatus: "pending", currentStage: null, citations: [], retrievalSummary: null };
    const view = render(<QueryClientProvider client={queryClient}><WorkspaceAnalysisTimeline workspaceId={workspaceId} conversationId={conversationId} answer={pending} autoRefresh /></QueryClientProvider>);

    await waitFor(() => expect(invalidate).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(screen.getByRole("button", { name: "重试读取进度" })).toBeInTheDocument());
    fireEvent.click(screen.getByRole("button", { name: "重试读取进度" }));

    await waitFor(() => expect(invalidate).toHaveBeenCalledTimes(4));
    pageState.timelineData = { ...pageState.timelineData };
    view.rerender(<QueryClientProvider client={queryClient}><WorkspaceAnalysisTimeline workspaceId={workspaceId} conversationId={conversationId} answer={pending} autoRefresh /></QueryClientProvider>);
    expect(invalidate).toHaveBeenCalledTimes(4);
  });

  it("终态 Answer 的 Timeline 同步失败后可重试，成功后只失效一次", async () => {
    const queryClient = createQueryClient();
    const invalidate = vi.spyOn(queryClient, "invalidateQueries").mockRejectedValueOnce(new Error("timeline refresh failed"));
    pageState.timelineData = {
      schemaId: "conversation.workspace_analysis_timeline",
      schemaVersion: "v1",
      workspaceId,
      answerId,
      analysisRunId: "92000000-0000-4000-8000-00000000000e",
      runStatus: "running",
      terminationReason: null,
      items: [],
      budget: {
        modelCalls: { used: 1, max: 3 }, toolCalls: { used: 1, max: 6 }, sourceReads: { used: 0, max: 3 },
        inputTokens: { used: 16, max: 196_608 }, outputTokens: { used: 8, max: 5_376 }, estimatedCostMicrounits: null,
      },
      latestServerEventSequence: 2,
    };
    render(<QueryClientProvider client={queryClient}><WorkspaceAnalysisTimeline workspaceId={workspaceId} conversationId={conversationId} answer={workspaceAnalysisAnswer()} autoRefresh={false} /></QueryClientProvider>);

    fireEvent.click(screen.getByRole("button", { name: "查看分析过程" }));
    await waitFor(() => expect(invalidate).toHaveBeenCalledTimes(1));
    await waitFor(() => expect(screen.getByRole("button", { name: "重试读取进度" })).toBeInTheDocument());
    fireEvent.click(screen.getByRole("button", { name: "重试读取进度" }));

    await waitFor(() => expect(invalidate).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(screen.queryByRole("button", { name: "重试读取进度" })).not.toBeInTheDocument());
    expect(invalidate).toHaveBeenCalledTimes(2);
  });

  it("正式 Answer 的 Timeline 同步失败后允许显式重试", () => {
    pageState.timelineError = true;
    render(<QueryClientProvider client={createQueryClient()}><WorkspaceAnalysisTimeline workspaceId={workspaceId} conversationId={conversationId} answer={workspaceAnalysisAnswer()} autoRefresh /></QueryClientProvider>);

    fireEvent.click(screen.getByRole("button", { name: "重试读取进度" }));

    expect(pageState.timelineRefetch).toHaveBeenCalledTimes(1);
  });
});

describe("CitationInspector", () => {
  it("使用应用内受控读取按钮，不渲染绕过 authFetch 的原始 Span 链接", () => {
    renderWithAppProviders(<CitationInspector citation={{
      id: citationId,
      workspaceId,
      indexVersionId: runId,
      chunkId: questionId,
      sourceVersionId: modelRunId,
      sourceSpanId: conversationId,
      href: `/api/v1/workspaces/${workspaceId}/source-versions/${modelRunId}/spans/${conversationId}`,
    }} onClose={vi.fn()} />);

    expect(screen.getByRole("button", { name: "打开段落证据" })).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "打开段落证据" })).not.toBeInTheDocument();
  });
});

describe("mergeLatestTurn", () => {
  it("用 latest 权威投影替换同 Question 的旧 pending Answer", () => {
    const pending: Answer = { ...base, publicationStatus: "pending", citations: [], retrievalSummary: null };
    const completed = { ...pending, publicationStatus: "refused", resultType: "refusal", assistantText: "证据不足", retrievalSummary: summary,
      result: { resultType: "refusal", schemaId: "agent.refusal", schemaVersion: "v1", modelRunRef: modelRunId, payload: { reasonCode: "NO_RELEVANT_EVIDENCE", summary: "证据不足", retrievalScope: "workspace", missingRequirements: [], suggestedActions: [] } },
    } as Answer;
    const question = { id: questionId, workspaceId, conversationId, mode: "rag" as const, ordinal: 1, contextThroughOrdinal: 0, question: "问题", scope: { retrievalMode: "hybrid" as const, sourceIds: [], sourceVersionIds: [], pathPrefixes: [], capturedAtFrom: null, capturedAtBefore: null, allowOriginalSources: false, allowWeb: false }, answerDepth: "standard" as const, outputFormat: "markdown" as const, createdAt: base.createdAt };
    expect(mergeLatestTurn([{ question, answer: pending }], { question, answer: completed })[0]?.answer.publicationStatus).toBe("refused");
    expect(mergeLatestTurn([{ question, answer: completed }], { question, answer: pending })[0]?.answer.publicationStatus).toBe("refused");
  });

  it("pending stage 只能单调前进，不能被并发旧响应回退", () => {
    const question = { id: questionId, workspaceId, conversationId, mode: "rag" as const, ordinal: 1, contextThroughOrdinal: 0, question: "问题", scope: { retrievalMode: "hybrid" as const, sourceIds: [], sourceVersionIds: [], pathPrefixes: [], capturedAtFrom: null, capturedAtBefore: null, allowOriginalSources: false, allowWeb: false }, answerDepth: "standard" as const, outputFormat: "markdown" as const, createdAt: base.createdAt };
    const advanced: Answer = { ...base, currentStage: "validation.started", publicationStatus: "pending", citations: [], retrievalSummary: null };
    const stale: Answer = { ...base, currentStage: "plan.completed", publicationStatus: "pending", citations: [], retrievalSummary: null };
    expect(mergeLatestTurn([{ question, answer: advanced }], { question, answer: stale })[0]?.answer.currentStage).toBe("validation.started");
  });
});
