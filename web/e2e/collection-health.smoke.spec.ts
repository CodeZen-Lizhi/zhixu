import { expect, test, type Page } from "@playwright/test";

const uuidPattern = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;

const requiredUuid = (name: string): string => {
  const value = process.env[name]?.trim();
  if (value === undefined || !uuidPattern.test(value)) {
    throw new Error(`${name} must be a canonical UUID`);
  }
  return value;
};

const requiredUrl = (name: string): string => {
  const value = process.env[name]?.trim();
  if (value === undefined || value === "") throw new Error(`${name} is required`);
  const parsed = new URL(value);
  if (parsed.protocol !== "http:" || !["127.0.0.1", "localhost", "[::1]"].includes(parsed.hostname)) {
    throw new Error(`${name} must be a loopback HTTP URL`);
  }
  return value.replace(/\/$/, "");
};

const apiBaseUrl = requiredUrl("ZHIXU_COLLECTION_HEALTH_SMOKE_API_URL");
const appBaseUrl = requiredUrl("ZHIXU_COLLECTION_HEALTH_SMOKE_BASE_URL");
const fixture = {
  workspaceId: requiredUuid("ZHIXU_COLLECTION_HEALTH_SMOKE_WORKSPACE_ID"),
  collectionId: requiredUuid("ZHIXU_COLLECTION_HEALTH_SMOKE_COLLECTION_ID"),
  initialScanId: requiredUuid("ZHIXU_COLLECTION_HEALTH_SMOKE_SCAN_ID"),
};

const captureRuntimeIssues = (page: Page, label: string, issues: string[]): void => {
  page.on("console", (message) => {
    if (message.type() === "warning" || message.type() === "error") {
      issues.push(`${label} console.${message.type()}: ${message.text()}`);
    }
  });
  page.on("pageerror", (error) => {
    issues.push(`${label} pageerror: ${error.message}`);
  });
};

const assertNoHorizontalOverflow = async (page: Page, selector: string): Promise<void> => {
  const widths = await page.evaluate((rootSelector) => {
    const root = document.querySelector<HTMLElement>(rootSelector);
    return {
      viewport: window.innerWidth,
      document: Math.max(document.documentElement.scrollWidth, document.body.scrollWidth),
      root: root?.scrollWidth ?? 0,
    };
  }, selector);
  expect(widths.document).toBeLessThanOrEqual(widths.viewport + 1);
  expect(widths.root).toBeLessThanOrEqual(widths.viewport + 1);
};

const apiGet = async (page: Page, path: string): Promise<unknown> => {
  const response = await page.request.get(`${apiBaseUrl}${path}`, { headers: { Accept: "application/json" } });
  expect(response.status()).toBe(200);
  return response.json();
};

const isIssueItem = (value: unknown): value is { id: string } => {
  if (typeof value !== "object" || value === null || !("id" in value)) return false;
  const id = value.id;
  return typeof id === "string" && uuidPattern.test(id);
};

const waitForScanTerminal = async (page: Page, scanId: string): Promise<string> => {
  return expect.poll(async () => {
    const body = await apiGet(page, `/api/v1/health/scans/${scanId}?workspace_id=${fixture.workspaceId}`);
    if (typeof body !== "object" || body === null || !("status" in body) || typeof body.status !== "string") {
      throw new Error("scan response status is missing");
    }
    return body.status;
  }, { timeout: 60_000, intervals: [1_000] }).toMatch(/^(SUCCEEDED|PARTIAL)$/).then(() => scanId);
};

const firstIssueId = async (page: Page): Promise<string> => {
  const body = await apiGet(page, `/api/v1/health/issues?workspace_id=${fixture.workspaceId}&limit=25`);
  if (typeof body !== "object" || body === null || !("items" in body) || !Array.isArray(body.items)) {
    throw new Error("issue list response is invalid");
  }
  const issue = body.items.find(isIssueItem);
  if (issue === undefined) throw new Error("health issue list is empty");
  return issue.id;
};

const assertCollectionDetailViews = async (page: Page): Promise<void> => {
  await page.goto(`/collections/${fixture.collectionId}`);
  await expect(page.getByRole("heading", { name: "Collection Health Browser Smoke" })).toBeVisible();
  await expect(page.getByRole("heading", { name: /个结果$/ })).toBeVisible();
  await expect(page.locator(".collection-items article").first()).toBeVisible();
  await page.getByRole("button", { name: "TABLE" }).click();
  await expect(page.getByRole("table", { name: "Collection 表格视图" })).toBeVisible();
  await page.getByRole("button", { name: "COMPACT_CARD" }).click();
  await expect(page.locator(".collection-card-grid article").first()).toBeVisible();
  await page.getByRole("button", { name: "LIST" }).click();
  await expect(page.locator(".collection-items article").first()).toBeVisible();
  await assertNoHorizontalOverflow(page, ".workbench__content");
};

