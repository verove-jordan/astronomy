<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'

const props = defineProps<{ status: string }>()
const { t } = useI18n()

const classes: Record<string, string> = {
  queued: 'bg-amber-100 text-amber-800 dark:bg-amber-900/40 dark:text-amber-300',
  running: 'bg-brand-100 text-brand-800 dark:bg-brand-900/40 dark:text-brand-300',
  succeeded: 'bg-green-100 text-green-800 dark:bg-green-900/40 dark:text-green-300',
  failed: 'bg-red-100 text-red-800 dark:bg-red-900/40 dark:text-red-300',
}

const cls = computed(() => classes[props.status] || classes.queued)
const label = computed(() => t(`status.${props.status}`))
</script>

<template>
  <span :class="['inline-flex rounded-full px-2 py-0.5 text-xs font-medium', cls]" role="status">
    {{ label }}
  </span>
</template>
