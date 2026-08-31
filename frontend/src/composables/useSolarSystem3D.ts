import { onBeforeUnmount, ref, watch, type Ref } from "vue";

import skymap from "@/assets/skymap.json";
import { BASE } from "@/services/api";
import {
  applyZoom,
  ECLIPTIC_FRAME,
  PITCH_LIMIT,
  viewBasis,
  viewMatrixAtOrigin,
  zoomExponent,
  type Orbit,
} from "@/utils/orbitcam";
import {
  multiply,
  perspective,
  projectToScreen,
  type Mat4,
  type Vec3,
} from "@/utils/mat4";
import {
  AU_PER_KM,
  bodyBasis,
  drawRadius,
  equatorialToEcliptic,
  heliocentricAt,
  jdFromMs,
  localPositionAt,
  orbitPath,
  orientationAt,
  radialWarp,
  scenePosition,
  unitVector,
} from "@/utils/solarsystem";
import type { SolarBody, SolarManifest } from "@/types";

// The solar-system renderer: hand-rolled WebGL2, like the star field it sits beside.
//
// Two things make this different from drawing any other scene, and both are consequences of the
// same fact — the solar system spans six and a half orders of magnitude, from a moon eleven
// kilometres across to Neptune's orbit thirty astronomical units out.
//
//  1. Every position is computed in double precision and subtracted from the eye BEFORE it reaches
//     the GPU. Quoted from the Sun in float32, two points on Earth's surface are the same point.
//  2. Depth is logarithmic. A conventional depth buffer stretched from a metre to thirty AU has no
//     precision left anywhere useful, and the planets z-fight with their own moons.
//
// Everything else follows the field map: shaders as template literals up top, a dirty-flag draw
// loop, all the maths in utils/ where it can be tested without a GL context.

// --- shaders -------------------------------------------------------------------------------------

// LOG_DEPTH is shared by every program that writes depth, interpolated in rather than repeated, so
// two of them can never disagree about where a fragment is. uFC is 2/log2(far+1).
const LOG_DEPTH_VS = `
out float vLogZ;
void setLogDepth() { vLogZ = 1.0 + gl_Position.w; }
`;
const LOG_DEPTH_FS = `
in float vLogZ;
uniform float uFC;
void writeLogDepth() { gl_FragDepth = log2(vLogZ) * uFC * 0.5; }
`;

const BODY_VERT = `#version 300 es
precision highp float;
layout(location=0) in vec3 aSphere;   // unit sphere
layout(location=1) in vec2 aUV;

uniform mat4 uViewProj;
uniform vec3 uCentre;                 // camera-relative, scene units
uniform mat3 uBasis;                  // body-fixed axes, unit length
uniform vec3 uRadii;                  // equatorial, equatorial, polar — scene units

out vec3 vNormal;
out vec3 vWorld;
out vec2 vUV;
${LOG_DEPTH_VS}

void main() {
  vec3 local = uBasis * (aSphere * uRadii);
  vWorld = uCentre + local;
  // The inverse-transpose of an orthogonal basis scaled by the radii is the same basis scaled by
  // their reciprocals — which is what keeps the terminator in the right place on a flattened planet.
  vNormal = normalize(uBasis * (aSphere / uRadii));
  vUV = aUV;
  gl_Position = uViewProj * vec4(vWorld, 1.0);
  setLogDepth();
}`;

const BODY_FRAG = `#version 300 es
precision highp float;
in vec3 vNormal;
in vec3 vWorld;
in vec2 vUV;

uniform vec3 uSun;          // the Sun's position, camera-relative
uniform vec3 uColour;
uniform sampler2D uMap;
uniform float uHasMap;
uniform float uEmissive;    // 1 for the Sun: it makes its own light
uniform float uAmbient;

out vec4 frag;
${LOG_DEPTH_FS}

void main() {
  vec3 albedo = uColour;
  if (uHasMap > 0.5) albedo = texture(uMap, vUV).rgb;

  if (uEmissive > 0.5) {
    frag = vec4(albedo, 1.0);
    writeLogDepth();
    return;
  }

  vec3 n = normalize(vNormal);
  vec3 l = normalize(uSun - vWorld);
  // A little wrap around the terminator. A hard Lambert cut looks like a polygon edge on a body
  // only a few pixels across, which is most of them most of the time.
  float lambert = clamp((dot(n, l) + 0.08) / 1.08, 0.0, 1.0);
  float lit = lambert * lambert * (3.0 - 2.0 * lambert);
  frag = vec4(albedo * (uAmbient + (1.0 - uAmbient) * lit), 1.0);
  writeLogDepth();
}`;

// The ring is an annulus in the planet's equatorial plane, lit from both faces — ring particles
// scatter, so the unlit side is dim rather than black.
const RING_VERT = `#version 300 es
precision highp float;
layout(location=0) in vec2 aRing;     // x = radius fraction 0..1, y = angle turns

uniform mat4 uViewProj;
uniform vec3 uCentre;
uniform mat3 uBasis;
uniform vec2 uRadii;                  // inner, outer — scene units

out float vT;
out vec3 vWorld;
${LOG_DEPTH_VS}

void main() {
  float r = mix(uRadii.x, uRadii.y, aRing.x);
  float a = aRing.y * 6.283185307179586;
  vec3 local = uBasis * vec3(r * cos(a), r * sin(a), 0.0);
  vWorld = uCentre + local;
  vT = aRing.x;
  gl_Position = uViewProj * vec4(vWorld, 1.0);
  setLogDepth();
}`;

const RING_FRAG = `#version 300 es
precision highp float;
in float vT;
in vec3 vWorld;

uniform vec3 uSun;
uniform vec3 uColour;
uniform sampler2D uMap;
uniform float uHasMap;
uniform float uOpacity;

out vec4 frag;
${LOG_DEPTH_FS}

void main() {
  vec4 sampled = uHasMap > 0.5 ? texture(uMap, vec2(vT, 0.5)) : vec4(uColour, 1.0);
  float alpha = sampled.a * uOpacity;
  if (alpha < 0.004) discard;
  frag = vec4(sampled.rgb, alpha);
  writeLogDepth();
}`;

