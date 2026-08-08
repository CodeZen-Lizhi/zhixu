import { render, screen } from "@testing-library/react";
import type { ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";

const composition = vi.hoisted(() => ({ authenticated: false }));

vi.mock("./auth-context", () => ({
  AuthProvider: ({ children }: { children: ReactNode }) => <>{children}</>,
  AuthBoundary: ({ children }: { children: ReactNode }) => composition.authenticated ? <div data-testid="auth-boundary">{children}</div> : <div>认证门禁</div>,
}));
vi.mock("../routes/AppRoutes", () => ({ AppRoutes: () => <div>业务路由</div> }));
vi.mock("../events/event-store", () => ({ EventStoreProvider: ({ children }: { children: ReactNode }) => <div data-testid="event-store">{children}</div> }));
vi.mock("./WorkspaceCacheBoundary", () => ({ WorkspaceCacheBoundary: ({ children }: { children: ReactNode }) => <div data-testid="workspace-boundary">{children}</div> }));

import { App } from "./App";

describe("App authentication composition", () => {
  beforeEach(() => {
    composition.authenticated = false;
  });

  it("认证门禁在 Router、SSE 和业务页面之前阻断渲染", () => {
    render(<App />);

    expect(screen.getByText("认证门禁")).toBeInTheDocument();
    expect(screen.queryByText("业务路由")).not.toBeInTheDocument();
  });

  it("认证成功后依次挂载 Active Workspace、SSE 与业务路由", () => {
    composition.authenticated = true;
    render(<App />);

    const authBoundary = screen.getByTestId("auth-boundary");
    const workspaceBoundary = screen.getByTestId("workspace-boundary");
    const eventStore = screen.getByTestId("event-store");
    const routes = screen.getByText("业务路由");
    expect(authBoundary).toContainElement(workspaceBoundary);
    expect(workspaceBoundary).toContainElement(eventStore);
    expect(eventStore).toContainElement(routes);
  });
});
