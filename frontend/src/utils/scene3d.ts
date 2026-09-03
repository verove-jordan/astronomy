import type { Scene3DBillboard, Scene3DManifest, Scene3DShape } from "@/types";
import {
  perspective,
  projectToScreen,
  type Mat4,
  type Viewport,
} from "@/utils/mat4";
import { IMAGE_FRAME, type Orbit } from "@/utils/orbitcam";
import * as cam from "@/utils/orbitcam";

// Pure geometry and decoding for the 3D field map. Everything the WebGL renderer needs that is not
// a GL call lives here, because happy-dom has no WebGL context — anything left inside the draw loop
// is untestable by construction, the same split that keeps starOverlay.ts out of its canvas.
//
// The engine did all the astronomy. What arrives is a unit direction and a distance per star; this
// module only decides where that lands on screen.
//
// The matrix and orbit-camera maths moved to mat4.ts and orbitcam.ts when the solar-system map
// needed the same camera in a scene whose up axis is different. They are re-exported from here so
// this module's callers — and its spec — are unaffected; the field map binds IMAGE_FRAME, the
// convention where +Y runs DOWN the photograph's rows.

// --- the binary star field ---------------------------------------------------------------------

// Mirrors internal/scene3d/format.go. A reader that meets an unknown version or record size must
// refuse the buffer rather than misread it — the offsets below are only meaningful for version 2.
export const SCENE_MAGIC = "ASTRO3DS";
export const SCENE_VERSION = 2;
export const HEADER_SIZE = 64;
export const RECORD_SIZE = 32;

// Record byte offsets. Floats sit on multiples of 4 and the name index on a multiple of 2, which is
// what lets every attribute be read straight off the same interleaved buffer by the GPU.
export const OFF_DIR = 0; // 3 × float32
export const OFF_DIST = 12; // float32, parsec
export const OFF_RGB = 16; // 3 × uint8
export const OFF_ABSMAG = 19; // int8 ×4, ABSMAG_UNKNOWN when absent
export const OFF_FLAGS = 20;
export const OFF_MAG = 21; // uint8, (m+5)×8, MAG_UNKNOWN when absent
export const OFF_NAME = 22; // uint16, 1-based index into the name table
export const OFF_VX = 24; // int16 ×10, space velocity in scene coordinates, km/s
export const OFF_VY = 26;
export const OFF_VZ = 28;
export const OFF_SRC = 30; // uint16, index into the run's stars.json

// VEL_SCALE mirrors the engine's quantisation: km/s ×10.
export const VEL_SCALE = 10;

export const ABSMAG_UNKNOWN = -128;
export const MAG_UNKNOWN = 255;

// Depth provenance, in the low two bits of the flags byte.
export const DEPTH_UNKNOWN = 0;
export const DEPTH_MEASURED = 1;
export const DEPTH_ESTIMATED = 2;
export const FLAG_DEPTH_MASK = 0x03;
export const FLAG_IDENTIFIED = 0x04;
export const FLAG_CLUSTER_MEMBER = 0x08;
export const FLAG_HAS_VELOCITY = 0x10;
export const FLAG_PHYSICAL_COLOUR = 0x20;

export interface Scene3DPoints {
  count: number;
  // The raw interleaved records, handed to the GPU as one buffer with no copy or reshaping.
  buffer: ArrayBuffer;
  byteOffset: number;
  // Views over the same bytes, for picking and the info card. No allocation per star.
  view: DataView;
  names: string[];
}

// decodeScene reads a scene3d.bin into something the renderer can upload directly. It throws on a
// buffer it does not recognise: drawing a misread star field would put every star in the wrong
// place while looking perfectly plausible.
export function decodeScene(buf: ArrayBuffer): Scene3DPoints {
  if (buf.byteLength < HEADER_SIZE)
    throw new Error("scene3d: buffer too short");
  const view = new DataView(buf);
  const magic = new TextDecoder().decode(new Uint8Array(buf, 0, 8));
  if (magic !== SCENE_MAGIC) throw new Error("scene3d: not a scene file");
  const version = view.getUint16(8, true);
  if (version !== SCENE_VERSION)
    throw new Error(`scene3d: unsupported version ${version}`);
  const recordSize = view.getUint16(10, true);
  if (recordSize !== RECORD_SIZE)
    throw new Error(`scene3d: unsupported record size ${recordSize}`);

  const count = view.getUint32(12, true);
  const recOff = view.getUint32(16, true);
  const strOff = view.getUint32(20, true);
  if (recOff < HEADER_SIZE || strOff < recOff + count * RECORD_SIZE)
    throw new Error("scene3d: inconsistent offsets");
  if (strOff > buf.byteLength) throw new Error("scene3d: truncated");

  return {
    count,
    buffer: buf,
    byteOffset: recOff,
    view,
    names: decodeNames(view, strOff, buf.byteLength),
  };
}

