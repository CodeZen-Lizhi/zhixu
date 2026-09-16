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
    if (url === `${prefix}/notes/${synthesisId(8)}/revisions/${current.id}`) return Promise.resolve(json({ workspace_id: workspaceId, note_id: synthesisId(8), revision: current }));
    if (url === `${prefix}/notes/${synthesisId(8)}/revisions/${original.id}`) return Promise.resolve(json({ workspace_id: workspaceId, note_id: synthesisId(8), revision: original }));
    if (url.endsWith("/interviews")) return Promise.resolve(json({ items: [] }));
    for (const revision of [original, current]) {
      if (url === `${prefix}/notes/${synthesisId(8)}/revisions/${revision.id}/sources/${synthesisId(6)}`) return Promise.resolve(json({ workspace_id: workspaceId, note_id: synthesisId(8), revision_id: revision.id, reference: synthesisSourceFixture(), availability, text: availability === "AVAILABLE" ? "原始版本片段" : null }));
    }
    return Promise.reject(new Error(`unexpected request: ${url}`));
  });
};

describe("合成笔记工作台", () => {
  it("按版本哈希定位被引用片段并拒绝漂移链接", async () => {
    mockDetail();
    const scroll = vi.fn();
    Object.defineProperty(HTMLElement.prototype, "scrollIntoView", { configurable: true, value: scroll });
    const revision = synthesisRevisionFixture();
    const params = new URLSearchParams({ revision_id: revision.id, body_item_id: synthesisId(11), body_workspace_id: workspaceId, body_projection_hash: revision.projection_hash });
    renderPage(`/authoring/notes/${synthesisId(8)}?${params.toString()}`);
    await waitFor(() => expect(document.activeElement?.id).toBe(`synthesis-item-${synthesisId(11)}`));
    expect(scroll).toHaveBeenCalled();
    cleanup();
    params.set("body_projection_hash", "0".repeat(64));
    renderPage(`/authoring/notes/${synthesisId(8)}?${params.toString()}`);
    expect(await screen.findByText("无法定位引用的主笔记片段")).toBeInTheDocument();
    cleanup();
    params.set("body_projection_hash", revision.projection_hash);
    params.set("revision_id", "");
    renderPage(`/authoring/notes/${synthesisId(8)}?${params.toString()}`);
    expect(await screen.findByText("无法定位引用的主笔记片段")).toBeInTheDocument();
    Reflect.deleteProperty(HTMLElement.prototype, "scrollIntoView");
  });

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
    expect(await screen.findByRole("heading", { name: "还没有主笔记" })).toBeInTheDocument();
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
    expect(await screen.findByText("缓存需要明确的过期策略")).toBeInTheDocument();
    expect(screen.queryByText("新增资料补充了缓存过期策略")).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "审阅候选版本 2" }));
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

  it("按需打开后补证据，读取它保存的片段并保持已发布正文", async () => {
    mockDetail();
    const original = fetchMock.getMockImplementation();
    const supplement = { id: synthesisId(40), workspace_id: workspaceId, note_id: synthesisId(8), base_revision_id: synthesisId(7), item_id: synthesisId(11),
      slot: "FACT", alternative_index: -1, processing_id: synthesisId(9), reference: synthesisSourceFixture(), created_at: "2026-09-14T00:00:00Z" };
    fetchMock.mockImplementation((input, init) => {
      const url = requestUrl(input);
      if (url.endsWith("/supplements?limit=20")) return Promise.resolve(json({ workspace_id: workspaceId, note_id: synthesisId(8), items: [supplement], next_cursor: null }));
      if (url.endsWith(`/supplements/${supplement.id}/source`)) return Promise.resolve(json({ workspace_id: workspaceId, note_id: synthesisId(8), supplement, availability: "AVAILABLE", text: "后来加入的支持证据" }));
      if (!original) throw new Error("detail fixture missing");
      return original(input, init);
    });
    renderPage(`/authoring/notes/${synthesisId(8)}`);
    expect(await screen.findByText("缓存需要明确的过期策略")).toBeInTheDocument();
    expect(fetchMock.mock.calls.some(([url]) => requestUrl(url).includes("/supplements"))).toBe(false);
    fireEvent.click(screen.getByRole("button", { name: "后补来源" }));
    const button = await screen.findByRole("button", { name: "缓存实践原文，查看补充片段" });
    fireEvent.click(button);
    expect(await screen.findByText("后来加入的支持证据")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "关闭" }));
    await waitFor(() => expect(button).toHaveFocus());
    expect(screen.getByText("缓存需要明确的过期策略")).toBeInTheDocument();
    expect(screen.queryByText("新增资料补充了缓存过期策略")).not.toBeInTheDocument();
  });

  it("没有已发布版本时不把候选正文显示为正式笔记", async () => {
    const detail = { ...synthesisDetailFixture(), published_revision: null };
    fetchMock.mockImplementation((input) => {
      const url = requestUrl(input);
      if (url === `${prefix}/notes/${synthesisId(8)}`) return Promise.resolve(json(detail));
      if (url.includes("/revisions?")) return Promise.resolve(json({ workspace_id: workspaceId, note_id: synthesisId(8), items: [], next_cursor: null }));
      if (url.endsWith("/interviews")) return Promise.resolve(json({ items: [] }));
      return Promise.reject(new Error(`unexpected request: ${url}`));
    });
    renderPage(`/authoring/notes/${synthesisId(8)}`);
    expect(await screen.findByRole("heading", { name: "这份主笔记尚未发布" })).toBeInTheDocument();
    expect(screen.queryByText("缓存需要明确的过期策略")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "审阅候选版本 1" })).toBeInTheDocument();
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
    expect(fetchMock.mock.calls.filter(([url]) => requestUrl(url).includes("/sources/")).map(([url]) => requestUrl(url))).toEqual([`${prefix}/notes/${synthesisId(8)}/revisions/${synthesisId(7)}/sources/${synthesisId(6)}`]);
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

