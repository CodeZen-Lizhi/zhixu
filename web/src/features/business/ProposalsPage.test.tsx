import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes, useNavigate } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const workspaceId = "10000000-0000-4000-8000-000000000002";
const proposalId = "10000000-0000-4000-8000-000000000001";
const revisionId = "10000000-0000-4000-8000-000000000003";
const changeHash = "b".repeat(64);

const api = vi.hoisted(() => ({
  decideProposal: vi.fn(),
  getProposal: vi.fn(),
  getProposalCurrentContent: vi.fn(),
  listProposals: vi.fn(),
  preflightProposal: vi.fn(),
}));
type MonacoMode = "ready" | "loading" | "error";
interface MonacoDiffViewerMockProps {
  onReady: () => void;
  onError: (error: Error) => void;
}
const monaco = vi.hoisted((): { diffViewer: ReturnType<typeof vi.fn<(props: MonacoDiffViewerMockProps) => void>>; mode: MonacoMode } => ({
  diffViewer: vi.fn<(props: MonacoDiffViewerMockProps) => void>(),
  mode: "ready",
}));
const workspaceState = vi.hoisted(() => ({ id: "10000000-0000-4000-8000-000000000002" }));

vi.mock("../../api/business", async (importOriginal) => ({
  // eslint-disable-next-line @typescript-eslint/consistent-type-imports
  ...(await importOriginal<typeof import("../../api/business")>()),
  decideProposal: api.decideProposal,
  getProposal: api.getProposal,
  getProposalCurrentContent: api.getProposalCurrentContent,
  listProposals: api.listProposals,
  preflightProposal: api.preflightProposal,
}));
vi.mock("../../app/active-workspace", async (importOriginal) => ({
  // eslint-disable-next-line @typescript-eslint/consistent-type-imports
  ...(await importOriginal<typeof import("../../app/active-workspace")>()),
  getActiveWorkspaceId: () => workspaceState.id,
  useActiveWorkspaceId: () => workspaceState.id,
}));
vi.mock("./MonacoDiffViewer", () => ({
  MonacoDiffViewer: (props: MonacoDiffViewerMockProps) => {
    monaco.diffViewer(props);
    if (monaco.mode === "ready") queueMicrotask(() => props.onReady());
    if (monaco.mode === "error") queueMicrotask(() => props.onError(new Error("本地 Monaco worker 加载失败")));
    return <div>Diff Viewer</div>;
  },
}));

import { BusinessApiError } from "../../api/business";
import { ProposalDetailPage, ProposalsPage } from "./ProposalsPage";

const HistoryBackButton = () => {
  const navigate = useNavigate();
  return <button type="button" onClick={() => void navigate(-1)}>浏览器返回</button>;
};

const fileProposal = (overrides: Record<string, unknown> = {}) => ({
  id: proposalId,
  workspaceId,
  type: "file_patch" as const,
  targetPath: "docs/a.md",
  status: "ready_for_review" as const,
  riskLevel: "LOW" as const,
  revision: {
    id: revisionId,
    revisionNo: 1,
    baseHash: "a".repeat(64),
    content: "next",
    evidenceSummary: "evidence",
    risk: "变更影响有限",
    rollbackPlan: "revert",
    changeHash,
    createdAt: "2026-07-22T00:00:00Z",
  },
  createdAt: "2026-07-22T00:00:00Z",
  updatedAt: "2026-07-22T00:01:00Z",
  ...overrides,
});

const publishArtifactProposal = (overrides: Record<string, unknown> = {}) => ({
  id: proposalId,
  workspaceId,
  type: "publish_artifact" as const,
  status: "ready_for_review" as const,
  riskLevel: "HIGH" as const,
  revision: {
    id: revisionId,
    revisionNo: 1,
    publication: {
      workspaceId,
      artifactId: "10000000-0000-4000-8000-000000000006",
      revisionId: "10000000-0000-4000-8000-000000000007",
      revisionNo: 3,
      artifactVersion: 5,
      contentHash: "c".repeat(64),
      sourceCoverage: [
        { sectionKey: "intro", status: "COVERED" as const, gaps: [] },
        { sectionKey: "limits", status: "GAP" as const, gaps: [{ code: "NO_SOURCE", description: "没有可验证来源" }] },
      ],
      schemaVersion: "artifact-publication/v1" as const,
    },
    risk: "发布将影响正式知识边界",
    rollbackPlan: "保留隔离 Artifact",
    changeHash,
    createdAt: "2026-07-26T00:00:00Z",
  },
  createdAt: "2026-07-26T00:00:00Z",
  updatedAt: "2026-07-26T00:01:00Z",
  ...overrides,
});

