import { expect, test, type BrowserContext, type Page, type Response } from "@playwright/test";
import { createHash } from "node:crypto";
import { readFile } from "node:fs/promises";

const workspaceStorageKey = "zhixu.active-workspace-id";
const uuidPattern = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;

const requiredEnvironment = (name: string): string => {
  const value = process.env[name]?.trim();
  if (value === undefined || !uuidPattern.test(value)) throw new Error(`${name} must be a canonical UUID`);
  return value;
};
const fixture = {
  workspaceId: requiredEnvironment("ZHIXU_EXPORT_SMOKE_WORKSPACE_ID"),
  collectionId: requiredEnvironment("ZHIXU_EXPORT_SMOKE_COLLECTION_ID"),
  failedExportId: requiredEnvironment("ZHIXU_EXPORT_SMOKE_FAILED_EXPORT_ID"),
  expiredExportId: requiredEnvironment("ZHIXU_EXPORT_SMOKE_EXPIRED_EXPORT_ID"),
};

const collectionPath = `/collections/${fixture.collectionId}`;
const installWorkspace = (context: BrowserContext): void => {
  void context.addInitScript(({ key, workspaceId }) => { window.localStorage.setItem(key, workspaceId); }, { key: workspaceStorageKey, workspaceId: fixture.workspaceId });
};
const captureRuntimeIssues = (page: Page, issues: string[]): void => {
  page.on("console", (message) => { if (message.type() === "warning" || message.type() === "error") issues.push(`console.${message.type()}: ${message.text()}`); });
  page.on("pageerror", (error) => issues.push(`pageerror: ${error.message}`));
};
const assertNoHorizontalOverflow = async (page: Page): Promise<void> => {
  const widths = await page.evaluate(() => ({ viewport: window.innerWidth, document: Math.max(document.documentElement.scrollWidth, document.body.scrollWidth), panel: document.querySelector<HTMLElement>(".collection-export-panel")?.scrollWidth ?? 0 }));
  expect(widths.document).toBeLessThanOrEqual(widths.viewport + 1);
  expect(widths.panel).toBeLessThanOrEqual(widths.viewport + 1);
};
interface ExportJobProjection { id: string; status: string; kind: string; fileHash: string | null; fileSize: number; readModelRevision: string | null; exactCount: number | null; }
const isRecord = (value: unknown): value is Record<string, unknown> => typeof value === "object" && value !== null && !Array.isArray(value);
const collectionBinding = (value: unknown): { version: number; queryHash: string } => {
  if (!isRecord(value) || typeof value.version !== "number" || !Number.isSafeInteger(value.version) || value.version < 1 || typeof value.query_hash !== "string" || !/^[0-9a-f]{64}$/.test(value.query_hash)) throw new Error("Export smoke received malformed Collection binding");
  return { version: value.version, queryHash: value.query_hash };
};
const readExportJob = (value: unknown): ExportJobProjection => {
  if (!isRecord(value) || typeof value.id !== "string" || typeof value.status !== "string" || typeof value.kind !== "string" || typeof value.file_size !== "number" || !Number.isSafeInteger(value.file_size) || value.file_size < 0) throw new Error("Export smoke received malformed Job projection");
  const fileHash = value.file_hash === undefined || value.file_hash === null ? null : typeof value.file_hash === "string" ? value.file_hash : (() => { throw new Error("Export smoke received malformed file_hash"); })();
  const readModelRevision = value.read_model_revision === undefined || value.read_model_revision === null ? null : typeof value.read_model_revision === "string" ? value.read_model_revision : (() => { throw new Error("Export smoke received malformed read_model_revision"); })();
  const exactCount = value.exact_count === undefined || value.exact_count === null ? null : typeof value.exact_count === "number" && Number.isSafeInteger(value.exact_count) && value.exact_count >= 0 ? value.exact_count : (() => { throw new Error("Export smoke received malformed exact_count"); })();
  return { id: value.id, status: value.status, kind: value.kind, fileHash, fileSize: value.file_size, readModelRevision, exactCount };
};
const getExportJob = async (page: Page, exportId: string): Promise<ExportJobProjection> => {
  const response = await page.request.get(`/api/v1/exports/${exportId}?workspace_id=${fixture.workspaceId}`);
  expect(response.status()).toBe(200);
  return readExportJob(await response.json() as unknown);
};
const listExports = async (page: Page): Promise<ExportJobProjection[]> => {
  const response = await page.request.get(`/api/v1/workspaces/${fixture.workspaceId}/exports?collection_id=${fixture.collectionId}&limit=25`);
  expect(response.status()).toBe(200);
  const body: unknown = await response.json();
  if (!isRecord(body) || !Array.isArray(body.items)) throw new Error("Export smoke received malformed list projection");
  return body.items.map(readExportJob);
};
const waitForNewExport = async (page: Page, existingIds: Set<string>, label: string): Promise<ExportJobProjection> => {
  let created: ExportJobProjection | undefined;
  await expect.poll(async () => {
    created = (await listExports(page)).find((job) => !existingIds.has(job.id));
    return created?.id ?? "";
  }, { timeout: 60_000, intervals: [250, 500, 1_000] }).toMatch(uuidPattern);
  if (created === undefined) throw new Error(`${label} Export was not created`);
  return created;
};
const waitForExportStatus = async (page: Page, exportId: string, expected: string): Promise<ExportJobProjection> => {
  let current: ExportJobProjection | undefined;
  await expect.poll(async () => {
    current = await getExportJob(page, exportId);
    return current.status;
  }, { timeout: 60_000, intervals: [250, 500, 1_000] }).toBe(expected);
  if (current === undefined) throw new Error(`Export ${exportId} did not return a status`);
  return current;
};
const isCollectionExportListResponse = (response: Response): boolean => {
  const url = new URL(response.url());
  return response.request().method() === "GET" && url.pathname === `/api/v1/workspaces/${fixture.workspaceId}/exports` && url.searchParams.get("collection_id") === fixture.collectionId;
};
const assertSafeExportText = (text: string): void => {
  expect(text).not.toContain("graph integration provenance");
  expect(text).not.toMatch(/(?:api[_-]?key|access[_-]?token|password|secret)\s*[:=]/i);
  expect(text).not.toContain("/tmp/graph-http-integration-");
  expect(text).not.toMatch(/(?:^|\s)\/(?:Users|home|tmp)\//);
};

test("真实 Collection Export 创建、刷新恢复、下载和桌面/移动状态", async ({ browser, page }) => {
  const runtimeIssues: string[] = [];
  installWorkspace(page.context());
  captureRuntimeIssues(page, runtimeIssues);
  const sseConnection = page.waitForResponse((response) => new URL(response.url()).pathname === "/api/v1/events" && response.headers()["content-type"]?.toLowerCase().startsWith("text/event-stream") === true);
  await page.goto(collectionPath);
  await expect(page.getByRole("heading", { name: "Collection Export", exact: true })).toBeVisible();
  await sseConnection;
  await page.getByRole("button", { name: "创建 Markdown 导出" }).focus();
  await expect(page.getByRole("button", { name: "创建 Markdown 导出" })).toBeFocused();
  await page.keyboard.press("Enter");
  await expect(page.getByText(/等待处理|正在生成|可以下载/).first()).toBeVisible();
  await page.reload();
  await expect(page.getByRole("heading", { name: "Collection Export", exact: true })).toBeVisible();
  const download = page.getByRole("button", { name: "下载结果" }).first();
  await expect(download).toBeVisible({ timeout: 60_000 });
  const downloaded = await Promise.all([page.waitForEvent("download"), download.click()]);
  const downloadedName = downloaded[0].suggestedFilename();
  const downloadedID = new RegExp(`^collection-${fixture.collectionId}-(${uuidPattern.source.slice(1, -1)})\\.md$`).exec(downloadedName)?.[1];
  if (downloadedID === undefined) throw new Error(`Export smoke received unexpected Markdown filename: ${downloadedName}`);
  const downloadedPath = await downloaded[0].path();
  const jobs = await listExports(page);
  const successfulMarkdown = jobs.find((job) => job.id === downloadedID);
  if (successfulMarkdown === undefined) throw new Error("Export smoke download Job is absent from the Collection history");
  if (successfulMarkdown.status !== "SUCCEEDED" || successfulMarkdown.kind !== "MARKDOWN") throw new Error("Export smoke download Job is not a successful Markdown Export");
  if (successfulMarkdown.fileHash === null || successfulMarkdown.readModelRevision === null || successfulMarkdown.exactCount === null) throw new Error("Export smoke Markdown Job lacks frozen result facts");
  const successfulDetail = await getExportJob(page, successfulMarkdown.id);
  const exportedBytes = await readFile(downloadedPath);
  expect(exportedBytes.byteLength).toBe(successfulDetail.fileSize);
  expect(createHash("sha256").update(exportedBytes).digest("hex")).toBe(successfulDetail.fileHash);
  const exportedText = exportedBytes.toString("utf8");
  expect(exportedText).toContain("export/v1");
  expect(exportedText).toContain(fixture.workspaceId);
  expect(exportedText).toContain(fixture.collectionId);
  expect(exportedText).toContain(successfulDetail.readModelRevision);
  expect(exportedText).toContain(String(successfulDetail.exactCount));
  expect(exportedText).toContain("# '=Collection Export Browser Smoke");
  expect(exportedText).not.toContain("# =Collection Export Browser Smoke");
  assertSafeExportText(exportedText);

  const existingJSONIds = new Set((await listExports(page)).map((job) => job.id));
  await page.getByLabel("导出格式").selectOption("METADATA_JSON");
  const createMetadata = page.getByRole("button", { name: "创建 领域元数据 JSON 导出" });
  await expect(createMetadata).toBeVisible();
  await createMetadata.click();
  const metadataJob = await waitForNewExport(page, existingJSONIds, "metadata JSON");
  expect(metadataJob.kind).toBe("METADATA_JSON");
  const successfulMetadata = await waitForExportStatus(page, metadataJob.id, "SUCCEEDED");
  if (successfulMetadata.fileHash === null || successfulMetadata.readModelRevision === null || successfulMetadata.exactCount === null) throw new Error("Export smoke Metadata JSON Job lacks frozen result facts");
  const metadataHistory = page.locator(".collection-export-job", { hasText: metadataJob.id.slice(0, 8) });
  const metadataDownload = metadataHistory.getByRole("button", { name: "下载结果" });
  await expect(metadataDownload).toBeVisible();
  const [metadataResponse, metadataFile] = await Promise.all([
    page.waitForResponse((response) => new URL(response.url()).pathname === `/api/v1/exports/${metadataJob.id}/download`),
    page.waitForEvent("download"),
    metadataDownload.click(),
  ]);
  expect(metadataResponse.status()).toBe(200);
  expect(metadataResponse.headers()["content-type"]).toBe("application/json");
  expect(metadataFile.suggestedFilename()).toBe(`collection-${fixture.collectionId}-${metadataJob.id}.json`);
  const metadataBytes = await readFile(await metadataFile.path());
  expect(metadataBytes.byteLength).toBe(successfulMetadata.fileSize);
  expect(createHash("sha256").update(metadataBytes).digest("hex")).toBe(successfulMetadata.fileHash);
  const metadataText = metadataBytes.toString("utf8");
  const metadataEnvelope: unknown = JSON.parse(metadataText);
  if (!isRecord(metadataEnvelope)) throw new Error("Export smoke Metadata JSON result is not an object");
  expect(metadataEnvelope).toMatchObject({
    schema_version: "export/v1",
    workspace_id: fixture.workspaceId,
    collection_id: fixture.collectionId,
    kind: "METADATA_JSON",
    read_model_revision: successfulMetadata.readModelRevision,
    exact_count: successfulMetadata.exactCount,
  });
  assertSafeExportText(metadataText);

  const collectionResponse = await page.request.get(`/api/v1/collections/${fixture.collectionId}?workspace_id=${fixture.workspaceId}`);
  expect(collectionResponse.status()).toBe(200);
  const binding = collectionBinding(await collectionResponse.json() as unknown);
  let sseRefreshStartedAt = 0;
  const sseRefresh = page.waitForResponse(async (response) => {
    if (!isCollectionExportListResponse(response) || Date.now() - sseRefreshStartedAt > 1_500) return false;
    const body: unknown = await response.json();
    return isRecord(body) && Array.isArray(body.items) && body.items.some((item) => isRecord(item) && item.kind === "METADATA_JSON" && Array.isArray(item.fields) && item.fields.length === 2 && item.fields[0] === "object_type" && item.fields[1] === "id");
  });
  sseRefreshStartedAt = Date.now();
  const sseCreate = await page.request.post("/api/v1/exports", {
    headers: { "Content-Type": "application/json", "Idempotency-Key": "export-browser-sse-refresh" },
    data: { workspace_id: fixture.workspaceId, collection_id: fixture.collectionId, collection_version: binding.version, query_hash: binding.queryHash, kind: "METADATA_JSON", fields: ["object_type", "id"], redaction_policy: "MASKED" },
  });
  expect(sseCreate.status()).toBe(202);
  await sseRefresh;
  await assertNoHorizontalOverflow(page);

  expect((await getExportJob(page, fixture.failedExportId)).status).toBe("FAILED");
  expect((await getExportJob(page, fixture.expiredExportId)).status).toBe("EXPIRED");
  await page.getByRole("button", { name: "刷新导出历史" }).click();
  const failedHistory = page.locator(".collection-export-job", { hasText: fixture.failedExportId.slice(0, 8) });
  const expiredHistory = page.locator(".collection-export-job", { hasText: fixture.expiredExportId.slice(0, 8) });
  await expect(failedHistory.getByText("生成失败")).toBeVisible();
  await expect(expiredHistory.getByText("结果已过期")).toBeVisible();
  await expect(failedHistory.getByRole("button", { name: "新建导出" })).toBeVisible();
  await expect(expiredHistory.getByRole("button", { name: "新建导出" })).toBeVisible();
  const beforeFailedRetry = new Set((await listExports(page)).map((job) => job.id));
  await failedHistory.getByRole("button", { name: "新建导出" }).click();
  const failedRetry = await waitForNewExport(page, beforeFailedRetry, "failed retry");
  expect(failedRetry.id).not.toBe(fixture.failedExportId);
  expect(failedRetry.id).not.toBe(fixture.expiredExportId);
  expect((await waitForExportStatus(page, failedRetry.id, "SUCCEEDED")).kind).toBe("MARKDOWN");

  const beforeExpiredRetry = new Set((await listExports(page)).map((job) => job.id));
  await expiredHistory.getByRole("button", { name: "新建导出" }).click();
  const expiredRetry = await waitForNewExport(page, beforeExpiredRetry, "expired retry");
  expect(expiredRetry.id).not.toBe(fixture.failedExportId);
  expect(expiredRetry.id).not.toBe(fixture.expiredExportId);
  expect(expiredRetry.id).not.toBe(failedRetry.id);
  expect((await waitForExportStatus(page, expiredRetry.id, "SUCCEEDED")).kind).toBe("MARKDOWN");

  const historyAfterRetries = await listExports(page);
  expect(historyAfterRetries.map((job) => job.id)).toEqual(expect.arrayContaining([
    fixture.failedExportId,
    fixture.expiredExportId,
    failedRetry.id,
    expiredRetry.id,
  ]));
  expect(historyAfterRetries.length).toBeGreaterThanOrEqual(beforeFailedRetry.size + 2);

  const mobile = await browser.newContext({ viewport: { width: 390, height: 844 } });
  installWorkspace(mobile);
  const mobilePage = await mobile.newPage();
  captureRuntimeIssues(mobilePage, runtimeIssues);
  try {
    await mobilePage.goto(collectionPath);
    await expect(mobilePage.getByRole("heading", { name: "Collection Export" })).toBeVisible();
    await expect(mobilePage.getByRole("button", { name: /创建 .* 导出/ })).toBeVisible();
    await assertNoHorizontalOverflow(mobilePage);
  } finally {
    await mobile.close();
  }
  expect(runtimeIssues, runtimeIssues.join("\n")).toEqual([]);
});