// decodeNames reads the trailing name table: a uint32 count, then uint16 length + UTF-8 bytes each.
function decodeNames(view: DataView, off: number, end: number): string[] {
  if (off + 4 > end) return [];
  const n = view.getUint32(off, true);
  const out: string[] = [];
  let p = off + 4;
  const dec = new TextDecoder();
  for (let i = 0; i < n && p + 2 <= end; i++) {
    const len = view.getUint16(p, true);
    p += 2;
    if (p + len > end) break;
    out.push(dec.decode(new Uint8Array(view.buffer, view.byteOffset + p, len)));
    p += len;
  }
  return out;
}

// StarRecord is one decoded star, read on demand for a hover or a click rather than held for all of
// them — 5000 objects would be 5000 allocations the renderer never needs.
export interface StarRecord {
  index: number;
  dir: [number, number, number];
  distPc: number;
  hex: string;
  mag: number | null;
  absMag: number | null;
  depth: number;
  identified: boolean;
  clusterMember: boolean;
  physicalColour: boolean;
  name: string;
  // Space velocity in scene coordinates, km/s. Null when the catalogue measured no motion.
  velocity: [number, number, number] | null;
  // Index into the run's stars.json, so a hover reads the full catalogue row out of the annotation
  // the viewer has already fetched instead of a second copy of it living in this file.
  srcIndex: number;
}

export function readStar(p: Scene3DPoints, i: number): StarRecord {
  const o = p.byteOffset + i * RECORD_SIZE;
  const v = p.view;
  const flags = v.getUint8(o + OFF_FLAGS);
  const rawAbs = v.getInt8(o + OFF_ABSMAG);
  const rawMag = v.getUint8(o + OFF_MAG);
  const nameIdx = v.getUint16(o + OFF_NAME, true);
  const hx = (n: number) => n.toString(16).padStart(2, "0");
  return {
    index: i,
    dir: [
      v.getFloat32(o + OFF_DIR, true),
      v.getFloat32(o + OFF_DIR + 4, true),
      v.getFloat32(o + OFF_DIR + 8, true),
    ],
    distPc: v.getFloat32(o + OFF_DIST, true),
    hex: `#${hx(v.getUint8(o + OFF_RGB))}${hx(v.getUint8(o + OFF_RGB + 1))}${hx(v.getUint8(o + OFF_RGB + 2))}`,
    mag: rawMag === MAG_UNKNOWN ? null : rawMag / 8 - 5,
    absMag: rawAbs === ABSMAG_UNKNOWN ? null : rawAbs / 4,
    depth: flags & FLAG_DEPTH_MASK,
    identified: (flags & FLAG_IDENTIFIED) !== 0,
    clusterMember: (flags & FLAG_CLUSTER_MEMBER) !== 0,
    physicalColour: (flags & FLAG_PHYSICAL_COLOUR) !== 0,
    name: nameIdx > 0 ? (p.names[nameIdx - 1] ?? "") : "",
    velocity:
      (flags & FLAG_HAS_VELOCITY) === 0
        ? null
        : [
            v.getInt16(o + OFF_VX, true) / VEL_SCALE,
            v.getInt16(o + OFF_VY, true) / VEL_SCALE,
            v.getInt16(o + OFF_VZ, true) / VEL_SCALE,
          ],
    srcIndex: v.getUint16(o + OFF_SRC, true),
  };
}

// --- the depth warp ------------------------------------------------------------------------------

// Z_REF / Z_SPAN place the scene in front of the camera. They are view constants, not measurements:
// the engine ships the field's real distances and deliberately leaves how deep the cone should LOOK
// to whoever is drawing it.
export const Z_REF = 1;
export const Z_SPAN = 4;

// warpZ maps a distance in parsec to a depth plane. `depth` is the slider: at 0 every star lands on
// the same plane, so the perspective projection of that plane is the photograph itself; at 1 the
// field opens into a logarithmic cone spanning near→far.
//
// Logarithmic because a real field covers three or four decades. Placed linearly, everything past
// the first tenth of the range piles onto the back plane and the picture has no depth at all.
export function warpZ(
  distPc: number,
  near: number,
  far: number,
  depth: number,
): number {
  if (!(far > near) || !(distPc > 0)) return Z_REF;
  const t = Math.min(
    1,
    Math.max(
      0,
      (Math.log(distPc) - Math.log(near)) / (Math.log(far) - Math.log(near)),
    ),
  );
  return Z_REF + t * depth * Z_SPAN;
}

// scenePosition is where a star actually sits: along its own line of sight, at the depth plane the
// warp put it on. Dividing by dir[2] is what keeps its screen position fixed as depth opens — the
// star slides along the ray it was seen on, never across it.
export function scenePosition(
  dir: readonly [number, number, number],
  distPc: number,
  near: number,
  far: number,
  depth: number,
): [number, number, number] {
  const z = warpZ(distPc, near, far, depth);
  const k = dir[2] > 1e-6 ? z / dir[2] : z;
  return [dir[0] * k, dir[1] * k, dir[2] * k];
}

// --- linear space ----------------------------------------------------------------------------------

