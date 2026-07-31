import { useQueryClient } from "@tanstack/react-query";
import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from "react";

import {
  checkControllerWorkspaceAvailability,
  clearControllerCsrfToken,
  ControllerApiError,
  type ControllerOperation,
  type ControllerSession,
  type ControllerState,
  type ControllerWorkspaceSwitchRequest,
  exchangeControllerSession,
  getControllerSession,
  getControllerState,
  removeControllerWorkspace,
  startControllerWorkspaceSwitch,
} from "../api/controller";
import { setActiveWorkspaceId } from "./active-workspace";
import { clearWorkspaceRuntimeState } from "./workspace-runtime-state";

export type HostControlState =
  | { status: "loading" }
  | { status: "session_required" }
  | { status: "error"; error: Error }
  | { status: "ready"; session: ControllerSession; controlState: ControllerState };

interface HostControlContextValue {
  state: HostControlState;
  effectiveWorkspaceId: string;
  actionPending: boolean;
  actionError?: Error;
  refresh: () => Promise<void>;
  switchToRegisteredWorkspace: (workspaceId: string) => Promise<void>;
  createAndSwitchWorkspace: (input: { name: string; rootPath: string; initializeGit: boolean }) => Promise<void>;
  checkWorkspaceAvailability: (workspaceId: string) => Promise<void>;
  removeWorkspace: (workspaceId: string) => Promise<void>;
}

const HostControlContext = createContext<HostControlContextValue | undefined>(undefined);

const isTerminalOperation = (operation: ControllerOperation): boolean => operation.result !== undefined;

export const effectiveWorkspaceFromState = (
  state: ControllerState,
  suspendedOperationId?: string,
): string => {
  if (suspendedOperationId !== undefined) return "";
  if (state.runtime.status !== "ready" || state.runtime.api.status !== "ready" || state.runtime.worker.status !== "ready") return "";
  const active = state.activeWorkspace;
  if (active?.availability !== "available") return "";
  const operation = state.operation;
  if (operation === null) return active.workspaceId;
  if (!isTerminalOperation(operation) || operation.result === "failed") return "";
  if (operation.result === "succeeded") {
    return operation.targetWorkspaceId === active.workspaceId ? active.workspaceId : "";
  }
  return active.workspaceId;
};

const newIdempotencyKey = (): string => {
  if (typeof globalThis.crypto.randomUUID !== "function") {
    throw new Error("当前浏览器无法生成安全的幂等请求标识");
  }
  return globalThis.crypto.randomUUID();
};

const asError = (value: unknown): Error => value instanceof Error ? value : new Error("Workspace 控制请求失败");
const isAbortError = (value: unknown): boolean => (value instanceof DOMException || value instanceof Error) && value.name === "AbortError";

