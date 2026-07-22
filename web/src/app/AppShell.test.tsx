import { act, render, screen } from "@testing-library/react";
import { lazy, type ComponentType } from "react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { describe, expect, it, vi } from "vitest";

vi.mock("../events/event-store", () => ({
  useEventStore: () => ({ state: "open", retryRecovery: vi.fn() }),
}));
vi.mock("./active-workspace", () => ({
  useActiveWorkspaceId: () => "10000000-0000-4000-8000-000000000002",
}));

import { AppShell } from "./AppShell";

describe("AppShell", () => {
  it("keeps navigation and context mounted while a lazy route loads", async () => {
    let resolvePage: ((module: { default: ComponentType }) => void) | undefined;
    const DeferredPage = lazy(() => new Promise<{ default: ComponentType }>((resolve) => {
      resolvePage = resolve;
    }));

    render(
      <MemoryRouter initialEntries={["/dashboard"]}>
        <Routes>
          <Route element={<AppShell />}>
            <Route path="/dashboard" element={<DeferredPage />} />
          </Route>
        </Routes>
      </MemoryRouter>,
    );

    expect(screen.getByRole("navigation", { name: "主导航" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Search" })).toHaveAttribute("href", "/search");
    expect(screen.getByRole("status")).toHaveTextContent("实时同步");
    expect(screen.getByText("正在加载工作台…")).toBeInTheDocument();

    act(() => {
      resolvePage?.({ default: () => <p>Lazy route ready</p> });
    });

    expect(await screen.findByText("Lazy route ready")).toBeInTheDocument();
    expect(screen.getByRole("navigation", { name: "主导航" })).toBeInTheDocument();
  });
});
