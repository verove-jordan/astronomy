// Tour-screenshot generator: drives the real web UI and writes one image per tour step into
// frontend/public/tour/<locale>/<page>-<step>.webp, with the focus highlight BAKED INTO the image.
//
// It shares the recorder's machinery rather than duplicating it — the same injected runtime, the same
// spotlight cut-out, the same target resolution, the same locale pinning — because the recorder had
// already solved every hard part of driving this app in a browser. The only thing it does not do is
// record video.
//
// Baking the highlight in is what keeps the modal simple: the app never has to tell the tour where a
// control is, and a screenshot cannot drift out of alignment with its own highlight.
//
// Usage:  tsx src/shots.ts [scenario] [--headless] [--locales en,fr]
import { mkdir, rm } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { chromium } from "playwright";
import type { Browser, Page } from "playwright";
import { readFile } from "node:fs/promises";
import { parse as parseYaml } from "yaml";
import { z } from "zod";
import { execFile } from "node:child_process";
import { promisify } from "node:util";
import { ffmpeg, FFMPEG } from "./ffmpeg.js";
import { demoRuntime } from "./runtime/inject.js";
import { Target } from "./scenario.js";
import * as a from "./actions.js";

const pExecFile = promisify(execFile);
const PKG_ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const REPO_ROOT = path.resolve(PKG_ROOT, "..", "..");
const ACCENT = "#7c9cff";

// A shot step: reach a state, then photograph it with one region highlighted. `step` must match the
// step key in frontend/src/constants/tour.ts — that pairing is the whole contract.
const Shot = z.object({
  step: z.string(),
  goto: z.string().optional(), // overrides the page's route for this step
  click: Target.optional(), // one interaction to reach the state (open a tab, expand a panel)
  scrollTo: Target.optional(),
  waitFor: Target.optional(),
  highlight: Target.optional(), // omitted → the whole page, no cut-out
  dwell: z.number().nonnegative().default(400), // ms to settle (charts, maps, images)
});
export type Shot = z.infer<typeof Shot>;

const PageShots = z.object({
  page: z.string(), // route name; must be a key of TOURS
  goto: z.string(), // route path
  dwell: z.number().nonnegative().default(700), // ms after navigation, before the first shot
  steps: z.array(Shot).min(1),
});

const ShotsScenario = z.object({
  meta: z
    .object({
      baseWeb: z.string().default("http://localhost:5173"),
      viewport: z.tuple([z.number(), z.number()]).default([1440, 900]),
      scale: z.number().positive().default(2),
      locales: z.array(z.string()).default(["en", "fr"]),
      // Output width in CSS pixels. The browser shoots at viewport×scale; ffmpeg then bounds the
      // long edge so ~60 committed images stay a few MB rather than tens.
      width: z.number().positive().default(1280),
      quality: z.number().min(1).max(100).default(80),
    })
    .default({}),
  pages: z.array(PageShots).min(1),
});
export type ShotsScenario = z.infer<typeof ShotsScenario>;

async function loadScenario(file: string): Promise<ShotsScenario> {
  const parsed = ShotsScenario.safeParse(parseYaml(await readFile(file, "utf8")));
  if (!parsed.success) {
    throw new Error(`invalid shots scenario ${path.basename(file)}:\n${parsed.error.message}`);
  }
  const scenario = parsed.data;
  // ASTRO_DEMO_WEB wins over the scenario's default. scripts/tour-shots.sh sets it to whichever
  // frontend it actually found alive — the Vite dev server or the containerized one — so the
  // scenario does not have to name a port, and neither does the person running it.
  if (process.env.ASTRO_DEMO_WEB) {
    scenario.meta.baseWeb = process.env.ASTRO_DEMO_WEB;
  }
  return scenario;
}

// shootPage walks one page's steps in one locale, writing a PNG per step.
async function shootPage(
  page: Page,
  baseWeb: string,
  spec: z.infer<typeof PageShots>,
  outDir: string,
): Promise<string[]> {
  const written: string[] = [];
  for (const shot of spec.steps) {
    const route = shot.goto ?? spec.goto;
    try {
      // Re-navigate for every step, so one step's interaction never leaks into the next one's
      // picture. Cheap next to the rest, and it makes each shot reproducible on its own.
      await page.goto(new URL(route, baseWeb).toString(), { waitUntil: "domcontentloaded" });
      await page.waitForTimeout(spec.dwell);
      if (shot.waitFor) await a.resolve(page, shot.waitFor).waitFor({ timeout: 8000 });
      if (shot.click) await a.resolve(page, shot.click).click({ timeout: 7000 });
      if (shot.scrollTo) await a.scrollTo(page, a.resolve(page, shot.scrollTo), 0);
      await page.waitForTimeout(shot.dwell);
      if (shot.highlight) {
        const loc = a.resolve(page, shot.highlight);
        // Bring the target into view before photographing it. Most pages are taller than the
        // viewport, so a perfectly resolvable control (the Run button under a long form) was being
        // ringed off-screen: the shot came out fully dimmed with the highlight nowhere in it.
        await loc.scrollIntoViewIfNeeded({ timeout: 3000 }).catch(() => {});
        await page.waitForTimeout(200); // let smooth-scroll settle before measuring
        // A target that is absent has no bounding box and actions.spotlight then quietly clears the
        // highlight. Say so — an unhighlighted shot usually means the scenario never reached the
        // state its step describes (nothing selected, a panel still collapsed).
        const box = await loc.boundingBox().catch(() => null);
        if (!box) {
          console.warn(
            `  ! ${spec.page}-${shot.step}: highlight target not visible — shot taken without focus`,
          );
        }
        await a.spotlight(page, loc);
      } else {
        await a.spotlight(page, null);
      }
      await page.waitForTimeout(450); // the cut-out transitions in over ~400ms

      const file = path.join(outDir, `${spec.page}-${shot.step}.png`);
      await page.screenshot({ path: file });
      written.push(file);
    } catch (err) {
      // One unreachable control must not abandon the whole sweep: the modal degrades to a caption
      // for a missing shot, so a gap is survivable and a half-generated set is not.
      const msg = err instanceof Error ? err.message.split("\n")[0] : String(err);
      console.warn(`  ! ${spec.page}-${shot.step}: ${msg}`);
    }
  }
  return written;
}

