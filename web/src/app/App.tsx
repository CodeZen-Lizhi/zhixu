import { BrowserRouter } from "react-router-dom";

import { AppRoutes } from "../routes/AppRoutes";
import { EventStoreProvider } from "../events/event-store";
import { WorkspaceCacheBoundary } from "./WorkspaceCacheBoundary";

export const App = () => (
  <WorkspaceCacheBoundary>
    <BrowserRouter>
      <EventStoreProvider>
        <AppRoutes />
      </EventStoreProvider>
    </BrowserRouter>
  </WorkspaceCacheBoundary>
);
