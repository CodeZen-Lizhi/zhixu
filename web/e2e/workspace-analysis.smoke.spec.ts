import { expect, test, type BrowserContext, type ConsoleMessage, type Page, type Request, type Response } from "@playwright/test";
import { createHash } from "node:crypto";
import { writeFile } from "node:fs/promises";
import { basename, dirname, isAbsolute, normalize } from "node:path";

const workspaceStorageKey = "zhixu.active-workspace-id";
const csrfStorageKey = "zhixu.csrf-token";
const uuidPattern = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;
const smokeTimeoutMs = 180_000;
// Keep this in lockstep with WORKSPACE_ANALYSIS_CANCEL_MAX_ATTEMPTS; the unit
// suite locks the production bound while this smoke asserts the wire bound.
const workspaceAnalysisCancelMaxAttempts = 5;

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

const requiredBarrierReadyPath = (name: string): string => {
  const value = requiredToken(name);
  if (
    !isAbsolute(value) || normalize(value) !== value || basename(value) !== "browser-ready" ||
    basename(dirname(value)) !== "workspace-analysis-barrier"
  ) {
    throw new Error(`${name} must name the canonical smoke barrier readiness file`);
  }
  return value;
};

const requiredProgressPath = (name: string): string => {
  const value = requiredToken(name);
  if (
    !isAbsolute(value) || normalize(value) !== value || basename(value) !== "browser-progress" ||
    basename(dirname(value)) !== "workspace-analysis-barrier"
  ) {
    throw new Error(`${name} must name the canonical smoke progress file`);
  }
  return value;
};

const expectedTerminal = (): "completed" | "refused" | "cancelled" => {
  const value = process.env.ZHIXU_WORKSPACE_ANALYSIS_SMOKE_EXPECTED_TERMINAL?.trim() ?? "completed";
  if (value !== "completed" && value !== "refused" && value !== "cancelled") {
    throw new Error("ZHIXU_WORKSPACE_ANALYSIS_SMOKE_EXPECTED_TERMINAL must be completed, refused, or cancelled");
  }
  return value;
};

const fixture = {
  baseUrl: requiredLoopbackUrl("ZHIXU_WORKSPACE_ANALYSIS_SMOKE_BASE_URL"),
  sessionToken: requiredToken("ZHIXU_WORKSPACE_ANALYSIS_SMOKE_SESSION_TOKEN"),
  csrfToken: requiredToken("ZHIXU_WORKSPACE_ANALYSIS_SMOKE_CSRF_TOKEN"),
  workspaceId: requiredUuid("ZHIXU_WORKSPACE_ANALYSIS_SMOKE_WORKSPACE_ID"),
  question: requiredToken("ZHIXU_WORKSPACE_ANALYSIS_SMOKE_QUESTION"),
  privateMarker: requiredToken("ZHIXU_WORKSPACE_ANALYSIS_SMOKE_PRIVATE_MARKER"),
  barrierReadyPath: requiredBarrierReadyPath("ZHIXU_WORKSPACE_ANALYSIS_SMOKE_BARRIER_READY_FILE"),
  progressPath: requiredProgressPath("ZHIXU_WORKSPACE_ANALYSIS_SMOKE_PROGRESS_FILE"),
  terminal: expectedTerminal(),
};

