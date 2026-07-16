import { Navigate, Route, Routes } from "react-router-dom";

import { SystemStatusPage } from "../features/system-status/SystemStatusPage";

export const AppRoutes = () => (
  <Routes>
    <Route path="/" element={<SystemStatusPage />} />
    <Route path="*" element={<Navigate to="/" replace />} />
  </Routes>
);
