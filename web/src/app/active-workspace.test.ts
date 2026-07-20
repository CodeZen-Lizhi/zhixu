import { act, renderHook } from "@testing-library/react";
import { beforeEach, describe, expect, it } from "vitest";

import {
  activeWorkspaceStorageKey,
  getActiveWorkspaceId,
  setActiveWorkspaceId,
  useActiveWorkspaceId,
} from "./active-workspace";

const workspaceId = "92000000-0000-4000-8000-000000000001";

describe("active workspace owner", () => {
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
  });

  it("在单一 storage key 中读写并通知当前页面", () => {
    const { result } = renderHook(() => useActiveWorkspaceId());
    expect(result.current).toBe("");
    act(() => setActiveWorkspaceId(workspaceId));
    expect(result.current).toBe(workspaceId);
    expect(window.localStorage.getItem(activeWorkspaceStorageKey)).toBe(workspaceId);
    act(() => setActiveWorkspaceId(""));
    expect(result.current).toBe("");
  });

  it("拒绝写入并忽略读取非法 Workspace ID", () => {
    expect(() => setActiveWorkspaceId("bad")).toThrow("规范 UUID");
    window.localStorage.setItem(activeWorkspaceStorageKey, "bad");
    expect(getActiveWorkspaceId()).toBe("");
  });
});
