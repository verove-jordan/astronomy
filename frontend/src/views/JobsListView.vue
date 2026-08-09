<script setup lang="ts">
import { onMounted, onBeforeUnmount, computed, ref } from "vue";
import { useRouter } from "vue-router";
import { useI18n } from "vue-i18n";
import { useJobsStore } from "@/stores/jobs";
import { useBrowseStore } from "@/stores/browse";
import GenericTable, {
  type Column,
} from "@/components/Common/GenericTable.vue";
import StatusPill from "@/components/Common/StatusPill.vue";
import Spinner from "@/components/Common/Spinner.vue";
import ParamChips from "@/components/Common/ParamChips.vue";
import HelpButton from "@/components/Common/HelpButton.vue";
import type { JobParams } from "@/types";
import { formatTimestamp, humanizeMs } from "@/utils/format";
import { btnGhost, btnPrimary } from "@/constants/styles";

const router = useRouter();
const { t } = useI18n();
const jobsStore = useJobsStore();
const browseStore = useBrowseStore();

// Jobs still in the queue or running — drives auto-refresh and the cancel/remove affordance.
const ACTIVE = new Set(["queued", "running"]);
const hasActive = computed(() =>
  jobsStore.jobs.some((j) => ACTIVE.has(j.status)),
);
const hasRunning = computed(() =>
  jobsStore.jobs.some((j) => j.status === "running"),
);
const queuedCount = computed(
  () => jobsStore.jobs.filter((j) => j.status === "queued").length,
);

// Ticks once a second (only while a job is running) so the live Duration column advances between polls.
const now = ref(Date.now());

// Poll while anything is in flight so the sequential queue visibly auto-advances; idle otherwise.
let pollTimer: ReturnType<typeof setInterval> | null = null;
let nowTimer: ReturnType<typeof setInterval> | null = null;
onMounted(async () => {
  // loadProcessed gives the per-folder local/S3 truth that gates the "Remove local files" action; it is
  // now fast (batched /api/processed), so it's cheap to fetch alongside the job list.
  await Promise.all([jobsStore.list(), browseStore.loadProcessed()]);
  pollTimer = setInterval(() => {
    if (hasActive.value) jobsStore.list();
  }, 2500);
  nowTimer = setInterval(() => {
    if (hasRunning.value) now.value = Date.now();
  }, 1000);
});
onBeforeUnmount(() => {
  if (pollTimer) clearInterval(pollTimer);
  if (nowTimer) clearInterval(nowTimer);
});

type Row = Record<string, unknown>;
// Active jobs first in submission (id) order — the running one then the queue — and finished jobs after,
// most-recent first. Header clicks still re-sort via GenericTable.
const rows = computed<Row[]>(() =>
  jobsStore.jobs
    .map((j) => ({
      id: j.id,
      kind: j.kind,
      status: j.status,
      progress: j.progress,
      object: j.result?.input_dir?.split("/").pop() || "",
      started: j.started_at_ms, // 0 while queued; numeric so the column sorts
      finished_at_ms: j.finished_at_ms,
      updated_at: j.updated_at,
      params: j.params, // for the Options chips column
      storageMode: j.params?.storage_mode, // gates the "Remove local files" action
      // A running job is pausable if it has a safe mid-run boundary: the deep-sky channel loop, or any
      // S3 copy (full-S3 run / standalone transfer / backup).
      pausable:
        j.status === "running" &&
        j.params?.mode !== "livestack" &&
        (["deepsky", "nebula"].includes(j.params?.mode ?? "") ||
          j.params?.storage_mode === "s3" ||
          j.kind === "transfer" ||
          j.kind === "backup"),
    }))
    .sort((a, b) => {
      const aActive = ACTIVE.has(String(a.status)) ? 0 : 1;
      const bActive = ACTIVE.has(String(b.status)) ? 0 : 1;
      if (aActive !== bActive) return aActive - bActive;
      return aActive === 0
        ? Number(a.id) - Number(b.id)
        : Number(b.id) - Number(a.id);
    }),
);

