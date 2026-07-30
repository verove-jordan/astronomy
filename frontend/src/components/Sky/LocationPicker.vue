<script setup lang="ts">
import { onMounted, onBeforeUnmount, ref, computed, watch } from "vue";
import { useI18n } from "vue-i18n";
import {
  map as createMap,
  tileLayer,
  circleMarker,
  type Map as LMap,
  type CircleMarker,
  type TileLayer,
  type LeafletMouseEvent,
  type LeafletEvent,
} from "leaflet";
import "leaflet/dist/leaflet.css";
import { apiGet } from "@/services/api";
import { addDarkBaseMap } from "@/utils/basemap";
import { useMapLayers } from "@/composables/useMapLayers";
import { useMapPinchZoom } from "@/composables/useMapPinchZoom";
import { useWeatherStore } from "@/stores/weather";
import { useLightPollutionStore } from "@/stores/lightpollution";
import { createFrameTileLayer } from "@/composables/useFrameTileLayer";
import { createRainviewerLayer } from "@/composables/useRainviewerLayer";
import LightPollutionLegend from "@/components/Sky/LightPollutionLegend.vue";
import WeatherTimeline from "@/components/Sky/WeatherTimeline.vue";
import WeatherLegend from "@/components/Sky/WeatherLegend.vue";
import RadarLegend from "@/components/Sky/RadarLegend.vue";
import { input, btnPrimary } from "@/constants/styles";
import { CHART_ALT_FILL } from "@/constants/colors";
import type { GeoResult } from "@/types";

// Leaflet is only reachable through the lazily-loaded /tonight route, so it stays out of the main
// bundle. A circleMarker avoids Leaflet's default-icon asset-path issues under the bundler.
const props = defineProps<{ lat: number; lon: number }>();
const emit = defineEmits<{
  pick: [lat: number, lon: number, label?: string];
}>();
const { t } = useI18n();

// Modular overlay layers: light-pollution tiles, the animated forecast grid layers, and live RainViewer
// radar — all toggled here.
const {
  overlays,
  isEnabled,
  toggle,
  anyWeatherEnabled,
  anyRainviewerEnabled,
  anyAnimatedEnabled,
} = useMapLayers();
const weather = useWeatherStore();
const lpStore = useLightPollutionStore();

// RainViewer live frames refresh cadence (the public maps JSON updates ~every 10 min).
const RV_REFRESH_MS = 5 * 60 * 1000;

const mapEl = ref<HTMLDivElement | null>(null);
const search = ref("");
const results = ref<GeoResult[]>([]);
const open = ref(false); // dropdown visible (input focused)
const searching = ref(false);
const searched = ref(false); // a search has completed for the current query

// Light-pollution download-on-demand: whether the current view falls outside the installed offline atlas
// (so we offer to download it), plus a per-session dismiss and a "status loaded" gate (avoids a flash
// before we know what's installed).
const lpUncovered = ref(false);
const lpPromptDismissed = ref(false);
const lpStatusReady = ref(false);
const lpAutoTried = ref(false); // gate: auto-download the observer's LP region at most once per mount

let lmap: LMap | null = null;
let marker: CircleMarker | null = null;
const overlayLayers: Record<string, TileLayer> = {}; // tile overlays (light pollution)
const weatherLayers: Record<
  string,
  ReturnType<typeof createFrameTileLayer>
> = {}; // animated server-rendered forecast-metric tiles (clouds/humidity/precip)
const rvLayers: Record<string, ReturnType<typeof createRainviewerLayer>> = {}; // live RainViewer tiles
let rvRefreshTimer: ReturnType<typeof setInterval> | null = null;
let detachWheel: (() => void) | null = null;

// The forecast weather layers currently enabled — drives the legends shown under the map.
const enabledWeatherLayers = computed(() =>
  overlays.filter((o) => o.kind === "weather" && isEnabled(o.id)),
);
// The live (RainViewer) layers currently enabled — drives the radar legend + repaint.
const enabledLiveLayers = computed(() =>
  overlays.filter((o) => o.kind === "rainviewer" && isEnabled(o.id)),
);

