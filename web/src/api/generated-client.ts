import {
  Configuration,
  FetchError,
  ResponseError,
  type ApiResponse,
} from "./generated/runtime";
import { isAbortError } from "../shared/codec";
import { getCsrfToken } from "./auth-session-state";
import { apiBaseUrl, transportFetch } from "./transport";

export const generatedConfiguration = new Configuration({
  basePath: apiBaseUrl,
  fetchApi: transportFetch,
});

export interface GeneratedBrowserSecurity {
  origin: string;
  xCSRFToken: string;
}

/**
 * OpenAPI 将 Origin/CSRF 建模为必填参数；这里提供当前页面 Origin 以满足生成方法签名。
 * 实际线路 Origin 仍由浏览器控制；空 CSRF 会由 Transport 删除并保持旧的“缺失 header”行为。
 */
export const generatedBrowserSecurity = (): GeneratedBrowserSecurity => ({
  origin: typeof window === "undefined" ? "http://localhost" : window.location.origin,
  xCSRFToken: getCsrfToken() ?? "",
});

export const generatedRequestInit = (
  signal?: AbortSignal,
  init: RequestInit = {},
): RequestInit => signal === undefined ? init : { ...init, signal };

/**
 * 生成 Raw 方法对非 2xx 抛出 ResponseError；项目边界需要读取该响应并映射稳定 Problem。
 */
export const generatedRawResponse = async <T>(operation: Promise<ApiResponse<T>>): Promise<Response> => {
  try {
    return (await operation).raw;
  } catch (error: unknown) {
    if (error instanceof ResponseError) return error.response;
    if (error instanceof FetchError) {
      if (isAbortError(error.cause)) throw error.cause;
      throw error.cause;
    }
    throw error;
  }
};
