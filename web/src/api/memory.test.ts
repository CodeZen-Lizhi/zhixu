import { afterEach, describe, expect, it, vi } from "vitest";

import { MemoryApiError, createMemoryCandidate, decodeMemory, editMemory, listMemories, parseMemoryJsonObject } from "./memory";

const workspaceId = "73000000-0000-4000-8000-000000000001";
const memoryId = "73000000-0000-4000-8000-000000000002";
const createdAt = "2026-07-27T00:00:00Z";

const memory = {
  id: memoryId,
  workspace_id: workspaceId,
  type: "PREFERENCE",
  content: { text: "concise explanations" },
  source: { type: "USER", ref: "settings" },
  status: "CANDIDATE",
  version: 1,
  created_at: createdAt,
  updated_at: createdAt,
};

const response = (value: unknown, status = 200): Response => new Response(JSON.stringify(value), { status, headers: { "Content-Type": "application/json" } });

afterEach(() => vi.unstubAllGlobals());

describe("memory API", () => {
  it("读取 Workspace 绑定的列表，并拒绝任何身份字段", async () => {
    const fetchMock = vi.fn<typeof fetch>().mockResolvedValue(response({ workspace_id: workspaceId, items: [memory] }));
    vi.stubGlobal("fetch", fetchMock);

    await expect(listMemories({ workspaceId, types: ["PREFERENCE"], statuses: ["CANDIDATE"] })).resolves.toMatchObject({ workspaceId, items: [{ id: memoryId }] });
    expect(fetchMock.mock.calls[0]?.[0]).toBe(`/api/v1/memories?workspace_id=${workspaceId}&limit=50&type=PREFERENCE&status=CANDIDATE`);
    expect(() => decodeMemory({ ...memory, owner: { id: workspaceId } })).toThrow(MemoryApiError);
    expect(() => decodeMemory({ ...memory, confirmed_by: { id: workspaceId } })).toThrow(MemoryApiError);
  });

  it("创建 Candidate 仅提交用户可编辑字段，且 Idempotency-Key 固定透传", async () => {
    const fetchMock = vi.fn<typeof fetch>().mockResolvedValue(response({ memory, replayed: false }, 201));
    vi.stubGlobal("fetch", fetchMock);

    await expect(createMemoryCandidate({ workspaceId, type: "PREFERENCE", content: { text: "concise explanations" }, idempotencyKey: "memory-create-1" })).resolves.toMatchObject({ memory: { id: memoryId }, replayed: false });
    const call = fetchMock.mock.calls[0];
    expect(new Headers(call?.[1]?.headers).get("Idempotency-Key")).toBe("memory-create-1");
    if (typeof call?.[1]?.body !== "string") throw new Error("expected JSON request body");
    expect(JSON.parse(call[1].body)).toEqual({ workspace_id: workspaceId, type: "PREFERENCE", content: { text: "concise explanations" } });
  });

  it("编辑只提交可变字段并保留响应中的来源 provenance", async () => {
    const edited = { ...memory, content: { text: "detailed explanations" }, source: { type: "AGENT", ref: "agent:candidate-1" }, version: 2 };
    const fetchMock = vi.fn<typeof fetch>().mockResolvedValue(response({ memory: edited, replayed: false }));
    vi.stubGlobal("fetch", fetchMock);

    await expect(editMemory({
      workspaceId,
      memoryId,
      expectedVersion: 1,
      content: { text: "detailed explanations" },
      idempotencyKey: "memory-edit-1",
    })).resolves.toMatchObject({ memory: { source: { type: "AGENT", ref: "agent:candidate-1" } } });
    const call = fetchMock.mock.calls[0];
    if (typeof call?.[1]?.body !== "string") throw new Error("expected JSON request body");
    expect(JSON.parse(call[1].body)).toEqual({ workspace_id: workspaceId, expected_version: 1, content: { text: "detailed explanations" } });
  });

  it("拒绝未确认的 ACTIVE、重复 JSON key 和无到期 EPISODIC", async () => {
    expect(() => decodeMemory({ ...memory, status: "ACTIVE" })).toThrow(MemoryApiError);
    await expect(createMemoryCandidate({ workspaceId, type: "EPISODIC", content: { text: "temporary" }, idempotencyKey: "memory-create-episodic" })).rejects.toMatchObject({ code: "INVALID_REQUEST" });
    vi.stubGlobal("fetch", vi.fn<typeof fetch>().mockResolvedValue(new Response(`{"workspace_id":"${workspaceId}","workspace_id":"${workspaceId}","items":[]}`, { status: 200 })));
    await expect(listMemories({ workspaceId })).rejects.toMatchObject({ code: "INVALID_RESPONSE" });
  });

  it("拒绝会被 JavaScript Date 归一化的非法月历日期", () => {
    expect(() => decodeMemory({ ...memory, created_at: "2026-02-31T00:00:00Z" })).toThrow(MemoryApiError);
  });

  it.each([
    ["0000", "0000-02-29T00:00:00Z", "0000-02-30T00:00:00Z"],
    ["0001", "0001-02-28T00:00:00Z", "0001-02-29T00:00:00Z"],
    ["1900", "1900-02-28T00:00:00Z", "1900-02-29T00:00:00Z"],
    ["2000", "2000-02-29T00:00:00Z", "2000-02-30T00:00:00Z"],
  ])("按 Gregorian 闰年规则校验 %s 年", (_year, validTimestamp, invalidTimestamp) => {
    expect(() => decodeMemory({ ...memory, created_at: validTimestamp })).not.toThrow();
    expect(() => decodeMemory({ ...memory, created_at: invalidTimestamp })).toThrow(MemoryApiError);
  });

  it("表单 JSON 也经过同一严格 Memory 内容边界", () => {
    expect(parseMemoryJsonObject('{"text":"safe"}')).toEqual({ text: "safe" });
    expect(parseMemoryJsonObject('{"text":"first","text":"second"}')).toBeUndefined();
    expect(parseMemoryJsonObject('[]')).toBeUndefined();
  });
});
