import { useEffect } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, expect, it, vi } from "vitest";
import { synthesisId as id } from "../../test/synthesis-fixtures";
import { ManuscriptReviewWorkbench } from "./ManuscriptReviewWorkbench";
import { WorkflowHumanTaskDecision } from "../business/WorkflowsPage";
const editor = vi.hoisted(() => ({ ready: true, mounts: [] as { onReady: () => void; onError: (e: Error) => void }[] }));
vi.mock("../../shared/MonacoTextEditor", () => ({ MonacoTextEditor: ({ value, onChange, disabled, onReady, onError }: { value: string; onChange: (v: string) => void; disabled: boolean; onReady: () => void; onError: (e: Error) => void }) => {
  useEffect(() => { editor.mounts.push({ onReady, onError }); if (editor.ready) onReady(); }, []);
  return <><textarea aria-label="完整合并结果" value={value} onChange={(e) => onChange(e.target.value)} disabled={disabled} /><button onClick={() => onError(new Error("editor failed"))}>模拟编辑器失败</button><button onClick={onReady}>模拟编辑器就绪</button></>;
} }));
vi.mock("../../shared/MonacoDiffViewer", () => ({ MonacoDiffViewer: ({ original, modified, onError }: { original: string; modified: string; onError: (e: Error) => void }) => <div>{original} / {modified}<button onClick={() => onError(new Error("diff failed"))}>模拟差异失败</button></div> }));
const binding = { workspace_id: id(1), processing_id: id(2), human_task_id: id(3), run_id: id(4), node_run_id: id(5), target_version: 2 };
const target = (note = 6, second = false, ready = false) => ({ note_id: id(note), attempt_id: id(note + 10), capture_id: id(note + 20), stage: second ? "WORKSPACE_MANUAL_CONTENT" : "CANDIDATE_MANUAL_CONTENT", preview_fingerprint: (second ? "b" : "a").repeat(64), ready });
const summary = (targets = [target()], submitted = false, b = binding) => ({ binding: b, targets, ready: targets.every((t) => t.ready), submitted });
const detail = (t = target(), b = binding) => ({ binding: b, target: t, review: { stage: t.stage, base: "base", current: "current", proposed: "proposed", candidate: t.stage === "WORKSPACE_MANUAL_CONTENT" ? "second candidate" : "first candidate", conflicts: [{ ordinal: 1, base: "base", current: "current", proposed: "proposed" }, { ordinal: 2, base: "base2", current: "current2", proposed: "proposed2" }] } });
const json = (v: unknown, status = 200) => new Response(JSON.stringify(v), { status, headers: { "content-type": "application/json" } });
const requestUrl = (input: RequestInfo | URL): string => typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
const requestBody = (init?: RequestInit): string => typeof init?.body === "string" ? init.body : "";
const clients: QueryClient[] = [];
afterEach(() => { editor.ready = true; editor.mounts = []; cleanup(); clients.splice(0).forEach((c) => c.clear()); vi.unstubAllGlobals(); });
const mount = (workspaceId = id(1)) => {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } }); clients.push(client);
  const view = (workspace: string) => <QueryClientProvider client={client}><MemoryRouter><ManuscriptReviewWorkbench workspaceId={workspace} processingId={id(2)} /></MemoryRouter></QueryClientProvider>;
  return { ...render(view(workspaceId)), view, client };
};
const select = async (number = 1) => { fireEvent.click(await screen.findByRole("button", { name: new RegExp(`主笔记 ${String(number)} ·`) })); await screen.findByRole("textbox", { name: "完整合并结果" }); };
const acknowledge = () => {
  fireEvent.mouseDown(screen.getByRole("tab", { name: "冲突逐项确认" }), { button: 0, ctrlKey: false });
  fireEvent.click(screen.getByRole("checkbox", { name: "已在完整结果中处理冲突 1" }));
  fireEvent.click(screen.getByRole("checkbox", { name: "已在完整结果中处理冲突 2" }));
};
it("two notes and two stages require fresh full acknowledgements; only final response reports candidate generation", async () => {
  let step = 0;
  const calls: { url: string; body: unknown }[] = [];
  vi.stubGlobal("fetch", vi.fn<typeof fetch>().mockImplementation((input, init) => {
    const url = requestUrl(input); calls.push({ url, body: init?.body ? JSON.parse(requestBody(init)) : null });
    if (url.endsWith("/decisions")) { step++; return Promise.resolve(json(step === 1 ? summary([target(6, true), target(7)]) : step === 2 ? summary([target(6, true, true), target(7)]) : summary([target(6, true, true), target(7, false, true)], true))); }
    return Promise.resolve(json(url.endsWith("/manuscript-review") ? summary([target(), target(7)]) : detail(target(url.endsWith(id(7)) ? 7 : 6, step > 0 && url.endsWith(id(6))))));
  }));
  mount(); await screen.findByText("请选择一份主笔记后读取全文预览。"); expect(calls).toHaveLength(1);
  await select(); expect(calls).toHaveLength(2);
  fireEvent.change(screen.getByRole("textbox"), { target: { value: "first edited result" } });
  expect(screen.getByRole("button", { name: "保存本阶段裁决" })).toBeDisabled(); acknowledge();
  fireEvent.click(screen.getByRole("button", { name: "保存本阶段裁决" }));
  await waitFor(() => expect(screen.getByRole("textbox")).toHaveValue("second candidate"));
  expect(screen.queryByText(/裁决已保存，正在生成/)).not.toBeInTheDocument();
  expect(screen.getByRole("button", { name: "保存本阶段裁决" })).toBeDisabled(); acknowledge();
  fireEvent.click(screen.getByRole("button", { name: "保存本阶段裁决" })); await screen.findByText("这份主笔记的裁决已保存。请处理其余待裁决笔记。");
  await select(2); acknowledge(); fireEvent.click(screen.getByRole("button", { name: "保存裁决并继续整理" }));
  await screen.findByText(/裁决已保存，正在生成待审核候选/);
  expect(calls.filter((c) => c.url.endsWith("/decisions"))).toHaveLength(3);
  expect(calls.some((c) => c.url.includes("/continue") || c.url.includes("proposals"))).toBe(false);
});
it("unknown response freezes text and replays exactly the same full command and key", async () => {
  const commands: string[] = [];
  vi.stubGlobal("fetch", vi.fn<typeof fetch>().mockImplementation((input, init) => {
    const url = requestUrl(input);
    if (url.endsWith("/decisions")) { commands.push(requestBody(init)); return commands.length === 1 ? Promise.reject(new TypeError("response lost")) : Promise.resolve(json(summary([target(6, false, true)], true))); }
    return Promise.resolve(json(url.endsWith("/manuscript-review") ? summary() : detail()));
  }));
  mount(); await select(); fireEvent.change(screen.getByRole("textbox"), { target: { value: "keep exact text" } }); acknowledge(); fireEvent.click(screen.getByRole("button", { name: "保存裁决并继续整理" }));
  const retry = await screen.findByRole("button", { name: "使用原命令精确重试" }); expect(screen.getByRole("textbox")).toHaveValue("keep exact text"); expect(screen.getByRole("textbox")).toBeDisabled();
  fireEvent.click(screen.getByRole("button", { name: "模拟编辑器失败" }));
  editor.ready = false; fireEvent.click(screen.getByRole("button", { name: "重新加载编辑器与差异" }));
  expect(screen.getByText(/提交结果未知。编辑与原命令已冻结/)).toBeInTheDocument();
  expect(retry).toBeEnabled();
  fireEvent.click(retry); await screen.findByText(/裁决已保存，正在生成待审核候选/); expect(commands[1]).toBe(commands[0]);
});
it("stale response keeps user editing visible and disables old submission", async () => {
  vi.stubGlobal("fetch", vi.fn<typeof fetch>().mockImplementation((input) => Promise.resolve(requestUrl(input).endsWith("/decisions") ? json({}, 409) : json(requestUrl(input).endsWith("/manuscript-review") ? summary() : detail()))));
  mount(); await select(); fireEvent.change(screen.getByRole("textbox"), { target: { value: "my stale text" } }); acknowledge(); fireEvent.click(screen.getByRole("button", { name: "保存裁决并继续整理" }));
  await screen.findByText(/当前任务或预览已失效。编辑保留供查看，旧提交已停用。/); expect(screen.getByRole("textbox")).toHaveValue("my stale text"); expect(screen.getByRole("button", { name: "保存裁决并继续整理" })).toBeDisabled(); expect(screen.queryByRole("button", { name: /重新准备/ })).not.toBeInTheDocument();
});
it("workspace switch ignores a late detail and never displays the previous workspace draft", async () => {
  let resolveOld: ((response: Response) => void) | undefined;
  vi.stubGlobal("fetch", vi.fn<typeof fetch>().mockImplementation((input) => {
    const url = requestUrl(input); if (url.includes(id(99))) return Promise.resolve(json(summary([target()], false, { ...binding, workspace_id: id(99) })));
    return url.endsWith("/manuscript-review") ? Promise.resolve(json(summary())) : new Promise<Response>((resolve) => { resolveOld = resolve; });
  }));
  const view = mount(); fireEvent.click(await screen.findByRole("button", { name: /主笔记 1 ·/ })); await waitFor(() => expect(resolveOld).toBeDefined());
  view.rerender(view.view(id(99))); act(() => { resolveOld?.(json(detail())); });
  expect(screen.queryByRole("textbox")).not.toBeInTheDocument(); await screen.findByText("请选择一份主笔记后读取全文预览。");
});
it("workflow owner task provides only controlled note-centre navigation", () => {
  const decide = vi.fn(); render(<MemoryRouter><WorkflowHumanTaskDecision task={{ id: id(3), runId: id(4), nodeRunId: id(5), status: "pending", targetVersion: 2, createdAt: "2026-09-15T00:00:00Z", decisionKind: "synthesis_manuscript", manuscript: { processingId: id(2), noteIds: [id(6)] }, review: null }} pending={false} error={null} onDecide={decide} /></MemoryRouter>);
  expect(screen.getByRole("link", { name: "处理主笔记冲突" })).toHaveAttribute("href", `/authoring/notes?processing=${id(2)}`); expect(screen.queryByRole("button", { name: "批准并继续" })).not.toBeInTheDocument(); expect(decide).not.toHaveBeenCalled();
});

