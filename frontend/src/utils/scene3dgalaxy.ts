// Putting the Galaxy into the scene: where the camera stands, and the scaffolding drawn around it.
//
// The Galaxy's own geometry is not here. It arrives as a point cloud sampled in Go from published
// structure (`internal/scene3d/galaxymodel.go`, decoded by `galaxycloud.ts`) and is rotated into the
// scene by a 3×3 uniform on the GPU. What is here is the camera's JOURNEY — from standing inside the
// photograph to looking back at the whole disc, and on out past it — plus the reference rings that
// give the journey a scale you can read.
//
// The journey moves the camera AND opens the lens, and it has to do both. The run's own field is about
// a degree across; framing a forty-kiloparsec disc through a one-degree lens would put the camera two
// megaparsecs away. Pulling back while widening the lens gets there instead.

import {
  MIN_ORBIT_DISTANCE,
  PITCH_LIMIT,
  UNITS_PER_PC,
  maxOrbitDistance,
  type Orbit,
} from "@/utils/scene3d";
import {
  DISC_EDGE_KPC,
  R_SUN_KPC,
  galactocentricToHeliocentric,
} from "@/utils/galaxy";
import { galacticToEquatorial } from "@/utils/galactic";
import { project, type SceneBasis, type Vec3 } from "@/utils/skyframe";

/**
 * GALAXY_FRAME_HALF_KPC is how much sky the galactic end of the journey frames, half-height.
 *
 * A little more than the disc's own 15 kpc radius, so a disc that happens to present its major axis
 * vertically still fits, and one seen at the vantage's 55° inclination — foreshortened to about 17 kpc
 * across — fills two thirds of the frame rather than floating in the middle of it.
 */
export const GALAXY_FRAME_HALF_KPC = 17;

/** GALAXY_VANTAGE_KPC is where the camera stands to see the disc: outside it, above the plane. */
export const GALAXY_VANTAGE_KPC = 35;

/** GALAXY_VANTAGE_ELEVATION_DEG keeps the disc from being seen edge-on, where structure degenerates. */
export const GALAXY_VANTAGE_ELEVATION_DEG = 55;

/**
 * How the journey continues past the Galaxy when the run caught something further out.
 *
 * OUTER_STANDOFF is how far the eye stands from the midpoint of the Sun and that object, in units of
 * their separation, and OUTER_FRAME_HALF is the half-frame it needs to hold both — a little over half
 * the separation, so neither end sits on the edge of the picture. Together they set a vertical field
 * of about fifty degrees, which is wide but not a fisheye.
 */
export const OUTER_STANDOFF = 1.4;
export const OUTER_FRAME_HALF = 0.65;

/**
 * OUTER_PIVOT_MARGIN ties the pivot to the FRAMING rather than to the slider, and it is what keeps the
 * Milky Way on screen the whole way out.
 *
 * The bug it fixes: with the pivot easing linearly toward the midpoint while the camera pulls back
 * logarithmically, halfway along the M51 journey the pivot had already jumped 360 kpc down the line of
 * sight with the camera only 62 kpc behind it and 45 kpc of frame — so the Galaxy was four hundred
 * kiloparsecs off screen, M51 was still megaparsecs away, and the middle of the slider showed empty
 * space. Keeping the pivot inside the frame by this margin makes the Galaxy visible at every point, and
 * it still lands exactly on the midpoint at the end, since the framing there is 0.65 of the separation
 * and 0.65/1.3 is a half.
 */
export const OUTER_PIVOT_MARGIN = 1.3;

/**
 * galacticToScene builds the 3×3 that takes heliocentric galactic kiloparsecs into scene units.
 *
 * Its columns are the three galactic axes expressed in the scene's frame, so it is orthonormal by
 * construction and its determinant carries the field's parity automatically — a mirrored run draws a
 * chirally flipped Galaxy, which is right, because the photograph is itself a mirror of the sky.
 */
export function galacticToScene(
  basis: SceneBasis,
  unitsPerPc = UNITS_PER_PC,
): [Vec3, Vec3, Vec3] {
  const axis = (l: number, b: number): Vec3 => {
    const { ra, dec } = galacticToEquatorial(l, b);
    const d = project(basis, ra, dec);
    // kpc in, scene units out.
    const k = 1000 * unitsPerPc;
    return [d[0] * k, d[1] * k, d[2] * k];
  };
  // Toward the galactic centre, the direction of rotation, and the north galactic pole.
  return [axis(0, 0), axis(90, 0), axis(0, 90)];
}

