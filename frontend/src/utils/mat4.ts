// The frame-free half of the 3-D maths: 4×4 matrices, the vector helpers they are built from, and
// the projection every viewer's picker and label overlay shares.
//
// Extracted from scene3d.ts so the solar-system map and the star field run one implementation
// instead of two. Nothing here knows which way is up or what a scene unit means — that belongs to
// orbitcam.ts and to each viewer.

export type Mat4 = Float32Array;

export function identity(): Mat4 {
  // prettier-ignore
  return new Float32Array([1,0,0,0, 0,1,0,0, 0,0,1,0, 0,0,0,1]);
}

// perspective builds a projection from the half-field TANGENTS rather than a field-of-view angle, so
// a camera measured off a plate solution goes in untouched — converting to degrees and back only
// loses precision.
export function perspective(
  tanHalfW: number,
  tanHalfH: number,
  near: number,
  far: number,
): Mat4 {
  const m = new Float32Array(16);
  m[0] = 1 / tanHalfW;
  m[5] = 1 / tanHalfH;
  m[10] = -(far + near) / (far - near);
  m[11] = -1;
  m[14] = -(2 * far * near) / (far - near);
  return m;
}

export function multiply(a: Mat4, b: Mat4): Mat4 {
  const out = new Float32Array(16);
  for (let c = 0; c < 4; c++) {
    for (let r = 0; r < 4; r++) {
      let s = 0;
      for (let k = 0; k < 4; k++) s += a[k * 4 + r] * b[c * 4 + k];
      out[c * 4 + r] = s;
    }
  }
  return out;
}

export type Vec3 = [number, number, number];

export function dot(a: readonly number[], b: readonly number[]): number {
  return a[0] * b[0] + a[1] * b[1] + a[2] * b[2];
}

export function cross(a: readonly number[], b: readonly number[]): Vec3 {
  return [
    a[1] * b[2] - a[2] * b[1],
    a[2] * b[0] - a[0] * b[2],
    a[0] * b[1] - a[1] * b[0],
  ];
}

export function normalize(v: readonly number[]): Vec3 {
  const n = Math.hypot(v[0], v[1], v[2]);
  if (!(n > 0)) return [0, 0, 0];
  return [v[0] / n, v[1] / n, v[2] / n];
}

export interface Viewport {
  width: number;
  height: number;
}

// projectToScreen maps a scene position to canvas pixels, or null when it falls behind the camera.
// Shared by the pickers and by every label the overlays draw, so a click can never land somewhere
// the thing it selected is not drawn.
export function projectToScreen(
  pos: readonly [number, number, number],
  viewProj: Mat4,
  vp: Viewport,
): [number, number] | null {
  const x =
    viewProj[0] * pos[0] +
    viewProj[4] * pos[1] +
    viewProj[8] * pos[2] +
    viewProj[12];
  const y =
    viewProj[1] * pos[0] +
    viewProj[5] * pos[1] +
    viewProj[9] * pos[2] +
    viewProj[13];
  const w =
    viewProj[3] * pos[0] +
    viewProj[7] * pos[1] +
    viewProj[11] * pos[2] +
    viewProj[15];
  if (!(w > 1e-9)) return null;
  return [((x / w) * 0.5 + 0.5) * vp.width, (0.5 - (y / w) * 0.5) * vp.height];
}
