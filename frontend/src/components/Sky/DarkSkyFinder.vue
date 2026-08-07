<script setup lang="ts">
import { onMounted, onBeforeUnmount, ref, computed, watch } from "vue";
import { useI18n } from "vue-i18n";
import {
  map as createMap,
  tileLayer,
  rectangle,
  circleMarker,
  latLngBounds,
  type Map as LMap,
  type Rectangle,
  type TileLayer,
  type CircleMarker,
  type LeafletMouseEvent,
} from "leaflet";
import "leaflet/dist/leaflet.css";
import { addDarkBaseMap } from "@/utils/basemap";
import { useMapLayers } from "@/composables/useMapLayers";
import { useMapPinchZoom } from "@/composables/useMapPinchZoom";
import { useDarkSkyStore } from "@/stores/darksky";
import { useSkyStore } from "@/stores/sky";
import { useLightPollutionStore } from "@/stores/lightpollution";
import { useCanopyStore } from "@/stores/canopy";
import GenericTable, {
  type Column,
} from "@/components/Common/GenericTable.vue";
import LightPollutionLegend from "@/components/Sky/LightPollutionLegend.vue";
import BortleScalePicker from "@/components/Sky/BortleScalePicker.vue";
import NightPicker from "@/components/Sky/NightPicker.vue";
import IconCar from "@/components/Icons/IconCar.vue";
import IconCloud from "@/components/Icons/IconCloud.vue";
import { btnPrimary, btnGhost, input, checkbox } from "@/constants/styles";
import { bortleColor } from "@/utils/bortle";
import { formatTimestamp } from "@/utils/format";
import {
  dewRiskColor,
  goodBad,
  verdictColor,
  verdictLabel,
} from "@/utils/weather";
import { apiGet } from "@/services/api";
import type {
  AtlasBuildRequest,
  DarkSite,
  GeoResult,
  LocationFavorite,
} from "@/types";

// Find the darkest, most open observing sites in a drawn map area. The user draws a rectangle, picks a
// max Bortle, and (optionally) evaluates horizon openness; results land as a ranked table + markers.
const emit = defineEmits<{ useLocation: [lat: number, lon: number] }>();
const { t } = useI18n();
const store = useDarkSkyStore();
const sky = useSkyStore();
const lpStore = useLightPollutionStore();
const canopyStore = useCanopyStore();
const { overlays } = useMapLayers();

// The observer's location — the origin for the distance + driving-time columns. Prefer the site the user has
// set (persisted in the sky store), else the last server echo. null until a location is chosen: then no
// origin is sent and driving distance is not computed (rather than measured from the server's default site).
function observerLatLon(): { lat: number; lon: number } | null {
  const lat = sky.params.lat ?? sky.query?.location.lat;
  const lon = sky.params.lon ?? sky.query?.location.lon;
  return lat != null && lon != null ? { lat, lon } : null;
}

const mapEl = ref<HTMLDivElement | null>(null);
const maxBortle = ref(4);
const evalHorizon = ref(true);
// Weather is on by default: "where should I go on Saturday" is a question about the sky, and a ranking
// that answers it from terrain alone is confidently wrong whenever the darkest spot is clouded in.
const useWeather = ref(true);
const nightIndex = ref(0);
// How much of the score the forecast takes. Moving it re-ranks the results already on screen straight
// away and re-searches on a debounce, so dragging never fires a burst of forecast calls.
const weatherWeight = ref(0.3);
const NIGHT_COUNT = 7;
const drawing = ref(false);
const hasArea = ref(false);
const region = ref("france");
const canopyRegion = ref("custom"); // canopy default: the drawn area (smallest download)

// Bidirectional map↔table selection: the highlighted candidate's index in rankedCandidates (stable across
// table sorting). null = nothing selected. tableRef.scrollToKey reveals the row when a marker is clicked.
const tableRef = ref<{ scrollToKey: (k: string | number) => void } | null>(
  null,
);
const selectedIdx = ref<number | null>(null);

let lmap: LMap | null = null;
let lpLayer: TileLayer | null = null;
let areaRect: Rectangle | null = null;
let dragStart: { lat: number; lng: number } | null = null;
const markers: CircleMarker[] = [];
let homeMarker: CircleMarker | null = null; // the observer's location (distance/route origin)
let detachWheel: (() => void) | null = null;

// --- observing location (origin for the distance + driving-time columns): geocode search + GPS ---
const locSearch = ref("");
const locResults = ref<GeoResult[]>([]);
const locOpen = ref(false);
const locLabel = ref("");
const locBusy = ref(false);
let locTimer: ReturnType<typeof setTimeout> | null = null;

const observerLabel = computed(() => {
  if (locLabel.value) return locLabel.value;
  const h = observerLatLon();
  return h
    ? `${h.lat.toFixed(3)}, ${h.lon.toFixed(3)}`
    : t("darksky.location.notSet");
});