const currentContent = (baseHashMatch = true, baseHash = "a".repeat(64)) => ({
  proposalId,
  workspaceId,
  targetPath: "docs/a.md",
  content: "current",
  currentHash: baseHashMatch ? baseHash : "c".repeat(64),
  baseHash,
  baseHashMatch,
});

const renderDetail = () => {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  const rendered = render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter initialEntries={[`/proposals/${proposalId}`]}>
        <Routes><Route path="/proposals/:proposalId" element={<ProposalDetailPage />} /></Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
  return { queryClient, unmount: rendered.unmount };
};

const renderList = (entry = "/proposals") => {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter initialEntries={[entry]}><ProposalsPage /><HistoryBackButton /></MemoryRouter>
    </QueryClientProvider>,
  );
};

beforeEach(() => {
  workspaceState.id = workspaceId;
  monaco.mode = "ready";
  api.decideProposal.mockResolvedValue({});
  api.getProposal.mockResolvedValue(fileProposal());
  api.getProposalCurrentContent.mockResolvedValue(currentContent());
  api.listProposals.mockResolvedValue({ items: [] });
  api.preflightProposal.mockResolvedValue({ proposalId, revisionId, changeHash, baseHash: "a".repeat(64), preflightPassed: true, mode: "preflight_only", writePerformed: false });
});

afterEach(() => {
  cleanup();
  api.decideProposal.mockReset();
  api.getProposal.mockReset();
  api.getProposalCurrentContent.mockReset();
  api.listProposals.mockReset();
  api.preflightProposal.mockReset();
  monaco.diffViewer.mockReset();
});

