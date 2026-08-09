<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from "vue";
import { useRouter } from "vue-router";
import { useI18n } from "vue-i18n";
import { useLogbookStore } from "@/stores/logbook";
import GenericTable, {
  type Column,
} from "@/components/Common/GenericTable.vue";
import FilterChip from "@/components/Common/FilterChip.vue";
import Spinner from "@/components/Common/Spinner.vue";
import HelpButton from "@/components/Common/HelpButton.vue";
import { btnGhost, input, statusPill } from "@/constants/styles";
import { humanizeMs } from "@/utils/format";
import { verdictColor } from "@/utils/weather";
import {
  hasConditions,
  moonPhaseKey,
  nightKey,
  perFilterCounts,
  sessionDurationMs,
  skyScore,
} from "@/utils/logbook";
import type { CaptureSessionRow } from "@/types";

// The observing logbook: every capture session, past and present.
//
// The list answers "what did I shoot, when, through what, and under what sky?" — one row per run,
// with the sky condensed to a single number so a good night is visible without opening anything.
const { t } = useI18n();
const router = useRouter();
const store = useLogbookStore();

// A running session's counters keep moving, so the list refreshes while one is live. The interval is
// slow on purpose: the capture page already streams the live run, and this is the history view.
const REFRESH_MS = 15_000;
let timer = 0;

const search = ref("");
let searchTimer = 0;

onMounted(() => {
  void store.listSessions();
  timer = window.setInterval(() => {
    if (rows.value.some((r) => isActive(r))) void store.listSessions(true);
  }, REFRESH_MS);
});

onBeforeUnmount(() => {
  window.clearInterval(timer);
  window.clearTimeout(searchTimer);
});

watch(search, (v) => {
  window.clearTimeout(searchTimer);
  searchTimer = window.setTimeout(
    () => void store.setQuery({ object: v }),
    300,
  );
});

const ACTIVE = new Set(["running", "paused"]);
const isActive = (s: CaptureSessionRow) => ACTIVE.has(s.status);

// The index signature is what GenericTable's `T extends Record<string, unknown>` needs; the named
// fields keep the template type-checked rather than falling back to `unknown`.
interface Row extends Record<string, unknown> {
  id: number;
  night: string;
  object: string;
  panel: string;
  status: string;
  frames: string;
  framesDone: number;
  integration: string;
  filters: [string, number][];
  duration: string;
  sky: number | null;
  moon: string;
  active: boolean;
}

const rows = computed<CaptureSessionRow[]>(() => store.sessions);

const tableRows = computed<Row[]>(() =>
  store.sessions.map((s) => {
    const summary = hasConditions(s.conditions_summary)
      ? s.conditions_summary
      : null;
    return {
      id: s.id,
      night: nightKey(s.started_at),
      object: s.object || t("logbook.untitled"),
      panel: s.panel,
      status: s.status,
      frames: `${s.frames_done} / ${s.total_frames}`,
      framesDone: s.frames_done,
      // The list has no frame rows to aggregate, so it uses the sequencer's own per-filter counters —
      // the same source the capture page shows live.
      filters: perFilterCounts([], s.progress?.captured),
      integration: integrationOf(s),
      duration: humanizeMs(sessionDurationMs(s)),
      sky: skyScore(summary),
      moon: summary
        ? t(`tonight.moonPhase.${moonPhaseKey(summary.moon_phase_angle_deg)}`)
        : "",
      active: isActive(s),
    };
  }),
);

// The planned integration: what the sequence asks for, times how far it got. The exact figure needs
// the frame rows, which only the detail view fetches.
function integrationOf(s: CaptureSessionRow): string {
  const steps = s.sequence?.steps ?? [];
  if (!steps.length || !s.total_frames) return "—";
  const plannedMs = steps.reduce(
    (sum, st) => sum + (st.exposure_us / 1000) * st.count,
    0,
  );
  return humanizeMs((plannedMs * s.frames_done) / s.total_frames);
}

