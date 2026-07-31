import type { ReactNode } from "react";
import { BrowserRouter, Navigate, Route, Routes, useLocation } from "react-router-dom";

import { AppRoutes } from "../routes/AppRoutes";
import { EventStoreProvider } from "../events/event-store";
import { ControllerWorkspacePage } from "../features/workspace/ControllerWorkspacePage";
import { AuthBoundary, AuthProvider } from "./auth-context";
import { HostControlProvider, useHostControl } from "./host-control-context";
import { runtimeMode } from "./runtime-mode";
import { WorkspaceCacheBoundary } from "./WorkspaceCacheBoundary";

const BusinessRuntime = () => (
  <AuthProvider>
    <AuthBoundary>
      <WorkspaceCacheBoundary>
        <EventStoreProvider>
          <AppRoutes />
        </EventStoreProvider>
      </WorkspaceCacheBoundary>
    </AuthBoundary>
  </AuthProvider>
);

const ControllerGate = ({
  eyebrow,
  title,
  message,
  action,
}: {
  eyebrow: string;
  title: string;
  message: string;
  action?: ReactNode;
}) => (
  <main className="control-gate">
    <section className="control-gate__content" aria-live="polite">
      <p className="eyebrow">{eyebrow}</p>
      <h1>{title}</h1>
      <p>{message}</p>
      {action}
    </section>
  </main>
);

const ControllerWorkspaceRoutes = () => (
  <main className="controller-entry">
    <div className="controller-entry__inner">
      <Routes>
        <Route path="/" element={<ControllerWorkspacePage />} />
        <Route path="/workspace" element={<ControllerWorkspacePage />} />
        <Route path="*" element={<Navigate to="/workspace" replace />} />
      </Routes>
    </div>
  </main>
);

export const HostControlledRuntime = () => {
  const { state, effectiveWorkspaceId, refresh } = useHostControl();
  const location = useLocation();

  if (state.status === "loading") {
    return <ControllerGate eyebrow="ZHIXU / CONTROL" title="正在确认本机运行边界" message="正在恢复控制会话并读取 Workspace 运行状态。" />;
  }
  if (state.status === "session_required") {
    return <ControllerGate eyebrow="ZHIXU / CONTROL" title="控制链接已失效" message="此入口需要启动器生成的一次性本机链接。请重新运行启动器并打开新链接。" />;
  }
  if (state.status === "error") {
    return <ControllerGate eyebrow="ZHIXU / CONTROL" title="无法确认本机运行状态" message={state.error.message} action={<button type="button" onClick={() => void refresh()}>重新检查</button>} />;
  }
  if (location.pathname === "/" || location.pathname === "/workspace") return <ControllerWorkspaceRoutes />;
  if (effectiveWorkspaceId === "") return <ControllerWorkspaceRoutes />;

  return <BusinessRuntime key={effectiveWorkspaceId} />;
};

export const DirectApp = () => (
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

export const ControllerApp = ({ initialControllerToken }: { initialControllerToken?: string | undefined }) => (
  <BrowserRouter>
    <HostControlProvider initialControllerToken={initialControllerToken}>
      <HostControlledRuntime />
    </HostControlProvider>
  </BrowserRouter>
);

export const App = ({ initialControllerToken }: { initialControllerToken?: string | undefined }) =>
  runtimeMode === "controller"
    ? <ControllerApp initialControllerToken={initialControllerToken} />
    : <DirectApp />;
