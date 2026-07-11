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
import { SCORE_TIER_HEX, CHART_AXIS, CHART_GRID, MAP_SELECTED } from "@/constants/colors";

use([ScatterChart, PolarComponent, TooltipComponent, CanvasRenderer]);

const { t } = useI18n();
const store = useSkyStore();

interface SkyPoint {
  name: string;
  value: [number, number]; // [radius = 90 − altitude, azimuth]
  altNow: number;
  azNow: number;
  below?: boolean;
  itemStyle?: { color: string };
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

// The selected target, highlighted with a bright ring + name label so it stands out among the dots.
// When it is below the horizon it cannot be plotted, so a rim marker at its azimuth keeps the selection
// visible instead of silently vanishing.
const selectedOverlay = computed<SkyPoint[]>(() => {
  const tg = store.selected;
  if (!tg || tg.alt_now_deg <= 0) return [];
  return [
    {
      name: tg.name,
      value: [90 - tg.alt_now_deg, tg.az_now_deg],
      altNow: Math.round(tg.alt_now_deg),
      azNow: Math.round(tg.az_now_deg),
    },
  ];
});

const belowHorizonHint = computed<SkyPoint[]>(() => {
  const tg = store.selected;
  if (!tg || tg.alt_now_deg > 0) return [];
  return [
    {
      name: tg.name,
      value: [89, tg.az_now_deg], // pinned just inside the horizon rim at its azimuth
      altNow: Math.round(tg.alt_now_deg),
      azNow: Math.round(tg.az_now_deg),
      below: true,
    },
  ];
});

const compass: Record<number, string> = { 0: "N", 90: "E", 180: "S", 270: "W" };

// ECharts callback params are loosely typed; `data` carries our SkyPoint shape.
/* eslint-disable @typescript-eslint/no-explicit-any */
const option = computed(() => ({
  tooltip: {
    formatter: (p: any) =>
      p.data.below
        ? `${p.data.name}<br/>${t("tonight.map.belowHorizon")}`
        : `${p.data.name}<br/>${t("tonight.cols.altNow")}: ${p.data.altNow}° · ${p.data.azNow}°`,
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
    {
      // Selected-target ring + label, drawn on top of the base dot.
      type: "scatter",
      coordinateSystem: "polar",
      symbolSize: 20,
      z: 10,
      silent: true,
      data: selectedOverlay.value,
      itemStyle: { color: "transparent", borderColor: MAP_SELECTED, borderWidth: 3 },
      label: {
        show: true,
        formatter: (p: any) => p.data.name,
        position: "top",
        color: MAP_SELECTED,
        fontWeight: "bold",
      },
      encode: { radius: 0, angle: 1 },
    },
    {
      // Below-horizon rim marker for the selected target (keeps the selection visible when it is not up).
      type: "scatter",
      coordinateSystem: "polar",
      symbol: "pin",
      symbolSize: 22,
      z: 9,
      data: belowHorizonHint.value,
      itemStyle: { color: MAP_SELECTED, opacity: 0.7 },
      label: {
        show: true,
        formatter: (p: any) => `${p.data.name} ↓`,
        position: "bottom",
        color: MAP_SELECTED,
      },
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