it("selected-note switching retains edits and an obsolete task binding cannot submit", async () => {
  vi.stubGlobal("fetch", vi.fn<typeof fetch>().mockImplementation((input) => Promise.resolve(json(requestUrl(input).endsWith("/manuscript-review") ? summary([target(), target(7)]) : detail(target(requestUrl(input).endsWith(id(7)) ? 7 : 6))))));
  const { client } = mount(); await select(); fireEvent.change(screen.getByRole("textbox"), { target: { value: "note one draft" } });
  await select(2); await waitFor(() => expect(screen.getByRole("textbox")).toHaveValue("first candidate"));
  await select(1); await waitFor(() => expect(screen.getByRole("textbox")).toHaveValue("note one draft"));
  act(() => { client.setQueryData(["synthesis", id(1), "manuscript-review", id(2)], { binding: { workspaceId: id(1), processingId: id(2), humanTaskId: id(90), runId: id(4), nodeRunId: id(5), targetVersion: 3 }, ready: false, submitted: false, targets: [{ noteId: id(6), attemptId: id(16), captureId: id(26), stage: "CANDIDATE_MANUAL_CONTENT", previewFingerprint: "a".repeat(64), ready: false }] }); });
  expect(screen.getByRole("textbox")).toHaveValue("note one draft"); await waitFor(() => expect(screen.getByRole("textbox")).toBeDisabled()); expect(screen.getByRole("button", { name: "保存裁决并继续整理" })).toBeDisabled();
});

