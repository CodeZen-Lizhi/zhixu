import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";

import {
  DocumentHistoryApiError,
  documentWorktreeRef,
  type CreateDocumentRestoreProposalInput,
} from "../../api/document-history";

const workspace = vi.hoisted(() => ({ id: "f1000000-0000-4000-8000-000000000001" }));
const query = vi.hoisted(() => ({
  history: {},
  compare: {},
  preview: {},
  proposal: {},
  compareHook: vi.fn(),
  previewMutate: vi.fn(),
  previewReset: vi.fn(),
  proposalMutate: vi.fn<(input: CreateDocumentRestoreProposalInput) => void>(),
  proposalReset: vi.fn(),
}));

vi.mock("../../app/active-workspace", () => ({ useActiveWorkspaceId: () => workspace.id }));
vi.mock("./queries", () => ({
  useDocumentHistoryPage: () => query.history,
  useDocumentHistoryCompare: (input: unknown, head: string, path: string, documentVersion: number) => {
    query.compareHook(input, head, path, documentVersion);
    return input === undefined ? { isFetching: false, isError: false, data: undefined, refetch: vi.fn() } : query.compare;
  },
  useCreateDocumentRestorePreview: () => ({ ...query.preview, mutate: query.previewMutate, reset: query.previewReset }),
  useCreateDocumentRestoreProposal: () => ({ ...query.proposal, mutate: query.proposalMutate, reset: query.proposalReset }),
  useDocumentHistoryRecovery: () => vi.fn(),
}));
vi.mock("../../shared/MonacoDiffViewer", () => ({
  MonacoDiffViewer: ({ original, modified }: { original: string; modified: string }) => <div data-testid="diff-viewer">{original} → {modified}</div>,
}));

import { DocumentHistoryPage } from "./DocumentHistoryPage";

const workspaceId = "f1000000-0000-4000-8000-000000000001";
const documentId = "f1000000-0000-4000-8000-000000000002";
const revisionId = "f1000000-0000-4000-8000-000000000003";
const proposalId = "f1000000-0000-4000-8000-000000000004";
const proposalRevisionId = "f1000000-0000-4000-8000-000000000005";
const approvalId = "f1000000-0000-4000-8000-000000000006";
const workflowId = "f1000000-0000-4000-8000-000000000007";
const writebackId = "f1000000-0000-4000-8000-000000000008";
const head = "a".repeat(40);
const managedCommit = "b".repeat(40);
const externalCommit = "c".repeat(40);
const hash = "d".repeat(64);
const previewHash = "e".repeat(64);
const canonicalPath = "notes/a-very-long-history-document-name-that-must-wrap-on-mobile.md";

const page = (dirty = false) => ({
  workspaceId,
  documentId,
  documentVersion: 7,
  path: canonicalPath,
  branch: "main",
  head,
  dirty,
  items: [
    ...(dirty ? [{ kind: "CURRENT_CHANGE" as const, commit: null, parentCommits: [head], authorName: "", authorEmail: "", committedAt: null, summary: "当前未提交改动" }] : []),
    {
      kind: "MANAGED" as const,
      commit: managedCommit,
      parentCommits: [externalCommit],
      authorName: "知序",
      authorEmail: "local@example.test",
      committedAt: "2026-08-03T08:00:00Z",
      summary: "受控发布",
      articleRevisionId: revisionId,
      articleRevisionNo: 3,
      proposalId,
      proposalRevisionId,
      approvalId,
      workflowRunId: workflowId,
      writebackId,
      proposalType: "file_patch",
      approvalDecidedAt: "2026-08-03T07:59:00Z",
    },
    { kind: "EXTERNAL" as const, commit: externalCommit, parentCommits: [], authorName: "Local user", authorEmail: "local@example.test", committedAt: "2026-08-02T08:00:00Z", summary: "外部编辑" },
  ],
  nextCursor: "next-cursor",
});

const preview = () => ({
  workspaceId,
  documentId,
  path: canonicalPath,
  targetCommit: managedCommit,
  expectedHead: head,
  expectedDocumentVersion: 7,
  currentContentHash: hash,
  targetContentHash: previewHash,
  currentContent: "# current\n",
  targetContent: "# old\n",
  patch: "@@ -1 +1 @@\n-current\n+old\n",
  diffHash: hash,
  previewHash,
  blockedByDirtyWorktree: false,
});

const renderPage = () => render(<MemoryRouter initialEntries={[`/authoring/documents/${documentId}/history`]}><Routes><Route path="/authoring/documents/:documentId/history" element={<DocumentHistoryPage />} /></Routes></MemoryRouter>);

