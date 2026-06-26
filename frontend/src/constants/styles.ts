// Shared class combinations (complete static strings — JIT-safe, never concatenated dynamically).

export const btn =
  "inline-flex items-center justify-center gap-2 rounded-md px-3 py-2 text-sm font-medium transition-colors disabled:cursor-not-allowed disabled:opacity-50";

export const btnPrimary = `${btn} bg-brand-600 text-white hover:bg-brand-500`;

export const btnGhost = `${btn} bg-slate-200 text-slate-800 hover:bg-slate-300 dark:bg-slate-700 dark:text-slate-100 dark:hover:bg-slate-600`;

export const btnDanger = `${btn} bg-danger-600 text-white hover:bg-danger-500`;

export const card =
  "rounded-lg border border-slate-200 bg-white p-4 shadow-sm dark:border-slate-700 dark:bg-surface-raised";

// cardElevated is a raised panel variant (log console, viewer toolbar) one step lighter in dark mode.
export const cardElevated =
  "rounded-lg border border-slate-200 bg-white p-4 shadow-sm dark:border-slate-700 dark:bg-surface-elevated";

export const input =
  "w-full rounded-md border border-slate-300 bg-white px-3 py-2 text-sm text-slate-900 placeholder-slate-400 focus:border-brand-500 focus:outline-none focus:ring-1 focus:ring-brand-500 dark:border-slate-600 dark:bg-slate-900 dark:text-slate-100";

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

// statusPill: job status chip colors (incl. cancelled).
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
};

// Folder entry pill: base vs selected (the capture chosen to process).
export const entryBase =
  "inline-flex items-center gap-1.5 rounded-md border border-slate-200 px-2 py-1 text-xs text-slate-600 transition-colors hover:border-brand-400 hover:text-brand-600 dark:border-slate-700 dark:text-slate-300 dark:hover:border-brand-500";
export const entrySelected =
  "inline-flex items-center gap-1.5 rounded-md border border-brand-500 bg-brand-50 px-2 py-1 text-xs font-medium text-brand-700 ring-1 ring-brand-500 dark:border-brand-500 dark:bg-brand-900/30 dark:text-brand-200";