it("reload recovers all-ready receipts with binding-only resume and exact unknown replay", async () => {
  const commands: string[] = [];
  let reads = 0;
  vi.stubGlobal("fetch", vi.fn<typeof fetch>().mockImplementation((input, init) => {
    const url = requestUrl(input);
    if (url.endsWith("/resume")) { commands.push(requestBody(init)); return commands.length === 1 ? Promise.resolve(json({}, 503)) : Promise.resolve(json(summary([target(6, false, true)], true))); }
    reads++; return Promise.resolve(json(summary([target(6, false, true)])));
  }));
  mount(); fireEvent.click(await screen.findByRole("button", { name: "继续生成候选 / 恢复整理" }));
  fireEvent.click(await screen.findByRole("button", { name: "使用同一绑定重试恢复" }));
  await screen.findByText(/裁决已保存，正在生成待审核候选/);
  expect(commands).toHaveLength(2); expect(commands[0]).toBe(commands[1]); expect(JSON.parse(commands[0] ?? "{}")).toEqual({ binding }); expect(reads).toBe(1); expect(screen.queryByRole("textbox")).not.toBeInTheDocument();
});
it("nonwaiting processing is explicitly empty and does not fetch a detail", async () => {
  const fetchMock = vi.fn<typeof fetch>().mockResolvedValue(json({ error_code: "SYNTHESIS_MANUSCRIPT_REVIEW_NOT_PENDING", message: "not pending", retryable: false }, 404)); vi.stubGlobal("fetch", fetchMock);
  mount(); await screen.findByText("当前没有待处理的主笔记冲突。"); expect(fetchMock).toHaveBeenCalledTimes(1); expect(screen.queryByRole("textbox")).not.toBeInTheDocument(); expect(screen.queryByRole("button", { name: "继续生成候选 / 恢复整理" })).not.toBeInTheDocument();
});

