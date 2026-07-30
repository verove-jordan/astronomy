<script setup lang="ts">
import { computed, ref } from "vue";
import { useI18n } from "vue-i18n";
import IconCompassArrow from "@/components/Icons/IconCompassArrow.vue";
import Pill from "@/components/Common/Pill.vue";
import StarFieldCanvas from "@/components/Mosaic/StarFieldCanvas.vue";
import TileProgressBar from "@/components/Mosaic/TileProgressBar.vue";
import type { StarFieldRect } from "@/composables/useStarFieldCanvas";
import { btnGhost, btnPrimary } from "@/constants/styles";
import { useNowTicker } from "@/composables/useNowTicker";
import { altAzAt } from "@/utils/altaz";
import { hourAngleDeg } from "@/utils/astro";
import { compass16 } from "@/utils/compass";
import { decToDMS, decToHC, raToHC, raToHMS } from "@/utils/sexagesimal";
import type { MosaicGrid, MosaicTile, MosaicTileStatus } from "@/types";

// One mosaic pointing at the scope: the hand-controller coordinates to key in (headline,
// monospace), a live "look there" line with a meridian-flip countdown, the expected star field
// with neighbor ghosts, and the panel-folder instruction.
const props = defineProps<{
  tile: MosaicTile;
  grid: MosaicGrid;
  neighbors: MosaicTile[];
  lat: number;
  lon: number;
  status: MosaicTileStatus;
  recommended: boolean;
  canUndo: boolean;
  windowMin: number;
}>();
const emit = defineEmits<{
  captured: [index: number];
  skip: [index: number];
  undo: [index: number];
}>();
const { t } = useI18n();
const now = useNowTicker();

const cardClass = computed(() => {
  if (props.status === "captured")
    return "border-l-4 border-l-green-500 bg-green-50/40 dark:bg-green-900/10";
  if (props.status === "skipped")
    return "border-l-4 border-l-amber-500 bg-amber-50/30 opacity-80 dark:bg-amber-900/10";
  if (props.recommended)
    return "border-l-4 border-l-brand-500 bg-brand-50/40 ring-1 ring-brand-500/40 dark:bg-brand-900/10";
  return "border-l-4 border-l-slate-300 opacity-70 dark:border-l-slate-700";
});

const live = computed(() =>
  altAzAt(
    props.tile.ra_deg,
    props.tile.dec_deg,
    props.lat,
    props.lon,
    now.value,
  ),
);

// Minutes until the tile crosses the meridian (negative = already past). 15.041°/h sidereal rate.
const minutesToTransit = computed(
  () =>
    (-hourAngleDeg(props.tile.ra_deg, props.lon, now.value) / 15.0410686) * 60,
);
const flipSoon = computed(
  () => minutesToTransit.value > 0 && minutesToTransit.value < props.windowMin,
);
const justFlipped = computed(
  () => minutesToTransit.value <= 0 && minutesToTransit.value > -15,
);

const showField = ref(false);
const fieldRects = computed<StarFieldRect[]>(() => [
  { cornersRaDec: props.tile.corners },
  ...props.neighbors.map((n) => ({ cornersRaDec: n.corners, dashed: true })),
]);

const copied = ref("");
async function copy(text: string, tag: string) {
  try {
    await navigator.clipboard.writeText(text);
    copied.value = tag;
    window.setTimeout(() => (copied.value = ""), 1500);
  } catch {
    // clipboard unavailable (non-secure context) — the text is on screen anyway
  }
}
</script>