// retryTile re-requests an overlay tile that failed to load. The backend returns a 5xx (not a blank
// tile) on a transient upstream failure, so without this a single failed tile in the initial burst
// would leave a permanent hole in the overlay. We back off and bust the cache, a few times per tile.
const MAX_TILE_RETRIES = 3;
function retryTile(e: LeafletEvent) {
  const img = (e as unknown as { tile?: HTMLImageElement }).tile;
  if (!img) return;
  const tries = Number(img.dataset.retry ?? "0");
  if (tries >= MAX_TILE_RETRIES) return;
  img.dataset.retry = String(tries + 1);
  const base = img.src.replace(/[?&]_r=\d+/, "");
  const sep = base.includes("?") ? "&" : "?";
  window.setTimeout(
    () => {
      img.src = `${base}${sep}_r=${Date.now()}`;
    },
    400 * (tries + 1),
  );
}

onMounted(() => {
  const el = mapEl.value;
  if (!el) return;
  // scrollWheelZoom off + zoomSnap 0 so trackpad gestures feel native (see the wheel handler below):
  // two-finger drag pans, pinch zooms — a two-finger scroll no longer zooms.
  // Zoom 7 frames the observer's region (~500 km across); the weather grid is fetched with a margin
  // beyond the view and snapped to a fixed global lattice, so it fully covers the view and — unlike
  // before — stays geographically put as you pan/zoom instead of re-centring on the focus.
  lmap = createMap(el, { scrollWheelZoom: false, zoomSnap: 0 }).setView(
    [props.lat, props.lon],
    7,
  );
  addDarkBaseMap(lmap);
  // Overlay layers (kept above the base map; the marker, a vector layer, stays on top of both). Tile
  // overlays are XYZ proxies; grid overlays are canvas image-overlays painted from the weather cube.
  for (const o of overlays) {
    if (o.kind === "weather") {
      // No onTileError here on purpose: when the forecast is degraded the server returns a transparent
      // 200 tile (not an error), so there is nothing to retry — and NOT retrying is what stops a
      // rate-limit outage from snowballing into a cache-busting request storm against the engine/upstream.
      weatherLayers[o.id] = createFrameTileLayer({
        opacity: o.opacity,
        attribution: o.attribution,
      });
      continue;
    }
    if (o.kind === "rainviewer") {
      rvLayers[o.id] = createRainviewerLayer(
        o.product ?? "radar",
        o.opacity,
        retryTile,
      );
      continue;
    }
    if (o.id === "lightPollution") {
      buildLpLayer(); // atlas-aware (cache-bust + full-zoom when covered); see below
      continue;
    }
    const layer = tileLayer(o.url ?? "", {
      opacity: o.opacity,
      attribution: o.attribution,
      maxZoom: 19,
      // The overlay source (e.g. GIBS, z≤8) has fewer levels than the base map; upscale its last
      // native level past maxNativeZoom instead of requesting missing tiles (which made it vanish).
      maxNativeZoom: o.maxNativeZoom,
    });
    layer.on("tileerror", retryTile); // self-heal transient tile failures (see retryTile above)
    overlayLayers[o.id] = layer;
  }
  marker = circleMarker([props.lat, props.lon], {
    radius: 7,
    color: CHART_ALT_FILL,
    fillColor: CHART_ALT_FILL,
    fillOpacity: 0.85,
  }).addTo(lmap);
  lmap.on("click", (e: LeafletMouseEvent) =>
    emit("pick", e.latlng.lat, e.latlng.lng),
  );

  // Trackpad gestures: ⌘/ctrl+wheel zooms about the cursor (velocity-sensitive); plain wheel pans.
  detachWheel = useMapPinchZoom(el, () => lmap);

  syncOverlays();
  maybeFetchWeather(); // load the frames index if a forecast weather layer was left enabled
  maybeFetchRainviewer(); // load live radar frames if a live layer was left enabled
  // Keep the live radar current: reload the RainViewer frame index every few minutes while it is on.
  rvRefreshTimer = setInterval(() => {
    if (anyRainviewerEnabled()) void weather.fetchRainviewer(true);
  }, RV_REFRESH_MS);

  // Light pollution: learn what the installed atlas covers, then re-check as the user pans so we can offer
  // to download data for an uncovered area (and refetch full-zoom tiles once a new atlas lands). The
  // weather overlays are now server-rendered tiles that Leaflet fetches per viewport, so — unlike the old
  // client-rendered cube — there is nothing to refetch on move; only LP coverage is re-evaluated (debounced,
  // since moveend fires per-frame during a pinch). Auto-download the observer's region once if uncovered.
  lmap.on("moveend", onMapMovedForLp);
  lpStore.fetchStatus().then(() => {
    lpStatusReady.value = true;
    recomputeLpCoverage();
    maybeAutoDownloadLp();
  });
});

