// The solar system's arithmetic, in the browser.
//
// This is a mirror of internal/solarsystem, not a second opinion: the engine serves the orbital
// elements and this propagates them, so scrubbing across two and a half centuries costs no round
// trips and the animation never stalls waiting for a server. solarsystem.spec.ts pins it against
// golden vectors the Go tests produce — the same mirrored-and-pinned arrangement utils/optics.ts has
// with internal/skyplan/optics.go.
//
// Everything here is pure: positions in AU, angles in degrees, no GL calls and no reactivity, so it
// is testable under happy-dom while the renderer that uses it is not.

import { julianDate, precessToJ2000 } from "@/utils/astro";
import { cross, normalize, type Vec3 } from "@/utils/mat4";
import type { SolarBody, SolarOrbitSpec, SolarPole } from "@/types";

const DEG = Math.PI / 180;
const RAD = 180 / Math.PI;

/** Must match solarsystem.ManifestVersion in Go. */
export const SOLAR_MANIFEST_VERSION = 1;

export const J2000 = 2451545.0;
export const KM_PER_AU = 149597870.7;
export const AU_PER_KM = 1 / KM_PER_AU;

/** The mean obliquity at J2000. The scene's frame is J2000, so this never varies with time. */
export const OBLIQUITY_J2000 = 23.43928;

/** The years the engine's orbital model is defended over. */
export const RANGE_FROM = 1800;
export const RANGE_TO = 2050;

const sinD = (d: number) => Math.sin(d * DEG);
const cosD = (d: number) => Math.cos(d * DEG);

export function norm360(d: number): number {
  const x = d % 360;
  return x < 0 ? x + 360 : x;
}

/** Mirrors astro.solveKepler: Newton iteration on Kepler's equation, in degrees. */
export function solveKepler(mDeg: number, e: number): number {
  const m = norm360(mDeg);
  let eAnom = m + RAD * e * sinD(m) * (1 + e * cosD(m));
  for (let i = 0; i < 12; i++) {
    const dE = (eAnom - RAD * e * sinD(eAnom) - m) / (1 - e * cosD(eAnom));
    eAnom -= dE;
    if (Math.abs(dE) < 1e-10) break;
  }
  return eAnom;
}

export interface Elements {
  a: number;
  e: number;
  iDeg: number;
  nodeDeg: number;
  periDeg: number;
  mDeg: number;
}

/** Mirrors solarsystem.Spec.ElementsAt. */
export function elementsAt(s: SolarOrbitSpec, jd: number): Elements {
  const d = jd - s.epoch_jd;
  return {
    a: s.a_au + (s.a_dot ?? 0) * d,
    e: s.e + (s.e_dot ?? 0) * d,
    iDeg: s.i_deg + (s.i_dot ?? 0) * d,
    nodeDeg: s.node_deg + (s.node_dot ?? 0) * d,
    periDeg: s.peri_deg + (s.peri_dot ?? 0) * d,
    mDeg: s.m_deg + s.n_deg * d,
  };
}

/** Mirrors astro.ElementsPosition: Kepler solve, then the orbital plane rotated into the frame. */
export function elementsPosition(el: Elements): Vec3 {
  const ecc = solveKepler(el.mDeg, el.e);
  const u = el.a * (cosD(ecc) - el.e);
  const v = el.a * Math.sqrt(1 - el.e * el.e) * sinD(ecc);

  const cw = cosD(el.periDeg);
  const sw = sinD(el.periDeg);
  const cn = cosD(el.nodeDeg);
  const sn = sinD(el.nodeDeg);
  const ci = cosD(el.iDeg);
  const si = sinD(el.iDeg);

  return [
    (cw * cn - sw * sn * ci) * u + (-sw * cn - cw * sn * ci) * v,
    (cw * sn + sw * cn * ci) * u + (-sw * sn + cw * cn * ci) * v,
    sw * si * u + cw * si * v,
  ];
}

export function unitVector(raDeg: number, decDeg: number): Vec3 {
  const cd = cosD(decDeg);
  return [cd * cosD(raDeg), cd * sinD(raDeg), sinD(decDeg)];
}

export function equatorialToEcliptic(p: Vec3): Vec3 {
  const c = cosD(OBLIQUITY_J2000);
  const s = sinD(OBLIQUITY_J2000);
  return [p[0], p[1] * c + p[2] * s, -p[1] * s + p[2] * c];
}

export function eclipticToEquatorial(p: Vec3): Vec3 {
  const c = cosD(OBLIQUITY_J2000);
  const s = sinD(OBLIQUITY_J2000);
  return [p[0], p[1] * c - p[2] * s, p[1] * s + p[2] * c];
}

