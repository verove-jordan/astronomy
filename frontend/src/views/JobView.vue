<script setup lang="ts">
import { computed, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { useJobsStore } from '@/stores/jobs'
import { useJobStream } from '@/composables/useJobStream'
import { fileUrl } from '@/services/api'
import GenericTable, { type Column } from '@/components/Common/GenericTable.vue'
import StatusPill from '@/components/Common/StatusPill.vue'
import ProgressBar from '@/components/Common/ProgressBar.vue'
import MetricsChart from '@/components/Dataviz/MetricsChart.vue'
import { card } from '@/constants/styles'
import { humanizeMs, baseName, tempC } from '@/utils/format'
import type { ChannelResult } from '@/types'

const props = defineProps<{ id: string }>()
const { t } = useI18n()
const jobsStore = useJobsStore()

const jobId = Number(props.id)
const { progress, step, status, done } = useJobStream(jobId, () => jobsStore.get(jobId))
onMounted(() => jobsStore.get(jobId))

const job = computed(() => jobsStore.current)
const result = computed(() => job.value?.result)
const liveStatus = computed(() => (done.value ? job.value?.status ?? status.value : status.value))
const running = computed(() => liveStatus.value === 'running' || liveStatus.value === 'queued')

const finalImage = computed(() => {
  const out = result.value?.final?.outputs?.find((o) => o.endsWith('.png'))
  return out ? fileUrl(out) : ''
})

type Row = Record<string, unknown>
const ms = (v: unknown) => humanizeMs(Number(v))

const channelRows = computed<Row[]>(() =>
  (result.value?.channels ?? []).map((c) => ({
    filter: c.filter,
    exposure_ms: c.exposure_ms,
    input: c.input_frames,
    stacked: c.stacked_frames,
    dark: c.selection?.dark ? '✓' : '—',
    flat: c.selection?.flat ? '✓' : '—',
    bias: c.selection?.bias ? '✓' : '—',
  })),
)
const channelColumns: Column<Row>[] = [
  { key: 'filter', label: t('fields.filter'), sortable: true, searchable: true },
  { key: 'exposure_ms', label: t('fields.exposure'), sortable: true, format: ms },
  { key: 'input', label: t('fields.input'), sortable: true, align: 'right' },
  { key: 'stacked', label: t('fields.stacked'), sortable: true, align: 'right' },
  { key: 'dark', label: 'Dark', align: 'right' },
  { key: 'flat', label: 'Flat', align: 'right' },
  { key: 'bias', label: 'Bias', align: 'right' },
]

const masterRows = computed<Row[]>(() =>
  (result.value?.masters ?? []).map((m) => ({
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
const masterColumns: Column<Row>[] = [
  { key: 'type', label: t('fields.type'), sortable: true, searchable: true },
  { key: 'filter', label: t('fields.filter'), sortable: true, searchable: true },
  { key: 'exposure_ms', label: t('fields.exposure'), sortable: true, format: ms },
  { key: 'gain', label: t('fields.gain'), sortable: true, align: 'right' },
  { key: 'offset', label: t('fields.offset'), sortable: true, align: 'right' },
  { key: 'temp_milli_c', label: t('fields.temp'), format: (v) => tempC(Number(v)), align: 'right' },
  { key: 'frame_count', label: t('fields.frames'), sortable: true, align: 'right' },
  { key: 'file', label: t('fields.file'), searchable: true },
]

const channelsWithMetrics = computed<ChannelResult[]>(() =>
  (result.value?.channels ?? []).filter((c) => (c.metrics?.length ?? 0) > 0),
)

function metricRows(c: ChannelResult): Row[] {
  return (c.metrics ?? []).map((m) => ({
    index: m.index,
    file: baseName(m.path),
    fwhm: m.fwhm,
    roundness: m.roundness,
    stars: m.star_count,
    status: m.rejected ? 'rejected' : 'kept',
    reason: m.reject_reason || '',
  }))
}
const metricColumns: Column<Row>[] = [
  { key: 'index', label: t('fields.index'), sortable: true, align: 'right' },
  { key: 'file', label: t('fields.file'), sortable: true, searchable: true },
  { key: 'fwhm', label: t('fields.fwhm'), sortable: true, format: (v) => Number(v).toFixed(2), align: 'right' },
  { key: 'roundness', label: t('fields.roundness'), sortable: true, format: (v) => Number(v).toFixed(3), align: 'right' },
  { key: 'stars', label: t('fields.stars'), sortable: true, align: 'right' },
  { key: 'status', label: t('fields.status'), sortable: true, searchable: true },
  { key: 'reason', label: t('fields.reason'), searchable: true },
]
const rejectedClass = (r: Row) =>
  r.status === 'rejected' ? 'bg-red-50 dark:bg-red-900/20' : 'hover:bg-slate-50 dark:hover:bg-slate-800/50'
</script>

<template>
  <div class="space-y-6">
    <div class="flex flex-wrap items-center gap-3">
      <h1 class="text-2xl font-semibold">{{ t('job.title') }} #{{ jobId }}</h1>
      <StatusPill :status="String(liveStatus)" />
      <span v-if="result?.input_dir" class="text-sm text-slate-500">{{ baseName(result.input_dir) }}</span>
    </div>

    <div v-if="running" :class="card">
      <div class="mb-2 flex justify-between text-sm">
        <span>{{ step || t('common.loading') }}</span>
        <span>{{ progress }}%</span>
      </div>
      <ProgressBar :percent="progress" />
    </div>

    <p v-if="job?.error" class="rounded-md bg-red-100 p-3 text-sm text-red-800 dark:bg-red-900/40 dark:text-red-300">
      {{ job.error }}
    </p>

    <template v-if="result && !running">
      <section v-if="finalImage" :class="card">
        <h2 class="mb-3 text-lg font-medium">
          {{ t('job.finalImage') }}
          <span class="ml-2 text-sm font-normal text-slate-500">{{ result.final?.mode }} · {{ result.final?.channels?.join('+') }}</span>
        </h2>
        <img :src="finalImage" alt="final stack" class="max-h-[28rem] rounded-md border border-slate-200 dark:border-slate-700" />
        <div class="mt-2 flex flex-wrap gap-3 text-sm">
          <a v-for="o in result.final?.outputs" :key="o" :href="fileUrl(o)" target="_blank" class="text-brand-600 hover:underline dark:text-brand-300">
            ⬇ {{ baseName(o) }}
          </a>
        </div>
      </section>

      <section>
        <h2 class="mb-2 text-lg font-medium">{{ t('job.channelsTitle') }}</h2>
        <GenericTable :columns="channelColumns" :rows="channelRows" />
      </section>

      <section v-if="channelsWithMetrics.length">
        <h2 class="mb-2 text-lg font-medium">{{ t('job.frameReview') }}</h2>
        <div v-for="c in channelsWithMetrics" :key="c.filter" class="mb-6">
          <h3 class="mb-2 font-medium text-brand-600 dark:text-brand-300">{{ c.filter }}</h3>
          <div :class="[card, 'mb-2']">
            <MetricsChart :metrics="c.metrics || []" />
          </div>
          <GenericTable :columns="metricColumns" :rows="metricRows(c)" :row-class="rejectedClass">
            <template #cell-status="{ value }">
              <span :class="value === 'rejected' ? 'text-danger' : 'text-success'">{{ value === 'rejected' ? t('job.rejected') : t('job.kept') }}</span>
            </template>
          </GenericTable>
        </div>
      </section>

      <section v-if="masterRows.length">
        <h2 class="mb-2 text-lg font-medium">{{ t('job.mastersUsed') }}</h2>
        <GenericTable :columns="masterColumns" :rows="masterRows" />
      </section>

      <section v-if="result.warnings && result.warnings.length">
        <h2 class="mb-2 text-lg font-medium">{{ t('import.warnings') }}</h2>
        <ul class="space-y-1">
          <li v-for="(w, i) in result.warnings" :key="i" class="text-sm text-warning">⚠ {{ w }}</li>
        </ul>
      </section>
    </template>
  </div>
</template>
