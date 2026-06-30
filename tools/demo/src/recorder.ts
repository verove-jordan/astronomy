// The recorder: drives one continuous Playwright recording through a scenario and emits a timeline of
// spans (with per-span speed / zoom / narration) for postprocess.ts to re-time and assemble.
import { mkdir } from "node:fs/promises";
import path from "node:path";
import { chromium } from "playwright";
import type { Browser, Page } from "playwright";
import { cardHtml } from "./card.js";
import { demoRuntime } from "./runtime/inject.js";
import { hasLiveJob, type Scenario, type Step } from "./scenario.js";
import * as a from "./actions.js";

const ACCENT = "#7c9cff";

export interface Span {
  name: string;
  startMs: number;
  endMs: number;
  speed: number;
  zoomScale?: number;
  zoomCx?: number; // 0..1 of frame width
  zoomCy?: number;
  narrate?: string;
}

export interface RecordResult {
  rawVideo: string;
  introPng?: string;
  outroPng?: string;
  spans: Span[];
  scenario: Scenario;
  workDir: string;
}

// Render the intro/outro cards to PNGs in a context that is NOT being recorded (so they never leak into
// the body video). ffmpeg turns them into clips later.
async function shootCards(
  browser: Browser,
  scenario: Scenario,
  workDir: string,
): Promise<{ introPng?: string; outroPng?: string }> {
  const [w, h] = scenario.meta.viewport;
  const ctx = await browser.newContext({
    viewport: { width: w, height: h },
    deviceScaleFactor: scenario.meta.scale,
  });
  const page = await ctx.newPage();
  const out: { introPng?: string; outroPng?: string } = {};
  for (const [key, card] of [
    ["introPng", scenario.intro],
    ["outroPng", scenario.outro],
  ] as const) {
    if (!card) continue;
    await page.setContent(
      cardHtml({ ...card, accent: ACCENT, width: w, height: h }),
    );
    await page.waitForTimeout(120);
    const file = path.join(workDir, `${key}.png`);
    await page.screenshot({ path: file });
    out[key] = file;
  }
  await ctx.close();
  return out;
}

export async function record(
  scenario: Scenario,
  opts: { headless: boolean; workDir: string },
): Promise<RecordResult> {
  const { meta } = scenario;
  const [w, h] = meta.viewport;
  await mkdir(path.join(opts.workDir, "raw"), { recursive: true });

  const browser = await chromium.launch({ headless: opts.headless });
  const cards = await shootCards(browser, scenario, opts.workDir);

  const ctx = await browser.newContext({
    viewport: { width: w, height: h },
    deviceScaleFactor: meta.scale,
    locale: meta.lang === "fr" ? "fr-FR" : "en-US",
    recordVideo: { dir: path.join(opts.workDir, "raw"), size: { width: w, height: h } },
  });
  // esbuild (tsx) wraps named functions with a __name() helper; Playwright serialises addInitScript
  // functions with toString(), so define __name as identity in-page first or the runtime throws
  // "ReferenceError: __name is not defined" and never initialises.
  await ctx.addInitScript("globalThis.__name=globalThis.__name||function(n){return n};");
  // Pin the UI locale (the app reads it from localStorage) and inject the cursor/caption runtime —
  // both before any app script runs, on every document.
  await ctx.addInitScript((lang) => localStorage.setItem("locale", lang), meta.lang);
  await ctx.addInitScript(demoRuntime, { accent: ACCENT });

  const page = await ctx.newPage();
  const video = page.video();
  const t0 = Date.now();
  const now = () => Date.now() - t0;
  const spans: Span[] = [];

  const steps = buildSteps(scenario);
  for (const step of steps) {
    await runStep(page, meta.baseWeb, step, now, spans);
  }

  await a.clearCaption(page).catch(() => {});
  await ctx.close(); // finalises the video
  const rawVideo = (await video?.path()) ?? "";
  await browser.close();

  return { rawVideo, ...cards, spans, scenario, workDir: opts.workDir };
}

// When no step launches a job, append the fallbackRun so the no-job scenario still ends on a result.
function buildSteps(scenario: Scenario): Step[] {
  if (hasLiveJob(scenario) || !scenario.fallbackRun) return scenario.steps;
  return [...scenario.steps, scenario.fallbackRun];
}

async function zoomCenter(
  page: Page,
  step: Step,
  w: number,
  h: number,
): Promise<Pick<Span, "zoomScale" | "zoomCx" | "zoomCy"> | undefined> {
  if (!step.zoom) return undefined;
  let cx = 0.5;
  let cy = 0.5;
  if (step.zoom.target) {
    const box = await a.resolve(page, step.zoom.target).boundingBox().catch(() => null);
    if (box) {
      cx = (box.x + box.width / 2) / w;
      cy = (box.y + box.height / 2) / h;
    }
  }
  return { zoomScale: step.zoom.scale, zoomCx: cx, zoomCy: cy };
}

// Execute one step in a fixed order and append its timeline span(s). The job+wait step is split so only
// the processing wait is time-lapsed; everything else is one span.
async function runStep(
  page: Page,
  baseWeb: string,
  step: Step,
  now: () => number,
  spans: Span[],
): Promise<void> {
  const start = now();
  await a.spotlight(page, null); // fade any previous spotlight

  // Navigate (DOM-ready only), show the caption immediately, THEN let data settle underneath it.
  if (step.goto) await a.navigate(page, baseWeb, step.goto);
  if (step.caption) await a.caption(page, step.caption);
  if (step.tab) await a.click(page, page.getByRole("tab", { name: step.tab }));
  if (step.goto || step.tab) await a.settle(page);
  if (step.type)
    await a.typeInto(page, a.resolve(page, step.type.into), step.type.text, step.type.enter);

  if (step.job) {
    await a.runJob(page, baseWeb, step.job);
    const waitStart = now();
    if (step.waitForJob) await a.waitForJob(page, step.waitForJob);
    const waitEnd = now();
    spans.push({ name: `${step.name ?? "step"}:setup`, startMs: start, endMs: waitStart, speed: 1 });
    spans.push({
      name: `${step.name ?? "step"}:process`,
      startMs: waitStart,
      endMs: waitEnd,
      speed: step.speed,
      narrate: step.narrate,
    });
    await page.waitForTimeout(Math.round(step.dwell * 1000));
    spans.push({ name: `${step.name ?? "step"}:settle`, startMs: waitEnd, endMs: now(), speed: 1 });
    return;
  }

  if (step.click) await a.click(page, a.resolve(page, step.click));
  if (step.hover) await a.hover(page, a.resolve(page, step.hover));
  if (step.scrollTo) await a.scrollTo(page, a.resolve(page, step.scrollTo));
  if (step.highlight) await a.spotlight(page, a.resolve(page, step.highlight));
  if (step.waitForJob) await a.waitForJob(page, step.waitForJob);

  const [w, h] = [page.viewportSize()?.width ?? 1920, page.viewportSize()?.height ?? 1080];
  const zoom = await zoomCenter(page, step, w, h);
  await page.waitForTimeout(Math.round(step.dwell * 1000));
  spans.push({
    name: step.name ?? "step",
    startMs: start,
    endMs: now(),
    speed: step.speed,
    narrate: step.narrate,
    ...zoom,
  });
}
