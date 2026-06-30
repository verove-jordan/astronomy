<script setup lang="ts">
import { ref, computed, onMounted, nextTick } from "vue";
import { useRouter } from "vue-router";
import { useI18n } from "vue-i18n";
import { useBrowseStore } from "@/stores/browse";
import { useJobsStore } from "@/stores/jobs";
import { useCaptureSummary } from "@/composables/useCaptureSummary";
import { useChannelMapping } from "@/composables/useChannelMapping";
import GenericTable, {
  type Column,
} from "@/components/Common/GenericTable.vue";
import FilterChip from "@/components/Common/FilterChip.vue";
import FileBrowser from "@/components/Common/FileBrowser.vue";
import CaptureSummary from "@/components/Capture/CaptureSummary.vue";
import FilterMappingEditor from "@/components/Capture/FilterMappingEditor.vue";
import ReusePanel from "@/components/Capture/ReusePanel.vue";
import CalibrationPanel from "@/components/Capture/CalibrationPanel.vue";
import FilePreviewButton from "@/components/Common/FilePreviewButton.vue";
import CollapsibleCard from "@/components/Common/CollapsibleCard.vue";
import StatusPill from "@/components/Common/StatusPill.vue";
import IconFolder from "@/components/Icons/IconFolder.vue";
import type { CreateOpts } from "@/stores/jobs";
import type {
  ReusePreview,
  CalibPreview,
  ProcessingHistoryEntry,
} from "@/types";
import {
  btnPrimary,
  btnGhost,
  card,
  input,
  frameTypeAccentClass,
  frameTypeCardClass,
} from "@/constants/styles";
import { humanizeMs, baseName, formatTimestamp } from "@/utils/format";
import type { FrameSet } from "@/types";

const router = useRouter();
const { t } = useI18n();
const browseStore = useBrowseStore();
const jobsStore = useJobsStore();

const selectedPaths = ref<string[]>([]);
const rootPath = ref("");
const selectedMode = ref("deepsky");
const selectedFormat = ref("image");
const launching = ref(false);

// Run-quality toggles (defaults match the backend presets).
const colorCalibration = ref(true);
const denoise = ref(true);
const dropWheelTransition = ref(true);
const haExcludeStars = ref(true); // default: Hα on the galaxy/nebulosity only; uncheck → over everything
// Opt-in: drive the local AI agent to auto-tune the finish (deepsky/nebula only). Off by default.
const supervise = ref(false);

const modes = ["deepsky", "nebula", "milkyway", "planetary", "comet"];
const formats = ["image", "video", "both"];

// Milky-Way nightscape render style (foreground composite + linear grade); only shown for milkyway.
const look = ref("natural");
const looks = ["natural", "iphone", "deepsky"];
// Sky brightness target for the nightscape auto-levels (data-driven stretch); balanced is the default.
const brightness = ref("balanced");
const brightnesses = ["darker", "balanced", "brighter"];
// Optional calibration-frame folders (dark/flat/bias) applied before stacking; empty = none.
const darkDir = ref("");
const flatDir = ref("");
const biasDir = ref("");
const isMilkyway = computed(() => selectedMode.value === "milkyway");
// The supervisor tunes the LRGB/combine finish, which only runs for deepsky/nebula.
const supportsSupervise = computed(
  () => selectedMode.value === "deepsky" || selectedMode.value === "nebula",
);

onMounted(async () => {
  await browseStore.browse();
  rootPath.value = browseStore.path;
  browseStore.loadProcessed(); // mark folders already used in a past processing
});

async function openDir(path: string) {
  await browseStore.browse(path);
}
// Cross-session reuse: discovered prior data + the user's selection.
const reusePreview = ref<ReusePreview | null>(null);
const reuseEnabled = ref(true);
const reuseSelected = ref<number[]>([]);
// Calibration suggestions from the library + the suggestion ids the user unchecked to skip.
const calibPreview = ref<CalibPreview | null>(null);
const calibExcluded = ref<string[]>([]);

const reuseSessionIds = computed(() =>
  (reusePreview.value?.reuse.sessions ?? []).map((s) => s.session_id),
);
// Disabled if the user turned reuse off, or kept it on but deselected every session.
const reuseDisabledForRun = computed(
  () =>
    !reuseEnabled.value ||
    (reuseSessionIds.value.length > 0 && reuseSelected.value.length === 0),
);
// Send a session list only when it is a strict subset; empty = fold in all discovered sessions.
const reuseSelectionForRun = computed(() =>
  reuseSelected.value.length === reuseSessionIds.value.length
    ? []
    : reuseSelected.value,
);