/** applyMatrix maps a heliocentric galactic point (kpc) into scene coordinates. */
export function applyMatrix(
  m: readonly [Vec3, Vec3, Vec3],
  x: number,
  y: number,
  z: number,
): Vec3 {
  return [
    m[0][0] * x + m[1][0] * y + m[2][0] * z,
    m[0][1] * x + m[1][1] * y + m[2][1] * z,
    m[0][2] * x + m[1][2] * y + m[2][2] * z,
  ];
}

// --- the journey ---------------------------------------------------------------------------------

export interface GalaxyView {
  orbit: Orbit;
  /** tanScale multiplies the run's own lens; 1 is the photograph's own field of view. */
  tanScale: number;
}

export interface JourneyContext {
  /** medianPc is the field's own scale — where the camera pivots at the near end. */
  medianPc: number;
  /** tanHalfH is the run's lens, needed to work out how far the lens has to open. */
  tanHalfH: number;
  /** toGalacticCentre is the unit direction of the galactic centre in scene coordinates. */
  toGalacticCentre: Vec3;
  /** toNorthPole is the unit direction of the north galactic pole in scene coordinates. */
  toNorthPole: Vec3;
  /**
   * farthestPc is how far away the most distant thing the scene draws is, and toFarthest is its unit
   * direction in scene coordinates. Absent, or nearer than the disc's own edge, and the journey ends
   * at the Galaxy exactly as it always did.
   */
  farthestPc?: number;
  toFarthest?: Vec3;
}

/** journeyEndKpc is where the journey finishes: the galactic vantage, or beyond it. */
export function journeyEndKpc(ctx: JourneyContext): number {
  const far = outerSeparationKpc(ctx);
  return far > 0 ? OUTER_STANDOFF * far : GALAXY_VANTAGE_KPC;
}

/**
 * outerSeparationKpc is the distance of the object the journey has to reach past the Galaxy for, or 0
 * when there is none.
 *
 * The threshold is the disc's own edge: an object inside the Galaxy is already framed by the galactic
 * vantage, and extending the journey for it would spend half the slider going nowhere.
 */
export function outerSeparationKpc(ctx: JourneyContext): number {
  const far = (ctx.farthestPc ?? 0) / 1000;
  if (!Number.isFinite(far) || far <= DISC_EDGE_KPC || !ctx.toFarthest)
    return 0;
  return far;
}

/**
 * journeySplit is the fraction of the slider spent reaching the Galaxy — 1 when that is the whole
 * journey.
 *
 * Log-proportional, because the legs can be three decades apart: an M51 field runs from six hundred
 * parsecs to ten megaparsecs, and splitting the slider evenly would spend half of it crawling across
 * the last decade while the Galaxy flashed past in a few pixels of travel.
 */
export function journeySplit(ctx: JourneyContext): number {
  const end = journeyEndKpc(ctx);
  if (!(end > GALAXY_VANTAGE_KPC)) return 1;
  const d0 = nearDistanceKpc(ctx);
  return Math.log(GALAXY_VANTAGE_KPC / d0) / Math.log(end / d0);
}

/** nearDistanceKpc is where the eye pivots at the start: the field's own scale. */
function nearDistanceKpc(ctx: JourneyContext): number {
  return clamp(ctx.medianPc, 50, 5000) * 1e-3;
}

/** galaxyMaxOrbitDistance is how far the camera may be dollied out by hand in the galaxy view. */
export function galaxyMaxOrbitDistance(ctx: JourneyContext): number {
  return maxOrbitDistance(journeyEndKpc(ctx));
}

/**
 * galaxyView is the whole journey.
 *
 * At t = 0 the eye is at Earth looking down the barrel with the run's own lens — byte for byte the
 * photograph. It then pulls back and swings above the plane until the disc is framed, and if the run
 * caught something outside the Galaxy it keeps going until that object and the Milky Way are in one
 * frame together, at true relative scale.
 *
 * Everything moves logarithmically or on an ease, because the ends are decades apart and a linear ramp
 * would spend nine tenths of the slider at one of them.
 */
