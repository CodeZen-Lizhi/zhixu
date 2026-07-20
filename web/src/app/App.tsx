import { BrowserRouter } from "react-router-dom";

import { GraphWorkspaceCacheBoundary } from "../features/graph/GraphWorkspaceCacheBoundary";
import { AppRoutes } from "../routes/AppRoutes";

export const App = () => (
  <GraphWorkspaceCacheBoundary>
    <BrowserRouter>
      <main className="app-shell">
        <AppRoutes />
      </main>
    </BrowserRouter>
  </GraphWorkspaceCacheBoundary>
);
