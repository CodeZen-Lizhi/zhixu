import { useSyncExternalStore } from "react";
import { canonicalUuidPattern as uuidPattern } from "../shared/codec";

const listeners = new Set<() => void>();
let snapshot = "";

/**
 * 仅保留页面生命周期内的投影。生产环境的 ID 由 Active Workspace API 发布，
 * 这里的 setter 只供边界和测试同步 React 订阅者，不读写浏览器存储。
 */
export const getActiveWorkspaceId = (): string => snapshot;

export const setActiveWorkspaceId = (workspaceId: string): void => {
  if (workspaceId !== "" && !uuidPattern.test(workspaceId)) {
    throw new Error("活动 Workspace ID 必须是规范 UUID");
  }
  if (snapshot === workspaceId) return;
  snapshot = workspaceId;
  listeners.forEach((listener) => listener());
};

export const subscribeActiveWorkspace = (listener: () => void): (() => void) => {
  listeners.add(listener);
  return () => listeners.delete(listener);
};

export const useActiveWorkspaceId = (): string =>
  useSyncExternalStore(subscribeActiveWorkspace, getActiveWorkspaceId, () => "");