// The second space, used only by the galaxy view: true parsecs, uniformly scaled.
//
// The warp above cannot carry the Milky Way. It maps THIS field's own 5th-to-95th-percentile
// distances onto z ∈ [1, 5], so its answer for eight kiloparsecs depends on which photograph you are
// looking at — the galactic centre lands on the back plane of one run and mid-cone in another, and a
// thirty-kiloparsec disc collapses to a plane. A galaxy drawn in there would be at a made-up
// distance, which is the one thing this feature must not do.
//
// So the galaxy view swaps the whole scene into linear parsecs. The two spaces are never mixed:
// mixing them would put the galaxy and the stars at incompatible distances and the picture would be
// a lie in exactly the way that is hardest to notice.

// PC_PER_SCENE_UNIT makes one scene unit one kiloparsec. Chosen so nothing else has to move: the
// field's stars land at 0.04–4 units, the Sun sits 8.15 from the centre, the disc edge at 15, and
// the whole range stays comfortably inside MIN/MAX_ORBIT_DISTANCE (4 pc to 400 kpc) and well within
// float32 precision.
export const PC_PER_SCENE_UNIT = 1000;
export const UNITS_PER_PC = 1 / PC_PER_SCENE_UNIT;

/**
 * linearPosition places a star at its true distance, uniformly scaled.
 *
 * Note what this shares with scenePosition: both put the star somewhere along the SAME ray from the
 * origin. A pinhole camera at the origin cannot tell them apart, because projection from the origin
 * depends only on direction — which is why switching a view whose eye is at Earth into linear space
 * is a zero-pixel change, and why the galaxy view can start from exactly the photograph without
 * having to special-case anything.
 */
export function linearPosition(
  dir: readonly [number, number, number],
  distPc: number,
  unitsPerPc: number = UNITS_PER_PC,
): [number, number, number] {
  const k = distPc * unitsPerPc;
  return [dir[0] * k, dir[1] * k, dir[2] * k];
}

// --- matrices ------------------------------------------------------------------------------------

export {
  identity,
  multiply,
  perspective,
  projectToScreen,
  type Mat4,
  type Viewport,
} from "@/utils/mat4";

// fitPerspective builds the projection for one scene, fitted to the CANVAS it will be drawn on.
//
// Two corrections happen here, and leaving either out looks like a rendering bug rather than a maths
// one:
//
//  1. The engine measured its half-field between PIXEL CENTRES — it anchored the frame on the centre
//     of pixel 0 and the centre of pixel W−1 — while a canvas covers those pixels' outer EDGES, half
//     a pixel further out. Feeding the tangents in raw squeezes the field by one pixel across the
//     frame.
//  2. The tangents describe the IMAGE's aspect ratio, but gl.viewport stretches clip space across
//     whatever shape the canvas happens to be. A 1.35:1 frame in a 4:1 canvas comes out squashed
//     nearly threefold. So the field is letterboxed: whichever axis the canvas has to spare is
//     WIDENED to match, showing more empty sky rather than distorting what is there.
//  3. tanScale opens the lens, and exists for the galaxy view. The run's own field is about a degree
//     across; framing a forty-kiloparsec disc through a one-degree lens would need the camera two
//     megaparsecs away, which breaks MAX_ORBIT_DISTANCE and reads as a bug. Widening the lens as the
//     camera pulls back reaches a sane vantage instead. It multiplies the tangents BEFORE the
//     letterboxing above, so aspect handling is untouched and tanScale = 1 is this function exactly
//     as it was.
export function fitPerspective(
  m: Scene3DManifest,
  canvasAspect: number,
  near = 0.01,
  far = 1000,
  tanScale = 1,
): Mat4 {
  const { tw, th } = fitTanHalf(m, canvasAspect, tanScale);
  return perspective(tw, th, near, far);
}

// fitTanHalf is the pair of half-field tangents fitPerspective ends up using — the corrections above,
// without the matrix.
//
// Split out because the galaxy shader needs the VERTICAL one: it sizes each point by the angle the
// patch of Galaxy it stands for actually subtends, and reading the field of view off a second
// expression is how a renderer ends up disagreeing with its own projection.
export function fitTanHalf(
  m: Scene3DManifest,
  canvasAspect: number,
  tanScale = 1,
): { tw: number; th: number } {
  const { width, height } = m.image;
  const kx = width > 1 ? width / (width - 1) : 1;
  const ky = height > 1 ? height / (height - 1) : 1;
  const s = Number.isFinite(tanScale) && tanScale > 0 ? tanScale : 1;
  let tw = m.camera.tan_half_w * kx * s;
  let th = m.camera.tan_half_h * ky * s;

  const imageAspect = tw / th;
  if (Number.isFinite(canvasAspect) && canvasAspect > 0 && imageAspect > 0) {
    if (canvasAspect > imageAspect) tw = th * canvasAspect;
    else th = tw / canvasAspect;
  }
  return { tw, th };
}

