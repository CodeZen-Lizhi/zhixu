import { act, renderHook } from "@testing-library/react";
import { beforeEach, describe, expect, it } from "vitest";

import {
  activeWorkspaceStorageKey,
  getActiveWorkspaceId,
  setActiveWorkspaceId,
  useActiveWorkspaceId,
} from "./active-workspace";
import { runtimeMode } from "./runtime-mode";

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
    setActiveWorkspaceId("");
  });

  it("Direct 模式持久化，Controller 模式只发布进程内权威值", () => {
    const { result } = renderHook(() => useActiveWorkspaceId());
    expect(result.current).toBe("");
    act(() => setActiveWorkspaceId(workspaceId));
    expect(result.current).toBe(workspaceId);
    expect(window.localStorage.getItem(activeWorkspaceStorageKey)).toBe(runtimeMode === "direct" ? workspaceId : null);
    act(() => setActiveWorkspaceId(""));
    expect(result.current).toBe("");
  });

  it("拒绝非法 ID，Controller 模式忽略 storage 恢复和跨标签页事件", () => {
    expect(() => setActiveWorkspaceId("bad")).toThrow("规范 UUID");
    window.localStorage.setItem(activeWorkspaceStorageKey, runtimeMode === "direct" ? "bad" : workspaceId);
    expect(getActiveWorkspaceId()).toBe("");
    if (runtimeMode === "controller") {
      window.dispatchEvent(new StorageEvent("storage", { key: activeWorkspaceStorageKey, newValue: workspaceId }));
      expect(getActiveWorkspaceId()).toBe("");
    }
  });
});
