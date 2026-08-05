import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes, useNavigate } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const workspaceId = "10000000-0000-4000-8000-000000000002";
const workflowId = "10000000-0000-4000-8000-000000000008";
const humanTaskId = "10000000-0000-4000-8000-000000000009";
const nodeRunId = "10000000-0000-4000-8000-000000000010";
const snapshotId = "10000000-0000-4000-8000-000000000011";
const reviewEvidence = {
  kind: "SOURCE_VERSION" as const,
  sourceVersionId: "10000000-0000-4000-8000-000000000012",
  sourceSpanId: "10000000-0000-4000-8000-000000000013",
  contentHash: "c".repeat(64),
  excerptHash: "d".repeat(64),
};
const reviewDocument = {
  kind: "DOCUMENT_REVISION" as const,
  documentId: "10000000-0000-4000-8000-000000000016",
  articleRevisionId: "10000000-0000-4000-8000-000000000017",
  revisionNo: 3,
  contentHash: "9".repeat(64),
};
const mergeReview = {
  kind: "MERGE_COMPARISON" as const,
  schemaVersion: 1 as const,
  workspaceId,
  runId: workflowId,
  taskId: humanTaskId,
  nodeRunId,
  snapshotId,
  snapshotHash: "a".repeat(64),
  artifactId: "10000000-0000-4000-8000-000000000014",
  revisionHash: "e".repeat(64),
  defaultTargetPath: "organized/merged.md",
  diffHash: "f".repeat(64),
  diffPreview: "--- /dev/null\n+++ b/organized-result.md\n@@ -0,0 +1,1 @@\n+# merged\n",
  diffTruncated: false,
  conflictCount: 1,
  evidenceCount: 1,
  documentCount: 1,
  categories: [
    { category: "DUPLICATE" as const, count: 0 },
    { category: "COMPLEMENTARY" as const, count: 0 },
    { category: "CONFLICT" as const, count: 1 },
    { category: "UNIQUE" as const, count: 1 },
  ],
  comparison: [{ category: "CONFLICT" as const, ...reviewEvidence }, { category: "UNIQUE" as const, ...reviewDocument }],
};
const topicReview = {
  kind: "TOPIC_OUTLINE" as const,
  schemaVersion: 1 as const,
  workspaceId,
  runId: workflowId,
  taskId: humanTaskId,
  nodeRunId,
  snapshotId,
  snapshotHash: "a".repeat(64),
  templateRevisionId: "10000000-0000-4000-8000-000000000015",
  templateHash: "b".repeat(64),
  outline: [
    { key: "context", title: "背景与范围", supports: [reviewEvidence], gapCode: null },
    { key: "limits", title: "限制", supports: [], gapCode: "EVIDENCE_UNAVAILABLE" },
  ],
};
const humanTask = (review: typeof mergeReview | typeof topicReview | null = mergeReview) => ({
  id: humanTaskId,
  runId: workflowId,
  nodeRunId,
  status: "pending" as const,
  targetVersion: 3,
  decisionKind: review?.kind === "TOPIC_OUTLINE" ? "approval" as const : "approval_with_target_path" as const,
  createdAt: "2026-07-22T00:10:00Z",
  review,
});
const api = vi.hoisted(() => ({ controlWorkflow: vi.fn(), getWorkflow: vi.fn(), listWorkflows: vi.fn(), submitWorkflowHumanDecision: vi.fn() }));
const workspaceState = vi.hoisted(() => ({ id: "10000000-0000-4000-8000-000000000002" }));