function onLocInput() {
  locOpen.value = true;
  if (locTimer) clearTimeout(locTimer);
  locTimer = setTimeout(runLocSearch, 350);
}
async function runLocSearch() {
  const q = locSearch.value.trim();
  if (!q) {
    locResults.value = [];
    return;
  }
  try {
    const data = await apiGet<{ results: GeoResult[] }>(
      `/api/sky/geocode?q=${encodeURIComponent(q)}`,
    );
    locResults.value = data.results ?? [];
  } catch {
    locResults.value = [];
  }
}
function chooseLoc(r: GeoResult) {
  locSearch.value = "";
  locResults.value = [];
  locOpen.value = false;
  locLabel.value = r.label;
  sky.setObserver(r.lat, r.lon);
  centerHome(r.lat, r.lon);
  if (store.searched && areaRect) search(); // refresh drives from the new origin
}
function useMyLocation() {
  if (!navigator.geolocation) return;
  locBusy.value = true;
  navigator.geolocation.getCurrentPosition(
    (pos) => {
      locBusy.value = false;
      locLabel.value = t("darksky.location.gpsLabel");
      sky.setObserver(pos.coords.latitude, pos.coords.longitude);
      centerHome(pos.coords.latitude, pos.coords.longitude);
      if (store.searched && areaRect) search(); // refresh drives from the new origin
    },
    () => {
      locBusy.value = false;
    },
    { enableHighAccuracy: false, timeout: 8000 },
  );
}
// Pick a saved favorite as the observing origin and focus the map on it (mirrors chooseLoc).
function useFavoriteLocation(fav: LocationFavorite) {
  locLabel.value = fav.label;
  sky.setObserver(fav.lat, fav.lon);
  centerHome(fav.lat, fav.lon);
  if (store.searched && areaRect) search(); // refresh drives from the new origin
}

// drawHome (re)draws the observer marker; centerHome also recenters the map on it.
function drawHome(lat: number, lon: number) {
  if (!lmap) return;
  if (homeMarker) lmap.removeLayer(homeMarker);
  homeMarker = circleMarker([lat, lon], {
    radius: 7,
    color: "#fff",
    weight: 2,
    fillColor: "#6366f1",
    fillOpacity: 1,
  })
    .addTo(lmap)
    .bindTooltip(t("darksky.location.you"), { direction: "top" });
}
function centerHome(lat: number, lon: number) {
  if (!lmap) return;
  lmap.setView([lat, lon], Math.max(lmap.getZoom(), 8));
  drawHome(lat, lon);
}

onMounted(() => {
  const el = mapEl.value;
  if (!el) return;
  const home = observerLatLon();
  lmap = createMap(el, { scrollWheelZoom: false, zoomSnap: 0 }).setView(
    [home?.lat ?? 46.5, home?.lon ?? 2.5],
    home ? 8 : 6,
  );
  addDarkBaseMap(lmap);
  // The finder is all about light pollution, so its overlay is always on (no toggle here).
  addLpLayer();
  lpStore.fetchStatus();
  canopyStore.fetchStatus();
  // Twilight and moonrise are computed, not fetched, so the night list is cheap and can load eagerly.
  void store.loadNights(home?.lat, home?.lon);
  if (home) drawHome(home.lat, home.lon);

  // Area drawing: while active, a press-drag traces the search rectangle (map panning is suspended).
  lmap.on("mousedown", onMapMouseDown);
  lmap.on("mousemove", onMapMouseMove);
  lmap.on("mouseup", onMapMouseUp);

  // Trackpad gestures: ⌘/ctrl+wheel zooms about the cursor (velocity-sensitive); plain wheel pans.
  detachWheel = useMapPinchZoom(el, () => lmap);
});

onBeforeUnmount(() => {
  detachWheel?.();
  detachWheel = null;
  lmap?.remove();
  lmap = null;
});

// The overlay is credited to David Lorenz once an offline atlas is installed (its propagation model), else
// to the keyless NASA GIBS fallback.
const lpAttribution = computed(() =>
  lpStore.present
    ? 'Light pollution model: © <a href="https://djlorenz.github.io/astronomy/lp/" target="_blank" rel="noopener">David Lorenz</a>'
    : "Light pollution: VIIRS (NASA/NOAA)",
);

// addLpLayer (re)creates the light-pollution overlay, appending the atlas build timestamp as a cache-buster
// so a freshly-built atlas is re-fetched instead of served stale from the browser cache.
function addLpLayer() {
  if (!lmap) return;
  const o = overlays.find((l) => l.id === "lightPollution");
  if (!o) return;
  if (lpLayer) lmap.removeLayer(lpLayer);
  const url = `${o.url ?? ""}&rev=${lpStore.builtAtMs}`;
  lpLayer = tileLayer(url, {
    opacity: o.opacity,
    attribution: lpAttribution.value,
    maxZoom: 19,
    maxNativeZoom: lpStore.present ? 19 : o.maxNativeZoom, // the atlas renders any zoom; GIBS caps at 8
  }).addTo(lmap);
}

