import { computed, onBeforeUnmount, ref, watch, type Ref } from "vue";
import type { Scene3DManifest, SkyFrame } from "@/types";
import {
  DEPTH_ESTIMATED,
  DEPTH_MEASURED,
  OFF_ABSMAG,
  OFF_DIR,
  OFF_DIST,
  OFF_FLAGS,
  OFF_MAG,
  OFF_RGB,
  PITCH_LIMIT,
  RECORD_SIZE,
  SHAPE_STRIDE_FLOATS,
  Z_REF,
  Z_SPAN,
  applyZoom,
  billboardQuad,
  cameraPhysical,
  decadeRings,
  defaultOrbit,
  fitPerspective,
  motionEndpoint,
  multiply,
  panOrbit,
  pickNearest,
  radialSign,
  readStar,
  scenePosition,
  UNITS_PER_PC,
  PC_PER_SCENE_UNIT,
  eyePosition,
  sortBillboardsFarFirst,
  tessellateShape,
  viewMatrix,
  warpZ,
  zoomExponent,
  type Mat4,
  type Orbit,
  type Scene3DPoints,
  type ShapeMesh,
  type StarRecord,
} from "@/utils/scene3d";
import { dragToLook, orbitPerPixel } from "@/utils/scene3dfly";
import { matchesCamera, newBasis } from "@/utils/skyframe";
import {
  buildGalaxyLines,
  buildGalaxyMesh,
  cameraPhysicalLinear,
  galacticToScene,
  galaxyOrbit,
  galaxyTanScale,
} from "@/utils/scene3dgalaxy";

// WebGL2 renderer for the 3D field map. The scene is point sprites for the stars, a mesh per
// catalogued object, and lines for the field cone and the motion vectors — which is why this is a
// few hundred lines of hand-written GL rather than a 3D engine: there is no scene graph, no material
// system and no mesh loading to be had use of.
//
// It does no astronomy. The engine shipped a unit direction, a distance and a velocity per star, and
// a shape descriptor per object; everything here is plumbing and one line of physics.

// --- shaders ---------------------------------------------------------------------------------------

// The depth warp, shared verbatim by every shader so a star and the object behind it can never end
// up on inconsistent planes. Mirrors warpZ() in utils/scene3d.ts, which the picker uses.
const WARP_GLSL = `
float warpZ(float distPc) {
  if (!(distPc > 0.0)) return uZRef;
  float t = clamp((log(distPc) - uLogNear) / max(1e-6, uLogFar - uLogNear), 0.0, 1.0);
  return uZRef + t * uDepth * uZSpan;
}
vec3 scenePos(vec3 dir, float distPc) {
  // uLinear swaps the whole scene into true parsecs for the galaxy view. The warp above is
  // calibrated to THIS field's own distances, so it cannot carry anything eight kiloparsecs away;
  // linear space can, and at the origin the two agree pixel for pixel because both put the star on
  // the same ray. Uniform control flow, so the branch is free.
  if (uLinear > 0.5) return dir * (distPc * uUnitPerPc);
  float z = warpZ(distPc);
  return dir * (z / max(1e-6, dir.z));
}`;

const STAR_VERT = `#version 300 es
precision highp float;
layout(location=0) in vec3 aDir;
layout(location=1) in float aDist;
layout(location=2) in vec3 aColor;
layout(location=3) in int aAbsMag;
layout(location=4) in uint aFlags;
layout(location=5) in uint aMag;

uniform mat4 uViewProj;
uniform float uDepth, uLogNear, uLogFar, uZRef, uZSpan, uSizeScale;
uniform float uLinear, uUnitPerPc;
uniform vec3 uCamPhys;
uniform uint uDepthMask;

out vec3 vColor;
out float vAlpha;
${WARP_GLSL}

void main() {
  uint src = aFlags & 3u;
  if ((uDepthMask & (1u << src)) == 0u) {
    // Culling in the vertex shader: a zero-size point outside the clip volume costs one vertex and
    // no fragments, which is cheaper than re-uploading a filtered buffer every time a box is ticked.
    gl_Position = vec4(2.0, 2.0, 2.0, 1.0);
    gl_PointSize = 0.0;
    return;
  }
  gl_Position = uViewProj * vec4(scenePos(aDir, aDist), 1.0);

  // A star is a light source, so how bright it looks follows the inverse-square law from wherever the
  // CAMERA actually is — not from Earth. That is the whole of the physics here:
  //
  //   m = M + 5·log10(d / 10 pc)
  //
  // computed in real parsecs, never in warped scene units. Flying toward a star therefore brightens
  // and swells it exactly as approaching a real one would, and a blue supergiant still reads as
  // luminous from far off while a red dwarf beside it stays faint.
  //
  // At depth 0 the camera sits at the origin, which is Earth, so d is the star's own distance and m
  // is its Earth magnitude — the photograph, out of the same expression, with no special case.
  float m;
  if (aAbsMag == -128) {
    m = aMag == 255u ? 13.0 : float(aMag) / 8.0 - 5.0;   // no luminosity known: fall back to the frame
  } else {
    float absMag = float(aAbsMag) / 4.0;
    float d = max(0.02, distance(aDir * aDist, uCamPhys));
    m = absMag + 5.0 * (log(d / 10.0) / log(10.0));
  }

  float size = uSizeScale * pow(2.0, (11.0 - m) * 0.26);
  gl_PointSize = clamp(size, 1.0, 96.0);

  vColor = aColor;
  // An estimated distance is drawn a little softer than a measured one — visible, but never passing
  // itself off as the same kind of fact.
  vAlpha = src == ${DEPTH_ESTIMATED}u ? 0.7 : 1.0;
}`;

const STAR_FRAG = `#version 300 es
precision mediump float;
in vec3 vColor;
in float vAlpha;
out vec4 fragColor;

void main() {
  vec2 d = gl_PointCoord * 2.0 - 1.0;
  float r2 = dot(d, d);
  if (r2 > 1.0) discard;
  // A bright core inside a soft halo. A flat disc reads as a dot on a screen; a star does not.
  float core = exp(-r2 * 7.0);
  float halo = exp(-r2 * 1.8) * 0.30;
  fragColor = vec4(vColor * (core + halo) * vAlpha, 1.0);
}`;

const QUAD_VERT = `#version 300 es
precision highp float;
layout(location=0) in vec3 aPos;
layout(location=1) in vec2 aUV;
layout(location=2) in vec2 aLocal;
uniform mat4 uViewProj;
out vec2 vUV;
out vec2 vLocal;
void main() {
  gl_Position = uViewProj * vec4(aPos, 1.0);
  vUV = aUV;
  vLocal = aLocal;
}`;

