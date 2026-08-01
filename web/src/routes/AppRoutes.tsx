import { Navigate, Route, Routes } from "react-router-dom";
import { lazy } from "react";
import { AppShell } from "../app/AppShell";

const GraphPage = lazy(() => import("../features/graph/GraphPage").then((module) => ({ default: module.GraphPage })));
const RagPage = lazy(() => import("../features/rag/RagPage").then((module) => ({ default: module.RagPage })));
const WorkspacePage = lazy(() => import("../features/workspace/WorkspacePage").then((module) => ({ default: module.WorkspacePage })));
const DashboardPage = lazy(() => import("../features/business/DashboardPage").then((module) => ({ default: module.DashboardPage })));
const BasicPages = lazy(() => import("../features/business/BasicPages").then((module) => ({ default: module.InboxPage })));
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

const Shell = () => <AppShell />;

export const AppRoutes = () => (
  <Routes>
    <Route element={<Shell />}>
      <Route path="/" element={<WorkspacePage />} />
      <Route path="/dashboard" element={<DashboardPage />} />
      <Route path="/inbox" element={<BasicPages />} />
      <Route path="/documents" element={<DocumentsPage />} />
      <Route path="/documents/:sourceVersionId" element={<DocumentsPage />} />
      <Route path="/search" element={<SearchPage />} />
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
      <Route path="/memories" element={<MemoriesPage />} />
      <Route path="/interviews" element={<InterviewsPage />} />
      <Route path="/interviews/:sessionId" element={<InterviewSessionPage />} />
      <Route path="/workspace" element={<WorkspacePage />} />
      <Route path="/settings" element={<SettingsPage />} />
      <Route path="/graph" element={<GraphPage />} />
      <Route path="/chat" element={<RagPage />} />
      <Route path="/chat/:conversationId" element={<RagPage />} />
    </Route>
    <Route path="*" element={<Navigate to="/dashboard" replace />} />
  </Routes>
);
