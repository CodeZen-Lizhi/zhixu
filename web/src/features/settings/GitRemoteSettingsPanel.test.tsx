import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type * as GitSyncAPI from "../../api/git-sync";

const workspaceId = "10000000-0000-4000-8000-000000000001";
const secondWorkspaceId = "10000000-0000-4000-8000-000000000002";
const runId = "20000000-0000-4000-8000-000000000001";
const api = vi.hoisted(() => ({
  createGitSyncRun: vi.fn(), getGitSyncStatus: vi.fn(), listGitSyncRuns: vi.fn(), removeGitRemoteConfig: vi.fn(),
  retryGitSyncRun: vi.fn(), saveGitRemoteConfig: vi.fn(), testGitRemoteConfig: vi.fn(),
}));
vi.mock("../../api/git-sync", async (importOriginal: () => Promise<typeof GitSyncAPI>) => ({ ...(await importOriginal()), ...api }));
const workspace = vi.hoisted(() => ({ id: "10000000-0000-4000-8000-000000000001" }));
vi.mock("../../app/active-workspace", () => ({ useActiveWorkspaceId: () => workspace.id }));

import { GitRemoteSettingsPanel } from "./GitRemoteSettingsPanel";

const config = { workspaceId, configured: true, remoteUrl: "https://git.example.com/team/repo.git", branch: "main", autoSync: false, tokenConfigured: true, revision: 2, replayed: false };
const failedIndexRun = {
  id: runId, workspaceId, configRevision: 2, remoteUrl: config.remoteUrl, branch: "main", trigger: "MANUAL" as const,
  status: "SUCCEEDED" as const, direction: "PULL" as const, failureClass: "NONE" as const, errorCode: "", retryable: false,
  expectedHeadOid: "a".repeat(40), expectedRemoteOid: "b".repeat(40), verifiedHeadOid: "b".repeat(40), verifiedRemoteOid: "b".repeat(40),
  changedFiles: [{ path: "notes/new.md", kind: "ADDED" as const }], indexStatus: "FAILED" as const, indexErrorCode: "INDEX_FAILED",
  indexRetryable: true, attemptCount: 1, version: 7, createdAt: "2026-08-03T01:00:00Z", updatedAt: "2026-08-03T01:01:00Z", completedAt: "2026-08-03T01:01:00Z", replayed: false,
};

const renderPanel = () => {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  const panel = () => <QueryClientProvider client={queryClient}><GitRemoteSettingsPanel /></QueryClientProvider>;
  const view = render(panel());
  return { queryClient, ...view, rerenderPanel: () => view.rerender(panel()) };
};

beforeEach(() => {
  window.localStorage.clear();
  workspace.id = workspaceId;
  Object.values(api).forEach((mock) => mock.mockReset());
  api.getGitSyncStatus.mockResolvedValue({ config, currentRun: failedIndexRun });
  api.listGitSyncRuns.mockResolvedValue({ items: [failedIndexRun] });
  api.saveGitRemoteConfig.mockResolvedValue(config);
  api.testGitRemoteConfig.mockResolvedValue({ remoteUrl: config.remoteUrl, branch: "main" });
});

