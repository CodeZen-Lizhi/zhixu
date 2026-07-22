import react from "@vitejs/plugin-react";
import type { Plugin } from "vite";
import { defineConfig } from "vitest/config";

const apiProxyTarget = process.env.VITE_API_PROXY_TARGET?.trim();
const monacoLoaderCdnDefault = "https://cdn.jsdelivr.net/npm/monaco-editor@0.55.1/min/vs";

const stripMonacoLoaderCdnDefault = (): Plugin => ({
  name: "strip-monaco-loader-cdn-default",
  enforce: "pre",
  transform(code, id) {
    if (!id.includes("@monaco-editor/loader/lib/es/config/index.js")) return null;
    return code.replaceAll(monacoLoaderCdnDefault, "/monaco-editor-local-loader-disabled");
  },
});

export default defineConfig({
  plugins: [react(), stripMonacoLoaderCdnDefault()],
  server: {
    host: "127.0.0.1",
    port: 5173,
    ...(apiProxyTarget === undefined || apiProxyTarget === "" ? {} : {
      proxy: {
        "/api": { target: apiProxyTarget, changeOrigin: true },
      },
    }),
  },
  preview: {
    host: "127.0.0.1",
    port: 4173,
  },
  test: {
    include: ["src/**/*.{test,spec}.{ts,tsx}"],
    environment: "jsdom",
    setupFiles: "./src/test/setup.ts",
    clearMocks: true,
  },
});
