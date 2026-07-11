<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { fileUrl } from "@/services/api";
import GenericTable, {
  type Column,
} from "@/components/Common/GenericTable.vue";
import MetricsChart from "@/components/Dataviz/MetricsChart.vue";
import ImageViewer from "@/components/Common/ImageViewer.vue";
import FilePreviewButton from "@/components/Common/FilePreviewButton.vue";
import FilterChip from "@/components/Common/FilterChip.vue";
import EngineChip from "@/components/Common/EngineChip.vue";
import StagePreviewTimeline from "@/components/Common/StagePreviewTimeline.vue";
import IconCheck from "@/components/Icons/IconCheck.vue";
import IconMinus from "@/components/Icons/IconMinus.vue";
import IconDownload from "@/components/Icons/IconDownload.vue";
import IconChevronRight from "@/components/Icons/IconChevronRight.vue";
import { card } from "@/constants/styles";
import { humanizeMs, baseName, tempC } from "@/utils/format";
import type { ChannelResult, RunResult } from "@/types";

const props = defineProps<{ result: RunResult; rerunnable?: boolean }>();
const emit = defineEmits<{ "rerun-stage": [stage: string] }>();
const { t } = useI18n();

type Row = Record<string, unknown>;
const ms = (v: unknown) => humanizeMs(Number(v));

// Outputs come either from the deep-sky `final` wrapper or, for planetary/comet lucky-imaging runs,
// from the flat top-level result. Notes fall back the same way.
const outputs = computed<string[]>(
  () => props.result.final?.outputs ?? props.result.outputs ?? [],
);
const notes = computed<string[]>(
  () => props.result.final?.notes ?? props.result.notes ?? [],
);
const finalImage = computed(() => {
  const out = outputs.value.find((o) => o.endsWith(".png"));
  return out ? fileUrl(out) : "";
});
const finalVideo = computed(() => {
  const out = outputs.value.find((o) => o.endsWith(".mp4"));
  return out ? fileUrl(out) : "";
});

// Planetary/comet lucky-imaging stats (frames kept of total), shown when there is no per-channel table.
const stackStats = computed(() => {
  const total = props.result.frame_count;
  const kept = props.result.stacked_frames;
  return typeof total === "number" && typeof kept === "number"
    ? { total, kept }
    : null;
});

// Planetary/lucky-imaging per-frame report: which frames were kept vs rejected, and their sharpness.
const planetaryFrames = computed(() => props.result.frames ?? []);
const planetaryFrameRows = computed<Row[]>(() =>
  planetaryFrames.value.map((f) => ({
    index: f.index,
    file: f.file,
    filter: f.filter || "",
    score: f.score,
    status: f.kept ? "kept" : "rejected",
  })),
);
const planetaryFrameColumns: Column<Row>[] = [
  { key: "index", label: t("fields.index"), sortable: true, align: "right" },
  { key: "file", label: t("fields.file"), searchable: true },
  {
    key: "filter",
    label: t("fields.filter"),
    sortable: true,
    searchable: true,
  },
  {
    key: "score",
    label: t("fields.score"),
    sortable: true,
    format: (v) => Number(v).toFixed(1),
    align: "right",
  },
  {
    key: "status",
    label: t("fields.status"),
    sortable: true,
    searchable: true,
  },
];

// Channel switcher: flip the preview between the final composite and each channel. Channel PNGs load
// only when selected (deferred). Ordered by the canonical filter sequence (L, R, G, B, Ha, …).
const FILTER_ORDER = ["L", "R", "G", "B", "Ha", "OIII", "SII"];
const channelViews = computed(() =>
  (props.result.channels ?? [])
    .filter((c) => c.preview_path)
    .slice()
    .sort(
      (a, b) =>
        (FILTER_ORDER.indexOf(a.filter) + 1 || 99) -
        (FILTER_ORDER.indexOf(b.filter) + 1 || 99),
    )
    .map((c) => ({ filter: c.filter, src: fileUrl(c.preview_path as string) })),
);
const activeView = ref("final"); // "final" or a channel filter name
const activeSrc = computed(() =>
  activeView.value === "final"
    ? finalImage.value
    : (channelViews.value.find((v) => v.filter === activeView.value)?.src ??
      finalImage.value),
);
// Reset to the composite whenever a different run is opened (the component instance is reused).
watch(
  () => props.result,
  () => {
    activeView.value = "final";
  },
);

