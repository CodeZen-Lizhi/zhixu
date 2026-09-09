import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";

import type { InterviewConfig, StartInterviewResult } from "../../api/interview";
import { setActiveWorkspaceId } from "../../app/active-workspace";
import { clearInterviewWorkspaceQueries, useStartInterview } from "./queries";
import { interviewQueryKeys } from "./query-keys";

const workspaceId = "71000000-0000-4000-8000-000000000001";
const otherWorkspaceId = "71000000-0000-4000-8000-000000000002";
afterEach(() => { cleanup(); setActiveWorkspaceId(""); vi.unstubAllGlobals(); });

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

  it.each([false, true])("创建面试响应晚到时仅写回当前 Workspace（已切换：%s）", async (switched) => {
    const sessionId = "71000000-0000-4000-8000-000000000003";
    const questionId = "71000000-0000-4000-8000-000000000004";
    const claimId = "71000000-0000-4000-8000-000000000005";
    const config: InterviewConfig = { schemaVersion: "interview/v1", role: "Go engineer", scope: { claimIds: [claimId], topicIds: [] }, difficulty: "INTERMEDIATE", durationMinutes: 30, questionCount: 1, maxFollowUps: 2 };
    let finish: ((response: Response) => void) | undefined;
    const response = new Promise<Response>((resolve) => { finish = resolve; });
    const fetchMock = vi.fn<typeof fetch>().mockReturnValue(response);
    vi.stubGlobal("fetch", fetchMock);
    setActiveWorkspaceId(workspaceId);
    const client = new QueryClient({ defaultOptions: { mutations: { retry: false } } });
    const wrapper = ({ children }: { children: ReactNode }) => <QueryClientProvider client={client}>{children}</QueryClientProvider>;
    const { result } = renderHook(() => useStartInterview(), { wrapper });
    let request: Promise<StartInterviewResult> | undefined;
    act(() => { request = result.current.mutateAsync({ workspaceId, config, idempotencyKey: "late-interview-create" }); });
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1));
    if (switched) {
      setActiveWorkspaceId(otherWorkspaceId);
      clearInterviewWorkspaceQueries(client, workspaceId);
    }
    const resolveResponse = finish;
    if (resolveResponse === undefined || request === undefined) throw new Error("interview request did not start");
    await act(async () => {
      resolveResponse(new Response(JSON.stringify({
        session: { id: sessionId, workspace_id: workspaceId, config: { schema_version: "interview/v1", role: config.role, scope: { claim_ids: [claimId] }, difficulty: config.difficulty, duration_minutes: 30, question_count: 1, max_follow_ups: 2 }, status: "ACTIVE", version: 1, follow_up_count: 0, started_at: "2026-09-08T10:00:00Z" },
        questions: [{ id: questionId, workspace_id: workspaceId, session_id: sessionId, question_no: 1, follow_up_no: 0, claim_id: claimId, prompt: "Explain cancellation.", status: "PENDING", created_at: "2026-09-08T10:00:00Z" }], replayed: false,
      }), { status: 201, headers: { "Content-Type": "application/json" } }));
      await request;
    });
    const snapshot = client.getQueryData(interviewQueryKeys.session(workspaceId, sessionId));
    if (switched) expect(snapshot).toBeUndefined();
    else expect(snapshot).toMatchObject({ session: { id: sessionId }, questions: [{ id: questionId }] });
    expect(client.getQueryData(interviewQueryKeys.session(otherWorkspaceId, sessionId))).toBeUndefined();
    client.clear();
  });
});
