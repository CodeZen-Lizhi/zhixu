import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { StrictMode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { BusinessApiError } from "../../api/business";
import type {
  AppendProposalRevisionInput,
  AppendProposalRevisionResult,
  ProposalRevisionMergePreview,
  ProposalRevisionPreviewBinding,
} from "../../api/business-revisions";

const revisionApi = vi.hoisted(() => ({
  preview: vi.fn(),
  append: vi.fn(),
  key: vi.fn(() => "proposal-revision-test-key"),
}));

vi.mock("../../api/business-revisions", () => ({
  proposalRevisionMaxBytes: 1024 * 1024,
  proposalRevisionMetadataMaxBytes: 64 * 1024,
  previewProposalRevision: revisionApi.preview,
  appendProposalRevision: revisionApi.append,
  createProposalRevisionIdempotencyKey: revisionApi.key,
}));

vi.mock("../../shared/MonacoTextEditor", () => ({
  MonacoTextEditor: (props: {
    ariaLabel: string;
    value: string;
    disabled?: boolean;
    onChange: (value: string) => void;
    onReady?: () => void;
  }) => <div>
    <textarea aria-label={props.ariaLabel} value={props.value} disabled={props.disabled} onChange={(event) => props.onChange(event.target.value)} />
    <button type="button" onClick={props.onReady}>编辑器就绪</button>
  </div>,
}));

vi.mock("../../shared/MonacoDiffViewer", () => ({
  MonacoDiffViewer: (props: { original: string; modified: string }) => <div aria-label="Diff mock">{props.original}::{props.modified}</div>,
}));

import {
  initialProposalRevisionWorkbenchState,
  proposalRevisionWorkbenchReducer,
  ProposalRevisionWorkbench,
  type ProposalRevisionSubmission,
} from "./ProposalRevisionWorkbench";

const workspaceId = "10000000-0000-4000-8000-000000000001";
const proposalId = "10000000-0000-4000-8000-000000000002";
const revisionId = "10000000-0000-4000-8000-000000000003";
const binding: ProposalRevisionPreviewBinding = {
  workspaceId,
  proposalId,
  expectedProposalVersion: 7,
  sourceRevisionId: revisionId,
  sourceRevisionNo: 1,
  sourceChangeHash: "c".repeat(64),
  targetPath: "notes/example.md",
};

const snapshot = (content: string, hash: string) => ({ content, hash, byteSize: new TextEncoder().encode(content).byteLength });
const preview = (overrides: Partial<ProposalRevisionMergePreview> = {}): ProposalRevisionMergePreview => ({
  schemaVersion: "proposal-text-merge-preview/v1",
  mergeAlgorithm: "git-merge-file",
  mergeAlgorithmVersion: "diff3/myers/marker32/v1",
  mergeFingerprint: "f".repeat(64),
  proposalId,
  workspaceId,
  proposalVersion: 7,
  sourceRevisionId: revisionId,
  sourceRevisionNo: 1,
  sourceChangeHash: "c".repeat(64),
  targetPath: "notes/example.md",
  targetMode: "REPLACE",
  base: snapshot("# Base\n", "a".repeat(64)),
  current: snapshot("# Current\n", "b".repeat(64)),
  proposed: snapshot("# Proposed\n", "d".repeat(64)),
  candidate: snapshot("# Candidate\n", "e".repeat(64)),
  conflictCount: 0,
  conflicts: [],
  ...overrides,
});

const appendedResult = (): AppendProposalRevisionResult => ({
  replayed: false,
  proposal: {
    type: "file_patch",
    id: proposalId,
    workspaceId,
    targetPath: "notes/example.md",
    status: "ready_for_review",
    riskLevel: "LOW",
    version: 8,
    revisionCapability: { editable: true, reason: "AVAILABLE" },
    createdAt: "2026-08-13T08:00:00Z",
    updatedAt: "2026-08-14T08:00:00Z",
    revision: {
      id: "10000000-0000-4000-8000-000000000004",
      revisionNo: 2,
      targetMode: "REPLACE",
      baseHash: "b".repeat(64),
      content: "# Edited\n",
      evidenceSummary: "证据",
      risk: "低风险",
      rollbackPlan: "回滚",
      changeHash: "9".repeat(64),
      createdAt: "2026-08-14T08:00:00Z",
    },
  },
});

const renderWorkbench = (
  overrides: Partial<React.ComponentProps<typeof ProposalRevisionWorkbench>> = {},
  options: { strict?: boolean } = {},
) => {
  const onClose = vi.fn();
  const onRevisionCreated = vi.fn().mockResolvedValue(undefined);
  const onAuthorityStale = vi.fn().mockResolvedValue(undefined);
  const workbench = <ProposalRevisionWorkbench
    binding={binding}
    authorityEditable
    evidenceSummary="证据"
    risk="低风险"
    rollbackPlan="回滚"
    riskLevel="LOW"
    onClose={onClose}
    onRevisionCreated={onRevisionCreated}
    onAuthorityStale={onAuthorityStale}
    {...overrides}
  />;
  render(options.strict ? <StrictMode>{workbench}</StrictMode> : workbench);
  return { onClose, onRevisionCreated, onAuthorityStale };
};

describe("proposalRevisionWorkbenchReducer", () => {
  it("preserves the exact submission through unknown delivery and preserves the draft through stale restart", () => {
    const mergePreview = preview();
    const metadata = { evidenceSummary: "证据", risk: "低风险", rollbackPlan: "回滚" };
    let state = proposalRevisionWorkbenchReducer(initialProposalRevisionWorkbenchState(), { type: "PREVIEW_SUCCESS", preview: mergePreview, metadata });
    state = proposalRevisionWorkbenchReducer(state, { type: "EDIT", field: "content", value: "# Local\n" });
    const submission: ProposalRevisionSubmission = {
      idempotencyKey: "same-key",
      input: {
        expectedProposalVersion: 7,
        sourceRevisionId: revisionId,
        sourceRevisionNo: 1,
        sourceChangeHash: "c".repeat(64),
        expectedCurrentHash: "b".repeat(64),
        mergeFingerprint: "f".repeat(64),
        mergeAlgorithm: "git-merge-file",
        mergeAlgorithmVersion: "diff3/myers/marker32/v1",
        content: "# Local\n",
        evidenceSummary: "证据",
        risk: "低风险",
        rollbackPlan: "回滚",
        resolvedConflictIds: [],
      },
    };
    state = proposalRevisionWorkbenchReducer(state, { type: "SUBMIT", submission });
    state = proposalRevisionWorkbenchReducer(state, { type: "DELIVERY_UNKNOWN", message: "unknown" });
    expect(state.submission).toBe(submission);
    expect(state.draft?.content).toBe("# Local\n");

    state = proposalRevisionWorkbenchReducer(state, { type: "RESUME_AFTER_UNKNOWN" });
    state = proposalRevisionWorkbenchReducer(state, { type: "STALE", message: "stale", errorCode: "PROPOSAL_REVISION_STALE" });
    state = proposalRevisionWorkbenchReducer(state, { type: "PREPARE", restart: true });
    state = proposalRevisionWorkbenchReducer(state, { type: "PREVIEW_SUCCESS", preview: preview({ proposalVersion: 8 }), metadata });
    expect(state.phase).toBe("restart_review");
    expect(state.previousDraft?.content).toBe("# Local\n");
    state = proposalRevisionWorkbenchReducer(state, { type: "KEEP_PREVIOUS" });
    expect(state.draft?.content).toBe("# Local\n");
    expect(state.submission).toBeUndefined();
  });
});

describe("ProposalRevisionWorkbench", () => {
  beforeEach(() => {
    revisionApi.preview.mockReset();
    revisionApi.append.mockReset();
    revisionApi.key.mockClear();
  });

  afterEach(() => vi.restoreAllMocks());

  it("retries an unknown delivery with the exact key and immutable body", async () => {
    revisionApi.preview.mockResolvedValue(preview());
    const networkError = new BusinessApiError("NETWORK_ERROR", "connection lost", true);
    revisionApi.append.mockRejectedValueOnce(networkError).mockResolvedValueOnce(appendedResult());
    const { onRevisionCreated } = renderWorkbench();

    await screen.findByRole("heading", { name: "Revision #1 -> #2" });
    fireEvent.click(screen.getByRole("button", { name: "编辑器就绪" }));
    fireEvent.change(screen.getByRole("textbox", { name: "待提交 Revision 正文" }), { target: { value: "# Edited\n" } });
    fireEvent.click(screen.getByRole("button", { name: "检查并创建 Revision #2" }));

    await screen.findByText("请求结果未知");
    const firstCall = revisionApi.append.mock.calls[0] as [string, string, string, AppendProposalRevisionInput];
    expect(firstCall[2]).toBe("proposal-revision-test-key");
    fireEvent.click(screen.getByRole("button", { name: "原样重试" }));

    await screen.findByText("Revision #2 已创建");
    const secondCall = revisionApi.append.mock.calls[1] as [string, string, string, AppendProposalRevisionInput];
    expect(secondCall[2]).toBe(firstCall[2]);
    expect(secondCall[3]).toEqual(firstCall[3]);
    expect(revisionApi.key).toHaveBeenCalledOnce();
    expect(onRevisionCreated).toHaveBeenCalledOnce();
  });

  it("keeps append success authoritative when refreshing page facts fails", async () => {
    revisionApi.preview.mockResolvedValue(preview());
    revisionApi.append.mockResolvedValue(appendedResult());
    const onRevisionCreated = vi.fn()
      .mockRejectedValueOnce(new Error("latest facts unavailable"))
      .mockResolvedValueOnce(undefined);
    renderWorkbench({ onRevisionCreated });

    await screen.findByRole("heading", { name: "Revision #1 -> #2" });
    fireEvent.click(screen.getByRole("button", { name: "编辑器就绪" }));
    fireEvent.click(screen.getByRole("button", { name: "检查并创建 Revision #2" }));

    expect(await screen.findByText("Revision #2 已创建")).toBeInTheDocument();
    expect(await screen.findByText("页面事实刷新失败")).toBeInTheDocument();
    expect(screen.getByText(/Revision 已确定创建/)).toBeInTheDocument();
    expect(revisionApi.append).toHaveBeenCalledOnce();
    expect(revisionApi.key).toHaveBeenCalledOnce();

    fireEvent.click(screen.getByRole("button", { name: "重新读取最新事实" }));
    await waitFor(() => expect(onRevisionCreated).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(screen.queryByText("页面事实刷新失败")).not.toBeInTheDocument());
    expect(revisionApi.append).toHaveBeenCalledOnce();
  });

  it("ignores an aborted preview response that completes after the current request", async () => {
    let resolveObsolete: ((value: ProposalRevisionMergePreview) => void) | undefined;
    let resolveCurrent: ((value: ProposalRevisionMergePreview) => void) | undefined;
    const obsolete = new Promise<ProposalRevisionMergePreview>((resolve) => { resolveObsolete = resolve; });
    const current = new Promise<ProposalRevisionMergePreview>((resolve) => { resolveCurrent = resolve; });
    revisionApi.preview.mockReturnValueOnce(obsolete).mockReturnValueOnce(current);
    renderWorkbench({}, { strict: true });

    await waitFor(() => expect(revisionApi.preview).toHaveBeenCalledTimes(2));
    resolveCurrent?.(preview({ candidate: snapshot("# Current request\n", "1".repeat(64)) }));
    expect(await screen.findByRole("textbox", { name: "待提交 Revision 正文" })).toHaveValue("# Current request\n");

    resolveObsolete?.(preview({ candidate: snapshot("# Obsolete request\n", "2".repeat(64)) }));
    await waitFor(() => expect(screen.getByRole("textbox", { name: "待提交 Revision 正文" })).toHaveValue("# Current request\n"));
  });

  it("keeps a dirty draft on stale and requires an explicit latest merge decision", async () => {
    revisionApi.preview.mockResolvedValueOnce(preview()).mockResolvedValueOnce(preview({
      proposalVersion: 8,
      candidate: snapshot("# Fresh candidate\n", "1".repeat(64)),
    }));
    revisionApi.append.mockRejectedValue(new BusinessApiError(
      "HTTP_ERROR",
      "Proposal 已变化",
      false,
      409,
      undefined,
      { errorCode: "PROPOSAL_REVISION_STALE" },
    ));
    const { onAuthorityStale } = renderWorkbench();

    await screen.findByRole("heading", { name: "Revision #1 -> #2" });
    fireEvent.click(screen.getByRole("button", { name: "编辑器就绪" }));
    const editor = screen.getByRole("textbox", { name: "待提交 Revision 正文" });
    fireEvent.change(editor, { target: { value: "# My preserved edit\n" } });
    fireEvent.click(screen.getByRole("button", { name: "检查并创建 Revision #2" }));

    await screen.findByText("版本已变化");
    expect(editor).toHaveValue("# My preserved edit\n");
    expect(onAuthorityStale).toHaveBeenCalledOnce();
    fireEvent.click(screen.getByRole("button", { name: "基于最新内容重新合并" }));
    await screen.findByRole("heading", { name: "上次编辑与最新候选" });
    fireEvent.click(screen.getByRole("button", { name: "保留上次编辑" }));
    expect(screen.getByRole("textbox", { name: "待提交 Revision 正文" })).toHaveValue("# My preserved edit\n");
  });

	it.each([
		"PROPOSAL_REVISION_NOT_EDITABLE",
		"PROPOSAL_REVISION_SIDE_EFFECT_STARTED",
		"PROPOSAL_REVISION_WORKFLOW_ACTIVE",
		"PROPOSAL_REVISION_AUTHORIZATION_CONFLICT",
	])("keeps the draft and disables resubmission after authority rejection %s", async (errorCode) => {
		revisionApi.preview.mockResolvedValue(preview());
		revisionApi.append.mockRejectedValue(new BusinessApiError(
			"HTTP_ERROR",
			"当前 Revision 不可安全替换",
			false,
			409,
			undefined,
			{ errorCode },
		));
		const { onAuthorityStale } = renderWorkbench();

		await screen.findByRole("heading", { name: "Revision #1 -> #2" });
		fireEvent.click(screen.getByRole("button", { name: "编辑器就绪" }));
		const editor = screen.getByRole("textbox", { name: "待提交 Revision 正文" });
		fireEvent.change(editor, { target: { value: "# Preserved authority draft\n" } });
		fireEvent.click(screen.getByRole("button", { name: "检查并创建 Revision #2" }));

		await screen.findByText("当前版本不可替换");
		expect(editor).toHaveValue("# Preserved authority draft\n");
		expect(onAuthorityStale).toHaveBeenCalledOnce();
		expect(screen.getByRole("button", { name: "检查并创建 Revision #2" })).toBeDisabled();
		expect(revisionApi.append).toHaveBeenCalledOnce();
	});

  it("uses non-color conflict state and blocks creation until every conflict is confirmed", async () => {
    const conflictId = "6".repeat(64);
    revisionApi.preview.mockResolvedValue(preview({
      conflictCount: 1,
      conflicts: [{ id: conflictId, ordinal: 1, base: "base", current: "current", proposed: "proposed" }],
    }));
    renderWorkbench();

    await screen.findByText("待处理");
    fireEvent.click(screen.getByRole("button", { name: "编辑器就绪" }));
    const submit = screen.getByRole("button", { name: "检查并创建 Revision #2" });
    expect(submit).toBeDisabled();
    fireEvent.click(screen.getByRole("checkbox", { name: "我已在待提交正文中处理此冲突" }));
    expect(screen.getByText("已确认处理")).toBeVisible();
    expect(submit).toBeEnabled();
  });

  it("warns before unload and asks before discarding a local draft", async () => {
    revisionApi.preview.mockResolvedValue(preview());
    const { onClose } = renderWorkbench();
    await screen.findByRole("heading", { name: "Revision #1 -> #2" });
    const event = new Event("beforeunload", { cancelable: true });
    window.dispatchEvent(event);
    expect(event.defaultPrevented).toBe(true);

    fireEvent.click(screen.getByRole("button", { name: "取消" }));
    await screen.findByRole("dialog", { name: "放弃未提交的 Revision？" });
    expect(onClose).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "放弃并关闭" }));
    expect(onClose).toHaveBeenCalledOnce();
  });
});
