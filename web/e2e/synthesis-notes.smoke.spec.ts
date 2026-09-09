import { expect, test, type BrowserContext, type Locator, type Page, type Response, type TestInfo } from "@playwright/test";
import { createHash } from "node:crypto";
import { mkdir, writeFile } from "node:fs/promises";
import { isAbsolute, join, normalize } from "node:path";
import { canonicalUuidPattern } from "../src/shared/codec";

const envPrefix = "ZHIXU_SYNTHESIS_SMOKE_";
const requiredToken = (name: string): string => {
  const value = process.env[`${envPrefix}${name}`];
  if (value === undefined || value === "" || value !== value.trim() || /[\u0000-\u001f\u007f]/.test(value)) throw new Error(`${envPrefix}${name} must be a canonical non-empty value`);
  return value;
};
const id = (value: unknown, label: string): string => {
  if (typeof value !== "string" || !canonicalUuidPattern.test(value)) throw new Error(`${label} must be a canonical UUID`);
  return value;
};
const text = (value: unknown, label: string): string => {
  if (typeof value !== "string" || value.trim() === "") throw new Error(`${label} must be non-empty text`);
  return value;
};
const hash = (value: unknown, label: string): string => {
  if (typeof value !== "string" || !/^[0-9a-f]{64}$/.test(value)) throw new Error(`${label} must be a SHA-256 digest`);
  return value;
};
const record = (value: unknown, label: string): Record<string, unknown> => {
  if (value === null || typeof value !== "object" || Array.isArray(value)) throw new Error(`${label} must be an object`);
  return value as Record<string, unknown>;
};
const records = (value: unknown, label: string): Record<string, unknown>[] => {
  if (!Array.isArray(value)) throw new Error(`${label} must be an array`);
  return value.map((item) => record(item, label));
};
const baseURL = new URL(requiredToken("BASE_URL"));
if (baseURL.protocol !== "http:" || !["127.0.0.1", "localhost", "[::1]"].includes(baseURL.hostname) || baseURL.username !== "" || baseURL.password !== "" || baseURL.pathname !== "/" || baseURL.search !== "" || baseURL.hash !== "") throw new Error(`${envPrefix}BASE_URL must be a loopback HTTP origin`);
const artifactDir = process.env[`${envPrefix}ARTIFACT_DIR`];
if (artifactDir !== undefined && (!isAbsolute(artifactDir) || normalize(artifactDir) !== artifactDir || /[\u0000-\u001f\u007f]/.test(artifactDir))) throw new Error(`${envPrefix}ARTIFACT_DIR must be a canonical absolute directory`);
const fixture = {
  baseUrl: baseURL.origin,
  workspaceId: id(requiredToken("WORKSPACE_ID"), "Workspace fixture"),
  noteId: id(requiredToken("NOTE_ID"), "Note fixture"),
  sessionToken: requiredToken("SESSION_TOKEN"),
  csrfToken: requiredToken("CSRF_TOKEN"),
};
const prefix = `/api/v1/workspaces/${fixture.workspaceId}/synthesis`;
const notePath = `${prefix}/notes/${fixture.noteId}`;
const noteRoute = `/authoring/notes/${fixture.noteId}`;
const interviewOptions = { role: "合成笔记浏览器验收", difficulty: "INTERMEDIATE", duration_minutes: 30, question_count: 3, max_follow_ups: 1 };

const installSession = async (context: BrowserContext): Promise<void> => {
  await context.addCookies([{ name: "zhixu_session", value: fixture.sessionToken, url: fixture.baseUrl, httpOnly: true, sameSite: "Strict" }]);
  await context.addInitScript(({ workspaceId, csrfToken }) => {
    window.localStorage.setItem("zhixu.active-workspace-id", workspaceId);
    window.localStorage.setItem("zhixu.csrf-token", csrfToken);
  }, { workspaceId: fixture.workspaceId, csrfToken: fixture.csrfToken });
};
const apiGet = async (page: Page, path: string): Promise<Record<string, unknown>> => {
  const response = await page.request.get(`${fixture.baseUrl}${path}`, { headers: { "X-Workspace-Id": fixture.workspaceId } });
  expect(response.status(), "Authoritative API read must succeed").toBe(200);
  return record(await response.json() as unknown, "API response");
};
const isResponse = (response: Response, path: string, method = "GET"): boolean => response.request().method() === method && new URL(response.url()).pathname === path;

