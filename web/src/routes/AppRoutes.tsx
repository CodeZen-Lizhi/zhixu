import { Navigate, Route, Routes } from "react-router-dom";

import { CollectionDetailPage, CollectionsPage } from "../features/collections/CollectionsPage";
import { GraphPage } from "../features/graph/GraphPage";
import { HealthPage } from "../features/health/HealthPage";
import { RagPage } from "../features/rag/RagPage";
import { WorkspacePage } from "../features/workspace/WorkspacePage";

export const AppRoutes = () => (
  <Routes>
    <Route path="/" element={<WorkspacePage />} />
    <Route path="/collections" element={<CollectionsPage />} />
    <Route path="/collections/:collectionId" element={<CollectionDetailPage />} />
    <Route path="/health" element={<HealthPage />} />
    <Route path="/settings" element={<WorkspacePage />} />
    <Route path="/graph" element={<GraphPage />} />
    <Route path="/chat" element={<RagPage />} />
    <Route path="/chat/:conversationId" element={<RagPage />} />
    <Route path="*" element={<Navigate to="/" replace />} />
  </Routes>
);
