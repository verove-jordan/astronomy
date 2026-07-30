<script setup lang="ts">
import { computed } from "vue";
import { use } from "echarts/core";
import { ScatterChart, LineChart } from "echarts/charts";
import { PolarComponent, TooltipComponent } from "echarts/components";
import { CanvasRenderer } from "echarts/renderers";
import VChart from "vue-echarts";
import { useI18n } from "vue-i18n";
import { useGotoStore } from "@/stores/goto";
import { compass16 } from "@/utils/compass";
import { CHART_AXIS, CHART_GRID } from "@/constants/colors";

use([
  ScatterChart,
  LineChart,
  PolarComponent,
  TooltipComponent,
  CanvasRenderer,
]);

const { t } = useI18n();
const store = useGotoStore();

// Marker color by sequence status: centered (green), center-now (brand), upcoming (slate).
const STATUS_HEX: Record<string, string> = {
  accepted: "#22c55e",
  recommended: "#6366f1",
  upcoming: "#94a3b8",
};

interface MapPoint {
  name: string;
  value: [number, number]; // [radius = 90 − altitude, azimuth]
  order: number;
  label: string; // marker text: "1","2"… align, "C1".."C4" calibration
  alt: number;
  az: number;
  itemStyle: { color: string };
}

const points = computed<MapPoint[]>(() =>
  store.stars
    .filter((s) => s.alt_deg > 0)
    .map((s) => ({
      name: s.hc_name || s.name,
      value: [90 - s.alt_deg, s.az_deg],
      order: s.order,
      label:
        s.phase === "calibration"
          ? `C${s.order - store.alignCount}`
          : String(s.order),
      alt: Math.round(s.alt_deg),
      az: Math.round(s.az_deg),
      itemStyle: { color: STATUS_HEX[s.status] ?? STATUS_HEX.upcoming },
    })),
);

// ECharts callback params are loosely typed; `data` carries our MapPoint shape.
/* eslint-disable @typescript-eslint/no-explicit-any */
const option = computed(() => ({
  tooltip: {
    formatter: (p: any) =>
      `${p.data.label}. ${p.data.name}<br/>${p.data.alt}° · ${p.data.az}°`,
  },
  polar: { center: ["50%", "54%"], radius: "76%" },
  angleAxis: {
    type: "value",
    min: 0,
    max: 360,
    startAngle: 90, // 0° (North) at the top
    clockwise: true, // azimuth increases toward the East
    interval: 45,
    // Localized 16-point codes (French uses O for west); `option` is computed, so a locale
    // switch re-renders the axis.
    axisLabel: {
      color: CHART_AXIS,
      formatter: (v: number) => t(`goto.compass.${compass16(v)}`),
    },
    axisLine: { lineStyle: { color: CHART_GRID } },
    splitLine: { lineStyle: { color: CHART_GRID } },
  },
  radiusAxis: {
    type: "value",
    min: 0,
    max: 90, // center = zenith, rim = horizon
    interval: 30,
    axisLabel: { color: CHART_AXIS, formatter: (v: number) => `${90 - v}°` },
    axisLine: { show: false },
    splitLine: { lineStyle: { color: CHART_GRID } },
  },
  series: [
    {
      // the spread path connecting the stars in alignment order
      type: "line",
      coordinateSystem: "polar",
      data: points.value.map((p) => p.value),
      showSymbol: false,
      silent: true,
      lineStyle: { color: "#6366f1", width: 1, opacity: 0.5, type: "dashed" },
      z: 1,
    },
    {
      type: "scatter",
      coordinateSystem: "polar",
      data: points.value,
      symbolSize: 18,
      encode: { radius: 0, angle: 1 },
      label: {
        show: true,
        formatter: (p: any) => String(p.data.label),
        color: "#fff",
        fontWeight: "bold",
        fontSize: 11,
      },
      z: 2,
    },
  ],
}));
/* eslint-enable @typescript-eslint/no-explicit-any */
</script>

<template>
  <div>
    <VChart :option="option" autoresize class="h-80 w-full" />
    <p class="mt-1 text-center text-[11px] text-slate-400">
      {{ t("goto.map.legend") }}
    </p>
  </div>
</template>
