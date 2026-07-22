import { BrowserRouter } from "react-router-dom";

import { CollectionHealthEventBridge } from "../events/collection-health-events";
import { AppRoutes } from "../routes/AppRoutes";
import { WorkspaceCacheBoundary } from "./WorkspaceCacheBoundary";

export const App = () => (
  <WorkspaceCacheBoundary>
    <BrowserRouter>
      <CollectionHealthEventBridge>
        <main className="app-shell">
          <AppRoutes />
        </main>
      </CollectionHealthEventBridge>
    </BrowserRouter>
  </WorkspaceCacheBoundary>
);
