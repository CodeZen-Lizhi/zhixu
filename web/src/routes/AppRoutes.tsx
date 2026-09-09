import { Navigate, Route, Routes } from "react-router-dom";
import { lazy, type ReactNode } from "react";
import { AppShell } from "../app/AppShell";
import { useAuth } from "../app/auth-context";
import { PageHeader, UnavailableState } from "../shared/ui";

const GraphPage = lazy(() => import("../features/graph/GraphPage").then((module) => ({ default: module.GraphPage })));
const RagPage = lazy(() => import("../features/rag/RagPage").then((module) => ({ default: module.RagPage })));
const WorkspacePage = lazy(() => import("../features/workspace/WorkspacePage").then((module) => ({ default: module.WorkspacePage })));
const DashboardPage = lazy(() => import("../features/business/DashboardPage").then((module) => ({ default: module.DashboardPage })));
const BasicPages = lazy(() => import("../features/business/BasicPages").then((module) => ({ default: module.InboxPage })));
const CaptureDetailPage = lazy(() => import("../features/capture/CaptureDetailPage").then((module) => ({ default: module.CaptureDetailPage })));
const DocumentsPage = lazy(() => import("../features/business/BasicPages").then((module) => ({ default: module.DocumentsPage })));
const SettingsPage = lazy(() => import("../features/settings/SettingsPage").then((module) => ({ default: module.SettingsPage })));
const ProposalsPage = lazy(() => import("../features/business/ProposalsPage").then((module) => ({ default: module.ProposalsPage })));
const ProposalDetailPage = lazy(() => import("../features/business/ProposalsPage").then((module) => ({ default: module.ProposalDetailPage })));
const WorkflowsPage = lazy(() => import("../features/business/WorkflowsPage").then((module) => ({ default: module.WorkflowsPage })));
const WorkflowDetailPage = lazy(() => import("../features/business/WorkflowsPage").then((module) => ({ default: module.WorkflowDetailPage })));
const CollectionsPage = lazy(() => import("../features/collections/CollectionsPage").then((module) => ({ default: module.CollectionsPage })));
const CollectionDetailPage = lazy(() => import("../features/collections/CollectionsPage").then((module) => ({ default: module.CollectionDetailPage })));
const HealthPage = lazy(() => import("../features/health/HealthPage").then((module) => ({ default: module.HealthPage })));
const SearchPage = lazy(() => import("../features/search/SearchPage").then((module) => ({ default: module.SearchPage })));
const ArtifactsPage = lazy(() => import("../features/artifacts/ArtifactsPage").then((module) => ({ default: module.ArtifactsPage })));
const ArtifactDetailPage = lazy(() => import("../features/artifacts/ArtifactsPage").then((module) => ({ default: module.ArtifactDetailPage })));
const ReviewPage = lazy(() => import("../features/review/ReviewPage").then((module) => ({ default: module.ReviewPage })));
const ReviewSessionPage = lazy(() => import("../features/review/ReviewSessionPage").then((module) => ({ default: module.ReviewSessionPage })));
const MemoriesPage = lazy(() => import("../features/memory/MemoriesPage").then((module) => ({ default: module.MemoriesPage })));
const InterviewsPage = lazy(() => import("../features/interview/InterviewPage").then((module) => ({ default: module.InterviewsPage })));
const InterviewSessionPage = lazy(() => import("../features/interview/InterviewPage").then((module) => ({ default: module.InterviewSessionPage })));
const TimelinePage = lazy(() => import("../features/timeline/TimelinePage").then((module) => ({ default: module.TimelinePage })));
const TimelineEventPage = lazy(() => import("../features/timeline/TimelinePage").then((module) => ({ default: module.TimelineEventPage })));
const AuthoringPage = lazy(() => import("../features/authoring/AuthoringPage").then((module) => ({ default: module.AuthoringPage })));
const SynthesisNotesPage = lazy(() => import("../features/synthesis").then((module) => ({ default: module.SynthesisNotesPage })));
const SynthesisNotePage = lazy(() => import("../features/synthesis").then((module) => ({ default: module.SynthesisNotePage })));
const NewDocumentPage = lazy(() => import("../features/authoring/NewDocumentPage").then((module) => ({ default: module.NewDocumentPage })));
const DocumentHistoryPage = lazy(() => import("../features/document-history").then((module) => ({ default: module.DocumentHistoryPage })));
const OrganizingPage = lazy(() => import("../features/organizing").then((module) => ({ default: module.OrganizingPage })));

