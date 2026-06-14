<script setup lang="ts">
import { computed } from 'vue'
import { use } from 'echarts/core'
import { BarChart, LineChart } from 'echarts/charts'
import { GridComponent, TooltipComponent, LegendComponent } from 'echarts/components'
import { CanvasRenderer } from 'echarts/renderers'
import VChart from 'vue-echarts'
import { useI18n } from 'vue-i18n'
import type { GradeMetric } from '@/types'

use([BarChart, LineChart, GridComponent, TooltipComponent, LegendComponent, CanvasRenderer])

const props = defineProps<{ metrics: GradeMetric[] }>()
const { t } = useI18n()

const option = computed(() => ({
  tooltip: { trigger: 'axis' },
  legend: { data: [t('fields.fwhm'), t('fields.roundness')], textStyle: { color: '#94a3b8' } },
  grid: { left: 45, right: 45, top: 30, bottom: 30 },
  xAxis: {
    type: 'category',
    data: props.metrics.map((m) => '#' + m.index),
    axisLabel: { color: '#94a3b8' },
  },
  yAxis: [
    { type: 'value', name: t('fields.fwhm'), axisLabel: { color: '#94a3b8' } },
    { type: 'value', name: t('fields.roundness'), min: 0, max: 1, axisLabel: { color: '#94a3b8' } },
  ],
  series: [
    {
      name: t('fields.fwhm'),
      type: 'bar',
      data: props.metrics.map((m) => ({
        value: m.fwhm,
        itemStyle: { color: m.rejected ? '#dc2626' : '#6366f1' },
      })),
    },
    {
      name: t('fields.roundness'),
      type: 'line',
      yAxisIndex: 1,
      data: props.metrics.map((m) => m.roundness),
      lineStyle: { color: '#16a34a' },
      itemStyle: { color: '#16a34a' },
    },
  ],
}))
</script>

<template>
  <VChart :option="option" autoresize class="h-72 w-full" />
</template>