const captureRuntime = (page: Page, target: string, issues: string[], sourceReads: string[], writes: string[]): void => {
  page.on("console", (message) => {
    if (message.type() === "error" || message.type() === "warning") issues.push(`${target}.console.${message.type()}.${createHash("sha256").update(message.text()).digest("hex").slice(0, 12)}`);
  });
  page.on("pageerror", () => issues.push(`${target}.pageerror`));
  page.on("response", (response) => {
    if (response.status() >= 400) issues.push(`${target}.http.${String(response.status())}.${new URL(response.url()).pathname}`);
  });
  page.on("requestfailed", (request) => {
    if (request.failure()?.errorText !== "net::ERR_ABORTED") issues.push(`${target}.requestfailed.${new URL(request.url()).pathname}`);
  });
  page.on("request", (request) => {
    const path = new URL(request.url()).pathname;
    if (path.startsWith(notePath) && path.includes("/sources/")) sourceReads.push(path);
    if ((path.startsWith("/api/v1/") || path.startsWith("/api/v2/")) && !["GET", "HEAD", "OPTIONS"].includes(request.method())) writes.push(`${request.method()} ${path}`);
  });
};
const assertNoHorizontalOverflow = async (page: Page): Promise<void> => {
  const widths = await page.evaluate(() => ({
    viewport: window.innerWidth,
    document: Math.max(document.documentElement.scrollWidth, document.body.scrollWidth),
    regions: [...document.querySelectorAll<HTMLElement>(".workbench__content, .synthesis-page, .synthesis-note-content, .synthesis-source-dialog, .interview-question-card")].map((element) => element.scrollWidth),
  }));
  expect(widths.document, "Document must fit the viewport").toBeLessThanOrEqual(widths.viewport + 1);
  for (const width of widths.regions) expect(width, "Workbench and source dialog must fit the viewport").toBeLessThanOrEqual(widths.viewport + 1);
};
const screenshot = async (page: Page, testInfo: TestInfo, name: string): Promise<void> => {
  const path = artifactDir === undefined ? testInfo.outputPath(name) : join(artifactDir, name);
  if (artifactDir !== undefined) await mkdir(artifactDir, { recursive: true, mode: 0o700 });
  await page.screenshot({ path, fullPage: true });
  await testInfo.attach(name, { path, contentType: "image/png" });
};