// Orbit is the camera: a target to look at, a distance from it, and two angles. The default —
// target on the scene's reference plane, no rotation — puts the eye at the origin, which is Earth,
// which is where the photograph was taken from.
export { PITCH_LIMIT, type Orbit } from "@/utils/orbitcam";

export function defaultOrbit(): Orbit {
  return { target: [0, 0, Z_REF], distance: Z_REF, yaw: 0, pitch: 0, roll: 0 };
}

// viewMatrix builds the world→eye transform for an orbit.
//
// Scene Y points DOWN, because the image's y axis does: the engine's basis runs along the
// picture's own rows. So the camera's up vector is −Y, not +Y. Getting that backwards does not
// look broken — it silently mirrors the whole field left-to-right, and the 3D view then opens from
// a picture that is not the run's. That is the whole of what IMAGE_FRAME says.
export function viewMatrix(o: Orbit): Mat4 {
  return cam.viewMatrix(o, IMAGE_FRAME);
}

export function eyePosition(o: Orbit): [number, number, number] {
  return cam.eyePosition(o, IMAGE_FRAME);
}

// --- physical space ------------------------------------------------------------------------------

// physicalPosition is where a star actually is, in parsec from Earth — as opposed to where the depth
// warp draws it. Brightness is computed here rather than in warped space: the warp is a view
// preference, and the inverse-square law is not.
export function physicalPosition(
  dir: readonly [number, number, number],
  distPc: number,
): [number, number, number] {
  return [dir[0] * distPc, dir[1] * distPc, dir[2] * distPc];
}

// unwarpZ inverts warpZ: given a scene depth, the distance in parsec it stands for. Only meaningful
// once the slider has opened — at depth 0 the whole field is on one plane and the map is not
// invertible, which is exactly the case cameraPhysical handles separately.
export function unwarpZ(
  z: number,
  near: number,
  far: number,
  depth: number,
): number {
  if (!(depth > 0) || !(far > near) || !(near > 0)) return 0;
  const t = (z - Z_REF) / (depth * Z_SPAN);
  return Math.exp(Math.log(near) + t * (Math.log(far) - Math.log(near)));
}

// cameraPhysical is where the eye sits in real space, which is what the inverse-square law needs.
//
// The scene is a radial map — every star lies along its own ray at a warped depth — so the camera is
// carried back through the same map, and the result is blended by the depth slider. That blend is
// what makes the two ends both correct: at depth 0 the eye is at the ORIGIN, which is Earth, so
// every star's brightness is exactly its Earth magnitude and the view is the photograph; at depth 1
// the eye is wherever it has actually flown to.
export function cameraPhysical(
  o: Orbit,
  m: Scene3DManifest,
  depth: number,
): [number, number, number] {
  const eye = eyePosition(o);
  if (!(depth > 0)) return [0, 0, 0];
  const n = Math.hypot(eye[0], eye[1], eye[2]);
  if (!(n > 0)) return [0, 0, 0];
  const d = unwarpZ(eye[2], m.depth.near_pc, m.depth.far_pc, depth);
  const k = (d / n) * depth;
  return [eye[0] * k, eye[1] * k, eye[2] * k];
}

// --- interaction ---------------------------------------------------------------------------------

// panOrbit slides the camera sideways by translating what it looks at. The step scales with the
// orbit distance so a drag moves the same number of SCREEN pixels whether you are outside the cone
// or in the middle of it — panning that changes speed with zoom is the thing that feels broken.
export function panOrbit(
  o: Orbit,
  dxPx: number,
  dyPx: number,
  viewportHeight: number,
  tanHalfH: number,
): Orbit {
  return cam.panOrbit(o, dxPx, dyPx, viewportHeight, tanHalfH, IMAGE_FRAME);
}

// ZOOM_* shape how a gesture becomes a distance change; zoomExponent turns one wheel or pinch event
// into a multiplier for the orbit distance, with the gain rising with how fast the gesture moves.
export {
  applyZoom,
  MAX_ORBIT_DISTANCE,
  MIN_ORBIT_DISTANCE,
  ZOOM_BASE,
  ZOOM_MAX_VELOCITY,
  ZOOM_VELOCITY_GAIN,
  zoomExponent,
} from "@/utils/orbitcam";

// maxOrbitDistance is how far the camera may pull back, given how far away the FARTHEST THING IN THE
// SCENE is (in scene units).
//
// A fixed ceiling cannot serve both spaces. The warped view spans five units end to end, so 400 is
// already eighty times more room than it can use. The galaxy view measures in kiloparsecs, and a run
// that caught a galaxy at seven megaparsecs needs seven thousand units just to have the thing in
// front of the lens — let alone to see it and the Milky Way in one frame. Capping that at 400 is why
// zooming out used to stop with the far object still off screen.
//
// ORBIT_HEADROOM is how much further than the farthest object the eye may go: enough to look back at
// everything from outside it, and no further, so the zoom-out does not run off into empty space.
export { maxOrbitDistance, ORBIT_HEADROOM } from "@/utils/orbitcam";

