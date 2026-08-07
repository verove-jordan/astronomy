// Putting the Galaxy into the scene: where the camera stands, and what geometry to draw.
//
// Two jobs, both pure. `galaxyOrbit` is the journey the slider drives — from standing inside the
// photograph to looking at the whole disc — and `buildGalaxyMesh` turns the structural model into
// triangles already transformed into scene coordinates, once, at load.
//
// The journey moves the camera AND opens the lens, and it has to do both. The run's own field is
// about a degree across; framing a forty-kiloparsec disc through a one-degree lens would put the
// camera two megaparsecs away — past MAX_ORBIT_DISTANCE, and it reads as a bug rather than as a
// journey. Pulling back to 35 kpc while widening the lens gets there with every existing constant
// intact, and because both ramps push the same way it simply reads as zooming out.

import {
  MAX_ORBIT_DISTANCE,
  MIN_ORBIT_DISTANCE,
  PITCH_LIMIT,
  UNITS_PER_PC,
  type Orbit,
} from "@/utils/scene3d";
import {
  ARMS,
  BAR_ANGLE_DEG,
  BULGE_SEMI_KPC,
  DISC_EDGE_KPC,
  R_SUN_KPC,
  armLocus,
  discBrightness,
  galactocentricToHeliocentric,
} from "@/utils/galaxy";
import { galacticToEquatorial } from "@/utils/galactic";
import { project, type SceneBasis, type Vec3 } from "@/utils/skyframe";

/** GALAXY_FRAME_HALF_KPC is how much sky the far end of the journey frames, half-height. */
export const GALAXY_FRAME_HALF_KPC = 20;

/** GALAXY_VANTAGE_KPC is where the camera ends up: outside the disc, above the plane. */
export const GALAXY_VANTAGE_KPC = 35;

/** GALAXY_VANTAGE_ELEVATION_DEG keeps the disc from being seen edge-on, where slices degenerate. */
export const GALAXY_VANTAGE_ELEVATION_DEG = 55;

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
}

/** galaxyTanScale is how far the lens has opened at t. */
export function galaxyTanScale(t: number, tanHalfH: number): number {
  const th = tanHalfH > 0 ? tanHalfH : 0.01;
  const max = GALAXY_FRAME_HALF_KPC / (GALAXY_VANTAGE_KPC * th);
  return Math.pow(Math.max(1, max), clamp01(t));
}

/** galaxyDistance is how far the eye stands from its pivot at t, in scene units (kpc). */
export function galaxyDistance(t: number, medianPc: number): number {
  const d0 = clamp(medianPc, 50, 5000) * 1e-3;
  const d1 = GALAXY_VANTAGE_KPC;
  return clamp(
    d0 * Math.pow(d1 / d0, clamp01(t)),
    MIN_ORBIT_DISTANCE,
    MAX_ORBIT_DISTANCE,
  );
}

/**
 * galaxyOrbit is the whole journey: at t = 0 the eye is at Earth looking down the barrel with the
 * run's own lens — byte for byte the photograph — and at t = 1 it is 35 kpc away, above the plane,
 * with the disc framed.
 *
 * Everything moves logarithmically or on an ease, because the two ends are three decades apart and a
 * linear ramp would spend nine tenths of the slider at one of them.
 */