/** Mirrors solarsystem.LaplaceBasis. */
export function laplaceBasis(
  poleRA: number,
  poleDec: number,
): { u: Vec3; v: Vec3; w: Vec3 } {
  const w = equatorialToEcliptic(unitVector(poleRA, poleDec));
  let u: Vec3 = [-w[1], w[0], 0];
  const n = Math.hypot(u[0], u[1]);
  if (n > 1e-12) u = [u[0] / n, u[1] / n, 0];
  else u = [1, 0, 0];
  return { u, v: cross(w, u), w };
}

/** Mirrors solarsystem.Spec.PositionAt: AU, J2000 ecliptic, relative to the orbit's centre. */
export function orbitPositionAt(s: SolarOrbitSpec, jd: number): Vec3 {
  const p = elementsPosition(elementsAt(s, jd));
  if (s.frame !== "laplace") return p;
  const { u, v, w } = laplaceBasis(s.pole_ra_deg ?? 0, s.pole_dec_deg ?? 0);
  return [
    p[0] * u[0] + p[1] * v[0] + p[2] * w[0],
    p[0] * u[1] + p[1] * v[1] + p[2] * w[1],
    p[0] * u[2] + p[1] * v[2] + p[2] * w[2],
  ];
}

// --- the Moon ------------------------------------------------------------------------------------

/** The series name the engine uses for the Astronomical Almanac lunar model. */
export const SERIES_MOON_AA = "moon_aa";

const EARTH_RADIUS_KM = 6378.137;

/** Mirrors astro.moonEclipticLongitude. */
function moonLongitude(d: number): number {
  return norm360(
    218.32 +
      13.176396 * d +
      6.29 * sinD(134.9 + 13.064993 * d) -
      1.27 * sinD(259.2 - 13.003 * d) +
      0.66 * sinD(235.7 + 24.381 * d) +
      0.21 * sinD(269.9 + 26.13 * d) -
      0.19 * sinD(357.5 + 0.9856 * d) -
      0.11 * sinD(186.6 + 26.184 * d),
  );
}

/** Mirrors astro.MoonEclipticLatitudeDeg. */
function moonLatitude(d: number): number {
  return (
    5.13 * sinD(93.3 + 13.22935 * d) +
    0.28 * sinD(228.2 + 26.294 * d) -
    0.28 * sinD(318.3 + 0.98 * d) -
    0.17 * sinD(217.6 - 13.087 * d)
  );
}

/** Mirrors astro.MoonParallaxDeg. */
function moonParallax(d: number): number {
  return (
    0.9508 +
    0.0518 * cosD(134.9 + 13.064993 * d) +
    0.0095 * cosD(259.2 - 13.003 * d) +
    0.0078 * cosD(235.7 + 24.381 * d) +
    0.0028 * cosD(269.9 + 26.13 * d)
  );
}

/**
 * Mirrors astro.MoonEclipticJ2000: the Moon's geocentric position in AU, in the J2000 ecliptic.
 *
 * The series is referred to the ecliptic OF DATE, so the vector is carried through the equator of
 * date and precessed back — over 1800–2050 that correction reaches three quarters of a degree, which
 * would otherwise show as the Moon's orbit slowly swinging away from the Earth.
 */
export function moonEclipticJ2000(jd: number): Vec3 {
  const d = jd - J2000;
  const lon = moonLongitude(d);
  const lat = moonLatitude(d);
  const r = EARTH_RADIUS_KM / sinD(moonParallax(d)) / KM_PER_AU;

  // Ecliptic of date → equatorial of date, using the same drifting obliquity the engine uses.
  const obl = 23.439 - 0.0000004 * d;
  const xd = cosD(lat) * cosD(lon);
  const yd = cosD(lat) * sinD(lon) * cosD(obl) - sinD(lat) * sinD(obl);
  const zd = cosD(lat) * sinD(lon) * sinD(obl) + sinD(lat) * cosD(obl);
  const raDate = norm360(Math.atan2(yd, xd) * RAD);
  const decDate = Math.atan2(zd, Math.hypot(xd, yd)) * RAD;

  const msUTC = (jd - 2440587.5) * 86400000;
  const { ra, dec } = precessToJ2000(raDate, decDate, msUTC);
  const cd = cosD(dec);
  return equatorialToEcliptic([
    r * cd * cosD(ra),
    r * cd * sinD(ra),
    r * sinD(dec),
  ]);
}

// --- positions -----------------------------------------------------------------------------------

/** localPositionAt is a body's position relative to whatever it orbits, in AU. */
export function localPositionAt(b: SolarBody, jd: number): Vec3 {
  if (b.series === SERIES_MOON_AA) return moonEclipticJ2000(jd);
  if (b.orbit) return orbitPositionAt(b.orbit, jd);
  return [0, 0, 0];
}

