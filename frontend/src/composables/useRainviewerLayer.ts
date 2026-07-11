import { type TileLayer, type LeafletEvent } from "leaflet";
import { createFrameTileLayer } from "@/composables/useFrameTileLayer";

// RainViewer live tiles. The maps JSON gives a per-frame `path`; a tile URL is
//   {host}{path}/{size}/{z}/{x}/{y}/{color}/{smooth}_{snow}.png
// Radar colour scheme 4 is a clear multi-hue precipitation ramp (light blue → red) that reads well over
// the dark base map; satellite IR uses scheme 0 (grey cloud-top temperature). Size 512 keeps tiles crisp.
export type RvProduct = "radar" | "satellite";

const RV_SIZE = 512;
const RADAR_COLOR = 4;
const RADAR_OPTS = "1_1"; // {smooth}_{snow}: smoothed, snow shown
const SAT_COLOR = 0;
const SAT_OPTS = "0_0";
// RainViewer serves tiles only up to zoom 7 — beyond that it returns a "zoom level not supported"
// placeholder. Cap the native zoom so Leaflet upscales the z7 tile for closer views instead (as
// RainViewer's own map does), keeping the live layer visible when zoomed into a small area.
const RV_MAX_NATIVE_ZOOM = 7;

export function rvTileUrl(
  host: string,
  path: string,
  product: RvProduct,
): string {
  const color = product === "radar" ? RADAR_COLOR : SAT_COLOR;
  const opts = product === "radar" ? RADAR_OPTS : SAT_OPTS;
  return `${host}${path}/${RV_SIZE}/{z}/{x}/{y}/${color}/${opts}.png`;
}

// RADAR_GRADIENT approximates RainViewer colour scheme 4 (light → heavy precipitation) for the map
// legend bar. Kept in JS (not the template) per the design-system rule that gradient stops live in code.
export const RADAR_GRADIENT = [
  "rgba(120,180,255,0)",
  "rgba(90,190,120,0.85)",
  "rgba(240,225,90,0.9)",
  "rgba(240,150,60,0.92)",
  "rgba(225,70,70,0.95)",
];

// createRainviewerLayer is a thin RainViewer-specific adapter over the shared frame-swappable tile layer:
// it computes the per-frame RainViewer URL (host + path → tile template) and swaps it in. `onTileError`
// lets the caller self-heal transient tile failures.
export function createRainviewerLayer(
  product: RvProduct,
  opacity: number,
  onTileError?: (e: LeafletEvent) => void,
) {
  const frameLayer = createFrameTileLayer({
    opacity,
    attribution: "RainViewer",
    maxNativeZoom: RV_MAX_NATIVE_ZOOM, // upscale past RainViewer's z7 cap (no "zoom not supported")
    className: "rv-live-tiles",
    onTileError,
  });
  return {
    // update points the layer at the given live frame (host + path), creating it on first call.
    update(host: string, path: string): TileLayer {
      return frameLayer.update(rvTileUrl(host, path, product));
    },
    get layer(): TileLayer | null {
      return frameLayer.layer;
    },
  };
}
