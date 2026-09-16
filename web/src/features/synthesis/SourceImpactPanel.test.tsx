import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, expect, it, vi } from "vitest";
import { decodeSynthesisRevision } from "../../api/synthesis";
import { setActiveWorkspaceId } from "../../app/active-workspace";
import { synthesisId, synthesisRevisionFixture, synthesisSourceFixture, synthesisWorkspaceId } from "../../test/synthesis-fixtures";
import { SourceImpactPanel } from "./SourceImpactPanel";

afterEach(() => { cleanup(); vi.unstubAllGlobals(); setActiveWorkspaceId(""); });
it("默认展示删除复核提醒，刷新后来源恢复仍保留提醒和准确历史链接", async () => {
  setActiveWorkspaceId(synthesisWorkspaceId);
  const wire = synthesisRevisionFixture();
  const revision = decodeSynthesisRevision(wire, synthesisWorkspaceId, wire.note_id);
  const item = { id: synthesisId(300), reason: "SOURCE_REMOVED", detected_at: "2026-09-15T00:00:00Z", currently_unavailable: true, reference: synthesisSourceFixture(), item_ids: [synthesisId(11), synthesisId(12)] };
  const json = (available: boolean) => new Response(JSON.stringify({ workspace_id: synthesisWorkspaceId, note_id: revision.noteId, revision_id: revision.id, items: [{ ...item, currently_unavailable: !available }] }), { headers: { "Content-Type": "application/json" } });
  const fetchMock = vi.fn<typeof fetch>().mockResolvedValueOnce(json(false)).mockResolvedValueOnce(json(true));
  vi.stubGlobal("fetch", fetchMock);
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(<QueryClientProvider client={client}><MemoryRouter><SourceImpactPanel revision={revision} /></MemoryRouter></QueryClientProvider>);
  expect(await screen.findByText("来源已删除，请复核相关内容。")).toBeInTheDocument();
  expect(screen.getByRole("link", { name: "查看受影响片段 1" })).toHaveAttribute("href", `#synthesis-item-${synthesisId(11)}`);
  expect(screen.getByRole("link", { name: "查看保存的原始引用" }).getAttribute("href")).toContain(`revision_id=${revision.id}`);
  fireEvent.click(screen.getByRole("button", { name: "刷新来源状态" }));
  expect(await screen.findByText("来源曾被删除，当前已恢复，相关内容仍待复核。")).toBeInTheDocument();
  expect(fetchMock).toHaveBeenCalledTimes(2);
  client.clear();
});