/**
 * heliocentricAt is a body's position relative to the Sun, in AU, following the chain up through its
 * parent. `byKey` is the manifest indexed by key.
 */
export function heliocentricAt(
  byKey: Map<string, SolarBody>,
  b: SolarBody,
  jd: number,
): Vec3 {
  if (b.kind === "star") return [0, 0, 0];
  const local = localPositionAt(b, jd);
  if (!b.parent) return local;
  const parent = byKey.get(b.parent);
  if (!parent) return local;
  const p = heliocentricAt(byKey, parent, jd);
  return [p[0] + local[0], p[1] + local[1], p[2] + local[2]];
}

/**
 * orbitPath samples one full revolution of an orbit for drawing, in AU relative to its centre.
 *
 * It sweeps the MEAN ANOMALY rather than time so the samples are spaced evenly round the ellipse
 * instead of bunching at perihelion, and it evaluates the slowly-drifting elements once at jd — an
 * ellipse drawn from elements that move as the sweep proceeds does not close.
 */
export function orbitPath(
  s: SolarOrbitSpec,
  jd: number,
  segments = 256,
): Float32Array {
  const base = elementsAt(s, jd);
  const out = new Float32Array((segments + 1) * 3);
  const rotate =
    s.frame === "laplace"
      ? laplaceBasis(s.pole_ra_deg ?? 0, s.pole_dec_deg ?? 0)
      : null;
  for (let i = 0; i <= segments; i++) {
    const p = elementsPosition({ ...base, mDeg: (360 * i) / segments });
    const q = rotate
      ? ([
          p[0] * rotate.u[0] + p[1] * rotate.v[0] + p[2] * rotate.w[0],
          p[0] * rotate.u[1] + p[1] * rotate.v[1] + p[2] * rotate.w[1],
          p[0] * rotate.u[2] + p[1] * rotate.v[2] + p[2] * rotate.w[2],
        ] as Vec3)
      : p;
    out[i * 3] = q[0];
    out[i * 3 + 1] = q[1];
    out[i * 3 + 2] = q[2];
  }
  return out;
}

// --- rotation ------------------------------------------------------------------------------------

export interface Orientation {
  poleRA: number;
  poleDec: number;
  wDeg: number;
}

/** Mirrors solarsystem.Pole.OrientationAt. */
export function orientationAt(p: SolarPole, jd: number): Orientation {
  const t = (jd - J2000) / 36525;
  const d = jd - J2000;
  let ra = p.ra0_deg + (p.ra_dot ?? 0) * t;
  let dec = p.dec0_deg + (p.dec_dot ?? 0) * t;
  let w = p.w0_deg + p.w_dot * d;
  if (p.libration) {
    const n = (p.libration.arg0_deg + p.libration.arg_dot * t) * DEG;
    ra += p.libration.ra_amp_deg * Math.sin(n);
    dec += p.libration.dec_amp_deg * Math.cos(n);
    w += p.libration.w_amp_deg * Math.sin(n);
  }
  return { poleRA: ra, poleDec: dec, wDeg: norm360(w) };
}

/**
 * bodyBasis is the rotation that carries body-fixed coordinates into the scene: x through the prime
 * meridian, z through the north pole. Mirrors solarsystem.Orientation.Basis.
 *
 * A retrograde rotator needs no special case — its pole is simply given south of the orbit plane, so
 * the same right-handed construction spins Venus and Uranus backwards with no sign test anywhere.
 */
export function bodyBasis(o: Orientation): { x: Vec3; y: Vec3; z: Vec3 } {
  const pole = unitVector(o.poleRA, o.poleDec);
  const ra = o.poleRA * DEG;
  const q: Vec3 = [-Math.sin(ra), Math.cos(ra), 0];
  const zq = cross(pole, q);
  const cw = Math.cos(o.wDeg * DEG);
  const sw = Math.sin(o.wDeg * DEG);
  const prime: Vec3 = [
    q[0] * cw + zq[0] * sw,
    q[1] * cw + zq[1] * sw,
    q[2] * cw + zq[2] * sw,
  ];
  const x = equatorialToEcliptic(prime);
  const z = equatorialToEcliptic(pole);
  return { x, y: cross(z, x), z };
}

/**
 * bodyMatrix packs bodyBasis into the column-major 3×3 a shader takes, scaled to the body's radius
 * in AU and squashed by its flattening — which is what makes Jupiter and Saturn the visibly
 * elliptical discs they are rather than balls.
 */
