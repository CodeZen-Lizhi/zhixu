import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { setActiveWorkspaceId } from "../../app/active-workspace";
import { synthesisId, synthesisProcessingFixture, synthesisTime, synthesisWorkspaceId as workspaceId } from "../../test/synthesis-fixtures";
import { SynthesisGoalsPanel } from "./SynthesisGoalsPanel";

const fetchMock = vi.fn<typeof fetch>();
const prefix = `/api/v1/workspaces/${workspaceId}/synthesis/goals`;
const json = (value: unknown, status = 200) => new Response(JSON.stringify(value), { status, headers: { "Content-Type": "application/json" } });
const requestUrl = (input: RequestInfo | URL): string => typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
const renderPanel = () => {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  render(<QueryClientProvider client={client}><MemoryRouter><SynthesisGoalsPanel /></MemoryRouter></QueryClientProvider>);
  return client;
};
const request = { id: synthesisId(30), workspace_id: workspaceId, goal: "整理 Redis 专项知识", status: "CATALOG_READY", error_code: null, version: 1, created_at: synthesisTime, updated_at: synthesisTime };
const failedSelection = { id: synthesisId(40), workspace_id: workspaceId, request_id: request.id, status: "FAILED", error_code: "SYNTHESIS_GOAL_MODEL_FAILED", retryable: true, version: 1, created_at: synthesisTime, updated_at: synthesisTime };
const selectionPage = Array.from({ length: 20 }, (_, index) => index === 0 ? failedSelection : { ...failedSelection, id: synthesisId(40 + index), status: "SUCCEEDED", error_code: null, retryable: false });
const failedGoal = () => ({ request, progress: { preparation_failures: 0, preparation_error_code: null, catalog_batches: 1, prepared_batches: 1, selections: 20, pending: 0, running: 0, succeeded: 19, failed: 1, recovery_required: 0, selected_points: 0, ready: false }, processing: null, candidate: null });

beforeEach(() => { setActiveWorkspaceId(workspaceId); vi.stubGlobal("fetch", fetchMock); });
afterEach(() => { cleanup(); fetchMock.mockReset(); setActiveWorkspaceId(""); vi.unstubAllGlobals(); });

describe("主笔记目标入口", () => {
  it("创建响应丢失后保留原目标和幂等键，即使输入框已改动", async () => {
    const creates: RequestInit[] = [];
    fetchMock.mockImplementation((input, init) => {
      const url = requestUrl(input);
      if (url === `${prefix}?limit=20`) return Promise.resolve(json({ workspace_id: workspaceId, items: [], next_cursor: null }));
      if (url === prefix && init?.method === "POST") {
        creates.push(init);
        return creates.length === 1 ? Promise.reject(new Error("response lost")) : Promise.resolve(json({ request, replayed: true }, 202));
      }
      return Promise.reject(new Error(`unexpected request: ${url}`));
    });
    renderPanel();
    const input = await screen.findByLabelText("希望整理的内容");
    fireEvent.change(input, { target: { value: request.goal } });
    fireEvent.click(screen.getByRole("button", { name: "开始整理" }));
    expect(await screen.findByText("尚未确认目标是否已保存")).toBeInTheDocument();
    fireEvent.change(input, { target: { value: "整理 MySQL 专项知识" } });
    fireEvent.click(screen.getByRole("button", { name: "重试" }));
    await waitFor(() => expect(creates).toHaveLength(2));
    expect(new Headers(creates[0]?.headers).get("Idempotency-Key")).toBe(new Headers(creates[1]?.headers).get("Idempotency-Key"));
    expect(creates.map((entry) => entry.body)).toEqual([`{"goal":"${request.goal}"}`, `{"goal":"${request.goal}"}`]);
  });

  it("使用 after_id 读取完整失败筛选记录，并以原 CAS 和幂等键重试", async () => {
    const retries: RequestInit[] = [];
    fetchMock.mockImplementation((input, init) => {
      const url = requestUrl(input);
      if (url === `${prefix}?limit=20`) return Promise.resolve(json({ workspace_id: workspaceId, items: [failedGoal()], next_cursor: null }));
      if (url === `${prefix}/${request.id}/selections?limit=20`) return Promise.resolve(json({ workspace_id: workspaceId, request_id: request.id, items: selectionPage, next_after_id: synthesisId(59) }));
      if (url === `${prefix}/${request.id}/selections?limit=20&after_id=${synthesisId(59)}`) return Promise.resolve(json({ workspace_id: workspaceId, request_id: request.id, items: [{ ...failedSelection, id: synthesisId(60), status: "RECOVERY_REQUIRED", retryable: false, error_code: "SYNTHESIS_GOAL_RESULT_UNKNOWN" }], next_after_id: null }));
      if (url === `${prefix}/${request.id}/selections/${failedSelection.id}/retry`) {
        retries.push(init ?? {});
        return retries.length === 1 ? Promise.reject(new Error("response lost")) : Promise.resolve(json({ ...failedSelection, status: "PENDING", error_code: null, retryable: false, version: 2 }, 202));
      }
      return Promise.reject(new Error(`unexpected request: ${url}`));
    });
    renderPanel();
    expect(await screen.findByText("筛选失败")).toBeInTheDocument();
    fireEvent.click(await screen.findByRole("button", { name: "重试知识筛选" }));
    expect(await screen.findByText("筛选重试未完成")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "重试知识筛选" }));
    await waitFor(() => expect(retries).toHaveLength(2));
    expect(new Headers(retries[0]?.headers).get("Idempotency-Key")).toBe(new Headers(retries[1]?.headers).get("Idempotency-Key"));
    expect(retries.map((entry) => entry.body)).toEqual(['{"expected_version":1}', '{"expected_version":1}']);
    fireEvent.click(screen.getByRole("button", { name: "加载更多筛选记录" }));
    expect(await screen.findByText("这次筛选的执行结果尚不能确认，不能自动重做。")).toBeInTheDocument();
  });

  it("候选只链接到冻结 revision，且不把它显示为已发布", async () => {
    const revisionId = synthesisId(31), noteId = synthesisId(32);
    const ready = { request, progress: { preparation_failures: 0, preparation_error_code: null, catalog_batches: 1, prepared_batches: 1, selections: 1, pending: 0, running: 0, succeeded: 1, failed: 0, recovery_required: 0, selected_points: 2, ready: true },
      processing: { ...synthesisProcessingFixture(), status: "SUCCEEDED", revision_ids: [revisionId], failure: null }, candidate: { note_id: noteId, revision_id: revisionId } };
    fetchMock.mockImplementation((input) => requestUrl(input) === `${prefix}?limit=20`
      ? Promise.resolve(json({ workspace_id: workspaceId, items: [ready], next_cursor: null }))
      : Promise.reject(new Error(`unexpected request: ${requestUrl(input)}`)));
    renderPanel();
    const candidate = await screen.findByRole("link", { name: "审阅候选版本" });
    expect(candidate).toHaveAttribute("href", `/authoring/notes/${noteId}?revision_id=${revisionId}`);
    expect(screen.getByText("候选已生成")).toBeInTheDocument();
    expect(screen.queryByText("已发布")).not.toBeInTheDocument();
  });
});
