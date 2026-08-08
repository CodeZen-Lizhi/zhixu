import { useQuery, useQueryClient } from "@tanstack/react-query";
import { createContext, type PropsWithChildren, useCallback, useContext, useEffect, useRef, useState } from "react";

import { getActiveWorkspace, type ActiveWorkspace } from "../api/active-workspace";
import { setActiveWorkspaceId, useActiveWorkspaceId } from "./active-workspace";
import { clearWorkspaceRuntimeState } from "./workspace-runtime-state";

interface ActiveWorkspaceBoundaryState {
  status: "loading" | "ready" | "unavailable" | "error";
  workspace?: ActiveWorkspace;
  error?: Error;
  refresh: () => Promise<void>;
}

const ActiveWorkspaceContext = createContext<ActiveWorkspaceBoundaryState | undefined>(undefined);

export const WorkspaceCacheBoundary = ({ children }: PropsWithChildren) => {
  const queryClient = useQueryClient();
  const publishedWorkspaceId = useActiveWorkspaceId();
  const [initialized, setInitialized] = useState(false);
  const [state, setState] = useState<ActiveWorkspaceBoundaryState["status"]>("loading");
  const [workspace, setWorkspace] = useState<ActiveWorkspace>();
  const [error, setError] = useState<Error>();
  const transitionRef = useRef<Promise<void> | undefined>(undefined);
  const transitionGenerationRef = useRef(0);
  const handledIdentityRef = useRef<string | undefined>(undefined);

  const activeQuery = useQuery({
    queryKey: ["active-workspace"],
    queryFn: ({ signal }) => getActiveWorkspace(signal),
    retry: false,
    refetchOnReconnect: true,
    refetchOnWindowFocus: true,
    refetchInterval: 5000,
  });

  const refresh = useCallback(async (): Promise<void> => {
    await activeQuery.refetch();
  }, [activeQuery]);

  useEffect(() => {
    if (activeQuery.isPending) {
      setState("loading");
      return;
    }
    const nextWorkspace = activeQuery.isError ? undefined : activeQuery.data;
    const nextIdentity = nextWorkspace === undefined
      ? `error:${activeQuery.error instanceof Error ? activeQuery.error.message : "unknown"}`
      : `${nextWorkspace.id}:${nextWorkspace.rootPath}:${nextWorkspace.status}:${nextWorkspace.availability}:${String(nextWorkspace.version)}`;
    if (handledIdentityRef.current === nextIdentity && initialized) return;
    handledIdentityRef.current = nextIdentity;
    const generation = transitionGenerationRef.current + 1;
    transitionGenerationRef.current = generation;
    const previousId = publishedWorkspaceId;
    const previousTransition = transitionRef.current;
    const transition = (async (): Promise<void> => {
      if (previousTransition !== undefined) await previousTransition.catch(() => undefined);
      if (transitionGenerationRef.current !== generation) return;
      setState("loading");
      const keepsPreviousGrant = nextWorkspace?.id === previousId && nextWorkspace.availability === "available";
      const shouldRevokePrevious = previousId !== "" && !keepsPreviousGrant;
      if (shouldRevokePrevious) {
        setWorkspace(undefined);
        setError(undefined);
        setActiveWorkspaceId("");
        await clearWorkspaceRuntimeState(queryClient, previousId);
        if (transitionGenerationRef.current !== generation) return;
      }
      if (nextWorkspace === undefined) {
        setWorkspace(undefined);
        setError(activeQuery.error instanceof Error ? activeQuery.error : new Error("无法读取当前 Workspace。"));
        setState("error");
      } else {
        setWorkspace(nextWorkspace);
        setError(undefined);
        if (nextWorkspace.availability === "available") {
          setActiveWorkspaceId(nextWorkspace.id);
          setState("ready");
        } else {
          setActiveWorkspaceId("");
          setState("unavailable");
        }
      }
      setInitialized(true);
    })();
    transitionRef.current = transition;
    void transition.then(
      () => {
        if (transitionRef.current === transition) transitionRef.current = undefined;
      },
      (transitionError: unknown) => {
        if (transitionRef.current === transition) transitionRef.current = undefined;
        if (transitionGenerationRef.current !== generation) return;
        setWorkspace(undefined);
        setActiveWorkspaceId("");
        setError(transitionError instanceof Error ? transitionError : new Error("无法清理旧 Workspace 运行状态。"));
        setState("error");
        setInitialized(true);
      },
    );
  }, [activeQuery.data, activeQuery.error, activeQuery.isError, activeQuery.isPending, initialized, publishedWorkspaceId, queryClient]);

  useEffect(() => () => {
    transitionGenerationRef.current += 1;
    const pending = transitionRef.current;
    if (pending !== undefined) void pending.catch(() => undefined);
  }, []);

  const queryConfirmsPublishedWorkspace = activeQuery.isSuccess
    && activeQuery.data.availability === "available"
    && activeQuery.data.id === publishedWorkspaceId;
  const mustBlockReadyTree = state === "ready" && !queryConfirmsPublishedWorkspace;
  let renderedState: ActiveWorkspaceBoundaryState["status"] = state;
  if (activeQuery.isError) renderedState = "error";
  else if (mustBlockReadyTree) renderedState = "loading";
  const renderedError = activeQuery.isError && activeQuery.error instanceof Error
    ? activeQuery.error
    : error;
  const renderedWorkspace = renderedState === "ready" || renderedState === "unavailable"
    ? workspace
    : undefined;
  const value: ActiveWorkspaceBoundaryState = {
    status: renderedState,
    ...(renderedWorkspace === undefined ? {} : { workspace: renderedWorkspace }),
    ...(renderedError === undefined ? {} : { error: renderedError }),
    refresh,
  };

  if (renderedState === "error") {
    return <ActiveWorkspaceContext.Provider value={value}>
      <main className="auth-gate"><section className="auth-card" aria-live="polite"><p className="eyebrow">ZHIXU / WORKSPACE</p><h1>无法确认当前 Workspace</h1><p>{renderedError?.message ?? "Active Workspace API 暂不可用。"}</p><button type="button" onClick={() => void refresh()}>重新读取</button></section></main>
    </ActiveWorkspaceContext.Provider>;
  }

  if (!initialized || renderedState === "loading") {
    return <ActiveWorkspaceContext.Provider value={value}>
      <main className="auth-gate"><section className="auth-card" aria-live="polite"><p className="eyebrow">ZHIXU / WORKSPACE</p><h1>正在读取当前 Workspace</h1><p>业务页面会在服务端确认授权目录后加载。</p></section></main>
    </ActiveWorkspaceContext.Provider>;
  }

  return <ActiveWorkspaceContext.Provider value={value}>{children}</ActiveWorkspaceContext.Provider>;
};

export const useActiveWorkspace = (): ActiveWorkspaceBoundaryState => {
  const value = useContext(ActiveWorkspaceContext);
  if (value === undefined) throw new Error("useActiveWorkspace must be used within WorkspaceCacheBoundary");
  return value;
};