export function bodyMatrix(b: SolarBody, jd: number): Float32Array {
  const { x, y, z } = bodyBasis(orientationAt(b.pole, jd));
  const req = b.radius_km * AU_PER_KM;
  const rpol = (b.polar_radius_km || b.radius_km) * AU_PER_KM;
  // prettier-ignore
  return new Float32Array([
    x[0] * req, x[1] * req, x[2] * req,
    y[0] * req, y[1] * req, y[2] * req,
    z[0] * rpol, z[1] * rpol, z[2] * rpol,
  ]);
}

// --- drawing the scene ---------------------------------------------------------------------------

/**
 * radialWarp compresses heliocentric distance so the inner planets and Neptune can share a frame.
 *
 * At warp 0 it is the identity and the map is to scale — Mercury really is a seventy-seventh of
 * Neptune's distance out, and everything inside Jupiter is a knot at the centre. At warp 1 it is
 * fully logarithmic and the orbits are evenly spaced, which is legible and is a lie the legend owns
 * up to. In between it is a blend, so the slider travels continuously between the two.
 *
 * It is applied to the DRAWING only. Distances, shadows, angular sizes and every printed number are
 * computed in true space, which is what keeps an eclipse happening at the right instant even when
 * the picture around it is stretched.
 */
export function radialWarp(rAU: number, warp: number): number {
  if (!(warp > 0) || !(rAU > 0)) return rAU;
  const k = 8; // scene units at one AU once fully warped
  const log = k * Math.log10(1 + rAU / 0.1) * 0.35;
  return rAU * (1 - warp) + log * warp;
}

/** warpPosition applies radialWarp to a heliocentric vector, keeping its direction. */
export function warpPosition(p: Vec3, warp: number): Vec3 {
  const r = Math.hypot(p[0], p[1], p[2]);
  if (!(r > 0) || !(warp > 0)) return p;
  const k = radialWarp(r, warp) / r;
  return [p[0] * k, p[1] * k, p[2] * k];
}

/**
 * scenePosition is where a body is drawn: its planet's warped heliocentric position, plus its own
 * offset from that planet scaled separately.
 *
 * Warping a moon about the SUN would squash every satellite system into its planet, because the warp
 * barely changes over a few hundred thousand kilometres. So the two are separated: the planet moves
 * in the warped radial field, and the moons keep their own local geometry around it, magnified by
 * moonScale so a system is big enough to see from outside it.
 */
export function scenePosition(
  helio: Vec3,
  local: Vec3,
  warp: number,
  moonScale: number,
): Vec3 {
  const parent: Vec3 = [
    helio[0] - local[0],
    helio[1] - local[1],
    helio[2] - local[2],
  ];
  const w = warpPosition(parent, warp);
  return [
    w[0] + local[0] * moonScale,
    w[1] + local[1] * moonScale,
    w[2] + local[2] * moonScale,
  ];
}

/** MIN_BODY_PX is how small a world may be drawn before it is held at a clickable minimum. */
export const MIN_BODY_PX = 2.5;

/**
 * drawRadius is the radius a body is drawn at, in scene units.
 *
 * From outside Neptune's orbit, Earth's true disc is a hundred-thousandth of a pixel — a solar system
 * drawn honestly is an empty screen with a dot in the middle. So a body is never drawn smaller than
 * MIN_BODY_PX: far away it is a coloured point you can see and click, and as the camera closes in
 * its true size overtakes the floor and the globe takes over, continuously, with no threshold anyone
 * can notice. `exaggerate` multiplies the true size on top, for the diagram look.
 */
export function drawRadius(
  trueRadiusAU: number,
  distanceFromEye: number,
  tanHalfH: number,
  viewportHeightPx: number,
  exaggerate = 1,
): number {
  const r = trueRadiusAU * exaggerate;
  if (!(distanceFromEye > 0) || !(viewportHeightPx > 0) || !(tanHalfH > 0)) {
    return r;
  }
  // Scene units per pixel at the body's distance.
  const perPx = (2 * tanHalfH * distanceFromEye) / viewportHeightPx;
  return Math.max(r, MIN_BODY_PX * perPx);
}

/** yearOf is the UTC year of a timestamp, for range checks against the model's validity. */
export function yearOf(ms: number): number {
  return new Date(ms).getUTCFullYear();
}

/** inModelRange reports whether an instant is inside the span the engine's elements are fitted for. */
export function inModelRange(ms: number): boolean {
  const y = yearOf(ms);
  return y >= RANGE_FROM && y <= RANGE_TO;
}

/** jdFromMs converts a Unix timestamp in milliseconds to a Julian Date. */
export function jdFromMs(ms: number): number {
  return julianDate(ms);
}

/** msFromJd is the inverse of jdFromMs. */
export function msFromJd(jd: number): number {
  return (jd - 2440587.5) * 86400000;
}

/** normalized is re-exported so the renderer does not import two vector modules. */
export { normalize as normalizeVec };
