import { imageOverlay, latLngBounds, type ImageOverlay } from "leaflet";
import type { WeatherGrid } from "@/types";
import type { GridBandDef, GridLayerDef, RGBA } from "@/utils/weather";

// Long-side pixel bounds of the interpolated overlay image. The caller passes a target derived from
// the on-screen map size so Leaflet never CSS-upscales a small canvas into blur; it is clamped to keep
// the per-pixel colour mapping affordable.
const OVERLAY_MIN_PX = 384;
const OVERLAY_MAX_PX = 1024;

// Rendered frames are memoized as data URLs (LRU, per layer) so scrubbing / looping playback repaints
// from cache instead of re-interpolating, and the next frame is pre-rendered during browser idle time.
const FRAME_CACHE_MAX = 16;

// 8×8 Bayer threshold matrix (values 0..63) for ordered alpha dithering: a smooth alpha ramp quantized
// to 8 bits bands visibly over large soft cloud fields; ±2/255 of spatially-ordered noise breaks the
// contours up without changing the mean intensity.
// prettier-ignore
const BAYER8 = [
   0, 32,  8, 40,  2, 34, 10, 42,
  48, 16, 56, 24, 50, 18, 58, 26,
  12, 44,  4, 36, 14, 46,  6, 38,
  60, 28, 52, 20, 62, 30, 54, 22,
   3, 35, 11, 43,  1, 33,  9, 41,
  51, 19, 59, 27, 49, 17, 57, 25,
  15, 47,  7, 39, 13, 45,  5, 37,
  63, 31, 55, 23, 61, 29, 53, 21,
];

function clamp01(x: number): number {
  return x < 0 ? 0 : x > 1 ? 1 : x;
}

// ditherAlpha quantizes a 0..1 alpha to a byte, applying the pixel's Bayer offset (±2/255) first.
function ditherAlpha(a: number, ox: number, oy: number): number {
  const offset = ((BAYER8[(oy & 7) * 8 + (ox & 7)] + 0.5) / 64 - 0.5) * 4;
  const v = Math.round(clamp01(a) * 255 + offset);
  return v < 0 ? 0 : v > 255 ? 255 : v;
}

interface AxisTaps {
  idx: Int32Array; // 4 edge-clamped source indices per output sample
  w: Float32Array; // the matching Catmull-Rom weights
}

// catmullTaps precomputes, per output sample along one axis, the four source indices (edge-clamped)
// and Catmull-Rom weights of the bicubic kernel, so the two interpolation passes are pure multiply-adds.
function catmullTaps(outN: number, gridN: number, k: number): AxisTaps {
  const idx = new Int32Array(outN * 4);
  const w = new Float32Array(outN * 4);
  for (let o = 0; o < outN; o++) {
    const g = (o + 0.5) / k - 0.5; // output pixel centre → grid cell-centre coords
    const base = Math.floor(g);
    const t = g - base;
    const t2 = t * t;
    const t3 = t2 * t;
    w[o * 4] = 0.5 * (-t3 + 2 * t2 - t);
    w[o * 4 + 1] = 0.5 * (3 * t3 - 5 * t2 + 2);
    w[o * 4 + 2] = 0.5 * (-3 * t3 + 4 * t2 + t);
    w[o * 4 + 3] = 0.5 * (t3 - t2);
    for (let j = 0; j < 4; j++) {
      const s = base - 1 + j;
      idx[o * 4 + j] = s < 0 ? 0 : s >= gridN ? gridN - 1 : s;
    }
  }
  return { idx, w };
}

