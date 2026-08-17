import { expect, test, type BrowserContext, type ConsoleMessage, type Page, type Request, type Response } from "@playwright/test";

const workspaceStorageKey = "zhixu.active-workspace-id";
const csrfStorageKey = "zhixu.csrf-token";
const uuidPattern = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;
const smokeTimeoutMs = 120_000;
const fixtureAnswer = "Approved recovery requires durable replay without duplicate provider work.";

const requiredToken = (name: string): string => {
  const value = process.env[name];
  if (value === undefined || value === "" || value !== value.trim() || value.includes("\0")) {
    throw new Error(`${name} must be a non-empty canonical token`);
  }
  return value;
};

const requiredUuid = (name: string): string => {
  const value = requiredToken(name);
  if (!uuidPattern.test(value)) throw new Error(`${name} must be a canonical UUID`);
  return value;
};

const requiredLoopbackUrl = (name: string): string => {
  const value = requiredToken(name);
  let parsed: URL;
  try {
    parsed = new URL(value);
  } catch {
    throw new Error(`${name} must be a loopback HTTP URL`);
  }
  if (
    parsed.protocol !== "http:" || !["127.0.0.1", "localhost", "[::1]"].includes(parsed.hostname) ||
    parsed.username !== "" || parsed.password !== "" || parsed.search !== "" || parsed.hash !== "" ||
    !["", "/"].includes(parsed.pathname)
  ) {
    throw new Error(`${name} must be a loopback HTTP URL`);
  }
  return value.replace(/\/$/, "");
};

const fixture = {
  baseUrl: requiredLoopbackUrl("ZHIXU_RAG_FIXTURE_SMOKE_BASE_URL"),
  sessionToken: requiredToken("ZHIXU_RAG_FIXTURE_SMOKE_SESSION_TOKEN"),
  csrfToken: requiredToken("ZHIXU_RAG_FIXTURE_SMOKE_CSRF_TOKEN"),
  workspaceId: requiredUuid("ZHIXU_RAG_FIXTURE_SMOKE_WORKSPACE_ID"),
  question: requiredToken("ZHIXU_RAG_FIXTURE_SMOKE_QUESTION"),
};

const installSession = async (context: BrowserContext): Promise<void> => {
  await context.addCookies([{ name: "zhixu_session", value: fixture.sessionToken, url: fixture.baseUrl, httpOnly: true, sameSite: "Strict" }]);
  await context.addInitScript(({ workspaceKey, workspaceId, csrfKey, csrfToken }) => {
    window.localStorage.setItem(workspaceKey, workspaceId);
    window.localStorage.setItem(csrfKey, csrfToken);
  }, {
    workspaceKey: workspaceStorageKey,
    workspaceId: fixture.workspaceId,
    csrfKey: csrfStorageKey,
    csrfToken: fixture.csrfToken,
  });
};

const consoleIssueClass = (message: ConsoleMessage): string => {
  const text = message.text();
  if (text.includes("Each child in a list should have a unique \"key\" prop")) return "react_missing_key";
  if (text.includes("Encountered two children with the same key")) return "react_duplicate_key";
  if (text.startsWith("Warning:")) return "react_warning";
  if (text.includes("An error occurred in the <")) return "react_render_error";
  const httpFailure = /^Failed to load resource: the server responded with a status of ([0-9]{3})/.exec(text);
  if (httpFailure?.[1] !== undefined) return `resource_http_${httpFailure[1]}`;
  const networkFailure = /^Failed to load resource: net::(ERR_[A-Z0-9_]+)$/.exec(text);
  if (networkFailure?.[1] !== undefined) return `resource_${networkFailure[1].toLowerCase()}`;
  return "unknown";
};

const captureRuntimeIssues = (page: Page, target: string, issues: string[]): (() => void) => {
  const captureConsole = (message: ConsoleMessage): void => {
    if (message.type() === "warning" || message.type() === "error") {
      issues.push(`${target}.console.${message.type()}.${consoleIssueClass(message)}`);
    }
  };
  const capturePageError = (): void => {
    issues.push(`${target}.pageerror`);
  };
  const captureResponse = (response: Response): void => {
    if (response.status() >= 400) issues.push(`${target}.http.${String(response.status())}`);
  };
  page.on("console", captureConsole);
  page.on("pageerror", capturePageError);
  page.on("response", captureResponse);
  return () => {
    page.off("console", captureConsole);
    page.off("pageerror", capturePageError);
    page.off("response", captureResponse);
  };
};