// Rebuild the overlay when a new atlas is installed (builtAtMs changes).
watch(
  () => lpStore.builtAtMs,
  () => addLpLayer(),
);

// When a canopy atlas is installed for the area, re-run the search so trees fold into the horizon.
watch(
  () => canopyStore.builtAtMs,
  (v, old) => {
    if (v && v !== old && store.searched && areaRect) search();
  },
);

// Picking a different night is a different question, so answer it immediately rather than waiting for
// the user to press Search again with a stale list on screen.
watch(nightIndex, () => {
  if (store.searched && areaRect) search();
});
watch(useWeather, () => {
  if (store.searched && areaRect) search();
});

// One-line summary of the installed canopy atlas, or a prompt to download one.
const canopyCoverageLabel = computed(() => {
  const c = canopyStore.coverage;
  if (!c?.present) return t("darksky.canopy.none");
  return t("darksky.canopy.covers", {
    area: `${c.min_lat.toFixed(0)}…${c.max_lat.toFixed(0)}°N, ${c.min_lon.toFixed(0)}…${c.max_lon.toFixed(0)}°E`,
    date: formatTimestamp(c.built_at_ms).slice(0, 10),
  });
});

// Download canopy-height data for the chosen region, or the drawn rectangle when "custom".
function downloadCanopy() {
  let req: AtlasBuildRequest;
  if (canopyRegion.value === "custom") {
    const b = areaRect?.getBounds();
    if (!b) return;
    req = {
      min_lat: b.getSouth(),
      min_lon: b.getWest(),
      max_lat: b.getNorth(),
      max_lon: b.getEast(),
    };
  } else {
    req = { region: canopyRegion.value };
  }
  canopyStore.build(req);
}

// Download / update the offline atlas for the chosen region, or the drawn rectangle when "custom".
function downloadOfflineData() {
  let req: AtlasBuildRequest;
  if (region.value === "custom") {
    const b = areaRect?.getBounds();
    if (!b) return;
    req = {
      min_lat: b.getSouth(),
      min_lon: b.getWest(),
      max_lat: b.getNorth(),
      max_lon: b.getEast(),
    };
  } else {
    req = { region: region.value };
  }
  lpStore.build(req);
}

// One-line summary of the installed offline atlas (its bbox + build date), or a prompt to install one.
const coverageLabel = computed(() => {
  const c = lpStore.coverage;
  if (!c?.present) return t("darksky.offline.none");
  return t("darksky.offline.covers", {
    area: `${c.min_lat.toFixed(0)}…${c.max_lat.toFixed(0)}°N, ${c.min_lon.toFixed(0)}…${c.max_lon.toFixed(0)}°E`,
    date: formatTimestamp(c.built_at_ms).slice(0, 10),
  });
});

// The map container's class must stay static: Leaflet adds its own runtime classes (leaflet-container,
// leaflet-grab, …) to that element, and a reactive :class binding would clobber them on every change —
// which breaks the map (the panes lose their layout and it appears to vanish). So toggle the draw
// cursor imperatively via inline style instead of a reactive class.
function setMapCursor(cursor: string) {
  if (mapEl.value) mapEl.value.style.cursor = cursor;
}
function startDraw() {
  if (!lmap) return;
  drawing.value = true;
  lmap.dragging.disable();
  setMapCursor("crosshair");
}
function onMapMouseDown(e: LeafletMouseEvent) {
  if (!drawing.value || !lmap) return;
  dragStart = { lat: e.latlng.lat, lng: e.latlng.lng };
  if (areaRect) lmap.removeLayer(areaRect);
  areaRect = rectangle(latLngBounds(e.latlng, e.latlng), {
    color: "#818cf8",
    weight: 1,
    fillOpacity: 0.08,
  }).addTo(lmap);
}
function onMapMouseMove(e: LeafletMouseEvent) {
  if (!drawing.value || !dragStart || !areaRect) return;
  areaRect.setBounds(
    latLngBounds([dragStart.lat, dragStart.lng], [e.latlng.lat, e.latlng.lng]),
  );
}
function onMapMouseUp() {
  if (!drawing.value || !dragStart || !lmap) return;
  drawing.value = false;
  dragStart = null;
  lmap.dragging.enable();
  setMapCursor("");
  // Discard an accidental click / sliver — require a real area.
  const b = areaRect?.getBounds();
  const ok =
    !!b &&
    b.getNorth() - b.getSouth() > 0.001 &&
    b.getEast() - b.getWest() > 0.001;
  if (!ok && areaRect) {
    lmap.removeLayer(areaRect);
    areaRect = null;
  }
  hasArea.value = ok;
}

function clearArea() {
  if (areaRect && lmap) lmap.removeLayer(areaRect);
  areaRect = null;
  hasArea.value = false;
  clearMarkers();
  store.reset();
}