const markProgress = async (stage: string): Promise<void> => {
  await writeFile(fixture.progressPath, `${stage}\n`, { encoding: "utf8", mode: 0o600 });
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

const isWorkflowCancelResponse = (response: Response, workflowRunId?: string): boolean => {
  const url = new URL(response.url());
  const expectedPath = workflowRunId === undefined
    ? /^\/api\/v1\/workflows\/[0-9a-f-]{36}\/cancel$/.test(url.pathname)
    : url.pathname === `/api/v1/workflows/${workflowRunId}/cancel`;
  return response.request().method() === "POST" && expectedPath;
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
  if (text.startsWith("EventSource")) return "event_source";
  if (text.startsWith("Access to fetch")) return "cors";

  const fingerprint = createHash("sha256").update(text).digest("hex").slice(0, 12);
  const source = (() => {
    try {
      const pathname = new URL(message.location().url).pathname;
      if (pathname === "/api/v1/events") return "events";
      if (pathname.startsWith("/src/")) return "application";
      if (pathname.startsWith("/node_modules/")) return "dependency";
      if (pathname === "/@vite/client") return "vite";
    } catch {
      // Fall through to the bounded unknown category.
    }
    return "unknown";
  })();
  return `unknown_${source}_${fingerprint}`;
};

const captureRuntimeIssues = (
  page: Page,
  target: string,
  issues: string[],
  expectedHttpFailure: (response: Response) => boolean = () => false,
  expectedConsoleFailure: (message: ConsoleMessage) => boolean = () => false,
): (() => void) => {
  const captureConsole = (message: ConsoleMessage): void => {
    if ((message.type() === "warning" || message.type() === "error") && !expectedConsoleFailure(message)) {
      issues.push(`${target}.console.${message.type()}.${consoleIssueClass(message)}`);
    }
  };
  const capturePageError = (): void => {
    issues.push(`${target}.pageerror`);
  };
  const captureResponse = (response: Response): void => {
    if (response.status() >= 400 && !expectedHttpFailure(response)) {
      issues.push(`${target}.http.${String(response.status())}`);
    }
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

const captureProposalWrites = (request: Request, writes: string[]): void => {
  const url = new URL(request.url());
  if (url.pathname.startsWith("/api/v1/proposals") && request.method() !== "GET") writes.push(request.method());
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

const assertComposerFollowsPublishedAnswer = async (page: Page): Promise<void> => {
  const geometry = await page.evaluate(() => {
    const answer = [...document.querySelectorAll<HTMLElement>(".rag-answer")].at(-1);
    const composer = document.querySelector<HTMLElement>(".rag-composer");
    if (answer === undefined || composer === null) return null;
    const answerRect = answer.getBoundingClientRect();
    const composerRect = composer.getBoundingClientRect();
    return { answerBottom: answerRect.bottom, composerPosition: getComputedStyle(composer).position, composerTop: composerRect.top };
  });
  expect(geometry).not.toBeNull();
  expect(geometry?.composerPosition).toBe("static");
  expect(geometry?.composerTop).toBeGreaterThanOrEqual((geometry?.answerBottom ?? 0) - 1);
};

const isQuestionRequest = (request: Request, conversationId: string): boolean => {
  const url = new URL(request.url());
  return request.method() === "POST" && url.pathname === `/api/v1/conversations/${conversationId}/questions`;
};

const conversationIdFromPage = (page: Page): string => {
  const conversationId = new URL(page.url()).pathname.split("/").at(-1);
  if (conversationId === undefined || !uuidPattern.test(conversationId)) {
    throw new Error("Conversation UI route did not contain a canonical ID");
  }
  return conversationId;
};

const record = (value: unknown, label: string): Record<string, unknown> => {
  if (value === null || typeof value !== "object" || Array.isArray(value)) throw new Error(`${label} must be an object`);
  return value as Record<string, unknown>;
};

const workflowCancelProblemCodePattern = /^[A-Z][A-Z0-9_]{0,127}$/;

const workflowCancelProblemCode = async (response: Response): Promise<string> => {
  let payload: unknown;
  try {
    payload = await response.json() as unknown;
  } catch {
    throw new Error("WORKSPACE_ANALYSIS_BROWSER_CANCEL_CONFLICT_JSON_INVALID");
  }
  const problem = record(payload, "Workflow cancel conflict");
  if (
    response.status() !== 409 || Object.keys(problem).sort().join(",") !== "error_code,message,retryable" ||
    typeof problem.error_code !== "string" || !workflowCancelProblemCodePattern.test(problem.error_code) ||
    problem.retryable !== false ||
    typeof problem.message !== "string" || problem.message.trim() === "" ||
    new TextEncoder().encode(problem.message).byteLength > 4096
  ) {
    throw new Error("WORKSPACE_ANALYSIS_BROWSER_CANCEL_CONFLICT_INVALID");
  }
  return problem.error_code;
};

const assertWorkflowVersionConflict = async (response: Response): Promise<void> => {
  if (await workflowCancelProblemCode(response) !== "WORKFLOW_VERSION_CONFLICT") {
    throw new Error("WORKSPACE_ANALYSIS_BROWSER_CANCEL_CONFLICT_CODE_INVALID");
  }
};

const workflowCancelExpectedVersion = (
  response: Response,
  binding: AcceptedWorkspaceAnalysisBinding,
): number => {
  const request = response.request();
  let payload: unknown;
  try {
    payload = request.postDataJSON();
  } catch {
    throw new Error("WORKSPACE_ANALYSIS_BROWSER_CANCEL_REQUEST_JSON_INVALID");
  }
  const body = record(payload, "Workflow cancellation request");
  const expectedVersion = body.expected_version;
  const headers = request.headers();
  if (
    Object.keys(body).join(",") !== "expected_version" ||
    !Number.isSafeInteger(expectedVersion) || Number(expectedVersion) < 1 ||
    headers["x-workspace-id"] !== fixture.workspaceId ||
    headers["idempotency-key"] !== `m9-workflow-${binding.workflowRunId}-cancel-${String(expectedVersion)}`
  ) {
    throw new Error("WORKSPACE_ANALYSIS_BROWSER_CANCEL_REQUEST_INVALID");
  }
  return Number(expectedVersion);
};

const cancelResponseCodes = async (responses: readonly Response[]): Promise<string[]> => {
  const conflictCodes: string[] = [];
  for (const response of responses) {
    if (response.status() === 409) conflictCodes.push(await workflowCancelProblemCode(response));
  }
  return conflictCodes;
};

const cancelResponseDiagnostic = (responses: readonly Response[], conflictCodes: readonly string[]): string => {
  const statuses = responses.map((response) => response.status());
  return `WORKSPACE_ANALYSIS_BROWSER_CANCEL_STATUS_SEQUENCE_${statuses.join("_")}_ERROR_CODE_SEQUENCE_${conflictCodes.join("_")}`;
};

interface AcceptedWorkspaceAnalysisBinding {
  answerId: string;
  workflowRunId: string;
  workflowVersion: number;
}

const assertWorkspaceAnalysisQuestion = (request: Request): void => {
  let payload: unknown;
  try {
    payload = request.postDataJSON();
  } catch {
    throw new Error("Workspace Analysis question request did not contain JSON");
  }
  const body = record(payload, "Workspace Analysis question request");
  if (body.workspace_id !== fixture.workspaceId || body.question !== fixture.question || body.mode !== "workspace_analysis") {
    throw new Error("Workspace Analysis question request did not preserve mode binding");
  }
};

const assertQuestionAccepted = async (response: Response, conversationId: string): Promise<AcceptedWorkspaceAnalysisBinding> => {
  expect(response.status()).toBe(202);
  const payload = record(await response.json() as unknown, "Workspace Analysis question acceptance");
  const question = record(payload.question, "Workspace Analysis accepted question");
  const answer = record(payload.answer, "Workspace Analysis accepted answer");
  const workflow = record(answer.workflow, "Workspace Analysis accepted workflow");
  if (
    question.conversation_id !== conversationId || question.workspace_id !== fixture.workspaceId ||
    question.mode !== "workspace_analysis" || typeof question.id !== "string" || !uuidPattern.test(question.id) ||
    answer.workspace_id !== fixture.workspaceId || answer.conversation_id !== conversationId ||
    answer.question_id !== question.id || answer.publication_status !== "pending" ||
    typeof answer.id !== "string" || !uuidPattern.test(answer.id) ||
    typeof workflow.run_id !== "string" || !uuidPattern.test(workflow.run_id) ||
    !Number.isSafeInteger(workflow.version) || Number(workflow.version) < 1
  ) {
    throw new Error("Workspace Analysis question acceptance did not bind its canonical resources");
  }
  return { answerId: answer.id, workflowRunId: workflow.run_id, workflowVersion: Number(workflow.version) };
};

const assertWorkflowCancellationAccepted = async (
  response: Response,
  binding: AcceptedWorkspaceAnalysisBinding,
  expectedVersion: number,
): Promise<void> => {
  const result = record(await response.json() as unknown, "Workflow cancellation acceptance");
  if (
    response.status() !== 200 ||
    Object.keys(result).sort().join(",") !== "cancel_requested,pause_requested,status,status_url,version,workflow_run_id" ||
    result.workflow_run_id !== binding.workflowRunId ||
    result.status_url !== `/api/v1/workflows/${binding.workflowRunId}` ||
    (result.status !== "running" && result.status !== "cancelled") ||
    result.pause_requested !== false || result.cancel_requested !== true ||
    !Number.isSafeInteger(result.version) || Number(result.version) <= expectedVersion
  ) {
    throw new Error("WORKSPACE_ANALYSIS_BROWSER_CANCEL_RESPONSE_INVALID");
  }
};

const terminalHeading = (): string => {
  if (fixture.terminal === "completed") return "已校验分析";
  if (fixture.terminal === "refused") return "现有证据不足以形成可靠结论";
  return "分析已取消";
};

const waitForTerminal = async (page: Page): Promise<void> => {
  await expect.poll(async () => {
    if (await page.getByText("已校验分析", { exact: true }).isVisible()) return "completed";
    if (await page.getByText("现有证据不足以形成可靠结论", { exact: true }).isVisible()) return "refused";
    if (await page.getByText("分析已取消", { exact: true }).isVisible()) return "cancelled";
    return "pending";
  }, { timeout: smokeTimeoutMs }).toBe(fixture.terminal);
};

const assertSafeTimeline = async (page: Page): Promise<void> => {
  const timeline = page.getByRole("region", { name: "工作区分析进度" });
  await expect(timeline).toBeVisible();
  const terminalStatus = fixture.terminal === "completed" ? "已完成" : fixture.terminal === "cancelled" ? "已取消" : "未发布";
  await expect(timeline.getByText(terminalStatus, { exact: true })).toBeVisible({ timeout: smokeTimeoutMs });
  await expect(timeline.getByText("模型调用", { exact: true })).toBeVisible();
  await expect(timeline.getByText("工具调用", { exact: true })).toBeVisible();
  await expect(timeline.getByText("输入 tokens", { exact: true })).toBeVisible();
  await expect(timeline.getByText("输出 tokens", { exact: true })).toBeVisible();
  if (fixture.terminal === "completed") {
    await expect(timeline.getByText("3/3", { exact: true })).toBeVisible();
    await expect(timeline.getByText("4/6", { exact: true })).toBeVisible();
  }
  await expect(timeline).not.toContainText(fixture.privateMarker);
  await expect(timeline).not.toContainText("server_binding");
};

test("工作区分析在真实 UI 中保持只读边界并通过桌面和移动端冒烟", async ({ browser }, testInfo) => {
  test.setTimeout(smokeTimeoutMs + 60_000);
  await markProgress("STARTED");
  const runtimeIssues: string[] = [];
  const proposalWrites: string[] = [];
  const desktopContext = await browser.newContext({ baseURL: fixture.baseUrl, viewport: { width: 1440, height: 900 } });
  await installSession(desktopContext);
  const desktop = await desktopContext.newPage();
  let cancelRecoveryActive = false;
  let expectedCancelWorkflowRunId: string | undefined;
  let expectedCancelConsoleFailures = 0;
  const stopDesktopRuntimeCapture = captureRuntimeIssues(
    desktop,
    "desktop",
    runtimeIssues,
    (response) => cancelRecoveryActive && expectedCancelWorkflowRunId !== undefined && response.status() === 409 && isWorkflowCancelResponse(response, expectedCancelWorkflowRunId),
    (message) => {
      const expected = cancelRecoveryActive && message.type() === "error" && consoleIssueClass(message) === "resource_http_409";
      if (expected) expectedCancelConsoleFailures++;
      return expected;
    },
  );
  desktop.on("request", (request) => captureProposalWrites(request, proposalWrites));

  try {
    expect(desktop.viewportSize()).toEqual({ width: 1440, height: 900 });
    await desktop.goto("/chat");
    await expect(desktop.getByRole("heading", { name: "证据研究台" })).toBeVisible();
    await markProgress("CHAT_READY");
    await desktop.getByLabel("新会话标题").fill(`Workspace analysis ${String(Date.now())}`);
    await markProgress("TITLE_FILLED");
    const createResponse = desktop.waitForResponse((response) => {
      const url = new URL(response.url());
      return response.request().method() === "POST" && url.pathname === "/api/v1/conversations";
    });
    await desktop.getByRole("button", { name: "新建会话" }).click();
    await markProgress("CREATE_SUBMITTED");
    expect((await createResponse).status()).toBe(201);
    await markProgress("CREATE_ACCEPTED");
    await desktop.waitForURL((url) => /^\/chat\/[0-9a-f-]{36}$/.test(url.pathname));
    const conversationId = conversationIdFromPage(desktop);
    await markProgress("CONVERSATION_ROUTED");
    await expect(desktop.getByRole("heading", { name: "提出第一个问题。" })).toBeVisible();
    await markProgress("CONVERSATION_CREATED");

    const modeControl = desktop.getByRole("radiogroup", { name: "回答模式" });
    await modeControl.getByRole("radio", { name: "工作区分析" }).click();
    await expect(modeControl.getByRole("radio", { name: "工作区分析" })).toHaveAttribute("aria-checked", "true");
    await expect(desktop.locator(".rag-scope")).toHaveCount(0);
    await markProgress("MODE_SELECTED");
    await desktop.getByLabel("问题").fill(fixture.question);
    const questionRequest = desktop.waitForRequest((request) => isQuestionRequest(request, conversationId));
    const questionResponse = desktop.waitForResponse((response) => isQuestionRequest(response.request(), conversationId));
    await desktop.getByRole("button", { name: "提交问题" }).click();
    assertWorkspaceAnalysisQuestion(await questionRequest);
    const accepted = await assertQuestionAccepted(await questionResponse, conversationId);
    expectedCancelWorkflowRunId = accepted.workflowRunId;
    await markProgress("QUESTION_ACCEPTED");

    const stop = desktop.getByRole("button", { name: "停止工作区分析" });
    await expect(stop).toBeVisible();
    if (fixture.terminal === "cancelled") {
      const cancelResponses: Response[] = [];
      const captureCancelResponse = (response: Response): void => {
        if (isWorkflowCancelResponse(response, accepted.workflowRunId)) cancelResponses.push(response);
      };
      desktop.on("response", captureCancelResponse);
      cancelRecoveryActive = true;
      try {
        await markProgress("STOP_VISIBLE");
        await stop.click();
        await markProgress("STOP_REQUEST_SENT");
        try {
          await expect.poll(() => {
            const statuses = cancelResponses.map((response) => response.status());
            const finalStatus = statuses.at(-1);
            if (finalStatus === 200 && statuses.slice(0, -1).every((status) => status === 409)) return "complete";
            if (
              statuses.length >= workspaceAnalysisCancelMaxAttempts ||
              statuses.some((status) => status !== 200 && status !== 409) ||
              statuses.slice(0, -1).includes(200)
            ) return `invalid:${statuses.join(",")}`;
            return `pending:${statuses.join(",")}`;
          }, { timeout: 15_000 }).toBe("complete");
        } catch {
          const statuses = cancelResponses.map((response) => response.status());
          if (statuses.length === 0) {
            await markProgress("STOP_RESPONSE_NONE");
            throw new Error("WORKSPACE_ANALYSIS_BROWSER_CANCEL_RESPONSE_NONE");
          }
          if (statuses.length === 1 && statuses[0] === 409) {
            const conflictCodes = await cancelResponseCodes(cancelResponses);
            if (conflictCodes.length === 1 && conflictCodes[0] === "WORKFLOW_VERSION_CONFLICT") {
              await markProgress("STOP_RESPONSE_VERSION_CONFLICT_ONLY");
              throw new Error(cancelResponseDiagnostic(cancelResponses, conflictCodes));
            }
            await markProgress("STOP_RESPONSE_INVALID");
            throw new Error(cancelResponseDiagnostic(cancelResponses, conflictCodes));
          }
          const conflictCodes = await cancelResponseCodes(cancelResponses);
          await markProgress("STOP_RESPONSE_INVALID");
          throw new Error(cancelResponseDiagnostic(cancelResponses, conflictCodes));
        }

        const statuses = cancelResponses.map((response) => response.status());
        expect(statuses.length).toBeLessThanOrEqual(workspaceAnalysisCancelMaxAttempts);
        expect(statuses.at(-1)).toBe(200);
        expect(statuses.slice(0, -1).every((status) => status === 409)).toBe(true);
        const conflictResponses = cancelResponses.slice(0, -1);
        for (const conflictResponse of conflictResponses) await assertWorkflowVersionConflict(conflictResponse);
        const attemptedVersions = cancelResponses.map((response) => workflowCancelExpectedVersion(response, accepted));
        const firstAttemptedVersion = attemptedVersions[0];
        if (firstAttemptedVersion === undefined) throw new Error("WORKSPACE_ANALYSIS_BROWSER_CANCEL_REQUEST_VERSION_MISSING");
        expect(firstAttemptedVersion).toBeGreaterThanOrEqual(accepted.workflowVersion);
        for (let index = 1; index < attemptedVersions.length; index += 1) {
          const previousVersion = attemptedVersions[index - 1];
          const currentVersion = attemptedVersions[index];
          if (previousVersion === undefined || currentVersion === undefined) {
            throw new Error("WORKSPACE_ANALYSIS_BROWSER_CANCEL_REQUEST_VERSION_MISSING");
          }
          expect(currentVersion).toBeGreaterThan(previousVersion);
        }
        if (conflictResponses.length > 0) {
          await markProgress("STOP_VERSION_REFRESHED");
        }
        const acceptedResponse = cancelResponses.at(-1);
        if (acceptedResponse === undefined) throw new Error("WORKSPACE_ANALYSIS_BROWSER_CANCEL_RESPONSE_MISSING");
        const acceptedExpectedVersion = attemptedVersions.at(-1);
        if (acceptedExpectedVersion === undefined) throw new Error("WORKSPACE_ANALYSIS_BROWSER_CANCEL_REQUEST_VERSION_MISSING");
        await assertWorkflowCancellationAccepted(acceptedResponse, accepted, acceptedExpectedVersion);
        await markProgress("STOP_ACCEPTED");
        await desktop.evaluate(() => new Promise<void>((resolve) => requestAnimationFrame(() => resolve())));
        expect(expectedCancelConsoleFailures).toBeLessThanOrEqual(conflictResponses.length);
      } finally {
        cancelRecoveryActive = false;
        desktop.off("response", captureCancelResponse);
      }
      await markProgress("STOP_CLICKED");
    } else {
      await markProgress("STOP_VISIBLE");
    }
    await writeFile(fixture.barrierReadyPath, "ready\n", { encoding: "utf8", mode: 0o600, flag: "wx" });
    await waitForTerminal(desktop);
    await markProgress("TERMINAL_VISIBLE");
    await assertSafeTimeline(desktop);
    await markProgress("TIMELINE_VERIFIED");

    await desktop.reload({ waitUntil: "domcontentloaded" });
    await expect(desktop).toHaveURL(new RegExp(`/chat/${conversationId}$`));
    await waitForTerminal(desktop);
    await assertSafeTimeline(desktop);

    const answer = desktop.locator(".rag-answer").filter({ has: desktop.getByText(terminalHeading(), { exact: true }) });
    await expect(answer).toBeVisible();
    await expect(answer).not.toContainText(fixture.privateMarker);

    if (fixture.terminal === "completed") {
      const gitAggregate = answer.locator(".rag-analysis-git");
      await expect(gitAggregate).toBeVisible();
      await expect(gitAggregate.getByText("工作树", { exact: true })).toBeVisible();
      await expect(gitAggregate).not.toContainText(fixture.privateMarker);
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
      await expect(sourceDialog).not.toContainText(fixture.privateMarker);
      await sourceDialog.getByRole("button", { name: "关闭来源片段" }).click();
      await expect(sourceDialog).toBeHidden();
      const proposal = answer.getByRole("link", { name: "查看提案" });
      await expect(proposal).toBeVisible();
      await expect(proposal).toHaveAttribute("href", "/proposals");
    } else {
      await expect(answer.locator(".rag-analysis-git")).toHaveCount(0);
      await expect(answer.getByRole("link", { name: "查看提案" })).toHaveCount(0);
    }
    if (fixture.terminal === "cancelled") {
      await expect(answer.getByText("WORKSPACE_ANALYSIS_CANCELLED", { exact: true })).toBeVisible();
      await expect(desktop.getByRole("button", { name: "停止工作区分析" })).toHaveCount(0);
    }
    await assertComposerFollowsPublishedAnswer(desktop);
    await assertNoHorizontalOverflow(desktop);
    await desktop.screenshot({ path: testInfo.outputPath("workspace-analysis-desktop.png"), fullPage: true });
    await markProgress("DESKTOP_VERIFIED");

    const mobileContext = await browser.newContext({ baseURL: fixture.baseUrl, viewport: { width: 390, height: 844 } });
    await installSession(mobileContext);
    const mobile = await mobileContext.newPage();
    const stopMobileRuntimeCapture = captureRuntimeIssues(mobile, "mobile", runtimeIssues);
    mobile.on("request", (request) => captureProposalWrites(request, proposalWrites));
    try {
      expect(mobile.viewportSize()).toEqual({ width: 390, height: 844 });
      await mobile.goto(`/chat/${conversationId}`);
      await expect(mobile.getByText(terminalHeading(), { exact: true })).toBeVisible();
      await expect(mobile.getByRole("region", { name: "工作区分析进度" })).toBeVisible();
      await expect(mobile.getByRole("complementary", { name: "引用证据", includeHidden: true })).toBeHidden();
      await assertComposerFollowsPublishedAnswer(mobile);
      await assertNoHorizontalOverflow(mobile);
      await mobile.screenshot({ path: testInfo.outputPath("workspace-analysis-mobile.png"), fullPage: true });
      await markProgress("MOBILE_VERIFIED");
    } finally {
      stopMobileRuntimeCapture();
      await mobileContext.close();
    }
  } finally {
    stopDesktopRuntimeCapture();
    await desktopContext.close();
  }

  expect(proposalWrites, "Workspace Analysis must not create or mutate Proposals").toEqual([]);
  expect(runtimeIssues, "browser console, page and network errors must remain empty").toEqual([]);
  await markProgress("COMPLETED");
});
