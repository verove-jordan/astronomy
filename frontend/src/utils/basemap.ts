import { tileLayer, type Map as LMap } from "leaflet";

// CartoDB "Dark Matter" — a free, no-key, near-black basemap. On a light OSM base the weather overlays
// (pale clouds, translucent humidity) and the light-pollution glow washed out to meaningless tints; over
// this dark cartography they read like a real weather / night-sky map, and it matches the app's dark
// theme. Labels are baked in so city/coastline context stays visible under the overlays.
const CARTO_DARK_URL =
  "https://{s}.basemaps.cartocdn.com/dark_all/{z}/{x}/{y}{r}.png";
const CARTO_ATTR =
  '© <a href="https://www.openstreetmap.org/copyright">OpenStreetMap</a>, © <a href="https://carto.com/attributions">CARTO</a>';

// addDarkBaseMap installs the dark base layer on a Leaflet map. Call it once, right after creating the
// map and before adding overlays/markers (which sit above it in their own panes).
export function addDarkBaseMap(map: LMap): void {
  tileLayer(CARTO_DARK_URL, {
    attribution: CARTO_ATTR,
    subdomains: "abcd",
    maxZoom: 20,
  }).addTo(map);
}