it("an old submission response cannot replace a newly read human-task binding", async () => {
  let resolveSubmit: ((v: Response) => void) | undefined;
  vi.stubGlobal("fetch", vi.fn<typeof fetch>().mockImplementation((input) => requestUrl(input).endsWith("/decisions") ? new Promise<Response>((resolve) => { resolveSubmit = resolve; }) : Promise.resolve(json(requestUrl(input).endsWith("/manuscript-review") ? summary() : detail()))));
  const { client } = mount(); await select(); fireEvent.change(screen.getByRole("textbox"), { target: { value: "retain pending text" } }); acknowledge(); fireEvent.click(screen.getByRole("button", { name: "保存裁决并继续整理" })); await waitFor(() => expect(resolveSubmit).toBeDefined());
  const key = ["synthesis", id(1), "manuscript-review", id(2)];
  act(() => { client.setQueryData(key, { binding: { workspaceId: id(1), processingId: id(2), humanTaskId: id(90), runId: id(4), nodeRunId: id(5), targetVersion: 3 }, ready: false, submitted: false, targets: [{ noteId: id(6), attemptId: id(16), captureId: id(26), stage: "CANDIDATE_MANUAL_CONTENT", previewFingerprint: "a".repeat(64), ready: false }] }); });
  await screen.findByText(/旧编辑仅供查看/);
  act(() => { resolveSubmit?.(json(summary([target(6, false, true)], true))); });
  await screen.findByText("任务或阶段已变化。旧请求结果不会覆盖当前状态，编辑保留供查看。");
  expect(screen.getByRole("textbox")).toHaveValue("retain pending text"); expect(screen.queryByText(/裁决已保存，正在生成待审核候选/)).not.toBeInTheDocument();
});

it("replays the saved stage-one command after a lost response and summary refresh to stage two, even with editor failure", async () => {
  let stored = false;
  const commands: string[] = [];
  vi.stubGlobal("fetch", vi.fn<typeof fetch>().mockImplementation((input, init) => {
    const url = requestUrl(input);
    if (url.endsWith("/decisions")) {
      commands.push(requestBody(init)); stored = true;
      return commands.length === 1 ? Promise.reject(new TypeError("response lost after commit")) : Promise.resolve(json(summary([target(6, true)])));
    }
    return Promise.resolve(json(url.endsWith("/manuscript-review") ? summary([target(6, stored)]) : detail(target(6, stored))));
  }));
  const { client } = mount(); await select();
  fireEvent.change(screen.getByRole("textbox"), { target: { value: "exact stage-one manuscript" } }); acknowledge();
  fireEvent.click(screen.getByRole("button", { name: "保存裁决并继续整理" }));
  await screen.findByRole("button", { name: "使用原命令精确重试" });
  await act(async () => { await client.refetchQueries({ queryKey: ["synthesis", id(1), "manuscript-review", id(2)] }); });
  expect(screen.getByRole("textbox")).toHaveValue("exact stage-one manuscript");
  expect(screen.getByRole("button", { name: "使用原命令精确重试" })).toBeEnabled();
  fireEvent.click(screen.getByRole("button", { name: "模拟编辑器失败" }));
  fireEvent.click(screen.getByRole("button", { name: "使用原命令精确重试" }));
  await waitFor(() => expect(screen.getByRole("textbox")).toHaveValue("second candidate"));
  expect(commands).toHaveLength(2); expect(commands[1]).toBe(commands[0]);
  expect(screen.getByRole("button", { name: "保存裁决并继续整理" })).toBeDisabled();
});
it("new decisions require editor readiness and block editor/diff failures until explicit reload", async () => {
  editor.ready = false;
  const fetchMock = vi.fn<typeof fetch>().mockImplementation((input) => Promise.resolve(json(requestUrl(input).endsWith("/manuscript-review") ? summary() : detail()))); vi.stubGlobal("fetch", fetchMock);
  mount(); await select(); acknowledge();
  const save = screen.getByRole("button", { name: "保存裁决并继续整理" }); expect(save).toBeDisabled();
  fireEvent.click(screen.getByRole("button", { name: "模拟编辑器就绪" })); expect(save).toBeEnabled();
  fireEvent.click(screen.getByRole("button", { name: "模拟编辑器失败" })); expect(save).toBeDisabled();
  editor.ready = true; fireEvent.click(screen.getByRole("button", { name: "重新加载编辑器与差异" })); await waitFor(() => expect(save).toBeEnabled());
  fireEvent.mouseDown(screen.getByRole("tab", { name: "Base / Current" }), { button: 0, ctrlKey: false });
  fireEvent.click(await screen.findByRole("button", { name: "模拟差异失败" })); expect(save).toBeDisabled();
  fireEvent.click(screen.getByRole("button", { name: "重新加载编辑器与差异" })); await waitFor(() => expect(save).toBeEnabled());
  expect(fetchMock).toHaveBeenCalledTimes(2);
});

