import { afterEach, describe, expect, it, vi } from "vitest";

import {
  ConversationApiError, createConversation, decodeAnswer, decodeConversationPage, decodeProblem,
  decodeQuestionAcceptance, decodeTurnPage, decodeWorkspaceAnalysisTimeline, getAnswer, getLatestTurn, getWorkspaceAnalysisTimeline, listConversations, submitFeedback, submitQuestion,
} from "./conversation";

const workspaceId = "92000000-0000-4000-8000-000000000001";
const conversationId = "92000000-0000-4000-8000-000000000002";
const questionId = "92000000-0000-4000-8000-000000000003";
const answerId = "92000000-0000-4000-8000-000000000004";
const workflowId = "92000000-0000-4000-8000-000000000005";
const modelRunId = "92000000-0000-4000-8000-000000000006";
const indexId = "92000000-0000-4000-8000-000000000007";
const chunkId = "92000000-0000-4000-8000-000000000008";
const sourceVersionId = "92000000-0000-4000-8000-000000000009";
const spanId = "92000000-0000-4000-8000-00000000000a";
const topicId = "92000000-0000-4000-8000-00000000000b";
const claimId1 = "92000000-0000-4000-8000-00000000000c";
const claimId2 = "92000000-0000-4000-8000-00000000000d";
const at = "2026-07-20T08:10:12.123456789Z";
const citationId = "citation-1";

