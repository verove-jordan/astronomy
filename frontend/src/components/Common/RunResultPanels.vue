<script setup lang="ts">
import { STAR_MODES } from "@/constants/modes";
import { computed, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { fileUrl, type ApiError } from "@/services/api";
import { useJobsStore } from "@/stores/jobs";
import GenericTable, {
  type Column,
} from "@/components/Common/GenericTable.vue";
import MetricsChart from "@/components/Dataviz/MetricsChart.vue";
import ImageViewer from "@/components/Common/ImageViewer.vue";
import StarLabelOverlay from "@/components/Common/StarLabelOverlay.vue";
import StarField3D from "@/components/Common/StarField3D.vue";
import FilePreviewButton from "@/components/Common/FilePreviewButton.vue";
import FilterChip from "@/components/Common/FilterChip.vue";
import EngineChip from "@/components/Common/EngineChip.vue";
import Pill from "@/components/Common/Pill.vue";
import Spinner from "@/components/Common/Spinner.vue";
import StagePreviewTimeline from "@/components/Common/StagePreviewTimeline.vue";
import IconCheck from "@/components/Icons/IconCheck.vue";
import IconMinus from "@/components/Icons/IconMinus.vue";
import IconDownload from "@/components/Icons/IconDownload.vue";
import IconChevronRight from "@/components/Icons/IconChevronRight.vue";
import IconStar from "@/components/Icons/IconStar.vue";
import { btnGhost, card } from "@/constants/styles";
import { humanizeMs, baseName, tempC } from "@/utils/format";
import type { ChannelResult, PhotomRecord, RunResult } from "@/types";

import { compareFilters } from "@/constants/filters";
// jobId is optional: JobView passes it for succeeded runs (enables the star count/labels); the
// Runs gallery mounts this component from a disk-loaded run.json with no job id — feature inert.
const props = defineProps<{
  result: RunResult;
  rerunnable?: boolean;
  jobId?: number;
}>();
const emit = defineEmits<{ "rerun-stage": [stage: string] }>();
const { t, locale } = useI18n();

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
const channelViews = computed(() =>
  (props.result.channels ?? [])
    .filter((c) => c.preview_path)
    .slice()
    .sort((a, b) => compareFilters(a.filter, b.filter))
    .map((c) => ({ filter: c.filter, src: fileUrl(c.preview_path as string) })),
);
// Extra monochrome deliverables (processed Luminance-only / combined all-channel) — shown as their
// own switcher entries next to the channels, opened in the same viewer. View key = "mono:<kind>".
const monoViews = computed(() =>
  (props.result.final?.mono_outputs ?? [])
    .filter((m) => m.png)
    .map((m) => ({
      key: `mono:${m.kind}`,
      label: t(`job.mono.${m.kind}`),
      src: fileUrl(m.png),
    })),
);
const activeView = ref("final"); // "final", a channel filter name, or a "mono:<kind>" key
const activeSrc = computed(() => {
  if (activeView.value === "final") return finalImage.value;
  const mono = monoViews.value.find((v) => v.key === activeView.value);
  if (mono) return mono.src;
  return (
    channelViews.value.find((v) => v.filter === activeView.value)?.src ??
    finalImage.value
  );
});
// Label for the currently-shown view (Final / a mono output's name / the channel filter).
const activeLabel = computed(() => {
  if (activeView.value === "final") return t("job.finalView");
  return (
    monoViews.value.find((v) => v.key === activeView.value)?.label ??
    activeView.value
  );
});
// Reset to the composite whenever a different run is opened (the component instance is reused).
watch(
  () => props.result,
  () => {
    activeView.value = "final";
  },
);

// Ordered list of switchable views (Final, then each channel, then the mono outputs) for the prev/next
// arrows; cyclic.
const views = computed(() => [
  "final",
  ...channelViews.value.map((v) => v.filter),
  ...monoViews.value.map((v) => v.key),
]);
function step(dir: number) {
  const list = views.value;
  if (list.length < 2) return;
  // "3d" is deliberately not in `views`: the arrows step through IMAGES, and indexOf returning -1
  // lands the next press on "final", which is the sensible way back out of the 3D view.
  const i = list.indexOf(activeView.value);
  activeView.value = list[(i + dir + list.length) % list.length];
}

// --- Star count + name overlay (jobId-gated; see the prop comment) --------------------------------
const jobsStore = useJobsStore();
// The PROCESSING mode comes from run.json's options block. It is emphatically NOT `final.mode`,
// which is the channel COMPOSITION ("LRGB" / "HaLRGB" / "SHO" / "mono") — gating on that compared a
// composition against processing-mode names, never matched, and silently hid the whole star feature
// on every run. The backend has always read the mode from the job params (internal/api/stars.go).
const runMode = computed(() => props.result.options?.mode ?? "");
const starsEnabled = computed(
  () =>
    !!props.jobId &&
    !!finalImage.value &&
    !!props.result.final &&
    STAR_MODES.includes(runMode.value),
);
const stars = computed(() =>
  props.jobId ? jobsStore.starsFor(props.jobId) : null,
);
const counting = computed(
  () => !!props.jobId && !!jobsStore.starsBusy[props.jobId],
);
const starsError = ref("");
const overlayOn = ref(false);
// The overlay is worth showing as soon as there is ANYTHING to draw. Detected-star markers need no
// astrometric solution, so a run whose plate-solve failed still gets its stars plotted even though
// it can never have name labels.
const overlayAvailable = computed(
  () =>
    (!!stars.value?.solved && (stars.value?.labels.length ?? 0) > 0) ||
    (stars.value?.stars?.length ?? 0) > 0,
);

// How many detected stars to plot. Starts at a readable density rather than 0 (an overlay that
// draws nothing reads as broken) or everything (a grey wash over the image).
const plottedStars = computed(() => stars.value?.stars?.length ?? 0);
// Count the identifications on the plotted list rather than trusting solve.identified: an older
// stars.json predates that field, and the markers are what the user can actually hover.
const identifiedStars = computed(
  () => stars.value?.stars?.filter((s) => s.star?.name).length ?? 0,
);
const starLimit = ref(250);
watch(plottedStars, (n) => {
  if (n && starLimit.value > n) starLimit.value = n;
});
const overlayVisible = computed(
  () =>
    overlayOn.value && overlayAvailable.value && activeView.value === "final",
);
const formattedCount = computed(() =>
  (stars.value?.count ?? 0).toLocaleString(locale.value),
);

// Load the cached annotation as soon as the feature applies (silent when never computed). The 3D
// scene rides along: it is built by the same annotation pass, so if there is one there is the other.
watch(
  () => [props.jobId, starsEnabled.value] as const,
  ([id, enabled]) => {
    starsError.value = "";
    if (id && enabled) {
      void jobsStore.fetchStars(id);
      void jobsStore.fetchScene3D(id);
    }
  },
  { immediate: true },
);

// --- 3D field map --------------------------------------------------------------------------------
const scene = computed(() =>
  props.jobId ? jobsStore.sceneFor(props.jobId) : null,
);
// The chip appears whenever the engine answered at all — including with available:false. Hiding it
// then is what made a run with 957 detected stars look as though the 3D view simply did not exist;
// opening it and being told why (and offered the fix) is the honest version.
const scene3dAvailable = computed(() => !!scene.value);
const is3D = computed(() => activeView.value === "3d");

async function countStarsAction() {
  if (!props.jobId || counting.value) return;
  starsError.value = "";
  try {
    const res = await jobsStore.countStars(props.jobId);
    // The user just asked for stars — turn the labels on when there are any to show.
    if (res.solved && res.labels.length) overlayOn.value = true;
  } catch (e) {
    starsError.value = (e as ApiError).message || t("stars.failed");
  }
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
    coverage:
      c.covered_frac != null ? `${(c.covered_frac * 100).toFixed(0)}%` : "—",
  })),
);

