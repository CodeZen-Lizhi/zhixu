import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { setActiveWorkspaceId } from "../../app/active-workspace";
import { clearWorkspaceRuntimeState } from "../../app/workspace-runtime-state";
import {
  synthesisDetailFixture, synthesisId, synthesisPreparationFixture, synthesisProcessingFixture, synthesisRevisionFixture,
  synthesisRevisionSummaryFixture, synthesisSourceFixture, synthesisWorkspaceId as workspaceId,
} from "../../test/synthesis-fixtures";
import { SynthesisNotePage } from "./SynthesisNotePage";
import { SynthesisNotesPage } from "./SynthesisNotesPage";
import { synthesisQueryKeys } from "./queries";

const fetchMock = vi.fn<typeof fetch>();
const clients: QueryClient[] = [];
const prefix = `/api/v1/workspaces/${workspaceId}/synthesis`;
const json = (value: unknown, status = 200) => new Response(JSON.stringify(value), { status, headers: { "Content-Type": "application/json" } });
const requestUrl = (input: RequestInfo | URL): string => typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
const sourceLinkParams = () => {
  const reference = synthesisSourceFixture();
  return new URLSearchParams({ ...reference.source, source_span_id: reference.source_span_id, excerpt_hash: reference.excerpt_hash });
};
const Location = () => <output data-testid="location">{useLocation().search}</output>;
const renderPage = (path = "/authoring/notes") => {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: Infinity }, mutations: { retry: false } } });
  clients.push(client);
  render(<QueryClientProvider client={client}><MemoryRouter initialEntries={[path]}><Location /><Routes>
    <Route path="/authoring/notes" element={<SynthesisNotesPage />} />
    <Route path="/authoring/notes/:noteId" element={<SynthesisNotePage />} />
  </Routes></MemoryRouter></QueryClientProvider>);
  return client;
};

beforeEach(() => { setActiveWorkspaceId(workspaceId); vi.stubGlobal("fetch", fetchMock); });
afterEach(() => { cleanup(); clients.splice(0).forEach((client) => client.clear()); fetchMock.mockReset(); setActiveWorkspaceId(""); vi.unstubAllGlobals(); });

const mockDetail = (availability: "AVAILABLE" | "STALE" = "AVAILABLE") => {
  const original = synthesisRevisionFixture();
  const current = { ...original, id: synthesisId(21), article_revision_id: synthesisId(22), revision_no: 2, article_revision_no: 2, parent_revision_id: original.id, content_hash: "e".repeat(64), projection_hash: "f".repeat(64),
    items: original.items.map((item, index) => index === 0 ? { ...item, fact: { text: "新增资料补充了缓存过期策略", applicability: "高频查询", sources: [synthesisSourceFixture()] } } : item) };
  const detail = { ...synthesisDetailFixture(), note: { ...synthesisDetailFixture().note, current_revision_id: current.id, status: "PENDING_APPROVAL" }, current_revision: current,
    publication: { ...synthesisDetailFixture().publication, revision_id: current.id, article_revision_id: current.article_revision_id, content_hash: current.content_hash } };
  fetchMock.mockImplementation((input) => {
    const url = requestUrl(input);
    if (url === `${prefix}/notes/${synthesisId(8)}`) return Promise.resolve(json(detail));
    if (url === `${prefix}/notes/${synthesisId(8)}/revisions?limit=20`) return Promise.resolve(json({ workspace_id: workspaceId, note_id: synthesisId(8), items: [
      { ...synthesisRevisionSummaryFixture(), id: current.id, article_revision_id: current.article_revision_id, revision_no: 2, article_revision_no: 2, content_hash: current.content_hash }, synthesisRevisionSummaryFixture(),
    ], next_cursor: null }));
    if (url === `${prefix}/notes/${synthesisId(8)}/revisions/${original.id}`) return Promise.resolve(json({ workspace_id: workspaceId, note_id: synthesisId(8), revision: original }));
    if (url.endsWith("/interviews")) return Promise.resolve(json({ items: [] }));
    for (const revision of [original, current]) {
      if (url === `${prefix}/notes/${synthesisId(8)}/revisions/${revision.id}/sources/${synthesisId(6)}`) return Promise.resolve(json({ workspace_id: workspaceId, note_id: synthesisId(8), revision_id: revision.id, reference: synthesisSourceFixture(), availability, text: availability === "AVAILABLE" ? "原始版本片段" : null }));
    }
    return Promise.reject(new Error(`unexpected request: ${url}`));
  });
};