describe("GitRemoteSettingsPanel", () => {
  it("separates successful Git synchronization from failed indexing", async () => {
    renderPanel();
    expect(await screen.findByText("Git 已同步")).toBeInTheDocument();
    expect(screen.getByText("重新索引失败")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "重试索引" })).toBeInTheDocument();
    expect(screen.getByText("notes/new.md")).toBeInTheDocument();
  });

  it("does not describe an active run with an unknown direction as no change", async () => {
    const run = {
      ...failedIndexRun, status: "COMPARING" as const, direction: "UNKNOWN" as const,
      failureClass: "NONE" as const, errorCode: "", retryable: false,
      expectedHeadOid: undefined, expectedRemoteOid: undefined, verifiedHeadOid: undefined, verifiedRemoteOid: undefined,
      indexStatus: "NOT_REQUIRED" as const, indexErrorCode: "", indexRetryable: false,
      completedAt: undefined,
    };
    api.getGitSyncStatus.mockResolvedValue({ config, currentRun: run });
    api.listGitSyncRuns.mockResolvedValue({ items: [run] });

    renderPanel();

    expect(await screen.findByText("尚未判断")).toBeInTheDocument();
    expect(screen.queryByText("无变更")).not.toBeInTheDocument();
  });

  it("labels a full changed-file page as a bounded preview", async () => {
    const preview = Array.from({ length: 500 }, (_, index) => ({ path: `notes/${String(index).padStart(3, "0")}.md`, kind: "MODIFIED" as const }));
    const run = { ...failedIndexRun, changedFiles: preview };
    api.getGitSyncStatus.mockResolvedValue({ config, currentRun: run });
    api.listGitSyncRuns.mockResolvedValue({ items: [run] });

    renderPanel();

    expect(await screen.findByText("显示前 500 个文件变化，可能还有更多")).toBeInTheDocument();
  });

  it("reports connection success without claiming the saved state changed", async () => {
    renderPanel();
    await screen.findByText("Git 已同步");

    fireEvent.click(screen.getByRole("button", { name: "测试连接" }));

    expect(await screen.findByText("当前填写的连接信息测试通过。")).toBeInTheDocument();
    expect(screen.queryByText(/尚未保存/)).not.toBeInTheDocument();
  });

  it("explains ref drift without assuming it happened during the run", async () => {
    const run = { ...failedIndexRun, status: "CONFLICT" as const, direction: "NONE" as const, failureClass: "REF_DRIFT" as const, errorCode: "GIT_SYNC_REF_DRIFT", indexStatus: "NOT_REQUIRED" as const };
    api.getGitSyncStatus.mockResolvedValue({ config, currentRun: run });
    api.listGitSyncRuns.mockResolvedValue({ items: [run] });

    renderPanel();

    expect(await screen.findByText(/当前分支与配置不一致，或同步期间分支发生变化/)).toBeInTheDocument();
  });

  it("retries only the failed index follow-up for a successful Git run", async () => {
    api.retryGitSyncRun.mockResolvedValue({ ...failedIndexRun, indexStatus: "PENDING", version: 8 });
    renderPanel();
    fireEvent.click(await screen.findByRole("button", { name: "重试索引" }));

    await waitFor(() => expect(api.retryGitSyncRun).toHaveBeenCalledTimes(1));
    const [calledWorkspace, calledRun, input] = api.retryGitSyncRun.mock.calls[0] as [string, string, { expectedVersion: number; idempotencyKey: string }];
    expect(calledWorkspace).toBe(workspaceId);
    expect(calledRun).toBe(runId);
    expect(input.expectedVersion).toBe(7);
    expect(input.idempotencyKey).toMatch(/^git-sync-retry-/);
    expect(api.createGitSyncRun).not.toHaveBeenCalled();
  });

  it("clears a replacement token before the mutation settles and keeps it out of caches/storage", async () => {
    let resolveSave: ((value: typeof config) => void) | undefined;
    api.saveGitRemoteConfig.mockReturnValue(new Promise((resolve) => { resolveSave = resolve; }));
    const { queryClient } = renderPanel();
    await screen.findByText("Git 已同步");
    fireEvent.click(screen.getByRole("radio", { name: "替换" }));
    const token = "git-token-that-must-not-persist";
    fireEvent.change(screen.getByLabelText("访问令牌"), { target: { value: token } });
    await waitFor(() => expect(screen.getByRole("button", { name: "保存配置" })).toBeEnabled());
    fireEvent.click(screen.getByRole("button", { name: "保存配置" }));

    await waitFor(() => expect(api.saveGitRemoteConfig).toHaveBeenCalledTimes(1));
    const [calledWorkspace, input, signal] = api.saveGitRemoteConfig.mock.calls[0] as [string, GitSyncAPI.SaveGitRemoteInput, AbortSignal];
    expect(calledWorkspace).toBe(workspaceId);
    expect(input.token).toEqual({ action: "replace", value: token });
    expect(input.idempotencyKey).toMatch(/^git-remote-save-/);
    expect(signal).toBeInstanceOf(AbortSignal);
    expect(screen.getByLabelText("访问令牌")).toHaveValue("");
    expect(JSON.stringify(window.localStorage)).not.toContain(token);
    expect(JSON.stringify(queryClient.getQueryCache().getAll().map((query) => query.state.data))).not.toContain(token);
    expect(JSON.stringify(queryClient.getMutationCache().getAll().map((mutation) => mutation.state.variables))).not.toContain(token);
    resolveSave?.(config);
    await waitFor(() => expect(screen.getByText("远端配置已保存。")).toBeInTheDocument());
  });

  it("uses a new command key when a replacement token is re-entered after a retryable error", async () => {
    api.saveGitRemoteConfig.mockRejectedValueOnce(Object.assign(new Error("网络暂时不可用"), { retryable: true }));
    renderPanel();
    await screen.findByText("Git 已同步");
    fireEvent.click(screen.getByRole("radio", { name: "替换" }));
    fireEvent.change(screen.getByLabelText("访问令牌"), { target: { value: "first-token" } });
    fireEvent.click(screen.getByRole("button", { name: "保存配置" }));

    await screen.findByRole("alert", { name: "" });
    const first = api.saveGitRemoteConfig.mock.calls[0] as [string, GitSyncAPI.SaveGitRemoteInput, AbortSignal];
    fireEvent.change(screen.getByLabelText("访问令牌"), { target: { value: "second-token" } });
    fireEvent.click(screen.getByRole("button", { name: "保存配置" }));

    await waitFor(() => expect(api.saveGitRemoteConfig).toHaveBeenCalledTimes(2));
    const second = api.saveGitRemoteConfig.mock.calls[1] as [string, GitSyncAPI.SaveGitRemoteInput, AbortSignal];
    expect(first[1].token).toEqual({ action: "replace", value: "first-token" });
    expect(second[1].token).toEqual({ action: "replace", value: "second-token" });
    expect(second[1].idempotencyKey).not.toBe(first[1].idempotencyKey);
  });

  it("reuses one command key for an unresolved manual-sync response and binds it to the original Workspace", async () => {
    let rejectRun: ((error: Error) => void) | undefined;
    api.createGitSyncRun.mockReturnValue(new Promise((_resolve, reject) => { rejectRun = reject; }));
    const { rerenderPanel } = renderPanel();
    await screen.findByText("Git 已同步");
    fireEvent.click(screen.getByRole("button", { name: "立即同步" }));
    await waitFor(() => expect(api.createGitSyncRun).toHaveBeenCalledTimes(1));
    const first = api.createGitSyncRun.mock.calls[0] as [string, { idempotencyKey: string }, AbortSignal];
    expect(first[0]).toBe(workspaceId);

    workspace.id = secondWorkspaceId;
    api.getGitSyncStatus.mockResolvedValue({ config: { ...config, workspaceId: secondWorkspaceId, revision: 2 }, currentRun: undefined });
    api.listGitSyncRuns.mockResolvedValue({ items: [] });
    rerenderPanel();
    rejectRun?.(new DOMException("aborted", "AbortError"));
    await screen.findByText("还没有同步记录");
    expect(first[2].aborted).toBe(true);
    expect(screen.queryByText("同步任务已进入队列。")).not.toBeInTheDocument();
  });

  it("never renders the previous Workspace form while hydrating a cached switch", async () => {
    const secondConfig = { ...config, workspaceId: secondWorkspaceId, remoteUrl: "https://git.example.com/team/second.git", revision: 4 };
    const { queryClient, rerenderPanel } = renderPanel();
    expect(await screen.findByDisplayValue(config.remoteUrl)).toBeInTheDocument();
    queryClient.setQueryData(["settings", "git-sync", secondWorkspaceId, "status"], { config: secondConfig, currentRun: undefined });
    queryClient.setQueryData(["settings", "git-sync", secondWorkspaceId, "runs"], { items: [] });
    api.getGitSyncStatus.mockResolvedValue({ config: secondConfig, currentRun: undefined });
    api.listGitSyncRuns.mockResolvedValue({ items: [] });

    workspace.id = secondWorkspaceId;
    rerenderPanel();

    expect(screen.queryByDisplayValue(config.remoteUrl)).not.toBeInTheDocument();
    expect(await screen.findByDisplayValue(secondConfig.remoteUrl)).toBeInTheDocument();
  });
});
