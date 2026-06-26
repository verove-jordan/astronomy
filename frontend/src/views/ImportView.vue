<script setup lang="ts">
import { ref, computed, onMounted } from "vue";
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
import type { ReusePreview } from "@/types";
import {
  btnPrimary,
  card,
  input,
  frameTypeAccentClass,
  frameTypeCardClass,
} from "@/constants/styles";
import { humanizeMs } from "@/utils/format";
import type { FrameSet } from "@/types";

const router = useRouter();
const { t } = useI18n();
const browseStore = useBrowseStore();
const jobsStore = useJobsStore();

const selectedPath = ref("");
const rootPath = ref("");
const selectedMode = ref("deepsky");
const selectedFormat = ref("image");
const launching = ref(false);

// Run-quality toggles (defaults match the backend presets).
const colorCalibration = ref(true);
const denoise = ref(true);
const dropWheelTransition = ref(true);

const modes = ["deepsky", "nebula", "milkyway", "planetary"];
const formats = ["image", "video", "both"];

onMounted(async () => {
  await browseStore.browse();
  rootPath.value = browseStore.path;
});

async function openDir(path: string) {
  await browseStore.browse(path);
}
// Cross-session reuse: discovered prior data + the user's selection.
const reusePreview = ref<ReusePreview | null>(null);
const reuseEnabled = ref(true);
const reuseSelected = ref<number[]>([]);

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

async function inspectSelected(path: string) {
  selectedPath.value = path;
  await browseStore.inspect(path);
  reusePreview.value = await jobsStore.previewReuse(path);
  // Default: fold in every discovered prior session (user can deselect).
  reuseSelected.value = (reusePreview.value?.reuse.sessions ?? []).map(
    (s) => s.session_id,
  );
  reuseEnabled.value = true;
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

const canRun = computed(
  () => !!selectedPath.value && !!inv.value && !launching.value,
);

async function runPipeline() {
  launching.value = true;
  try {
    const id = await jobsStore.create(
      selectedPath.value,
      selectedMode.value,
      selectedFormat.value,
      {
        filterMap: overrides.value,
        colorCalibration: colorCalibration.value,
        denoise: denoise.value,
        dropWheelTransition: dropWheelTransition.value,
        inventory: inv.value,
        reuseDisabled: reuseDisabledForRun.value,
        // Only send a session list when the user deselected some; empty = fold in all discovered.
        reuseSessions: reuseSelectionForRun.value,
      },
    );
    router.push({ name: "job", params: { id: String(id) } });
  } finally {
    launching.value = false;
  }
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
        :selected="selectedPath"
        :error="browseStore.error"
        @navigate="openDir"
        @inspect="inspectSelected"
      />
    </div>

    <!-- Selected capture + channel mapping + run controls -->
    <div v-if="inv" class="grid gap-4 lg:grid-cols-2">
      <CaptureSummary :summary="summary" :path="selectedPath" />
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

    <div :class="card">
      <div class="flex flex-wrap items-end gap-4">
        <label class="text-sm">
          <span class="mb-1 block text-xs font-medium text-slate-500">{{
            t("run.mode")
          }}</span>
          <select v-model="selectedMode" :class="input">
            <option v-for="mo in modes" :key="mo" :value="mo">
              {{ t("run.modes." + mo) }}
            </option>
          </select>
        </label>
        <label class="text-sm">
          <span class="mb-1 block text-xs font-medium text-slate-500">{{
            t("run.format")
          }}</span>
          <select v-model="selectedFormat" :class="input">
            <option v-for="fmt in formats" :key="fmt" :value="fmt">
              {{ t("run.formats." + fmt) }}
            </option>
          </select>
        </label>
        <div class="flex flex-col gap-1 text-sm">
          <label class="flex items-center gap-2">
            <input
              v-model="colorCalibration"
              type="checkbox"
              class="accent-brand-500"
            />
            {{ t("run.colorCalibration") }}
          </label>
          <label class="flex items-center gap-2">
            <input v-model="denoise" type="checkbox" class="accent-brand-500" />
            {{ t("run.denoise") }}
          </label>
          <label class="flex items-center gap-2">
            <input
              v-model="dropWheelTransition"
              type="checkbox"
              class="accent-brand-500"
            />
            {{ t("run.dropTransition") }}
          </label>
        </div>
        <button :class="btnPrimary" :disabled="!canRun" @click="runPipeline">
          {{ t("common.run") }}
        </button>
      </div>
      <p v-if="!selectedPath" class="mt-2 text-xs text-slate-400">
        {{ t("import.selectCapture") }}
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
