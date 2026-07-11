<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from "vue";
import { useRouter } from "vue-router";
import { useI18n } from "vue-i18n";
import { useJobsStore } from "@/stores/jobs";
import { useBrowseStore } from "@/stores/browse";
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
import CalibrationPanel from "@/components/Capture/CalibrationPanel.vue";
import ChannelMappingList from "@/components/Capture/ChannelMappingList.vue";
import RunResultPanels from "@/components/Common/RunResultPanels.vue";
import SupervisorPanel from "@/components/Common/SupervisorPanel.vue";
import SupervisorChat from "@/components/Common/SupervisorChat.vue";
import StagePreviewTimeline from "@/components/Common/StagePreviewTimeline.vue";
import StageParamEditor from "@/components/Common/StageParamEditor.vue";
import SeriesTimeline from "@/components/Common/SeriesTimeline.vue";
import EnvWarnings from "@/components/Common/EnvWarnings.vue";
import ParamChips from "@/components/Common/ParamChips.vue";
import TwoPane from "@/components/Common/TwoPane.vue";
import StatGrid from "@/components/Common/StatGrid.vue";
import { btnDanger, btnGhost, btnPrimary, card } from "@/constants/styles";
import { baseName, formatBytes } from "@/utils/format";
import type { Inventory } from "@/types";

const props = defineProps<{ id: string }>();
const { t } = useI18n();
const router = useRouter();
const jobsStore = useJobsStore();
const browseStore = useBrowseStore();
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
  bytesDone,
  bytesTotal,
  bytesPerSec,
  iterations,
  stagePreviews,
  seed,
  reconnect,
} = useJobStream(jobId, () => jobsStore.get(jobId), false); // connect only for live jobs (below)

const reInv = ref<Inventory | null>(null);
const cancelling = ref(false);

// An S3 transfer job reports byte progress + throughput instead of a subprocess's CPU/RAM.
const isTransfer = computed(() => bytesTotal.value > 0);

// Live readouts for the running job, packed into a compact StatGrid: transferred/throughput for an S3
// copy, else the running subprocess's CPU/RAM.
const progressStats = computed(() => {
  if (isTransfer.value) {
    return [
      {
        label: t("job.transferred"),
        value: `${formatBytes(bytesDone.value)} / ${formatBytes(bytesTotal.value)}`,
      },
      {
        label: t("job.throughput"),
        value: `${formatBytes(bytesPerSec.value)}/s`,
      },
    ];
  }
  const s = [
    { label: t("job.cpu"), value: `${Math.round(cpuPercent.value)}%` },
    { label: t("job.memory"), value: formatBytes(rssBytes.value) },
  ];
  if (peakRssBytes.value)
    s.push({ label: t("job.peak"), value: formatBytes(peakRssBytes.value) });
  return s;
});

