// Scenario schema + loader. A scenario is a declarative walkthrough of the AstroStack web UI:
// ordered steps, each a small bag of optional fields executed in a fixed order (see recorder.ts).
// Parsed from YAML and validated with zod so a malformed scenario fails fast with a clear message.
import { readFile } from "node:fs/promises";
import { parse as parseYaml } from "yaml";
import { z } from "zod";

// How to find a DOM element. Prefer `testid` (a data-demo attribute) for must-work controls; fall back
// to visible `text` (+ optional `role`) or a raw `css` selector. `firstCard` targets the first run card.
export const Target = z
  .object({
    text: z.string().optional(),
    role: z.string().optional(),
    css: z.string().optional(),
    testid: z.string().optional(), // resolves to [data-demo="<testid>"]
    exact: z.boolean().optional(), // exact text match (default: substring)
    nth: z.number().int().nonnegative().optional(),
    firstCard: z.boolean().optional(), // first run card on /processing/runs
  })
  .refine(
    (t) =>
      t.text || t.css || t.testid || t.firstCard,
    "target needs one of: text, css, testid, firstCard",
  );
export type Target = z.infer<typeof Target>;

const Card = z.object({
  title: z.string(),
  subtitle: z.string().optional(),
  seconds: z.number().positive().default(3),
});
export type Card = z.infer<typeof Card>;

// A live job configured entirely from the scenario: which folder to inspect, the mode/format selects,
// optional run-control checkbox overrides, and the button that launches it.
const JobConfig = z.object({
  input: z.string(), // folder typed into the FileBrowser address bar
  mode: z
    .enum(["deepsky", "nebula", "milkyway", "planetary"])
    .default("deepsky"),
  format: z.enum(["image", "video", "both"]).default("image"),
  options: z.record(z.boolean()).optional(), // e.g. { colorCalibration: true }
  run: Target.optional(), // defaults to the Run button (data-demo="run-pipeline")
});
export type JobConfig = z.infer<typeof JobConfig>;

const WaitForJob = z.object({
  until: z.enum(["complete", "percent"]).default("complete"),
  percent: z.number().min(1).max(100).optional(),
  maxSeconds: z.number().positive().default(600),
});
export type WaitForJob = z.infer<typeof WaitForJob>;

const Zoom = z.object({
  target: Target.optional(), // zoom toward this element's centre; omitted = frame centre
  scale: z.number().min(1).max(3).default(1.25),
});
export type Zoom = z.infer<typeof Zoom>;

// A single walkthrough beat. Fields are independent and run in a documented order; most steps set just
// a caption + one action + a dwell.
const Step = z.object({
  name: z.string().optional(),
  caption: z.string().optional(),
  narrate: z.string().optional(), // spoken (TTS) when meta.voiceover != 'none'
  goto: z.string().optional(), // app path, e.g. /processing/import
  tab: z.string().optional(), // page-tab label to click (role=tab)
  click: Target.optional(),
  type: z
    .object({ into: Target, text: z.string(), enter: z.boolean().default(false) })
    .optional(),
  hover: Target.optional(),
  scrollTo: Target.optional(),
  highlight: Target.optional(), // spotlight until the next step
  job: JobConfig.optional(),
  waitForJob: WaitForJob.optional(),
  speed: z.number().min(1).default(1), // post-process time-lapse factor for this step's span
  zoom: Zoom.optional(),
  dwell: z.number().nonnegative().default(1), // seconds to hold after the action
});
export type Step = z.infer<typeof Step>;

const Meta = z.object({
  title: z.string().default("AstroStack"),
  lang: z.enum(["en", "fr"]).default("en"),
  viewport: z.tuple([z.number().int(), z.number().int()]).default([1920, 1080]),
  scale: z.number().min(1).max(3).default(2), // deviceScaleFactor
  fps: z.number().int().positive().default(30),
  baseWeb: z.string().url().default("http://localhost:5173"),
  baseApi: z.string().url().default("http://localhost:8080"),
  music: z.string().optional(), // path (relative to the package) to a background track
  voiceover: z.enum(["say", "none"]).default("none"),
});
export type Meta = z.infer<typeof Meta>;

export const Scenario = z.object({
  meta: Meta.default({}),
  intro: Card.optional(),
  outro: Card.optional(),
  steps: z.array(Step).min(1),
  // Used in place of any `job` step when no step defines a `job` block (deterministic fallback demo).
  fallbackRun: Step.optional(),
});
export type Scenario = z.infer<typeof Scenario>;

// Parse + validate a scenario YAML file. Throws a readable error on malformed input.
export async function loadScenario(path: string): Promise<Scenario> {
  const raw = await readFile(path, "utf8");
  const data = parseYaml(raw);
  const parsed = Scenario.safeParse(data);
  if (!parsed.success) {
    const issues = parsed.error.issues
      .map((i) => `  • ${i.path.join(".") || "(root)"}: ${i.message}`)
      .join("\n");
    throw new Error(`invalid scenario ${path}:\n${issues}`);
  }
  return parsed.data;
}

// True when any step launches a live job; otherwise the recorder uses meta-level fallbackRun.
export function hasLiveJob(s: Scenario): boolean {
  return s.steps.some((step) => step.job);
}