const scope = {
  retrieval_mode: "hybrid", source_ids: [], source_version_ids: [], path_prefixes: [],
  captured_at_from: null, captured_at_before: null, allow_original_sources: false, allow_web: false,
};
const retrievalScope = {
  source_ids: [], source_version_ids: [], path_prefixes: [], captured_at_from: null, captured_at_before: null,
  allow_original_sources: false, allow_web: false,
};
const conversation = {
  id: conversationId, workspace_id: workspaceId, status: "open", title: null, version: 1,
  last_activity_at: at, created_at: at, updated_at: at, archived_at: null,
};
const question = {
  id: questionId, workspace_id: workspaceId, conversation_id: conversationId, ordinal: 1,
  mode: "rag", context_through_ordinal: 0, question: "如何恢复？", scope, answer_depth: "standard", output_format: "markdown", created_at: at,
};
const workflow = { run_id: workflowId, status: "succeeded", version: 3, updated_at: at, status_url: `/api/v1/workflows/${workflowId}` };
const identity = { id: citationId, workspace_id: workspaceId, index_version_id: indexId, chunk_id: chunkId, source_version_id: sourceVersionId, source_span_id: spanId };
const href = `/api/v1/workspaces/${workspaceId}/source-versions/${sourceVersionId}/spans/${spanId}`;
const summary = {
  rewrites: ["恢复"], requested_mode: "hybrid", effective_mode: "keyword", scope: retrievalScope,
  index_version_id: indexId, embedding_version_id: null, candidate_count: 3, selected_count: 1, conflict_count: 0,
  degradations: [{ capability: "vector", code: "RETRIEVAL_VECTOR_UNAVAILABLE", retryable: false }],
};
const ragResult = {
  result_type: "rag_answer", schema_id: "agent.rag-answer", schema_version: "v2", model_run_ref: modelRunId,
  payload: {
    conclusion: "使用恢复命令。",
    assertions: [{ id: "assertion-1", text: "可恢复。", kind: "FACTUAL", citation_ids: [citationId] }],
    citations: [identity], conflict_positions: [], conflict_summary: "",
    related_topics: [{ topic_id: topicId, name: "恢复", citation_ids: [citationId] }], follow_up_questions: ["如何验证？"],
  },
};
const completedAnswer = {
  id: answerId, workspace_id: workspaceId, conversation_id: conversationId, question_id: questionId,
  publication_status: "completed", current_stage: null, result_type: "rag_answer", assistant_text: "使用恢复命令。", result: ragResult,
  citations: [{ ...identity, href }], retrieval_summary: summary, workflow, version: 2, created_at: at, updated_at: at,
};
const pendingAnswer = {
  id: answerId, workspace_id: workspaceId, conversation_id: conversationId, question_id: questionId,
  publication_status: "pending", current_stage: "plan.started", citations: [], retrieval_summary: null,
  workflow: { ...workflow, status: "running" }, version: 1, created_at: at, updated_at: at,
};
const workspaceAnalysisResult = {
  result_type: "workspace_analysis", schema_id: "conversation.workspace_analysis_answer", schema_version: "v1", model_run_ref: modelRunId,
  payload: {
    answer_markdown: "工作区分析已完成。", citations: [identity],
    git_status: { branch: "main", head: "a".repeat(40), clean: true, staged_count: 0, unstaged_count: 0, untracked_count: 0, conflict_count: 0 },
    budget: { model_calls: 3, tool_calls: 4, input_tokens: 100, output_tokens: 300, estimated_cost_microunits: null },
    proposal_suggestion: { summary: "查看现有提案", citation_ids: [citationId], href: "/proposals" }, termination_reason: "COMPLETED",
  },
};
const workspaceAnalysisAnswer = {
  id: answerId, workspace_id: workspaceId, conversation_id: conversationId, question_id: questionId,
  publication_status: "completed", current_stage: null, result_type: "workspace_analysis", assistant_text: "工作区分析已完成。", result: workspaceAnalysisResult,
  citations: [{ ...identity, href }], retrieval_summary: null, workflow, version: 2, created_at: at, updated_at: at,
};
const workspaceAnalysisTimeline = {
  schema_id: "conversation.workspace_analysis_timeline", schema_version: "v1", workspace_id: workspaceId, answer_id: answerId,
  analysis_run_id: "92000000-0000-4000-8000-00000000000e", run_status: "queued", termination_reason: null, items: [],
  budget: { model_calls: { used: 0, max: 3 }, tool_calls: { used: 0, max: 6 }, source_reads: { used: 0, max: 3 }, input_tokens: { used: 0, max: 196608 }, output_tokens: { used: 0, max: 5376 }, estimated_cost_microunits: null }, latest_server_event_sequence: 0,
};
const successfulWorkspaceAnalysisTimeline = {
  ...workspaceAnalysisTimeline,
  run_status: "succeeded",
  termination_reason: "COMPLETED",
  items: [
    { sequence: 1, kind: "tool", phase: "inspect_workspace", status: "succeeded", tool_ref: { name: "ReadGitStatus", version: 2 }, duration_ms: 1, error_code: null, summary: { kind: "git", git: workspaceAnalysisResult.payload.git_status } },
    { sequence: 2, kind: "model", phase: "retrieve_evidence", status: "succeeded", tool_ref: null, duration_ms: 1, error_code: null, summary: { kind: "model_usage", model_usage: { input_tokens: 20, output_tokens: 20 } } },
    { sequence: 3, kind: "tool", phase: "retrieve_evidence", status: "succeeded", tool_ref: { name: "SearchKnowledge", version: 2 }, duration_ms: 1, error_code: null, summary: { kind: "search", search: { hit_count: 1, degradation_codes: [] } } },
    { sequence: 4, kind: "tool", phase: "read_evidence", status: "succeeded", tool_ref: { name: "ReadSource", version: 3 }, duration_ms: 1, error_code: null, summary: { kind: "source", source: { evidence_ref: "E1", content_hash: "b".repeat(64), truncated: false } } },
    { sequence: 5, kind: "model", phase: "synthesize_answer", status: "succeeded", tool_ref: null, duration_ms: 1, error_code: null, summary: { kind: "model_usage", model_usage: { input_tokens: 30, output_tokens: 30 } } },
    { sequence: 6, kind: "tool", phase: "validate_citations", status: "succeeded", tool_ref: { name: "ValidateCitation", version: 3 }, duration_ms: 1, error_code: null, summary: { kind: "citation_validation", citation_validation: { valid_count: 1, invalid_count: 0, reason_codes: ["OK"] } } },
    { sequence: 7, kind: "model", phase: "review_publish", status: "succeeded", tool_ref: null, duration_ms: 1, error_code: null, summary: { kind: "model_usage", model_usage: { input_tokens: 40, output_tokens: 40 } } },
  ],
  budget: { ...workspaceAnalysisTimeline.budget, model_calls: { used: 3, max: 3 }, tool_calls: { used: 4, max: 6 }, source_reads: { used: 1, max: 3 }, input_tokens: { used: 90, max: 196608 }, output_tokens: { used: 90, max: 5376 } },
};

afterEach(() => vi.unstubAllGlobals());