it("来源弹层按需展开该版本的片段知识目录", async () => {
  mockDetail();
  const fallback = fetchMock.getMockImplementation();
  const reference = synthesisSourceFixture();
  const point = { locator: { profile_revision_id: synthesisId(70), kind: "KNOWLEDGE_POINT", index: 0 }, text: "该片段说明缓存到期策略", source_span_ids: [reference.source_span_id] };
  fetchMock.mockImplementation((input, init) => {
    if (requestUrl(input).endsWith("/knowledge-points")) return Promise.resolve(json({ workspace_id: workspaceId, note_id: synthesisId(8), revision_id: synthesisId(7), reference,
      directory: { workspace_id: workspaceId, source_version_id: reference.source.source_version_id, status: "ANALYZED", profile_status: "READY", profile_revision_id: synthesisId(70),
        parse_projection_id: reference.source.parse_projection_id, summary: "缓存策略", topics: [{ label: "缓存", source_span_ids: [reference.source_span_id] }], points: [point] }, points: [point] }));
    if (!fallback) throw new Error("fixture missing");
    return fallback(input, init);
  });
  renderPage(`/authoring/notes/${synthesisId(8)}`);
  const trigger = (await screen.findAllByRole("button", { name: /缓存实践原文，打开原始片段/ }))[0];
  if (!trigger) throw new Error("source missing");
  fireEvent.click(trigger);
  expect(await screen.findByText("原始版本片段")).toBeInTheDocument();
  expect(fetchMock.mock.calls.some(([input]) => requestUrl(input).endsWith("/knowledge-points"))).toBe(false);
  fireEvent.click(screen.getByRole("button", { name: "查看片段对应知识点" }));
  expect(await screen.findByText("该片段说明缓存到期策略")).toBeInTheDocument();
  expect(screen.getByText("缓存需要明确的过期策略")).toBeInTheDocument();
});