export function galaxyView(t: number, ctx: JourneyContext): GalaxyView {
  const split = journeySplit(ctx);
  const u = clamp01(t);
  if (u <= split || split >= 1)
    return galacticLeg(split > 0 ? u / split : 1, ctx);
  return outerLeg((u - split) / (1 - split), ctx);
}

/** galaxyTanScale is how far the lens has opened at t. */
export function galaxyTanScale(t: number, ctx: JourneyContext): number {
  return galaxyView(t, ctx).tanScale;
}

/** tanScaleFor is the lens opening that frames halfKpc of sky from distanceKpc away. */
function tanScaleFor(
  halfKpc: number,
  distanceKpc: number,
  tanHalfH: number,
): number {
  const th = tanHalfH > 0 ? tanHalfH : 0.01;
  return Math.max(1, halfKpc / (distanceKpc * th));
}

/**
 * galacticLeg is the first half of the journey: Earth to the galactic vantage.
 *
 * The vantage is built in the GALACTIC frame and only then expressed as scene angles. That is the bug
 * that once made the Galaxy unrecognisable: `pitch` is an angle in SCENE coordinates, and the scene's
 * axes are the PHOTOGRAPH's — so ramping pitch to 55° put the camera 55° above the image's own y axis,
 * which has nothing to do with the galactic plane. The elevation that actually resulted was whatever
 * fell out of where the telescope happened to be pointing: measured at −16.3° for the Orion field and
 * −14.6° for the M51 one. Both were views from BELOW the disc, sixteen degrees off edge-on — and a
 * spiral galaxy seen sixteen degrees from edge-on is a set of nested arcs with no visible disc and no
 * visible bar, which is exactly what was on screen.
 */
function galacticLeg(v: number, ctx: JourneyContext): GalaxyView {
  const u = clamp01(v);
  const d0 = nearDistanceKpc(ctx);
  const distance = clamp(
    d0 * Math.pow(GALAXY_VANTAGE_KPC / d0, u),
    MIN_ORBIT_DISTANCE,
    galaxyMaxOrbitDistance(ctx),
  );
  const tanScale = Math.pow(
    tanScaleFor(GALAXY_FRAME_HALF_KPC, GALAXY_VANTAGE_KPC, ctx.tanHalfH),
    u,
  );

  // The pivot slides from just in front of the observer to the galactic centre. Eased so the camera
  // pulls back before it starts swinging round, which reads as one movement rather than two.
  const gc = ctx.toGalacticCentre;
  const k = u * u * R_SUN_KPC;
  const target: Vec3 = [gc[0] * k, gc[1] * k, (1 - u * u) * d0 + gc[2] * k];

  // At t = 0 the eye must be exactly at the origin, which means looking straight down the barrel.
  // Blending from that to the galactic vantage keeps the anchor exact and the movement continuous.
  const vantage = galacticVantageDir(ctx, u);
  const dir = normalise([
    u * vantage[0],
    u * vantage[1],
    (1 - u) * -1 + u * vantage[2],
  ]);
  return { orbit: orbitLookingAlong(target, distance, dir), tanScale };
}

/** galacticVantageDir is away from the centre along the Sun's side, lifted toward the north pole. */
function galacticVantageDir(ctx: JourneyContext, u: number): Vec3 {
  const eps = ((GALAXY_VANTAGE_ELEVATION_DEG * Math.PI) / 180) * clamp01(u);
  const gc = ctx.toGalacticCentre;
  const np = ctx.toNorthPole;
  return [
    -Math.cos(eps) * gc[0] + Math.sin(eps) * np[0],
    -Math.cos(eps) * gc[1] + Math.sin(eps) * np[1],
    -Math.cos(eps) * gc[2] + Math.sin(eps) * np[2],
  ];
}

/**
 * outerLeg continues out to where the Milky Way and the run's own galaxy are both in frame.
 *
 * This is the part of the view that answers "how far away is it, really". The Galaxy is fifteen
 * kiloparsecs across and M51 is seven MEGAparsecs off; there is no lens that shows both from inside
 * the disc, so the eye has to leave. The pivot moves to the midpoint of the two and the camera stands
 * off to one side of the line joining them, which is the only vantage from which their separation is
 * what it looks like.
 */