onBeforeUnmount(() => {
  detachWheel?.();
  detachWheel = null;
  weather.pause(); // stop the animation interval so it doesn't keep mutating the store after we leave /tonight
  lpStore.stopPolling(); // stop the LP build-status poll if a download was still in progress
  if (lpMoveTimer) clearTimeout(lpMoveTimer);
  if (rvRefreshTimer) clearInterval(rvRefreshTimer);
  rvRefreshTimer = null;
  lmap?.remove();
  lmap = null;
  marker = null;
});

// syncOverlays reconciles the Leaflet layers with the enabled set (persisted in the composable). Tile
// overlays add/remove directly; weather overlays are server-rendered tiles pointed at the current frame
// (added once a frame time is known).
function syncOverlays() {
  if (!lmap) return;
  for (const o of overlays) {
    if (o.kind === "weather") {
      syncWeatherOverlay(o.id);
      continue;
    }
    if (o.kind === "rainviewer") {
      syncRainviewerOverlay(o.id);
      continue;
    }
    const layer = overlayLayers[o.id];
    if (!layer) continue;
    if (isEnabled(o.id)) {
      if (!lmap.hasLayer(layer)) layer.addTo(lmap);
    } else if (lmap.hasLayer(layer)) {
      lmap.removeLayer(layer);
    }
  }
}

// syncWeatherOverlay adds/removes one server-rendered weather-metric tile layer and points it at the
// current frame. The backend renders each PNG tile from its own region's cube, so Leaflet composites the
// tiles natively (covering the whole viewport, no seam) and a frame change is just a tile-URL swap —
// there is zero per-pixel work on the main thread. The layer is added only once a frame time is known.
function syncWeatherOverlay(id: string) {
  const wrapper = weatherLayers[id];
  if (!lmap || !wrapper) return;
  const o = overlays.find((l) => l.id === id);
  const url = o?.metric ? weather.weatherTileUrl(o.metric) : "";
  if (isEnabled(id) && url) {
    const layer = wrapper.update(url);
    if (!lmap.hasLayer(layer)) layer.addTo(lmap);
  } else if (wrapper.layer && lmap.hasLayer(wrapper.layer)) {
    lmap.removeLayer(wrapper.layer);
  }
}

// maybeFetchWeather loads the lightweight frames index (the scrubber's time axis) for the current map
// centre + zoom whenever an animated weather layer is on. The zoom lets the backend anchor the request
// to the same tile-block region the tiles resolve, so this fetch pre-warms exactly the cube the tiles
// read. The forecast hours are the same across the region, so it runs only on mount / toggle / site
// change — Leaflet fetches per-viewport tiles itself, and a zoomed cube warms on demand.
function maybeFetchWeather(force = false) {
  if (!lmap || !anyWeatherEnabled()) return;
  const c = lmap.getCenter();
  void weather.fetchFrames(c.lat, c.lng, Math.round(lmap.getZoom()), force);
}

// syncRainviewerOverlay adds/removes/repaints one live layer: it paints the frame nearest the playhead
// (weather.radarFrame / satelliteFrame), and removes the layer when it is off OR the playhead is outside
// the feed's observed window (frame == null), so a stale "now" frame never shows over the forecast future.
function syncRainviewerOverlay(id: string) {
  const wrapper = rvLayers[id];
  if (!lmap || !wrapper) return;
  const o = overlays.find((l) => l.id === id);
  const frame =
    o?.product === "satellite" ? weather.satelliteFrame : weather.radarFrame;
  if (isEnabled(id) && frame) {
    const layer = wrapper.update(frame.host, frame.path);
    if (!lmap.hasLayer(layer)) layer.addTo(lmap);
  } else if (wrapper.layer && lmap.hasLayer(wrapper.layer)) {
    lmap.removeLayer(wrapper.layer);
  }
}

// maybeFetchRainviewer loads the live-frame index when a live layer is on (global product, no viewport).
function maybeFetchRainviewer() {
  if (anyRainviewerEnabled()) void weather.fetchRainviewer();
}

// onMapMovedForLp re-evaluates light-pollution coverage after a move, debounced: moveend fires per-frame
// during a pinch/inertia, and recomputeLpCoverage reads Leaflet bounds + drives a reactive prompt, so
// coalescing to one call per settle keeps the pan smooth. (The weather overlays are server tiles now, so
// there is nothing else to do on move — Leaflet fetches the tiles it needs itself.)
let lpMoveTimer: ReturnType<typeof setTimeout> | null = null;
function onMapMovedForLp() {
  if (lpMoveTimer) clearTimeout(lpMoveTimer);
  lpMoveTimer = setTimeout(recomputeLpCoverage, 200);
}

