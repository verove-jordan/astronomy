// The orbit camera: a target to look at, a distance from it, and two angles.
//
// Extracted from scene3d.ts so the star field and the solar-system map share one camera rather than
// growing two that drift apart. The only thing that differed between them is which way the world's
// axes point, so that is the one thing this takes as a parameter: the star field's scene is built on
// image rows and has Y pointing DOWN, while the solar system is built on the ecliptic and has Z
// pointing to its north pole. Everything else — the gestures, the limits, the feel — is identical,
// and should stay identical.

import { cross, dot, normalize, type Mat4, type Vec3 } from "@/utils/mat4";

// Frame says which way a scene's world axes point.
export interface Frame {
  // up is the direction that appears up on screen at zero roll.
  up: Vec3;
  // forward is the direction the camera looks along at yaw = pitch = 0.
  forward: Vec3;
}

// IMAGE_FRAME is the star field's convention: the scene is laid out along the photograph's own rows,
// so +Y runs DOWN the picture and the camera starts looking along +Z, out into the field.
export const IMAGE_FRAME: Frame = { up: [0, -1, 0], forward: [0, 0, 1] };

// ECLIPTIC_FRAME is the solar system's: +Z is the ecliptic north pole, and the camera starts edge-on
// to the plane. Pitching up to +90° looks straight down on the system from the north, which is the
// view everyone pictures when they think of the solar system.
export const ECLIPTIC_FRAME: Frame = { up: [0, 0, 1], forward: [0, 1, 0] };

export interface Orbit {
  target: [number, number, number];
  distance: number;
  yaw: number;
  pitch: number;
  roll: number;
}

// PITCH_LIMIT stops the camera passing through the poles, where the up vector flips and the view
// rolls over unpredictably under the pointer.
export const PITCH_LIMIT = Math.PI / 2 - 0.01;

// frameRight is the third axis of a frame, completing the right-handed set.
function frameRight(f: Frame): Vec3 {
  return normalize(cross(f.forward, f.up));
}

function axes(o: Orbit, f: Frame) {
  const right = frameRight(f);
  const cp = Math.cos(o.pitch);
  const sp = Math.sin(o.pitch);
  const cy = Math.cos(o.yaw);
  const sy = Math.sin(o.yaw);
  return { right, cp, sp, cy, sy };
}

// eyePosition is where the camera sits, in world coordinates.
export function eyePosition(o: Orbit, f: Frame): Vec3 {
  const { right, cp, sp, cy, sy } = axes(o, f);
  const d = o.distance;
  return [
    o.target[0] +
      d * (-cp * cy * f.forward[0] + cp * sy * right[0] + sp * f.up[0]),
    o.target[1] +
      d * (-cp * cy * f.forward[1] + cp * sy * right[1] + sp * f.up[1]),
    o.target[2] +
      d * (-cp * cy * f.forward[2] + cp * sy * right[2] + sp * f.up[2]),
  ];
}

// viewBasis is the camera's own axes in world coordinates: where it looks, what is to its right, and
// which way is up on screen. The renderer, the picker and the panner all read the camera from here,
// so none of them can hold a different opinion of where it is pointing.
export function viewBasis(
  o: Orbit,
  f: Frame,
): { forward: Vec3; right: Vec3; up: Vec3 } {
  const eye = eyePosition(o, f);
  const forward = normalize([
    o.target[0] - eye[0],
    o.target[1] - eye[1],
    o.target[2] - eye[2],
  ]);
  const worldRight = frameRight(f);
  const rolled: Vec3 = [
    Math.cos(o.roll) * f.up[0] + Math.sin(o.roll) * worldRight[0],
    Math.cos(o.roll) * f.up[1] + Math.sin(o.roll) * worldRight[1],
    Math.cos(o.roll) * f.up[2] + Math.sin(o.roll) * worldRight[2],
  ];
  let right = normalize(cross(forward, rolled));
  if (!Number.isFinite(right[0])) right = [1, 0, 0];
  return { forward, right, up: cross(right, forward) };
}

// viewMatrix builds the world→eye transform for an orbit.
export function viewMatrix(o: Orbit, f: Frame): Mat4 {
  const eye = eyePosition(o, f);
  const { forward, right, up } = viewBasis(o, f);
  return assemble(forward, right, up, eye);
}

// viewMatrixAtOrigin is the same rotation with the eye pinned to the origin — the camera-relative
// form.
//
// A scene that spans from a moon eleven kilometres across to Neptune's orbit at thirty astronomical
// units cannot be held in the float32 a GPU takes: the difference between two nearby points
// disappears entirely once they are quoted from the Sun. So positions are subtracted from the eye in
// double precision on the way to the buffer, and the view matrix must then NOT translate again.
export function viewMatrixAtOrigin(o: Orbit, f: Frame): Mat4 {
  const { forward, right, up } = viewBasis(o, f);
  return assemble(forward, right, up, [0, 0, 0]);
}