const Shell = () => <AppShell />;

const AuthenticatedPrincipalRoute = ({ children, title, description }: { children: ReactNode; title: string; description: string }) => {
  const { state } = useAuth();
  if (state.status === "authenticated" && state.mode === "required") return <>{children}</>;

  return <div className="page-stack">
    <PageHeader title={title} description={description} />
    <UnavailableState title="需要登录后使用" description="此功能会绑定当前用户。请启用身份认证并登录；当前开发模式不会发起相关请求。" />
  </div>;
};

export const AppRoutes = () => (
  <Routes>
    <Route element={<Shell />}>
      <Route path="/" element={<WorkspacePage />} />
      <Route path="/dashboard" element={<DashboardPage />} />
      <Route path="/inbox" element={<BasicPages />} />
      <Route path="/captures/:captureId" element={<CaptureDetailPage />} />
      <Route path="/documents" element={<DocumentsPage />} />
      <Route path="/documents/:sourceVersionId" element={<DocumentsPage />} />
      <Route path="/search" element={<SearchPage />} />
      <Route path="/authoring" element={<AuthoringPage />} />
      <Route path="/authoring/notes" element={<SynthesisNotesPage />} />
      <Route path="/authoring/notes/:noteId" element={<SynthesisNotePage />} />
      <Route path="/authoring/new" element={<NewDocumentPage />} />
      <Route path="/authoring/documents/:documentId/history" element={<DocumentHistoryPage />} />
      <Route path="/authoring/organize" element={<OrganizingPage />} />
      <Route path="/proposals" element={<ProposalsPage />} />
      <Route path="/proposals/:proposalId" element={<ProposalDetailPage />} />
      <Route path="/workflows" element={<WorkflowsPage />} />
      <Route path="/workflows/:workflowId" element={<WorkflowDetailPage />} />
      <Route path="/collections" element={<CollectionsPage />} />
      <Route path="/collections/:collectionId" element={<CollectionDetailPage />} />
      <Route path="/health" element={<HealthPage />} />
      <Route path="/timeline" element={<TimelinePage />} />
      <Route path="/timeline/:eventId" element={<TimelineEventPage />} />
      <Route path="/artifacts" element={<ArtifactsPage />} />
      <Route path="/artifacts/:artifactId" element={<ArtifactDetailPage />} />
      <Route path="/review" element={<ReviewPage />} />
      <Route path="/review/session" element={<ReviewSessionPage />} />
      <Route path="/memories" element={<AuthenticatedPrincipalRoute title="记忆" description="管理需要明确确认的长期上下文。"><MemoriesPage /></AuthenticatedPrincipalRoute>} />
      <Route path="/interviews" element={<AuthenticatedPrincipalRoute title="访谈" description="基于知识内容进行模拟问答。"><InterviewsPage /></AuthenticatedPrincipalRoute>} />
      <Route path="/interviews/:sessionId" element={<AuthenticatedPrincipalRoute title="模拟面试" description="恢复服务端保存的题目、回答与学习路径。"><InterviewSessionPage /></AuthenticatedPrincipalRoute>} />
      <Route path="/workspace" element={<WorkspacePage />} />
      <Route path="/settings" element={<SettingsPage />} />
      <Route path="/graph" element={<GraphPage />} />
      <Route path="/chat" element={<RagPage />} />
      <Route path="/chat/:conversationId" element={<RagPage />} />
    </Route>
    <Route path="*" element={<Navigate to="/dashboard" replace />} />
  </Routes>
);