const assertNoHorizontalOverflow = async (page: Page): Promise<void> => {
  const widths = await page.evaluate(() => ({
    viewport: window.innerWidth,
    document: Math.max(document.documentElement.scrollWidth, document.body.scrollWidth),
    page: document.querySelector<HTMLElement>(".rag-page")?.scrollWidth ?? 0,
    layout: document.querySelector<HTMLElement>(".rag-layout")?.scrollWidth ?? 0,
  }));
  expect(widths.document).toBeLessThanOrEqual(widths.viewport + 1);
  expect(widths.page).toBeLessThanOrEqual(widths.viewport + 1);
  expect(widths.layout).toBeLessThanOrEqual(widths.viewport + 1);
};

const conversationIdFromPage = (page: Page): string => {
  const conversationId = new URL(page.url()).pathname.split("/").at(-1);
  if (conversationId === undefined || !uuidPattern.test(conversationId)) {
    throw new Error("RAG fixture conversation route did not contain a canonical ID");
  }
  return conversationId;
};

const isQuestionRequest = (request: Request, conversationId: string): boolean => {
  const url = new URL(request.url());
  return request.method() === "POST" && url.pathname === `/api/v1/conversations/${conversationId}/questions`;
};

const assertKeywordRagRequest = (request: Request): void => {
  const payload: unknown = request.postDataJSON();
  if (payload === null || typeof payload !== "object" || Array.isArray(payload)) {
    throw new Error("RAG fixture question request must contain an object");
  }
  const body = payload as Record<string, unknown>;
  const scope = body.scope;
  if (body.workspace_id !== fixture.workspaceId || body.question !== fixture.question || body.mode !== "rag" ||
    scope === null || typeof scope !== "object" || Array.isArray(scope) ||
    (scope as Record<string, unknown>).retrieval_mode !== "keyword") {
    throw new Error("RAG fixture question request did not preserve the exact rag keyword contract");
  }
};

const answerIdFromAcceptance = async (response: Response, conversationId: string): Promise<string> => {
  expect(response.status()).toBe(202);
  const payload: unknown = await response.json();
  if (payload === null || typeof payload !== "object" || Array.isArray(payload)) throw new Error("RAG fixture acceptance must be an object");
  const body = payload as Record<string, unknown>;
  const question = body.question;
  const answer = body.answer;
  if (question === null || typeof question !== "object" || Array.isArray(question) ||
    answer === null || typeof answer !== "object" || Array.isArray(answer)) {
    throw new Error("RAG fixture acceptance omitted canonical resources");
  }
  const acceptedQuestion = question as Record<string, unknown>;
  const acceptedAnswer = answer as Record<string, unknown>;
  const answerId = acceptedAnswer.id;
  if (acceptedQuestion.workspace_id !== fixture.workspaceId || acceptedQuestion.conversation_id !== conversationId ||
    acceptedQuestion.mode !== "rag" || typeof answerId !== "string" || !uuidPattern.test(answerId) ||
    acceptedAnswer.conversation_id !== conversationId) {
    throw new Error("RAG fixture acceptance did not bind its canonical resources");
  }
  return answerId;
};

const isDraftStream = (response: Response, answerId: string): boolean => {
  const url = new URL(response.url());
  return response.request().method() === "GET" && url.pathname === `/api/v1/answers/${answerId}/stream` &&
    url.searchParams.get("workspace_id") === fixture.workspaceId;
};