describe("Conversation response decoders", () => {
  it("严格解码 Conversation page、Turn 与完整 RAG v2", () => {
    expect(decodeConversationPage({ items: [conversation], next_cursor: "opaque.cursor" })).toMatchObject({ items: [{ id: conversationId, status: "open" }], nextCursor: "opaque.cursor" });
    expect(decodeTurnPage({ items: [{ question, answer: completedAnswer }] }).items[0]).toMatchObject({
      question: { answerDepth: "standard" },
      answer: { publicationStatus: "completed", result: { resultType: "rag_answer", payload: { relatedTopics: [{ topicId }], followUpQuestions: ["如何验证？"] } }, retrievalSummary: { effectiveMode: "keyword" } },
    });
    expect(decodeQuestionAcceptance({ question, answer: pendingAnswer, status_url: `/api/v1/answers/${answerId}?workspace_id=${workspaceId}` }).answer.publicationStatus).toBe("pending");
  });

  it("Conversation title 按实际 wire 接受最多 512 UTF-8 bytes", () => {
    expect(decodeConversationPage({ items: [{ ...conversation, title: "x".repeat(512) }] }).items[0]?.title).toHaveLength(512);
    expect(() => decodeConversationPage({ items: [{ ...conversation, title: "x".repeat(513) }] })).toThrow(ConversationApiError);
  });

  it("解码 Refusal 与 Clarification 的结构化结果", () => {
    const refusal = decodeAnswer({ ...completedAnswer, publication_status: "refused", result_type: "refusal", assistant_text: "证据不足", citations: [], result: {
      result_type: "refusal", schema_id: "agent.refusal", schema_version: "v1", model_run_ref: modelRunId,
      payload: { reason_code: "NO_RELEVANT_EVIDENCE", summary: "证据不足", retrieval_scope: "工作区", missing_requirements: [], suggested_actions: ["导入来源"] },
    } });
    expect(refusal.publicationStatus).toBe("refused");
    if (refusal.publicationStatus !== "refused") throw new Error("expected refused answer");
    expect(refusal.result).toMatchObject({ resultType: "refusal", payload: { reasonCode: "NO_RELEVANT_EVIDENCE" } });
    const clarification = decodeAnswer({ ...completedAnswer, publication_status: "clarification_required", result_type: "clarification", assistant_text: "请说明范围", citations: [], retrieval_summary: {
      ...summary, rewrites: [], requested_mode: "hybrid", effective_mode: "hybrid", index_version_id: null,
      candidate_count: 0, selected_count: 0, conflict_count: 0, degradations: [],
    }, result: {
      result_type: "clarification", schema_id: "conversation.clarification", schema_version: "v1", model_run_ref: modelRunId,
      payload: { reason: "范围不明", question: "请说明范围", suggested_scopes: ["当前版本"] },
    } });
    expect(clarification.publicationStatus).toBe("clarification_required");
    if (clarification.publicationStatus !== "clarification_required") throw new Error("expected clarification answer");
    expect(clarification.result).toMatchObject({ resultType: "clarification", payload: { suggestedScopes: ["当前版本"] } });
  });

  it("严格解码 Workspace Analysis 成功、拒答、失败和取消终态", () => {
    const completed = decodeAnswer(workspaceAnalysisAnswer);
    expect(completed).toMatchObject({ publicationStatus: "completed", resultType: "workspace_analysis", result: { payload: { proposalSuggestion: { href: "/proposals" } } } });
    const refusal = decodeAnswer({ ...workspaceAnalysisAnswer, publication_status: "refused", result_type: "workspace_analysis_refusal", assistant_text: "证据不足", citations: [], result: {
      result_type: "workspace_analysis_refusal", schema_id: "conversation.workspace_analysis_refusal", schema_version: "v1", model_run_ref: null,
      payload: { reason_code: "WORKSPACE_ANALYSIS_EVIDENCE_INSUFFICIENT", summary: "证据不足" },
    } });
    expect(refusal).toMatchObject({ publicationStatus: "refused", resultType: "workspace_analysis_refusal" });
    const clarification = decodeAnswer({ ...workspaceAnalysisAnswer, publication_status: "clarification_required", result_type: "clarification", assistant_text: "请补充范围", citations: [], retrieval_summary: null, result: {
      result_type: "clarification", schema_id: "conversation.clarification", schema_version: "v1", model_run_ref: modelRunId,
      payload: { reason: "范围不明确", question: "请补充范围", suggested_scopes: ["当前工作区"] },
    } });
    expect(clarification).toMatchObject({ publicationStatus: "clarification_required", retrievalSummary: null });
    const termination = (status: "failed" | "cancelled", reason: "WORKSPACE_ANALYSIS_TOOL_FAILED" | "WORKSPACE_ANALYSIS_RUNTIME_FAILED" | "WORKSPACE_ANALYSIS_CANCELLED") => decodeAnswer({ ...workspaceAnalysisAnswer, publication_status: status, result_type: "workspace_analysis_termination", assistant_text: "分析已终止", citations: [], result: {
      result_type: "workspace_analysis_termination", schema_id: "conversation.workspace_analysis_termination", schema_version: "v1", model_run_ref: null,
      payload: { termination_reason: reason, summary: "分析已终止" },
    } });
    expect(termination("failed", "WORKSPACE_ANALYSIS_TOOL_FAILED").publicationStatus).toBe("failed");
    expect(termination("failed", "WORKSPACE_ANALYSIS_RUNTIME_FAILED")).toMatchObject({ result: { payload: { terminationReason: "WORKSPACE_ANALYSIS_RUNTIME_FAILED" } } });
    expect(termination("cancelled", "WORKSPACE_ANALYSIS_CANCELLED").publicationStatus).toBe("cancelled");
    expect(() => termination("failed", "WORKSPACE_ANALYSIS_CANCELLED")).toThrow(ConversationApiError);
  });

  it("严格解码并绑定 Workspace Analysis 权威时间线", () => {
    expect(decodeWorkspaceAnalysisTimeline(workspaceAnalysisTimeline)).toMatchObject({ answerId, workspaceId, runStatus: "queued", items: [] });
    expect(decodeWorkspaceAnalysisTimeline(successfulWorkspaceAnalysisTimeline)).toMatchObject({ runStatus: "succeeded", items: { length: 7 }, budget: { modelCalls: { used: 3 }, toolCalls: { used: 4 }, sourceReads: { used: 1 } } });
    expect(() => decodeWorkspaceAnalysisTimeline({ ...workspaceAnalysisTimeline, workspace_path: "/private" })).toThrow(ConversationApiError);
    expect(() => decodeWorkspaceAnalysisTimeline({ ...workspaceAnalysisTimeline, budget: { ...workspaceAnalysisTimeline.budget, model_calls: { used: 1, max: 4 } } })).toThrow(ConversationApiError);
    expect(() => decodeWorkspaceAnalysisTimeline({ ...workspaceAnalysisTimeline, budget: { ...workspaceAnalysisTimeline.budget, source_reads: { used: 0, max: 4 } } })).toThrow(ConversationApiError);
    expect(() => decodeWorkspaceAnalysisTimeline({ ...successfulWorkspaceAnalysisTimeline, items: [{ ...successfulWorkspaceAnalysisTimeline.items[0], tool_ref: { name: "ReadGitStatus", version: 3 } }, ...successfulWorkspaceAnalysisTimeline.items.slice(1)] })).toThrow(ConversationApiError);
    expect(() => decodeWorkspaceAnalysisTimeline({ ...workspaceAnalysisTimeline, analysis_run_id: answerId })).toThrow(ConversationApiError);
    expect(() => decodeWorkspaceAnalysisTimeline({ ...successfulWorkspaceAnalysisTimeline, items: [] })).toThrow(ConversationApiError);
    expect(() => decodeWorkspaceAnalysisTimeline({ ...successfulWorkspaceAnalysisTimeline, budget: { ...successfulWorkspaceAnalysisTimeline.budget, input_tokens: { used: 89, max: 196608 } } })).toThrow(ConversationApiError);
    expect(() => decodeWorkspaceAnalysisTimeline({ ...workspaceAnalysisTimeline, run_status: "running", items: [{ sequence: 1, kind: "node", phase: "inspect_workspace", status: "failed", tool_ref: null, duration_ms: 1, error_code: "WORKSPACE_ANALYSIS_PRIVATE_FAILURE", summary: null }] })).toThrow(ConversationApiError);
    expect(decodeWorkspaceAnalysisTimeline({ ...workspaceAnalysisTimeline, run_status: "failed", termination_reason: "WORKSPACE_ANALYSIS_RUNTIME_FAILED", items: [{ sequence: 1, kind: "node", phase: "inspect_workspace", status: "failed", tool_ref: null, duration_ms: 1, error_code: "WORKSPACE_ANALYSIS_RUNTIME_FAILED", summary: null }] })).toMatchObject({ runStatus: "failed", terminationReason: "WORKSPACE_ANALYSIS_RUNTIME_FAILED", items: [{ errorCode: "WORKSPACE_ANALYSIS_RUNTIME_FAILED" }] });
  });

  it.each([
    ["未知顶层字段", { ...completedAnswer, drift: true }],
    ["非法 UUID", { ...completedAnswer, id: "bad" }],
    ["非法时间", { ...completedAnswer, updated_at: "2026-02-30T00:00:00Z" }],
    ["未知发布状态", { ...completedAnswer, publication_status: "draft" }],
    ["未知 RAG stage", { ...pendingAnswer, current_stage: "generation.started" }],
    ["pending 携带 result", { ...pendingAnswer, result_type: "rag_answer", result: ragResult }],
    ["Citation 缺 href", { ...completedAnswer, citations: [identity] }],
    ["Citation href 漂移", { ...completedAnswer, citations: [{ ...identity, href: "/wrong" }] }],
    ["RAG v1 schema 漂移", { ...completedAnswer, result: { ...ragResult, schema_version: "v1" } }],
    ["Assertion 未知 Citation", { ...completedAnswer, result: { ...ragResult, payload: { ...ragResult.payload, assertions: [{ ...ragResult.payload.assertions[0], citation_ids: ["missing"] }] } } }],
    ["Related Topic 未知字段", { ...completedAnswer, result: { ...ragResult, payload: { ...ragResult.payload, related_topics: [{ ...ragResult.payload.related_topics[0], extra: true }] } } }],
  ])("拒绝%s", (_name, payload) => expect(() => decodeAnswer(payload)).toThrow(ConversationApiError));

  it("严格解码 Problem 并拒绝漂移", () => {
    expect(decodeProblem({ error_code: "ANSWER_NOT_FOUND", message: "不存在", retryable: false, workflow_run_id: workflowId, details: { action: "refetch" } })).toMatchObject({ errorCode: "ANSWER_NOT_FOUND", workflowRunId: workflowId });
    expect(() => decodeProblem({ error_code: "X", message: "x", retryable: false, extra: true })).toThrow(ConversationApiError);
  });

  it.each([
    ["缺少 workspace query", `/api/v1/answers/${answerId}`],
    ["workspace 不匹配", `/api/v1/answers/${answerId}?workspace_id=${conversationId}`],
    ["重复 workspace", `/api/v1/answers/${answerId}?workspace_id=${workspaceId}&workspace_id=${workspaceId}`],
    ["额外 query", `/api/v1/answers/${answerId}?workspace_id=${workspaceId}&extra=1`],
  ])("拒绝 QuestionAcceptance status_url %s", (_name, statusUrl) => {
    expect(() => decodeQuestionAcceptance({ question, answer: pendingAnswer, status_url: statusUrl })).toThrow(ConversationApiError);
  });

  it("接受完整冲突披露并拒绝单边冲突", () => {
    const conflict = (claimId: string, position: string) => ({ claim_id: claimId, position, applicability: { region: "cn" }, citation_ids: [citationId], updated_at: at });
    const valid = { ...completedAnswer, result: { ...ragResult, payload: { ...ragResult.payload, conflict_positions: [conflict(claimId1, "A"), conflict(claimId2, "B")], conflict_summary: "存在差异" } } };
    const decoded = decodeAnswer(valid);
    expect(decoded.publicationStatus).toBe("completed");
    if (decoded.publicationStatus !== "completed" || decoded.resultType !== "rag_answer") throw new Error("expected RAG completed answer");
    expect(decoded.result.payload.conflictPositions[0]?.applicability).toEqual({ region: "cn" });
    expect(() => decodeAnswer({ ...valid, result: { ...ragResult, payload: { ...ragResult.payload, conflict_positions: [conflict(claimId1, "A")], conflict_summary: "存在差异" } } })).toThrow(ConversationApiError);
  });
});