// upsampleField resamples the nx×ny value grid to outW×outH with a separable (16-tap) Catmull-Rom
// bicubic — smooth AND locally faithful, where bilinear looked faceted. The result is clamped to the
// metric's [0,100] domain: Catmull-Rom overshoots at sharp fronts, which would otherwise colour-map
// into bright/dark halos around cloud edges.
function upsampleField(
  cells: ArrayLike<number>,
  nx: number,
  ny: number,
  xt: AxisTaps,
  yt: AxisTaps,
  outW: number,
  outH: number,
): Float32Array {
  const mid = new Float32Array(ny * outW); // horizontal pass: grid rows → outW columns
  for (let y = 0; y < ny; y++) {
    const row = y * nx;
    const midRow = y * outW;
    for (let ox = 0; ox < outW; ox++) {
      const o4 = ox * 4;
      mid[midRow + ox] =
        (cells[row + xt.idx[o4]] ?? 0) * xt.w[o4] +
        (cells[row + xt.idx[o4 + 1]] ?? 0) * xt.w[o4 + 1] +
        (cells[row + xt.idx[o4 + 2]] ?? 0) * xt.w[o4 + 2] +
        (cells[row + xt.idx[o4 + 3]] ?? 0) * xt.w[o4 + 3];
    }
  }
  const out = new Float32Array(outW * outH); // vertical pass + domain clamp
  for (let oy = 0; oy < outH; oy++) {
    const o4 = oy * 4;
    const r0 = yt.idx[o4] * outW;
    const r1 = yt.idx[o4 + 1] * outW;
    const r2 = yt.idx[o4 + 2] * outW;
    const r3 = yt.idx[o4 + 3] * outW;
    const w0 = yt.w[o4];
    const w1 = yt.w[o4 + 1];
    const w2 = yt.w[o4 + 2];
    const w3 = yt.w[o4 + 3];
    const outRow = oy * outW;
    for (let ox = 0; ox < outW; ox++) {
      const v =
        mid[r0 + ox] * w0 +
        mid[r1 + ox] * w1 +
        mid[r2 + ox] * w2 +
        mid[r3 + ox] * w3;
      out[outRow + ox] = v < 0 ? 0 : v > 100 ? 100 : v;
    }
  }
  return out;
}

// paintSingle colour-maps one upsampled field into the image buffer (the classic single-ramp path).
function paintSingle(
  data: Uint8ClampedArray,
  field: Float32Array,
  color: (v: number) => RGBA,
  outW: number,
  outH: number,
): void {
  for (let oy = 0; oy < outH; oy++) {
    for (let ox = 0; ox < outW; ox++) {
      const p = oy * outW + ox;
      const [r, g, b, a] = color(field[p]);
      const i = p * 4;
      data[i] = r;
      data[i + 1] = g;
      data[i + 2] = b;
      data[i + 3] = ditherAlpha(a, ox, oy);
    }
  }
}

// paintBands source-over composites the altitude bands per pixel, bands[0] at the bottom (high → mid →
// low for clouds, so the dense low deck covers the thin cirrus veil). Accumulation is premultiplied;
// ImageData wants straight RGBA, so colour is divided back out before writing.
function paintBands(
  data: Uint8ClampedArray,
  fields: Float32Array[],
  bands: GridBandDef[],
  outW: number,
  outH: number,
): void {
  for (let oy = 0; oy < outH; oy++) {
    for (let ox = 0; ox < outW; ox++) {
      const p = oy * outW + ox;
      let r = 0;
      let g = 0;
      let b = 0;
      let a = 0;
      for (let n = 0; n < bands.length; n++) {
        const [br, bg, bb, ba] = bands[n].color(fields[n][p]);
        const sa = clamp01(ba);
        const inv = 1 - sa;
        r = br * sa + r * inv;
        g = bg * sa + g * inv;
        b = bb * sa + b * inv;
        a = sa + a * inv;
      }
      const i = p * 4;
      if (a > 0) {
        data[i] = Math.round(r / a);
        data[i + 1] = Math.round(g / a);
        data[i + 2] = Math.round(b / a);
      }
      data[i + 3] = ditherAlpha(a, ox, oy);
    }
  }
}

