<script setup lang="ts">
import { computed } from "vue";
import { use } from "echarts/core";
import { ScatterChart } from "echarts/charts";
import { PolarComponent, TooltipComponent } from "echarts/components";
import { CanvasRenderer } from "echarts/renderers";
import VChart from "vue-echarts";
import { useI18n } from "vue-i18n";
import { useSkyStore } from "@/stores/sky";
import { scoreTier } from "@/constants/styles";
import { SCORE_TIER_HEX, CHART_AXIS, CHART_GRID } from "@/constants/colors";

use([ScatterChart, PolarComponent, TooltipComponent, CanvasRenderer]);

const { t } = useI18n();
const store = useSkyStore();

interface SkyPoint {
  name: string;
  value: [number, number]; // [radius = 90 − altitude, azimuth]
  altNow: number;
  azNow: number;
  itemStyle: { color: string };
}

// Only targets currently above the horizon can be plotted on the sky now.
const points = computed<SkyPoint[]>(() =>
  store.targets
    .filter((tg) => tg.alt_now_deg > 0)
    .map((tg) => ({
      name: tg.name,
      value: [90 - tg.alt_now_deg, tg.az_now_deg],
      altNow: Math.round(tg.alt_now_deg),
      azNow: Math.round(tg.az_now_deg),
      itemStyle: { color: SCORE_TIER_HEX[scoreTier(tg.score)] },
    })),
);

const compass: Record<number, string> = { 0: "N", 90: "E", 180: "S", 270: "W" };

// ECharts callback params are loosely typed; `data` carries our SkyPoint shape.
/* eslint-disable @typescript-eslint/no-explicit-any */
const option = computed(() => ({
  tooltip: {
    formatter: (p: any) =>
      `${p.data.name}<br/>${t("tonight.cols.altNow")}: ${p.data.altNow}° · ${p.data.azNow}°`,
  },
  polar: { center: ["50%", "52%"], radius: "78%" },
  angleAxis: {
    type: "value",
    min: 0,
    max: 360,
    startAngle: 90, // 0° (North) at the top
    clockwise: true, // azimuth increases toward the East
    interval: 90,
    axisLabel: {
      color: CHART_AXIS,
      formatter: (v: number) => compass[v] ?? "",
    },
    axisLine: { lineStyle: { color: CHART_GRID } },
    splitLine: { lineStyle: { color: CHART_GRID } },
  },
  radiusAxis: {
    type: "value",
    min: 0,
    max: 90, // center = zenith (alt 90°), rim = horizon (alt 0°)
    interval: 30,
    axisLabel: { color: CHART_AXIS, formatter: (v: number) => `${90 - v}°` },
    axisLine: { show: false },
    splitLine: { lineStyle: { color: CHART_GRID } },
  },
  series: [
    {
      type: "scatter",
      coordinateSystem: "polar",
      symbolSize: 10,
      data: points.value,
      encode: { radius: 0, angle: 1 },
    },
  ],
}));

function onClick(p: any) {
  if (p?.data?.name) store.select(String(p.data.name));
}
/* eslint-enable @typescript-eslint/no-explicit-any */
</script>

<template>
  <VChart :option="option" autoresize class="h-96 w-full" @click="onClick" />
</template>
