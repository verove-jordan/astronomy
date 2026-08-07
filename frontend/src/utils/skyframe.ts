// The scene's own coordinate frame, reconstructed in the browser.
//
// The engine builds this frame to place the stars (internal/scene3d/basis.go) but ships only its
// Z axis — `center_ra`/`center_dec` in the manifest. That fixes where the camera looks and says
// nothing about which way is up, so X and Y are one unknown angle short. Anything that has to put an
// external object into the scene — the Milky Way, a galactic direction, a coordinate grid — needs
// that missing angle, because a frame rotated by an arbitrary roll draws a galaxy that looks
// entirely plausible and is wrong.
//
// The three anchors that determine it are already on the wire in `stars.json` as `solve.frame`; the
// TypeScript type simply never declared them. So this is a faithful port of basis.go rather than a
// new derivation, and it is checkable: newBasis recovers the field of view and the parity as well as
// the axes, and those can be compared against the manifest the engine wrote. If they disagree, the
// two files came from different passes and nothing should be drawn.

import type { SkyFrame } from "@/types";

export type Vec3 = [number, number, number];

/** SceneBasis is the image's frame on the sky: three axes plus what they imply about the camera. */
export interface SceneBasis {
  /** X and Y run along the final image's own x and y axes; Z points at the field centre. */
  X: Vec3;
  Y: Vec3;
  Z: Vec3;
  tanHalfW: number;
  tanHalfH: number;
  fovYDeg: number;
  /** rightHanded is false on a mirrored field — a session shot through a star diagonal. */
  rightHanded: boolean;
}

// minAxisSine is the smallest angle (as a sine) two anchors may subtend and still define an axis.
// 1e-9 rad is 0.2 milliarcseconds; the narrowest real field is arcminutes across.
const MIN_AXIS_SINE = 1e-9;

const DEG = Math.PI / 180;

/**
 * skyToVec converts an equatorial position (degrees, ICRS/J2000) to a unit vector. No precession
 * anywhere here — everything this consumes is already in one frame, exactly as in Go.
 */
export function skyToVec(raDeg: number, decDeg: number): Vec3 {
  const ra = raDeg * DEG;
  const dec = decDeg * DEG;
  const cosDec = Math.cos(dec);
  return [cosDec * Math.cos(ra), cosDec * Math.sin(ra), Math.sin(dec)];
}

function dot(a: Vec3, b: Vec3): number {
  return a[0] * b[0] + a[1] * b[1] + a[2] * b[2];
}

function cross(a: Vec3, b: Vec3): Vec3 {
  return [
    a[1] * b[2] - a[2] * b[1],
    a[2] * b[0] - a[0] * b[2],
    a[0] * b[1] - a[1] * b[0],
  ];
}

/** perpendicularTo removes a's component along the unit vector u (Gram-Schmidt). */
function perpendicularTo(a: Vec3, u: Vec3): Vec3 {
  const k = dot(a, u);
  return [a[0] - u[0] * k, a[1] - u[1] * k, a[2] - u[2] * k];
}

/** unitAbove normalises, or returns null when the vector is shorter than the tolerance. */
function unitAbove(v: Vec3, minLen: number): Vec3 | null {
  const n = Math.hypot(v[0], v[1], v[2]);
  if (!(n > minLen)) return null;
  return [v[0] / n, v[1] / n, v[2] / n];
}

/**
 * newBasis rebuilds the scene frame from the image's three sky anchors.
 *
 * Returns null on a degenerate frame rather than a NaN basis, which the caller reports as "no
 * galaxy" rather than drawing something wrong. Mirrors newBasis in basis.go line for line, including
 * the Gram-Schmidt ORDER: y is taken perpendicular to both z and x, because a TAN field's two image
 * axes are perpendicular in the image and any residue there shears the reconstruction.
 */
export function newBasis(f: SkyFrame): SceneBasis | null {
  const z = skyToVec(f.center_ra, f.center_dec);
  const ex = skyToVec(f.x_edge_ra, f.x_edge_dec);
  const ey = skyToVec(f.y_edge_ra, f.y_edge_dec);

  const x = unitAbove(perpendicularTo(ex, z), MIN_AXIS_SINE);
  if (!x) return null;
  const y = unitAbove(
    perpendicularTo(perpendicularTo(ey, z), x),
    MIN_AXIS_SINE,
  );
  if (!y) return null;

  // tan of the half-field angle = (component across the axis) / (component along it), which is
  // exactly the gnomonic projection of the edge midpoint.
  const tanHalfW = Math.abs(dot(ex, x) / dot(ex, z));
  const tanHalfH = Math.abs(dot(ey, y) / dot(ey, z));
  if (!(tanHalfW > 0) || !(tanHalfH > 0)) return null;

  return {
    X: x,
    Y: y,
    Z: z,
    tanHalfW,
    tanHalfH,
    fovYDeg: (2 * Math.atan(tanHalfH) * 180) / Math.PI,
    rightHanded: dot(cross(x, y), z) > 0,
  };
}

/**
 * project maps an equatorial position to a unit direction in scene coordinates — the same direction
 * the engine stored for every star, so anything projected here lands in the same space as the field.
 */
export function project(b: SceneBasis, raDeg: number, decDeg: number): Vec3 {
  const v = skyToVec(raDeg, decDeg);
  return [dot(v, b.X), dot(v, b.Y), dot(v, b.Z)];
}

/**
 * matchesCamera checks the reconstruction against what the engine actually wrote.
 *
 * This is the cheapest guard available against the one failure that would look completely
 * believable. `stars.json` and the scene manifest are written by separate passes; if they ever come
 * from different runs, the frame here would describe a different photograph and the galaxy would be
 * drawn confidently at the wrong roll — a picture with no visible defect and no correct part. The
 * field of view and the parity fall out of newBasis for free, so comparing them costs one `if`.
 */
export function matchesCamera(
  b: SceneBasis,
  camera: { tan_half_w: number; tan_half_h: number; right_handed?: boolean },
  tol = 1e-6,
): boolean {
  if (
    camera.right_handed !== undefined &&
    camera.right_handed !== b.rightHanded
  ) {
    return false;
  }
  return (
    Math.abs(b.tanHalfW - camera.tan_half_w) <=
      tol * Math.max(1, camera.tan_half_w) &&
    Math.abs(b.tanHalfH - camera.tan_half_h) <=
      tol * Math.max(1, camera.tan_half_h)
  );
}
