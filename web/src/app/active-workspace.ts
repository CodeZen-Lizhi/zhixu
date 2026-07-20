import { useSyncExternalStore } from "react";

export const activeWorkspaceStorageKey = "zhixu.active-workspace-id";

const uuidPattern = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;
const listeners = new Set<() => void>();

const readStorage = (): string => {
  if (typeof window === "undefined") return "";
  try {
    const value = window.localStorage.getItem(activeWorkspaceStorageKey) ?? "";
    return uuidPattern.test(value) ? value : "";
  } catch {
    return "";
  }
};

let snapshot = readStorage();

const emit = (): void => {
  snapshot = readStorage();
  listeners.forEach((listener) => listener());
};

if (typeof window !== "undefined") {
  window.addEventListener("storage", (event) => {
    if (event.key === activeWorkspaceStorageKey) emit();
  });
}

export const getActiveWorkspaceId = (): string => {
  snapshot = readStorage();
  return snapshot;
};

export const setActiveWorkspaceId = (workspaceId: string): void => {
  if (workspaceId !== "" && !uuidPattern.test(workspaceId)) {
    throw new Error("活动 Workspace ID 必须是规范 UUID");
  }
  if (workspaceId === "") window.localStorage.removeItem(activeWorkspaceStorageKey);
  else window.localStorage.setItem(activeWorkspaceStorageKey, workspaceId);
  emit();
};

export const subscribeActiveWorkspace = (listener: () => void): (() => void) => {
  listeners.add(listener);
  return () => listeners.delete(listener);
};

export const useActiveWorkspaceId = (): string =>
  useSyncExternalStore(subscribeActiveWorkspace, getActiveWorkspaceId, () => "");