async function inspectSelected(paths: string[]) {
  selectedPaths.value = paths;
  await browseStore.inspect(paths);
  // Reuse + calibration previews are independent — fetch them together.
  const [reuse, calib] = await Promise.all([
    jobsStore.previewReuse(paths),
    jobsStore.previewCalibration(paths),
  ]);
  reusePreview.value = reuse;
  // Default: fold in every discovered prior session (user can deselect).
  reuseSelected.value = (reuse?.reuse.sessions ?? []).map((s) => s.session_id);
  reuseEnabled.value = true;
  // Default: include every matched library master (user can uncheck).
  calibPreview.value = calib;
  calibExcluded.value = [];
}

const inv = computed(() => browseStore.inventory);
const summary = useCaptureSummary(inv);
const { detectedFilters, mapping, overrides } = useChannelMapping(inv);

const counts = computed(() => {
  const c: Record<string, number> = {};
  for (const f of inv.value?.frames ?? []) c[f.type] = (c[f.type] || 0) + 1;
  return c;
});

type Row = Record<string, unknown>;
function rowsFor(types: string[]): Row[] {
  return (inv.value?.sets ?? [])
    .filter((s: FrameSet) => types.includes(s.key.type))
    .map((s: FrameSet) => ({
      type: s.key.type,
      object: s.key.object || "",
      filter: s.key.filter || "",
      exposure_ms: s.key.exposure_ms,
      count: s.count,
      integration: s.total_integration_ms,
      gain: s.key.gain,
      offset: s.key.offset,
      temp: s.key.temp_bucket_c,
    }));
}
const lightRows = computed(() => rowsFor(["LIGHT"]));
const calibRows = computed(() => rowsFor(["DARK", "FLAT", "DARKFLAT", "BIAS"]));

const ms = (v: unknown) => humanizeMs(Number(v));
const degC = (v: unknown) => `${v}°C`;

const lightColumns: Column<Row>[] = [
  {
    key: "object",
    label: t("fields.object"),
    sortable: true,
    searchable: true,
  },
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
  { key: "count", label: t("fields.count"), sortable: true, align: "right" },
  {
    key: "integration",
    label: t("fields.integration"),
    sortable: true,
    format: ms,
    align: "right",
  },
  { key: "gain", label: t("fields.gain"), sortable: true, align: "right" },
  { key: "offset", label: t("fields.offset"), sortable: true, align: "right" },
  {
    key: "temp",
    label: t("fields.temp"),
    sortable: true,
    format: degC,
    align: "right",
  },
];
const calibColumns: Column<Row>[] = [
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
  { key: "count", label: t("fields.count"), sortable: true, align: "right" },
  { key: "gain", label: t("fields.gain"), sortable: true, align: "right" },
  { key: "offset", label: t("fields.offset"), sortable: true, align: "right" },
  {
    key: "temp",
    label: t("fields.temp"),
    sortable: true,
    format: degC,
    align: "right",
  },
];

// Individual files (with paths) for the click-to-view file viewer — the set tables omit frame paths.
const fileRows = computed<Row[]>(() =>
  (inv.value?.frames ?? []).map((f) => ({
    name: f.path.split("/").pop() || f.path,
    path: f.path,
    type: f.type,
    filter: f.filter || "",
    exposure_ms: f.exposure_ms,
    dims: f.width && f.height ? `${f.width}×${f.height}` : "",
  })),
);
const fileColumns: Column<Row>[] = [
  { key: "name", label: t("fields.file"), sortable: true, searchable: true },
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
  { key: "dims", label: t("fields.dimensions") },
  { key: "view", label: "", align: "right" },
];

const canRun = computed(
  () => selectedPaths.value.length > 0 && !!inv.value && !launching.value,
);

// One path → show it; several → a count (the pills in the browser already list them).
const summaryPath = computed(() => {
  if (selectedPaths.value.length === 1) return selectedPaths.value[0];
  if (selectedPaths.value.length)
    return t("import.nFolders", { n: selectedPaths.value.length });
  return "";
});