it("late callbacks from a replaced stage or load attempt cannot authorize or fail the current editor", async () => {
  let second = false;
  vi.stubGlobal("fetch", vi.fn<typeof fetch>().mockImplementation((input) => {
    const url = requestUrl(input);
    if (url.endsWith("/decisions")) { second = true; return Promise.resolve(json(summary([target(6, true)]))); }
    return Promise.resolve(json(url.endsWith("/manuscript-review") ? summary() : detail(target(6, second))));
  }));
  mount(); await select(); acknowledge();
  const oldStage = editor.mounts[0]; if (!oldStage) throw new Error("Editor did not mount");
  editor.ready = false;
  fireEvent.click(screen.getByRole("button", { name: "保存裁决并继续整理" }));
  await waitFor(() => expect(screen.getByRole("textbox")).toHaveValue("second candidate")); acknowledge();
  const save = screen.getByRole("button", { name: "保存裁决并继续整理" });
  act(() => oldStage.onReady()); expect(save).toBeDisabled();
  act(() => oldStage.onError(new Error("obsolete stage failure")));
  expect(screen.queryByText(/obsolete stage failure/)).not.toBeInTheDocument();
  const oldLoad = editor.mounts.at(-1); if (!oldLoad) throw new Error("Editor did not mount");
  act(() => oldLoad.onError(new Error("load failed")));
  act(() => oldLoad.onReady()); expect(save).toBeDisabled();
  fireEvent.click(screen.getByRole("button", { name: "重新加载编辑器与差异" }));
  act(() => { oldLoad.onReady(); oldLoad.onError(new Error("obsolete load failure")); });
  expect(save).toBeDisabled(); expect(screen.queryByText(/obsolete load failure/)).not.toBeInTheDocument();
  fireEvent.click(screen.getByRole("button", { name: "模拟编辑器就绪" })); expect(save).toBeEnabled();
});
it("an unknown command cannot replay into a different task even after late Monaco readiness", async () => {
  let changed = false;
  const commands: string[] = [];
  vi.stubGlobal("fetch", vi.fn<typeof fetch>().mockImplementation((input, init) => {
    const url = requestUrl(input);
    if (url.endsWith("/decisions")) { commands.push(requestBody(init)); return Promise.reject(new TypeError("lost response")); }
    const b = changed ? { ...binding, human_task_id: id(90), target_version: 3 } : binding;
    return Promise.resolve(json(url.endsWith("/manuscript-review") ? summary([target()], false, b) : detail(target(), b)));
  }));
  const { client } = mount(); await select(); acknowledge();
  const oldEditor = editor.mounts[0]; if (!oldEditor) throw new Error("Editor did not mount");
  fireEvent.click(screen.getByRole("button", { name: "保存裁决并继续整理" }));
  await screen.findByRole("button", { name: "使用原命令精确重试" }); changed = true;
  await act(async () => { await client.refetchQueries({ queryKey: ["synthesis", id(1), "manuscript-review", id(2)] }); });
  await screen.findByText(/预览身份已变化/);
  act(() => oldEditor.onReady());
  expect(screen.getByRole("button", { name: "保存裁决并继续整理" })).toBeDisabled();
  fireEvent.click(screen.getByRole("button", { name: "保存裁决并继续整理" })); expect(commands).toHaveLength(1);
});
it("Monaco readiness cannot clear a failed HTTP detail read", async () => {
  let fail = false;
  vi.stubGlobal("fetch", vi.fn<typeof fetch>().mockImplementation((input) => {
    const url = requestUrl(input);
    return Promise.resolve(url.endsWith("/manuscript-review") ? json(summary([target(), target(7)])) : fail && url.endsWith(id(6)) ? json({}, 503) : json(detail(target(url.endsWith(id(7)) ? 7 : 6))));
  }));
  mount(); await select(); acknowledge(); await select(2); fail = true; editor.ready = false; await select(1);
  await screen.findByText("暂时无法读取");
  fireEvent.click(screen.getByRole("button", { name: "模拟编辑器就绪" }));
  expect(screen.getByRole("button", { name: "保存本阶段裁决" })).toBeDisabled();
  expect(screen.getByText("暂时无法读取")).toBeInTheDocument();
});
