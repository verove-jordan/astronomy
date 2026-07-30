<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import ProgressBar from "@/components/Common/ProgressBar.vue";
import StatGrid from "@/components/Common/StatGrid.vue";
import {
  formatBytes,
  formatDurationClock,
  formatRate,
  formatTimestamp,
} from "@/utils/format";

// A compact live-progress card for a transfer-style job (today the S3→S3 move): a bar + throughput + ETA +
// elapsed, driven by plain props so the parent can source them from a job SSE stream. Byte figures come from
// the stream; Started/Elapsed/Remaining are derived here (Elapsed ticks once a second while running).
const props = defineProps<{
  title: string; // already-translated heading (e.g. "Moving 3 item(s)…")
  progress: number; // 0–100
  step?: string; // optional backend step text ("Moving 3/10 files · …")
  bytesDone: number;
  bytesTotal: number;
  bytesPerSec: number;
  startedAtMs?: number;
  running: boolean;
}>();

const { t } = useI18n();

// Live clock: tick once a second only while the transfer is running, so Elapsed advances between byte events.
const now = ref(Date.now());
let timer: ReturnType<typeof setInterval> | null = null;
function stop() {
  if (timer) {
    clearInterval(timer);
    timer = null;
  }
}
watch(
  () => props.running,
  (r) => {
    stop();
    if (r) {
      now.value = Date.now();
      timer = setInterval(() => (now.value = Date.now()), 1000);
    }
  },
  { immediate: true },
);
onBeforeUnmount(stop);

const elapsedMs = computed(() =>
  props.startedAtMs ? now.value - props.startedAtMs : 0,
);
// ETA from the smoothed rate — only meaningful while running with a positive rate and known total.
const etaMs = computed(() => {
  if (!props.running || props.bytesPerSec <= 0 || props.bytesTotal <= 0)
    return 0;
  const left = props.bytesTotal - props.bytesDone;
  return left > 0 ? (left / props.bytesPerSec) * 1000 : 0;
});

const stats = computed(() => [
  {
    label: t("storage.move.transferred"),
    value: `${formatBytes(props.bytesDone)} / ${formatBytes(props.bytesTotal)}`,
  },
  { label: t("storage.move.throughput"), value: formatRate(props.bytesPerSec) },
]);
</script>

<template>
  <div
    class="rounded-lg border border-slate-200 bg-slate-50 p-3 dark:border-slate-700 dark:bg-slate-800/60"
  >
    <div class="mb-2 flex items-baseline justify-between gap-3">
      <span
        class="truncate text-sm font-medium text-slate-700 dark:text-slate-200"
      >
        {{ title }}
      </span>
      <span class="shrink-0 text-sm tabular-nums text-slate-500">
        {{ Math.round(progress) }}%
      </span>
    </div>

    <ProgressBar :percent="progress" :active="running" />

    <p
      v-if="step"
      class="mt-2 truncate text-xs tabular-nums text-slate-500"
      :title="step"
    >
      {{ step }}
    </p>

    <StatGrid :items="stats" :cols="2" class="mt-3" />

    <div
      class="mt-3 grid grid-cols-2 gap-x-6 gap-y-1 text-xs tabular-nums text-slate-500 sm:grid-cols-3"
    >
      <div v-if="startedAtMs" class="min-w-0">
        <span class="text-slate-400">{{ t("storage.move.started") }}:</span>
        {{ formatTimestamp(startedAtMs) }}
      </div>
      <div class="min-w-0">
        <span class="text-slate-400">{{ t("storage.move.elapsed") }}:</span>
        {{ formatDurationClock(elapsedMs) }}
      </div>
      <div v-if="running" class="min-w-0">
        <span class="text-slate-400">{{ t("storage.move.remaining") }}:</span>
        ~{{ formatDurationClock(etaMs) }}
      </div>
    </div>
  </div>
</template>
