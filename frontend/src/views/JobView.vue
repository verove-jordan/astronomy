<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from "vue";
import { useI18n } from "vue-i18n";
import { useJobsStore } from "@/stores/jobs";
import { useJobStream } from "@/composables/useJobStream";
import { fileUrl } from "@/services/api";
import {
  summaryFromChannels,
  summaryFromInventory,
} from "@/composables/useCaptureSummary";
import StatusPill from "@/components/Common/StatusPill.vue";
import ProgressBar from "@/components/Common/ProgressBar.vue";
import LogConsole from "@/components/Common/LogConsole.vue";
import CaptureSummary from "@/components/Capture/CaptureSummary.vue";
import ChannelMappingList from "@/components/Capture/ChannelMappingList.vue";
import RunResultPanels from "@/components/Common/RunResultPanels.vue";
import SupervisorPanel from "@/components/Common/SupervisorPanel.vue";
import { btnDanger, card } from "@/constants/styles";
import { baseName, formatBytes } from "@/utils/format";
import type { Inventory } from "@/types";

const props = defineProps<{ id: string }>();
const { t } = useI18n();
const jobsStore = useJobsStore();

const jobId = Number(props.id);
const {
  progress,
  step,
  status,
  done,
  lines,
  preview,
  rssBytes,
  cpuPercent,
  peakRssBytes,
  seed,
} = useJobStream(jobId, () => jobsStore.get(jobId));

const reInv = ref<Inventory | null>(null);
const cancelling = ref(false);

onMounted(async () => {
  await jobsStore.get(jobId);
  const jb = jobsStore.current;
  if (jb?.log_tail) seed(jb.log_tail.split("\n"));
  // If the create-time inventory was lost (hard reload) and the job is still running, re-inspect.
  const stillRunning =
    jb && (jb.status === "running" || jb.status === "queued");
  if (stillRunning && !jobsStore.captureFor(jobId) && jb?.params?.path) {
    reInv.value = await jobsStore.inspectCapture(jb.params.path);
  }
});

const job = computed(() => jobsStore.current);
const result = computed(() => job.value?.result);
const liveStatus = computed(() =>
  done.value ? (job.value?.status ?? status.value) : status.value,
);
const running = computed(
  () => liveStatus.value === "running" || liveStatus.value === "queued",
);
// Live-stacking jobs run until stopped; the "cancel" affordance is really "stop & finalize".
const isLive = computed(() => job.value?.params?.mode === "livestack");

// Processing timer: ticks each second while running, then freezes at the total once the job finishes.
const now = ref(Date.now());
let timer: ReturnType<typeof setInterval> | null = null;
onMounted(() => {
  timer = setInterval(() => (now.value = Date.now()), 1000);
});
onBeforeUnmount(() => {
  if (timer) clearInterval(timer);
});
const elapsedMs = computed(() => {
  const start = job.value?.created_at;
  if (!start) return 0;
  const end = running.value ? now.value : (job.value?.updated_at ?? now.value);
  return Math.max(0, end - start);
});
function fmtElapsed(ms: number): string {
  const s = Math.floor(ms / 1000);
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  const pad = (n: number) => String(n).padStart(2, "0");
  return h > 0 ? `${h}:${pad(m)}:${pad(s % 60)}` : `${m}:${pad(s % 60)}`;
}

const stashed = computed(() => jobsStore.captureFor(jobId));
const summary = computed(() => {
  if (stashed.value) return summaryFromInventory(stashed.value);
  if (reInv.value) return summaryFromInventory(reInv.value);
  if (result.value?.channels?.length)
    return summaryFromChannels(
      result.value.object || "",
      result.value.channels,
    );
  return null;
});
const detection = computed(
  () =>
    result.value?.detection ||
    stashed.value?.channel_detection ||
    reInv.value?.channel_detection ||
    null,
);
const previewUrl = computed(() =>
  preview.value ? fileUrl(preview.value) : "",
);

async function cancelJob() {
  cancelling.value = true;
  try {
    await jobsStore.cancel(jobId);
  } finally {
    cancelling.value = false;
  }
}
</script>

<template>
  <div class="space-y-6">
    <div class="flex flex-wrap items-center gap-3">
      <h1 class="text-2xl font-semibold">{{ t("job.title") }} #{{ jobId }}</h1>
      <StatusPill :status="String(liveStatus)" />
      <span v-if="job?.params?.mode" class="text-sm text-slate-500">
        {{ t("run.modes." + job.params.mode) }} ·
        {{ t("run.formats." + (job.params.format || "image")) }}
      </span>
      <span v-if="result?.input_dir" class="text-sm text-slate-500">{{
        baseName(result.input_dir)
      }}</span>
      <span
        v-if="job && !running && elapsedMs > 0"
        class="text-sm text-slate-500"
      >
        {{ t("job.duration") }} {{ fmtElapsed(elapsedMs) }}
      </span>
      <button
        v-if="running"
        :class="[btnDanger, 'ml-auto']"
        :disabled="cancelling"
        @click="cancelJob"
      >
        {{ isLive ? t("job.stopFinalize") : t("job.cancel") }}
      </button>
    </div>

    <p
      v-if="job?.error"
      class="rounded-md bg-red-100 p-3 text-sm text-red-800 dark:bg-red-900/40 dark:text-red-300"
    >
      {{ job.error }}
    </p>

    <!-- While processing: keep capture context visible + live progress, logs and preview -->
    <template v-if="running">
      <div :class="card">
        <div class="mb-2 flex items-center justify-between gap-3 text-sm">
          <span class="min-w-0 truncate">{{
            step || t("common.loading")
          }}</span>
          <span
            class="flex shrink-0 items-center gap-3 text-slate-500 dark:text-slate-400"
          >
            <span class="tabular-nums">{{ fmtElapsed(elapsedMs) }}</span>
            <span class="font-medium text-slate-700 dark:text-slate-200"
              >{{ progress }}%</span
            >
          </span>
        </div>
        <ProgressBar :percent="progress" :active="running" />
        <div
          v-if="rssBytes || cpuPercent"
          class="mt-3 flex flex-wrap items-center gap-x-5 gap-y-1 text-xs text-slate-500 dark:text-slate-400"
        >
          <span
            >{{ t("job.cpu") }}
            <span class="font-medium text-slate-700 dark:text-slate-200"
              >{{ Math.round(cpuPercent) }}%</span
            ></span
          >
          <span
            >{{ t("job.memory") }}
            <span class="font-medium text-slate-700 dark:text-slate-200">{{
              formatBytes(rssBytes)
            }}</span></span
          >
          <span v-if="peakRssBytes"
            >{{ t("job.peak") }}
            <span class="font-medium text-slate-700 dark:text-slate-200">{{
              formatBytes(peakRssBytes)
            }}</span></span
          >
        </div>
      </div>

      <div class="grid gap-4 lg:grid-cols-2">
        <CaptureSummary v-if="summary" :summary="summary" />
        <ChannelMappingList v-if="detection" :detection="detection" />
      </div>

      <section v-if="previewUrl" :class="card">
        <h2 class="mb-3 text-lg font-medium">{{ t("job.livePreview") }}</h2>
        <img
          :src="previewUrl"
          alt="live preview"
          class="block max-h-[28rem] w-full max-w-full rounded-md border border-slate-200 object-contain dark:border-slate-700"
        />
      </section>

      <LogConsole :lines="lines" />
    </template>

    <!-- After completion: the full result panels + the supervisor iteration timeline -->
    <template v-else-if="result && (result.channels?.length || result.final)">
      <RunResultPanels :result="result" />
      <SupervisorPanel :result="result" />
    </template>
  </div>
</template>