const QUAD_FRAG = `#version 300 es
precision mediump float;
in vec2 vUV;
in vec2 vLocal;
uniform sampler2D uTex;
uniform float uOpacity;
out vec4 fragColor;

void main() {
  // Outside the texture there is nothing to draw. Without this the sampler clamps to the edge and
  // smears the outermost row of pixels across the sky — which is exactly what a footprint larger
  // than its own image used to do.
  if (vUV.x < 0.0 || vUV.x > 1.0 || vUV.y < 0.0 || vUV.y > 1.0) discard;
  float m = 1.0 - smoothstep(0.88, 1.0, length(vLocal));
  if (m <= 0.0) discard;
  fragColor = vec4(texture(uTex, vUV).rgb * m * uOpacity, 1.0);
}`;

// The mesh shader draws a galaxy's disc or a nebula's shell: real geometry, textured from the run's
// own image, warped per-vertex by the very same function the stars use.
const MESH_VERT = `#version 300 es
precision highp float;
layout(location=0) in vec3 aDir;
layout(location=1) in float aDist;
layout(location=2) in vec2 aUV;
layout(location=3) in float aSliceT;

uniform mat4 uViewProj;
uniform float uDepth, uLogNear, uLogFar, uZRef, uZSpan;
uniform float uLinear, uUnitPerPc;
out vec2 vUV;
out float vSliceT;
${WARP_GLSL}

void main() {
  gl_Position = uViewProj * vec4(scenePos(aDir, aDist), 1.0);
  vUV = aUV;
  vSliceT = aSliceT;
}`;

const MESH_FRAG = `#version 300 es
precision mediump float;
in vec2 vUV;
in float vSliceT;

uniform sampler2D uTex;
uniform float uOpacity;
// uSlices > 0 selects the volume path; the rest describe its profile.
uniform float uSlices, uExponent, uBowl, uHollow;
uniform vec4 uFootprint;  // texture-space centre.xy and radii.zw
out vec4 fragColor;

void main() {
  if (vUV.x < 0.0 || vUV.x > 1.0 || vUV.y < 0.0 || vUV.y > 1.0) discard;
  vec3 c = texture(uTex, vUV).rgb;

  if (uSlices <= 0.0) {          // a solid surface: a disc or a shell
    fragColor = vec4(c * uOpacity, 1.0);
    return;
  }

  // A volume. The image records the emission integrated along each line of sight, so the only way
  // back to a depth is an assumption — that the structure is about as deep as it is wide. Under it,
  // a pixel of brightness I spreads its emission over a depth proportional to I^exponent, and this
  // slice takes the share that falls at its own depth.
  float I = dot(c, vec3(0.2126, 0.7152, 0.0722));
  if (I <= 0.003) discard;
  float halfDepth = max(0.02, 0.5 * pow(I, uExponent));

  // How far this fragment sits from the object's centre, in units of its own footprint.
  float r = length((vUV - uFootprint.xy) / max(vec2(1e-6), uFootprint.zw));

  // A blister nebula is not a cloud: it is the excavated near face of one, so its emission bows
  // toward the observer in the middle and falls back at the rim.
  float centre = -uBowl * halfDepth * (1.0 - clamp(r * r, 0.0, 1.0));
  float d = (vSliceT - centre) / halfDepth;
  float w = exp(-d * d);
  // A shell or torus is empty in the middle along the line of sight, which is what makes its rim
  // bright in the first place.
  if (uHollow > 0.0) w *= 1.0 - uHollow * exp(-4.0 * r * r);

  // Normalise so the slices sum back to the image's own brightness: the Gaussian's integral is
  // halfDepth·√π, and the slices sample it every 1/uSlices.
  float alpha = I * w / (halfDepth * 1.7724539 * uSlices);
  fragColor = vec4(c * alpha * uOpacity * 6.0, 1.0);
}`;

const LINE_VERT = `#version 300 es
precision highp float;
layout(location=0) in vec3 aPos;
layout(location=1) in vec3 aColor;
uniform mat4 uViewProj;
out vec3 vColor;
void main() {
  gl_Position = uViewProj * vec4(aPos, 1.0);
  vColor = aColor;
}`;

const LINE_FRAG = `#version 300 es
precision mediump float;
in vec3 vColor;
uniform float uAlpha;
out vec4 fragColor;
void main() { fragColor = vec4(vColor * uAlpha, 1.0); }`;

// --- GL helpers ------------------------------------------------------------------------------------

function compile(
  gl: WebGL2RenderingContext,
  type: number,
  src: string,
): WebGLShader {
  const sh = gl.createShader(type)!;
  gl.shaderSource(sh, src);
  gl.compileShader(sh);
  if (!gl.getShaderParameter(sh, gl.COMPILE_STATUS)) {
    const log = gl.getShaderInfoLog(sh);
    gl.deleteShader(sh);
    throw new Error(`shader: ${log}`);
  }
  return sh;
}

function link(
  gl: WebGL2RenderingContext,
  vs: string,
  fs: string,
): WebGLProgram {
  const p = gl.createProgram()!;
  const v = compile(gl, gl.VERTEX_SHADER, vs);
  const f = compile(gl, gl.FRAGMENT_SHADER, fs);
  gl.attachShader(p, v);
  gl.attachShader(p, f);
  gl.linkProgram(p);
  gl.deleteShader(v);
  gl.deleteShader(f);
  if (!gl.getProgramParameter(p, gl.LINK_STATUS)) {
    const log = gl.getProgramInfoLog(p);
    gl.deleteProgram(p);
    throw new Error(`program: ${log}`);
  }
  return p;
}

// uniforms caches every location once — getUniformLocation on every frame is a needless round trip
// into the driver.
function uniforms(
  gl: WebGL2RenderingContext,
  p: WebGLProgram,
  names: string[],
) {
  const out: Record<string, WebGLUniformLocation | null> = {};
  for (const n of names) out[n] = gl.getUniformLocation(p, n);
  return out;
}

// --- the composable --------------------------------------------------------------------------------

