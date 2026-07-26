import { expect, test, type BrowserContext, type Page } from "@playwright/test";

const workspaceStorageKey = "zhixu.active-workspace-id";
const uuidPattern = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;
const artifactTitle = "Approved recovery replays durable facts without duplicating provider work.";
const artifactScope = "Approved recovery replays durable facts without duplicating provider work.";
const coveredContent = "Approved recovery replays durable facts without duplicating provider work.";
const coveredContentHash = "5132f3a91cdf35b71bca4b61ff1ada5a9a5bb63318efd4b0353821ba42cc536c";
const gapDescription = "已批准知识不足，保留人工缺口供后续审阅。";
const revisedGapDescription = "修订后仍缺少批准知识，保留新的人工缺口说明。";

const requiredUuid = (name: string): string => {
  const value = process.env[name]?.trim();
  if (value === undefined || !uuidPattern.test(value)) throw new Error(`${name} must be a canonical UUID`);
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

const appBaseUrl = requiredUrl("ZHIXU_ARTIFACT_SMOKE_BASE_URL");
const workspaceId = requiredUuid("ZHIXU_ARTIFACT_SMOKE_WORKSPACE_ID");
const sourceVersionId = requiredUuid("ZHIXU_ARTIFACT_SMOKE_SOURCE_VERSION_ID");
const sourceSpanId = requiredUuid("ZHIXU_ARTIFACT_SMOKE_SOURCE_SPAN_ID");

const installWorkspace = async (context: BrowserContext): Promise<void> => {
  await context.addInitScript(({ key, value }) => window.localStorage.setItem(key, value), { key: workspaceStorageKey, value: workspaceId });
};

const captureRuntimeIssues = (page: Page, label: string, issues: string[]): void => {
  page.on("console", (message) => {
    if (message.type() === "warning" || message.type() === "error") issues.push(`${label} console.${message.type()}: ${message.text()}`);
  });
  page.on("pageerror", (error) => issues.push(`${label} pageerror: ${error.message}`));
};

const assertNoHorizontalOverflow = async (page: Page): Promise<void> => {
  const widths = await page.evaluate(() => {
    const root = document.querySelector<HTMLElement>(".workbench__content");
    return {
      viewport: window.innerWidth,
      document: Math.max(document.documentElement.scrollWidth, document.body.scrollWidth),
      root: root?.scrollWidth ?? 0,
    };
  });
  expect(widths.document).toBeLessThanOrEqual(widths.viewport + 1);
  expect(widths.root).toBeLessThanOrEqual(widths.viewport + 1);
};

const artifactIdFromLocation = (page: Page): string => {
  const match = /^\/artifacts\/([0-9a-f-]{36})$/.exec(new URL(page.url()).pathname);
  const artifactId = match?.[1];
  if (artifactId === undefined || !uuidPattern.test(artifactId)) throw new Error("Artifact navigation did not retain a canonical ID");
  return artifactId;
};

const expectLifecycleStatus = async (page: Page, status: string): Promise<void> => {
  await expect(page.getByText(status, { exact: true }).first()).toBeVisible();
};

const record = (value: unknown, name: string): Record<string, unknown> => {
  if (value === null || typeof value !== "object" || Array.isArray(value)) throw new Error(`${name} must be an object`);
  return value as Record<string, unknown>;
};

const list = (value: unknown, name: string): unknown[] => {
  if (!Array.isArray(value)) throw new Error(`${name} must be an array`);
  return value;
};

const positiveInteger = (value: unknown, name: string): number => {
  if (typeof value !== "number" || !Number.isSafeInteger(value) || value < 1) throw new Error(`${name} must be a positive safe integer`);
  return value;
};

const artifactFromServer = async (page: Page, artifactId: string): Promise<Record<string, unknown>> => {
  const result: unknown = await page.evaluate(async ({ id, workspace }) => {
    const response = await fetch(`/api/v1/artifacts/${id}?workspace_id=${workspace}`, { headers: { Accept: "application/json" } });
    if (!response.ok) throw new Error(`Artifact response was ${String(response.status)}`);
    const payload: unknown = await response.json();
    return payload;
  }, { id: artifactId, workspace: workspaceId });
  return record(result, "artifact response");
};

const jsonFromServer = async (page: Page, path: string): Promise<Record<string, unknown>> => {
  const result: unknown = await page.evaluate(async (requestPath) => {
    const response = await fetch(requestPath, { headers: { Accept: "application/json" } });
    if (!response.ok) throw new Error(`Artifact response was ${String(response.status)}`);
    const payload: unknown = await response.json();
    return payload;
  }, path);
  return record(result, "server response");
};

const expectCoveredServerSection = async (page: Page, artifactId: string): Promise<void> => {
  const artifact = await artifactFromServer(page, artifactId);
  const revision = record(artifact.revision, "artifact.revision");
  const section = list(revision.sections, "artifact.revision.sections").map((item) => record(item, "artifact section"))
    .find((item) => item.key === "covered");
  expect(section).toBeDefined();
  expect(section?.content).toBe(coveredContent);
  expect(record(section?.coverage, "covered coverage").status).toBe("COVERED");
  const citations = list(section?.citations, "covered citations");
  expect(citations).toHaveLength(1);
  const citation = record(citations[0], "covered citation");
  expect(citation.verified).toBe(true);
  expect(citation.source_version_id).toBe(sourceVersionId);
  expect(citation.source_span_id).toBe(sourceSpanId);
  expect(citation.verified_content_hash).toBe(coveredContentHash);
  expect(citation.excerpt).toBe(coveredContent);
};

const expectGapServerSection = async (page: Page, artifactId: string, expectedDescription = gapDescription): Promise<void> => {
  const artifact = await artifactFromServer(page, artifactId);
  const revision = record(artifact.revision, "artifact.revision");
  const section = list(revision.sections, "artifact.revision.sections").map((item) => record(item, "artifact section"))
    .find((item) => item.key === "gap");
  expect(section).toBeDefined();
  expect(section?.content).toBe("");
  expect(list(section?.citations, "gap citations")).toHaveLength(0);
  const coverage = record(section?.coverage, "gap coverage");
  expect(coverage.status).toBe("GAP");
  expect(list(coverage.gaps, "gap coverage gaps").map((item) => record(item, "gap").description)).toContain(expectedDescription);
};

const expectExportBinding = (value: Record<string, unknown>, artifact: Record<string, unknown>): void => {
  const revision = record(artifact.revision, "export artifact revision");
  expect(value.id).toMatch(uuidPattern);
  expect(value.workspace_id).toBe(workspaceId);
  expect(value.artifact_id).toBe(artifact.id);
  expect(value.revision_id).toBe(revision.id);
  expect(value.artifact_version).toBe(artifact.version);
  expect(value.revision_no).toBe(revision.revision_no);
  expect(value.revision_hash).toBe(revision.content_hash);
  expect(value.output_hash).toMatch(/^[0-9a-f]{64}$/);
  expect(value.output_size).toEqual(expect.any(Number));
  expect(value.output_size).toBeGreaterThan(0);
};

const expectPublicationBinding = (value: Record<string, unknown>, artifact: Record<string, unknown>): void => {
  const revision = record(artifact.revision, "publication artifact revision");
  expect(value.workspace_id).toBe(workspaceId);
  expect(value.artifact_id).toBe(artifact.id);
  expect(value.revision_id).toBe(revision.id);
  expect(value.artifact_version).toBe(artifact.version);
  expect(value.revision_no).toBe(revision.revision_no);
  expect(value.content_hash).toBe(revision.content_hash);
  expect(value.proposal_id).toMatch(uuidPattern);
};

test("Artifact 在真实 API/Worker/Vite 下完成隔离草稿、导出和 Publish Proposal 的桌面与移动 smoke", async ({ browser, page }) => {
  const runtimeIssues: string[] = [];
  await installWorkspace(page.context());
  captureRuntimeIssues(page, "desktop", runtimeIssues);

  await page.goto("/artifacts");
  expect(page.viewportSize()).toEqual({ width: 1440, height: 900 });
  await expect(page.getByRole("heading", { name: "先冻结产物，再决定是否入库。" })).toBeVisible();
  await expect(page.getByText(/Publish Proposal 不会直接写入正式知识。/, { exact: true })).toBeVisible();
  await expect(page.getByText("当前页 0 个 Artifact", { exact: true })).toBeVisible();
  await expect.poll(() => page.evaluate(() => typeof crypto.randomUUID === "function" ? crypto.randomUUID() : "")).toMatch(uuidPattern);
  await page.getByLabel("标题").fill(artifactTitle);
  await page.getByLabel("范围").fill(artifactScope);
  await page.getByRole("button", { name: "创建 Artifact" }).click();
  const artifactLink = page.getByRole("link", { name: new RegExp(artifactTitle) });
  await expect(artifactLink).toBeVisible();
  await artifactLink.click();
  const artifactId = artifactIdFromLocation(page);

  await expect(page.getByRole("heading", { name: artifactTitle })).toBeVisible();
  const lifecycle: string[] = ["PLANNING"];
  await page.getByLabel("大纲").fill("covered: Approved recovery\ngap: 人工知识缺口");
  await page.getByRole("button", { name: "提交大纲" }).click();
  await expectLifecycleStatus(page, "OUTLINE_REVIEW");
  lifecycle.push("OUTLINE_REVIEW");
  await page.getByRole("button", { name: "审批大纲" }).click();
  await expectLifecycleStatus(page, "GENERATING");
  lifecycle.push("GENERATING");
  await page.getByRole("button", { name: "生成章节 Approved recovery" }).click();
  await expect(page.getByText("COVERED", { exact: true }).first()).toBeVisible({ timeout: 30_000 });
  await expect(page.locator(".artifact-section__content", { hasText: coveredContent })).toBeVisible();
  await expect(page.locator(".artifact-citations code")).toContainText(sourceVersionId.slice(0, 8));
  await expect(page.locator(".artifact-citations code")).toContainText(sourceSpanId.slice(0, 8));
  await expectCoveredServerSection(page, artifactId);
  await page.getByLabel("修订章节").selectOption("gap");
  await page.getByLabel("缺口说明").fill(gapDescription);
  await page.getByRole("button", { name: "记录 GAP 章节" }).click();
  await expectLifecycleStatus(page, "DRAFT");
  lifecycle.push("DRAFT");
  await expect(page.getByText("GAP", { exact: true }).first()).toBeVisible();
  await expectGapServerSection(page, artifactId);

  await page.reload();
  await expectLifecycleStatus(page, "DRAFT");
  await expect(page.getByText(gapDescription, { exact: true })).toBeVisible();
  const initialDraftArtifact = await artifactFromServer(page, artifactId);
  const initialDraftRevision = record(initialDraftArtifact.revision, "initial draft revision");
  const initialDraftVersion = positiveInteger(initialDraftArtifact.version, "initial draft artifact version");
  const initialDraftRevisionNo = positiveInteger(initialDraftRevision.revision_no, "initial draft revision number");

  const firstRevisionResponse = page.waitForResponse((response) => response.request().method() === "POST" && new URL(response.url()).pathname.endsWith(`/artifacts/${artifactId}/revisions`));
  await page.getByRole("button", { name: "开始修订" }).click();
  expect((await firstRevisionResponse).status()).toBe(200);
  await expectLifecycleStatus(page, "GENERATING");
  lifecycle.push("GENERATING");
  const firstRevisingArtifact = await artifactFromServer(page, artifactId);
  const firstRevisingRevision = record(firstRevisingArtifact.revision, "first revising revision");
  expect(firstRevisingRevision.id).toBe(initialDraftRevision.id);
  expect(firstRevisingRevision.revision_no).toBe(initialDraftRevisionNo);
  expect(firstRevisingRevision.content_hash).toBe(initialDraftRevision.content_hash);
  expect(firstRevisingArtifact.version).toBe(initialDraftVersion + 1);

  const regenerateResponse = page.waitForResponse((response) => response.request().method() === "POST" && new URL(response.url()).pathname.endsWith(`/artifacts/${artifactId}/sections/generate`));
  await page.getByRole("button", { name: "重新生成章节 Approved recovery" }).click();
  expect((await regenerateResponse).status()).toBe(202);
  await expect.poll(async () => (await artifactFromServer(page, artifactId)).status, { timeout: 30_000 }).toBe("DRAFT");
  await expectLifecycleStatus(page, "DRAFT");
  lifecycle.push("DRAFT");
  const regeneratedArtifact = await artifactFromServer(page, artifactId);
  const regeneratedRevision = record(regeneratedArtifact.revision, "regenerated revision");
  expect(regeneratedRevision.id).not.toBe(initialDraftRevision.id);
  expect(regeneratedRevision.revision_no).toBe(initialDraftRevisionNo + 1);
  expect(regeneratedArtifact.version).toBe(initialDraftVersion + 2);
  await expectCoveredServerSection(page, artifactId);
  await expectGapServerSection(page, artifactId);

  const secondRevisionResponse = page.waitForResponse((response) => response.request().method() === "POST" && new URL(response.url()).pathname.endsWith(`/artifacts/${artifactId}/revisions`));
  await page.getByRole("button", { name: "开始修订" }).click();
  expect((await secondRevisionResponse).status()).toBe(200);
  await expectLifecycleStatus(page, "GENERATING");
  lifecycle.push("GENERATING");
  const secondRevisingArtifact = await artifactFromServer(page, artifactId);
  const secondRevisingRevision = record(secondRevisingArtifact.revision, "second revising revision");
  const regeneratedVersion = positiveInteger(regeneratedArtifact.version, "regenerated artifact version");
  const regeneratedRevisionNo = positiveInteger(regeneratedRevision.revision_no, "regenerated revision number");
  expect(secondRevisingRevision.id).toBe(regeneratedRevision.id);
  expect(secondRevisingRevision.revision_no).toBe(regeneratedRevisionNo);
  expect(secondRevisingRevision.content_hash).toBe(regeneratedRevision.content_hash);
  expect(secondRevisingArtifact.version).toBe(regeneratedVersion + 1);

  await page.getByLabel("修订章节").selectOption("gap");
  await page.getByLabel("缺口说明").fill(revisedGapDescription);
  const reviseGapResponse = page.waitForResponse((response) => response.request().method() === "POST" && new URL(response.url()).pathname.endsWith(`/artifacts/${artifactId}/sections`));
  await page.getByRole("button", { name: "替换为 GAP 章节" }).click();
  expect((await reviseGapResponse).status()).toBe(200);
  await expectLifecycleStatus(page, "DRAFT");
  lifecycle.push("DRAFT");
  await expect(page.getByText(revisedGapDescription, { exact: true })).toBeVisible();
  const revisedArtifact = await artifactFromServer(page, artifactId);
  const revisedRevision = record(revisedArtifact.revision, "revised GAP revision");
  expect(revisedRevision.id).not.toBe(regeneratedRevision.id);
  expect(revisedRevision.revision_no).toBe(regeneratedRevisionNo + 1);
  expect(revisedRevision.content_hash).not.toBe(regeneratedRevision.content_hash);
  expect(revisedArtifact.version).toBe(regeneratedVersion + 2);
  await expectCoveredServerSection(page, artifactId);
  await expectGapServerSection(page, artifactId, revisedGapDescription);

  await page.getByRole("button", { name: "审批草稿" }).click();
  await expectLifecycleStatus(page, "APPROVED");
  lifecycle.push("APPROVED");
  const approvedArtifact = await artifactFromServer(page, artifactId);
  const exportResponse = page.waitForResponse((response) => response.request().method() === "POST" && new URL(response.url()).pathname.endsWith("/exports/markdown"));
  await page.getByRole("button", { name: "导出 Markdown" }).click();
  const exportPayload: unknown = await exportResponse.then(async (response) => {
    const payload: unknown = await response.json();
    return payload;
  });
  const exportCommand = record(exportPayload, "export command");
  const exported = record(exportCommand.export, "export command export");
  const exportedArtifact = record(exportCommand.artifact, "export command artifact");
  expectExportBinding(exported, exportedArtifact);
  const approvedRevision = record(approvedArtifact.revision, "approved artifact revision");
  const exportedRevision = record(exportedArtifact.revision, "exported artifact revision");
  expect(exportedRevision.id).toBe(approvedRevision.id);
  expect(exportedRevision.revision_no).toBe(approvedRevision.revision_no);
  expect(exportedRevision.content_hash).toBe(approvedRevision.content_hash);
  const exportRecord = await jsonFromServer(page, `/api/v1/artifacts/${artifactId}/exports/${String(exported.id)}?workspace_id=${workspaceId}`);
  expect(exportRecord).toEqual(exported);
  await expect(page.getByRole("status").filter({ hasText: /Markdown 已导出并绑定 Revision \d+，输出哈希 [0-9a-f]{16}…/ })).toBeVisible();
  await expectLifecycleStatus(page, "EXPORTED");
  lifecycle.push("EXPORTED");
  const publishResponse = page.waitForResponse((response) => response.request().method() === "POST" && new URL(response.url()).pathname.endsWith("/publish-proposals"));
  await page.getByRole("button", { name: "创建 Publish Proposal" }).click();
  const publishPayload: unknown = await publishResponse.then(async (response) => {
    const payload: unknown = await response.json();
    return payload;
  });
  const publishCommand = record(publishPayload, "publish command");
  const publication = record(publishCommand.publication, "publish command publication");
  const proposedArtifact = record(publishCommand.artifact, "publish command artifact");
  expectPublicationBinding(publication, proposedArtifact);
  const proposalLink = page.locator('a[href^="/proposals/"]');
  await expect(proposalLink).toHaveText(String(publication.proposal_id));
  await expect(page.getByText(/Publish Proposal 已创建/)).toBeVisible();
  await expect(page.getByRole("status").filter({ hasText: "Artifact 尚未成为正式知识。" })).toBeVisible();
  await expectLifecycleStatus(page, "PUBLISH_PROPOSED");
  const persistedProposedArtifact = await artifactFromServer(page, artifactId);
  expect(persistedProposedArtifact.status).toBe("PUBLISH_PROPOSED");
  expectPublicationBinding(publication, persistedProposedArtifact);
  lifecycle.push("PUBLISH_PROPOSED");
  expect(lifecycle).toEqual(["PLANNING", "OUTLINE_REVIEW", "GENERATING", "DRAFT", "GENERATING", "DRAFT", "GENERATING", "DRAFT", "APPROVED", "EXPORTED", "PUBLISH_PROPOSED"]);
  await assertNoHorizontalOverflow(page);

  const mobileContext = await browser.newContext({ baseURL: appBaseUrl, viewport: { width: 390, height: 844 } });
  await installWorkspace(mobileContext);
  const mobilePage = await mobileContext.newPage();
  captureRuntimeIssues(mobilePage, "mobile", runtimeIssues);
  try {
    await mobilePage.goto(`/artifacts/${artifactId}`);
    expect(mobilePage.viewportSize()).toEqual({ width: 390, height: 844 });
    await expect(mobilePage.getByRole("heading", { name: artifactTitle })).toBeVisible();
    await expectLifecycleStatus(mobilePage, "PUBLISH_PROPOSED");
    await expect(mobilePage.getByText("已创建 Proposal；正式知识写入仍须在 Proposals 中审批与执行。", { exact: true })).toBeVisible();
    await assertNoHorizontalOverflow(mobilePage);
  } finally {
    await mobileContext.close();
  }

  expect(runtimeIssues, runtimeIssues.join("\n")).toEqual([]);
});
