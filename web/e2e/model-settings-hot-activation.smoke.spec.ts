import { expect, test, type BrowserContext, type Page, type Request, type Response } from "@playwright/test";
import { mkdir, writeFile } from "node:fs/promises";
import { join } from "node:path";

const csrfStorageKey = "zhixu.csrf-token";
const uuidPattern = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;
const relayBaseURL = "http://127.0.0.1:11434";

const required = (name: string): string => {
  const value = process.env[name]?.trim();
  if (value === undefined || value === "") throw new Error(`${name} is required`);
  return value;
};

const requiredUUID = (name: string): string => {
  const value = required(name);
  if (!uuidPattern.test(value)) throw new Error(`${name} must be a canonical UUID`);
  return value;
};

const appBaseURL = required("ZHIXU_PLAYWRIGHT_BASE_URL").replace(/\/$/, "");
const sessionToken = required("ZHIXU_MODEL_SETTINGS_SMOKE_SESSION_TOKEN");
const csrfToken = required("ZHIXU_MODEL_SETTINGS_SMOKE_CSRF_TOKEN");
const validAPIKey = required("ZHIXU_MODEL_SETTINGS_SMOKE_API_KEY");
const rejectedAPIKey = required("ZHIXU_MODEL_SETTINGS_SMOKE_REJECTED_API_KEY");
const providerModelVersion = required("ZHIXU_MODEL_SETTINGS_SMOKE_PROVIDER_MODEL_VERSION");
const fixtureURL = required("ZHIXU_MODEL_SETTINGS_SMOKE_FIXTURE_URL").replace(/\/$/, "");
const workspaceID = requiredUUID("ZHIXU_MODEL_SETTINGS_SMOKE_WORKSPACE_ID");
const oldConversationID = requiredUUID("ZHIXU_MODEL_SETTINGS_SMOKE_OLD_CONVERSATION_ID");
const newConversationID = requiredUUID("ZHIXU_MODEL_SETTINGS_SMOKE_NEW_CONVERSATION_ID");
const outputDirectory = required("ZHIXU_PLAYWRIGHT_OUTPUT_DIR");
const runtimeInstanceIDs = [
  requiredUUID("ZHIXU_MODEL_SETTINGS_SMOKE_API_INSTANCE_ID"),
  requiredUUID("ZHIXU_MODEL_SETTINGS_SMOKE_WORKER_INSTANCE_ID"),
];

interface RuntimeProjection {
  applied_revision: number;
  phase: "active" | "unavailable";
  fresh: boolean;
}

interface SettingsProjection {
  desired_revision: number;
  active_revision: number;
  runtime: { api: RuntimeProjection; worker: RuntimeProjection };
  rollout: { phase: "idle" | "preparing" | "arming" | "activating" | "failed"; target_revision: number | null };
  apply_required: boolean;
  restart_required: boolean;
}

interface FixtureGateState {
  armed: boolean;
  entered: boolean;
}

const installSession = async (context: BrowserContext): Promise<void> => {
  await context.addCookies([{ name: "zhixu_session", value: sessionToken, url: appBaseURL, httpOnly: true, sameSite: "Strict" }]);
  await context.addInitScript(({ key, value }) => window.localStorage.setItem(key, value), { key: csrfStorageKey, value: csrfToken });
};

const assertNoHorizontalOverflow = async (page: Page): Promise<void> => {
  const widths = await page.evaluate(() => ({
    viewport: window.innerWidth,
    document: Math.max(document.documentElement.scrollWidth, document.body.scrollWidth),
    content: document.querySelector<HTMLElement>(".workbench__content")?.scrollWidth ?? 0,
  }));
  expect(widths.document).toBeLessThanOrEqual(widths.viewport + 1);
  expect(widths.content).toBeLessThanOrEqual(widths.viewport + 1);
};

const settingsFromServer = async (page: Page): Promise<SettingsProjection> => page.evaluate(async () => {
  const response = await fetch("/api/v1/settings/models", { headers: { Accept: "application/json" } });
  if (!response.ok) throw new Error(`settings response was ${String(response.status)}`);
  return response.json() as Promise<SettingsProjection>;
});

