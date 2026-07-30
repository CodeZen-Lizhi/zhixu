import { useQueryClient } from "@tanstack/react-query";
import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from "react";

import {
  AuthApiError,
  bootstrapSession,
  clearCsrfToken,
  getCurrentSession,
  getCsrfToken,
  revokeSession,
  subscribeAuthInvalidation,
  subscribeCsrfTokenChanges,
  type AuthInvalidationReason,
  type SessionInfo,
} from "../api/auth";
import { fetchSystemStatus } from "../api/system-status";

type AuthState =
  | { status: "loading"; mode: "unknown" | "disabled" | "required"; session?: undefined; error?: undefined }
  | { status: "anonymous"; mode: "required"; session?: undefined; error?: AuthApiError }
  | { status: "authenticated"; mode: "required" | "disabled"; session?: SessionInfo; error?: undefined }
  | { status: "error"; mode: "unknown" | "required"; session?: undefined; error: Error };

interface AuthContextValue {
  state: AuthState;
  signIn: (bootstrapToken: string) => Promise<void>;
  signOut: () => Promise<void>;
  refresh: () => Promise<void>;
}

const AuthContext = createContext<AuthContextValue | undefined>(undefined);

export const AuthProvider = ({ children }: { children: ReactNode }) => {
  const queryClient = useQueryClient();
  const [state, setState] = useState<AuthState>({ status: "loading", mode: "unknown" });

  const cancelAndClearQueries = useCallback(() => {
    void queryClient.cancelQueries();
    queryClient.clear();
  }, [queryClient]);

  const becomeUnavailable = useCallback((error: Error) => {
    cancelAndClearQueries();
    setState({ status: "error", mode: "required", error });
  }, [cancelAndClearQueries]);

  const becomeAnonymous = useCallback((error?: AuthApiError) => {
    cancelAndClearQueries();
    setState({ status: "anonymous", mode: "required", ...(error === undefined ? {} : { error }) });
  }, [cancelAndClearQueries]);

  const clearAndBecomeAnonymous = useCallback((error?: AuthApiError) => {
    try {
      clearCsrfToken();
    } catch (storageError: unknown) {
      becomeUnavailable(storageError instanceof Error ? storageError : new Error("认证存储不可用"));
      return;
    }
    becomeAnonymous(error);
  }, [becomeAnonymous, becomeUnavailable]);

  const refresh = useCallback(async () => {
    try {
      const system = await fetchSystemStatus();
      if (system.auth.status === "disabled") {
        cancelAndClearQueries();
        setState({ status: "authenticated", mode: "disabled" });
        return;
      }
      if (system.auth.status === "unavailable") {
        becomeUnavailable(new AuthApiError("AUTH_DEPENDENCY_UNAVAILABLE", "认证依赖暂不可用。", null, true));
        return;
      }
      cancelAndClearQueries();
      setState({ status: "loading", mode: "required" });
      if (getCsrfToken() === undefined) {
        becomeAnonymous();
        return;
      }
      setState({ status: "authenticated", mode: "required", session: await getCurrentSession() });
    } catch (error: unknown) {
      if (error instanceof AuthApiError && error.status === 401) {
        becomeAnonymous(error);
        return;
      }
      becomeUnavailable(error instanceof Error ? error : new Error("认证状态不可用"));
    }
  }, [becomeAnonymous, becomeUnavailable, cancelAndClearQueries]);

  useEffect(() => {
    void refresh();
    const unsubscribeInvalidation = subscribeAuthInvalidation((reason: AuthInvalidationReason) => {
      if (reason.kind === "storage_unavailable") becomeUnavailable(reason.error);
      else becomeAnonymous();
    });
    const unsubscribeStorage = subscribeCsrfTokenChanges(() => {
      cancelAndClearQueries();
      setState({ status: "loading", mode: "required" });
      void refresh();
    });
    return () => {
      unsubscribeInvalidation();
      unsubscribeStorage();
    };
  }, [becomeAnonymous, becomeUnavailable, cancelAndClearQueries, refresh]);

  const signIn = useCallback(async (bootstrapToken: string) => {
    cancelAndClearQueries();
    setState({ status: "loading", mode: "required" });
    try {
      await bootstrapSession(bootstrapToken);
      setState({ status: "authenticated", mode: "required", session: await getCurrentSession() });
    } catch (error: unknown) {
      const authError = error instanceof AuthApiError ? error : new AuthApiError("AUTH_UNAVAILABLE", "登录失败。", null, false);
      if (authError.code === "AUTH_STORAGE_UNAVAILABLE") becomeUnavailable(authError);
      else clearAndBecomeAnonymous(authError);
      throw error;
    }
  }, [becomeUnavailable, cancelAndClearQueries, clearAndBecomeAnonymous]);

  const signOut = useCallback(async () => {
    if (state.mode === "disabled") return;
    try {
      await revokeSession();
      becomeAnonymous();
    } catch (error: unknown) {
      if (error instanceof AuthApiError && error.status === 401) {
        becomeAnonymous(error);
        return;
      }
      throw error;
    }
  }, [becomeAnonymous, state.mode]);

  const value = useMemo(() => ({ state, signIn, signOut, refresh }), [refresh, signIn, signOut, state]);
  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
};

export const useAuth = (): AuthContextValue => {
  const value = useContext(AuthContext);
  if (value === undefined) throw new Error("useAuth must be used within AuthProvider");
  return value;
};

const AuthLoading = ({ message, onRetry }: { message: string; onRetry?: () => void }) => <main className="auth-gate"><section className="auth-card" aria-live="polite"><p className="eyebrow">ZHIXU / AUTH</p><h1>正在确认访问边界</h1><p>{message}</p>{onRetry ? <button type="button" onClick={onRetry}>重新检查</button> : null}</section></main>;

const AuthLogin = ({ error, onSubmit, pending }: { error: Error | undefined; onSubmit: (token: string) => Promise<void>; pending: boolean }) => {
  const [token, setToken] = useState("");
  const [submitError, setSubmitError] = useState<Error | undefined>(undefined);
  return <main className="auth-gate"><section className="auth-card"><p className="eyebrow">ZHIXU / AUTH</p><h1>输入 Bootstrap Token</h1><p>当前 API 要求单用户认证。Token 只用于换取 HttpOnly Session，不会显示在页面或持久化到日志。</p><form onSubmit={(event) => { event.preventDefault(); setSubmitError(undefined); void onSubmit(token).catch((value: unknown) => setSubmitError(value instanceof Error ? value : new Error("登录失败"))); }}><label htmlFor="bootstrap-token">Bootstrap Token</label><input id="bootstrap-token" type="password" autoComplete="off" value={token} onChange={(event) => setToken(event.target.value)} disabled={pending} /><button type="submit" disabled={pending || token.trim() === ""}>{pending ? "正在登录…" : "建立 Session"}</button></form>{submitError ? <p className="form-error" role="alert">{submitError.message}</p> : error ? <p className="form-error" role="alert">{error.message}</p> : null}</section></main>;
};

export const AuthBoundary = ({ children }: { children: ReactNode }) => {
  const { state, signIn, refresh } = useAuth();
  if (state.status === "loading") return <AuthLoading message="正在读取系统认证状态…" />;
  if (state.status === "error") return <AuthLoading message={state.error.message} onRetry={() => void refresh()} />;
  if (state.status === "anonymous") return <AuthLogin error={state.error} onSubmit={signIn} pending={false} />;
  return <>{children}</>;
};