async function search() {
  if (!areaRect) return;
  const b = areaRect.getBounds();
  const home = observerLatLon();
  await store.find({
    minLat: b.getSouth(),
    minLon: b.getWest(),
    maxLat: b.getNorth(),
    maxLon: b.getEast(),
    maxBortle: maxBortle.value,
    horizon: evalHorizon.value,
    lat: home?.lat,
    lon: home?.lon,
    night: nightIndex.value,
    weather: useWeather.value,
    weatherWeight: useWeather.value ? weatherWeight.value : undefined,
  });
}

// Re-searching after a slider nudge would spend a forecast call per pixel dragged. The visible ranking
// updates instantly from the sub-scores already in hand (see rankedCandidates); this only refreshes the
// shortlist, which can change when the weight moves far enough to pull in a spot that missed the cut.
let weightTimer: ReturnType<typeof setTimeout> | null = null;
function onWeightChange() {
  if (weightTimer) clearTimeout(weightTimer);
  if (!hasArea.value || !store.searched) return;
  weightTimer = setTimeout(() => void search(), 500);
}
onBeforeUnmount(() => {
  if (weightTimer) clearTimeout(weightTimer);
});

// Re-blend the returned candidates for the current slider position. The server ordered them for the
// weight it was given; this keeps the list honest while the user drags, without another round trip.
const rankedCandidates = computed<DarkSite[]>(() => {
  const w = useWeather.value ? weatherWeight.value : 0;
  if (!w || !store.weatherAvailable) return store.candidates;
  return [...store.candidates].sort(
    (a, b) => blendScore(b, w) - blendScore(a, w),
  );
});

function blendScore(c: DarkSite, weight: number): number {
  const terrain = 0.6 * c.sub.darkness + 0.4 * c.sub.openness;
  if (!c.sub.weather_known) return terrain;
  return (1 - weight) * terrain + weight * c.sub.weather;
}

// The markers are indexed by the displayed order, so any re-ranking — a new search or a slider move —
// has to rebuild them in lockstep, or clicking a pin would select a different spot than the one shown.
watch(rankedCandidates, () => renderMarkers());

function clearMarkers() {
  for (const m of markers) lmap?.removeLayer(m);
  markers.length = 0;
  selectedIdx.value = null; // a rebuilt result set invalidates any prior selection
}

// Paint one candidate marker. The selected one gets a bright ring + larger radius and is raised above the
// others so the highlight reads clearly against the light-pollution overlay.
function styleMarker(m: CircleMarker, c: DarkSite, selected: boolean) {
  m.setStyle({
    color: selected ? "#f8fafc" : "#0b0b0d",
    weight: selected ? 3 : 1,
    fillColor: bortleColor(c.bortle),
    fillOpacity: selected ? 1 : 0.9,
  });
  m.setRadius(selected ? 9 : 6);
  if (selected) m.bringToFront();
}
function restyleMarkers() {
  rankedCandidates.value.forEach((c, i) => {
    const m = markers[i];
    if (m) styleMarker(m, c, i === selectedIdx.value);
  });
}
function renderMarkers() {
  clearMarkers();
  if (!lmap) return;
  const map = lmap;
  rankedCandidates.value.forEach((c, i) => {
    const m = circleMarker([c.lat, c.lon]).addTo(map);
    styleMarker(m, c, false);
    m.bindTooltip(markerTip(c), { direction: "top" });
    m.on("click", () => selectCandidate(i, { scrollRow: true })); // map → table
    markers.push(m);
  });
}

// Select a candidate and highlight it on BOTH surfaces (marker ring + table-row ring). Reveal it only when
// it's off-screen: pan the map iff the marker is outside the current view (zoomTo forces a zoom-in for the
// explicit "Locate" button), and scroll the table row into view iff requested. Shared by marker clicks
// (map → table, scrollRow) and row clicks (table → map).
function selectCandidate(
  idx: number,
  opts: { zoomTo?: boolean; scrollRow?: boolean } = {},
) {
  const c = rankedCandidates.value[idx];
  if (!c) return;
  selectedIdx.value = idx;
  restyleMarkers();
  if (lmap) {
    if (opts.zoomTo) lmap.setView([c.lat, c.lon], Math.max(lmap.getZoom(), 9));
    else if (!lmap.getBounds().contains([c.lat, c.lon]))
      lmap.panTo([c.lat, c.lon]);
    markers[idx]?.openTooltip();
  }
  if (opts.scrollRow) tableRef.value?.scrollToKey(idx);
}
// Open the spot in Google Maps (drops a pin; Street View / satellite are one click away to scout it).
function mapsUrl(c: DarkSite): string {
  return `https://www.google.com/maps/search/?api=1&query=${c.lat},${c.lon}`;
}

// Human drive time: "45 min" or "1 h 05".
function formatDriveMin(min: number): string {
  const m = Math.round(min);
  if (m < 60) return t("darksky.driveMin", { n: m });
  return t("darksky.driveHour", {
    h: Math.floor(m / 60),
    m: String(m % 60).padStart(2, "0"),
  });
}

