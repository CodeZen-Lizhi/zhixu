import { render, screen } from "@testing-library/react";
import type { ReactNode } from "react";
import { describe, expect, it, vi } from "vitest";

vi.mock("./auth-context", () => ({
  AuthProvider: ({ children }: { children: ReactNode }) => <>{children}</>,
  AuthBoundary: () => <div>认证门禁</div>,
}));
vi.mock("../routes/AppRoutes", () => ({ AppRoutes: () => <div>业务路由</div> }));
vi.mock("../events/event-store", () => ({ EventStoreProvider: ({ children }: { children: ReactNode }) => <>{children}</> }));
vi.mock("./WorkspaceCacheBoundary", () => ({ WorkspaceCacheBoundary: ({ children }: { children: ReactNode }) => <>{children}</> }));

import { App } from "./App";

describe("App authentication composition", () => {
  it("认证门禁在 Router、SSE 和业务页面之前阻断渲染", () => {
    render(<App />);

    expect(screen.getByText("认证门禁")).toBeInTheDocument();
    expect(screen.queryByText("业务路由")).not.toBeInTheDocument();
  });
});
