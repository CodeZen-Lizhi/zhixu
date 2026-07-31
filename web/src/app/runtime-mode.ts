export type RuntimeMode = "controller" | "direct";

const readRuntimeMode = (value: string | undefined): RuntimeMode => {
  if (value === "controller" || value === "direct") return value;
  throw new Error("VITE_RUNTIME_MODE 必须显式设置为 controller 或 direct");
};

export const runtimeMode = readRuntimeMode(import.meta.env.VITE_RUNTIME_MODE);
