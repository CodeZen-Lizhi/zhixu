import { defineConfig } from "@playwright/test";

const readOptionalEnvironment = (name: string): string | undefined => {
  const value = process.env[name]?.trim();
  return value === undefined || value === "" ? undefined : value;
};

const baseURL = readOptionalEnvironment("ZHIXU_PLAYWRIGHT_BASE_URL")
  ?? readOptionalEnvironment("ZHIXU_SEMANTIC_LINK_SMOKE_BASE_URL")
  ?? readOptionalEnvironment("ZHIXU_EXPORT_SMOKE_BASE_URL")
  ?? readOptionalEnvironment("ZHIXU_COLLECTION_HEALTH_SMOKE_BASE_URL");
if (baseURL === undefined) {
  throw new Error("ZHIXU_PLAYWRIGHT_BASE_URL is required");
}

const executablePath = readOptionalEnvironment("ZHIXU_PLAYWRIGHT_EXECUTABLE_PATH");
const outputDir = readOptionalEnvironment("ZHIXU_PLAYWRIGHT_OUTPUT_DIR") ?? "test-results/browser-smoke";

export default defineConfig({
  testDir: "./e2e",
  outputDir,
  fullyParallel: false,
  workers: 1,
  retries: 0,
  forbidOnly: true,
  timeout: 90_000,
  expect: { timeout: 10_000 },
  reporter: [["line"]],
  use: {
    baseURL,
    browserName: "chromium",
    viewport: { width: 1440, height: 900 },
    screenshot: "only-on-failure",
    trace: "retain-on-failure",
    ...(executablePath === undefined
      ? { channel: "chrome" }
      : { launchOptions: { executablePath } }),
  },
});
