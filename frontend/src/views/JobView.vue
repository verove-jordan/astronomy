<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from "vue";
import { useRouter } from "vue-router";
import { useI18n } from "vue-i18n";
import { useJobsStore } from "@/stores/jobs";
import { useAgentStore } from "@/stores/agent";
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
import SupervisorChat from "@/components/Common/SupervisorChat.vue";
import StagePreviewTimeline from "@/components/Common/StagePreviewTimeline.vue";
import SeriesTimeline from "@/components/Common/SeriesTimeline.vue";
import EnvWarnings from "@/components/Common/EnvWarnings.vue";
import { btnDanger, btnPrimary, card } from "@/constants/styles";
import { baseName, formatBytes } from "@/utils/format";
import type { Inventory } from "@/types";

const props = defineProps<{ id: string }>();
const { t } = useI18n();
const router = useRouter();
const jobsStore = useJobsStore();
const agent = useAgentStore();
// Set (this session) for a supervised/refine run: the id of its live steerable conversation turn.
const turnId = computed(() => jobsStore.turnFor(jobId));

const TERMINAL = ["succeeded", "failed", "cancelled"];
function isTerminal(s?: string): boolean {
  return !!s && TERMINAL.includes(s);
}

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
  iterations,
  stagePreviews,
  seed,
} = useJobStream(jobId, () => jobsStore.get(jobId));

const reInv = ref<Inventory | null>(null);
const cancelling = ref(false);

