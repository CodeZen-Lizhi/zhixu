import { expect, test, type BrowserContext, type Page } from "@playwright/test";

const csrfStorageKey = "zhixu.csrf-token";
const uuid = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;
const appBaseURL = process.env.ZHIXU_PLAYWRIGHT_BASE_URL?.trim() ?? (() => { throw new Error("ZHIXU_PLAYWRIGHT_BASE_URL is required"); })();
const sessionToken = process.env.ZHIXU_M8_LEARNING_SMOKE_SESSION_TOKEN?.trim() ?? (() => { throw new Error("ZHIXU_M8_LEARNING_SMOKE_SESSION_TOKEN is required"); })();
const csrfToken = process.env.ZHIXU_M8_LEARNING_SMOKE_CSRF_TOKEN?.trim() ?? (() => { throw new Error("ZHIXU_M8_LEARNING_SMOKE_CSRF_TOKEN is required"); })();

const requiredUUID = (name: string): string => {
  const value = process.env[name]?.trim();
  if (value === undefined || !uuid.test(value)) throw new Error(`${name} must be a canonical UUID`);
  return value;
};

const fixture = {
  workspaceId: requiredUUID("ZHIXU_M8_LEARNING_SMOKE_WORKSPACE_ID"),
  deckId: requiredUUID("ZHIXU_M8_LEARNING_SMOKE_DECK_ID"),
  reviewSessionId: requiredUUID("ZHIXU_M8_LEARNING_SMOKE_REVIEW_SESSION_ID"),
  mobileReviewSessionId: requiredUUID("ZHIXU_M8_LEARNING_SMOKE_MOBILE_REVIEW_SESSION_ID"),
  interviewSessionId: requiredUUID("ZHIXU_M8_LEARNING_SMOKE_INTERVIEW_SESSION_ID"),
  pathId: requiredUUID("ZHIXU_M8_LEARNING_SMOKE_PATH_ID"),
};

const installCsrf = (context: BrowserContext): void => {
  void context.addInitScript(({ csrfKey, csrf }) => {
    window.localStorage.setItem(csrfKey, csrf);
  }, { csrfKey: csrfStorageKey, csrf: csrfToken });
};

const installSession = async (context: BrowserContext): Promise<void> => {
  await context.addCookies([{ name: "zhixu_session", value: sessionToken, url: appBaseURL, httpOnly: true, sameSite: "Strict" }]);
};

const captureRuntimeIssues = (page: Page, target: string, issues: string[]): void => {
  const availableReviewPaths = new Set<string>();
  page.on("console", (message) => {
    const genericResourceFailure = /^Failed to load resource: the server responded with a status of \d+ \([^)]+\)$/;
    if (message.type() === "warning" || (message.type() === "error" && !genericResourceFailure.test(message.text()))) {
      issues.push(`${target}.console.${message.type()}: ${message.text()}`);
    }
  });
  page.on("pageerror", (error) => issues.push(`${target}.pageerror: ${error.message}`));
  page.on("response", (response) => {
    const request = response.request();
    const url = new URL(response.url());
    const isReviewPath =
      /^\/api\/v1\/review\/answers\/[0-9a-f-]{36}\/learning-path$/.test(url.pathname);
    if (isReviewPath && response.status() < 400) {
      availableReviewPaths.add(url.pathname);
      return;
    }
    if (response.status() < 400) return;
    const expectedMissingReviewPath =
      isReviewPath &&
      !availableReviewPaths.has(url.pathname) &&
      request.method() === "GET" &&
      response.status() === 404;
    if (expectedMissingReviewPath) return;
    issues.push(`${target}.http: ${request.method()} ${String(response.status())} ${url.pathname}`);
  });
};

const assertNoHorizontalOverflow = async (page: Page): Promise<void> => {
  const widths = await page.evaluate(() => ({ viewport: window.innerWidth, document: Math.max(document.documentElement.scrollWidth, document.body.scrollWidth), workbench: document.querySelector<HTMLElement>(".workbench__content")?.scrollWidth ?? 0 }));
  expect(widths.document).toBeLessThanOrEqual(widths.viewport + 1);
  expect(widths.workbench).toBeLessThanOrEqual(widths.viewport + 1);
};

