// Flying the 3D field, as opposed to orbiting it.
//
// The view is an orbit rig — a target, a distance and two angles — which is the right model for
// turning something over in your hands and the wrong one for going somewhere. Orbiting always keeps
// the same point in the middle of the screen, so getting *into* the volume means dragging the target
// there by hand, at a speed that changes with zoom, while the thing you wanted to look at slides off
// the edge. That is the navigation the exploration mode replaces.
//
// The trick is that flying needs no second camera model. The eye is derived from the target, so
// translating the target translates the eye by exactly the same vector and leaves the orientation
// untouched: move the target forward and you fly forward. Every function here is a pure Orbit →
// Orbit step, which keeps the whole mode testable without a GPU.
//
// The one thing to be careful about: scene Y points DOWN, because the image's rows do (see
// viewMatrix in scene3d.ts). Every vertical sign in this file follows from that, and getting it
// backwards does not look broken — it just flies down when you asked for up.

import {
  MAX_ORBIT_DISTANCE,
  MIN_ORBIT_DISTANCE,
  PITCH_LIMIT,
  eyePosition,
  type Orbit,
} from "@/utils/scene3d";

export type Vec3 = [number, number, number];

/** Move is a movement intent in camera-relative units, each component in [-1, 1]. */
export interface Move {
  /** forward is +1 towards what the camera looks at, −1 away. */
  forward: number;
  /** right is +1 towards the right of the screen. */
  right: number;
  /** up is +1 towards the top of the world (NOT the top of the screen — see worldUp). */
  up: number;
}

/** CameraBasis is the camera's own axes in scene coordinates. */
export interface CameraBasis {
  forward: Vec3;
  right: Vec3;
  up: Vec3;
}

// worldUp is which way is up in the scene, independent of where the camera looks.
//
// Vertical movement uses this rather than the camera's own up vector on purpose. Tying "up" to the
// camera means that after looking down at the field, pressing up flies you forward into it — the
// control stops meaning one thing. A fixed world up keeps altitude and direction separate, which is
// what every flight control that people find predictable does.
const WORLD_UP: Vec3 = [0, -1, 0]; // −Y, because scene Y points down

/**
 * cameraBasis returns the camera's forward/right/up axes for an orbit, built the same way
 * viewMatrix builds them so that flying and drawing can never disagree about which way is which.
 */
export function cameraBasis(o: Orbit): CameraBasis {
  const eye = eyePosition(o);
  const forward = normalize([
    o.target[0] - eye[0],
    o.target[1] - eye[1],
    o.target[2] - eye[2],
  ]);
  // Same up convention as viewMatrix: −Y, rolled.
  const rigUp: Vec3 = [Math.sin(o.roll), -Math.cos(o.roll), 0];
  let right = normalize(cross(forward, rigUp));
  // Looking straight along the rig's up vector leaves the cross product undefined. PITCH_LIMIT
  // normally prevents it, but roll can still line them up, and a NaN here would put the camera
  // somewhere with no coordinates at all — from which no amount of further input recovers.
  if (!Number.isFinite(right[0])) right = [1, 0, 0];
  const up = cross(right, forward);
  return { forward, right, up };
}

/** FlySteps is how far one frame of held input travels, along the view axis and across it. */
export interface FlySteps {
  /** forward is the step towards or away from what the camera looks at. */
  forward: number;
  /** lateral is the step sideways or vertically, across the picture. */
  lateral: number;
}

/**
 * flySteps is how far one frame of held input should move the camera, in scene units.
 *
 * Forward and sideways get DIFFERENT speeds, and that is the whole point of this function rather
 * than a convenience. The scene is a needle: the depth cone runs from the observer out to Z_REF +
 * Z_SPAN, five units, while its width at that far end is only tan(half-FOV) × 5 — for the couple of
 * degrees an astrograph sees, well under a fifth of a unit. Those two extents differ by a factor of
 * thirty, so one speed cannot serve both. Sharing the radial speed with the sideways axes is what
 * made strafing leave the field in a twentieth of a second while flying forward felt right.
 *
 * So each axis is scaled by the thing it actually travels through:
 *
 *   - forward is RADIAL — it closes on the target, so it scales with how far away the target is,
 *     exactly as panOrbit scales with distance. Crossing ~90% of the viewing distance per second.
 *   - lateral is TRANSVERSE — it crosses the picture, so it scales with how much picture there is:
 *     the world height the viewport covers at the target, 2·tan(half-FOV)·distance. Crossing ~80%
 *     of a screen height per second, which is the same on screen at every zoom and every focal
 *     length.
 */