// The run options shared by "Run" (immediate) and "Add to queue" (sequential lane).
function runOpts(): CreateOpts {
  return {
    paths: selectedPaths.value,
    filterMap: overrides.value,
    colorCalibration: colorCalibration.value,
    denoise: denoise.value,
    haExcludeStars: haExcludeStars.value,
    dropWheelTransition: dropWheelTransition.value,
    supervise: supportsSupervise.value && supervise.value,
    look: isMilkyway.value ? look.value : undefined,
    brightness: isMilkyway.value ? brightness.value : undefined,
    darkDir: isMilkyway.value ? darkDir.value || undefined : undefined,
    flatDir: isMilkyway.value ? flatDir.value || undefined : undefined,
    biasDir: isMilkyway.value ? biasDir.value || undefined : undefined,
    inventory: inv.value,
    reuseDisabled: reuseDisabledForRun.value,
    // Only send a session list when the user deselected some; empty = fold in all discovered.
    reuseSessions: reuseSelectionForRun.value,
    // Library masters the user unchecked in the Calibration panel (skipped at process time).
    calibExclude: calibExcluded.value,
  };
}

async function runPipeline() {
  launching.value = true;
  try {
    const id = await jobsStore.create(
      selectedPaths.value[0],
      selectedMode.value,
      selectedFormat.value,
      runOpts(),
    );
    router.push({ name: "job", params: { id: String(id) } });
  } finally {
    launching.value = false;
  }
}

// Add to queue: enqueue a sequential job and stay on the page so the user can stack more. The chain runs
// one-at-a-time, auto-advancing — visible in the Tasks tab.
const queuedCount = ref(0);
async function queuePipeline() {
  launching.value = true;
  try {
    await jobsStore.create(
      selectedPaths.value[0],
      selectedMode.value,
      selectedFormat.value,
      { ...runOpts(), sequential: true },
    );
    queuedCount.value++;
    browseStore.loadProcessed(); // surface the just-queued set in the Processing history
  } finally {
    launching.value = false;
  }
}

// Re-run a past folder-set: re-select the folders that still exist, restore mode/format, inspect, and
// scroll to the run controls. Deleted folders are dropped (the chips show them crossed-out).
const runControls = ref<HTMLElement | null>(null);
async function useHistory(entry: ProcessingHistoryEntry) {
  const existing = entry.paths.filter((p) => p.exists).map((p) => p.path);
  if (!existing.length) return;
  browseStore.selectPaths(existing);
  if (entry.mode && modes.includes(entry.mode)) selectedMode.value = entry.mode;
  if (entry.format && formats.includes(entry.format))
    selectedFormat.value = entry.format;
  await inspectSelected(existing);
  await nextTick();
  runControls.value?.scrollIntoView({ behavior: "smooth", block: "center" });
}

// Chip style for a history folder: muted + struck-through when the folder no longer exists on disk.
function histChip(exists: boolean): string {
  return exists
    ? "inline-flex items-center gap-1 rounded-md border border-slate-200 px-2 py-1 text-xs text-slate-600 dark:border-slate-700 dark:text-slate-300"
    : "inline-flex items-center gap-1 rounded-md border border-dashed border-slate-300 px-2 py-1 text-xs text-slate-400 line-through dark:border-slate-600 dark:text-slate-500";
}
</script>