const LINE_VERT = `#version 300 es
precision highp float;
layout(location=0) in vec3 aPos;
layout(location=1) in vec3 aColour;
layout(location=2) in float aFade;    // 0..1 along the trail, for the tail

uniform mat4 uViewProj;
out vec3 vColour;
out float vFade;
${LOG_DEPTH_VS}

void main() {
  vColour = aColour;
  vFade = aFade;
  gl_Position = uViewProj * vec4(aPos, 1.0);
  setLogDepth();
}`;

const LINE_FRAG = `#version 300 es
precision highp float;
in vec3 vColour;
in float vFade;
uniform float uOpacity;
out vec4 frag;
${LOG_DEPTH_FS}

void main() {
  frag = vec4(vColour, uOpacity * vFade);
  writeLogDepth();
}`;

// The Sun's glow: one screen-aligned quad, drawn additively over the disc.
//
// Without it the Sun is a small bright circle among other small bright circles, and the thing every
// orbit on screen is bent around does not read as the source of the light.
const GLOW_VERT = `#version 300 es
precision highp float;
layout(location=0) in vec2 aQuad;     // −1..1 both axes

uniform mat4 uViewProj;
uniform vec3 uCentre;
uniform vec3 uRight;
uniform vec3 uUp;
uniform float uSize;

out vec2 vQuad;

void main() {
  vQuad = aQuad;
  vec3 world = uCentre + (uRight * aQuad.x + uUp * aQuad.y) * uSize;
  gl_Position = uViewProj * vec4(world, 1.0);
}`;

const GLOW_FRAG = `#version 300 es
precision highp float;
in vec2 vQuad;
uniform vec3 uColour;
uniform float uGain;
out vec4 frag;

void main() {
  float r = length(vQuad);
  if (r > 1.0) discard;
  // A tight core with a wide, fast-falling skirt: the shape a bright source makes in a lens, not a
  // linear ramp, which reads as a flat disc.
  float a = pow(1.0 - r, 3.0) * 0.55 + pow(max(0.0, 1.0 - r * 3.2), 6.0) * 0.9;
  frag = vec4(uColour * a * uGain, a * uGain);
}`;

// The background sky is drawn at infinity: direction only, no depth, so it can never occlude or be
// occluded by anything in the system.
const SKY_VERT = `#version 300 es
precision highp float;
layout(location=0) in vec3 aDir;
layout(location=1) in float aMag;

uniform mat4 uViewRot;                // rotation only — the sky does not translate with the camera
uniform float uPixelScale;
uniform float uGain;

out float vBright;

void main() {
  gl_Position = uViewRot * vec4(aDir, 1.0);
  gl_Position.z = 0.0;
  // Magnitude to a linear brightness, then to a point a couple of pixels across at most.
  float b = pow(10.0, -0.4 * (aMag - 1.0));
  vBright = clamp(b * uGain, 0.0, 1.0);
  gl_PointSize = clamp(1.0 + 1.6 * pow(vBright, 0.4), 1.0, 4.0) * uPixelScale;
}`;

const SKY_FRAG = `#version 300 es
precision highp float;
in float vBright;
out vec4 frag;

void main() {
  vec2 d = gl_PointCoord - 0.5;
  float r = length(d) * 2.0;
  float a = smoothstep(1.0, 0.0, r) * vBright;
  frag = vec4(vec3(0.85, 0.88, 1.0) * a, a);
}`;

// --- GL helpers ----------------------------------------------------------------------------------

function compile(gl: WebGL2RenderingContext, type: number, src: string) {
  const sh = gl.createShader(type)!;
  gl.shaderSource(sh, src);
  gl.compileShader(sh);
  if (!gl.getShaderParameter(sh, gl.COMPILE_STATUS)) {
    throw new Error(`shader: ${gl.getShaderInfoLog(sh)}`);
  }
  return sh;
}

function link(gl: WebGL2RenderingContext, vs: string, fs: string) {
  const p = gl.createProgram()!;
  gl.attachShader(p, compile(gl, gl.VERTEX_SHADER, vs));
  gl.attachShader(p, compile(gl, gl.FRAGMENT_SHADER, fs));
  gl.linkProgram(p);
  if (!gl.getProgramParameter(p, gl.LINK_STATUS)) {
    throw new Error(`program: ${gl.getProgramInfoLog(p)}`);
  }
  return p;
}

function uniforms(
  gl: WebGL2RenderingContext,
  p: WebGLProgram,
  names: string[],
): Record<string, WebGLUniformLocation | null> {
  const out: Record<string, WebGLUniformLocation | null> = {};
  for (const n of names) out[n] = gl.getUniformLocation(p, n);
  return out;
}

// --- geometry ------------------------------------------------------------------------------------

/** A UV sphere: one mesh serves every world, reshaped per body by the basis and radii uniforms. */
function sphereMesh(segments = 64, rings = 32) {
  const verts: number[] = [];
  const idx: number[] = [];
  for (let r = 0; r <= rings; r++) {
    const v = r / rings;
    const phi = v * Math.PI;
    for (let s = 0; s <= segments; s++) {
      const u = s / segments;
      const theta = u * 2 * Math.PI;
      // u = 0.5 puts the prime meridian at the centre of an equirectangular map, which is how they
      // are made — Earth's day map is centred on Greenwich.
      const a = theta - Math.PI;
      verts.push(
        Math.sin(phi) * Math.cos(a),
        Math.sin(phi) * Math.sin(a),
        Math.cos(phi),
        u,
        v,
      );
    }
  }
  for (let r = 0; r < rings; r++) {
    for (let s = 0; s < segments; s++) {
      const a = r * (segments + 1) + s;
      const b = a + segments + 1;
      idx.push(a, b, a + 1, b, b + 1, a + 1);
    }
  }
  return {
    verts: new Float32Array(verts),
    idx: new Uint16Array(idx),
    count: idx.length,
  };
}

function ringMesh(segments = 192) {
  const verts: number[] = [];
  const idx: number[] = [];
  for (let s = 0; s <= segments; s++) {
    const t = s / segments;
    verts.push(0, t, 1, t);
  }
  for (let s = 0; s < segments; s++) {
    const a = s * 2;
    idx.push(a, a + 1, a + 2, a + 1, a + 3, a + 2);
  }
  return {
    verts: new Float32Array(verts),
    idx: new Uint16Array(idx),
    count: idx.length,
  };
}