const columns: Column<Row>[] = [
  { key: "id", label: t("fields.id"), sortable: true, align: "right" },
  { key: "kind", label: t("fields.kind"), sortable: true, searchable: true },
  {
    key: "object",
    label: t("fields.object"),
    sortable: true,
    searchable: true,
  },
  {
    key: "status",
    label: t("fields.status"),
    sortable: true,
    searchable: true,
  },
  {
    key: "progress",
    label: t("fields.progress"),
    sortable: true,
    format: (v) => `${v}%`,
    align: "right",
  },
  { key: "options", label: t("fields.options") },
  { key: "started", label: t("fields.started"), sortable: true },
  { key: "duration", label: t("fields.duration"), align: "right" },
  { key: "actions", label: "", align: "right" },
];

// Started: the moment the job left the queue and began processing (0 while still queued → "—").
function fmtStarted(row: Row): string {
  const ms = Number(row.started) || 0;
  return ms ? formatTimestamp(ms) : "—";
}
// Duration: elapsed processing time — live (to `now`) while running, else finished−started. Queued
// jobs (never started) show "—".
function fmtDuration(row: Row): string {
  const start = Number(row.started) || 0;
  if (!start) return "—";
  const end =
    row.status === "running"
      ? now.value
      : Number(row.finished_at_ms) || Number(row.updated_at) || start;
  return humanizeMs(Math.max(0, end - start));
}

function open(id: unknown) {
  router.push({ name: "job", params: { id: String(id) } });
}

// Cancel a running job (terminates it) or remove a still-queued one from the chain, then refresh.
async function cancel(id: unknown) {
  await jobsStore.cancel(Number(id));
  await jobsStore.list();
}

// Pause a running job at its next safe boundary (deep-sky channel or S3 file). It stays paused until a
// manual Continue (unless it was auto-paused by a transfer error, which the engine retries).
const pausingId = ref<number | null>(null);
async function pause(id: unknown) {
  const jid = Number(id);
  pausingId.value = jid;
  try {
    await jobsStore.pause(jid);
    await jobsStore.list();
  } finally {
    pausingId.value = null;
  }
}

// Restart re-runs a failed/cancelled job as a new job, then jumps to it.
const restartingId = ref<number | null>(null);
async function restart(id: unknown) {
  const jid = Number(id);
  restartingId.value = jid;
  try {
    const newId = await jobsStore.restart(jid);
    router.push({ name: "job", params: { id: String(newId) } });
  } catch {
    restartingId.value = null;
  }
}

// Continue resumes a paused job (same id) and opens it so its resumed progress is visible.
async function continueJob(id: unknown) {
  const jid = Number(id);
  await jobsStore.continueJob(jid);
  router.push({ name: "job", params: { id: String(jid) } });
}

// jobsWithLocalFiles: the set of job ids whose capture folders are still on local disk (any path with
// local === true), from /api/processed. Drives the "Remove local files" action's visibility.
const jobsWithLocalFiles = computed(() => {
  const ids = new Set<number>();
  for (const g of browseStore.processedGroups) {
    if (g.paths.some((p) => p.local)) ids.add(g.job_id);
  }
  return ids;
});
// freedIds optimistically hides the button right after a free is enqueued (the removeLocal transfers then
// run in Tasks); processedGroups catches up on the next load.
const freedIds = ref<Set<number>>(new Set());
function canFreeLocal(row: Row): boolean {
  return (
    row.status === "succeeded" &&
    row.storageMode === "s3" &&
    !freedIds.value.has(Number(row.id)) &&
    jobsWithLocalFiles.value.has(Number(row.id))
  );
}
// freeLocal deletes a finished full-S3 run's local files (safe — each is verified on S3 first, server-side)
// by enqueuing removeLocal transfers, which then show progress in this list.
async function freeLocal(id: unknown) {
  const jid = Number(id);
  if (!window.confirm(t("job.freeLocalConfirm"))) return;
  try {
    await jobsStore.freeLocal(jid);
    freedIds.value = new Set(freedIds.value).add(jid); // optimistic hide
    await jobsStore.list(); // surface the enqueued transfer jobs
  } catch {
    // a failed transfer (e.g. a file not yet on S3) surfaces in this list
  }
}
</script>

