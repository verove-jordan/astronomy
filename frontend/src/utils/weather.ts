// Colour ramps, thresholds and formatters for the astronomy-weather panel + animated map layers.
// Two colour contexts: the PANEL uses a consistent green→red "quality" scale (green = good observing),
// while the MAP grid overlay uses metric-specific raster ramps (white clouds, blue humidity) so it reads
// like a weather map.
import type { WeatherHour } from "@/types";

function clamp01(x: number): number {
  return x < 0 ? 0 : x > 1 ? 1 : x;
}

// goodBad maps a value to a green→red quality colour. invert=true means higher values are better.
export function goodBad(
  value: number,
  lo: number,
  hi: number,
  invert = false,
): string {
  let t = clamp01((value - lo) / (hi - lo));
  if (invert) t = 1 - t;
  const hue = Math.round(140 * (1 - t)); // 140 = green … 0 = red
  return `hsl(${hue} 70% 42%)`;
}

export function verdictColor(v: number): string {
  return goodBad(v, 0, 100, true);
}

// verdictLabel buckets a 0..100 observability score into a worded rating (i18n key suffix under
// tonight.weather.verdictLabel.*) — the at-a-glance "is tonight any good?" answer.
export function verdictLabel(
  v: number,
): "excellent" | "good" | "fair" | "poor" {
  if (v >= 80) return "excellent";
  if (v >= 60) return "good";
  if (v >= 40) return "fair";
  return "poor";
}

// tempColor is an INFORMATIONAL air-temperature ramp (cold blue → warm red), not a good/bad quality
// scale: temperature doesn't make a night "better" or "worse" for observing, it just reports the value
// (planning warm layers, frost/dew). −15 °C → blue, 40 °C → red.
export function tempColor(c: number): string {
  const t = clamp01((c + 15) / 55);
  const hue = Math.round(210 * (1 - t)); // 210 = blue … 0 = red
  return `hsl(${hue} 60% 45%)`;
}

const DEW_RISK_COLOR: Record<string, string> = {
  low: "hsl(140 70% 42%)",
  moderate: "hsl(40 85% 48%)",
  high: "hsl(0 75% 50%)",
};
export function dewRiskColor(risk: string): string {
  return DEW_RISK_COLOR[risk] ?? "hsl(0 0% 50%)";
}

export function auroraColor(level: string): string {
  return level === "likely"
    ? "hsl(150 70% 45%)"
    : level === "possible"
      ? "hsl(95 60% 45%)"
      : "hsl(0 0% 45%)";
}

// --- Per-metric panel rows -------------------------------------------------------------------------

// WeatherMetric describes one row of the forecast panel: how to read, colour and format an hour's value.
// `icon` is an id the panel maps to an icon component; `has` is false when the feed didn't supply it.
export interface WeatherMetric {
  key: string;
  labelKey: string;
  icon: string;
  has: (h: WeatherHour) => boolean;
  text: (h: WeatherHour) => string;
  color: (h: WeatherHour) => string;
}