// --- motion --------------------------------------------------------------------------------------

// KM_PER_PC and SEC_PER_YEAR convert a velocity in km/s into parsecs per year, which is what turns
// "50 km/s" into a visible displacement over a hundred thousand years.
export const KM_PER_PC = 3.0856775814913673e13;
export const SEC_PER_YEAR = 31557600; // Julian year

// motionEndpoint is where a star will be after `years`, as a SCENE position — the physical
// displacement is applied first and the depth warp second, so the arrow is compressed along the
// line of sight exactly as the field around it is. Null when the star has no measured motion.
export function motionEndpoint(
  s: StarRecord,
  m: Scene3DManifest,
  depth: number,
  years: number,
): [number, number, number] | null {
  if (!s.velocity) return null;
  const pcPerYear = (SEC_PER_YEAR / KM_PER_PC) * years;
  const p = physicalPosition(s.dir, s.distPc);
  const q: [number, number, number] = [
    p[0] + s.velocity[0] * pcPerYear,
    p[1] + s.velocity[1] * pcPerYear,
    p[2] + s.velocity[2] * pcPerYear,
  ];
  const dist = Math.hypot(q[0], q[1], q[2]);
  if (!(dist > 0)) return null;
  const dir: [number, number, number] = [q[0] / dist, q[1] / dist, q[2] / dist];
  return scenePosition(dir, dist, m.depth.near_pc, m.depth.far_pc, depth);
}

// radialSign says whether a star is approaching (−1) or receding (+1), which is what colours its
// arrow. The sign of the velocity along the star's own line of sight, not along the field axis.
export function radialSign(s: StarRecord): number {
  if (!s.velocity) return 0;
  const dot =
    s.velocity[0] * s.dir[0] +
    s.velocity[1] * s.dir[1] +
    s.velocity[2] * s.dir[2];
  return Math.sign(dot);
}

// --- projection & picking ------------------------------------------------------------------------

export interface PickOptions {
  near: number;
  far: number;
  depth: number;
  radiusPx?: number;
  // Which depth sources are currently drawn — picking must not select a hidden star.
  visible?: (depth: number) => boolean;
}

// PICK_RADIUS_PX is how close a click has to land. Generous, because a star is drawn as a few pixels
// and pointing devices are not precise; the nearest one inside the radius still wins.
export const PICK_RADIUS_PX = 18;

// pickNearest finds the drawn star closest to a canvas point. One linear pass over the records is
// enough at this scale (5000 stars, once per click) and it reuses the very same projection the
// renderer draws with, so there is no second opinion about where a star is.
export function pickNearest(
  points: Scene3DPoints,
  sx: number,
  sy: number,
  viewProj: Mat4,
  vp: Viewport,
  opts: PickOptions,
): StarRecord | null {
  const radius = opts.radiusPx ?? PICK_RADIUS_PX;
  let best = -1;
  let bestD = radius * radius;
  const v = points.view;
  for (let i = 0; i < points.count; i++) {
    const o = points.byteOffset + i * RECORD_SIZE;
    if (
      opts.visible &&
      !opts.visible(v.getUint8(o + OFF_FLAGS) & FLAG_DEPTH_MASK)
    )
      continue;
    const dir: [number, number, number] = [
      v.getFloat32(o + OFF_DIR, true),
      v.getFloat32(o + OFF_DIR + 4, true),
      v.getFloat32(o + OFF_DIR + 8, true),
    ];
    const p = scenePosition(
      dir,
      v.getFloat32(o + OFF_DIST, true),
      opts.near,
      opts.far,
      opts.depth,
    );
    const s = projectToScreen(p, viewProj, vp);
    if (!s) continue;
    const d = (s[0] - sx) ** 2 + (s[1] - sy) ** 2;
    if (d < bestD) {
      best = i;
      bestD = d;
    }
  }
  return best < 0 ? null : readStar(points, best);
}

// --- billboards ----------------------------------------------------------------------------------

export interface BillboardQuad {
  // Four corners in scene coordinates, and the matching backdrop texture coordinates.
  corners: [number, number, number][];
  uvs: [number, number][];
  distPc: number;
}

// billboardQuad places one object's cutout in space. The quad faces the object's OWN line of sight
// rather than the field axis (an object near the frame edge would otherwise render visibly skewed),
// and it is sized so it subtends exactly the angle its footprint does in the picture — so at depth
// zero it lands back on top of the pixels it was cut from.
/**
 * billboardDirection is the unit line of sight an object lies on, in scene coordinates.
 *
 * Derived from the footprint's centre and the run's own lens by exactly the relation billboardQuad
 * places the quad with, so the direction an object is FLOWN toward can never disagree with the pixels
 * it is drawn from.
 */