export function flySteps(
  o: Orbit,
  tanHalfH: number,
  dtMs: number,
  boost = false,
): FlySteps {
  const dt = Math.min(dtMs, 100) / 1000; // a backgrounded tab must not teleport the camera
  const gain = (boost ? 3 : 1) * dt;
  return {
    forward: o.distance * 0.9 * gain,
    lateral: viewHeightAt(o, tanHalfH) * 0.8 * gain,
  };
}

// viewHeightAt is the world height the viewport spans at the target's distance.
//
// A missing or nonsense field of view falls back to something wide rather than to zero: a zero would
// make the sideways keys dead, which is a worse failure than a slightly brisk strafe.
function viewHeightAt(o: Orbit, tanHalfH: number): number {
  const t = Number.isFinite(tanHalfH) && tanHalfH > 0 ? tanHalfH : 0.5;
  return 2 * t * o.distance;
}

/**
 * fly translates the orbit's target — and with it the eye — along the camera axes. Orientation and
 * distance are untouched, so the view keeps looking exactly where it looked.
 */
export function fly(o: Orbit, move: Move, steps: FlySteps): Orbit {
  const len = Math.hypot(move.forward, move.right, move.up);
  if (!(len > 0)) return o;
  // Divided by the length of the INPUT so that holding two directions travels at the same speed as
  // holding one, instead of the √2 bonus that makes diagonal movement feel like a cheat. Normalising
  // the input rather than the output keeps each axis at its own speed, which is the point.
  const f = (move.forward * steps.forward) / len;
  const r = (move.right * steps.lateral) / len;
  const u = (move.up * steps.lateral) / len;
  const { forward, right } = cameraBasis(o);
  const dx = forward[0] * f + right[0] * r + WORLD_UP[0] * u;
  const dy = forward[1] * f + right[1] * r + WORLD_UP[1] * u;
  const dz = forward[2] * f + right[2] * r + WORLD_UP[2] * u;
  return {
    ...o,
    target: [o.target[0] + dx, o.target[1] + dy, o.target[2] + dz],
  };
}

/**
 * look turns the camera IN PLACE — the eye stays exactly where it is and the view direction swings.
 *
 * This is the difference between exploring and inspecting, and it is not what changing yaw and pitch
 * does on its own. In an orbit rig those angles move the EYE around a fixed target, so dragging
 * while standing among the stars swings you bodily around a point somewhere out in front — the view
 * lurches sideways and whatever you were flying towards slides away. Here the new target is
 * recomputed so that eyePosition comes out unchanged, which turns the same two angles into a head
 * that looks around.
 *
 * Pitch is clamped short of the poles, where the up vector flips and the view rolls over under the
 * hand.
 */
export function look(o: Orbit, dYaw: number, dPitch: number): Orbit {
  const eye = eyePosition(o);
  const turned: Orbit = {
    ...o,
    yaw: o.yaw + dYaw,
    pitch: Math.max(-PITCH_LIMIT, Math.min(PITCH_LIMIT, o.pitch + dPitch)),
  };
  // Pull the target back by however far the eye drifted, which puts the eye back where it started.
  const drifted = eyePosition(turned);
  return {
    ...turned,
    target: [
      turned.target[0] + (eye[0] - drifted[0]),
      turned.target[1] + (eye[1] - drifted[1]),
      turned.target[2] + (eye[2] - drifted[2]),
    ],
  };
}

/**
 * dolly changes how far the eye sits from the point it turns around. In flight this is the closest
 * thing to a zoom, and it is clamped to the same range the orbit zoom uses so the two cannot leave
 * the camera in a state the other one refuses to accept.
 */
export function dolly(o: Orbit, factor: number): Orbit {
  if (!(factor > 0)) return o;
  return {
    ...o,
    distance: Math.max(
      MIN_ORBIT_DISTANCE,
      Math.min(MAX_ORBIT_DISTANCE, o.distance * factor),
    ),
  };
}

