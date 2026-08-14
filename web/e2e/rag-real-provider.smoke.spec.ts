import { expect, test, type BrowserContext, type Locator, type Page, type Response as PlaywrightResponse } from "@playwright/test";
import { realpathSync } from "node:fs";
import { link, realpath, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";

const workspaceStorageKey = "zhixu.active-workspace-id";
const csrfStorageKey = "zhixu.csrf-token";
const uuidPattern = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;
const realProviderTestTimeoutMs = 25 * 60_000;
const realProviderFlowTimeoutMs = 20 * 60_000;
const commandResponseTimeoutMs = 60_000;
const canonicalTemporaryRoot = realpathSync(tmpdir());

const requiredCanonicalToken = (name: string): string => {
  const value = process.env[name];
  if (value === undefined || value === "" || value !== value.trim() || value.includes("\0")) {
    throw new Error(`${name} must be a non-empty canonical token`);
  }
  return value;
};

const requiredSafeAbsoluteOutputFile = (name: string): string => {
  const value = requiredCanonicalToken(name);
  if (!path.isAbsolute(value) || path.normalize(value) !== value) {
    throw new Error(`${name} must be a normalized absolute path`);
  }
  const relativeToTemporaryRoot = path.relative(canonicalTemporaryRoot, value);
  if (relativeToTemporaryRoot === "" || relativeToTemporaryRoot === ".." ||
    relativeToTemporaryRoot.startsWith(`..${path.sep}`) || path.isAbsolute(relativeToTemporaryRoot) ||
    path.dirname(value) === canonicalTemporaryRoot || path.extname(value) !== ".json" ||
    path.basename(value).startsWith(".")) {
    throw new Error(`${name} must be a non-hidden JSON file in a private temporary subdirectory`);
  }
  return value;
};

const requiredLoopbackHttpUrl = (name: string): string => {
  const value = requiredCanonicalToken(name);
  let parsed: URL;
  try {
    parsed = new URL(value);
  } catch {
    throw new Error(`${name} must be a loopback HTTP URL`);
  }
  if (parsed.protocol !== "http:" || !["127.0.0.1", "localhost", "[::1]"].includes(parsed.hostname) ||
    parsed.username !== "" || parsed.password !== "" || parsed.search !== "" || parsed.hash !== "" ||
    !["", "/"].includes(parsed.pathname)) {
    throw new Error(`${name} must be a loopback HTTP URL`);
  }
  return value.replace(/\/$/, "");
};

const requiredUuid = (name: string): string => {
  const value = requiredCanonicalToken(name);
  if (!uuidPattern.test(value)) throw new Error(`${name} must be a canonical UUID`);
  return value;
};

const appBaseUrl = requiredLoopbackHttpUrl("ZHIXU_PLAYWRIGHT_BASE_URL");
const fixture = {
  sessionToken: requiredCanonicalToken("ZHIXU_RAG_REAL_PROVIDER_SESSION_TOKEN"),
  csrfToken: requiredCanonicalToken("ZHIXU_RAG_REAL_PROVIDER_CSRF_TOKEN"),
  workspaceId: requiredUuid("ZHIXU_RAG_REAL_PROVIDER_WORKSPACE_ID"),
  sourceVersionId: requiredUuid("ZHIXU_RAG_REAL_PROVIDER_SOURCE_VERSION_ID"),
  sourceSpanId: requiredUuid("ZHIXU_RAG_REAL_PROVIDER_SOURCE_SPAN_ID"),
  question: requiredCanonicalToken("ZHIXU_RAG_REAL_PROVIDER_QUESTION"),
  evidenceToken: requiredCanonicalToken("ZHIXU_RAG_REAL_PROVIDER_EVIDENCE_TOKEN"),
  outputFile: requiredSafeAbsoluteOutputFile("ZHIXU_RAG_REAL_PROVIDER_OUTPUT_FILE"),
};
if (!fixture.question.includes(fixture.evidenceToken)) {
  throw new Error("ZHIXU_RAG_REAL_PROVIDER_QUESTION must contain ZHIXU_RAG_REAL_PROVIDER_EVIDENCE_TOKEN");
}

interface RealProviderReceipt {
  answer_id: string;
  draft_observed: true;
  desktop_draft_chunk_events: number;
  mobile_draft_chunk_events: number;
  desktop_first_draft_observed_latency_ms: number;
  mobile_first_draft_observed_latency_ms: number;
  completion_latency_ms: number;
  citation_opened: true;
  mobile_verified: true;
}

interface DraftObservation {
  chunkEvents: number;
  firstDraftObservedLatencyMs: number;
}

interface DraftStreamAuditState {
  responses: number;
  byteChunks: number;
  credentialLeak: boolean;
  scanFailed: boolean;
}

interface CredentialFingerprint {
  length: number;
  h1: number;
  h2: number;
}

const rollingFingerprint = (value: string): CredentialFingerprint => {
  const bytes = new TextEncoder().encode(value);
  let h1 = 0;
  let h2 = 0;
  for (const byte of bytes) {
    h1 = (Math.imul(h1, 257) + byte) >>> 0;
    h2 = (Math.imul(h2, 263) + byte) >>> 0;
  }
  return { length: bytes.length, h1, h2 };
};

const writeReceipt = async (receipt: RealProviderReceipt): Promise<void> => {
  const outputDirectory = path.dirname(fixture.outputFile);
  if (await realpath(outputDirectory) !== outputDirectory) {
    throw new Error("ZHIXU_RAG_REAL_PROVIDER_OUTPUT_FILE parent must be a canonical directory");
  }
  const temporary = path.join(
    outputDirectory,
    `.rag-real-provider-result-${String(process.pid)}-${String(Date.now())}.json`,
  );
  try {
    await writeFile(temporary, `${JSON.stringify(receipt)}\n`, { encoding: "utf8", mode: 0o600, flag: "wx" });
    await link(temporary, fixture.outputFile);
  } finally {
    await rm(temporary, { force: true });
  }
};

const installWorkspace = async (context: BrowserContext): Promise<void> => {
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

const installSession = async (context: BrowserContext): Promise<void> => {
  await context.addCookies([{ name: "zhixu_session", value: fixture.sessionToken, url: appBaseUrl, httpOnly: true, sameSite: "Strict" }]);
};

const installDraftStreamAudit = async (context: BrowserContext): Promise<void> => {
  const credentialFingerprints = [fixture.sessionToken, fixture.csrfToken].map(rollingFingerprint);
  const selfTestValue = "zhixu-draft-stream-audit-self-test";
  await context.addInitScript(({ fingerprints, selfTest }) => {
    const auditWindow = window as typeof window & { __zhixuDraftStreamAudit?: DraftStreamAuditState };
    const state: DraftStreamAuditState = { responses: 0, byteChunks: 0, credentialLeak: false, scanFailed: false };
    Object.defineProperty(auditWindow, "__zhixuDraftStreamAudit", {
      configurable: false,
      enumerable: false,
      get: () => ({ ...state }),
    });
    const originalFetch = window.fetch.bind(window);
    const power = (base: number, exponent: number): number => {
      let value = 1;
      for (let index = 0; index < exponent; index += 1) value = Math.imul(value, base) >>> 0;
      return value;
    };
    const createScanners = (sourceFingerprints = fingerprints) => sourceFingerprints.map((fingerprint) => ({
      ...fingerprint,
      count: 0,
      cursor: 0,
      currentH1: 0,
      currentH2: 0,
      power1: power(257, fingerprint.length - 1),
      power2: power(263, fingerprint.length - 1),
      ring: new Uint8Array(fingerprint.length),
    }));
    type Scanner = ReturnType<typeof createScanners>[number];
    const scanByte = (scanners: Scanner[], byte: number): boolean => {
      let matched = false;
      for (const scanner of scanners) {
        if (scanner.count < scanner.length) {
          scanner.ring[scanner.count] = byte;
          scanner.count += 1;
          scanner.currentH1 = (Math.imul(scanner.currentH1, 257) + byte) >>> 0;
          scanner.currentH2 = (Math.imul(scanner.currentH2, 263) + byte) >>> 0;
        } else {
          const outgoing = scanner.ring[scanner.cursor] ?? 0;
          scanner.ring[scanner.cursor] = byte;
          scanner.cursor = (scanner.cursor + 1) % scanner.length;
          scanner.currentH1 = (Math.imul(
            (scanner.currentH1 - Math.imul(outgoing, scanner.power1)) >>> 0,
            257,
          ) + byte) >>> 0;
          scanner.currentH2 = (Math.imul(
            (scanner.currentH2 - Math.imul(outgoing, scanner.power2)) >>> 0,
            263,
          ) + byte) >>> 0;
        }
        if (scanner.count === scanner.length &&
            scanner.currentH1 === scanner.h1 && scanner.currentH2 === scanner.h2) {
          matched = true;
        }
      }
      return matched;
    };
    const selfTestScanners = createScanners([selfTest.fingerprint]);
    const selfTestBytes = new TextEncoder().encode(`noise:${selfTest.value}`);
    let selfTestMatched = false;
    for (const byte of selfTestBytes.subarray(0, 11)) {
      selfTestMatched = scanByte(selfTestScanners, byte) || selfTestMatched;
    }
    for (const byte of selfTestBytes.subarray(11)) {
      selfTestMatched = scanByte(selfTestScanners, byte) || selfTestMatched;
    }
    if (!selfTestMatched) state.scanFailed = true;
    window.fetch = async (input: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
      const response = await originalFetch(input, init);
      const method = input instanceof Request ? input.method : (init?.method ?? "GET");
      const rawURL = input instanceof Request ? input.url : input instanceof URL ? input.href : input;
      const url = new URL(rawURL, window.location.href);
      if (method.toUpperCase() !== "GET" ||
          !/^\/api\/v1\/answers\/[^/]+\/stream$/.test(url.pathname) || response.body === null) {
        return response;
      }
      state.responses += 1;
      const responseScanners = createScanners();
      const scanner = new TransformStream<Uint8Array, Uint8Array>({
        transform(chunk, controller) {
          try {
            state.byteChunks += 1;
            for (const byte of chunk) {
              state.credentialLeak = scanByte(responseScanners, byte) || state.credentialLeak;
            }
            controller.enqueue(chunk);
          } catch (error: unknown) {
            state.scanFailed = true;
            throw error;
          }
        },
      });
      const audited = new Response(response.body.pipeThrough(scanner), {
        headers: response.headers,
        status: response.status,
        statusText: response.statusText,
      });
      Object.defineProperties(audited, {
        redirected: { get: () => response.redirected },
        type: { get: () => response.type },
        url: { get: () => response.url },
      });
      return audited;
    };
  }, {
    fingerprints: credentialFingerprints,
    selfTest: { value: selfTestValue, fingerprint: rollingFingerprint(selfTestValue) },
  });
};

const captureRuntimeIssues = (page: Page, target: string, issues: string[]): void => {
  page.on("console", (message) => {
    if (message.type() === "warning" || message.type() === "error") {
      issues.push(`${target}.console.${message.type()}`);
    }
  });
  page.on("pageerror", () => issues.push(`${target}.pageerror`));
  page.on("response", (response) => {
    if (response.status() < 400) return;
    const request = response.request();
    issues.push(`${target}.http: ${request.method()} ${String(response.status())} ${new URL(response.url()).pathname}`);
  });
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

const conversationPath = (conversationId: string): string => `/chat/${conversationId}`;

const sourceSpanPath = (): string =>
  `/api/v1/workspaces/${fixture.workspaceId}/source-versions/${fixture.sourceVersionId}/spans/${fixture.sourceSpanId}`;

const isQuestionAcceptance = (response: PlaywrightResponse, conversationId: string): boolean => {
  const url = new URL(response.url());
  return response.request().method() === "POST" &&
    url.pathname === `/api/v1/conversations/${conversationId}/questions`;
};

const answerIdFromDraftStream = (response: PlaywrightResponse): string | undefined => {
  const url = new URL(response.url());
  if (response.request().method() !== "GET" || url.searchParams.get("workspace_id") !== fixture.workspaceId) {
    return undefined;
  }
  const match = /^\/api\/v1\/answers\/([^/]+)\/stream$/.exec(url.pathname);
  const answerId = match?.[1];
  return answerId !== undefined && uuidPattern.test(answerId) ? answerId : undefined;
};

const answerFromAcceptance = async (response: PlaywrightResponse, conversationId: string): Promise<string> => {
  expect(response.status()).toBe(202);
  const body: unknown = await response.json();
  if (body === null || typeof body !== "object" || Array.isArray(body)) throw new Error("Question acceptance must be an object");
  const acceptance = body as {
    question?: { conversation_id?: unknown; scope?: { retrieval_mode?: unknown } };
    answer?: { id?: unknown; conversation_id?: unknown };
  };
  const acceptedQuestion = acceptance.question;
  const acceptedAnswer = acceptance.answer;
  if (acceptedQuestion === undefined || acceptedAnswer === undefined || acceptedQuestion.scope === undefined) {
    throw new Error("Question acceptance did not bind the answer and conversation IDs");
  }
  const answerId = acceptedAnswer.id;
  if (typeof answerId !== "string" || !uuidPattern.test(answerId) ||
    acceptedQuestion.conversation_id !== conversationId || acceptedAnswer.conversation_id !== conversationId ||
    acceptedQuestion.scope.retrieval_mode !== "hybrid") {
    throw new Error("Question acceptance did not bind the answer and conversation IDs");
  }
  return answerId;
};

const draftChunkEventCount = async (draft: Locator): Promise<number> => {
  const value = await draft.getAttribute("data-draft-sequence");
  if (value === null) return 0;
  if (!/^[1-9][0-9]*$/.test(value)) throw new Error("Draft frame count must be a positive integer");
  const count = Number(value);
  if (!Number.isSafeInteger(count)) throw new Error("Draft frame count exceeds the safe integer range");
  return count;
};

const assertDraftStreamAuditClean = async (page: Page, requireTraffic = false): Promise<void> => {
  const audit = await page.evaluate(() => {
    const state = (window as typeof window & { __zhixuDraftStreamAudit?: DraftStreamAuditState }).__zhixuDraftStreamAudit;
    return state === undefined ? undefined : { ...state };
  });
  if (audit === undefined) throw new Error("RAG_BROWSER_DRAFT_AUDIT_MISSING");
  if (audit.scanFailed) throw new Error("RAG_BROWSER_DRAFT_AUDIT_FAILED");
  if (audit.credentialLeak) throw new Error("RAG_BROWSER_DRAFT_CREDENTIAL_LEAK");
  if (requireTraffic && (audit.responses < 1 || audit.byteChunks < 1)) {
    throw new Error("RAG_BROWSER_DRAFT_AUDIT_NO_TRAFFIC");
  }
};

const assertBrowserCredentialsAbsent = async (contentRoot: Locator, surface: string): Promise<void> => {
  const contents = await contentRoot.allTextContents();
  if (contents.some((content) => content.includes(fixture.sessionToken) || content.includes(fixture.csrfToken))) {
    throw new Error(`${surface} exposed a browser credential`);
  }
};

const observedPublication = async (page: Page): Promise<"pending" | "completed" | "refused" | "clarification_required"> => {
  if (await page.getByText("已校验回答", { exact: true }).isVisible()) return "completed";
  if (await page.getByText("明确拒答", { exact: true }).isVisible()) return "refused";
  if (await page.getByText("需要澄清", { exact: true }).isVisible()) return "clarification_required";
  return "pending";
};

const observeDraftBeforePublication = async (page: Page, flowStartedAtMs: number): Promise<DraftObservation> => {
  const draft = page.locator('[aria-label="生成中草稿"]');
  const validatedAnswer = page.getByText("已校验回答", { exact: true });
  const deadline = Date.now() + realProviderFlowTimeoutMs;
  while (!await draft.isVisible()) {
    await assertDraftStreamAuditClean(page);
    const publication = await observedPublication(page);
    if (publication !== "pending") throw new Error(`RAG reached ${publication} before the browser observed a draft chunk`);
    if (Date.now() >= deadline) throw new Error("Browser did not observe a draft chunk before the bounded timeout");
    await page.waitForTimeout(100);
  }
  const firstDraftObservedLatencyMs = Date.now() - flowStartedAtMs;
  expect(await validatedAnswer.isVisible()).toBe(false);
  await assertDraftStreamAuditClean(page);
  await assertBrowserCredentialsAbsent(draft, "Draft");
  let chunkEvents = await draftChunkEventCount(draft);
  expect(chunkEvents).toBeGreaterThanOrEqual(1);
  while (chunkEvents < 2) {
    const publication = await observedPublication(page);
    if (publication !== "pending") {
      throw new Error(`RAG reached ${publication} before the browser observed two draft chunk events`);
    }
    await assertDraftStreamAuditClean(page);
    await assertBrowserCredentialsAbsent(draft, "Draft");
    chunkEvents = Math.max(chunkEvents, await draftChunkEventCount(draft));
    if (chunkEvents >= 2) break;
    if (Date.now() >= deadline) throw new Error("Browser did not observe two draft chunk events before the bounded timeout");
    await page.waitForTimeout(100);
  }
  expect(await validatedAnswer.isVisible()).toBe(false);
  await assertDraftStreamAuditClean(page);
  await assertBrowserCredentialsAbsent(draft, "Draft");
  return { chunkEvents, firstDraftObservedLatencyMs };
};

test("真实 Provider RAG 在桌面和移动端观察多帧草稿后恢复已校验 Answer", async ({ browser, page }) => {
  test.setTimeout(realProviderTestTimeoutMs);
  const issues: string[] = [];
  await installDraftStreamAudit(page.context());
  await installWorkspace(page.context());
  await installSession(page.context());
  captureRuntimeIssues(page, "desktop", issues);
  expect(page.viewportSize()).toEqual({ width: 1440, height: 900 });

  await page.goto("/chat");
  await expect(page.getByRole("heading", { name: "证据研究台" })).toBeVisible();
  await page.getByLabel("新会话标题").fill(`Real provider ${String(Date.now())}`);
  const createConversation = page.waitForResponse((response) => {
    const url = new URL(response.url());
    return response.request().method() === "POST" && url.pathname === "/api/v1/conversations";
  });
  await page.getByRole("button", { name: "新建会话" }).click();
  expect((await createConversation).status()).toBe(201);
  await expect(page).toHaveURL(new RegExp("/chat/[0-9a-f-]{36}$"));
  const conversationId = new URL(page.url()).pathname.split("/").at(-1);
  if (conversationId === undefined || !uuidPattern.test(conversationId)) throw new Error("Conversation UI route did not contain a canonical ID");
  await expect(page.getByRole("heading", { name: "提出第一个问题。" })).toBeVisible();

  const mobile = await browser.newContext({ baseURL: appBaseUrl, viewport: { width: 390, height: 844 } });
  await installDraftStreamAudit(mobile);
  await installWorkspace(mobile);
  await installSession(mobile);
  const mobilePage = await mobile.newPage();
  captureRuntimeIssues(mobilePage, "mobile", issues);
  let mobileQuestionPosts = 0;
  mobilePage.on("request", (request) => {
    const url = new URL(request.url());
    if (request.method() === "POST" && url.pathname === `/api/v1/conversations/${conversationId}/questions`) mobileQuestionPosts += 1;
  });
  let receipt: RealProviderReceipt;
  try {
    expect(mobilePage.viewportSize()).toEqual({ width: 390, height: 844 });
    await mobilePage.goto(conversationPath(conversationId));
    await expect(mobilePage.getByRole("heading", { name: "提出第一个问题。" })).toBeVisible();

    await page.locator(".rag-scope summary").click();
    await page.getByLabel("检索模式").selectOption("hybrid");
    await expect(page.getByLabel("检索模式")).toHaveValue("hybrid");
    await page.getByLabel("回答深度").selectOption("concise");
    await expect(page.getByLabel("回答深度")).toHaveValue("concise");
    await page.getByLabel("问题").fill(fixture.question);
    const questionAcceptance = page.waitForResponse(
      (response) => isQuestionAcceptance(response, conversationId),
      { timeout: commandResponseTimeoutMs },
    );
    const answerDraftStream = page.waitForResponse(
      (response) => answerIdFromDraftStream(response) !== undefined,
      { timeout: realProviderFlowTimeoutMs },
    );
    const flowStartedAtMs = Date.now();
    const desktopDraftObservation = observeDraftBeforePublication(page, flowStartedAtMs);
    await page.getByRole("button", { name: "提交问题" }).click();
    const answerId = await answerFromAcceptance(await questionAcceptance, conversationId);
    const mobileAnswerDraftStream = mobilePage.waitForResponse(
      (response) => answerIdFromDraftStream(response) === answerId,
      { timeout: realProviderFlowTimeoutMs },
    );
    await mobilePage.reload({ waitUntil: "domcontentloaded" });
    const mobileDraftObservation = observeDraftBeforePublication(mobilePage, flowStartedAtMs);
    const [stream, mobileStream, desktopDraft, mobileDraft] = await Promise.all([
      answerDraftStream,
      mobileAnswerDraftStream,
      desktopDraftObservation,
      mobileDraftObservation,
    ]);
    expect(answerIdFromDraftStream(stream)).toBe(answerId);
    expect(stream.status()).toBe(200);
    expect(stream.headers()["content-type"]?.toLowerCase()).toContain("text/event-stream");
    expect(answerIdFromDraftStream(mobileStream)).toBe(answerId);
    expect(mobileStream.status()).toBe(200);
    expect(mobileStream.headers()["content-type"]?.toLowerCase()).toContain("text/event-stream");
    await assertDraftStreamAuditClean(page, true);
    await assertDraftStreamAuditClean(mobilePage, true);

    const draft = page.locator('[aria-label="生成中草稿"]');
    const validatedAnswer = page.getByText("已校验回答", { exact: true });
    await expect.poll(async () => {
      await assertDraftStreamAuditClean(page);
      await assertBrowserCredentialsAbsent(draft, "Desktop draft");
      if (await validatedAnswer.isVisible()) return "completed";
      if (await page.getByText("明确拒答", { exact: true }).isVisible()) return "refused";
      if (await page.getByText("需要澄清", { exact: true }).isVisible()) return "clarification_required";
      return "pending";
    }, { timeout: realProviderFlowTimeoutMs }).toBe("completed");
    await expect(draft).toHaveCount(0);
    await assertDraftStreamAuditClean(page, true);
    const mobileDraftRegion = mobilePage.locator('[aria-label="生成中草稿"]');
    const mobileValidatedAnswer = mobilePage.getByText("已校验回答", { exact: true });
    await expect.poll(async () => {
      await assertDraftStreamAuditClean(mobilePage);
      await assertBrowserCredentialsAbsent(mobileDraftRegion, "Mobile draft");
      if (await mobileValidatedAnswer.isVisible()) return "completed";
      if (await mobilePage.getByText("明确拒答", { exact: true }).isVisible()) return "refused";
      if (await mobilePage.getByText("需要澄清", { exact: true }).isVisible()) return "clarification_required";
      return "pending";
    }, { timeout: commandResponseTimeoutMs }).toBe("completed");
    await expect(mobileDraftRegion).toHaveCount(0);
    await assertDraftStreamAuditClean(mobilePage, true);
    const completionLatencyMs = Date.now() - flowStartedAtMs;
    const answer = page.locator(".rag-answer").filter({ has: validatedAnswer });
    await assertBrowserCredentialsAbsent(answer, "Desktop final Answer");
    await expect(answer).toContainText(fixture.evidenceToken);
    const mobileAnswer = mobilePage.locator(".rag-answer").filter({ has: mobileValidatedAnswer });
    await assertBrowserCredentialsAbsent(mobileAnswer, "Mobile final Answer");
    await expect(mobileAnswer).toContainText(fixture.evidenceToken);
    const citation = answer.getByRole("button", { name: "打开段落证据" }).first();
    await expect(citation).toBeVisible();
    await citation.click();
    const evidencePanel = page.getByRole("complementary", { name: "引用证据" });
    await expect(evidencePanel.getByText("已选择引用", { exact: true })).toBeVisible();
    await expect(evidencePanel.getByText("Source Version", { exact: true })).toBeVisible();
    await expect(evidencePanel.locator("dl > div").filter({ hasText: "Source Version" }).locator("dd")).toHaveText(fixture.sourceVersionId);
    const sourceSpanResponse = page.waitForResponse((response) =>
      response.request().method() === "GET" && new URL(response.url()).pathname === sourceSpanPath(),
      { timeout: commandResponseTimeoutMs },
    );
    await evidencePanel.getByRole("button", { name: "打开段落证据" }).click();
    expect((await sourceSpanResponse).status()).toBe(200);
    const sourceSpanDialog = page.getByRole("dialog", { name: "Source Span 证据" });
    await expect(sourceSpanDialog).toBeVisible();
    await expect(sourceSpanDialog.locator("pre")).toContainText(fixture.evidenceToken);
    await assertNoHorizontalOverflow(page);

    await expect(mobilePage.getByRole("button", { name: "打开段落证据" }).first()).toBeVisible();
    await assertNoHorizontalOverflow(mobilePage);
    expect(mobileQuestionPosts).toBe(0);
    expect(desktopDraft.firstDraftObservedLatencyMs).toBeGreaterThanOrEqual(0);
    expect(mobileDraft.firstDraftObservedLatencyMs).toBeGreaterThanOrEqual(0);
    expect(completionLatencyMs).toBeGreaterThanOrEqual(desktopDraft.firstDraftObservedLatencyMs);
    expect(completionLatencyMs).toBeGreaterThanOrEqual(mobileDraft.firstDraftObservedLatencyMs);
    receipt = {
      answer_id: answerId,
      draft_observed: true,
      desktop_draft_chunk_events: desktopDraft.chunkEvents,
      mobile_draft_chunk_events: mobileDraft.chunkEvents,
      desktop_first_draft_observed_latency_ms: desktopDraft.firstDraftObservedLatencyMs,
      mobile_first_draft_observed_latency_ms: mobileDraft.firstDraftObservedLatencyMs,
      completion_latency_ms: completionLatencyMs,
      citation_opened: true,
      mobile_verified: true,
    };
  } finally {
    await mobile.close();
  }

  expect(issues, issues.join("\n")).toEqual([]);
  await writeReceipt(receipt);
});