// webpEncoder resolves how to produce WebP on this host, once.
//
// Many ffmpeg builds — including Homebrew's default — ship WITHOUT libwebp, so `-i x.png out.webp`
// dies with "Default encoder for format webp is probably disabled". `cwebp` (brew install webp) is
// the common way to have it anyway, so try ffmpeg first and fall back to cwebp rather than telling
// someone with a perfectly capable machine that they cannot generate screenshots.
async function webpEncoder(): Promise<"ffmpeg" | "cwebp"> {
  try {
    const { stdout } = await pExecFile(FFMPEG, ["-hide_banner", "-encoders"]);
    if (/\bwebp\b/.test(stdout)) return "ffmpeg";
  } catch {
    /* fall through to cwebp */
  }
  try {
    await pExecFile("cwebp", ["-version"]);
    return "cwebp";
  } catch {
    throw new Error(
      "no WebP encoder found: this ffmpeg was built without libwebp and cwebp is not installed.\n" +
        "  fix with:  brew install webp        (macOS)\n" +
        "             apt install webp         (Debian/Ubuntu)",
    );
  }
}

// toWebp re-encodes the PNGs to bounded-width WebP and removes the originals. WebP because these are
// committed assets: the same screenshot is roughly a quarter the size of the PNG.
//
// The scale always goes through ffmpeg (every build has the png/scale path) so both encoders share
// one definition of "bound the width, never upscale"; cwebp then only encodes.
async function toWebp(pngs: string[], width: number, quality: number): Promise<void> {
  if (pngs.length === 0) return;
  const encoder = await webpEncoder();
  const scale = `scale='min(${width},iw)':-2:flags=lanczos`;

  for (const png of pngs) {
    const webp = png.replace(/\.png$/, ".webp");
    if (encoder === "ffmpeg") {
      await ffmpeg(["-y", "-i", png, "-vf", scale, "-quality", String(quality), webp]);
    } else {
      const scaled = png.replace(/\.png$/, ".scaled.png");
      await ffmpeg(["-y", "-i", png, "-vf", scale, scaled]);
      try {
        await pExecFile("cwebp", ["-quiet", "-q", String(quality), scaled, "-o", webp]);
      } finally {
        await rm(scaled, { force: true });
      }
    }
    await rm(png, { force: true });
  }
}

async function shootLocale(
  browser: Browser,
  scenario: ShotsScenario,
  locale: string,
): Promise<void> {
  const { meta } = scenario;
  const [w, h] = meta.viewport;
  const outDir = path.join(REPO_ROOT, "frontend", "public", "tour", locale);
  await mkdir(outDir, { recursive: true });

  const ctx = await browser.newContext({
    viewport: { width: w, height: h },
    deviceScaleFactor: meta.scale,
    locale: locale === "fr" ? "fr-FR" : "en-US",
  });
  // Same two init scripts the recorder needs: the __name shim (esbuild wraps named functions and
  // Playwright serialises these with toString()), then the UI locale, then the runtime that owns the
  // spotlight cut-out.
  await ctx.addInitScript("globalThis.__name=globalThis.__name||function(n){return n};");
  await ctx.addInitScript((lang) => localStorage.setItem("locale", lang), locale);
  await ctx.addInitScript(demoRuntime, { accent: ACCENT });

  const page = await ctx.newPage();
  page.setDefaultTimeout(7000);

  const pngs: string[] = [];
  for (const spec of scenario.pages) {
    console.log(`  ${locale}/${spec.page}`);
    pngs.push(...(await shootPage(page, meta.baseWeb, spec, outDir)));
  }
  await ctx.close();
  await toWebp(pngs, meta.width, meta.quality);
  console.log(`  ${locale}: ${pngs.length} shot(s) → ${path.relative(REPO_ROOT, outDir)}`);
}

async function main(): Promise<void> {
  const argv = process.argv.slice(2);
  let headless = true;
  let localesOverride: string[] | undefined;
  const positional: string[] = [];
  for (let i = 0; i < argv.length; i++) {
    if (argv[i] === "--headed") headless = false;
    else if (argv[i] === "--headless") headless = true;
    else if (argv[i] === "--locales") localesOverride = argv[++i].split(",");
    else positional.push(argv[i]);
  }
  const name = positional[0] ?? "tour-shots";
  const file = name.endsWith(".yaml") || name.includes("/")
    ? path.resolve(name)
    : path.join(PKG_ROOT, "scenarios", `${name}.yaml`);

  const scenario = await loadScenario(file);
  const locales = localesOverride ?? scenario.meta.locales;
  console.log(`==> tour shots from ${path.basename(file)} · locales: ${locales.join(", ")}`);

  const browser = await chromium.launch({ headless });
  try {
    for (const locale of locales) await shootLocale(browser, scenario, locale);
  } finally {
    await browser.close();
  }
  console.log("==> done. Commit frontend/public/tour/ so the tour works from a clone.");
}

main().catch((err) => {
  console.error(err instanceof Error ? err.message : err);
  process.exit(1);
});
