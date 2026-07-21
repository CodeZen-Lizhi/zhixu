import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";

import { SemanticLinkCandidatePanel } from "./SemanticLinkCandidatePanel";

const workspaceId = "92000000-0000-4000-8000-000000000001";
const claimId = "92000000-0000-4000-8000-000000000002";
const topicId = "92000000-0000-4000-8000-000000000003";
const otherClaimId = "92000000-0000-4000-8000-00000000000c";
const candidateId = "92000000-0000-4000-8000-000000000004";
const sourceVersionId = "92000000-0000-4000-8000-000000000005";
const sourceSpanId = "92000000-0000-4000-8000-000000000006";
const indexVersionId = "92000000-0000-4000-8000-000000000007";
const decisionId = "92000000-0000-4000-8000-000000000008";
const proposalId = "92000000-0000-4000-8000-000000000009";
const at = "2026-07-20T08:10:12.123456789Z";
const candidateHref = `/api/v1/workspaces/${workspaceId}/source-versions/${sourceVersionId}`;
const spanHref = `${candidateHref}/spans/${sourceSpanId}`;
const scanStatusUrl = (scanId: string): string => `/api/v1/graph/candidate-scans/${scanId}?workspace_id=${workspaceId}`;

const candidatePayload = {
  id: candidateId,
  workspace_id: workspaceId,
  fingerprint: "a".repeat(64),
  status: "ACTIVE",
  version: 3,
  source: { type: "CLAIM", id: claimId, version: 5, summary: "Claim summary", excerpt: "Claim source context" },
  target: { type: "TOPIC", id: topicId, version: 2, summary: "Topic summary", excerpt: "Topic target context" },
  proposed_relation_type: "BELONGS_TO",
  confidence: 0.92,
  reason: "The claim belongs to the topic.",
  discovery_methods: ["TERM_MATCH", "COMMON_TOPIC"],
  evidence: [{
    id: sourceSpanId,
    semantic_hash: "b".repeat(64),
    source_version_id: sourceVersionId,
    source_span_id: sourceSpanId,
    source_version_href: candidateHref,
    source_span_href: spanHref,
    excerpt: "shared terminology",
    reason: "The source span uses the same bounded term.",
  }],
  generation: { index_version_id: indexVersionId, embedding_version_id: null, rerank_version_id: null },
  reopened_reason: null,
  reopened_from_candidate_id: null,
  proposal_id: null,
  deferred_until: null,
  created_at: at,
  updated_at: at,
};

const pagePayload = { workspace_id: workspaceId, items: [candidatePayload] };
const topicScanPagePayload = {
  workspace_id: workspaceId,
  items: [{
    ...candidatePayload,
    target: { ...candidatePayload.target, type: "CLAIM", id: otherClaimId, summary: "Related claim", excerpt: "Related claim context" },
    proposed_relation_type: "COMPLEMENTS",
    reason: "The Topic scan found complementary Claim evidence.",
  }],
};

const jsonResponse = (payload: unknown, status = 200): Response => new Response(JSON.stringify(payload), {
  status,
  headers: { "Content-Type": "application/json" },
});

const renderPanel = (fetchMock: typeof fetch, nodeScope: { type: "CLAIM" | "TOPIC"; id: string } | null = null) => {
  vi.stubGlobal("fetch", fetchMock);
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  return render(<QueryClientProvider client={queryClient}><MemoryRouter><SemanticLinkCandidatePanel
    workspaceId={workspaceId}
    nodeScope={nodeScope}
    idempotencyKeyFactory={() => "candidate-decision:test-key"}
    scanIdempotencyKeyFactory={() => "candidate-scan:test-key"}
  /></MemoryRouter></QueryClientProvider>);
};