export interface StarField3DInputs {
  manifest: Ref<Scene3DManifest | null>;
  points: Ref<Scene3DPoints | null>;
  backdropUrl: Ref<string>;
  depth: Ref<number>;
  showStars: Ref<boolean>;
  showObjects: Ref<boolean>;
  showFrustum: Ref<boolean>;
  showEstimated: Ref<boolean>;
  showMotion: Ref<boolean>;
  motionYears: Ref<number>;
  starSize: Ref<number>;
  // The galaxy view: off by default, and a slider that flies from the field out to the whole Galaxy.
  // `frame` is the image's sky anchors — without them there is no way to know the field's roll, and
  // the galaxy must not be drawn at all rather than drawn at a guessed orientation.
  showGalaxy?: Ref<boolean>;
  galaxyZoom?: Ref<number>;
  frame?: Ref<SkyFrame | null>;
}

interface MeshDraw {
  mesh: ShapeMesh;
  vao: WebGLVertexArrayObject | null;
  vbo: WebGLBuffer | null;
  ibo: WebGLBuffer | null;
  exponent: number;
  bowl: number;
  hollow: number;
}

export function useStarField3D(
  canvas: Ref<HTMLCanvasElement | null>,
  input: StarField3DInputs,
) {
  const supported = ref(true);
  const error = ref("");
  const orbit = ref<Orbit>(defaultOrbit());
  const selected = ref<StarRecord | null>(null);
  const hovered = ref<StarRecord | null>(null);
  const hoverAt = ref({ x: 0, y: 0 });

  let gl: WebGL2RenderingContext | null = null;
  let starProg: WebGLProgram | null = null;
  let quadProg: WebGLProgram | null = null;
  let meshProg: WebGLProgram | null = null;
  let lineProg: WebGLProgram | null = null;
  let starU: Record<string, WebGLUniformLocation | null> = {};
  let quadU: Record<string, WebGLUniformLocation | null> = {};
  let meshU: Record<string, WebGLUniformLocation | null> = {};
  let lineU: Record<string, WebGLUniformLocation | null> = {};

  let starVAO: WebGLVertexArrayObject | null = null;
  let starBuf: WebGLBuffer | null = null;
  let starCount = 0;
  let quadVAO: WebGLVertexArrayObject | null = null;
  let quadBuf: WebGLBuffer | null = null;
  let quadIdx: WebGLBuffer | null = null;
  let quadDraws: { offset: number; count: number }[] = [];
  let lineVAO: WebGLVertexArrayObject | null = null;
  let lineBuf: WebGLBuffer | null = null;
  let lineCount = 0;
  let motionVAO: WebGLVertexArrayObject | null = null;
  let motionBuf: WebGLBuffer | null = null;
  let motionCount = 0;
  let meshes: MeshDraw[] = [];
  let tex: WebGLTexture | null = null;
  let texReady = false;

  let raf = 0;
  let dirty = true;
  let observer: ResizeObserver | null = null;
  let viewportAspect = 1;

  const requestDraw = () => {
    dirty = true;
  };

  // --- setup -------------------------------------------------------------------------------------

  function init(): boolean {
    const el = canvas.value;
    if (!el) return false;
    const ctx = el.getContext("webgl2", {
      antialias: true,
      alpha: false,
      preserveDrawingBuffer: false,
    });
    if (!ctx) {
      supported.value = false;
      return false;
    }
    gl = ctx;
    try {
      starProg = link(gl, STAR_VERT, STAR_FRAG);
      quadProg = link(gl, QUAD_VERT, QUAD_FRAG);
      meshProg = link(gl, MESH_VERT, MESH_FRAG);
      lineProg = link(gl, LINE_VERT, LINE_FRAG);
    } catch (e) {
      error.value = (e as Error).message;
      supported.value = false;
      return false;
    }
    starU = uniforms(gl, starProg, [
      "uViewProj",
      "uLinear",
      "uUnitPerPc",
      "uDepth",
      "uLogNear",
      "uLogFar",
      "uZRef",
      "uZSpan",
      "uSizeScale",
      "uDepthMask",
      "uCamPhys",
    ]);
    quadU = uniforms(gl, quadProg, ["uViewProj", "uTex", "uOpacity"]);
    meshU = uniforms(gl, meshProg, [
      "uViewProj",
      "uLinear",
      "uUnitPerPc",
      "uDepth",
      "uLogNear",
      "uLogFar",
      "uZRef",
      "uZSpan",
      "uTex",
      "uOpacity",
      "uSlices",
      "uExponent",
      "uBowl",
      "uHollow",
      "uFootprint",
    ]);
    lineU = uniforms(gl, lineProg, ["uViewProj", "uAlpha"]);

    gl.disable(gl.DEPTH_TEST);
    gl.enable(gl.BLEND);
    // Additive: stars and nebulosity EMIT light, they do not occlude one another. It also makes the
    // draw order irrelevant, so nothing has to be depth-sorted per frame.
    gl.blendFunc(gl.ONE, gl.ONE);
    gl.clearColor(0.016, 0.02, 0.035, 1);

    observer = new ResizeObserver(requestDraw);
    observer.observe(el);
    return true;
  }

  // uploadStars hands the engine's buffer to the GPU untouched — the record layout was chosen so
  // every attribute can be read straight out of it, with no unpacking pass in JavaScript.
  function uploadStars(points: Scene3DPoints | null) {
    if (!gl) return;
    if (starVAO) gl.deleteVertexArray(starVAO);
    if (starBuf) gl.deleteBuffer(starBuf);
    starVAO = null;
    starBuf = null;
    starCount = 0;
    if (!points || points.count === 0) return;

    starVAO = gl.createVertexArray();
    starBuf = gl.createBuffer();
    gl.bindVertexArray(starVAO);
    gl.bindBuffer(gl.ARRAY_BUFFER, starBuf);
    gl.bufferData(
      gl.ARRAY_BUFFER,
      new Uint8Array(
        points.buffer,
        points.byteOffset,
        points.count * RECORD_SIZE,
      ),
      gl.STATIC_DRAW,
    );
    const S = RECORD_SIZE;
    gl.enableVertexAttribArray(0);
    gl.vertexAttribPointer(0, 3, gl.FLOAT, false, S, OFF_DIR);
    gl.enableVertexAttribArray(1);
    gl.vertexAttribPointer(1, 1, gl.FLOAT, false, S, OFF_DIST);
    gl.enableVertexAttribArray(2);
    gl.vertexAttribPointer(2, 3, gl.UNSIGNED_BYTE, true, S, OFF_RGB);
    // The three packed fields are integers, not normalised floats: a magnitude byte scaled to [0,1]
    // would have to be un-scaled in the shader, and the flags are a bitmask that must stay exact.
    gl.enableVertexAttribArray(3);
    gl.vertexAttribIPointer(3, 1, gl.BYTE, S, OFF_ABSMAG);
    gl.enableVertexAttribArray(4);
    gl.vertexAttribIPointer(4, 1, gl.UNSIGNED_BYTE, S, OFF_FLAGS);
    gl.enableVertexAttribArray(5);
    gl.vertexAttribIPointer(5, 1, gl.UNSIGNED_BYTE, S, OFF_MAG);
    gl.bindVertexArray(null);
    starCount = points.count;
    requestDraw();
  }

  function loadBackdrop(url: string) {
    if (!gl || !url) {
      texReady = false;
      return;
    }
    const img = new Image();
    img.crossOrigin = "anonymous";
    img.onload = () => {
      if (!gl) return;
      if (!tex) tex = gl.createTexture();
      gl.bindTexture(gl.TEXTURE_2D, tex);
      gl.texImage2D(gl.TEXTURE_2D, 0, gl.RGBA, gl.RGBA, gl.UNSIGNED_BYTE, img);
      gl.generateMipmap(gl.TEXTURE_2D);
      gl.texParameteri(
        gl.TEXTURE_2D,
        gl.TEXTURE_MIN_FILTER,
        gl.LINEAR_MIPMAP_LINEAR,
      );
      gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_S, gl.CLAMP_TO_EDGE);
      gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_T, gl.CLAMP_TO_EDGE);
      texReady = true;
      requestDraw();
    };
    // A missing backdrop costs the objects their texture, not the scene its stars.
    img.onerror = () => {
      texReady = false;
    };
    img.src = url;
  }

  // buildMeshes tessellates every object that has a real shape. Done once per scene rather than per
  // frame: the geometry is in (dir, distPc) space, so the depth slider warps it in the shader and
  // nothing here has to be rebuilt when it moves.
  function buildMeshes() {
    if (!gl) return;
    for (const d of meshes) {
      if (d.vao) gl.deleteVertexArray(d.vao);
      if (d.vbo) gl.deleteBuffer(d.vbo);
      if (d.ibo) gl.deleteBuffer(d.ibo);
    }
    meshes = [];
    const m = input.manifest.value;
    if (!m?.billboards?.length) return;

    for (const b of sortBillboardsFarFirst(m.billboards)) {
      const mesh = tessellateShape(b, m);
      if (!mesh) continue;
      const vao = gl.createVertexArray();
      const vbo = gl.createBuffer();
      const ibo = gl.createBuffer();
      gl.bindVertexArray(vao);
      gl.bindBuffer(gl.ARRAY_BUFFER, vbo);
      gl.bufferData(gl.ARRAY_BUFFER, mesh.vertices, gl.STATIC_DRAW);
      const stride = SHAPE_STRIDE_FLOATS * 4;
      gl.enableVertexAttribArray(0);
      gl.vertexAttribPointer(0, 3, gl.FLOAT, false, stride, 0);
      gl.enableVertexAttribArray(1);
      gl.vertexAttribPointer(1, 1, gl.FLOAT, false, stride, 12);
      gl.enableVertexAttribArray(2);
      gl.vertexAttribPointer(2, 2, gl.FLOAT, false, stride, 16);
      gl.enableVertexAttribArray(3);
      gl.vertexAttribPointer(3, 1, gl.FLOAT, false, stride, 24);
      gl.bindBuffer(gl.ELEMENT_ARRAY_BUFFER, ibo);
      gl.bufferData(gl.ELEMENT_ARRAY_BUFFER, mesh.indices, gl.STATIC_DRAW);
      gl.bindVertexArray(null);
      meshes.push({
        mesh,
        vao,
        vbo,
        ibo,
        exponent: b.shape?.profile?.exponent ?? 0.5,
        bowl: b.shape?.profile?.bowl ?? 0,
        hollow: b.shape?.profile?.hollow ?? 0,
      });
    }
    requestDraw();
  }

  // rebuildQuads places the objects that have NO shape as flat cards at the current depth. Rebuilt
  // per frame while the slider moves, which is free: a field holds a handful of objects.
  function rebuildQuads() {
    if (!gl) return;
    const m = input.manifest.value;
    quadDraws = [];
    if (!m?.billboards?.length) return;

    const verts: number[] = [];
    const idx: number[] = [];
    for (const b of sortBillboardsFarFirst(m.billboards)) {
      if (b.shape && b.shape.kind !== "plane") continue; // it has a real mesh
      const q = billboardQuad(
        b,
        m,
        input.depth.value,
        galaxyOn() ? UNITS_PER_PC : undefined,
      );
      if (!q) continue;
      const base = verts.length / 7;
      const local: [number, number][] = [
        [-1, -1],
        [1, -1],
        [1, 1],
        [-1, 1],
      ];
      q.corners.forEach((c, i) => {
        verts.push(
          c[0],
          c[1],
          c[2],
          q.uvs[i][0],
          q.uvs[i][1],
          local[i][0],
          local[i][1],
        );
      });
      const start = idx.length;
      idx.push(base, base + 1, base + 2, base, base + 2, base + 3);
      quadDraws.push({ offset: start * 2, count: 6 });
    }
    if (!idx.length) return;

    if (!quadVAO) {
      quadVAO = gl.createVertexArray();
      quadBuf = gl.createBuffer();
      quadIdx = gl.createBuffer();
      gl.bindVertexArray(quadVAO);
      gl.bindBuffer(gl.ARRAY_BUFFER, quadBuf);
      const stride = 7 * 4;
      gl.enableVertexAttribArray(0);
      gl.vertexAttribPointer(0, 3, gl.FLOAT, false, stride, 0);
      gl.enableVertexAttribArray(1);
      gl.vertexAttribPointer(1, 2, gl.FLOAT, false, stride, 12);
      gl.enableVertexAttribArray(2);
      gl.vertexAttribPointer(2, 2, gl.FLOAT, false, stride, 20);
      gl.bindBuffer(gl.ELEMENT_ARRAY_BUFFER, quadIdx);
      gl.bindVertexArray(null);
    }
    gl.bindVertexArray(quadVAO);
    gl.bindBuffer(gl.ARRAY_BUFFER, quadBuf);
    gl.bufferData(gl.ARRAY_BUFFER, new Float32Array(verts), gl.DYNAMIC_DRAW);
    gl.bindBuffer(gl.ELEMENT_ARRAY_BUFFER, quadIdx);
    gl.bufferData(
      gl.ELEMENT_ARRAY_BUFFER,
      new Uint16Array(idx),
      gl.DYNAMIC_DRAW,
    );
    gl.bindVertexArray(null);
  }

  // rebuildLines draws the field of view itself: four rays out from Earth through the frame's
  // corners, and the frame's own rectangle at each round distance inside the field. Those rectangles
  // are what turn an abstract cloud into a scale you can read.
  function rebuildLines() {
    if (!gl) return;
    const m = input.manifest.value;
    lineCount = 0;
    if (!m) return;

    const tw = m.camera.tan_half_w;
    const th = m.camera.tan_half_h;
    const corner = (i: number): [number, number] =>
      [
        [-tw, -th],
        [tw, -th],
        [tw, th],
        [-tw, th],
      ][i] as [number, number];

    const v: number[] = [];
    const col: [number, number, number] = [0.16, 0.24, 0.36];
    const push = (a: [number, number, number], b: [number, number, number]) => {
      v.push(a[0], a[1], a[2], ...col, b[0], b[1], b[2], ...col);
    };
    const zFar = Z_REF + input.depth.value * Z_SPAN;
    for (let i = 0; i < 4; i++) {
      const [u, w] = corner(i);
      push([0, 0, 0], [u * zFar, w * zFar, zFar]);
    }
    const frame = (z: number) => {
      for (let i = 0; i < 4; i++) {
        const a = corner(i);
        const b = corner((i + 1) % 4);
        push([a[0] * z, a[1] * z, z], [b[0] * z, b[1] * z, z]);
      }
    };
    frame(Z_REF);
    frame(zFar);
    for (const pc of decadeRings(m.depth.near_pc, m.depth.far_pc)) {
      frame(warpZ(pc, m.depth.near_pc, m.depth.far_pc, input.depth.value));
    }

    lineCount = uploadLines(lineVAO, lineBuf, v, (vao, buf) => {
      lineVAO = vao;
      lineBuf = buf;
    });
  }

  // rebuildMotion draws where each star will be after the chosen time. The endpoint is displaced in
  // REAL space and then warped, so the arrow is squeezed along the line of sight exactly as the
  // field around it is — a vector drawn straight through warped space would lie about its direction.
  function rebuildMotion() {
    if (!gl) return;
    const m = input.manifest.value;
    const pts = input.points.value;
    motionCount = 0;
    if (!m || !pts) return;

    const v: number[] = [];
    const mask = depthMask();
    for (let i = 0; i < pts.count; i++) {
      const s = readStar(pts, i);
      if (!s.velocity || (mask & (1 << s.depth)) === 0) continue;
      const from = scenePosition(
        s.dir,
        s.distPc,
        m.depth.near_pc,
        m.depth.far_pc,
        input.depth.value,
      );
      const to = motionEndpoint(
        s,
        m,
        input.depth.value,
        input.motionYears.value,
      );
      if (!to) continue;
      // Red receding, blue approaching — the sign of the motion along the star's own line of sight.
      const sign = radialSign(s);
      const c: [number, number, number] =
        sign > 0 ? [0.85, 0.28, 0.2] : [0.3, 0.55, 1.0];
      // The tail fades in, so the arrow reads as motion rather than as a stick through the star.
      v.push(from[0], from[1], from[2], c[0] * 0.15, c[1] * 0.15, c[2] * 0.15);
      v.push(to[0], to[1], to[2], ...c);
    }
    motionCount = uploadLines(motionVAO, motionBuf, v, (vao, buf) => {
      motionVAO = vao;
      motionBuf = buf;
    });
  }

  // uploadLines is the shared position+colour line buffer setup. Returns the vertex count.
  function uploadLines(
    vao: WebGLVertexArrayObject | null,
    buf: WebGLBuffer | null,
    data: number[],
    keep: (vao: WebGLVertexArrayObject | null, buf: WebGLBuffer | null) => void,
  ): number {
    if (!gl || !data.length) return 0;
    if (!vao) {
      vao = gl.createVertexArray();
      buf = gl.createBuffer();
      gl.bindVertexArray(vao);
      gl.bindBuffer(gl.ARRAY_BUFFER, buf);
      gl.enableVertexAttribArray(0);
      gl.vertexAttribPointer(0, 3, gl.FLOAT, false, 24, 0);
      gl.enableVertexAttribArray(1);
      gl.vertexAttribPointer(1, 3, gl.FLOAT, false, 24, 12);
      gl.bindVertexArray(null);
      keep(vao, buf);
    }
    gl.bindVertexArray(vao);
    gl.bindBuffer(gl.ARRAY_BUFFER, buf);
    gl.bufferData(gl.ARRAY_BUFFER, new Float32Array(data), gl.DYNAMIC_DRAW);
    gl.bindVertexArray(null);
    return data.length / 6;
  }

  // --- drawing -----------------------------------------------------------------------------------

  // --- galaxy view -------------------------------------------------------------------------------

  // The galaxy is only drawn when it can be drawn CORRECTLY: it needs the image's sky anchors to
  // know the field's roll, and a frame that disagrees with the manifest's camera means the two files
  // came from different passes. Guessing an orientation would produce a picture with no visible
  // defect and no correct part, which is the one outcome worth refusing.
  const galaxyBasis = computed(() => {
    const f = input.frame?.value;
    const m = input.manifest.value;
    if (!f || !m) return null;
    const b = newBasis(f);
    if (!b) return null;
    return matchesCamera(b, m.camera) ? b : null;
  });

  function galaxyOn(): boolean {
    return !!input.showGalaxy?.value && !!galaxyBasis.value;
  }

  function galaxyT(): number {
    return galaxyOn() ? (input.galaxyZoom?.value ?? 0) : 0;
  }

  let galaxyVAO: WebGLVertexArrayObject | null = null;
  let galaxyPos: WebGLBuffer | null = null;
  let galaxyCol: WebGLBuffer | null = null;
  let galaxyIdx: WebGLBuffer | null = null;
  let galaxyCount = 0;
  let galaxyLineVAO: WebGLVertexArrayObject | null = null;
  let galaxyLinePos: WebGLBuffer | null = null;
  let galaxyLineCol: WebGLBuffer | null = null;
  let galaxyLineCount = 0;
  let galaxyBuiltFor: unknown = null;

  // Built once per scene, never per frame: the transform, the scale and the per-vertex brightness
  // are all baked in, so drawing it is one bind and one call.
  function buildGalaxy() {
    const b = galaxyBasis.value;
    if (!gl || !b || galaxyBuiltFor === b) return;
    galaxyBuiltFor = b;
    const m = galacticToScene(b);
    const mesh = buildGalaxyMesh(m);
    const lines = buildGalaxyLines(m);

    galaxyVAO = galaxyVAO ?? gl.createVertexArray();
    galaxyPos = galaxyPos ?? gl.createBuffer();
    galaxyCol = galaxyCol ?? gl.createBuffer();
    galaxyIdx = galaxyIdx ?? gl.createBuffer();
    gl.bindVertexArray(galaxyVAO);
    gl.bindBuffer(gl.ARRAY_BUFFER, galaxyPos);
    gl.bufferData(gl.ARRAY_BUFFER, mesh.positions, gl.STATIC_DRAW);
    gl.enableVertexAttribArray(0);
    gl.vertexAttribPointer(0, 3, gl.FLOAT, false, 0, 0);
    gl.bindBuffer(gl.ARRAY_BUFFER, galaxyCol);
    gl.bufferData(gl.ARRAY_BUFFER, mesh.colors, gl.STATIC_DRAW);
    gl.enableVertexAttribArray(1);
    gl.vertexAttribPointer(1, 3, gl.FLOAT, false, 0, 0);
    gl.bindBuffer(gl.ELEMENT_ARRAY_BUFFER, galaxyIdx);
    gl.bufferData(gl.ELEMENT_ARRAY_BUFFER, mesh.indices, gl.STATIC_DRAW);
    galaxyCount = mesh.indices.length;

    galaxyLineVAO = galaxyLineVAO ?? gl.createVertexArray();
    galaxyLinePos = galaxyLinePos ?? gl.createBuffer();
    galaxyLineCol = galaxyLineCol ?? gl.createBuffer();
    gl.bindVertexArray(galaxyLineVAO);
    gl.bindBuffer(gl.ARRAY_BUFFER, galaxyLinePos);
    gl.bufferData(gl.ARRAY_BUFFER, lines.positions, gl.STATIC_DRAW);
    gl.enableVertexAttribArray(0);
    gl.vertexAttribPointer(0, 3, gl.FLOAT, false, 0, 0);
    gl.bindBuffer(gl.ARRAY_BUFFER, galaxyLineCol);
    gl.bufferData(gl.ARRAY_BUFFER, lines.colors, gl.STATIC_DRAW);
    gl.enableVertexAttribArray(1);
    gl.vertexAttribPointer(1, 3, gl.FLOAT, false, 0, 0);
    galaxyLineCount = lines.positions.length / 3;
    gl.bindVertexArray(null);
  }

  function viewProj(): Mat4 | null {
    const m = input.manifest.value;
    if (!m) return null;
    if (galaxyOn()) {
      // The lens opens as the camera pulls back; the clip planes follow it, since the scene spans
      // parsecs to tens of kiloparsecs. Depth testing is off, so only w > 0 and z in [-1,1] matter.
      const d = orbit.value.distance;
      const proj = fitPerspective(
        m,
        viewportAspect,
        d * 1e-4,
        Math.max((d + 60) * 2, 400),
        galaxyTanScale(galaxyT(), m.camera.tan_half_h),
      );
      return multiply(proj, viewMatrix(orbit.value));
    }
    return multiply(fitPerspective(m, viewportAspect), viewMatrix(orbit.value));
  }

  function resize(): boolean {
    const el = canvas.value;
    if (!el || !gl) return false;
    const dpr = Math.min(2, window.devicePixelRatio || 1);
    const w = Math.max(1, Math.round(el.clientWidth * dpr));
    const h = Math.max(1, Math.round(el.clientHeight * dpr));
    if (el.width !== w || el.height !== h) {
      el.width = w;
      el.height = h;
    }
    gl.viewport(0, 0, w, h);
    // The projection is fitted to THIS aspect. gl.viewport stretches clip space across the canvas
    // whatever shape it is, so a field built from the image's own aspect would be squashed by
    // however much the two differ — which on a wide panel is nearly threefold.
    viewportAspect = w / h;
    return true;
  }

  // depthMask is the set of provenances currently drawn. Unknown-distance stars are never in the
  // buffer at all — the engine leaves out what it could not place, rather than parking it somewhere
  // convenient.
  function depthMask(): number {
    return (
      (1 << DEPTH_MEASURED) |
      (input.showEstimated.value ? 1 << DEPTH_ESTIMATED : 0)
    );
  }

  function setWarpUniforms(
    u: Record<string, WebGLUniformLocation | null>,
    m: Scene3DManifest,
  ) {
    if (!gl) return;
    gl.uniform1f(u.uLinear!, galaxyOn() ? 1 : 0);
    gl.uniform1f(u.uUnitPerPc!, UNITS_PER_PC);
    gl.uniform1f(u.uDepth!, input.depth.value);
    gl.uniform1f(u.uLogNear!, Math.log(Math.max(1e-6, m.depth.near_pc)));
    gl.uniform1f(u.uLogFar!, Math.log(Math.max(1e-6, m.depth.far_pc)));
    gl.uniform1f(u.uZRef!, Z_REF);
    gl.uniform1f(u.uZSpan!, Z_SPAN);
  }

  function draw() {
    if (!gl || !resize()) return;
    const m = input.manifest.value;
    gl.clear(gl.COLOR_BUFFER_BIT);
    const vp = viewProj();
    if (!m || !vp) return;

    // The galaxy goes first, straight after the clear. Blending is additive and there is no depth
    // buffer, so "behind" is a matter of emitting little light rather than of ordering — but drawing
    // it first still keeps the intent legible.
    if (galaxyOn() && lineProg && galaxyCount) {
      gl.useProgram(lineProg);
      gl.uniformMatrix4fv(lineU.uViewProj!, false, vp);
      gl.uniform1f(lineU.uAlpha!, 1);
      gl.bindVertexArray(galaxyVAO);
      gl.drawElements(gl.TRIANGLES, galaxyCount, gl.UNSIGNED_SHORT, 0);
      if (galaxyLineCount) {
        gl.bindVertexArray(galaxyLineVAO);
        gl.uniform1f(lineU.uAlpha!, 0.5);
        gl.drawArrays(gl.LINES, 0, galaxyLineCount);
      }
      gl.bindVertexArray(null);
    }

    if (input.showFrustum.value && lineProg) {
      rebuildLines();
      if (lineCount) {
        gl.useProgram(lineProg);
        gl.uniformMatrix4fv(lineU.uViewProj!, false, vp);
        gl.uniform1f(lineU.uAlpha!, 1);
        gl.bindVertexArray(lineVAO);
        gl.drawArrays(gl.LINES, 0, lineCount);
      }
    }

    if (input.showObjects.value && texReady) {
      if (quadProg) {
        rebuildQuads();
        if (quadDraws.length) {
          gl.useProgram(quadProg);
          gl.uniformMatrix4fv(quadU.uViewProj!, false, vp);
          gl.activeTexture(gl.TEXTURE0);
          gl.bindTexture(gl.TEXTURE_2D, tex);
          gl.uniform1i(quadU.uTex!, 0);
          gl.uniform1f(quadU.uOpacity!, 1);
          gl.bindVertexArray(quadVAO);
          for (const d of quadDraws) {
            gl.drawElements(gl.TRIANGLES, d.count, gl.UNSIGNED_SHORT, d.offset);
          }
        }
      }
      if (meshProg && meshes.length) {
        gl.useProgram(meshProg);
        gl.uniformMatrix4fv(meshU.uViewProj!, false, vp);
        setWarpUniforms(meshU, m);
        gl.activeTexture(gl.TEXTURE0);
        gl.bindTexture(gl.TEXTURE_2D, tex);
        gl.uniform1i(meshU.uTex!, 0);
        gl.uniform1f(meshU.uOpacity!, 1);
        for (const d of meshes) {
          const f = d.mesh.footprint;
          gl.uniform1f(meshU.uSlices!, d.mesh.slices);
          gl.uniform1f(meshU.uExponent!, d.exponent);
          gl.uniform1f(meshU.uBowl!, d.bowl);
          gl.uniform1f(meshU.uHollow!, d.hollow);
          gl.uniform4f(
            meshU.uFootprint!,
            f?.cx ?? 0.5,
            f?.cy ?? 0.5,
            f?.rx ?? 0.5,
            f?.ry ?? 0.5,
          );
          gl.bindVertexArray(d.vao);
          gl.drawElements(
            gl.TRIANGLES,
            d.mesh.indices.length,
            gl.UNSIGNED_SHORT,
            0,
          );
        }
      }
    }

    if (input.showMotion.value && lineProg) {
      rebuildMotion();
      if (motionCount) {
        gl.useProgram(lineProg);
        gl.uniformMatrix4fv(lineU.uViewProj!, false, vp);
        gl.uniform1f(lineU.uAlpha!, 0.9);
        gl.bindVertexArray(motionVAO);
        gl.drawArrays(gl.LINES, 0, motionCount);
      }
    }

    if (input.showStars.value && starCount && starProg) {
      gl.useProgram(starProg);
      gl.uniformMatrix4fv(starU.uViewProj!, false, vp);
      setWarpUniforms(starU, m);
      gl.uniform1f(starU.uSizeScale!, input.starSize.value);
      gl.uniform1ui(starU.uDepthMask!, depthMask());
      const cam = cameraPhysical(orbit.value, m, input.depth.value);
      const camLin = galaxyOn()
        ? cameraPhysicalLinear(eyePosition(orbit.value), PC_PER_SCENE_UNIT)
        : cam;
      gl.uniform3f(starU.uCamPhys!, camLin[0], camLin[1], camLin[2]);
      gl.bindVertexArray(starVAO);
      gl.drawArrays(gl.POINTS, 0, starCount);
    }
    gl.bindVertexArray(null);
  }

  function loop() {
    if (dirty) {
      dirty = false;
      draw();
    }
    raf = requestAnimationFrame(loop);
  }

  // --- interaction -------------------------------------------------------------------------------

  // Pointers are tracked by id so a two-finger gesture can be told from a one-finger drag — the same
  // handler serves mouse, trackpad and touch.
  const active = new Map<number, { x: number; y: number }>();
  let dragging = false;
  let panning = false;
  let moved = 0;
  let lastX = 0;
  let lastY = 0;
  let pinchDist = 0;
  let lastZoomAt = 0;

  function onPointerDown(e: PointerEvent) {
    // Once a gesture starts the pointer is manipulating the view, not pointing at anything, and the
    // hover is not refreshed while dragging — so leaving the last one set would show a stale star
    // for the whole drag.
    hovered.value = null;
    active.set(e.pointerId, { x: e.clientX, y: e.clientY });
    (e.target as Element).setPointerCapture?.(e.pointerId);
    if (active.size === 2) {
      const [a, b] = [...active.values()];
      pinchDist = Math.hypot(a.x - b.x, a.y - b.y);
      dragging = false;
      panning = true;
      return;
    }
    dragging = true;
    // Middle button, or shift with the left — the two conventions people already have in their hands.
    panning = e.button === 1 || e.shiftKey;
    moved = 0;
    lastX = e.clientX;
    lastY = e.clientY;
  }

  function onPointerMove(e: PointerEvent) {
    if (active.has(e.pointerId))
      active.set(e.pointerId, { x: e.clientX, y: e.clientY });

    if (active.size === 2) {
      const [a, b] = [...active.values()];
      const d = Math.hypot(a.x - b.x, a.y - b.y);
      if (pinchDist > 0 && d > 0) {
        const now = performance.now();
        const dt = lastZoomAt ? now - lastZoomAt : 16;
        lastZoomAt = now;
        // A pinch is a distance change, so feed it in as an equivalent wheel delta — the same
        // velocity curve then serves the trackpad, the mouse and the touchscreen.
        orbit.value = applyZoom(orbit.value, zoomExponent(pinchDist - d, dt));
        requestDraw();
      }
      pinchDist = d;
      return;
    }
    if (!dragging) {
      updateHover(e);
      return;
    }

    const dx = e.clientX - lastX;
    const dy = e.clientY - lastY;
    lastX = e.clientX;
    lastY = e.clientY;
    moved += Math.abs(dx) + Math.abs(dy);

    const m = input.manifest.value;
    const el = canvas.value;
    if (panning && m && el) {
      orbit.value = panOrbit(
        orbit.value,
        dx,
        dy,
        el.clientHeight,
        m.camera.tan_half_h,
      );
    } else {
      const o = orbit.value;
      // Same sign convention as looking around (dragToLook) so the field follows the pointer on both
      // axes, but a rotation rate of its own — orbiting is circling the field, not panning across
      // it. The hand-rolled 0.005 rad/px this replaces was independent of canvas size AND inverted
      // horizontally, so a right-drag pushed the field left.
      const { dYaw, dPitch } = dragToLook(
        dx,
        dy,
        orbitPerPixel(el?.clientHeight ?? 0),
      );
      orbit.value = {
        ...o,
        yaw: o.yaw + dYaw,
        // Clamped short of the poles, where the up vector flips and the view rolls over under the hand.
        pitch: Math.max(-PITCH_LIMIT, Math.min(PITCH_LIMIT, o.pitch + dPitch)),
      };
    }
    requestDraw();
  }

  function onPointerUp(e: PointerEvent) {
    active.delete(e.pointerId);
    if (active.size < 2) pinchDist = 0;
    if (dragging && !panning && moved < 5) selected.value = pickAt(e);
    dragging = false;
    panning = false;
  }

  function onWheel(e: WheelEvent) {
    e.preventDefault();
    const now = performance.now();
    const dt = lastZoomAt ? now - lastZoomAt : 16;
    lastZoomAt = now;
    // A trackpad pinch arrives as a wheel event with ctrlKey and much smaller deltas; scaling it up
    // makes the gesture cover the same ground as a physical wheel.
    const delta = e.ctrlKey ? e.deltaY * 4 : e.deltaY;
    orbit.value = applyZoom(orbit.value, zoomExponent(delta, dt));
    requestDraw();
  }

  // pickAt answers "what is under the pointer?" through the very same projection the renderer drew
  // with, so a hit can never land on a star that is not where it appears.
  function pickAt(e: PointerEvent): StarRecord | null {
    const el = canvas.value;
    const pts = input.points.value;
    const m = input.manifest.value;
    const vp = viewProj();
    if (!el || !pts || !m || !vp) return null;
    const rect = el.getBoundingClientRect();
    return pickNearest(
      pts,
      e.clientX - rect.left,
      e.clientY - rect.top,
      vp,
      { width: rect.width, height: rect.height },
      {
        near: m.depth.near_pc,
        far: m.depth.far_pc,
        depth: input.depth.value,
        visible: (d) => (depthMask() & (1 << d)) !== 0,
      },
    );
  }

  function updateHover(e: PointerEvent) {
    const el = canvas.value;
    if (!el) return;
    const rect = el.getBoundingClientRect();
    const hit = pickAt(e);
    const changed = hit?.index !== hovered.value?.index;
    hovered.value = hit;
    hoverAt.value = { x: e.clientX - rect.left, y: e.clientY - rect.top };
    if (changed) requestDraw();
  }

  function onPointerLeave() {
    hovered.value = null;
  }

  function resetView() {
    orbit.value = defaultOrbit();
    selected.value = null;
    requestDraw();
  }

  // openView swings the camera off the optical axis AND opens the depth. From Earth the scene is the
  // photograph at every depth — the stars slide along the rays they were seen on — so neither move
  // alone shows anything. Doing both is what makes one click reveal the volume.
  function openView() {
    input.depth.value = Math.max(input.depth.value, 0.6);
    orbit.value = {
      ...defaultOrbit(),
      target: [0, 0, Z_REF + Z_SPAN * 0.4],
      distance: Z_REF + Z_SPAN * 0.9,
      yaw: 0.62,
      pitch: 0.22,
    };
    requestDraw();
  }

  // --- lifecycle ---------------------------------------------------------------------------------

  watch(
    canvas,
    (el) => {
      if (!el || gl) return;
      if (!init()) return;
      uploadStars(input.points.value);
      loadBackdrop(input.backdropUrl.value);
      buildMeshes();
      raf = requestAnimationFrame(loop);
    },
    { immediate: true },
  );

  watch(input.points, (p) => uploadStars(p));
  watch(input.backdropUrl, (u) => loadBackdrop(u));
  watch(input.manifest, () => {
    buildMeshes();
    buildGalaxy();
  });
  watch(
    () => input.frame?.value,
    () => buildGalaxy(),
  );
  watch(
    () => input.showGalaxy?.value,
    () => buildGalaxy(),
  );

  // The slider WRITES the camera — the same contract openView() already has. The mouse is then free
  // to modify it, and moving the slider re-snaps. Because galaxyOrbit(0) puts the eye at Earth with
  // the run's own lens, switching the mode on at zero is a picture that does not move.
  watch(
    () => [input.showGalaxy?.value, input.galaxyZoom?.value] as const,
    ([on]) => {
      const m = input.manifest.value;
      const b = galaxyBasis.value;
      if (!m) return;
      if (!on || !b) {
        if (!on) orbit.value = defaultOrbit();
        requestDraw();
        return;
      }
      const g = galacticToScene(b);
      const unit = (v: readonly number[]): [number, number, number] => {
        const n = Math.hypot(v[0], v[1], v[2]) || 1;
        return [v[0] / n, v[1] / n, v[2] / n];
      };
      orbit.value = galaxyOrbit(input.galaxyZoom?.value ?? 0, {
        medianPc: m.depth.median_pc,
        tanHalfH: m.camera.tan_half_h,
        toGalacticCentre: unit(g[0]),
        toNorthPole: unit(g[2]),
      }).orbit;
      requestDraw();
    },
  );
  watch(
    [
      input.depth,
      input.showStars,
      input.showObjects,
      input.showFrustum,
      input.showEstimated,
      input.showMotion,
      input.motionYears,
      input.starSize,
      input.showGalaxy ?? ref(false),
      input.galaxyZoom ?? ref(0),
    ],
    requestDraw,
  );

  onBeforeUnmount(() => {
    cancelAnimationFrame(raf);
    observer?.disconnect();
    if (!gl) return;
    for (const b of [starBuf, quadBuf, quadIdx, lineBuf, motionBuf])
      if (b) gl.deleteBuffer(b);
    for (const a of [starVAO, quadVAO, lineVAO, motionVAO])
      if (a) gl.deleteVertexArray(a);
    for (const d of meshes) {
      if (d.vao) gl.deleteVertexArray(d.vao);
      if (d.vbo) gl.deleteBuffer(d.vbo);
      if (d.ibo) gl.deleteBuffer(d.ibo);
    }
    for (const p of [starProg, quadProg, meshProg, lineProg])
      if (p) gl.deleteProgram(p);
    if (tex) gl.deleteTexture(tex);
    gl = null;
  });

  return {
    supported,
    error,
    orbit,
    // Whether the field's orientation is known well enough to place a galaxy around it.
    galaxyAvailable: computed(() => !!galaxyBasis.value),
    selected,
    hovered,
    hoverAt,
    resetView,
    openView,
    requestDraw,
    onPointerDown,
    onPointerMove,
    onPointerUp,
    onPointerLeave,
    onWheel,
  };
}