const expectApplied = (settings: SettingsProjection, revision: number): void => {
  expect(settings.desired_revision).toBe(revision);
  expect(settings.active_revision).toBe(revision);
  expect(settings.runtime.api).toEqual({ applied_revision: revision, phase: "active", fresh: true });
  expect(settings.runtime.worker).toEqual({ applied_revision: revision, phase: "active", fresh: true });
  expect(settings.rollout.phase).toBe("idle");
  expect(settings.apply_required).toBe(false);
  expect(settings.restart_required).toBe(false);
};

const ensureChatExpanded = async (page: Page): Promise<void> => {
  const model = page.getByLabel("对话模型名称");
  if (!(await model.isVisible())) await page.getByRole("button", { name: "配置对话模型" }).click();
  await expect(model).toBeVisible();
};

const replaceChatDraft = async (page: Page, modelVersion: string, apiKey: string): Promise<void> => {
  await ensureChatExpanded(page);
  await page.getByLabel("对话模型提供方").selectOption("openai-compatible");
  await page.getByLabel("对话模型基础地址（Base URL）").fill(relayBaseURL);
  await page.getByLabel("对话模型名称").fill("fixture-model");
  await page.getByLabel("对话模型版本").fill(modelVersion);
  await page.getByRole("radiogroup", { name: "对话 API Key操作" }).getByRole("radio", { name: "替换" }).check();
  await page.getByLabel("对话 API Key", { exact: true }).fill(apiKey);
};

const waitForApplied = async (page: Page, previousRevision: number): Promise<SettingsProjection> => {
  await expect.poll(async () => {
    const settings = await settingsFromServer(page);
    if (
      settings.active_revision <= previousRevision
      || settings.desired_revision !== settings.active_revision
      || settings.runtime.api.applied_revision !== settings.active_revision
      || settings.runtime.worker.applied_revision !== settings.active_revision
      || !settings.runtime.api.fresh
      || !settings.runtime.worker.fresh
      || settings.rollout.phase !== "idle"
      || settings.apply_required
    ) return -1;
    return settings.active_revision;
  }, { timeout: 45_000 }).toBeGreaterThan(previousRevision);
  await expect(page.getByText("API 与工作进程已应用当前生效版本。", { exact: true })).toBeVisible();
  const settings = await settingsFromServer(page);
  expectApplied(settings, settings.desired_revision);
  return settings;
};

const fixtureControl = async (path: "/control/block-next" | "/control/state" | "/control/release", method: "GET" | "POST"): Promise<FixtureGateState> => {
  const response = await fetch(`${fixtureURL}${path}`, {
    method,
    headers: { Authorization: `Bearer ${validAPIKey}` },
  });
  const body = await response.json() as unknown;
  if (!response.ok || typeof body !== "object" || body === null) throw new Error(`fixture control ${path} returned ${String(response.status)}`);
  const state = body as Partial<FixtureGateState>;
  if (typeof state.armed !== "boolean" || typeof state.entered !== "boolean") throw new Error(`fixture control ${path} returned an invalid state`);
  return { armed: state.armed, entered: state.entered };
};

const submitQuestion = async (page: Page, conversationID: string, idempotencyKey: string): Promise<string> => {
  const result = await page.evaluate(async ({ conversation, csrf, idempotency, workspace }) => {
    const response = await fetch(`/api/v2/conversations/${conversation}/questions`, {
      method: "POST",
      credentials: "same-origin",
      headers: {
        "Content-Type": "application/json",
        "Idempotency-Key": idempotency,
        "X-CSRF-Token": csrf,
      },
      body: JSON.stringify({
        workspace_id: workspace,
        question: "What does the approved runtime generation evidence require?",
        scope: { retrieval_mode: "keyword" },
        answer_depth: "standard",
        output_format: "markdown",
      }),
    });
    const body = await response.json() as { answer?: { id?: unknown }; error_code?: unknown };
    if (response.status !== 202 || typeof body.answer?.id !== "string") {
		const errorCode = typeof body.error_code === "string" ? body.error_code : "unknown";
		throw new Error(`question returned ${String(response.status)} (${errorCode})`);
    }
    return body.answer.id;
  }, { conversation: conversationID, csrf: csrfToken, idempotency: idempotencyKey, workspace: workspaceID });
  if (!uuidPattern.test(result)) throw new Error("question response omitted a canonical answer id");
  return result;
};

