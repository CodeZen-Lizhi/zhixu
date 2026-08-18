export class AuthApiError extends Error {
  readonly code: string;
  readonly status: number | null;
  readonly retryable: boolean;

  constructor(code: string, message: string, status: number | null, retryable: boolean, options?: ErrorOptions) {
    super(message, options);
    this.name = "AuthApiError";
    this.code = code;
    this.status = status;
    this.retryable = retryable;
  }
}

export type AuthInvalidationReason =
  | { kind: "unauthorized" }
  | { kind: "storage_unavailable"; error: AuthApiError };

const csrfStorageKey = "zhixu.csrf-token";
const authInvalidationListeners = new Set<(reason: AuthInvalidationReason) => void>();

/** 订阅服务端返回 401 后的全局认证失效通知。 */
export const subscribeAuthInvalidation = (listener: (reason: AuthInvalidationReason) => void): (() => void) => {
  authInvalidationListeners.add(listener);
  return () => authInvalidationListeners.delete(listener);
};

const notifyAuthInvalidation = (reason: AuthInvalidationReason): void => {
  for (const listener of [...authInvalidationListeners]) listener(reason);
};

const storageFailure = (operation: "read" | "write" | "remove", cause: unknown): never => {
  const message = operation === "read"
    ? "浏览器无法读取 Session 恢复状态，请允许本站存储后重新登录。"
    : operation === "write"
      ? "浏览器无法保存 Session 恢复状态，请允许本站存储后重新登录。"
      : "浏览器无法清理 Session 恢复状态，请允许本站存储后重新登录。";
  const error = new AuthApiError("AUTH_STORAGE_UNAVAILABLE", message, null, false, { cause });
  notifyAuthInvalidation({ kind: "storage_unavailable", error });
  throw error;
};

const readStorage = (operation: "read" | "write" | "remove"): Storage | undefined => {
  if (typeof window === "undefined") return undefined;
  try {
    return window.localStorage;
  } catch (error: unknown) {
    return storageFailure(operation, error);
  }
};

export const getCsrfToken = (): string | undefined => {
  let value: string | null | undefined;
  try {
    value = readStorage("read")?.getItem(csrfStorageKey);
  } catch (error: unknown) {
    if (error instanceof AuthApiError && error.code === "AUTH_STORAGE_UNAVAILABLE") throw error;
    return storageFailure("read", error);
  }
  return value === undefined || value === null || value === "" ? undefined : value;
};

export const setCsrfToken = (value: string): void => {
  try {
    readStorage("write")?.setItem(csrfStorageKey, value);
  } catch (error: unknown) {
    if (error instanceof AuthApiError && error.code === "AUTH_STORAGE_UNAVAILABLE") throw error;
    storageFailure("write", error);
  }
};

export const clearCsrfToken = (): void => {
  try {
    readStorage("remove")?.removeItem(csrfStorageKey);
  } catch (error: unknown) {
    if (error instanceof AuthApiError && error.code === "AUTH_STORAGE_UNAVAILABLE") throw error;
    storageFailure("remove", error);
  }
};

/** 将 REST 与 SSE 的 401 收敛到同一个本地认证失效入口。 */
export const invalidateAuthSession = (): void => {
  clearCsrfToken();
  notifyAuthInvalidation({ kind: "unauthorized" });
};

/** 订阅其他同源 Tab 的 CSRF 恢复状态变化。 */
export const subscribeCsrfTokenChanges = (listener: () => void): (() => void) => {
  if (typeof window === "undefined") return () => undefined;
  const handleStorage = (event: StorageEvent): void => {
    if (event.key !== csrfStorageKey) return;
    try {
      const storage = readStorage("read");
      if (event.storageArea !== null && event.storageArea !== storage) return;
    } catch {
      return;
    }
    listener();
  };
  window.addEventListener("storage", handleStorage);
  return () => window.removeEventListener("storage", handleStorage);
};
