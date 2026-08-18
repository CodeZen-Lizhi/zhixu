import { getCsrfToken, invalidateAuthSession } from "./auth-session-state";

export const apiBaseUrl = (import.meta.env.VITE_API_BASE_URL ?? "").replace(/\/$/, "");

const unsafeMethod = (method: string): boolean => !["GET", "HEAD", "OPTIONS"].includes(method.toUpperCase());

const resolveApiInput = (input: RequestInfo | URL): RequestInfo | URL => {
  if (typeof input !== "string" || !input.startsWith("/")) return input;
  return `${apiBaseUrl}${input}`;
};

const requestPathname = (input: RequestInfo | URL): string => {
  const rawUrl = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
  const fallbackOrigin = typeof window === "undefined" ? "http://localhost" : window.location.origin;
  return new URL(rawUrl, fallbackOrigin).pathname;
};

const mergeRequestHeaders = (input: RequestInfo | URL, init: RequestInit): Headers => {
  const headers = new Headers(input instanceof Request ? input.headers : undefined);
  new Headers(init.headers).forEach((value, name) => headers.set(name, value));
  return headers;
};

const authenticatedFetch = async (input: RequestInfo | URL, init: RequestInit): Promise<Response> => {
  const headers = mergeRequestHeaders(input, init);
  if (headers.get("X-CSRF-Token") === "") headers.delete("X-CSRF-Token");
  if (!headers.has("Accept")) headers.set("Accept", "application/json");
  const multipartBody = typeof FormData !== "undefined" && init.body instanceof FormData;
  if (init.body !== undefined && !multipartBody && !headers.has("Content-Type")) {
    headers.set("Content-Type", "application/json");
  }
  const method = (init.method ?? (input instanceof Request ? input.method : "GET")).toUpperCase();
  const pathname = requestPathname(input);
  const sessionExchange = pathname.endsWith("/api/v1/auth/sessions");
  if (unsafeMethod(method) && !headers.has("Authorization") && !sessionExchange) {
    const csrf = getCsrfToken();
    if (csrf !== undefined) headers.set("X-CSRF-Token", csrf);
  }
  const response = await fetch(input, { ...init, headers, credentials: "include" });
  if (response.status === 401 && !sessionExchange) {
    invalidateAuthSession();
  }
  return response;
};

/** 接收生成 runtime 已经拼接 basePath 的 URL，不再重复添加 API base URL。 */
export const transportFetch = (input: RequestInfo | URL, init: RequestInit = {}): Promise<Response> =>
  authenticatedFetch(input, init);

/** 兼容现有专用 Adapter 的相对路径调用，并复用生成客户端的认证执行逻辑。 */
export const authFetch = (input: RequestInfo | URL, init: RequestInit = {}): Promise<Response> =>
  authenticatedFetch(resolveApiInput(input), init);