const waitForAnswer = async (page: Page, answerID: string): Promise<void> => {
  await expect.poll(async () => page.evaluate(async ({ answer, workspace }) => {
    const response = await fetch(`/api/v2/answers/${answer}?workspace_id=${workspace}`, { headers: { Accept: "application/json" } });
    const body = await response.json() as { publication_status?: unknown; workflow?: { status?: unknown }; error_code?: unknown };
		if (!response.ok) {
			const errorCode = typeof body.error_code === "string" ? body.error_code : "unknown";
			throw new Error(`answer returned ${String(response.status)} (${errorCode})`);
		}
    return `${String(body.publication_status)}:${String(body.workflow?.status)}`;
  }, { answer: answerID, workspace: workspaceID }), { timeout: 45_000 }).toBe("completed:succeeded");
};

const captureRequest = (request: Request, activationBodies: unknown[], saveSecretRequests: string[]): void => {
  const url = new URL(request.url());
  if (request.method() === "POST" && url.pathname === "/api/v1/settings/models/activations") {
    activationBodies.push(request.postDataJSON());
  }
  const body = request.postData() ?? "";
  if (body.includes(validAPIKey) || body.includes(rejectedAPIKey)) {
    expect(request.method()).toBe("PUT");
    expect(url.pathname).toBe("/api/v1/settings/models");
    saveSecretRequests.push(body.includes(validAPIKey) ? "valid" : "rejected");
  }
};

const captureResponse = (response: Response, observations: Promise<void>[]): void => {
  const url = new URL(response.url());
  if (!url.pathname.startsWith("/api/")) return;
  observations.push(response.text().then((body) => {
    expect(body).not.toContain(validAPIKey);
    expect(body).not.toContain(rejectedAPIKey);
    for (const instanceID of runtimeInstanceIDs) expect(body).not.toContain(instanceID);
  }));
};

const assertBrowserSecretsAbsent = async (page: Page): Promise<void> => {
  const surface = await page.evaluate(() => ({
    body: document.body.textContent,
    html: document.documentElement.outerHTML,
    local: JSON.stringify(window.localStorage),
    session: JSON.stringify(window.sessionStorage),
  }));
  const serialized = JSON.stringify(surface);
  expect(serialized).not.toContain(validAPIKey);
  expect(serialized).not.toContain(rejectedAPIKey);
  for (const instanceID of runtimeInstanceIDs) expect(serialized).not.toContain(instanceID);
};

