import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { decodeSynthesisNoteDetail, type SynthesisNote } from "../../api/synthesis";
import { synthesisDetailFixture, synthesisId, synthesisSourceFixture, synthesisWorkspaceId as workspaceId } from "../../test/synthesis-fixtures";
import { AnchorSetupPanel } from "./AnchorSetupPanel";

const fetchMock = vi.fn<typeof fetch>();
const clients: QueryClient[] = [];
const json = (value: unknown, status = 200) => new Response(JSON.stringify(value), { status, headers: { "Content-Type": "application/json" } });
const urlOf = (input: RequestInfo | URL) => typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
const note = decodeSynthesisNoteDetail(synthesisDetailFixture(), workspaceId, synthesisId(8)).note;
const suggestion = { title: "Redis 专项", kind: "INITIAL_SCOPE", scope: { topics: ["Redis"], audiences: ["开发者"], description: "Redis 的机制与使用" }, reason: "笔记内容介绍缓存和过期机制。", evidence: [synthesisSourceFixture()] };
const completed = { id: synthesisId(81), workspace_id: workspaceId, note_id: note.id, anchor_id: null, basis_revision_id: note.currentRevisionId, expected_scope_version: null,
  kind: "INITIAL_SCOPE", status: "SUCCEEDED", recommendation: suggestion, proposal_id: null, error_code: null, retryable: false, version: 3, created_at: "2026-09-14T00:00:00Z", updated_at: "2026-09-14T00:00:01Z" };
const mount = (value: SynthesisNote = note) => { const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } }); clients.push(client); return render(<QueryClientProvider client={client}><MemoryRouter><AnchorSetupPanel note={value} /></MemoryRouter></QueryClientProvider>); };
beforeEach(() => { vi.stubGlobal("fetch", fetchMock); });
afterEach(() => { cleanup(); clients.splice(0).forEach((client) => client.clear()); fetchMock.mockReset(); vi.unstubAllGlobals(); });

it("发起异步分析，读取 AI 建议并在确认时保留同一请求重试", async () => {
  let requested = false;
  const confirms: RequestInit[] = [];
  const analysis: RequestInit[] = [];
  fetchMock.mockImplementation((input, init) => {
    const url = urlOf(input);
    if (url.endsWith("/anchor-recommendations") && init?.method === "POST") { requested = true; analysis.push(init); return Promise.resolve(json({ ...completed, status: "PENDING", recommendation: null, version: 1 }, 202)); }
    if (url.includes("/anchor-recommendations?")) return Promise.resolve(json({ items: requested ? [completed] : [], next_after_id: null }));
    if (url.endsWith("/anchors") && init?.method === "POST") {
      confirms.push(init);
      if (confirms.length === 1) return Promise.reject(new Error("lost response"));
      return Promise.resolve(json({ anchor: { id: synthesisId(82), workspace_id: workspaceId, note_id: note.id, basis_revision_id: note.currentRevisionId, title: suggestion.title, scope: suggestion.scope, scope_version: 1, version: 1, created_at: completed.created_at, updated_at: completed.created_at }, replayed: true }, 201));
    }
    return Promise.reject(new Error(`unexpected request ${url}`));
  });
  mount();
  await waitFor(() => expect(screen.getByRole("button", { name: "分析维护范围" })).toBeEnabled());
  fireEvent.click(screen.getByRole("button", { name: "分析维护范围" }));
  expect(await screen.findByDisplayValue("Redis 专项")).toBeInTheDocument();
  expect(confirms).toHaveLength(0);
  expect(analysis).toHaveLength(1);
  expect(JSON.parse(analysis[0]?.body as string)).toEqual({ note_id: note.id, basis_revision_id: note.currentRevisionId, expected_note_version: note.version });
  const source = screen.getByRole("link", { name: "查看依据：缓存实践原文" });
  expect(source.getAttribute("href")).toContain(`revision_id=${synthesisId(7)}`);
  expect(source.getAttribute("href")).toContain(`source_span_id=${synthesisId(6)}`);
  fireEvent.click(screen.getByRole("button", { name: "确认并持续维护" }));
  const error = await screen.findByRole("alert");
  expect(screen.getByDisplayValue("Redis 专项")).toBeDisabled();
  fireEvent.click(within(error).getByRole("button", { name: "重试" }));
  expect(await screen.findByText("已确认维护范围，可在来源审核中处理后续更新。")).toBeInTheDocument();
  expect(confirms).toHaveLength(2);
  expect(confirms[0]?.body).toBe(confirms[1]?.body);
  expect(new Headers(confirms[0]?.headers).get("Idempotency-Key")).toBe(new Headers(confirms[1]?.headers).get("Idempotency-Key"));
});
it("旧版本建议不可确认，恢复必需不提供直接重试", async () => {
  fetchMock.mockResolvedValue(json({ items: [completed, { ...completed, id: synthesisId(83), status: "RECOVERY_REQUIRED", recommendation: null, error_code: "ANCHOR_MODEL_RECOVERY_REQUIRED" }], next_after_id: null }));
  mount({ ...note, currentRevisionId: synthesisId(84), version: 2 });
  expect(await screen.findByText("笔记内容已更新。这份建议基于旧内容，请重新分析后确认。")).toBeInTheDocument();
  expect(screen.getByRole("button", { name: "确认并持续维护" })).toBeDisabled();
  expect(screen.queryByRole("button", { name: "重试这次分析" })).not.toBeInTheDocument();
});
