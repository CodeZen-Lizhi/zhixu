import { expect, test, type BrowserContext, type Page } from "@playwright/test";

const workspaceStorageKey = "zhixu.active-workspace-id";
const uuidPattern = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;

const requiredEnvironment = (name: string): string => {
  const value = process.env[name]?.trim();
  if (value === undefined || !uuidPattern.test(value)) {
    throw new Error(`${name} must be a canonical UUID`);
  }
  return value;
};

const baseURL = process.env.ZHIXU_SEMANTIC_LINK_SMOKE_BASE_URL?.trim();
if (baseURL === undefined || baseURL === "") {
  throw new Error("ZHIXU_SEMANTIC_LINK_SMOKE_BASE_URL is required");
}

const fixture = {
  workspaceId: requiredEnvironment("ZHIXU_SEMANTIC_LINK_SMOKE_WORKSPACE_ID"),
  topicId: requiredEnvironment("ZHIXU_SEMANTIC_LINK_SMOKE_TOPIC_ID"),
  firstClaimId: requiredEnvironment("ZHIXU_SEMANTIC_LINK_SMOKE_FIRST_CLAIM_ID"),
  discoveryClaimId: requiredEnvironment("ZHIXU_SEMANTIC_LINK_SMOKE_DISCOVERY_CLAIM_ID"),
};

const graphPath = (): string => {
  const parameters = new URLSearchParams({
    mode: "local",
    center_type: "TOPIC",
    center_id: fixture.topicId,
    depth: "1",
    direction: "BOTH",
  });
  return `/graph?${parameters.toString()}`;
};

const installWorkspace = async (context: BrowserContext): Promise<void> => {
  await context.addInitScript(({ key, workspaceId }) => {
    window.localStorage.setItem(key, workspaceId);
  }, { key: workspaceStorageKey, workspaceId: fixture.workspaceId });
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

const graphCountText = async (page: Page): Promise<string> => {
  const count = page.locator(".graph-toolbar strong");
  await expect(count).toHaveText(/^\d+ nodes · \d+ edges$/);
  const value = await count.textContent();
  if (value === null) throw new Error("formal graph count is missing");
  return value.trim();
};

const assertNoHorizontalOverflow = async (page: Page): Promise<void> => {
  const widths = await page.evaluate(() => {
    const documentWidth = Math.max(document.documentElement.scrollWidth, document.body.scrollWidth);
    const graphPage = document.querySelector<HTMLElement>(".graph-page");
    const candidatePanel = document.querySelector<HTMLElement>(".semantic-link-panel");
    return {
      viewport: window.innerWidth,
      document: documentWidth,
      graphPage: graphPage?.scrollWidth ?? 0,
      candidatePanel: candidatePanel?.scrollWidth ?? 0,
    };
  });
  expect(widths.document).toBeLessThanOrEqual(widths.viewport + 1);
  expect(widths.graphPage).toBeLessThanOrEqual(widths.viewport + 1);
  expect(widths.candidatePanel).toBeLessThanOrEqual(widths.viewport + 1);
};

const assertCandidateIsNotAFormalEdge = async (page: Page): Promise<void> => {
  const response = await page.request.post("/api/v1/graph/path", {
    data: {
      workspace_id: fixture.workspaceId,
      from: { type: "CLAIM", id: fixture.discoveryClaimId },
      to: { type: "CLAIM", id: fixture.firstClaimId },
      direction: "BOTH",
      max_depth: 4,
      max_visited: 20,
    },
  });
  expect(response.status()).toBe(200);
  const body: unknown = await response.json();
  expect(body).toMatchObject({
    workspace_id: fixture.workspaceId,
    status: "found",
    from: { type: "CLAIM", id: fixture.discoveryClaimId },
    to: { type: "CLAIM", id: fixture.firstClaimId },
    hop_count: 2,
  });
};

const assertDecisionKeyboardFlow = async (page: Page): Promise<void> => {
  const trigger = page.getByRole("button", { name: "确认建议关系", exact: true }).first();
  await trigger.focus();
  await expect(trigger).toBeFocused();
  await page.keyboard.press("Enter");
  const dialog = page.getByRole("dialog", { name: "确认建议关系" });
  await expect(dialog).toBeVisible();
  await expect(dialog.locator("[data-autofocus]")).toBeFocused();
  await page.keyboard.press("Escape");
  await expect(dialog).toBeHidden();
  await expect(trigger).toBeFocused();
};

test("真实 Topic scan 在桌面与移动端恢复 Candidate 且不污染正式 Graph", async ({ browser, page }) => {
  const runtimeIssues: string[] = [];
  await installWorkspace(page.context());
  captureRuntimeIssues(page, "desktop", runtimeIssues);

  let scanPosts = 0;
  page.on("request", (request) => {
    if (request.method() === "POST" && new URL(request.url()).pathname === "/api/v1/graph/candidate-scans") {
      scanPosts += 1;
    }
  });

  await page.goto(graphPath());
  await expect(page.getByRole("heading", { name: "知识图谱" })).toBeVisible();
  expect(page.viewportSize()).toEqual({ width: 1440, height: 900 });
  const initialGraphCount = await graphCountText(page);

  await page.getByRole("button", { name: "扫描当前 Topic" }).click();
  await expect.poll(() => new URL(page.url()).searchParams.get("candidate_scan_id")).toMatch(uuidPattern);
  const scanId = new URL(page.url()).searchParams.get("candidate_scan_id");
  if (scanId === null) throw new Error("candidate_scan_id was not persisted in the URL");

  await page.reload();
  await expect(page.getByRole("heading", { name: "知识图谱" })).toBeVisible();
  await page.getByRole("button", { name: "审阅候选" }).click();
  await expect(page.getByText("Topic 扫描 · SUCCEEDED")).toBeVisible({ timeout: 60_000 });
  await expect(page.getByText("Candidate · 未进入正式图").first()).toBeVisible();
  expect(scanPosts).toBe(1);
  expect(await graphCountText(page)).toBe(initialGraphCount);
  await assertCandidateIsNotAFormalEdge(page);
  await assertDecisionKeyboardFlow(page);
  await assertNoHorizontalOverflow(page);

  const mobileContext = await browser.newContext({
    baseURL,
    viewport: { width: 390, height: 844 },
  });
  await installWorkspace(mobileContext);
  const mobilePage = await mobileContext.newPage();
  captureRuntimeIssues(mobilePage, "mobile", runtimeIssues);
  let mobileScanPosts = 0;
  mobilePage.on("request", (request) => {
    if (request.method() === "POST" && new URL(request.url()).pathname === "/api/v1/graph/candidate-scans") {
      mobileScanPosts += 1;
    }
  });
  try {
    await mobilePage.goto(`${graphPath()}&candidate_scan_id=${scanId}`);
    await expect(mobilePage.getByRole("heading", { name: "知识图谱" })).toBeVisible();
    expect(mobilePage.viewportSize()).toEqual({ width: 390, height: 844 });
    await mobilePage.getByRole("button", { name: "审阅候选" }).click();
    await expect(mobilePage.getByText("Topic 扫描 · SUCCEEDED")).toBeVisible();
    await expect(mobilePage.getByText("Candidate · 未进入正式图").first()).toBeVisible();
    expect(mobileScanPosts).toBe(0);
    await assertDecisionKeyboardFlow(mobilePage);
    await assertNoHorizontalOverflow(mobilePage);
  } finally {
    await mobileContext.close();
  }

  expect(runtimeIssues, runtimeIssues.join("\n")).toEqual([]);
});
