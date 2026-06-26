// JS mirror of the domain color *values* defined in tailwind.config.js (the config is authoritative).
// Used by consumers that need raw hex — ECharts and the zoom navigator canvas — never in templates.

export const FILTER_HEX: Record<string, string> = {
  L: "#cbd5e1",
  R: "#ef4444",
  G: "#22c55e",
  B: "#3b82f6",
  Ha: "#f43f5e",
  OIII: "#06b6d4",
  SII: "#f59e0b",
};

const BRAND = "#6366f1";

// filterHex resolves a filter token to its hex, falling back to the brand color.
export function filterHex(filter?: string): string {
  if (!filter) return BRAND;
  return FILTER_HEX[filter] ?? BRAND;
}

// Chart palette (replaces hardcoded hex in MetricsChart).
export const CHART_AXIS = "#94a3b8";
export const CHART_KEPT = BRAND;
export const CHART_REJECTED = "#dc2626";
export const CHART_LINE = "#22c55e";
export const NAV_RECT = BRAND;

// Night-sky background canvas — neutral silver/grey to match the grey-black sky (no blue cast).
export const NIGHT_SKY = {
  starCore: "#f5f5f6", // brightest stars
  starDim: "#d4d4d8", // most stars
  starWarm: "#ece7df", // a few faintly warm stars, for life
  starLight: "#a1a1aa", // faint dots in light mode (visible on a pale background)
  cometHead: "#f4f4f5",
  skyGlow: "rgba(38, 38, 44, 0.5)", // subtle grey lift from the top (depth), fades to transparent
};
