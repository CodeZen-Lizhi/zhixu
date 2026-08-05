import { fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";

const workspace = vi.hoisted(() => ({ id: "c1000000-0000-4000-8000-000000000001" }));
const query: { state: Record<string, unknown>; refetch: ReturnType<typeof vi.fn> } = vi.hoisted(() => ({
  state: {},
  refetch: vi.fn(),
}));

vi.mock("../../app/active-workspace", () => ({ useActiveWorkspaceId: () => workspace.id }));
vi.mock("./queries", () => ({ useAuthoringOverview: () => ({ ...query.state, refetch: query.refetch }) }));

import { AuthoringPage } from "./AuthoringPage";

const draftId = "c1000000-0000-4000-8000-000000000002";
const documentId = "c1000000-0000-4000-8000-000000000003";
const revisionId = "c1000000-0000-4000-8000-000000000004";
const proposalId = "c1000000-0000-4000-8000-000000000005";
const proposalRevisionId = "c1000000-0000-4000-8000-000000000006";

const overview = (organizingAvailable = false) => ({
  workspaceId: workspace.id,
  organizing: organizingAvailable
    ? { available: true, reason: null, href: "/authoring/organize" }
    : { available: false, reason: "整理模板尚未接入", href: null },
  recentDrafts: [{
    id: draftId,
    workspaceId: workspace.id,
    documentId: null,
    title: "Java AI 知识点",
    targetPath: "notes/java-ai.md",
    status: "EDITING",
    version: 3,
    updatedAt: "2026-08-03T08:00:00Z",
  }],
  pendingPublications: [{
    id: "c1000000-0000-4000-8000-000000000007",
    workspaceId: workspace.id,
    documentId,
    articleRevisionId: revisionId,
    proposalId,
    proposalRevisionId,
    targetPath: "notes/recovery.md",
    contentHash: "a".repeat(64),
    status: "RECOVERY_REQUIRED",
    gitCommit: null,
    errorCode: "AUTHORING_FINALIZER_CONFLICT",
    version: 2,
    createdAt: "2026-08-03T08:00:00Z",
    updatedAt: "2026-08-03T08:01:00Z",
    publishedAt: null,
    proposalHref: `/proposals/${proposalId}`,
  }],
  completedDocuments: [{
    id: documentId,
    workspaceId: workspace.id,
    title: "已发布文章",
    canonicalPath: "notes/published.md",
    lifecycleStatus: "PUBLISHED",
    currentPublishedRevisionId: revisionId,
    version: 2,
    createdAt: "2026-08-03T08:00:00Z",
    updatedAt: "2026-08-03T08:01:00Z",
  }],
});

beforeEach(() => {
  workspace.id = "c1000000-0000-4000-8000-000000000001";
  query.refetch.mockReset();
  query.state = { isPending: false, isError: false, data: overview() };
});

describe("AuthoringPage", () => {
  it("shows real draft, recovery and completed lists without enabling absent Organizing", () => {
    render(<MemoryRouter><AuthoringPage /></MemoryRouter>);

    expect(screen.getByRole("link", { name: /新建文章/ })).toHaveAttribute("href", "/authoring/new");
    expect(screen.queryByRole("link", { name: /整理成文/ })).not.toBeInTheDocument();
    expect(screen.getByText("整理模板尚未接入").closest("[aria-disabled='true']")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /Java AI 知识点/ })).toHaveAttribute("href", `/authoring/new?draft=${draftId}`);
    expect(screen.getByRole("link", { name: /notes\/recovery\.md/ })).toHaveAttribute("href", `/proposals/${proposalId}`);
    expect(screen.getByText("待恢复")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /已发布文章/ })).toHaveAttribute("href", `/authoring/documents/${documentId}/history`);
    const advancedRecords = screen.getByRole("navigation", { name: "创作高级记录" });
    expect(advancedRecords).toHaveTextContent("高级记录");
    expect(screen.getByRole("link", { name: "审批记录" })).toHaveAttribute("href", "/proposals");
    expect(screen.getByRole("link", { name: "执行记录" })).toHaveAttribute("href", "/workflows");
    expect(screen.getByRole("link", { name: "生成产物" })).toHaveAttribute("href", "/artifacts");
  });

  it("enables Organizing only when overview returns a real route", () => {
    query.state = { isPending: false, isError: false, data: overview(true) };
    render(<MemoryRouter><AuthoringPage /></MemoryRouter>);

    expect(screen.getByRole("link", { name: /整理成文/ })).toHaveAttribute("href", "/authoring/organize");
  });

  it("keeps overview failures retryable", () => {
    query.state = { isPending: false, isError: true, error: new Error("overview offline"), data: undefined };
    render(<MemoryRouter><AuthoringPage /></MemoryRouter>);

    expect(screen.getByRole("alert")).toHaveTextContent("overview offline");
    fireEvent.click(screen.getByRole("button", { name: "重试" }));
    expect(query.refetch).toHaveBeenCalledOnce();
  });
});