test("固定 RAG fixture 在真实桌面和移动 UI 中完成 keyword SSE、Answer 与 Citation", async ({ browser }, testInfo) => {
  test.setTimeout(smokeTimeoutMs + 30_000);
  const runtimeIssues: string[] = [];
  const desktopContext = await browser.newContext({ baseURL: fixture.baseUrl, viewport: { width: 1440, height: 900 } });
  await installSession(desktopContext);
  const desktop = await desktopContext.newPage();
  const stopDesktopRuntimeCapture = captureRuntimeIssues(desktop, "desktop", runtimeIssues);

  try {
    expect(desktop.viewportSize()).toEqual({ width: 1440, height: 900 });
    await desktop.goto("/chat");
    await expect(desktop.getByRole("heading", { name: "证据研究台" })).toBeVisible();
    await desktop.getByLabel("新会话标题").fill(`Fixed RAG fixture ${String(Date.now())}`);
    const createResponse = desktop.waitForResponse((response) => {
      const url = new URL(response.url());
      return response.request().method() === "POST" && url.pathname === "/api/v1/conversations";
    });
    await desktop.getByRole("button", { name: "新建会话" }).click();
    expect((await createResponse).status()).toBe(201);
    await desktop.waitForURL((url) => /^\/chat\/[0-9a-f-]{36}$/.test(url.pathname));
    const conversationId = conversationIdFromPage(desktop);
    await expect(desktop.getByRole("heading", { name: "提出第一个问题。" })).toBeVisible();

    const modeControl = desktop.getByRole("radiogroup", { name: "回答模式" });
    await expect(modeControl.getByRole("radio", { name: "证据问答" })).toHaveAttribute("aria-checked", "true");
    await desktop.locator(".rag-scope summary").click();
    await desktop.getByLabel("检索模式").selectOption("keyword");
    await expect(desktop.getByLabel("检索模式")).toHaveValue("keyword");
    await desktop.getByLabel("问题").fill(fixture.question);
    const questionRequest = desktop.waitForRequest((request) => isQuestionRequest(request, conversationId));
    const questionResponse = desktop.waitForResponse((response) => isQuestionRequest(response.request(), conversationId));
    const draftStreamResponse = desktop.waitForResponse((response) => {
      const url = new URL(response.url());
      return response.request().method() === "GET" && /^\/api\/v1\/answers\/[0-9a-f-]{36}\/stream$/.test(url.pathname) &&
        url.searchParams.get("workspace_id") === fixture.workspaceId;
    }, { timeout: smokeTimeoutMs });
    await desktop.getByRole("button", { name: "提交问题" }).click();
    assertKeywordRagRequest(await questionRequest);
    const answerId = await answerIdFromAcceptance(await questionResponse, conversationId);
    const draftStream = await draftStreamResponse;
    expect(isDraftStream(draftStream, answerId)).toBe(true);
    expect(draftStream.status()).toBe(200);
    expect(draftStream.headers()["content-type"]?.toLowerCase()).toContain("text/event-stream");

    const draft = desktop.getByLabel("生成中草稿");
    await expect(draft).toBeVisible({ timeout: smokeTimeoutMs });
    await expect(draft).toContainText("Approved recovery requires durable");
    await expect(draft).toHaveAttribute("data-draft-sequence", /^[1-9][0-9]*$/);
    const published = desktop.getByText("已校验回答", { exact: true });
    await expect(published).toBeVisible({ timeout: smokeTimeoutMs });
    await expect(draft).toHaveCount(0);
    const answer = desktop.locator(".rag-answer").filter({ has: published });
    await expect(answer).toContainText(fixtureAnswer);
    const citation = answer.getByRole("button", { name: "打开段落证据" }).first();
    await expect(citation).toBeVisible();
    await citation.click();
    const evidencePanel = desktop.getByRole("complementary", { name: "引用证据" });
    await expect(evidencePanel.getByText("已选择引用", { exact: true })).toBeVisible();
    const sourceResponse = desktop.waitForResponse((response) => {
      const url = new URL(response.url());
      return response.request().method() === "GET" && url.pathname.startsWith(`/api/v1/workspaces/${fixture.workspaceId}/source-versions/`);
    });
    await evidencePanel.getByRole("button", { name: "打开段落证据" }).click();
    expect((await sourceResponse).status()).toBe(200);
    const sourceDialog = desktop.getByRole("dialog", { name: "来源片段证据" });
    await expect(sourceDialog).toBeVisible();
    await sourceDialog.getByRole("button", { name: "关闭来源片段" }).click();
    await expect(sourceDialog).toBeHidden();
    await assertNoHorizontalOverflow(desktop);
    await desktop.screenshot({ path: testInfo.outputPath("rag-fixture-desktop.png"), fullPage: true });

    const mobileContext = await browser.newContext({ baseURL: fixture.baseUrl, viewport: { width: 390, height: 844 } });
    await installSession(mobileContext);
    const mobile = await mobileContext.newPage();
    const stopMobileRuntimeCapture = captureRuntimeIssues(mobile, "mobile", runtimeIssues);
    try {
      expect(mobile.viewportSize()).toEqual({ width: 390, height: 844 });
      await mobile.goto(`/chat/${conversationId}`);
      await expect(mobile.getByText("已校验回答", { exact: true })).toBeVisible({ timeout: smokeTimeoutMs });
      await expect(mobile.locator(".rag-answer").filter({ has: mobile.getByText("已校验回答", { exact: true }) })).toContainText(fixtureAnswer);
      await expect(mobile.getByRole("button", { name: "打开段落证据" }).first()).toBeVisible();
      await assertNoHorizontalOverflow(mobile);
      await mobile.screenshot({ path: testInfo.outputPath("rag-fixture-mobile.png"), fullPage: true });
    } finally {
      stopMobileRuntimeCapture();
      await mobileContext.close();
    }
  } finally {
    stopDesktopRuntimeCapture();
    await desktopContext.close();
  }

  expect(runtimeIssues, "browser console, page and network errors must remain empty").toEqual([]);
});
