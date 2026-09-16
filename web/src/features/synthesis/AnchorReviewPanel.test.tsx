import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { setActiveWorkspaceId } from "../../app/active-workspace";
import { synthesisId, synthesisSourceFixture, synthesisWorkspaceId as workspaceId } from "../../test/synthesis-fixtures";
import { AnchorReviewPanel } from "./AnchorReviewPanel";

const fetchMock = vi.fn<typeof fetch>();
const clients: QueryClient[] = [];
const json = (value: unknown) => new Response(JSON.stringify(value), { headers: { "Content-Type": "application/json" } });
const urlOf = (input: RequestInfo | URL) => typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
const anchor = { id: synthesisId(50), workspace_id: workspaceId, note_id: synthesisId(8), basis_revision_id: synthesisId(7), title: "Redis 专项",
  scope: { topics: ["Redis"], audiences: ["开发者"], description: "Redis 使用与机制" }, scope_version: 1, version: 1,
  created_at: "2026-09-14T00:00:00Z", updated_at: "2026-09-14T00:00:00Z" };
const association = (number: number) => ({ id: synthesisId(number), workspace_id: workspaceId, anchor_id: anchor.id, kind: "SOURCE_ASSOCIATION",
  scope_version: 1, before: null, suggested: null, reason: `补充缓存知识 ${String(number)}`, evidence: [synthesisSourceFixture()],
  model_run_id: synthesisId(60), status: "PENDING", version: 1, created_at: anchor.created_at });
const mount = () => {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  clients.push(client);
  render(<QueryClientProvider client={client}><MemoryRouter><AnchorReviewPanel noteId={anchor.note_id} /></MemoryRouter></QueryClientProvider>);
};
beforeEach(() => { setActiveWorkspaceId(workspaceId); vi.stubGlobal("fetch", fetchMock); });
afterEach(() => { cleanup(); clients.splice(0).forEach((client) => client.clear()); fetchMock.mockReset(); setActiveWorkspaceId(""); vi.unstubAllGlobals(); });

it("按需读取并批量接受；响应丢失后冻结原选择和幂等键，刷新显示持久审核结果", async () => {
  const records = [association(51), association(52)];
  const calls: RequestInit[] = [];
  let accepted = false;
  fetchMock.mockImplementation((input, init) => {
    const url = urlOf(input);
    if (url.includes("/associations/decisions")) {
      calls.push(init ?? {});
      accepted = true;
      if (calls.length === 1) return Promise.reject(new Error("response lost"));
      return Promise.resolve(json({ anchor: { ...anchor, version: 2 }, items: records.map((item) => ({ ...item, status: "ACCEPTED", version: 2 })), replayed: true }));
    }
    if (url.includes("/associations?")) return Promise.resolve(json({ items: records.map((item) => accepted ? { ...item, status: "ACCEPTED", version: 2 } : item), next_after_id: null }));
    if (url.includes("/scope-proposals?")) return Promise.resolve(json({ items: [], next_after_id: null }));
    if (url.includes("/anchors?")) return Promise.resolve(json({ items: [{ ...anchor, version: accepted ? 2 : 1 }], next_after_id: null }));
    return Promise.reject(new Error(`unexpected request: ${url}`));
  });
  mount();
  expect(fetchMock).not.toHaveBeenCalled();
  fireEvent.click(screen.getByRole("button", { name: "维护范围与来源审核" }));
  fireEvent.click(await screen.findByRole("checkbox", { name: "选择：补充缓存知识 51" }));
  fireEvent.click(screen.getByRole("checkbox", { name: "选择：补充缓存知识 52" }));
  fireEvent.click(screen.getByRole("button", { name: "接受所选（2）" }));
  const failure = await screen.findByRole("alert");
  expect(failure).toHaveTextContent("审核结果尚未确认");
  expect(screen.getByRole("checkbox", { name: "选择：补充缓存知识 51" })).toBeDisabled();
  fireEvent.click(within(failure).getByRole("button", { name: "重试" }));
  await waitFor(() => expect(screen.getAllByText("已接受")).toHaveLength(2));
  expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  expect(screen.getByText("审核结果已保存，正式正文保持原样。")).toBeInTheDocument();
  expect(calls).toHaveLength(2);
  expect(calls[0]?.body).toBe(calls[1]?.body);
  const retriedBody = calls[1]?.body;
  if (typeof retriedBody !== "string") throw new Error("missing decision body");
  expect(JSON.parse(retriedBody)).toEqual({ expected_anchor_version: 1, decision: "ACCEPTED", items: records.map((item) => ({ proposal_id: item.id, expected_version: 1 })) });
  expect(new Headers(calls[0]?.headers).get("Idempotency-Key")).toBe(new Headers(calls[1]?.headers).get("Idempotency-Key"));
  expect(screen.getByText("主题：Redis · 用途与受众：开发者")).toBeInTheDocument();
});

it("范围变化后保留旧建议可见，但旧建议不能再次纳入融合", async () => {
  fetchMock.mockImplementation((input) => {
    const url = urlOf(input);
    if (url.includes("/associations?")) return Promise.resolve(json({ items: [association(51)], next_after_id: null }));
    if (url.includes("/scope-proposals?")) return Promise.resolve(json({ items: [], next_after_id: null }));
    if (url.includes("/anchors?")) return Promise.resolve(json({ items: [{ ...anchor, scope_version: 2, version: 2 }], next_after_id: null }));
    return Promise.reject(new Error(`unexpected request: ${url}`));
  });
  mount();
  fireEvent.click(screen.getByRole("button", { name: "维护范围与来源审核" }));
  expect(await screen.findByText("范围已变化，建议过期")).toBeInTheDocument();
  expect(screen.getByText("补充缓存知识 51")).toBeInTheDocument();
  expect(screen.queryByRole("checkbox")).not.toBeInTheDocument();
  expect(screen.queryByRole("button", { name: /接受所选/ })).not.toBeInTheDocument();
});
