<script setup lang="ts">
import { onMounted, onBeforeUnmount, computed } from "vue";
import { useRouter } from "vue-router";
import { useI18n } from "vue-i18n";
import { useJobsStore } from "@/stores/jobs";
import GenericTable, {
  type Column,
} from "@/components/Common/GenericTable.vue";
import StatusPill from "@/components/Common/StatusPill.vue";
import Spinner from "@/components/Common/Spinner.vue";
import { btnGhost } from "@/constants/styles";

const router = useRouter();
const { t } = useI18n();
const jobsStore = useJobsStore();

// Jobs still in the queue or running — drives auto-refresh and the cancel/remove affordance.
const ACTIVE = new Set(["queued", "running"]);
const hasActive = computed(() =>
  jobsStore.jobs.some((j) => ACTIVE.has(j.status)),
);
const queuedCount = computed(
  () => jobsStore.jobs.filter((j) => j.status === "queued").length,
);

// Poll while anything is in flight so the sequential queue visibly auto-advances; idle otherwise.
let pollTimer: ReturnType<typeof setInterval> | null = null;
onMounted(async () => {
  await jobsStore.list();
  pollTimer = setInterval(() => {
    if (hasActive.value) jobsStore.list();
  }, 2500);
});
onBeforeUnmount(() => {
  if (pollTimer) clearInterval(pollTimer);
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
  { key: "actions", label: "", align: "right" },
];

function open(id: unknown) {
  router.push({ name: "job", params: { id: String(id) } });
}

// Cancel a running job (terminates it) or remove a still-queued one from the chain, then refresh.
async function cancel(id: unknown) {
  await jobsStore.cancel(Number(id));
  await jobsStore.list();
}
</script>

<template>
  <div class="space-y-4">
    <div class="flex items-center justify-between">
      <div class="flex items-center gap-3">
        <h1 class="text-2xl font-semibold">{{ t("nav.jobs") }}</h1>
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

    <GenericTable :columns="columns" :rows="rows">
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
      <template #cell-actions="{ row }">
        <button
          v-if="row.status === 'queued' || row.status === 'running'"
          :class="btnGhost"
          class="!px-2 !py-1 !text-xs"
          @click="cancel(row.id)"
        >
          {{ row.status === "running" ? t("job.cancel") : t("job.remove") }}
        </button>
      </template>
    </GenericTable>
  </div>
</template>