// Map hover tooltip: the key at-a-glance stats for a candidate.
function markerTip(c: DarkSite): string {
  const lines = [`Bortle ${c.bortle} · SQM ${c.sqm.toFixed(1)}`];
  if (c.horizon)
    lines.push(
      `${t("darksky.openness")} ${Math.round(c.horizon.openness_pct)}% · ${t("darksky.south")} ${Math.round(c.horizon.south_openness_pct)}%`,
    );
  if (c.horizon?.canopy_m)
    lines.push(`${t("darksky.trees")} ${Math.round(c.horizon.canopy_m)} m`);
  if (hasForecast(c))
    lines.push(
      `${t(`tonight.weather.verdictLabel.${verdictLabel(c.weather!.score)}`)} · ${Math.round(c.weather!.cloud_pct)}% ${t("darksky.weather.clouds").toLowerCase()} · ${t("darksky.weather.hours", { n: c.weather!.clear_hours })}`,
    );
  if (c.drive_km)
    lines.push(
      `🚗 ${Math.round(c.drive_km)} km · ${formatDriveMin(c.drive_min ?? 0)}`,
    );
  return lines.join("<br>");
}

type Row = Record<string, unknown>;
const rows = computed<Row[]>(() =>
  rankedCandidates.value.map((c, i) => ({
    idx: i, // stable identity → correlates a row with its marker across sorting/selection
    n: i + 1,
    coords: `${c.lat.toFixed(3)}, ${c.lon.toFixed(3)}`,
    bortle: c.bortle,
    sqm: c.sqm,
    openness: c.horizon?.openness_pct ?? null,
    south: c.horizon?.south_openness_pct ?? null,
    trees: c.horizon?.canopy_m ?? null,
    sky: hasForecast(c) ? c.weather!.score : null,
    clouds: hasForecast(c) ? c.weather!.cloud_pct : null,
    clear: hasForecast(c) ? c.weather!.clear_hours : null,
    seeing:
      hasForecast(c) && c.weather!.seeing_arcsec > 0
        ? c.weather!.seeing_arcsec
        : null,
    // Sort by drive time (nearest first); rows without routing sort last.
    drive:
      c.drive_min && c.drive_min > 0 ? c.drive_min : Number.POSITIVE_INFINITY,
    site: c as unknown,
  })),
);

// A candidate has a forecast only when the scan actually returned hours for it. A zero-hour outlook is
// "we do not know", and must never be rendered as a perfectly cloudy night.
function hasForecast(c: DarkSite): boolean {
  return !!c.weather && c.weather.sample_hours > 0;
}

// Flags are locale-neutral keys from the engine; the ones worth interrupting the user for get a chip.
const FLAG_CHIPS: Record<string, { icon: string; tone: string }> = {
  above_inversion: { icon: "⛰", tone: "text-success" },
  fog_risk: { icon: "≈", tone: "text-warning" },
  frost: { icon: "❄", tone: "text-info" },
};
function flagChips(c: DarkSite): { key: string; icon: string; tone: string }[] {
  return (c.weather?.flags ?? [])
    .filter((f) => FLAG_CHIPS[f])
    .map((f) => ({ key: f, ...FLAG_CHIPS[f] }));
}

function rowIdx(row: Row): number {
  return Number(row.idx);
}
// Table → map: clicking a row selects + highlights its marker (gentle reveal, keeps the current zoom).
function onRowClick(row: Row) {
  selectCandidate(rowIdx(row));
}
// The selected candidate's row gets a ring; every other row keeps the default clickable hover style.
// A spot whose night the forecast writes off is dimmed rather than dropped — on a marginal night the
// least-bad option is still the answer, and hiding it would leave an empty map.
function rowClassFor(row: Row): string {
  const base =
    rowIdx(row) === selectedIdx.value
      ? "cursor-pointer bg-brand-100/70 ring-1 ring-inset ring-brand-400/60 dark:bg-brand-500/20"
      : "cursor-pointer hover:bg-slate-50 dark:hover:bg-slate-800/50";
  const sky = row.sky;
  return typeof sky === "number" && sky < HOPELESS_SKY
    ? `${base} opacity-60`
    : base;
}
const HOPELESS_SKY = 20;
const columns: Column<Row>[] = [
  { key: "n", label: "#", align: "right" },
  { key: "coords", label: t("darksky.coords"), searchable: true },
  { key: "bortle", label: t("darksky.bortle"), sortable: true, align: "right" },
  {
    key: "sqm",
    label: t("darksky.sqm"),
    sortable: true,
    align: "right",
    format: (v) => Number(v).toFixed(1),
  },
  {
    key: "openness",
    label: t("darksky.openness"),
    sortable: true,
    align: "right",
    format: (v) => (v == null ? "—" : `${Math.round(Number(v))}%`),
  },
  {
    key: "south",
    label: t("darksky.south"),
    sortable: true,
    align: "right",
    format: (v) => (v == null ? "—" : `${Math.round(Number(v))}%`),
  },
  {
    key: "trees",
    label: t("darksky.trees"),
    sortable: true,
    align: "right",
    format: (v) =>
      v == null || Number(v) <= 0 ? "—" : `${Math.round(Number(v))} m`,
  },
  { key: "sky", label: t("darksky.weather.sky"), sortable: true },
  {
    key: "clouds",
    label: t("darksky.weather.clouds"),
    sortable: true,
    align: "right",
    format: (v) => (v == null ? "—" : `${Math.round(Number(v))}%`),
  },
  {
    key: "clear",
    label: t("darksky.weather.clearHours"),
    sortable: true,
    align: "right",
    format: (v) => (v == null ? "—" : t("darksky.weather.hours", { n: v })),
  },
  {
    key: "seeing",
    label: t("darksky.weather.seeing"),
    sortable: true,
    align: "right",
    format: (v) => (v == null ? "—" : `${Number(v).toFixed(1)}″`),
  },
  { key: "drive", label: t("darksky.drive"), sortable: true, align: "right" },
  { key: "actions", label: "", align: "right" },
];
</script>