<template>
  <div class="space-y-4">
    <div class="flex items-center justify-between">
      <div class="flex items-center gap-3">
        <h1 class="text-2xl font-semibold">{{ t("nav.jobs") }}</h1>
        <HelpButton />
        <span
          v-if="queuedCount"
          class="rounded-full bg-brand-100 px-2 py-0.5 text-xs font-medium text-brand-700 dark:bg-brand-900/40 dark:text-brand-200"
        >
          {{ t("job.queueBadge", { n: queuedCount }) }}
        </span>
      </div>
      <button :class="btnGhost" @click="jobsStore.list()">
        {{ t("common.refresh") }}
      </button>
    </div>

    <Spinner v-if="jobsStore.loading && !jobsStore.jobs.length">{{
      t("common.loading")
    }}</Spinner>

    <GenericTable
      :columns="columns"
      :rows="rows"
      :row-key="(r) => Number(r.id)"
    >
      <template #cell-id="{ row }">
        <button
          class="font-medium text-brand-600 hover:underline dark:text-brand-300"
          @click="open(row.id)"
        >
          #{{ row.id }}
        </button>
      </template>
      <template #cell-status="{ row }">
        <StatusPill :status="String(row.status)" />
      </template>
      <template #cell-started="{ row }">
        <span class="text-xs tabular-nums text-slate-500 dark:text-slate-400">{{
          fmtStarted(row)
        }}</span>
      </template>
      <template #cell-duration="{ row }">
        <span class="text-xs tabular-nums text-slate-500 dark:text-slate-400">{{
          fmtDuration(row)
        }}</span>
      </template>
      <template #cell-options="{ row }">
        <ParamChips :params="row.params as JobParams | undefined" />
      </template>
      <template #cell-actions="{ row }">
        <button
          v-if="row.pausable"
          :class="btnGhost"
          class="!mr-1 !px-2 !py-1 !text-xs"
          :disabled="pausingId === row.id"
          @click="pause(row.id)"
        >
          {{ pausingId === row.id ? t("job.pausing") : t("job.pause") }}
        </button>
        <button
          v-if="row.status === 'queued' || row.status === 'running'"
          :class="btnGhost"
          class="!px-2 !py-1 !text-xs"
          @click="cancel(row.id)"
        >
          {{ row.status === "running" ? t("job.cancel") : t("job.remove") }}
        </button>
        <button
          v-else-if="row.status === 'paused'"
          :class="btnPrimary"
          class="!px-2 !py-1 !text-xs"
          @click="continueJob(row.id)"
        >
          {{ t("job.continue") }}
        </button>
        <button
          v-else-if="row.status === 'failed' || row.status === 'cancelled'"
          :class="btnPrimary"
          class="!px-2 !py-1 !text-xs"
          :disabled="restartingId === row.id"
          @click="restart(row.id)"
        >
          {{ restartingId === row.id ? t("job.restarting") : t("job.restart") }}
        </button>
        <button
          v-if="canFreeLocal(row)"
          :class="btnGhost"
          class="!px-2 !py-1 !text-xs text-danger"
          :title="t('job.freeLocalHint')"
          @click="freeLocal(row.id)"
        >
          {{ t("job.freeLocal") }}
        </button>
      </template>
    </GenericTable>

    <div v-if="jobsStore.jobsHasMore" class="mt-4 text-center">
      <button
        :class="btnGhost"
        :disabled="jobsStore.loadingMore"
        @click="jobsStore.loadMoreJobs()"
      >
        {{ jobsStore.loadingMore ? t("common.loading") : t("runs.loadMore") }}
      </button>
    </div>
  </div>
</template>