// --- Light-pollution offline atlas: atlas-aware rendering + download-on-demand for uncovered areas ---

// David Lorenz propagation-model credit once an offline atlas is installed; else the NASA GIBS default.
const DJLORENZ_ATTRIB =
  'Light pollution model: © <a href="https://djlorenz.github.io/astronomy/lp/" target="_blank" rel="noopener">David Lorenz</a>';

// buildLpLayer (re)creates the light-pollution tile layer atlas-aware: a build-timestamp cache-buster so
// a freshly-downloaded atlas is refetched, full-zoom native tiles where an atlas covers the view (else the
// GIBS z8 cap), and the matching attribution. Stored like any overlay so syncOverlays toggles it.
function buildLpLayer() {
  const o = overlays.find((l) => l.id === "lightPollution");
  if (!o) return;
  const layer = tileLayer(`${o.url ?? ""}&rev=${lpStore.builtAtMs}`, {
    opacity: o.opacity,
    attribution: lpStore.present ? DJLORENZ_ATTRIB : o.attribution,
    maxZoom: 19,
    maxNativeZoom: lpStore.present ? 19 : o.maxNativeZoom,
  });
  layer.on("tileerror", retryTile);
  overlayLayers[o.id] = layer;
}

// refreshLpLayer swaps in a fresh LP layer (after a new atlas is installed) and re-adds it if it was on.
function refreshLpLayer() {
  if (!lmap) return;
  const old = overlayLayers["lightPollution"];
  if (old && lmap.hasLayer(old)) lmap.removeLayer(old);
  buildLpLayer();
  syncOverlays();
  recomputeLpCoverage();
}

// recomputeLpCoverage flags whether the current view extends beyond the installed atlas's bbox (or no
// atlas is installed), which drives the "download this area" prompt.
function recomputeLpCoverage() {
  if (!lmap || !isEnabled("lightPollution")) {
    lpUncovered.value = false;
    return;
  }
  const c = lpStore.coverage;
  if (!c?.present) {
    lpUncovered.value = true;
    return;
  }
  const b = lmap.getBounds();
  lpUncovered.value =
    b.getSouth() < c.min_lat ||
    b.getWest() < c.min_lon ||
    b.getNorth() > c.max_lat ||
    b.getEast() > c.max_lon;
}

// showLpPrompt: offer the download when the LP layer is on, the view isn't fully covered (or a build is
// running), status has loaded, and the user hasn't dismissed it this session.
const showLpPrompt = computed(
  () =>
    lpStatusReady.value &&
    isEnabled("lightPollution") &&
    !lpPromptDismissed.value &&
    (lpUncovered.value || lpStore.building || !!lpStore.buildError),
);

// downloadLpArea builds the offline atlas for the current view, UNIONed with any existing coverage so
// downloads accumulate (downloading a new area never drops a region you already have).
function downloadLpArea() {
  if (!lmap) return;
  const b = lmap.getBounds();
  const clampLat = (v: number) => Math.max(-60, Math.min(75, v));
  const clampLon = (v: number) => Math.max(-180, Math.min(180, v));
  let minLat = clampLat(b.getSouth());
  let minLon = clampLon(b.getWest());
  let maxLat = clampLat(b.getNorth());
  let maxLon = clampLon(b.getEast());
  const c = lpStore.coverage;
  if (c?.present) {
    minLat = Math.min(minLat, c.min_lat);
    minLon = Math.min(minLon, c.min_lon);
    maxLat = Math.max(maxLat, c.max_lat);
    maxLon = Math.max(maxLon, c.max_lon);
  }
  lpStore.build({
    min_lat: minLat,
    min_lon: minLon,
    max_lat: maxLat,
    max_lon: maxLon,
  });
}

function dismissLpPrompt() {
  lpPromptDismissed.value = true;
}

