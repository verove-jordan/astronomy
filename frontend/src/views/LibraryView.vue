<script setup lang="ts">
import { onMounted, computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { useLibraryStore } from '@/stores/library'
import GenericTable, { type Column } from '@/components/Common/GenericTable.vue'
import Spinner from '@/components/Common/Spinner.vue'
import { btnGhost } from '@/constants/styles'
import { humanizeMs, baseName, tempC } from '@/utils/format'

const { t } = useI18n()
const libraryStore = useLibraryStore()

onMounted(() => libraryStore.load())

type Row = Record<string, unknown>
const rows = computed<Row[]>(() =>
  libraryStore.masters.map((m) => ({
    type: m.type,
    filter: m.filter || '',
    exposure_ms: m.exposure_ms,
    gain: m.gain,
    offset: m.offset,
    temp_milli_c: m.temp_milli_c,
    frame_count: m.frame_count,
    file: baseName(m.path),
  })),
)

const columns: Column<Row>[] = [
  { key: 'type', label: t('fields.type'), sortable: true, searchable: true },
  { key: 'filter', label: t('fields.filter'), sortable: true, searchable: true },
  { key: 'exposure_ms', label: t('fields.exposure'), sortable: true, format: (v) => humanizeMs(Number(v)) },
  { key: 'gain', label: t('fields.gain'), sortable: true, align: 'right' },
  { key: 'offset', label: t('fields.offset'), sortable: true, align: 'right' },
  { key: 'temp_milli_c', label: t('fields.temp'), sortable: true, format: (v) => tempC(Number(v)), align: 'right' },
  { key: 'frame_count', label: t('fields.frames'), sortable: true, align: 'right' },
  { key: 'file', label: t('fields.file'), searchable: true },
]
</script>

<template>
  <div class="space-y-4">
    <div class="flex items-center justify-between">
      <h1 class="text-2xl font-semibold">{{ t('library.title') }}</h1>
      <button :class="btnGhost" @click="libraryStore.load()">{{ t('common.refresh') }}</button>
    </div>

    <Spinner v-if="libraryStore.loading">{{ t('common.loading') }}</Spinner>

    <p v-if="!libraryStore.loading && rows.length === 0" class="text-sm text-slate-400">
      {{ t('library.empty') }}
    </p>
    <GenericTable v-else :columns="columns" :rows="rows" />
  </div>
</template>