function outerLeg(w: number, ctx: JourneyContext): GalaxyView {
  const u = clamp01(w);
  const sep = outerSeparationKpc(ctx);
  const end = journeyEndKpc(ctx);
  const distance = clamp(
    GALAXY_VANTAGE_KPC * Math.pow(end / GALAXY_VANTAGE_KPC, u),
    MIN_ORBIT_DISTANCE,
    galaxyMaxOrbitDistance(ctx),
  );
  const halfKpc =
    GALAXY_FRAME_HALF_KPC *
    Math.pow((OUTER_FRAME_HALF * sep) / GALAXY_FRAME_HALF_KPC, u);
  const tanScale = tanScaleFor(halfKpc, distance, ctx.tanHalfH);

  // From the galactic centre out along the line of sight, never further than the frame can hold — so
  // the Galaxy stays in shot the whole way and the pivot still arrives at the midpoint of the pair.
  const gc = ctx.toGalacticCentre;
  const f = ctx.toFarthest ?? [0, 0, 1];
  const pivot = Math.min(0.5 * sep, halfKpc / OUTER_PIVOT_MARGIN);
  const target: Vec3 = [
    (1 - u) * gc[0] * R_SUN_KPC + u * f[0] * pivot,
    (1 - u) * gc[1] * R_SUN_KPC + u * f[1] * pivot,
    (1 - u) * gc[2] * R_SUN_KPC + u * f[2] * pivot,
  ];

  const from = galacticVantageDir(ctx, 1);
  const to = sideOnDir(f, ctx.toNorthPole, gc);
  const dir = normalise([
    (1 - u) * from[0] + u * to[0],
    (1 - u) * from[1] + u * to[1],
    (1 - u) * from[2] + u * to[2],
  ]);
  return { orbit: orbitLookingAlong(target, distance, dir), tanScale };
}

/**
 * sideOnDir is a direction perpendicular to `along`, as close to `up` as it can be.
 *
 * The degeneracy is real and has to be handled rather than hoped away: a target sitting at the north
 * galactic pole leaves no component of the pole perpendicular to it, and normalising the residue of two
 * near-parallel unit vectors returns a direction made of rounding error. The fallback axis is the
 * galactic centre, which cannot also be parallel to the pole.
 */
function sideOnDir(along: Vec3, up: Vec3, fallback: Vec3): Vec3 {
  const perp = (v: Vec3): Vec3 => {
    const d = v[0] * along[0] + v[1] * along[1] + v[2] * along[2];
    return [v[0] - d * along[0], v[1] - d * along[1], v[2] - d * along[2]];
  };
  const first = perp(up);
  if (Math.hypot(first[0], first[1], first[2]) > 0.15) return normalise(first);
  return normalise(perp(fallback));
}

/** orbitLookingAlong turns a pivot, a standoff and an eye direction into the orbit that produces them. */
function orbitLookingAlong(target: Vec3, distance: number, dir: Vec3): Orbit {
  // Invert eyePosition in closed form: eye = target + distance · dir.
  const pitch = clamp(Math.asin(-dir[1]), -PITCH_LIMIT, PITCH_LIMIT);
  const yaw = Math.atan2(dir[0], -dir[2]);
  return {
    target: [target[0], target[1], target[2]],
    distance,
    yaw,
    pitch,
    roll: 0,
  };
}

function normalise(v: Vec3): Vec3 {
  const n = Math.hypot(v[0], v[1], v[2]);
  if (!(n > 0)) return [0, 0, -1];
  return [v[0] / n, v[1] / n, v[2] / n];
}

/** frameSpanPc is how much sky the view covers at t, for an honest readout on the slider. */
export function frameSpanPc(
  t: number,
  ctx: JourneyContext,
  tanHalfW: number,
): number {
  const view = galaxyView(t, ctx);
  return 2 * tanHalfW * view.tanScale * view.orbit.distance * 1000;
}

