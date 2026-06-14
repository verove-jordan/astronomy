<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { useBrowseStore } from '@/stores/browse'
import { useJobsStore } from '@/stores/jobs'
import GenericTable, { type Column } from '@/components/Common/GenericTable.vue'
import Spinner from '@/components/Common/Spinner.vue'
import { btnPrimary, btnGhost, card, input } from '@/constants/styles'
import { humanizeMs } from '@/utils/format'
import type { FrameSet } from '@/types'

const router = useRouter()
const { t } = useI18n()
const browseStore = useBrowseStore()
const jobsStore = useJobsStore()

const dir = ref('')
const launching = ref(false)

onMounted(async () => {
  await browseStore.browse()
  dir.value = browseStore.path
})

async function openDir(path: string) {
  await browseStore.browse(path)
  dir.value = browseStore.path
}
function goUp() {
  const parent = browseStore.path.replace(/\/[^/]+$/, '')
  if (parent) openDir(parent)
}

const inv = computed(() => browseStore.inventory)
const counts = computed(() => {
  const c: Record<string, number> = {}
  for (const f of inv.value?.frames ?? []) c[f.type] = (c[f.type] || 0) + 1
  return c
})

type Row = Record<string, unknown>
function rowsFor(types: string[]): Row[] {
  return (inv.value?.sets ?? [])
    .filter((s: FrameSet) => types.includes(s.key.type))
    .map((s: FrameSet) => ({
      type: s.key.type,
      object: s.key.object || '',
      filter: s.key.filter || '',
      exposure_ms: s.key.exposure_ms,
      count: s.count,
      integration: s.total_integration_ms,
      gain: s.key.gain,
      offset: s.key.offset,
      temp: s.key.temp_bucket_c,
    }))
}
const lightRows = computed(() => rowsFor(['LIGHT']))
const calibRows = computed(() => rowsFor(['DARK', 'FLAT', 'DARKFLAT', 'BIAS']))

const ms = (v: unknown) => humanizeMs(Number(v))
const degC = (v: unknown) => `${v}°C`

const lightColumns: Column<Row>[] = [
  { key: 'object', label: t('fields.object'), sortable: true, searchable: true },
  { key: 'filter', label: t('fields.filter'), sortable: true, searchable: true },
  { key: 'exposure_ms', label: t('fields.exposure'), sortable: true, format: ms },
  { key: 'count', label: t('fields.count'), sortable: true, align: 'right' },
  { key: 'integration', label: t('fields.integration'), sortable: true, format: ms, align: 'right' },
  { key: 'gain', label: t('fields.gain'), sortable: true, align: 'right' },
  { key: 'offset', label: t('fields.offset'), sortable: true, align: 'right' },
  { key: 'temp', label: t('fields.temp'), sortable: true, format: degC, align: 'right' },
]
const calibColumns: Column<Row>[] = [
  { key: 'type', label: t('fields.type'), sortable: true, searchable: true },
  { key: 'filter', label: t('fields.filter'), sortable: true, searchable: true },
  { key: 'exposure_ms', label: t('fields.exposure'), sortable: true, format: ms },
  { key: 'count', label: t('fields.count'), sortable: true, align: 'right' },
  { key: 'gain', label: t('fields.gain'), sortable: true, align: 'right' },
  { key: 'offset', label: t('fields.offset'), sortable: true, align: 'right' },
  { key: 'temp', label: t('fields.temp'), sortable: true, format: degC, align: 'right' },
]

async function runPipeline() {
  launching.value = true
  try {
    const id = await jobsStore.create(dir.value)
    router.push({ name: 'job', params: { id: String(id) } })
  } finally {
    launching.value = false
  }
}
</script>

<template>
  <div class="space-y-6">
    <div>
      <h1 class="text-2xl font-semibold">{{ t('import.title') }}</h1>
      <p class="text-sm text-slate-500 dark:text-slate-400">{{ t('import.hint') }}</p>
    </div>

    <div :class="card">
      <div class="flex flex-wrap items-end gap-3">
        <div class="grow">
          <label class="mb-1 block text-xs font-medium text-slate-500">{{ t('common.path') }}</label>
          <input v-model="dir" :class="input" type="text" @keyup.enter="browseStore.inspect(dir)" />
        </div>
        <button :class="btnGhost" @click="goUp">{{ t('common.up') }}</button>
        <button :class="btnGhost" @click="openDir(dir)">{{ t('common.browse') }}</button>
        <button :class="btnPrimary" @click="browseStore.inspect(dir)">{{ t('common.inspect') }}</button>
      </div>

      <div v-if="browseStore.entries.length" class="mt-3 flex flex-wrap gap-2">
        <button
          v-for="e in browseStore.entries"
          :key="e.path"
          class="rounded-md border border-slate-200 px-2 py-1 text-xs text-slate-600 hover:border-brand-400 hover:text-brand-600 dark:border-slate-700 dark:text-slate-300"
          @click="openDir(e.path)"
        >
          📁 {{ e.name }}
        </button>
      </div>

      <p v-if="browseStore.error" class="mt-2 text-sm text-danger">{{ browseStore.error }}</p>
    </div>

    <Spinner v-if="browseStore.loading">{{ t('common.loading') }}</Spinner>

    <div v-if="inv" class="space-y-6">
      <div class="flex flex-wrap gap-3">
        <div
          v-for="(n, type) in counts"
          :key="type"
          :class="[card, 'min-w-[7rem] text-center']"
        >
          <div class="text-2xl font-bold text-brand-600 dark:text-brand-300">{{ n }}</div>
          <div class="text-xs uppercase tracking-wide text-slate-500">{{ type }}</div>
        </div>
        <div class="ml-auto flex items-end">
          <button :class="btnPrimary" :disabled="launching || lightRows.length === 0" @click="runPipeline">
            {{ t('common.run') }}
          </button>
        </div>
      </div>

      <section v-if="lightRows.length">
        <h2 class="mb-2 text-lg font-medium">{{ t('import.lightSets') }}</h2>
        <GenericTable :columns="lightColumns" :rows="lightRows" />
      </section>

      <section v-if="calibRows.length">
        <h2 class="mb-2 text-lg font-medium">{{ t('import.calibSets') }}</h2>
        <GenericTable :columns="calibColumns" :rows="calibRows" />
      </section>

      <section v-if="inv.warnings && inv.warnings.length">
        <h2 class="mb-2 text-lg font-medium">{{ t('import.warnings') }}</h2>
        <ul class="space-y-1">
          <li v-for="(w, i) in inv.warnings" :key="i" class="text-sm text-warning">⚠ {{ w }}</li>
        </ul>
      </section>
    </div>

    <p v-else-if="!browseStore.loading" class="text-sm text-slate-400">{{ t('import.noData') }}</p>
  </div>
</template>
