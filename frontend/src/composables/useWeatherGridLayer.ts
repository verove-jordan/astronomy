import { imageOverlay, latLngBounds, type ImageOverlay } from "leaflet";
import type { WeatherGrid } from "@/types";
import type { GridLayerDef } from "@/utils/weather";

// Long-side pixels of the interpolated overlay image. We bilinear-upsample the coarse nx×ny grid to
// this size ourselves (in value space, then colour-map per pixel) so the raster looks smooth and
// detailed instead of a handful of browser-stretched blocks.
const OVERLAY_MAX_PX = 384;

// createWeatherGridLayer wraps a Leaflet imageOverlay backed by a canvas. The coarse nx×ny weather grid
// is bilinearly interpolated to a larger canvas and colour-mapped per pixel, so it renders as a smooth
// heatmap. setFrame() repaints it — that is how the cloud map "plays".
export function createWeatherGridLayer(opacity = 0.7) {
  const canvas = document.createElement("canvas");
  let layer: ImageOverlay | null = null;

  function clamp01(x: number): number {
    return x < 0 ? 0 : x > 1 ? 1 : x;
  }

  // sampleBilinear reads the grid value at fractional cell coords (gx, gy), clamped to the edges.
  function sampleBilinear(
    cells: ArrayLike<number>,
    nx: number,
    ny: number,
    gx: number,
    gy: number,
  ): number {
    const x0 = Math.max(0, Math.min(nx - 1, Math.floor(gx)));
    const y0 = Math.max(0, Math.min(ny - 1, Math.floor(gy)));
    const x1 = Math.min(nx - 1, x0 + 1);
    const y1 = Math.min(ny - 1, y0 + 1);
    const fx = clamp01(gx - x0);
    const fy = clamp01(gy - y0);
    const v00 = cells[y0 * nx + x0] ?? 0;
    const v10 = cells[y0 * nx + x1] ?? 0;
    const v01 = cells[y1 * nx + x0] ?? 0;
    const v11 = cells[y1 * nx + x1] ?? 0;
    const top = v00 + (v10 - v00) * fx;
    const bot = v01 + (v11 - v01) * fx;
    return top + (bot - top) * fy;
  }

  // render bilinearly upsamples the nx×ny value grid to ~OVERLAY_MAX_PX and colour-maps each output
  // pixel, so smooth value gradients (not blocky cells) reach the colour ramp. Returns a PNG data URL.
  function render(grid: WeatherGrid, frame: number, def: GridLayerDef): string {
    const { nx, ny } = grid;
    const cells = grid.layers[def.metric]?.[frame];
    const ctx = canvas.getContext("2d");
    if (!ctx || !cells || nx < 1 || ny < 1) return canvas.toDataURL();
    const k = Math.max(1, Math.floor(OVERLAY_MAX_PX / Math.max(nx, ny)));
    const outW = nx * k;
    const outH = ny * k;
    canvas.width = outW;
    canvas.height = outH;
    const img = ctx.createImageData(outW, outH);
    for (let oy = 0; oy < outH; oy++) {
      const gy = (oy + 0.5) / k - 0.5; // output pixel → grid cell-centre coords
      for (let ox = 0; ox < outW; ox++) {
        const gx = (ox + 0.5) / k - 0.5;
        const [r, g, b, a] = def.color(sampleBilinear(cells, nx, ny, gx, gy));
        const idx = (oy * outW + ox) * 4;
        img.data[idx] = r;
        img.data[idx + 1] = g;
        img.data[idx + 2] = b;
        img.data[idx + 3] = Math.round(clamp01(a) * 255);
      }
    }
    ctx.putImageData(img, 0, 0);
    return canvas.toDataURL();
  }

  function boundsOf(grid: WeatherGrid) {
    const [w, s, e, n] = grid.bbox;
    return latLngBounds([s, w], [n, e]);
  }

  return {
    // update creates the Leaflet layer (first call) or repaints it to the given frame, and returns it.
    update(grid: WeatherGrid, frame: number, def: GridLayerDef): ImageOverlay {
      const url = render(grid, frame, def);
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