// Photometric normalization of a cross-session run: one row per (channel, group), from the rich
// per-group provenance when present (run.json `groups`), falling back to the bare photom records so
// older reuse-merged runs display too. Absent for single-session runs → the section hides.
const photomRows = computed<Row[]>(() =>
  (props.result.channels ?? []).flatMap((c) => {
    const groups = c.groups?.length
      ? c.groups.map((g) => ({ g, rec: g.photom }))
      : (c.photom ?? []).map((rec) => ({ g: undefined, rec }));
    return groups
      .filter(({ rec }) => rec)
      .map(({ g, rec }) => ({
        session: g?.session || rec!.session || rec!.label,
        filter: c.filter,
        frames: g?.frames ?? rec!.frames,
        stacked:
          g?.stacked_frames != null ? `${g.stacked_frames}/${g.frames}` : "—",
        scale: `×${rec!.scale.toFixed(2)}`,
        offset: `${rec!.offset >= 0 ? "+" : ""}${rec!.offset.toFixed(3)}`,
        residual: `${(rec!.resid * 100).toFixed(1)}%`,
        rotation:
          g?.rotation_deg != null
            ? `↻ ${g.rotation_deg.toFixed(1)}°${g.overlap_frac != null ? ` · ${(g.overlap_frac * 100).toFixed(0)}%` : ""}`
            : "—",
        applied: photomStateLabel(rec!),
        masters: [g?.dark, g?.flat, g?.bias].filter(Boolean).join(" · ") || "—",
      }));
  }),
);
const photomColumns: Column<Row>[] = [
  { key: "session", label: t("fields.session"), sortable: true },
  { key: "filter", label: t("fields.filter"), sortable: true },
  { key: "frames", label: t("fields.count"), sortable: true, align: "right" },
  { key: "stacked", label: t("job.photomStacked"), align: "right" },
  { key: "scale", label: t("fields.scale"), align: "right" },
  { key: "offset", label: t("fields.offset"), align: "right" },
  { key: "residual", label: t("fields.residual"), align: "right" },
  { key: "rotation", label: t("fields.rotation"), align: "right" },
  { key: "applied", label: t("fields.applied") },
  { key: "masters", label: t("calib.title") },
];