// maybeAutoDownloadLp fetches a detailed offline LP atlas for a bounded box around the OBSERVING SITE the
// first time the layer is on and the site isn't already covered — so light pollution is detailed by default
// without a manual click. Gated to run at most once per mount (never loops), bounded to ±LP_AUTO_DEG so it
// stays a quick download, and UNIONed with any existing coverage. Panning far away still uses the manual
// "download this area" prompt (showLpPrompt) for the wider region.
const LP_AUTO_DEG = 1.5; // ~150 km half-span around the site — detailed yet a small, fast build
function maybeAutoDownloadLp() {
  if (!lpStatusReady.value || lpAutoTried.value) return;
  if (!isEnabled("lightPollution") || lpStore.building) return;
  const c = lpStore.coverage;
  const siteCovered =
    !!c?.present &&
    props.lat >= c.min_lat &&
    props.lat <= c.max_lat &&
    props.lon >= c.min_lon &&
    props.lon <= c.max_lon;
  lpAutoTried.value = true; // mark tried regardless — a covered site is a no-op, an uncovered one builds once
  if (siteCovered) return;
  const clampLat = (v: number) => Math.max(-60, Math.min(75, v));
  const clampLon = (v: number) => Math.max(-180, Math.min(180, v));
  let minLat = clampLat(props.lat - LP_AUTO_DEG);
  let maxLat = clampLat(props.lat + LP_AUTO_DEG);
  let minLon = clampLon(props.lon - LP_AUTO_DEG);
  let maxLon = clampLon(props.lon + LP_AUTO_DEG);
  if (c?.present) {
    minLat = Math.min(minLat, c.min_lat);
    minLon = Math.min(minLon, c.min_lon);
    maxLat = Math.max(maxLat, c.max_lat);
    maxLon = Math.max(maxLon, c.max_lon);
  }
  lpStore.build({
    min_lat: minLat,
    min_lon: minLon,
    max_lat: maxLat,
    max_lon: maxLon,
  });
}

watch(
  () => overlays.map((o) => isEnabled(o.id)),
  () => {
    syncOverlays();
    maybeFetchWeather();
    maybeFetchRainviewer();
    if (isEnabled("lightPollution")) {
      lpPromptDismissed.value = false; // re-offer when toggled back on
      maybeAutoDownloadLp(); // auto-fetch the observer's region the first time LP is switched on
    }
    recomputeLpCoverage();
  },
);

// Rebuild the LP overlay (full-zoom, fresh tiles) when a new atlas is installed.
watch(
  () => lpStore.builtAtMs,
  () => refreshLpLayer(),
);
// Re-evaluate the download prompt when coverage changes (e.g. status just loaded).
watch(() => lpStore.coverage, recomputeLpCoverage);

// Re-point enabled weather overlays as the frames index loads or the scrubber/playback advances the frame
// (weatherFrameTimeMs changes → the tile URL's {time} changes → a cheap setUrl swap).
watch(
  () => [weather.framesMeta, weather.weatherFrameTimeMs] as const,
  () => {
    for (const o of enabledWeatherLayers.value) syncWeatherOverlay(o.id);
  },
);

// Repaint enabled live overlays as their frames load or the playhead moves in/out of the observed window.
watch(
  () => [weather.radarFrame, weather.satelliteFrame] as const,
  () => {
    for (const o of enabledLiveLayers.value) syncRainviewerOverlay(o.id);
  },
);

// Recenter and move the marker when the bound coordinates change (geolocation / manual entry).
watch(
  () => [props.lat, props.lon] as [number, number],
  ([lat, lon]) => {
    if (!lmap || !marker) return;
    marker.setLatLng([lat, lon]);
    lmap.setView([lat, lon]);
    maybeFetchWeather(true); // refetch the weather cube for the new site (if a grid layer is on)
  },
);

let searchTimer: ReturnType<typeof setTimeout> | null = null;
function onSearchInput() {
  open.value = true;
  searched.value = false;
  if (searchTimer) clearTimeout(searchTimer);
  searchTimer = setTimeout(runSearch, 350);
}

async function runSearch() {
  const q = search.value.trim();
  if (!q) {
    results.value = [];
    searched.value = false;
    return;
  }
  searching.value = true;
  try {
    const data = await apiGet<{ results: GeoResult[] }>(
      `/api/sky/geocode?q=${encodeURIComponent(q)}`,
    );
    results.value = data.results ?? [];
  } catch {
    results.value = [];
  } finally {
    searching.value = false;
    searched.value = true;
  }
}

function choose(r: GeoResult) {
  search.value = r.label;
  results.value = [];
  open.value = false;
  emit("pick", r.lat, r.lon, r.label);
}
</script>

