// Shared class combinations (complete static strings — JIT-safe, never concatenated dynamically).

export const btn =
  "inline-flex items-center justify-center gap-2 rounded-md px-3 py-2 text-sm font-medium transition-colors disabled:cursor-not-allowed disabled:opacity-50";

export const btnPrimary = `${btn} bg-brand-600 text-white hover:bg-brand-500`;

export const btnGhost = `${btn} bg-slate-200 text-slate-800 hover:bg-slate-300 dark:bg-slate-700 dark:text-slate-100 dark:hover:bg-slate-600`;

export const btnDanger = `${btn} bg-danger-600 text-white hover:bg-danger-500`;

// Segmented control (a row of mutually-exclusive toggle buttons): the wrapper, each button, and the
// active/idle states. Shared by the calendar mode switch, the Tonight tab strip, the camera/eyepiece
// toggle and the polar-scope orientation toggle.
export const segWrap =
  "flex overflow-hidden rounded-md border border-slate-300 dark:border-slate-600";
export const segBtn = "px-2 py-1 text-xs transition-colors";
export const segActive = "bg-brand-600 text-white";
export const segIdle =
  "bg-transparent text-slate-500 hover:bg-slate-100 dark:hover:bg-slate-700";

export const card =
  "rounded-lg border border-slate-200 bg-white p-4 shadow-sm dark:border-slate-700 dark:bg-surface-raised";

// cardElevated is a raised panel variant (log console, viewer toolbar) one step lighter in dark mode.
export const cardElevated =
  "rounded-lg border border-slate-200 bg-white p-4 shadow-sm dark:border-slate-700 dark:bg-surface-elevated";

// Form fields harmonize with the indigo brand (buttons are brand-600 #4f46e5): a faint indigo-tinted
// surface + a soft indigo border at rest, brightening to a full brand ring on focus — instead of the
// old blue-shifted slate-900 fill, which read as a different (blue) hue next to the indigo buttons.
export const input =
  "w-full rounded-md border border-slate-300 bg-white px-3 py-2 text-sm text-slate-900 placeholder-slate-400 focus:border-brand-500 focus:outline-none focus:ring-1 focus:ring-brand-500 dark:border-brand-800/60 dark:bg-brand-900/20 dark:text-slate-100 dark:placeholder-slate-500";

// checkbox tints native checkboxes/radios to the brand indigo (accent-color = brand-600 #4f46e5, the
// button color), replacing the browser's default blue so they match the buttons and focus rings exactly.
export const checkbox = "accent-brand-600";

export const th =
  "cursor-pointer select-none whitespace-nowrap px-3 py-2 text-left text-xs font-semibold uppercase tracking-wide text-slate-500 hover:text-slate-800 dark:text-slate-400 dark:hover:text-slate-100";

export const td =
  "whitespace-nowrap px-3 py-2 text-sm text-slate-700 dark:text-slate-200";

// --- Domain color maps (complete strings, keyed by runtime value; JIT-safe) ---

// filterChip: per-filter chip colors. Each carries a ring so L (slate/white) stays visible on white,
// and a dark: counterpart for every utility. Always render the filter LETTER too (color-blind safe).
export const filterChip: Record<string, string> = {
  L: "bg-slate-100 text-slate-700 ring-1 ring-slate-300 dark:bg-slate-600/40 dark:text-slate-100 dark:ring-slate-500",
  R: "bg-red-100 text-red-800 ring-1 ring-red-200 dark:bg-red-900/40 dark:text-red-300 dark:ring-red-800/50",
  G: "bg-green-100 text-green-800 ring-1 ring-green-200 dark:bg-green-900/40 dark:text-green-300 dark:ring-green-800/50",
  B: "bg-blue-100 text-blue-800 ring-1 ring-blue-200 dark:bg-blue-900/40 dark:text-blue-300 dark:ring-blue-800/50",
  Ha: "bg-rose-100 text-rose-800 ring-1 ring-rose-200 dark:bg-rose-900/40 dark:text-rose-300 dark:ring-rose-800/50",
  OIII: "bg-cyan-100 text-cyan-800 ring-1 ring-cyan-200 dark:bg-cyan-900/40 dark:text-cyan-300 dark:ring-cyan-800/50",
  SII: "bg-amber-100 text-amber-800 ring-1 ring-amber-200 dark:bg-amber-900/40 dark:text-amber-300 dark:ring-amber-800/50",
};
export const filterChipFallback =
  "bg-slate-100 text-slate-700 ring-1 ring-slate-300 dark:bg-slate-600/40 dark:text-slate-100 dark:ring-slate-500";
export const filterChipClass = (filter?: string): string =>
  filterChip[filter ?? ""] ?? filterChipFallback;

// frameTypeAccent: text color for the big count numbers per frame type.
export const frameTypeAccent: Record<string, string> = {
  LIGHT: "text-brand-600 dark:text-brand-300",
  DARK: "text-violet-600 dark:text-violet-300",
  FLAT: "text-amber-600 dark:text-amber-300",
  DARKFLAT: "text-orange-600 dark:text-orange-300",
  BIAS: "text-cyan-600 dark:text-cyan-300",
};
export const frameTypeAccentFallback = "text-slate-600 dark:text-slate-300";
export const frameTypeAccentClass = (t?: string): string =>
  frameTypeAccent[t ?? ""] ?? frameTypeAccentFallback;