describe("Conversation request clients", () => {
  it("创建会话发送 Idempotency-Key 并读取 ETag", async () => {
    const fetchMock = vi.fn<typeof fetch>().mockResolvedValue(new Response(JSON.stringify(conversation), { status: 201, headers: { "Content-Type": "application/json", ETag: 'W/"1"' } }));
    vi.stubGlobal("fetch", fetchMock);
    await expect(createConversation({ workspaceId, idempotencyKey: "create-1", title: null })).resolves.toMatchObject({ resource: { id: conversationId }, etag: 'W/"1"', notModified: false });
    expect(new Headers(fetchMock.mock.calls[0]?.[1]?.headers).get("Idempotency-Key")).toBe("create-1");
  });

  it("提交问题省略 mode 以保持后端 RAG 默认值并解码 202", async () => {
    const fetchMock = vi.fn<typeof fetch>().mockResolvedValue(new Response(JSON.stringify({ question, answer: pendingAnswer, status_url: `/api/v1/answers/${answerId}?workspace_id=${workspaceId}` }), { status: 202, headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", fetchMock);
    await submitQuestion({ workspaceId, conversationId, idempotencyKey: "question-1", question: "如何恢复？" });
    const init = fetchMock.mock.calls[0]?.[1]; if (typeof init?.body !== "string") throw new Error("missing body");
    expect(JSON.parse(init.body)).toEqual({ workspace_id: workspaceId, question: "如何恢复？", scope: { retrieval_mode: "hybrid", source_ids: [], source_version_ids: [], path_prefixes: [], captured_at_from: null, captured_at_before: null, allow_original_sources: false, allow_web: false }, answer_depth: "standard", output_format: "markdown" });
    expect(new Headers(init.headers).get("Idempotency-Key")).toBe("question-1");
  });

  it("提交工作区分析显式发送 mode，并按 Workspace/Answer 绑定读取时间线", async () => {
    const fetchMock = vi.fn<typeof fetch>()
      .mockResolvedValueOnce(new Response(JSON.stringify({ question: { ...question, mode: "workspace_analysis" }, answer: pendingAnswer, status_url: `/api/v1/answers/${answerId}?workspace_id=${workspaceId}` }), { status: 202, headers: { "Content-Type": "application/json" } }))
      .mockResolvedValueOnce(new Response(JSON.stringify(workspaceAnalysisTimeline), { status: 200, headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", fetchMock);
    await submitQuestion({ workspaceId, conversationId, idempotencyKey: "workspace-analysis-1", question: "检查当前工作区", mode: "workspace_analysis" });
    const init = fetchMock.mock.calls[0]?.[1]; if (typeof init?.body !== "string") throw new Error("missing body");
    expect(JSON.parse(init.body)).toMatchObject({ mode: "workspace_analysis" });
    await expect(getWorkspaceAnalysisTimeline({ workspaceId, answerId })).resolves.toMatchObject({ answerId, workspaceId });
    expect(fetchMock.mock.calls[1]?.[0]).toBe(`/api/v1/answers/${answerId}/analysis-timeline?workspace_id=${workspaceId}`);
  });

  it("GET 编码 cursor/limit 并处理带 stage 的 Answer ETag 200/304", async () => {
    const fetchMock = vi.fn<typeof fetch>()
      .mockResolvedValueOnce(new Response(JSON.stringify({ items: [] }), { status: 200, headers: { "Content-Type": "application/json" } }))
      .mockResolvedValueOnce(new Response(JSON.stringify(completedAnswer), { status: 200, headers: { "Content-Type": "application/json", ETag: 'W/"answer-2-workflow-3-stage-none"' } }))
      .mockResolvedValueOnce(new Response(null, { status: 304 }));
    vi.stubGlobal("fetch", fetchMock);
    await listConversations({ workspaceId, cursor: "opaque+cursor", limit: 20 });
    expect(fetchMock.mock.calls[0]?.[0]).toContain(`workspace_id=${workspaceId}&cursor=opaque%2Bcursor&limit=20`);
    await expect(getAnswer({ workspaceId, id: answerId })).resolves.toMatchObject({ resource: { publicationStatus: "completed" }, etag: 'W/"answer-2-workflow-3-stage-none"', notModified: false });
    await expect(getAnswer({ workspaceId, id: answerId, ifNoneMatch: 'W/"answer-2-workflow-3-stage-plan.started"' })).resolves.toEqual({ resource: null, etag: 'W/"answer-2-workflow-3-stage-plan.started"', notModified: true });
    expect(new Headers(fetchMock.mock.calls[2]?.[1]?.headers).get("If-None-Match")).toBe('W/"answer-2-workflow-3-stage-plan.started"');
  });

  it("latest Turn 使用有界恢复查询", async () => {
    const fetchMock = vi.fn<typeof fetch>().mockResolvedValue(new Response(JSON.stringify({ items: [{ question, answer: pendingAnswer }] }), { status: 200, headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", fetchMock);
    await expect(getLatestTurn({ workspaceId, conversationId })).resolves.toMatchObject({ question: { ordinal: 1 }, answer: { publicationStatus: "pending" } });
    expect(fetchMock.mock.calls[0]?.[0]).toContain(`/conversations/${conversationId}/turns?workspace_id=${workspaceId}&latest=true`);
  });

  it("拒绝 Answer ETag 的未知 stage", async () => {
    vi.stubGlobal("fetch", vi.fn<typeof fetch>().mockResolvedValue(new Response(JSON.stringify(completedAnswer), {
      status: 200, headers: { "Content-Type": "application/json", ETag: 'W/"answer-2-workflow-3-stage-generation.started"' },
    })));
    await expect(getAnswer({ workspaceId, id: answerId })).rejects.toMatchObject({ errorCode: "INVALID_RESPONSE" });
    await expect(getAnswer({ workspaceId, id: answerId, ifNoneMatch: 'W/"answer-2-workflow-3-stage-generation.started"' })).rejects.toMatchObject({ errorCode: "INVALID_REQUEST" });
  });

  it("Feedback 校验 Citation 语义并发送 snake_case", async () => {
    const feedback = { id: topicId, workspace_id: workspaceId, answer_id: answerId, feedback_type: "broken_citation", citation_id: citationId, comment: null, created_at: at };
    const fetchMock = vi.fn<typeof fetch>().mockResolvedValue(new Response(JSON.stringify(feedback), { status: 201, headers: { "Content-Type": "application/json" } })); vi.stubGlobal("fetch", fetchMock);
    await submitFeedback({ workspaceId, answerId, idempotencyKey: "feedback-1", feedbackType: "broken_citation", citationId });
    const init = fetchMock.mock.calls[0]?.[1]; if (typeof init?.body !== "string") throw new Error("missing body");
    expect(JSON.parse(init.body)).toEqual({ workspace_id: workspaceId, feedback_type: "broken_citation", citation_id: citationId, comment: null });
    await expect(submitFeedback({ workspaceId, answerId, idempotencyKey: "bad", feedbackType: "helpful", citationId })).rejects.toMatchObject({ errorCode: "INVALID_REQUEST" });
  });

  it.each([
    ["非法 UUID", () => listConversations({ workspaceId: "bad" })],
    ["非法 cursor", () => listConversations({ workspaceId, cursor: "x".repeat(2049) })],
    ["非法 limit", () => listConversations({ workspaceId, limit: 101 })],
    ["NUL question", () => submitQuestion({ workspaceId, conversationId, idempotencyKey: "key", question: "a\0b" })],
    ["非法 time", () => submitQuestion({ workspaceId, conversationId, idempotencyKey: "key", question: "q", scope: { capturedAtFrom: "2026-02-30T00:00:00Z" } })],
  ])("请求前拒绝%s", async (_name, operation) => await expect(operation()).rejects.toMatchObject({ errorCode: "INVALID_REQUEST" }));

  it("将服务端 Problem 映射为稳定错误并拒绝 Problem schema 漂移", async () => {
    vi.stubGlobal("fetch", vi.fn<typeof fetch>().mockResolvedValue(new Response(JSON.stringify({ error_code: "CONVERSATION_ACTIVE_WORKFLOW", message: "已有任务", retryable: false, workflow_run_id: workflowId }), { status: 409, headers: { "Content-Type": "application/json" } })));
    await expect(listConversations({ workspaceId })).rejects.toMatchObject({ errorCode: "CONVERSATION_ACTIVE_WORKFLOW", status: 409, workflowRunId: workflowId });
    vi.stubGlobal("fetch", vi.fn<typeof fetch>().mockResolvedValue(new Response(JSON.stringify({ error_code: "X", message: "x", retryable: false, extra: true }), { status: 500, headers: { "Content-Type": "application/json" } })));
    await expect(listConversations({ workspaceId })).rejects.toMatchObject({ errorCode: "INVALID_RESPONSE", status: 500 });
  });
});
