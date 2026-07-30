// Client-side equatorial → horizontal transform for scrubbing the sky map through the night without
// a round-trip per step. This is a direct port of the backend's internal/astro math (Meeus eq. 12.4
// GMST + the atan2 alt/az form) so a scrubbed position lands exactly where the backend's
// alt_now/az_now would for the same instant.

const DEG2RAD = Math.PI / 180;
const RAD2DEG = 180 / Math.PI;
const MS_PER_DAY = 86_400_000;
const UNIX_EPOCH_JD = 2440587.5;
const J2000 = 2451545.0;

function norm360(d: number): number {
  const r = d % 360;
  return r < 0 ? r + 360 : r;
}

// gmstDeg is the Greenwich Mean Sidereal Time in degrees [0,360) (Meeus, eq. 12.4).
function gmstDeg(tMs: number): number {
  const jd = tMs / MS_PER_DAY + UNIX_EPOCH_JD;
  const d = jd - J2000;
  const c = d / 36525;
  return norm360(
    280.46061837 +
      360.98564736629 * d +
      0.000387933 * c * c -
      (c * c * c) / 38710000,
  );
}

export interface AltAz {
  altDeg: number;
  azDeg: number; // compass convention: N=0, E=90, S=180, W=270
}

// altAzAt converts RA/Dec (degrees) to geometric altitude/azimuth for an observer at latDeg/lonDeg
// (east-positive) at epoch-ms tMs. No refraction — matches the map's existing alt_now semantics.
export function altAzAt(
  raDeg: number,
  decDeg: number,
  latDeg: number,
  lonDeg: number,
  tMs: number,
): AltAz {
  const lstDeg = norm360(gmstDeg(tMs) + lonDeg);
  let haDeg = lstDeg - raDeg; // hour angle, normalized to (-180,180]
  haDeg = ((haDeg % 360) + 360) % 360;
  if (haDeg > 180) haDeg -= 360;

  const h = haDeg * DEG2RAD;
  const lat = latDeg * DEG2RAD;
  const dec = decDeg * DEG2RAD;

  const sinAlt =
    Math.sin(lat) * Math.sin(dec) + Math.cos(lat) * Math.cos(dec) * Math.cos(h);
  const altDeg = Math.asin(Math.min(1, Math.max(-1, sinAlt))) * RAD2DEG;

  // Azimuth from South (positive toward West), rotated to from-North/east-positive.
  const y = Math.sin(h) * Math.cos(dec);
  const x =
    Math.cos(h) * Math.sin(lat) * Math.cos(dec) - Math.sin(dec) * Math.cos(lat);
  const azDeg = norm360(Math.atan2(y, x) * RAD2DEG + 180);
  return { altDeg, azDeg };
}