export function billboardDirection(
  b: Scene3DBillboard,
  m: Scene3DManifest,
): [number, number, number] | null {
  const { width, height } = m.image;
  if (!(width > 1) || !(height > 1)) return null;
  const perPxX = (2 * m.camera.tan_half_w) / (width - 1);
  const perPxY = (2 * m.camera.tan_half_h) / (height - 1);
  const v: [number, number, number] = [
    (b.x - (width - 1) / 2) * perPxX,
    (b.y - (height - 1) / 2) * perPxY,
    1,
  ];
  const n = Math.hypot(v[0], v[1], v[2]);
  return n > 0 ? [v[0] / n, v[1] / n, v[2] / n] : null;
}

export function billboardQuad(
  b: Scene3DBillboard,
  m: Scene3DManifest,
  depth: number,
  // When set, the quad is placed at the object's TRUE distance instead of on a warped depth plane —
  // the galaxy view's linear space. It is not optional cosmetics: warping is calibrated to this
  // field's own stars, so an extragalactic object like M51 at 7 Mpc gets squashed onto the back
  // plane and drawn a couple of kiloparsecs away, apparently inside the Milky Way.
  unitsPerPc?: number,
): BillboardQuad | null {
  const { width, height } = m.image;
  if (!(b.dist_pc > 0) || !(width > 0) || !(height > 0)) return null;
  if (!(b.rx_px > 0) || !(b.ry_px > 0)) return null;

  const z =
    unitsPerPc !== undefined
      ? b.dist_pc * unitsPerPc
      : warpZ(b.dist_pc, m.depth.near_pc, m.depth.far_pc, depth);
  // A pixel offset in the image is an offset in the tangent plane, and at depth z that is z × tan.
  // The object's own centre goes through the same relation, so the quad is placed by its footprint
  // and nothing else — it cannot drift off the pixels it is cut from.
  const perPxX = (2 * m.camera.tan_half_w) / Math.max(1, width - 1);
  const perPxY = (2 * m.camera.tan_half_h) / Math.max(1, height - 1);
  const cx = (b.x - (width - 1) / 2) * perPxX * z;
  const cy = (b.y - (height - 1) / 2) * perPxY * z;
  const cos = Math.cos(b.angle_rad);
  const sin = Math.sin(b.angle_rad);

  const corners: [number, number, number][] = [];
  const uvs: [number, number][] = [];
  for (const [su, sv] of [
    [-1, -1],
    [1, -1],
    [1, 1],
    [-1, 1],
  ] as const) {
    // Corner offset in image pixels, in the ellipse's own rotated frame.
    const dx = su * b.rx_px * cos - sv * b.ry_px * sin;
    const dy = su * b.rx_px * sin + sv * b.ry_px * cos;
    corners.push([cx + dx * perPxX * z, cy + dy * perPxY * z, z]);
    uvs.push([(b.x + dx) / width, (b.y + dy) / height]);
  }
  return { corners, uvs, distPc: b.dist_pc };
}

// sortBillboardsFarFirst orders quads back to front. Their texture is blended additively, which is
// order-independent, but they are drawn against each other and against the depth buffer — and a
// near galaxy drawn first would then reject the far one behind it.
export function sortBillboardsFarFirst(
  list: readonly Scene3DBillboard[],
): Scene3DBillboard[] {
  return [...list].sort((a, b) => b.dist_pc - a.dist_pc);
}

// --- range rings ---------------------------------------------------------------------------------

// decadeRings picks the round distances worth marking inside the field's range: 10 pc, 100 pc,
// 1 kpc and so on. They are what turn an abstract cloud into a scale you can read.
export function decadeRings(near: number, far: number): number[] {
  if (!(near > 0) || !(far > near)) return [];
  const out: number[] = [];
  const first = Math.ceil(Math.log10(near));
  const last = Math.floor(Math.log10(far));
  for (let e = first; e <= last && out.length < 12; e++) out.push(10 ** e);
  return out;
}

// formatDistance renders a distance for display, in the unit that keeps it readable.
export function formatDistance(pc: number): string {
  if (!(pc > 0)) return "—";
  if (pc >= 1e6) return `${(pc / 1e6).toFixed(2)} Mpc`;
  if (pc >= 1e3) return `${(pc / 1e3).toFixed(2)} kpc`;
  return `${pc.toFixed(pc < 100 ? 1 : 0)} pc`;
}

// LY_PER_PC lets the card show both units: parsecs are what the catalogue measures in, light-years
// are what most people picture.
export const LY_PER_PC = 3.261563777;

// --- object shapes -------------------------------------------------------------------------------

// Turning the engine's shape descriptor into triangles.
//
// The tessellator is deliberately dumb: it knows circles, spheres and stacks of quads, and nothing
// about astronomy. Every decision that needed a catalogue — whether an object is a disc, how far it
// is tilted, how deep it is, which honesty tier that puts it in — was made in Go. Shipping the
// DESCRIPTOR rather than the mesh is also the lighter wire format by a wide margin: a 32×32 disc is
// a few thousand vertices and about a dozen numbers.

// Vertices carry (dir, distPc, uv) rather than a position, so the SAME warpZ the stars use applies
// per-vertex in the shader — a mesh spanning depth cannot drift out of step with the field it sits in.
export const SHAPE_STRIDE_FLOATS = 7;