vi.mock("../../api/business", async (importOriginal) => ({
  // eslint-disable-next-line @typescript-eslint/consistent-type-imports
  ...(await importOriginal<typeof import("../../api/business")>()),
  controlWorkflow: api.controlWorkflow,
  getWorkflow: api.getWorkflow,
  listWorkflows: api.listWorkflows,
  submitWorkflowHumanDecision: api.submitWorkflowHumanDecision,
}));
vi.mock("../../app/active-workspace", async (importOriginal) => ({
  // eslint-disable-next-line @typescript-eslint/consistent-type-imports
  ...(await importOriginal<typeof import("../../app/active-workspace")>()),
  getActiveWorkspaceId: () => workspaceState.id,
  useActiveWorkspaceId: () => workspaceState.id,
}));
vi.mock("../source-spans", () => ({
  SourceSpanViewer: ({ label, reference }: { label: string; reference: { workspaceId: string; sourceVersionId: string; sourceSpanId: string } }) => (
    <button
      type="button"
      data-workspace-id={reference.workspaceId}
      data-source-version-id={reference.sourceVersionId}
      data-source-span-id={reference.sourceSpanId}
    >{label}</button>
  ),
}));

import { WorkflowDetailPage, WorkflowsPage } from "./WorkflowsPage";

const HistoryBackButton = () => {
  const navigate = useNavigate();
  return <button type="button" onClick={() => void navigate(-1)}>浏览器返回</button>;
};

const workflow = (status: "running" | "paused" | "succeeded" | "waiting_for_human") => ({
  id: workflowId,
  workspaceId,
  definitionId: "10000000-0000-4000-8000-000000000003",
  status,
  input: {},
  version: 2,
  createdAt: "2026-07-22T00:00:00Z",
  updatedAt: "2026-07-22T00:15:00Z",
  pauseRequested: false,
  cancelRequested: false,
  ...(status === "succeeded" ? { completedAt: "2026-07-22T00:15:00Z" } : {}),
});

const renderDetail = () => render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } })}><MemoryRouter initialEntries={[`/workflows/${workflowId}`]}><Routes><Route path="/workflows/:workflowId" element={<WorkflowDetailPage />} /></Routes></MemoryRouter></QueryClientProvider>);
const renderList = () => render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}><MemoryRouter initialEntries={["/workflows"]}><WorkflowsPage /><HistoryBackButton /></MemoryRouter></QueryClientProvider>);

beforeEach(() => {
  workspaceState.id = workspaceId;
  api.controlWorkflow.mockResolvedValue({ workflowRunId: workflowId, status: "running", version: 3, statusUrl: `/api/v1/workflows/${workflowId}`, pauseRequested: true, cancelRequested: false });
  api.getWorkflow.mockResolvedValue(workflow("running"));
  api.listWorkflows.mockResolvedValue({ items: [] });
  api.submitWorkflowHumanDecision.mockResolvedValue(undefined);
});

afterEach(() => {
  cleanup();
  api.controlWorkflow.mockReset();
  api.getWorkflow.mockReset();
  api.listWorkflows.mockReset();
  api.submitWorkflowHumanDecision.mockReset();
});