const columns = computed<Column<Row>[]>(() => [
  {
    key: "night",
    label: t("logbook.cols.night"),
    sortable: true,
    searchable: true,
  },
  {
    key: "object",
    label: t("logbook.cols.object"),
    sortable: true,
    searchable: true,
  },
  { key: "status", label: t("logbook.cols.status"), sortable: true },
  { key: "frames", label: t("logbook.cols.frames"), align: "right" },
  { key: "filters", label: t("logbook.cols.filters") },
  { key: "integration", label: t("logbook.cols.integration"), align: "right" },
  { key: "duration", label: t("logbook.cols.duration"), align: "right" },
  { key: "sky", label: t("logbook.cols.sky"), sortable: true, align: "right" },
  { key: "moon", label: t("logbook.cols.moon"), sortable: true },
]);

// A four-dot glyph next to the number: readable at a glance, and still meaningful in greyscale.
const skyDots = (score: number) =>
  Math.max(1, Math.min(4, Math.ceil(score / 25)));

function open(row: Row) {
  void router.push({ name: "logbookSession", params: { id: row.id } });
}
</script>

<template>
  <div class="space-y-6">
    <div class="flex flex-wrap items-end justify-between gap-3">
      <div>
        <div class="flex items-center gap-2">
          <h1 class="text-2xl font-semibold">{{ t("logbook.title") }}</h1>
          <HelpButton />
        </div>
        <p class="text-sm text-slate-500 dark:text-slate-400">
          {{ t("logbook.subtitle") }}
        </p>
      </div>
      <div class="flex items-center gap-2" data-demo="logbook-filters">
        <input
          v-model="search"
          :class="[input, 'w-48']"
          type="search"
          :placeholder="t('logbook.searchObject')"
        />
        <button :class="btnGhost" @click="store.listSessions(true)">
          {{ t("common.refresh") }}
        </button>
      </div>
    </div>

    <p v-if="store.error" class="text-sm text-danger-500">{{ store.error }}</p>

    <Spinner v-if="store.loading && !store.sessions.length">
      {{ t("common.loading") }}
    </Spinner>

    <p v-else-if="!store.sessions.length" class="text-sm text-slate-400">
      {{ t("logbook.empty") }}
    </p>

    <template v-else>
      <GenericTable
        :columns="columns"
        :rows="tableRows"
        :row-key="(r) => r.id"
        :row-class="
          (r) => (r.active ? 'bg-brand-500/5 dark:bg-brand-500/10' : '')
        "
        @row-click="open"
      >
        <template #cell-status="{ row }">
          <span
            class="inline-block rounded px-1.5 py-0.5 text-xs"
            :class="statusPill[row.status] ?? statusPill.cancelled"
          >
            {{ t(`capture.status.${row.status}`) }}
          </span>
        </template>

        <template #cell-filters="{ row }">
          <span class="flex flex-wrap gap-1">
            <FilterChip v-for="[f, n] in row.filters" :key="f" :filter="f">
              <span class="ml-1 font-mono tabular-nums">{{ n }}</span>
            </FilterChip>
            <span v-if="!row.filters.length" class="text-slate-400">—</span>
          </span>
        </template>

        <!-- A session captured before the logbook shipped has no sky record and cannot get one:
             the weather feeds have no archive. Saying "not recorded" is the honest cell. -->
        <template #cell-sky="{ row }">
          <span v-if="row.sky === null" class="text-xs text-slate-400">
            {{ t("logbook.notRecorded") }}
          </span>
          <span v-else class="inline-flex items-center gap-1.5">
            <span class="font-mono tabular-nums">{{
              Math.round(row.sky)
            }}</span>
            <span class="flex gap-0.5">
              <span
                v-for="i in 4"
                :key="i"
                class="h-1.5 w-1.5 rounded-full"
                :class="
                  i <= skyDots(row.sky)
                    ? ''
                    : 'ring-1 ring-inset ring-slate-300 dark:ring-slate-600'
                "
                :style="
                  i <= skyDots(row.sky)
                    ? { backgroundColor: verdictColor(row.sky) }
                    : undefined
                "
              />
            </span>
          </span>
        </template>
      </GenericTable>

      <p class="text-center text-xs text-slate-400">
        {{
          t("logbook.count", { n: store.sessions.length, total: store.total })
        }}
      </p>
    </template>
  </div>
</template>
