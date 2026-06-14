<script setup lang="ts">
import { onMounted, computed } from 'vue'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { useJobsStore } from '@/stores/jobs'
import GenericTable, { type Column } from '@/components/Common/GenericTable.vue'
import StatusPill from '@/components/Common/StatusPill.vue'
import Spinner from '@/components/Common/Spinner.vue'
import { btnGhost } from '@/constants/styles'

const router = useRouter()
const { t } = useI18n()
const jobsStore = useJobsStore()

onMounted(() => jobsStore.list())

type Row = Record<string, unknown>
const rows = computed<Row[]>(() =>
  jobsStore.jobs.map((j) => ({
    id: j.id,
    kind: j.kind,
    status: j.status,
    progress: j.progress,
    object: j.result?.input_dir?.split('/').pop() || '',
  })),
)

const columns: Column<Row>[] = [
  { key: 'id', label: t('fields.id'), sortable: true, align: 'right' },
  { key: 'kind', label: t('fields.kind'), sortable: true, searchable: true },
  { key: 'object', label: t('fields.object'), sortable: true, searchable: true },
  { key: 'status', label: t('fields.status'), sortable: true, searchable: true },
  { key: 'progress', label: t('fields.progress'), sortable: true, format: (v) => `${v}%`, align: 'right' },
]

function open(id: unknown) {
  router.push({ name: 'job', params: { id: String(id) } })
}
</script>

<template>
  <div class="space-y-4">
    <div class="flex items-center justify-between">
      <h1 class="text-2xl font-semibold">{{ t('nav.jobs') }}</h1>
      <button :class="btnGhost" @click="jobsStore.list()">{{ t('common.refresh') }}</button>
    </div>

    <Spinner v-if="jobsStore.loading">{{ t('common.loading') }}</Spinner>

    <GenericTable :columns="columns" :rows="rows">
      <template #cell-id="{ row }">
        <button class="font-medium text-brand-600 hover:underline dark:text-brand-300" @click="open(row.id)">
          #{{ row.id }}
        </button>
      </template>
      <template #cell-status="{ row }">
        <StatusPill :status="String(row.status)" />
      </template>
    </GenericTable>
  </div>
</template>
