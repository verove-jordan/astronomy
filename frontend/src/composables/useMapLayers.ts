import { reactive } from "vue";
import { BASE } from "@/services/api";
import type { RvProduct } from "@/composables/useRainviewerLayer";

// MapOverlay describes one toggleable map overlay. This registry is the extensibility seam: light
// pollution is a backend-proxied static XYZ tile layer (kind "tile"); the animated forecast layers are
// server-rendered weather-metric tiles (kind "weather") whose `{time}` frame is swapped by the scrubber;
// live radar/satellite are direct RainViewer XYZ tiles swapped per frame (kind "rainviewer"). The
// LocationPicker's layer control and persistence handle them generically.
export interface MapOverlay {
  id: string;
  labelKey: string; // i18n key for the toggle label
  kind?: "tile" | "weather" | "rainviewer"; // default "tile"
  url?: string; // tile overlays: Leaflet XYZ tile template
  metric?: string; // weather overlays: the metric rendered server-side (clouds/humidity/precip)
  product?: RvProduct; // rainviewer overlays: which live product
  live?: boolean; // real-time observation (vs forecast) — grouped + badged apart in the UI
  opacity: number;
  attribution: string;
  legend?: "bortle" | "radar"; // tile-overlay legend the picker shows when on (weather → WeatherLegend)
  maxNativeZoom?: number; // tile overlays: highest zoom the source has tiles for
}

// Tiles are proxied through the backend so any provider API key stays server-side. Weather-metric layers
// are rendered into PNG map tiles by the backend (from one Open-Meteo cube) and animate over a shared time
// scrubber — the browser only composites the tiles, doing zero per-pixel render work.
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
    kind: "weather",
    metric: "clouds",
    labelKey: "tonight.layers.clouds",
    opacity: 0.9,
    attribution: "Weather: Open-Meteo",
  },
  {
    id: "humidity",
    kind: "weather",
    metric: "humidity",
    labelKey: "tonight.layers.humidity",
    opacity: 0.6,
    attribution: "Weather: Open-Meteo",
  },
  {
    id: "precip",
    kind: "weather",
    metric: "precip",
    labelKey: "tonight.layers.precip",
    opacity: 0.8,
    attribution: "Weather: Open-Meteo",
  },
  {
    // Live precipitation radar (real observation, not forecast) — RainViewer past+nowcast frames, keyless
    // and CORS-enabled, swapped per animation frame. Answers "is rain reaching me right now?".
    id: "radar",
    kind: "rainviewer",
    product: "radar",
    labelKey: "tonight.layers.radar",
    live: true,
    opacity: 0.75,
    attribution: "Radar: RainViewer",
    legend: "radar",
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
  // anyWeatherEnabled gates the lightweight frames-index fetch (only the animated weather layers need it).
  function anyWeatherEnabled(): boolean {
    return OVERLAYS.some((o) => o.kind === "weather" && enabled.has(o.id));
  }
  // anyRainviewerEnabled gates the RainViewer live-frame fetch + refresh loop.
  function anyRainviewerEnabled(): boolean {
    return OVERLAYS.some((o) => o.kind === "rainviewer" && enabled.has(o.id));
  }
  // anyAnimatedEnabled drives whether the time scrubber is shown (weather + live layers both animate).
  function anyAnimatedEnabled(): boolean {
    return anyWeatherEnabled() || anyRainviewerEnabled();
  }
  return {
    overlays: OVERLAYS,
    isEnabled,
    toggle,
    anyWeatherEnabled,
    anyRainviewerEnabled,
    anyAnimatedEnabled,
  };
}