// createWeatherGridLayer wraps a Leaflet imageOverlay backed by a canvas. The coarse nx×ny weather grid
// is bicubically interpolated to a viewport-sized canvas and colour-mapped per pixel (compositing the
// per-altitude bands when the cube carries them), so it renders as a smooth, intensity-true raster.
// update() repaints it — that is how the cloud map "plays".
export function createWeatherGridLayer(opacity = 0.7) {
  const canvas = document.createElement("canvas");
  let layer: ImageOverlay | null = null;
  const frameCache = new Map<string, string>(); // insertion-ordered → doubles as LRU recency

  function outSize(grid: WeatherGrid, targetPx: number) {
    const k = Math.max(1, Math.floor(targetPx / Math.max(grid.nx, grid.ny)));
    return { k, outW: grid.nx * k, outH: grid.ny * k };
  }

  // renderableBands returns the layer's altitude bands when the cube carries every band metric for the
  // frame; otherwise undefined (older cubes → single-metric fallback on def.color).
  function renderableBands(
    grid: WeatherGrid,
    frame: number,
    def: GridLayerDef,
  ): GridBandDef[] | undefined {
    const bands = def.bands;
    if (!bands?.length) return undefined;
    return bands.every((b) => grid.layers[b.metric]?.[frame])
      ? bands
      : undefined;
  }

  // render interpolates the frame's value grid(s) to the target size and colour-maps each output pixel,
  // so smooth value gradients (not blocky cells) reach the colour ramps. Returns a PNG data URL.
  function render(
    grid: WeatherGrid,
    frame: number,
    def: GridLayerDef,
    targetPx: number,
  ): string {
    const { nx, ny } = grid;
    const ctx = canvas.getContext("2d");
    if (!ctx || nx < 1 || ny < 1) return canvas.toDataURL();
    const bands = renderableBands(grid, frame, def);
    const total = grid.layers[def.metric]?.[frame];
    if (!bands && !total) return canvas.toDataURL();
    const { k, outW, outH } = outSize(grid, targetPx);
    canvas.width = outW;
    canvas.height = outH;
    const xt = catmullTaps(outW, nx, k);
    const yt = catmullTaps(outH, ny, k);
    const img = ctx.createImageData(outW, outH);
    if (bands) {
      const fields = bands.map((b) =>
        upsampleField(
          grid.layers[b.metric]?.[frame] ?? [],
          nx,
          ny,
          xt,
          yt,
          outW,
          outH,
        ),
      );
      paintBands(img.data, fields, bands, outW, outH);
    } else if (total) {
      paintSingle(
        img.data,
        upsampleField(total, nx, ny, xt, yt, outW, outH),
        def.color,
        outW,
        outH,
      );
    }
    ctx.putImageData(img, 0, 0);
    return canvas.toDataURL();
  }

  // renderCached memoizes rendered frames by (cube issue time, frame, layer, output size); a hit is
  // re-inserted so the Map's insertion order stays LRU.
  function renderCached(
    grid: WeatherGrid,
    frame: number,
    def: GridLayerDef,
    targetPx: number,
  ): string {
    const key = `${grid.issued_ms ?? 0}:${frame}:${def.id}:${outSize(grid, targetPx).outW}`;
    const hit = frameCache.get(key);
    if (hit !== undefined) {
      frameCache.delete(key);
      frameCache.set(key, hit);
      return hit;
    }
    const url = render(grid, frame, def, targetPx);
    frameCache.set(key, url);
    while (frameCache.size > FRAME_CACHE_MAX) {
      const oldest = frameCache.keys().next().value;
      if (oldest === undefined) break;
      frameCache.delete(oldest);
    }
    return url;
  }

  // prefetchNext pre-renders the frame playback will ask for next (it loops), during browser idle time,
  // so advancing the animation is a cache hit instead of a full re-interpolation.
  function prefetchNext(
    grid: WeatherGrid,
    frame: number,
    def: GridLayerDef,
    targetPx: number,
  ): void {
    if (typeof requestIdleCallback !== "function") return;
    // A degraded cube serializes its empty timestep list as null — same guard as the weather store.
    const frames = (grid.timesteps ?? []).length;
    if (frames < 2) return;
    const next = (frame + 1) % frames;
    requestIdleCallback(() => {
      renderCached(grid, next, def, targetPx);
    });
  }

  function boundsOf(grid: WeatherGrid) {
    const [w, s, e, n] = grid.bbox;
    return latLngBounds([s, w], [n, e]);
  }

  return {
    // update creates the Leaflet layer (first call) or repaints it to the given frame, and returns it.
    // targetPx is the wanted long-side resolution (usually the on-screen map size), clamped to
    // [OVERLAY_MIN_PX, OVERLAY_MAX_PX].
    update(
      grid: WeatherGrid,
      frame: number,
      def: GridLayerDef,
      targetPx?: number,
    ): ImageOverlay {
      const px = Math.min(
        OVERLAY_MAX_PX,
        Math.max(OVERLAY_MIN_PX, Math.round(targetPx ?? OVERLAY_MIN_PX)),
      );
      const url = renderCached(grid, frame, def, px);
      prefetchNext(grid, frame, def, px);
      const bounds = boundsOf(grid);
      if (!layer) {
        layer = imageOverlay(url, bounds, { opacity, interactive: false });
      } else {
        layer.setBounds(bounds);
        layer.setUrl(url);
      }
      return layer;
    },
    get layer(): ImageOverlay | null {
      return layer;
    },
  };
}
