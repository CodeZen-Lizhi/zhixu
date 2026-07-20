import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it } from "vitest";
import { Link, MemoryRouter, Route, Routes } from "react-router-dom";

import { setActiveWorkspaceId } from "../../app/active-workspace";
import { GraphWorkspaceCacheBoundary } from "./GraphWorkspaceCacheBoundary";
import { graphQueryKeys } from "./query-keys";

const workspaceId = "96000000-0000-4000-8000-000000000001";
const otherWorkspaceId = "97000000-0000-4000-8000-000000000001";
const relationId = "96000000-0000-4000-8000-000000000002";

beforeEach(() => {
  const values = new Map<string, string>();
  Object.defineProperty(window, "localStorage", {
    configurable: true,
    value: {
      clear: () => values.clear(),
      getItem: (key: string) => values.get(key) ?? null,
      removeItem: (key: string) => values.delete(key),
      setItem: (key: string, value: string) => values.set(key, value),
    },
  });
  setActiveWorkspaceId(workspaceId);
});

describe("GraphWorkspaceCacheBoundary", () => {
  it("Graph 路由卸载后切换 Workspace 仍清理旧 Graph 缓存", async () => {
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: Infinity } } });
    queryClient.setQueryData(graphQueryKeys.evidence({ workspaceId, relationId, limit: 20 }), { sensitive: "old evidence" });
    queryClient.setQueryData(graphQueryKeys.global({ workspaceId: otherWorkspaceId, limit: 25 }), { current: true });

    render(
      <QueryClientProvider client={queryClient}>
        <GraphWorkspaceCacheBoundary>
          <MemoryRouter initialEntries={["/graph"]}>
            <Routes>
              <Route path="/graph" element={<Link to="/">Workspace</Link>} />
              <Route path="/" element={<button type="button" onClick={() => setActiveWorkspaceId(otherWorkspaceId)}>切换 Workspace</button>} />
            </Routes>
          </MemoryRouter>
        </GraphWorkspaceCacheBoundary>
      </QueryClientProvider>,
    );

    fireEvent.click(screen.getByRole("link", { name: "Workspace" }));
    expect(queryClient.getQueryCache().findAll({ queryKey: graphQueryKeys.all(workspaceId) })).toHaveLength(1);

    fireEvent.click(screen.getByRole("button", { name: "切换 Workspace" }));
    await waitFor(() => expect(queryClient.getQueryCache().findAll({ queryKey: graphQueryKeys.all(workspaceId) })).toHaveLength(0));
    expect(queryClient.getQueryCache().findAll({ queryKey: graphQueryKeys.all(otherWorkspaceId) })).toHaveLength(1);
  });
});