// photomStateLabel renders a record's outcome + the ladder rung that set its scale (method) and
// the no-clip degrade flag — the "which evidence balanced this night" chip.
const photomStateLabel = (rec: PhotomRecord): string => {
  let state = rec.ref
    ? t("job.photomReference")
    : rec.applied
      ? t("job.photomApplied")
      : t("job.photomSkipped");
  const method = rec.method || (rec.meta_seeded ? "seeded" : "");
  if (method && method !== "measured" && !rec.ref)
    state += ` · ${t(`job.photomMethod.${method}`)}`;
  if (rec.reverted) state += ` · ⚠ ${t("job.photomReverted")}`;
  return state;
};

// Photom records flattened for the filmstrip captions on the prenorm/normalized cards.
const photomRecords = computed(() =>
  (props.result.channels ?? []).flatMap(
    (c) => c.groups?.map((g) => g.photom).filter((r) => !!r) ?? c.photom ?? [],
  ),
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
  { key: "coverage", label: t("fields.coverage"), align: "right" },
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
      <!-- Channel switcher: flip the preview between the final composite, each channel, any mono
           output, and the 3D field map -->
      <div
        v-if="channelViews.length || monoViews.length || scene3dAvailable"
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
        <button
          v-for="v in monoViews"
          :key="v.key"
          type="button"
          class="rounded-md border px-2.5 py-1 text-xs font-medium transition-colors"
          :class="
            activeView === v.key
              ? 'border-brand-500 bg-brand-50 text-brand-700 dark:bg-brand-900/30 dark:text-brand-200'
              : 'border-slate-200 text-slate-600 hover:border-brand-400 dark:border-slate-700 dark:text-slate-300'
          "
          @click="activeView = v.key"
        >
          {{ v.label }}
        </button>
        <button
          v-if="scene3dAvailable"
          type="button"
          class="rounded-md border px-2.5 py-1 text-xs font-medium transition-colors"
          :class="
            is3D
              ? 'border-brand-500 bg-brand-50 text-brand-700 dark:bg-brand-900/30 dark:text-brand-200'
              : 'border-slate-200 text-slate-600 hover:border-brand-400 dark:border-slate-700 dark:text-slate-300'
          "
          :title="t('scene3d.chipHint')"
          data-demo="scene3d-chip"
          @click="activeView = '3d'"
        >
          {{ t("scene3d.chip") }}
        </button>
      </div>
      <StarField3D
        v-if="is3D && scene"
        :manifest="scene"
        :stars="stars"
        :rebuilding="counting"
        @recompute="countStarsAction"
      />
      <div v-else class="relative">
        <ImageViewer
          :src="activeSrc"
          :alt="activeView"
          :no-transition="overlayVisible"
        >
          <template v-if="overlayVisible && stars" #overlay="frame">
            <StarLabelOverlay
              :labels="stars.labels"
              :stars="stars.stars"
              :starLimit="starLimit"
              :scaleArcsecPx="stars.solve?.scale_arcsec_px"
              :image-w="stars.image?.width || frame.natW"
              :image-h="stars.image?.height || frame.natH"
              :nat-w="frame.natW"
              :nat-h="frame.natH"
              :scale="frame.scale"
              :tx="frame.tx"
              :ty="frame.ty"
              :cw="frame.cw"
              :ch="frame.ch"
            />
          </template>
        </ImageViewer>
        <!-- Star-name overlay toggle (bottom-left; the viewer's own toolbar owns the top-right). -->
        <button
          v-if="overlayAvailable && activeView === 'final'"
          type="button"
          class="absolute bottom-2 left-2 z-10 rounded-md bg-slate-900/80 p-2 text-slate-200 backdrop-blur transition-colors hover:bg-slate-700"
          :aria-pressed="overlayOn"
          :aria-label="
            overlayOn ? t('stars.overlayHide') : t('stars.overlayShow')
          "
          :title="overlayOn ? t('stars.overlayHide') : t('stars.overlayShow')"
          data-demo="stars-toggle"
          @click="overlayOn = !overlayOn"
        >
          <IconStar :filled="overlayOn" />
        </button>
        <!-- Prev/next arrows: step through Final + each channel + mono output (cyclic) so it's clear you can switch -->
        <template v-if="channelViews.length || monoViews.length">
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
            {{ activeLabel }}
          </span>
        </template>
      </div>
      <!-- Star counter: manual compute → count badge (+ the overlay toggle above once solved). -->
      <div
        v-if="starsEnabled"
        class="mt-2 flex flex-wrap items-center gap-3 text-sm"
      >
        <button
          v-if="!stars"
          :class="btnGhost"
          :disabled="counting"
          data-demo="stars-count"
          @click="countStarsAction"
        >
          ★ {{ counting ? t("stars.counting") : t("stars.count") }}
        </button>
        <Spinner v-if="counting">{{ t("stars.solveHint") }}</Spinner>
        <Pill
          v-if="stars"
          color-class="bg-brand-100 text-brand-800 ring-1 ring-brand-200 dark:bg-brand-900/40 dark:text-brand-300 dark:ring-brand-800/50"
          data-demo="stars-badge"
        >
          ★ {{ t("stars.detected", { n: formattedCount }) }}
        </Pill>
        <!-- How many of those the catalogue could actually name. Worth stating plainly: it is the
             difference between a field of anonymous dots and one you can read, and when it is low
             because the deep catalogue was never downloaded, the hint says so. -->
        <span
          v-if="identifiedStars"
          class="text-xs text-slate-500 dark:text-slate-400"
          :title="
            stars?.solve?.star_catalog === 'embedded'
              ? t('stars.shallowCatalogue')
              : undefined
          "
        >
          {{ t("stars.identified", { n: identifiedStars }) }}
          <template v-if="stars?.solve?.star_catalog === 'embedded'">
            ⚠</template
          >
        </span>
        <span
          v-if="stars && !stars.solved"
          class="text-xs text-slate-500 dark:text-slate-400"
        >
          {{ t("stars.noSolve")
          }}<template v-if="stars.solve?.reason">
            ({{ stars.solve.reason }})</template
          >
        </span>
        <!-- How many detected stars to plot. Zooming in reveals fainter ones for free: the same
             budget covers a smaller patch of sky, so it reaches deeper into the brightest-first list. -->
        <label
          v-if="plottedStars"
          class="flex items-center gap-2 text-xs text-slate-500 dark:text-slate-400"
        >
          {{ t("stars.shown") }}
          <input
            v-model.number="starLimit"
            type="range"
            min="0"
            :max="plottedStars"
            step="10"
            class="w-32 accent-brand-600"
            :aria-label="t('stars.shown')"
          />
          <span class="w-10 tabular-nums">{{ starLimit }}</span>
          <span
            v-if="stars && stars.count > plottedStars"
            :title="t('stars.plottedCapHint', { n: plottedStars })"
            class="text-slate-400"
            >⚠</span
          >
        </label>
      </div>
      <p
        v-if="starsError"
        class="mt-1 text-sm text-red-600 dark:text-red-400"
        data-demo="stars-error"
      >
        {{ starsError }}
      </p>
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
      :photom="photomRecords"
      :job-id="props.jobId"
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

    <!-- Cross-session runs: how each night's signal was mapped onto the reference before stacking. -->
    <section v-if="photomRows.length">
      <h2 class="mb-2 text-lg font-medium">{{ t("job.photomTitle") }}</h2>
      <p class="mb-2 text-xs text-slate-500 dark:text-slate-400">
        {{ t("job.photomHint") }}
      </p>
      <p
        v-if="result.combine_crop"
        class="mb-2 text-xs"
        :class="
          result.combine_crop.applied
            ? 'text-emerald-600 dark:text-emerald-400'
            : 'text-amber-600 dark:text-amber-400'
        "
      >
        {{
          result.combine_crop.applied
            ? t("job.combineCropApplied", {
                w: result.combine_crop.w,
                h: result.combine_crop.h,
                pct: Math.round(result.combine_crop.frac * 100),
              })
            : t("job.combineCropSkipped", {
                pct: Math.round(result.combine_crop.frac * 100),
              })
        }}
      </p>
      <GenericTable :columns="photomColumns" :rows="photomRows">
        <template #cell-filter="{ value }">
          <FilterChip :filter="String(value)" />
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
