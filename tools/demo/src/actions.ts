// Action handlers. Each drives the real app through Playwright while animating the injected cursor /
// caption / spotlight (window.__demo). The recorder (recorder.ts) sequences these per step and times
// them; nothing here knows about video or the timeline.
import type { Page, Locator } from "playwright";
import type { JobConfig, Target, WaitForJob } from "./scenario.js";

const MOVE_MS = 650; // cursor glide duration

// Resolve a target spec to a single Playwright locator. Prefer data-demo hooks; fall back to text/role.
export function resolve(page: Page, t: Target): Locator {
  let loc: Locator;
  if (t.firstCard) loc = page.locator('[data-demo="run-card"]');
  else if (t.testid) loc = page.locator(`[data-demo="${t.testid}"]`);
  else if (t.css) loc = page.locator(t.css);
  else if (t.role && t.text)
    // role is a Playwright AriaRole; the scenario validates it as a free string.
    loc = page.getByRole(t.role as Parameters<Page["getByRole"]>[0], {
      name: t.text,
      exact: t.exact,
    });
  else if (t.text) loc = page.getByText(t.text, { exact: t.exact });
  else throw new Error("unresolvable target");
  return t.nth != null ? loc.nth(t.nth) : loc.first();
}

export const caption = (page: Page, text: string) =>
  page.evaluate((t) => window.__demo?.caption(t), text);
export const clearCaption = (page: Page) =>
  page.evaluate(() => window.__demo?.clearCaption());

export async function spotlight(page: Page, loc: Locator | null): Promise<void> {
  if (!loc) {
    await page.evaluate(() => window.__demo?.spotlight(null));
    return;
  }
  const box = await loc.boundingBox().catch(() => null);
  await page.evaluate(
    (r) => window.__demo?.spotlight(r),
    box ? { x: box.x, y: box.y, w: box.width, h: box.height } : null,
  );
}

// Glide the fake cursor to a locator's centre and wait for the animation to finish.
async function moveTo(page: Page, loc: Locator, ms = MOVE_MS): Promise<void> {
  await loc.scrollIntoViewIfNeeded().catch(() => {});
  const box = await loc.boundingBox().catch(() => null);
  if (!box) return;
  const x = box.x + box.width / 2;
  const y = box.y + box.height / 2;
  await page.evaluate(
    (a) => window.__demo?.moveCursor(a.x, a.y, a.ms),
    { x, y, ms },
  );
}

export async function click(page: Page, loc: Locator): Promise<void> {
  await moveTo(page, loc);
  await page.evaluate(() => window.__demo?.ripple());
  await loc.click({ timeout: 15000 });
}

export async function hover(page: Page, loc: Locator): Promise<void> {
  await moveTo(page, loc);
  await loc.hover({ timeout: 15000 }).catch(() => {});
}

export async function scrollTo(_page: Page, loc: Locator): Promise<void> {
  await loc.scrollIntoViewIfNeeded().catch(() => {});
}

export async function typeInto(
  page: Page,
  loc: Locator,
  text: string,
  enter: boolean,
): Promise<void> {
  await moveTo(page, loc);
  await page.evaluate(() => window.__demo?.ripple());
  await loc.click({ timeout: 15000 });
  await loc.fill("");
  await loc.pressSequentially(text, { delay: 45 });
  if (enter) await loc.press("Enter");
}

// Settle helper: wait for the network to go idle, but never hang (SSE/polling pages never idle).
export const settle = (page: Page, ms = 8000) =>
  page.waitForLoadState("networkidle", { timeout: ms }).catch(() => {});

// Navigate without waiting for the network to settle — so the caption can be shown immediately, then
// the data loads underneath it (see recorder.runStep). `goto` keeps the settle for callers that want it.
export async function navigate(page: Page, baseWeb: string, path: string): Promise<void> {
  const url = baseWeb.replace(/\/$/, "") + path;
  await page.goto(url, { waitUntil: "domcontentloaded" });
}

export async function goto(page: Page, baseWeb: string, path: string): Promise<void> {
  await navigate(page, baseWeb, path);
  await settle(page);
}

// Configure and launch a real pipeline run from the Import page, then return the new job id (parsed from
// the /processing/tasks/:id URL the app routes to). Uses the data-demo hooks added to ImportView.
export async function runJob(
  page: Page,
  baseWeb: string,
  job: JobConfig,
): Promise<string | null> {
  await goto(page, baseWeb, "/processing/import");

  // Browse to the capture folder via the address bar (Enter navigates), then inspect the active folder.
  const pathInput = page.locator('[data-demo="browse-path"]');
  await typeInto(page, pathInput, job.input, true);
  await settle(page);

  const useBtn = page.locator('[data-demo="browse-inspect"]');
  await click(page, useBtn);

  // Inventory loaded → the run controls appear. Set mode + format by <select> value (locale-stable).
  const modeSel = page.locator('[data-demo="run-mode"]');
  await modeSel.waitFor({ state: "visible", timeout: 60000 });
  await modeSel.selectOption(job.mode);
  await page.locator('[data-demo="run-format"]').selectOption(job.format);

  if (job.options) {
    for (const [name, want] of Object.entries(job.options)) {
      const box = page.locator(`[data-demo="opt-${name}"]`);
      if ((await box.count()) === 0) continue;
      if ((await box.isChecked().catch(() => want)) !== want) await box.click();
    }
  }

  const runBtn = job.run ? resolve(page, job.run) : page.locator('[data-demo="run-pipeline"]');
  await click(page, runBtn);

  await page.waitForURL(/\/processing\/tasks\/\d+/, { timeout: 20000 }).catch(() => {});
  const m = page.url().match(/tasks\/(\d+)/);
  return m ? m[1] : null;
}

// Wait for the running job to reach a target percent or to complete. Completion is detected purely from
// the DOM: JobView's progress bar lives inside a v-if="running" block, so it DETACHES when the job
// finishes (or fails) and the result panels take its place — no API coupling.
export async function waitForJob(page: Page, cfg: WaitForJob): Promise<void> {
  const pb = page.locator('[role="progressbar"]').first();
  await pb.waitFor({ state: "visible", timeout: 30000 }).catch(() => {});

  if (cfg.until === "percent" && cfg.percent != null) {
    const deadline = Date.now() + cfg.maxSeconds * 1000;
    while (Date.now() < deadline) {
      const v = await pb.getAttribute("aria-valuenow").catch(() => null);
      if (v != null && Number(v) >= cfg.percent) return;
      await page.waitForTimeout(1000);
    }
    return;
  }
  await pb.waitFor({ state: "detached", timeout: cfg.maxSeconds * 1000 }).catch(() => {});
}