const requestUrl = (input: RequestInfo | URL | undefined): URL => {
  if (input === undefined) throw new Error("missing request");
  const raw = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
  return new URL(raw, "http://localhost");
};

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("SemanticLinkCandidatePanel", () => {
  it("仅对正式 Topic 启动持久扫描，并在成功后刷新候选", async () => {
    const scanId = "92000000-0000-4000-8000-00000000000a";
    const workflowRunId = "92000000-0000-4000-8000-00000000000b";
    let candidateCalls = 0;
    let scanReads = 0;
    const fetchMock = vi.fn<typeof fetch>((input) => {
      const url = requestUrl(input);
      if (url.pathname.endsWith("/candidate-scans")) return Promise.resolve(jsonResponse({
        scan_id: scanId,
        workflow_run_id: workflowRunId,
        status: "PENDING",
        version: 1,
        status_url: scanStatusUrl(scanId),
      }, 202));
      if (url.pathname.endsWith(`/candidate-scans/${scanId}`)) {
        scanReads += 1;
        return Promise.resolve(jsonResponse({
          id: scanId,
          workspace_id: workspaceId,
          scope: { kind: "TOPIC", topic_id: topicId },
          status: "SUCCEEDED",
          workflow_run_id: workflowRunId,
          version: 3,
          status_url: scanStatusUrl(scanId),
          total_count: 2,
          processed_count: 2,
          candidate_count: 1,
          ignored_count: 0,
          failed_count: 0,
          last_error: null,
          created_at: at,
          updated_at: at,
          completed_at: at,
        }));
      }
      if (url.pathname.endsWith("/candidates")) {
        candidateCalls += 1;
        return Promise.resolve(jsonResponse(topicScanPagePayload));
      }
      return Promise.reject(new Error(`unexpected request ${url.pathname}`));
    });
    renderPanel(fetchMock, { type: "TOPIC", id: topicId });

    fireEvent.click(screen.getByRole("button", { name: "扫描当前 Topic" }));
    expect(await screen.findByText("Topic 扫描 · SUCCEEDED")).toBeInTheDocument();
    expect(await screen.findByText("Candidate · 未进入正式图")).toBeInTheDocument();
    await waitFor(() => expect(candidateCalls).toBeGreaterThanOrEqual(1));
    expect(scanReads).toBe(1);

    const startRequest = fetchMock.mock.calls.find(([input]) => requestUrl(input).pathname.endsWith("/candidate-scans"));
    expect(startRequest?.[1]?.headers).toMatchObject({ "Idempotency-Key": "candidate-scan:test-key" });
    if (typeof startRequest?.[1]?.body !== "string") throw new Error("missing scan body");
    expect(JSON.parse(startRequest[1].body)).toEqual({ workspace_id: workspaceId, scope: { kind: "TOPIC", topic_id: topicId } });
  });

  it("Claim 选择不暴露未实现的 Node scan", () => {
    const fetchMock = vi.fn<typeof fetch>();
    renderPanel(fetchMock, { type: "CLAIM", id: claimId });
    expect(screen.queryByRole("button", { name: "扫描当前 Topic" })).not.toBeInTheDocument();
  });

  it("scan 启动响应丢失后复用同一 Idempotency-Key 精确重试", async () => {
    const scanId = "92000000-0000-4000-8000-00000000000a";
    const workflowRunId = "92000000-0000-4000-8000-00000000000b";
    let starts = 0;
    let keys = 0;
    const keyFactory = vi.fn(() => `candidate-scan:key-${String(++keys)}`);
    const fetchMock = vi.fn<typeof fetch>((input) => {
      const url = requestUrl(input);
      if (url.pathname.endsWith("/candidate-scans")) {
        starts += 1;
        if (starts === 1) return Promise.reject(new TypeError("response lost"));
        return Promise.resolve(jsonResponse({
          scan_id: scanId,
          workflow_run_id: workflowRunId,
          status: "PENDING",
          version: 1,
          status_url: scanStatusUrl(scanId),
        }, 202));
      }
      if (url.pathname.endsWith(`/candidate-scans/${scanId}`)) return Promise.resolve(jsonResponse({
        id: scanId,
        workspace_id: workspaceId,
        scope: { kind: "TOPIC", topic_id: topicId },
        status: "SUCCEEDED",
        workflow_run_id: workflowRunId,
        version: 3,
        status_url: scanStatusUrl(scanId),
        total_count: 2,
        processed_count: 2,
        candidate_count: 1,
        ignored_count: 0,
        failed_count: 0,
        last_error: null,
        created_at: at,
        updated_at: at,
        completed_at: at,
      }));
      if (url.pathname.endsWith("/candidates")) return Promise.resolve(jsonResponse(pagePayload));
      return Promise.reject(new Error(`unexpected request ${url.pathname}`));
    });
    vi.stubGlobal("fetch", fetchMock);
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
    render(<QueryClientProvider client={queryClient}><MemoryRouter><SemanticLinkCandidatePanel
      workspaceId={workspaceId}
      nodeScope={{ type: "TOPIC", id: topicId }}
      scanIdempotencyKeyFactory={keyFactory}
    /></MemoryRouter></QueryClientProvider>);

    fireEvent.click(screen.getByRole("button", { name: "扫描当前 Topic" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("NETWORK_ERROR");
    fireEvent.click(screen.getByRole("button", { name: "重试扫描启动" }));
    expect(await screen.findByText("Topic 扫描 · SUCCEEDED")).toBeInTheDocument();

    const requests = fetchMock.mock.calls.filter(([input]) => requestUrl(input).pathname.endsWith("/candidate-scans"));
    expect(requests).toHaveLength(2);
    expect(requests[0]?.[1]?.headers).toMatchObject({ "Idempotency-Key": "candidate-scan:key-1" });
    expect(requests[1]?.[1]?.headers).toMatchObject({ "Idempotency-Key": "candidate-scan:key-1" });
    expect(keyFactory).toHaveBeenCalledTimes(1);
  });

  it("已有终态 scan 时新启动响应丢失仍精确重放新命令", async () => {
    const oldScanId = "92000000-0000-4000-8000-00000000000a";
    const newScanId = "92000000-0000-4000-8000-00000000000b";
    const oldWorkflowRunId = "92000000-0000-4000-8000-00000000000c";
    const newWorkflowRunId = "92000000-0000-4000-8000-00000000000d";
    let starts = 0;
    let keys = 0;
    const keyFactory = vi.fn(() => `candidate-scan:replacement-${String(++keys)}`);
    const fetchMock = vi.fn<typeof fetch>((input) => {
      const url = requestUrl(input);
      if (url.pathname.endsWith(`/candidate-scans/${oldScanId}`)) return Promise.resolve(jsonResponse({
        id: oldScanId,
        workspace_id: workspaceId,
        scope: { kind: "TOPIC", topic_id: topicId },
        status: "SUCCEEDED",
        workflow_run_id: oldWorkflowRunId,
        version: 3,
        status_url: scanStatusUrl(oldScanId),
        total_count: 2,
        processed_count: 2,
        candidate_count: 1,
        ignored_count: 0,
        failed_count: 0,
        last_error: null,
        created_at: at,
        updated_at: at,
        completed_at: at,
      }));
      if (url.pathname.endsWith("/candidate-scans")) {
        starts += 1;
        if (starts === 1) return Promise.reject(new TypeError("replacement response lost"));
        return Promise.resolve(jsonResponse({
          scan_id: newScanId,
          workflow_run_id: newWorkflowRunId,
          status: "PENDING",
          version: 1,
          status_url: scanStatusUrl(newScanId),
        }, 202));
      }
      if (url.pathname.endsWith(`/candidate-scans/${newScanId}`)) return Promise.resolve(jsonResponse({
        id: newScanId,
        workspace_id: workspaceId,
        scope: { kind: "TOPIC", topic_id: topicId },
        status: "RUNNING",
        workflow_run_id: newWorkflowRunId,
        version: 2,
        status_url: scanStatusUrl(newScanId),
        total_count: 2,
        processed_count: 1,
        candidate_count: 0,
        ignored_count: 0,
        failed_count: 0,
        last_error: null,
        created_at: at,
        updated_at: at,
        completed_at: null,
      }));
      if (url.pathname.endsWith("/candidates")) return Promise.resolve(jsonResponse(pagePayload));
      return Promise.reject(new Error(`unexpected request ${url.pathname}`));
    });
    vi.stubGlobal("fetch", fetchMock);
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
    render(<QueryClientProvider client={queryClient}><MemoryRouter><SemanticLinkCandidatePanel
      workspaceId={workspaceId}
      nodeScope={{ type: "TOPIC", id: topicId }}
      persistedScanId={oldScanId}
      scanIdempotencyKeyFactory={keyFactory}
    /></MemoryRouter></QueryClientProvider>);

    fireEvent.click(screen.getByRole("button", { name: "扫描当前 Topic" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("NETWORK_ERROR");
    fireEvent.click(screen.getByRole("button", { name: "重试扫描启动" }));
    await waitFor(() => expect(starts).toBe(2));

    const startRequests = fetchMock.mock.calls.filter(([input]) => requestUrl(input).pathname.endsWith("/candidate-scans"));
    expect(startRequests[0]?.[1]?.headers).toMatchObject({ "Idempotency-Key": "candidate-scan:replacement-1" });
    expect(startRequests[1]?.[1]?.headers).toMatchObject({ "Idempotency-Key": "candidate-scan:replacement-1" });
    expect(keyFactory).toHaveBeenCalledTimes(1);
  });

  it("从持久 URL scan ID 恢复服务端进度", async () => {
    const scanId = "92000000-0000-4000-8000-00000000000a";
    const workflowRunId = "92000000-0000-4000-8000-00000000000b";
    const fetchMock = vi.fn<typeof fetch>((input) => {
      const url = requestUrl(input);
      if (url.pathname.endsWith(`/candidate-scans/${scanId}`)) return Promise.resolve(jsonResponse({
        id: scanId,
        workspace_id: workspaceId,
        scope: { kind: "TOPIC", topic_id: topicId },
        status: "RUNNING",
        workflow_run_id: workflowRunId,
        version: 2,
        status_url: scanStatusUrl(scanId),
        total_count: 10,
        processed_count: 4,
        candidate_count: 2,
        ignored_count: 1,
        failed_count: 0,
        last_error: null,
        created_at: at,
        updated_at: at,
        completed_at: null,
      }));
      if (url.pathname.endsWith("/candidates")) return Promise.resolve(jsonResponse(pagePayload));
      return Promise.reject(new Error(`unexpected request ${url.pathname}`));
    });
    vi.stubGlobal("fetch", fetchMock);
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
    render(<QueryClientProvider client={queryClient}><MemoryRouter><SemanticLinkCandidatePanel
      workspaceId={workspaceId}
      nodeScope={{ type: "TOPIC", id: topicId }}
      persistedScanId={scanId}
    /></MemoryRouter></QueryClientProvider>);

    fireEvent.click(screen.getByRole("button", { name: "审阅候选" }));
    expect(await screen.findByText("Topic 扫描 · RUNNING")).toBeInTheDocument();
    expect(screen.getByText("4 / 10 节点")).toBeInTheDocument();
  });

  it("切换到其他 Topic 时清除不匹配的持久 scan 绑定", async () => {
    const scanId = "92000000-0000-4000-8000-00000000000a";
    const workflowRunId = "92000000-0000-4000-8000-00000000000b";
    const otherTopicId = "93000000-0000-4000-8000-000000000002";
    const onPersistedScanIdChange = vi.fn();
    const fetchMock = vi.fn<typeof fetch>((input) => {
      const url = requestUrl(input);
      if (url.pathname.endsWith(`/candidate-scans/${scanId}`)) return Promise.resolve(jsonResponse({
        id: scanId,
        workspace_id: workspaceId,
        scope: { kind: "TOPIC", topic_id: topicId },
        status: "RUNNING",
        workflow_run_id: workflowRunId,
        version: 2,
        status_url: scanStatusUrl(scanId),
        total_count: 10,
        processed_count: 4,
        candidate_count: 2,
        ignored_count: 1,
        failed_count: 0,
        last_error: null,
        created_at: at,
        updated_at: at,
        completed_at: null,
      }));
      if (url.pathname.endsWith("/candidates")) return Promise.resolve(jsonResponse({ workspace_id: workspaceId, items: [] }));
      return Promise.reject(new Error(`unexpected request ${url.pathname}`));
    });
    vi.stubGlobal("fetch", fetchMock);
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
    const view = render(<QueryClientProvider client={queryClient}><MemoryRouter><SemanticLinkCandidatePanel
      workspaceId={workspaceId}
      nodeScope={{ type: "TOPIC", id: topicId }}
      persistedScanId={scanId}
      onPersistedScanIdChange={onPersistedScanIdChange}
    /></MemoryRouter></QueryClientProvider>);

    fireEvent.click(screen.getByRole("button", { name: "审阅候选" }));
    expect(await screen.findByText("Topic 扫描 · RUNNING")).toBeInTheDocument();

    view.rerender(<QueryClientProvider client={queryClient}><MemoryRouter><SemanticLinkCandidatePanel
      workspaceId={workspaceId}
      nodeScope={{ type: "TOPIC", id: otherTopicId }}
      persistedScanId={scanId}
      onPersistedScanIdChange={onPersistedScanIdChange}
    /></MemoryRouter></QueryClientProvider>);

    await waitFor(() => expect(onPersistedScanIdChange).toHaveBeenCalledWith(null));
    expect(screen.queryByText("Topic 扫描 · RUNNING")).not.toBeInTheDocument();
  });

  it("刷新后展示持久 FAILED scan 的稳定错误和重新发起入口", async () => {
    const scanId = "92000000-0000-4000-8000-00000000000a";
    const workflowRunId = "92000000-0000-4000-8000-00000000000b";
    const fetchMock = vi.fn<typeof fetch>((input) => {
      const url = requestUrl(input);
      if (url.pathname.endsWith(`/candidate-scans/${scanId}`)) return Promise.resolve(jsonResponse({
        id: scanId,
        workspace_id: workspaceId,
        scope: { kind: "TOPIC", topic_id: topicId },
        status: "FAILED",
        workflow_run_id: workflowRunId,
        version: 4,
        status_url: scanStatusUrl(scanId),
        total_count: 10,
        processed_count: 4,
        candidate_count: 2,
        ignored_count: 1,
        failed_count: 1,
        last_error: { stage: "discover", code: "SEMANTIC_PROVIDER_TIMEOUT", retryable: true },
        created_at: at,
        updated_at: at,
        completed_at: at,
      }));
      if (url.pathname.endsWith("/candidates")) return Promise.resolve(jsonResponse(pagePayload));
      return Promise.reject(new Error(`unexpected request ${url.pathname}`));
    });
    vi.stubGlobal("fetch", fetchMock);
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
    render(<QueryClientProvider client={queryClient}><MemoryRouter><SemanticLinkCandidatePanel
      workspaceId={workspaceId}
      nodeScope={{ type: "TOPIC", id: topicId }}
      persistedScanId={scanId}
    /></MemoryRouter></QueryClientProvider>);

    fireEvent.click(screen.getByRole("button", { name: "审阅候选" }));
    expect(await screen.findByText("扫描失败 · SEMANTIC_PROVIDER_TIMEOUT")).toBeInTheDocument();
    expect(screen.getByText("阶段：discover · 该故障可重试。")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "重新发起扫描" })).toBeEnabled();
  });

  it("Workspace 切换会清除旧 scan URL 绑定", async () => {
    const scanId = "92000000-0000-4000-8000-00000000000a";
    const otherWorkspaceId = "93000000-0000-4000-8000-000000000001";
    const onPersistedScanIdChange = vi.fn();
    vi.stubGlobal("fetch", vi.fn<typeof fetch>().mockResolvedValue(jsonResponse({
      error_code: "SEMANTIC_LINK_NOT_FOUND",
      message: "not found",
      retryable: false,
    }, 404)));
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
    const view = render(<QueryClientProvider client={queryClient}><MemoryRouter><SemanticLinkCandidatePanel
      workspaceId={workspaceId}
      persistedScanId={scanId}
      onPersistedScanIdChange={onPersistedScanIdChange}
    /></MemoryRouter></QueryClientProvider>);

    view.rerender(<QueryClientProvider client={queryClient}><MemoryRouter><SemanticLinkCandidatePanel
      workspaceId={otherWorkspaceId}
      persistedScanId={scanId}
      onPersistedScanIdChange={onPersistedScanIdChange}
    /></MemoryRouter></QueryClientProvider>);
    await waitFor(() => expect(onPersistedScanIdChange).toHaveBeenCalledWith(null));
  });

  it("默认折叠且不请求 Candidate；打开后以独立候选卡片展示端点和证据", async () => {
    const fetchMock = vi.fn<typeof fetch>().mockResolvedValue(jsonResponse(pagePayload));
    renderPanel(fetchMock, { type: "CLAIM", id: claimId });

    expect(screen.queryByText("Candidate · 未进入正式图")).not.toBeInTheDocument();
    expect(fetchMock).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "审阅候选" }));

    expect(await screen.findByText("Candidate · 未进入正式图")).toBeInTheDocument();
    expect(screen.getByText("Claim source context")).toBeInTheDocument();
    expect(screen.getByText("Topic target context")).toBeInTheDocument();
    expect(screen.getByText("The claim belongs to the topic.")).toBeInTheDocument();
    const evidenceSummary = screen.getByText(/查看候选证据/);
    const evidenceDetails = evidenceSummary.closest("details");
    expect(evidenceDetails).not.toBeNull();
    expect(evidenceDetails?.open).toBe(false);
    fireEvent.click(evidenceSummary);
    expect(evidenceDetails?.open).toBe(true);
    expect(screen.getByText("shared terminology")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "打开来源片段" })).toHaveAttribute("href", spanHref);

    fireEvent.change(screen.getByRole("slider", { name: "最低置信度" }), { target: { value: "0.75" } });
    await waitFor(() => expect(fetchMock.mock.calls.some(([input]) =>
      requestUrl(input).searchParams.get("min_confidence") === "0.75")).toBe(true));
    expect(screen.getByText("75%")).toBeInTheDocument();

    const url = requestUrl(fetchMock.mock.calls[0]?.[0]);
    expect(url.pathname).toBe("/api/v1/graph/candidates");
    expect(url.searchParams.get("node_type")).toBe("CLAIM");
    expect(url.searchParams.get("node_id")).toBe(claimId);
  });

  it("忽略要求非空规范化原因，并复用 Idempotency-Key 重试成功", async () => {
    let decisionCalls = 0;
    const fetchMock = vi.fn<typeof fetch>((input) => {
      const url = requestUrl(input);
      if (url.pathname.endsWith("/candidates")) return Promise.resolve(jsonResponse(pagePayload));
      if (url.pathname.endsWith(`/candidates/${candidateId}/decisions`)) {
        decisionCalls += 1;
        if (decisionCalls === 1) return Promise.resolve(jsonResponse({ error_code: "TEMPORARY", message: "try again", retryable: true }, 503));
        return Promise.resolve(jsonResponse({
          id: decisionId,
          candidate_id: candidateId,
          workspace_id: workspaceId,
          action: "IGNORE",
          status: "IGNORED",
          version: 4,
          proposal_id: null,
          created_at: at,
          updated_at: at,
        }, 201));
      }
      return Promise.reject(new Error(`unexpected request ${url.pathname}`));
    });
    renderPanel(fetchMock);
    fireEvent.click(screen.getByRole("button", { name: "审阅候选" }));
    await screen.findByText("Candidate · 未进入正式图");
    fireEvent.click(screen.getByRole("button", { name: "忽略" }));
    fireEvent.click(screen.getByRole("button", { name: "提交决策" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("请填写非空原因");
    expect(decisionCalls).toBe(0);
    fireEvent.change(screen.getByLabelText("原因"), { target: { value: "  同一 证据  已覆盖  " } });
    fireEvent.click(screen.getByRole("button", { name: "提交决策" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("try again");
    fireEvent.click(screen.getByRole("button", { name: "重试提交" }));
    await waitFor(() => expect(screen.getByText(/已完成“忽略”/)).toBeInTheDocument());

    const decisionRequests = fetchMock.mock.calls.filter(([input]) => requestUrl(input).pathname.endsWith(`/candidates/${candidateId}/decisions`));
    expect(decisionRequests).toHaveLength(2);
    expect(decisionRequests[0]?.[1]?.headers).toMatchObject({ "Idempotency-Key": "candidate-decision:test-key" });
    expect(decisionRequests[1]?.[1]?.headers).toMatchObject({ "Idempotency-Key": "candidate-decision:test-key" });
    const firstDecisionBody = decisionRequests[0]?.[1]?.body;
    if (typeof firstDecisionBody !== "string") throw new Error("missing decision body");
    expect(JSON.parse(firstDecisionBody)).toMatchObject({
      action: "IGNORE",
      reason: "同一 证据 已覆盖",
      expected_version: 3,
    });
  });

  it("409 版本冲突显示可操作恢复，而不是伪造成功", async () => {
    const fetchMock = vi.fn<typeof fetch>((input) => {
      const url = requestUrl(input);
      if (url.pathname.endsWith("/candidates")) return Promise.resolve(jsonResponse(pagePayload));
      return Promise.resolve(jsonResponse({
        error_code: "SEMANTIC_LINK_VERSION_CONFLICT",
        message: "candidate version changed",
        retryable: false,
      }, 409));
    });
    renderPanel(fetchMock);
    fireEvent.click(screen.getByRole("button", { name: "审阅候选" }));
    await screen.findByText("Candidate · 未进入正式图");
    fireEvent.click(screen.getByRole("button", { name: "确认建议关系" }));
    fireEvent.click(screen.getByRole("button", { name: "提交决策" }));
    expect(await screen.findByText("候选已变化")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "刷新候选" })).toBeInTheDocument();
    expect(screen.queryByText(/已创建独立 Proposal/)).not.toBeInTheDocument();
  });

  it("确认成功展示 Proposal 边界，并 Escape 关闭后恢复触发按钮焦点", async () => {
    const deferred = { resolve: (response: Response): void => { void response; } };
    const decisionResponse = new Promise<Response>((resolve) => { deferred.resolve = resolve; });
    const fetchMock = vi.fn<typeof fetch>((input) => {
      const url = requestUrl(input);
      if (url.pathname.endsWith("/candidates")) return Promise.resolve(jsonResponse(pagePayload));
      return decisionResponse;
    });
    renderPanel(fetchMock);
    const toggle = screen.getByRole("button", { name: "审阅候选" });
    fireEvent.click(toggle);
    await screen.findByText("Candidate · 未进入正式图");
    const confirm = screen.getByRole("button", { name: "确认建议关系" });
    fireEvent.click(confirm);
    const dialog = screen.getByRole("dialog");
    const close = screen.getByRole("button", { name: "关闭候选决策" });
    const submit = screen.getByRole("button", { name: "提交决策" });
    submit.focus();
    fireEvent.keyDown(dialog, { key: "Tab" });
    expect(document.activeElement).toBe(close);
    fireEvent.keyDown(dialog, { key: "Tab", shiftKey: true });
    expect(document.activeElement).toBe(submit);
    fireEvent.keyDown(dialog, { key: "Escape" });
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(document.activeElement).toBe(confirm);
    fireEvent.click(confirm);
    fireEvent.click(screen.getByRole("button", { name: "提交决策" }));
    expect(screen.getByRole("button", { name: "正在提交" })).toBeDisabled();
    deferred.resolve(jsonResponse({
      id: decisionId,
      candidate_id: candidateId,
      workspace_id: workspaceId,
      action: "CONFIRM",
      status: "PROPOSAL_CREATED",
      version: 4,
      proposal_id: proposalId,
      created_at: at,
      updated_at: at,
    }, 201));
    expect(await screen.findByText(new RegExp(`已创建独立 Proposal ${proposalId}`))).toBeInTheDocument();
    expect(screen.getByText(/审批应用前不会进入正式图/)).toBeInTheDocument();
  });

  it("改类型确认只提供端点兼容类型并序列化所选 Relation Type", async () => {
    const fetchMock = vi.fn<typeof fetch>((input) => {
      const url = requestUrl(input);
      if (url.pathname.endsWith("/candidates")) return Promise.resolve(jsonResponse(pagePayload));
      return Promise.resolve(jsonResponse({
        id: decisionId,
        candidate_id: candidateId,
        workspace_id: workspaceId,
        action: "CONFIRM_WITH_RELATION_TYPE",
        status: "PROPOSAL_CREATED",
        version: 4,
        proposal_id: proposalId,
        created_at: at,
        updated_at: at,
      }, 201));
    });
    renderPanel(fetchMock);
    fireEvent.click(screen.getByRole("button", { name: "审阅候选" }));
    await screen.findByText("Candidate · 未进入正式图");
    fireEvent.click(screen.getByRole("button", { name: "改类型后确认" }));
    const relationSelect = screen.getByLabelText("确认的关系类型");
    expect(screen.getByRole("option", { name: "归属于 · BELONGS_TO" })).toBeInTheDocument();
    expect(screen.getByRole("option", { name: "影响 · IMPACTS" })).toBeInTheDocument();
    expect(screen.queryByRole("option", { name: /支持 · SUPPORTS/ })).not.toBeInTheDocument();
    fireEvent.change(relationSelect, { target: { value: "IMPACTS" } });
    fireEvent.click(screen.getByRole("button", { name: "提交决策" }));
    await screen.findByText(new RegExp(`已创建独立 Proposal ${proposalId}`));

    const decisionRequest = fetchMock.mock.calls.find(([input]) => requestUrl(input).pathname.endsWith(`/candidates/${candidateId}/decisions`));
    const body = decisionRequest?.[1]?.body;
    if (typeof body !== "string") throw new Error("missing typed decision body");
    expect(JSON.parse(body)).toMatchObject({ action: "CONFIRM_WITH_RELATION_TYPE", relation_type: "IMPACTS" });
  });

  it("稍后和恢复动作保持各自状态语义", async () => {
    let deferred = false;
    const bodies: Record<string, unknown>[] = [];
    const fetchMock = vi.fn<typeof fetch>((input, init) => {
      const url = requestUrl(input);
      if (url.pathname.endsWith("/candidates")) {
        const payload = deferred ? { ...candidatePayload, status: "DEFERRED", version: 4 } : candidatePayload;
        return Promise.resolve(jsonResponse({ workspace_id: workspaceId, items: [payload] }));
      }
      if (typeof init?.body !== "string") return Promise.reject(new Error("missing decision body"));
      const body = JSON.parse(init.body) as Record<string, unknown>;
      bodies.push(body);
      deferred = body.action === "DEFER";
      return Promise.resolve(jsonResponse({
        id: decisionId,
        candidate_id: candidateId,
        workspace_id: workspaceId,
        action: body.action,
        status: deferred ? "DEFERRED" : "ACTIVE",
        version: deferred ? 4 : 5,
        proposal_id: null,
        created_at: at,
        updated_at: at,
      }, 201));
    });
    renderPanel(fetchMock);
    fireEvent.click(screen.getByRole("button", { name: "审阅候选" }));
    await screen.findByText("Candidate · 未进入正式图");
    fireEvent.click(screen.getByRole("button", { name: "稍后处理" }));
    fireEvent.change(screen.getByLabelText("备注（可选）"), { target: { value: "  等待 新 证据 " } });
    fireEvent.click(screen.getByRole("button", { name: "提交决策" }));
    await waitFor(() => expect(bodies).toHaveLength(1));
    expect(bodies[0]).toMatchObject({ action: "DEFER", reason: "等待 新 证据", deferred_until: null });

    fireEvent.change(screen.getByLabelText("处理状态"), { target: { value: "DEFERRED" } });
    await waitFor(() => expect(screen.getByRole("button", { name: "恢复处理" })).toBeInTheDocument());
    fireEvent.click(screen.getByRole("button", { name: "恢复处理" }));
    fireEvent.click(screen.getByRole("button", { name: "提交决策" }));
    await waitFor(() => expect(bodies).toHaveLength(2));
    expect(bodies[1]).toMatchObject({ action: "RESUME", expected_version: 4 });
  });
});
