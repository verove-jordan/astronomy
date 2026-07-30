<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { btnGhost, btnPrimary, card, input } from "@/constants/styles";
import { useMosaicStore } from "@/stores/mosaic";
import type { MosaicCaptureTarget } from "@/types";

import { nextUnusedFilter } from "@/constants/filters";
// A mosaic is shot over many nights, so the Capture tab needs to answer two questions before
// anything else: how much is enough per tile (the targets), and how much is already in the can
// (reconciled from the frames on disk). Everything below the fold — the tile cards — reads from
// these two.
const { t } = useI18n();
const store = useMosaicStore();

const targets = ref<MosaicCaptureTarget[]>([]);
const rootPath = ref("");
const busyMsg = ref("");
const errorMsg = ref("");
const editing = ref(false);

// Re-seed the editor whenever a different plan is loaded, so it never shows another plan's goals.
watch(
  () => store.activePlan?.id,
  () => {
    targets.value = (store.activePlan?.capture_targets ?? []).map((tgt) => ({
      ...tgt,
    }));
    rootPath.value = store.activePlan?.capture_root ?? "";
  },
  { immediate: true },
);

const totalFramesGoal = computed(() =>
  targets.value.reduce((n, tgt) => n + Math.max(0, tgt.frames || 0), 0),
);

// Overall completion across every tile: the honest "how far into this project am I" number.
const overall = computed(() => {
  const plan = store.activePlan;
  if (!plan || !plan.capture_targets?.length) return null;
  let got = 0;
  let want = 0;
  for (const tile of plan.tiles) {
    for (const tgt of plan.capture_targets) {
      if (tgt.frames <= 0) continue;
      want += tgt.frames;
      got += Math.min(
        tgt.frames,
        plan.tile_progress?.[tile.folder]?.[tgt.filter]?.frames ?? 0,
      );
    }
  }
  return want ? { got, want, pct: Math.round((got / want) * 100) } : null;
});

const reconciledAt = computed(() => {
  const ms = store.activePlan?.reconciled_at;
  return ms ? new Date(ms).toLocaleString() : "";
});

function addTarget() {
  const next = nextUnusedFilter(targets.value.map((tgt) => tgt.filter));
  targets.value.push({ filter: next, frames: 20, exposure_ms: 120000 });
}

function removeTarget(i: number) {
  targets.value.splice(i, 1);
}

async function saveTargets() {
  errorMsg.value = "";
  busyMsg.value = t("mosaic.targets.saving");
  try {
    await store.setCaptureTargets(
      targets.value.filter((tgt) => tgt.filter.trim()),
    );
    editing.value = false;
    busyMsg.value = "";
  } catch (e) {
    busyMsg.value = "";
    errorMsg.value = String(e instanceof Error ? e.message : e);
  }
}

async function reconcile() {
  errorMsg.value = "";
  const path = rootPath.value.trim();
  if (!path) {
    errorMsg.value = t("mosaic.targets.pathRequired");
    return;
  }
  busyMsg.value = t("mosaic.targets.scanning");
  try {
    const marked = await store.reconcile(path);
    busyMsg.value = t("mosaic.targets.scanned", { n: marked });
  } catch (e) {
    busyMsg.value = "";
    errorMsg.value = String(e instanceof Error ? e.message : e);
  }
}
</script>