export const HostControlProvider = ({
  children,
  initialControllerToken,
}: {
  children: ReactNode;
  initialControllerToken?: string | undefined;
}) => {
  const queryClient = useQueryClient();
  const [state, setState] = useState<HostControlState>({ status: "loading" });
  const [effectiveWorkspaceId, setEffectiveWorkspaceId] = useState("");
  const [actionPending, setActionPending] = useState(false);
  const [actionError, setActionError] = useState<Error>();
  const sessionRef = useRef<ControllerSession | undefined>(undefined);
  const controllerStateRef = useRef<ControllerState | undefined>(undefined);
  const effectiveWorkspaceIdRef = useRef("");
  const initialTokenRef = useRef(initialControllerToken);
  const sessionPromiseRef = useRef<Promise<ControllerSession> | undefined>(undefined);
  const cleanupPromiseRef = useRef<Promise<void>>(Promise.resolve());
  const applySequenceRef = useRef(0);
  const suspendedOperationIdRef = useRef<string | undefined>(undefined);
  const actionPendingRef = useRef(false);
  const commandAttemptRef = useRef<{ signature: string; key: string } | undefined>(undefined);
  const bootstrapEffectRef = useRef<object | undefined>(undefined);
  const pollingEffectRef = useRef<object | undefined>(undefined);
  const sessionEpochRef = useRef(0);
  const stateRequestControllersRef = useRef(new Set<AbortController>());
  const stateRequestSequenceRef = useRef(0);
  const appliedStateRequestSequenceRef = useRef(0);

  const invalidateStateRequests = useCallback((): void => {
    sessionEpochRef.current += 1;
    for (const controller of stateRequestControllersRef.current) controller.abort();
    stateRequestControllersRef.current.clear();
  }, []);

  const queueWorkspaceCleanup = useCallback((workspaceId: string): Promise<void> => {
    if (workspaceId === "") return cleanupPromiseRef.current;
    const cleanup = cleanupPromiseRef.current.then(async () => {
      await clearWorkspaceRuntimeState(queryClient, workspaceId);
    });
    cleanupPromiseRef.current = cleanup.catch(() => undefined);
    return cleanup;
  }, [queryClient]);

  const suspendBusinessRuntime = useCallback(async (): Promise<void> => {
    const previous = effectiveWorkspaceIdRef.current;
    effectiveWorkspaceIdRef.current = "";
    setEffectiveWorkspaceId("");
    setActiveWorkspaceId("");
    await Promise.all([
      queueWorkspaceCleanup(previous),
      new Promise<void>((resolve) => window.setTimeout(resolve, 0)),
    ]);
  }, [queueWorkspaceCleanup]);

  const applyControllerState = useCallback(async (
    session: ControllerSession,
    next: ControllerState,
  ): Promise<void> => {
    if (next.controllerInstanceId !== session.controllerInstanceId) {
      throw new ControllerApiError(
        "INVALID_RESPONSE",
        "CONTROLLER_INSTANCE_CHANGED",
        "控制器实例已变化，请通过启动器打开新的控制链接。",
        false,
      );
    }
    const currentVersion = controllerStateRef.current?.stateVersion ?? 0;
    if (next.stateVersion < currentVersion) return;

    const sequence = applySequenceRef.current + 1;
    applySequenceRef.current = sequence;
    controllerStateRef.current = next;
    if (suspendedOperationIdRef.current !== undefined
      && next.operation?.operationId === suspendedOperationIdRef.current
      && isTerminalOperation(next.operation)) {
      suspendedOperationIdRef.current = undefined;
    }
    const nextEffective = effectiveWorkspaceFromState(next, suspendedOperationIdRef.current);
    const previousEffective = effectiveWorkspaceIdRef.current;
    setState({ status: "ready", session, controlState: next });

    if (previousEffective !== "" && previousEffective !== nextEffective) {
      effectiveWorkspaceIdRef.current = "";
      setEffectiveWorkspaceId("");
      setActiveWorkspaceId("");
      void queueWorkspaceCleanup(previousEffective);
    }
    await cleanupPromiseRef.current;
    if (applySequenceRef.current !== sequence) return;

    if (nextEffective === "") {
      effectiveWorkspaceIdRef.current = "";
      setEffectiveWorkspaceId("");
      setActiveWorkspaceId("");
      return;
    }
    setActiveWorkspaceId(nextEffective);
    effectiveWorkspaceIdRef.current = nextEffective;
    setEffectiveWorkspaceId(nextEffective);
  }, [queueWorkspaceCleanup]);

  const establishSession = useCallback((): Promise<ControllerSession> => {
    if (sessionRef.current !== undefined) return Promise.resolve(sessionRef.current);
    if (sessionPromiseRef.current !== undefined) return sessionPromiseRef.current;
    const token = initialTokenRef.current;
    initialTokenRef.current = undefined;
    const promise = token === undefined ? getControllerSession() : exchangeControllerSession(token);
    const trackedPromise = promise.then((session) => {
      sessionRef.current = session;
      sessionEpochRef.current += 1;
      return session;
    }, (error: unknown) => {
      sessionPromiseRef.current = undefined;
      throw error;
    });
    sessionPromiseRef.current = trackedPromise;
    return trackedPromise;
  }, []);

  const becomeUnavailable = useCallback(async (error: Error): Promise<void> => {
    invalidateStateRequests();
    applySequenceRef.current += 1;
    if (error instanceof ControllerApiError
      && (error.status === 401 || error.errorCode === "CONTROLLER_INSTANCE_CHANGED")) {
      clearControllerCsrfToken();
      sessionRef.current = undefined;
      sessionPromiseRef.current = undefined;
      controllerStateRef.current = undefined;
      setState({ status: "session_required" });
    } else {
      setState({ status: "error", error });
    }
    await suspendBusinessRuntime();
  }, [invalidateStateRequests, suspendBusinessRuntime]);

  const loadControllerState = useCallback(async (
    session: ControllerSession,
    parentSignal?: AbortSignal,
  ): Promise<void> => {
    const epoch = sessionEpochRef.current;
    const requestSequence = stateRequestSequenceRef.current + 1;
    stateRequestSequenceRef.current = requestSequence;
    const controller = new AbortController();
    const abortRequest = (): void => controller.abort();
    if (parentSignal?.aborted === true) return;
    parentSignal?.addEventListener("abort", abortRequest, { once: true });
    stateRequestControllersRef.current.add(controller);
    const isCurrentSession = (): boolean => sessionEpochRef.current === epoch
      && sessionRef.current?.sessionId === session.sessionId
      && sessionRef.current.controllerInstanceId === session.controllerInstanceId;
    try {
      const next = await getControllerState(controller.signal);
      if (!controller.signal.aborted && isCurrentSession()
        && requestSequence >= appliedStateRequestSequenceRef.current) {
        appliedStateRequestSequenceRef.current = requestSequence;
        await applyControllerState(session, next);
      }
    } catch (error: unknown) {
      if (isAbortError(error) || controller.signal.aborted || !isCurrentSession()) return;
      await becomeUnavailable(asError(error));
      throw error;
    } finally {
      stateRequestControllersRef.current.delete(controller);
      parentSignal?.removeEventListener("abort", abortRequest);
    }
  }, [applyControllerState, becomeUnavailable]);

  const refresh = useCallback(async (): Promise<void> => {
    setActionError(undefined);
    let session: ControllerSession;
    try {
      session = await establishSession();
    } catch (error: unknown) {
      await becomeUnavailable(asError(error));
      return;
    }
    try { await loadControllerState(session); } catch { /* fail-closed state is already visible */ }
  }, [becomeUnavailable, establishSession, loadControllerState]);

  const pollingSession = state.status === "ready" ? state.session : undefined;

  useEffect(() => {
    if (state.status !== "loading") return undefined;
    const identity = {};
    bootstrapEffectRef.current = identity;
    void (async () => {
      let session: ControllerSession;
      try {
        session = await establishSession();
      } catch (error: unknown) {
        if (bootstrapEffectRef.current === identity) await becomeUnavailable(asError(error));
        return;
      }
      if (bootstrapEffectRef.current !== identity) return;
      try { await loadControllerState(session); } catch { /* fail-closed state is already visible */ }
    })();
    return () => {
      if (bootstrapEffectRef.current === identity) bootstrapEffectRef.current = undefined;
    };
  }, [becomeUnavailable, establishSession, loadControllerState, state.status]);

  useEffect(() => {
    if (pollingSession === undefined) return undefined;
    const identity = {};
    const pollController = new AbortController();
    pollingEffectRef.current = identity;
    let timer: number | undefined;
    const poll = async (): Promise<void> => {
      try {
        await loadControllerState(pollingSession, pollController.signal);
      } catch {
        return;
      }
      if (pollingEffectRef.current === identity) {
        timer = window.setTimeout(() => void poll(), controllerStateRef.current?.pollAfterMs ?? 1000);
      }
    };
    timer = window.setTimeout(() => void poll(), controllerStateRef.current?.pollAfterMs ?? 1000);
    return () => {
      if (pollingEffectRef.current === identity) pollingEffectRef.current = undefined;
      pollController.abort();
      window.clearTimeout(timer);
    };
  }, [loadControllerState, pollingSession]);

  useEffect(() => () => {
    applySequenceRef.current += 1;
    invalidateStateRequests();
  }, [invalidateStateRequests]);

  const commandOptions = useCallback((signature: string) => {
    const controlState = controllerStateRef.current;
    if (controlState === undefined || sessionRef.current === undefined) {
      throw new Error("控制状态尚未就绪");
    }
    const existing = commandAttemptRef.current;
    const attempt = existing?.signature === signature ? existing : { signature, key: newIdempotencyKey() };
    commandAttemptRef.current = attempt;
    return { stateVersion: controlState.stateVersion, idempotencyKey: attempt.key };
  }, []);

  const finishCommand = useCallback((error?: unknown): void => {
    if (!(error instanceof ControllerApiError) || !error.retryable) commandAttemptRef.current = undefined;
    actionPendingRef.current = false;
    setActionPending(false);
  }, []);

  const beginCommand = useCallback((): boolean => {
    if (actionPendingRef.current) return false;
    actionPendingRef.current = true;
    setActionPending(true);
    setActionError(undefined);
    return true;
  }, []);

  const runWorkspaceSwitch = useCallback(async (input: ControllerWorkspaceSwitchRequest): Promise<void> => {
    if (!beginCommand()) return;
    const signature = JSON.stringify(input);
    try {
      const options = commandOptions(signature);
      suspendedOperationIdRef.current = `pending:${options.idempotencyKey}`;
      await suspendBusinessRuntime();
      const operation = await startControllerWorkspaceSwitch(input, options);
      suspendedOperationIdRef.current = operation.operationId;
      commandAttemptRef.current = undefined;
      const session = sessionRef.current;
      if (session !== undefined) await loadControllerState(session);
      finishCommand();
    } catch (error: unknown) {
      const failure = asError(error);
      setActionError(failure);
      suspendedOperationIdRef.current = undefined;
      finishCommand(error);
      if (error instanceof ControllerApiError && error.status === 401) {
        await becomeUnavailable(error);
        return;
      }
      const session = sessionRef.current;
      if (session !== undefined) {
        try { await loadControllerState(session); } catch { /* fail-closed state is already visible */ }
      }
    }
  }, [becomeUnavailable, beginCommand, commandOptions, finishCommand, loadControllerState, suspendBusinessRuntime]);

  const runRegistryCommand = useCallback(async (
    signature: string,
    command: (options: { stateVersion: number; idempotencyKey: string }) => Promise<unknown>,
  ): Promise<void> => {
    if (!beginCommand()) return;
    try {
      await command(commandOptions(signature));
      commandAttemptRef.current = undefined;
      const session = sessionRef.current;
      if (session !== undefined) await loadControllerState(session);
      finishCommand();
    } catch (error: unknown) {
      const failure = asError(error);
      setActionError(failure);
      finishCommand(error);
      if (error instanceof ControllerApiError && error.status === 401) {
        await becomeUnavailable(error);
        return;
      }
      const session = sessionRef.current;
      if (session !== undefined && error instanceof ControllerApiError && (error.status === 409 || error.status === 412)) {
        try { await loadControllerState(session); } catch { /* fail-closed state is already visible */ }
      }
    }
  }, [becomeUnavailable, beginCommand, commandOptions, finishCommand, loadControllerState]);

  const switchToRegisteredWorkspace = useCallback(async (workspaceId: string): Promise<void> => {
    await runWorkspaceSwitch({ targetKind: "registered", workspaceId });
  }, [runWorkspaceSwitch]);

  const createAndSwitchWorkspace = useCallback(async (input: { name: string; rootPath: string; initializeGit: boolean }): Promise<void> => {
    await runWorkspaceSwitch({ targetKind: "new", ...input });
  }, [runWorkspaceSwitch]);

  const checkWorkspaceAvailability = useCallback(async (workspaceId: string): Promise<void> => {
    await runRegistryCommand(`availability:${workspaceId}`, async (options) => checkControllerWorkspaceAvailability(workspaceId, options));
  }, [runRegistryCommand]);

  const removeWorkspace = useCallback(async (workspaceId: string): Promise<void> => {
    await runRegistryCommand(`remove:${workspaceId}`, async (options) => removeControllerWorkspace(workspaceId, options));
  }, [runRegistryCommand]);

  const value = useMemo<HostControlContextValue>(() => ({
    state,
    effectiveWorkspaceId,
    actionPending,
    ...(actionError === undefined ? {} : { actionError }),
    refresh,
    switchToRegisteredWorkspace,
    createAndSwitchWorkspace,
    checkWorkspaceAvailability,
    removeWorkspace,
  }), [
    actionError,
    actionPending,
    checkWorkspaceAvailability,
    createAndSwitchWorkspace,
    effectiveWorkspaceId,
    refresh,
    removeWorkspace,
    state,
    switchToRegisteredWorkspace,
  ]);

  return <HostControlContext.Provider value={value}>{children}</HostControlContext.Provider>;
};

export const useHostControl = (): HostControlContextValue => {
  const value = useContext(HostControlContext);
  if (value === undefined) throw new Error("useHostControl must be used within HostControlProvider");
  return value;
};
