import { expect, test, type BrowserContext, type Page } from "@playwright/test";
import { chmod, mkdir, rename, rm, writeFile } from "node:fs/promises";
import path from "node:path";

const workspaceStorageKey = "zhixu.active-workspace-id";
const uuidPattern = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;

const optionalUUID = (name: string): string | undefined => {
  const value = process.env[name]?.trim();
  if (value === undefined || value === "") return undefined;
  if (!uuidPattern.test(value)) throw new Error(`${name} must be a canonical UUID`);
  return value;
};

const boundedNumber = (name: string, fallback: number, minimum: number, maximum: number): number => {
  const raw = process.env[name]?.trim();
  const value = raw === undefined || raw === "" ? fallback : Number(raw);
  if (!Number.isFinite(value) || value < minimum || value > maximum) {
    throw new Error(`${name} must be between ${String(minimum)} and ${String(maximum)}`);
  }
  return value;
};

const workspaceID = optionalUUID("ZHIXU_GRAPH_FPS_WORKSPACE_ID");
const centerTopicID = optionalUUID("ZHIXU_GRAPH_FPS_CENTER_TOPIC_ID");
const durationMs = boundedNumber("ZHIXU_GRAPH_FPS_DURATION_MS", 2_000, 250, 10_000);
const sixtyHzFrameBudgetMs = 1_000 / 60;

interface FrameSchedulingDiagnostic {
  durationMs: number;
  scheduledFrameCount: number;
  observedAverageFPS: number;
  p95FrameIntervalMs: number;
  framesOver60HzBudget: number;
}

const installWorkspace = async (context: BrowserContext, id: string): Promise<void> => {
  await context.addInitScript(({ key, value }) => {
    window.localStorage.setItem(key, value);
  }, { key: workspaceStorageKey, value: id });
};

const measureFrames = async (page: Page): Promise<FrameSchedulingDiagnostic> => page.evaluate(async ({ duration, frameBudget }) => {
  const canvas = document.querySelector<HTMLElement>(".graph-canvas");
  if (canvas === null) throw new Error("graph canvas is not rendered; use a capacity Graph fixture");
  const originalTransform = canvas.style.transform;
  const timestamps: number[] = [];
  const started = performance.now();
  await new Promise<void>((resolve) => {
    const tick = (timestamp: number) => {
      timestamps.push(timestamp);
      // 让采样包含一次最小交互样式更新，而不是只测空闲页面的 rAF。
      canvas.style.transform = `translateZ(0) rotate(${String((timestamp - started) / 1000)}deg)`;
      if (timestamp - started >= duration) {
        resolve();
        return;
      }
      window.requestAnimationFrame(tick);
    };
    window.requestAnimationFrame(tick);
  });
  canvas.style.transform = originalTransform;
  const intervals: number[] = [];
  for (let index = 1; index < timestamps.length; index += 1) {
    const current = timestamps[index];
    const previous = timestamps[index - 1];
    if (current !== undefined && previous !== undefined) intervals.push(current - previous);
  }
  const sorted = [...intervals].sort((left, right) => left - right);
  const rank = sorted.length === 0 ? 0 : Math.max(0, Math.ceil(sorted.length * 0.95) - 1);
  const sampledDuration = Math.max((timestamps.at(-1) ?? performance.now()) - (timestamps[0] ?? started), 1);
  const p95 = sorted[rank] ?? sampledDuration;
  return {
    durationMs: sampledDuration,
    scheduledFrameCount: timestamps.length,
    observedAverageFPS: intervals.length * 1000 / sampledDuration,
    p95FrameIntervalMs: p95,
    framesOver60HzBudget: intervals.filter((interval) => interval > frameBudget).length,
  };
}, { duration: durationMs, frameBudget: sixtyHzFrameBudgetMs });

const writeArtifact = async (result: FrameSchedulingDiagnostic, workspace: string, centerTopic: string): Promise<void> => {
  const outputDirectory = process.env.ZHIXU_CAPACITY_ARTIFACT_DIR?.trim();
  if (outputDirectory === undefined || outputDirectory === "") return;
  await mkdir(outputDirectory, { recursive: true, mode: 0o700 });
  await chmod(outputDirectory, 0o700);
  const file = path.join(outputDirectory, "frontend-frame-scheduling-diagnostic.json");
  const temporary = path.join(outputDirectory, `.frontend-frame-scheduling-diagnostic-${String(process.pid)}-${String(Date.now())}.json`);
  try {
    await writeFile(temporary, `${JSON.stringify({
      schema_version: "zhixu-graph-frame-scheduling-diagnostic/v1",
      formal: false,
      evidence_scope: "Synthetic requestAnimationFrame scheduling while rotating .graph-canvas; it does not measure formal graph rendering capacity or satisfy the FPS gate.",
      workspace_id: workspace,
      center_topic_id: centerTopic,
      ...result,
    }, null, 2)}\n`, { mode: 0o600 });
    await rename(temporary, file);
    await chmod(file, 0o600);
  } finally {
    await rm(temporary, { force: true });
  }
};

test.skip(workspaceID === undefined && centerTopicID === undefined, "capacity Graph fixture is not configured");

test("Graph 页面写出非正式帧调度诊断", async ({ page }) => {
  if (workspaceID === undefined || centerTopicID === undefined) {
    throw new Error("ZHIXU_GRAPH_FPS_WORKSPACE_ID and ZHIXU_GRAPH_FPS_CENTER_TOPIC_ID must be set together");
  }
  await installWorkspace(page.context(), workspaceID);
  await page.goto(`/graph?mode=local&center_type=TOPIC&center_id=${centerTopicID}&depth=1&direction=BOTH`);
  await expect(page.getByRole("heading", { name: "知识图谱" })).toBeVisible();
  await expect(page.locator(".graph-canvas")).toBeVisible();
  const result = await measureFrames(page);
  await writeArtifact(result, workspaceID, centerTopicID);
  expect(result.scheduledFrameCount).toBeGreaterThan(1);
});