it("来源图谱按需读取已发布版本，并链接共享笔记的精确引用", async () => {
  mockDetail();
  const original = fetchMock.getMockImplementation();
  const reference = synthesisSourceFixture();
  const peer = { note_id: synthesisId(80), revision_id: synthesisId(81), revision_no: 2, title: "数据库面试复习", sources: [{ ...reference, source_span_id: synthesisId(82) }] };
  fetchMock.mockImplementation((input, init) => {
    if (requestUrl(input).endsWith("/source-graph?limit=20")) return Promise.resolve(json({ workspace_id: workspaceId, note_id: synthesisId(8), revision_id: synthesisId(7), sources: [reference], shared_notes: [peer], next_after_note_id: null }));
    if (!original) throw new Error("missing original mock");
    return original(input, init);
  });
  renderPage(`/authoring/notes/${synthesisId(8)}`);
  const trigger = await screen.findByRole("button", { name: "查看来源图谱" });
  expect(fetchMock.mock.calls.some(([input]) => requestUrl(input).includes("/source-graph"))).toBe(false);
  fireEvent.click(trigger);
  const peerLink = await screen.findByRole("link", { name: "数据库面试复习 · 已发布版本 2" });
  expect(peerLink).toHaveAttribute("href", `/authoring/notes/${peer.note_id}?revision_id=${peer.revision_id}`);
  const sourceLink = screen.getByRole("link", { name: "查看它引用的片段 1" });
  const href = new URL(sourceLink.getAttribute("href") ?? "", "http://localhost");
  expect(href.searchParams.get("source_span_id")).toBe(synthesisId(82));
  expect(href.searchParams.get("source_version_id")).toBe(reference.source.source_version_id);
  expect(href.searchParams.get("revision_id")).toBe(peer.revision_id);
  expect(fetchMock.mock.calls.some(([input]) => requestUrl(input) === `${prefix}/notes/${synthesisId(8)}/revisions/${synthesisId(7)}/source-graph?limit=20`)).toBe(true);
  fireEvent.click(screen.getByRole("button", { name: "审阅候选版本 2" }));
  await waitFor(() => { expect(screen.getByRole("button", { name: "查看来源图谱" })).toHaveAttribute("aria-expanded", "false"); });
});

it("来源失效时仍显示经过验证的历史原文，并保留来源不可用标记", async () => {
  mockDetail();
  const original = fetchMock.getMockImplementation();
  fetchMock.mockImplementation((input, init) => {
    const url = requestUrl(input);
    if (url === `${prefix}/notes/${synthesisId(8)}/revisions/${synthesisId(7)}/sources/${synthesisId(6)}`) return Promise.resolve(json({ workspace_id: workspaceId, note_id: synthesisId(8), revision_id: synthesisId(7), reference: synthesisSourceFixture(), availability: "UNAVAILABLE", text: null, snapshot_text: "被删除文件的历史原始片段" }));
    if (!original) throw new Error("detail fixture missing");
    return original(input, init);
  });
  renderPage(`/authoring/notes/${synthesisId(8)}`);
  const triggers = await screen.findAllByRole("button", { name: /缓存实践原文，打开原始片段/ });
  const trigger = triggers[0]; if (!trigger) throw new Error("missing source trigger");
  fireEvent.click(trigger);
  expect(await screen.findByText("被删除文件的历史原始片段")).toBeInTheDocument();
  expect(screen.getByText("来源不可用")).toBeInTheDocument();
  expect(screen.getByText("已保存的历史原文，已核验片段哈希。")).toBeInTheDocument();
  expect(screen.queryByText("无法核验这个历史片段。笔记保留原引用，没有替换为其他资料。")).not.toBeInTheDocument();
});