// frameTypeCard: left accent stripe per frame type, layered onto `card`.
export const frameTypeCard: Record<string, string> = {
  LIGHT: "border-l-4 border-l-brand-500",
  DARK: "border-l-4 border-l-violet-500",
  FLAT: "border-l-4 border-l-amber-500",
  DARKFLAT: "border-l-4 border-l-orange-500",
  BIAS: "border-l-4 border-l-cyan-500",
};
export const frameTypeCardFallback = "border-l-4 border-l-slate-400";
export const frameTypeCardClass = (t?: string): string =>
  frameTypeCard[t ?? ""] ?? frameTypeCardFallback;

// statusPill: job status chip colors (incl. cancelled + paused).
export const statusPill: Record<string, string> = {
  queued:
    "bg-amber-100 text-amber-800 dark:bg-amber-900/40 dark:text-amber-300",
  running:
    "bg-brand-100 text-brand-800 dark:bg-brand-900/40 dark:text-brand-300",
  succeeded:
    "bg-green-100 text-green-800 dark:bg-green-900/40 dark:text-green-300",
  failed: "bg-red-100 text-red-800 dark:bg-red-900/40 dark:text-red-300",
  cancelled:
    "bg-slate-200 text-slate-700 dark:bg-slate-700 dark:text-slate-300",
  paused: "bg-sky-100 text-sky-800 dark:bg-sky-900/40 dark:text-sky-300",
};

// Folder entry pill: base vs selected (the capture chosen to process).
export const entryBase =
  "inline-flex items-center gap-1.5 rounded-md border border-slate-200 px-2 py-1 text-xs text-slate-600 transition-colors hover:border-brand-400 hover:text-brand-600 dark:border-slate-700 dark:text-slate-300 dark:hover:border-brand-500";
export const entrySelected =
  "inline-flex items-center gap-1.5 rounded-md border border-brand-500 bg-brand-50 px-2 py-1 text-xs font-medium text-brand-700 ring-1 ring-brand-500 dark:border-brand-500 dark:bg-brand-900/30 dark:text-brand-200";

// --- Tonight / sky-target scoring (complete JIT-safe strings, keyed by runtime value) ---

export type ScoreTier = "excellent" | "good" | "fair" | "poor";

// scoreTier buckets a 0–100 score; mirrored by SCORE_TIER_HEX in constants/colors.ts for the sky map.
export function scoreTier(score: number): ScoreTier {
  if (score >= 80) return "excellent";
  if (score >= 60) return "good";
  if (score >= 40) return "fair";
  return "poor";
}

export const scoreTierPill: Record<ScoreTier, string> = {
  excellent:
    "bg-green-100 text-green-800 dark:bg-green-900/40 dark:text-green-300",
  good: "bg-brand-100 text-brand-800 dark:bg-brand-900/40 dark:text-brand-300",
  fair: "bg-amber-100 text-amber-800 dark:bg-amber-900/40 dark:text-amber-300",
  poor: "bg-slate-200 text-slate-600 dark:bg-slate-700 dark:text-slate-300",
};

// scoreTierBar: fill color for a ProgressBar tinted by tier.
export const scoreTierBar: Record<ScoreTier, string> = {
  excellent: "bg-green-500",
  good: "bg-brand-500",
  fair: "bg-amber-500",
  poor: "bg-slate-400 dark:bg-slate-500",
};

// skyTypePill: chip colors per derived object type.
export const skyTypePill: Record<string, string> = {
  galaxy:
    "bg-violet-100 text-violet-800 ring-1 ring-violet-200 dark:bg-violet-900/40 dark:text-violet-300 dark:ring-violet-800/50",
  nebula:
    "bg-rose-100 text-rose-800 ring-1 ring-rose-200 dark:bg-rose-900/40 dark:text-rose-300 dark:ring-rose-800/50",
  emission_nebula:
    "bg-rose-100 text-rose-800 ring-1 ring-rose-200 dark:bg-rose-900/40 dark:text-rose-300 dark:ring-rose-800/50",
  planetary_nebula:
    "bg-cyan-100 text-cyan-800 ring-1 ring-cyan-200 dark:bg-cyan-900/40 dark:text-cyan-300 dark:ring-cyan-800/50",
  dark_nebula:
    "bg-slate-200 text-slate-700 ring-1 ring-slate-300 dark:bg-slate-700/50 dark:text-slate-200 dark:ring-slate-600",
  cluster:
    "bg-amber-100 text-amber-800 ring-1 ring-amber-200 dark:bg-amber-900/40 dark:text-amber-300 dark:ring-amber-800/50",
  globular:
    "bg-orange-100 text-orange-800 ring-1 ring-orange-200 dark:bg-orange-900/40 dark:text-orange-300 dark:ring-orange-800/50",
  supernova_remnant:
    "bg-teal-100 text-teal-800 ring-1 ring-teal-200 dark:bg-teal-900/40 dark:text-teal-300 dark:ring-teal-800/50",
  other:
    "bg-slate-100 text-slate-700 ring-1 ring-slate-300 dark:bg-slate-700/40 dark:text-slate-200 dark:ring-slate-600",
};
export const skyTypePillClass = (objType?: string): string =>
  skyTypePill[objType ?? ""] ?? skyTypePill.other;

// emissionLineBar: bar fill color per emission line (mirrors the filter palette colors).
export const emissionLineBar: Record<string, string> = {
  ha: "bg-rose-500",
  oiii: "bg-cyan-500",
  sii: "bg-amber-500",
  hb: "bg-sky-500",
  nii: "bg-red-500",
};
