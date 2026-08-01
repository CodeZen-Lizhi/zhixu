export type RuntimeAccessInvalidationMode = "refresh" | "suspend";

const listeners = new Set<(mode: RuntimeAccessInvalidationMode) => void | Promise<void>>();

/** 业务代理明确撤销 runtime 时，REST 与 SSE 共用此信号立即停止旧业务树。 */
export const notifyRuntimeAccessInvalidation = (): void => {
  for (const listener of [...listeners]) void listener("refresh");
};

/** 控制命令提交前等待旧 Workspace 的 Query/SSE 作用域完成撤销。 */
export const suspendRuntimeAccess = async (): Promise<void> => {
  await Promise.all([...listeners].map(async (listener) => listener("suspend")));
};

export const subscribeRuntimeAccessInvalidation = (
  listener: (mode: RuntimeAccessInvalidationMode) => void | Promise<void>,
): (() => void) => {
  listeners.add(listener);
  return () => listeners.delete(listener);
};
