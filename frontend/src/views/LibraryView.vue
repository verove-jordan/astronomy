<script setup lang="ts">
import { onMounted, computed } from "vue";
import { useI18n } from "vue-i18n";
import { useLibraryStore } from "@/stores/library";
import { useS3Store } from "@/stores/s3";
import GenericTable, {
  type Column,
} from "@/components/Common/GenericTable.vue";
import Spinner from "@/components/Common/Spinner.vue";
import { btnGhost, btnPrimary } from "@/constants/styles";
import { humanizeMs, baseName, tempC } from "@/utils/format";

const { t } = useI18n();
const libraryStore = useLibraryStore();
const s3 = useS3Store();

onMounted(() => libraryStore.load());

// Mirror the whole master library to <prefix>/library/ on S3 (a background job); a later run pulls back any
// matched master it is missing. Only offered once an S3 connection + bucket are chosen (s3.active).
function onCopyToS3() {
  void libraryStore.copyToS3(s3.bucket, s3.prefix);
}

type Row = Record<string, unknown>;
const rows = computed<Row[]>(() =>
  libraryStore.masters.map((m) => ({
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

const columns: Column<Row>[] = [
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
    format: (v) => humanizeMs(Number(v)),
  },
  { key: "gain", label: t("fields.gain"), sortable: true, align: "right" },
  { key: "offset", label: t("fields.offset"), sortable: true, align: "right" },
  {
    key: "temp_milli_c",
    label: t("fields.temp"),
    sortable: true,
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

// Phone/DSLR calibration masters (iPhone DNG darks/bias/flats) — keyed by ISO / exposure / sensor
// dimensions rather than gain/offset, so they get their own table.
const phoneRows = computed<Row[]>(() =>
  libraryStore.phoneMasters.map((m) => ({
    type: m.type,
    iso: m.iso,
    exposure_ms: m.exposure_ms,
    camera: m.camera_model || "",
    dims: m.width && m.height ? `${m.width}×${m.height}` : "",
    frame_count: m.frame_count,
    file: baseName(m.path),
  })),
);

const phoneColumns: Column<Row>[] = [
  { key: "type", label: t("fields.type"), sortable: true, searchable: true },
  { key: "iso", label: t("fields.iso"), sortable: true, align: "right" },
  {
    key: "exposure_ms",
    label: t("fields.exposure"),
    sortable: true,
    format: (v) => humanizeMs(Number(v)),
  },
  {
    key: "camera",
    label: t("fields.camera"),
    sortable: true,
    searchable: true,
  },
  { key: "dims", label: t("fields.dimensions"), sortable: true },
  {
    key: "frame_count",
    label: t("fields.frames"),
    sortable: true,
    align: "right",
  },
  { key: "file", label: t("fields.file"), searchable: true },
];
</script>

<template>
  <div class="space-y-4">
    <div class="flex items-center justify-between">
      <h1 class="text-2xl font-semibold">{{ t("library.title") }}</h1>
      <div class="flex items-center gap-2">
        <button
          v-if="s3.active"
          :class="btnPrimary"
          :disabled="libraryStore.copying"
          :title="t('library.copyToS3Hint')"
          @click="onCopyToS3"
        >
          {{
            libraryStore.copying ? t("library.copying") : t("library.copyToS3")
          }}
        </button>
        <button :class="btnGhost" @click="libraryStore.load()">
          {{ t("common.refresh") }}
        </button>
      </div>
    </div>

    <!-- Copy-to-S3 feedback: a queued toast linking to the live job, or the error. -->
    <p
      v-if="libraryStore.copiedJobId"
      class="text-sm text-emerald-600 dark:text-emerald-400"
    >
      {{ t("library.copyQueued") }}
      <router-link
        :to="{ name: 'job', params: { id: String(libraryStore.copiedJobId) } }"
        class="underline"
      >
        {{ t("import.viewQueue") }}
      </router-link>
    </p>
    <p v-else-if="libraryStore.copyError" class="text-sm text-rose-500">
      {{ libraryStore.copyError }}
    </p>

    <Spinner v-if="libraryStore.loading">{{ t("common.loading") }}</Spinner>

    <template v-else>
      <p
        v-if="rows.length === 0 && phoneRows.length === 0"
        class="text-sm text-slate-400"
      >
        {{ t("library.empty") }}
      </p>

      <div v-if="rows.length" class="space-y-2">
        <h2 class="text-lg font-semibold">{{ t("library.deepskyTitle") }}</h2>
        <GenericTable :columns="columns" :rows="rows" max-height="28rem" />
      </div>

      <div v-if="phoneRows.length" class="space-y-2">
        <h2 class="text-lg font-semibold">{{ t("library.phoneTitle") }}</h2>
        <GenericTable
          :columns="phoneColumns"
          :rows="phoneRows"
          max-height="28rem"
        />
      </div>
    </template>
  </div>
</template>