beforeEach(() => {
  workspace.id = workspaceId;
  query.compareHook.mockReset();
  query.previewMutate.mockReset();
  query.previewReset.mockReset();
  query.proposalMutate.mockReset();
  query.proposalReset.mockReset();
  query.history = { isPending: false, isFetching: false, isError: false, error: undefined, data: page(false), refetch: vi.fn() };
  query.compare = { isFetching: false, isError: false, error: undefined, data: undefined, refetch: vi.fn() };
  query.preview = { isPending: false, isError: false, error: undefined, data: undefined };
  query.proposal = { isPending: false, isError: false, error: undefined, data: undefined };
});

describe("DocumentHistoryPage", () => {
  it("shows the Workspace gate without emitting a false loading state", () => {
    workspace.id = "";
    renderPage();

    expect(screen.getByText("先连接 Workspace")).toBeInTheDocument();
    expect(screen.queryByText("正在读取当前分支历史…")).not.toBeInTheDocument();
  });

  it("distinguishes current, managed and external facts and blocks restore while dirty", async () => {
    query.history = { ...query.history, data: page(true) };
    renderPage();

    expect(screen.getByText("当前未提交改动", { selector: ".ui-badge" })).toBeInTheDocument();
    expect(screen.getByText("受控写回")).toBeInTheDocument();
    expect(screen.getByText("外部变更")).toBeInTheDocument();
    expect(screen.getByText("未发现知序的 Revision、审批或写回映射；不会补造旧关系。")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: new RegExp(proposalId) })).toHaveAttribute("href", `/proposals/${proposalId}`);
    expect(screen.getByRole("link", { name: workflowId })).toHaveAttribute("href", `/workflows/${workflowId}`);
    for (const button of screen.getAllByRole("button", { name: "恢复此版本" })) expect(button).toBeDisabled();
    await waitFor(() => expect(screen.getAllByRole("option", { name: /当前工作树/ })).toHaveLength(2));
  });

  it.each([
    ["Workflow", { writebackId: null }, workflowId, "Writeback 未关联"],
    ["Writeback", { workflowRunId: null }, writebackId, "Workflow 未关联"],
  ])("preserves a managed %s relation when its peer is missing", (_label, changes, present, missing) => {
    const value = page(false);
    const managed = value.items.find((item) => item.kind === "MANAGED");
    if (managed === undefined) throw new Error("managed history fixture missing");
    query.history = { ...query.history, data: { ...value, items: [{ ...managed, ...changes }] } };

    renderPage();

    expect(screen.getByText(present)).toBeInTheDocument();
    expect(screen.getByText(missing)).toBeInTheDocument();
  });

  it("compares explicit left/right identities with an accessible patch fallback", async () => {
    query.compare = {
      isFetching: false,
      isError: false,
      error: undefined,
      refetch: vi.fn(),
      data: {
        workspaceId,
        documentId,
        path: "notes/a.md",
        head,
        left: managedCommit,
        right: documentWorktreeRef,
        leftContent: "# old\n",
        rightContent: "# current\n",
        patch: "@@ -1 +1 @@\n-old\n+current\n",
        diffHash: hash,
      },
    };
    renderPage();
    await waitFor(() => expect(screen.getAllByRole("option", { name: /受控发布/ })).toHaveLength(2));

    fireEvent.change(screen.getByLabelText("左侧版本"), { target: { value: managedCommit } });
    fireEvent.change(screen.getByLabelText("右侧版本"), { target: { value: documentWorktreeRef } });
    fireEvent.click(screen.getByRole("button", { name: "比较版本" }));

    await waitFor(() => expect(query.compareHook).toHaveBeenLastCalledWith(
      { documentId, left: managedCommit, right: documentWorktreeRef }, head, canonicalPath, 7,
    ));
    expect(await screen.findByTestId("diff-viewer")).toHaveTextContent("# old → # current");
    expect(screen.getByText("屏幕阅读器与纯文本补丁")).toBeInTheDocument();
  });

  it("previews restore before creating one same-key Proposal intent", async () => {
    const view = renderPage();
    const restoreButtons = screen.getAllByRole("button", { name: "恢复此版本" });
    const restoreButton = restoreButtons.at(0);
    expect(restoreButton).toBeDefined();
    if (restoreButton === undefined) throw new Error("restore button missing");
    fireEvent.click(restoreButton);

    expect(query.previewMutate).toHaveBeenCalledWith({ workspaceId, documentId, targetCommit: managedCommit, expectedHead: head, expectedDocumentVersion: 7 });
    query.preview = { isPending: false, isError: false, error: undefined, data: preview() };
    view.rerender(<MemoryRouter initialEntries={[`/authoring/documents/${documentId}/history`]}><Routes><Route path="/authoring/documents/:documentId/history" element={<DocumentHistoryPage />} /></Routes></MemoryRouter>);

    const dialog = await screen.findByRole("dialog", { name: "恢复为历史版本" });
    expect(within(dialog).getByText("这是相对当前版本的反向 Diff。", { exact: false })).toBeInTheDocument();
    const create = await within(dialog).findByRole("button", { name: "创建恢复 Proposal" });
    await waitFor(() => expect(create).toBeEnabled());
    fireEvent.click(create);

    expect(query.proposalMutate).toHaveBeenCalledOnce();
    const command = query.proposalMutate.mock.calls.at(0)?.at(0);
    expect(command).toMatchObject({
      workspaceId,
      documentId,
      targetCommit: managedCommit,
      expectedHead: head,
      expectedDocumentVersion: 7,
      previewHash,
    });
    expect(command?.idempotencyKey).toMatch(/^restore-document-/);

    query.proposal = {
      isPending: false,
      isError: true,
      error: new DocumentHistoryApiError("HTTP_ERROR", "DOCUMENT_RESTORE_STALE", "preview changed", { status: 409 }),
      data: undefined,
    };
    view.rerender(<MemoryRouter initialEntries={[`/authoring/documents/${documentId}/history`]}><Routes><Route path="/authoring/documents/:documentId/history" element={<DocumentHistoryPage />} /></Routes></MemoryRouter>);

    const staleDialog = await screen.findByRole("dialog", { name: "恢复为历史版本" });
    expect(within(staleDialog).getByRole("button", { name: "创建恢复 Proposal" })).toBeDisabled();
    fireEvent.click(within(staleDialog).getByRole("button", { name: "重试" }));
    await waitFor(() => expect(screen.queryByRole("dialog", { name: "恢复为历史版本" })).not.toBeInTheDocument());
  });

  it("invalidates an explicit comparison when Document version changes under the same HEAD", async () => {
    const view = renderPage();
    await waitFor(() => expect(screen.getAllByRole("option", { name: /受控发布/ })).toHaveLength(2));
    fireEvent.change(screen.getByLabelText("左侧版本"), { target: { value: managedCommit } });
    fireEvent.change(screen.getByLabelText("右侧版本"), { target: { value: documentWorktreeRef } });
    fireEvent.click(screen.getByRole("button", { name: "比较版本" }));
    await waitFor(() => expect(query.compareHook).toHaveBeenLastCalledWith(
      { documentId, left: managedCommit, right: documentWorktreeRef }, head, canonicalPath, 7,
    ));

    query.history = { ...query.history, data: { ...page(false), documentVersion: 8 } };
    view.rerender(<MemoryRouter initialEntries={[`/authoring/documents/${documentId}/history`]}><Routes><Route path="/authoring/documents/:documentId/history" element={<DocumentHistoryPage />} /></Routes></MemoryRouter>);

    await waitFor(() => expect(query.compareHook).toHaveBeenLastCalledWith(undefined, head, canonicalPath, 8));
  });

  it.each([
    ["DOCUMENT_HISTORY_CURSOR_STALE", "历史基线已变化"],
    ["DOCUMENT_HISTORY_CURSOR_INVALID", "历史游标已失效"],
  ])("surfaces %s recovery instead of showing an empty timeline", (errorCode, title) => {
    query.history = {
      isPending: false,
      isFetching: false,
      isError: true,
      error: new DocumentHistoryApiError("HTTP_ERROR", errorCode, "baseline changed", { status: 409 }),
      data: undefined,
      refetch: vi.fn(),
    };
    renderPage();

    expect(screen.getByRole("alert")).toHaveTextContent(title);
    expect(screen.queryByText("当前路径还没有历史版本")).not.toBeInTheDocument();
  });

  it("synchronously discards restore state when the Workspace scope changes", async () => {
    const view = renderPage();
    const restoreButton = screen.getAllByRole("button", { name: "恢复此版本" }).at(0);
    if (restoreButton === undefined) throw new Error("restore button missing");
    fireEvent.click(restoreButton);
    expect(await screen.findByRole("dialog", { name: "恢复为历史版本" })).toBeInTheDocument();

    workspace.id = "f1000000-0000-4000-8000-000000000099";
    query.history = { isPending: true, isFetching: true, isError: false, error: undefined, data: undefined, refetch: vi.fn() };
    view.rerender(<MemoryRouter initialEntries={[`/authoring/documents/${documentId}/history`]}><Routes><Route path="/authoring/documents/:documentId/history" element={<DocumentHistoryPage />} /></Routes></MemoryRouter>);

    expect(screen.queryByRole("dialog", { name: "恢复为历史版本" })).not.toBeInTheDocument();
    expect(screen.queryByText(managedCommit)).not.toBeInTheDocument();
  });
});
