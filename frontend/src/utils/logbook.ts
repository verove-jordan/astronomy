// Logbook formatting: the derivations the session list and the detail page share.

import type {
  CaptureConditionRow,
  CaptureFrameStat,
  CaptureSessionRow,
  ConditionsSummary,
} from "@/types";

// MOON_PHASES buckets a phase angle into the eight names the Tonight page already translates
// (i18n tonight.moonPhase.*), so the logbook reuses those keys instead of minting a ninth spelling.
// Boundaries are the usual ±22.5° around each quarter; 0 = new, 90 = first quarter, 180 = full.
const MOON_PHASES = [
  "new",
  "waxing_crescent",
  "first_quarter",
  "waxing_gibbous",
  "full",
  "waning_gibbous",
  "last_quarter",
  "waning_crescent",
] as const;

export type MoonPhase = (typeof MOON_PHASES)[number];

export function moonPhaseKey(angleDeg: number): MoonPhase {
  const a = ((angleDeg % 360) + 360) % 360;
  return MOON_PHASES[Math.floor((a + 22.5) / 45) % 8];
}

// hasConditions distinguishes a session that was sampled from one captured before the logbook
// existed. The API sends {} rather than null for the latter, so a field probe is the honest test.
export function hasConditions(
  s: CaptureSessionRow["conditions_summary"] | null | undefined,
): s is ConditionsSummary {
  return (
    !!s &&
    typeof s === "object" &&
    "samples" in s &&
    (s as ConditionsSummary).samples > 0
  );
}

// nightKey buckets an instant into the OBSERVING night it belongs to, at local noon — so a sub taken
// at 02:00 belongs to the previous evening. This is the same rule the stacker uses to group frames
// (Go: inspect.NightKey), and the logbook has to agree with it or the two disagree about what a
// "night" is.
export function nightKey(ms: number): string {
  if (!ms) return "";
  const d = new Date(ms - 12 * 3600_000);
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
}

// sessionDurationMs is how long the run lasted; a still-running session is measured to now.
export function sessionDurationMs(
  s: CaptureSessionRow,
  now = Date.now(),
): number {
  if (!s.started_at) return 0;
  return Math.max(0, (s.ended_at || now) - s.started_at);
}

// integrationMs is the total open-shutter time of the LIGHT frames — the number that actually
// predicts how deep the stack will go, unlike the wall-clock duration.
export function integrationMs(stats: CaptureFrameStat[]): number {
  return stats
    .filter((s) => isLight(s.frame_type))
    .reduce((sum, s) => sum + s.total_exposure_us / 1000, 0);
}

// The sequencer writes "light" while inspect classifies "LIGHT"; accept either.
export function isLight(frameType: string): boolean {
  return frameType.toLowerCase() === "light";
}

// perFilterCounts prefers the frame rows (which know what was really written) and falls back to the
// session's cached progress counters, which are all a session from before frame aggregation has.
export function perFilterCounts(
  stats: CaptureFrameStat[],
  progressCaptured?: Record<string, number>,
): [string, number][] {
  if (stats.length) {
    const out = new Map<string, number>();
    for (const s of stats.filter((x) => isLight(x.frame_type))) {
      out.set(s.filter, (out.get(s.filter) ?? 0) + s.frames);
    }
    return [...out.entries()];
  }
  return Object.entries(progressCaptured ?? {}).filter(([, n]) => n > 0);
}

// skyScore condenses a night into one 0–100 number for the list's at-a-glance cell.
//
// It is the median hourly verdict the weather provider already computed — deliberately NOT a new
// scoring formula. Sessions with no weather record return null so the UI can show "not recorded"
// rather than a zero that reads as "terrible night".
export function skyScore(summary: ConditionsSummary | null): number | null {
  if (!summary || !summary.verdict || summary.verdict.n === 0) return null;
  return summary.verdict.median;
}

// moonPenalty is how much the Moon threatened this session: bright and close is bad, and either one
// alone is not. Returns 0..1, or null when the session carried no target to measure against.
export function moonPenalty(summary: ConditionsSummary | null): number | null {
  if (!summary || !summary.target_valid || !summary.moon_up) return null;
  const sep = Math.max(0, Math.min(180, summary.moon_sep_min_deg));
  const proximity = 1 - sep / 180;
  return Math.max(0, Math.min(1, summary.moon_illum_max * proximity));
}

// weatherRecorded reports whether any sample actually carried weather, so an all-zero chart can say
// "the feeds were down" instead of drawing a flat clear-sky line.
export function weatherRecorded(rows: CaptureConditionRow[]): boolean {
  return rows.some((r) => r.source !== "unavailable");
}

// fmtDeg / fmtOne keep the detail page's many small numbers consistent.
export function fmtDeg(v: number, digits = 0): string {
  return `${v.toFixed(digits)}°`;
}

export function fmtOne(v: number): string {
  return v.toFixed(1);
}