<template>
  <div class="space-y-6">
    <div>
      <h1 class="text-2xl font-semibold">{{ t("import.title") }}</h1>
      <p class="text-sm text-slate-500 dark:text-slate-400">
        {{ t("import.hint") }}
      </p>
    </div>

    <div :class="card">
      <FileBrowser
        :path="browseStore.path"
        :root="rootPath"
        :entries="browseStore.entries"
        :loading="browseStore.loading"
        :selected="browseStore.selected"
        :error="browseStore.error"
        :fetch-children="browseStore.listDir"
        :processed="browseStore.processedByPath"
        @navigate="openDir"
        @inspect="inspectSelected"
        @toggle="browseStore.toggleSelected"
        @clear-selection="browseStore.clearSelected"
      />
    </div>

    <!-- Processing history: re-run a past folder-set (deleted folders shown crossed-out) -->
    <CollapsibleCard
      v-if="browseStore.processingHistory.length"
      :title="t('import.history.title')"
      storage-key="astrostack.import.history"
    >
      <ul class="max-h-72 space-y-3 overflow-y-auto">
        <li
          v-for="entry in browseStore.processingHistory"
          :key="entry.jobId"
          class="rounded-md border border-slate-200 p-2 dark:border-slate-700"
        >
          <div class="flex flex-wrap items-center gap-2">
            <StatusPill :status="entry.status" />
            <span class="text-sm font-medium">{{
              entry.object || t("import.history.untitled")
            }}</span>
            <span class="text-xs text-slate-400">
              {{ entry.mode ? t("run.modes." + entry.mode) : "" }} ·
              {{ formatTimestamp(entry.createdAtMs) }}
              <template v-if="entry.runs > 1">
                · {{ t("import.history.runs", { n: entry.runs }) }}</template
              >
            </span>
            <button
              :class="btnGhost"
              class="ml-auto !px-2 !py-1 !text-xs"
              :disabled="!entry.paths.some((p) => p.exists)"
              @click="useHistory(entry)"
            >
              {{ t("import.history.useAgain") }}
            </button>
          </div>
          <div class="mt-1.5 flex flex-wrap gap-1.5">
            <span
              v-for="p in entry.paths"
              :key="p.path"
              :class="histChip(p.exists)"
              :title="
                p.exists ? p.path : t('import.history.deleted') + ': ' + p.path
              "
            >
              <IconFolder class="h-3 w-3 shrink-0" />
              {{ baseName(p.path) }}
            </span>
          </div>
        </li>
      </ul>
    </CollapsibleCard>

    <!-- Selected capture + channel mapping + run controls -->
    <div v-if="inv" class="grid gap-4 lg:grid-cols-2">
      <CaptureSummary :summary="summary" :path="summaryPath" />
      <FilterMappingEditor
        v-if="detectedFilters.length"
        v-model="mapping"
        :detected-filters="detectedFilters"
        :detection="inv.channel_detection"
      />
    </div>

    <ReusePanel
      v-if="inv"
      v-model:enabled="reuseEnabled"
      v-model:selected="reuseSelected"
      :preview="reusePreview"
    />

    <CalibrationPanel
      v-if="inv"
      v-model:excluded="calibExcluded"
      :preview="calibPreview"
    />

    <div ref="runControls" :class="card">
      <div class="flex flex-wrap items-end gap-4">
        <label class="text-sm">
          <span class="mb-1 block text-xs font-medium text-slate-500">{{
            t("run.mode")
          }}</span>
          <select v-model="selectedMode" :class="input" data-demo="run-mode">
            <option v-for="mo in modes" :key="mo" :value="mo">
              {{ t("run.modes." + mo) }}
            </option>
          </select>
        </label>
        <label class="text-sm">
          <span class="mb-1 block text-xs font-medium text-slate-500">{{
            t("run.format")
          }}</span>
          <select
            v-model="selectedFormat"
            :class="input"
            data-demo="run-format"
          >
            <option v-for="fmt in formats" :key="fmt" :value="fmt">
              {{ t("run.formats." + fmt) }}
            </option>
          </select>
        </label>
        <label v-if="isMilkyway" class="text-sm">
          <span class="mb-1 block text-xs font-medium text-slate-500">{{
            t("run.look")
          }}</span>
          <select v-model="look" :class="input">
            <option v-for="lk in looks" :key="lk" :value="lk">
              {{ t("run.looks." + lk) }}
            </option>
          </select>
        </label>
        <label v-if="isMilkyway" class="text-sm">
          <span class="mb-1 block text-xs font-medium text-slate-500">{{
            t("run.brightness")
          }}</span>
          <select v-model="brightness" :class="input">
            <option v-for="b in brightnesses" :key="b" :value="b">
              {{ t("run.brightnesses." + b) }}
            </option>
          </select>
        </label>
        <div class="flex flex-col gap-1 text-sm">
          <label class="flex items-center gap-2">
            <input
              v-model="colorCalibration"
              type="checkbox"
              class="accent-brand-500"
              data-demo="opt-colorCalibration"
            />
            {{ t("run.colorCalibration") }}
          </label>
          <label class="flex items-center gap-2">
            <input
              v-model="denoise"
              type="checkbox"
              class="accent-brand-500"
              data-demo="opt-denoise"
            />
            {{ t("run.denoise") }}
          </label>
          <label class="flex items-center gap-2">
            <input
              v-model="haExcludeStars"
              type="checkbox"
              class="accent-brand-500"
              data-demo="opt-haExcludeStars"
            />
            {{ t("run.haExcludeStars") }}
          </label>
          <label class="flex items-center gap-2">
            <input
              v-model="dropWheelTransition"
              type="checkbox"
              class="accent-brand-500"
              data-demo="opt-dropWheelTransition"
            />
            {{ t("run.dropTransition") }}
          </label>
          <label
            v-if="supportsSupervise"
            class="flex items-center gap-2"
            :title="t('run.superviseHint')"
          >
            <input
              v-model="supervise"
              type="checkbox"
              class="accent-brand-500"
              data-demo="opt-supervise"
            />
            {{ t("run.supervise") }}
          </label>
        </div>
        <button
          :class="btnPrimary"
          :disabled="!canRun"
          data-demo="run-pipeline"
          @click="runPipeline"
        >
          {{ t("common.run") }}
        </button>
        <button
          :class="btnGhost"
          :disabled="!canRun"
          :title="t('run.addToQueueHint')"
          @click="queuePipeline"
        >
          {{ t("run.addToQueue") }}
        </button>
      </div>

      <!-- Optional calibration-frame folders (milkyway): point at separate dark/flat/bias dirs. -->
      <details v-if="isMilkyway" class="mt-3 text-sm">
        <summary class="cursor-pointer text-xs font-medium text-slate-500">
          {{ t("run.calibration") }}
        </summary>
        <div class="mt-2 grid gap-3 sm:grid-cols-3">
          <label class="text-sm">
            <span class="mb-1 block text-xs font-medium text-slate-500">{{
              t("run.darks")
            }}</span>
            <input
              v-model="darkDir"
              type="text"
              :placeholder="t('run.calibPlaceholder')"
              :class="input"
            />
          </label>
          <label class="text-sm">
            <span class="mb-1 block text-xs font-medium text-slate-500">{{
              t("run.flats")
            }}</span>
            <input
              v-model="flatDir"
              type="text"
              :placeholder="t('run.calibPlaceholder')"
              :class="input"
            />
          </label>
          <label class="text-sm">
            <span class="mb-1 block text-xs font-medium text-slate-500">{{
              t("run.bias")
            }}</span>
            <input
              v-model="biasDir"
              type="text"
              :placeholder="t('run.calibPlaceholder')"
              :class="input"
            />
          </label>
        </div>
      </details>
      <p v-if="!selectedPaths.length" class="mt-2 text-xs text-slate-400">
        {{ t("import.selectCapture") }}
      </p>
      <p
        v-if="queuedCount"
        class="mt-2 text-xs text-success-600 dark:text-success-300"
      >
        {{ t("import.queuedToast", { n: queuedCount }) }}
        <router-link
          :to="{ name: 'jobs' }"
          class="font-medium underline hover:text-success-700 dark:hover:text-success-200"
        >
          {{ t("import.viewQueue") }}
        </router-link>
      </p>
    </div>

    <div v-if="inv" class="space-y-6">
      <div class="flex flex-wrap gap-3">
        <div
          v-for="(n, type) in counts"
          :key="type"
          :class="[
            card,
            frameTypeCardClass(String(type)),
            'min-w-[7rem] text-center',
          ]"
        >
          <div
            class="text-2xl font-bold"
            :class="frameTypeAccentClass(String(type))"
          >
            {{ n }}
          </div>
          <div class="text-xs uppercase tracking-wide text-slate-500">
            {{ type }}
          </div>
        </div>
      </div>

      <section v-if="lightRows.length">
        <h2 class="mb-2 text-lg font-medium">{{ t("import.lightSets") }}</h2>
        <GenericTable :columns="lightColumns" :rows="lightRows">
          <template #cell-filter="{ value }">
            <FilterChip v-if="value" :filter="String(value)" />
            <span v-else class="text-slate-400">—</span>
          </template>
        </GenericTable>
      </section>

      <section v-if="calibRows.length">
        <h2 class="mb-2 text-lg font-medium">{{ t("import.calibSets") }}</h2>
        <GenericTable :columns="calibColumns" :rows="calibRows">
          <template #cell-filter="{ value }">
            <FilterChip v-if="value" :filter="String(value)" />
            <span v-else class="text-slate-400">—</span>
          </template>
        </GenericTable>
      </section>

      <section v-if="fileRows.length">
        <h2 class="mb-2 text-lg font-medium">{{ t("import.files") }}</h2>
        <GenericTable
          :columns="fileColumns"
          :rows="fileRows"
          max-height="28rem"
        >
          <template #cell-filter="{ value }">
            <FilterChip v-if="value" :filter="String(value)" />
            <span v-else class="text-slate-400">—</span>
          </template>
          <template #cell-view="{ row }">
            <FilePreviewButton :path="String(row.path)" />
          </template>
        </GenericTable>
      </section>

      <section v-if="inv.warnings && inv.warnings.length">
        <h2 class="mb-2 text-lg font-medium">{{ t("import.warnings") }}</h2>
        <ul class="space-y-1">
          <li
            v-for="(w, i) in inv.warnings"
            :key="i"
            class="text-sm text-warning"
          >
            ⚠ {{ w }}
          </li>
        </ul>
      </section>
    </div>

    <p v-else-if="!browseStore.loading" class="text-sm text-slate-400">
      {{ t("import.noData") }}
    </p>
  </div>
</template>