onMounted(async () => {
  // A supervised/refine run started this session has a live conversation turn — open it so its previews,
  // reasoning and steering show inline (and it lands in the AstroAgent history). Fire-and-forget; the
  // stream ends itself when the run finishes.
  if (turnId.value) {
    void agent.watchTurn(turnId.value, {
      title: t("supervisorChat.convTitle", { id: jobId }),
    });
  }
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
// The persisted DB status is authoritative for a finished job: a row that is failed/cancelled/succeeded
// can never still be running, so trust it over the SSE stream. This also un-sticks the case in the
// screenshot — a terminally-failed job whose stream delivered no snapshot kept showing the stream's
// seed status ("queued"), which lit up the processing UI (Cancel button, "loading…" bar) for a job that
// had actually failed. Only fall back to the live stream status while the job is not yet terminal.
const liveStatus = computed(() => {
  const persisted = job.value?.status;
  if (isTerminal(persisted)) return persisted;
  return done.value ? (persisted ?? status.value) : status.value;
});
const running = computed(
  () => liveStatus.value === "running" || liveStatus.value === "queued",
);
// A failed or cancelled job can be re-run as a fresh job with the same parameters.
const canRestart = computed(() => {
  const s = job.value?.status;
  return s === "failed" || s === "cancelled";
});
const restarting = ref(false);
// Live-stacking jobs run until stopped; the "cancel" affordance is really "stop & finalize".
const isLive = computed(() => job.value?.params?.mode === "livestack");

// Improvement series this job belongs to: series_id lives on the job row itself and is echoed in its
// persisted params (the RunRequest) — read both so every row resolves. 0 = not part of a series.
const seriesId = computed(
  () => job.value?.series_id || job.value?.params?.series_id || 0,
);

// Any completed run can be re-finished by the AI supervisor: it re-tunes that mode's finish from the
// masters/intermediates left on disk. Needs a nested `final` (deep-sky/comet/milkyway) or a flat
// `outputs` (planetary).
const canRefine = computed(
  () =>
    job.value?.status === "succeeded" &&
    (!!result.value?.final || !!result.value?.outputs?.length),
);
// Only the deep-sky supervisor has cost tiers (composite / finish / re-stack); the other modes re-finish
// in a single cheap stage, so the tier selector is hidden for them.
const refineHasTiers = computed(() => {
  const mode = job.value?.params?.mode ?? "deepsky";
  return mode === "deepsky" || mode === "nebula" || mode === "livestack";
});
const refining = ref(false);
// Refine re-finishes an existing run from its stacked masters, so it reaches Tier A (composite) or B
// (reprocess the finish) — not Tier C (re-stack). Full autonomy incl. re-stack is the supervise
// checkbox at run time (a fresh run keeps its raw frames wired for Tier C).
const refineTier = ref<"A" | "B">("B");
const refineIters = ref<number | null>(null);
const refineError = ref("");

async function refineJob() {
  refining.value = true;
  refineError.value = "";
  try {
    const newId = await jobsStore.refine(jobId, {
      tier: refineTier.value,
      maxIters: refineIters.value || undefined,
    });
    router.push({ name: "job", params: { id: String(newId) } });
  } catch (e) {
    // Surface the failure (e.g. a run with nothing on disk to refine) instead of silently doing nothing.
    refineError.value = (e as Error).message || t("refine.failed");
    refining.value = false; // stay on the run so the error stays visible
  }
}

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

async function restartJob() {
  restarting.value = true;
  try {
    const newId = await jobsStore.restart(jobId);
    // Same route, different param — ProcessingView keys the router-view by path, so this remounts
    // JobView and re-opens the new job's event stream.
    router.push({ name: "job", params: { id: String(newId) } });
  } catch {
    restarting.value = false; // stay on the failed job so the error remains visible
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
      <button
        v-else-if="canRestart"
        :class="[btnPrimary, 'ml-auto']"
        :disabled="restarting"
        @click="restartJob"
      >
        {{ restarting ? t("job.restarting") : t("job.restart") }}
      </button>
    </div>

    <!-- Environment warnings (missing/broken tools, catalogues): collapsed count chip, expandable. -->
    <EnvWarnings />

    <p
      v-if="job?.error"
      class="rounded-md bg-red-100 p-3 text-sm text-red-800 dark:bg-red-900/40 dark:text-red-300"
    >
      {{ job.error }}
    </p>

    <!-- Live, steerable AI-supervisor conversation for this run: previews + reasoning + steering. Shows
         whenever this session started the run (supervise checkbox or "Améliorer avec l'IA"). -->
    <SupervisorChat v-if="turnId" :turn-id="turnId" />

    <!-- Durable improvement campaign this run belongs to: goal, status, best-vs-target and every
         attempt (each a job) as a horizontal timeline, with Continue/Stop controls. -->
    <SeriesTimeline v-if="seriesId" :series-id="seriesId" />

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

      <!-- Supervised finish: stream the agent's iterations (preview + defects + scores) as they land. -->
      <StagePreviewTimeline :live="stagePreviews" />
      <SupervisorPanel v-if="iterations.length" :live="iterations" />

      <LogConsole :lines="lines" />
    </template>

    <!-- After completion: the full result panels + the supervisor iteration timeline. `outputs` covers
         planetary/comet lucky-imaging runs, which carry no channels/final wrapper. -->
    <template
      v-else-if="
        result &&
        (result.channels?.length || result.final || result.outputs?.length)
      "
    >
      <!-- Ask the local AI agent to look at this result and re-finish it, iterating until it's clean. -->
      <section v-if="canRefine" :class="card" data-demo="refine-panel">
        <div class="flex flex-wrap items-end gap-4">
          <div class="min-w-0 flex-1">
            <h2 class="text-lg font-medium">{{ t("refine.title") }}</h2>
            <p class="text-sm text-slate-500 dark:text-slate-400">
              {{ t("refine.hint") }}
            </p>
          </div>
          <label
            v-if="refineHasTiers"
            class="flex flex-col gap-1 text-xs text-slate-500 dark:text-slate-400"
          >
            {{ t("refine.reach") }}
            <select
              v-model="refineTier"
              class="rounded-md border border-slate-300 bg-white px-2 py-1 text-sm text-slate-800 dark:border-slate-600 dark:bg-slate-800 dark:text-slate-100"
            >
              <option value="A">{{ t("refine.tierA") }}</option>
              <option value="B">{{ t("refine.tierB") }}</option>
            </select>
          </label>
          <label
            class="flex flex-col gap-1 text-xs text-slate-500 dark:text-slate-400"
          >
            {{ t("refine.iters") }}
            <input
              v-model.number="refineIters"
              type="number"
              min="1"
              max="8"
              :placeholder="t('refine.itersAuto')"
              class="w-24 rounded-md border border-slate-300 bg-white px-2 py-1 text-sm text-slate-800 dark:border-slate-600 dark:bg-slate-800 dark:text-slate-100"
            />
          </label>
          <button
            :class="btnPrimary"
            :disabled="refining"
            data-demo="refine-run"
            @click="refineJob"
          >
            {{ refining ? t("refine.starting") : t("refine.run") }}
          </button>
        </div>
        <p
          v-if="refineError"
          class="mt-2 text-sm text-red-600 dark:text-red-400"
          data-demo="refine-error"
        >
          {{ refineError }}
        </p>
      </section>

      <RunResultPanels :result="result" />
      <SupervisorPanel :result="result" :live="iterations" />
    </template>
  </div>
</template>
