<script setup lang="ts">
import { computed } from "vue";
import { use } from "echarts/core";
import { BarChart } from "echarts/charts";
import { GridComponent, TooltipComponent } from "echarts/components";
import { CanvasRenderer } from "echarts/renderers";
import VChart from "vue-echarts";
import { useI18n } from "vue-i18n";
import { CHART_AXIS } from "@/constants/colors";
import type { LiveStats } from "@/types";

use([BarChart, GridComponent, TooltipComponent, CanvasRenderer]);

// The live histogram — the single most useful number-picture while framing: it shows at a glance
// whether the sky is buried in the read noise (exposure too short), whether stars are clipping
// (too long or too much gain), and where the black point should sit.
const props = defineProps<{ stats: LiveStats | null; log?: boolean }>();
const { t } = useI18n();

const option = computed(() => {
  const hist = props.stats?.histogram ?? [];
  const data = props.log
    ? hist.map((v) => (v > 0 ? Math.log10(v + 1) : 0))
    : hist;
  return {
    animation: false,
    grid: { left: 8, right: 8, top: 8, bottom: 18, containLabel: false },
    xAxis: {
      type: "category",
      data: hist.map((_, i) =>
        Math.round((i * 65535) / Math.max(1, hist.length - 1)),
      ),
      axisLabel: {
        color: CHART_AXIS,
        fontSize: 9,
        showMaxLabel: true,
        interval: Math.floor(hist.length / 4),
      },
      axisLine: { lineStyle: { color: CHART_AXIS } },
    },
    yAxis: { type: "value", show: false },
    tooltip: {
      trigger: "axis",
      formatter: (params: { name: string; value: number }[]) =>
        `${params[0]?.name} ADU`,
    },
    series: [
      {
        type: "bar",
        data,
        barWidth: "100%",
        itemStyle: { color: "#6366f1" },
        large: true,
      },
    ],
  };
});

const summary = computed(() => {
  const s = props.stats;
  if (!s) return [];
  return [
    { label: t("capture.hist.min"), value: String(s.min) },
    { label: t("capture.hist.median"), value: String(s.median) },
    { label: t("capture.hist.max"), value: String(s.max) },
    {
      label: t("capture.hist.saturated"),
      value: `${s.saturated_pct.toFixed(2)}%`,
      warn: s.saturated_pct > 0.5,
    },
  ];
});
</script>

<template>
  <div>
    <VChart v-if="stats" :option="option" autoresize class="h-24 w-full" />
    <p v-else class="py-6 text-center text-xs text-slate-400">
      {{ t("capture.hist.waiting") }}
    </p>
    <div
      v-if="summary.length"
      class="mt-1 flex flex-wrap gap-x-4 gap-y-1 text-[11px]"
    >
      <span
        v-for="item in summary"
        :key="item.label"
        :class="
          item.warn
            ? 'text-amber-600 dark:text-amber-400'
            : 'text-slate-500 dark:text-slate-400'
        "
      >
        {{ item.label }}
        <span class="font-mono">{{ item.value }}</span>
      </span>
    </div>
  </div>
</template>
