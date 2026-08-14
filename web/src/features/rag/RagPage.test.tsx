import { fireEvent, screen } from "@testing-library/react";
import { useState } from "react";
import { describe, expect, it, vi } from "vitest";

import type { Answer } from "../../api/conversation";
import { renderWithAppProviders } from "../../test/render";
import { AnswerPublication, CitationInspector, mergeLatestTurn } from "./RagPage";
import { type AnswerDraftState } from "./answer-draft";

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
    const question = { id: questionId, workspaceId, conversationId, ordinal: 1, contextThroughOrdinal: 0, question: "问题", scope: { retrievalMode: "hybrid" as const, sourceIds: [], sourceVersionIds: [], pathPrefixes: [], capturedAtFrom: null, capturedAtBefore: null, allowOriginalSources: false, allowWeb: false }, answerDepth: "standard" as const, outputFormat: "markdown" as const, createdAt: base.createdAt };
    expect(mergeLatestTurn([{ question, answer: pending }], { question, answer: completed })[0]?.answer.publicationStatus).toBe("refused");
    expect(mergeLatestTurn([{ question, answer: completed }], { question, answer: pending })[0]?.answer.publicationStatus).toBe("refused");
  });

  it("pending stage 只能单调前进，不能被并发旧响应回退", () => {
    const question = { id: questionId, workspaceId, conversationId, ordinal: 1, contextThroughOrdinal: 0, question: "问题", scope: { retrievalMode: "hybrid" as const, sourceIds: [], sourceVersionIds: [], pathPrefixes: [], capturedAtFrom: null, capturedAtBefore: null, allowOriginalSources: false, allowWeb: false }, answerDepth: "standard" as const, outputFormat: "markdown" as const, createdAt: base.createdAt };
    const advanced: Answer = { ...base, currentStage: "validation.started", publicationStatus: "pending", citations: [], retrievalSummary: null };
    const stale: Answer = { ...base, currentStage: "plan.completed", publicationStatus: "pending", citations: [], retrievalSummary: null };
    expect(mergeLatestTurn([{ question, answer: advanced }], { question, answer: stale })[0]?.answer.currentStage).toBe("validation.started");
  });
});
