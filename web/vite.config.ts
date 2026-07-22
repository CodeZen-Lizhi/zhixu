import react from "@vitejs/plugin-react";
import { defineConfig } from "vitest/config";

const apiProxyTarget = process.env.VITE_API_PROXY_TARGET?.trim();

export default defineConfig({
  plugins: [react()],
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
