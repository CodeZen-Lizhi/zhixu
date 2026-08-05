import { render, screen } from "@testing-library/react";
import { MemoryRouter, Outlet, useLocation } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";

const auth = vi.hoisted(() => ({
  state: { status: "authenticated", mode: "required" },
}));

vi.mock("../app/AppShell", () => ({ AppShell: () => <Outlet /> }));
vi.mock("../app/auth-context", () => ({ useAuth: () => ({ state: auth.state }) }));
vi.mock("../features/workspace/WorkspacePage", () => ({ WorkspacePage: () => <div>Workspace route</div> }));
vi.mock("../features/graph/GraphPage", () => ({ GraphPage: () => <div>Graph route</div> }));
vi.mock("../features/rag/RagPage", () => ({ RagPage: () => <div>Chat route</div> }));
vi.mock("../features/search/SearchPage", () => ({ SearchPage: () => <div>Search route</div> }));
vi.mock("../features/business/DashboardPage", () => ({ DashboardPage: () => <div>Dashboard route</div> }));
vi.mock("../features/business/BasicPages", () => ({
  InboxPage: () => <div>Inbox route</div>,
  DocumentsPage: () => <div>Documents route</div>,
}));
vi.mock("../features/settings/SettingsPage", () => ({ SettingsPage: () => <div>Settings route</div> }));
vi.mock("../features/business/ProposalsPage", () => ({
  ProposalsPage: () => <div>Proposals route</div>,
  ProposalDetailPage: () => <div>Proposal detail route</div>,
}));
vi.mock("../features/business/WorkflowsPage", () => ({
  WorkflowsPage: () => <div>Workflows route</div>,
  WorkflowDetailPage: () => <div>Workflow detail route</div>,
}));
vi.mock("../features/review/ReviewPage", () => ({ ReviewPage: () => <div>Review route</div> }));
vi.mock("../features/review/ReviewSessionPage", () => ({ ReviewSessionPage: () => <div>Review session route</div> }));
vi.mock("../features/memory/MemoriesPage", () => ({ MemoriesPage: () => <div>Memories route</div> }));
vi.mock("../features/interview/InterviewPage", () => ({
  InterviewsPage: () => <div>Interviews route</div>,
  InterviewSessionPage: () => <div>Interview session route</div>,
}));
vi.mock("../features/timeline/TimelinePage", () => ({
  TimelinePage: () => <div>Timeline route</div>,
  TimelineEventPage: () => <div>Timeline event route</div>,
}));
vi.mock("../features/collections/CollectionsPage", () => ({
  CollectionsPage: () => <div>Collections route</div>,
  CollectionDetailPage: () => <div>Collection detail route</div>,
}));
vi.mock("../features/health/HealthPage", () => ({ HealthPage: () => <div>Health route</div> }));
vi.mock("../features/artifacts/ArtifactsPage", () => ({
  ArtifactsPage: () => <div>Artifacts route</div>,
  ArtifactDetailPage: () => <div>Artifact detail route</div>,
}));
vi.mock("../features/authoring/AuthoringPage", () => ({ AuthoringPage: () => <div>Authoring route</div> }));
vi.mock("../features/authoring/NewDocumentPage", () => ({ NewDocumentPage: () => <div>New document route</div> }));
vi.mock("../features/document-history", () => ({ DocumentHistoryPage: () => <div>Document history route</div> }));
vi.mock("../features/organizing", () => ({ OrganizingPage: () => <div>Organizing route</div> }));

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
  beforeEach(() => {
    auth.state = { status: "authenticated", mode: "required" };
  });

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
    ["/authoring", "Authoring route"],
    ["/authoring/new?draft=10000000-0000-4000-8000-000000000009", "New document route"],
    ["/authoring/documents/10000000-0000-4000-8000-000000000004/history", "Document history route"],
    ["/authoring/organize?draft=10000000-0000-4000-8000-000000000009", "Organizing route"],
    ["/proposals", "Proposals route"],
    ["/proposals/10000000-0000-4000-8000-000000000001", "Proposal detail route"],
    ["/workflows", "Workflows route"],
    ["/workflows/10000000-0000-4000-8000-000000000008", "Workflow detail route"],
    ["/settings", "Settings route"],
    ["/settings?section=system", "Settings route"],
    ["/collections", "Collections route"],
    ["/collections/10000000-0000-4000-8000-000000000006", "Collection detail route"],
    ["/health", "Health route"],
    ["/artifacts", "Artifacts route"],
    ["/artifacts/10000000-0000-4000-8000-000000000007", "Artifact detail route"],
    ["/review", "Review route"],
    ["/review/session?deck=10000000-0000-4000-8000-000000000001&session=10000000-0000-4000-8000-000000000002", "Review session route"],
    ["/memories", "Memories route"],
    ["/interviews", "Interviews route"],
    ["/interviews/10000000-0000-4000-8000-000000000003", "Interview session route"],
    ["/timeline", "Timeline route"],
    ["/timeline/10000000-0000-4000-8000-000000000003", "Timeline event route"],
  ])("保留 M9 业务深链 %s", async (path, expected) => {
    renderRoute(path);

    expect(await screen.findByText(expected)).toBeInTheDocument();
    expect(screen.getByTestId("location")).toHaveTextContent(path);
  });

  it.each([
    ["/memories", "记忆", "Memories route"],
    ["/interviews", "访谈", "Interviews route"],
    ["/interviews/10000000-0000-4000-8000-000000000003", "模拟面试", "Interview session route"],
  ])("开发模式访问身份型功能 %s 时不挂载受保护页面", async (path, title, protectedContent) => {
    auth.state = { status: "authenticated", mode: "disabled" };

    renderRoute(path);

    expect(await screen.findByRole("heading", { level: 1, name: title })).toBeInTheDocument();
    expect(screen.getByText("需要登录后使用")).toBeInTheDocument();
    expect(screen.getByText(/不会发起相关请求/)).toBeInTheDocument();
    expect(screen.queryByText(protectedContent)).not.toBeInTheDocument();
    expect(screen.getByTestId("location")).toHaveTextContent(path);
  });
});