interface SavedSource {
  title: string;
  params: {
    workspace_id: string; source_id: string; source_version_id: string; content_artifact_id: string;
    parse_projection_id: string; source_span_id: string; content_hash: string; excerpt_hash: string;
  };
}
const savedSource = (value: unknown): SavedSource => {
  const reference = record(value, "Saved source reference");
  const source = record(reference.source, "Saved source tuple");
  const result = {
    title: text(reference.title, "Saved source title"),
    params: {
      workspace_id: id(source.workspace_id, "Source workspace"), source_id: id(source.source_id, "Source"),
      source_version_id: id(source.source_version_id, "Source version"), content_artifact_id: id(source.content_artifact_id, "Content artifact"),
      parse_projection_id: id(source.parse_projection_id, "Parse projection"), source_span_id: id(reference.source_span_id, "Source span"),
      content_hash: hash(source.content_hash, "Source content hash"), excerpt_hash: hash(reference.excerpt_hash, "Source excerpt hash"),
    },
  };
  expect(result.params.workspace_id).toBe(fixture.workspaceId);
  return result;
};
const itemSources = (item: Record<string, unknown>): SavedSource[] => {
  let sources: Record<string, unknown>[];
  if (item.kind === "FACT") sources = records(record(item.fact, "Fact").sources, "Fact sources");
  else if (item.kind === "CONFLICT") sources = records(record(item.conflict, "Conflict").alternatives, "Conflict alternatives").flatMap((alternative) => records(alternative.sources, "Alternative sources"));
  else if (item.kind === "GAP") {
    const gap = record(item.gap, "Gap");
    sources = [...records(gap.sources, "Gap sources"), ...(gap.resolution === null ? [] : records(record(gap.resolution, "Gap resolution").sources, "Resolution sources"))];
  } else throw new Error("Unknown synthesis item kind");
  return sources.map(savedSource);
};
interface Revision {
  wire: Record<string, unknown>;
  id: string;
  number: number;
  items: Record<string, unknown>[];
  sources: SavedSource[];
}
const revision = (value: unknown): Revision => {
  const wire = record(value, "Synthesis revision");
  expect(wire.workspace_id).toBe(fixture.workspaceId);
  expect(wire.note_id).toBe(fixture.noteId);
  if (typeof wire.revision_no !== "number" || !Number.isSafeInteger(wire.revision_no) || wire.revision_no < 1) throw new Error("Revision number is invalid");
  const items = records(wire.items, "Revision items");
  expect(items.length).toBeGreaterThan(0);
  expect(new Set(items.map((item) => id(item.id, "Revision item"))).size).toBe(items.length);
  const sources = [...new Map(items.flatMap(itemSources).map((source) => [JSON.stringify(source.params), source])).values()];
  expect(sources.length).toBeGreaterThan(0);
  return { wire, id: id(wire.id, "Synthesis revision"), number: wire.revision_no, items, sources };
};
const frozenReference = (value: Revision): Record<string, unknown> => ({
  workspace_id: fixture.workspaceId, note_id: fixture.noteId, revision_id: value.id, document_id: value.wire.document_id,
  article_revision_id: value.wire.article_revision_id, revision_no: value.number, article_revision_no: value.wire.article_revision_no,
  content_hash: value.wire.content_hash, projection_hash: value.wire.projection_hash, title: value.wire.title,
});
const firstFactSource = (value: Revision): { item: Record<string, unknown>; source: SavedSource } => {
  const item = value.items.find((candidate) => candidate.kind === "FACT");
  const source = item === undefined ? undefined : itemSources(item)[0];
  if (item === undefined || source === undefined) throw new Error("Revision fixture requires a FACT with an original source");
  return { item, source };
};
const kindCards = (page: Page, kind: unknown): Locator => {
  if (kind !== "FACT" && kind !== "CONFLICT" && kind !== "GAP") throw new Error("Unknown synthesis item kind");
  return page.getByRole("region", { name: "笔记内容", exact: true })
    .locator(`section[aria-labelledby="synthesis-${kind}"] > .synthesis-items > article.synthesis-item--${kind.toLowerCase()}`);
};
const itemCard = (page: Page, value: Revision, item: Record<string, unknown>): Locator => {
  // NoteContent groups by kind and preserves API order within each group. The saved item ID selects that position.
  const index = value.items.filter((candidate) => candidate.kind === item.kind).findIndex((candidate) => candidate.id === item.id);
  if (index < 0) throw new Error("Displayed item must belong to the requested revision");
  return kindCards(page, item.kind).nth(index);
};
const statementSourceButtons = (owner: Locator): Locator => owner.locator(":scope > .synthesis-source-links > li > button");
const assertStatementSources = async (owner: Locator, values: unknown): Promise<SavedSource[]> => {
  const sources = records(values, "Statement sources").map(savedSource);
  const buttons = statementSourceButtons(owner);
  await expect(buttons).toHaveCount(sources.length);
  await expect(buttons).toHaveText(sources.map((source) => `${source.title}，打开原始片段`));
  return sources;
};
const assertStatement = async (owner: Locator, value: Record<string, unknown>): Promise<void> => {
  await expect(owner.locator(":scope > .synthesis-statement")).toHaveText(text(value.text, "Statement text"));
  await expect(owner.locator(":scope > .synthesis-statement")).toBeVisible();
  if (typeof value.applicability !== "string") throw new Error("Statement applicability must be text");
  await expect(owner.locator(":scope > .synthesis-conditions")).toHaveText(`适用条件${value.applicability || "来源未说明"}`);
  await assertStatementSources(owner, value.sources);
};
const exactSourceIndex = (sources: SavedSource[], source: SavedSource): number => {
  const matches = sources.flatMap((candidate, index) => JSON.stringify(candidate.params) === JSON.stringify(source.params) ? [index] : []);
  expect(matches, "Source button must map to exactly one saved tuple within its owner").toHaveLength(1);
  const index = matches[0];
  if (index === undefined) throw new Error("Original source is missing from its owner");
  return index;
};
const assertRevisionVisible = async (page: Page, value: Revision): Promise<void> => {
  const reading = page.getByRole("region", { name: "笔记内容", exact: true });
  await expect(reading.locator(".synthesis-reading-heading h2")).toHaveText(`版本 ${String(value.number)}`);
  await expect(reading.locator("article.synthesis-item")).toHaveCount(value.items.length);
  for (const kind of ["FACT", "CONFLICT", "GAP"]) await expect(kindCards(page, kind)).toHaveCount(value.items.filter((item) => item.kind === kind).length);
  for (const item of value.items) {
    const card = itemCard(page, value, item);
    await expect(card).toBeVisible();
    if (item.kind === "FACT") await assertStatement(card, record(item.fact, "Fact"));
    else if (item.kind === "CONFLICT") {
      const conflict = record(item.conflict, "Conflict");
      await expect(card.locator(":scope > h3")).toHaveText(text(conflict.subject, "Conflict subject"));
      await expect(card.locator(":scope > h3")).toBeVisible();
      const alternatives = records(conflict.alternatives, "Conflict alternatives");
      const views = card.locator(":scope > .synthesis-alternatives > section");
      await expect(views).toHaveCount(alternatives.length);
      for (const [index, alternative] of alternatives.entries()) {
        await expect(views.nth(index)).toHaveAttribute("aria-label", `观点 ${String(index + 1)}`);
        await assertStatement(views.nth(index), alternative);
      }
    } else {
      const gap = record(item.gap, "Gap");
      await expect(card.locator(":scope > .synthesis-item-heading > h3")).toHaveText(text(gap.question, "Gap question"));
      await expect(card.locator(":scope > .synthesis-item-heading > h3")).toBeVisible();
      await expect(card.locator(":scope > .synthesis-item-heading .ui-badge")).toHaveText(gap.resolution === null ? "待补充" : "已补充");
      if (typeof gap.context !== "string") throw new Error("Gap context must be text");
      await expect(card.locator(":scope > p:not(.synthesis-help)")).toHaveText(gap.context === "" ? [] : [gap.context]);
      await assertStatementSources(card, gap.sources);
      const resolution = card.locator(":scope > .synthesis-gap-resolution");
      await expect(resolution).toHaveCount(gap.resolution === null ? 0 : 1);
      if (gap.resolution !== null) await assertStatement(resolution, record(gap.resolution, "Gap resolution"));
    }
  }
  await assertNoHorizontalOverflow(page);
};
const selectRevision = async (page: Page, value: Revision): Promise<void> => {
  await page.locator(".synthesis-history .synthesis-history-button").filter({ has: page.getByText(`版本 ${String(value.number)}`, { exact: true }) }).click();
  await expect.poll(() => new URL(page.url()).searchParams.get("revision_id")).toBe(value.id);
  await assertRevisionVisible(page, value);
};
const sourcePath = (value: Revision, source: SavedSource): string => `${notePath}/revisions/${value.id}/sources/${source.params.source_span_id}`;
const assertSourceOpened = async (page: Page, response: Response, value: Revision, source: SavedSource): Promise<void> => {
  expect(response.status()).toBe(200);
  expect(new URL(response.url()).pathname).toBe(sourcePath(value, source));
  const view = record(await response.json() as unknown, "Opened source");
  expect([view.workspace_id, view.note_id, view.revision_id]).toEqual([fixture.workspaceId, fixture.noteId, value.id]);
  expect(savedSource(view.reference).params).toEqual(source.params);
  expect(view.availability).toBe("AVAILABLE");
  const excerpt = text(view.text, "Source excerpt");
  expect(createHash("sha256").update(excerpt).digest("hex")).toBe(source.params.excerpt_hash);
  const dialog = page.getByRole("dialog", { name: source.title, exact: true });
  await expect(dialog).toBeVisible();
  await expect.poll(() => dialog.locator(".synthesis-source-excerpt").textContent()).toBe(excerpt);
  await assertNoHorizontalOverflow(page);
};
const openManualSource = async (page: Page, value: Revision, item: Record<string, unknown>, source: SavedSource): Promise<void> => {
  expect(item.kind).toBe("FACT");
  const card = itemCard(page, value, item);
  const sources = await assertStatementSources(card, record(item.fact, "Fact").sources);
  const trigger = statementSourceButtons(card).nth(exactSourceIndex(sources, source));
  await trigger.focus();
  await expect(trigger).toBeFocused();
  const response = page.waitForResponse((response) => isResponse(response, sourcePath(value, source)));
  await trigger.click();
  await assertSourceOpened(page, await response, value, source);
  await page.getByRole("dialog").getByRole("button", { name: "关闭", exact: true }).click();
  await expect(trigger).toBeFocused();
};
const openSourceLink = async (page: Page, value: Revision, source: SavedSource, explicitRevision: boolean): Promise<void> => {
  const params = new URLSearchParams(source.params);
  if (explicitRevision) params.set("revision_id", value.id);
  const response = page.waitForResponse((response) => isResponse(response, sourcePath(value, source)));
  await page.goto(`${noteRoute}?${params.toString()}`);
  await assertSourceOpened(page, await response, value, source);
};
const readInterview = async (page: Page, sessionId: string, frozen: Record<string, unknown>): Promise<Record<string, unknown>> => {
  const result = await apiGet(page, `/api/v2/review/interviews/${sessionId}?workspace_id=${fixture.workspaceId}`);
  const session = record(result.session, "Interview session");
  expect([session.id, session.workspace_id]).toEqual([sessionId, fixture.workspaceId]);
  expect(record(record(session.config, "Interview config").scope, "Interview scope").note_revision).toEqual(frozen);
  for (const question of records(result.questions, "Interview questions")) {
    expect(question.source_kind).toBe("NOTE_REVISION");
    expect(question.claim_id).toBeNull();
    const item = record(question.note_item, "Interview note item");
    expect(item.revision).toEqual(frozen);
    expect(Object.keys(item).sort()).toEqual(["item_id", "item_kind", "revision"]);
    for (const hidden of ["answer_points", "follow_up_plan", "sources", "note_source"]) expect(Object.hasOwn(question, hidden)).toBe(false);
  }
  return result;
};

