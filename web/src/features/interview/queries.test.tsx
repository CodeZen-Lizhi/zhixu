import { QueryClient } from "@tanstack/react-query";
import { describe, expect, it } from "vitest";

import { clearInterviewWorkspaceQueries } from "./queries";
import { interviewQueryKeys } from "./query-keys";

const workspaceId = "71000000-0000-4000-8000-000000000001";
const otherWorkspaceId = "71000000-0000-4000-8000-000000000002";

describe("Interview query cache", () => {
  it("只在 Workspace 切换时清理对应 Interview 快照", () => {
    const client = new QueryClient();
    client.setQueryData(interviewQueryKeys.sessions(workspaceId), { pages: [{ items: [] }] });
    client.setQueryData(["interview", workspaceId, "session", "71000000-0000-4000-8000-000000000003"], { session: {} });
    client.setQueryData(["interview", otherWorkspaceId, "session", "71000000-0000-4000-8000-000000000004"], { session: {} });

    clearInterviewWorkspaceQueries(client, workspaceId);

    expect(client.getQueryData(interviewQueryKeys.sessions(workspaceId))).toBeUndefined();
    expect(client.getQueryData(["interview", workspaceId, "session", "71000000-0000-4000-8000-000000000003"])).toBeUndefined();
    expect(client.getQueryData(["interview", otherWorkspaceId, "session", "71000000-0000-4000-8000-000000000004"])).toEqual({ session: {} });
  });
});