<template>
  <div class="space-y-3">
    <p class="text-sm text-slate-500 dark:text-slate-400">
      {{ t("darksky.hint") }}
    </p>

    <!-- observing location: origin for the distance + driving-time columns -->
    <div class="flex flex-wrap items-center gap-x-3 gap-y-2">
      <span class="text-sm text-slate-500 dark:text-slate-400"
        >{{ t("darksky.location.label") }}:</span
      >
      <span class="text-sm font-medium text-slate-700 dark:text-slate-200">
        {{ observerLabel }}
      </span>
      <div class="relative">
        <input
          v-model="locSearch"
          :class="input"
          class="!w-56 !py-1"
          :placeholder="t('darksky.location.placeholder')"
          @input="onLocInput"
          @focus="locOpen = true"
        />
        <ul
          v-if="locOpen && locResults.length"
          class="absolute z-[1200] mt-1 max-h-56 w-72 overflow-auto rounded-md border border-slate-200 bg-white shadow-lg dark:border-slate-700 dark:bg-slate-800"
        >
          <li
            v-for="(r, i) in locResults"
            :key="i"
            class="cursor-pointer px-3 py-1.5 text-sm hover:bg-slate-100 dark:hover:bg-slate-700"
            @click="chooseLoc(r)"
          >
            {{ r.label }}
          </li>
        </ul>
      </div>
      <button
        :class="btnGhost"
        class="!px-2 !py-1 !text-xs"
        :disabled="locBusy"
        @click="useMyLocation"
      >
        {{ locBusy ? t("common.loading") : t("darksky.location.gps") }}
      </button>

      <!-- saved favorites: click to focus the map + set as the observing origin -->
      <template v-if="sky.locationFavorites.length">
        <span class="text-sm text-slate-500 dark:text-slate-400"
          >{{ t("darksky.location.favorites") }}:</span
        >
        <button
          v-for="fav in sky.locationFavorites"
          :key="fav.id"
          :class="btnGhost"
          class="!px-2 !py-1 !text-xs"
          :title="`${fav.lat.toFixed(3)}, ${fav.lon.toFixed(3)}`"
          @click="useFavoriteLocation(fav)"
        >
          {{ fav.label }}
        </button>
      </template>
    </div>

    <!-- controls -->
    <div class="flex flex-wrap items-center gap-x-4 gap-y-2">
      <button :class="drawing ? btnPrimary : btnGhost" @click="startDraw">
        {{ drawing ? t("darksky.drawing") : t("darksky.drawArea") }}
      </button>
      <button :class="btnGhost" :disabled="!hasArea" @click="clearArea">
        {{ t("common.clear") }}
      </button>
      <label class="flex items-center gap-2 text-sm">
        <span class="text-slate-500 dark:text-slate-400">{{
          t("darksky.maxBortle", { n: maxBortle })
        }}</span>
        <BortleScalePicker v-model="maxBortle" />
      </label>
      <label
        class="flex items-center gap-2 text-sm"
        :title="t('darksky.evalHorizonHint')"
      >
        <input v-model="evalHorizon" type="checkbox" class="accent-brand-500" />
        {{ t("darksky.evalHorizon") }}
      </label>
      <label
        class="flex items-center gap-2 text-sm"
        :title="t('darksky.weather.useHint')"
      >
        <input v-model="useWeather" type="checkbox" :class="checkbox" />
        {{ t("darksky.weather.use") }}
      </label>
      <button
        :class="btnPrimary"
        :disabled="!hasArea || store.loading"
        @click="search"
      >
        {{ store.loading ? t("common.loading") : t("darksky.search") }}
      </button>
    </div>

    <!-- which night to plan for, and how much the forecast should count -->
    <div class="flex flex-wrap items-center gap-x-4 gap-y-2">
      <NightPicker
        v-model="nightIndex"
        :nights="store.nights"
        :count="NIGHT_COUNT"
      />
      <label
        v-if="useWeather"
        class="flex items-center gap-2 text-sm"
        :title="t('darksky.weather.weightHint')"
      >
        <span class="text-slate-500 dark:text-slate-400">{{
          t("darksky.weather.darkest")
        }}</span>
        <input
          v-model.number="weatherWeight"
          type="range"
          min="0"
          max="0.8"
          step="0.05"
          class="w-32 accent-brand-600"
          :aria-label="t('darksky.weather.weightHint')"
          @change="onWeightChange"
        />
        <span class="text-slate-500 dark:text-slate-400">{{
          t("darksky.weather.clearest")
        }}</span>
      </label>
    </div>

    <!-- what the chosen night actually looks like, before any spot is picked -->
    <div
      v-if="useWeather && store.night"
      class="flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-slate-500 dark:text-slate-400"
    >
      <span class="inline-flex items-center gap-1">
        <IconCloud class="text-slate-400" />
        {{
          t("darksky.weather.nightSummary", {
            hours: store.night.dark_hours.toFixed(1),
            moon: Math.round(store.night.moon_illum * 100),
          })
        }}
      </span>
      <span v-if="store.night.moon_up_hours > 0.1">
        {{
          t("darksky.weather.moonUp", {
            hours: store.night.moon_up_hours.toFixed(1),
          })
        }}
      </span>
      <span
        v-if="store.night.confidence"
        :title="t('darksky.weather.agreementHint')"
      >
        {{
          t("darksky.weather.agreement", {
            pct: Math.round(store.night.confidence.agreement * 100),
            clear: store.night.confidence.clear_members,
            total: store.night.confidence.members,
          })
        }}
      </span>
      <span v-if="!store.weatherAvailable" class="text-warning">
        {{ t("darksky.weather.degraded") }}
      </span>
    </div>

    <!-- map (static class — Leaflet manages this element's classes/cursor at runtime; see setMapCursor) -->
    <div
      ref="mapEl"
      class="relative z-0 h-[26rem] w-full overflow-hidden rounded-md border border-slate-200 dark:border-slate-700"
      :aria-label="t('darksky.mapLabel')"
    />
    <LightPollutionLegend />

    <!-- Offline light-pollution data: pick a region and download it once; queries are then offline. -->
    <div
      class="flex flex-wrap items-center gap-x-3 gap-y-2 rounded-md border border-slate-200 px-3 py-2 text-xs dark:border-slate-700"
    >
      <span class="font-medium text-slate-600 dark:text-slate-300">{{
        t("darksky.offline.title")
      }}</span>
      <span class="text-slate-500 dark:text-slate-400">{{
        coverageLabel
      }}</span>
      <select v-model="region" :class="input" class="ml-auto !w-auto !py-1">
        <option value="france">
          {{ t("darksky.offline.regions.france") }}
        </option>
        <option value="europe">
          {{ t("darksky.offline.regions.europe") }}
        </option>
        <option value="world">{{ t("darksky.offline.regions.world") }}</option>
        <option value="custom" :disabled="!hasArea">
          {{ t("darksky.offline.regions.custom") }}
        </option>
      </select>
      <button
        :class="btnPrimary"
        class="!px-3 !py-1"
        :disabled="lpStore.building || (region === 'custom' && !hasArea)"
        @click="downloadOfflineData"
      >
        {{
          lpStore.building
            ? t("darksky.offline.building", {
                done: lpStore.state?.done ?? 0,
                total: lpStore.state?.total ?? 0,
              })
            : t("darksky.offline.download")
        }}
      </button>
      <p class="w-full text-slate-400">{{ t("darksky.offline.hint") }}</p>
      <p v-if="lpStore.error" class="w-full text-danger">{{ lpStore.error }}</p>
    </div>

    <!-- Canopy (tree/forest) offline data: download once for the area, then trees enter the horizon. -->
    <div
      class="flex flex-wrap items-center gap-x-3 gap-y-2 rounded-md border border-slate-200 px-3 py-2 text-xs dark:border-slate-700"
    >
      <span class="font-medium text-slate-600 dark:text-slate-300">{{
        t("darksky.canopy.title")
      }}</span>
      <span class="text-slate-500 dark:text-slate-400">{{
        canopyCoverageLabel
      }}</span>
      <select
        v-model="canopyRegion"
        :class="input"
        class="ml-auto !w-auto !py-1"
      >
        <option value="custom" :disabled="!hasArea">
          {{ t("darksky.canopy.regions.custom") }}
        </option>
        <option value="france">
          {{ t("darksky.canopy.regions.france") }}
        </option>
      </select>
      <button
        :class="btnPrimary"
        class="!px-3 !py-1"
        :disabled="
          canopyStore.building || (canopyRegion === 'custom' && !hasArea)
        "
        @click="downloadCanopy"
      >
        {{
          canopyStore.building
            ? t("darksky.canopy.building", {
                done: canopyStore.state?.done ?? 0,
                total: canopyStore.state?.total ?? 0,
              })
            : t("darksky.canopy.download")
        }}
      </button>
      <p class="w-full text-slate-400">{{ t("darksky.canopy.hint") }}</p>
      <p v-if="canopyStore.buildError" class="w-full text-danger">
        {{ canopyStore.buildError }}
      </p>
    </div>

    <p v-if="store.error" class="text-sm text-danger">{{ store.error }}</p>
    <ul v-if="store.warnings.length" class="space-y-1">
      <li
        v-for="(w, i) in store.warnings"
        :key="i"
        class="text-xs text-warning"
      >
        ⚠ {{ w }}
      </li>
    </ul>

    <!-- results -->
    <GenericTable
      v-if="rows.length"
      ref="tableRef"
      :columns="columns"
      :rows="rows"
      :row-key="rowIdx"
      :row-class="rowClassFor"
      max-height="24rem"
      @row-click="onRowClick"
    >
      <template #cell-coords="{ row }">
        <a
          :href="mapsUrl(row.site as DarkSite)"
          target="_blank"
          rel="noopener noreferrer"
          class="text-brand-600 hover:underline dark:text-brand-300"
          :title="t('darksky.openMaps')"
          @click.stop
        >
          {{ row.coords }}
        </a>
      </template>
      <template #cell-bortle="{ value }">
        <span
          class="inline-block h-4 w-4 rounded-sm align-middle"
          :style="{ backgroundColor: bortleColor(Number(value)) }"
          :title="`Bortle ${value}`"
        />
        <span class="ml-1.5 align-middle">{{ value }}</span>
      </template>
      <template #cell-sky="{ row }">
        <span
          v-if="row.sky != null"
          class="inline-flex items-center gap-1.5 whitespace-nowrap"
        >
          <span
            class="inline-block h-2.5 w-2.5 rounded-full"
            :style="{ backgroundColor: verdictColor(Number(row.sky)) }"
          />
          <span>{{
            t(`tonight.weather.verdictLabel.${verdictLabel(Number(row.sky))}`)
          }}</span>
          <span
            v-for="chip in flagChips(row.site as DarkSite)"
            :key="chip.key"
            :class="chip.tone"
            :title="t(`darksky.weather.flags.${chip.key}`)"
            >{{ chip.icon }}</span
          >
          <span
            v-if="(row.site as DarkSite).weather?.dew_risk === 'high'"
            :style="{ color: dewRiskColor('high') }"
            :title="t('darksky.weather.dewHigh')"
            >•</span
          >
        </span>
        <span v-else class="text-slate-400">—</span>
      </template>
      <template #cell-clouds="{ row }">
        <span
          v-if="row.clouds != null"
          :style="{ color: goodBad(Number(row.clouds), 0, 100) }"
          >{{ Math.round(Number(row.clouds)) }}%</span
        >
        <span v-else class="text-slate-400">—</span>
      </template>
      <template #cell-drive="{ row }">
        <span
          v-if="(row.site as DarkSite).drive_km"
          class="inline-flex items-center justify-end gap-1 whitespace-nowrap"
          :title="t('darksky.driveTitle')"
        >
          <IconCar class="text-slate-400" />
          <span>{{ Math.round((row.site as DarkSite).drive_km!) }} km</span>
          <span class="text-slate-400"
            >· {{ formatDriveMin((row.site as DarkSite).drive_min ?? 0) }}</span
          >
        </span>
        <span
          v-else
          class="whitespace-nowrap text-slate-400"
          :title="t('darksky.driveDirectTitle')"
        >
          ~{{ Math.round((row.site as DarkSite).distance_km) }} km
        </span>
      </template>
      <template #cell-actions="{ row }">
        <div class="flex items-center justify-end gap-1">
          <button
            :class="btnGhost"
            class="!px-2 !py-1 !text-xs"
            @click.stop="selectCandidate(rowIdx(row), { zoomTo: true })"
          >
            {{ t("darksky.locate") }}
          </button>
          <button
            :class="btnPrimary"
            class="!px-2 !py-1 !text-xs"
            @click.stop="
              emit(
                'useLocation',
                (row.site as DarkSite).lat,
                (row.site as DarkSite).lon,
              )
            "
          >
            {{ t("darksky.useLocation") }}
          </button>
        </div>
      </template>
    </GenericTable>
    <p
      v-else-if="store.searched && !store.loading"
      class="text-sm text-slate-400"
    >
      {{ t("darksky.noResults") }}
    </p>

    <!-- Open-Meteo's free tier is CC BY 4.0: attribution is a licence condition, not decoration. -->
    <p v-if="useWeather" class="text-xs text-slate-400">
      {{ t("darksky.weather.attribution") }}
    </p>
  </div>
</template>