it("列表按已发布及候选版本显示持久提醒，刷新重读计数且不链接漂移版本", async () => {
  const detail = synthesisDetailFixture();
  const published = synthesisRevisionSummaryFixture();
  const current = { ...published, id: synthesisId(21), article_revision_id: synthesisId(22), revision_no: 2, article_revision_no: 2 };
  const note = { note: { ...detail.note, current_revision_id: current.id, status: "PENDING_APPROVAL" }, current_revision: current, published_revision: published, publication: null, item_count: 3, conflict_count: 1, gap_count: 1, open_gap_count: 1 };
  let summaryReads = 0, failed = false, stale = false;
  fetchMock.mockImplementation((input) => {
    const url = requestUrl(input);
    if (url === `${prefix}/notes?limit=20`) return Promise.resolve(json({ workspace_id: workspaceId, items: [note], next_cursor: null }));
    if (url.includes("/update-summaries?")) {
      summaryReads++;
      if (failed) return Promise.resolve(json({ error_code: "READ_FAILED", message: "unavailable", retryable: true }, 503));
      return Promise.resolve(json({ workspace_id: workspaceId, items: [{ note_id: note.note.id, current_revision_id: stale ? synthesisId(99) : current.id, published_revision_id: published.id, items: [
        { revision_id: published.id, source_review_count: 1, body_review_count: 0 },
        { revision_id: current.id, source_review_count: 0, body_review_count: 2 },
      ] }] }));
    }
    if (url === `${prefix}/processing?limit=20` || url.includes("/goals?")) return Promise.resolve(json({ workspace_id: workspaceId, items: [], next_cursor: null }));
    return Promise.reject(new Error(`unexpected request: ${url}`));
  });
  renderPage();
  expect(await screen.findByRole("link", { name: "已发布版本 1：来源待复核 1 条" })).toHaveAttribute("href", `/authoring/notes/${note.note.id}?revision_id=${published.id}`);
  expect(screen.getByRole("link", { name: "候选版本 2：正文引用待复核 2 条" })).toHaveAttribute("href", `/authoring/notes/${note.note.id}?revision_id=${current.id}`);
  expect(screen.getByRole("link", { name: /缓存失效策略/ })).toHaveAttribute("href", `/authoring/notes/${note.note.id}`);
  failed = true;
  fireEvent.click(screen.getByRole("button", { name: "刷新" }));
  expect(await screen.findByText("更新提醒读取失败或版本已变化，请刷新列表重试。")).toBeInTheDocument();
  expect(screen.queryByRole("link", { name: /正文引用待复核/ })).not.toBeInTheDocument();
  failed = false; stale = true;
  fireEvent.click(screen.getByRole("button", { name: "刷新" }));
  await waitFor(() => expect(summaryReads).toBeGreaterThanOrEqual(3));
  expect(await screen.findByText("更新提醒读取失败或版本已变化，请刷新列表重试。")).toBeInTheDocument();
  expect(document.querySelector(`a[href*="${synthesisId(99)}"]`)).toBeNull();
  stale = false;
  fireEvent.click(screen.getByRole("button", { name: "刷新" }));
  expect(await screen.findByRole("link", { name: /候选版本 2：正文引用待复核/ })).toBeInTheDocument();
});

it("人工全文在刷新和历史读取后保留，历史来源明确待复核且安全渲染", async () => {
  const original = synthesisRevisionFixture();
  const manual = "人工批注：不要把缓存命中率当成一致性保证。";
  const revision = { ...original, items: [], display: { renderer_version: "synthesis-markdown/v2", full_content: `# 人工版本\n\n${manual}\n\n<script>alert('xss')</script>\n[危险链接](javascript:alert(1))\n![外部图片](https://example.com/track.png)`, manual_changes: true, review_required: true, historical_sources: [synthesisSourceFixture()] } };
  const detail = { ...synthesisDetailFixture(), current_revision: revision, published_revision: revision };
  fetchMock.mockImplementation((input) => {
    const url = requestUrl(input);
    if (url === `${prefix}/notes/${original.note_id}`) return Promise.resolve(json(detail));
    if (url === `${prefix}/notes/${original.note_id}/revisions/${original.id}`) return Promise.resolve(json({ workspace_id: workspaceId, note_id: original.note_id, revision }));
    if (url.endsWith(`/sources/${synthesisId(6)}`)) return Promise.resolve(json({ workspace_id: workspaceId, note_id: original.note_id, revision_id: original.id, reference: synthesisSourceFixture(), availability: "AVAILABLE", text: "历史原文", role: "HISTORICAL_REVIEW" }));
    if (url.includes("revisions?")) return Promise.resolve(json({ workspace_id: workspaceId, note_id: original.note_id, items: [synthesisRevisionSummaryFixture()], next_cursor: null }));
    if (url.endsWith("/interviews")) return Promise.resolve(json({ items: [] }));
    return Promise.reject(new Error(`unexpected request: ${url}`));
  });
  renderPage(`/authoring/notes/${original.note_id}`);
  expect(await screen.findByText(manual)).toBeInTheDocument();
  expect(screen.getByText("含人工编辑，来源需复核")).toBeInTheDocument();
  expect(document.querySelector("script, img[src], a[href^='javascript:']")).toBeNull();
  fireEvent.click(screen.getByText("历史参考来源（仅供复核）"));
  fireEvent.click(screen.getByRole("button", { name: /打开原始片段/ }));
  expect(await screen.findByText("历史原文")).toBeInTheDocument();
  expect(screen.getByText("历史参考，当前正文待复核")).toBeInTheDocument();
  expect(screen.getByText("此版本未记录该历史参考的知识目录快照，原文仍可追溯。")).toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "查看片段对应知识点" })).not.toBeInTheDocument();
  cleanup();
  renderPage(`/authoring/notes/${original.note_id}?revision_id=${original.id}`);
  expect(await screen.findByText(manual)).toBeInTheDocument();
  fireEvent.click(screen.getByRole("button", { name: "刷新" }));
  await waitFor(() => expect(screen.getByText(manual)).toBeInTheDocument());
});

