import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, useLocation } from "react-router-dom";
import { afterEach, expect, it, vi } from "vitest";
import { synthesisId as id } from "../../test/synthesis-fixtures";
import type * as HistoryAPI from "../../api/synthesis-historical-republish";
import { HistoricalRepublishApiError, type HistoricalRepublishReview } from "../../api/synthesis-historical-republish";
import { HistoricalRepublishWorkbench } from "./HistoricalRepublishWorkbench";
const api = vi.hoisted(() => ({ target: vi.fn<typeof HistoryAPI.getHistoricalRepublishTarget>(), begin: vi.fn<typeof HistoryAPI.beginHistoricalRepublish>(), read: vi.fn<typeof HistoryAPI.readHistoricalRepublish>(), apply: vi.fn<typeof HistoryAPI.applyHistoricalRepublish>(), resume: vi.fn<typeof HistoryAPI.resumeHistoricalRepublish>() }));
vi.mock("../../api/synthesis-historical-republish", async (original) => ({ ...await original<typeof HistoryAPI>(), getHistoricalRepublishTarget: api.target, beginHistoricalRepublish: api.begin, readHistoricalRepublish: api.read, applyHistoricalRepublish: api.apply, resumeHistoricalRepublish: api.resume }));
vi.mock("../../shared/MonacoDiffViewer", () => ({ MonacoDiffViewer: ({ original, modified }: { original: string; modified: string }) => <div aria-label="readonly diff">{original} → {modified}</div> }));
const target = { workspace_id: id(1), note_id: id(2), selected_revision_id: id(4), selected_projection_hash: "a".repeat(64), expected_revision_id: id(5), expected_note_version: 2, expected_document_id: id(6), expected_document_version: 3, requires_retirement: true, expected_publication_id: id(7), expected_proposal_id: id(8), expected_proposal_revision_id: id(9), expected_proposal_version: 1 };
const review: HistoricalRepublishReview = { currentScope: null, workspaceId: id(1), noteId: id(2), attemptId: id(3), state: "READY", target, candidate: "历史精确正文\n", currentContent: "当前人工内容\n", publishedContent: "发布内容\n", previewFingerprint: "b".repeat(64), warnings: [], replayed: false };
const applied: HistoricalRepublishReview = { ...review, state: "APPLIED", result: { revisionId: id(10), articleRevisionId: id(11) } };
const clients: QueryClient[] = [];
const Location = () => <output aria-label="location">{useLocation().search}</output>;
const mount = (url = `?revision_id=${id(4)}`, selectedId = id(4)) => { const client = new QueryClient({ defaultOptions: { queries: { retry: false } } }); clients.push(client); return render(<QueryClientProvider client={client}><MemoryRouter initialEntries={[url]}><HistoricalRepublishWorkbench workspaceId={id(1)} noteId={id(2)} selectedId={selectedId} /><Location /></MemoryRouter></QueryClientProvider>); };
afterEach(() => { cleanup(); clients.splice(0).forEach((c) => c.clear()); vi.resetAllMocks(); });
it("requires both confirmations, persists original Apply across reload and resumes only the same attempt", async () => {
 api.target.mockResolvedValue(target); api.begin.mockResolvedValue(review); api.apply.mockRejectedValueOnce(new HistoricalRepublishApiError("lost"));
 mount(); const begin = await screen.findByRole("button", { name: "恢复此版本／建立审阅预览" }); await waitFor(() => expect(begin).toBeEnabled()); fireEvent.click(begin);
 const apply = await screen.findByRole("button", { name: "确认整篇恢复并创建新候选" }); expect(apply).toBeDisabled(); expect(screen.queryByRole("textbox")).not.toBeInTheDocument();
 fireEvent.click(screen.getByRole("checkbox", { name: /我已审阅/ })); expect(apply).toBeDisabled(); fireEvent.click(screen.getByRole("checkbox", { name: /明确替代/ })); fireEvent.click(apply);
 await screen.findByText("lost"); const command = api.apply.mock.calls[0]?.[0]; expect(command).toMatchObject({ confirm_exact_restore: true, retire_current_candidate: true, preview_fingerprint: review.previewFingerprint }); expect(command).not.toHaveProperty("final_content");
 const url = screen.getByLabelText("location").textContent; expect(url).toContain("restore_apply_key="); expect(url).toContain(`restore_workspace=${id(1)}`); cleanup();
 api.read.mockResolvedValue(review); api.apply.mockResolvedValue(applied); mount(url); const retry = await screen.findByRole("button", { name: "使用原命令精确重试" }); expect(api.begin).toHaveBeenCalledTimes(1); expect(api.apply).toHaveBeenCalledTimes(1); fireEvent.click(retry); await screen.findByRole("button", { name: "继续创建原恢复提案" }); expect(api.apply.mock.calls[1]?.[0]).toEqual(command);
 api.resume.mockResolvedValue({ ...applied, result: { revisionId: id(10), articleRevisionId: id(11), publicationId: id(12), proposalId: id(13), proposalRevisionId: id(14) } }); fireEvent.click(screen.getByRole("button", { name: "继续创建原恢复提案" })); expect(await screen.findByRole("link", { name: "审阅恢复提案" })).toHaveAttribute("href", `/proposals/${id(13)}`); expect(api.resume).toHaveBeenCalledWith(id(1), id(2), id(4), id(3), expect.any(AbortSignal)); expect(api.begin).toHaveBeenCalledTimes(1);
});
it("does not post or read another selected revision from a mismatched recovery link", () => { mount(`?restore_key=old&restore_workspace=${id(1)}&restore_selected=${id(99)}`); expect(screen.getByRole("alert")).toHaveTextContent("恢复链接"); expect(api.target).not.toHaveBeenCalled(); expect(api.read).not.toHaveBeenCalled(); expect(api.begin).not.toHaveBeenCalled(); });
it("server denial leaves restore disabled and explains the actual error", async () => { api.target.mockRejectedValue(new HistoricalRepublishApiError("当前根目录读取许可已失效", 403)); mount(); expect(await screen.findByRole("alert")).toHaveTextContent("当前根目录读取许可已失效"); expect(screen.getByRole("button", { name: "恢复此版本／建立审阅预览" })).toBeDisabled(); });

it("reload after an uncommitted Begin reads first, then explicitly retries the original full command", async () => {
 api.target.mockResolvedValue(target); api.begin.mockRejectedValueOnce(new HistoricalRepublishApiError("begin lost")); mount(); const button = await screen.findByRole("button", { name: "恢复此版本／建立审阅预览" }); await waitFor(() => expect(button).toBeEnabled()); fireEvent.click(button); await screen.findByText("begin lost");
 const original = api.begin.mock.calls[0]?.[0]; const url = screen.getByLabelText("location").textContent; expect(url).toContain("restore_begin="); cleanup();
 api.read.mockRejectedValue(new HistoricalRepublishApiError("尚无持久预览", 404)); api.begin.mockResolvedValue(review); mount(url); await screen.findByText("尚无持久预览"); expect(api.begin).toHaveBeenCalledTimes(1); expect(api.target).toHaveBeenCalledTimes(1);
 fireEvent.click(screen.getByRole("button", { name: "使用原命令重试建立预览" })); await screen.findByRole("button", { name: "确认整篇恢复并创建新候选" }); expect(api.begin.mock.calls[1]?.[0]).toEqual(original);
});