/**
 * cameraPhysicalLinear is where the eye really is, in parsecs.
 *
 * The star shader's inverse-square law is written in real parsecs, so feeding it this keeps the
 * existing physics correct at every scale for free: at t = 0 the eye is at Earth and the stars carry
 * their Earth magnitudes — the photograph — and by 35 kpc every field star has dimmed by some twenty
 * magnitudes and collapses to the shader's one-pixel floor, which is exactly why the field reads as a
 * thin bright ray from the Sun rather than a blob that refuses to shrink.
 */
export function cameraPhysicalLinear(
  eye: readonly [number, number, number],
  pcPerUnit: number,
): [number, number, number] {
  return [eye[0] * pcPerUnit, eye[1] * pcPerUnit, eye[2] * pcPerUnit];
}

// --- the scaffolding -----------------------------------------------------------------------------

/** GalaxyLines is the reference frame: rings at known radii and the Sun–centre line. */
export interface GalaxyLines {
  positions: Float32Array;
  colors: Float32Array;
}

/** Galactocentric rings, in kiloparsec — the Galaxy's own scale bar. */
const RING_RADII_KPC = [5, 10, 15];

/**
 * buildGalaxyLines draws the rings the journey is read against.
 *
 * Three galactocentric circles and the Sun–centre line always; and when the scene reaches past the
 * disc, heliocentric circles at every decade out to the farthest object, so the gap between the Milky
 * Way and a galaxy seven megaparsecs away has something to be measured with. Without them the far end
 * of the journey is two bright patches on black with no way to tell whether they are a kiloparsec or a
 * megaparsec apart.
 */
export function buildGalaxyLines(
  m: readonly [Vec3, Vec3, Vec3],
  farthestPc = 0,
): GalaxyLines {
  const pos: number[] = [];
  const col: number[] = [];
  const add = (p: Vec3, rgb: Vec3) => {
    pos.push(p[0], p[1], p[2]);
    col.push(rgb[0], rgb[1], rgb[2]);
  };
  const galactic: Vec3 = [0.22, 0.3, 0.42];
  const ladder: Vec3 = [0.34, 0.26, 0.16];

  const ring = (
    radiusKpc: number,
    centre: readonly [number, number, number],
    rgb: Vec3,
    segments: number,
  ) => {
    for (let si = 0; si < segments; si++) {
      const a = (si / segments) * 2 * Math.PI;
      const b = ((si + 1) / segments) * 2 * Math.PI;
      // In the galactic plane, around the given centre: x and y are the two in-plane galactic axes.
      const at = (th: number): Vec3 =>
        applyMatrix(
          m,
          centre[0] + radiusKpc * Math.cos(th),
          centre[1] + radiusKpc * Math.sin(th),
          centre[2],
        );
      add(at(a), rgb);
      add(at(b), rgb);
    }
  };

  const centre = galactocentricToHeliocentric(0, 0, 0);
  for (const r of RING_RADII_KPC) ring(r, centre, galactic, 180);

  // The Sun to the galactic centre — the line every other number here is measured from.
  const sun = galactocentricToHeliocentric(R_SUN_KPC, 0, 0);
  add(applyMatrix(m, sun[0], sun[1], sun[2]), galactic);
  add(applyMatrix(m, centre[0], centre[1], centre[2]), galactic);

  for (const kpc of decadeLadderKpc(farthestPc / 1000)) {
    ring(kpc, [0, 0, 0], ladder, 240);
  }
  return { positions: new Float32Array(pos), colors: new Float32Array(col) };
}

/**
 * decadeLadderKpc is the decade radii to ring, centred on the SUN, out past the farthest object.
 *
 * Starts above the disc so it never doubles up on the galactocentric rings, and stops one decade past
 * the object so the object itself is inside the outermost ring rather than on it.
 */
export function decadeLadderKpc(farthestKpc: number): number[] {
  if (!Number.isFinite(farthestKpc) || farthestKpc <= DISC_EDGE_KPC) return [];
  const out: number[] = [];
  for (let kpc = 100; kpc <= farthestKpc * 10 && out.length < 8; kpc *= 10) {
    out.push(kpc);
  }
  return out;
}

function clamp01(t: number): number {
  return Number.isFinite(t) ? Math.max(0, Math.min(1, t)) : 0;
}

function clamp(v: number, lo: number, hi: number): number {
  return Math.max(lo, Math.min(hi, Number.isFinite(v) ? v : lo));
}