/** The background sky, as directions in the J2000 ecliptic frame the scene is drawn in. */
function skyBuffer(): { data: Float32Array; count: number } {
  const stars = skymap.stars as [number, number, number][];
  const out = new Float32Array(stars.length * 4);
  for (let i = 0; i < stars.length; i++) {
    const [raDeg, decDeg, mag] = stars[i];
    const dir = equatorialToEcliptic(unitVector(raDeg, decDeg));
    out[i * 4] = dir[0];
    out[i * 4 + 1] = dir[1];
    out[i * 4 + 2] = dir[2];
    out[i * 4 + 3] = mag;
  }
  return { data: out, count: stars.length };
}

function hexToRgb(hex: string): Vec3 {
  const m = /^#?([0-9a-f]{6})$/i.exec(hex.trim());
  if (!m) return [1, 1, 1];
  const n = parseInt(m[1], 16);
  return [((n >> 16) & 255) / 255, ((n >> 8) & 255) / 255, (n & 255) / 255];
}

// --- the composable ------------------------------------------------------------------------------

/** Where a body ended up this frame — what the label overlay and the picker read. */
export interface DrawnBody {
  key: string;
  /** Camera-relative scene position. */
  rel: Vec3;
  /** Screen position in CSS pixels, or null when off screen or behind the camera. */
  screen: [number, number] | null;
  /** Drawn radius in CSS pixels. */
  radiusPx: number;
  distanceFromEye: number;
  colour: string;
}

export interface SolarSystem3DInput {
  manifest: Ref<SolarManifest | null>;
  timeMs: Ref<number>;
  /** 0 = true distances, 1 = fully log-compressed. */
  warp: Ref<number>;
  /** How much satellite systems are blown up, so they are visible from outside. */
  moonScale: Ref<number>;
  /** Multiplies every body's true size, for the diagram look. */
  exaggerate: Ref<number>;
  showOrbits: Ref<boolean>;
  showAxes: Ref<boolean>;
  showStars: Ref<boolean>;
  showLabels: Ref<boolean>;
  /** The body the camera tracks, or null to stay put. */
  follow: Ref<string | null>;
  selected: Ref<string | null>;
}

