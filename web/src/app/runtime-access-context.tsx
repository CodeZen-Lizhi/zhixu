import { useQueryClient } from "@tanstack/react-query";
import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { Link } from "react-router-dom";

import { getRuntimeAccess, type RuntimeAccess, type RuntimeAccessStatus } from "../api/runtime-access";
import { subscribeRuntimeAccessInvalidation } from "../shared/runtime-access-invalidation";
import { Button } from "../shared/ui";
import { setActiveWorkspaceId } from "./active-workspace";
import { clearWorkspaceRuntimeState } from "./workspace-runtime-state";

export type RuntimeAccessState =
  | { status: "loading" }
  | { status: "error" }
  | RuntimeAccess;

interface RuntimeAccessContextValue {
  state: RuntimeAccessState;
  refresh: () => void;
}

const RuntimeAccessContext = createContext<RuntimeAccessContextValue | undefined>(undefined);
const isAbortError = (value: unknown): boolean => (value instanceof Error || value instanceof DOMException) && value.name === "AbortError";
const nextPollAfter = (state: RuntimeAccessState, failures: number): number => {
  if (state.status === "ready" || state.status === "waiting" || state.status === "unavailable") return state.pollAfterMs;
  return Math.min(5000, 500 * 2 ** Math.min(failures, 4));
};

export const RuntimeAccessProvider = ({ children }: { children: ReactNode }) => {
  const queryClient = useQueryClient();
  const [state, setState] = useState<RuntimeAccessState>({ status: "loading" });
  const stateRef = useRef<RuntimeAccessState>({ status: "loading" });
  const publishedWorkspaceRef = useRef("");
  const requestRef = useRef<AbortController | undefined>(undefined);
  const epochRef = useRef(0);
  const failureRef = useRef(0);
  const timerRef = useRef<number | undefined>(undefined);
  const cleanupRef = useRef(Promise.resolve());

  const queueWorkspaceCleanup = useCallback((workspaceId: string): Promise<void> => {
    const cleanup = cleanupRef.current.then(async () => {
      await clearWorkspaceRuntimeState(queryClient, workspaceId);
    });
    cleanupRef.current = cleanup.catch(() => undefined);
    return cleanup;
  }, [queryClient]);

  const publish = useCallback(async (next: RuntimeAccessState, epoch: number): Promise<void> => {
    const nextWorkspace = next.status === "ready" ? next.activeWorkspaceId : "";
    const previousWorkspace = publishedWorkspaceRef.current;
    if (previousWorkspace !== nextWorkspace) {
      publishedWorkspaceRef.current = "";
      setActiveWorkspaceId("");
      if (previousWorkspace !== "") {
        // 先卸载旧业务树；A -> B 清理期间保持 loading，绝不提前渲染 B。
        const suspended: RuntimeAccessState = next.status === "ready" ? { status: "loading" } : next;
        stateRef.current = suspended;
        setState(suspended);
      }
      await queueWorkspaceCleanup(previousWorkspace);
      if (epoch !== epochRef.current) return;
      publishedWorkspaceRef.current = nextWorkspace;
      setActiveWorkspaceId(nextWorkspace);
    }
    if (epoch === epochRef.current) {
      stateRef.current = next;
      setState(next);
    }
  }, [queueWorkspaceCleanup]);

  const suspend = useCallback(async (): Promise<void> => {
    epochRef.current += 1;
    const epoch = epochRef.current;
    requestRef.current?.abort();
    window.clearTimeout(timerRef.current);
    await publish({ status: "error" }, epoch);
  }, [publish]);

  const refresh = useCallback((failClosed = false): void => {
    epochRef.current += 1;
    const epoch = epochRef.current;
    requestRef.current?.abort();
    window.clearTimeout(timerRef.current);
    void (async () => {
      const controller = new AbortController();
      requestRef.current = controller;
      try {
        if (failClosed) {
          await publish({ status: "error" }, epoch);
          if (epoch !== epochRef.current) return;
        }
        const next = await getRuntimeAccess(controller.signal);
        if (controller.signal.aborted || epoch !== epochRef.current) return;
        failureRef.current = 0;
        await publish(next, epoch);
      } catch (error: unknown) {
        if (controller.signal.aborted || isAbortError(error) || epoch !== epochRef.current) return;
        failureRef.current += 1;
        await publish({ status: "error" }, epoch);
      } finally {
        if (epoch === epochRef.current) {
          timerRef.current = window.setTimeout(refresh, nextPollAfter(stateRef.current, failureRef.current));
        }
      }
    })();
  }, [publish]);

  useEffect(() => {
    refresh();
    return subscribeRuntimeAccessInvalidation((mode) => {
      if (mode === "suspend") return suspend();
      refresh(true);
    });
  }, [refresh, suspend]);
  useEffect(() => () => {
    epochRef.current += 1;
    requestRef.current?.abort();
    window.clearTimeout(timerRef.current);
    const previousWorkspace = publishedWorkspaceRef.current;
    publishedWorkspaceRef.current = "";
    setActiveWorkspaceId("");
    void queueWorkspaceCleanup(previousWorkspace);
  }, [queueWorkspaceCleanup]);

  const value = useMemo<RuntimeAccessContextValue>(() => ({ state, refresh }), [refresh, state]);
  return <RuntimeAccessContext.Provider value={value}>{children}</RuntimeAccessContext.Provider>;
};

export const useRuntimeAccess = (): RuntimeAccessContextValue => {
  const value = useContext(RuntimeAccessContext);
  if (value === undefined) throw new Error("useRuntimeAccess must be used within RuntimeAccessProvider");
  return value;
};

const RuntimeGate = ({ status, retry }: { status: Exclude<RuntimeAccessStatus, "ready"> | "loading" | "error"; retry: () => void }) => {
  const copy = status === "waiting"
    ? ["尚未连接工作区", "选择一个目录后即可进入。"]
    : status === "unavailable" || status === "error"
      ? ["暂时无法进入", "服务恢复后会自动重试。"]
      : ["正在连接", "正在确认当前工作区。"];
  const action = status === "waiting"
    ? <Button className="control-gate__action" asChild><Link to="/workspace">连接工作区</Link></Button>
    : status === "error" || status === "unavailable"
      ? <Button className="control-gate__action" variant="secondary" onClick={() => retry()}>重试</Button>
      : null;
  return (
    <main className="control-gate">
      <section className="control-gate__content" aria-live="polite">
        <p className="eyebrow">知序</p>
        <h1>{copy[0]}</h1>
        <p>{copy[1]}</p>
        {action}
      </section>
    </main>
  );
};

export const RuntimeAccessBoundary = ({ children }: { children: ReactNode }) => {
  const { state, refresh } = useRuntimeAccess();
  if (state.status !== "ready") return <RuntimeGate status={state.status} retry={refresh} />;
  return children;
};