describe("ProposalsPage", () => {
  it("从 URL 恢复 uppercase 风险等级并按同一值请求列表", async () => {
    renderList("/proposals?risk=HIGH");

    expect(await screen.findByRole("combobox", { name: "风险等级" })).toHaveValue("HIGH");
    await waitFor(() => expect(api.listProposals).toHaveBeenCalledWith(workspaceId, expect.objectContaining({ risk: "HIGH" }), expect.any(AbortSignal)));
  });

  it("从 URL 恢复 publish_artifact 筛选并显示独立类型", async () => {
    api.listProposals.mockResolvedValue({
      items: [{
        id: proposalId,
        workspaceId,
        type: "publish_artifact",
        status: "ready_for_review",
        target: "Artifact 发布",
        riskLevel: "HIGH",
        risk: "发布将影响正式知识边界",
        revisionId,
        changeHash,
        createdAt: "2026-07-26T00:00:00Z",
        updatedAt: "2026-07-26T00:01:00Z",
      }],
    });

    renderList("/proposals?proposal_type=publish_artifact");

    expect(await screen.findByRole("combobox", { name: "类型" })).toHaveValue("publish_artifact");
    await waitFor(() => expect(api.listProposals).toHaveBeenCalledWith(workspaceId, expect.objectContaining({ type: "publish_artifact" }), expect.any(AbortSignal)));
    expect(screen.getAllByText("Artifact 发布").length).toBeGreaterThanOrEqual(2);
    expect(screen.getByText("风险说明：发布将影响正式知识边界")).toBeInTheDocument();
  });

  it("展示列表中的持久 Approval 与 Workflow 绑定", async () => {
    const workflowRunId = "10000000-0000-4000-8000-000000000008";
    api.listProposals.mockResolvedValue({
      items: [{
        id: proposalId,
        workspaceId,
        type: "file_patch",
        status: "approved",
        target: "docs/a.md",
        riskLevel: "LOW",
        risk: "低风险说明",
        revisionId,
        changeHash,
        approval: {
          id: "10000000-0000-4000-8000-000000000004",
          proposalId,
          revisionId,
          changeHash,
          decision: "approved",
          approvedGitHead: "c".repeat(40),
          workflowRunId,
          workflowStatusUrl: `/api/v1/workflows/${workflowRunId}`,
          decidedAt: "2026-07-22T00:02:00Z",
        },
        createdAt: "2026-07-22T00:00:00Z",
        updatedAt: "2026-07-22T00:02:00Z",
      }],
    });

    renderList();

    expect(await screen.findByText(`Workflow ${workflowRunId.slice(0, 8)}…`)).toBeInTheDocument();
    expect(screen.getAllByText("approved")).toHaveLength(2);
    expect(screen.getByText("风险等级 LOW")).toBeInTheDocument();
    expect(screen.getByText("风险说明：低风险说明")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /docs\/a\.md/ })).toHaveAttribute("href", `/proposals/${proposalId}`);
  });

  it("区分可重派发与只能人工恢复的历史 Approval", async () => {
    api.listProposals.mockResolvedValue({
      items: [
        {
          id: proposalId,
          workspaceId,
          type: "file_patch",
          status: "approved",
          target: "docs/pending.md",
          riskLevel: "LOW",
          risk: "待恢复写回说明",
          revisionId,
          changeHash,
          approval: {
            id: "10000000-0000-4000-8000-000000000004",
            proposalId,
            revisionId,
            changeHash,
            decision: "approved",
            approvedGitHead: "c".repeat(40),
            writebackState: "pending_dispatch",
            decidedAt: "2026-07-22T00:02:00Z",
          },
          createdAt: "2026-07-22T00:00:00Z",
          updatedAt: "2026-07-22T00:02:00Z",
        },
        {
          id: "10000000-0000-4000-8000-000000000009",
          workspaceId,
          type: "file_patch",
          status: "approved",
          target: "docs/manual.md",
          riskLevel: "HIGH",
          risk: "历史审批说明",
          revisionId: "10000000-0000-4000-8000-000000000010",
          changeHash: "d".repeat(64),
          approval: {
            id: "10000000-0000-4000-8000-000000000011",
            proposalId: "10000000-0000-4000-8000-000000000009",
            revisionId: "10000000-0000-4000-8000-000000000010",
            changeHash: "d".repeat(64),
            decision: "approved",
            writebackState: "legacy_unrecoverable",
            decidedAt: "2026-07-22T00:03:00Z",
          },
          createdAt: "2026-07-22T00:00:00Z",
          updatedAt: "2026-07-22T00:03:00Z",
        },
      ],
    });

    renderList();

    expect(await screen.findByText("待恢复写回")).toBeInTheDocument();
    expect(screen.getByText("历史审批缺少 Git 基线")).toBeInTheDocument();
  });

  it("没有活动 Workspace 时显示恢复入口且不发列表请求", () => {
    workspaceState.id = "";
    renderList();

    expect(screen.getByText("先连接 Workspace")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "连接或切换 Workspace" })).toHaveAttribute("href", "/workspace");
    expect(api.listProposals).not.toHaveBeenCalled();
  });

  it("浏览器历史恢复旧筛选时从第一页请求，不复活当前筛选的游标", async () => {
    api.listProposals.mockResolvedValue({ items: [], nextCursor: "proposal-page-2" });
    renderList();
    await waitFor(() => expect(api.listProposals).toHaveBeenLastCalledWith(
      workspaceId,
      { limit: 30 },
      expect.any(AbortSignal),
    ));

    fireEvent.change(screen.getByRole("combobox", { name: "风险等级" }), { target: { value: "HIGH" } });
    await waitFor(() => expect(api.listProposals).toHaveBeenLastCalledWith(
      workspaceId,
      { limit: 30, risk: "HIGH" },
      expect.any(AbortSignal),
    ));
    fireEvent.click(screen.getByRole("button", { name: "下一页" }));
    await waitFor(() => expect(api.listProposals).toHaveBeenLastCalledWith(
      workspaceId,
      { limit: 30, cursor: "proposal-page-2", risk: "HIGH" },
      expect.any(AbortSignal),
    ));

    fireEvent.click(screen.getByRole("button", { name: "浏览器返回" }));

    await waitFor(() => expect(api.listProposals).toHaveBeenLastCalledWith(
      workspaceId,
      { limit: 30 },
      expect.any(AbortSignal),
    ));
  });
});

