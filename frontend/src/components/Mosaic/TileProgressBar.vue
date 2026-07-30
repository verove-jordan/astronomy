<script setup lang="ts">
import { computed } from "vue";
import { useI18n } from "vue-i18n";
import { useMosaicStore } from "@/stores/mosaic";

// Per-filter capture progress for one tile — the "L 12/40 · R 0/20" line that turns a multi-night
// mosaic into something resumable. Counts come from the frames on disk (reconciled), not from a
// checkbox, so they survive app restarts, other capture software and a forgotten tick.
const props = defineProps<{ folder: string; compact?: boolean }>();
const { t } = useI18n();
const store = useMosaicStore();

const rows = computed(() => {
  const targets = store.activePlan?.capture_targets ?? [];
  const done = store.progressFor(props.folder);
  if (targets.length) {
    return targets.map((tgt) => ({
      filter: tgt.filter,
      got: done[tgt.filter]?.frames ?? 0,
      want: tgt.frames,
    }));
  }
  // No targets set: show what exists, with no denominator to compare against.
  return Object.entries(done).map(([filter, p]) => ({
    filter,
    got: p.frames,
    want: 0,
  }));
});

const anything = computed(() =>
  rows.value.some((r) => r.got > 0 || r.want > 0),
);

function pct(got: number, want: number): number {
  if (want <= 0) return got > 0 ? 100 : 0;
  return Math.min(100, Math.round((got / want) * 100));
}
</script>

<template>
  <div v-if="anything" class="space-y-1">
    <div
      v-for="row in rows"
      :key="row.filter"
      class="flex items-center gap-2 text-[11px]"
    >
      <span class="w-6 shrink-0 font-mono text-slate-500 dark:text-slate-400">{{
        row.filter
      }}</span>
      <div
        class="h-1.5 flex-1 rounded-full bg-slate-200 dark:bg-slate-700"
        :title="t('mosaic.progress.bar', { got: row.got, want: row.want })"
      >
        <div
          class="h-1.5 rounded-full transition-all"
          :class="
            row.want > 0 && row.got >= row.want ? 'bg-success' : 'bg-brand-500'
          "
          :style="{ width: `${pct(row.got, row.want)}%` }"
        />
      </div>
      <span class="w-14 shrink-0 text-right text-slate-500 dark:text-slate-400">
        {{ row.got }}<template v-if="row.want > 0">/{{ row.want }}</template>
      </span>
    </div>
    <p
      v-if="!compact && store.activePlan?.capture_root"
      class="text-[10px] text-slate-400"
    >
      {{ t("mosaic.progress.fromDisk") }}
    </p>
  </div>
</template>
