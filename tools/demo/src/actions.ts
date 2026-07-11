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
  await loc.click({ timeout: 7000 });
}

export async function hover(page: Page, loc: Locator): Promise<void> {
  await moveTo(page, loc);
  await loc.hover({ timeout: 7000 }).catch(() => {});
}

// Smoothly scroll an element to the vertical centre of the viewport (animated, tour-paced).
export async function scrollTo(page: Page, loc: Locator, ms = 1200): Promise<void> {
  const box = await loc.boundingBox().catch(() => null);
  if (!box) return;
  const vh = page.viewportSize()?.height ?? 1080;
  const cur = await page.evaluate(() => window.scrollY);
  const targetY = box.y + cur - vh / 2 + box.height / 2;
  await page.evaluate((a) => window.__demo?.scrollToY(a.y, a.ms), { y: targetY, ms });
}

// Smoothly scroll the page itself: to an edge, or by a pixel delta (default ~half a viewport).
export async function scrollPage(
  page: Page,
  opts: { by?: number; edge?: "top" | "bottom"; ms?: number },
): Promise<void> {
  const ms = opts.ms ?? 2500;
  let targetY: number;
  if (opts.edge === "top") targetY = 0;
  else if (opts.edge === "bottom")
    targetY = await page.evaluate(() => document.documentElement.scrollHeight);
  else {
    const cur = await page.evaluate(() => window.scrollY);
    targetY = cur + (opts.by ?? 600);
  }
  await page.evaluate((a) => window.__demo?.scrollToY(a.y, a.ms), { y: targetY, ms });
}

export async function typeInto(
  page: Page,
  loc: Locator,
  text: string,
  enter: boolean,
): Promise<void> {
  await moveTo(page, loc);
  await page.evaluate(() => window.__demo?.ripple());
  await loc.click({ timeout: 7000 });
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

// Real Leaflet zoom via Ctrl+wheel over the map center (these maps disable plain scroll-zoom and have an
// unsized/unclickable +/- control, and headless dblclick-zoom doesn't register — but the component's
// custom Ctrl/⌘+wheel handler zooms about the cursor). A genuine tile reload, unlike the cosmetic `zoom`.
export async function mapZoom(
  page: Page,
  loc: Locator,
  opts: { in?: number; out?: number; ms?: number },
): Promise<void> {
  const ms = opts.ms ?? 600;
  const box = await loc.boundingBox().catch(() => null);
  if (!box) return;
  const cx = box.x + box.width / 2;
  const cy = box.y + box.height / 2;
  const n = opts.out ?? opts.in ?? 1;
  const dy = opts.out != null ? 240 : -240; // negative wheel = zoom in
  await page.evaluate((p) => window.__demo?.moveCursor(p.x, p.y, p.ms), { x: cx, y: cy, ms: 350 });
  await page.mouse.move(cx, cy);
  await page.keyboard.down("Control");
  for (let i = 0; i < n; i++) {
    await page.mouse.wheel(0, dy);
    await page.waitForTimeout(ms);
  }
  await page.keyboard.up("Control");
}

// Drag a rectangle on an element (corners as 0..1 fractions of its box) with the real mouse, while the
// fake cursor glides in parallel. Used to draw the dark-sky search area (a plain click won't do it).
export async function drawRect(
  page: Page,
  loc: Locator,
  from: [number, number],
  to: [number, number],
  ms = 1200,
): Promise<void> {
  const box = await loc.boundingBox().catch(() => null);
  if (!box) return;
  const ax = box.x + from[0] * box.width;
  const ay = box.y + from[1] * box.height;
  const bx = box.x + to[0] * box.width;
  const by = box.y + to[1] * box.height;
  // Deliberate drag with small pauses so Leaflet reliably registers mousedown → mousemove → mouseup
  // (a too-fast drag intermittently fails to form the rectangle).
  await page.evaluate((p) => window.__demo?.moveCursor(p.x, p.y, p.ms), { x: ax, y: ay, ms: 450 });
  await page.mouse.move(ax, ay);
  await page.waitForTimeout(150);
  await page.mouse.down();
  await page.waitForTimeout(150);
  const glide = page.evaluate((p) => window.__demo?.moveCursor(p.x, p.y, p.ms), { x: bx, y: by, ms });
  await page.mouse.move((ax + bx) / 2, (ay + by) / 2, { steps: 12 });
  await page.mouse.move(bx, by, { steps: 12 });
  await glide;
  await page.waitForTimeout(150);
  await page.mouse.up();
}

export async function selectOption(
  page: Page,
  loc: Locator,
  value?: string,
  label?: string,
): Promise<void> {
  await moveTo(page, loc);
  await page.evaluate(() => window.__demo?.ripple());
  await loc.selectOption(label != null ? { label } : { value: value ?? "" });
}

// Click a link that opens a new tab (target=_blank) — the "handoff": show the click without depending on
// the external page. It must NEVER stall the tour, so: (1) abort google.* loads and auto-close any popup
// the click spawns, and (2) race the click against a timeout — Playwright's click can otherwise block on
// the popup's slow cross-origin navigation.
export async function clickExternalTab(page: Page, loc: Locator): Promise<void> {
  const ctx = page.context();
  const closePopup = (pg: import("playwright").Page) => {
    pg.close({ runBeforeUnload: false }).catch(() => {});
  };
  const isGoogle = (url: URL) => /(^|\.)google\.[a-z.]+$/.test(url.hostname);
  ctx.on("page", closePopup);
  await ctx.route(isGoogle, (r) => r.abort().catch(() => {})).catch(() => {});
  try {
    await moveTo(page, loc);
    await page.evaluate(() => window.__demo?.ripple());
    await Promise.race([
      loc.click({ timeout: 6000 }).catch(() => {}),
      page.waitForTimeout(6500),
    ]);
    await page.waitForTimeout(500);
  } finally {
    ctx.off("page", closePopup);
    await ctx.unroute(isGoogle).catch(() => {});
  }
}

export async function waitForVisible(_page: Page, loc: Locator, ms = 20000): Promise<void> {
  await loc
    .first()
    .waitFor({ state: "visible", timeout: ms })
    .catch(() => {});
}
