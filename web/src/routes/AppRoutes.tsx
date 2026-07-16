import { Navigate, Route, Routes } from "react-router-dom";

import { WorkspacePage } from "../features/workspace/WorkspacePage";

export const AppRoutes = () => (
  <Routes>
    <Route path="/" element={<WorkspacePage />} />
    <Route path="*" element={<Navigate to="/" replace />} />
  </Routes>
);