// Ordered list of switchable views (Final first, then each channel) for the prev/next arrows; cyclic.
const views = computed(() => [
  "final",
  ...channelViews.value.map((v) => v.filter),
]);
function step(dir: number) {
  const list = views.value;
  if (list.length < 2) return;
  const i = list.indexOf(activeView.value);
  activeView.value = list[(i + dir + list.length) % list.length];
}

// Integration (exposure) time that went into the final image: per channel = stacked subs × per-sub
// exposure, and the sum across channels.
const integrationByChannel = computed(() =>
  (props.result.channels ?? [])
    .map((c) => ({ filter: c.filter, ms: c.stacked_frames * c.exposure_ms }))
    .filter((c) => c.ms > 0),
);
const totalIntegrationMs = computed(() =>
  integrationByChannel.value.reduce((sum, c) => sum + c.ms, 0),
);

// Pointing-pattern verdict from the registration offsets (dither/drift diagnosis). "drift" and
// "static" leave fixed-pattern residuals correlated (walking-noise risk) — flagged with ⚠.
const pointingLabel = (p?: string) => {
  if (!p) return "—";
  return p === "drift" || p === "static" ? `⚠ ${p}` : p;
};

const channelRows = computed<Row[]>(() =>
  (props.result.channels ?? []).map((c) => ({
    filter: c.filter,
    exposure_ms: c.exposure_ms,
    input: c.input_frames,
    stacked: c.stacked_frames,
    dark: c.selection?.dark ? "✓" : "—",
    flat: c.selection?.flat ? "✓" : "—",
    bias: c.selection?.bias ? "✓" : "—",
    pointing: pointingLabel(c.dither?.pattern),
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
  { key: "pointing", label: t("fields.pointing"), align: "right" },
];

const masterRows = computed<Row[]>(() =>
  // Deep-sky/comet results carry masters as an array; planetary's is a {label: path} object (no calib
  // table) — guard so a non-array never crashes the whole result view.
  (Array.isArray(props.result.masters) ? props.result.masters : []).map(
    (m) => ({
      type: m.type,
      filter: m.filter || "",
      exposure_ms: m.exposure_ms,
      gain: m.gain,
      offset: m.offset,
      temp_milli_c: m.temp_milli_c,
      frame_count: m.frame_count,
      file: baseName(m.path),
    }),
  ),
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
    path: m.path,
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
  { key: "view", label: "", align: "right" },
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
        <span
          v-if="result.final"
          class="ml-2 text-sm font-normal text-slate-500"
        >
          {{ result.final?.mode }} · {{ result.final?.channels?.join("+") }}
        </span>
        <span
          v-else-if="stackStats"
          class="ml-2 text-sm font-normal text-slate-500"
        >
          {{
            t("job.framesStacked", {
              kept: stackStats.kept,
              total: stackStats.total,
            })
          }}
        </span>
        <!-- Engine build that produced this result; amber when older than the serving engine. -->
        <EngineChip :engine="result.engine" class="ml-2 align-middle" />
      </h2>
      <div
        v-if="totalIntegrationMs > 0"
        class="mb-3 flex flex-wrap items-center gap-x-4 gap-y-1 text-sm"
      >
        <span class="text-slate-600 dark:text-slate-300">
          {{ t("capture.integration") }}:
          <span class="font-semibold text-brand-600 dark:text-brand-300">{{
            humanizeMs(totalIntegrationMs)
          }}</span>
        </span>
        <span
          v-for="c in integrationByChannel"
          :key="c.filter"
          class="inline-flex items-center gap-1.5"
        >
          <FilterChip :filter="c.filter" />
          <span class="text-slate-500 dark:text-slate-400">{{
            humanizeMs(c.ms)
          }}</span>
        </span>
      </div>
      <!-- Channel switcher: flip the preview between the final composite and each channel -->
      <div
        v-if="channelViews.length"
        class="mb-2 flex flex-wrap items-center gap-1.5"
      >
        <button
          type="button"
          class="rounded-md border px-2.5 py-1 text-xs font-medium transition-colors"
          :class="
            activeView === 'final'
              ? 'border-brand-500 bg-brand-50 text-brand-700 dark:bg-brand-900/30 dark:text-brand-200'
              : 'border-slate-200 text-slate-600 hover:border-brand-400 dark:border-slate-700 dark:text-slate-300'
          "
          @click="activeView = 'final'"
        >
          {{ t("job.finalView") }}
        </button>
        <button
          v-for="v in channelViews"
          :key="v.filter"
          type="button"
          class="rounded-md transition-transform"
          :class="
            activeView === v.filter
              ? 'ring-2 ring-brand-500 ring-offset-1 dark:ring-offset-slate-900'
              : 'opacity-75 hover:opacity-100'
          "
          :aria-label="v.filter"
          @click="activeView = v.filter"
        >
          <FilterChip :filter="v.filter" />
        </button>
      </div>
      <div class="relative">
        <ImageViewer :src="activeSrc" :alt="activeView" />
        <!-- Prev/next arrows: step through Final + each channel (cyclic) so it's clear you can switch -->
        <template v-if="channelViews.length">
          <button
            type="button"
            class="absolute left-2 top-1/2 z-10 -translate-y-1/2 rounded-md bg-slate-900/80 p-2 text-slate-200 backdrop-blur transition-colors hover:bg-slate-700"
            :aria-label="t('common.previous')"
            :title="t('common.previous')"
            @click="step(-1)"
          >
            <IconChevronRight class="rotate-180" />
          </button>
          <button
            type="button"
            class="absolute right-2 top-1/2 z-10 -translate-y-1/2 rounded-md bg-slate-900/80 p-2 text-slate-200 backdrop-blur transition-colors hover:bg-slate-700"
            :aria-label="t('common.next')"
            :title="t('common.next')"
            @click="step(1)"
          >
            <IconChevronRight />
          </button>
          <span
            class="pointer-events-none absolute left-2 top-2 z-10 rounded-md bg-slate-900/80 px-2 py-1 text-xs font-medium text-slate-100 backdrop-blur"
          >
            {{ activeView === "final" ? t("job.finalView") : activeView }}
          </span>
        </template>
      </div>
      <video
        v-if="finalVideo"
        :src="finalVideo"
        controls
        class="mt-3 w-full max-w-full rounded-md border border-slate-200 dark:border-slate-700"
      />
      <div class="mt-2 flex flex-wrap gap-3 text-sm">
        <a
          v-for="o in outputs"
          :key="o"
          :href="fileUrl(o)"
          target="_blank"
          class="inline-flex items-center gap-1 text-brand-600 hover:underline dark:text-brand-300"
        >
          <IconDownload /> {{ baseName(o) }}
        </a>
      </div>
      <ul v-if="notes.length" class="mt-2 space-y-1">
        <li
          v-for="(n, i) in notes"
          :key="i"
          class="text-xs text-slate-500 dark:text-slate-400"
        >
          · {{ n }}
        </li>
      </ul>
    </section>

    <!-- Processing-step filmstrip, directly below the final image (before the data tables). -->
    <StagePreviewTimeline
      :result="props.result"
      :editable="rerunnable"
      @edit="(s) => emit('rerun-stage', s)"
    />

    <section v-if="planetaryFrames.length">
      <h2 class="mb-2 text-lg font-medium">{{ t("job.frameReview") }}</h2>
      <GenericTable
        :columns="planetaryFrameColumns"
        :rows="planetaryFrameRows"
        :row-class="rejectedClass"
      >
        <template #cell-filter="{ value }">
          <FilterChip v-if="value" :filter="String(value)" />
          <span v-else class="text-slate-400">—</span>
        </template>
        <template #cell-status="{ value }">
          <span :class="value === 'rejected' ? 'text-danger' : 'text-success'">
            {{ value === "rejected" ? t("job.rejected") : t("job.kept") }}
          </span>
        </template>
      </GenericTable>
    </section>

    <section v-if="channelRows.length">
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
          <template #cell-view="{ row }">
            <FilePreviewButton :path="String(row.path)" />
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
