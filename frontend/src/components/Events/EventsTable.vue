<script setup lang="ts">
import { computed } from "vue";
import { useI18n } from "vue-i18n";
import GenericTable, {
  type Column,
} from "@/components/Common/GenericTable.vue";
import Pill from "@/components/Common/Pill.vue";
import ScoreBadge from "@/components/Common/ScoreBadge.vue";
import EventIcon from "@/components/Events/EventIcon.vue";
import { scoreTier, scoreTierBar } from "@/constants/styles";
import { fmtDateTime } from "@/utils/tz";
import { eventTitle, kindLabel, kindPillClass } from "@/utils/events";
import type { SkyEvent } from "@/types";

// The shared sky-event table (score / date / event / type / naked-eye·scope / mag), used for both the
// date-window list and the "next N by type" series. Sort + per-column search come from GenericTable.
const props = withDefaults(
  defineProps<{
    events: SkyEvent[];
    tz: string;
    selectedId: string | null;
    maxHeight?: string;
    sort?: "score" | "date"; // default row order
  }>(),
  { maxHeight: "32rem", sort: "score" },
);
const emit = defineEmits<{ select: [id: string] }>();
const { t } = useI18n();

type Row = Record<string, unknown>;
const rows = computed<Row[]>(() =>
  [...props.events]
    .sort((a, b) =>
      props.sort === "date" ? a.peak_utc_ms - b.peak_utc_ms : b.score - a.score,
    )
    .map((e) => ({
      id: e.id,
      kind: e.kind,
      kindLabel: kindLabel(e, t),
      title: eventTitle(e, t),
      date_ms: e.peak_utc_ms,
      score: e.score,
      naked: e.visibility.naked_eye,
      scope: e.visibility.telescope,
      mag: e.has_mag ? e.magnitude : null,
    })),
);

const magFmt = (v: unknown): string => {
  const n = Number(v);
  return v != null && Number.isFinite(n) ? n.toFixed(1) : "—";
};

const columns: Column<Row>[] = [
  { key: "score", label: t("calendar.cols.score"), sortable: true },
  {
    key: "date_ms",
    label: t("calendar.cols.date"),
    sortable: true,
    format: (v) => fmtDateTime(Number(v), props.tz),
  },
  { key: "title", label: t("calendar.cols.event"), sortable: true, searchable: true },
  { key: "kindLabel", label: t("calendar.cols.kind"), sortable: true, searchable: true },
  { key: "naked", label: t("calendar.cols.vis"), sortable: true },
  {
    key: "mag",
    label: t("calendar.cols.mag"),
    sortable: true,
    align: "right",
    format: magFmt,
  },
];

function rowClass(row: Row): string {
  return row.id === props.selectedId
    ? "cursor-pointer bg-brand-50 dark:bg-brand-900/30"
    : "cursor-pointer hover:bg-slate-50 dark:hover:bg-slate-800/50";
}
</script>

<template>
  <GenericTable
    :columns="columns"
    :rows="rows"
    :row-class="rowClass"
    :max-height="maxHeight"
  >
    <template #cell-score="{ row }">
      <ScoreBadge :score="Number(row.score)" />
    </template>
    <template #cell-title="{ row }">
      <button
        class="text-left font-medium text-brand-600 hover:underline dark:text-brand-300"
        :aria-pressed="row.id === selectedId"
        @click="emit('select', String(row.id))"
      >
        {{ row.title }}
      </button>
    </template>
    <template #cell-kind="{ row }">
      <Pill :color-class="kindPillClass(String(row.kind))">
        <EventIcon :kind="String(row.kind)" class="h-3 w-3" />
        {{ row.kindLabel }}
      </Pill>
    </template>
    <template #cell-naked="{ row }">
      <div class="flex items-center gap-2 text-[10px] text-slate-400">
        <span class="flex items-center gap-1" title="naked eye">
          👁
          <span class="h-1.5 w-8 overflow-hidden rounded-full bg-slate-200 dark:bg-slate-700">
            <span
              class="block h-full rounded-full"
              :class="scoreTierBar[scoreTier(Number(row.naked))]"
              :style="{ width: Number(row.naked) + '%' }"
            />
          </span>
        </span>
        <span class="flex items-center gap-1" title="your scope">
          🔭
          <span class="h-1.5 w-8 overflow-hidden rounded-full bg-slate-200 dark:bg-slate-700">
            <span
              class="block h-full rounded-full"
              :class="scoreTierBar[scoreTier(Number(row.scope))]"
              :style="{ width: Number(row.scope) + '%' }"
            />
          </span>
        </span>
      </div>
    </template>
  </GenericTable>
</template>