it("v2全文片段索引可辨认知识点，精确聚焦冲突且相同来源只显示一次", async () => {
  const original = synthesisRevisionFixture();
  const revision = { ...original, display: { renderer_version: "synthesis-markdown/v2", full_content: "# 完整正文\n\n缓存策略全文。", manual_changes: false, review_required: false, historical_sources: [] } };
  const detail = { ...synthesisDetailFixture(), current_revision: revision, published_revision: revision };
  fetchMock.mockImplementation((input) => {
    const url = requestUrl(input);
    if (url === `${prefix}/notes/${original.note_id}`) return Promise.resolve(json(detail));
    if (url === `${prefix}/notes/${original.note_id}/revisions/${original.id}`) return Promise.resolve(json({ workspace_id: workspaceId, note_id: original.note_id, revision }));
    if (url.includes("revisions?")) return Promise.resolve(json({ workspace_id: workspaceId, note_id: original.note_id, items: [synthesisRevisionSummaryFixture()], next_cursor: null }));
    if (url.endsWith("/interviews")) return Promise.resolve(json({ items: [] }));
    return Promise.reject(new Error(`unexpected request: ${url}`));
  });
  const scroll = vi.fn();
  Object.defineProperty(HTMLElement.prototype, "scrollIntoView", { configurable: true, value: scroll });
  const errors = vi.spyOn(console, "error");
  try {
    const params = new URLSearchParams({ revision_id: original.id, body_item_id: synthesisId(12), body_workspace_id: workspaceId, body_projection_hash: original.projection_hash });
    renderPage(`/authoring/notes/${original.note_id}?${params.toString()}`);
    expect(await screen.findByRole("heading", { name: "全文片段索引" })).toBeInTheDocument();
    expect(screen.getByText("以下摘录用于辨认上方全文中的知识点及其来源，不是新增正文。")).toBeInTheDocument();
    expect(screen.getByText("缓存需要明确的过期策略")).toBeInTheDocument();
    expect(screen.getByText("缓存容量应如何确定？")).toBeInTheDocument();
    await waitFor(() => expect(document.activeElement?.id).toBe(`synthesis-item-${synthesisId(12)}`));
    const focused = document.activeElement;
    expect(focused).toHaveTextContent("过期时间存在不同建议");
    expect(focused).toHaveTextContent("使用 30 秒过期时间");
    expect(focused).toHaveTextContent("使用 60 秒过期时间");
    expect(focused?.querySelectorAll(".synthesis-source-links button")).toHaveLength(1);
    expect(scroll).toHaveBeenCalled();
    expect(errors.mock.calls.some((args) => args.some((value) => typeof value === "string" && /same key|unique.*key/i.test(value)))).toBe(false);
  } finally {
    errors.mockRestore();
    Reflect.deleteProperty(HTMLElement.prototype, "scrollIntoView");
  }
});