test("模型配置在真实 API/Worker 容器内热应用，失败保持旧版本并可修正", async ({ browser, page }) => {
  test.setTimeout(180_000);
  const issues: string[] = [];
  const responseObservations: Promise<void>[] = [];
  const activationBodies: unknown[] = [];
  const saveSecretRequests: string[] = [];
  await installSession(page.context());
  page.on("console", (message) => {
    if (message.type() === "warning" || message.type() === "error") issues.push(`desktop.console.${message.type()}: ${message.text()}`);
  });
  page.on("pageerror", (error) => issues.push(`desktop.pageerror: ${error.message}`));
  page.on("request", (request) => captureRequest(request, activationBodies, saveSecretRequests));
  page.on("response", (response) => {
    const path = new URL(response.url()).pathname;
    const expectedNoWorkspace = path === "/api/v1/workspaces/active" && response.status() === 404;
    if (response.status() >= 400 && !expectedNoWorkspace) issues.push(`desktop.http: ${response.request().method()} ${String(response.status())} ${path}`);
    captureResponse(response, responseObservations);
  });

  await page.goto("/settings?section=models");
  expect(page.viewportSize()).toEqual({ width: 1440, height: 900 });
  await expect(page.getByRole("heading", { name: "模型与检索" })).toBeVisible();
  await assertNoHorizontalOverflow(page);

  const initial = await settingsFromServer(page);
  await replaceChatDraft(page, providerModelVersion, validAPIKey);
  await page.getByRole("button", { name: "保存并应用" }).click();
	const firstApplied = await waitForApplied(page, initial.active_revision);
	expect(firstApplied.active_revision).toBeGreaterThan(0);

	let gateArmed = false;
	let oldAnswerID = "";
	let newAnswerID = "";
	let recovered: SettingsProjection;
	try {
		expect(await fixtureControl("/control/block-next", "POST")).toEqual({ armed: true, entered: false });
		gateArmed = true;
		oldAnswerID = await submitQuestion(page, oldConversationID, "runtime-generation-old");
		await expect.poll(async () => fixtureControl("/control/state", "GET"), { timeout: 15_000 }).toEqual({ armed: true, entered: true });

			await replaceChatDraft(page, providerModelVersion, rejectedAPIKey);
		await page.getByRole("button", { name: "保存并应用" }).click();
		await expect(page.getByText("配置应用失败", { exact: true })).toBeVisible({ timeout: 45_000 });
		const failed = await settingsFromServer(page);
		expect(failed.desired_revision).toBeGreaterThan(firstApplied.active_revision);
		expect(failed.active_revision).toBe(firstApplied.active_revision);
		expect(failed.runtime.api.applied_revision).toBe(firstApplied.active_revision);
		expect(failed.runtime.worker.applied_revision).toBe(firstApplied.active_revision);
		expect(failed.rollout.phase).toBe("failed");
		expect(failed.apply_required).toBe(true);

			await replaceChatDraft(page, providerModelVersion, validAPIKey);
		await expect(page.getByRole("button", { name: "保存并应用" })).toBeEnabled();
		await page.getByRole("button", { name: "保存并应用" }).click();
		recovered = await waitForApplied(page, failed.desired_revision);
		expect(recovered.active_revision).toBeGreaterThan(failed.desired_revision);
		expect(await fixtureControl("/control/state", "GET")).toEqual({ armed: true, entered: true });

		newAnswerID = await submitQuestion(page, newConversationID, "runtime-generation-new");
		await waitForAnswer(page, newAnswerID);
		expect(await fixtureControl("/control/state", "GET")).toEqual({ armed: true, entered: true });
		expect(await fixtureControl("/control/release", "POST")).toEqual({ armed: false, entered: false });
		gateArmed = false;
		await waitForAnswer(page, oldAnswerID);
	} finally {
		if (gateArmed) await fixtureControl("/control/release", "POST").catch(() => undefined);
	}
	await mkdir(outputDirectory, { recursive: true });
	await writeFile(join(outputDirectory, "runtime-generation-proof.json"), `${JSON.stringify({
		old_answer_id: oldAnswerID,
		new_answer_id: newAnswerID,
		old_revision: firstApplied.active_revision,
		new_revision: recovered.active_revision,
		old_blocked_through_activation: true,
	})}\n`, { mode: 0o600 });

	await page.reload();
  await expect(page.getByText("API 与工作进程已应用当前生效版本。", { exact: true })).toBeVisible();
  expectApplied(await settingsFromServer(page), recovered.active_revision);
  await assertNoHorizontalOverflow(page);
  await assertBrowserSecretsAbsent(page);

  const mobileContext = await browser.newContext({ baseURL: appBaseURL, viewport: { width: 390, height: 844 } });
  await installSession(mobileContext);
  const mobile = await mobileContext.newPage();
  mobile.on("console", (message) => {
    if (message.type() === "warning" || message.type() === "error") issues.push(`mobile.console.${message.type()}: ${message.text()}`);
  });
  mobile.on("pageerror", (error) => issues.push(`mobile.pageerror: ${error.message}`));
  mobile.on("response", (response) => {
    const path = new URL(response.url()).pathname;
    const expectedNoWorkspace = path === "/api/v1/workspaces/active" && response.status() === 404;
    if (response.status() >= 400 && !expectedNoWorkspace) issues.push(`mobile.http: ${response.request().method()} ${String(response.status())} ${path}`);
    captureResponse(response, responseObservations);
  });
  try {
    await mobile.goto("/settings?section=models");
    expect(mobile.viewportSize()).toEqual({ width: 390, height: 844 });
    await expect(mobile.getByRole("heading", { name: "模型与检索" })).toBeVisible();
    await expect(mobile.getByText("API 与工作进程已应用当前生效版本。", { exact: true })).toBeVisible();
    await assertNoHorizontalOverflow(mobile);
    await assertBrowserSecretsAbsent(mobile);
  } finally {
    await mobileContext.close();
  }

  await Promise.all(responseObservations);
  expect(activationBodies).toHaveLength(3);
  for (const body of activationBodies) expect(body).toEqual({ expected_revision: expect.any(Number) });
  expect(saveSecretRequests).toEqual(["valid", "rejected", "valid"]);
  expect(issues, issues.join("\n")).toEqual([]);
});
