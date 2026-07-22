import { render, screen } from "@testing-library/react";
import { MemoryRouter, Outlet, useLocation } from "react-router-dom";
import { describe, expect, it, vi } from "vitest";

vi.mock("../app/AppShell", () => ({ AppShell: () => <Outlet /> }));
vi.mock("../features/workspace/WorkspacePage", () => ({ WorkspacePage: () => <div>Workspace route</div> }));
vi.mock("../features/graph/GraphPage", () => ({ GraphPage: () => <div>Graph route</div> }));
vi.mock("../features/rag/RagPage", () => ({ RagPage: () => <div>Chat route</div> }));
vi.mock("../features/search/SearchPage", () => ({ SearchPage: () => <div>Search route</div> }));
vi.mock("../features/business/DashboardPage", () => ({ DashboardPage: () => <div>Dashboard route</div> }));
vi.mock("../features/business/BasicPages", () => ({
  InboxPage: () => <div>Inbox route</div>,
  DocumentsPage: () => <div>Documents route</div>,
  SettingsPage: () => <div>Settings route</div>,
}));
vi.mock("../features/business/ProposalsPage", () => ({
  ProposalsPage: () => <div>Proposals route</div>,
  ProposalDetailPage: () => <div>Proposal detail route</div>,
}));
vi.mock("../features/business/WorkflowsPage", () => ({
  WorkflowsPage: () => <div>Workflows route</div>,
  WorkflowDetailPage: () => <div>Workflow detail route</div>,
}));

import { AppRoutes } from "./AppRoutes";

const LocationProbe = () => {
  const location = useLocation();
  return <output data-testid="location">{location.pathname}{location.search}</output>;
};

const renderRoute = (path: string) => render(
  <MemoryRouter initialEntries={[path]}>
    <LocationProbe />
    <AppRoutes />
  </MemoryRouter>,
);

describe("AppRoutes compatibility", () => {
  it("保留根路由的 Workspace 语义且不改写 URL", async () => {
    renderRoute("/");

    expect(await screen.findByText("Workspace route")).toBeInTheDocument();
    expect(screen.getByTestId("location")).toHaveTextContent("/");
  });

  it.each([
    ["/graph", "Graph route"],
    ["/chat", "Chat route"],
    ["/chat/7a000000-0000-4000-8000-000000000001", "Chat route"],
  ])("保留既有深链 %s", async (path, expected) => {
    renderRoute(path);

    expect(await screen.findByText(expected)).toBeInTheDocument();
    expect(screen.getByTestId("location")).toHaveTextContent(path);
  });

  it.each([
    ["/dashboard", "Dashboard route"],
    ["/inbox", "Inbox route"],
    ["/documents", "Documents route"],
    ["/documents/10000000-0000-4000-8000-000000000004", "Documents route"],
    ["/search?query=knowledge&mode=hybrid", "Search route"],
    ["/proposals", "Proposals route"],
    ["/proposals/10000000-0000-4000-8000-000000000001", "Proposal detail route"],
    ["/workflows", "Workflows route"],
    ["/workflows/10000000-0000-4000-8000-000000000008", "Workflow detail route"],
    ["/settings", "Settings route"],
  ])("保留 M9 业务深链 %s", async (path, expected) => {
    renderRoute(path);

    expect(await screen.findByText(expected)).toBeInTheDocument();
    expect(screen.getByTestId("location")).toHaveTextContent(path);
  });
});