describe("ProposalDetailPage", () => {
  it("分开展示受控风险等级与 Revision 风险说明", async () => {
    renderDetail();

    expect(await screen.findAllByText("LOW · 低")).toHaveLength(2);
    expect(screen.getAllByText("变更影响有限")).toHaveLength(2);
  });

  it("按 Workspace 和 Proposal 隔离 Diff Model", async () => {
    renderDetail();

    expect(await screen.findByText("Diff Viewer")).toBeInTheDocument();
    expect(monaco.diffViewer).toHaveBeenCalledWith(expect.objectContaining({
      originalModelPath: `inmemory://zhixu/workspaces/${workspaceId}/proposals/${proposalId}/revisions/${revisionId}/original.md`,
      modifiedModelPath: `inmemory://zhixu/workspaces/${workspaceId}/proposals/${proposalId}/revisions/${revisionId}/modified.md`,
      identityKey: `${workspaceId}:${proposalId}:${revisionId}:${"a".repeat(64)}:${changeHash}:${"a".repeat(64)}:0`,
    }));
    expect(api.getProposalCurrentContent).toHaveBeenCalledWith(workspaceId, proposalId, {
      targetPath: "docs/a.md",
      baseHash: "a".repeat(64),
    }, expect.any(AbortSignal));
  });

  it("离开详情后立即清除 Proposal 正文与当前正文缓存", async () => {
    const { queryClient, unmount } = renderDetail();

    expect(await screen.findByText("Diff Viewer")).toBeInTheDocument();
    expect(queryClient.getQueryData(["business", workspaceId, "proposal", proposalId])).toBeDefined();
    expect(queryClient.getQueriesData({ queryKey: ["business", workspaceId, "proposal-current-content", proposalId] })).toHaveLength(1);

    unmount();
    await waitFor(() => {
      expect(queryClient.getQueryData(["business", workspaceId, "proposal", proposalId])).toBeUndefined();
      expect(queryClient.getQueriesData({ queryKey: ["business", workspaceId, "proposal-current-content", proposalId] })).toHaveLength(0);
    });
  });

  it("首次批准直接提交审批命令，不调用只允许 approved 状态的 apply preflight", async () => {
    renderDetail();

    expect(await screen.findByText("Diff Viewer")).toBeInTheDocument();
    await waitFor(() => expect(screen.getByRole("button", { name: "批准" })).toBeEnabled());
    fireEvent.click(screen.getByRole("button", { name: "批准" }));
    fireEvent.click(screen.getByRole("button", { name: "确认提交" }));

    await waitFor(() => expect(api.decideProposal).toHaveBeenCalledWith(proposalId, { revisionId, changeHash, decision: "approved", proposalType: "file_patch" }));
    expect(api.preflightProposal).not.toHaveBeenCalled();
  });

  it("Diff 挂载成功前禁用文件批准但保持驳回可用", async () => {
    monaco.mode = "loading";
    renderDetail();

    expect(await screen.findByText("Diff Viewer")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "批准" })).toBeDisabled();
    expect(screen.getByText("等待 Diff Viewer 成功挂载后才能批准；驳回仍可提交。")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "驳回" }));
    fireEvent.click(screen.getByRole("button", { name: "确认提交" }));

    await waitFor(() => expect(api.decideProposal).toHaveBeenCalledWith(proposalId, { revisionId, changeHash, decision: "rejected", proposalType: "file_patch" }));
  });

  it("Diff 加载失败时显式报错、禁用批准且允许驳回", async () => {
    monaco.mode = "error";
    renderDetail();

    expect(await screen.findByRole("alert")).toHaveTextContent("Diff Viewer 加载失败");
    expect(screen.getByText("本地 Monaco worker 加载失败")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "批准" })).toBeDisabled();
    fireEvent.click(screen.getByRole("button", { name: "驳回" }));
    fireEvent.click(screen.getByRole("button", { name: "确认提交" }));

    await waitFor(() => expect(api.decideProposal).toHaveBeenCalledWith(proposalId, { revisionId, changeHash, decision: "rejected", proposalType: "file_patch" }));
  });

  it("确认框绑定用户打开时的 Revision，Revision 漂移后立即关闭且不提交", async () => {
    const nextRevisionId = "10000000-0000-4000-8000-000000000006";
    const nextProposal = fileProposal({
      revision: {
        ...fileProposal().revision,
        id: nextRevisionId,
        revisionNo: 2,
        changeHash: "e".repeat(64),
      },
    });
    const { queryClient } = renderDetail();

    expect(await screen.findByText("Diff Viewer")).toBeInTheDocument();
    const approve = screen.getByRole("button", { name: "批准" });
    await waitFor(() => expect(approve).toBeEnabled());
    fireEvent.click(approve);
    expect(screen.getByRole("dialog")).toBeInTheDocument();

    act(() => { queryClient.setQueryData(["business", workspaceId, "proposal", proposalId], nextProposal); });

    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(api.decideProposal).not.toHaveBeenCalled();
  });

  it("确认框把风险等级绑定到打开时快照，等级漂移后立即关闭", async () => {
    const { queryClient } = renderDetail();

    expect(await screen.findByText("Diff Viewer")).toBeInTheDocument();
    const approve = screen.getByRole("button", { name: "批准" });
    await waitFor(() => expect(approve).toBeEnabled());
    fireEvent.click(approve);
    expect(screen.getByRole("dialog")).toBeInTheDocument();

    act(() => {
      queryClient.setQueryData(["business", workspaceId, "proposal", proposalId], fileProposal({ riskLevel: "HIGH" }));
    });

    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(api.decideProposal).not.toHaveBeenCalled();
  });

  it("确认提交使用打开时的 Revision 快照，不重读提交瞬间的 Query", async () => {
    const nextRevisionId = "10000000-0000-4000-8000-000000000006";
    const nextChangeHash = "e".repeat(64);
    const nextProposal = fileProposal({
      revision: {
        ...fileProposal().revision,
        id: nextRevisionId,
        revisionNo: 2,
        changeHash: nextChangeHash,
      },
    });
    let resolveDecision: ((value: unknown) => void) | undefined;
    api.decideProposal.mockImplementation(() => new Promise((resolve) => { resolveDecision = resolve; }));
    const { queryClient } = renderDetail();

    expect(await screen.findByText("Diff Viewer")).toBeInTheDocument();
    const approve = screen.getByRole("button", { name: "批准" });
    await waitFor(() => expect(approve).toBeEnabled());
    fireEvent.click(approve);
    fireEvent.click(screen.getByRole("button", { name: "确认提交" }));
    act(() => { queryClient.setQueryData(["business", workspaceId, "proposal", proposalId], nextProposal); });

    await waitFor(() => expect(api.decideProposal).toHaveBeenCalledWith(proposalId, {
      revisionId,
      changeHash,
      decision: "approved",
      proposalType: "file_patch",
    }));
    act(() => { resolveDecision?.({}); });
    await waitFor(() => {
      expect(queryClient.isMutating()).toBe(0);
      expect(queryClient.isFetching()).toBe(0);
    });
  });

  it("只为已批准的文件 Proposal 提供独立写回前检查", async () => {
    const workflowRunId = "10000000-0000-4000-8000-000000000008";
    api.getProposal.mockResolvedValue(fileProposal({
      status: "approved",
      approval: {
        id: "10000000-0000-4000-8000-000000000004",
        proposalId,
        revisionId,
        changeHash,
        decision: "approved",
        approvedGitHead: "c".repeat(40),
        workflowRunId,
        workflowStatusUrl: `/api/v1/workflows/${workflowRunId}`,
        decidedAt: "2026-07-22T00:02:00Z",
      },
    }));
    renderDetail();

    const preflightButton = await screen.findByRole("button", { name: "执行 Apply Preflight" });
    await waitFor(() => expect(preflightButton).toBeEnabled());
    fireEvent.click(preflightButton);
    await waitFor(() => expect(api.preflightProposal).toHaveBeenCalledWith(proposalId, { revisionId, changeHash }));
    expect(await screen.findByText("写回前检查通过")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: workflowRunId })).toHaveAttribute("href", `/workflows/${workflowRunId}`);
    expect(screen.queryByText("queued")).not.toBeInTheDocument();
    expect(api.decideProposal).not.toHaveBeenCalled();
  });

  it("历史批准有 Git 基线时可复用 approved 命令恢复唯一 Workflow", async () => {
    api.getProposal.mockResolvedValue(fileProposal({
      status: "approved",
      approval: {
        id: "10000000-0000-4000-8000-000000000004",
        proposalId,
        revisionId,
        changeHash,
        decision: "approved",
        approvedGitHead: "c".repeat(40),
        writebackState: "pending_dispatch",
        decidedAt: "2026-07-22T00:02:00Z",
      },
    }));
    renderDetail();

    const redispatch = await screen.findByRole("button", { name: "恢复写回 Workflow" });
    await waitFor(() => expect(redispatch).toBeEnabled());
    fireEvent.click(redispatch);
    expect(screen.getByRole("dialog")).toHaveTextContent("确认恢复写回 Workflow");
    fireEvent.click(screen.getByRole("button", { name: "确认提交" }));

    await waitFor(() => expect(api.decideProposal).toHaveBeenCalledWith(proposalId, {
      revisionId,
      changeHash,
      decision: "approved",
      proposalType: "file_patch",
    }));
  });

  it("历史批准恢复派发必须等待 Diff 挂载成功", async () => {
    monaco.mode = "loading";
    api.getProposal.mockResolvedValue(fileProposal({
      status: "approved",
      approval: {
        id: "10000000-0000-4000-8000-000000000004",
        proposalId,
        revisionId,
        changeHash,
        decision: "approved",
        approvedGitHead: "c".repeat(40),
        writebackState: "pending_dispatch",
        decidedAt: "2026-07-22T00:02:00Z",
      },
    }));
    renderDetail();

    expect(await screen.findByText("Diff Viewer")).toBeInTheDocument();
    const redispatch = await screen.findByRole("button", { name: "恢复写回 Workflow" });
    expect(redispatch).toBeDisabled();
    expect(screen.getByText("等待 Diff Viewer 成功挂载后才能恢复派发。")).toBeInTheDocument();
    expect(api.decideProposal).not.toHaveBeenCalled();
  });

  it("历史批准缺少 Git 基线时保持只读且不提供恢复按钮", async () => {
    api.getProposal.mockResolvedValue(fileProposal({
      status: "approved",
      approval: {
        id: "10000000-0000-4000-8000-000000000004",
        proposalId,
        revisionId,
        changeHash,
        decision: "approved",
        writebackState: "legacy_unrecoverable",
        decidedAt: "2026-07-22T00:02:00Z",
      },
    }));
    renderDetail();

    expect(await screen.findByText("历史审批缺少 Git 基线")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "恢复写回 Workflow" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "批准" })).not.toBeInTheDocument();
  });

  it("重新读取发现基线漂移后立即隐藏旧的 Preflight 成功", async () => {
    const workflowRunId = "10000000-0000-4000-8000-000000000008";
    api.getProposal.mockResolvedValue(fileProposal({
      status: "approved",
      approval: {
        id: "10000000-0000-4000-8000-000000000004",
        proposalId,
        revisionId,
        changeHash,
        decision: "approved",
        approvedGitHead: "c".repeat(40),
        workflowRunId,
        workflowStatusUrl: `/api/v1/workflows/${workflowRunId}`,
        writebackState: "bound",
        decidedAt: "2026-07-22T00:02:00Z",
      },
    }));
    api.getProposalCurrentContent
      .mockResolvedValueOnce(currentContent(true))
      .mockResolvedValueOnce(currentContent(false));
    renderDetail();

    const preflightButton = await screen.findByRole("button", { name: "执行 Apply Preflight" });
    await waitFor(() => expect(preflightButton).toBeEnabled());
    fireEvent.click(preflightButton);
    expect(await screen.findByText("写回前检查通过")).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "重新读取" }));

    expect(await screen.findByText("版本冲突：基线已经漂移")).toBeInTheDocument();
    expect(screen.queryByText("写回前检查通过")).not.toBeInTheDocument();
  });

  it("knowledge change 可以批准且不会读取文件正文", async () => {
    api.getProposal.mockReset().mockResolvedValue({
      id: proposalId,
      workspaceId,
      type: "knowledge_change",
      status: "ready_for_review",
      riskLevel: "MEDIUM",
      revision: {
        id: revisionId,
        revisionNo: 1,
        schemaVersion: "knowledge-relation-change/v1",
        targetRefs: [{ type: "RELATION_CANDIDATE", id: "10000000-0000-4000-8000-000000000004", fingerprint: "d".repeat(64) }],
        baseVersions: [{ nodeType: "CLAIM", nodeId: "10000000-0000-4000-8000-000000000005", version: 1 }, { nodeType: "TOPIC", nodeId: "10000000-0000-4000-8000-000000000006", version: 2 }],
        changeSet: { operation: "CREATE_RELATION", source: { type: "CLAIM", id: "10000000-0000-4000-8000-000000000005", version: 1 }, target: { type: "TOPIC", id: "10000000-0000-4000-8000-000000000006", version: 2 }, relationType: "SUPPORTS" },
        evidenceRefs: [{ candidateEvidenceId: "10000000-0000-4000-8000-000000000007", semanticHash: "e".repeat(64) }],
        risk: "medium",
        rollbackPlan: "remove relation",
        changeHash,
        createdAt: "2026-07-22T00:00:00Z",
      },
      createdAt: "2026-07-22T00:00:00Z",
      updatedAt: "2026-07-22T00:01:00Z",
    });
    api.getProposalCurrentContent.mockClear();
    const { queryClient } = renderDetail();
    const invalidate = vi.spyOn(queryClient, "invalidateQueries").mockResolvedValue(undefined);

    expect(await screen.findByText("Relation Diff")).toBeInTheDocument();
    expect(screen.getByText("RELATION_CANDIDATE:10000000-0000-4000-8000-000000000004")).toBeInTheDocument();
    expect(screen.getByText(/fingerprint: d{64}/)).toBeInTheDocument();
    expect(screen.getAllByText("CLAIM:10000000-0000-4000-8000-000000000005 · v1")).toHaveLength(2);
    expect(screen.getAllByText("TOPIC:10000000-0000-4000-8000-000000000006 · v2")).toHaveLength(2);
    expect(screen.getByText("10000000-0000-4000-8000-000000000007")).toBeInTheDocument();
    expect(screen.getByText(/semantic hash: e{64}/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "批准" }));
    fireEvent.click(screen.getByRole("button", { name: "确认提交" }));

    await waitFor(() => expect(api.decideProposal).toHaveBeenCalledWith(proposalId, { revisionId, changeHash, decision: "approved", proposalType: "knowledge_change" }));
    expect(api.getProposalCurrentContent).not.toHaveBeenCalled();
    expect(invalidate).toHaveBeenCalledWith({ queryKey: ["graph", workspaceId] });
    expect(invalidate).toHaveBeenCalledWith({ queryKey: ["semantic-links", workspaceId] });
    expect(invalidate).toHaveBeenCalledWith({ queryKey: ["collections", workspaceId] });
    expect(invalidate).toHaveBeenCalledWith({ queryKey: ["knowledge-health", workspaceId] });
  });

  it("publish_artifact 展示冻结来源覆盖，并把 Approval 与正式知识写入分开", async () => {
    api.getProposal.mockReset().mockResolvedValue(publishArtifactProposal());
    api.getProposalCurrentContent.mockClear();
    renderDetail();

    expect(await screen.findByText("Artifact Publication Snapshot")).toBeInTheDocument();
    expect(screen.getByText("10000000-0000-4000-8000-000000000006")).toBeInTheDocument();
    expect(screen.getByText(/#3 ·/)).toHaveTextContent("10000000-0000-4000-8000-000000000007");
    expect(screen.getByText("v5")).toBeInTheDocument();
    expect(screen.getAllByText("c".repeat(64))).toHaveLength(2);
    expect(screen.getByText("COVERED · 无知识缺口")).toBeInTheDocument();
    expect(screen.getByText("GAP · NO_SOURCE：没有可验证来源")).toBeInTheDocument();
    expect(screen.getByText("尚未成为正式知识")).toBeInTheDocument();
    expect(screen.getByText(/即使 Approval 为 approved，本页也不表示已创建 Document、Git 写入或索引/)).toBeInTheDocument();
    expect(api.getProposalCurrentContent).not.toHaveBeenCalled();
    expect(screen.queryByRole("button", { name: "执行 Apply Preflight" })).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "批准" }));
    expect(screen.getByRole("dialog")).toHaveTextContent("批准只形成 Approval，不表示已经创建 Document、Git 写入或索引");
    fireEvent.click(screen.getByRole("button", { name: "确认提交" }));

    await waitFor(() => expect(api.decideProposal).toHaveBeenCalledWith(proposalId, {
      revisionId,
      changeHash,
      decision: "approved",
      proposalType: "publish_artifact",
    }));
    expect(api.preflightProposal).not.toHaveBeenCalled();
  });

  it("基线漂移时阻止批准并给出恢复说明", async () => {
    api.getProposalCurrentContent.mockResolvedValue(currentContent(false));
    renderDetail();

    expect(await screen.findByRole("alert")).toHaveTextContent("版本冲突：基线已经漂移");
    expect(screen.queryByRole("button", { name: "批准" })).not.toBeInTheDocument();
    expect(screen.getByText("批准已被阻止；请重新生成 Proposal Revision。")).toBeInTheDocument();
  });

  it("审批 409 后重新读取 Proposal 与当前文件", async () => {
    api.decideProposal.mockRejectedValueOnce(new BusinessApiError("HTTP_ERROR", "版本冲突", false, 409));
    renderDetail();

    expect(await screen.findByText("Diff Viewer")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "批准" }));
    fireEvent.click(screen.getByRole("button", { name: "确认提交" }));

    await waitFor(() => expect(api.getProposal).toHaveBeenCalledTimes(2));
    expect(api.getProposalCurrentContent).toHaveBeenCalledTimes(2);
    expect(screen.getByRole("alert")).toHaveTextContent("版本冲突");
  });

  it("Revision 更新后按新绑定顺序读取 current-content，完成前保持批准禁用", async () => {
    const nextRevisionId = "10000000-0000-4000-8000-000000000006";
    const nextBaseHash = "f".repeat(64);
    const nextChangeHash = "e".repeat(64);
    const nextProposal = fileProposal({
      revision: {
        ...fileProposal().revision,
        id: nextRevisionId,
        revisionNo: 2,
        baseHash: nextBaseHash,
        content: "next revision",
        changeHash: nextChangeHash,
      },
    });
    let resolveNextContent: ((value: ReturnType<typeof currentContent>) => void) | undefined;
    const nextContentPending = new Promise<ReturnType<typeof currentContent>>((resolve) => { resolveNextContent = resolve; });
    api.getProposal
      .mockReset()
      .mockResolvedValueOnce(fileProposal())
      .mockResolvedValueOnce(nextProposal);
    api.getProposalCurrentContent
      .mockReset()
      .mockResolvedValueOnce(currentContent())
      .mockImplementationOnce(() => nextContentPending);
    api.decideProposal.mockRejectedValueOnce(new BusinessApiError("HTTP_ERROR", "版本冲突", false, 409));
    renderDetail();

    expect(await screen.findByText("Diff Viewer")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "批准" }));
    fireEvent.click(screen.getByRole("button", { name: "确认提交" }));

    await waitFor(() => expect(api.getProposalCurrentContent).toHaveBeenCalledWith(workspaceId, proposalId, {
      targetPath: "docs/a.md",
      baseHash: nextBaseHash,
    }, expect.any(AbortSignal)));
    expect(screen.getByRole("button", { name: "批准" })).toBeDisabled();

    resolveNextContent?.(currentContent(true, nextBaseHash));
    await waitFor(() => expect(screen.getByRole("button", { name: "批准" })).toBeEnabled());
    expect(monaco.diffViewer).toHaveBeenLastCalledWith(expect.objectContaining({
      modified: "next revision",
      originalModelPath: `inmemory://zhixu/workspaces/${workspaceId}/proposals/${proposalId}/revisions/${nextRevisionId}/original.md`,
    }));
  });

  it("没有活动 Workspace 时显示恢复入口且不读取详情", () => {
    workspaceState.id = "";
    renderDetail();

    expect(screen.getByText("先连接 Workspace")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "连接或切换 Workspace" })).toHaveAttribute("href", "/workspace");
    expect(api.getProposal).not.toHaveBeenCalled();
    expect(api.getProposalCurrentContent).not.toHaveBeenCalled();
  });

  it.each(["HIGH", "CRITICAL"] as const)("%s 风险等级触发高风险确认并在取消后恢复焦点", async (riskLevel) => {
    api.getProposal.mockResolvedValue(fileProposal({ riskLevel, revision: { ...fileProposal().revision, risk: "普通说明文本" } }));
    renderDetail();

    const approve = await screen.findByRole("button", { name: "批准" });
    await waitFor(() => expect(approve).toBeEnabled());
    approve.focus();
    fireEvent.click(approve);
    expect(await screen.findByRole("dialog")).toHaveTextContent("高风险变更：再次确认批准");
    fireEvent.click(screen.getByRole("button", { name: "取消" }));
    await waitFor(() => expect(approve).toHaveFocus());
  });

  it.each(["LOW", "MEDIUM"] as const)("%s 等级不会被风险说明中的严重词误判为高风险", async (riskLevel) => {
    api.getProposal.mockResolvedValue(fileProposal({ riskLevel, revision: { ...fileProposal().revision, risk: "critical 严重高风险说明" } }));
    renderDetail();

    const approve = await screen.findByRole("button", { name: "批准" });
    await waitFor(() => expect(approve).toBeEnabled());
    fireEvent.click(approve);

    expect(await screen.findByRole("dialog")).toHaveTextContent("确认批准这项变更");
    expect(screen.getByRole("dialog")).not.toHaveTextContent("高风险变更");
  });
});