<template>
  <div>
    <!-- High stacking context so the results dropdown floats above the Leaflet map below. -->
    <div class="relative z-[1000]">
      <input
        v-model="search"
        :class="input"
        type="text"
        :placeholder="t('tonight.location.searchPlaceholder')"
        autocomplete="off"
        @input="onSearchInput"
        @focus="open = true"
        @blur="open = false"
      />
      <ul
        v-if="
          open && search.trim() && (searching || results.length || searched)
        "
        class="absolute z-[1000] mt-1 max-h-48 w-full overflow-auto rounded-md border border-slate-200 bg-white text-sm shadow-lg dark:border-slate-700 dark:bg-slate-800"
      >
        <li v-if="searching" class="px-3 py-1.5 text-slate-400">
          {{ t("common.loading") }}
        </li>
        <li v-else-if="!results.length" class="px-3 py-1.5 text-slate-400">
          {{ t("tonight.location.noResults") }}
        </li>
        <li
          v-for="r in results"
          :key="`${r.lat},${r.lon}`"
          class="cursor-pointer truncate px-3 py-1.5 text-slate-700 hover:bg-slate-100 dark:text-slate-200 dark:hover:bg-slate-700"
          @mousedown.prevent="choose(r)"
        >
          {{ r.label }}
        </li>
      </ul>
    </div>
    <!-- z-0 confines Leaflet's internal z-indexes (panes/controls reach ~1000) below the dropdown. -->
    <div class="relative mt-2">
      <div
        ref="mapEl"
        class="relative z-0 aspect-square w-full overflow-hidden rounded-md border border-slate-200 dark:border-slate-700"
        :aria-label="t('tonight.location.map')"
      />
      <!-- Download-on-demand: when the light-pollution overlay is on but the viewed area isn't in the
           offline atlas, offer to download it (unioned with existing coverage). Overlaid on the map. -->
      <div
        v-if="showLpPrompt"
        class="absolute inset-x-2 bottom-2 z-[500] flex items-center gap-2 rounded-md border border-brand-500/40 bg-surface-raised/95 px-3 py-2 text-xs text-slate-200 shadow-lg backdrop-blur-sm"
      >
        <span v-if="lpStore.buildError" class="text-red-300">
          {{ t("tonight.lp.failed") }}: {{ lpStore.buildError }}
        </span>
        <span v-else>{{
          lpStore.building
            ? t("tonight.lp.downloading", {
                done: lpStore.state?.done ?? 0,
                total: lpStore.state?.total ?? 0,
              })
            : t("tonight.lp.uncovered")
        }}</span>
        <button
          v-if="!lpStore.building"
          :class="btnPrimary"
          class="ml-auto !px-2.5 !py-1 !text-xs"
          @click="downloadLpArea"
        >
          {{ t("tonight.lp.download") }}
        </button>
        <button
          v-if="!lpStore.building"
          class="rounded p-1 text-slate-400 transition-colors hover:text-slate-200"
          :aria-label="t('common.close')"
          @click="dismissLpPrompt"
        >
          ✕
        </button>
      </div>
    </div>
    <!-- Modular overlay toggles (light pollution now; weather/seeing later). -->
    <div
      v-if="overlays.length"
      class="mt-2 flex flex-wrap items-center gap-x-3 gap-y-1"
    >
      <span class="text-xs font-medium text-slate-500 dark:text-slate-400">{{
        t("tonight.layers.title")
      }}</span>
      <label
        v-for="o in overlays"
        :key="o.id"
        class="flex items-center gap-1 text-xs text-slate-600 dark:text-slate-300"
      >
        <input
          type="checkbox"
          class="accent-brand-600"
          :checked="isEnabled(o.id)"
          @change="toggle(o.id)"
        />
        {{ t(o.labelKey) }}
        <span
          v-if="o.live"
          class="rounded bg-emerald-500/15 px-1 text-[10px] font-medium uppercase tracking-wide text-emerald-600 dark:text-emerald-400"
          >{{ t("tonight.layers.live") }}</span
        >
      </label>
      <!-- Degraded-weather badge: the layers fail SOFT (stale frames or transparent tiles), so without
           this the difference between "clear skies" and "the data feed is down" was invisible. -->
      <span
        v-if="anyWeatherEnabled() && weather.warning"
        :title="weather.warning"
        class="rounded bg-amber-500/15 px-1.5 py-0.5 text-[10px] font-medium text-amber-600 dark:text-amber-400"
      >
        {{ t("tonight.layers.weatherDegraded") }}
      </span>
    </div>
    <WeatherTimeline v-if="anyAnimatedEnabled()" />
    <LightPollutionLegend v-if="isEnabled('lightPollution')" />
    <WeatherLegend
      v-for="o in enabledWeatherLayers"
      :key="o.id"
      :layer-id="o.id"
    />
    <RadarLegend v-if="isEnabled('radar')" />
  </div>
</template>
