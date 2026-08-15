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
const revisionApi = vi.hoisted(() => ({
  listProposalRevisions: vi.fn(),
  getProposalRevision: vi.fn(),
  workbenchProps: undefined as {
    authorityEditable: boolean;
    onClose: () => void;
    onRevisionCreated: (result: unknown) => Promise<void> | void;
    onAuthorityStale: () => Promise<void> | void;
  } | undefined,
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
vi.mock("../../api/business-revisions", async (importOriginal) => ({
  // eslint-disable-next-line @typescript-eslint/consistent-type-imports
  ...(await importOriginal<typeof import("../../api/business-revisions")>()),
  listProposalRevisions: revisionApi.listProposalRevisions,
  getProposalRevision: revisionApi.getProposalRevision,
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
vi.mock("../../shared/MonacoDiffViewer", () => ({
  MonacoDiffViewer: (props: MonacoDiffViewerMockProps & { original: string; modified: string }) => {
    monaco.diffViewer(props);
    queueMicrotask(() => props.onReady());
    return <div>Historical Diff Viewer</div>;
  },
}));
vi.mock("./ProposalRevisionWorkbench", () => ({
  ProposalRevisionWorkbench: (props: {
    authorityEditable: boolean;
    onClose: () => void;
    onRevisionCreated: (result: unknown) => Promise<void> | void;
    onAuthorityStale: () => Promise<void> | void;
  }) => {
    revisionApi.workbenchProps = props;
    return <section aria-label="Revision 工作台">
      <h2>Revision Workbench</h2>
      <p>{props.authorityEditable ? "权威可编辑" : "权威不可编辑，草稿已保留"}</p>
      <button type="button" onClick={props.onClose}>返回审阅</button>
    </section>;
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
  version: 1,
  revisionCapability: { editable: true, reason: "AVAILABLE" as const },
  revision: {
    id: revisionId,
    revisionNo: 1,
    targetMode: "REPLACE" as const,
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

const restoreDocumentId = "10000000-0000-4000-8000-000000000006";
const restoreTargetCommit = "c".repeat(40);
const restoreExpectedHead = "d".repeat(40);
const restorePreviewHash = "e".repeat(64);
const restoreTargetContentHash = "f".repeat(64);
const restoreProposal = (overrides: Record<string, unknown> = {}) => ({
  id: proposalId,
  workspaceId,
  type: "restore_document" as const,
  targetPath: "docs/a.md",
  status: "ready_for_review" as const,
  riskLevel: "HIGH" as const,
  revision: {
    id: revisionId,
    revisionNo: 1,
    targetMode: "REPLACE" as const,
    baseHash: "a".repeat(64),
    content: "historic\n",
    evidenceSummary: "恢复到经过审阅的历史版本",
    risk: "恢复会覆盖当前工作区正文",
    rollbackPlan: "通过新的恢复 Proposal 回到当前 Commit",
    changeHash,
    createdAt: "2026-08-02T00:00:00Z",
    restore: {
      workspaceId,
      documentId: restoreDocumentId,
      targetCommit: restoreTargetCommit,
      expectedHead: restoreExpectedHead,
      expectedDocumentVersion: 7,
      previewHash: restorePreviewHash,
      currentContentHash: "a".repeat(64),
      targetContentHash: restoreTargetContentHash,
      schemaVersion: "document-restore/v1" as const,
    },
  },
  createdAt: "2026-08-02T00:00:00Z",
  updatedAt: "2026-08-02T00:01:00Z",
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

const downstreamArtifactProposal = (overrides: Record<string, unknown> = {}) => ({
  id: proposalId,
  workspaceId,
  type: "downstream_update" as const,
  status: "ready_for_review" as const,
  riskLevel: "HIGH" as const,
  revision: {
    id: revisionId,
    revisionNo: 1,
    update: {
      workspaceId,
      sourceReport: {
        id: "10000000-0000-4000-8000-000000000006",
        analysisVersion: "impact-analysis/v2" as const,
        fingerprint: "a".repeat(64),
      },
      sourceEvent: { id: "10000000-0000-4000-8000-000000000007", eventVersion: 2 },
      targetType: "ARTIFACT" as const,
      targetId: "10000000-0000-4000-8000-000000000008",
      baseVersion: 5,
      action: "REGENERATE_ARTIFACT" as const,
      artifactBinding: {
        artifactId: "10000000-0000-4000-8000-000000000008",
        artifactVersion: 5,
        revisionId: "10000000-0000-4000-8000-000000000009",
        revisionNo: 3,
        contentHash: "b".repeat(64),
      },
      reason: "引用来源发生变化",
      schemaVersion: "impact-downstream-update/v1" as const,
    },
    risk: "Impact report identified an owner-backed downstream dependency",
    rollbackPlan: "No target write has executed",
    changeHash,
    createdAt: "2026-07-28T00:00:00Z",
  },
  createdAt: "2026-07-28T00:00:00Z",
  updatedAt: "2026-07-28T00:01:00Z",
  ...overrides,
});

const downstreamReviewCardProposal = (overrides: Record<string, unknown> = {}) => {
  const proposal = downstreamArtifactProposal();
  return {
    ...proposal,
    revision: {
      ...proposal.revision,
      update: {
        ...proposal.revision.update,
        targetType: "REVIEW_CARD" as const,
        targetId: "10000000-0000-4000-8000-000000000010",
        baseVersion: 4,
        action: "REVALIDATE_REVIEW_CARD" as const,
        artifactBinding: undefined,
        reviewCardBinding: {
          cardId: "10000000-0000-4000-8000-000000000010",
          cardVersion: 4,
          status: "INVALIDATED" as const,
          fingerprint: "d".repeat(64),
          claimId: "10000000-0000-4000-8000-000000000011",
          evidenceBindingFingerprint: "e".repeat(64),
        },
      },
    },
    ...overrides,
  };
};

const currentContent = (baseHashMatch = true, baseHash = "a".repeat(64)) => ({
  proposalId,
  workspaceId,
  targetPath: "docs/a.md",
  targetMode: "REPLACE" as const,
  content: "current",
  currentHash: baseHashMatch ? baseHash : "c".repeat(64),
  baseHash,
  baseHashMatch,
});

const oldRevisionId = "10000000-0000-4000-8000-000000000009";
const historyItem = (id: string, revisionNo: number, current: boolean) => ({
  proposalId,
  revisionId: id,
  revisionNo,
  targetPath: "docs/a.md",
  targetMode: "REPLACE" as const,
  baseHash: "a".repeat(64),
  changeHash: current ? changeHash : "d".repeat(64),
  baseAvailable: true,
  current,
  createdAt: current ? "2026-07-22T00:00:00Z" : "2026-07-21T00:00:00Z",
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
  api.preflightProposal.mockResolvedValue({ proposalId, revisionId, changeHash, targetMode: "REPLACE", baseHash: "a".repeat(64), preflightPassed: true, mode: "preflight_only", writePerformed: false });
  revisionApi.listProposalRevisions.mockResolvedValue({ items: [historyItem(revisionId, 1, true)] });
  revisionApi.getProposalRevision.mockResolvedValue({});
  revisionApi.workbenchProps = undefined;
});

afterEach(() => {
  cleanup();
  api.decideProposal.mockReset();
  api.getProposal.mockReset();
  api.getProposalCurrentContent.mockReset();
  api.listProposals.mockReset();
  api.preflightProposal.mockReset();
  revisionApi.listProposalRevisions.mockReset();
  revisionApi.getProposalRevision.mockReset();
  revisionApi.workbenchProps = undefined;
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
    expect(screen.getAllByText("产物发布").length).toBeGreaterThanOrEqual(2);
    expect(screen.getByText("风险说明：发布将影响正式知识边界")).toBeInTheDocument();
  });

  it("从 URL 恢复 restore_document 筛选并明确显示文档恢复", async () => {
    api.listProposals.mockResolvedValue({
      items: [{
        id: proposalId,
        workspaceId,
        type: "restore_document",
        status: "ready_for_review",
        target: "docs/a.md",
        riskLevel: "HIGH",
        risk: "恢复会覆盖当前工作区正文",
        revisionId,
        changeHash,
        createdAt: "2026-08-02T00:00:00Z",
        updatedAt: "2026-08-02T00:01:00Z",
      }],
    });

    renderList("/proposals?proposal_type=restore_document");

    expect(await screen.findByRole("combobox", { name: "类型" })).toHaveValue("restore_document");
    await waitFor(() => expect(api.listProposals).toHaveBeenCalledWith(workspaceId, expect.objectContaining({ type: "restore_document" }), expect.any(AbortSignal)));
    expect(screen.getByText("docs/a.md")).toBeInTheDocument();
    expect(document.querySelector(".proposal-row small")).toHaveTextContent("文档恢复");
    expect(document.querySelector(".kind-mark--restore_document svg")).toBeInTheDocument();
  });

  it("从 URL 恢复 downstream_update 筛选并显示独立标签与图标", async () => {
    api.listProposals.mockResolvedValue({
      items: [{
        id: proposalId,
        workspaceId,
        type: "downstream_update",
        status: "ready_for_review",
        target: "ARTIFACT:10000000-0000-4000-8000-000000000008",
        riskLevel: "HIGH",
        risk: "Impact report identified an owner-backed downstream dependency",
        revisionId,
        changeHash,
        createdAt: "2026-07-28T00:00:00Z",
        updatedAt: "2026-07-28T00:01:00Z",
      }],
    });

    renderList("/proposals?proposal_type=downstream_update");

    expect(await screen.findByRole("combobox", { name: "类型" })).toHaveValue("downstream_update");
    await waitFor(() => expect(api.listProposals).toHaveBeenCalledWith(workspaceId, expect.objectContaining({ type: "downstream_update" }), expect.any(AbortSignal)));
    expect(screen.getAllByText("下游更新")).toHaveLength(2);
    expect(document.querySelector(".kind-mark--downstream_update svg")).toBeInTheDocument();
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
    expect(screen.getAllByText("已批准", { selector: ".ui-badge" })).toHaveLength(2);
    expect(screen.getByText("风险等级 低")).toBeInTheDocument();
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

    expect(await screen.findAllByText("低")).toHaveLength(2);
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
      targetMode: "REPLACE",
      baseHash: "a".repeat(64),
    }, expect.any(AbortSignal));
  });

  it("把 CREATE_ONLY Proposal 显示为空文件到新正文的 Diff", async () => {
    const absenceToken = `workspace-target-absent/v1:${"d".repeat(64)}`;
    const proposal = fileProposal({
      targetPath: "java-ai-guide.md",
      revision: {
        ...fileProposal().revision,
        targetMode: "CREATE_ONLY" as const,
        baseHash: absenceToken,
      },
    });
    api.getProposal.mockResolvedValue(proposal);
    api.getProposalCurrentContent.mockResolvedValue({
      proposalId,
      workspaceId,
      targetPath: "java-ai-guide.md",
      targetMode: "CREATE_ONLY",
      content: "",
      currentHash: absenceToken,
      baseHash: absenceToken,
      baseHashMatch: true,
    });
    renderDetail();

    expect(await screen.findByText("新文件 → 提案")).toBeInTheDocument();
    expect(screen.getByText("目标路径当前不存在；差异对比基线为空文件。")).toBeInTheDocument();
    expect(screen.getByText("缺失证明")).toBeInTheDocument();
    await waitFor(() => expect(screen.getByRole("button", { name: "批准" })).toBeEnabled());
    expect(monaco.diffViewer).toHaveBeenCalledWith(expect.objectContaining({ original: "", modified: "next" }));
    expect(api.getProposalCurrentContent).toHaveBeenCalledWith(workspaceId, proposalId, {
      targetPath: "java-ai-guide.md",
      targetMode: "CREATE_ONLY",
      baseHash: absenceToken,
    }, expect.any(AbortSignal));
  });

  it("展示文档恢复来源并通过文件 Diff 审批 restore_document", async () => {
    api.getProposal.mockResolvedValue(restoreProposal());
    renderDetail();

    expect(await screen.findByText("当前文档 → 恢复目标")).toBeInTheDocument();
    expect(screen.getByText(/审阅台 \/ 文档恢复/)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: restoreDocumentId })).toHaveAttribute("href", `/authoring/documents/${restoreDocumentId}/history`);
    expect(screen.getByText("来源 Commit").parentElement).toHaveTextContent(restoreTargetCommit);
    expect(screen.getByText("预览 HEAD").parentElement).toHaveTextContent(restoreExpectedHead);
    expect(screen.getByText("文档版本").parentElement).toHaveTextContent("v7");
    expect(screen.getByText("预览哈希").parentElement).toHaveTextContent(restorePreviewHash);
    expect(screen.getByText("目标内容哈希").parentElement).toHaveTextContent(restoreTargetContentHash);
    expect(screen.getByText("document-restore/v1")).toBeInTheDocument();
    expect(await screen.findByText("Diff Viewer")).toBeInTheDocument();
    expect(api.getProposalCurrentContent).toHaveBeenCalledWith(workspaceId, proposalId, {
      targetPath: "docs/a.md",
      targetMode: "REPLACE",
      baseHash: "a".repeat(64),
    }, expect.any(AbortSignal));
    expect(monaco.diffViewer).toHaveBeenCalledWith(expect.objectContaining({
      original: "current",
      modified: "historic\n",
    }));

    const approve = screen.getByRole("button", { name: "批准" });
    await waitFor(() => expect(approve).toBeEnabled());
    fireEvent.click(approve);
    expect(screen.getByRole("dialog")).toHaveTextContent("恢复来源文档");
    expect(screen.getByRole("dialog")).toHaveTextContent("不会改写既有历史");
    fireEvent.click(screen.getByRole("button", { name: "确认提交" }));

    await waitFor(() => expect(api.decideProposal).toHaveBeenCalledWith(proposalId, {
      revisionId,
      changeHash,
      decision: "approved",
      proposalType: "restore_document",
    }));
    expect(api.preflightProposal).not.toHaveBeenCalled();
  });

  it("为已批准的 restore_document 提供独立 Apply Preflight", async () => {
    const writebackWorkflowRunId = "10000000-0000-4000-8000-000000000008";
    api.getProposal.mockResolvedValue(restoreProposal({
      status: "approved",
      approval: {
        id: "10000000-0000-4000-8000-000000000004",
        proposalId,
        revisionId,
        changeHash,
        decision: "approved",
        approvedGitHead: restoreExpectedHead,
        workflowRunId: writebackWorkflowRunId,
        workflowStatusUrl: `/api/v1/workflows/${writebackWorkflowRunId}`,
        writebackState: "bound",
        decidedAt: "2026-08-02T00:02:00Z",
      },
    }));
    renderDetail();

    const preflightButton = await screen.findByRole("button", { name: "执行 Apply Preflight" });
    expect(screen.getByText(/冻结的文档恢复绑定/)).toBeInTheDocument();
    await waitFor(() => expect(preflightButton).toBeEnabled());
    fireEvent.click(preflightButton);

    await waitFor(() => expect(api.preflightProposal).toHaveBeenCalledWith(proposalId, {
      revisionId,
      changeHash,
      targetMode: "REPLACE",
      baseHash: "a".repeat(64),
    }));
    expect(await screen.findByText("写回前检查通过")).toBeInTheDocument();
    expect(api.decideProposal).not.toHaveBeenCalled();
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
    expect(screen.getByText("等待差异查看器成功挂载后才能批准；驳回仍可提交。")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "驳回" }));
    fireEvent.click(screen.getByRole("button", { name: "确认提交" }));

    await waitFor(() => expect(api.decideProposal).toHaveBeenCalledWith(proposalId, { revisionId, changeHash, decision: "rejected", proposalType: "file_patch" }));
  });

  it("Diff 加载失败时显式报错、禁用批准且允许驳回", async () => {
    monaco.mode = "error";
    renderDetail();

    expect(await screen.findByRole("alert")).toHaveTextContent("差异查看器加载失败");
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
    await waitFor(() => expect(api.preflightProposal).toHaveBeenCalledWith(proposalId, {
      revisionId,
      changeHash,
      targetMode: "REPLACE",
      baseHash: "a".repeat(64),
    }));
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
    expect(screen.getByText("等待差异查看器成功挂载后才能恢复派发。")).toBeInTheDocument();
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

    expect(await screen.findByText("关系变更")).toBeInTheDocument();
    expect(screen.getByText("关系候选：10000000-0000-4000-8000-000000000004")).toBeInTheDocument();
    expect(screen.getByText(/指纹：d{64}/)).toBeInTheDocument();
    expect(screen.getAllByText("主张：10000000-0000-4000-8000-000000000005 · v1")).toHaveLength(2);
    expect(screen.getAllByText("主题：10000000-0000-4000-8000-000000000006 · v2")).toHaveLength(2);
    expect(screen.getByText("10000000-0000-4000-8000-000000000007")).toBeInTheDocument();
    expect(screen.getByText(/语义哈希：e{64}/)).toBeInTheDocument();
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

    expect(await screen.findByText("产物发布快照")).toBeInTheDocument();
    expect(screen.getByText("10000000-0000-4000-8000-000000000006")).toBeInTheDocument();
    expect(screen.getByText(/#3 ·/)).toHaveTextContent("10000000-0000-4000-8000-000000000007");
    expect(screen.getByText("v5")).toBeInTheDocument();
    expect(screen.getAllByText("c".repeat(64))).toHaveLength(2);
    expect(screen.getByText("已覆盖 · 无知识缺口")).toBeInTheDocument();
    expect(screen.getByText("知识缺口 · NO_SOURCE：没有可验证来源")).toBeInTheDocument();
    expect(screen.getByText("尚未成为正式知识")).toBeInTheDocument();
    expect(screen.getByText(/即使审批状态为已批准，本页也不表示已创建文档、Git 写入或索引/)).toBeInTheDocument();
    expect(api.getProposalCurrentContent).not.toHaveBeenCalled();
    expect(screen.queryByRole("button", { name: "执行 Apply Preflight" })).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "批准" }));
    expect(screen.getByRole("dialog")).toHaveTextContent("批准只形成审批记录，不表示已经创建文档、Git 写入或索引");
    fireEvent.click(screen.getByRole("button", { name: "确认提交" }));

    await waitFor(() => expect(api.decideProposal).toHaveBeenCalledWith(proposalId, {
      revisionId,
      changeHash,
      decision: "approved",
      proposalType: "publish_artifact",
    }));
    expect(api.preflightProposal).not.toHaveBeenCalled();
  });

  it("downstream_update ready 状态展示冻结 Impact 与 Artifact owner 并允许审批", async () => {
    api.getProposal.mockReset().mockResolvedValue(downstreamArtifactProposal());
    api.getProposalCurrentContent.mockClear();
    renderDetail();

    expect(await screen.findByText("下游更新快照")).toBeInTheDocument();
    expect(screen.getByText("10000000-0000-4000-8000-000000000006")).toBeInTheDocument();
    expect(screen.getByText("impact-analysis/v2")).toBeInTheDocument();
    expect(screen.getByText("来源事件").parentElement).toHaveTextContent("10000000-0000-4000-8000-000000000007 · v2");
    expect(screen.getByText("目标").parentElement).toHaveTextContent("产物：10000000-0000-4000-8000-000000000008");
    expect(screen.getByText("产物绑定").parentElement).toHaveTextContent("10000000-0000-4000-8000-000000000008 · v5");
    expect(screen.getByText("产物修订版本").parentElement).toHaveTextContent("#3 · 10000000-0000-4000-8000-000000000009");
    expect(screen.getByText("b".repeat(64))).toBeInTheDocument();
    expect(screen.getByText("引用来源发生变化")).toBeInTheDocument();
    expect(api.getProposalCurrentContent).not.toHaveBeenCalled();

    fireEvent.click(screen.getByRole("button", { name: "批准" }));
    expect(screen.getByRole("dialog")).toHaveTextContent("批准只记录更新意图，不授予目标执行能力");
    fireEvent.click(screen.getByRole("button", { name: "确认提交" }));

    await waitFor(() => expect(api.decideProposal).toHaveBeenCalledWith(proposalId, {
      revisionId,
      changeHash,
      decision: "approved",
      proposalType: "downstream_update",
    }));
    expect(api.preflightProposal).not.toHaveBeenCalled();
  });

  it("approved downstream_update 展示 Review Card owner 与稳定的执行能力不可用状态", async () => {
    api.getProposal.mockReset().mockResolvedValue(downstreamReviewCardProposal({
      status: "approved",
      approval: {
        id: "10000000-0000-4000-8000-000000000012",
        proposalId,
        revisionId,
        changeHash,
        decision: "approved",
        decidedAt: "2026-07-28T00:02:00Z",
      },
    }));
    api.getProposalCurrentContent.mockClear();
    renderDetail();

    expect(await screen.findByText("执行能力不可用")).toBeInTheDocument();
    expect(screen.getByText("复习卡绑定").parentElement).toHaveTextContent("10000000-0000-4000-8000-000000000010 · v4 · 已失效");
    expect(screen.getByText("10000000-0000-4000-8000-000000000011")).toBeInTheDocument();
    expect(screen.getByText("d".repeat(64))).toBeInTheDocument();
    expect(screen.getByText("e".repeat(64))).toBeInTheDocument();
    expect(screen.getByText(/仅记录下游更新意图/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "批准" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "驳回" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "执行 Apply Preflight" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "恢复写回 Workflow" })).not.toBeInTheDocument();
    expect(screen.queryByText("写回前检查通过")).not.toBeInTheDocument();
    expect(api.getProposalCurrentContent).not.toHaveBeenCalled();
    expect(api.preflightProposal).not.toHaveBeenCalled();
  });

  it("基线漂移时阻止批准并给出恢复说明", async () => {
    api.getProposalCurrentContent.mockResolvedValue(currentContent(false));
    renderDetail();

    expect(await screen.findByRole("alert")).toHaveTextContent("版本冲突：基线已经漂移");
    expect(screen.queryByRole("button", { name: "批准" })).not.toBeInTheDocument();
    expect(screen.getByText("批准已被阻止；请重新生成提案修订版本。")).toBeInTheDocument();
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
      targetMode: "REPLACE",
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

  it("按服务端 capability 进入全宽 Revision 工作台并隐藏普通决策", async () => {
    renderDetail();

    const entry = await screen.findByRole("button", { name: "编辑修订版本" });
    fireEvent.click(entry);

    expect(await screen.findByRole("heading", { name: "Revision Workbench" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "批准" })).not.toBeInTheDocument();
    expect(screen.queryByRole("region", { name: "修订历史" })).not.toBeInTheDocument();

    api.getProposal.mockResolvedValue(fileProposal({
      revisionCapability: { editable: false, reason: "PROPOSAL_REVISION_STATUS_NOT_EDITABLE" },
    }));
    await act(async () => { await revisionApi.workbenchProps?.onAuthorityStale(); });
    expect(screen.getByRole("heading", { name: "Revision Workbench" })).toBeInTheDocument();
    expect(await screen.findByText("权威不可编辑，草稿已保留")).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "返回审阅" }));
    expect(screen.queryByRole("button", { name: "编辑修订版本" })).not.toBeInTheDocument();

    cleanup();
    api.getProposal.mockResolvedValue(fileProposal({
      revisionCapability: { editable: false, reason: "PROPOSAL_REVISION_STATUS_NOT_EDITABLE" },
    }));
    const { queryClient } = renderDetail();
    await waitFor(() => expect(queryClient.isFetching()).toBe(0));
    expect(screen.queryByRole("button", { name: "编辑修订版本" })).not.toBeInTheDocument();
  });

  it("选择旧 Revision 后只读展示冻结 base 到 proposed，并隐藏审批与编辑", async () => {
    api.getProposal.mockResolvedValue(fileProposal({
      revision: { ...fileProposal().revision, revisionNo: 2 },
    }));
    revisionApi.listProposalRevisions.mockResolvedValue({
      items: [historyItem(revisionId, 2, true), historyItem(oldRevisionId, 1, false)],
    });
    revisionApi.getProposalRevision.mockResolvedValue({
      workspaceId,
      proposalId,
      currentRevisionId: revisionId,
      current: false,
      revision: {
        ...fileProposal().revision,
        id: oldRevisionId,
        revisionNo: 1,
        content: "historic proposed",
        changeHash: "d".repeat(64),
      },
      baseAvailable: true,
      baseSnapshot: {
        hash: "a".repeat(64),
        content: "historic base",
        byteSize: 13,
        schemaVersion: "proposal-base-snapshot/v1",
        createdAt: "2026-07-21T00:00:00Z",
      },
    });
    renderDetail();

    const revision = await screen.findByRole("button", { name: "Revision #1，历史，未审批" });
    fireEvent.click(revision);

    await waitFor(() => expect(revision).toHaveAttribute("aria-current", "true"));
    expect(await screen.findByRole("heading", { name: "历史 Revision #1" })).toBeInTheDocument();
    expect(await screen.findByText("Historical Diff Viewer")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "批准" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "编辑修订版本" })).not.toBeInTheDocument();
    expect(revisionApi.getProposalRevision).toHaveBeenCalledWith(expect.objectContaining({
      workspaceId,
      proposalId,
      revisionId: oldRevisionId,
      revisionNo: 1,
    }), expect.any(AbortSignal));
    expect(monaco.diffViewer).toHaveBeenLastCalledWith(expect.objectContaining({
      original: "historic base",
      modified: "historic proposed",
    }));
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