export function useSolarSystem3D(
  canvas: Ref<HTMLCanvasElement | null>,
  input: SolarSystem3DInput,
) {
  const supported = ref(true);
  const hovered = ref<string | null>(null);
  const drawn = ref<DrawnBody[]>([]);
  const eyeDistanceAU = ref(0);

  let gl: WebGL2RenderingContext | null = null;
  let el: HTMLCanvasElement | null = null;
  let raf = 0;
  let dirty = true;
  let observer: ResizeObserver | null = null;

  const orbit = ref<Orbit>({
    target: [0, 0, 0],
    distance: 3.2,
    yaw: 0,
    pitch: 0.62,
    roll: 0,
  });

  // Programs and their uniforms.
  let bodyProg: WebGLProgram | null = null;
  let bodyU: Record<string, WebGLUniformLocation | null> = {};
  let ringProg: WebGLProgram | null = null;
  let ringU: Record<string, WebGLUniformLocation | null> = {};
  let lineProg: WebGLProgram | null = null;
  let lineU: Record<string, WebGLUniformLocation | null> = {};
  let skyProg: WebGLProgram | null = null;
  let skyU: Record<string, WebGLUniformLocation | null> = {};
  let glowProg: WebGLProgram | null = null;
  let glowU: Record<string, WebGLUniformLocation | null> = {};
  let glowVAO: WebGLVertexArrayObject | null = null;

  let sphereVAO: WebGLVertexArrayObject | null = null;
  let sphereCount = 0;
  let ringVAO: WebGLVertexArrayObject | null = null;
  let ringCount = 0;
  let skyVAO: WebGLVertexArrayObject | null = null;
  let skyCount = 0;
  let lineVAO: WebGLVertexArrayObject | null = null;
  let lineBuf: WebGLBuffer | null = null;
  let lineCount = 0;
  let whiteTex: WebGLTexture | null = null;

  const textures = new Map<string, WebGLTexture>();
  const textureTried = new Set<string>();

  let viewportW = 1;
  let viewportH = 1;
  let dpr = 1;
  let tanHalfH = 0.4;

  const requestDraw = () => {
    dirty = true;
  };

  // --- setup -------------------------------------------------------------------------------------

  function init(): boolean {
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
      bodyProg = link(gl, BODY_VERT, BODY_FRAG);
      ringProg = link(gl, RING_VERT, RING_FRAG);
      lineProg = link(gl, LINE_VERT, LINE_FRAG);
      skyProg = link(gl, SKY_VERT, SKY_FRAG);
      glowProg = link(gl, GLOW_VERT, GLOW_FRAG);
    } catch {
      supported.value = false;
      return false;
    }

    bodyU = uniforms(gl, bodyProg!, [
      "uViewProj",
      "uCentre",
      "uBasis",
      "uRadii",
      "uSun",
      "uColour",
      "uMap",
      "uHasMap",
      "uEmissive",
      "uAmbient",
      "uFC",
    ]);
    ringU = uniforms(gl, ringProg!, [
      "uViewProj",
      "uCentre",
      "uBasis",
      "uRadii",
      "uSun",
      "uColour",
      "uMap",
      "uHasMap",
      "uOpacity",
      "uFC",
    ]);
    lineU = uniforms(gl, lineProg!, ["uViewProj", "uOpacity", "uFC"]);
    skyU = uniforms(gl, skyProg!, ["uViewRot", "uPixelScale", "uGain"]);
    glowU = uniforms(gl, glowProg!, [
      "uViewProj",
      "uCentre",
      "uRight",
      "uUp",
      "uSize",
      "uColour",
      "uGain",
    ]);

    buildMeshes();

    gl.enable(gl.DEPTH_TEST);
    gl.depthFunc(gl.LEQUAL);
    gl.enable(gl.BLEND);
    gl.blendFunc(gl.SRC_ALPHA, gl.ONE_MINUS_SRC_ALPHA);
    gl.clearColor(0.004, 0.006, 0.016, 1);

    observer = new ResizeObserver(requestDraw);
    observer.observe(el);
    return true;
  }

  function buildMeshes() {
    if (!gl) return;

    const sphere = sphereMesh();
    sphereVAO = gl.createVertexArray();
    gl.bindVertexArray(sphereVAO);
    const sv = gl.createBuffer();
    gl.bindBuffer(gl.ARRAY_BUFFER, sv);
    gl.bufferData(gl.ARRAY_BUFFER, sphere.verts, gl.STATIC_DRAW);
    gl.enableVertexAttribArray(0);
    gl.vertexAttribPointer(0, 3, gl.FLOAT, false, 20, 0);
    gl.enableVertexAttribArray(1);
    gl.vertexAttribPointer(1, 2, gl.FLOAT, false, 20, 12);
    const si = gl.createBuffer();
    gl.bindBuffer(gl.ELEMENT_ARRAY_BUFFER, si);
    gl.bufferData(gl.ELEMENT_ARRAY_BUFFER, sphere.idx, gl.STATIC_DRAW);
    sphereCount = sphere.count;

    const ring = ringMesh();
    ringVAO = gl.createVertexArray();
    gl.bindVertexArray(ringVAO);
    const rv = gl.createBuffer();
    gl.bindBuffer(gl.ARRAY_BUFFER, rv);
    gl.bufferData(gl.ARRAY_BUFFER, ring.verts, gl.STATIC_DRAW);
    gl.enableVertexAttribArray(0);
    gl.vertexAttribPointer(0, 2, gl.FLOAT, false, 8, 0);
    const ri = gl.createBuffer();
    gl.bindBuffer(gl.ELEMENT_ARRAY_BUFFER, ri);
    gl.bufferData(gl.ELEMENT_ARRAY_BUFFER, ring.idx, gl.STATIC_DRAW);
    ringCount = ring.count;

    const sky = skyBuffer();
    skyVAO = gl.createVertexArray();
    gl.bindVertexArray(skyVAO);
    const kv = gl.createBuffer();
    gl.bindBuffer(gl.ARRAY_BUFFER, kv);
    gl.bufferData(gl.ARRAY_BUFFER, sky.data, gl.STATIC_DRAW);
    gl.enableVertexAttribArray(0);
    gl.vertexAttribPointer(0, 3, gl.FLOAT, false, 16, 0);
    gl.enableVertexAttribArray(1);
    gl.vertexAttribPointer(1, 1, gl.FLOAT, false, 16, 12);
    skyCount = sky.count;

    lineVAO = gl.createVertexArray();
    gl.bindVertexArray(lineVAO);
    lineBuf = gl.createBuffer();
    gl.bindBuffer(gl.ARRAY_BUFFER, lineBuf);
    gl.enableVertexAttribArray(0);
    gl.vertexAttribPointer(0, 3, gl.FLOAT, false, 28, 0);
    gl.enableVertexAttribArray(1);
    gl.vertexAttribPointer(1, 3, gl.FLOAT, false, 28, 12);
    gl.enableVertexAttribArray(2);
    gl.vertexAttribPointer(2, 1, gl.FLOAT, false, 28, 24);

    glowVAO = gl.createVertexArray();
    gl.bindVertexArray(glowVAO);
    const gv = gl.createBuffer();
    gl.bindBuffer(gl.ARRAY_BUFFER, gv);
    gl.bufferData(
      gl.ARRAY_BUFFER,
      new Float32Array([-1, -1, 1, -1, -1, 1, 1, 1]),
      gl.STATIC_DRAW,
    );
    gl.enableVertexAttribArray(0);
    gl.vertexAttribPointer(0, 2, gl.FLOAT, false, 8, 0);

    gl.bindVertexArray(null);

    // A one-pixel white texture keeps the sampler bound even for a body with no map, so the shader
    // never reads from an unbound unit.
    whiteTex = gl.createTexture();
    gl.bindTexture(gl.TEXTURE_2D, whiteTex);
    gl.texImage2D(
      gl.TEXTURE_2D,
      0,
      gl.RGBA,
      1,
      1,
      0,
      gl.RGBA,
      gl.UNSIGNED_BYTE,
      new Uint8Array([255, 255, 255, 255]),
    );
  }

  /**
   * loadTexture fetches a surface map the first time a body needs it. A body whose map was never
   * downloaded is not an error and is not retried: it keeps its procedural colour, and the engine's
   * manifest already said which maps exist.
   */
  function loadTexture(key: string) {
    if (!gl || textures.has(key) || textureTried.has(key)) return;
    // The manifest already said which maps this engine holds. Asking for one it does not have would
    // be a 404 per body per reload, and a console full of red for a case that is entirely normal.
    if (!input.manifest.value?.textures?.includes(key)) return;
    textureTried.add(key);
    const img = new Image();
    img.crossOrigin = "anonymous";
    img.onload = () => {
      if (!gl) return;
      const tex = gl.createTexture()!;
      gl.bindTexture(gl.TEXTURE_2D, tex);
      gl.pixelStorei(gl.UNPACK_FLIP_Y_WEBGL, false);
      gl.texImage2D(gl.TEXTURE_2D, 0, gl.RGBA, gl.RGBA, gl.UNSIGNED_BYTE, img);
      gl.generateMipmap(gl.TEXTURE_2D);
      gl.texParameteri(
        gl.TEXTURE_2D,
        gl.TEXTURE_MIN_FILTER,
        gl.LINEAR_MIPMAP_LINEAR,
      );
      gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_S, gl.REPEAT);
      gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_T, gl.CLAMP_TO_EDGE);
      textures.set(key, tex);
      requestDraw();
    };
    img.src = `${BASE}/api/solarsystem/texture?key=${encodeURIComponent(key)}`;
  }

  // --- the frame ---------------------------------------------------------------------------------

  interface Placed {
    body: SolarBody;
    /** Camera-relative scene position, in scene units. */
    rel: Vec3;
    /** True heliocentric position, AU — the physics, untouched by the warp. */
    helio: Vec3;
    radius: number;
    distance: number;
  }

  function place(manifest: SolarManifest, jd: number, eye: Vec3): Placed[] {
    const byKey = new Map<string, SolarBody>(
      manifest.bodies.map((b) => [b.key, b]),
    );
    const out: Placed[] = [];
    for (const body of manifest.bodies) {
      const helio = heliocentricAt(byKey, body, jd);
      const local = body.parent
        ? localPositionAt(body, jd)
        : ([0, 0, 0] as Vec3);
      const scene = scenePosition(
        helio,
        local,
        input.warp.value,
        input.moonScale.value,
      );
      const rel: Vec3 = [
        scene[0] - eye[0],
        scene[1] - eye[1],
        scene[2] - eye[2],
      ];
      const distance = Math.hypot(rel[0], rel[1], rel[2]);
      out.push({
        body,
        rel,
        helio,
        distance,
        radius: drawRadius(
          body.radius_km * AU_PER_KM,
          distance,
          tanHalfH,
          viewportH,
          input.exaggerate.value,
        ),
      });
    }
    // Far first: the ring blends, and blending is only order-independent if you never do it.
    return out.sort((a, b) => b.distance - a.distance);
  }

  /** The camera's target, following a body when one is chosen. */
  function resolveTarget(manifest: SolarManifest, jd: number): Vec3 {
    const key = input.follow.value;
    if (!key) return orbit.value.target;
    const byKey = new Map<string, SolarBody>(
      manifest.bodies.map((b) => [b.key, b]),
    );
    const body = byKey.get(key);
    if (!body) return orbit.value.target;
    const helio = heliocentricAt(byKey, body, jd);
    const local = body.parent ? localPositionAt(body, jd) : ([0, 0, 0] as Vec3);
    return scenePosition(helio, local, input.warp.value, input.moonScale.value);
  }

  function resize() {
    if (!gl || !el) return;
    dpr = Math.min(2, window.devicePixelRatio || 1);
    const w = Math.max(1, Math.round(el.clientWidth * dpr));
    const h = Math.max(1, Math.round(el.clientHeight * dpr));
    if (el.width !== w || el.height !== h) {
      el.width = w;
      el.height = h;
    }
    gl.viewport(0, 0, w, h);
    viewportW = w;
    viewportH = h;
  }

  function draw() {
    if (!gl || !el) return;
    resize();
    const manifest = input.manifest.value;
    gl.clear(gl.COLOR_BUFFER_BIT | gl.DEPTH_BUFFER_BIT);
    if (!manifest) return;

    const jd = jdFromMs(input.timeMs.value);
    orbit.value.target = resolveTarget(manifest, jd);

    // The eye, in double precision, before anything is cast to float32.
    const o = orbit.value;
    const cp = Math.cos(o.pitch);
    const eye: Vec3 = [
      o.target[0] + o.distance * (cp * Math.sin(o.yaw)),
      o.target[1] + o.distance * -cp * Math.cos(o.yaw),
      o.target[2] + o.distance * Math.sin(o.pitch),
    ];
    eyeDistanceAU.value = Math.hypot(eye[0], eye[1], eye[2]);

    // Near and far bracket the whole scene. The log depth buffer is what makes a range this wide
    // usable at all: a metre and a hundred astronomical units in the same frame.
    const near = 1e-9;
    const far = 400;
    const aspect = viewportW / viewportH;
    tanHalfH = 0.4;
    const proj = perspective(tanHalfH * aspect, tanHalfH, near, far);
    const viewRot = viewMatrixAtOrigin(o, ECLIPTIC_FRAME);
    const viewProj = multiply(proj, viewRot);
    const fc = 2.0 / Math.log2(far + 1.0);

    if (input.showStars.value) drawSky(viewProj, proj, viewRot);

    const placed = place(manifest, jd, eye);
    const sunRel: Vec3 = [-eye[0], -eye[1], -eye[2]];

    if (input.showOrbits.value || input.showAxes.value) {
      drawLines(manifest, jd, eye, placed, viewProj, fc);
    }
    drawBodies(placed, sunRel, viewProj, fc, jd);

    // What the overlay and the picker read: computed from the SAME matrix that drew the frame, so a
    // label can never sit somewhere its body is not.
    drawn.value = placed.map((p) => ({
      key: p.body.key,
      rel: p.rel,
      screen: toCss(
        projectToScreen(p.rel, viewProj, {
          width: viewportW,
          height: viewportH,
        }),
      ),
      radiusPx: p.radius / ((2 * tanHalfH * p.distance) / viewportH) / dpr,
      distanceFromEye: p.distance,
      colour: p.body.colour,
    }));
  }

  function toCss(p: [number, number] | null): [number, number] | null {
    return p ? [p[0] / dpr, p[1] / dpr] : null;
  }

  function drawSky(viewProj: Mat4, proj: Mat4, viewRot: Mat4) {
    if (!gl || !skyProg || !skyVAO) return;
    gl.useProgram(skyProg);
    gl.bindVertexArray(skyVAO);
    gl.depthMask(false);
    gl.uniformMatrix4fv(skyU.uViewRot, false, multiply(proj, viewRot));
    gl.uniform1f(skyU.uPixelScale, dpr);
    gl.uniform1f(skyU.uGain, 0.9);
    gl.drawArrays(gl.POINTS, 0, skyCount);
    gl.depthMask(true);
    void viewProj;
  }

  function drawBodies(
    placed: Placed[],
    sunRel: Vec3,
    viewProj: Mat4,
    fc: number,
    jd: number,
  ) {
    if (!gl || !bodyProg || !sphereVAO) return;

    for (const p of placed) {
      const b = p.body;
      if (b.texture) loadTexture(b.texture);

      gl.useProgram(bodyProg);
      gl.bindVertexArray(sphereVAO);
      gl.uniformMatrix4fv(bodyU.uViewProj, false, viewProj);
      gl.uniform3f(bodyU.uCentre, p.rel[0], p.rel[1], p.rel[2]);
      gl.uniform1f(bodyU.uFC, fc);

      const { x, y, z } = bodyBasis(orientationAt(b.pole, jd));
      // prettier-ignore
      gl.uniformMatrix3fv(bodyU.uBasis, false, new Float32Array([
        x[0], x[1], x[2],
        y[0], y[1], y[2],
        z[0], z[1], z[2],
      ]));
      const flattening = b.polar_radius_km
        ? b.polar_radius_km / b.radius_km
        : 1;
      gl.uniform3f(bodyU.uRadii, p.radius, p.radius, p.radius * flattening);
      gl.uniform3f(bodyU.uSun, sunRel[0], sunRel[1], sunRel[2]);

      const rgb = hexToRgb(b.colour);
      gl.uniform3f(bodyU.uColour, rgb[0], rgb[1], rgb[2]);
      gl.uniform1f(bodyU.uEmissive, b.kind === "star" ? 1 : 0);
      // A body drawn at the screen-size floor is a marker, not a globe: shading it would make it a
      // dark speck at half phase, which is exactly when someone is looking for it.
      const tiny =
        p.radius > b.radius_km * AU_PER_KM * input.exaggerate.value * 1.5;
      gl.uniform1f(bodyU.uAmbient, tiny ? 0.75 : 0.045);

      const tex = b.texture ? textures.get(b.texture) : undefined;
      gl.activeTexture(gl.TEXTURE0);
      gl.bindTexture(gl.TEXTURE_2D, tex ?? whiteTex);
      gl.uniform1i(bodyU.uMap, 0);
      gl.uniform1f(bodyU.uHasMap, tex && !tiny ? 1 : 0);

      gl.drawElements(gl.TRIANGLES, sphereCount, gl.UNSIGNED_SHORT, 0);

      if (b.ring) drawRing(p, sunRel, viewProj, fc, jd);
      drawGlow(p, viewProj);
    }
  }

  /**
   * The halo: the Sun's corona, and the soft marker every other world wears while it is too far away
   * to be more than a dot.
   *
   * The marker's gain falls to zero exactly as a body's true size overtakes the screen-space floor,
   * so a planet you have flown up to is a globe with no halo round it, and the same planet seen from
   * across the system is a coloured point you can actually find. Nothing switches; it crosses over.
   */
  function drawGlow(p: Placed, viewProj: Mat4) {
    if (!gl || !glowProg || !glowVAO) return;

    const star = p.body.kind === "star";
    const trueRadius = p.body.radius_km * AU_PER_KM * input.exaggerate.value;
    const gain = star
      ? 1
      : Math.max(0, 1 - trueRadius / Math.max(p.radius, 1e-30));
    if (gain <= 0.01) return;

    const perPx = (2 * tanHalfH * p.distance) / viewportH;
    // The Sun keeps a floor in screen space so its corona never vanishes at the distance its disc
    // becomes a dot — which is most of the time, from anywhere useful.
    const size = star
      ? Math.max(p.radius * 7, 26 * perPx)
      : Math.max(p.radius * 3.5, 5 * perPx);

    const { right, up } = viewBasis(orbit.value, ECLIPTIC_FRAME);
    const rgb = star ? ([1.0, 0.92, 0.72] as Vec3) : hexToRgb(p.body.colour);

    gl.useProgram(glowProg);
    gl.bindVertexArray(glowVAO);
    gl.uniformMatrix4fv(glowU.uViewProj, false, viewProj);
    gl.uniform3f(glowU.uCentre, p.rel[0], p.rel[1], p.rel[2]);
    gl.uniform3f(glowU.uRight, right[0], right[1], right[2]);
    gl.uniform3f(glowU.uUp, up[0], up[1], up[2]);
    gl.uniform1f(glowU.uSize, size);
    gl.uniform3f(glowU.uColour, rgb[0], rgb[1], rgb[2]);
    gl.uniform1f(glowU.uGain, star ? 1 : gain * 0.9);

    // Straight addition, not SRC_ALPHA. The shader already premultiplies, and blending by alpha a
    // second time squares the falloff — which collapses the skirt to nothing and leaves a bare dot.
    gl.depthMask(false);
    gl.blendFunc(gl.ONE, gl.ONE);
    gl.drawArrays(gl.TRIANGLE_STRIP, 0, 4);
    gl.blendFunc(gl.SRC_ALPHA, gl.ONE_MINUS_SRC_ALPHA);
    gl.depthMask(true);
  }

  function drawRing(
    p: Placed,
    sunRel: Vec3,
    viewProj: Mat4,
    fc: number,
    jd: number,
  ) {
    if (!gl || !ringProg || !ringVAO || !p.body.ring) return;
    const ring = p.body.ring;
    // The ring scales with the body's DRAWN radius, so an exaggerated Saturn keeps its rings in
    // proportion instead of wearing a hoop at its true distance.
    const scale = p.radius / (p.body.radius_km * AU_PER_KM);
    const inner = ring.inner_km * AU_PER_KM * scale;
    const outer = ring.outer_km * AU_PER_KM * scale;

    gl.useProgram(ringProg);
    gl.bindVertexArray(ringVAO);
    gl.uniformMatrix4fv(ringU.uViewProj, false, viewProj);
    gl.uniform3f(ringU.uCentre, p.rel[0], p.rel[1], p.rel[2]);
    gl.uniform1f(ringU.uFC, fc);
    const { x, y, z } = bodyBasis(orientationAt(p.body.pole, jd));
    // prettier-ignore
    gl.uniformMatrix3fv(ringU.uBasis, false, new Float32Array([
      x[0], x[1], x[2],
      y[0], y[1], y[2],
      z[0], z[1], z[2],
    ]));
    gl.uniform2f(ringU.uRadii, inner, outer);
    gl.uniform3f(ringU.uSun, sunRel[0], sunRel[1], sunRel[2]);
    const rgb = hexToRgb(p.body.colour);
    gl.uniform3f(ringU.uColour, rgb[0], rgb[1], rgb[2]);

    const tex = ring.texture ? textures.get(ring.texture) : undefined;
    if (ring.texture) loadTexture(ring.texture);
    gl.activeTexture(gl.TEXTURE0);
    gl.bindTexture(gl.TEXTURE_2D, tex ?? whiteTex);
    gl.uniform1i(ringU.uMap, 0);
    gl.uniform1f(ringU.uHasMap, tex ? 1 : 0);
    gl.uniform1f(ringU.uOpacity, ring.faint ? 0.18 : 0.85);

    gl.disable(gl.CULL_FACE);
    gl.drawElements(gl.TRIANGLES, ringCount, gl.UNSIGNED_SHORT, 0);
  }

  function drawLines(
    manifest: SolarManifest,
    jd: number,
    eye: Vec3,
    placed: Placed[],
    viewProj: Mat4,
    fc: number,
  ) {
    if (!gl || !lineProg || !lineVAO || !lineBuf) return;
    const verts: number[] = [];

    if (input.showOrbits.value) {
      const byKey = new Map<string, SolarBody>(
        manifest.bodies.map((b) => [b.key, b]),
      );
      for (const b of manifest.bodies) {
        if (!b.orbit) continue;
        const rgb = hexToRgb(b.colour);
        const selected =
          b.key === input.selected.value || b.key === hovered.value;
        const fade = selected ? 1 : b.kind === "moon" ? 0.35 : 0.55;
        // A moon's ellipse is drawn around its planet's warped position; a planet's around the Sun.
        let centre: Vec3 = [0, 0, 0];
        if (b.parent) {
          const parent = byKey.get(b.parent);
          if (!parent) continue;
          const ph = heliocentricAt(byKey, parent, jd);
          const pl = parent.parent
            ? localPositionAt(parent, jd)
            : ([0, 0, 0] as Vec3);
          centre = scenePosition(
            ph,
            pl,
            input.warp.value,
            input.moonScale.value,
          );
        }
        const path = orbitPath(b.orbit, jd, b.kind === "moon" ? 96 : 256);
        const localScale = b.parent ? input.moonScale.value : 1;
        for (let i = 0; i < path.length / 3 - 1; i++) {
          for (const j of [i, i + 1]) {
            const raw: Vec3 = [path[j * 3], path[j * 3 + 1], path[j * 3 + 2]];
            const world: Vec3 = b.parent
              ? [
                  centre[0] + raw[0] * localScale,
                  centre[1] + raw[1] * localScale,
                  centre[2] + raw[2] * localScale,
                ]
              : (() => {
                  const w = scenePosition(raw, [0, 0, 0], input.warp.value, 1);
                  return w;
                })();
            verts.push(
              world[0] - eye[0],
              world[1] - eye[1],
              world[2] - eye[2],
              rgb[0],
              rgb[1],
              rgb[2],
              fade,
            );
          }
        }
      }
    }

    if (input.showAxes.value) {
      for (const p of placed) {
        if (p.body.kind === "star") continue;
        const { z } = bodyBasis(orientationAt(p.body.pole, jd));
        const len = p.radius * 2.2;
        const rgb = hexToRgb(p.body.colour);
        verts.push(
          p.rel[0] - z[0] * len,
          p.rel[1] - z[1] * len,
          p.rel[2] - z[2] * len,
          rgb[0],
          rgb[1],
          rgb[2],
          0.85,
          p.rel[0] + z[0] * len,
          p.rel[1] + z[1] * len,
          p.rel[2] + z[2] * len,
          rgb[0],
          rgb[1],
          rgb[2],
          0.85,
        );
      }
    }

    lineCount = verts.length / 7;
    if (!lineCount) return;

    gl.useProgram(lineProg);
    gl.bindVertexArray(lineVAO);
    gl.bindBuffer(gl.ARRAY_BUFFER, lineBuf);
    gl.bufferData(gl.ARRAY_BUFFER, new Float32Array(verts), gl.DYNAMIC_DRAW);
    gl.uniformMatrix4fv(lineU.uViewProj, false, viewProj);
    gl.uniform1f(lineU.uOpacity, 0.85);
    gl.uniform1f(lineU.uFC, fc);
    gl.drawArrays(gl.LINES, 0, lineCount);
  }

  function loop() {
    if (dirty) {
      dirty = false;
      draw();
    }
    raf = requestAnimationFrame(loop);
  }

  // --- interaction -------------------------------------------------------------------------------

  const pointers = new Map<number, { x: number; y: number }>();
  let dragging = false;
  let moved = 0;
  let lastX = 0;
  let lastY = 0;
  let pinchDist = 0;
  let lastWheel = 0;

  function onPointerDown(e: PointerEvent) {
    el?.setPointerCapture(e.pointerId);
    pointers.set(e.pointerId, { x: e.clientX, y: e.clientY });
    dragging = true;
    moved = 0;
    lastX = e.clientX;
    lastY = e.clientY;
    hovered.value = null;
  }

  function onPointerMove(e: PointerEvent) {
    if (!dragging) {
      updateHover(e);
      return;
    }
    pointers.set(e.pointerId, { x: e.clientX, y: e.clientY });

    if (pointers.size === 2) {
      const [a, b] = [...pointers.values()];
      const d = Math.hypot(a.x - b.x, a.y - b.y);
      if (pinchDist > 0) {
        // A pinch is fed in as an equivalent wheel delta, so one velocity curve serves every device.
        applyWheel((pinchDist - d) * 2, 16);
      }
      pinchDist = d;
      return;
    }

    const dx = e.clientX - lastX;
    const dy = e.clientY - lastY;
    lastX = e.clientX;
    lastY = e.clientY;
    moved += Math.abs(dx) + Math.abs(dy);

    if (e.shiftKey || e.buttons === 4) {
      // Panning breaks the follow lock: you asked to look somewhere else.
      pan(dx, dy);
    } else {
      const o = orbit.value;
      const perPx = (2 * Math.PI) / Math.max(1, el?.clientHeight ?? 1);
      orbit.value = {
        ...o,
        yaw: o.yaw - dx * perPx,
        pitch: Math.max(
          -PITCH_LIMIT,
          Math.min(PITCH_LIMIT, o.pitch + dy * perPx),
        ),
      };
    }
    requestDraw();
  }

  function pan(dxPx: number, dyPx: number) {
    const o = orbit.value;
    const h = el?.clientHeight ?? 1;
    const perPx = (2 * tanHalfH * o.distance) / h;
    const cp = Math.cos(o.pitch);
    const sp = Math.sin(o.pitch);
    const cy = Math.cos(o.yaw);
    const sy = Math.sin(o.yaw);
    // The camera's own right and up axes in the ecliptic frame.
    const right: Vec3 = [cy, sy, 0];
    const up: Vec3 = [-sy * sp, cy * sp, cp];
    const ar = -dxPx * perPx;
    const au = dyPx * perPx;
    orbit.value = {
      ...o,
      target: [
        o.target[0] + ar * right[0] + au * up[0],
        o.target[1] + ar * right[1] + au * up[1],
        o.target[2] + ar * right[2] + au * up[2],
      ],
    };
  }

  function onPointerUp(e: PointerEvent) {
    pointers.delete(e.pointerId);
    if (pointers.size < 2) pinchDist = 0;
    if (pointers.size === 0) dragging = false;
    if (moved < 5) {
      const hit = pickAt(e);
      input.selected.value = hit;
      requestDraw();
    }
  }

  function onPointerLeave() {
    hovered.value = null;
    dragging = false;
    pointers.clear();
    requestDraw();
  }

  function applyWheel(deltaY: number, dt: number) {
    // The closest the camera may go is a hundred and fifty kilometres, so a moon can be approached
    // rather than merely looked at.
    orbit.value = applyZoom(orbit.value, zoomExponent(deltaY, dt), 400, 1e-6);
    requestDraw();
  }

  function onWheel(e: WheelEvent) {
    e.preventDefault();
    const now = performance.now();
    const dt = lastWheel ? now - lastWheel : 16;
    lastWheel = now;
    applyWheel(e.ctrlKey ? e.deltaY * 4 : e.deltaY, dt);
  }

  /** PICK_SLACK_PX is how far off a body a click may land and still count — small worlds need it. */
  const PICK_SLACK_PX = 12;

  function pickAt(e: PointerEvent): string | null {
    const rect = el?.getBoundingClientRect();
    if (!rect) return null;
    const x = e.clientX - rect.left;
    const y = e.clientY - rect.top;
    let best: string | null = null;
    let bestD = Infinity;
    for (const d of drawn.value) {
      if (!d.screen) continue;
      const dist = Math.hypot(d.screen[0] - x, d.screen[1] - y);
      if (dist > Math.max(d.radiusPx, 0) + PICK_SLACK_PX) continue;
      // Nearest to the pointer wins, not nearest to the camera: a moon in front of its planet is
      // what you were aiming at.
      if (dist < bestD) {
        bestD = dist;
        best = d.key;
      }
    }
    return best;
  }

  function updateHover(e: PointerEvent) {
    const hit = pickAt(e);
    if (hit !== hovered.value) {
      hovered.value = hit;
      requestDraw();
    }
  }

  /** frame points the camera at a body and pulls back far enough to see it whole. */
  function frameBody(key: string) {
    const manifest = input.manifest.value;
    if (!manifest) return;
    const body = manifest.bodies.find((b) => b.key === key);
    if (!body) return;
    input.follow.value = key;
    const rAU = body.radius_km * AU_PER_KM * input.exaggerate.value;
    // Far enough that the body fills about a third of the frame, and never closer than the floor.
    orbit.value = {
      ...orbit.value,
      distance: Math.max(1e-6, (rAU / tanHalfH) * 3.2),
    };
    requestDraw();
  }

  /**
   * systemRadius is how far out the drawn scene reaches: the outermost heliocentric orbit's
   * aphelion, in the CURRENT warped space. It moves with the distance slider, which is the point —
   * a framing computed in true space would leave the system a dot once the view is compressed.
   */
  function systemRadius(): number {
    let far = 1;
    for (const b of input.manifest.value?.bodies ?? []) {
      if (!b.orbit || b.parent) continue;
      far = Math.max(
        far,
        radialWarp(b.orbit.a_au * (1 + b.orbit.e), input.warp.value),
      );
    }
    return far;
  }

  /** home restores the opening view: the whole planetary system, seen from above the ecliptic. */
  function home() {
    input.follow.value = null;
    orbit.value = {
      target: [0, 0, 0],
      // Far enough that the outermost orbit clears the frame's short axis, with a little air.
      distance: (systemRadius() / tanHalfH) * 1.15,
      yaw: 0,
      pitch: 0.62,
      roll: 0,
    };
    requestDraw();
  }

  // The opening view has to wait for the model: until the manifest lands there is no outermost orbit
  // to frame. Framing once, on the first manifest, leaves later changes to the user.
  let framed = false;
  watch(
    input.manifest,
    (m) => {
      if (!m || framed) return;
      framed = true;
      home();
    },
    { immediate: true },
  );

  // --- lifecycle ---------------------------------------------------------------------------------

  watch(
    canvas,
    (node) => {
      if (!node || gl) return;
      el = node;
      if (!init()) return;
      node.addEventListener("pointerdown", onPointerDown);
      node.addEventListener("pointermove", onPointerMove);
      node.addEventListener("pointerup", onPointerUp);
      node.addEventListener("pointercancel", onPointerUp);
      node.addEventListener("pointerleave", onPointerLeave);
      node.addEventListener("wheel", onWheel, { passive: false });
      raf = requestAnimationFrame(loop);
      requestDraw();
    },
    { immediate: true },
  );

  watch(
    [
      input.manifest,
      input.timeMs,
      input.warp,
      input.moonScale,
      input.exaggerate,
      input.showOrbits,
      input.showAxes,
      input.showStars,
      input.showLabels,
      input.follow,
      input.selected,
    ],
    requestDraw,
  );

  onBeforeUnmount(() => {
    cancelAnimationFrame(raf);
    observer?.disconnect();
    observer = null;
    if (el) {
      el.removeEventListener("pointerdown", onPointerDown);
      el.removeEventListener("pointermove", onPointerMove);
      el.removeEventListener("pointerup", onPointerUp);
      el.removeEventListener("pointercancel", onPointerUp);
      el.removeEventListener("pointerleave", onPointerLeave);
      el.removeEventListener("wheel", onWheel);
    }
    if (gl) {
      for (const t of textures.values()) gl.deleteTexture(t);
      textures.clear();
      if (whiteTex) gl.deleteTexture(whiteTex);
      for (const p of [bodyProg, ringProg, lineProg, skyProg, glowProg]) {
        if (p) gl.deleteProgram(p);
      }
      for (const v of [sphereVAO, ringVAO, skyVAO, lineVAO, glowVAO]) {
        if (v) gl.deleteVertexArray(v);
      }
      if (lineBuf) gl.deleteBuffer(lineBuf);
    }
    gl = null;
    el = null;
  });

  return {
    supported,
    hovered,
    drawn,
    orbit,
    eyeDistanceAU,
    requestDraw,
    frameBody,
    home,
  };
}
