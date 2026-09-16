import { useEffect } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, useLocation, useNavigate } from "react-router-dom";
import { afterEach, expect, it, vi } from "vitest";
import { synthesisId as id } from "../../test/synthesis-fixtures";
import type * as RemergeModule from "../../api/synthesis-candidate-remerge";
import { CandidateRemergeWorkbench } from "./CandidateRemergeWorkbench";
import { CandidateRemergeApiError, type CandidateRemergeReview } from "../../api/synthesis-candidate-remerge";
const api = vi.hoisted(() => ({ resume: vi.fn<typeof RemergeModule.resumeCandidateRemerge>(), target: vi.fn<typeof RemergeModule.getCandidateRemergeTarget>(), begin: vi.fn<typeof RemergeModule.beginCandidateRemerge>(), read: vi.fn<typeof RemergeModule.readCandidateRemerge>(), apply: vi.fn<typeof RemergeModule.applyCandidateRemerge>() }));
vi.mock("../../api/synthesis-candidate-remerge", async (importOriginal) => ({ ...await importOriginal<typeof RemergeModule>(), resumeCandidateRemerge: api.resume, getCandidateRemergeTarget: api.target, beginCandidateRemerge: api.begin, readCandidateRemerge: api.read, applyCandidateRemerge: api.apply }));
const editor = vi.hoisted(() => ({ ready: true, callbacks: [] as { onReady: () => void; onError: () => void; onChange: (value: string) => void }[] }));
vi.mock("../../shared/MonacoTextEditor", () => ({ MonacoTextEditor: ({ value, onChange, disabled, onReady, onError }: { value: string; onChange: (v: string) => void; disabled: boolean; onReady: () => void; onError: () => void }) => {
  useEffect(() => { editor.callbacks.push({ onReady, onError, onChange }); if (editor.ready) onReady(); }, []);
  return <><textarea aria-label="重新合并完整正文" value={value} disabled={disabled} onChange={(e) => onChange(e.target.value)} /><button onClick={onError}>模拟加载错误</button><button onClick={onReady}>模拟就绪</button></>;
} }));
const target = { workspace_id: id(1), note_id: id(2), expected_revision_id: id(4), expected_note_version: 2, expected_document_id: id(5), expected_document_version: 3, expected_publication_id: id(6), expected_proposal_id: id(7), expected_proposal_revision_id: id(8), expected_proposal_version: 4 };
const review: CandidateRemergeReview = { workspaceId: id(1), noteId: id(2), attemptId: id(3), state: "CONFLICTS", previewFingerprint: "a".repeat(64), replayed: false, review: { stage: "WORKSPACE_MANUAL_CONTENT", base: "base", current: "latest file", proposed: "human candidate", candidate: "merge preview", conflicts: [{ ordinal: 1, base: "base", current: "latest", proposed: "human" }] } };
const applied: CandidateRemergeReview = { workspaceId: id(1), noteId: id(2), attemptId: id(3), state: "APPLIED", previewFingerprint: "a".repeat(64), replayed: false, result: { revisionId: id(9), articleRevisionId: id(10), publicationId: id(11), proposalId: id(12), proposalRevisionId: id(13) } };
const clients: QueryClient[] = [];
const Location = () => { const navigate = useNavigate(); return <><output aria-label="location">{useLocation().search}</output><button onClick={() => { void navigate("?remerge_key=other"); }}>切换恢复请求</button></>; };
const mount = (url = "/authoring/notes/" + id(2), workspaceId = id(1)) => {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } }); clients.push(client);
  const view = (workspace: string) => <QueryClientProvider client={client}><MemoryRouter initialEntries={[url]}><CandidateRemergeWorkbench workspaceId={workspace} noteId={id(2)} eligible currentRevisionId={id(4)} /><Location /></MemoryRouter></QueryClientProvider>;
  return { ...render(view(workspaceId)), view, client };
};
afterEach(() => { cleanup(); clients.splice(0).forEach((c) => c.clear()); vi.resetAllMocks(); editor.ready = true; editor.callbacks = []; });
const begin = async () => { api.target.mockResolvedValue(target); api.begin.mockResolvedValue(review); fireEvent.click(screen.getByRole("button", { name: "基于当前文件重新合并" })); await screen.findByRole("textbox"); await waitFor(() => expect(screen.getByLabelText("location")).toHaveTextContent("remerge_key=")); };
const edit = () => { fireEvent.change(screen.getByRole("textbox"), { target: { value: "preserved human final content" } }); fireEvent.click(screen.getByRole("checkbox")); };
it("gets real target then freezes Begin key, unknown apply retries the exact original and links the new candidate", async () => {
  const m = mount(); await begin(); expect(api.target).toHaveBeenCalledWith(id(1), id(2), expect.any(AbortSignal));
  expect(api.begin.mock.calls[0]?.[0]).toMatchObject(target); const key = api.begin.mock.calls[0]?.[0].idempotency_key; if (!key) throw new Error("missing Begin key"); await waitFor(() => expect(screen.getByLabelText("location")).toHaveTextContent(`remerge_key=${key}`));
  edit(); api.apply.mockRejectedValueOnce(new CandidateRemergeApiError("lost")).mockResolvedValueOnce(applied);
  fireEvent.click(screen.getByRole("button", { name: "确认合并并创建新候选" }));
  await screen.findByText(/提交结果未知/); expect(screen.getByRole("textbox")).toBeDisabled();
  fireEvent.click(screen.getByRole("button", { name: "使用原命令精确重试" }));
  await screen.findByRole("link", { name: "查看新候选" }); expect(api.apply.mock.calls[0]?.[0]).toEqual(api.apply.mock.calls[1]?.[0]);
  expect(api.apply.mock.calls[0]?.[0].resolution?.final_content).toBe("preserved human final content");
  expect(screen.getByRole("link", { name: "查看新候选的更新提案" })).toHaveAttribute("href", `/proposals/${id(12)}`);
  expect(screen.getByRole("link", { name: "查看新候选" })).toHaveAttribute("href", `/authoring/notes/${id(2)}?revision_id=${id(9)}&remerge_key=${key}`); expect(m.client.getQueryCache().findAll()).toHaveLength(0);
});
it("lost Begin response permits only original key retry and refresh uses read without reconstructing Begin", async () => {
  api.target.mockResolvedValue(target); api.begin.mockRejectedValueOnce(new CandidateRemergeApiError("lost")).mockResolvedValueOnce(review);
  const m = mount(); fireEvent.click(screen.getByRole("button", { name: "基于当前文件重新合并" })); await screen.findByRole("button", { name: "使用原命令重试建立预览" });
  const command = api.begin.mock.calls[0]?.[0]; if (!command) throw new Error("missing Begin"); fireEvent.click(screen.getByRole("button", { name: "使用原命令重试建立预览" })); await screen.findByRole("textbox"); expect(api.begin.mock.calls[1]?.[0]).toEqual(command); expect(api.target).toHaveBeenCalledTimes(1);
  m.unmount(); api.read.mockResolvedValue(review); mount(`/authoring/notes/${id(2)}?remerge_key=${command.idempotency_key}`); await screen.findByRole("textbox"); expect(api.read).toHaveBeenCalledWith(id(1), id(2), command.idempotency_key, expect.any(AbortSignal)); expect(api.begin).toHaveBeenCalledTimes(2);
});
it("editor loading/error and late ready cannot authorize a new decision, stale response preserves text", async () => {
  editor.ready = false; mount(); await begin(); edit(); const save = screen.getByRole("button", { name: "确认合并并创建新候选" }); expect(save).toBeDisabled();
  fireEvent.click(screen.getByRole("button", { name: "模拟加载错误" })); fireEvent.click(screen.getByRole("button", { name: "模拟就绪" })); expect(save).toBeDisabled();
  fireEvent.click(screen.getByRole("button", { name: "重新加载编辑器" })); act(() => { editor.callbacks[0]?.onReady(); editor.callbacks[0]?.onChange("late old editor content"); }); expect(save).toBeDisabled(); fireEvent.click(await screen.findByRole("button", { name: "模拟就绪" })); expect(save).toBeEnabled(); expect(screen.getByRole("textbox")).toHaveValue("preserved human final content");
  api.apply.mockRejectedValue(new CandidateRemergeApiError("stale", 409)); fireEvent.click(save); await screen.findByText(/旧编辑仅供查看/); expect(screen.getByRole("textbox")).toHaveValue("preserved human final content"); expect(save).toBeDisabled();
});
it("missing P2 uses Resume even when the original Apply command is still in memory", async () => {
  mount(); await begin(); edit(); const partial = { ...applied, result: { revisionId: id(9), articleRevisionId: id(10) } }; api.apply.mockResolvedValueOnce(partial); api.resume.mockResolvedValueOnce(applied);
  fireEvent.click(screen.getByRole("button", { name: "确认合并并创建新候选" })); await screen.findByText(/更新提案仍待确认/); expect(screen.queryByRole("link", { name: "查看新候选的更新提案" })).not.toBeInTheDocument();
  fireEvent.click(screen.getByRole("button", { name: "继续创建更新提案" })); await screen.findByRole("link", { name: "查看新候选的更新提案" }); expect(api.apply).toHaveBeenCalledTimes(1); expect(api.resume).toHaveBeenCalledWith(id(1), id(2), id(3), expect.any(AbortSignal));
});
it("late workspace response cannot populate another workspace", async () => {
  let resolve: ((r: CandidateRemergeReview) => void) | undefined; api.read.mockImplementationOnce(() => new Promise<CandidateRemergeReview>((r) => { resolve = r; })).mockRejectedValue(new CandidateRemergeApiError("missing", 404));
  const m = mount(`/authoring/notes/${id(2)}?remerge_key=old`); await waitFor(() => expect(resolve).toBeDefined()); m.rerender(m.view(id(99))); act(() => resolve?.(review)); await screen.findByText(/尚未确认此请求/); expect(screen.queryByRole("textbox")).not.toBeInTheDocument(); expect(api.begin).not.toHaveBeenCalled();
});
it("ambiguous restoration URL performs no reads or writes", () => { mount(`/authoring/notes/${id(2)}?remerge_key=a&remerge_key=b`); expect(screen.getByText(/恢复链接无效/)).toBeInTheDocument(); expect(api.read).not.toHaveBeenCalled(); expect(api.begin).not.toHaveBeenCalled(); });

