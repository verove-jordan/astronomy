// Minimal client-side astronomy: a faithful port of the subset of the Go engine's `internal/astro` the
// sky map needs — J2000→epoch precession and equatorial→horizontal (alt/az) for the observer. Same
// formulae as the engine, so client alt/az matches the server's `alt_deg/az_deg` for any star.

const DEG = Math.PI / 180;
const RAD = 180 / Math.PI;
const J2000 = 2451545.0;
const UNIX_EPOCH_JD = 2440587.5;
const MS_PER_DAY = 86_400_000;

function norm360(d: number): number {
  d = d % 360;
  return d < 0 ? d + 360 : d;
}
function norm180(d: number): number {
  d = norm360(d);
  return d > 180 ? d - 360 : d;
}
function clamp1(x: number): number {
  return x < -1 ? -1 : x > 1 ? 1 : x;
}

export function julianDate(msUTC: number): number {
  return msUTC / MS_PER_DAY + UNIX_EPOCH_JD;
}

// GMST in degrees [0,360) — Meeus eq. 12.4 (matches astro/time.go).
export function gmstDeg(msUTC: number): number {
  const jd = julianDate(msUTC);
  const d = jd - J2000;
  const c = (jd - J2000) / 36525;
  const g =
    280.46061837 +
    360.98564736629 * d +
    0.000387933 * c * c -
    (c * c * c) / 38_710_000;
  return norm360(g);
}

export function lstDeg(msUTC: number, lonDeg: number): number {
  return norm360(gmstDeg(msUTC) + lonDeg);
}

export function hourAngleDeg(
  raDeg: number,
  lonDeg: number,
  msUTC: number,
): number {
  return norm180(lstDeg(msUTC, lonDeg) - raDeg);
}

export interface AltAz {
  alt: number;
  az: number;
}

// equatorialToHorizontal → GEOMETRIC alt/az (degrees). Azimuth is the compass convention: from North,
// increasing eastward (N=0, E=90, S=180, W=270). atan2 form, pole-stable (matches astro/coords.go).
export function equatorialToHorizontal(
  raDeg: number,
  decDeg: number,
  latDeg: number,
  lonDeg: number,
  msUTC: number,
): AltAz {
  const h = hourAngleDeg(raDeg, lonDeg, msUTC) * DEG;
  const lat = latDeg * DEG;
  const dec = decDeg * DEG;
  const sinAlt =
    Math.sin(lat) * Math.sin(dec) + Math.cos(lat) * Math.cos(dec) * Math.cos(h);
  const alt = Math.asin(clamp1(sinAlt)) * RAD;
  const y = Math.sin(h) * Math.cos(dec);
  const x =
    Math.cos(h) * Math.sin(lat) * Math.cos(dec) - Math.sin(dec) * Math.cos(lat);
  const az = norm360(Math.atan2(y, x) * RAD + 180);
  return { alt, az };
}

// precessFromJ2000 (IAU 1976 ζ,z,θ; Meeus eq. 21.4) — matches astro/poles.go. J2000 → epoch of msUTC.
export function precessFromJ2000(
  raDeg: number,
  decDeg: number,
  msUTC: number,
): { ra: number; dec: number } {
  const tc = (julianDate(msUTC) - J2000) / 36525;
  const zeta = (2306.2181 * tc + 0.30188 * tc * tc + 0.017998 * tc ** 3) / 3600;
  const z = (2306.2181 * tc + 1.09468 * tc * tc + 0.018203 * tc ** 3) / 3600;
  const theta =
    (2004.3109 * tc - 0.42665 * tc * tc - 0.041833 * tc ** 3) / 3600;
  const cosD = (x: number) => Math.cos(x * DEG);
  const sinD = (x: number) => Math.sin(x * DEG);
  const a = cosD(decDeg) * sinD(raDeg + zeta);
  const b =
    cosD(theta) * cosD(decDeg) * cosD(raDeg + zeta) -
    sinD(theta) * sinD(decDeg);
  const c =
    sinD(theta) * cosD(decDeg) * cosD(raDeg + zeta) +
    cosD(theta) * sinD(decDeg);
  return {
    ra: norm360(Math.atan2(a, b) * RAD + z),
    dec: Math.atan2(c, Math.hypot(a, b)) * RAD,
  };
}

// tangentPlane projects (raDeg,decDeg) onto the gnomonic tangent plane at (ra0,dec0): standard
// coordinates ξ (east-positive) / η (north-positive) in DEGREES. Mirrors astro/tangent.go — the
// mosaic tile math — so canvas previews and Aladin hit-testing agree with the server's tiles.
// Returns null at/beyond 90° from the tangent point.
export function tangentPlane(
  ra0: number,
  dec0: number,
  raDeg: number,
  decDeg: number,
): { xi: number; eta: number } | null {
  const sinDec = Math.sin(decDeg * DEG);
  const cosDec = Math.cos(decDeg * DEG);
  const sinDec0 = Math.sin(dec0 * DEG);
  const cosDec0 = Math.cos(dec0 * DEG);
  const dRa = (raDeg - ra0) * DEG;
  const div = sinDec * sinDec0 + cosDec * cosDec0 * Math.cos(dRa);
  if (div < 1e-12) return null;
  return {
    xi: ((cosDec * Math.sin(dRa)) / div) * RAD,
    eta: ((sinDec * cosDec0 - cosDec * sinDec0 * Math.cos(dRa)) / div) * RAD,
  };
}

// tangentSky inverts tangentPlane (ξ/η degrees → RA/Dec degrees, RA in [0,360)).
export function tangentSky(
  ra0: number,
  dec0: number,
  xiDeg: number,
  etaDeg: number,
): { ra: number; dec: number } {
  const xi = xiDeg * DEG;
  const eta = etaDeg * DEG;
  const sinDec0 = Math.sin(dec0 * DEG);
  const cosDec0 = Math.cos(dec0 * DEG);
  const norm = Math.sqrt(1 + xi * xi + eta * eta);
  return {
    dec: Math.asin(clamp1((sinDec0 + eta * cosDec0) / norm)) * RAD,
    ra: norm360(ra0 + Math.atan2(xi, cosDec0 - eta * sinDec0) * RAD),
  };
}

// apparentAltitude applies Saemundsson refraction to a geometric altitude (matches astro/coords.go).
export function apparentAltitude(trueAltDeg: number): number {
  const r =
    trueAltDeg < -1
      ? 0
      : 1.02 / Math.tan((trueAltDeg + 10.3 / (trueAltDeg + 5.11)) * DEG);
  return trueAltDeg + r / 60;
}
