// Turning a catalogue row into things a person can read.
//
// The engine ships raw astrophysics — parsecs, absolute magnitude, B−V colour index — because that
// is what the catalogue measured and storing anything else would be storing an opinion. Every
// derivation lives here instead: light years, solar luminosities, an effective temperature. They are
// all exact functions of the shipped numbers, so nothing is lost by deriving them at display time,
// and a formula that turns out to be wrong is fixed in one place rather than baked into every
// stars.json ever written.

import type { StarCatalogInfo } from "@/types";

export const LY_PER_PARSEC = 3.26156;

// The Sun's absolute visual magnitude — the zero point every other star's luminosity is measured
// against.
export const SUN_ABS_MAG = 4.83;

/** Distance in light years, or null when the catalogue had no usable parallax. */
export function lightYears(info?: StarCatalogInfo | null): number | null {
  const pc = info?.dist_pc;
  if (!pc || pc <= 0 || !Number.isFinite(pc)) return null;
  return pc * LY_PER_PARSEC;
}

/**
 * Luminosity in solar units from the absolute magnitude: L/L☉ = 10^((M☉ − M)/2.5).
 * Null when the star has no absolute magnitude — note that absmag 0 is a real value, so only
 * null/undefined mean "unknown".
 */
export function solarLuminosity(info?: StarCatalogInfo | null): number | null {
  const m = info?.absmag;
  if (m === null || m === undefined || !Number.isFinite(m)) return null;
  return Math.pow(10, (SUN_ABS_MAG - m) / 2.5);
}

/**
 * Effective surface temperature in kelvin from the B−V colour index, by Ballesteros' formula
 * (2012): T = 4600·[1/(0.92·BV + 1.7) + 1/(0.92·BV + 0.62)]. It reproduces the Sun to within a few
 * kelvin and stays good across the main sequence — an approximation, and labelled as one in the UI.
 * Null when there is no colour index (0 is a real B−V, so only null/undefined mean unknown).
 *
 * The guard is on the INPUT: the fit was made over roughly -0.4…2.0 in B−V, and beyond that it
 * degrades into numbers that still look like plausible temperatures (B−V 5 returns 1611 K — no star,
 * but nothing about 1611 K announces that). Kept identical to bvToTemperatureK in
 * internal/scene3d/colour.go, which colours the 3D field map: a star's rendered hue and the
 * temperature written beside it must come from one relation, not two that drift.
 */
export const BV_FIT_MIN = -0.4;
export const BV_FIT_MAX = 2.0;

export function effectiveTemperatureK(
  info?: StarCatalogInfo | null,
): number | null {
  const bv = info?.ci;
  if (bv === null || bv === undefined || !Number.isFinite(bv)) return null;
  if (bv < BV_FIT_MIN || bv > BV_FIT_MAX) return null;
  const a = 0.92 * bv;
  const t = 4600 * (1 / (a + 1.7) + 1 / (a + 0.62));
  return Number.isFinite(t) && t > 1000 && t < 60000 ? t : null;
}

/**
 * The Harvard spectral class letter (O B A F G K M), which is what carries the star's colour and
 * temperature in a type like "B7 III/IV". "" when the type is absent or non-standard.
 */
export function spectralClass(info?: StarCatalogInfo | null): string {
  const m = /^[OBAFGKM]/.exec((info?.spect ?? "").trim());
  return m ? m[0] : "";
}

/** Right ascension as sexagesimal hours, the form every catalogue and planetarium accepts. */
export function formatRA(deg?: number): string {
  if (deg === undefined || !Number.isFinite(deg)) return "";
  const hours = (((deg % 360) + 360) % 360) / 15;
  const h = Math.floor(hours);
  const m = Math.floor((hours - h) * 60);
  const s = ((hours - h) * 60 - m) * 60;
  return `${pad(h)}h ${pad(m)}m ${s.toFixed(1).padStart(4, "0")}s`;
}

/** Declination as signed sexagesimal degrees. */
export function formatDec(deg?: number): string {
  if (deg === undefined || !Number.isFinite(deg)) return "";
  const sign = deg < 0 ? "−" : "+";
  const a = Math.abs(deg);
  const d = Math.floor(a);
  const m = Math.floor((a - d) * 60);
  const s = Math.round(((a - d) * 60 - m) * 60);
  return `${sign}${pad(d)}° ${pad(m)}′ ${pad(s)}″`;
}

/**
 * Compact number for display: enough significant figures to be useful, never a wall of digits.
 * 0.0004 → "0.0004", 1.7 → "1.7", 470 → "470", 12345 → "12 300".
 */
export function compact(v: number): string {
  const a = Math.abs(v);
  if (a === 0) return "0";
  if (a < 0.01) return v.toPrecision(1);
  if (a < 10) return trimZeros(v.toFixed(2));
  if (a < 1000) return v.toFixed(0);
  return Number(v.toPrecision(3)).toLocaleString("en-US").replace(/,/g, " ");
}

function trimZeros(s: string): string {
  return s.replace(/\.?0+$/, "");
}

function pad(v: number): string {
  return String(v).padStart(2, "0");
}