it("clean preview displays actual merged full text and still waits for explicit Apply", async () => {
  const clean: CandidateRemergeReview = { workspaceId: id(1), noteId: id(2), attemptId: id(3), state: "READY", previewFingerprint: "a".repeat(64), replayed: false, candidate: "# 最新文件和人工候选的实际合并全文" };
  api.target.mockResolvedValue(target); api.begin.mockResolvedValue(clean); api.apply.mockResolvedValue(applied);
  mount(); fireEvent.click(screen.getByRole("button", { name: "基于当前文件重新合并" })); await screen.findByRole("heading", { name: "最新文件和人工候选的实际合并全文" }); await waitFor(() => expect(screen.getByRole("button", { name: "确认合并并创建新候选" })).toBeEnabled());
  expect(api.apply).not.toHaveBeenCalled(); expect(screen.queryByRole("textbox")).not.toBeInTheDocument(); fireEvent.click(screen.getByRole("button", { name: "确认合并并创建新候选" })); await screen.findByRole("link", { name: "查看新候选" }); expect(api.apply.mock.calls[0]?.[0].resolution).toBeUndefined();
});

it("refresh restores APPLIED without P2 and resumes the same attempt after an unknown response", async () => {
  const partial: CandidateRemergeReview = { ...applied, state: "APPLIED", result: { revisionId: id(9), articleRevisionId: id(10) } };
  api.read.mockResolvedValue(partial);
  api.resume.mockRejectedValueOnce(new CandidateRemergeApiError("lost")).mockResolvedValueOnce(applied);
  const m = mount(`/authoring/notes/${id(2)}?remerge_key=original`);
  fireEvent.click(await screen.findByRole("button", { name: "继续创建更新提案" }));
  await screen.findByText(/更新提案创建结果尚未确认/);
  fireEvent.click(screen.getByRole("button", { name: "按原请求重新读取" }));
  await waitFor(() => expect(api.read).toHaveBeenCalledTimes(2));
  await waitFor(() => expect(screen.queryByText(/更新提案创建结果尚未确认/)).not.toBeInTheDocument());
  m.unmount();
  mount(`/authoring/notes/${id(2)}?remerge_key=original`);
  fireEvent.click(await screen.findByRole("button", { name: "继续创建更新提案" }));
  expect(await screen.findByRole("link", { name: "查看新候选的更新提案" })).toHaveAttribute("href", `/proposals/${id(12)}`);
  expect(api.resume).toHaveBeenCalledTimes(2);
  for (const call of api.resume.mock.calls) expect(call.slice(0, 3)).toEqual([id(1), id(2), id(3)]);
  expect(api.begin).not.toHaveBeenCalled(); expect(api.apply).not.toHaveBeenCalled();
});
it("workspace switch ignores a late Resume response", async () => {
  const partial: CandidateRemergeReview = { ...applied, state: "APPLIED", result: { revisionId: id(9), articleRevisionId: id(10) } };
  api.read.mockResolvedValueOnce(partial).mockResolvedValueOnce({ ...partial, workspaceId: id(20) });
  let resolve!: (r: CandidateRemergeReview) => void;
  api.resume.mockReturnValue(new Promise((r) => { resolve = r; }));
  const m = mount(`/authoring/notes/${id(2)}?remerge_key=original`);
  fireEvent.click(await screen.findByRole("button", { name: "继续创建更新提案" }));
  m.rerender(m.view(id(20)));
  await waitFor(() => expect(api.read).toHaveBeenCalledTimes(2));
  await act(async () => { resolve(applied); await Promise.resolve(); });
  expect(screen.queryByRole("link", { name: "查看新候选的更新提案" })).not.toBeInTheDocument();
});

it("key switch prevents a late Resume result from installing proposal links", async () => {
  const partial: CandidateRemergeReview = { ...applied, state: "APPLIED", result: { revisionId: id(9), articleRevisionId: id(10) } };
  api.read.mockResolvedValue(partial);
  let resolve!: (r: CandidateRemergeReview) => void;
  api.resume.mockReturnValue(new Promise((r) => { resolve = r; }));
  mount(`/authoring/notes/${id(2)}?remerge_key=original`);
  fireEvent.click(await screen.findByRole("button", { name: "继续创建更新提案" }));
  fireEvent.click(screen.getByRole("button", { name: "切换恢复请求" }));
  await act(async () => { resolve(applied); await Promise.resolve(); });
  expect(screen.queryByRole("link", { name: "查看新候选的更新提案" })).not.toBeInTheDocument();
  expect(screen.getByRole("button", { name: "继续创建更新提案" })).toBeDisabled();
});
