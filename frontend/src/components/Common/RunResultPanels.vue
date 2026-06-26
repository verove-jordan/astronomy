<script setup lang="ts">
import { computed } from "vue";
import { useI18n } from "vue-i18n";
import { fileUrl } from "@/services/api";
import GenericTable, {
  type Column,
} from "@/components/Common/GenericTable.vue";
import MetricsChart from "@/components/Dataviz/MetricsChart.vue";
import ImageViewer from "@/components/Common/ImageViewer.vue";
import FilterChip from "@/components/Common/FilterChip.vue";
import IconCheck from "@/components/Icons/IconCheck.vue";
import IconMinus from "@/components/Icons/IconMinus.vue";
import IconDownload from "@/components/Icons/IconDownload.vue";
import { card } from "@/constants/styles";
import { humanizeMs, baseName, tempC } from "@/utils/format";
import type { ChannelResult, RunResult } from "@/types";

const props = defineProps<{ result: RunResult }>();
const { t } = useI18n();

type Row = Record<string, unknown>;
const ms = (v: unknown) => humanizeMs(Number(v));

const finalImage = computed(() => {
  const out = props.result.final?.outputs?.find((o) => o.endsWith(".png"));
  return out ? fileUrl(out) : "";
});
const finalVideo = computed(() => {
  const out = props.result.final?.outputs?.find((o) => o.endsWith(".mp4"));
  return out ? fileUrl(out) : "";
});

const channelRows = computed<Row[]>(() =>
  (props.result.channels ?? []).map((c) => ({
    filter: c.filter,
    exposure_ms: c.exposure_ms,
    input: c.input_frames,
    stacked: c.stacked_frames,
    dark: c.selection?.dark ? "✓" : "—",
    flat: c.selection?.flat ? "✓" : "—",
    bias: c.selection?.bias ? "✓" : "—",
  })),
);
const channelColumns: Column<Row>[] = [
  {
    key: "filter",
    label: t("fields.filter"),
    sortable: true,
    searchable: true,
  },
  {
    key: "exposure_ms",
    label: t("fields.exposure"),
    sortable: true,
    format: ms,
  },
  { key: "input", label: t("fields.input"), sortable: true, align: "right" },
  {
    key: "stacked",
    label: t("fields.stacked"),
    sortable: true,
    align: "right",
  },
  { key: "dark", label: "Dark", align: "right" },
  { key: "flat", label: "Flat", align: "right" },
  { key: "bias", label: "Bias", align: "right" },
];

const masterRows = computed<Row[]>(() =>
  (props.result.masters ?? []).map((m) => ({
    type: m.type,
    filter: m.filter || "",
    exposure_ms: m.exposure_ms,
    gain: m.gain,
    offset: m.offset,
    temp_milli_c: m.temp_milli_c,
    frame_count: m.frame_count,
    file: baseName(m.path),
  })),
);
const masterColumns: Column<Row>[] = [
  { key: "type", label: t("fields.type"), sortable: true, searchable: true },
  {
    key: "filter",
    label: t("fields.filter"),
    sortable: true,
    searchable: true,
  },
  {
    key: "exposure_ms",
    label: t("fields.exposure"),
    sortable: true,
    format: ms,
  },
  { key: "gain", label: t("fields.gain"), sortable: true, align: "right" },
  { key: "offset", label: t("fields.offset"), sortable: true, align: "right" },
  {
    key: "temp_milli_c",
    label: t("fields.temp"),
    format: (v) => tempC(Number(v)),
    align: "right",
  },
  {
    key: "frame_count",
    label: t("fields.frames"),
    sortable: true,
    align: "right",
  },
  { key: "file", label: t("fields.file"), searchable: true },
];

const channelsWithMetrics = computed<ChannelResult[]>(() =>
  (props.result.channels ?? []).filter((c) => (c.metrics?.length ?? 0) > 0),
);
function metricRows(c: ChannelResult): Row[] {
  return (c.metrics ?? []).map((m) => ({
    index: m.index,
    file: baseName(m.path),
    fwhm: m.fwhm,
    roundness: m.roundness,
    stars: m.star_count,
    status: m.rejected ? "rejected" : "kept",
    reason: m.reject_reason || "",
  }));
}
const metricColumns: Column<Row>[] = [
  { key: "index", label: t("fields.index"), sortable: true, align: "right" },
  { key: "file", label: t("fields.file"), sortable: true, searchable: true },
  {
    key: "fwhm",
    label: t("fields.fwhm"),
    sortable: true,
    format: (v) => Number(v).toFixed(2),
    align: "right",
  },
  {
    key: "roundness",
    label: t("fields.roundness"),
    sortable: true,
    format: (v) => Number(v).toFixed(3),
    align: "right",
  },
  { key: "stars", label: t("fields.stars"), sortable: true, align: "right" },
  {
    key: "status",
    label: t("fields.status"),
    sortable: true,
    searchable: true,
  },
  { key: "reason", label: t("fields.reason"), searchable: true },
];
const rejectedClass = (r: Row) =>
  r.status === "rejected"
    ? "bg-red-50 dark:bg-red-900/20"
    : "hover:bg-slate-50 dark:hover:bg-slate-800/50";
