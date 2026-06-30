import { imageOverlay, latLngBounds, type ImageOverlay } from "leaflet";
import type { WeatherGrid } from "@/types";
import type { GridLayerDef } from "@/utils/weather";

// createWeatherGridLayer wraps a Leaflet imageOverlay backed by a tiny per-cell canvas. The canvas is
// nx×ny pixels (one per grid cell) and the browser smooths it up to the overlay bounds, so a coarse
// weather grid renders as a soft heatmap. setFrame() repaints it — that is how the cloud map "plays".
export function createWeatherGridLayer(opacity = 0.7) {
  const canvas = document.createElement("canvas");
  let layer: ImageOverlay | null = null;

  function clamp01(x: number): number {
    return x < 0 ? 0 : x > 1 ? 1 : x;
  }

  function render(grid: WeatherGrid, frame: number, def: GridLayerDef): string {
    const { nx, ny } = grid;
    canvas.width = nx;
    canvas.height = ny;
    const ctx = canvas.getContext("2d");
    const cells = grid.layers[def.metric]?.[frame];
    if (!ctx || !cells) return canvas.toDataURL();
    const img = ctx.createImageData(nx, ny);
    for (let i = 0; i < nx * ny; i++) {
      const [r, g, b, a] = def.color(cells[i] ?? 0);
      img.data[i * 4] = r;
      img.data[i * 4 + 1] = g;
      img.data[i * 4 + 2] = b;
      img.data[i * 4 + 3] = Math.round(clamp01(a) * 255);
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
