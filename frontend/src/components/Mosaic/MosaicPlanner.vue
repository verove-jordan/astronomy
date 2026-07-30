<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import TwoPane from "@/components/Common/TwoPane.vue";
import AladinView from "@/components/Sky/AladinView.vue";
import MosaicControls from "@/components/Mosaic/MosaicControls.vue";
import MosaicTileTable from "@/components/Mosaic/MosaicTileTable.vue";
import PlanListPanel from "@/components/Mosaic/PlanListPanel.vue";
import { card } from "@/constants/styles";
import { useMosaicStore } from "@/stores/mosaic";
import type { SkyTarget } from "@/types";

// The Plan tab: real-sky preview of every tile footprint + the tile table (main pane), the
// geometry controls and saved plans (aside). Geometry is entirely server-computed; a knob change
// re-fetches the debounced preview.
const { t } = useI18n();
const store = useMosaicStore();

// AladinView centers on a SkyTarget; the planner synthesizes one from the resolved preview echo.
// It follows the GRID centre, not the object, so a hand-framed mosaic stays in view.
const centerTarget = computed<SkyTarget | null>(() => {
  const q = store.preview?.query;
  if (!q) return null;
  return {
    name: q.target || t("mosaic.controls.customTarget"),
    ra_deg: q.center_ra_deg ?? q.ra_deg,
    dec_deg: q.center_dec_deg ?? q.dec_deg,
  } as SkyTarget;
});

const gridCenter = computed(() => {
  const q = store.preview?.query;
  if (!q) return null;
  return {
    ra: q.center_ra_deg ?? q.ra_deg,
    dec: q.center_dec_deg ?? q.dec_deg,
  };
});

// The catalogued ellipse, drawn under the grid so the framing decision is made against what is
// actually being covered rather than against a bare rectangle.
const objectEllipse = computed(() => {
  const q = store.preview?.query;
  if (!q || !q.size_arcmin) return null;
  return {
    ra: q.ra_deg,
    dec: q.dec_deg,
    majorArcmin: q.size_arcmin,
    minorArcmin: q.size_minor_arcmin ?? q.size_arcmin,
    paDeg: q.object_pa_deg ?? 0,
  };
});

// Dragging the grid writes the new centre into the draft; the deep watcher below then recomputes
// the authoritative plan server-side.
function onGridMove(center: { ra: number; dec: number }) {
  store.draft.centerRaDeg = center.ra;
  store.draft.centerDecDeg = center.dec;
}

function onGridRotate(deltaPaDeg: number) {
  const next = (store.draft.cameraPaDeg + deltaPaDeg) % 360;
  store.draft.cameraPaDeg = Math.round(((next + 360) % 360) * 10) / 10;
}

const overlays = computed(() =>
  (store.preview?.tiles ?? []).map((tile) => ({
    corners: tile.corners,
    selected: tile.index === store.selectedTileIndex,
  })),
);

const tableRef = ref<InstanceType<typeof MosaicTileTable> | null>(null);
function onOverlayClick(i: number) {
  const tile = store.preview?.tiles[i];
  if (!tile) return;
  store.selectedTileIndex = tile.index;
  tableRef.value?.scrollToTile(tile.index);
}

// Any draft change recomputes the preview (debounced in the store); deep — the draft is one object.
watch(
  () => store.draft,
  () => void store.computePreview(),
  { deep: true },
);
</script>

<template>
  <TwoPane split="main-aside">
    <template #main>
      <div class="space-y-4">
        <div :class="card">
          <div class="mb-2 flex flex-wrap items-center justify-between gap-2">
            <h2
              class="text-sm font-semibold text-slate-700 dark:text-slate-200"
            >
              {{ t("mosaic.preview.title") }}
            </h2>
            <span v-if="store.previewLoading" class="text-xs text-slate-400">{{
              t("mosaic.preview.computing")
            }}</span>
          </div>
          <AladinView
            :target="centerTarget"
            :fov-w-deg="store.preview?.grid.tile_w_deg ?? 1"
            :fov-h-deg="store.preview?.grid.tile_h_deg ?? 1"
            :overlays="overlays"
            interactive
            :center="gridCenter"
            :object-ellipse="objectEllipse"
            @overlay-click="onOverlayClick"
            @grid-move="onGridMove"
            @grid-rotate="onGridRotate"
          />
          <p v-if="store.previewError" class="mt-2 text-sm text-danger-500">
            {{ store.previewError }}
          </p>
          <div
            v-if="store.preview?.warnings?.length"
            class="mt-2 flex flex-wrap gap-2"
          >
            <span
              v-for="code in store.preview.warnings"
              :key="code"
              class="rounded-md bg-amber-100 px-2 py-0.5 text-xs text-amber-800 dark:bg-amber-900/40 dark:text-amber-300"
            >
              {{ t(`mosaic.warnings.${code}`) }}
            </span>
          </div>
        </div>
        <MosaicTileTable ref="tableRef" />
      </div>
    </template>
    <template #aside>
      <div class="space-y-4">
        <MosaicControls />
        <PlanListPanel />
      </div>
    </template>
  </TwoPane>
</template>