</script>

<template>
  <div class="space-y-6">
    <section v-if="finalImage" :class="card">
      <h2 class="mb-3 text-lg font-medium">
        {{ t("job.finalImage") }}
        <span class="ml-2 text-sm font-normal text-slate-500">
          {{ result.final?.mode }} · {{ result.final?.channels?.join("+") }}
        </span>
      </h2>
      <ImageViewer :src="finalImage" alt="final stack" />
      <video
        v-if="finalVideo"
        :src="finalVideo"
        controls
        class="mt-3 w-full max-w-full rounded-md border border-slate-200 dark:border-slate-700"
      />
      <div class="mt-2 flex flex-wrap gap-3 text-sm">
        <a
          v-for="o in result.final?.outputs"
          :key="o"
          :href="fileUrl(o)"
          target="_blank"
          class="inline-flex items-center gap-1 text-brand-600 hover:underline dark:text-brand-300"
        >
          <IconDownload /> {{ baseName(o) }}
        </a>
      </div>
      <ul v-if="result.final?.notes?.length" class="mt-2 space-y-1">
        <li
          v-for="(n, i) in result.final?.notes"
          :key="i"
          class="text-xs text-slate-500 dark:text-slate-400"
        >
          · {{ n }}
        </li>
      </ul>
    </section>

    <section>
      <h2 class="mb-2 text-lg font-medium">{{ t("job.channelsTitle") }}</h2>
      <GenericTable :columns="channelColumns" :rows="channelRows">
        <template #cell-filter="{ value }">
          <FilterChip :filter="String(value)" />
        </template>
        <template #cell-dark="{ value }">
          <IconCheck
            v-if="value === '✓'"
            class="ml-auto text-success-600 dark:text-success-500"
          />
          <IconMinus
            v-else
            class="ml-auto text-slate-300 dark:text-slate-600"
          />
        </template>
        <template #cell-flat="{ value }">
          <IconCheck
            v-if="value === '✓'"
            class="ml-auto text-success-600 dark:text-success-500"
          />
          <IconMinus
            v-else
            class="ml-auto text-slate-300 dark:text-slate-600"
          />
        </template>
        <template #cell-bias="{ value }">
          <IconCheck
            v-if="value === '✓'"
            class="ml-auto text-success-600 dark:text-success-500"
          />
          <IconMinus
            v-else
            class="ml-auto text-slate-300 dark:text-slate-600"
          />
        </template>
      </GenericTable>
    </section>

    <section v-if="channelsWithMetrics.length">
      <h2 class="mb-2 text-lg font-medium">{{ t("job.frameReview") }}</h2>
      <div v-for="c in channelsWithMetrics" :key="c.filter" class="mb-6">
        <div class="mb-2">
          <FilterChip :filter="c.filter" />
        </div>
        <div :class="[card, 'mb-2']">
          <MetricsChart :metrics="c.metrics || []" :filter="c.filter" />
        </div>
        <GenericTable
          :columns="metricColumns"
          :rows="metricRows(c)"
          :row-class="rejectedClass"
        >
          <template #cell-status="{ value }">
            <span
              :class="value === 'rejected' ? 'text-danger' : 'text-success'"
            >
              {{ value === "rejected" ? t("job.rejected") : t("job.kept") }}
            </span>
          </template>
        </GenericTable>
      </div>
    </section>

    <section v-if="masterRows.length">
      <h2 class="mb-2 text-lg font-medium">{{ t("job.mastersUsed") }}</h2>
      <GenericTable :columns="masterColumns" :rows="masterRows">
        <template #cell-filter="{ value }">
          <FilterChip v-if="value" :filter="String(value)" />
          <span v-else class="text-slate-400">—</span>
        </template>
      </GenericTable>
    </section>

    <section v-if="result.warnings && result.warnings.length">
      <h2 class="mb-2 text-lg font-medium">{{ t("import.warnings") }}</h2>
      <ul class="space-y-1">
        <li
          v-for="(w, i) in result.warnings"
          :key="i"
          class="text-sm text-warning"
        >
          ⚠ {{ w }}
        </li>
      </ul>
    </section>
  </div>
</template>
