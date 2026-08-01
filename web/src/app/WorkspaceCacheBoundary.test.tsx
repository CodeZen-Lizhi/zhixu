import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, waitFor } from "@testing-library/react";
import { type PropsWithChildren } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";

const workspaceState = vi.hoisted(() => ({ id: "77000000-0000-4000-8000-000000000001" }));

vi.mock("./active-workspace", () => ({ useActiveWorkspaceId: () => workspaceState.id }));

import { timelineQueryKeys } from "../features/timeline/query-keys";
import { runtimeMode } from "./runtime-mode";
import { WorkspaceCacheBoundary } from "./WorkspaceCacheBoundary";

const workspaceId = "77000000-0000-4000-8000-000000000001";
const nextWorkspaceId = "77000000-0000-4000-8000-000000000002";

const Boundary = ({ children }: PropsWithChildren) => <WorkspaceCacheBoundary>{children}</WorkspaceCacheBoundary>;

afterEach(() => {
  workspaceState.id = workspaceId;
});

describe("WorkspaceCacheBoundary", () => {
  it("Direct 模式清理旧缓存，Controller 模式交给 RuntimeAccessProvider", async () => {
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: Infinity } } });
    const previousKey = timelineQueryKeys.list(workspaceId, "{}", 25);
    const currentKey = timelineQueryKeys.list(nextWorkspaceId, "{}", 25);
    queryClient.setQueryData(previousKey, { workspaceId, items: [] });
    queryClient.setQueryData(currentKey, { workspaceId: nextWorkspaceId, items: [] });
    const view = render(
      <QueryClientProvider client={queryClient}>
        <Boundary><div>content</div></Boundary>
      </QueryClientProvider>,
    );

    workspaceState.id = nextWorkspaceId;
    view.rerender(
      <QueryClientProvider client={queryClient}>
        <Boundary><div>content</div></Boundary>
      </QueryClientProvider>,
    );

    if (runtimeMode === "direct") {
      await waitFor(() => expect(queryClient.getQueryData(previousKey)).toBeUndefined());
    } else {
      expect(queryClient.getQueryData(previousKey)).toEqual({ workspaceId, items: [] });
    }
    expect(queryClient.getQueryData(currentKey)).toEqual({ workspaceId: nextWorkspaceId, items: [] });
  });
});
