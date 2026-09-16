import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, expect, it, vi } from "vitest";
import { decodeSynthesisBodyImpacts, decodeSynthesisRevision } from "../../api/synthesis";
import { setActiveWorkspaceId } from "../../app/active-workspace";
import { synthesisId as id, synthesisRevisionFixture, synthesisWorkspaceId } from "../../test/synthesis-fixtures";
import { BodyImpactPanel } from "./BodyImpactPanel";

const ref = { workspace_id: synthesisWorkspaceId, note_id: id(100), revision_id: id(101), item_id: id(102), publication_id: id(103), projection_hash: "a".repeat(64) };
const wire = synthesisRevisionFixture();
const revision = decodeSynthesisRevision({ ...wire, items: wire.items.map((item, index) => index === 0 ? { ...item, body_reference: ref } : item) }, synthesisWorkspaceId, wire.note_id);
const impact = (n = 0) => ({ id: id(300 + n), item_id: id(11), upstream_note_id: ref.note_id, upstream_revision_id: ref.revision_id, upstream_item_id: ref.item_id, upstream_publication_id: ref.publication_id, publication_id: id(400 + n), published_revision_id: id(500 + n), reason: "CONTENT_CHANGED", detected_at: "2026-09-15T00:00:00Z" });
const page = (items = [impact()], next: string | null = null) => ({ workspace_id: synthesisWorkspaceId, note_id: wire.note_id, revision_id: wire.id, items, next_after_id: next });
const json = (value: unknown) => new Response(JSON.stringify(value), { headers: { "Content-Type": "application/json" } });
afterEach(() => { cleanup(); vi.unstubAllGlobals(); setActiveWorkspaceId(""); });

it("严格绑定当前展示版本、条目和旧引用，并拒绝分页漂移", () => {
  expect(decodeSynthesisBodyImpacts(page(), revision).items).toHaveLength(1);
  for (const field of ["workspace_id", "note_id", "revision_id"]) expect(() => decodeSynthesisBodyImpacts({ ...page(), [field]: id(999) }, revision)).toThrow();
  for (const field of ["item_id", "upstream_note_id", "upstream_revision_id", "upstream_item_id", "upstream_publication_id"]) expect(() => decodeSynthesisBodyImpacts(page([{ ...impact(), [field]: id(999) }]), revision)).toThrow();
  for (const value of [page([{ ...impact(), reason: "OTHER" }]), page([impact(), impact()]), page([impact()], id(300)), { ...page(), extra: true }]) expect(() => decodeSynthesisBodyImpacts(value, revision)).toThrow();
  expect(() => decodeSynthesisBodyImpacts(page(), revision, id(300))).toThrow();
  expect(decodeSynthesisBodyImpacts(page([impact()], id(300)), revision, null, 1).nextAfterId).toBe(id(300));
  const foreignRef = { ...revision, items: revision.items.map((item) => item.bodyReference ? { ...item, bodyReference: { ...item.bodyReference, workspaceId: id(999) } } : item) };
  expect(() => decodeSynthesisBodyImpacts(page(), foreignRef)).toThrow();
});

it("自动读取，保留准确历史链接，新版本不拼接旧 hash；续页失败可重试并刷新", async () => {
  setActiveWorkspaceId(synthesisWorkspaceId);
  const first = Array.from({ length: 20 }, (_, n) => impact(n));
  const last = { ...impact(20), reason: "ITEM_MISSING" };
  const fetchMock = vi.fn<typeof fetch>()
    .mockResolvedValueOnce(json(page(first, id(319))))
    .mockRejectedValueOnce(new Error("offline"))
    .mockResolvedValueOnce(json(page([last])))
    .mockResolvedValueOnce(json(page(first, id(319))))
    .mockResolvedValueOnce(json(page([last])));
  vi.stubGlobal("fetch", fetchMock);
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(<QueryClientProvider client={client}><MemoryRouter><BodyImpactPanel revision={revision} /></MemoryRouter></QueryClientProvider>);
  expect(await screen.findByRole("heading", { name: "主笔记引用待复核" })).toBeInTheDocument();
  expect(screen.getAllByRole("link", { name: "查看受影响片段 1" })[0]).toHaveAttribute("href", `#synthesis-item-${id(11)}`);
  expect(screen.getAllByRole("link", { name: "查看原引用片段" })[0]).toHaveAttribute("href", `/authoring/notes/${ref.note_id}?revision_id=${ref.revision_id}&body_item_id=${ref.item_id}&body_workspace_id=${ref.workspace_id}&body_projection_hash=${ref.projection_hash}`);
  expect(screen.getAllByRole("link", { name: "查看新发布版本" })[0]).toHaveAttribute("href", `/authoring/notes/${ref.note_id}?revision_id=${id(500)}`);
  fireEvent.click(screen.getByRole("button", { name: "加载更多引用提醒" }));
  expect(await screen.findByText("更多主笔记引用提醒读取失败")).toBeInTheDocument();
  expect(screen.getAllByRole("link", { name: "查看原引用片段" })).toHaveLength(20);
  fireEvent.click(screen.getByRole("button", { name: /重试/ }));
  expect(await screen.findByText("新发布版本中已无原引用片段，请复核；当前片段不会自动删除。")).toBeInTheDocument();
  expect(fetchMock.mock.calls[1]?.[0]).toContain(`after_id=${id(319)}`);
  expect(fetchMock.mock.calls[2]?.[0]).toContain(`after_id=${id(319)}`);
  fireEvent.click(screen.getByRole("button", { name: "刷新引用提醒" }));
  await vi.waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(5));
  expect(screen.getAllByRole("link", { name: "查看原引用片段" })).toHaveLength(21);
  client.clear();
});

it("不同活动工作区不发请求或显示旧提醒", () => {
  setActiveWorkspaceId(id(999)); const fetchMock = vi.fn<typeof fetch>(); vi.stubGlobal("fetch", fetchMock);
  const client = new QueryClient();
  render(<QueryClientProvider client={client}><MemoryRouter><BodyImpactPanel revision={revision} /></MemoryRouter></QueryClientProvider>);
  expect(fetchMock).not.toHaveBeenCalled(); expect(screen.queryByText("主笔记引用待复核")).not.toBeInTheDocument(); client.clear();
});