function assemble(forward: Vec3, right: Vec3, up: Vec3, eye: Vec3): Mat4 {
  const m = new Float32Array(16);
  m[0] = right[0];
  m[4] = right[1];
  m[8] = right[2];
  m[1] = up[0];
  m[5] = up[1];
  m[9] = up[2];
  m[2] = -forward[0];
  m[6] = -forward[1];
  m[10] = -forward[2];
  m[12] = -dot(right, eye);
  m[13] = -dot(up, eye);
  m[14] = dot(forward, eye);
  m[15] = 1;
  return m;
}

// panOrbit slides the camera sideways by translating what it looks at. The step scales with the
// orbit distance so a drag moves the same number of SCREEN pixels whether you are among the moons or
// outside Neptune — panning that changes speed with zoom is the thing that feels broken.
export function panOrbit(
  o: Orbit,
  dxPx: number,
  dyPx: number,
  viewportHeight: number,
  tanHalfH: number,
  f: Frame,
): Orbit {
  if (!(viewportHeight > 0)) return o;
  // Screen height at the target's distance, in scene units — one pixel is this much world.
  const perPx = (2 * tanHalfH * o.distance) / viewportHeight;
  const { right: worldRight, cy, sy } = axes(o, f);

  const eye = eyePosition(o, f);
  const forward = normalize([
    o.target[0] - eye[0],
    o.target[1] - eye[1],
    o.target[2] - eye[2],
  ]);
  const right: Vec3 = [
    cy * worldRight[0] + sy * f.forward[0],
    cy * worldRight[1] + sy * f.forward[1],
    cy * worldRight[2] + sy * f.forward[2],
  ];
  const up = cross(right, forward);

  // The field follows the pointer on BOTH axes, and the opposite signs are what achieve that rather
  // than a typo: the whole rig translates, so a fixed world point picks up −Δ in camera axes, and
  // screen +y points DOWN while `up` points up the screen.
  const alongRight = -dxPx * perPx;
  const alongUp = dyPx * perPx;
  return {
    ...o,
    target: [
      o.target[0] + alongRight * right[0] + alongUp * up[0],
      o.target[1] + alongRight * right[1] + alongUp * up[1],
      o.target[2] + alongRight * right[2] + alongUp * up[2],
    ],
  };
}

// ZOOM_* shape how a gesture becomes a distance change.
export const ZOOM_BASE = 0.0016; // exponent per pixel of a slow, deliberate gesture
export const ZOOM_VELOCITY_GAIN = 0.9; // how much a fast flick is amplified
export const ZOOM_MAX_VELOCITY = 6; // px/ms beyond which extra speed buys nothing

// zoomExponent turns one wheel or pinch event into a multiplier for the orbit distance, with the
// gain rising with how FAST the gesture is moving.
//
// A single fixed exponent per pixel cannot serve both jobs: small enough to place the camera
// precisely and it takes a dozen swipes to cross three decades of scale; large enough to cross them
// and fine positioning is impossible. Scaling with gesture velocity gives both — ease the wheel and
// it creeps, flick it and it covers the field.
export function zoomExponent(deltaY: number, dtMs: number): number {
  const speed = dtMs > 0 ? Math.abs(deltaY) / dtMs : ZOOM_MAX_VELOCITY;
  const gain = 1 + ZOOM_VELOCITY_GAIN * Math.min(ZOOM_MAX_VELOCITY, speed);
  return deltaY * ZOOM_BASE * gain;
}

export const MIN_ORBIT_DISTANCE = 0.004;
export const MAX_ORBIT_DISTANCE = 400;

// ORBIT_HEADROOM is how much further than the farthest object the eye may go: enough to look back at
// everything from outside it, and no further, so the zoom-out does not run off into empty space.
export const ORBIT_HEADROOM = 6;

export function maxOrbitDistance(farthestUnits = 0): number {
  const want = Number.isFinite(farthestUnits)
    ? farthestUnits * ORBIT_HEADROOM
    : 0;
  return Math.max(MAX_ORBIT_DISTANCE, want);
}

export function applyZoom(
  o: Orbit,
  exponent: number,
  maxDistance?: number,
  minDistance = MIN_ORBIT_DISTANCE,
): Orbit {
  const d = o.distance * Math.exp(exponent);
  const hi = maxDistance ?? MAX_ORBIT_DISTANCE;
  return {
    ...o,
    distance: Math.min(hi, Math.max(minDistance, d)),
  };
}