<template>
  <section v-if="store.activePlan" :class="card" class="space-y-3">
    <div class="flex flex-wrap items-center justify-between gap-2">
      <h2 class="text-sm font-semibold text-slate-700 dark:text-slate-200">
        {{ t("mosaic.targets.title") }}
      </h2>
      <button
        class="text-xs text-brand-600 hover:underline dark:text-brand-300"
        @click="editing = !editing"
      >
        {{ editing ? t("mosaic.targets.close") : t("mosaic.targets.edit") }}
      </button>
    </div>

    <!-- Overall progress -->
    <div v-if="overall">
      <div
        class="flex items-center justify-between text-xs text-slate-500 dark:text-slate-400"
      >
        <span>{{ t("mosaic.targets.overall") }}</span>
        <span>{{ overall.got }}/{{ overall.want }} · {{ overall.pct }}%</span>
      </div>
      <div class="mt-1 h-2 w-full rounded-full bg-slate-200 dark:bg-slate-700">
        <div
          class="h-2 rounded-full bg-brand-500 transition-all"
          :style="{ width: `${overall.pct}%` }"
        />
      </div>
    </div>
    <p v-else class="text-xs text-slate-400">
      {{ t("mosaic.targets.noTargets") }}
    </p>

    <!-- Target editor -->
    <div v-if="editing" class="space-y-2">
      <div
        v-for="(tgt, i) in targets"
        :key="i"
        class="flex flex-wrap items-end gap-2"
      >
        <label class="text-xs text-slate-500 dark:text-slate-400"
          >{{ t("mosaic.targets.filter") }}
          <input v-model="tgt.filter" :class="input" class="w-20" />
        </label>
        <label class="text-xs text-slate-500 dark:text-slate-400"
          >{{ t("mosaic.targets.frames") }}
          <input
            v-model.number="tgt.frames"
            type="number"
            min="0"
            :class="input"
            class="w-20"
          />
        </label>
        <label class="text-xs text-slate-500 dark:text-slate-400"
          >{{ t("mosaic.targets.exposure") }}
          <input
            :value="(tgt.exposure_ms ?? 0) / 1000"
            type="number"
            min="0"
            step="0.1"
            :class="input"
            class="w-24"
            @input="
              tgt.exposure_ms =
                Number(($event.target as HTMLInputElement).value) * 1000
            "
          />
        </label>
        <label class="text-xs text-slate-500 dark:text-slate-400"
          >{{ t("mosaic.targets.gain") }}
          <input
            v-model.number="tgt.gain"
            type="number"
            min="0"
            :class="input"
            class="w-20"
          />
        </label>
        <button
          class="pb-2 text-xs text-danger-500 hover:underline"
          @click="removeTarget(i)"
        >
          {{ t("mosaic.targets.remove") }}
        </button>
      </div>
      <div class="flex flex-wrap items-center gap-2">
        <button
          :class="btnGhost"
          class="!px-2 !py-1 text-xs"
          @click="addTarget"
        >
          {{ t("mosaic.targets.add") }}
        </button>
        <button
          :class="btnPrimary"
          class="!px-3 !py-1 text-xs"
          :disabled="store.busy"
          @click="saveTargets"
        >
          {{ t("mosaic.targets.save") }}
        </button>
        <span class="text-xs text-slate-400">{{
          t("mosaic.targets.perTile", { n: totalFramesGoal })
        }}</span>
      </div>
    </div>

    <!-- Reconcile with the disk -->
    <div class="space-y-1 border-t border-slate-200 pt-3 dark:border-slate-700">
      <label
        class="text-xs text-slate-500 dark:text-slate-400"
        for="mosaic-capture-root"
        >{{ t("mosaic.targets.rootLabel") }}</label
      >
      <div class="flex flex-wrap gap-2">
        <input
          id="mosaic-capture-root"
          v-model="rootPath"
          :class="input"
          class="flex-1"
          :placeholder="t('mosaic.targets.rootPlaceholder')"
        />
        <button :class="btnGhost" :disabled="store.busy" @click="reconcile">
          {{ t("mosaic.targets.scan") }}
        </button>
      </div>
      <p class="text-[11px] text-slate-400">
        {{ t("mosaic.targets.rootHint") }}
        <template v-if="reconciledAt">
          · {{ t("mosaic.targets.lastScan", { at: reconciledAt }) }}
        </template>
      </p>
      <p v-if="busyMsg" class="text-xs text-brand-600 dark:text-brand-300">
        {{ busyMsg }}
      </p>
      <p v-if="errorMsg" class="text-xs text-danger-500">{{ errorMsg }}</p>
    </div>
  </section>
</template>
