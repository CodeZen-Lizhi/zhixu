import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, expect, it, vi } from "vitest";
import { decodeSynthesisRevision } from "../../api/synthesis";
import { setActiveWorkspaceId } from "../../app/active-workspace";
import { synthesisId, synthesisRevisionFixture, synthesisSourceFixture, synthesisWorkspaceId } from "../../test/synthesis-fixtures";
import { SourceGraph } from "./SourceGraph";

afterEach(() => { cleanup(); vi.unstubAllGlobals(); setActiveWorkspaceId(""); });
it("按来源选择共享笔记，分页保留选择且不会提前断言没有关联", async () => {
  setActiveWorkspaceId(synthesisWorkspaceId);
  const wire = synthesisRevisionFixture();
  const second = { ...synthesisSourceFixture(), title: "Redis 面试资料", source: { ...synthesisSourceFixture().source, source_id: synthesisId(90) }, source_span_id: synthesisId(91) };
  const revision = decodeSynthesisRevision({ ...wire, items: [...wire.items, { id: synthesisId(92), kind: "FACT", fact: { text: "第二份资料说明 Redis 持久化", applicability: "", sources: [second] }, conflict: null, gap: null }] }, synthesisWorkspaceId, wire.note_id);
  const envelope = { workspace_id: synthesisWorkspaceId, note_id: revision.noteId, revision_id: revision.id, sources: [synthesisSourceFixture(), second] };
  const firstPage = Array.from({ length: 20 }, (_, index) => ({ note_id: synthesisId(100 + index), revision_id: synthesisId(200 + index), revision_no: 1, title: `缓存笔记 ${String(index + 1)}`, sources: [synthesisSourceFixture()] }));
  const fetchMock = vi.fn<typeof fetch>().mockResolvedValueOnce(new Response(JSON.stringify({ ...envelope, shared_notes: firstPage, next_after_note_id: synthesisId(119) }), { headers: { "Content-Type": "application/json" } }))
    .mockResolvedValueOnce(new Response(JSON.stringify({ ...envelope, shared_notes: [{ note_id: synthesisId(120), revision_id: synthesisId(220), revision_no: 1, title: "Redis 持久化专项", sources: [second] }], next_after_note_id: null }), { headers: { "Content-Type": "application/json" } }));
  vi.stubGlobal("fetch", fetchMock);
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(<QueryClientProvider client={client}><MemoryRouter><SourceGraph revision={revision} /></MemoryRouter></QueryClientProvider>);
  fireEvent.click(screen.getByRole("button", { name: "查看来源图谱" }));
  fireEvent.click(await screen.findByRole("button", { name: "Redis 面试资料" }));
  expect(screen.getByText("已加载的主笔记中暂无共享此来源的记录。")).toBeInTheDocument();
  fireEvent.click(screen.getByRole("button", { name: "加载更多共享主笔记" }));
  expect(await screen.findByRole("link", { name: "Redis 持久化专项 · 已发布版本 1" })).toBeInTheDocument();
  expect(within(screen.getByRole("region", { name: "共享来源的主笔记" })).queryByText("缓存笔记 1 · 已发布版本 1")).not.toBeInTheDocument();
  expect(screen.getByRole("button", { name: "Redis 面试资料" })).toHaveAttribute("aria-pressed", "true");
  await waitFor(() => { expect(screen.queryByRole("button", { name: "加载更多共享主笔记" })).not.toBeInTheDocument(); });
  expect(fetchMock.mock.calls[1]?.[0]).toContain(`after_note_id=${synthesisId(119)}`);
  client.clear();
});
