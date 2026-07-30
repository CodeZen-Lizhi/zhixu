import { expect, test, type BrowserContext, type Page } from "@playwright/test";
import { createHash } from "node:crypto";
import { execFile } from "node:child_process";
import { mkdtemp, readFile, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { promisify } from "node:util";

const execFileAsync = promisify(execFile);
const workspaceStorageKey = "zhixu.active-workspace-id";
const eventCursorStorageKey = (workspaceId: string): string => `zhixu.event-cursor.${workspaceId}`;
const uuidPattern = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;

const requiredEnvironment = (name: string): string => {
  const value = process.env[name]?.trim();
  if (value === undefined || !uuidPattern.test(value)) throw new Error(`${name} must be a canonical UUID`);
  return value;
};

const requiredTextEnvironment = (name: string): string => {
  const value = process.env[name]?.trim();
  if (value === undefined || value === "") throw new Error(`${name} is required`);
  return value;
};

const fixture = {
  workspaceId: requiredEnvironment("ZHIXU_ATTACHMENT_EXPORT_SMOKE_WORKSPACE_ID"),
  secondaryWorkspaceId: requiredEnvironment("ZHIXU_ATTACHMENT_EXPORT_SMOKE_SECONDARY_WORKSPACE_ID"),
  collectionId: requiredEnvironment("ZHIXU_ATTACHMENT_EXPORT_SMOKE_COLLECTION_ID"),
  recoveryExportId: requiredEnvironment("ZHIXU_ATTACHMENT_EXPORT_SMOKE_RECOVERY_EXPORT_ID"),
  failedExportId: requiredEnvironment("ZHIXU_ATTACHMENT_EXPORT_SMOKE_FAILED_EXPORT_ID"),
  expiredExportId: requiredEnvironment("ZHIXU_ATTACHMENT_EXPORT_SMOKE_EXPIRED_EXPORT_ID"),
  mutationExportId: requiredEnvironment("ZHIXU_ATTACHMENT_EXPORT_SMOKE_MUTATION_EXPORT_ID"),
  tamperedExportId: requiredEnvironment("ZHIXU_ATTACHMENT_EXPORT_SMOKE_TAMPERED_EXPORT_ID"),
  authBaseUrl: requiredTextEnvironment("ZHIXU_ATTACHMENT_EXPORT_SMOKE_AUTH_BASE_URL"),
  deniedToken: requiredTextEnvironment("ZHIXU_ATTACHMENT_EXPORT_SMOKE_DENIED_TOKEN"),
};

interface AttachmentJob {
  id: string;
  status: string;
  archiveSHA256: string | null;
  archiveSize: number | null;
  manifestSHA256: string | null;
  entryCount: number | null;
  totalUncompressedBytes: number | null;
  errorCode: string | null;
  downloadCount: number;
}

const expectedFiles = new Map<string, Buffer>([
  ["alpha.txt", Buffer.from("Attachment export browser fixture\n", "utf8")],
  ["nested/binary.bin", Buffer.from([0, 1, 2, 3, 250, 251, 252, 253, 254, 255, 0])],
]);

const isRecord = (value: unknown): value is Record<string, unknown> => typeof value === "object" && value !== null && !Array.isArray(value);
const nullableString = (value: unknown, field: string): string | null => value === null || value === undefined ? null : typeof value === "string" ? value : (() => { throw new Error(`Malformed ${field}`); })();
const nullableInteger = (value: unknown, field: string): number | null => value === null || value === undefined ? null : typeof value === "number" && Number.isSafeInteger(value) && value >= 0 ? value : (() => { throw new Error(`Malformed ${field}`); })();
const requiredInteger = (value: unknown, field: string): number => {
  const decoded = nullableInteger(value, field);
  if (decoded === null) throw new Error(`Malformed ${field}`);
  return decoded;
};

const readJob = (value: unknown): AttachmentJob => {
  if (!isRecord(value) || typeof value.id !== "string" || !uuidPattern.test(value.id) || typeof value.status !== "string") throw new Error("Malformed attachment export job");
  return {
    id: value.id,
    status: value.status,
    archiveSHA256: nullableString(value.archive_sha256, "archive_sha256"),
    archiveSize: nullableInteger(value.archive_size, "archive_size"),
    manifestSHA256: nullableString(value.manifest_sha256, "manifest_sha256"),
    entryCount: nullableInteger(value.entry_count, "entry_count"),
    totalUncompressedBytes: nullableInteger(value.total_uncompressed_bytes, "total_uncompressed_bytes"),
    errorCode: nullableString(value.error_code, "error_code"),
    downloadCount: requiredInteger(value.download_count, "download_count"),
  };
};

interface BrowserJSONResponse { status: number; body: unknown; }
interface BrowserJSONRequest { method?: "GET" | "POST"; headers?: Record<string, string>; body?: string; }

const browserJSONRequest = async (page: Page, url: string, request: BrowserJSONRequest = {}): Promise<BrowserJSONResponse> => page.evaluate(async ({ target, init }) => {
  const options: RequestInit = { method: init.method ?? "GET" };
  if (init.headers !== undefined) options.headers = init.headers;
  if (init.body !== undefined) options.body = init.body;
  const response = await fetch(target, options);
  const text = await response.text();
  let body: unknown;
  try { body = JSON.parse(text) as unknown; } catch { throw new Error(`Expected JSON from ${target}, received ${String(response.status)}`); }
  return { status: response.status, body };
}, { target: url, init: request });

const expectBrowserError = (response: BrowserJSONResponse, status: number, code: string): void => {
  expect(response.status).toBe(status);
  if (!isRecord(response.body)) throw new Error("Malformed API error response");
  expect(response.body.error_code).toBe(code);
};

const installWorkspace = async (context: BrowserContext): Promise<void> => {
  await context.addInitScript(({ key, workspaceId }) => window.localStorage.setItem(key, workspaceId), { key: workspaceStorageKey, workspaceId: fixture.workspaceId });
};

const captureRuntimeIssues = (page: Page, issues: string[]): void => {
  page.on("console", (message) => { if (message.type() === "warning" || message.type() === "error") issues.push(`console.${message.type()}: ${message.text()}`); });
  page.on("pageerror", (error) => issues.push(`pageerror: ${error.message}`));
  page.on("requestfailed", (request) => { if (new URL(request.url()).pathname !== "/api/v1/events" && request.failure()?.errorText !== "net::ERR_ABORTED") issues.push(`requestfailed: ${request.method()} ${request.url()}`); });
};

const listJobs = async (page: Page): Promise<AttachmentJob[]> => {
  const response = await page.request.get(`/api/v1/workspaces/${fixture.workspaceId}/attachment-exports?limit=100`);
  expect(response.status()).toBe(200);
  const body: unknown = await response.json();
  if (!isRecord(body) || !Array.isArray(body.items)) throw new Error("Malformed attachment export list");
  return body.items.map(readJob);
};

const getJob = async (page: Page, exportId: string): Promise<AttachmentJob> => {
  const response = await page.request.get(`/api/v1/workspaces/${fixture.workspaceId}/attachment-exports/${exportId}`);
  expect(response.status()).toBe(200);
  return readJob(await response.json() as unknown);
};

const createAttachmentExportOutsideTheUI = async (page: Page): Promise<AttachmentJob> => {
  const response = await page.request.post(`/api/v1/workspaces/${fixture.workspaceId}/attachment-exports`, {
    headers: { "Content-Type": "application/json", "Idempotency-Key": `attachment-export-browser-sse-${crypto.randomUUID()}` },
    data: { kind: "ATTACHMENTS_ZIP", schema_version: "attachment-export/v1", attachment_root_contract_version: "workspace-attachments/v1", content_policy: "RAW_USER_OWNED" },
  });
  expect(response.status()).toBe(202);
  const body: unknown = await response.json();
  if (!isRecord(body)) throw new Error("Malformed attachment export create response");
  return readJob(body.job);
};

const isRequestForPath = (request: { url: () => string }, path: string): boolean => new URL(request.url()).pathname === path;

const waitForNewJob = async (page: Page, existing: Set<string>): Promise<AttachmentJob> => {
  let job: AttachmentJob | undefined;
  await expect.poll(async () => {
    job = (await listJobs(page)).find((candidate) => !existing.has(candidate.id));
    return job?.id ?? "";
  }, { timeout: 60_000, intervals: [250, 500, 1_000] }).toMatch(uuidPattern);
  if (job === undefined) throw new Error("Attachment export was not created");
  return job;
};

const waitForStatus = async (page: Page, exportId: string, expected: string): Promise<AttachmentJob> => {
  let job: AttachmentJob | undefined;
  await expect.poll(async () => {
    job = await getJob(page, exportId);
    return job.status;
  }, { timeout: 60_000, intervals: [250, 500, 1_000] }).toBe(expected);
  if (job === undefined) throw new Error(`Attachment export ${exportId} was not returned`);
  return job;
};

const openAttachmentPanel = async (page: Page): Promise<void> => {
  await page.goto("/settings");
  await page.getByRole("tab", { name: "数据导出" }).click();
  await expect(page.getByRole("heading", { name: "Workspace 附件导出" })).toBeVisible();
};

const assertNoHorizontalOverflow = async (page: Page): Promise<void> => {
  const widths = await page.evaluate(() => ({
    viewport: window.innerWidth,
    document: Math.max(document.documentElement.scrollWidth, document.body.scrollWidth),
    panel: document.querySelector<HTMLElement>(".attachment-export-panel")?.scrollWidth ?? 0,
  }));
  expect(widths.document).toBeLessThanOrEqual(widths.viewport + 1);
  expect(widths.panel).toBeLessThanOrEqual(widths.viewport + 1);
};

const verifyDownloadedArchive = async (archivePath: string, job: AttachmentJob): Promise<void> => {
  if (job.archiveSHA256 === null || job.archiveSize === null || job.manifestSHA256 === null || job.entryCount === null || job.totalUncompressedBytes === null) throw new Error("Succeeded attachment export lacks frozen result facts");
  const archiveBytes = await readFile(archivePath);
  expect(archiveBytes.byteLength).toBe(job.archiveSize);
  expect(createHash("sha256").update(archiveBytes).digest("hex")).toBe(job.archiveSHA256);
  const extraction = await mkdtemp(join(tmpdir(), "zhixu-attachment-export-"));
  try {
    const entries = (await execFileAsync("unzip", ["-Z", "-1", archivePath])).stdout.trim().split("\n");
    expect(entries).toEqual(["manifest.json", "attachments/alpha.txt", "attachments/nested/binary.bin"]);
    await execFileAsync("unzip", ["-qq", archivePath, "-d", extraction]);
    const manifestBytes = await readFile(join(extraction, "manifest.json"));
    expect(createHash("sha256").update(manifestBytes).digest("hex")).toBe(job.manifestSHA256);
    const manifest: unknown = JSON.parse(manifestBytes.toString("utf8"));
    if (!isRecord(manifest) || !Array.isArray(manifest.entries)) throw new Error("Attachment manifest is malformed");
    expect(manifest).toMatchObject({
      schema_version: "attachment-export/v1",
      workspace_id: fixture.workspaceId,
      attachment_root_contract_version: "workspace-attachments/v1",
      entry_count: expectedFiles.size,
      total_uncompressed_bytes: [...expectedFiles.values()].reduce((total, value) => total + value.byteLength, 0),
    });
    expect(job.entryCount).toBe(expectedFiles.size);
    expect(job.totalUncompressedBytes).toBe([...expectedFiles.values()].reduce((total, value) => total + value.byteLength, 0));
    expect(manifest.entries).toEqual([...expectedFiles.entries()].map(([path, bytes]) => ({ path, sha256: createHash("sha256").update(bytes).digest("hex"), size: bytes.byteLength })));
    for (const [path, expected] of expectedFiles) expect(await readFile(join(extraction, "attachments", path))).toEqual(expected);
  } finally {
    await rm(extraction, { recursive: true, force: true });
  }
};

test("浏览器网络边界拒绝越权、跨 Workspace 与不可下载结果", async ({ browser, page }) => {
  await page.goto("/");
  const attachmentBase = `/api/v1/workspaces/${fixture.workspaceId}/attachment-exports`;
  const secondBase = `/api/v1/workspaces/${fixture.secondaryWorkspaceId}/attachment-exports`;

  const secondList = await browserJSONRequest(page, `${secondBase}?limit=100`);
  expect(secondList.status).toBe(200);
  expect(secondList.body).toMatchObject({ workspace_id: fixture.secondaryWorkspaceId, scope_kind: "WORKSPACE_ATTACHMENTS", items: [] });
  expectBrowserError(await browserJSONRequest(page, `${secondBase}/${fixture.recoveryExportId}`), 404, "EXPORT_NOT_FOUND");
  expectBrowserError(await browserJSONRequest(page, `${secondBase}/${fixture.recoveryExportId}/download`), 404, "EXPORT_NOT_FOUND");

  for (const expected of [
    { id: fixture.failedExportId, code: "EXPORT_FILE_PATH_UNSAFE" },
    { id: fixture.mutationExportId, code: "EXPORT_RESULT_INCONSISTENT" },
  ]) {
    const detail = await browserJSONRequest(page, `${attachmentBase}/${expected.id}`);
    expect(detail.status).toBe(200);
    const job = readJob(detail.body);
    expect(job).toMatchObject({ status: "FAILED", errorCode: expected.code });
    expectBrowserError(await browserJSONRequest(page, `${attachmentBase}/${expected.id}/download`), 409, "EXPORT_RESULT_NOT_READY");
  }

  const expired = await browserJSONRequest(page, `${attachmentBase}/${fixture.expiredExportId}`);
  expect(expired.status).toBe(200);
  expect(readJob(expired.body).status).toBe("EXPIRED");
  expectBrowserError(await browserJSONRequest(page, `${attachmentBase}/${fixture.expiredExportId}/download`), 410, "EXPORT_EXPIRED");

  const tamperedBefore = readJob((await browserJSONRequest(page, `${attachmentBase}/${fixture.tamperedExportId}`)).body);
  expectBrowserError(await browserJSONRequest(page, `${attachmentBase}/${fixture.tamperedExportId}/download`), 500, "EXPORT_RESULT_INCONSISTENT");
  const tamperedAfter = readJob((await browserJSONRequest(page, `${attachmentBase}/${fixture.tamperedExportId}`)).body);
  expect(tamperedAfter.downloadCount).toBe(tamperedBefore.downloadCount);

  const authContext = await browser.newContext();
  const authPage = await authContext.newPage();
  try {
    await authPage.goto(`${fixture.authBaseUrl}/readyz`);
    const authorization = { Authorization: `Bearer ${fixture.deniedToken}`, Accept: "application/json" };
    const createBody = JSON.stringify({ kind: "ATTACHMENTS_ZIP", schema_version: "attachment-export/v1", attachment_root_contract_version: "workspace-attachments/v1", content_policy: "RAW_USER_OWNED" });
    expectBrowserError(await browserJSONRequest(authPage, `${fixture.authBaseUrl}${attachmentBase}`, { method: "POST", headers: { ...authorization, "Content-Type": "application/json", "Idempotency-Key": "attachment-export-browser-playwright-denied" }, body: createBody }), 403, "AUTH_CAPABILITY_DENIED");
    expectBrowserError(await browserJSONRequest(authPage, `${fixture.authBaseUrl}${attachmentBase}?limit=100`, { headers: authorization }), 403, "AUTH_CAPABILITY_DENIED");
    expectBrowserError(await browserJSONRequest(authPage, `${fixture.authBaseUrl}${attachmentBase}/${fixture.recoveryExportId}`, { headers: authorization }), 403, "AUTH_CAPABILITY_DENIED");
    expectBrowserError(await browserJSONRequest(authPage, `${fixture.authBaseUrl}${attachmentBase}/${fixture.recoveryExportId}/download`, { headers: authorization }), 403, "AUTH_CAPABILITY_DENIED");
  } finally {
    await authContext.close();
  }
});

test("真实 Workspace 附件导出可恢复、下载并保持桌面与移动布局", async ({ browser, page }) => {
  const runtimeIssues: string[] = [];
  await installWorkspace(page.context());
  captureRuntimeIssues(page, runtimeIssues);
  const attachmentEvents = page.waitForRequest((request) => isRequestForPath(request, "/api/v1/events"));
  await openAttachmentPanel(page);
  const initialJobs = await listJobs(page);
  expect(initialJobs.map((job) => job.id)).toEqual(expect.arrayContaining([fixture.recoveryExportId, fixture.failedExportId, fixture.expiredExportId]));
  await attachmentEvents;

  const collectionContext = await browser.newContext();
  await installWorkspace(collectionContext);
  const collectionPage = await collectionContext.newPage();
  captureRuntimeIssues(collectionPage, runtimeIssues);
  let attachmentListRequests = 0;
  let collectionExportListRequests = 0;
  const attachmentListPath = `/api/v1/workspaces/${fixture.workspaceId}/attachment-exports`;
  const collectionExportListPath = `/api/v1/workspaces/${fixture.workspaceId}/exports`;
  page.on("request", (request) => {
    if (request.method() === "GET" && isRequestForPath(request, attachmentListPath)) attachmentListRequests += 1;
  });
  collectionPage.on("request", (request) => {
    if (request.method() === "GET" && isRequestForPath(request, collectionExportListPath)) collectionExportListRequests += 1;
  });
  try {
    const collectionEvents = collectionPage.waitForRequest((request) => isRequestForPath(request, "/api/v1/events"));
    await collectionPage.goto(`/collections/${fixture.collectionId}`);
    await expect(collectionPage.getByRole("heading", { name: "Collection Export", exact: true })).toBeVisible();
    await collectionEvents;
    const collectionCursorBefore = await collectionPage.evaluate((key) => window.sessionStorage.getItem(key), eventCursorStorageKey(fixture.workspaceId));
    attachmentListRequests = 0;
    collectionExportListRequests = 0;
    const eventCreated = await createAttachmentExportOutsideTheUI(page);
    await expect.poll(() => attachmentListRequests, { timeout: 10_000, intervals: [100, 250, 500] }).toBeGreaterThan(0);
    await expect(page.locator(".attachment-export-job", { hasText: eventCreated.id.slice(0, 8) })).toBeVisible();
    await expect.poll(
      () => collectionPage.evaluate((key) => window.sessionStorage.getItem(key), eventCursorStorageKey(fixture.workspaceId)),
      { timeout: 10_000, intervals: [100, 250, 500] },
    ).not.toBe(collectionCursorBefore);
    expect(collectionExportListRequests).toBe(0);
  } finally {
    await collectionContext.close();
  }

  const beforeCreate = new Set((await listJobs(page)).map((job) => job.id));
  const create = page.getByRole("button", { name: "创建附件导出" });
  await create.focus();
  await expect(create).toBeFocused();
  await page.keyboard.press("Enter");
  const created = await waitForNewJob(page, beforeCreate);
  await expect(page.getByText(/等待处理|正在归档|可以下载/).first()).toBeVisible();
  await page.reload();
  await page.getByRole("tab", { name: "数据导出" }).click();
  const succeeded = await waitForStatus(page, created.id, "SUCCEEDED");
  const createdRow = page.locator(".attachment-export-job", { hasText: created.id.slice(0, 8) });
  const downloadButton = createdRow.getByRole("button", { name: "下载 ZIP" });
  await expect(downloadButton).toBeVisible();
  const [download] = await Promise.all([page.waitForEvent("download"), downloadButton.click()]);
  expect(download.suggestedFilename()).toBe(`workspace-attachments-${created.id}.zip`);
  const downloadPath = await download.path();
  await verifyDownloadedArchive(downloadPath, succeeded);

  await page.getByRole("button", { name: "刷新附件导出历史" }).click();
  const failedRow = page.locator(".attachment-export-job", { hasText: fixture.failedExportId.slice(0, 8) });
  const expiredRow = page.locator(".attachment-export-job", { hasText: fixture.expiredExportId.slice(0, 8) });
  await expect(failedRow.getByText("归档失败")).toBeVisible();
  await expect(expiredRow.getByText("结果已过期")).toBeVisible();
  const beforeFailedRetry = new Set((await listJobs(page)).map((job) => job.id));
  await failedRow.getByRole("button", { name: "新建附件导出" }).click();
  const failedRetry = await waitForNewJob(page, beforeFailedRetry);
  expect(failedRetry.id).not.toBe(fixture.failedExportId);
  await waitForStatus(page, failedRetry.id, "SUCCEEDED");
  const beforeExpiredRetry = new Set((await listJobs(page)).map((job) => job.id));
  await expiredRow.getByRole("button", { name: "新建附件导出" }).click();
  const expiredRetry = await waitForNewJob(page, beforeExpiredRetry);
  expect(expiredRetry.id).not.toBe(fixture.expiredExportId);
  await waitForStatus(page, expiredRetry.id, "SUCCEEDED");
  await assertNoHorizontalOverflow(page);

  const mobile = await browser.newContext({ viewport: { width: 390, height: 844 } });
  await installWorkspace(mobile);
  const mobilePage = await mobile.newPage();
  captureRuntimeIssues(mobilePage, runtimeIssues);
  try {
    await openAttachmentPanel(mobilePage);
    await expect(mobilePage.getByRole("button", { name: "创建附件导出" })).toBeVisible();
    await assertNoHorizontalOverflow(mobilePage);
  } finally {
    await mobile.close();
  }
  expect(runtimeIssues, runtimeIssues.join("\n")).toEqual([]);
});