<template>
  <div
    :id="`mosaic-tile-${tile.index}`"
    :class="[
      'rounded-lg border border-slate-200 p-3 transition-colors dark:border-slate-700',
      cardClass,
    ]"
  >
    <div class="flex items-start gap-3">
      <div
        class="flex h-7 w-7 shrink-0 items-center justify-center rounded-full bg-slate-200 text-sm font-bold text-slate-700 dark:bg-slate-700 dark:text-slate-100"
      >
        {{ tile.order }}
      </div>
      <div class="min-w-0 flex-1">
        <div class="flex flex-wrap items-center gap-2">
          <span class="font-semibold text-slate-800 dark:text-slate-100">
            {{ t("mosaic.tile.title", { folder: tile.folder }) }}
          </span>
          <Pill
            color-class="bg-slate-100 text-slate-600 dark:bg-slate-700 dark:text-slate-200"
            >r{{ tile.row }}c{{ tile.col }}</Pill
          >
          <Pill
            v-if="flipSoon"
            color-class="bg-amber-100 text-amber-800 dark:bg-amber-900/40 dark:text-amber-300"
            >{{
              t("mosaic.tile.flipWarn", { min: Math.round(minutesToTransit) })
            }}</Pill
          >
          <Pill
            v-else-if="justFlipped"
            color-class="bg-sky-100 text-sky-800 dark:bg-sky-900/40 dark:text-sky-300"
            >{{ t("mosaic.tile.justFlipped") }}</Pill
          >
        </div>

        <!-- Hand-controller entry block -->
        <div class="mt-2 space-y-1">
          <button
            class="block w-full rounded-md bg-slate-100 px-2 py-1 text-left font-mono text-xl font-bold tabular-nums text-slate-800 hover:bg-slate-200 dark:bg-slate-800 dark:text-slate-100 dark:hover:bg-slate-700"
            :title="t('mosaic.tile.copy')"
            @click="copy(raToHC(tile.ra_deg), 'ra')"
          >
            RA&nbsp;&nbsp;{{ raToHC(tile.ra_deg) }}
            <span v-if="copied === 'ra'" class="text-xs text-green-500">✓</span>
          </button>
          <button
            class="block w-full rounded-md bg-slate-100 px-2 py-1 text-left font-mono text-xl font-bold tabular-nums text-slate-800 hover:bg-slate-200 dark:bg-slate-800 dark:text-slate-100 dark:hover:bg-slate-700"
            :title="t('mosaic.tile.copy')"
            @click="copy(decToHC(tile.dec_deg), 'dec')"
          >
            Dec&nbsp;{{ decToHC(tile.dec_deg) }}
            <span v-if="copied === 'dec'" class="text-xs text-green-500"
              >✓</span
            >
          </button>
          <p class="text-[11px] text-slate-400">
            {{ raToHMS(tile.ra_deg) }} · {{ decToDMS(tile.dec_deg) }}
          </p>
        </div>

        <!-- Live pointing line -->
        <div
          class="mt-2 flex flex-wrap items-center gap-x-2 gap-y-0.5 text-sm text-slate-600 dark:text-slate-300"
        >
          <IconCompassArrow
            class="text-brand-500"
            :style="{ transform: `rotate(${live.azDeg}deg)` }"
          />
          <span class="font-medium">{{
            t("mosaic.tile.look", {
              dir: t(`goto.compass.${compass16(live.azDeg)}`),
              alt: Math.round(live.altDeg),
            })
          }}</span>
          <span class="text-xs text-slate-400"
            >·
            {{
              t("goto.card.meridian", {
                side: t(`goto.card.${tile.meridian_side}`),
              })
            }}</span
          >
        </div>

        <!-- What is already in the can for this tile (reconciled from the frames on disk) -->
        <TileProgressBar :folder="tile.folder" compact class="mt-2" />

        <!-- Folder instruction -->
        <p class="mt-2 text-xs text-slate-500 dark:text-slate-400">
          {{ t("mosaic.tile.folder") }}
          <button
            class="rounded bg-slate-100 px-1.5 py-0.5 font-mono font-semibold text-slate-700 hover:bg-slate-200 dark:bg-slate-800 dark:text-slate-200 dark:hover:bg-slate-700"
            @click="copy(tile.folder, 'folder')"
          >
            {{ tile.folder }}/
          </button>
          <span v-if="copied === 'folder'" class="text-green-500">✓</span>
        </p>

        <!-- Expected field (lazy: fetches its starfield only when opened) -->
        <button
          class="mt-2 text-xs text-brand-600 hover:underline dark:text-brand-300"
          @click="showField = !showField"
        >
          {{
            showField
              ? t("mosaic.tile.hideField")
              : t("mosaic.tile.expectedField")
          }}
        </button>
        <div v-if="showField" class="mt-2 aspect-square max-w-xs">
          <StarFieldCanvas
            :center-ra-deg="tile.ra_deg"
            :center-dec-deg="tile.dec_deg"
            :fov-deg="1.6 * Math.max(grid.tile_w_deg, grid.tile_h_deg)"
            :rects="fieldRects"
            class="h-full"
          />
        </div>
      </div>
    </div>

    <div
      v-if="recommended && status === 'pending'"
      class="mt-3 flex flex-wrap gap-2"
    >
      <button :class="btnPrimary" @click="emit('captured', tile.index)">
        {{ t("mosaic.tile.captured") }}
      </button>
      <button :class="btnGhost" @click="emit('skip', tile.index)">
        {{ t("mosaic.tile.skip") }}
      </button>
    </div>
    <div
      v-else-if="status !== 'pending'"
      class="mt-2 flex items-center justify-between"
    >
      <span
        class="text-xs font-medium"
        :class="
          status === 'captured'
            ? 'text-green-600 dark:text-green-400'
            : 'text-amber-600 dark:text-amber-400'
        "
        >{{
          status === "captured"
            ? `✓ ${t("mosaic.status.captured")}`
            : t("mosaic.status.skipped")
        }}</span
      >
      <button
        v-if="canUndo"
        class="text-xs text-slate-400 hover:text-slate-600 dark:hover:text-slate-200"
        @click="emit('undo', tile.index)"
      >
        {{ t("mosaic.tile.undo") }}
      </button>
    </div>
  </div>
</template>