/**
 * lookPerPixel is how far the view should turn per pixel of mouse movement, in radians.
 *
 * A fixed constant cannot work here for the same reason a single movement speed could not: a turn is
 * an ANGLE, but what the eye judges is how far the picture slid, and that depends on how much sky the
 * screen is showing. The old flat 0.005 rad/px is a quarter of a degree per pixel — pleasant across a
 * sixty-degree view, and across the two degrees of an astrograph it sweeps the entire field in seven
 * pixels of mouse travel.
 *
 * So the unit is one pixel's own angular size, 2·tan(half-FOV) / viewport height. At a gain of 1 the
 * drag is exactly one-to-one: whatever sits under the cursor stays under the cursor, which is the
 * only sensitivity nobody has to learn. It also makes the feel identical at every zoom and every
 * focal length, since both cancel out of the ratio.
 *
 * Note there is deliberately no cos(pitch) correction on yaw. It would make the motion truly
 * one-to-one while looking steeply up or down, at the cost of amplifying the mouse a hundredfold
 * near the pole — a bargain every first-person control declines, and so does this one.
 */
export function lookPerPixel(
  tanHalfH: number,
  viewportHeightPx: number,
  gain = 1,
): number {
  const t = Number.isFinite(tanHalfH) && tanHalfH > 0 ? tanHalfH : 0.5;
  const h = viewportHeightPx > 0 ? viewportHeightPx : 800;
  return ((2 * t) / h) * gain;
}

/**
 * orbitPerPixel is how far the camera should swing AROUND the target per pixel, in radians.
 *
 * Deliberately not lookPerPixel, and the difference is not an oversight. Looking around is a pan
 * across the sky, so one-to-one is the only rate nobody has to learn. Orbiting is circling an
 * object, and the thing you want from it is to get round to the other side — at the one-to-one rate
 * a one-degree field would need a hundred and eighty full-screen drags to show you the back. Perfect
 * consistency, entirely useless.
 *
 * So orbiting is measured in fractions of a TURN rather than pixels of sky: a full-height drag is
 * half a revolution, at every zoom and every focal length. The signs still come from dragToLook, so
 * the two gestures agree about which way the field goes even though they disagree about how far.
 */
export function orbitPerPixel(viewportHeightPx: number): number {
  const h = viewportHeightPx > 0 ? viewportHeightPx : 800;
  return Math.PI / h;
}

/**
 * dragToLook turns a mouse movement into a turn of the view.
 *
 * It exists to hold the SIGNS, which are the whole difficulty. The field has to follow the pointer on
 * both axes — grab it, drag it, it comes with you — and that means the camera turns the opposite way
 * to the drag: pull right and the view swings left, so the stars travel right. Getting one axis right
 * and the other backwards is not subtly wrong, it is nauseating, and it is easy to do because yaw and
 * pitch do not share a handedness in the orbit rig.
 *
 * So the rule is stated once here, and asserted against cameraBasis rather than against the raw angle
 * signs — the test then survives anyone changing the rig's conventions.
 */
export function dragToLook(
  dxPx: number,
  dyPx: number,
  perPx: number,
): { dYaw: number; dPitch: number } {
  return { dYaw: dxPx * perPx, dPitch: -dyPx * perPx };
}

/**
 * LOOK_GAIN_DRAG is 1, so dragging moves the sky with the pointer exactly.
 */
export const LOOK_GAIN_DRAG = 1;

/**
 * LOOK_GAIN_LOCKED is a little brisker than one-to-one. With the cursor captured there is no longer
 * anything on screen for the motion to stay glued to, and the limit becomes how far the hand can go
 * before it runs out of desk — so a locked look trades the exact correspondence for reach.
 */
export const LOOK_GAIN_LOCKED = 2;

// --- small vector helpers (scene3d.ts keeps its own private copies) -------------------------------

function cross(a: Vec3, b: Vec3): Vec3 {
  return [
    a[1] * b[2] - a[2] * b[1],
    a[2] * b[0] - a[0] * b[2],
    a[0] * b[1] - a[1] * b[0],
  ];
}

function normalize(v: Vec3): Vec3 {
  const n = Math.hypot(v[0], v[1], v[2]);
  if (!(n > 0)) return [0, 0, 0];
  return [v[0] / n, v[1] / n, v[2] / n];
}
