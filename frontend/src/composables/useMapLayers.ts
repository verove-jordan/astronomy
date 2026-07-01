import { reactive } from "vue";
import { BASE } from "@/services/api";

// MapOverlay describes one toggleable map overlay. This registry is the extensibility seam: light
// pollution is a backend-proxied XYZ tile layer (kind "tile"); the animated weather layers are gridded
// canvas overlays (kind "grid") painted from the weather store's cube. The LocationPicker's layer
// control and persistence handle both generically.
export interface MapOverlay {
  id: string;
  labelKey: string; // i18n key for the toggle label
  kind?: "tile" | "grid"; // default "tile"
  url?: string; // tile overlays: Leaflet XYZ tile template
  metric?: string; // grid overlays: the weather grid layer key (clouds/humidity/precip)
  opacity: number;
  attribution: string;
  legend?: "bortle"; // tile-overlay legend component the picker shows when on (grid → WeatherLegend)
  maxNativeZoom?: number; // tile overlays: highest zoom the source has tiles for
}

// Tiles are proxied through the backend so any provider API key stays server-side. Weather grid layers
// are rendered client-side from a single Open-Meteo cube and animate over a shared time scrubber.
const OVERLAYS: MapOverlay[] = [
  {
    id: "lightPollution",
    kind: "tile",
    labelKey: "tonight.layers.lightPollution",
    // ?style busts stale browser-cached tiles when the rendering changes (the server ignores the query;
    // bump it in lockstep with the backend coloredCacheVersion). v3 = tiles rendered from the offline atlas.
    url: `${BASE}/api/sky/lightpollution/tiles/{z}/{x}/{y}?style=bortle-v3`,
    opacity: 0.6,
    attribution: "Light pollution: VIIRS (NASA/NOAA)",
    legend: "bortle",
    maxNativeZoom: 8, // GIBS Black Marble tops out at z8; Leaflet upscales it beyond that
  },
  {
    id: "clouds",
    kind: "grid",
    metric: "clouds",
    labelKey: "tonight.layers.clouds",
    opacity: 0.8,
    attribution: "Weather: Open-Meteo",
  },
  {
    id: "humidity",
    kind: "grid",
    metric: "humidity",
    labelKey: "tonight.layers.humidity",
    opacity: 0.6,
    attribution: "Weather: Open-Meteo",
  },
  {
    id: "precip",
    kind: "grid",
    metric: "precip",
    labelKey: "tonight.layers.precip",
    opacity: 0.8,
    attribution: "Weather: Open-Meteo",
  },
];

const ENABLED_KEY = "astrostack.map.layers";

function loadEnabled(): Set<string> {
  try {
    const raw = localStorage.getItem(ENABLED_KEY);
    return new Set(raw ? (JSON.parse(raw) as string[]) : []);
  } catch {
    return new Set();
  }
}

// Module-level reactive state so every map shares one set of enabled overlays (persisted across reloads).
const enabled = reactive(loadEnabled());

export function useMapLayers() {
  function isEnabled(id: string): boolean {
    return enabled.has(id);
  }
  function toggle(id: string): void {
    if (enabled.has(id)) enabled.delete(id);
    else enabled.add(id);
    try {
      localStorage.setItem(ENABLED_KEY, JSON.stringify([...enabled]));
    } catch {
      // ignore quota / private-mode errors
    }
  }
  // anyGridEnabled drives whether the time scrubber is shown (only the grid layers animate).
  function anyGridEnabled(): boolean {
    return OVERLAYS.some((o) => o.kind === "grid" && enabled.has(o.id));
  }
  return { overlays: OVERLAYS, isEnabled, toggle, anyGridEnabled };
}