export interface ShapeMesh {
  // 7 floats per vertex: dirX, dirY, dirZ, distPc, u, v, sliceT. sliceT is the vertex's normalised
  // offset through a volume (−0.5 … +0.5), and 0 on a solid mesh — it is what lets the fragment
  // shader decide how much of a pixel's emission belongs at this depth, so a volume's actual SHAPE
  // costs no geometry at all.
  vertices: Float32Array;
  indices: Uint16Array;
  // Slice count for a volume, so the renderer can normalise its alpha; 0 for solid meshes.
  slices: number;
  kind: Scene3DShape["kind"];
  // The object's footprint in texture coordinates. The volume shader needs to know how far a
  // fragment sits from the object's centre — for the bowl and the hollow — and four uniforms is
  // cheaper than another per-vertex attribute.
  footprint?: { cx: number; cy: number; rx: number; ry: number };
}

export const DISC_SEGMENTS = 48;
export const DISC_RINGS = 10;
export const SHELL_SEGMENTS = 32;
export const VOLUME_SLICES = 24;

// objectCentre is the object's own position in parsec, from its image footprint and the camera.
function objectCentre(
  b: Scene3DBillboard,
  m: Scene3DManifest,
): { centre: [number, number, number]; perPxX: number; perPxY: number } {
  const { width, height } = m.image;
  const perPxX = (2 * m.camera.tan_half_w) / Math.max(1, width - 1);
  const perPxY = (2 * m.camera.tan_half_h) / Math.max(1, height - 1);
  const u = (b.x - (width - 1) / 2) * perPxX;
  const v = (b.y - (height - 1) / 2) * perPxY;
  const n = Math.hypot(u, v, 1);
  const d = b.dist_pc;
  return { centre: [(u / n) * d, (v / n) * d, (1 / n) * d], perPxX, perPxY };
}

// pushVertex converts a physical offset from the object's centre into the (dir, dist, uv) a vertex
// carries. The UV comes from projecting the same point back to image pixels, so the texture is
// wrapped by exactly the geometry it is painted on.
function pushVertex(
  out: number[],
  centre: readonly [number, number, number],
  off: readonly [number, number, number],
  m: Scene3DManifest,
  perPxX: number,
  perPxY: number,
  sliceT = 0,
): void {
  const p: [number, number, number] = [
    centre[0] + off[0],
    centre[1] + off[1],
    centre[2] + off[2],
  ];
  const dist = Math.hypot(p[0], p[1], p[2]);
  if (!(dist > 0)) {
    out.push(0, 0, 1, 1, 0.5, 0.5, sliceT);
    return;
  }
  const { width, height } = m.image;
  // Gnomonic projection back to the image, the same relation billboardQuad places a flat quad by.
  const px = p[0] / p[2] / perPxX + (width - 1) / 2;
  const py = p[1] / p[2] / perPxY + (height - 1) / 2;
  out.push(
    p[0] / dist,
    p[1] / dist,
    p[2] / dist,
    dist,
    px / width,
    py / height,
    sliceT,
  );
}

// tessellateShape builds the mesh for one object, or null when it has no shape and should keep the
// flat quad.
export function tessellateShape(
  b: Scene3DBillboard,
  m: Scene3DManifest,
): ShapeMesh | null {
  const shape = b.shape;
  if (!shape || shape.kind === "plane" || !(b.dist_pc > 0)) return null;
  const { centre, perPxX, perPxY } = objectCentre(b, m);
  switch (shape.kind) {
    case "disc":
      return discMesh(shape, centre, m, perPxX, perPxY);
    case "shell":
      return shellMesh(shape, centre, m, perPxX, perPxY);
    case "volume":
      return volumeMesh(shape, b, centre, m, perPxX, perPxY);
    default:
      return null;
  }
}

// discMesh is a galaxy: a flat annulus of radius R, tilted by its inclination about its major axis
// and turned to its position angle. Its projection back onto the image is, by construction, the very
// ellipse the inclination was derived from — so at depth zero it lands back on its own pixels.
function discMesh(
  shape: Scene3DShape,
  centre: [number, number, number],
  m: Scene3DManifest,
  perPxX: number,
  perPxY: number,
): ShapeMesh | null {
  const R = shape.radius_pc ?? 0;
  if (!(R > 0)) return null;
  const inc = ((shape.inclination_deg ?? 0) * Math.PI) / 180;
  const pa = ((shape.position_angle_deg ?? 0) * Math.PI) / 180;
  const ci = Math.cos(inc);
  const si = Math.sin(inc);
  const cp = Math.cos(pa);
  const sp = Math.sin(pa);

  const verts: number[] = [];
  const idx: number[] = [];
  for (let ring = 0; ring <= DISC_RINGS; ring++) {
    const r = (ring / DISC_RINGS) * R;
    for (let seg = 0; seg <= DISC_SEGMENTS; seg++) {
      const a = (seg / DISC_SEGMENTS) * Math.PI * 2;
      // In the disc's own plane, then tilted, then rotated into the image frame.
      const u = r * Math.cos(a);
      const w = r * Math.sin(a);
      const x = u * cp - w * ci * sp;
      const y = u * sp + w * ci * cp;
      const z = w * si;
      pushVertex(verts, centre, [x, y, z], m, perPxX, perPxY);
    }
  }
  const stride = DISC_SEGMENTS + 1;
  for (let ring = 0; ring < DISC_RINGS; ring++) {
    for (let seg = 0; seg < DISC_SEGMENTS; seg++) {
      const a = ring * stride + seg;
      idx.push(a, a + 1, a + stride, a + 1, a + stride + 1, a + stride);
    }
  }
  return {
    vertices: new Float32Array(verts),
    indices: new Uint16Array(idx),
    slices: 0,
    kind: "disc",
  };
}