export const WEATHER_METRICS: WeatherMetric[] = [
  {
    key: "clouds",
    labelKey: "tonight.weather.metrics.clouds",
    icon: "cloud",
    has: () => true,
    text: (h) => `${Math.round(h.cloud_pct)}%`,
    color: (h) => goodBad(h.cloud_pct, 0, 100),
  },
  {
    key: "seeing",
    labelKey: "tonight.weather.metrics.seeing",
    icon: "seeing",
    has: (h) => h.seeing_arcsec > 0,
    text: (h) => `${h.seeing_arcsec.toFixed(1)}″`,
    color: (h) => goodBad(h.seeing_arcsec, 0.6, 3.0),
  },
  {
    key: "transparency",
    labelKey: "tonight.weather.metrics.transparency",
    icon: "transparency",
    has: (h) => h.transparency > 0,
    text: (h) => `${Math.round(h.transparency * 100)}%`,
    color: (h) => goodBad(h.transparency, 0, 1, true),
  },
  {
    key: "humidity",
    labelKey: "tonight.weather.metrics.humidity",
    icon: "droplet",
    has: (h) => h.humidity_pct > 0,
    text: (h) => `${Math.round(h.humidity_pct)}%`,
    color: (h) => goodBad(h.humidity_pct, 60, 100),
  },
  {
    // Air temperature — supplied by Open-Meteo (the forecast backbone), so always present. Coloured on an
    // informational cold→warm ramp, not the good/bad quality scale (see tempColor).
    key: "temp",
    labelKey: "tonight.weather.metrics.temp",
    icon: "thermo",
    has: () => true,
    text: (h) => `${Math.round(h.temp_c)}°`,
    color: (h) => tempColor(h.temp_c),
  },
  {
    key: "dew",
    labelKey: "tonight.weather.metrics.dew",
    icon: "droplet",
    has: () => true,
    text: (h) => `${h.dew_spread_c.toFixed(0)}°`,
    color: (h) => dewRiskColor(h.dew_risk),
  },
  {
    key: "wind",
    labelKey: "tonight.weather.metrics.wind",
    icon: "wind",
    has: () => true,
    text: (h) => `${Math.round(h.wind_kmh)}`,
    color: (h) => goodBad(h.wind_kmh, 5, 40),
  },
  {
    key: "jet",
    labelKey: "tonight.weather.metrics.jet",
    icon: "wind",
    has: (h) => h.jet300_kmh > 0,
    text: (h) => `${Math.round(h.jet300_kmh)}`,
    color: (h) => goodBad(h.jet300_kmh, 30, 130),
  },
  {
    key: "instability",
    labelKey: "tonight.weather.metrics.instability",
    icon: "seeing",
    has: () => true,
    text: (h) => h.lifted_index.toFixed(0),
    color: (h) => goodBad(h.lifted_index, -4, 2, true),
  },
  {
    key: "aerosols",
    labelKey: "tonight.weather.metrics.aerosols",
    icon: "haze",
    has: (h) => h.aod > 0,
    text: (h) => h.aod.toFixed(2),
    color: (h) => goodBad(h.aod, 0.05, 0.6),
  },
];

// --- Animated map grid layers ----------------------------------------------------------------------

export type RGBA = [number, number, number, number]; // r,g,b in 0..255, a in 0..1

// GridBandDef is one altitude band of a composite layer (see GridLayerDef.bands).
export interface GridBandDef {
  metric: string; // backend grid layer key for this band
  labelKey: string;
  color: (v: number) => RGBA;
  gradient: string[]; // legend gradient stops for this band's legend row
}

export interface GridLayerDef {
  id: string; // overlay id in the map-layer registry
  metric: string; // backend grid layer key (the total: timeline value + raster fallback)
  labelKey: string;
  // color maps a cell value (cloud %, humidity %, precip %) to an RGBA the canvas paints.
  color: (v: number) => RGBA;
  // legend gradient stops (left → right) for the map legend.
  gradient: string[];
  // Optional altitude bands, in paint order (index 0 composited first = bottom). When present and the
  // cube carries every band metric, the map source-over composites these instead of painting `color` —
  // low/mid/high clouds are physically different things (a dense blanket vs a thin cirrus veil) and
  // should read that way. Older cubes without the band layers fall back to `color`.
  bands?: GridBandDef[];
}

// rampRGBA interpolates a value through colour stops (sorted by `at`) so the weather rasters vary in HUE
// across their range — a single-hue alpha ramp is unreadable when values cluster, as regional humidity
// does (it sits in a narrow band at any one hour).
function rampRGBA(value: number, stops: { at: number; rgba: RGBA }[]): RGBA {
  if (value <= stops[0].at) return stops[0].rgba;
  const last = stops[stops.length - 1];
  if (value >= last.at) return last.rgba;
  for (let i = 1; i < stops.length; i++) {
    if (value <= stops[i].at) {
      const a = stops[i - 1];
      const b = stops[i];
      const t = (value - a.at) / (b.at - a.at);
      return [
        Math.round(a.rgba[0] + (b.rgba[0] - a.rgba[0]) * t),
        Math.round(a.rgba[1] + (b.rgba[1] - a.rgba[1]) * t),
        Math.round(a.rgba[2] + (b.rgba[2] - a.rgba[2]) * t),
        a.rgba[3] + (b.rgba[3] - a.rgba[3]) * t,
      ];
    }
  }
  return last.rgba;
}

