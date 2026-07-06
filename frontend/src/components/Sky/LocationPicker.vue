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
import { useWeatherStore } from "@/stores/weather";
import { useLightPollutionStore } from "@/stores/lightpollution";
import { createWeatherGridLayer } from "@/composables/useWeatherGridLayer";
import { gridLayerById } from "@/utils/weather";
import LightPollutionLegend from "@/components/Sky/LightPollutionLegend.vue";
import WeatherTimeline from "@/components/Sky/WeatherTimeline.vue";
import WeatherLegend from "@/components/Sky/WeatherLegend.vue";
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

// Modular overlay layers: light-pollution tiles + the animated weather grid layers, all toggled here.
const { overlays, isEnabled, toggle, anyGridEnabled } = useMapLayers();
const weather = useWeatherStore();
const lpStore = useLightPollutionStore();

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

let lmap: LMap | null = null;
let marker: CircleMarker | null = null;
const overlayLayers: Record<string, TileLayer> = {}; // tile overlays (light pollution)
const gridLayers: Record<
  string,
  ReturnType<typeof createWeatherGridLayer>
> = {}; // animated weather grids
let detachWheel: (() => void) | null = null;

// The weather grid layers currently enabled — drives the legends shown under the map.
const enabledGridLayers = computed(() =>
  overlays.filter((o) => o.kind === "grid" && isEnabled(o.id)),
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
  // Zoom 7 frames the observer's region (~500 km across) so the ±4° weather grid fully covers the view
  // (at zoom 6 the wider view outran the overlay, leaving uncovered edge strips).
  lmap = createMap(el, { scrollWheelZoom: false, zoomSnap: 0 }).setView(
    [props.lat, props.lon],
    7,
  );
  addDarkBaseMap(lmap);
  // Overlay layers (kept above the base map; the marker, a vector layer, stays on top of both). Tile
  // overlays are XYZ proxies; grid overlays are canvas image-overlays painted from the weather cube.
  for (const o of overlays) {
    if (o.kind === "grid") {
      gridLayers[o.id] = createWeatherGridLayer(o.opacity);
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

  // Trackpad gestures: browsers report a pinch as ctrl/⌘ + wheel → zoom around the cursor; a plain
  // two-finger scroll (no modifier) → pan. preventDefault stops the page from scrolling/zooming.
  const onWheel = (e: WheelEvent) => {
    if (!lmap) return;
    e.preventDefault();
    if (e.ctrlKey || e.metaKey) {
      // Pinch → zoom around the cursor. Work in Leaflet's zoom-level (log) space, so the same gesture
      // gives the same perceptual zoom at any level. Normalize line-mode wheels, use a snappy factor,
      // and clamp per event so one fast pinch doesn't overshoot.
      const px = e.deltaMode === 1 ? e.deltaY * 16 : e.deltaY;
      const dz = Math.max(-1.2, Math.min(1.2, -px * 0.035));
      lmap.setZoomAround(
        lmap.mouseEventToContainerPoint(e),
        lmap.getZoom() + dz,
      );
    } else {
      lmap.panBy([e.deltaX, e.deltaY], { animate: false });
    }
  };
  el.addEventListener("wheel", onWheel, { passive: false });
  detachWheel = () => el.removeEventListener("wheel", onWheel);

  syncOverlays();
  maybeFetchWeather(); // load the weather cube if an animated layer was left enabled

  // Light pollution: learn what the installed atlas covers, then re-check as the user pans so we can
  // offer to download data for an uncovered area (and refetch full-zoom tiles once a new atlas lands).
  lmap.on("moveend", recomputeLpCoverage);
  lmap.on("moveend zoomend", onMapMovedForGrid); // weather grid follows the viewport (debounced)
  lpStore.fetchStatus().then(() => {
    lpStatusReady.value = true;
    recomputeLpCoverage();
  });
});

onBeforeUnmount(() => {
  detachWheel?.();
  detachWheel = null;
  if (gridMoveTimer) clearTimeout(gridMoveTimer);
  lmap?.remove();
  lmap = null;
  marker = null;
});

// syncOverlays reconciles the Leaflet layers with the enabled set (persisted in the composable). Tile
// overlays add/remove directly; grid overlays are painted from the weather cube (added once data is in).
function syncOverlays() {
  if (!lmap) return;
  for (const o of overlays) {
    if (o.kind === "grid") {
      syncGridOverlay(o.id);
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

// syncGridOverlay adds/removes one weather grid layer, painting the current frame when it is enabled.
function syncGridOverlay(id: string) {
  const wrapper = gridLayers[id];
  if (!lmap || !wrapper) return;
  if (isEnabled(id) && weather.grid) {
    const def = gridLayerById(id);
    if (!def) return;
    const layer = wrapper.update(weather.grid, weather.frameIndex, def);
    if (!lmap.hasLayer(layer)) layer.addTo(lmap);
  } else if (wrapper.layer && lmap.hasLayer(wrapper.layer)) {
    lmap.removeLayer(wrapper.layer);
  }
}

// Fetch the weather cube for the CURRENT map viewport (centre + half-span) whenever an animated layer
// is on, so the overlay always shows real data where the map is focused — not a fixed box around the
// initial site.
function maybeFetchWeather(force = false) {
  if (!lmap || !anyGridEnabled()) return;
  const c = lmap.getCenter();
  const b = lmap.getBounds();
  const radius =
    Math.max(b.getNorth() - b.getSouth(), b.getEast() - b.getWest()) / 2;
  void weather.fetchGrid(c.lat, c.lng, radius, force);
}

// Refetch the grid for the new viewport on pan/zoom (debounced), so the layer follows the map.
let gridMoveTimer: ReturnType<typeof setTimeout> | null = null;
function onMapMovedForGrid() {
  if (!anyGridEnabled()) return;
  if (gridMoveTimer) clearTimeout(gridMoveTimer);
  gridMoveTimer = setTimeout(() => maybeFetchWeather(), 450);
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

watch(
  () => overlays.map((o) => isEnabled(o.id)),
  () => {
    syncOverlays();
    maybeFetchWeather();
    if (isEnabled("lightPollution")) lpPromptDismissed.value = false; // re-offer when toggled back on
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

// Repaint enabled grid overlays as the cube loads or the scrubber/playback advances the frame.
watch(
  () => [weather.grid, weather.frameIndex] as const,
  () => {
    for (const o of enabledGridLayers.value) syncGridOverlay(o.id);
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
      </label>
    </div>
    <WeatherTimeline v-if="anyGridEnabled()" />
    <LightPollutionLegend v-if="isEnabled('lightPollution')" />
    <WeatherLegend
      v-for="o in enabledGridLayers"
      :key="o.id"
      :layer-id="o.id"
    />
  </div>
</template>
