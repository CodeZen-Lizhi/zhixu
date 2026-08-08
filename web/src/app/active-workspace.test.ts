import { act, renderHook } from "@testing-library/react";
import { beforeEach, describe, expect, it } from "vitest";

import { getActiveWorkspaceId, setActiveWorkspaceId, useActiveWorkspaceId } from "./active-workspace";

const workspaceId = "92000000-0000-4000-8000-000000000001";
const legacyWorkspaceKey = ["legacy", "active", "workspace"].join(".");

describe("active workspace projection", () => {
  beforeEach(() => setActiveWorkspaceId(""));

  it("在内存中发布服务端确认的 Workspace ID", () => {
    const { result } = renderHook(() => useActiveWorkspaceId());
    expect(result.current).toBe("");
    act(() => setActiveWorkspaceId(workspaceId));
    expect(result.current).toBe(workspaceId);
    expect(getActiveWorkspaceId()).toBe(workspaceId);
    expect(window.localStorage.getItem(legacyWorkspaceKey)).toBeNull();
    act(() => setActiveWorkspaceId(""));
    expect(result.current).toBe("");
  });

  it("拒绝非法 ID，并忽略 localStorage 中的旧身份投影", () => {
    expect(() => setActiveWorkspaceId("bad")).toThrow("规范 UUID");
    window.localStorage.setItem(legacyWorkspaceKey, workspaceId);
    expect(getActiveWorkspaceId()).toBe("");
  });
});