export function galaxyOrbit(t: number, ctx: JourneyContext): GalaxyView {
  const u = clamp01(t);
  const distance = galaxyDistance(u, ctx.medianPc);
  const tanScale = galaxyTanScale(u, ctx.tanHalfH);

  // The pivot slides from just in front of the observer to the galactic centre. Eased so the camera
  // pulls back before it starts swinging round, which reads as one movement rather than two.
  const gc = ctx.toGalacticCentre;
  const k = u * u * R_SUN_KPC;
  const target: [number, number, number] = [
    gc[0] * k,
    gc[1] * k,
    (1 - u * u) * clamp(ctx.medianPc, 50, 5000) * 1e-3 + gc[2] * k,
  ];

  // Where the eye stands, built in the GALACTIC frame and only then expressed as scene angles.
  //
  // This is the bug that made the Galaxy unrecognisable. `pitch` is an angle in SCENE coordinates,
  // and the scene's axes are the PHOTOGRAPH's — so ramping pitch to 55° put the camera 55° above the
  // image's own y axis, which has nothing to do with the galactic plane. The elevation that actually
  // resulted was whatever fell out of where the telescope happened to be pointing: measured at
  // −16.3° for the Orion field and −14.6° for the M51 one. Both were views from BELOW the disc,
  // sixteen degrees off edge-on — and a spiral galaxy seen sixteen degrees from edge-on is a set of
  // nested arcs with no visible disc and no visible bar, which is exactly what was on screen.
  //
  // ctx.toNorthPole was computed for this and never used. Now the vantage direction is a combination
  // of the galactic-centre and north-pole axes, so the elevation means what it says at any pointing.
  const eps = ((GALAXY_VANTAGE_ELEVATION_DEG * Math.PI) / 180) * u;
  const np = ctx.toNorthPole;
  // Away from the centre along the Sun's side, lifted toward the north galactic pole.
  const vantage: Vec3 = [
    -Math.cos(eps) * gc[0] + Math.sin(eps) * np[0],
    -Math.cos(eps) * gc[1] + Math.sin(eps) * np[1],
    -Math.cos(eps) * gc[2] + Math.sin(eps) * np[2],
  ];
  // At t = 0 the eye must be exactly at the origin, which means looking straight down the barrel.
  // Blending from that to the galactic vantage keeps the anchor exact and the movement continuous.
  const dir = normalise([
    (1 - u) * 0 + u * vantage[0],
    (1 - u) * 0 + u * vantage[1],
    (1 - u) * -1 + u * vantage[2],
  ]);
  // Invert eyePosition in closed form: eye = target + distance · dir.
  const pitch = clamp(Math.asin(-dir[1]), -PITCH_LIMIT, PITCH_LIMIT);
  const yaw = Math.atan2(dir[0], -dir[2]);
  return {
    orbit: { target, distance, yaw, pitch, roll: 0 },
    tanScale,
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
  const d = galaxyDistance(t, ctx.medianPc);
  return 2 * tanHalfW * galaxyTanScale(t, ctx.tanHalfH) * d * 1000;
}

/**
 * cameraPhysicalLinear is where the eye really is, in parsecs.
 *
 * The star shader's inverse-square law is written in real parsecs, so feeding it this keeps the
 * existing physics correct at every scale for free: at t = 0 the eye is at Earth and the stars carry
 * their Earth magnitudes — the photograph — and by 35 kpc every field star has dimmed by some twenty
 * magnitudes and collapses to the shader's one-pixel floor, which is exactly why the field reads as
 * a thin bright ray from the Sun rather than a blob that refuses to shrink.
 */
export function cameraPhysicalLinear(
  eye: readonly [number, number, number],
  pcPerUnit: number,
): [number, number, number] {
  return [eye[0] * pcPerUnit, eye[1] * pcPerUnit, eye[2] * pcPerUnit];
}

// --- geometry ------------------------------------------------------------------------------------

export interface GalaxyMesh {
  /** positions are already in scene units. */
  positions: Float32Array;
  /** colors are premultiplied by alpha, which is exactly what additive blending wants. */
  colors: Float32Array;
  indices: Uint16Array;
}

/** GalaxyLines is the reference frame: galactocentric circles and the Sun–centre line. */
export interface GalaxyLines {
  positions: Float32Array;
  colors: Float32Array;
}

const DISC_RGB: Vec3 = [0.55, 0.52, 0.45];
const ARM_RGB: Vec3 = [0.45, 0.58, 0.85];
const BULGE_RGB: Vec3 = [0.85, 0.65, 0.35];

// Vertical slices through the disc, in kpc. Non-uniform so the bright midplane is sampled finely.
// Plane-parallel, which means an exactly edge-on view degrades into a few bright lines — the same
// trade the nebula volumeMesh already documents and accepts.
const SLICES_KPC = [0, 0.15, -0.15, 0.35, -0.35, 0.7, -0.7];

const RINGS = 26;
const SEGMENTS = 72;

/**
 * buildGalaxyMesh tessellates disc, bulge and arms into one triangle soup in scene coordinates.
 *
 * Built once per scene, never per frame. Everything is baked here — the galactic transform, the unit
 * scale and the per-vertex brightness — so drawing it costs one buffer bind and one call.
 */
export function buildGalaxyMesh(
  m: readonly [Vec3, Vec3, Vec3],
  opacity = 1,
): GalaxyMesh {
  const pos: number[] = [];
  const col: number[] = [];
  const idx: number[] = [];

  const push = (p: Vec3, rgb: Vec3, a: number) => {
    pos.push(p[0], p[1], p[2]);
    // Premultiplied: additive blending adds exactly this, so alpha needs no separate channel.
    col.push(rgb[0] * a, rgb[1] * a, rgb[2] * a);
    return pos.length / 3 - 1;
  };

  const slabs = SLICES_KPC.length;

  // --- disc: concentric rings, replicated through the slices ---
  for (const zk of SLICES_KPC) {
    const base = pos.length / 3;
    for (let ri = 0; ri <= RINGS; ri++) {
      const r = (ri / RINGS) * DISC_EDGE_KPC;
      for (let si = 0; si < SEGMENTS; si++) {
        const beta = (si / SEGMENTS) * 360;
        const [hx, hy, hz] = galactocentricToHeliocentric(r, beta, zk);
        // Normalised by the slice count so the summed column is independent of how finely it is cut.
        const a = (discBrightness(r, zk) * opacity * 0.28) / slabs;
        push(applyMatrix(m, hx, hy, hz), DISC_RGB, a);
      }
    }
    for (let ri = 0; ri < RINGS; ri++) {
      for (let si = 0; si < SEGMENTS; si++) {
        const s2 = (si + 1) % SEGMENTS;
        const a = base + ri * SEGMENTS + si;
        const b = base + ri * SEGMENTS + s2;
        const c = base + (ri + 1) * SEGMENTS + si;
        const d = base + (ri + 1) * SEGMENTS + s2;
        idx.push(a, c, b, b, c, d);
      }
    }
  }

  // --- bulge: a filled ellipse per slice, fading outward ---
  for (const zk of SLICES_KPC) {
    const scale = Math.max(0, 1 - Math.abs(zk) / BULGE_SEMI_KPC[2]);
    if (scale <= 0) continue;
    const [cx, cy, cz] = galactocentricToHeliocentric(0, 0, zk);
    const centre = push(
      applyMatrix(m, cx, cy, cz),
      BULGE_RGB,
      (0.9 * opacity) / slabs,
    );
    const base = pos.length / 3;
    for (let si = 0; si < SEGMENTS; si++) {
      const th = (si / SEGMENTS) * 2 * Math.PI;
      // A real ellipse in the galactic plane, rotated to the bar angle. The previous expression
      // mixed a radius with a hypotenuse, which is what drew the wedges that made the bulge look
      // broken rather than elliptical.
      const ex = BULGE_SEMI_KPC[0] * scale * Math.cos(th);
      const ey = BULGE_SEMI_KPC[1] * scale * Math.sin(th);
      const bar = BAR_ANGLE_DEG * (Math.PI / 180);
      const rx = ex * Math.cos(bar) - ey * Math.sin(bar);
      const ry = ex * Math.sin(bar) + ey * Math.cos(bar);
      const [hx, hy, hz] = galactocentricToHeliocentric(
        Math.hypot(rx, ry),
        (Math.atan2(ry, rx) * 180) / Math.PI,
        zk,
      );
      push(applyMatrix(m, hx, hy, hz), BULGE_RGB, 0);
    }
    for (let si = 0; si < SEGMENTS; si++) {
      idx.push(centre, base + si, base + ((si + 1) % SEGMENTS));
    }
  }

  // --- arms: ribbons along each locus, faded across their width ---
  for (const arm of ARMS) {
    const base = pos.length / 3;
    // Only where the arm was measured, plus a little. Sweeping every arm through a full turn is
    // what turned the low-pitch ones into rings.
    const samples: number[] = [];
    const margin = 25;
    for (
      let beta = arm.betaMinDeg - margin;
      beta <= arm.betaMaxDeg + margin;
      beta += 2
    ) {
      samples.push(beta);
    }
    let n = 0;
    for (const beta of samples) {
      const r = armLocus(arm, beta);
      if (!(r >= 2.5 && r <= DISC_EDGE_KPC)) continue;
      const w = arm.widthKpc * 1.5;
      for (const [dr, a] of [
        [-w, 0],
        [0, 0.32 * opacity],
        [w, 0],
      ] as const) {
        const [hx, hy, hz] = galactocentricToHeliocentric(r + dr, beta, 0);
        push(applyMatrix(m, hx, hy, hz), ARM_RGB, a * discEdge(r));
      }
      n++;
    }
    for (let i = 0; i + 1 < n; i++) {
      const a = base + i * 3;
      const b = base + (i + 1) * 3;
      for (let k2 = 0; k2 < 2; k2++) {
        idx.push(a + k2, b + k2, a + k2 + 1, a + k2 + 1, b + k2, b + k2 + 1);
      }
    }
  }

  return {
    positions: new Float32Array(pos),
    colors: new Float32Array(col),
    indices: new Uint16Array(idx),
  };
}

function discEdge(r: number): number {
  const start = DISC_EDGE_KPC - 2;
  if (r <= start) return 1;
  if (r >= DISC_EDGE_KPC) return 0;
  return 0.5 * (1 + Math.cos((Math.PI * (r - start)) / 2));
}

/** buildGalaxyLines is the readable scaffolding: rings at 5/10/15 kpc and the Sun–centre line. */
export function buildGalaxyLines(m: readonly [Vec3, Vec3, Vec3]): GalaxyLines {
  const pos: number[] = [];
  const col: number[] = [];
  const rgb: Vec3 = [0.22, 0.3, 0.42];
  const add = (p: Vec3) => {
    pos.push(p[0], p[1], p[2]);
    col.push(rgb[0], rgb[1], rgb[2]);
  };
  for (const r of [5, 10, 15]) {
    for (let si = 0; si < 180; si++) {
      const b0 = (si / 180) * 360;
      const b1 = ((si + 1) / 180) * 360;
      const p0 = galactocentricToHeliocentric(r, b0, 0);
      const p1 = galactocentricToHeliocentric(r, b1, 0);
      add(applyMatrix(m, p0[0], p0[1], p0[2]));
      add(applyMatrix(m, p1[0], p1[1], p1[2]));
    }
  }
  // The Sun to the galactic centre — the line every other number here is measured from.
  const sun = galactocentricToHeliocentric(R_SUN_KPC, 0, 0);
  const centre = galactocentricToHeliocentric(0, 0, 0);
  add(applyMatrix(m, sun[0], sun[1], sun[2]));
  add(applyMatrix(m, centre[0], centre[1], centre[2]));
  return { positions: new Float32Array(pos), colors: new Float32Array(col) };
}

function clamp01(t: number): number {
  return Number.isFinite(t) ? Math.max(0, Math.min(1, t)) : 0;
}

function clamp(v: number, lo: number, hi: number): number {
  return Math.max(lo, Math.min(hi, Number.isFinite(v) ? v : lo));
}
