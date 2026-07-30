// RA/Dec sexagesimal formatting for the mosaic capture assistant. Two flavors per axis: a pretty
// display string with unit glyphs, and a bare zero-padded "hand-controller" variant matching what
// the Celestron/SynScan coordinate screens expect on their keypads. All inputs are decimal degrees
// (the API's only coordinate format); formatting is a display concern and lives client-side only.

function pad2(n: number): string {
  return n < 10 ? `0${n}` : String(n);
}

function norm360(d: number): number {
  const r = d % 360;
  return r < 0 ? r + 360 : r;
}

// raParts splits an RA into hours/minutes/seconds with the requested seconds precision, carrying
// rounding overflow up (59.95s → next minute; 24h wraps to 0h).
function raParts(
  raDeg: number,
  secDecimals: number,
): { h: number; m: number; s: number } {
  const scale = 10 ** secDecimals;
  let s = Math.round((norm360(raDeg) / 15) * 3600 * scale) / scale;
  const total = 24 * 3600;
  if (s >= total) s -= total;
  const h = Math.floor(s / 3600);
  s -= h * 3600;
  const m = Math.floor(s / 60);
  s -= m * 60;
  // Guard the 60.0s edge the float math can leave after the subtraction.
  if (s >= 60 - 0.5 / scale)
    return { h: (h + (m === 59 ? 1 : 0)) % 24, m: (m + 1) % 60, s: 0 };
  return { h, m, s: Math.round(s * scale) / scale };
}

// decParts splits a declination into sign/degrees/arcminutes/arcseconds (integer arcseconds, carry
// on rounding; the sign is captured before |dec| so −0°12′ keeps its minus).
function decParts(decDeg: number): {
  sign: string;
  d: number;
  m: number;
  s: number;
} {
  const sign = decDeg < 0 ? "-" : "+";
  let s = Math.round(Math.abs(decDeg) * 3600);
  if (s > 90 * 3600) s = 90 * 3600;
  const d = Math.floor(s / 3600);
  s -= d * 3600;
  const m = Math.floor(s / 60);
  s -= m * 60;
  return { sign, d, m, s };
}

// raToHMS renders "05h 34m 31.9s" (1-decimal seconds).
export function raToHMS(raDeg: number): string {
  const { h, m, s } = raParts(raDeg, 1);
  return `${pad2(h)}h ${pad2(m)}m ${s < 10 ? "0" : ""}${s.toFixed(1)}s`;
}

// decToDMS renders "+22° 00′ 52″" (integer arcseconds).
export function decToDMS(decDeg: number): string {
  const { sign, d, m, s } = decParts(decDeg);
  return `${sign}${pad2(d)}° ${pad2(m)}′ ${pad2(s)}″`;
}

// raToHC renders the hand-controller keypad form "05 34 32" (integer seconds, zero-padded).
export function raToHC(raDeg: number): string {
  const { h, m, s } = raParts(raDeg, 0);
  return `${pad2(h)} ${pad2(m)} ${pad2(Math.round(s))}`;
}

// decToHC renders the hand-controller keypad form "+41 16 09" (sign always, even for −00°).
export function decToHC(decDeg: number): string {
  const { sign, d, m, s } = decParts(decDeg);
  return `${sign}${pad2(d)} ${pad2(m)} ${pad2(s)}`;
}
