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

// Night-sky background canvas — subtle natural star tints + real comet colors on the grey-black sky.
// All tints sit at high lightness / low chroma so they read as "near-white with a hint of color"
// on #0b0b0d, never neon. Consumed only by composables/useNightSky.ts (a canvas can't use classes).
export const NIGHT_SKY = {
  // Stars — spectral tints, weighted in generate(): ~65% white, ~17% warm, ~11% yellow, ~7% blue.
  starWhite: ["#f7f8fb", "#eef1f7"], // A/F white & blue-white (the majority)
  starWarm: ["#f1d7b4", "#ebcca2"], // K-type light orange
  starYellow: ["#f6eed4", "#f3ebcd"], // G-type yellow-white
  starBlue: ["#d2e1ff", "#c4d6ff"], // B-type distinct blue (the minority)
  starLight: "#a1a1aa", // light-mode single faint grey (visible on a pale background)

  // Comets — natural coma/tail schemes (green C2 coma, blue ion tail, pale gold dust). Subtle:
  // alphas are baked low and further multiplied by globalAlpha at draw time.
  cometSchemes: [
    {
      coma: "rgba(140,214,178,0.55)",
      core: "#ecfff5",
      tailHead: "rgba(150,200,232,0.55)",
      tail: "rgba(120,165,230,0.45)",
    }, // green coma + blue ion
    {
      coma: "rgba(140,206,232,0.50)",
      core: "#eef7ff",
      tailHead: "rgba(150,196,236,0.50)",
      tail: "rgba(120,160,232,0.45)",
    }, // cyan/blue ion
    {
      coma: "rgba(228,210,164,0.50)",
      core: "#fff7e8",
      tailHead: "rgba(226,206,158,0.50)",
      tail: "rgba(214,194,150,0.42)",
    }, // pale gold dust
    {
      coma: "rgba(150,222,198,0.50)",
      core: "#f1fff8",
      tailHead: "rgba(196,224,212,0.50)",
      tail: "rgba(176,212,200,0.42)",
    }, // green coma + teal/white
  ],

  // Shooting stars (meteors) — fast, brief streaks that flare and burn out. core = hot head, glow =
  // soft head bloom, train = the tapering wake. Mostly blue-white (fast), some green/yellow (ablation).
  meteorSchemes: [
    {
      core: "#f4f8ff",
      glow: "rgba(196,216,255,0.95)",
      train: "rgba(150,188,255,0.80)",
    }, // blue-white (fast, common)
    {
      core: "#f3fff6",
      glow: "rgba(170,242,200,0.90)",
      train: "rgba(150,228,188,0.72)",
    }, // green (magnesium)
    {
      core: "#fff4e6",
      glow: "rgba(255,212,150,0.90)",
      train: "rgba(244,196,140,0.72)",
    }, // yellow-orange (sodium/iron)
  ],

  skyGlow: "rgba(38, 38, 44, 0.5)", // subtle grey lift from the top (depth), fades to transparent
} as const;

// Tonight altitude chart + polar sky map (ECharts canvases — never referenced in templates).
export const CHART_GRID = "#1f2937";
export const CHART_ALT_LINE = "#818cf8"; // brand-400
export const CHART_ALT_FILL = "#6366f1"; // brand-500
export const CHART_DARK_BAND = "rgba(99,102,241,0.12)";
export const CHART_HORIZON = "#64748b";
export const CHART_MINALT = "#f59e0b";
export const CHART_TRANSIT = "#22c55e";
export const CHART_NOW = "#e11d48";
export const CHART_SUN = "#fbbf24"; // amber-400 (sun curve + sunset/sunrise)
export const CHART_MOON = "#cbd5e1"; // slate-300 (moon curve + moonrise/moonset)
export const MAP_SELECTED = "#f8fafc"; // near-white ring + label for the selected target on the sky map

// Interactive GoTo sky map (raw canvas — never referenced in templates). A deep night sky, faint
// constellation "bars", the target star glowing amber, and Moon/planet landmark hues.
export const SKY_MAP = {
  bgTop: "#0b1220",
  bgBottom: "#020617",
  star: "#e8edf6", // star fill (alpha scaled by brightness at draw time)
  line: "rgba(129,140,248,0.35)", // constellation figure lines (brand-400, faint)
  horizon: "#334155", // horizon circle
  cardinal: "#94a3b8", // N/E/S/W labels
  starLabel: "#cbd5e1", // named-star labels
  conLabel: "rgba(148,163,184,0.75)", // constellation-name labels
  targetCore: "#fcd34d", // amber-300 target star
  targetRing: "#6366f1", // brand-500 highlight ring
  targetGlow: "#fbbf24", // amber-400 glow
  targetLabel: "#fde68a", // amber-200 target label
  moon: "#e2e8f0",
  moonDark: "#334155", // unlit side of the moon disc
  planet: "#fbbf24", // amber planet dot + label
} as const;

// Score-tier hexes for the polar sky-map markers (mirror scoreTierBar in constants/styles.ts).
export const SCORE_TIER_HEX: Record<string, string> = {
  excellent: "#22c55e",
  good: "#6366f1",
  fair: "#f59e0b",
  poor: "#94a3b8",
};

// Distinct hues for "processed together" folder groups in the file browser (used as an inline dot
// colour, not a Tailwind class). A folder processed on its own gets PROCESSED_SINGLE.
export const PROCESSED_GROUP_COLORS = [
  "#818cf8",
  "#34d399",
  "#fbbf24",
  "#f472b6",
  "#22d3ee",
  "#c084fc",
  "#fb7185",
  "#2dd4bf",
];
export const PROCESSED_SINGLE = "#94a3b8"; // neutral grey mark for a single-folder processing
