// Entry point: `tsx src/index.ts <scenario> [--headless] [--out file]`.
// Loads a scenario, records the walkthrough, post-processes to an MP4. Output defaults to
// <repo>/output/demo/<scenario>.mp4.
import { access, mkdir } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { hasFfmpeg } from "./ffmpeg.js";
import { postprocess } from "./postprocess.js";
import { record } from "./recorder.js";
import { loadScenario } from "./scenario.js";

const PKG_ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const REPO_ROOT = path.resolve(PKG_ROOT, "..", "..");

interface Args {
  scenario: string;
  headless: boolean;
  out?: string;
}

function parseArgs(argv: string[]): Args {
  const positional: string[] = [];
  let headless = false;
  let out: string | undefined;
  for (let i = 0; i < argv.length; i++) {
    const a = argv[i];
    if (a === "--headless") headless = true;
    else if (a === "--out") out = argv[++i];
    else positional.push(a);
  }
  if (positional.length === 0) {
    throw new Error("usage: record <scenario> [--headless] [--out file]");
  }
  return { scenario: positional[0], headless, out };
}

function scenarioPath(name: string): string {
  if (name.includes("/") || name.endsWith(".yaml") || name.endsWith(".yml")) {
    return path.resolve(name);
  }
  return path.join(PKG_ROOT, "scenarios", `${name}.yaml`);
}

async function exists(p: string): Promise<boolean> {
  return access(p).then(() => true).catch(() => false);
}

async function main(): Promise<void> {
  const args = parseArgs(process.argv.slice(2));
  if (!(await hasFfmpeg())) {
    throw new Error("ffmpeg not found on PATH (set FFMPEG_BIN). It is required for post-processing.");
  }

  const scPath = scenarioPath(args.scenario);
  const scenario = await loadScenario(scPath);
  const baseName = path.basename(scPath).replace(/\.(ya?ml)$/, "");

  // Resolve optional background music relative to the package; warn (don't fail) if it's missing.
  let music: string | null = null;
  if (scenario.meta.music) {
    const m = path.resolve(PKG_ROOT, scenario.meta.music);
    if (await exists(m)) music = m;
    else console.warn(`! music not found, skipping: ${scenario.meta.music}`);
  }

  const outFile = args.out
    ? path.resolve(args.out)
    : path.join(REPO_ROOT, "output", "demo", `${baseName}.mp4`);
  const workDir = path.join(REPO_ROOT, "output", "demo", ".work", `${baseName}-${Date.now()}`);
  await mkdir(workDir, { recursive: true });

  console.log(`▶ recording "${scenario.meta.title}" (${args.headless ? "headless" : "headed"})`);
  console.log(`  scenario: ${scPath}`);
  const rec = await record(scenario, { headless: args.headless, workDir });
  console.log(`  recorded ${rec.spans.length} spans → post-processing…`);

  const final = await postprocess(rec, { outFile, music });
  console.log(`✓ demo video: ${final}`);
}

main().catch((err) => {
  console.error(`✗ ${err instanceof Error ? err.message : err}`);
  process.exit(1);
});