onMounted(async () => {
  // A supervised/refine run started this session has a live conversation turn — open it so its previews,
  // reasoning and steering show inline (and it lands in the AstroAgent history). Fire-and-forget; the
  // stream ends itself when the run finishes.
  if (turnId.value) {
    void agent.watchTurn(turnId.value, {
      title: t("supervisorChat.convTitle", { id: jobId }),
    });
  }
  void browseStore.loadProcessed(); // per-folder local/S3 truth for the "Remove local files" action
  await jobsStore.get(jobId);
  const jb = jobsStore.current;
  if (jb?.log_tail) seed(jb.log_tail.split("\n"));
  // Open the SSE stream only for a job that can still emit events. A finished job's state is fully
  // in the fetched row — opening a stream for it just spends a connection (and, on a loaded engine,
  // makes the page look slow while that pointless request waits its turn).
  if (!isTerminal(jb?.status)) reconnect();
  // If the create-time inventory was lost (a restarted job, or a hard reload) and the job is still
  // running, re-inspect. Pass the FULL folder selection (params.paths ?? [params.path]) so a
  // multi-folder run whose primary path is a calibration folder still finds the light frames.
  const stillRunning =
    jb && (jb.status === "running" || jb.status === "queued");
  const folders = jb?.params?.paths?.length
    ? jb.params.paths
    : jb?.params?.path
      ? [jb.params.path]
      : [];
  if (stillRunning && !jobsStore.captureFor(jobId) && folders.length) {
    reInv.value = await jobsStore.inspectCapture(folders);
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

// A paused job (manual pause, or auto-paused on a transient S3 error) can be continued from where it
// left off. Not terminal — it shows Continue + Cancel.
const isPaused = computed(() => liveStatus.value === "paused");
// Manual mid-run pause is honored by the multi-channel deep-sky path (deepsky/nebula) AND by any S3
// copy — a full-S3 run or a standalone transfer/backup pauses between files. Other local modes have no
// safe mid-run boundary, so we don't offer a Pause that would look like a no-op.
const PAUSABLE_MODES = ["deepsky", "nebula"];
const isS3Copy = computed(
  () =>
    job.value?.params?.storage_mode === "s3" ||
    job.value?.kind === "transfer" ||
    job.value?.kind === "backup",
);
const canPause = computed(
  () =>
    running.value &&
    !isLive.value &&
    (PAUSABLE_MODES.includes(job.value?.params?.mode ?? "") || isS3Copy.value),
);
const pausing = ref(false);
const continuing = ref(false);

// A short line under the paused notice: a manual pause waits for the user; an error pause auto-resumes.
const pauseCauseText = computed(() => {
  const r = job.value?.resume;
  if (!isPaused.value || !r?.cause) return "";
  if (r.cause === "manual") return t("job.pausedManual");
  return t("job.pausedError", { n: r.attempts ?? 1, max: 8 });
});

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
// On-demand GraXpert denoise of the final image — offered on any succeeded run that produced a final.
const canDenoiseFinal = computed(
  () => job.value?.status === "succeeded" && !!result.value?.final,
);
// "Remove local files": offered on a succeeded full-S3 run whose capture folders are still on local disk
// (from /api/processed). Freeing is safe — each file is verified on S3 server-side before deletion.
const jobFilesLocal = computed(() => {
  const g = browseStore.processedGroups.find((x) => x.job_id === jobId);
  return !!g && g.paths.some((p) => p.local);
});
const freedLocal = ref(false);
const canFreeLocal = computed(
  () =>
    job.value?.status === "succeeded" &&
    job.value?.params?.storage_mode === "s3" &&
    !freedLocal.value &&
    jobFilesLocal.value,
);
async function freeLocalAction() {
  if (!window.confirm(t("job.freeLocalConfirm"))) return;
  try {
    await jobsStore.freeLocal(jobId);
    freedLocal.value = true; // optimistic hide; the removeLocal transfers run in Tasks
  } catch {
    // a failed transfer surfaces in the Tasks list
  }
}
const denoising = ref(false);
const denoiseError = ref("");
async function denoiseFinalJob() {
  denoising.value = true;
  denoiseError.value = "";
  try {
    const newId = await jobsStore.denoiseFinal(jobId);
    router.push({ name: "job", params: { id: String(newId) } });
  } catch (e) {
    denoiseError.value = (e as Error).message;
  } finally {
    denoising.value = false;
  }
}
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

// A completed deepsky/nebula run supports editable per-stage rerun: tweak a knob at a stage and re-run
// from there in place (the manual, non-supervised counterpart of Refine — same tier model). The
// StagePreviewTimeline shows an "edit & re-run" affordance per card; clicking opens StageParamEditor.
const canRerun = computed(
  () =>
    job.value?.status === "succeeded" &&
    !!result.value?.final &&
    refineHasTiers.value,
);
const rerunStage = ref<string | null>(null);
const rerunning = ref(false);
const rerunError = ref("");
function openStageEditor(stage: string) {
  rerunError.value = "";
  rerunStage.value = stage;
}
function closeStageEditor() {
  if (rerunning.value) return; // don't close mid-submit
  rerunStage.value = null;
}
async function submitRerun(payload: {
  stage: string;
  params: Record<string, unknown>;
}) {
  rerunning.value = true;
  rerunError.value = "";
  try {
    const newId = await jobsStore.rerun(jobId, {
      stage: payload.stage,
      params: payload.params,
    });
    rerunStage.value = null;
    // A rerun mints a new job (same run dir); follow it to watch progress + the refreshed previews.
    router.push({ name: "job", params: { id: String(newId) } });
  } catch (e) {
    rerunError.value = (e as Error).message || t("rerun.failed");
  } finally {
    rerunning.value = false;
  }
}

// Processing timer: ticks each second while running, then freezes at the total once the job finishes.
const now = ref(Date.now());
let timer: ReturnType<typeof setInterval> | null = null;
function startTicker() {
  if (timer) return;
  timer = setInterval(() => (now.value = Date.now()), 1000);
}
function stopTicker() {
  if (timer) {
    clearInterval(timer);
    timer = null;
  }
}
// Only tick while the job is live; once it settles, elapsed freezes from updated_at, so a running
// interval would just re-render every second for nothing. Restart if a paused job resumes.
onMounted(() => {
  if (running.value) startTicker();
});
watch(running, (r) => {
  if (r) {
    startTicker();
  } else {
    now.value = Date.now();
    stopTicker();
  }
});
onBeforeUnmount(stopTicker);
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

// Estimated time remaining for an S3 transfer, from the already-smoothed throughput and bytes left.
// 0 (hidden) for non-transfer jobs, a stalled rate, or once the copy is done.
const etaMs = computed(() => {
  if (!isTransfer.value || bytesPerSec.value <= 0) return 0;
  const remain = bytesTotal.value - bytesDone.value;
  if (remain <= 0) return 0;
  return (remain / bytesPerSec.value) * 1000;
});

// The S3 mirror destination for a transfer job (`s3://<bucket>/<prefix>/<namespace>/<rel_path>`), rebuilt
// from job params like the backend's baseKey(). Empty for non-transfer jobs. JS has no path.Join, so trim
// slashes and drop empty segments (external-drive copies leave namespace empty).
const destPath = computed(() => {
  const tr = job.value?.params?.transfer;
  if (!tr?.bucket) return "";
  const key = [tr.prefix, tr.namespace, tr.rel_path]
    .map((s) => (s ?? "").replace(/^\/+|\/+$/g, ""))
    .filter(Boolean)
    .join("/");
  return `s3://${tr.bucket}${key ? "/" + key : ""}`;
});

function truncate(s: string, n: number): string {
  return s.length > n ? s.slice(0, n - 1) + "…" : s;
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

async function pauseJobAction() {
  pausing.value = true;
  try {
    await jobsStore.pause(jobId);
  } finally {
    pausing.value = false;
  }
}

async function continueJobAction() {
  continuing.value = true;
  try {
    await jobsStore.continueJob(jobId);
    await jobsStore.get(jobId); // reflect running again
    reconnect(); // the stream closed on pause — re-open it to follow the resumed run
  } finally {
    continuing.value = false;
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
      <ParamChips :params="job?.params" show-goal />
      <span
        v-if="job?.params?.goal"
        class="text-xs italic text-slate-400"
        :title="job.params.goal"
      >
        {{ t("run.chips.goal", { goal: truncate(job.params.goal, 60) }) }}
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
      <div v-if="running" class="ml-auto flex gap-2">
        <button
          v-if="canPause"
          :class="btnGhost"
          :disabled="pausing"
          @click="pauseJobAction"
        >
          {{ pausing ? t("job.pausing") : t("job.pause") }}
        </button>
        <button :class="btnDanger" :disabled="cancelling" @click="cancelJob">
          {{ isLive ? t("job.stopFinalize") : t("job.cancel") }}
        </button>
      </div>
      <div v-else-if="isPaused" class="ml-auto flex gap-2">
        <button
          :class="btnPrimary"
          :disabled="continuing"
          @click="continueJobAction"
        >
          {{ continuing ? t("job.continuing") : t("job.continue") }}
        </button>
        <button :class="btnDanger" :disabled="cancelling" @click="cancelJob">
          {{ t("job.cancel") }}
        </button>
      </div>
      <button
        v-else-if="canRestart"
        :class="[btnPrimary, 'ml-auto']"
        :disabled="restarting"
        @click="restartJob"
      >
        {{ restarting ? t("job.restarting") : t("job.restart") }}
      </button>
      <button
        v-if="canFreeLocal"
        :class="[btnGhost, 'ml-auto text-danger']"
        :title="t('job.freeLocalHint')"
        @click="freeLocalAction"
      >
        {{ t("job.freeLocal") }}
      </button>
    </div>

    <!-- Environment warnings (missing/broken tools, catalogues): collapsed count chip, expandable. -->
    <EnvWarnings />

    <!-- A paused job stores its (benign) pause reason in `error`; render it as a neutral/amber notice,
         not the red failure banner, and say whether it will auto-resume or waits for the user. -->
    <p
      v-if="isPaused && job?.error"
      class="rounded-md bg-amber-100 p-3 text-sm text-amber-800 dark:bg-amber-900/40 dark:text-amber-300"
    >
      {{ job.error }}
      <span v-if="pauseCauseText" class="mt-1 block text-xs opacity-80">{{
        pauseCauseText
      }}</span>
    </p>
    <p
      v-else-if="job?.error && !running"
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

    <!-- While processing: a compact context rail (progress, capture summary, channel mapping) beside a
         wide live-feed column (preview + tailing log). The log fills to the viewport bottom in its own
         column, so it stays tall while the page barely scrolls. Stacks context-first on mobile. -->
    <template v-if="running">
      <TwoPane split="even">
        <template #main>
          <div class="space-y-4">
            <div :class="card">
              <div class="mb-2 flex items-center justify-between gap-3 text-sm">
                <span class="min-w-0 truncate">{{
                  step || t("common.loading")
                }}</span>
                <span
                  class="flex shrink-0 items-center gap-3 text-slate-500 dark:text-slate-400"
                >
                  <span class="tabular-nums">{{ fmtElapsed(elapsedMs) }}</span>
                  <span
                    v-if="etaMs"
                    class="tabular-nums"
                    :title="t('job.remaining')"
                    >~{{ fmtElapsed(etaMs) }}</span
                  >
                  <span class="font-medium text-slate-700 dark:text-slate-200"
                    >{{ progress }}%</span
                  >
                </span>
              </div>
              <ProgressBar :percent="progress" :active="running" />
              <StatGrid
                v-if="rssBytes || cpuPercent || isTransfer"
                class="mt-3"
                :cols="3"
                :items="progressStats"
              />
              <p
                v-if="destPath"
                class="mt-3 truncate font-mono text-xs text-slate-500 dark:text-slate-400"
                :title="destPath"
              >
                {{ t("job.destination") }}: {{ destPath }}
              </p>
            </div>
            <CaptureSummary v-if="summary" :summary="summary" />
            <CalibrationPanel
              v-if="job?.params?.calib_plan"
              :preview="job.params.calib_plan"
              :excluded="job?.params?.calib_exclude ?? []"
              readonly
            />
            <ChannelMappingList v-if="detection" :detection="detection" />
            <!-- Live preview sits directly under the progression + information cards. -->
            <section v-if="previewUrl" :class="card">
              <h2 class="mb-3 text-lg font-medium">
                {{ t("job.livePreview") }}
              </h2>
              <img
                :src="previewUrl"
                alt="live preview"
                class="block max-h-[28rem] w-full max-w-full rounded-md border border-slate-200 object-contain dark:border-slate-700"
              />
            </section>
          </div>
        </template>
        <template #aside>
          <div class="space-y-4">
            <LogConsole :lines="lines" />
          </div>
        </template>
      </TwoPane>

      <!-- Supervised finish: stream the agent's iterations (preview + defects + scores) as they land. -->
      <StagePreviewTimeline :live="stagePreviews" />
      <SupervisorPanel v-if="iterations.length" :live="iterations" />
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

      <!-- On-demand GraXpert AI denoise of the final image. Runs on the native host GraXpert service when
           ASTRO_GRAXPERT_URL is set (faster + non-blocking); else the in-container CPU GraXpert. -->
      <section v-if="canDenoiseFinal" :class="card">
        <div class="flex flex-wrap items-end gap-4">
          <div class="min-w-0 flex-1">
            <h2 class="text-lg font-medium">{{ t("denoiseFinal.title") }}</h2>
            <p class="text-sm text-slate-500 dark:text-slate-400">
              {{ t("denoiseFinal.hint") }}
            </p>
          </div>
          <button
            :class="btnPrimary"
            :disabled="denoising"
            @click="denoiseFinalJob"
          >
            {{ denoising ? t("denoiseFinal.starting") : t("denoiseFinal.run") }}
          </button>
        </div>
        <p
          v-if="denoiseError"
          class="mt-2 text-sm text-red-600 dark:text-red-400"
        >
          {{ denoiseError }}
        </p>
      </section>

      <RunResultPanels
        :result="result"
        :rerunnable="canRerun"
        @rerun-stage="openStageEditor"
      />
      <SupervisorPanel :result="result" :live="iterations" />
      <StageParamEditor
        v-if="rerunStage"
        :key="rerunStage"
        :stage="rerunStage"
        :params="job?.params"
        :busy="rerunning"
        :error="rerunError"
        @submit="submitRerun"
        @close="closeStageEditor"
      />
    </template>
  </div>
</template>
