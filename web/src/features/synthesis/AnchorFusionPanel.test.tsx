import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, expect, it, vi } from "vitest";
import { decodeAnchorFusionRecent, decodeKnowledgeAnchor, type KnowledgeAnchor } from "../../api/synthesis";
import { setActiveWorkspaceId } from "../../app/active-workspace";
import { synthesisId, synthesisProcessingFixture, synthesisSourceFixture, synthesisWorkspaceId as workspaceId } from "../../test/synthesis-fixtures";
import { AnchorFusionPanel } from "./AnchorFusionPanel";

const anchor: KnowledgeAnchor = decodeKnowledgeAnchor({ id: synthesisId(50), workspace_id: workspaceId, note_id: synthesisId(8), basis_revision_id: synthesisId(7), title: "Redis",
  scope: { topics: ["Redis"], audiences: ["复习"], description: "Redis 复习" }, scope_version: 1, version: 1, created_at: "2026-09-14T00:00:00Z", updated_at: "2026-09-14T00:00:00Z" }, workspaceId);
const request = { id: synthesisId(60), workspace_id: workspaceId, anchor_id: anchor.id, note_id: anchor.noteId, proposal_id: synthesisId(61), scope_version: 1,
  sources: [synthesisSourceFixture()], status: "PENDING", processing_id: null as string | null, created_at: anchor.createdAt, updated_at: anchor.updatedAt };
const envelope = (items: unknown[]) => ({ workspace_id: workspaceId, anchor_id: anchor.id, items });
const json = (value: unknown) => new Response(JSON.stringify(value), { headers: { "Content-Type": "application/json" } });
const clients: QueryClient[] = [];
afterEach(() => { cleanup(); clients.splice(0).forEach((client) => client.clear()); vi.unstubAllGlobals(); setActiveWorkspaceId(""); });
const mount = () => {
  setActiveWorkspaceId(workspaceId);
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } }); clients.push(client);
  render(<QueryClientProvider client={client}><MemoryRouter><AnchorFusionPanel anchor={anchor} /></MemoryRouter></QueryClientProvider>);
};
it("融合记录拒绝把接受或入队当成成功，并核对笔记来源绑定", () => {
  expect(decodeAnchorFusionRecent(envelope([request]), anchor)[0]?.status).toBe("PENDING");
  for (const bad of [{ ...request, status: "SUCCEEDED" }, { ...request, status: "DISPATCHED" }, { ...request, processing_id: synthesisId(15) }, { ...request, note_id: synthesisId(90) }, { ...request, sources: [] }]) {
    expect(() => decodeAnchorFusionRecent(envelope([bad]), anchor)).toThrow();
  }
});
it("按需读取，入队后显示真实失败状态，刷新可见新结果", async () => {
  let completed = false;
  const fetchMock = vi.fn<typeof fetch>().mockImplementation((input) => {
    const url = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
    if (url.endsWith("/fusion-requests")) return Promise.resolve(json(envelope([{ ...request, status: "DISPATCHED", processing_id: synthesisId(15) }])));
    if (url.endsWith(`/processing/${synthesisId(15)}`)) return Promise.resolve(json({ workspace_id: workspaceId, processing: completed ? { ...synthesisProcessingFixture(), status: "NO_CHANGE", failure: null } : synthesisProcessingFixture() }));
    return Promise.reject(new Error("unexpected request"));
  });
  vi.stubGlobal("fetch", fetchMock);mount();
  expect(fetchMock).not.toHaveBeenCalled();
  fireEvent.click(screen.getByRole("button", { name: "查看来源融合进度" }));
  expect(await screen.findByText("整理失败")).toBeInTheDocument();
  expect(screen.queryByText("已生成更新")).not.toBeInTheDocument();
  completed = true;
  fireEvent.click(screen.getByRole("button", { name: "刷新融合记录" }));
  expect(await screen.findByText("已覆盖，无需更新")).toBeInTheDocument();
});
it("范围或来源已变化时显示失效，不查询不存在的整理任务", async () => {
  const fetchMock = vi.fn<typeof fetch>().mockResolvedValue(json(envelope([{ ...request, status: "STALE" }])));vi.stubGlobal("fetch", fetchMock);mount();
  fireEvent.click(screen.getByRole("button", { name: "查看来源融合进度" }));
  expect(await screen.findByText("维护范围或来源已变化，本次融合请求已失效，请重新检查关联建议。")).toBeInTheDocument();
  expect(fetchMock).toHaveBeenCalledTimes(1);
});
