<script setup lang="ts">
import { computed, ref } from "vue";
import { useI18n } from "vue-i18n";
import ProgressBar from "@/components/Common/ProgressBar.vue";
import CameraAngleCard from "@/components/Mosaic/CameraAngleCard.vue";
import TileCard from "@/components/Mosaic/TileCard.vue";
import { useMosaicStore } from "@/stores/mosaic";
import type { MosaicTile, MosaicTileStatus } from "@/types";

// The ordered tile walk: orientation first, then one card per pointing in serpentine order. The
// first pending tile is "recommended"; Undo is offered only on the most recently actioned tile
// (deliberately NOT a generalization of Goto's AlignmentSequence — that one is welded to the goto
// store's star semantics).
const { t } = useI18n();
const store = useMosaicStore();

const plan = computed(() => store.activePlan);
const windowMin = ref(60); // expected minutes on one tile — drives the meridian-flip warning

const ordered = computed<MosaicTile[]>(() =>
  [...(plan.value?.tiles ?? [])].sort((a, b) => a.order - b.order),
);

function statusOf(tile: MosaicTile): MosaicTileStatus {
  return plan.value?.tile_status[String(tile.index)] ?? "pending";
}

// The recommended tile is the first one that is neither ticked nor complete against its capture
// targets — so "where do I point next?" survives a night that ended mid-tile.
const recommendedIndex = computed(() => {
  if (!plan.value?.orientation_done) return null;
  const next = ordered.value.find(
    (tile) =>
      statusOf(tile) === "pending" &&
      (store.tileFraction(tile.folder) ?? 0) < 1,
  );
  return next ? next.index : null;
});

// Scroll straight to the recommended tile: the "resume where I stopped" button.
function resume() {
  const idx = recommendedIndex.value;
  if (idx === null) return;
  document
    .getElementById(`mosaic-tile-${idx}`)
    ?.scrollIntoView({ behavior: "smooth", block: "center" });
}

// The most recently actioned tile (highest order with a status) is the only one offering Undo.
const undoableIndex = computed(() => {
  for (let i = ordered.value.length - 1; i >= 0; i--) {
    if (statusOf(ordered.value[i]) !== "pending") return ordered.value[i].index;
  }
  return null;
});

function neighborsOf(tile: MosaicTile): MosaicTile[] {
  return (plan.value?.tiles ?? []).filter(
    (other) =>
      Math.abs(other.row - tile.row) + Math.abs(other.col - tile.col) === 1,
  );
}

const actionError = ref("");
async function act(fn: () => Promise<void>) {
  actionError.value = "";
  try {
    await fn();
  } catch (e) {
    actionError.value = t("mosaic.capture.offlineHint", {
      error: String(e instanceof Error ? e.message : e),
    });
  }
}

const resetConfirm = ref(false);
async function resetAll() {
  if (!resetConfirm.value) {
    resetConfirm.value = true;
    window.setTimeout(() => (resetConfirm.value = false), 4000);
    return;
  }
  resetConfirm.value = false;
  for (const tile of ordered.value) {
    if (statusOf(tile) !== "pending")
      await act(() => store.setTileStatus(tile.index, "pending"));
  }
}
</script>

<template>
  <div v-if="plan" class="space-y-3">
    <div class="flex flex-wrap items-center gap-3">
      <div class="min-w-40 flex-1">
        <ProgressBar
          :percent="
            store.progress
              ? (100 * store.progress.captured) /
                Math.max(1, store.progress.total)
              : 0
          "
        />
      </div>
      <span class="text-sm text-slate-500 dark:text-slate-400">{{
        t("mosaic.capture.progress", {
          captured: store.progress?.captured ?? 0,
          total: store.progress?.total ?? 0,
        })
      }}</span>
      <label class="flex items-center gap-1.5 text-xs text-slate-400">
        {{ t("mosaic.capture.window") }}
        <input
          v-model.number="windowMin"
          type="number"
          min="5"
          max="480"
          class="w-16 rounded-md border border-slate-300 bg-white px-1 py-0.5 text-xs dark:border-slate-600 dark:bg-slate-800 dark:text-slate-100"
        />
      </label>
      <button
        v-if="recommendedIndex !== null"
        class="text-xs text-brand-600 hover:underline dark:text-brand-300"
        @click="resume"
      >
        {{ t("mosaic.capture.resume") }}
      </button>
      <button
        class="text-xs text-slate-400 hover:text-danger-500"
        @click="resetAll"
      >
        {{
          resetConfirm
            ? t("mosaic.capture.confirmReset")
            : t("mosaic.capture.reset")
        }}
      </button>
    </div>
    <p v-if="actionError" class="text-xs text-danger-500">{{ actionError }}</p>

    <CameraAngleCard />

    <TileCard
      v-for="tile in ordered"
      :key="tile.index"
      :tile="tile"
      :grid="plan.grid"
      :neighbors="neighborsOf(tile)"
      :lat="plan.request.lat"
      :lon="plan.request.lon"
      :status="statusOf(tile)"
      :recommended="tile.index === recommendedIndex"
      :can-undo="tile.index === undoableIndex"
      :window-min="windowMin"
      @captured="(i) => act(() => store.setTileStatus(i, 'captured'))"
      @skip="(i) => act(() => store.setTileStatus(i, 'skipped'))"
      @undo="(i) => act(() => store.setTileStatus(i, 'pending'))"
    />
  </div>
</template>
