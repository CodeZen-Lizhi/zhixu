import { Navigate, Route, Routes } from "react-router-dom";

import { GraphPage } from "../features/graph/GraphPage";
import { RagPage } from "../features/rag/RagPage";
import { WorkspacePage } from "../features/workspace/WorkspacePage";

export const AppRoutes = () => (
  <Routes>
    <Route path="/" element={<WorkspacePage />} />
    <Route path="/graph" element={<GraphPage />} />
    <Route path="/chat" element={<RagPage />} />
    <Route path="/chat/:conversationId" element={<RagPage />} />
    <Route path="*" element={<Navigate to="/" replace />} />
  </Routes>
);
