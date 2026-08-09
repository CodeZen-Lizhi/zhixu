import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import { MemoryApiError, type MemoryRecord } from "../../api/memory";

const workspaceId = "73000000-0000-4000-8000-000000000001";
const memoryId = "73000000-0000-4000-8000-000000000002";

const mocks = vi.hoisted(() => {
  const values: Record<string, Record<string, unknown>> = { memories: {}, create: {}, edit: {}, confirm: {}, pause: {}, resume: {}, remove: {} };
  return values;
});

vi.mock("../../app/active-workspace", () => ({ useActiveWorkspaceId: () => workspaceId }));
vi.mock("./queries", () => ({
  useMemories: () => mocks.memories,
  useCreateMemoryCandidate: () => mocks.create,
  useEditMemory: () => mocks.edit,
  useConfirmMemory: () => mocks.confirm,
  usePauseMemory: () => mocks.pause,
  useResumeMemory: () => mocks.resume,
  useDeleteMemory: () => mocks.remove,
}));

import { MemoriesPage } from "./MemoriesPage";

const candidate: MemoryRecord = {
  id: memoryId, workspaceId, type: "PREFERENCE", content: { text: "concise explanations" }, source: { type: "AGENT", ref: "agent:candidate-1" }, status: "CANDIDATE", version: 1, createdAt: "2026-07-27T00:00:00Z", updatedAt: "2026-07-27T00:00:00Z",
};

const mutation = () => ({ isPending: false, isError: false, error: undefined, variables: undefined, mutate: vi.fn() });

describe("MemoriesPage", () => {
  it("创建 Candidate、确认生命周期且重试复用原 Idempotency-Key", () => {
    mocks.memories = { data: { workspaceId, items: [candidate] }, isPending: false, isError: false, isFetching: false, refetch: vi.fn() };
    mocks.create = mutation(); mocks.edit = mutation(); mocks.confirm = mutation(); mocks.pause = mutation(); mocks.resume = mutation(); mocks.remove = mutation();
    const view = render(<MemoriesPage />);

    expect(screen.queryByText(/owner/i)).not.toBeInTheDocument();
    expect(screen.queryByLabelText("来源类型")).not.toBeInTheDocument();
    expect(screen.queryByLabelText("来源引用")).not.toBeInTheDocument();
    expect(screen.getByText(/来源 助手 · agent:candidate-1/)).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("记忆内容（JSON 对象）"), { target: { value: '{"text":"new candidate"}' } });
    fireEvent.click(screen.getByRole("button", { name: "创建候选" }));
    const createInput = (mocks.create.mutate as ReturnType<typeof vi.fn>).mock.calls[0]?.[0] as { idempotencyKey: string };
    expect(createInput).toMatchObject({ workspaceId, type: "PREFERENCE", content: { text: "new candidate" } });
    expect(createInput).not.toHaveProperty("source");

    fireEvent.click(screen.getByRole("button", { name: "编辑" }));
    expect(screen.getByLabelText("类型")).toBeDisabled();
    fireEvent.change(screen.getByLabelText("记忆内容（JSON 对象）"), { target: { value: '{"text":"edited candidate"}' } });
    fireEvent.click(screen.getByRole("button", { name: "保存修改" }));
    const editInput = (mocks.edit.mutate as ReturnType<typeof vi.fn>).mock.calls[0]?.[0] as Record<string, unknown>;
    expect(editInput).toMatchObject({ workspaceId, memoryId, expectedVersion: 1, content: { text: "edited candidate" } });
    expect(editInput).not.toHaveProperty("source");
    expect(editInput).not.toHaveProperty("type");

    fireEvent.click(screen.getByRole("button", { name: "确认" }));
    const confirmInput = (mocks.confirm.mutate as ReturnType<typeof vi.fn>).mock.calls[0]?.[0] as { idempotencyKey: string };
    expect(confirmInput).toMatchObject({ workspaceId, memoryId, expectedVersion: 1 });

    mocks.confirm = { isPending: false, isError: true, error: new MemoryApiError("NETWORK_ERROR", "NETWORK_ERROR", "lost", true), variables: confirmInput, mutate: mocks.confirm.mutate };
    view.rerender(<MemoriesPage />);
    fireEvent.click(screen.getByRole("button", { name: "重试原请求" }));
    expect((mocks.confirm.mutate as ReturnType<typeof vi.fn>).mock.calls[1]?.[0]).toBe(confirmInput);
  });
});