const assertPages = async (page: Page, reviewSessionId: string, completeBrowserCommands: boolean): Promise<void> => {
  await page.goto("/review");
  await expect(page.getByRole("heading", { name: "从正式知识开始，完成今天的主动回忆。" })).toBeVisible();
  await expect(page.getByText("M8 Learning Smoke", { exact: true })).toBeVisible();
  await assertNoHorizontalOverflow(page);

  await page.goto(`/review/session?deck=${fixture.deckId}&session=${reviewSessionId}`);
  await expect(page.getByRole("heading", { name: "今日复习" })).toBeVisible();
  if (completeBrowserCommands) {
    await expect(page.getByRole("heading", { name: "回忆问题", level: 2 })).toBeVisible();
    await page.getByLabel("你的回答").fill("bounded browser learner response");
    await page.getByRole("button", { name: "提交回答" }).click();
    await expect(page.getByRole("heading", { name: "本题结果", level: 2 })).toBeVisible();
    await expect(page.getByRole("heading", { name: "评分已写入下一次调度", level: 3 })).toBeVisible();
    await page.getByRole("button", { name: "打开片段" }).click();
    await expect(page.getByRole("heading", { name: "不可变来源片段", level: 2 })).toBeVisible();
    await page.getByRole("button", { name: "关闭来源片段" }).click();
    await page.getByRole("button", { name: "创建学习路径" }).click();
    await expect(page.getByRole("heading", { name: "评分缺口学习路径", level: 3 })).toBeVisible();
    await page.getByRole("button", { name: "结束本轮" }).click();
    await expect(page).toHaveURL(/\/review$/);
  } else {
    await expect(page.getByRole("heading", { name: "今天没有待复习题", level: 2 })).toBeVisible();
  }
  await assertNoHorizontalOverflow(page);

  await page.goto("/memories");
  await expect(page.getByRole("heading", { name: "把长期上下文留在你的确认之后。" })).toBeVisible();
  const preference = page.locator("article.collection-definition-row").filter({ hasText: '"mode": "concise"' });
  if (completeBrowserCommands) {
    await expect(preference.locator("span.ui-badge").filter({ hasText: /^CANDIDATE$/ })).toBeVisible();
    await preference.getByRole("button", { name: "确认" }).click();
  }
  await expect(preference.locator("span.ui-badge").filter({ hasText: /^ACTIVE$/ })).toBeVisible();
  await expect(page.locator("span.ui-badge").filter({ hasText: /^ACTIVE$/ })).toBeVisible();
  await expect(page.locator("span.ui-badge").filter({ hasText: /^EXPIRED$/ })).toBeVisible();
  await assertNoHorizontalOverflow(page);

  await page.goto("/interviews");
  await expect(page.getByRole("heading", { name: "模拟面试" })).toBeVisible();
  await expect(page.getByRole("heading", { name: "恢复面试会话" })).toBeVisible();
  await expect(page.getByRole("heading", { name: "最近会话" })).toBeVisible();
  await expect(page.getByText("M8 smoke role", { exact: true })).toBeVisible();
  await expect(page.locator(`a[href="/interviews/${fixture.interviewSessionId}"]`)).toBeVisible();
  await assertNoHorizontalOverflow(page);

  await page.goto(`/interviews/${fixture.interviewSessionId}`);
  await expect(page.getByRole("heading", { name: "模拟面试" })).toBeVisible();
  await expect(page.locator("span.ui-badge").filter({ hasText: /^COMPLETED$/ })).toBeVisible();
  await expect(page.getByRole("heading", { name: "面试报告" })).toBeVisible();
  await expect(page.getByRole("heading", { name: "学习路径" })).toBeVisible();
  await expect(page.getByText("学习中", { exact: true })).toBeVisible();
  await expect(page.locator(`a[href*="${fixture.pathId}"]`)).toHaveCount(0);
  await assertNoHorizontalOverflow(page);
};

test("M8 Learning 在真实 API/Worker/Vite 下完成桌面与 390x844 浏览器 smoke", async ({ browser, page }) => {
  const issues: string[] = [];
  installCsrf(page.context());
  await installSession(page.context());
  captureRuntimeIssues(page, "desktop", issues);
  expect(page.viewportSize()).toEqual({ width: 1440, height: 900 });
  await assertPages(page, fixture.reviewSessionId, true);

  const mobile = await browser.newContext({ baseURL: appBaseURL, viewport: { width: 390, height: 844 } });
  installCsrf(mobile);
  await installSession(mobile);
  const mobilePage = await mobile.newPage();
  captureRuntimeIssues(mobilePage, "mobile", issues);
  try {
    expect(mobilePage.viewportSize()).toEqual({ width: 390, height: 844 });
    await assertPages(mobilePage, fixture.mobileReviewSessionId, false);
  } finally {
    await mobile.close();
  }
  expect(issues, issues.join("\n")).toEqual([]);
});