describe("WorkflowDetailPage controls", () => {
  it("only renders commands allowed by the current server state", async () => {
    renderDetail();

    expect(await screen.findByRole("button", { name: "暂停" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "取消" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "恢复" })).not.toBeInTheDocument();
  });

  it("renders no fake controls after the Run reaches a terminal state", async () => {
    api.getWorkflow.mockResolvedValue(workflow("succeeded"));
    renderDetail();

    expect(await screen.findByText("当前 Run 不允许控制")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /暂停|恢复|取消/ })).not.toBeInTheDocument();
    expect(screen.getByText("15 分钟")).toBeInTheDocument();
  });

  it("服务端 pending 控制请求在刷新后仍禁用所有命令", async () => {
    api.getWorkflow.mockResolvedValue({ ...workflow("running"), pauseRequested: true });
    renderDetail();

    expect(await screen.findByText("正在等待安全检查点")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "暂停" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "取消" })).toBeDisabled();
  });

  it("控制成功后等待权威详情回查完成才重新启用命令", async () => {
    let resolveRefetch: ((value: ReturnType<typeof workflow>) => void) | undefined;
    const refetchPending = new Promise<ReturnType<typeof workflow>>((resolve) => { resolveRefetch = resolve; });
    api.getWorkflow
      .mockReset()
      .mockResolvedValueOnce(workflow("running"))
      .mockImplementationOnce(() => refetchPending);
    renderDetail();

    const pause = await screen.findByRole("button", { name: "暂停" });
    fireEvent.click(pause);
    await waitFor(() => expect(api.controlWorkflow).toHaveBeenCalledWith(workspaceId, workflowId, "pause", 2));
    await waitFor(() => expect(api.getWorkflow).toHaveBeenCalledTimes(2));
    expect(screen.getByText("running")).toBeInTheDocument();
    expect(screen.queryByText("paused")).not.toBeInTheDocument();
    expect(pause).toBeDisabled();
    expect(screen.getByRole("button", { name: "取消" })).toBeDisabled();

    resolveRefetch?.(workflow("paused"));
    expect(await screen.findByRole("button", { name: "恢复" })).toBeEnabled();
  });

  it("合并 Human Task 要求目标路径并提交服务端版本", async () => {
    const task = humanTask();
    api.getWorkflow.mockResolvedValue({ ...workflow("waiting_for_human"), humanTask: task });
    renderDetail();

    const approve = await screen.findByRole("button", { name: "批准并继续" });
    expect(approve).toBeEnabled();
    expect(screen.getByRole("textbox", { name: "目标 Markdown 路径" })).toHaveValue(mergeReview.defaultTargetPath);
    const reviewHeading = screen.getByRole("heading", { name: "确认分类、冲突与来源" });
    expect(reviewHeading.compareDocumentPosition(approve) & Node.DOCUMENT_POSITION_FOLLOWING).not.toBe(0);
    expect(screen.getByRole("link", { name: "打开 Artifact" })).toHaveAttribute("href", `/artifacts/${mergeReview.artifactId}`);
    expect(screen.getByText(mergeReview.revisionHash)).toBeInTheDocument();
    expect(screen.getAllByText(mergeReview.diffHash)).toHaveLength(2);
    expect(screen.getByLabelText("统一 Diff 预览")).toHaveTextContent("+# merged");
    expect(screen.queryByRole("note")).not.toBeInTheDocument();
    const summary = screen.getByLabelText("合并比较统计");
    expect(within(summary).getByText("正式证据")).toBeInTheDocument();
    expect(within(summary).getByText("来源文档")).toBeInTheDocument();
    expect(screen.getByText(reviewEvidence.sourceVersionId)).toBeInTheDocument();
    expect(screen.getByText(reviewDocument.documentId)).toBeInTheDocument();
    expect(screen.getByText(reviewDocument.articleRevisionId)).toBeInTheDocument();
    expect(screen.getByText(String(reviewDocument.revisionNo))).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "打开来源片段" })).toHaveAttribute("data-workspace-id", workspaceId);
    expect(screen.getByRole("button", { name: "打开来源片段" })).toHaveAttribute("data-source-version-id", reviewEvidence.sourceVersionId);
    expect(screen.getByRole("button", { name: "打开来源片段" })).toHaveAttribute("data-source-span-id", reviewEvidence.sourceSpanId);
    fireEvent.change(screen.getByRole("textbox", { name: "目标 Markdown 路径" }), { target: { value: "notes/merged.md" } });
    expect(approve).toBeEnabled();
    fireEvent.click(approve);

    await waitFor(() => expect(api.submitWorkflowHumanDecision).toHaveBeenCalledWith(workspaceId, workflowId, task, { approved: true, targetPath: "notes/merged.md" }));
  });

  it("合并 Diff 被截断时在决策前明确提示审阅上限", async () => {
    const task = humanTask({ ...mergeReview, diffTruncated: true });
    api.getWorkflow.mockResolvedValue({ ...workflow("waiting_for_human"), humanTask: task });
    renderDetail();

    expect(await screen.findByRole("note")).toHaveTextContent("32 KiB");
    const note = screen.getByRole("note");
    const approve = screen.getByRole("button", { name: "批准并继续" });
    expect(note.compareDocumentPosition(approve) & Node.DOCUMENT_POSITION_FOLLOWING).not.toBe(0);
  });

  it("专题大纲在批准按钮前展示章节 Evidence tuple 与 GAP", async () => {
    const task = humanTask(topicReview);
    api.getWorkflow.mockResolvedValue({ ...workflow("waiting_for_human"), humanTask: task });
    renderDetail();

    const approve = await screen.findByRole("button", { name: "批准并继续" });
    const reviewHeading = screen.getByRole("heading", { name: "逐节核对证据与缺口" });
    expect(reviewHeading.compareDocumentPosition(approve) & Node.DOCUMENT_POSITION_FOLLOWING).not.toBe(0);
    expect(screen.getByRole("heading", { name: "背景与范围" })).toBeInTheDocument();
    expect(screen.getByText(reviewEvidence.sourceVersionId)).toBeInTheDocument();
    expect(screen.getByText(reviewEvidence.sourceSpanId)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "打开来源片段" })).toBeInTheDocument();
    expect(screen.getByText("EVIDENCE_UNAVAILABLE")).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: /Evidence|Source/ })).not.toBeInTheDocument();
    expect(approve).toBeEnabled();
  });

  it("审阅投影缺失时明确停用批准和拒绝，禁止盲批", async () => {
    const task = humanTask(null);
    api.getWorkflow.mockResolvedValue({ ...workflow("waiting_for_human"), humanTask: task });
    renderDetail();

    expect(await screen.findByRole("alert")).toHaveTextContent("审阅内容不可用");
    expect(screen.getByRole("button", { name: "批准并继续" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "拒绝" })).toBeDisabled();
    expect(screen.getByRole("textbox", { name: "目标 Markdown 路径" })).toBeDisabled();
    expect(api.submitWorkflowHumanDecision).not.toHaveBeenCalled();
  });

  it("没有活动 Workspace 时显示恢复入口且不读取详情", () => {
    workspaceState.id = "";
    renderDetail();

    expect(screen.getByText("先连接 Workspace")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "连接或切换 Workspace" })).toHaveAttribute("href", "/workspace");
    expect(api.getWorkflow).not.toHaveBeenCalled();
  });
});