// bandColor builds an altitude band's colour ramp: a fixed body colour whose opacity follows the cover
// % through a perceptual gamma (>1 keeps scattered cover airy while full cover still bites), so painted
// intensity tracks the actual cloud amount instead of saturating early.
function bandColor(
  rgb: [number, number, number],
  maxAlpha: number,
  gamma: number,
): (pct: number) => RGBA {
  return (pct) => [
    rgb[0],
    rgb[1],
    rgb[2],
    maxAlpha * Math.pow(clamp01(pct / 100), gamma),
  ];
}

export const GRID_LAYERS: GridLayerDef[] = [
  {
    // Cloud cover, painted from the low/mid/high altitude bands composited bottom→top as high → mid →
    // low: a thin cool cirrus veil under a blue-grey mid deck under a dense warm-white low blanket, each
    // with a perceptual (gamma) alpha so intensity is true to the actual cover. `color` is the
    // total-cover fallback for cubes that don't carry the bands: the same perceptual curve, luminance
    // rising 210→250 so heavier cover also reads brighter, not just more opaque.
    id: "clouds",
    metric: "clouds",
    labelKey: "tonight.layers.clouds",
    color: (pct) => {
      const t = Math.pow(clamp01(pct / 100), 1.1);
      const lum = Math.round(210 + 40 * t);
      return [lum, lum, lum, 0.95 * t];
    },
    gradient: [
      "rgba(210,210,210,0)",
      "rgba(235,235,235,0.5)",
      "rgba(250,250,250,0.95)",
    ],
    bands: [
      {
        metric: "clouds_high",
        labelKey: "tonight.layers.cloudHigh",
        color: bandColor([190, 215, 235], 0.45, 1.15),
        gradient: ["rgba(190,215,235,0)", "rgba(190,215,235,0.45)"],
      },
      {
        metric: "clouds_mid",
        labelKey: "tonight.layers.cloudMid",
        color: bandColor([205, 215, 228], 0.7, 1.1),
        gradient: ["rgba(205,215,228,0)", "rgba(205,215,228,0.7)"],
      },
      {
        metric: "clouds_low",
        labelKey: "tonight.layers.cloudLow",
        color: bandColor([236, 240, 246], 0.95, 1.05),
        gradient: ["rgba(236,240,246,0)", "rgba(236,240,246,0.95)"],
      },
    ],
  },
  {
    // Humidity: green (dry) → red (≥95%, dew / poor transparency). A steep hue ramp through the 70–100%
    // band where regional humidity actually sits, so even a few % difference is legible.
    id: "humidity",
    metric: "humidity",
    labelKey: "tonight.layers.humidity",
    color: (rh) =>
      rampRGBA(rh, [
        { at: 50, rgba: [80, 190, 120, 0.25] },
        { at: 70, rgba: [150, 205, 80, 0.42] },
        { at: 85, rgba: [240, 195, 60, 0.55] },
        { at: 93, rgba: [240, 130, 55, 0.64] },
        { at: 100, rgba: [232, 70, 70, 0.72] },
      ]),
    gradient: [
      "rgba(80,190,120,0.35)",
      "rgba(240,195,60,0.55)",
      "rgba(232,70,70,0.72)",
    ],
  },
  {
    // Rain chance (precipitation probability %): blue → violet, visible from a modest chance — the rain
    // amount in mm is ~0 almost everywhere and never showed. Alpha onset at 12% so a background drizzle
    // probability doesn't wash the whole map blue.
    id: "precip",
    metric: "precip",
    labelKey: "tonight.layers.precip",
    color: (prob) =>
      rampRGBA(prob, [
        { at: 0, rgba: [60, 170, 230, 0] },
        { at: 12, rgba: [60, 170, 230, 0.35] },
        { at: 40, rgba: [45, 115, 230, 0.62] },
        { at: 70, rgba: [80, 70, 220, 0.8] },
        { at: 100, rgba: [125, 45, 200, 0.9] },
      ]),
    gradient: [
      "rgba(60,170,230,0)",
      "rgba(45,115,230,0.62)",
      "rgba(125,45,200,0.9)",
    ],
  },
];

export function gridLayerById(id: string): GridLayerDef | undefined {
  return GRID_LAYERS.find((l) => l.id === id);
}