const startCollectionHealthScan = async (page: Page): Promise<string> => {
  await page.getByRole("button", { name: "启动 Health Scan" }).click();
  await expect.poll(() => new URL(page.url()).pathname).toBe("/health");
  await expect.poll(() => new URL(page.url()).searchParams.get("scan")).toMatch(uuidPattern);
  const scanId = new URL(page.url()).searchParams.get("scan");
  if (scanId === null) throw new Error("Collection Health scan ID was not persisted in URL");
  await waitForScanTerminal(page, scanId);
  return scanId;
};

const assertHealthIssueEvidenceAndDecision = async (page: Page): Promise<void> => {
  await page.goto(`/health?scan=${fixture.initialScanId}`);
  await expect(page.getByRole("heading", { name: "持续发现知识质量问题。" })).toBeVisible();
  await expect(page.getByText(/Scan (SUCCEEDED|PARTIAL)/)).toBeVisible({ timeout: 60_000 });
  await firstIssueId(page);
  const trigger = page.getByRole("button", { name: "Evidence" }).first();
  await trigger.focus();
  await expect(trigger).toBeFocused();
  await trigger.click();
  const dialog = page.getByRole("dialog", { name: "Issue Evidence" });
  await expect(dialog).toBeVisible();
  await expect(dialog.locator(".health-evidence-list article").first()).toBeVisible();
  await page.getByRole("button", { name: "确认" }).click();
  await expect(dialog.getByText("ACKNOWLEDGED")).toBeVisible({ timeout: 15_000 });
  await page.keyboard.press("Escape");
  await expect(dialog).toBeHidden();
  await expect(trigger).toBeFocused();
  await assertNoHorizontalOverflow(page, ".workbench__content");
};

test("Collection 与 Knowledge Health 在真实 API/Worker/Vite 下完成桌面和移动 smoke", async ({ browser, page }) => {
  const runtimeIssues: string[] = [];
  captureRuntimeIssues(page, "desktop", runtimeIssues);

  await page.goto("/collections");
  expect(page.viewportSize()).toEqual({ width: 1440, height: 900 });
  await expect(page.getByRole("heading", { name: "把知识组织成可复用的查询。" })).toBeVisible();
  await expect(page.getByRole("heading", { name: /个匹配对象$/ })).toBeVisible();
  await expect(page.getByRole("link", { name: /Collection Health Browser Smoke/ })).toBeVisible();
  await assertNoHorizontalOverflow(page, ".workbench__content");

  await assertCollectionDetailViews(page);
  const collectionScanId = await startCollectionHealthScan(page);
  await expect(page.getByText(/Scan (SUCCEEDED|PARTIAL)/)).toBeVisible({ timeout: 60_000 });
  await waitForScanTerminal(page, collectionScanId);
  await assertHealthIssueEvidenceAndDecision(page);

  const mobileContext = await browser.newContext({
    baseURL: appBaseUrl,
    viewport: { width: 390, height: 844 },
  });
  const mobilePage = await mobileContext.newPage();
  captureRuntimeIssues(mobilePage, "mobile", runtimeIssues);
  try {
    await mobilePage.goto(`/collections/${fixture.collectionId}?view=COMPACT_CARD`);
    expect(mobilePage.viewportSize()).toEqual({ width: 390, height: 844 });
    await expect(mobilePage.getByRole("heading", { name: "Collection Health Browser Smoke" })).toBeVisible();
    await expect(mobilePage.locator(".collection-card-grid article").first()).toBeVisible();
    await assertNoHorizontalOverflow(mobilePage, ".workbench__content");
    await mobilePage.goto(`/health?scan=${fixture.initialScanId}`);
    await expect(mobilePage.getByRole("heading", { name: "持续发现知识质量问题。" })).toBeVisible();
    await expect(mobilePage.getByText(/Scan (SUCCEEDED|PARTIAL)/)).toBeVisible();
    await assertNoHorizontalOverflow(mobilePage, ".workbench__content");
  } finally {
    await mobileContext.close();
  }

  expect(runtimeIssues, runtimeIssues.join("\n")).toEqual([]);
});