test("合成笔记在真实 API/Worker 下完成版本、原始来源和 NOTE 面试桌面及窄屏闭环", async ({ browser }, testInfo) => {
  test.setTimeout(180_000);
  const issues: string[] = [], sourceReads: string[] = [], writes: string[] = [];
  const context = await browser.newContext({ baseURL: fixture.baseUrl, viewport: { width: 1440, height: 900 } });
  await installSession(context);
  const page = await context.newPage();
  captureRuntime(page, "desktop", issues, sourceReads, writes);
  try {
    // The localStorage seed is not authority: the running API must expose the requested Workspace.
    expect((await apiGet(page, "/api/v1/workspaces/active")).id).toBe(fixture.workspaceId);
    const detail = await apiGet(page, notePath);
    expect(detail.workspace_id).toBe(fixture.workspaceId);
    expect(record(detail.note, "Synthesis note").id).toBe(fixture.noteId);
    const current = revision(detail.current_revision), published = revision(detail.published_revision);
    expect(current.id).not.toBe(published.id);
    expect(current.number).toBeGreaterThan(published.number);
    expect([...new Set(current.items.map((item) => item.kind))].sort()).toEqual(["CONFLICT", "FACT", "GAP"]);
    const { item: sourceItem, source } = firstFactSource(published);
    expect(current.sources.some((candidate) => JSON.stringify(candidate.params) === JSON.stringify(source.params))).toBe(true);
    const frozen = frozenReference(published);

    await page.goto("/authoring/notes");
    await expect(page.getByRole("heading", { name: "合成笔记", exact: true })).toBeVisible();
    const entry = page.locator(`a.synthesis-note-main[href="${noteRoute}"]`);
    await expect(entry).toBeVisible();
    await assertNoHorizontalOverflow(page);
    await entry.click();
    await assertRevisionVisible(page, current);
    await expect(page.locator(".synthesis-detail-status").getByText(`正式版本 ${String(published.number)}`, { exact: true })).toBeVisible();
    await expect(page.locator(".synthesis-reading-heading").getByText("当前候选", { exact: true })).toBeVisible();
    await expect(page.getByRole("link", { name: "查看更新提案", exact: true })).toHaveAttribute("href", `/proposals/${id(record(detail.publication, "Current publication").proposal_id, "Proposal")}`);
    expect(sourceReads).toEqual([]);
    await screenshot(page, testInfo, "synthesis-desktop-note.png");
    await selectRevision(page, published);
    expect(sourceReads).toEqual([]);
    await openManualSource(page, published, sourceItem, source);

    await openSourceLink(page, current, source, false);
    await page.getByRole("dialog").getByRole("button", { name: "关闭", exact: true }).click();
    const readsBeforeRefresh = sourceReads.length;
    const refreshed = page.waitForResponse((response) => isResponse(response, notePath));
    await page.getByRole("button", { name: "刷新", exact: true }).click();
    expect((await refreshed).status()).toBe(200);
    await assertRevisionVisible(page, current);
    await expect(page.getByRole("dialog")).toHaveCount(0);
    expect(sourceReads).toHaveLength(readsBeforeRefresh);
    await openSourceLink(page, published, source, true);
    await screenshot(page, testInfo, "synthesis-desktop-frozen-source.png");
    await page.getByRole("dialog").getByRole("button", { name: "关闭", exact: true }).click();
    const badParams = new URLSearchParams(source.params);
    badParams.set("excerpt_hash", `${source.params.excerpt_hash.startsWith("a") ? "b" : "a"}${source.params.excerpt_hash.slice(1)}`);
    const readsBeforeInvalid = sourceReads.length;
    await page.goto(`${noteRoute}?${badParams.toString()}`);
    await expect(page.getByText("无法定位原始来源", { exact: true })).toBeVisible();
    await expect(page.getByRole("dialog")).toHaveCount(0);
    expect(sourceReads).toHaveLength(readsBeforeInvalid);

    await page.goto(noteRoute);
    await assertRevisionVisible(page, current);
    const panel = page.getByRole("region", { name: "用这篇笔记面试", exact: true });
    await panel.getByLabel("面试方向", { exact: true }).fill(interviewOptions.role);
    await panel.getByLabel("题目数", { exact: true }).fill(String(interviewOptions.question_count));
    await panel.getByLabel("最多追问", { exact: true }).fill(String(interviewOptions.max_follow_ups));
    const accepted = page.waitForResponse((response) => isResponse(response, `${notePath}/interviews`, "POST"));
    await panel.getByRole("button", { name: "准备 AI 面试", exact: true }).click();
    const acceptedResponse = await accepted;
    expect(acceptedResponse.status()).toBe(202);
    expect(acceptedResponse.request().postDataJSON() as unknown).toEqual(interviewOptions);
    if (acceptedResponse.request().headers()["x-csrf-token"] !== fixture.csrfToken) throw new Error("Note preparation did not preserve CSRF binding");
    const preparation = record(record(await acceptedResponse.json() as unknown, "Preparation envelope").preparation, "Note preparation");
    const preparationId = id(preparation.id, "Preparation");
    expect(preparation.note_revision).toEqual(frozen);
    await expect.poll(() => new URL(page.url()).searchParams.get("preparation_id")).toBe(preparationId);
    await page.reload();
    await expect(panel.getByRole("link", { name: "继续面试", exact: true })).toBeVisible({ timeout: 90_000 });
    const ready = record((await apiGet(page, `${notePath}/interviews/${preparationId}`)).preparation, "Ready preparation");
    expect(ready.status).toBe("READY");
    expect(ready.note_revision).toEqual(frozen);
    expect(ready.options).toEqual(interviewOptions);
    const sessionId = id(ready.session_id, "Prepared interview session");
    const interviewRoute = `/interviews/${sessionId}`;
    await expect(panel.getByRole("link", { name: "继续面试", exact: true })).toHaveAttribute("href", interviewRoute);
    await panel.getByRole("link", { name: "继续面试", exact: true }).click();
    const initial = await readInterview(page, sessionId, frozen);
    expect(records(initial.turns, "Initial turns")).toHaveLength(0);
    const first = records(initial.questions, "Prepared questions").find((question) => question.question_no === 1 && question.follow_up_no === 0);
    if (first === undefined) throw new Error("Prepared interview requires its first base question");
    expect(record(first.note_item, "First note item").item_kind).toBe("FACT");
    await expect(page.locator(".interview-question-card").getByRole("heading", { name: text(first.prompt, "First prompt"), exact: true })).toBeVisible();
    await expect(page.locator(".synthesis-evidence")).toHaveCount(0);
    await expect(page.getByText(`题目绑定「${text(published.wire.title, "Note title")}」版本 ${String(published.number)}。AI 已生成题目与追问计划，评分使用确定性规则。`, { exact: true })).toBeVisible();
    await assertNoHorizontalOverflow(page);
    await screenshot(page, testInfo, "synthesis-desktop-interview-question.png");
    await page.getByLabel("你的回答", { exact: true }).fill("I would verify the original source and applicability before choosing the cache expiration, and keep unresolved outage conditions explicit.");
    const submitted = page.waitForResponse((response) => isResponse(response, `/api/v2/review/interviews/${sessionId}/turns`, "POST"));
    await page.getByRole("button", { name: "提交回答", exact: true }).click();
    const turnResponse = await submitted;
    expect(turnResponse.status()).toBe(200);
    const result = record(await turnResponse.json() as unknown, "Interview turn result");
    const turn = record(result.turn, "Interview turn");
    expect(turn.question_id).toBe(first.id);
    expect(turn.scorer_version).toBe("interview-deterministic/v2");
    const score = record(turn.score, "Interview score");
    expect(score.evidence).toEqual([]);
    const scoredSource = record(score.note_source, "Scored note source");
    expect(scoredSource.revision).toEqual(frozen);
    const scoredSources = records(scoredSource.sources, "Scored original sources").map(savedSource);
    const originalSource = scoredSources[0];
    if (originalSource === undefined) throw new Error("First FACT score requires an original source");
    expect(published.sources.some((candidate) => JSON.stringify(candidate.params) === JSON.stringify(originalSource.params))).toBe(true);
    const scorePanel = page.getByRole("region", { name: "本题服务端评分", exact: true });
    await expect(scorePanel).toBeVisible();
    await expect(scorePanel.locator(".synthesis-evidence")).toHaveCount(1);
    const displayedScoreSources = [...new Map(scoredSources.map((reference) => [reference.params.source_span_id, reference])).values()];
    const scoreSourceButtons = scorePanel.locator(".synthesis-evidence > .synthesis-source-links > li > button");
    await expect(scoreSourceButtons).toHaveCount(displayedScoreSources.length);
    await expect(scoreSourceButtons).toHaveText(displayedScoreSources.map((reference) => reference.title));
    const scoreSourceResponse = page.waitForResponse((response) => isResponse(response, sourcePath(published, originalSource)));
    await scoreSourceButtons.nth(exactSourceIndex(displayedScoreSources, originalSource)).click();
    await assertSourceOpened(page, await scoreSourceResponse, published, originalSource);
    await screenshot(page, testInfo, "synthesis-desktop-interview-source.png");
    await page.getByRole("dialog").getByRole("button", { name: "关闭", exact: true }).click();
    const next = record(result.next_question, "Next question");
    await page.reload();
    await expect(page.locator(".interview-question-card").getByRole("heading", { name: text(next.prompt, "Restored question prompt"), exact: true })).toBeVisible();
    const restored = await readInterview(page, sessionId, frozen);
    expect(records(restored.turns, "Restored turns").map((item) => item.id)).toEqual([turn.id]);
    expect(records(restored.questions, "Restored questions").find((question) => question.id === first.id)?.status).toBe("ANSWERED");
    await assertNoHorizontalOverflow(page);

    const mobileContext = await browser.newContext({ baseURL: fixture.baseUrl, viewport: { width: 390, height: 844 } });
    await installSession(mobileContext);
    const mobile = await mobileContext.newPage();
    captureRuntime(mobile, "mobile", issues, sourceReads, writes);
    try {
      await mobile.goto(noteRoute);
      await assertRevisionVisible(mobile, current);
      await screenshot(mobile, testInfo, "synthesis-mobile-note.png");
      await selectRevision(mobile, published);
      await openSourceLink(mobile, published, source, true);
      await screenshot(mobile, testInfo, "synthesis-mobile-frozen-source.png");
      await mobile.getByRole("dialog").getByRole("button", { name: "关闭", exact: true }).click();
      await mobile.goto(`${noteRoute}?preparation_id=${preparationId}`);
      await mobile.getByRole("region", { name: "用这篇笔记面试", exact: true }).getByRole("link", { name: "继续面试", exact: true }).click();
      await expect(mobile).toHaveURL(`${fixture.baseUrl}${interviewRoute}`);
      await expect(mobile.locator(".interview-question-card").getByRole("heading", { name: text(next.prompt, "Mobile restored question"), exact: true })).toBeVisible();
      expect(records((await readInterview(mobile, sessionId, frozen)).turns, "Mobile turns").map((item) => item.id)).toEqual([turn.id]);
      await assertNoHorizontalOverflow(mobile);
      await screenshot(mobile, testInfo, "synthesis-mobile-interview.png");
    } finally { await mobileContext.close(); }

    expect(writes, "Browsing and refreshing must not create duplicate commands or write a Proposal").toEqual([`POST ${notePath}/interviews`, `POST /api/v2/review/interviews/${sessionId}/turns`]);
    expect(issues, "Browser console, page and network errors must remain empty").toEqual([]);
    const summaryPath = artifactDir === undefined ? testInfo.outputPath("synthesis-browser-summary.json") : join(artifactDir, "synthesis-browser-summary.json");
    await writeFile(summaryPath, `${JSON.stringify({ workspace_id: fixture.workspaceId, note_id: fixture.noteId, published_revision_id: published.id, candidate_revision_id: current.id, preparation_id: preparationId, interview_session_id: sessionId, answered_turn_id: turn.id, source_reads: sourceReads, viewports: [1440, 390], runtime_issues: issues }, null, 2)}\n`, { mode: 0o600 });
    await testInfo.attach("synthesis-browser-summary", { path: summaryPath, contentType: "application/json" });
  } finally { await context.close(); }
});