describe("合成笔记工作台", () => {
  it("首次失败无笔记仍可见，只有可重试失败显示重试，丢失响应保持同一幂等键", async () => {
    let retried = false;
    let attempts = 0;
    const retryCalls: RequestInit[] = [];
    fetchMock.mockImplementation((input, init) => {
      const url = requestUrl(input);
      if (url === `${prefix}/notes?limit=20`) return Promise.resolve(json({ workspace_id: workspaceId, items: [], next_cursor: null }));
      if (url.endsWith("/retry")) {
        retryCalls.push(init ?? {}); attempts += 1;
        if (attempts === 1) return Promise.reject(new Error("response lost"));
        retried = true;
        return Promise.resolve(json({ workspace_id: workspaceId, processing: { ...synthesisProcessingFixture(), status: "PENDING", completed_at: null, failure: null, version: 2 }, replayed: true }, 202));
      }
      if (url === `${prefix}/processing?limit=20`) return Promise.resolve(json({ workspace_id: workspaceId, items: [
        retried ? { ...synthesisProcessingFixture(), status: "PENDING", completed_at: null, failure: null, version: 2 } : synthesisProcessingFixture(),
        { ...synthesisProcessingFixture(), id: synthesisId(19), status: "RECOVERY_REQUIRED", failure: { code: "SYNTHESIS_RESULT_UNKNOWN", retryable: false }, created_at: "2026-09-08T08:00:00Z", updated_at: "2026-09-08T08:00:00Z", completed_at: "2026-09-08T08:00:00Z" },
      ], next_cursor: null }));
      return Promise.reject(new Error(`unexpected request: ${url}`));
    });
    renderPage();
    expect(await screen.findByRole("heading", { name: "还没有合成笔记" })).toBeInTheDocument();
    expect(await screen.findByText("需要人工恢复")).toBeInTheDocument();
    expect(screen.getAllByRole("button", { name: "重试整理" })).toHaveLength(1);
    fireEvent.click(screen.getByRole("button", { name: "重试整理" }));
    expect(await screen.findByText("重试未完成")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "重试整理" }));
    await waitFor(() => expect(retryCalls).toHaveLength(2));
    expect(new Headers(retryCalls[0]?.headers).get("Idempotency-Key")).toBe(new Headers(retryCalls[1]?.headers).get("Idempotency-Key"));
    expect(retryCalls.map((call) => call.body)).toEqual(['{"expected_version":1}', '{"expected_version":1}']);
  });

  it("切换历史版本后按需读取该版本来源，并在关闭后恢复键盘焦点", async () => {
    mockDetail();
    renderPage(`/authoring/notes/${synthesisId(8)}`);
    expect(await screen.findByText("新增资料补充了缓存过期策略")).toBeInTheDocument();
    expect(fetchMock.mock.calls.some(([url]) => requestUrl(url).includes("/sources/"))).toBe(false);
    fireEvent.click(screen.getByRole("button", { name: /^版本 1/ }));
    expect(await screen.findByText("缓存需要明确的过期策略")).toBeInTheDocument();
    expect(screen.getByTestId("location")).toHaveTextContent(`revision_id=${synthesisId(7)}`);
    const trigger = screen.getAllByRole("button", { name: /缓存实践原文，打开原始片段/ })[0];
    if (trigger === undefined) throw new Error("source trigger missing");
    fireEvent.click(trigger);
    expect(await screen.findByText("原始版本片段")).toBeInTheDocument();
    expect(fetchMock.mock.calls.some(([url]) => requestUrl(url) === `${prefix}/notes/${synthesisId(8)}/revisions/${synthesisId(7)}/sources/${synthesisId(6)}`)).toBe(true);
    fireEvent.click(screen.getByRole("button", { name: "关闭" }));
    await waitFor(() => expect(trigger).toHaveFocus());
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });

  it("来源失效时保留历史引用并明确显示失效，不请求新版本", async () => {
    mockDetail("STALE");
    renderPage(`/authoring/notes/${synthesisId(8)}?revision_id=${synthesisId(7)}`);
    const trigger = (await screen.findAllByRole("button", { name: /缓存实践原文，打开原始片段/ }))[0];
    if (trigger === undefined) throw new Error("source trigger missing");
    fireEvent.click(trigger);
    expect(await screen.findByText("来源已变化")).toBeInTheDocument();
    expect(screen.queryByText("原始版本片段")).not.toBeInTheDocument();
    expect(fetchMock.mock.calls.filter(([url]) => requestUrl(url).includes("/sources/")).map(([url]) => requestUrl(url))).toEqual([`${prefix}/notes/${synthesisId(8)}/revisions/${synthesisId(7)}/sources/${synthesisId(6)}`]);
  });

  it("发布 Markdown 的完整来源链接打开保存的精确片段，关闭后刷新不会再次弹出", async () => {
    mockDetail();
    const params = sourceLinkParams();
    params.set("title", "URL 中的伪造标题");
    const client = renderPage(`/authoring/notes/${synthesisId(8)}?${params.toString()}`);
    expect(await screen.findByRole("dialog", { name: "缓存实践原文" })).toBeInTheDocument();
    expect(await screen.findByText("原始版本片段")).toBeInTheDocument();
    expect(screen.queryByText("URL 中的伪造标题")).not.toBeInTheDocument();
    expect(fetchMock.mock.calls.filter(([url]) => requestUrl(url).includes("/sources/")).map(([url]) => requestUrl(url))).toEqual([`${prefix}/notes/${synthesisId(8)}/revisions/${synthesisId(21)}/sources/${synthesisId(6)}`]);
    fireEvent.click(screen.getByRole("button", { name: "关闭" }));
    await waitFor(() => expect(screen.getByRole("region", { name: "笔记内容" }).querySelector(".synthesis-note-content")).toHaveFocus());
    fireEvent.click(screen.getByRole("button", { name: "刷新" }));
    await waitFor(() => expect(fetchMock.mock.calls.filter(([url]) => requestUrl(url) === `${prefix}/notes/${synthesisId(8)}`)).toHaveLength(2));
    await waitFor(() => expect(client.getQueryState(synthesisQueryKeys.note(workspaceId, synthesisId(8)))?.fetchStatus).toBe("idle"));
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(fetchMock.mock.calls.filter(([url]) => requestUrl(url).includes("/sources/"))).toHaveLength(1);
  });

  it("显式历史版本的来源深链接始终请求该冻结版本", async () => {
    mockDetail();
    const params = sourceLinkParams();
    params.set("revision_id", synthesisId(7));
    renderPage(`/authoring/notes/${synthesisId(8)}?${params.toString()}`);
    expect(await screen.findByText("原始版本片段")).toBeInTheDocument();
    expect(fetchMock.mock.calls.filter(([url]) => requestUrl(url).includes("/sources/")).map(([url]) => requestUrl(url))).toEqual([`${prefix}/notes/${synthesisId(8)}/revisions/${synthesisId(7)}/sources/${synthesisId(6)}`]);
  });

  it.each(["source_id", "source_version_id", "content_artifact_id", "parse_projection_id", "source_span_id", "content_hash", "excerpt_hash"])("来源链接的 %s 不匹配时明确失败，不读取其他来源", async (field) => {
    mockDetail();
    const params = sourceLinkParams();
    params.set(field, field.endsWith("_hash") ? "a".repeat(64) : synthesisId(100));
    renderPage(`/authoring/notes/${synthesisId(8)}?${params.toString()}`);
    expect(await screen.findByText("无法定位原始来源")).toBeInTheDocument();
    expect(screen.getByText("所选笔记版本未保存这个精确引用。请选择对应的历史版本，或从笔记中的来源按钮重新打开。")).toBeInTheDocument();
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(fetchMock.mock.calls.some(([url]) => requestUrl(url).includes("/sources/"))).toBe(false);
  });

  it.each([
    { name: "缺少信息", change: (params: URLSearchParams) => { params.delete("content_artifact_id"); } },
    { name: "重复参数", change: (params: URLSearchParams) => { params.append("source_span_id", synthesisId(6)); } },
    { name: "非法 UUID", change: (params: URLSearchParams) => { params.set("source_version_id", "invalid"); } },
    { name: "非规范哈希", change: (params: URLSearchParams) => { params.set("content_hash", "B".repeat(64)); } },
    { name: "跨 Workspace", change: (params: URLSearchParams) => { params.set("workspace_id", synthesisId(100)); } },
  ])("来源链接$name时不发出来源请求", async ({ change }) => {
    mockDetail();
    const params = sourceLinkParams();
    change(params);
    renderPage(`/authoring/notes/${synthesisId(8)}?${params.toString()}`);
    expect(await screen.findByText("无法定位原始来源")).toBeInTheDocument();
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(fetchMock.mock.calls.some(([url]) => requestUrl(url).includes("/sources/"))).toBe(false);
  });

  it("准备请求响应丢失后从服务端列表恢复，保留继续面试入口", async () => {
    const ready = { ...synthesisPreparationFixture(), status: "READY", session_id: synthesisId(20) };
    let created = false;
    fetchMock.mockImplementation((input, init) => {
      const url = requestUrl(input);
      if (init?.method === "POST") { created = true; return Promise.reject(new Error("response lost")); }
      if (url.endsWith("/interviews")) return Promise.resolve(json({ items: created ? [ready] : [] }));
      if (url.endsWith("/revisions?limit=20")) return Promise.resolve(json({ workspace_id: workspaceId, note_id: synthesisId(8), items: [synthesisRevisionSummaryFixture()], next_cursor: null }));
      if (url === `${prefix}/notes/${synthesisId(8)}`) return Promise.resolve(json(synthesisDetailFixture()));
      return Promise.reject(new Error(`unexpected request: ${url}`));
    });
    renderPage(`/authoring/notes/${synthesisId(8)}`);
    fireEvent.click(await screen.findByRole("button", { name: "准备 AI 面试" }));
    expect(await screen.findByRole("link", { name: "继续面试" })).toHaveAttribute("href", `/interviews/${synthesisId(20)}`);
    expect(fetchMock.mock.calls.filter(([, init]) => init?.method === "POST")).toHaveLength(1);
    expect(screen.getByText("基于笔记版本 1，6 道题。")).toBeInTheDocument();
  });

  it("重试面试响应丢失后即使发布新版，仍复用冻结版本的幂等键和原选项", async () => {
    const initial = synthesisDetailFixture();
    const revision = { ...initial.current_revision, id: synthesisId(21), article_revision_id: synthesisId(22), revision_no: 2, article_revision_no: 2, parent_revision_id: initial.current_revision.id };
    const published = { ...initial, note: { ...initial.note, current_revision_id: revision.id, version: 3 }, current_revision: revision, published_revision: revision,
      publication: { ...initial.publication, revision_id: revision.id, article_revision_id: revision.article_revision_id } };
    const failed = { ...synthesisPreparationFixture(), status: "FAILED", failure: { code: "NOTE_INTERVIEW_MODEL_UNAVAILABLE", retryable: true } };
    const ready = { ...failed, status: "READY", session_id: synthesisId(20), failure: null };
    const retries: RequestInit[] = [];
    fetchMock.mockImplementation((input, init) => {
      const url = requestUrl(input);
      if (init?.method === "POST") {
        retries.push(init);
        return retries.length === 1 ? Promise.reject(new Error("response lost")) : Promise.resolve(json({ preparation: ready, replayed: true }, 202));
      }
      if (url.endsWith(`/interviews/${failed.id}`)) return Promise.resolve(json({ preparation: ready, replayed: false }));
      if (url.endsWith("/interviews")) return Promise.resolve(json({ items: [retries.length > 1 ? ready : failed] }));
      if (url.endsWith("/revisions?limit=20")) return Promise.resolve(json({ workspace_id: workspaceId, note_id: synthesisId(8), items: [synthesisRevisionSummaryFixture()], next_cursor: null }));
      if (url === `${prefix}/notes/${synthesisId(8)}`) return Promise.resolve(json(retries.length > 0 ? published : initial));
      return Promise.reject(new Error(`unexpected request: ${url}`));
    });
    renderPage(`/authoring/notes/${synthesisId(8)}`);
    fireEvent.click(await screen.findByRole("button", { name: "重试准备" }));
    expect(await screen.findByText("面试准备请求未完成")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "刷新" }));
    expect(await screen.findByText("正式版本 2")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("面试方向"), { target: { value: "新版本方向" } });
    fireEvent.click(screen.getByRole("button", { name: "重试准备" }));
    expect(await screen.findByRole("link", { name: "继续面试" })).toHaveAttribute("href", `/interviews/${synthesisId(20)}`);
    await waitFor(() => expect(screen.queryByText("面试准备请求未完成")).not.toBeInTheDocument());
    expect(screen.getByTestId("location")).toHaveTextContent(`preparation_id=${failed.id}`);
    expect(retries).toHaveLength(2);
    expect(new Headers(retries[1]?.headers).get("Idempotency-Key")).toBe(new Headers(retries[0]?.headers).get("Idempotency-Key"));
    expect(retries[1]?.body).toBe(retries[0]?.body);
    expect(screen.getByText("基于笔记版本 1，6 道题。")).toBeInTheDocument();
  });

  it("切换 Workspace 清除笔记、历史、原文和面试准备缓存", async () => {
    const client = new QueryClient();
    clients.push(client);
    const keys = [synthesisQueryKeys.note(workspaceId, synthesisId(8)), synthesisQueryKeys.revisions(workspaceId, synthesisId(8)), ["synthesis", workspaceId, "source", synthesisId(8)], synthesisQueryKeys.preparations(workspaceId, synthesisId(8))];
    keys.forEach((key) => client.setQueryData(key, "old workspace"));
    const other = synthesisQueryKeys.note(synthesisId(100), synthesisId(8));
    client.setQueryData(other, "other workspace");
    await clearWorkspaceRuntimeState(client, workspaceId);
    keys.forEach((key) => expect(client.getQueryData(key)).toBeUndefined());
    expect(client.getQueryData(other)).toBe("other workspace");
  });
});
