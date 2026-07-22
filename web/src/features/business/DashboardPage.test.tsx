import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const workspaceId = "10000000-0000-4000-8000-000000000002";
const workspaceState = vi.hoisted(() => ({ id: "10000000-0000-4000-8000-000000000002" }));
const api = vi.hoisted(() => ({
  getWorkspace: vi.fn(),
  listProposals: vi.fn(),
  listSourceVersions: vi.fn(),
  listWorkflows: vi.fn(),
}));

vi.mock("../../app/active-workspace", async (importOriginal) => ({
  // eslint-disable-next-line @typescript-eslint/consistent-type-imports
  ...(await importOriginal<typeof import("../../app/active-workspace")>()),
  useActiveWorkspaceId: () => workspaceState.id,
}));
vi.mock("../../api/business", async (importOriginal) => ({
  // eslint-disable-next-line @typescript-eslint/consistent-type-imports
  ...(await importOriginal<typeof import("../../api/business")>()),
  listProposals: api.listProposals,
  listSourceVersions: api.listSourceVersions,
  listWorkflows: api.listWorkflows,
}));
vi.mock("../../api/workspace", async (importOriginal) => ({
  // eslint-disable-next-line @typescript-eslint/consistent-type-imports
  ...(await importOriginal<typeof import("../../api/workspace")>()),
  getWorkspace: api.getWorkspace,
}));
vi.mock("../system-status/SystemStatusPage", () => ({ SystemStatusPage: () => <div>System status facts</div> }));
vi.mock("../system-status/use-system-status", () => ({
  useSystemStatus: () => ({
    isPending: false,
    isError: false,
    data: {
      status: "ready",
      database: { status: "ready" },
      rag: { status: "ready" },
      graph: { status: "ready" },
      semanticLinks: { status: "ready" },
    },
  }),
}));

import { DashboardPage } from "./DashboardPage";

const renderPage = () => render(
  <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
    <MemoryRouter><DashboardPage /></MemoryRouter>
  </QueryClientProvider>,
);

beforeEach(() => {
  workspaceState.id = workspaceId;
  api.getWorkspace.mockResolvedValue({
    id: workspaceId,
    name: "Workspace",
    rootPath: "/tmp/workspace",
    status: "active",
    version: 1,
    git: { present: true, repositoryPath: "/tmp/workspace", branch: "main", head: "abc", dirty: false, checkedAt: "2026-07-22T00:00:00Z" },
    warnings: [],
    createdAt: "2026-07-22T00:00:00Z",
    updatedAt: "2026-07-22T00:00:00Z",
  });
  api.listProposals.mockResolvedValue({ items: [] });
  api.listSourceVersions.mockResolvedValue({ items: [] });
  api.listWorkflows.mockResolvedValue({ items: [] });
});

afterEach(() => {
  cleanup();
  api.getWorkspace.mockReset();
  api.listProposals.mockReset();
  api.listSourceVersions.mockReset();
  api.listWorkflows.mockReset();
});

describe("DashboardPage", () => {
  it("没有活动 Workspace 时展示连接入口且不请求业务列表", () => {
    workspaceState.id = "";
    renderPage();

    expect(screen.getByRole("heading", { name: "先连接一个 Workspace" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "前往连接" })).toHaveAttribute("href", "/workspace");
    expect(screen.getByText("System status facts")).toBeInTheDocument();
    expect(api.getWorkspace).not.toHaveBeenCalled();
    expect(api.listProposals).not.toHaveBeenCalled();
    expect(api.listSourceVersions).not.toHaveBeenCalled();
    expect(api.listWorkflows).not.toHaveBeenCalled();
  });

  it("列表部分失败时保留其他真实区域并明确暴露错误", async () => {
    api.listProposals.mockRejectedValue(new Error("proposal database unavailable"));
    renderPage();

    expect(await screen.findByText("Proposal 列表不可用")).toBeInTheDocument();
    expect(screen.getByText("没有持久 Run")).toBeInTheDocument();
    expect(screen.getByText("还没有捕获资料")).toBeInTheDocument();
    expect(screen.getByText("proposal database unavailable")).toBeInTheDocument();
  });
});
