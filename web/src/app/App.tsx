import { BrowserRouter } from "react-router-dom";

import { EventStoreProvider } from "../events/event-store";
import { AppRoutes } from "../routes/AppRoutes";
import { AuthBoundary, AuthProvider } from "./auth-context";
import { WorkspaceCacheBoundary } from "./WorkspaceCacheBoundary";

/** 单一业务入口：认证成功后才查询 Active Workspace 并挂载业务路由。 */
export const App = () => (
  <BrowserRouter>
    <AuthProvider>
      <AuthBoundary>
        <WorkspaceCacheBoundary>
          <EventStoreProvider>
            <AppRoutes />
          </EventStoreProvider>
        </WorkspaceCacheBoundary>
      </AuthBoundary>
    </AuthProvider>
  </BrowserRouter>
);
