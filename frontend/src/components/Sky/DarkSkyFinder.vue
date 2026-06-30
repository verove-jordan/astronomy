<script setup lang="ts">
import { onMounted, onBeforeUnmount, ref, computed } from "vue";
import { useI18n } from "vue-i18n";
import {
  map as createMap,
  tileLayer,
  rectangle,
  circleMarker,
  latLngBounds,
  type Map as LMap,
  type Rectangle,
  type CircleMarker,
  type LeafletMouseEvent,
} from "leaflet";
import "leaflet/dist/leaflet.css";
import { useMapLayers } from "@/composables/useMapLayers";
import { useDarkSkyStore } from "@/stores/darksky";
import { useSkyStore } from "@/stores/sky";
import GenericTable, {
  type Column,
} from "@/components/Common/GenericTable.vue";
import LightPollutionLegend from "@/components/Sky/LightPollutionLegend.vue";
import BortleScalePicker from "@/components/Sky/BortleScalePicker.vue";
import { btnPrimary, btnGhost } from "@/constants/styles";
import { bortleColor } from "@/utils/bortle";
import type { DarkSite } from "@/types";

// Find the darkest, most open observing sites in a drawn map area. The user draws a rectangle, picks a
// max Bortle, and (optionally) evaluates horizon openness; results land as a ranked table + markers.
const emit = defineEmits<{ useLocation: [lat: number, lon: number] }>();
const { t } = useI18n();
const store = useDarkSkyStore();
const sky = useSkyStore();
const { overlays } = useMapLayers();

const mapEl = ref<HTMLDivElement | null>(null);
const maxBortle = ref(4);
const evalHorizon = ref(true);
const drawing = ref(false);
const hasArea = ref(false);

let lmap: LMap | null = null;
let areaRect: Rectangle | null = null;
let dragStart: { lat: number; lng: number } | null = null;
const markers: CircleMarker[] = [];
let detachWheel: (() => void) | null = null;

onMounted(() => {
  const el = mapEl.value;
  if (!el) return;
  lmap = createMap(el, { scrollWheelZoom: false, zoomSnap: 0 }).setView(
    [sky.query?.location.lat ?? 46.5, sky.query?.location.lon ?? 2.5],
    6,
  );
  tileLayer("https://tile.openstreetmap.org/{z}/{x}/{y}.png", {
    attribution: "© OpenStreetMap contributors",
    maxZoom: 19,
  }).addTo(lmap);
  // The finder is all about light pollution, so its overlay is always on (no toggle here).
  const lp = overlays.find((o) => o.id === "lightPollution");
  if (lp) {
    tileLayer(lp.url ?? "", {
      opacity: lp.opacity,
      attribution: lp.attribution,
      maxZoom: 19,
      maxNativeZoom: lp.maxNativeZoom,
    }).addTo(lmap);
  }

  // Area drawing: while active, a press-drag traces the search rectangle (map panning is suspended).
  lmap.on("mousedown", onMapMouseDown);
  lmap.on("mousemove", onMapMouseMove);
  lmap.on("mouseup", onMapMouseUp);

  // Trackpad gestures (mirrors LocationPicker): ⌘/ctrl+wheel zooms about the cursor; plain wheel pans.
  const onWheel = (e: WheelEvent) => {
    if (!lmap) return;
    e.preventDefault();
    if (e.ctrlKey || e.metaKey) {
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
});

onBeforeUnmount(() => {
  detachWheel?.();
  detachWheel = null;
  lmap?.remove();
  lmap = null;
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
  await store.find({
    minLat: b.getSouth(),
    minLon: b.getWest(),
    maxLat: b.getNorth(),
    maxLon: b.getEast(),
    maxBortle: maxBortle.value,
    horizon: evalHorizon.value,
    lat: sky.query?.location.lat,
    lon: sky.query?.location.lon,
  });
  renderMarkers();
}

function clearMarkers() {
  for (const m of markers) lmap?.removeLayer(m);
  markers.length = 0;
}
function renderMarkers() {
  clearMarkers();
  if (!lmap) return;
  for (const c of store.candidates) {
    markers.push(
      circleMarker([c.lat, c.lon], {
        radius: 6,
        color: "#0b0b0d",
        weight: 1,
        fillColor: bortleColor(c.bortle),
        fillOpacity: 0.9,
      }).addTo(lmap),
    );
  }
}
function focusCandidate(c: DarkSite) {
  if (!lmap) return;
  lmap.setView([c.lat, c.lon], Math.max(lmap.getZoom(), 9));
}
// Open the spot in Google Maps (drops a pin; Street View / satellite are one click away to scout it).
function mapsUrl(c: DarkSite): string {
  return `https://www.google.com/maps/search/?api=1&query=${c.lat},${c.lon}`;
}

type Row = Record<string, unknown>;
const rows = computed<Row[]>(() =>
  store.candidates.map((c, i) => ({
    n: i + 1,
    coords: `${c.lat.toFixed(3)}, ${c.lon.toFixed(3)}`,
    bortle: c.bortle,
    sqm: c.sqm,
    elevation: c.elevation_m ?? null,
    openness: c.horizon?.openness_pct ?? null,
    distance: c.distance_km,
    site: c as unknown,
  })),
);
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
    key: "elevation",
    label: t("darksky.elevation"),
    sortable: true,
    align: "right",
    format: (v) => (v == null ? "—" : `${Math.round(Number(v))} m`),
  },
  {
    key: "openness",
    label: t("darksky.openness"),
    sortable: true,
    align: "right",
    format: (v) => (v == null ? "—" : `${Math.round(Number(v))}%`),
  },
  {
    key: "distance",
    label: t("darksky.distance"),
    sortable: true,
    align: "right",
    format: (v) => `${Number(v).toFixed(0)} km`,
  },
  { key: "actions", label: "", align: "right" },
];
</script>

<template>
  <div class="space-y-3">
    <p class="text-sm text-slate-500 dark:text-slate-400">
      {{ t("darksky.hint") }}
    </p>

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
      <label class="flex items-center gap-2 text-sm">
        <input v-model="evalHorizon" type="checkbox" class="accent-brand-500" />
        {{ t("darksky.evalHorizon") }}
      </label>
      <button
        :class="btnPrimary"
        :disabled="!hasArea || store.loading"
        @click="search"
      >
        {{ store.loading ? t("common.loading") : t("darksky.search") }}
      </button>
    </div>

    <!-- map (static class — Leaflet manages this element's classes/cursor at runtime; see setMapCursor) -->
    <div
      ref="mapEl"
      class="relative z-0 h-[26rem] w-full overflow-hidden rounded-md border border-slate-200 dark:border-slate-700"
      :aria-label="t('darksky.mapLabel')"
    />
    <LightPollutionLegend />

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
      :columns="columns"
      :rows="rows"
      max-height="24rem"
    >
      <template #cell-coords="{ row }">
        <a
          :href="mapsUrl(row.site as DarkSite)"
          target="_blank"
          rel="noopener noreferrer"
          class="text-brand-600 hover:underline dark:text-brand-300"
          :title="t('darksky.openMaps')"
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
      <template #cell-actions="{ row }">
        <div class="flex items-center justify-end gap-1">
          <button
            :class="btnGhost"
            class="!px-2 !py-1 !text-xs"
            @click="focusCandidate(row.site as DarkSite)"
          >
            {{ t("darksky.locate") }}
          </button>
          <button
            :class="btnPrimary"
            class="!px-2 !py-1 !text-xs"
            @click="
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
  </div>
</template>
