<script setup lang="ts">
import { computed, onMounted } from "vue";
import { useI18n } from "vue-i18n";
import IconMosaic from "@/components/Icons/IconMosaic.vue";
import { useMosaicStore } from "@/stores/mosaic";

// A GoTo cross-link shown while a mosaic capture is underway (some — not all — tiles done): once
// the mount is aligned, the next stop is the tile walk.
const { t } = useI18n();
const store = useMosaicStore();
onMounted(() => void store.listPlans());

// The second line is the real state of the project: frames actually on disk against the plan's
// goals, not just how many tiles have been ticked.
const detail = computed(() => {
  const plan = store.inProgressPlan;
  if (!plan?.capture_targets?.length) return "";
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
  if (!want) return "";
  const p = store.planProgress(plan);
  return t("mosaic.goto.continueDetail", {
    done: p.captured,
    total: p.total,
    pct: Math.round((got / want) * 100),
  });
});
</script>

<template>
  <router-link
    v-if="store.inProgressPlan"
    :to="{
      name: 'mosaic',
      query: { tab: 'capture', plan: String(store.inProgressPlan.id) },
    }"
    class="flex items-center gap-3 rounded-lg border border-brand-500/40 bg-brand-50/40 p-3 text-sm transition-colors hover:border-brand-500 dark:bg-brand-900/10"
  >
    <IconMosaic class="shrink-0 text-brand-500" />
    <span class="text-slate-700 dark:text-slate-200">
      {{
        t("mosaic.goto.continueCard", {
          name: store.inProgressPlan.name,
          done: store.planProgress(store.inProgressPlan).captured,
          total: store.inProgressPlan.tiles.length,
        })
      }}
      <span
        v-if="detail"
        class="block text-xs text-slate-500 dark:text-slate-400"
        >{{ detail }}</span
      >
    </span>
  </router-link>
</template>
