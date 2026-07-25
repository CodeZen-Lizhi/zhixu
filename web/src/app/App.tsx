import { BrowserRouter } from "react-router-dom";

import { AppRoutes } from "../routes/AppRoutes";
import { EventStoreProvider } from "../events/event-store";
import { AuthBoundary, AuthProvider } from "./auth-context";
import { WorkspaceCacheBoundary } from "./WorkspaceCacheBoundary";

export const App = () => (
  <AuthProvider>
    <AuthBoundary>
      <WorkspaceCacheBoundary>
        <BrowserRouter>
          <EventStoreProvider>
            <AppRoutes />
          </EventStoreProvider>
        </BrowserRouter>
      </WorkspaceCacheBoundary>
    </AuthBoundary>
  </AuthProvider>
);