// shellMesh is an expanding shell — a planetary nebula or a supernova remnant. Only the surface is
// built, and it is drawn additively, so the far wall shows through the near one. That is not a
// shortcut: an optically thin shell really is seen through, and the bright rim in the image is the
// long path length around the limb, which this reproduces for free.
function shellMesh(
  shape: Scene3DShape,
  centre: [number, number, number],
  m: Scene3DManifest,
  perPxX: number,
  perPxY: number,
): ShapeMesh | null {
  const R = shape.radius_pc ?? 0;
  if (!(R > 0)) return null;
  const verts: number[] = [];
  const idx: number[] = [];
  const rings = SHELL_SEGMENTS / 2;
  for (let i = 0; i <= rings; i++) {
    const phi = (i / rings) * Math.PI;
    for (let j = 0; j <= SHELL_SEGMENTS; j++) {
      const th = (j / SHELL_SEGMENTS) * Math.PI * 2;
      pushVertex(
        verts,
        centre,
        [
          R * Math.sin(phi) * Math.cos(th),
          R * Math.sin(phi) * Math.sin(th),
          R * Math.cos(phi),
        ],
        m,
        perPxX,
        perPxY,
      );
    }
  }
  const stride = SHELL_SEGMENTS + 1;
  for (let i = 0; i < rings; i++) {
    for (let j = 0; j < SHELL_SEGMENTS; j++) {
      const a = i * stride + j;
      idx.push(a, a + 1, a + stride, a + 1, a + stride + 1, a + stride);
    }
  }
  return {
    vertices: new Float32Array(verts),
    indices: new Uint16Array(idx),
    slices: 0,
    kind: "shell",
  };
}

// volumeMesh is a diffuse nebula: a stack of flat quads at successive depths through the object.
//
// The shape lives entirely in the FRAGMENT shader, which is why each slice is four vertices and not
// a grid — it samples the backdrop, reads the brightness there, and decides how much of that
// emission belongs at this slice's depth. The bowl of a blister nebula and the hollow of a shell are
// both just terms in that decision, so the geometry never has to know about them.
function volumeMesh(
  shape: Scene3DShape,
  b: Scene3DBillboard,
  centre: [number, number, number],
  m: Scene3DManifest,
  perPxX: number,
  perPxY: number,
): ShapeMesh | null {
  const depthPc = shape.profile?.depth_pc ?? 0;
  if (!(depthPc > 0)) return null;
  // The slices span the object's own footprint, clamped to the image — sampling outside it would
  // smear the edge row of the texture across the sky.
  const halfW = Math.min(b.rx_px, m.image.width);
  const halfH = Math.min(b.ry_px, m.image.height);
  const cos = Math.cos(b.angle_rad);
  const sin = Math.sin(b.angle_rad);

  const verts: number[] = [];
  const idx: number[] = [];
  for (let k = 0; k < VOLUME_SLICES; k++) {
    // Centred on the object: half the slices in front of it, half behind.
    const t = k / Math.max(1, VOLUME_SLICES - 1) - 0.5;
    const dz = t * depthPc;
    const base = verts.length / SHAPE_STRIDE_FLOATS;
    for (const [su, sv] of [
      [-1, -1],
      [1, -1],
      [1, 1],
      [-1, 1],
    ] as const) {
      const dx = su * halfW * cos - sv * halfH * sin;
      const dy = su * halfW * sin + sv * halfH * cos;
      pushVertex(
        verts,
        centre,
        [dx * perPxX * b.dist_pc, dy * perPxY * b.dist_pc, dz],
        m,
        perPxX,
        perPxY,
        t,
      );
    }
    idx.push(base, base + 1, base + 2, base, base + 2, base + 3);
  }
  return {
    vertices: new Float32Array(verts),
    indices: new Uint16Array(idx),
    slices: VOLUME_SLICES,
    kind: "volume",
    footprint: {
      cx: b.x / m.image.width,
      cy: b.y / m.image.height,
      rx: Math.max(1e-6, halfW / m.image.width),
      ry: Math.max(1e-6, halfH / m.image.height),
    },
  };
}
