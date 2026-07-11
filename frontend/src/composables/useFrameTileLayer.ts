import { tileLayer, type TileLayer, type LeafletEvent } from "leaflet";

// A frame-swappable tile layer: one Leaflet TileLayer whose `{z}/{x}/{y}` template URL is swapped per
// animation frame (via setUrl). Tiles for already-visited frames are served from Leaflet's cache + the
// server's long cache headers, so looping playback is smooth after the first pass. keepBuffer holds nearby
// tiles so a frame swap doesn't blank the map. This is the shared engine behind the live RainViewer radar
// (useRainviewerLayer) and the server-rendered weather-metric overlays (clouds/humidity/precip): the caller
// computes the full template URL for the current frame and calls update(); the browser only composites.
export interface FrameTileOptions {
  opacity: number;
  attribution: string;
  maxZoom?: number;
  maxNativeZoom?: number; // highest zoom the source has tiles for (Leaflet upscales beyond it)
  keepBuffer?: number;
  className?: string;
  onTileError?: (e: LeafletEvent) => void;
}

export interface FrameTileLayer {
  // update points the layer at the given frame's template URL, creating it on first call. A URL identical
  // to the current one is a no-op (so a redundant repaint doesn't churn tiles).
  update(url: string): TileLayer;
  readonly layer: TileLayer | null;
}

export function createFrameTileLayer(opts: FrameTileOptions): FrameTileLayer {
  let layer: TileLayer | null = null;
  let lastUrl = "";
  return {
    update(url: string): TileLayer {
      if (!layer) {
        layer = tileLayer(url, {
          opacity: opts.opacity,
          attribution: opts.attribution,
          maxZoom: opts.maxZoom ?? 19,
          maxNativeZoom: opts.maxNativeZoom,
          keepBuffer: opts.keepBuffer ?? 4,
          updateWhenIdle: false,
          className: opts.className,
        });
        if (opts.onTileError) layer.on("tileerror", opts.onTileError);
      } else if (url !== lastUrl) {
        layer.setUrl(url);
      }
      lastUrl = url;
      return layer;
    },
    get layer(): TileLayer | null {
      return layer;
    },
  };
}