describe("WorkflowsPage list", () => {
  it("同时展示创建时间、更新时间和等待人工状态", async () => {
    api.listWorkflows.mockResolvedValue({
      items: [{
        id: workflowId,
        workspaceId,
        definitionKey: "safe-writeback",
        definitionVersion: 1,
        status: "waiting_for_human",
        version: 2,
        createdAt: "2026-07-22T00:00:00Z",
        updatedAt: "2026-07-22T00:15:00Z",
        waitingForHuman: true,
        pauseRequested: false,
        cancelRequested: false,
      }],
    });
    renderList();

    expect(await screen.findByText(/创建/)).toBeInTheDocument();
    expect(screen.getByText(/更新/)).toBeInTheDocument();
    expect(screen.getByText("等待用户输入")).toBeInTheDocument();
  });

  it("浏览器历史恢复旧筛选时从第一页请求，不复活当前筛选的游标", async () => {
    api.listWorkflows.mockResolvedValue({ items: [], nextCursor: "workflow-page-2" });
    renderList();
    await waitFor(() => expect(api.listWorkflows).toHaveBeenLastCalledWith(
      workspaceId,
      { limit: 30 },
      expect.any(AbortSignal),
    ));

    fireEvent.change(screen.getByRole("combobox", { name: "运行状态" }), { target: { value: "running" } });
    await waitFor(() => expect(api.listWorkflows).toHaveBeenLastCalledWith(
      workspaceId,
      { limit: 30, status: "running" },
      expect.any(AbortSignal),
    ));
    fireEvent.click(screen.getByRole("button", { name: "下一页" }));
    await waitFor(() => expect(api.listWorkflows).toHaveBeenLastCalledWith(
      workspaceId,
      { limit: 30, status: "running", cursor: "workflow-page-2" },
      expect.any(AbortSignal),
    ));

    fireEvent.click(screen.getByRole("button", { name: "浏览器返回" }));

    await waitFor(() => expect(api.listWorkflows).toHaveBeenLastCalledWith(
      workspaceId,
      { limit: 30 },
      expect.any(AbortSignal),
    ));
  });
});
